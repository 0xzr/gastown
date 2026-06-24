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

// ReviewerResult is the classified outcome for one reviewer.
type ReviewerResult struct {
	Reviewer string          `json:"reviewer"`
	Verdict  ReviewerVerdict `json:"verdict"`
	Blockers []string        `json:"blockers,omitempty"`
	Evidence string          `json:"evidence,omitempty"`
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

// EvaluateReviews aggregates per-reviewer results into an overall decision.
//
// Rules, in order:
//  1. Any FAIL with concrete blockers -> FAIL.
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

	for _, r := range results {
		switch r.Verdict {
		case ReviewerVerdictPass:
			ev.PassCount++
		case ReviewerVerdictFail:
			ev.FailCount++
		case ReviewerVerdictNoVerdict:
			ev.NoVerdictCount++
		case ReviewerVerdictUnavailable:
			ev.UnavailableCount++
		}
	}

	// Any explicit FAIL with concrete blockers is a hard rejection.
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
