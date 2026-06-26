package refinery

import (
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// review is a compact constructor for a raw gh-style review used by the
// collapse/classify characterization tests (gastown-cet.12.6.1, gastown-6z5).
// commitID is optional — pass "" for tests that don't exercise commit-aware
// collapse or commit_history basis stamping.
func review(reviewer, state, submittedAt, body, commitID string) git.PRReview {
	return git.PRReview{Reviewer: reviewer, State: state, SubmittedAt: submittedAt, Body: body, CommitID: commitID}
}

// reviewNoCommit is the legacy 4-arg constructor used by tests predating the
// gastown-6z5 commit-aware collapse. New tests should use review() so the
// CommitID is explicit.
func reviewNoCommit(reviewer, state, submittedAt, body string) git.PRReview {
	return review(reviewer, state, submittedAt, body, "")
}

// evaluateGitHubReviews runs the same collapse + classify + Evaluate path the
// GitHub provider uses, without shelling to gh. It is the characterization
// seam for the final-diff selection fix.
//
// headSHA is the PR's current head commit; pass "" to disable commit_history
// basis stamping (legacy merge-candidate behavior).
func evaluateGitHubReviews(reviews []git.PRReview, headSHA string) ReviewEvaluation {
	results := classifyCollapsedReviews(collapseReviews(reviews), DiffBasis{}, headSHA)
	return EvaluateReviews(results, DegradedQuorumRule{})
}

// evaluateGitHubReviewsLegacy is the pre-gastown-6z5 helper for tests that
// were written before commit-aware collapse and commit_history stamping.
// Equivalent to evaluateGitHubReviews(reviews, "").
func evaluateGitHubReviewsLegacy(reviews []git.PRReview) ReviewEvaluation {
	return evaluateGitHubReviews(reviews, "")
}

// TestCollapseReviews_StaleChangesRequestedDoesNotBlockApproval covers the
// regression at the heart of gastown-cet.12.6.1: a reviewer who requested
// changes and then later approved must not block the merge. Before the fix the
// stale CHANGES_REQUESTED was counted as an independent hard FAIL.
func TestCollapseReviews_StaleChangesRequestedDoesNotBlockApproval(t *testing.T) {
	reviews := []git.PRReview{
		review("alice", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race condition", ""),
		review("alice", "APPROVED", "2026-06-21T10:00:00Z", "lgtm", ""),
	}
	ev := evaluateGitHubReviewsLegacy(reviews)
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
		review("bob", "APPROVED", "2026-06-20T10:00:00Z", "looks fine", ""),
		review("bob", "CHANGES_REQUESTED", "2026-06-22T10:00:00Z", "- blocker: missing test", ""),
	}
	ev := evaluateGitHubReviewsLegacy(reviews)
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
		review("carol", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race", ""),
		review("carol", "APPROVED", "2026-06-21T10:00:00Z", "fixed", ""),
	}
	reversed := []git.PRReview{
		review("carol", "APPROVED", "2026-06-21T10:00:00Z", "fixed", ""),
		review("carol", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race", ""),
	}
	a := evaluateGitHubReviewsLegacy(chronological)
	b := evaluateGitHubReviewsLegacy(reversed)
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
		review("dave", "APPROVED", "2026-06-20T10:00:00Z", "lgtm", ""),
		review("dave", "DISMISSED", "2026-06-21T10:00:00Z", "", ""),
	}
	ev := evaluateGitHubReviewsLegacy(reviews)
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
		review("erin", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race", ""),
		review("erin", "DISMISSED", "2026-06-21T10:00:00Z", "", ""),
		review("erin", "APPROVED", "2026-06-22T10:00:00Z", "all good now", ""),
	}
	ev := evaluateGitHubReviewsLegacy(reviews)
	if ev.State != ReviewStatePass {
		t.Fatalf("CHANGES_REQUESTED -> DISMISSED -> APPROVED must PASS, got %s: %s", ev.State, ev.Error)
	}
}

// TestCollapseReviews_CommentedDoesNotOverrideTerminal confirms COMMENTED is a
// non-decision: it must not override a prior APPROVED or CHANGES_REQUESTED.
func TestCollapseReviews_CommentedDoesNotOverrideTerminal(t *testing.T) {
	t.Run("approved_then_commented_still_pass", func(t *testing.T) {
		reviews := []git.PRReview{
			review("frank", "APPROVED", "2026-06-20T10:00:00Z", "lgtm", ""),
			review("frank", "COMMENTED", "2026-06-21T10:00:00Z", "nit: rename var", ""),
		}
		ev := evaluateGitHubReviewsLegacy(reviews)
		if ev.State != ReviewStatePass {
			t.Fatalf("APPROVED then COMMENTED must stay PASS, got %s: %s", ev.State, ev.Error)
		}
	})
	t.Run("changes_then_commented_still_fail", func(t *testing.T) {
		reviews := []git.PRReview{
			review("grace", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: missing test", ""),
			review("grace", "COMMENTED", "2026-06-21T10:00:00Z", "also see line 4", ""),
		}
		ev := evaluateGitHubReviewsLegacy(reviews)
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
		review("alice", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race", ""),
		review("bob", "APPROVED", "2026-06-20T09:00:00Z", "lgtm", ""),
		review("alice", "APPROVED", "2026-06-21T10:00:00Z", "fixed now", ""),
		review("bob", "COMMENTED", "2026-06-22T09:00:00Z", "nice", ""),
	}
	ev := evaluateGitHubReviewsLegacy(reviews)
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
		review("alice", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race", ""),
		review("alice", "COMMENTED", "2026-06-20T11:00:00Z", "more", ""),
		review("alice", "APPROVED", "2026-06-21T10:00:00Z", "ok", ""),
		review("bob", "APPROVED", "2026-06-20T10:00:00Z", "ok", ""),
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
	ev := evaluateGitHubReviewsLegacy(reviews)
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
		ev := evaluateGitHubReviewsLegacy(nil)
		if ev.State != ReviewStateNoVerdict {
			t.Errorf("expected NO_VERDICT for no reviews, got %s", ev.State)
		}
	})
	t.Run("single_approved", func(t *testing.T) {
		ev := evaluateGitHubReviewsLegacy([]git.PRReview{review("alice", "APPROVED", "2026-06-20T10:00:00Z", "ok", "")})
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
		review("Alice", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race", ""),
		review("alice", "APPROVED", "2026-06-21T10:00:00Z", "ok", ""),
	}
	collapsed := collapseReviews(reviews)
	if len(collapsed) != 1 {
		t.Fatalf("expected Alice/alice collapsed to one reviewer, got %d: %+v", len(collapsed), collapsed)
	}
	ev := evaluateGitHubReviewsLegacy(reviews)
	if ev.State != ReviewStatePass {
		t.Errorf("expected PASS (case-insensitive collapse), got %s", ev.State)
	}
}

// ============================================================================
// gastown-6z5: commit-aware collapse, commit_history stamping, CauseKey
// derivation, and packet-level preservation.
// ============================================================================

// TestCollapseReviews_StaleOlderCommitDoesNotBlock covers the gap at the
// heart of gastown-6z5: when a reviewer requested changes on an older commit
// and then only COMMENTED (non-terminal) on the current head, the latest
// commit_id wins so the CHANGES_REQUESTED is discarded and the reviewer is
// treated as no-verdict rather than a blocker.
//
// Without the fix the older CHANGES_REQUESTED would be counted as the
// reviewer's effective verdict (latest terminal = CHANGES_REQUESTED on the
// older commit) and the merge would FAIL.
func TestCollapseReviews_StaleOlderCommitDoesNotBlock(t *testing.T) {
	reviews := []git.PRReview{
		// Stale CHANGES_REQUESTED on commit X1 (older).
		review("henry", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race", "X1"),
		// Non-terminal on commit X2 (current head).
		review("henry", "COMMENTED", "2026-06-21T10:00:00Z", "looks fine now", "X2"),
	}
	// head=X2: the latest commit_id the reviewer touched is X2, so only the
	// COMMENTED survives. No terminal verdict -> NO_VERDICT.
	ev := evaluateGitHubReviews(reviews, "X2")
	if ev.State != ReviewStateNoVerdict {
		t.Fatalf("stale CHANGES_REQUESTED on X1 must not block newer COMMENTED on X2, got %s: %s", ev.State, ev.Error)
	}
	if ev.FailCount != 0 {
		t.Errorf("expected FailCount=0 (stale rejection dropped per-commit), got %d", ev.FailCount)
	}
	if ev.NoVerdictCount != 1 {
		t.Errorf("expected NoVerdictCount=1, got %d", ev.NoVerdictCount)
	}
}

// TestCollapseReviews_LatestCommitApproveWins confirms the symmetric case:
// when a reviewer requested changes on an older commit and approved on the
// newer commit, the latest commit_id wins and the merge is unblocked.
func TestCollapseReviews_LatestCommitApproveWins(t *testing.T) {
	reviews := []git.PRReview{
		review("ivy", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race", "X1"),
		review("ivy", "APPROVED", "2026-06-21T10:00:00Z", "lgtm", "X2"),
	}
	ev := evaluateGitHubReviews(reviews, "X2")
	if ev.State != ReviewStatePass {
		t.Fatalf("APPROVED on newer commit must PASS, got %s: %s", ev.State, ev.Error)
	}
	if ev.PassCount != 1 {
		t.Errorf("expected PassCount=1, got %d", ev.PassCount)
	}
}

// TestCollapseReviews_LatestCommitChangesRequestedWins confirms that when
// a reviewer approves on an older commit and then requests changes on the
// newer (current) head, the newer commit wins and the merge is blocked.
func TestCollapseReviews_LatestCommitChangesRequestedWins(t *testing.T) {
	reviews := []git.PRReview{
		review("jack", "APPROVED", "2026-06-20T10:00:00Z", "looks fine", "X1"),
		review("jack", "CHANGES_REQUESTED", "2026-06-22T10:00:00Z", "- blocker: missing test", "X2"),
	}
	ev := evaluateGitHubReviews(reviews, "X2")
	if ev.State != ReviewStateFail {
		t.Fatalf("CHANGES_REQUESTED on newer commit must FAIL, got %s: %s", ev.State, ev.Error)
	}
	if ev.FailCount != 1 {
		t.Errorf("expected FailCount=1, got %d", ev.FailCount)
	}
}

// TestCollapseReviews_NoCommitIDFallsBackToPosition confirms the legacy
// path: when CommitID is absent, the collapse falls back to position-only
// ordering so older gh output keeps working unchanged.
func TestCollapseReviews_NoCommitIDFallsBackToPosition(t *testing.T) {
	reviews := []git.PRReview{
		review("kim", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race", ""),
		review("kim", "APPROVED", "2026-06-21T10:00:00Z", "ok", ""),
	}
	// headSHA != "" but per-review CommitID == "" — should still collapse via
	// SubmittedAt (position-only) to the latest APPROVED.
	ev := evaluateGitHubReviews(reviews, "ANYHEAD")
	if ev.State != ReviewStatePass {
		t.Fatalf("no-commit-id collapse must still PASS on latest APPROVED, got %s: %s", ev.State, ev.Error)
	}
}

// TestCollapseReviews_MultipleCommitsSameReviewerIsolated confirms a
// reviewer touching N commits emits one collapsed review per distinct
// commit_id, and the result picks the LATEST commit's terminal verdict.
func TestCollapseReviews_MultipleCommitsSameReviewerIsolated(t *testing.T) {
	reviews := []git.PRReview{
		review("leo", "APPROVED", "2026-06-19T10:00:00Z", "v1", "X1"),
		review("leo", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "v2 blocker", "X2"),
		review("leo", "APPROVED", "2026-06-21T10:00:00Z", "v3 lgtm", "X3"),
	}
	collapsed := collapseReviews(reviews)
	if len(collapsed) != 1 {
		t.Fatalf("expected one collapsed review per reviewer, got %d: %+v", len(collapsed), collapsed)
	}
	if collapsed[0].State != "APPROVED" {
		t.Errorf("expected latest-commit APPROVED, got %s", collapsed[0].State)
	}
	if collapsed[0].CommitID != "X3" {
		t.Errorf("expected commit_id=X3 to be preserved, got %s", collapsed[0].CommitID)
	}
}

// TestClassify_StaleReviewStampsCommitHistory confirms that when a
// reviewer's commit_id != head SHA, classifyCollapsedReviews stamps
// DiffBasis.Kind = "commit_history" so EvaluateReviews can reclassify the
// FAIL as a no-verdict audit gap (hq-luba class).
func TestClassify_StaleReviewStampsCommitHistory(t *testing.T) {
	reviews := []git.PRReview{
		review("mia", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race", "X1"),
	}
	results := classifyCollapsedReviews(collapseReviews(reviews), MergeCandidateBasis("base", "X2"), "X2")
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if results[0].DiffBasis.Kind != "commit_history" {
		t.Errorf("expected Kind=commit_history for stale review, got %s", results[0].DiffBasis.Kind)
	}
	if results[0].DiffBasis.Head != "X1" {
		t.Errorf("expected Head=X1 (the stale review's commit), got %s", results[0].DiffBasis.Head)
	}
}

// TestClassify_CurrentReviewStampsMergeCandidate confirms that when a
// reviewer's commit_id == head SHA, classifyCollapsedReviews keeps the
// packet-level merge_candidate basis (no commit_history flip).
func TestClassify_CurrentReviewStampsMergeCandidate(t *testing.T) {
	reviews := []git.PRReview{
		review("nia", "APPROVED", "2026-06-20T10:00:00Z", "lgtm", "HEAD"),
	}
	results := classifyCollapsedReviews(collapseReviews(reviews), MergeCandidateBasis("base", "HEAD"), "HEAD")
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if results[0].DiffBasis.Kind != "merge_candidate" {
		t.Errorf("expected Kind=merge_candidate for current-head review, got %s", results[0].DiffBasis.Kind)
	}
}

// TestClassify_CommitHistoryFAILReclassifiedToNoVerdict covers the full
// commit_history reclassification path added for hq-luba: a CHANGES_REQUESTED
// review on an older commit must NOT authoritatively reject the merge
// candidate. After commit_history stamping, EvaluateReviews reclassifies
// the FAIL as NO_VERDICT.
func TestClassify_CommitHistoryFAILReclassifiedToNoVerdict(t *testing.T) {
	reviews := []git.PRReview{
		review("olive", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race", "X1"),
	}
	ev := evaluateGitHubReviews(reviews, "X2")
	if ev.State != ReviewStateNoVerdict {
		t.Fatalf("commit_history FAIL must reclassify to NO_VERDICT, got %s: %s", ev.State, ev.Error)
	}
	if ev.FailCount != 0 {
		t.Errorf("expected FailCount=0 (commit_history FAIL reclassified), got %d", ev.FailCount)
	}
	if ev.NoVerdictCount != 1 {
		t.Errorf("expected NoVerdictCount=1, got %d", ev.NoVerdictCount)
	}
}

// TestClassify_EmptyHeadSHADisablesCommitHistoryStamping confirms that when
// the provider cannot determine the head SHA, classifyCollapsedReviews
// keeps the packet-level basis and does NOT stamp commit_history. This is
// the safe offline/error fallback.
func TestClassify_EmptyHeadSHADisablesCommitHistoryStamping(t *testing.T) {
	reviews := []git.PRReview{
		review("paul", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race", "X1"),
	}
	results := classifyCollapsedReviews(collapseReviews(reviews), MergeCandidateBasis("base", "X1"), "")
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if results[0].DiffBasis.Kind != "merge_candidate" {
		t.Errorf("empty headSHA must keep packet-level merge_candidate basis, got %s", results[0].DiffBasis.Kind)
	}
}

// TestClassify_ChangesRequestedSetsCauseKey confirms that a CHANGES_REQUESTED
// review carries a non-empty CauseKey so downstream tooling can branch on a
// stable machine-readable failure reason.
func TestClassify_ChangesRequestedSetsCauseKey(t *testing.T) {
	reviews := []git.PRReview{
		review("quinn", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race condition", "HEAD"),
	}
	results := classifyCollapsedReviews(collapseReviews(reviews), MergeCandidateBasis("base", "HEAD"), "HEAD")
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if results[0].CauseKey == "" {
		t.Fatalf("expected non-empty CauseKey on CHANGES_REQUESTED, got empty")
	}
	// Blockers produce a snake_cased key derived from the first blocker.
	if results[0].CauseKey != "race_condition" {
		t.Errorf("expected snake_cased blocker-derived CauseKey 'race_condition', got %q", results[0].CauseKey)
	}
}

// TestClassify_ChangesRequestedFallsBackToGenericKey confirms that when the
// CHANGES_REQUESTED body has no extractable blockers, the CauseKey falls
// back to a stable generic key so downstream tooling always has a string.
func TestClassify_ChangesRequestedFallsBackToGenericKey(t *testing.T) {
	reviews := []git.PRReview{
		review("raven", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "lgtm overall but please consider restructuring the API surface significantly across many files", "HEAD"),
	}
	results := classifyCollapsedReviews(collapseReviews(reviews), MergeCandidateBasis("base", "HEAD"), "HEAD")
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if results[0].CauseKey == "" {
		t.Fatalf("expected non-empty CauseKey on CHANGES_REQUESTED, got empty")
	}
	// Free-form prose > 6 words has no extractable blocker; falls back.
	if results[0].CauseKey != "gh_changes_requested" {
		t.Errorf("expected generic fallback CauseKey 'gh_changes_requested', got %q", results[0].CauseKey)
	}
}

// TestNormalizeBlockerCauseKey covers the cause-key extraction edge cases.
func TestNormalizeBlockerCauseKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"race condition", "race_condition"},
		{"- blocker: missing test", "missing_test"},
		{"* API break", "api_break"},
		{"  extra   whitespace  ", "extra_whitespace"},
		{"", ""},
		{"a b c d e f g", ""}, // > 6 words, reject
		// Single very long token rejected.
		{"this_is_an_extraordinarily_long_single_token_that_exceeds_the_sixty_four_char_limit_x", ""},
		// All-punctuation token rejected.
		{"... !!!", ""},
	}
	for _, tc := range cases {
		got := normalizeBlockerCauseKey(tc.in)
		if got != tc.want {
			t.Errorf("normalizeBlockerCauseKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
