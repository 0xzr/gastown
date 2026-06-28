package refinery

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/bitbucket"
	"github.com/steveyegge/gastown/internal/git"
)

// bitbucketBaseResolver returns a PR's destination branch name
// (gastown-cet.12.6.6). It is the seam used by mergeCandidateBasis to look
// up the actual merge target — not a hardcoded "main" — so the basis is
// testable against non-main targets without shelling to the Bitbucket REST
// API. Production wiring resolves this from
// *git.Git.GetBitbucketPRBaseBranch; tests substitute a stub.
type bitbucketBaseResolver func(workspace, repoSlug string, prID int) (string, error)

// bitbucketPRProvider implements PRProvider using the Bitbucket Cloud REST API.
type bitbucketPRProvider struct {
	git           *git.Git
	workspace     string
	repoSlug      string
	resolveTarget bitbucketBaseResolver
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
		git:           g,
		workspace:     workspace,
		repoSlug:      repoSlug,
		resolveTarget: g.GetBitbucketPRBaseBranch,
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
// review. The base branch is the PR's actual destination — not hardcoded to
// main — so PRs targeting release/ or other non-default branches diff against
// the correct range (gastown-cet.12.6.6). Best-effort resolution; an empty
// component means "unknown" (treated as a merge-candidate basis, the safe
// default).
//
// Fallback order for the base branch:
//  1. Resolve the PR's destination branch via the Bitbucket REST API. If
//     non-empty, use origin/<branch>.
//  2. On provider error (no token, network failure, timeout), fall back to
//     origin/main so existing single-target deployments keep working. The
//     bug the fix addresses (hardcoded origin/main when target differs) only
//     manifests when the API succeeds; degraded failures were already latent
//     and remain so by design.
func (p *bitbucketPRProvider) mergeCandidateBasis(prNumber int) DiffBasis {
	target := "main"
	if p.resolveTarget != nil {
		if name, err := p.resolveTarget(p.workspace, p.repoSlug, prNumber); err == nil && name != "" {
			target = name
		}
	}
	base, _ := p.git.RemoteBranchTip("origin", target)
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
