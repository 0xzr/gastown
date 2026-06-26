package refinery

import (
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
