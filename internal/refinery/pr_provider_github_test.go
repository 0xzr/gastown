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
	return evaluateGitHubReviewsAtHead(reviews, "")
}

// evaluateGitHubReviewsAtHead runs classifyCollapsedReviews against the supplied
// current head SHA, so tests can exercise commit-history detection.
func evaluateGitHubReviewsAtHead(reviews []git.PRReview, head string) ReviewEvaluation {
	mergeBasis := MergeCandidateBasis("base-sha", head)
	results := classifyCollapsedReviews(collapseReviews(reviews), mergeBasis, head)
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

// reviewOnCommit is a compact constructor that also sets the commit SHA the
// review was submitted against.
func reviewOnCommit(reviewer, state, submittedAt, body, commitID string) git.PRReview {
	r := review(reviewer, state, submittedAt, body)
	r.CommitID = commitID
	return r
}

// TestClassifyCollapsedReviews_CurrentCommitUsesMergeCandidateBasis confirms a
// review whose CommitID matches the current PR head is stamped with the final
// merge-candidate DiffBasis, so a FAIL remains authoritative.
func TestClassifyCollapsedReviews_CurrentCommitUsesMergeCandidateBasis(t *testing.T) {
	head := "final-head-sha"
	reviews := []git.PRReview{
		reviewOnCommit("luba", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race", head),
	}
	ev := evaluateGitHubReviewsAtHead(reviews, head)
	if ev.State != ReviewStateFail {
		t.Fatalf("review on current head must be authoritative FAIL, got %s: %s", ev.State, ev.Error)
	}
	if !ev.Results[0].DiffBasis.IsMergeCandidate() {
		t.Errorf("review on current head must have merge-candidate basis, got %+v", ev.Results[0].DiffBasis)
	}
}

// TestClassifyCollapsedReviews_StaleCommitUsesCommitHistoryBasis covers the
// hq-luba incident class (gastown-cet.12.6.7): a review submitted against an
// earlier push is stamped with commit_history DiffBasis, so a FAIL is
// reclassified to a no-verdict audit gap rather than blocking the merge.
func TestClassifyCollapsedReviews_StaleCommitUsesCommitHistoryBasis(t *testing.T) {
	head := "final-head-sha"
	stale := "stale-head-sha"
	reviews := []git.PRReview{
		reviewOnCommit("alice", "APPROVED", "2026-06-20T10:00:00Z", "lgtm", head),
		reviewOnCommit("luba", "CHANGES_REQUESTED", "2026-06-20T09:00:00Z", "- blocker: race", stale),
	}
	mergeBasis := MergeCandidateBasis("base-sha", head)
	results := classifyCollapsedReviews(collapseReviews(reviews), mergeBasis, head)
	ev := EvaluateReviews(results, DegradedQuorumRule{Enabled: true, MinPassReviews: 1})

	// The stale commit-history FAIL must not produce a hard FAIL state.
	if ev.State == ReviewStateFail {
		t.Fatalf("stale commit-history FAIL must not authoritatively reject the merge candidate, got state %s (cause=%s): %s", ev.State, ev.CauseKey, ev.Error)
	}
	if ev.FailCount != 0 {
		t.Errorf("expected FailCount=0 after reclassifying stale FAIL, got %d", ev.FailCount)
	}
	if ev.NoVerdictCount != 1 {
		t.Errorf("expected NoVerdictCount=1 (reclassified luba), got %d", ev.NoVerdictCount)
	}
	if ev.State != ReviewStateDegradedQuorum {
		t.Errorf("expected DEGRADED_QUORUM (proceed under audit), got %s", ev.State)
	}

	// The stale review must carry a commit_history basis.
	var found bool
	for _, r := range ev.Results {
		if r.Reviewer == "luba" {
			found = true
			if r.DiffBasis.Kind != "commit_history" {
				t.Errorf("stale review must have commit_history basis, got %+v", r.DiffBasis)
			}
			if r.DiffBasis.Head != stale {
				t.Errorf("stale review basis head must be the review commit %s, got %s", stale, r.DiffBasis.Head)
			}
		}
	}
	if !found {
		t.Errorf("expected luba in results, got %+v", ev.Results)
	}
}

// TestClassifyCollapsedReviews_MissingCommitIDDefaultsToMergeCandidate confirms
// backward compatibility: reviews that do not carry a CommitID are treated as
// merge-candidate verdicts. This avoids reclassifying real FAILs when the
// provider field is unavailable.
func TestClassifyCollapsedReviews_MissingCommitIDDefaultsToMergeCandidate(t *testing.T) {
	reviews := []git.PRReview{
		review("luba", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race"),
	}
	ev := evaluateGitHubReviewsAtHead(reviews, "any-head-sha")
	if ev.State != ReviewStateFail {
		t.Fatalf("review without CommitID must remain authoritative FAIL, got %s: %s", ev.State, ev.Error)
	}
	if !ev.Results[0].DiffBasis.IsMergeCandidate() {
		t.Errorf("review without CommitID must default to merge-candidate basis, got %+v", ev.Results[0].DiffBasis)
	}
}

// TestClassifyCollapsedReviews_UnknownHeadDefaultsToMergeCandidate confirms that
// when the current head cannot be resolved, reviews do not get stamped as
// commit_history just because they carry a CommitID. Reclassifying every
// verdict as history would silently bypass real rejections.
func TestClassifyCollapsedReviews_UnknownHeadDefaultsToMergeCandidate(t *testing.T) {
	reviews := []git.PRReview{
		reviewOnCommit("luba", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race", "some-commit-sha"),
	}
	mergeBasis := MergeCandidateBasis("base-sha", "")
	results := classifyCollapsedReviews(collapseReviews(reviews), mergeBasis, "")
	ev := EvaluateReviews(results, DegradedQuorumRule{})
	if ev.State != ReviewStateFail {
		t.Fatalf("review with unknown head must remain authoritative FAIL, got %s: %s", ev.State, ev.Error)
	}
	if !ev.Results[0].DiffBasis.IsMergeCandidate() {
		t.Errorf("review with unknown head must default to merge-candidate basis, got %+v", ev.Results[0].DiffBasis)
	}
}
