package refinery

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/bitbucket"
	"github.com/steveyegge/gastown/internal/git"
)

// bitbucketPRProvider implements PRProvider using the Bitbucket Cloud REST API.
type bitbucketPRProvider struct {
	git       *git.Git
	workspace string
	repoSlug  string
}

func newBitbucketPRProvider(g *git.Git) (PRProvider, error) {
	remoteURL, err := g.RemoteURL("origin")
	if err != nil {
		return nil, fmt.Errorf("bitbucket provider: failed to get origin remote URL: %w", err)
	}
	workspace, repoSlug, err := bitbucket.ParseBitbucketRemote(remoteURL)
	if err != nil {
		return nil, fmt.Errorf("bitbucket provider: %w", err)
	}
	return &bitbucketPRProvider{
		git:       g,
		workspace: workspace,
		repoSlug:  repoSlug,
	}, nil
}

func (p *bitbucketPRProvider) FindPRNumber(branch string) (int, error) {
	return p.git.FindBitbucketPRNumber(p.workspace, p.repoSlug, branch)
}

func (p *bitbucketPRProvider) IsPRApproved(prNumber int) (bool, error) {
	return p.git.IsBitbucketPRApproved(p.workspace, p.repoSlug, prNumber)
}

func (p *bitbucketPRProvider) GetReviewEvaluation(prNumber int) (*ReviewEvaluation, error) {
	participants, err := p.git.GetBitbucketPRParticipants(p.workspace, p.repoSlug, prNumber)
	if err != nil {
		return &ReviewEvaluation{
			State:            ReviewStateUnavailable,
			Results:          []ReviewerResult{{Reviewer: "bitbucket", Verdict: ReviewerVerdictUnavailable, Evidence: err.Error()}},
			UnavailableCount: 1,
			Error:            err.Error(),
		}, nil
	}

	if len(participants) == 0 {
		return &ReviewEvaluation{
			State:          ReviewStateNoVerdict,
			Results:        []ReviewerResult{{Reviewer: "bitbucket", Verdict: ReviewerVerdictNoVerdict, Evidence: "no participants"}},
			NoVerdictCount: 1,
			Error:          "no participants",
		}, nil
	}

	results := make([]ReviewerResult, 0, len(participants))
	for _, participant := range participants {
		result := ReviewerResult{Reviewer: participant.User}
		if participant.Role == "REVIEWER" && participant.Approved {
			result.Verdict = ReviewerVerdictPass
		} else if participant.Role == "REVIEWER" {
			// Bitbucket participants API does not expose CHANGES_REQUESTED state;
			// a non-approving reviewer is treated as no-verdict, not a blocker.
			result.Verdict = ReviewerVerdictNoVerdict
			result.Evidence = "reviewer has not approved"
		} else {
			// Non-reviewer participants do not count as reviewers.
			continue
		}
		results = append(results, result)
	}

	if len(results) == 0 {
		return &ReviewEvaluation{
			State:          ReviewStateNoVerdict,
			Results:        []ReviewerResult{{Reviewer: "bitbucket", Verdict: ReviewerVerdictNoVerdict, Evidence: "no reviewers"}},
			NoVerdictCount: 1,
			Error:          "no reviewers",
		}, nil
	}

	ev := EvaluateReviews(results, DegradedQuorumRule{})
	return &ev, nil
}

func (p *bitbucketPRProvider) MergePR(prNumber int, method string) (string, error) {
	// Map generic merge methods to Bitbucket strategy names.
	bbStrategy := method
	switch method {
	case "squash":
		bbStrategy = "squash"
	case "merge":
		bbStrategy = "merge_commit"
	case "rebase":
		bbStrategy = "fast_forward"
	}
	return p.git.BitbucketPRMerge(p.workspace, p.repoSlug, prNumber, bbStrategy)
}
