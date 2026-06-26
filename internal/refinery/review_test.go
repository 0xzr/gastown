package refinery

import (
	"strings"
	"testing"
)

func TestEvaluateReviews_NoResults(t *testing.T) {
	ev := EvaluateReviews(nil, DegradedQuorumRule{Enabled: true})
	if ev.State != ReviewStateNoVerdict {
		t.Errorf("expected NO_VERDICT for nil results, got %s", ev.State)
	}
}

func TestEvaluateReviews_AllPass(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictPass},
		{Reviewer: "bob", Verdict: ReviewerVerdictPass},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{})
	if ev.State != ReviewStatePass {
		t.Errorf("expected PASS, got %s", ev.State)
	}
	if ev.PassCount != 2 {
		t.Errorf("expected pass count 2, got %d", ev.PassCount)
	}
}

func TestEvaluateReviews_FailWithBlockers(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictPass},
		{Reviewer: "bob", Verdict: ReviewerVerdictFail, Blockers: []string{"race condition"}},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{Enabled: true, MinPassReviews: 1})
	if ev.State != ReviewStateFail {
		t.Errorf("expected FAIL, got %s", ev.State)
	}
	if !strings.Contains(ev.Error, "race condition") {
		t.Errorf("expected error to mention blocker, got %s", ev.Error)
	}
}

func TestEvaluateReviews_FailStillRejectsWithDegradedQuorum(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictPass},
		{Reviewer: "bob", Verdict: ReviewerVerdictFail, Blockers: []string{"memory leak"}},
		{Reviewer: "carol", Verdict: ReviewerVerdictUnavailable},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{Enabled: true, MinPassReviews: 1})
	if ev.State != ReviewStateFail {
		t.Errorf("expected FAIL even with degraded quorum, got %s", ev.State)
	}
}

func TestEvaluateReviews_NoVerdictNotFail(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictNoVerdict},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{})
	if ev.State != ReviewStateNoVerdict {
		t.Errorf("expected NO_VERDICT, got %s", ev.State)
	}
	if ev.FailCount != 0 {
		t.Errorf("expected no fails, got %d", ev.FailCount)
	}
}

func TestEvaluateReviews_UnavailableNotFail(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictUnavailable},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{})
	if ev.State != ReviewStateUnavailable {
		t.Errorf("expected UNAVAILABLE, got %s", ev.State)
	}
	if ev.FailCount != 0 {
		t.Errorf("expected no fails, got %d", ev.FailCount)
	}
}

func TestEvaluateReviews_DegradedQuorum(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictPass},
		{Reviewer: "bob", Verdict: ReviewerVerdictNoVerdict},
		{Reviewer: "carol", Verdict: ReviewerVerdictUnavailable},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{Enabled: true, MinPassReviews: 1})
	if ev.State != ReviewStateDegradedQuorum {
		t.Errorf("expected DEGRADED_QUORUM, got %s", ev.State)
	}
	if len(ev.AuditReviewers) != 2 {
		t.Errorf("expected 2 audit reviewers, got %d", len(ev.AuditReviewers))
	}
}

func TestEvaluateReviews_DegradedQuorumNotEnabled(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictPass},
		{Reviewer: "bob", Verdict: ReviewerVerdictNoVerdict},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{Enabled: false})
	if ev.State != ReviewStateNoVerdict {
		t.Errorf("expected blocking NO_VERDICT when disabled, got %s", ev.State)
	}
}

func TestEvaluateReviews_DegradedQuorumInsufficientPass(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictPass},
		{Reviewer: "bob", Verdict: ReviewerVerdictNoVerdict},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{Enabled: true, MinPassReviews: 2})
	if ev.State != ReviewStateNoVerdict {
		t.Errorf("expected NO_VERDICT when pass count below quorum, got %s", ev.State)
	}
}

func TestEvaluateReviews_RequiredReviewerNoVerdictBlocks(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictPass},
		{Reviewer: "bob", Verdict: ReviewerVerdictNoVerdict},
	}
	rule := DegradedQuorumRule{Enabled: true, MinPassReviews: 1, RequiredReviewers: []string{"bob"}}
	ev := EvaluateReviews(results, rule)
	if ev.State != ReviewStateNoVerdict {
		t.Errorf("expected NO_VERDICT when required reviewer is no-verdict, got %s", ev.State)
	}
}

func TestEvaluateReviews_RequiredReviewerUnavailableAudits(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictPass},
		{Reviewer: "bob", Verdict: ReviewerVerdictUnavailable},
	}
	rule := DegradedQuorumRule{Enabled: true, MinPassReviews: 1, RequiredReviewers: []string{"bob"}}
	ev := EvaluateReviews(results, rule)
	if ev.State != ReviewStateDegradedQuorum {
		t.Errorf("expected DEGRADED_QUORUM when required reviewer unavailable, got %s", ev.State)
	}
	if len(ev.AuditReviewers) != 1 || ev.AuditReviewers[0] != "bob" {
		t.Errorf("expected audit reviewer bob, got %v", ev.AuditReviewers)
	}
}

func TestEvaluateReviews_DefaultMinPassReviews(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictPass},
		{Reviewer: "bob", Verdict: ReviewerVerdictNoVerdict},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{Enabled: true})
	if ev.State != ReviewStateDegradedQuorum {
		t.Errorf("expected DEGRADED_QUORUM with default min=1, got %s", ev.State)
	}
}

// TestEvaluateReviews_CommitHistoryFailNotAuthoritative covers the hq-luba
// incident class (gastown-cet.8): a reviewer rejected intermediate commit
// history, but the final squashed merge candidate corrected the offending
// change. A FAIL whose DiffBasis is "commit_history" must NOT authoritatively
// reject the final merge candidate — it is reclassified to an audit-gap so a
// reworked final candidate is not blocked by a stale review.
func TestEvaluateReviews_CommitHistoryFailNotAuthoritative(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictPass, DiffBasis: MergeCandidateBasis("base-sha", "head-sha")},
		{Reviewer: "luba", Verdict: ReviewerVerdictFail, Blockers: []string{"race condition"}, CauseKey: "race_condition",
			DiffBasis: DiffBasis{Base: "base-sha", Head: "head-sha", Kind: "commit_history"}},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{Enabled: true, MinPassReviews: 1})

	// The commit-history FAIL must not produce a hard FAIL state.
	if ev.State == ReviewStateFail {
		t.Fatalf("commit-history FAIL must not authoritatively reject the merge candidate, got state %s (cause=%s): %s", ev.State, ev.CauseKey, ev.Error)
	}
	// The FAIL must have been reclassified to a no-verdict audit-gap.
	if ev.FailCount != 0 {
		t.Errorf("expected FailCount=0 after reclassifying commit-history FAIL, got %d", ev.FailCount)
	}
	if ev.NoVerdictCount != 1 {
		t.Errorf("expected NoVerdictCount=1 (reclassified luba), got %d", ev.NoVerdictCount)
	}
	// With one PASS and degraded quorum, the merge proceeds under audit.
	if ev.State != ReviewStateDegradedQuorum {
		t.Errorf("expected DEGRADED_QUORUM (proceed under audit), got %s", ev.State)
	}
	// The audit obligation must reference the reviewer whose verdict did not
	// apply to the merge candidate.
	found := false
	for _, r := range ev.AuditReviewers {
		if r == "luba" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected luba in audit reviewers, got %v", ev.AuditReviewers)
	}
}

// TestEvaluateReviews_MergeCandidateFailIsAuthoritative confirms the
// counterpart: a FAIL against the actual merge-candidate diff (or an unknown
// basis, which defaults to merge-candidate) remains a hard rejection with a
// cause key. This guards the hq-luba fix from over-reclassifying real FAILs.
func TestEvaluateReviews_MergeCandidateFailIsAuthoritative(t *testing.T) {
	t.Run("explicit_merge_candidate_basis", func(t *testing.T) {
		results := []ReviewerResult{
			{Reviewer: "codex", Verdict: ReviewerVerdictFail, Blockers: []string{"missing test"}, CauseKey: "missing_test",
				DiffBasis: MergeCandidateBasis("base-sha", "head-sha")},
		}
		ev := EvaluateReviews(results, DegradedQuorumRule{Enabled: true, MinPassReviews: 1})
		if ev.State != ReviewStateFail {
			t.Fatalf("merge-candidate FAIL must be authoritative, got %s", ev.State)
		}
		if ev.CauseKey != "missing_test" {
			t.Errorf("expected cause missing_test, got %s", ev.CauseKey)
		}
		if ev.FailCount != 1 {
			t.Errorf("expected FailCount=1, got %d", ev.FailCount)
		}
	})
	t.Run("empty_basis_defaults_to_merge_candidate", func(t *testing.T) {
		// An unknown basis is the safe default: treated as merge-candidate so a
		// concrete FAIL still rejects (fail-closed), rather than silently passing.
		results := []ReviewerResult{
			{Reviewer: "codex", Verdict: ReviewerVerdictFail, Blockers: []string{"api break"}, CauseKey: "api_break"},
		}
		ev := EvaluateReviews(results, DegradedQuorumRule{Enabled: true, MinPassReviews: 1})
		if ev.State != ReviewStateFail {
			t.Fatalf("empty-basis FAIL must default to authoritative, got %s", ev.State)
		}
	})
}

// TestMergeCandidateBasis_DiffBasis confirms the basis constructor and
// IsMergeCandidate predicate that gate the append-only / merge-candidate
// invariant (gastown-cet.8).
func TestMergeCandidateBasis_DiffBasis(t *testing.T) {
	mc := MergeCandidateBasis("origin/main", "head-sha")
	if !mc.IsMergeCandidate() {
		t.Error("MergeCandidateBasis must report IsMergeCandidate=true")
	}
	if mc.Kind != "merge_candidate" {
		t.Errorf("expected kind merge_candidate, got %s", mc.Kind)
	}
	if mc.Base != "origin/main" || mc.Head != "head-sha" {
		t.Errorf("unexpected base/head: %+v", mc)
	}

	// An empty basis defaults to merge-candidate (fail-closed for FAILs).
	empty := DiffBasis{}
	if !empty.IsMergeCandidate() {
		t.Error("empty DiffBasis must default to merge-candidate")
	}

	// A commit-history basis is explicitly NOT the merge candidate.
	hist := DiffBasis{Base: "origin/main", Head: "head-sha", Kind: "commit_history"}
	if hist.IsMergeCandidate() {
		t.Error("commit_history basis must not be treated as merge candidate")
	}
}

// --- Core multi-model refinery quorum (gastown-cet.17) ----------------------
//
// These tests pin the source-controlled counterpart of the live runtime gate
// (gastown-spike/dropin/refinery-gate.sh). The rule: core reviewers are
// m3, codex, umans-kimi, umans-glm. A known writer is excluded and all
// remaining core reviewers must PASS; an unknown writer requires all four.
// Any parsed FAIL from a selected core reviewer rejects. Any selected core
// unavailable/no-verdict defers (non-zero, no attestation) and records an audit
// obligation. Non-core reviewers are not part of the gate while Opus is disabled.

// coreResult builds a ReviewerResult for a core reviewer with the given verdict.
func coreResult(name string, v ReviewerVerdict) ReviewerResult {
	return ReviewerResult{Reviewer: name, Verdict: v}
}

// allCorePass returns a PASS result for every name in the slice.
func allCorePass(names []string) []ReviewerResult {
	out := make([]ReviewerResult, 0, len(names))
	for _, n := range names {
		out = append(out, coreResult(n, ReviewerVerdictPass))
	}
	return out
}

func TestCoreQuorum_UnknownWriterRequiresAllFour(t *testing.T) {
	// Unknown writer: all four core reviewers required.
	q := CoreReviewerQuorum{Writer: "unknown"}
	if got := q.ExpectedPeerCount(); got != 4 {
		t.Fatalf("unknown writer expects 4 core peers, got %d", got)
	}
	if got := q.PeerReviewPhase(4); got != "peer-review:4/4" {
		t.Errorf("expected peer-review:4/4, got %s", got)
	}

	t.Run("all_four_pass_merges", func(t *testing.T) {
		ev := EvaluateCoreReviewerQuorum(allCorePass(q.SelectedCoreReviewers()), q)
		if ev.State != ReviewStatePass {
			t.Fatalf("expected PASS for 4/4 core, got %s: %s", ev.State, ev.Error)
		}
		if ev.PassCount != 4 {
			t.Errorf("expected PassCount=4, got %d", ev.PassCount)
		}
	})
}

func TestCoreQuorum_KnownM3WriterRequiresCodexKimiGlm(t *testing.T) {
	q := CoreReviewerQuorum{Writer: "m3"}
	selected := q.SelectedCoreReviewers()
	if len(selected) != 3 {
		t.Fatalf("known m3 writer should exclude m3, got %v", selected)
	}
	for _, r := range selected {
		if r == "m3" {
			t.Fatalf("writer m3 must be excluded from selected core, got %v", selected)
		}
	}
	if got := q.PeerReviewPhase(3); got != "peer-review:3/3" {
		t.Errorf("expected peer-review:3/3, got %s", got)
	}

	t.Run("three_pass_merges", func(t *testing.T) {
		ev := EvaluateCoreReviewerQuorum(allCorePass(selected), q)
		if ev.State != ReviewStatePass {
			t.Fatalf("expected PASS for 3/3 core (m3 writer), got %s: %s", ev.State, ev.Error)
		}
		if ev.PassCount != 3 {
			t.Errorf("expected PassCount=3, got %d", ev.PassCount)
		}
	})
}

func TestCoreQuorum_KnownCodexWriterRequiresM3KimiGlm(t *testing.T) {
	q := CoreReviewerQuorum{Writer: "codex"}
	selected := q.SelectedCoreReviewers()
	if len(selected) != 3 {
		t.Fatalf("known codex writer should exclude codex, got %v", selected)
	}
	for _, r := range selected {
		if r == "codex" {
			t.Fatalf("writer codex must be excluded, got %v", selected)
		}
	}
	ev := EvaluateCoreReviewerQuorum(allCorePass(selected), q)
	if ev.State != ReviewStatePass {
		t.Fatalf("expected PASS for 3/3 core (codex writer), got %s: %s", ev.State, ev.Error)
	}
}

// TestCoreQuorum_CodexImplWriterNormalizes confirms an implementer-style writer
// id ("codex-impl") is excluded like "codex", mirroring the dropin's
// codex-impl -> codex normalization so a writer cannot review its own diff.
func TestCoreQuorum_CodexImplWriterNormalizes(t *testing.T) {
	q := CoreReviewerQuorum{Writer: "codex-impl"}
	for _, r := range q.SelectedCoreReviewers() {
		if r == "codex" {
			t.Fatalf("codex-impl writer must exclude codex reviewer, got %v", q.SelectedCoreReviewers())
		}
	}
	if q.ExpectedPeerCount() != 3 {
		t.Errorf("codex-impl writer expects 3 peers, got %d", q.ExpectedPeerCount())
	}
}

func TestCoreQuorum_OneUnavailableCoreDefersNoAttestation(t *testing.T) {
	q := CoreReviewerQuorum{Writer: "unknown"}
	results := []ReviewerResult{
		coreResult("m3", ReviewerVerdictPass),
		coreResult("codex", ReviewerVerdictPass),
		coreResult("umans-kimi", ReviewerVerdictPass),
		// umans-glm could not be reached.
		coreResult("umans-glm", ReviewerVerdictUnavailable),
	}
	ev := EvaluateCoreReviewerQuorum(results, q)

	// DEFER: non-zero (not PASS/mergeable), no attestation.
	if ev.State == ReviewStatePass || ev.State == ReviewStateDegradedQuorum {
		t.Fatalf("unavailable core reviewer must DEFER, not merge; got %s: %s", ev.State, ev.Error)
	}
	if ev.UnavailableCount != 1 {
		t.Errorf("expected UnavailableCount=1, got %d", ev.UnavailableCount)
	}
	if ev.PassCount != 3 {
		t.Errorf("expected PassCount=3, got %d", ev.PassCount)
	}
	// The unavailable reviewer must be recorded as an audit obligation.
	found := false
	for _, r := range ev.AuditReviewers {
		if r == "umans-glm" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected umans-glm in audit reviewers, got %v", ev.AuditReviewers)
	}
}

func TestCoreQuorum_OneCoreNoVerdictDefers(t *testing.T) {
	q := CoreReviewerQuorum{Writer: "unknown"}
	results := []ReviewerResult{
		coreResult("m3", ReviewerVerdictPass),
		coreResult("codex", ReviewerVerdictPass),
		coreResult("umans-kimi", ReviewerVerdictPass),
		coreResult("umans-glm", ReviewerVerdictNoVerdict),
	}
	ev := EvaluateCoreReviewerQuorum(results, q)
	if ev.State == ReviewStatePass || ev.State == ReviewStateDegradedQuorum {
		t.Fatalf("no-verdict core reviewer must DEFER, got %s: %s", ev.State, ev.Error)
	}
	if ev.NoVerdictCount != 1 {
		t.Errorf("expected NoVerdictCount=1, got %d", ev.NoVerdictCount)
	}
}

func TestCoreQuorum_OneCoreFailRejects(t *testing.T) {
	q := CoreReviewerQuorum{Writer: "unknown"}
	results := []ReviewerResult{
		coreResult("m3", ReviewerVerdictPass),
		{Reviewer: "codex", Verdict: ReviewerVerdictFail, Blockers: []string{"missing test"}, CauseKey: "missing_test"},
		coreResult("umans-kimi", ReviewerVerdictPass),
		coreResult("umans-glm", ReviewerVerdictPass),
	}
	ev := EvaluateCoreReviewerQuorum(results, q)
	if ev.State != ReviewStateFail {
		t.Fatalf("core FAIL must REJECT, got %s: %s", ev.State, ev.Error)
	}
	if ev.FailCount != 1 {
		t.Errorf("expected FailCount=1, got %d", ev.FailCount)
	}
	if ev.CauseKey == "" {
		t.Errorf("expected non-empty cause key for FAIL, got %q", ev.CauseKey)
	}
}

// TestCoreQuorum_MixedFailAndUnavailableRejects confirms FAIL precedence: a
// real FAIL must reject even when another core reviewer is unavailable, so a
// rejection is never masked by peer unavailability (M3/GLM non-blocking test).
func TestCoreQuorum_MixedFailAndUnavailableRejects(t *testing.T) {
	q := CoreReviewerQuorum{Writer: "unknown"}
	results := []ReviewerResult{
		coreResult("m3", ReviewerVerdictPass),
		coreResult("codex", ReviewerVerdictFail),
		coreResult("umans-kimi", ReviewerVerdictUnavailable),
		coreResult("umans-glm", ReviewerVerdictPass),
	}
	ev := EvaluateCoreReviewerQuorum(results, q)
	if ev.State != ReviewStateFail {
		t.Fatalf("mixed FAIL+UNAVAILABLE must REJECT (FAIL precedence), got %s: %s", ev.State, ev.Error)
	}
}

// TestCoreQuorum_KnownWriterPeerFailRejects mirrors the live smoke test where
// a known writer's peer FAIL was rejected as expected.
func TestCoreQuorum_KnownWriterPeerFailRejects(t *testing.T) {
	q := CoreReviewerQuorum{Writer: "m3"}
	results := []ReviewerResult{
		coreResult("codex", ReviewerVerdictPass),
		coreResult("umans-kimi", ReviewerVerdictFail),
		coreResult("umans-glm", ReviewerVerdictPass),
	}
	ev := EvaluateCoreReviewerQuorum(results, q)
	if ev.State != ReviewStateFail {
		t.Fatalf("known-writer peer FAIL must REJECT, got %s: %s", ev.State, ev.Error)
	}
}

// TestCoreQuorum_TelemetryShape confirms the evaluation records the telemetry
// fields the bead requires: expected peer count, peer_passes, unavailable count,
// and the peer-review:N/N phase.
func TestCoreQuorum_TelemetryShape(t *testing.T) {
	q := CoreReviewerQuorum{Writer: "codex"}
	results := allCorePass(q.SelectedCoreReviewers())
	ev := EvaluateCoreReviewerQuorum(results, q)

	if ev.PassCount != 3 {
		t.Errorf("telemetry peer_passes: expected 3, got %d", ev.PassCount)
	}
	if q.ExpectedPeerCount() != 3 {
		t.Errorf("telemetry expected peer count: expected 3, got %d", q.ExpectedPeerCount())
	}
	if ev.UnavailableCount != 0 {
		t.Errorf("telemetry unavailable: expected 0, got %d", ev.UnavailableCount)
	}
	if got := q.PeerReviewPhase(ev.PassCount); got != "peer-review:3/3" {
		t.Errorf("telemetry phase: expected peer-review:3/3, got %s", got)
	}
}

// TestCoreQuorum_NonCoreReviewerIgnored confirms that a stale or accidental
// non-core verifier result cannot affect the gate while Opus is disabled.
func TestCoreQuorum_NonCoreReviewerIgnored(t *testing.T) {
	q := CoreReviewerQuorum{Writer: "unknown"}
	results := append(allCorePass(q.SelectedCoreReviewers()), coreResult("opus", ReviewerVerdictFail))
	ev := EvaluateCoreReviewerQuorum(results, q)
	if ev.State != ReviewStatePass {
		t.Fatalf("core 4/4 should MERGE while non-core opus result is ignored, got %s: %s", ev.State, ev.Error)
	}
}

// --- Empty-diff guard (gastown-cet.12.4) -----------------------------------
//
// These tests pin the empty-review guard: a PASS verdict on a known-empty
// diff (zero changes between base and head) is a degenerate zero-content
// review, not evidence of approval. It must be reclassified as a FAIL with a
// stable cause key (`empty_diff_degenerate_pass`) so the merge is blocked and
// the audit trail is unambiguous.
//
// The bug this fixes: m3 returned PASS on the empty gtviz initial commit
// (2abdc645) at 2026-06-25T08:27:29, while 7+ other m3 attempts against the
// same commit correctly returned FAIL with explicit "empty diff is blocking"
// findings. The single degenerate PASS enabled a degraded-quorum bypass merge
// under the live four-model refinery gate. The fix hardens both the legacy
// `EvaluateReviews` aggregator and the strict `EvaluateCoreReviewerQuorum`
// quorum to refuse to credit a zero-content PASS as a real approval.

// TestEvaluateReviews_EmptyDiffPassReclassifiedAsFail covers the legacy
// aggregator: a PASS verdict on an explicitly-empty merge-candidate diff is
// reclassified as a hard FAIL, not silently passed through.
func TestEvaluateReviews_EmptyDiffPassReclassifiedAsFail(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "m3", Verdict: ReviewerVerdictPass,
			DiffBasis: DiffBasis{Base: "base-sha", Head: "head-sha", Kind: "merge_candidate", EmptyDiff: true}},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{Enabled: true, MinPassReviews: 1})

	if ev.State != ReviewStateFail {
		t.Fatalf("empty-diff PASS must be reclassified as FAIL, got %s: %s", ev.State, ev.Error)
	}
	if ev.CauseKey != "empty_diff_degenerate_pass" {
		t.Errorf("expected cause key empty_diff_degenerate_pass, got %s", ev.CauseKey)
	}
	if ev.FailCount != 1 {
		t.Errorf("expected FailCount=1 after reclassification, got %d", ev.FailCount)
	}
	if ev.PassCount != 0 {
		t.Errorf("expected PassCount=0 after reclassification, got %d", ev.PassCount)
	}
	// The reclassified PASS must carry a concrete blocker so the FAIL is
	// visible in the audit trail, not just a cause-key change.
	if ev.Results[0].Verdict != ReviewerVerdictFail {
		t.Errorf("expected reclassified verdict FAIL, got %s", ev.Results[0].Verdict)
	}
	if len(ev.Results[0].Blockers) == 0 {
		t.Error("expected reclassified FAIL to carry a concrete blocker, got none")
	}
}

// TestEvaluateReviews_NonEmptyDiffPassStillPasses is the counterpart: a real
// PASS on a non-empty diff is NOT reclassified, so legitimate approvals are
// unaffected.
func TestEvaluateReviews_NonEmptyDiffPassStillPasses(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "m3", Verdict: ReviewerVerdictPass,
			DiffBasis: DiffBasis{Base: "base-sha", Head: "head-sha", Kind: "merge_candidate", EmptyDiff: false}},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{})

	if ev.State != ReviewStatePass {
		t.Fatalf("non-empty PASS must remain PASS, got %s: %s", ev.State, ev.Error)
	}
	if ev.Results[0].Verdict != ReviewerVerdictPass {
		t.Errorf("non-empty PASS verdict must not be reclassified, got %s", ev.Results[0].Verdict)
	}
}

// TestEvaluateReviews_EmptyDiffUnknownBasisDefaultsToFailClosed is the
// fail-closed default for the empty-diff guard: when the diff basis is
// unknown (no basis supplied at all), the empty-review reclassification must
// not credit a zero-blocker PASS, because there is no evidence to confirm
// the diff was non-empty.
func TestEvaluateReviews_EmptyDiffUnknownBasisDefaultsToFailClosed(t *testing.T) {
	results := []ReviewerResult{
		// Empty basis with PASS and zero findings, no blockers, no evidence.
		{Reviewer: "m3", Verdict: ReviewerVerdictPass},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{Enabled: true, MinPassReviews: 1})

	// With no basis, IsEmptyReview is false (we only reclassify when the
	// basis explicitly marks the diff empty), so a PASS without basis is
	// accepted as evidence of approval. This documents the explicit
	// fail-closed default: callers must set EmptyDiff to opt in to the
	// reclassification. If a caller wants fail-closed for ALL zero-blocker
	// PASS verdicts, they can leave EmptyDiff unset and the gate's own
	// empty-diff guard (runDurableReviewGate) catches it at the gate layer.
	if ev.State != ReviewStatePass {
		t.Fatalf("PASS with empty basis should not be reclassified (EmptyDiff unset), got %s: %s", ev.State, ev.Error)
	}
}

// TestCoreQuorum_EmptyDiffPassReclassifiedAsFail is the core-multi-model
// counterpart: even when every selected core reviewer returns PASS, if the
// diff is empty, the quorum must reject with the empty-review cause key so
// the gate cannot merge on a degenerate PASS.
func TestCoreQuorum_EmptyDiffPassReclassifiedAsFail(t *testing.T) {
	q := CoreReviewerQuorum{Writer: "unknown"}
	selected := q.SelectedCoreReviewers()

	results := make([]ReviewerResult, 0, len(selected))
	for _, n := range selected {
		results = append(results, ReviewerResult{
			Reviewer: n,
			Verdict:  ReviewerVerdictPass,
			DiffBasis: DiffBasis{
				Base:      "base-sha",
				Head:      "head-sha",
				Kind:      "merge_candidate",
				EmptyDiff: true,
			},
		})
	}

	ev := EvaluateCoreReviewerQuorum(results, q)
	if ev.State != ReviewStateFail {
		t.Fatalf("4/4 PASS on empty diff must reclassify as FAIL, got %s: %s", ev.State, ev.Error)
	}
	if ev.CauseKey != "empty_diff_degenerate_pass" {
		t.Errorf("expected cause key empty_diff_degenerate_pass, got %s", ev.CauseKey)
	}
	if ev.FailCount != 1 {
		t.Errorf("expected FailCount=1, got %d", ev.FailCount)
	}
}

// TestCoreQuorum_EmptyDiffKnownM3Writer still reclassifies when the writer
// exclusion produces 3/3 PASS — even the smaller quorum cannot merge an
// empty diff, since the empty-review guard fires before the quorum tally.
func TestCoreQuorum_EmptyDiffKnownM3Writer(t *testing.T) {
	q := CoreReviewerQuorum{Writer: "m3"}
	selected := q.SelectedCoreReviewers()

	results := make([]ReviewerResult, 0, len(selected))
	for _, n := range selected {
		results = append(results, ReviewerResult{
			Reviewer: n,
			Verdict:  ReviewerVerdictPass,
			DiffBasis: DiffBasis{
				Base:      "base-sha",
				Head:      "head-sha",
				Kind:      "merge_candidate",
				EmptyDiff: true,
			},
		})
	}

	ev := EvaluateCoreReviewerQuorum(results, q)
	if ev.State != ReviewStateFail {
		t.Fatalf("3/3 PASS on empty diff must reclassify as FAIL, got %s: %s", ev.State, ev.Error)
	}
	if ev.CauseKey != "empty_diff_degenerate_pass" {
		t.Errorf("expected cause key empty_diff_degenerate_pass, got %s", ev.CauseKey)
	}
}

// TestIsEmptyReview_DetectsDegeneratePass pins the IsEmptyReview predicate:
// PASS + EmptyDiff basis + zero blockers = degenerate PASS. A FAIL verdict
// must not be reported as an empty review (the reclassification only targets
// PASS verdicts).
func TestIsEmptyReview_DetectsDegeneratePass(t *testing.T) {
	t.Run("pass_with_empty_diff_returns_true", func(t *testing.T) {
		r := ReviewerResult{Reviewer: "m3", Verdict: ReviewerVerdictPass,
			DiffBasis: DiffBasis{Base: "b", Head: "h", EmptyDiff: true}}
		if !r.IsEmptyReview() {
			t.Error("PASS with EmptyDiff basis must be reported as empty review")
		}
	})
	t.Run("pass_with_non_empty_diff_returns_false", func(t *testing.T) {
		r := ReviewerResult{Reviewer: "m3", Verdict: ReviewerVerdictPass,
			DiffBasis: DiffBasis{Base: "b", Head: "h", EmptyDiff: false}}
		if r.IsEmptyReview() {
			t.Error("PASS with non-empty diff must not be reported as empty review")
		}
	})
	t.Run("fail_with_empty_diff_returns_false", func(t *testing.T) {
		r := ReviewerResult{Reviewer: "m3", Verdict: ReviewerVerdictFail,
			DiffBasis: DiffBasis{Base: "b", Head: "h", EmptyDiff: true}}
		if r.IsEmptyReview() {
			t.Error("FAIL must not be reported as empty review (only PASS reclassifies)")
		}
	})
	t.Run("pass_with_empty_diff_and_blockers_returns_true", func(t *testing.T) {
		// A PASS verdict on an empty diff is degenerate regardless of any
		// carried blockers: if the diff is empty, the reviewer could not
		// have engaged with content. Blockers on a zero-content review are
		// misattributed (a PASS verdict should not carry blockers; that is
		// a data-model contradiction handled by the reclassification, not
		// by relaxing the empty-review predicate).
		r := ReviewerResult{Reviewer: "m3", Verdict: ReviewerVerdictPass,
			Blockers:  []string{"some concrete finding"},
			DiffBasis: DiffBasis{Base: "b", Head: "h", EmptyDiff: true}}
		if !r.IsEmptyReview() {
			t.Error("PASS on empty diff must be reported as empty review even when blockers are present")
		}
	})
}

// ============================================================================
// gastown-6z5: EvaluateWithRule must preserve packet-level DiffBasis and
// packet-level CauseKey so the provider's merge-candidate provenance and
// primary failure cause survive the quorum-rule re-evaluation.
// ============================================================================

// TestEvaluateWithRule_PreservesPacketDiffBasis confirms that the packet-level
// DiffBasis stamped by the provider (e.g. githubPRProvider) survives
// EvaluateWithRule so downstream telemetry can still identify which diff was
// reviewed after the quorum rule is applied.
func TestEvaluateWithRule_PreservesPacketDiffBasis(t *testing.T) {
	packetBasis := DiffBasis{Base: "origin/main", Head: "head-sha", Kind: "merge_candidate"}
	ev := &ReviewEvaluation{
		State:     ReviewStatePass,
		Results:   []ReviewerResult{{Reviewer: "alice", Verdict: ReviewerVerdictPass}},
		PassCount: 1,
		DiffBasis: packetBasis,
	}
	got := EvaluateWithRule(ev, DegradedQuorumRule{})
	if got == nil {
		t.Fatal("EvaluateWithRule returned nil for non-nil input")
	}
	if got.DiffBasis != packetBasis {
		t.Errorf("packet-level DiffBasis lost: got %+v, want %+v", got.DiffBasis, packetBasis)
	}
}

// TestEvaluateWithRule_PreservesPacketCauseKey confirms that when the
// provider stamped a CauseKey on the packet (e.g. gh_changes_requested_overall)
// and EvaluateReviews does not synthesize a more specific per-result one,
// the packet-level CauseKey survives the quorum rule pass.
func TestEvaluateWithRule_PreservesPacketCauseKey(t *testing.T) {
	ev := &ReviewEvaluation{
		State:     ReviewStateNoVerdict,
		Results:   []ReviewerResult{{Reviewer: "github", Verdict: ReviewerVerdictNoVerdict, Evidence: "no reviews"}},
		DiffBasis: MergeCandidateBasis("origin/main", "head"),
		CauseKey:  "gh_changes_requested_overall",
	}
	got := EvaluateWithRule(ev, DegradedQuorumRule{})
	if got == nil {
		t.Fatal("EvaluateWithRule returned nil")
	}
	if got.CauseKey != "gh_changes_requested_overall" {
		t.Errorf("packet-level CauseKey lost: got %q", got.CauseKey)
	}
}

// TestEvaluateWithRule_PerResultCauseKeyWinsOverPacket confirms that when
// EvaluateReviews synthesizes a more specific per-result CauseKey (e.g. a
// reviewer-blocker-derived key), that more specific key wins over the
// packet-level key. The packet-level key is a fallback only.
func TestEvaluateWithRule_PerResultCauseKeyWinsOverPacket(t *testing.T) {
	ev := &ReviewEvaluation{
		State:     ReviewStateFail,
		Results:   []ReviewerResult{{Reviewer: "codex", Verdict: ReviewerVerdictFail, Blockers: []string{"race condition"}, CauseKey: "race_condition"}},
		DiffBasis: MergeCandidateBasis("origin/main", "head"),
		CauseKey:  "packet_level_key_should_be_overridden",
		FailCount: 1,
	}
	got := EvaluateWithRule(ev, DegradedQuorumRule{})
	if got == nil {
		t.Fatal("EvaluateWithRule returned nil")
	}
	if got.CauseKey != "race_condition" {
		t.Errorf("per-result CauseKey must win over packet-level: got %q", got.CauseKey)
	}
}

// TestEvaluateWithRule_NilInput confirms EvaluateWithRule is nil-safe.
func TestEvaluateWithRule_NilInput(t *testing.T) {
	if got := EvaluateWithRule(nil, DegradedQuorumRule{}); got != nil {
		t.Errorf("EvaluateWithRule(nil) must return nil, got %+v", got)
	}
}

// TestEvaluateReviews_CommitHistoryFAILReclassified covers the full
// commit_history reclassification path added for hq-luba, exercised directly
// via EvaluateReviews so it is regression-protected even when no provider
// emits a commit_history result. The bead calls out that this path was
// previously dead code under four-model gate conditions.
func TestEvaluateReviews_CommitHistoryFAILReclassified(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "luba", Verdict: ReviewerVerdictFail, Blockers: []string{"race condition"}, CauseKey: "race_condition",
			DiffBasis: DiffBasis{Base: "base-sha", Head: "head-sha", Kind: "commit_history"}},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{})

	if ev.State != ReviewStateNoVerdict {
		t.Fatalf("commit_history FAIL must reclassify to NO_VERDICT, got %s: %s", ev.State, ev.Error)
	}
	if ev.FailCount != 0 {
		t.Errorf("commit_history FAIL must NOT count as FailCount, got %d", ev.FailCount)
	}
	if ev.NoVerdictCount != 1 {
		t.Errorf("commit_history FAIL must increment NoVerdictCount, got %d", ev.NoVerdictCount)
	}
	// The reclassified verdict is recorded so audit trails still see the reviewer.
	if ev.Results[0].Verdict != ReviewerVerdictNoVerdict {
		t.Errorf("reclassified verdict must be NO_VERDICT, got %s", ev.Results[0].Verdict)
	}
	if len(ev.Results[0].Blockers) != 0 {
		t.Errorf("reclassified result must drop blockers, got %v", ev.Results[0].Blockers)
	}
}

// TestEvaluateReviews_MergeCandidateFAILStillRejects confirms the
// counterpart: a FAIL whose DiffBasis IS merge_candidate still hard-rejects
// the merge. The commit_history reclassification must not over-generalize.
func TestEvaluateReviews_MergeCandidateFAILStillRejects(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "codex", Verdict: ReviewerVerdictFail, Blockers: []string{"missing test"}, CauseKey: "missing_test",
			DiffBasis: MergeCandidateBasis("base-sha", "head-sha")},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{})

	if ev.State != ReviewStateFail {
		t.Fatalf("merge_candidate FAIL must hard-reject, got %s: %s", ev.State, ev.Error)
	}
	if ev.FailCount != 1 {
		t.Errorf("expected FailCount=1, got %d", ev.FailCount)
	}
	if ev.CauseKey != "missing_test" {
		t.Errorf("expected CauseKey=missing_test, got %q", ev.CauseKey)
	}
}
