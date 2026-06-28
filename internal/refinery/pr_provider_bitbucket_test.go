package refinery

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

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
