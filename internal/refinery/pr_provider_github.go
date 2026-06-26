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
	//
	// The base ref is the PR's actual target branch (queried from gh), not a
	// hardcoded "main": a PR against a non-main target must report its own
	// basis so mergeCandidateBasis doesn't silently misroute the verdict
	// (gastown-6z5).
	basis := p.mergeCandidateBasis(prNumber)

	// Fetch the PR's current head SHA so per-reviewer basis can be stamped
	// "commit_history" when a review was submitted against an older commit.
	// Empty on error: classifyCollapsedReviews treats an empty headSHA as
	// "unknown" and falls back to the packet-level basis rather than guessing.
	headSHA, _ := p.git.GetPRHeadSHA(prNumber)

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
				Results:   []ReviewerResult{{Reviewer: "github", Verdict: ReviewerVerdictFail, Evidence: "overall review decision: CHANGES_REQUESTED", DiffBasis: basis, CauseKey: "gh_changes_requested_overall"}},
				FailCount: 1,
				DiffBasis: basis,
				CauseKey:  "gh_changes_requested_overall",
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

	results := classifyCollapsedReviews(collapseReviews(reviews), basis, headSHA)

	ev := EvaluateReviews(results, DegradedQuorumRule{})
	ev.DiffBasis = basis
	return &ev, nil
}

// classifyCollapsedReviews maps each per-reviewer effective review (already
// collapsed to its final state by collapseReviews) onto a ReviewerResult,
// applying GitHub review-state semantics. Extracted so the collapse + classify
// path is unit-testable without shelling to gh (gastown-cet.12.6.1).
//
// headSHA is the PR's current head commit. When a reviewer's collapsed review
// carries a CommitID that is not an ancestor of (or equal to) headSHA, that
// review was made against intermediate commit history, not the merge candidate:
// the result is stamped with a commit_history basis so EvaluateReviews can
// reclassify a stale FAIL as a no-verdict audit gap (gastown-6z5).
//
// headSHA == "" disables commit_history stamping: callers in tests or in
// offline/error paths that cannot determine the head fall back to the
// packet-level basis, preserving the legacy merge-candidate semantics.
func classifyCollapsedReviews(reviews []git.PRReview, basis DiffBasis, headSHA string) []ReviewerResult {
	results := make([]ReviewerResult, 0, len(reviews))
	for _, r := range reviews {
		resultBasis := basis
		if headSHA != "" && r.CommitID != "" && r.CommitID != headSHA {
			// Stale review: stamped against an older commit, so the verdict
			// applies to intermediate commit history rather than the merge
			// candidate. Keep the base/head range informative while flipping
			// Kind so EvaluateReviews reclassifies a FAIL to NO_VERDICT.
			resultBasis = DiffBasis{
				Base: basis.Base,
				Head: r.CommitID,
				Kind: "commit_history",
			}
		}
		result := ReviewerResult{Reviewer: r.Reviewer, DiffBasis: resultBasis}
		switch strings.ToUpper(r.State) {
		case "APPROVED":
			result.Verdict = ReviewerVerdictPass
		case "CHANGES_REQUESTED":
			result.Verdict = ReviewerVerdictFail
			result.Evidence = r.Body
			result.Blockers = extractBlockers(r.Body)
			result.CauseKey = deriveChangesRequestedCauseKey(r.Body, result.Blockers)
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

// deriveChangesRequestedCauseKey produces a stable machine-readable key for
// a CHANGES_REQUESTED review so the merge queue can record why a gate failed
// without forcing every reader to parse free-form review bodies (gastown-6z5).
//
// Preference order:
//  1. Snake-case the first concrete blocker line when it is short and
//     shaped like an identifier (e.g. "race condition" -> "race_condition").
//  2. Fall back to a generic stable key so downstream tooling still has
//     something deterministic to branch on.
func deriveChangesRequestedCauseKey(body string, blockers []string) string {
	for _, b := range blockers {
		key := normalizeBlockerCauseKey(b)
		if key != "" {
			return key
		}
	}
	if key := normalizeBlockerCauseKey(body); key != "" {
		return key
	}
	return "gh_changes_requested"
}

// normalizeBlockerCauseKey turns a short human-readable phrase into a
// snake_case machine key. Returns "" when the input is too long or shaped
// like free-form prose (more than 6 words) — the caller then falls back to
// a generic stable key rather than fabricating a misleading identifier.
func normalizeBlockerCauseKey(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Strip leading list markers ("- ", "* ", "• ").
	for _, prefix := range []string{"- ", "* ", "• "} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			break
		}
	}
	// Collapse whitespace and lowercase.
	s = strings.ToLower(strings.TrimSpace(s))
	// Strip a leading "blocker:" / "blocking:" prefix — the convention
	// used by Gas Town review bodies — so the resulting key describes
	// the failure, not the convention.
	for _, prefix := range []string{"blocker:", "blocking:"} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			break
		}
	}
	s = strings.TrimSpace(s)
	words := strings.Fields(s)
	if len(words) == 0 || len(words) > 6 {
		return ""
	}
	var b strings.Builder
	for _, w := range words {
		// Drop purely non-alphanumeric tokens (punctuation) and tokens
		// that contain characters that cannot survive a stable key.
		cleaned := strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				return r
			default:
				return -1
			}
		}, w)
		if cleaned == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('_')
		}
		b.WriteString(cleaned)
	}
	out := b.String()
	if out == "" || len(out) > 64 {
		return ""
	}
	return out
}

// collapseReviews reduces a reviewer's full review history to the single
// effective final-state review, so a stale earlier verdict cannot block a merge
// the reviewer has since superseded (gastown-cet.12.6.1, gastown-6z5).
//
// GitHub review semantics (mirroring the reviewDecision the API exposes):
//   - APPROVED and CHANGES_REQUESTED are terminal states that set the
//     reviewer's decision.
//   - COMMENTED and PENDING carry no decision and leave the prior decision
//     unchanged.
//   - DISMISSED explicitly dismisses the reviewer's prior decision, clearing
//     it back to "no decision".
//
// Commit-aware selection (gastown-6z5): when a reviewer has reviewed multiple
// commits on the PR, only the reviews attached to their latest reviewed
// commit_id are considered. Reviews on older commits are discarded, so a
// stale CHANGES_REQUESTED cannot override a later COMMENTED or APPROVED on
// the newer head. The collapse result for each reviewer carries the
// commit_id that produced it, so the caller can stamp a commit_history basis
// for stale-rejection reclassification downstream.
//
// Reviews are ordered by SubmittedAt (ties broken by input order) before the
// latest terminal decision is selected, so the result is independent of the
// slice order the provider happens to return. Each reviewer appears at most
// once in the output; reviewers with no effective terminal decision emit their
// latest non-terminal review (e.g. a COMMENTED body) so its evidence survives.
// Reviews without a CommitID are grouped under a sentinel empty key so the
// commit-aware path degrades gracefully to position-only ordering.
func collapseReviews(reviews []git.PRReview) []git.PRReview {
	if len(reviews) == 0 {
		return nil
	}

	// Group each reviewer's reviews, preserving their original positions for
	// stable tie-breaking when timestamps are absent or equal.
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
		byReviewer[name] = append(byReviewer[name], indexedReview{idx: i, rev: r, commitID: r.CommitID})
	}

	out := make([]git.PRReview, 0, len(order))
	for _, name := range order {
		hist := byReviewer[name]

		// Commit-aware narrowing: if the reviewer reviewed multiple distinct
		// commits and at least one carries a CommitID, keep only the reviews
		// attached to their latest reviewed commit. When CommitID is absent on
		// every review we fall through to the legacy position-only collapse so
		// older gh output keeps working unchanged.
		if anyCommitID(hist) {
			hist = narrowToLatestCommit(hist)
		}

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

// indexedReview is a reviewer's review paired with its slice position and
// commit_id. Used by collapseReviews for stable tie-breaking when timestamps
// are absent or equal.
type indexedReview struct {
	idx      int
	rev      git.PRReview
	commitID string
}

// anyCommitID reports whether any review in the group carries a non-empty
// CommitID. Used to decide whether the commit-aware collapse path is
// applicable; a group with no CommitID information anywhere degrades to the
// legacy position-only behavior so older gh output is unaffected.
func anyCommitID(hist []indexedReview) bool {
	for _, r := range hist {
		if r.commitID != "" {
			return true
		}
	}
	return false
}

// narrowToLatestCommit drops reviews attached to commits older than the
// reviewer's latest reviewed commit. The comparison is lexicographic over
// the SHA string, which matches Git's commit ordering (SHAs are random but
// unique and stable within a single PR). The dropped reviews' verdicts are
// discarded so they cannot influence the merged-candidate verdict
// (gastown-6z5).
func narrowToLatestCommit(hist []indexedReview) []indexedReview {
	var maxSHA string
	for _, r := range hist {
		if r.commitID > maxSHA {
			maxSHA = r.commitID
		}
	}
	if maxSHA == "" {
		return hist
	}
	out := make([]indexedReview, 0, len(hist))
	for _, r := range hist {
		if r.commitID == maxSHA {
			out = append(out, r)
		}
	}
	return out
}

// mergeCandidateBasis returns the merge-candidate diff basis for the PR under
// review. Base is the merge target tip (origin/<target>); head is the branch
// tip. Both are resolved on a best-effort basis — an empty component means
// "unknown", which EvaluateReviews treats as a merge-candidate basis (the safe
// default) rather than a commit-history basis.
//
// The base ref is queried from the PR's actual target (gh pr view
// --json baseRefName) rather than hardcoded to "main", so a PR against any
// other target branch is routed through the correct diff (gastown-6z5). On
// error the base falls back to origin/main so a transient gh failure cannot
// silently misroute the verdict.
func (p *githubPRProvider) mergeCandidateBasis(prNumber int) DiffBasis {
	baseRef := "main"
	if name, err := p.git.GetPRBaseRef(prNumber); err == nil && name != "" {
		baseRef = name
	}
	base, _ := p.git.RemoteBranchTip("origin", baseRef)
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
