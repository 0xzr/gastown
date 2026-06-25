package refinery

import (
	"strings"

	"github.com/steveyegge/gastown/internal/git"
)

// githubPRProvider implements PRProvider using the gh CLI via git.Git.
type githubPRProvider struct {
	git *git.Git
}

func newGitHubPRProvider(g *git.Git) PRProvider {
	return &githubPRProvider{git: g}
}

func (p *githubPRProvider) FindPRNumber(branch string) (int, error) {
	return p.git.FindPRNumber(branch)
}

func (p *githubPRProvider) IsPRApproved(prNumber int) (bool, error) {
	return p.git.IsPRApproved(prNumber)
}

func (p *githubPRProvider) GetReviewEvaluation(prNumber int) (*ReviewEvaluation, error) {
	// The merge-candidate diff basis: GitHub reviews are submitted against the
	// PR head, which is the diff the squash merge will land. Recording this
	// makes the review packet identify the exact diff reviewed (gastown-cet.8),
	// so a verdict against intermediate commit history can be distinguished
	// from a verdict against the final merge candidate.
	basis := p.mergeCandidateBasis()

	reviews, err := p.git.GetPRReviews(prNumber)
	if err != nil {
		// If we cannot reach the review provider at all, treat as a single
		// unavailable reviewer rather than a hard merge failure.
		return &ReviewEvaluation{
			State:            ReviewStateUnavailable,
			Results:          []ReviewerResult{{Reviewer: "github", Verdict: ReviewerVerdictUnavailable, Evidence: err.Error(), DiffBasis: basis}},
			UnavailableCount: 1,
			DiffBasis:        basis,
			Error:            err.Error(),
		}, nil
	}

	if len(reviews) == 0 {
		// No reviews at all is a no-verdict state, not a failure.
		decision, _ := p.git.GetPRReviewDecision(prNumber)
		if decision == "CHANGES_REQUESTED" {
			// The overall PR decision can be changes-requested even when there are
			// no individual reviews reachable (e.g. branch protection rule).
			return &ReviewEvaluation{
				State:     ReviewStateFail,
				Results:   []ReviewerResult{{Reviewer: "github", Verdict: ReviewerVerdictFail, Evidence: "overall review decision: CHANGES_REQUESTED", DiffBasis: basis}},
				FailCount: 1,
				DiffBasis: basis,
				Error:     "overall review decision: CHANGES_REQUESTED",
			}, nil
		}
		return &ReviewEvaluation{
			State:          ReviewStateNoVerdict,
			Results:        []ReviewerResult{{Reviewer: "github", Verdict: ReviewerVerdictNoVerdict, Evidence: "no reviews", DiffBasis: basis}},
			NoVerdictCount: 1,
			DiffBasis:      basis,
			Error:          "no reviews",
		}, nil
	}

	results := make([]ReviewerResult, 0, len(reviews))
	for _, r := range reviews {
		result := ReviewerResult{Reviewer: r.Reviewer, DiffBasis: basis}
		switch strings.ToUpper(r.State) {
		case "APPROVED":
			result.Verdict = ReviewerVerdictPass
		case "CHANGES_REQUESTED":
			result.Verdict = ReviewerVerdictFail
			result.Evidence = r.Body
			result.Blockers = extractBlockers(r.Body)
		case "COMMENTED", "PENDING", "DISMISSED", "":
			result.Verdict = ReviewerVerdictNoVerdict
			result.Evidence = r.Body
		default:
			result.Verdict = ReviewerVerdictNoVerdict
			result.Evidence = "unrecognized review state: " + r.State
		}
		results = append(results, result)
	}

	ev := EvaluateReviews(results, DegradedQuorumRule{})
	ev.DiffBasis = basis
	return &ev, nil
}

// mergeCandidateBasis returns the merge-candidate diff basis for the PR under
// review. Base is the merge target tip (origin/<target>); head is the branch
// tip. Both are resolved on a best-effort basis — an empty component means
// "unknown", which EvaluateReviews treats as a merge-candidate basis (the safe
// default) rather than a commit-history basis.
func (p *githubPRProvider) mergeCandidateBasis() DiffBasis {
	base, _ := p.git.RemoteBranchTip("origin", "main")
	head, _ := p.git.Rev("HEAD")
	return MergeCandidateBasis(base, head)
}

func (p *githubPRProvider) MergePR(prNumber int, method string) (string, error) {
	return p.git.GhPrMerge(prNumber, method)
}

// extractBlockers pulls explicit blocking statements out of a review body.
func extractBlockers(body string) []string {
	var blockers []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "blocking") || strings.Contains(lower, "blocker") || strings.HasPrefix(lower, "- ") || strings.HasPrefix(lower, "* ") {
			blockers = append(blockers, line)
		}
	}
	return blockers
}
