package refinery

import (
	"fmt"
	"sort"
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
		return classifyGitHubUnavailableError(err, basis), nil
	}

	if len(reviews) == 0 {
		// No reviews at all is a no-verdict state, not a failure. The overall
		// PR decision can still surface CHANGES_REQUESTED via branch protection,
		// which is a hard blocking signal even when no individual review row is
		// reachable.
		decision, _ := p.git.GetPRReviewDecision(prNumber)
		return classifyGitHubEmptyReviews(decision, basis), nil
	}

	results := classifyCollapsedReviews(collapseReviews(reviews), basis)

	ev := EvaluateReviews(results, DegradedQuorumRule{})
	ev.DiffBasis = basis
	return &ev, nil
}

// classifyGitHubUnavailableError maps a failed gh pr-reviews call onto a
// single-reviewer UNAVAILABLE evaluation. Extracted so the network-failure
// path is unit-testable without shelling to gh (gastown-cet.12.6.3).
func classifyGitHubUnavailableError(callErr error, basis DiffBasis) *ReviewEvaluation {
	return &ReviewEvaluation{
		State:            ReviewStateUnavailable,
		Results:          []ReviewerResult{{Reviewer: "github", Verdict: ReviewerVerdictUnavailable, Evidence: callErr.Error(), DiffBasis: basis}},
		UnavailableCount: 1,
		DiffBasis:        basis,
		Error:            callErr.Error(),
	}
}

// classifyGitHubEmptyReviews resolves the no-individual-review-reachable case.
// The overall PR decision can be CHANGES_REQUESTED (e.g. via a branch
// protection rule applied without surfaceable reviewer rows), which is a hard
// blocking signal; otherwise the absence of any review is a non-fatal
// no-verdict state. Extracted so the branch-protection path is unit-testable
// without shelling to gh (gastown-cet.12.6.3).
func classifyGitHubEmptyReviews(decision string, basis DiffBasis) *ReviewEvaluation {
	if decision == "CHANGES_REQUESTED" {
		return &ReviewEvaluation{
			State:     ReviewStateFail,
			Results:   []ReviewerResult{{Reviewer: "github", Verdict: ReviewerVerdictFail, Evidence: "overall review decision: CHANGES_REQUESTED", DiffBasis: basis}},
			FailCount: 1,
			DiffBasis: basis,
			Error:     "overall review decision: CHANGES_REQUESTED",
		}
	}
	return &ReviewEvaluation{
		State:          ReviewStateNoVerdict,
		Results:        []ReviewerResult{{Reviewer: "github", Verdict: ReviewerVerdictNoVerdict, Evidence: "no reviews", DiffBasis: basis}},
		NoVerdictCount: 1,
		DiffBasis:      basis,
		Error:          "no reviews",
	}
}

// classifyCollapsedReviews maps each per-reviewer effective review (already
// collapsed to its final state by collapseReviews) onto a ReviewerResult,
// applying GitHub review-state semantics. Extracted so the collapse + classify
// path is unit-testable without shelling to gh (gastown-cet.12.6.1).
func classifyCollapsedReviews(reviews []git.PRReview, basis DiffBasis) []ReviewerResult {
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
	return results
}

// collapseReviews reduces a reviewer's full review history to the single
// effective final-state review, so a stale earlier verdict cannot block a merge
// the reviewer has since superseded (gastown-cet.12.6.1).
//
// GitHub review semantics (mirroring the reviewDecision the API exposes):
//   - APPROVED and CHANGES_REQUESTED are terminal states that set the
//     reviewer's decision.
//   - COMMENTED and PENDING carry no decision and leave the prior decision
//     unchanged.
//   - DISMISSED explicitly dismisses the reviewer's prior decision, clearing
//     it back to "no decision".
//
// Reviews are ordered by SubmittedAt (ties broken by input order) before the
// latest terminal decision is selected, so the result is independent of the
// slice order the provider happens to return. Each reviewer appears at most
// once in the output; reviewers with no effective terminal decision emit their
// latest non-terminal review (e.g. a COMMENTED body) so its evidence survives.
func collapseReviews(reviews []git.PRReview) []git.PRReview {
	if len(reviews) == 0 {
		return nil
	}

	// Group each reviewer's reviews, preserving their original positions for
	// stable tie-breaking when timestamps are absent or equal.
	type indexedReview struct {
		idx int
		rev git.PRReview
	}
	byReviewer := make(map[string][]indexedReview)
	order := make([]string, 0, len(reviews))
	for i, r := range reviews {
		name := strings.ToLower(strings.TrimSpace(r.Reviewer))
		if name == "" {
			// A review with no identifiable author cannot be collapsed per
			// reviewer; keep it distinct so it is never silently dropped.
			name = fmt.Sprintf("anonymous-%d", i)
		}
		if _, seen := byReviewer[name]; !seen {
			order = append(order, name)
		}
		byReviewer[name] = append(byReviewer[name], indexedReview{idx: i, rev: r})
	}

	out := make([]git.PRReview, 0, len(order))
	for _, name := range order {
		hist := byReviewer[name]
		// Chronological order by SubmittedAt; equal/empty timestamps keep
		// original order (sort.SliceStable).
		sort.SliceStable(hist, func(a, b int) bool {
			ta, tb := hist[a].rev.SubmittedAt, hist[b].rev.SubmittedAt
			if ta == "" || tb == "" {
				return hist[a].idx < hist[b].idx
			}
			return ta < tb
		})

		// Walk chronologically applying GitHub review-state semantics. Track the
		// latest review that established a decision (APPROVED/CHANGES_REQUESTED);
		// DISMISSED clears it. If no decision holds, fall back to the latest
		// review overall so its evidence (e.g. a COMMENTED body) survives.
		decisionSet := false
		decision := git.PRReview{}
		for _, cur := range hist {
			switch strings.ToUpper(cur.rev.State) {
			case "APPROVED", "CHANGES_REQUESTED":
				decision = cur.rev
				decisionSet = true
			case "DISMISSED":
				// Dismissing clears the reviewer's prior decision.
				decisionSet = false
				decision = git.PRReview{}
			}
		}

		if decisionSet {
			out = append(out, decision)
		} else {
			// hist is non-empty (grouped from at least one review).
			out = append(out, hist[len(hist)-1].rev)
		}
	}
	return out
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
