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
	reviews, err := p.git.GetPRReviews(prNumber)
	if err != nil {
		// If we cannot reach the review provider at all, treat as a single
		// unavailable reviewer rather than a hard merge failure.
		return &ReviewEvaluation{
			State:            ReviewStateUnavailable,
			Results:          []ReviewerResult{{Reviewer: "github", Verdict: ReviewerVerdictUnavailable, Evidence: err.Error()}},
			UnavailableCount: 1,
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
				Results:   []ReviewerResult{{Reviewer: "github", Verdict: ReviewerVerdictFail, Evidence: "overall review decision: CHANGES_REQUESTED"}},
				FailCount: 1,
				Error:     "overall review decision: CHANGES_REQUESTED",
			}, nil
		}
		return &ReviewEvaluation{
			State:          ReviewStateNoVerdict,
			Results:        []ReviewerResult{{Reviewer: "github", Verdict: ReviewerVerdictNoVerdict, Evidence: "no reviews"}},
			NoVerdictCount: 1,
			Error:          "no reviews",
		}, nil
	}

	results := make([]ReviewerResult, 0, len(reviews))
	for _, r := range reviews {
		result := ReviewerResult{Reviewer: r.Reviewer}
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
	return &ev, nil
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
