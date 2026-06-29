package refinery

// PRProvider abstracts VCS-specific PR operations for the merge queue.
// Implementations exist for GitHub (default) and Bitbucket Cloud.
type PRProvider interface {
	// FindPRNumber returns the PR number/ID for the given branch, or 0 if none exists.
	FindPRNumber(branch string) (int, error)

	// GetReviewEvaluation returns classified per-reviewer results for the PR.
	// Implementations should distinguish explicit approval, explicit rejection
	// with blockers, no-verdict/no-output, and provider/reviewer unavailability.
	GetReviewEvaluation(prNumber int) (*ReviewEvaluation, error)

	// MergePR merges a PR using the specified method (e.g., "squash", "merge", "rebase").
	// Returns the merge commit SHA on success (if available).
	MergePR(prNumber int, method string) (string, error)
}
