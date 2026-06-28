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

	// EmptyDiff reports whether the reviewed range had zero changes. A
	// reviewer that returns PASS with zero findings on an empty diff produced
	// no actual review (gastown-cet.12.4: m3 PASS on the empty gtviz initial
	// commit enabled a degraded-quorum bypass). Callers should set this from
	// `git diff --name-only base...head` so the evaluator can defend against
	// degenerate PASS verdicts on empty diffs.
	EmptyDiff bool `json:"empty_diff,omitempty"`
}

// IsMergeCandidate reports whether this basis is the final merge candidate.
func (d DiffBasis) IsMergeCandidate() bool {
	return d.Kind == "merge_candidate" || d.Kind == ""
}

// IsEmptyReview reports whether a PASS verdict on this basis is a degenerate
// zero-content review (no changes between base and head). The PASS is treated
// as evidence-free because there was nothing to actually review, so it must
// not authoritatively approve the change (gastown-cet.12.4).
func (r ReviewerResult) IsEmptyReview() bool {
	return r.Verdict == ReviewerVerdictPass && r.DiffBasis.EmptyDiff
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

// CommitHistoryBasis returns a DiffBasis describing a review submitted against
// an intermediate commit of the PR, not the final merge candidate. Verdicts on
// this basis are not authoritative for the merge candidate and are reclassified
// to no-verdict audit gaps by EvaluateReviews (hq-luba).
func CommitHistoryBasis(base, head string) DiffBasis {
	return DiffBasis{Base: base, Head: head, Kind: "commit_history"}
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
	// Also reclassify empty-review PASSes as FAILs so a zero-content review
	// cannot authoritatively approve the merge (gastown-cet.12.4).
	auditGaps := []string{}
	for i := range results {
		r := &results[i]
		switch r.Verdict {
		case ReviewerVerdictPass:
			if r.IsEmptyReview() {
				// Empty-review guard: PASS on a known-empty diff is a
				// degenerate zero-content verdict, not evidence of approval.
				// Reclassify as a hard FAIL with a stable cause key so the
				// merge is blocked and the audit trail is unambiguous.
				// Do not increment PassCount — the reclassified FAIL is
				// counted in FailCount below.
				r.Verdict = ReviewerVerdictFail
				r.CauseKey = "empty_diff_degenerate_pass"
				if len(r.Blockers) == 0 {
					r.Blockers = []string{"PASS on empty diff (no content reviewed)"}
				}
				ev.FailCount++
			} else {
				ev.PassCount++
			}
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

// CoreMultiModelReviewers are the mandatory multi-model peer reviewers for the
// Gastown refinery gate. This is the source-controlled counterpart of the live
// runtime gate in gastown-spike/dropin/refinery-gate.sh: every selected core
// reviewer must return PASS before a merge may proceed. A missing/unavailable
// core reviewer DEFERS the merge (it never merges under core coverage); a parsed
// FAIL from any reviewer REJECTs.
//
// The set is intentionally a fixed four-family pool so that, even when the writer
// is unknown, no model family is arbitrarily dropped — the change is reviewed by
// every family other than (optionally) the writer's own.
var CoreMultiModelReviewers = []string{"m3", "codex", "umans-kimi", "umans-glm"}

// CoreReviewerQuorum encodes the strict multi-model refinery quorum that the
// live runtime gate (refinery-gate.sh) enforces. It is the source-controlled
// source of truth for that behavior so it is durable and testable, not just a
// property of the dropin script.
//
// Rules (mirroring the dropin exactly):
//   - Core reviewers are CoreMultiModelReviewers (m3, codex, umans-kimi, umans-glm).
//   - If Writer is a known core reviewer, it is the only reviewer excluded; the
//     merge requires ALL remaining core reviewers to return PASS
//     (peer-review:3/3).
//   - If Writer is empty/"unknown", the merge requires ALL FOUR core reviewers
//     to return PASS (peer-review:4/4).
//   - Any parsed FAIL from a reviewed reviewer REJECTs, taking precedence over
//     any unavailability (a real rejection is never masked by a peer being down).
//   - Any selected core reviewer that is UNAVAILABLE or returns NO_VERDICT
//     DEFERS the merge (non-zero, no attestation) and is recorded as an audit
//     reviewer so a follow-up re-audit bead can be filed.
//   - Opus/final-verifier review is not part of this gate until a real Opus
//     backend path is available and tested. Non-core reviewer results are ignored
//     by this core quorum mirror.
type CoreReviewerQuorum struct {
	// Writer identifies the implementer whose own review must be excluded to
	// avoid self-review. Empty or "unknown" means the writer is unknown and
	// all four core reviewers are required. Writer ids are matched against the
	// core reviewer ids case-insensitively (e.g. "codex" matches writer
	// "codex-impl").
	Writer string
}

// SelectedCoreReviewers returns the core reviewers that must PASS for the given
// writer: all four when the writer is unknown, or all except the writer when it
// names a known core reviewer. The returned slice preserves the canonical order
// of CoreMultiModelReviewers.
func (q CoreReviewerQuorum) SelectedCoreReviewers() []string {
	writer := normalizeWriter(q.Writer)
	if writer == "" || writer == "unknown" {
		out := make([]string, len(CoreMultiModelReviewers))
		copy(out, CoreMultiModelReviewers)
		return out
	}
	out := make([]string, 0, len(CoreMultiModelReviewers))
	for _, r := range CoreMultiModelReviewers {
		if strings.EqualFold(r, writer) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// ExpectedPeerCount returns the number of core reviewers whose PASS is required
// (4 for an unknown writer, 3 for a known core writer).
func (q CoreReviewerQuorum) ExpectedPeerCount() int {
	return len(q.SelectedCoreReviewers())
}

// PeerReviewPhase returns the telemetry phase string for the core panel, e.g.
// "peer-review:4/4" or "peer-review:3/3".
func (q CoreReviewerQuorum) PeerReviewPhase(passes int) string {
	return fmt.Sprintf("peer-review:%d/%d", passes, q.ExpectedPeerCount())
}

// EvaluateCoreReviewerQuorum aggregates per-reviewer results under the strict
// core multi-model quorum. It is the source-controlled mirror of the live
// runtime gate's core-review logic.
//
// results should contain the classified verdict for each reviewer that was
// consulted from the selected core reviewer set. Reviewers that were not
// consulted are simply absent, and non-core reviewers are ignored. The decision
// is:
//
//   - Any parsed FAIL from a selected core reviewer -> ReviewStateFail (REJECT).
//   - Otherwise, if every selected core reviewer PASSed -> ReviewStatePass.
//   - Any selected core reviewer UNAVAILABLE/NO_VERDICT with no FAIL ->
//     ReviewStateNoVerdict (DEFER; non-zero, no attestation) with the missing
//     reviewer(s) recorded in AuditReviewers.
//
// The returned ReviewEvaluation records PassCount/FailCount/NoVerdictCount/
// UnavailableCount for the core panel so telemetry can record expected peer
// count, peer_passes, unavailable count, and the peer-review:N/N phase.
func EvaluateCoreReviewerQuorum(results []ReviewerResult, q CoreReviewerQuorum) ReviewEvaluation {
	selected := q.SelectedCoreReviewers()
	selectedSet := make(map[string]struct{}, len(selected))
	for _, name := range selected {
		selectedSet[strings.ToLower(name)] = struct{}{}
	}
	// index verdicts by reviewer for quick lookup; last one wins if duplicated.
	byName := make(map[string]ReviewerVerdict, len(results))
	for _, r := range results {
		byName[strings.ToLower(r.Reviewer)] = r.Verdict
	}

	ev := ReviewEvaluation{
		Results: results,
	}

	// FAIL precedence: a parsed FAIL from any selected core reviewer rejects
	// immediately, even if other core reviewers were unavailable. This matches
	// the dropin's "any selected core FAIL rejects" rule.
	//
	// Empty-review guard (gastown-cet.12.4): a PASS verdict from a selected
	// core reviewer on a known-empty diff is treated as a FAIL, not a PASS.
	// Non-core reviewer results are ignored while Opus/final-verifier is
	// disabled, matching the rest of this quorum mirror.
	for _, r := range results {
		if _, ok := selectedSet[strings.ToLower(r.Reviewer)]; !ok {
			continue
		}
		if r.IsEmptyReview() {
			ev.State = ReviewStateFail
			ev.CauseKey = "empty_diff_degenerate_pass"
			ev.FailCount = 1
			ev.Error = fmt.Sprintf("core multi-model reviewer rejection: %s returned PASS on empty diff (zero findings, no content reviewed)", r.Reviewer)
			return ev
		}
	}

	var failReviewer string
	var failCause string
	for _, r := range results {
		if _, ok := selectedSet[strings.ToLower(r.Reviewer)]; !ok {
			continue
		}
		if r.Verdict == ReviewerVerdictFail {
			failReviewer = r.Reviewer
			failCause = r.CauseKey
			break
		}
	}
	if failReviewer != "" {
		ev.State = ReviewStateFail
		if failCause == "" {
			failCause = "reviewer_rejection"
		}
		ev.CauseKey = failCause
		ev.FailCount = 1
		ev.Error = fmt.Sprintf("core multi-model reviewer rejection: %s", failReviewer)
		return ev
	}

	// Evaluate the core panel: every selected reviewer must PASS.
	corePass := 0
	coreUnavailable := 0
	coreNoVerdict := 0
	var missing []string
	for _, name := range selected {
		v, ok := byName[strings.ToLower(name)]
		switch {
		case !ok || v == ReviewerVerdictNoVerdict:
			coreNoVerdict++
			missing = append(missing, name)
		case v == ReviewerVerdictUnavailable:
			coreUnavailable++
			missing = append(missing, name)
		case v == ReviewerVerdictPass:
			corePass++
		}
	}
	ev.PassCount = corePass
	ev.NoVerdictCount = coreNoVerdict
	ev.UnavailableCount = coreUnavailable

	// Any selected core reviewer that did not explicitly PASS defers the merge.
	// It must never merge on unavailable/no-verdict core coverage.
	if corePass != len(selected) {
		ev.State = ReviewStateNoVerdict
		ev.AuditReviewers = missing
		ev.Error = fmt.Sprintf("core multi-model quorum incomplete: %d/%d core PASS (missing: %s)", corePass, len(selected), strings.Join(missing, ", "))
		return ev
	}

	ev.State = ReviewStatePass
	ev.Error = fmt.Sprintf("core multi-model quorum met: %d/%d core PASS", corePass, len(selected))
	return ev
}

// normalizeWriter maps implementer-style writer ids onto the canonical core
// reviewer id space so writer-exclusion actually works. The peer pool uses ids
// m3/codex/umans-kimi/umans-glm, but an implementer may be registered as
// codex-impl / umans-kimi / umans-glm / m3. This mirrors the dropin's
// codex-impl -> codex normalization.
func normalizeWriter(writer string) string {
	w := strings.ToLower(strings.TrimSpace(writer))
	switch {
	case w == "" || w == "unknown":
		return w
	case strings.HasPrefix(w, "codex-"):
		return "codex"
	}
	return w
}
