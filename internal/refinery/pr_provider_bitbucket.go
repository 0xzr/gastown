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
	// Record the merge-candidate diff basis so the review packet identifies the
	// exact diff reviewed (gastown-cet.8). A verdict against intermediate
	// commit history is distinguished from one against the final candidate.
	//
	// Resolve the PR's actual destination/target branch from the Bitbucket
	// API. We must NOT fall back to a hardcoded "origin/main" when the
	// resolver fails or returns an empty result — a transient
	// curl/auth/network failure would otherwise let review evaluation
	// continue and PASS with a basis from origin/main for a PR that actually
	// targets a release or other non-main branch (gastown-cet.12.6.6). The
	// fix: surface the resolver error and fail closed via the UNAVAILABLE
	// evaluation path.
	basis, err := p.mergeCandidateBasis(prNumber)
	if err != nil {
		return classifyBitbucketUnavailableError(err, DiffBasis{}), nil
	}

	participants, err := p.git.GetBitbucketPRParticipants(p.workspace, p.repoSlug, prNumber)
	if err != nil {
		return classifyBitbucketUnavailableError(err, basis), nil
	}

	return classifyBitbucketParticipants(participants, basis), nil
}

// classifyBitbucketUnavailableError maps a failed Bitbucket participants call
// onto a single-reviewer UNAVAILABLE evaluation. Extracted so the
// network-failure path is unit-testable without live HTTP calls
// (gastown-cet.12.6.3).
func classifyBitbucketUnavailableError(callErr error, basis DiffBasis) *ReviewEvaluation {
	return &ReviewEvaluation{
		State:            ReviewStateUnavailable,
		Results:          []ReviewerResult{{Reviewer: "bitbucket", Verdict: ReviewerVerdictUnavailable, Evidence: callErr.Error(), DiffBasis: basis}},
		UnavailableCount: 1,
		DiffBasis:        basis,
		Error:            callErr.Error(),
	}
}

// classifyBitbucketParticipants maps each Bitbucket participant onto a
// ReviewerResult under Bitbucket Cloud semantics, and aggregates them through
// EvaluateReviews. Extracted so the role/approved classification is
// unit-testable without live HTTP calls (gastown-cet.12.6.3).
//
// Semantics:
//   - REVIEWER + approved                       -> PASS
//   - REVIEWER + not approved                    -> NO_VERDICT
//     (Bitbucket's participants API does not expose CHANGES_REQUESTED; a
//     reviewer who has weighed in without approving is non-blocking.)
//   - any non-REVIEWER role (PARTICIPANT, etc.) -> skipped entirely;
//     non-reviewer participants do not count as reviewers and must not dilute
//     the quorum or generate blocker signals.
//
// The caller-facing empty-participants or all-non-reviewers case lands in the
// NO_VERDICT catch-all so the merge cannot proceed authoritatively without a
// surfaceable reviewer row.
func classifyBitbucketParticipants(participants []git.BitbucketParticipant, basis DiffBasis) *ReviewEvaluation {
	if len(participants) == 0 {
		return &ReviewEvaluation{
			State:          ReviewStateNoVerdict,
			Results:        []ReviewerResult{{Reviewer: "bitbucket", Verdict: ReviewerVerdictNoVerdict, Evidence: "no participants", DiffBasis: basis}},
			NoVerdictCount: 1,
			DiffBasis:      basis,
			Error:          "no participants",
		}
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
		}
	}

	ev := EvaluateReviews(results, DegradedQuorumRule{})
	ev.DiffBasis = basis
	return &ev
}

// mergeCandidateBasis returns the merge-candidate diff basis for the PR under
// review. Base is the merge target tip (origin/<PR-target-branch>); head is
// the local HEAD SHA.
//
// The PR's destination/target branch is resolved authoritatively from the
// Bitbucket Cloud REST API via GetBitbucketPRDestination. A resolver failure
// (auth, network, timeout, parse error) or an empty destination branch
// returned by the API are treated as errors, NOT silently replaced with
// "main" — that silent fallback is the original hardcoded-main bug
// (gastown-cet.12.6.6). The caller maps a returned error to a fail-closed
// UNAVAILABLE evaluation so the merge cannot authoritatively PASS against a
// basis whose target branch was never confirmed.
//
// On a clean resolver success with a concrete destination branch (e.g.
// "main", "release"), the basis is authoritative and behaves identically to
// the pre-fix semantics for legitimate main-target PRs.
func (p *bitbucketPRProvider) mergeCandidateBasis(prNumber int) (DiffBasis, error) {
	baseBranch, err := p.git.GetBitbucketPRDestination(p.workspace, p.repoSlug, prNumber)
	if err != nil {
		return DiffBasis{}, fmt.Errorf("resolve Bitbucket PR destination branch: %w", err)
	}
	if baseBranch == "" {
		return DiffBasis{}, fmt.Errorf("Bitbucket PR destination branch is empty (API returned no destination.branch.name)")
	}
	base, err := p.git.RemoteBranchTip("origin", baseBranch)
	if err != nil {
		return DiffBasis{}, fmt.Errorf("resolve origin/%s tip: %w", baseBranch, err)
	}
	if base == "" {
		// RemoteBranchTip returns "", nil when the branch ref is absent on
		// origin (git ls-remote --heads exits 0 with empty output). Without
		// this check we'd silently fall back to an empty-base merge-candidate
		// basis — same fail-open class as the original hardcoded-main bug,
		// just one resolution step later (gastown-cet.12.6.6).
		return DiffBasis{}, fmt.Errorf("origin/%s tip is empty: branch ref not present on remote", baseBranch)
	}
	head, _ := p.git.Rev("HEAD")
	return MergeCandidateBasis(base, head), nil
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
