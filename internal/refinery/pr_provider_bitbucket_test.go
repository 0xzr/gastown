package refinery

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// installStubCurl writes a shell script named "curl" into a fresh temp dir
// and prepends that dir to PATH. The script reads its argv and responds per
// the Bitbucket prTestFixture map (shared shape with the GitHub stub for
// setup simplicity). BITBUCKET_TOKEN env must be set by the test before
// invoking mergeCandidateBasis — GetBitbucketPRDestination enforces this.
//
// Recorded args (last write wins for repeated tests):
//   - "HTTP method prefix": write a fake curl that fails the test if it
//     spots an arg it doesn't recognize.
func installStubCurl(t *testing.T, fixtures map[int]prTestFixture) string {
	t.Helper()
	binDir := t.TempDir()
	fixturePath := filepath.Join(binDir, "fixtures.json")
	encoded, err := json.Marshal(fixtures)
	if err != nil {
		t.Fatalf("encode curl fixture: %v", err)
	}
	if err := os.WriteFile(fixturePath, encoded, 0644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	script := fmt.Sprintf(`#!/bin/sh
# Stub curl for Bitbucket PR provider mergeCandidateBasis regression tests
# (gastown-cet.12.6.6). Recognizes the call shape produced by
# GetBitbucketPRDestination: curl -s -H "Authorization: Bearer <token>"
# https://api.bitbucket.org/2.0/repositories/<workspace>/<repo>/pullrequests/<id>
FIXTURE_FILE=%q
pr_id=""
for arg in "$@"; do
  case "$arg" in
    -s|-H) ;;
    -*) ;;
    *pullrequests/*) pr_id=$(echo "$arg" | sed 's|.*pullrequests/||') ;;
    *) ;;
  esac
done
if [ -z "$pr_id" ]; then
  echo "stub-curl: no PR id found in args: $*" >&2
  exit 2
fi
python3 -c "
import json, sys
raw = json.load(open(sys.argv[1]))
fixtures = {int(k): v for k, v in raw.items()}
key = int(sys.argv[2])
if key not in fixtures:
    print('stub-curl: unknown PR', key, file=sys.stderr)
    sys.exit(3)
f = fixtures[key]
if f.get('NotFound'):
    print('{}')
    sys.exit(0)
if f.get('FailWithStderr'):
    print(f['FailWithStderr'], file=sys.stderr)
    sys.exit(1)
if f.get('EmitEmpty'):
    sys.exit(0)
print(json.dumps({'destination': {'branch': {'name': f.get('BaseBranch', '')}}}))
" "$FIXTURE_FILE" "$pr_id"
exit $?
`, fixturePath)
	if err := os.WriteFile(filepath.Join(binDir, "curl"), []byte(script), 0755); err != nil {
		t.Fatalf("write stub curl: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return binDir
}

// initBitbucketPRProviderRepo is the Bitbucket counterpart of
// initPRProviderOriginRepo: a real git repo with origin pointing at a
// local bare remote. workspace/repoSlug are returned as fixed dummy values
// because the tests construct bitbucketPRProvider via the unexported
// provider struct directly (newBitbucketProviderForTest), bypassing
// newBitbucketPRProvider and its RemoteURL→ParseBitbucketRemote round trip.
// The git repo only needs the bare remote so RemoteBranchTip has a real
// target to ls-remote against; no network is required.
func initBitbucketPRProviderRepo(t *testing.T, branchOnOrigin string) (workDir, workspace, repoSlug, originTip, headSHA string) {
	t.Helper()
	workDir, _, originTip, headSHA = initPRProviderOriginRepo(t, branchOnOrigin)
	// initPRProviderOriginRepo already pushed branchOnOrigin to a local
	// bare remote and set origin's URL to that bare-dir path. The
	// Bitbucket provider's Git wrapper reads from this same origin for
	// RemoteBranchTip, so no further setup is required.
	workspace = "dementus"
	repoSlug = "dementusrepo"
	return workDir, workspace, repoSlug, originTip, headSHA
}

// newBitbucketProviderForTest constructs a bitbucketPRProvider wrapping the
// given workDir. Centralized so tests don't repeat the wiring and so any
// necessary auth-setup (BITBUCKET_TOKEN) lives in one place.
func newBitbucketProviderForTest(workDir, workspace, repoSlug string) *bitbucketPRProvider {
	return &bitbucketPRProvider{
		git:       git.NewGit(workDir),
		workspace: workspace,
		repoSlug:  repoSlug,
	}
}

// TestClassifyBitbucketParticipants covers the per-role classification logic
// in classifyBitbucketParticipants (gastown-cet.12.6.3):
//   - REVIEWER + approved                       -> PASS
//   - REVIEWER + not approved                    -> NO_VERDICT  (non-blocking)
//   - non-REVIEWER roles                         -> skipped entirely
//   - empty / all-non-reviewer participants      -> NO_VERDICT
//
// Bitbucket Cloud's participants API does not expose CHANGES_REQUESTED state,
// so a reviewer who has not approved is recorded as NO_VERDICT rather than
// hard-FAIL. This guards the no-verdict/degraded-quorum semantics that
// callers depend on.
func TestClassifyBitbucketParticipants(t *testing.T) {
	basis := MergeCandidateBasis("origin/main", "head-sha")

	// participant is a compact constructor for the per-participant unit.
	participant := func(user, role string, approved bool) git.BitbucketParticipant {
		return git.BitbucketParticipant{User: user, Role: role, Approved: approved}
	}

	t.Run("empty_participants_is_no_verdict", func(t *testing.T) {
		ev := classifyBitbucketParticipants(nil, basis)
		if ev.State != ReviewStateNoVerdict {
			t.Fatalf("empty participants must be NO_VERDICT, got %s: %s", ev.State, ev.Error)
		}
		if ev.NoVerdictCount != 1 {
			t.Errorf("expected NoVerdictCount=1, got %d", ev.NoVerdictCount)
		}
		if ev.Error != "no participants" {
			t.Errorf("error should be canonical 'no participants', got %q", ev.Error)
		}
	})

	t.Run("approved_reviewer_passes", func(t *testing.T) {
		ev := classifyBitbucketParticipants(
			[]git.BitbucketParticipant{participant("alice", "REVIEWER", true)},
			basis,
		)
		if ev.State != ReviewStatePass {
			t.Fatalf("single approved reviewer must PASS, got %s: %s", ev.State, ev.Error)
		}
		if ev.PassCount != 1 {
			t.Errorf("expected PassCount=1, got %d", ev.PassCount)
		}
		if len(ev.Results) != 1 {
			t.Fatalf("expected one result, got %d", len(ev.Results))
		}
		r := ev.Results[0]
		if r.Reviewer != "alice" || r.Verdict != ReviewerVerdictPass {
			t.Errorf("expected alice PASS, got %+v", r)
		}
	})

	t.Run("non_approving_reviewer_is_no_verdict_not_fail", func(t *testing.T) {
		// This is the bead's specific gap case: a REVIEWER who has not
		// approved must be classified as NO_VERDICT (non-blocking under
		// degraded quorum), NOT as FAIL. Bitbucket Cloud does not surface
		// CHANGES_REQUESTED via the participants API, so any "weighed in but
		// not approved" reviewer cannot authoritatively block the merge.
		ev := classifyBitbucketParticipants(
			[]git.BitbucketParticipant{participant("bob", "REVIEWER", false)},
			basis,
		)
		if ev.State != ReviewStateNoVerdict {
			t.Fatalf("non-approving REVIEWER must be NO_VERDICT, got %s: %s", ev.State, ev.Error)
		}
		if ev.NoVerdictCount != 1 {
			t.Errorf("expected NoVerdictCount=1, got %d", ev.NoVerdictCount)
		}
		if ev.FailCount != 0 {
			t.Errorf("non-approving REVIEWER must NOT count as FAIL, got FailCount=%d", ev.FailCount)
		}
		if len(ev.Results) != 1 {
			t.Fatalf("expected one reviewer result, got %d", len(ev.Results))
		}
		r := ev.Results[0]
		if r.Reviewer != "bob" || r.Verdict != ReviewerVerdictNoVerdict {
			t.Errorf("expected bob NO_VERDICT, got %+v", r)
		}
		if !strings.Contains(r.Evidence, "reviewer has not approved") {
			t.Errorf("evidence should explain the non-blocking state, got %q", r.Evidence)
		}
	})

	t.Run("non_reviewer_role_is_skipped", func(t *testing.T) {
		// PARTICIPANT is Bitbucket's default role for any user attached to the
		// PR (authorship, mention, etc.). They must not count as reviewers,
		// must not appear in Results, and must not influence the overall
		// verdict.
		ev := classifyBitbucketParticipants(
			[]git.BitbucketParticipant{
				participant("alice", "PARTICIPANT", false),
				participant("carol", "PARTICIPANT", true),
			},
			basis,
		)
		if ev.State != ReviewStateNoVerdict {
			t.Fatalf("only PARTICIPANT entries must yield NO_VERDICT, got %s: %s", ev.State, ev.Error)
		}
		if ev.Error != "no reviewers" {
			t.Errorf("expected canonical 'no reviewers' message, got %q", ev.Error)
		}
		if len(ev.Results) != 1 {
			t.Fatalf("expected one synthetic 'no reviewers' result, got %d", len(ev.Results))
		}
		if ev.Results[0].Reviewer != "bitbucket" || ev.Results[0].Verdict != ReviewerVerdictNoVerdict {
			t.Errorf("synthetic 'no reviewers' result malformed: %+v", ev.Results[0])
		}
	})

	t.Run("non_reviewer_role_does_not_dilute_quorum", func(t *testing.T) {
		// The bead's other gap case: a non-REVIEWER participant alongside a
		// real approving reviewer must not reduce the quorum below a passing
		// threshold and must not introduce phantom NO_VERDICTs. Only the
		// REVIEWER who approved contributes to the verdict.
		ev := classifyBitbucketParticipants(
			[]git.BitbucketParticipant{
				participant("author", "PARTICIPANT", false),
				participant("reviewer", "REVIEWER", true),
				participant("mentioned", "PARTICIPANT", true), // approved but irrelevant
			},
			basis,
		)
		if ev.State != ReviewStatePass {
			t.Fatalf("approved REVIEWER with bystanders must PASS, got %s: %s", ev.State, ev.Error)
		}
		if ev.PassCount != 1 {
			t.Errorf("only the REVIEWER must count toward PassCount, got PassCount=%d", ev.PassCount)
		}
		if len(ev.Results) != 1 {
			t.Fatalf("only the REVIEWER may appear in Results, got %d entries: %+v", len(ev.Results), ev.Results)
		}
		if ev.Results[0].Reviewer != "reviewer" {
			t.Errorf("expected the single reviewer result to be 'reviewer', got %q", ev.Results[0].Reviewer)
		}
	})

	t.Run("mixed_reviewers_pass_count_includes_only_approvers", func(t *testing.T) {
		// GetReviewEvaluation passes DegradedQuorumRule{} (disabled by default),
		// so any abstaining reviewer blocks the merge with NO_VERDICT even when
		// other reviewers have approved. This test pins down the per-reviewer
		// count assignment: approvers contribute to PassCount, abstainers
		// contribute to NoVerdictCount, and no reviewer is silently dropped or
		// promoted to a blocker.
		ev := classifyBitbucketParticipants(
			[]git.BitbucketParticipant{
				participant("alice", "REVIEWER", true),
				participant("bob", "REVIEWER", false),
				participant("carol", "REVIEWER", true),
			},
			basis,
		)
		if ev.PassCount != 2 {
			t.Errorf("PassCount must reflect approvers only, got %d", ev.PassCount)
		}
		if ev.NoVerdictCount != 1 {
			t.Errorf("abstaining REVIEWER must be NO_VERDICT, got NoVerdictCount=%d", ev.NoVerdictCount)
		}
		if ev.FailCount != 0 {
			t.Errorf("abstaining REVIEWER must not count as FAIL, got FailCount=%d", ev.FailCount)
		}
		// With degraded quorum disabled, the abstain makes the overall
		// verdict NO_VERDICT (non-blocking reviewers become blocking) — that
		// is the no-verdict/degraded-quorum classification semantics this
		// provider depends on.
		if ev.State != ReviewStateNoVerdict {
			t.Errorf("one abstain with degraded quorum disabled must be NO_VERDICT, got %s: %s", ev.State, ev.Error)
		}
		if len(ev.Results) != 3 {
			t.Errorf("every REVIEWER must appear in Results (none silently dropped), got %d: %+v", len(ev.Results), ev.Results)
		}
	})

	t.Run("diff_basis_propagated_to_results", func(t *testing.T) {
		// Every result must carry the caller-supplied basis so the audit
		// packet can attribute each verdict to the exact diff reviewed.
		ev := classifyBitbucketParticipants(
			[]git.BitbucketParticipant{
				participant("alice", "REVIEWER", true),
				participant("bob", "REVIEWER", false),
			},
			basis,
		)
		for _, r := range ev.Results {
			if r.DiffBasis != basis {
				t.Errorf("basis not propagated to result %+v", r)
			}
		}
	})

	t.Run("unknown_role_treated_as_non_reviewer", func(t *testing.T) {
		// Forward-compat: an unrecognized role string (a new Bitbucket role
		// added in the future) must not be silently promoted to a reviewer
		// and must not block the merge. It must be skipped like PARTICIPANT.
		ev := classifyBitbucketParticipants(
			[]git.BitbucketParticipant{participant("alice", "WATCHER", true)},
			basis,
		)
		if ev.State != ReviewStateNoVerdict {
			t.Fatalf("unknown role must not count as reviewer, got %s: %s", ev.State, ev.Error)
		}
		if ev.Error != "no reviewers" {
			t.Errorf("expected canonical 'no reviewers' for unknown-role-only list, got %q", ev.Error)
		}
	})
}

// TestClassifyBitbucketUnavailableError covers the network-failure path
// (gastown-cet.12.6.3). A failed Bitbucket participants call must downgrade
// to a single-reviewer UNAVAILABLE verdict rather than a hard merge failure,
// matching the GitHub provider's network-failure semantics.
func TestClassifyBitbucketUnavailableError(t *testing.T) {
	basis := MergeCandidateBasis("origin/main", "head-sha")
	netErr := errors.New("bitbucket API: 503 service unavailable")
	wrappedErr := fmt.Errorf("failed to fetch participants: %w", netErr)

	for _, callErr := range []error{netErr, wrappedErr} {
		t.Run(callErr.Error(), func(t *testing.T) {
			ev := classifyBitbucketUnavailableError(callErr, basis)
			if ev.State != ReviewStateUnavailable {
				t.Fatalf("Bitbucket call failure must map to UNAVAILABLE, got %s", ev.State)
			}
			if ev.UnavailableCount != 1 {
				t.Errorf("expected UnavailableCount=1, got %d", ev.UnavailableCount)
			}
			if ev.FailCount != 0 || ev.PassCount != 0 || ev.NoVerdictCount != 0 {
				t.Errorf("UNAVAILABLE must have no other counters set, got %+v", ev)
			}
			if len(ev.Results) != 1 {
				t.Fatalf("expected single synthetic result, got %d", len(ev.Results))
			}
			r := ev.Results[0]
			if r.Reviewer != "bitbucket" {
				t.Errorf("synthetic reviewer should be the bitbucket sentinel, got %q", r.Reviewer)
			}
			if r.Verdict != ReviewerVerdictUnavailable {
				t.Errorf("synthetic verdict should be UNAVAILABLE, got %s", r.Verdict)
			}
			if r.Evidence != callErr.Error() {
				t.Errorf("evidence must carry the underlying error verbatim, got %q", r.Evidence)
			}
			if ev.Error != callErr.Error() {
				t.Errorf("top-level error must carry the underlying cause, got %q", ev.Error)
			}
			if r.DiffBasis != basis {
				t.Errorf("basis not propagated to unavailable result: got %+v want %+v", r.DiffBasis, basis)
			}
		})
	}
}

// TestBitbucketMergeCandidateBasis_FailsClosedOnResolverError covers the
// original hardcoded-main bug for the Bitbucket provider
// (gastown-cet.12.6.6): a transient Bitbucket API failure must NOT cause
// mergeCandidateBasis to silently fall back to "origin/main". The resolver
// error must propagate up so the caller can return UNAVAILABLE instead of
// approving against an unconfirmed target branch.
func TestBitbucketMergeCandidateBasis_FailsClosedOnResolverError(t *testing.T) {
	t.Setenv("BITBUCKET_TOKEN", "stub-token-for-test")
	workDir, workspace, repoSlug, originTip, headSHA := initBitbucketPRProviderRepo(t, "main")
	installStubCurl(t, map[int]prTestFixture{
		42: {FailWithStderr: "bitbucket: 401 Unauthorized"},
	})
	p := newBitbucketProviderForTest(workDir, workspace, repoSlug)

	_, err := p.mergeCandidateBasis(42)
	if err == nil {
		t.Fatalf("mergeCandidateBasis must NOT silently fall back to origin/main on a Bitbucket API error; got nil error, basis would be MergeCandidateBasis(originTip=%s, head=%s)", originTip, headSHA)
	}
	if !strings.Contains(err.Error(), "resolve Bitbucket PR destination branch") {
		t.Errorf("error should label the failure site; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "401") && !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("error must carry the underlying Bitbucket cause; got %q", err.Error())
	}
}

// TestBitbucketMergeCandidateBasis_FailsClosedOnEmptyDestination covers the
// empty-resolved destination path: even when the Bitbucket API reports
// success with a structurally-valid but empty destination.branch.name (an
// upstream or staging-tier quirk), the resolver must surface an error and
// not silently fall back to "origin/main".
func TestBitbucketMergeCandidateBasis_FailsClosedOnEmptyDestination(t *testing.T) {
	t.Setenv("BITBUCKET_TOKEN", "stub-token-for-test")
	workDir, workspace, repoSlug, _, _ := initBitbucketPRProviderRepo(t, "main")
	installStubCurl(t, map[int]prTestFixture{
		7: {BaseBranch: ""},
	})
	p := newBitbucketProviderForTest(workDir, workspace, repoSlug)

	_, err := p.mergeCandidateBasis(7)
	if err == nil {
		t.Fatalf("empty destination must surface as an error so the caller fails closed; got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error must explain the empty-destination gap; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "destination branch") {
		t.Errorf("error must name 'destination branch' so the audit trail identifies the resolution stage; got %q", err.Error())
	}
}

// TestBitbucketMergeCandidateBasis_FailsClosedOnMissingRemoteBranch covers
// the resolver-success-but-target-missing path: the Bitbucket API reports a
// destination branch (so the resolver "succeeds") but the local origin fetch
// cannot find that branch tip.
func TestBitbucketMergeCandidateBasis_FailsClosedOnMissingRemoteBranch(t *testing.T) {
	t.Setenv("BITBUCKET_TOKEN", "stub-token-for-test")
	workDir, workspace, repoSlug, _, _ := initBitbucketPRProviderRepo(t, "main")
	installStubCurl(t, map[int]prTestFixture{
		101: {BaseBranch: "release"},
	})
	p := newBitbucketProviderForTest(workDir, workspace, repoSlug)

	_, err := p.mergeCandidateBasis(101)
	if err == nil {
		t.Fatalf("resolver returned a destination ('release') that origin does not have; mergeCandidateBasis must surface the ls-remote failure rather than fall back to origin/main; got nil error")
	}
	if !strings.Contains(err.Error(), "release") {
		t.Errorf("error should name the missing destination branch; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "origin/release tip") {
		t.Errorf("error should label the remote-branch tip resolution step; got %q", err.Error())
	}
}

// TestBitbucketMergeCandidateBasis_SucceedsForValidMainTarget covers the
// happy path that must NOT regress: a PR whose destination is "main"
// resolves to (origin/main, HEAD) without error.
func TestBitbucketMergeCandidateBasis_SucceedsForValidMainTarget(t *testing.T) {
	t.Setenv("BITBUCKET_TOKEN", "stub-token-for-test")
	workDir, workspace, repoSlug, originTip, headSHA := initBitbucketPRProviderRepo(t, "main")
	installStubCurl(t, map[int]prTestFixture{
		1: {BaseBranch: "main"},
	})
	p := newBitbucketProviderForTest(workDir, workspace, repoSlug)

	basis, err := p.mergeCandidateBasis(1)
	if err != nil {
		t.Fatalf("valid main-target PR must resolve cleanly; got error: %v", err)
	}
	if basis.Base != originTip {
		t.Errorf("basis.Base = %q, want %q (origin/main tip)", basis.Base, originTip)
	}
	if basis.Head != headSHA {
		t.Errorf("basis.Head = %q, want %q (HEAD)", basis.Head, headSHA)
	}
}

// TestBitbucketMergeCandidateBasis_SucceedsForNonMainTarget proves the fix
// did not regress the legitimate non-main case (gastown-cet.12.6.6): a PR
// whose destination is "release" resolves to (origin/release, HEAD) without
// falling back to "origin/main".
func TestBitbucketMergeCandidateBasis_SucceedsForNonMainTarget(t *testing.T) {
	t.Setenv("BITBUCKET_TOKEN", "stub-token-for-test")
	workDir, workspace, repoSlug, releaseTip, headSHA := initBitbucketPRProviderRepo(t, "release")
	installStubCurl(t, map[int]prTestFixture{
		5: {BaseBranch: "release"},
	})
	p := newBitbucketProviderForTest(workDir, workspace, repoSlug)

	basis, err := p.mergeCandidateBasis(5)
	if err != nil {
		t.Fatalf("valid release-target PR must resolve cleanly; got error: %v", err)
	}
	if basis.Base != releaseTip {
		t.Errorf("basis.Base = %q, want %q (origin/release tip) — must NOT silently fall back to origin/main", basis.Base, releaseTip)
	}
	if basis.Head != headSHA {
		t.Errorf("basis.Head = %q, want %q (HEAD)", basis.Head, headSHA)
	}
}

// TestBitbucketGetReviewEvaluation_UnavailableOnResolverError covers the
// end-to-end fail-closed mapping for the Bitbucket provider
// (gastown-cet.12.6.6). When the resolver fails, GetReviewEvaluation must
// return a UNAVAILABLE evaluation with an empty DiffBasis so the merge
// gates defer rather than authoritatively PASSing against an unconfirmed
// target branch.
func TestBitbucketGetReviewEvaluation_UnavailableOnResolverError(t *testing.T) {
	t.Setenv("BITBUCKET_TOKEN", "stub-token-for-test")
	workDir, workspace, repoSlug, _, _ := initBitbucketPRProviderRepo(t, "main")
	installStubCurl(t, map[int]prTestFixture{
		77: {FailWithStderr: "bitbucket: 503 service unavailable"},
	})
	p := newBitbucketProviderForTest(workDir, workspace, repoSlug)

	ev, err := p.GetReviewEvaluation(77)
	if err != nil {
		t.Fatalf("GetReviewEvaluation must swallow the resolver error and return UNAVAILABLE; got error: %v", err)
	}
	if ev == nil {
		t.Fatalf("expected a non-nil evaluation; got nil")
	}
	if ev.State != ReviewStateUnavailable {
		t.Errorf("resolver failure must produce UNAVAILABLE so the merge gates defer; got state=%s error=%q", ev.State, ev.Error)
	}
	if ev.UnavailableCount != 1 {
		t.Errorf("expected UnavailableCount=1; got %d (full ev=%+v)", ev.UnavailableCount, ev)
	}
	if ev.PassCount != 0 || ev.FailCount != 0 {
		t.Errorf("resolver failure must NOT produce an authoritative PASS or hard FAIL; got PassCount=%d FailCount=%d", ev.PassCount, ev.FailCount)
	}
	if !strings.Contains(ev.Error, "Bitbucket PR destination branch") {
		t.Errorf("top-level error must label the resolver stage; got %q", ev.Error)
	}
	if ev.DiffBasis != (DiffBasis{}) {
		t.Errorf("DiffBasis must be empty on resolver failure so the audit trail cannot misattribute the verdict to origin/main; got %+v", ev.DiffBasis)
	}
}

// TestBitbucketGetReviewEvaluation_UnavailableOnEmptyDestination covers the
// end-to-end empty-resolved-destination path: even when the API returns a
// structurally-valid but empty destination.branch.name,
// GetReviewEvaluation must return UNAVAILABLE rather than proceeding
// against an unconfirmed basis.
func TestBitbucketGetReviewEvaluation_UnavailableOnEmptyDestination(t *testing.T) {
	t.Setenv("BITBUCKET_TOKEN", "stub-token-for-test")
	workDir, workspace, repoSlug, _, _ := initBitbucketPRProviderRepo(t, "main")
	installStubCurl(t, map[int]prTestFixture{
		88: {BaseBranch: ""},
	})
	p := newBitbucketProviderForTest(workDir, workspace, repoSlug)

	ev, err := p.GetReviewEvaluation(88)
	if err != nil {
		t.Fatalf("expected empty-destination to be swallowed into UNAVAILABLE; got error: %v", err)
	}
	if ev.State != ReviewStateUnavailable {
		t.Errorf("empty destination must produce UNAVAILABLE so the merge gates defer; got state=%s error=%q", ev.State, ev.Error)
	}
	if ev.PassCount != 0 || ev.FailCount != 0 {
		t.Errorf("empty-destination must NOT produce a terminal verdict (PASS or FAIL); got PassCount=%d FailCount=%d", ev.PassCount, ev.FailCount)
	}
}
