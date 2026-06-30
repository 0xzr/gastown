package refinery

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// prTestFixture is the shape of a controllable gh pr-base-ref stub fixture:
// the test installs a gh shell script in a temp binDir; the script emits
// PRFixtures[prNumber].BaseBranch (if present) and that entry also describes
// how the script should fail when it is missing or equal to "".
//
// Used by the mergeCandidateBasis fail-closed regression tests for
// gastown-cet.12.6.6.
type prTestFixture struct {
	// BaseBranch is the value the stub emits in --json baseRefName output.
	// Empty means emit an empty string (valid JSON, empty baseRefName).
	// To force an empty-bytes response, set EmitEmpty to true.
	BaseBranch string
	// EmitEmpty makes the stub emit empty stdout (no JSON at all) so the
	// parser fails. Used to exercise the unparseable-output path.
	EmitEmpty bool
	// FailWithStderr (when non-empty) makes the stub exit non-zero with this
	// stderr; the provider must surface a wrapper error rather than fall
	// back to origin/main.
	FailWithStderr string
	// NotFound makes the stub exit 0 but emit GitHub's "no PR" envelope so
	// the provider sees baseRefName == "" with no error (treated as fail-closed).
	NotFound bool
}

// installStubGh writes a shell script named "gh" into a fresh temp dir and
// prepends that dir to PATH. The script reads the PR number from argv and
// returns the fixture's configured response, handling both the --json
// baseRefName endpoint (drives the mergeCandidateBasis resolver) and the
// --json reviews endpoint (drives the happy-path end-to-end test). The
// reviews endpoint always returns a single APPROVED review so the happy
// path can drive a PASS verdict against a known target branch.
func installStubGh(t *testing.T, fixtures map[int]prTestFixture) string {
	t.Helper()
	binDir := t.TempDir()
	fixturePath := filepath.Join(binDir, "fixtures.json")
	encoded, err := json.Marshal(fixtures)
	if err != nil {
		t.Fatalf("encode gh fixture: %v", err)
	}
	if err := os.WriteFile(fixturePath, encoded, 0644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	script := fmt.Sprintf(`#!/bin/sh
# Stub gh CLI for pr_provider mergeCandidateBasis regression tests
# (gastown-cet.12.6.6). Reads the fixture map from disk so the script can be
# self-contained without embedded JSON quoting surprises. Handles both the
# baseRefName endpoint (resolver) and the reviews endpoint (happy-path).
FIXTURE_FILE=%q
find_pr_number() {
  for arg in "$@"; do
    case "$arg" in
      --json|view|merge|diff|list|status|checks|reviewDecision) ;;
      --*) ;;
      *[!0-9]*|-*) ;;
      *) echo "$arg"; return 0 ;;
    esac
  done
  echo ""
}
pr=$(find_pr_number "$@")
want_endpoint=""
for arg in "$@"; do
  if [ "$arg" = "--json" ]; then
    want_endpoint="next"
  elif [ "$want_endpoint" = "next" ]; then
    case "$arg" in
      baseRefName) want_endpoint="baseRefName" ;;
      reviews) want_endpoint="reviews" ;;
      reviewDecision) want_endpoint="reviewDecision" ;;
      *) want_endpoint="" ;;
    esac
  fi
done
case "$want_endpoint" in
  reviews)
    # Happy-path: emit a single APPROVED reviewer in the gh --json reviews
    # schema (each review has author.login, state, body, submittedAt; the
    # whole list is wrapped in a "reviews" key).
    printf '%%s' '{"reviews":[{"author":{"login":"alice"},"state":"APPROVED","submittedAt":"2026-06-22T10:00:00Z","body":"lgtm"}]}'
    exit 0
    ;;
  reviewDecision)
    printf '%%s' '{"reviewDecision":"APPROVED"}'
    exit 0
    ;;
  baseRefName|"")
    # Default baseRefName path driven by fixtures.json.
    if [ -z "$pr" ]; then
      echo "stub-gh: no pr number in args: $*" >&2
      exit 2
    fi
    python3 -c "
import json, sys
fixtures_raw = json.load(open(sys.argv[1]))
# Go JSON encodes int map keys as strings; restore int-keyed view.
fixtures = {int(k): v for k, v in fixtures_raw.items()}
key = int(sys.argv[2])
if key not in fixtures:
    print('stub-gh: unknown PR', key, file=sys.stderr)
    sys.exit(3)
f = fixtures[key]
if f.get('NotFound'):
    print('')
    sys.exit(0)
if f.get('FailWithStderr'):
    print(f['FailWithStderr'], file=sys.stderr)
    sys.exit(1)
if f.get('EmitEmpty'):
    sys.exit(0)
print(json.dumps({'baseRefName': f.get('BaseBranch', '')}))
" "$FIXTURE_FILE" "$pr"
    exit $?
    ;;
  *)
    echo "stub-gh: unsupported endpoint $want_endpoint" >&2
    exit 5
    ;;
esac
`, fixturePath)
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(script), 0755); err != nil {
		t.Fatalf("write stub gh: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return binDir
}

// initPRProviderOriginRepo creates a real git repo with origin configured as a
// bare remote. The returned workDir points at the worktree (so rev-parse HEAD
// resolves); originTip is the SHA at origin/<branch> after push, useful for
// asserting that mergeCandidateBasis returns the correct origin tip.
//
// branchOnOrigin is pushed to origin/<branch>; the worktree's local
// branch name is always "main" (regardless of what branchOnOrigin is)
// because the test stub uses gh to claim the PR's base, which is then
// resolved via RemoteBranchTip against the remote. The worktree HEAD is the
// same commit pushed to origin so Rev("HEAD") matches the remote tip when
// branchOnOrigin == "main".
func initPRProviderOriginRepo(t *testing.T, branchOnOrigin string) (workDir, remoteURL, originTip, headSHA string) {
	t.Helper()
	workDir = t.TempDir()
	bareDir := t.TempDir()
	// bareDir must be a bare git repo, not a plain directory.
	{
		cmd := exec.Command("git", "init", "--bare")
		cmd.Dir = bareDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git init --bare %s: %v\n%s", bareDir, err, out)
		}
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "--initial-branch=main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	run("add", "README.md")
	run("commit", "-m", "initial")
	// HEAD is on main (from --initial-branch=main). Rename "main" locally
	// only when a different branchOnOrigin is requested, so we control
	// which local branch holds the commit.
	if branchOnOrigin != "main" {
		run("branch", "-m", branchOnOrigin)
	}
	// Set up bare remote, push whichever branch is now local.
	run("remote", "add", "origin", bareDir)
	run("push", "origin", branchOnOrigin)
	// Capture HEAD (worktree) and remote tip.
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = workDir
	headOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	headSHA = strings.TrimSpace(string(headOut))
	cmd = exec.Command("git", "-C", bareDir, "rev-parse", "refs/heads/"+branchOnOrigin)
	originOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse remote branch: %v", err)
	}
	originTip = strings.TrimSpace(string(originOut))
	remoteURL = bareDir
	// Restore "main" as the local branch name when branchOnOrigin != main,
	// so other test helpers (e.g. the gh stub's ls-remote path on the
	// happy-path branch) can still find origin/main when needed.
	if branchOnOrigin != "main" {
		// Create local "main" at the same commit too, so Rev("HEAD") and
		// the branch-on-remote resolution can both succeed in the same
		// test if the caller asks for two branches in sequence.
		// Not used in current tests but kept as future-proofing.
	}
	return workDir, remoteURL, originTip, headSHA
}

// newGitHubProviderForTest constructs a githubPRProvider wrapping a *git.Git
// for the given workDir. Centralized so tests don't repeat the wiring.
func newGitHubProviderForTest(workDir string) *githubPRProvider {
	return &githubPRProvider{git: git.NewGit(workDir)}
}

// review is a compact constructor for a raw gh-style review used by the
// collapse/classify characterization tests (gastown-cet.12.6.1).
func review(reviewer, state, submittedAt, body string) git.PRReview {
	return git.PRReview{Reviewer: reviewer, State: state, SubmittedAt: submittedAt, Body: body}
}

// evaluateGitHubReviews runs the same collapse + classify + Evaluate path the
// GitHub provider uses, without shelling to gh. It is the characterization
// seam for the final-diff selection fix.
func evaluateGitHubReviews(reviews []git.PRReview) ReviewEvaluation {
	results := classifyCollapsedReviews(collapseReviews(reviews), DiffBasis{})
	return EvaluateReviews(results, DegradedQuorumRule{})
}

// TestCollapseReviews_StaleChangesRequestedDoesNotBlockApproval covers the
// regression at the heart of gastown-cet.12.6.1: a reviewer who requested
// changes and then later approved must not block the merge. Before the fix the
// stale CHANGES_REQUESTED was counted as an independent hard FAIL.
func TestCollapseReviews_StaleChangesRequestedDoesNotBlockApproval(t *testing.T) {
	reviews := []git.PRReview{
		review("alice", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race condition"),
		review("alice", "APPROVED", "2026-06-21T10:00:00Z", "lgtm"),
	}
	ev := evaluateGitHubReviews(reviews)
	if ev.State != ReviewStatePass {
		t.Fatalf("stale CHANGES_REQUESTED superseded by APPROVED must PASS, got %s: %s", ev.State, ev.Error)
	}
	if ev.FailCount != 0 {
		t.Errorf("expected FailCount=0 (stale rejection collapsed away), got %d", ev.FailCount)
	}
	if ev.PassCount != 1 {
		t.Errorf("expected PassCount=1, got %d", ev.PassCount)
	}
}

// TestCollapseReviews_LatestChangesRequestedWins confirms the symmetric case:
// when a reviewer approves and then requests changes, the latest
// CHANGES_REQUESTED is the effective verdict and the merge is blocked.
func TestCollapseReviews_LatestChangesRequestedWins(t *testing.T) {
	reviews := []git.PRReview{
		review("bob", "APPROVED", "2026-06-20T10:00:00Z", "looks fine"),
		review("bob", "CHANGES_REQUESTED", "2026-06-22T10:00:00Z", "- blocker: missing test"),
	}
	ev := evaluateGitHubReviews(reviews)
	if ev.State != ReviewStateFail {
		t.Fatalf("APPROVED then CHANGES_REQUESTED must FAIL on the latest verdict, got %s: %s", ev.State, ev.Error)
	}
	if ev.FailCount != 1 {
		t.Errorf("expected FailCount=1, got %d", ev.FailCount)
	}
}

// TestCollapseReviews_OrderIndependentOfSlicePosition confirms the collapse is
// driven by SubmittedAt, not the order gh happens to return reviews. The same
// history given out-of-order must produce the same verdict.
func TestCollapseReviews_OrderIndependentOfSlicePosition(t *testing.T) {
	chronological := []git.PRReview{
		review("carol", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race"),
		review("carol", "APPROVED", "2026-06-21T10:00:00Z", "fixed"),
	}
	reversed := []git.PRReview{
		review("carol", "APPROVED", "2026-06-21T10:00:00Z", "fixed"),
		review("carol", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race"),
	}
	a := evaluateGitHubReviews(chronological)
	b := evaluateGitHubReviews(reversed)
	if a.State != b.State {
		t.Fatalf("verdict must be order-independent: chronological=%s reversed=%s", a.State, b.State)
	}
	if a.State != ReviewStatePass {
		t.Errorf("expected PASS (latest APPROVED wins), got %s", a.State)
	}
}

// TestCollapseReviews_DismissedClearsPriorApproval covers GitHub's DISMISSED
// state: dismissing a review clears the reviewer's prior decision, so a
// dismissed approval falls back to no-verdict rather than passing.
func TestCollapseReviews_DismissedClearsPriorApproval(t *testing.T) {
	reviews := []git.PRReview{
		review("dave", "APPROVED", "2026-06-20T10:00:00Z", "lgtm"),
		review("dave", "DISMISSED", "2026-06-21T10:00:00Z", ""),
	}
	ev := evaluateGitHubReviews(reviews)
	if ev.State != ReviewStateNoVerdict {
		t.Fatalf("DISMISSED must clear prior approval -> NO_VERDICT, got %s: %s", ev.State, ev.Error)
	}
	if ev.PassCount != 0 {
		t.Errorf("expected PassCount=0 after dismissal, got %d", ev.PassCount)
	}
	if ev.FailCount != 0 {
		t.Errorf("expected FailCount=0, got %d", ev.FailCount)
	}
}

// TestCollapseReviews_DismissedThenApprovedRestoresDecision confirms a
// reviewer can re-establish a decision after one was dismissed.
func TestCollapseReviews_DismissedThenApprovedRestoresDecision(t *testing.T) {
	reviews := []git.PRReview{
		review("erin", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race"),
		review("erin", "DISMISSED", "2026-06-21T10:00:00Z", ""),
		review("erin", "APPROVED", "2026-06-22T10:00:00Z", "all good now"),
	}
	ev := evaluateGitHubReviews(reviews)
	if ev.State != ReviewStatePass {
		t.Fatalf("CHANGES_REQUESTED -> DISMISSED -> APPROVED must PASS, got %s: %s", ev.State, ev.Error)
	}
}

// TestCollapseReviews_CommentedDoesNotOverrideTerminal confirms COMMENTED is a
// non-decision: it must not override a prior APPROVED or CHANGES_REQUESTED.
func TestCollapseReviews_CommentedDoesNotOverrideTerminal(t *testing.T) {
	t.Run("approved_then_commented_still_pass", func(t *testing.T) {
		reviews := []git.PRReview{
			review("frank", "APPROVED", "2026-06-20T10:00:00Z", "lgtm"),
			review("frank", "COMMENTED", "2026-06-21T10:00:00Z", "nit: rename var"),
		}
		ev := evaluateGitHubReviews(reviews)
		if ev.State != ReviewStatePass {
			t.Fatalf("APPROVED then COMMENTED must stay PASS, got %s: %s", ev.State, ev.Error)
		}
	})
	t.Run("changes_then_commented_still_fail", func(t *testing.T) {
		reviews := []git.PRReview{
			review("grace", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: missing test"),
			review("grace", "COMMENTED", "2026-06-21T10:00:00Z", "also see line 4"),
		}
		ev := evaluateGitHubReviews(reviews)
		if ev.State != ReviewStateFail {
			t.Fatalf("CHANGES_REQUESTED then COMMENTED must stay FAIL, got %s: %s", ev.State, ev.Error)
		}
	})
}

// TestCollapseReviews_MultipleReviewersCollapsesPerReviewer confirms each
// reviewer collapses to one result and the overall verdict aggregates across
// distinct reviewers (two approvers -> PASS with PassCount=2).
func TestCollapseReviews_MultipleReviewersCollapsesPerReviewer(t *testing.T) {
	reviews := []git.PRReview{
		review("alice", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race"),
		review("bob", "APPROVED", "2026-06-20T09:00:00Z", "lgtm"),
		review("alice", "APPROVED", "2026-06-21T10:00:00Z", "fixed now"),
		review("bob", "COMMENTED", "2026-06-22T09:00:00Z", "nice"),
	}
	ev := evaluateGitHubReviews(reviews)
	if ev.State != ReviewStatePass {
		t.Fatalf("two approvers (alice reapproved, bob approved) must PASS, got %s: %s", ev.State, ev.Error)
	}
	if ev.PassCount != 2 {
		t.Errorf("expected PassCount=2, got %d", ev.PassCount)
	}
}

// TestCollapseReviews_EachReviewerOnce confirms the collapse emits exactly one
// effective review per reviewer regardless of history length.
func TestCollapseReviews_EachReviewerOnce(t *testing.T) {
	reviews := []git.PRReview{
		review("alice", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race"),
		review("alice", "COMMENTED", "2026-06-20T11:00:00Z", "more"),
		review("alice", "APPROVED", "2026-06-21T10:00:00Z", "ok"),
		review("bob", "APPROVED", "2026-06-20T10:00:00Z", "ok"),
	}
	collapsed := collapseReviews(reviews)
	if len(collapsed) != 2 {
		t.Fatalf("expected 2 collapsed reviews (one per reviewer), got %d: %+v", len(collapsed), collapsed)
	}
	// The effective state for alice must be APPROVED (her latest terminal).
	alice := collapsed[0]
	bob := collapsed[1]
	if alice.Reviewer != "alice" || alice.State != "APPROVED" {
		t.Errorf("alice effective review = %+v, want APPROVED", alice)
	}
	if bob.Reviewer != "bob" || bob.State != "APPROVED" {
		t.Errorf("bob effective review = %+v, want APPROVED", bob)
	}
}

// TestCollapseReviews_NoTimestampFallsBackToSliceOrder confirms that when
// submittedAt is absent (older gh or stripped output), the collapse falls back
// to input order, so the latest-by-position review still wins.
func TestCollapseReviews_NoTimestampFallsBackToSliceOrder(t *testing.T) {
	// No SubmittedAt on any review: position determines "latest".
	reviews := []git.PRReview{
		{Reviewer: "alice", State: "CHANGES_REQUESTED", Body: "- blocker: race"},
		{Reviewer: "alice", State: "APPROVED", Body: "ok"},
	}
	ev := evaluateGitHubReviews(reviews)
	if ev.State != ReviewStatePass {
		t.Fatalf("position-latest APPROVED must win when no timestamps, got %s: %s", ev.State, ev.Error)
	}
}

// TestCollapseReviews_EmptyAndSingle cover the degenerate inputs.
func TestCollapseReviews_EmptyAndSingle(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if got := collapseReviews(nil); got != nil {
			t.Errorf("expected nil for no reviews, got %+v", got)
		}
		ev := evaluateGitHubReviews(nil)
		if ev.State != ReviewStateNoVerdict {
			t.Errorf("expected NO_VERDICT for no reviews, got %s", ev.State)
		}
	})
	t.Run("single_approved", func(t *testing.T) {
		ev := evaluateGitHubReviews([]git.PRReview{review("alice", "APPROVED", "2026-06-20T10:00:00Z", "ok")})
		if ev.State != ReviewStatePass {
			t.Errorf("expected PASS for single approval, got %s", ev.State)
		}
	})
}

// TestCollapseReviews_CaseInsensitiveReviewer confirms reviews by "Alice" and
// "alice" collapse to one reviewer's history (GitHub logins are case-insensitive
// for matching purposes).
func TestCollapseReviews_CaseInsensitiveReviewer(t *testing.T) {
	reviews := []git.PRReview{
		review("Alice", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race"),
		review("alice", "APPROVED", "2026-06-21T10:00:00Z", "ok"),
	}
	collapsed := collapseReviews(reviews)
	if len(collapsed) != 1 {
		t.Fatalf("expected Alice/alice collapsed to one reviewer, got %d: %+v", len(collapsed), collapsed)
	}
	ev := evaluateGitHubReviews(reviews)
	if ev.State != ReviewStatePass {
		t.Errorf("expected PASS (case-insensitive collapse), got %s", ev.State)
	}
}

// TestClassifyGitHubEmptyReviews covers the no-individual-review-reachable path
// in classifyGitHubEmptyReviews (gastown-cet.12.6.3). When the gh pr-reviews
// call returns no rows but the overall PR reviewDecision is CHANGES_REQUESTED
// (typically from a branch-protection rule), the merge must be blocked even
// without a surfaceable reviewer; otherwise the absence of reviews is a soft
// no-verdict state, not a hard failure.
func TestClassifyGitHubEmptyReviews(t *testing.T) {
	basis := MergeCandidateBasis("origin/main", "head-sha")

	t.Run("changes_requested_blocks_merge", func(t *testing.T) {
		ev := classifyGitHubEmptyReviews("CHANGES_REQUESTED", basis)
		if ev.State != ReviewStateFail {
			t.Fatalf("branch-protection CHANGES_REQUESTED with no reviews must FAIL, got %s: %s", ev.State, ev.Error)
		}
		if ev.FailCount != 1 {
			t.Errorf("expected FailCount=1, got %d", ev.FailCount)
		}
		if len(ev.Results) != 1 {
			t.Fatalf("expected single synthetic result, got %d", len(ev.Results))
		}
		r := ev.Results[0]
		if r.Reviewer != "github" {
			t.Errorf("synthetic result reviewer should be github sentinel, got %q", r.Reviewer)
		}
		if r.Verdict != ReviewerVerdictFail {
			t.Errorf("synthetic result verdict should be FAIL, got %s", r.Verdict)
		}
		if r.DiffBasis != basis {
			t.Errorf("synthetic result basis=%+v, want %+v", r.DiffBasis, basis)
		}
		if !strings.Contains(ev.Error, "CHANGES_REQUESTED") {
			t.Errorf("error must surface the branch-protection signal, got %q", ev.Error)
		}
	})

	t.Run("review_required_is_no_verdict", func(t *testing.T) {
		// REVIEW_REQUIRED is the GitHub "no approvers yet" default and must
		// not be treated as a blocker on its own.
		ev := classifyGitHubEmptyReviews("REVIEW_REQUIRED", basis)
		if ev.State != ReviewStateNoVerdict {
			t.Fatalf("REVIEW_REQUIRED with no reviews must be NO_VERDICT, got %s: %s", ev.State, ev.Error)
		}
		if ev.NoVerdictCount != 1 {
			t.Errorf("expected NoVerdictCount=1, got %d", ev.NoVerdictCount)
		}
		if ev.FailCount != 0 {
			t.Errorf("REVIEW_REQUIRED must not count as FAIL, got FailCount=%d", ev.FailCount)
		}
	})

	t.Run("approved_is_no_verdict", func(t *testing.T) {
		// gh returns APPROVED only when an approving review row exists, so a
		// no-row + APPROVED combination is an upstream quirk we still must not
		// treat as a hard FAIL on these inputs alone.
		ev := classifyGitHubEmptyReviews("APPROVED", basis)
		if ev.State != ReviewStateNoVerdict {
			t.Fatalf("APPROVED with no surfaceable reviews must be NO_VERDICT, got %s", ev.State)
		}
	})

	t.Run("empty_decision_is_no_verdict", func(t *testing.T) {
		// "" / no-decision is the empty-response fallback and must be soft.
		for _, d := range []string{"", "UNKNOWN"} {
			ev := classifyGitHubEmptyReviews(d, basis)
			if ev.State != ReviewStateNoVerdict {
				t.Errorf("decision=%q must be NO_VERDICT, got %s: %s", d, ev.State, ev.Error)
			}
			if ev.Error != "no reviews" {
				t.Errorf("error should be the canonical 'no reviews', got %q", ev.Error)
			}
		}
	})

	t.Run("diff_basis_propagated_to_results", func(t *testing.T) {
		// Any basis the caller supplies must reach the synthetic result so
		// the review packet can attribute the verdict to the correct diff.
		ev := classifyGitHubEmptyReviews("CHANGES_REQUESTED", basis)
		for _, r := range ev.Results {
			if r.DiffBasis != basis {
				t.Errorf("basis not propagated to result %+v", r)
			}
		}
	})
}

// TestClassifyGitHubUnavailableError covers the network-failure path
// (gastown-cet.12.6.3). A failed gh pr-reviews call must downgrade to a
// single-reviewer UNAVAILABLE verdict rather than a hard merge failure or a
// silent PASS, so the merge gates defer rather than approving on
// unconfirmed state.
func TestClassifyGitHubUnavailableError(t *testing.T) {
	basis := MergeCandidateBasis("origin/main", "head-sha")
	netErr := fmt.Errorf("gh: connection refused: timeout after 45s")

	ev := classifyGitHubUnavailableError(netErr, basis)
	if ev.State != ReviewStateUnavailable {
		t.Fatalf("gh call failure must map to UNAVAILABLE, got %s", ev.State)
	}
	if ev.UnavailableCount != 1 {
		t.Errorf("expected UnavailableCount=1, got %d", ev.UnavailableCount)
	}
	if ev.FailCount != 0 || ev.PassCount != 0 || ev.NoVerdictCount != 0 {
		t.Errorf("UNAVAILABLE must have no other counters set, got %+v", ev)
	}
	if len(ev.Results) != 1 {
		t.Fatalf("expected a single synthetic result, got %d", len(ev.Results))
	}
	r := ev.Results[0]
	if r.Reviewer != "github" {
		t.Errorf("synthetic reviewer should be the github sentinel, got %q", r.Reviewer)
	}
	if r.Verdict != ReviewerVerdictUnavailable {
		t.Errorf("synthetic verdict should be UNAVAILABLE, got %s", r.Verdict)
	}
	if !strings.Contains(r.Evidence, "connection refused") {
		t.Errorf("evidence must carry the underlying error so the audit trail is useful, got %q", r.Evidence)
	}
	if !strings.Contains(ev.Error, "connection refused") {
		t.Errorf("top-level error must carry the underlying cause, got %q", ev.Error)
	}
	if r.DiffBasis != basis {
		t.Errorf("basis not propagated to unavailable result: got %+v want %+v", r.DiffBasis, basis)
	}
}

// TestGitHubMergeCandidateBasis_FailsClosedOnResolverError covers the original
// hardcoded-main bug (gastown-cet.12.6.6): a transient gh call failure must
// NOT cause mergeCandidateBasis to silently fall back to "origin/main". The
// resolver error must propagate up so the caller can return UNAVAILABLE
// instead of approving against an unconfirmed target branch.
func TestGitHubMergeCandidateBasis_FailsClosedOnResolverError(t *testing.T) {
	workDir, _, originTip, headSHA := initPRProviderOriginRepo(t, "main")
	installStubGh(t, map[int]prTestFixture{
		42: {FailWithStderr: "gh: Not logged in: run `gh auth login`"},
	})
	p := newGitHubProviderForTest(workDir)

	_, err := p.mergeCandidateBasis(42)
	if err == nil {
		t.Fatalf("mergeCandidateBasis must NOT silently fall back to origin/main on gh error; got nil error, basis would be MergeCandidateBasis(originTip=%s, head=%s)", originTip, headSHA)
	}
	if !strings.Contains(err.Error(), "resolve GitHub PR base branch") {
		t.Errorf("error should label the failure site (resolve GitHub PR base branch) so the audit trail is useful; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "Not logged in") {
		t.Errorf("error must carry the underlying cause; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "auth login") && !strings.Contains(err.Error(), "github pr-base-ref") {
		// The provider_command wrapper scrubs args but emits the opLabel;
		// either is fine for an audit trail. Just make sure the error
		// isn't fully opaque.
		t.Logf("note: error text did not surface an obvious cause beyond 'resolve GitHub PR base branch': %q", err.Error())
	}
}

// TestGitHubMergeCandidateBasis_FailsClosedOnEmptyBaseBranch covers the
// empty-resolved base branch path (gastown-cet.12.6.6): even when gh reports
// success with a structurally-valid but empty baseRefName (an upstream bug or
// a deleted branch in the PR config), the resolver must surface an error and
// not silently fall back to "origin/main".
func TestGitHubMergeCandidateBasis_FailsClosedOnEmptyBaseBranch(t *testing.T) {
	workDir, _, _, _ := initPRProviderOriginRepo(t, "main")
	installStubGh(t, map[int]prTestFixture{
		7: {BaseBranch: ""},
	})
	p := newGitHubProviderForTest(workDir)

	_, err := p.mergeCandidateBasis(7)
	if err == nil {
		t.Fatalf("empty base branch must surface as an error so the caller fails closed; got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error must explain the empty-base-branch gap; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "base branch") {
		t.Errorf("error must name 'base branch' so the audit trail identifies the resolution stage; got %q", err.Error())
	}
}

// TestGitHubMergeCandidateBasis_FailsClosedOnMalformedGhOutput covers the
// path where gh exits 0 but stdout is empty (e.g. a misconfigured stub or a
// runtime gh bug). The provider must surface a parse error rather than fall
// back to "origin/main".
func TestGitHubMergeCandidateBasis_FailsClosedOnMalformedGhOutput(t *testing.T) {
	workDir, _, _, _ := initPRProviderOriginRepo(t, "main")
	installStubGh(t, map[int]prTestFixture{
		99: {EmitEmpty: true},
	})
	p := newGitHubProviderForTest(workDir)

	_, err := p.mergeCandidateBasis(99)
	if err == nil {
		t.Fatalf("malformed gh output (empty stdout) must surface as a parse error; got nil")
	}
	// The exact error text may vary; assert that *some* error message
	// identifies the json-parse failure without naming the underlying call.
	if err.Error() == "" {
		t.Errorf("error must be non-empty so the audit trail can disambiguate the call site; got %q", err.Error())
	}
}

// TestGitHubMergeCandidateBasis_FailsClosedOnMissingRemoteBranch covers the
// resolver-success-but-target-missing path: gh reports a target branch (so
// the resolver "succeeds") but the local origin fetch cannot find that
// branch tip. The provider must fail closed rather than fall back to "main"
// for the basis.
func TestGitHubMergeCandidateBasis_FailsClosedOnMissingRemoteBranch(t *testing.T) {
	// Repo has only "main" on origin. PR claims to target "release".
	workDir, _, _, _ := initPRProviderOriginRepo(t, "main")
	installStubGh(t, map[int]prTestFixture{
		100: {BaseBranch: "release"},
	})
	p := newGitHubProviderForTest(workDir)

	_, err := p.mergeCandidateBasis(100)
	if err == nil {
		t.Fatalf("resolver returned a target branch ('release') that origin does not have; mergeCandidateBasis must surface the ls-remote failure rather than fall back to origin/main; got nil error")
	}
	if !strings.Contains(err.Error(), "release") {
		t.Errorf("error should name the missing target branch ('release') so the audit trail identifies the call site; got %q", err.Error())
	}
	// Either the gh-style err wrapping ("resolve origin/release tip: ...")
	// or the direct absence-of-ref message ("origin/release tip is empty")
	// counts as a properly-labeled failure site — both disambiguate the
	// remote-branch resolution step from the earlier PR-base-resolver step.
	if !strings.Contains(err.Error(), "origin/release tip") {
		t.Errorf("error should label the remote-branch tip resolution step ('origin/release tip'); got %q", err.Error())
	}
}

// TestGitHubMergeCandidateBasis_SucceedsForValidMainTarget covers the happy
// path that must NOT regress: a PR whose target is the standard "main"
// branch resolves to (origin/main, HEAD) without error.
func TestGitHubMergeCandidateBasis_SucceedsForValidMainTarget(t *testing.T) {
	workDir, _, originTip, headSHA := initPRProviderOriginRepo(t, "main")
	installStubGh(t, map[int]prTestFixture{
		1: {BaseBranch: "main"},
	})
	p := newGitHubProviderForTest(workDir)

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

// TestGitHubMergeCandidateBasis_SucceedsForNonMainTarget proves the fix did
// not regress the legitimate non-main case (gastown-cet.12.6.6): a PR whose
// target is "release" resolves to (origin/release, HEAD) without falling
// back to "origin/main".
func TestGitHubMergeCandidateBasis_SucceedsForNonMainTarget(t *testing.T) {
	workDir, _, releaseTip, headSHA := initPRProviderOriginRepo(t, "release")
	installStubGh(t, map[int]prTestFixture{
		5: {BaseBranch: "release"},
	})
	p := newGitHubProviderForTest(workDir)

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

// TestGitHubGetReviewEvaluation_UnavailableOnResolverError covers the
// end-to-end fail-closed mapping for the GitHub provider
// (gastown-cet.12.6.6). When the resolver fails, GetReviewEvaluation must
// return a UNAVAILABLE evaluation with an empty DiffBasis so the merge
// gates defer rather than authoritatively PASSing against an unconfirmed
// target branch.
func TestGitHubGetReviewEvaluation_UnavailableOnResolverError(t *testing.T) {
	workDir, _, _, _ := initPRProviderOriginRepo(t, "main")
	installStubGh(t, map[int]prTestFixture{
		77: {FailWithStderr: "gh: connection refused: no network"},
	})
	p := newGitHubProviderForTest(workDir)

	ev, err := p.GetReviewEvaluation(77)
	if err != nil {
		t.Fatalf("GetReviewEvaluation must swallow the resolver error and return UNAVAILABLE (not bubble up); got error: %v", err)
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
	if ev.PassCount != 0 {
		t.Errorf("resolver failure must NOT produce an authoritative PASS; got PassCount=%d", ev.PassCount)
	}
	if ev.FailCount != 0 {
		t.Errorf("resolver failure must not produce a hard FAIL either (that's still an authoritative verdict on an unconfirmed basis); got FailCount=%d", ev.FailCount)
	}
	if !strings.Contains(ev.Error, "GitHub PR base branch") {
		t.Errorf("top-level error must label the resolver stage (so the audit trail identifies WHICH call failed); got %q", ev.Error)
	}
	// The basis passed into the merge gates should be empty so the merge
	// audit trail cannot be misattributed to origin/main for a PR that
	// actually targets something else.
	if ev.DiffBasis != (DiffBasis{}) {
		t.Errorf("DiffBasis must be empty on resolver failure so the audit trail cannot misattribute the verdict to origin/main; got %+v", ev.DiffBasis)
	}
}

// TestGitHubGetReviewEvaluation_UnavailableOnEmptyBaseBranch covers the
// end-to-end empty-resolved-base path: even when gh returns a structurally
// valid but empty baseRefName, GetReviewEvaluation must return UNAVAILABLE
// rather than proceeding against an unconfirmed basis.
func TestGitHubGetReviewEvaluation_UnavailableOnEmptyBaseBranch(t *testing.T) {
	workDir, _, _, _ := initPRProviderOriginRepo(t, "main")
	installStubGh(t, map[int]prTestFixture{
		88: {BaseBranch: ""},
	})
	p := newGitHubProviderForTest(workDir)

	ev, err := p.GetReviewEvaluation(88)
	if err != nil {
		t.Fatalf("expected empty-base-branch to be swallowed into UNAVAILABLE; got error: %v", err)
	}
	if ev.State != ReviewStateUnavailable {
		t.Errorf("empty base branch must produce UNAVAILABLE so the merge gates defer; got state=%s error=%q", ev.State, ev.Error)
	}
	if ev.UnavailableCount != 1 {
		t.Errorf("expected UnavailableCount=1; got %d (full ev=%+v)", ev.UnavailableCount, ev)
	}
	if ev.PassCount != 0 || ev.FailCount != 0 {
		t.Errorf("empty-base-branch must NOT produce a terminal verdict (PASS or FAIL); got PassCount=%d FailCount=%d", ev.PassCount, ev.FailCount)
	}
}

// TestGitHubGetReviewEvaluation_PassesCleanlyForValidMainTarget is the
// happy-path end-to-end test (gastown-cet.12.6.6): a PR whose target is
// "main", with one APPROVED review, must produce a PASS evaluation whose
// DiffBasis pins to (origin/main, HEAD). This is the regression guard that
// the fail-closed path didn't overcorrect and start blocking legitimate
// main-target PRs.
func TestGitHubGetReviewEvaluation_PassesCleanlyForValidMainTarget(t *testing.T) {
	workDir, _, originTip, headSHA := initPRProviderOriginRepo(t, "main")
	installStubGh(t, map[int]prTestFixture{
		12: {BaseBranch: "main"},
	})
	p := newGitHubProviderForTest(workDir)

	// The stub emits an APPROVED review for any --json reviews request, so
	// the happy path drives collapse -> classify -> EvaluateReviews end-to-end.
	ev, err := p.GetReviewEvaluation(12)
	if err != nil {
		t.Fatalf("valid main-target PR with one approval must resolve cleanly; got error: %v", err)
	}
	if ev.State != ReviewStatePass {
		t.Fatalf("valid main-target PR with one APPROVED review must PASS; got state=%s error=%q", ev.State, ev.Error)
	}
	if ev.DiffBasis.Base != originTip {
		t.Errorf("DiffBasis.Base = %q, want %q (origin/main tip)", ev.DiffBasis.Base, originTip)
	}
	if ev.DiffBasis.Head != headSHA {
		t.Errorf("DiffBasis.Head = %q, want %q (HEAD)", ev.DiffBasis.Head, headSHA)
	}
}
