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
