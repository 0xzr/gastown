package refinery

import (
	"fmt"
	"strings"

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
	// Record the merge-candidate diff basis so the review packet identifies the
	// exact diff reviewed (gastown-cet.8). A verdict against intermediate
	// commit history is distinguished from one against the final candidate.
	//
	// The base ref is the PR's actual destination branch (queried via the
	// Bitbucket REST API), not a hardcoded "main": a PR against a non-main
	// target must report its own basis so mergeCandidateBasis doesn't silently
	// misroute the verdict (gastown-6z5).
	basis := p.mergeCandidateBasis(prNumber)

	participants, err := p.git.GetBitbucketPRParticipants(p.workspace, p.repoSlug, prNumber)
	if err != nil {
		return &ReviewEvaluation{
			State:            ReviewStateUnavailable,
			Results:          []ReviewerResult{{Reviewer: "bitbucket", Verdict: ReviewerVerdictUnavailable, Evidence: err.Error(), DiffBasis: basis}},
			UnavailableCount: 1,
			DiffBasis:        basis,
			Error:            err.Error(),
		}, nil
	}

	if len(participants) == 0 {
		return &ReviewEvaluation{
			State:          ReviewStateNoVerdict,
			Results:        []ReviewerResult{{Reviewer: "bitbucket", Verdict: ReviewerVerdictNoVerdict, Evidence: "no participants", DiffBasis: basis}},
			NoVerdictCount: 1,
			DiffBasis:      basis,
			Error:          "no participants",
		}, nil
	}

	results := make([]ReviewerResult, 0, len(participants))
	for _, participant := range participants {
		result := ReviewerResult{Reviewer: participant.User, DiffBasis: basis}
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
			Results:        []ReviewerResult{{Reviewer: "bitbucket", Verdict: ReviewerVerdictNoVerdict, Evidence: "no reviewers", DiffBasis: basis}},
			NoVerdictCount: 1,
			DiffBasis:      basis,
			Error:          "no reviewers",
		}, nil
	}

	ev := EvaluateReviews(results, DegradedQuorumRule{})
	ev.DiffBasis = basis
	return &ev, nil
}

// mergeCandidateBasis returns the merge-candidate diff basis for the PR under
// review.
//
// The base ref is the PR's actual destination branch (queried via the
// Bitbucket REST API), not a hardcoded "main", so a PR against any other
// target branch is routed through the correct diff (gastown-6z5).
//
// Fail-closed on unknown destination (gastown-6z5 rework): if the destination
// query fails or returns empty, return UnknownBasis() rather than fall back to
// origin/main. A non-main PR with an unresolvable destination must not be
// silently routed through origin/main — the verdict would be against the
// wrong diff and could authoritatively approve the merge under a fabricated
// basis. UnknownBasis defers the merge (every verdict becomes a no-verdict
// audit gap) until a real basis can be resolved.
func (p *bitbucketPRProvider) mergeCandidateBasis(prNumber int) DiffBasis {
	name, err := p.git.GetBitbucketPRDestination(p.workspace, p.repoSlug, prNumber)
	if err != nil || strings.TrimSpace(name) == "" {
		return UnknownBasis()
	}
	base, _ := p.git.RemoteBranchTip("origin", name)
	head, _ := p.git.Rev("HEAD")
	return MergeCandidateBasis(base, head)
}

// mergeCandidateBasisForDestination is the test-only seam mirroring
// mergeCandidateBasis: it returns UnknownBasis on empty/whitespace input and
// the merge-candidate basis otherwise. Used by the rework's
// TestBitbucketMergeCandidateBasis_FailClosedOnEmptyDestination test to pin
// the provider's fail-closed behavior without mocking the Bitbucket REST
// surface (gastown-6z5 rework).
func (p *bitbucketPRProvider) mergeCandidateBasisForDestination(name string) DiffBasis {
	if strings.TrimSpace(name) == "" {
		return UnknownBasis()
	}
	base, _ := p.git.RemoteBranchTip("origin", name)
	head, _ := p.git.Rev("HEAD")
	return MergeCandidateBasis(base, head)
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
