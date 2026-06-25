package refinery

import (
	"fmt"
	"strings"
)

// ReviewerVerdict classifies the result from a single reviewer or review probe.
// This is the core of the no-verdict/degraded-quorum fix: a missing or empty
// review is not a content failure.
type ReviewerVerdict string

const (
	// ReviewerVerdictPass means the reviewer explicitly approved the change.
	ReviewerVerdictPass ReviewerVerdict = "PASS"

	// ReviewerVerdictFail means the reviewer explicitly rejected the change with
	// concrete blockers or requested changes.
	ReviewerVerdictFail ReviewerVerdict = "FAIL"

	// ReviewerVerdictNoVerdict means the reviewer produced no actionable output:
	// no review, only comments, or an empty/unparseable verdict.
	ReviewerVerdictNoVerdict ReviewerVerdict = "NO_VERDICT"

	// ReviewerVerdictUnavailable means the reviewer could not be reached:
	// provider error, CAPTCHA, rate-limit, or reviewer is capped/disabled.
	ReviewerVerdictUnavailable ReviewerVerdict = "UNAVAILABLE"
)

// IsTerminal returns true if the verdict is a hard pass/fail decision.
// NO_VERDICT and UNAVAILABLE are non-terminal and may be handled by degraded quorum.
func (v ReviewerVerdict) IsTerminal() bool {
	return v == ReviewerVerdictPass || v == ReviewerVerdictFail
}

// DiffBasis identifies the exact range a reviewer or review probe examined.
// Recording the basis guards against two incident classes:
//   - hq-luba: an intermediate commit history was reviewed, but the final
//     squashed merge candidate differs from that history.
//   - chrome/polybot-dxz: a reviewer verdict is later divorced from the code
//     it was supposed to evaluate.
type DiffBasis struct {
	// Base is the commit/branch the reviewer diffed against (e.g. origin/main).
	Base string `json:"base,omitempty"`

	// Head is the commit/branch that was reviewed (e.g. the MR head SHA).
	Head string `json:"head,omitempty"`

	// Kind describes what kind of diff was reviewed:
	//   - "merge_candidate": the diff the merge will land (base...head triple-dot).
	//   - "commit_history": per-commit differences (base..head double-dot).
	Kind string `json:"kind,omitempty"`
}

// IsMergeCandidate reports whether this basis is the final merge candidate.
func (d DiffBasis) IsMergeCandidate() bool {
	return d.Kind == "merge_candidate" || d.Kind == ""
}

// ReviewerResult is the classified outcome for one reviewer.
type ReviewerResult struct {
	Reviewer string          `json:"reviewer"`
	Verdict  ReviewerVerdict `json:"verdict"`
	Blockers []string        `json:"blockers,omitempty"`
	Evidence string          `json:"evidence,omitempty"`

	// DiffBasis records the exact diff that was reviewed. Required for
	// traceability; callers that cannot determine it should leave it empty.
	DiffBasis DiffBasis `json:"diff_basis,omitempty"`

	// CauseKey is a stable machine-readable key for concrete FAILs, e.g.
	// "race_condition", "missing_test", "api_break". For NO_VERDICT or
	// UNAVAILABLE it should remain empty.
	CauseKey string `json:"cause_key,omitempty"`
}

// HasConcreteBlockers reports whether this result includes explicit blocking issues.
func (r ReviewerResult) HasConcreteBlockers() bool {
	return r.Verdict == ReviewerVerdictFail && len(r.Blockers) > 0
}

// ReviewState is the overall review status for a merge request.
type ReviewState string

const (
	// ReviewStatePass means the MR has enough explicit approving reviews.
	ReviewStatePass ReviewState = "PASS"

	// ReviewStateFail means at least one reviewer explicitly rejected with blockers.
	ReviewStateFail ReviewState = "FAIL"

	// ReviewStateNoVerdict means no reviewer returned a terminal verdict and
	// degraded quorum is not enabled or not satisfied.
	ReviewStateNoVerdict ReviewState = "NO_VERDICT"

	// ReviewStateUnavailable means every review probe failed and the system
	// cannot determine whether the change is safe to merge.
	ReviewStateUnavailable ReviewState = "UNAVAILABLE"

	// ReviewStateDegradedQuorum means the merge can proceed despite missing or
	// unavailable reviewers because enough independent PASS reviews exist and
	// degraded quorum is explicitly enabled.
	ReviewStateDegradedQuorum ReviewState = "DEGRADED_QUORUM"
)

// ReviewEvaluation is the result of evaluating all reviewers for an MR.
type ReviewEvaluation struct {
	State            ReviewState      `json:"state"`
	Results          []ReviewerResult `json:"results"`
	PassCount        int              `json:"pass_count"`
	FailCount        int              `json:"fail_count"`
	NoVerdictCount   int              `json:"no_verdict_count"`
	UnavailableCount int              `json:"unavailable_count"`
	AuditReviewers   []string         `json:"audit_reviewers,omitempty"`
	Error            string           `json:"error,omitempty"`

	// DiffBasis records the merge-candidate diff the reviewers evaluated.
	// All provider implementations should set this from the PR/MR under review.
	DiffBasis DiffBasis `json:"diff_basis,omitempty"`

	// CauseKey is the primary machine-readable failure cause when State=FAIL.
	// It is taken from the first concrete FAIL result that supplied one, or
	// synthesized as "reviewer_rejection" when no key is provided.
	CauseKey string `json:"cause_key,omitempty"`
}

// DegradedQuorumRule configures when a merge may proceed without full review
// coverage. The rule is explicit and opt-in: if disabled, NO_VERDICT and
// UNAVAILABLE are treated as blocking.
type DegradedQuorumRule struct {
	// Enabled turns on degraded-quorum handling.
	Enabled bool `json:"enabled"`

	// MinPassReviews is the minimum number of independent PASS reviews required
	// to override missing or unavailable reviewers. Zero defaults to 1.
	MinPassReviews int `json:"min_pass_reviews"`

	// RequiredReviewers is the set of reviewers whose explicit PASS is always
	// required. A missing required reviewer can only be satisfied by degraded
	// quorum if that reviewer is UNAVAILABLE (not merely NO_VERDICT).
	RequiredReviewers []string `json:"required_reviewers,omitempty"`
}

// HasRequiredReviewers reports whether the rule names specific required reviewers.
func (r DegradedQuorumRule) HasRequiredReviewers() bool {
	return len(r.RequiredReviewers) > 0
}

// IsRequiredReviewer reports whether reviewer is in the required set.
func (r DegradedQuorumRule) IsRequiredReviewer(reviewer string) bool {
	for _, req := range r.RequiredReviewers {
		if strings.EqualFold(req, reviewer) {
			return true
		}
	}
	return false
}

// minPassReviews returns the effective minimum, defaulting to 1 when enabled.
func (r DegradedQuorumRule) minPassReviews() int {
	if r.MinPassReviews > 0 {
		return r.MinPassReviews
	}
	return 1
}

// MergeCandidateBasis returns a DiffBasis describing the final merge-candidate
// diff: the range the merge will actually land, i.e. base...head (triple-dot).
// This is the authoritative range for append-only / invariant checks — never
// the per-commit history (base..head double-dot), which can include commits
// that were later rewritten or superseded by the final squashed candidate
// (the hq-luba incident class).
func MergeCandidateBasis(base, head string) DiffBasis {
	return DiffBasis{Base: base, Head: head, Kind: "merge_candidate"}
}

// EvaluateReviews aggregates per-reviewer results into an overall decision.
//
// Rules, in order:
//  1. Any authoritative FAIL (merge-candidate basis + concrete blockers) -> FAIL.
//     A FAIL whose DiffBasis is "commit_history" is NOT authoritative against
//     the final merge candidate: it reviewed intermediate commits that the
//     squashed merge candidate may have superseded. Such verdicts are
//     reclassified as audit-gaps (NO_VERDICT) rather than hard rejections,
//     so a reworked final candidate is not blocked by a stale review.
//  2. With degraded quorum enabled and enough independent PASS reviews:
//     missing/unavailable reviewers become an audit obligation, not a blocker.
//  3. Without degraded quorum, NO_VERDICT/UNAVAILABLE block the merge.
//  4. All PASS -> PASS.
//  5. No terminal verdicts at all -> NO_VERDICT or UNAVAILABLE.
func EvaluateReviews(results []ReviewerResult, rule DegradedQuorumRule) ReviewEvaluation {
	ev := ReviewEvaluation{Results: results}
	if len(results) == 0 {
		ev.State = ReviewStateNoVerdict
		ev.Error = "no reviewer results"
		return ev
	}

	// Classify each result, reclassifying commit-history FAILs as audit-gaps
	// so they cannot authoritatively reject the final merge candidate (hq-luba).
	auditGaps := []string{}
	for i := range results {
		r := &results[i]
		switch r.Verdict {
		case ReviewerVerdictPass:
			ev.PassCount++
		case ReviewerVerdictFail:
			if !r.DiffBasis.IsMergeCandidate() {
				// Reviewed intermediate commit history, not the final candidate.
				// Reclassify: the rejection does not apply to the merge candidate.
				auditGaps = append(auditGaps, r.Reviewer)
				r.Verdict = ReviewerVerdictNoVerdict
				r.Blockers = nil
				ev.NoVerdictCount++
			} else {
				ev.FailCount++
			}
		case ReviewerVerdictNoVerdict:
			ev.NoVerdictCount++
		case ReviewerVerdictUnavailable:
			ev.UnavailableCount++
		}
	}
	if len(auditGaps) > 0 {
		ev.Error = fmt.Sprintf("reviewer(s) reviewed commit history, not merge candidate: %s", strings.Join(auditGaps, ", "))
	}

	// Any authoritative FAIL with concrete blockers is a hard rejection.
	var failEvidence []string
	for _, r := range results {
		if r.Verdict == ReviewerVerdictFail {
			if r.HasConcreteBlockers() {
				failEvidence = append(failEvidence, fmt.Sprintf("%s: %s", r.Reviewer, strings.Join(r.Blockers, "; ")))
			} else {
				failEvidence = append(failEvidence, fmt.Sprintf("%s: requested changes", r.Reviewer))
			}
		}
	}
	if len(failEvidence) > 0 {
		ev.State = ReviewStateFail
		ev.Error = fmt.Sprintf("reviewer rejection: %s", strings.Join(failEvidence, " | "))
		// Use the first concrete FAIL that supplied a cause key; if none did,
		// default to a stable reviewer-rejection key.
		for _, r := range results {
			if r.Verdict == ReviewerVerdictFail && r.CauseKey != "" {
				ev.CauseKey = r.CauseKey
				break
			}
		}
		if ev.CauseKey == "" {
			ev.CauseKey = "reviewer_rejection"
		}
		return ev
	}

	// Pure pass with no failures.
	if ev.NoVerdictCount == 0 && ev.UnavailableCount == 0 {
		if ev.PassCount > 0 {
			ev.State = ReviewStatePass
			return ev
		}
		ev.State = ReviewStateNoVerdict
		ev.Error = "no reviewer returned a verdict"
		return ev
	}

	// Degraded-quorum handling for missing/unavailable reviewers.
	if rule.Enabled && ev.PassCount >= rule.minPassReviews() {
		// Required reviewers may only be skipped if they are genuinely unavailable.
		requiredMissing := false
		for _, r := range results {
			if rule.IsRequiredReviewer(r.Reviewer) && r.Verdict != ReviewerVerdictPass && r.Verdict != ReviewerVerdictUnavailable {
				requiredMissing = true
				break
			}
		}
		if !requiredMissing {
			for _, r := range results {
				if r.Verdict != ReviewerVerdictPass {
					ev.AuditReviewers = append(ev.AuditReviewers, r.Reviewer)
				}
			}
			ev.State = ReviewStateDegradedQuorum
			ev.Error = fmt.Sprintf("degraded quorum: %d PASS review(s), %d reviewer(s) need audit", ev.PassCount, len(ev.AuditReviewers))
			return ev
		}
	}

	// No degraded quorum or not enough PASS reviews -> blocking.
	if ev.UnavailableCount > 0 && ev.NoVerdictCount == 0 && ev.PassCount == 0 {
		ev.State = ReviewStateUnavailable
		ev.Error = fmt.Sprintf("all review probes unavailable (%d reviewer(s))", ev.UnavailableCount)
		return ev
	}

	ev.State = ReviewStateNoVerdict
	if ev.UnavailableCount > 0 {
		ev.Error = fmt.Sprintf("reviewer(s) unavailable or gave no verdict (%d no-verdict, %d unavailable)", ev.NoVerdictCount, ev.UnavailableCount)
	} else {
		ev.Error = fmt.Sprintf("reviewer(s) gave no verdict (%d)", ev.NoVerdictCount)
	}
	return ev
}
