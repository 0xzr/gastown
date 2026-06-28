package refinery

import (
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

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
