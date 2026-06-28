package mrtelemetry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRecordMRAttempt_AppendsAndComputesAttemptNumber verifies that
// RecordMRAttempt appends a row and that attempt_number increments per
// (rig, source_bead) pair across multiple submissions.
func TestRecordMRAttempt_AppendsAndComputesAttemptNumber(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	store := NewStore(path)
	ctx := context.Background()

	// First attempt for source_bead A in rig gastown: attempt_number=1.
	r1, err := store.RecordMRAttempt(ctx, RecordMRAttemptInput{
		Rig:         "gastown",
		SourceBead:  "gt-aaa",
		MRID:        "gt-mr1",
		Polecat:     "quartz",
		WriterModel: "umans-kimi",
		Branch:      "polecat/quartz/gt-aaa",
		CommitSHA:   "deadbeef1",
		TreeSHA:     "feedface1",
		SubmittedAt: time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("RecordMRAttempt #1: %v", err)
	}
	if r1.AttemptNumber != 1 {
		t.Errorf("first attempt: AttemptNumber=%d, want 1", r1.AttemptNumber)
	}
	if r1.RecordID == "" {
		t.Errorf("first attempt: RecordID is empty")
	}

	// Second attempt for same source_bead: attempt_number=2.
	r2, err := store.RecordMRAttempt(ctx, RecordMRAttemptInput{
		Rig:         "gastown",
		SourceBead:  "gt-aaa",
		MRID:        "gt-mr2",
		Polecat:     "onyx",
		WriterModel: "umans-glm",
		Branch:      "polecat/onyx/gt-aaa",
		CommitSHA:   "deadbeef2",
		TreeSHA:     "feedface2",
		SubmittedAt: time.Date(2026, 6, 28, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("RecordMRAttempt #2: %v", err)
	}
	if r2.AttemptNumber != 2 {
		t.Errorf("second attempt: AttemptNumber=%d, want 2", r2.AttemptNumber)
	}

	// Third attempt for different source_bead: attempt_number=1.
	r3, err := store.RecordMRAttempt(ctx, RecordMRAttemptInput{
		Rig:         "gastown",
		SourceBead:  "gt-bbb",
		MRID:        "gt-mr3",
		Polecat:     "jasper",
		WriterModel: "m3",
		Branch:      "polecat/jasper/gt-bbb",
		CommitSHA:   "deadbeef3",
		TreeSHA:     "feedface3",
		SubmittedAt: time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("RecordMRAttempt #3: %v", err)
	}
	if r3.AttemptNumber != 1 {
		t.Errorf("new source_bead: AttemptNumber=%d, want 1", r3.AttemptNumber)
	}

	// Verify all 3 records are persisted.
	all, err := store.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ListAll: len=%d, want 3", len(all))
	}
}

// TestRecordMRAttempt_AttemptNumberAcrossRigs verifies that attempt_number
// is scoped per-rig, not global. A second rig submitting against the same
// source_bead starts at 1.
func TestRecordMRAttempt_AttemptNumberAcrossRigs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	store := NewStore(path)
	ctx := context.Background()

	r1, err := store.RecordMRAttempt(ctx, RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-shared", MRID: "g-mr1",
		WriterModel: "umans-kimi", SubmittedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("gastown attempt: %v", err)
	}
	if r1.AttemptNumber != 1 {
		t.Errorf("gastown first: AttemptNumber=%d, want 1", r1.AttemptNumber)
	}

	r2, err := store.RecordMRAttempt(ctx, RecordMRAttemptInput{
		Rig: "longeye", SourceBead: "gt-shared", MRID: "l-mr1",
		WriterModel: "umans-glm", SubmittedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("longeye attempt: %v", err)
	}
	if r2.AttemptNumber != 1 {
		t.Errorf("longeye first: AttemptNumber=%d, want 1 (per-rig scoping)", r2.AttemptNumber)
	}
}

// TestRecordMRAttempt_WriterAttributionAcrossReassignment verifies that
// writer_model is preserved per MR attempt even when the source bead is
// reassigned to a different model between attempts. Each attempt records
// the model that actually authored the MR, not the current assignment.
func TestRecordMRAttempt_WriterAttributionAcrossReassignment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	store := NewStore(path)
	ctx := context.Background()
	base := time.Date(2026, 6, 28, 9, 0, 0, 0, time.UTC)

	// Attempt 1: kimi submits, codex rejects.
	a1, err := store.RecordMRAttempt(ctx, RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-xyz", MRID: "gt-mr-1",
		Polecat: "quartz", WriterModel: "umans-kimi",
		WriterModelSource: "agent_bead",
		Branch:            "polecat/quartz/gt-xyz",
		CommitSHA:         "sha-1", TreeSHA: "tree-1",
		SubmittedAt: base,
	})
	if err != nil {
		t.Fatalf("attempt 1: %v", err)
	}
	if err := store.RecordCodexReview(ctx, a1.MRID,
		base.Add(2*time.Minute), base.Add(7*time.Minute),
		"FAIL", []ReviewerResult{{Reviewer: "codex", Verdict: "FAIL", CauseKey: "race_condition"}}); err != nil {
		t.Fatalf("RecordCodexReview 1: %v", err)
	}
	if err := store.RecordFinalOutcome(ctx, a1.MRID, "rejected",
		"implementation_quality", time.Time{}, base.Add(8*time.Minute),
		"", ""); err != nil {
		t.Fatalf("RecordFinalOutcome 1: %v", err)
	}

	// Source bead is now reassigned to GLM. The rework attempt is authored
	// by GLM, even though the original bead assignment was kimi.
	a2, err := store.RecordMRAttempt(ctx, RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-xyz", MRID: "gt-mr-2",
		Polecat: "onyx", WriterModel: "umans-glm",
		WriterModelSource: "agent_bead",
		Branch:            "polecat/onyx/gt-xyz",
		CommitSHA:         "sha-2", TreeSHA: "tree-2",
		SubmittedAt:      base.Add(15 * time.Minute),
		ReworkPacketPath: "/tmp/rework-packet.json",
	})
	if err != nil {
		t.Fatalf("attempt 2: %v", err)
	}
	if a2.AttemptNumber != 2 {
		t.Errorf("rework: AttemptNumber=%d, want 2", a2.AttemptNumber)
	}
	if a2.WriterModel != "umans-glm" {
		t.Errorf("rework writer_model=%q, want umans-glm (actual MR author, not original assignee)",
			a2.WriterModel)
	}

	// Verify both records retain their own writer attribution.
	rec1, err := store.GetByMRID("gt-mr-1")
	if err != nil {
		t.Fatalf("GetByMRID 1: %v", err)
	}
	if rec1.WriterModel != "umans-kimi" {
		t.Errorf("attempt 1 writer_model changed to %q", rec1.WriterModel)
	}
	if rec1.CodexVerdict != "FAIL" {
		t.Errorf("attempt 1 CodexVerdict=%q, want FAIL", rec1.CodexVerdict)
	}
	if rec1.FailureClass != "implementation_quality" {
		t.Errorf("attempt 1 FailureClass=%q, want implementation_quality", rec1.FailureClass)
	}

	rec2, err := store.GetByMRID("gt-mr-2")
	if err != nil {
		t.Fatalf("GetByMRID 2: %v", err)
	}
	if rec2.WriterModel != "umans-glm" {
		t.Errorf("attempt 2 writer_model=%q, want umans-glm", rec2.WriterModel)
	}
	if rec2.ReworkPacketPath != "/tmp/rework-packet.json" {
		t.Errorf("attempt 2 ReworkPacketPath=%q, want /tmp/rework-packet.json",
			rec2.ReworkPacketPath)
	}
}

// TestRecordRefineryStartedAndValidation verifies that the lifecycle
// timestamps and validation outcomes are recorded correctly.
func TestRecordRefineryStartedAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	store := NewStore(path)
	ctx := context.Background()
	base := time.Date(2026, 6, 28, 9, 0, 0, 0, time.UTC)

	if _, err := store.RecordMRAttempt(ctx, RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-vv", MRID: "gt-mr-v",
		WriterModel: "umans-kimi", SubmittedAt: base,
	}); err != nil {
		t.Fatalf("RecordMRAttempt: %v", err)
	}

	if err := store.RecordRefineryStarted(ctx, "gt-mr-v", base.Add(time.Minute)); err != nil {
		t.Fatalf("RecordRefineryStarted: %v", err)
	}
	if err := store.RecordValidation(ctx, "gt-mr-v",
		base.Add(2*time.Minute), base.Add(5*time.Minute), true); err != nil {
		t.Fatalf("RecordValidation: %v", err)
	}

	rec, err := store.GetByMRID("gt-mr-v")
	if err != nil {
		t.Fatalf("GetByMRID: %v", err)
	}
	if !rec.RefineryStartedAt.Equal(base.Add(time.Minute)) {
		t.Errorf("RefineryStartedAt=%v, want %v", rec.RefineryStartedAt, base.Add(time.Minute))
	}
	if !rec.ValidationPassed {
		t.Errorf("ValidationPassed=false, want true")
	}
}

// TestRecordCodexReview_ComputesDurations verifies that the elapsed
// submit-to-Codex-verdict and refinery-to-Codex-verdict durations are
// recomputed when the review finishes.
func TestRecordCodexReview_ComputesDurations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	store := NewStore(path)
	ctx := context.Background()
	submitted := time.Date(2026, 6, 28, 9, 0, 0, 0, time.UTC)
	refineryStarted := submitted.Add(time.Minute)
	codexStarted := refineryStarted.Add(time.Minute)
	codexFinished := codexStarted.Add(4 * time.Minute)

	if _, err := store.RecordMRAttempt(ctx, RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-tt", MRID: "gt-mr-t",
		WriterModel: "umans-kimi", SubmittedAt: submitted,
	}); err != nil {
		t.Fatalf("RecordMRAttempt: %v", err)
	}
	if err := store.RecordRefineryStarted(ctx, "gt-mr-t", refineryStarted); err != nil {
		t.Fatalf("RecordRefineryStarted: %v", err)
	}
	if err := store.RecordCodexReview(ctx, "gt-mr-t",
		codexStarted, codexFinished, "PASS",
		[]ReviewerResult{{Reviewer: "codex", Verdict: "PASS"}}); err != nil {
		t.Fatalf("RecordCodexReview: %v", err)
	}

	rec, err := store.GetByMRID("gt-mr-t")
	if err != nil {
		t.Fatalf("GetByMRID: %v", err)
	}
	// submitted to codexFinished: 1m + 1m + 4m = 6m
	if want := int64(6 * 60 * 1000); rec.SubmitToCodexVerdictMS != want {
		t.Errorf("SubmitToCodexVerdictMS=%d, want %d", rec.SubmitToCodexVerdictMS, want)
	}
	// refineryStarted to codexFinished: 1m + 4m = 5m
	if want := int64(5 * 60 * 1000); rec.RefineryToCodexVerdictMS != want {
		t.Errorf("RefineryToCodexVerdictMS=%d, want %d", rec.RefineryToCodexVerdictMS, want)
	}
}

// TestRecordFinalOutcome verifies merged/rejected terminal state recording.
func TestRecordFinalOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	store := NewStore(path)
	ctx := context.Background()

	if _, err := store.RecordMRAttempt(ctx, RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-ff", MRID: "gt-mr-f",
		WriterModel: "umans-kimi", SubmittedAt: time.Now(),
	}); err != nil {
		t.Fatalf("RecordMRAttempt: %v", err)
	}
	mergedAt := time.Date(2026, 6, 28, 10, 5, 0, 0, time.UTC)
	if err := store.RecordFinalOutcome(ctx, "gt-mr-f",
		"merged", "", mergedAt, time.Time{}, "merge-sha", "publish-sha"); err != nil {
		t.Fatalf("RecordFinalOutcome: %v", err)
	}

	rec, err := store.GetByMRID("gt-mr-f")
	if err != nil {
		t.Fatalf("GetByMRID: %v", err)
	}
	if rec.FinalGateDecision != "merged" {
		t.Errorf("FinalGateDecision=%q, want merged", rec.FinalGateDecision)
	}
	if rec.MergeCommitSHA != "merge-sha" {
		t.Errorf("MergeCommitSHA=%q, want merge-sha", rec.MergeCommitSHA)
	}
	if rec.PublishedCommitSHA != "publish-sha" {
		t.Errorf("PublishedCommitSHA=%q, want publish-sha", rec.PublishedCommitSHA)
	}
}

// TestUpdateByMRID_MissingMRID verifies that an update for an unknown MR
// returns an error rather than silently no-op'ing.
func TestUpdateByMRID_MissingMRID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	store := NewStore(path)
	err := store.UpdateByMRID("nope", func(a *MRAttempt) {})
	if err == nil {
		t.Errorf("UpdateByMRID(nope) returned nil error, want one")
	}
}

// TestStore_RoundTripJSONL verifies that the on-disk format is JSONL with
// one record per line and that we round-trip every field through the wire.
func TestStore_RoundTripJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "telemetry.jsonl")
	store := NewStore(path)
	ctx := context.Background()

	submitted := time.Date(2026, 6, 28, 9, 0, 0, 0, time.UTC)
	if _, err := store.RecordMRAttempt(ctx, RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-rt", MRID: "gt-mr-rt",
		Polecat: "quartz", WriterModel: "umans-glm",
		WriterModelSource: "agent_bead",
		Branch:            "polecat/quartz/gt-rt",
		CommitSHA:         "rt-sha", TreeSHA: "rt-tree",
		SubmittedAt: submitted,
		RawLogPath:  "/var/log/refinery/gt-mr-rt.log",
	}); err != nil {
		t.Fatalf("RecordMRAttempt: %v", err)
	}

	// File must contain exactly one non-empty JSON line.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got := countLines(string(data)); got != 1 {
		t.Fatalf("file lines=%d, want 1", got)
	}
	var got MRAttempt
	if err := json.Unmarshal([]byte(trimFirstLine(string(data))), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Polecat != "quartz" || got.WriterModel != "umans-glm" || got.RawLogPath == "" {
		t.Errorf("round-trip lost fields: %+v", got)
	}

	// Reopen the store and confirm it can read what it wrote.
	store2 := NewStore(path)
	all, err := store2.ListAll()
	if err != nil {
		t.Fatalf("ListAll after reopen: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("reopen ListAll: len=%d, want 1", len(all))
	}
}

// TestReport_FirstPassAndRework verifies that Report correctly separates
// first-pass Codex passes from rework passes, and counts reworks as
// attempts > 1.
func TestReport_FirstPassAndRework(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	store := NewStore(path)
	ctx := context.Background()
	base := time.Date(2026, 6, 28, 8, 0, 0, 0, time.UTC)

	// kimi: 2 first-pass attempts, 1 PASS, 1 FAIL; 1 rework (after FAIL) PASSes.
	// a1: kimi first-pass PASS
	a1, _ := store.RecordMRAttempt(ctx, RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-k1", MRID: "k-mr-1",
		WriterModel: "umans-kimi", SubmittedAt: base,
	})
	_ = store.RecordCodexReview(ctx, a1.MRID, base.Add(time.Minute), base.Add(3*time.Minute),
		"PASS", []ReviewerResult{{Reviewer: "codex", Verdict: "PASS"}})
	_ = store.RecordFinalOutcome(ctx, a1.MRID, "merged", "", base.Add(10*time.Minute), time.Time{}, "m1", "")

	// a2: kimi first-pass FAIL
	a2, _ := store.RecordMRAttempt(ctx, RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-k2", MRID: "k-mr-2",
		WriterModel: "umans-kimi", SubmittedAt: base.Add(time.Hour),
	})
	_ = store.RecordCodexReview(ctx, a2.MRID, base.Add(time.Hour).Add(time.Minute),
		base.Add(time.Hour).Add(2*time.Minute),
		"FAIL", []ReviewerResult{{Reviewer: "codex", Verdict: "FAIL"}})
	_ = store.RecordFinalOutcome(ctx, a2.MRID, "rejected", "implementation_quality",
		time.Time{}, base.Add(time.Hour).Add(3*time.Minute), "", "")

	// a3: kimi rework PASS for gt-k2
	a3, _ := store.RecordMRAttempt(ctx, RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-k2", MRID: "k-mr-3",
		WriterModel: "umans-kimi", SubmittedAt: base.Add(2 * time.Hour),
	})
	_ = store.RecordCodexReview(ctx, a3.MRID, base.Add(2*time.Hour).Add(time.Minute),
		base.Add(2*time.Hour).Add(3*time.Minute),
		"PASS", []ReviewerResult{{Reviewer: "codex", Verdict: "PASS"}})
	_ = store.RecordFinalOutcome(ctx, a3.MRID, "merged", "", base.Add(2*time.Hour).Add(10*time.Minute), time.Time{}, "m3", "")

	// glm: 1 first-pass FAIL
	a4, _ := store.RecordMRAttempt(ctx, RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-g1", MRID: "g-mr-1",
		WriterModel: "umans-glm", SubmittedAt: base.Add(3 * time.Hour),
	})
	_ = store.RecordCodexReview(ctx, a4.MRID, base.Add(3*time.Hour).Add(time.Minute),
		base.Add(3*time.Hour).Add(4*time.Minute),
		"FAIL", []ReviewerResult{{Reviewer: "codex", Verdict: "FAIL"}})
	_ = store.RecordFinalOutcome(ctx, a4.MRID, "rejected", "implementation_quality",
		time.Time{}, base.Add(3*time.Hour).Add(5*time.Minute), "", "")

	// m3: 1 first-pass UNAVAILABLE (reviewer unavailable, excluded infra)
	a5, _ := store.RecordMRAttempt(ctx, RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-m1", MRID: "m-mr-1",
		WriterModel: "m3", SubmittedAt: base.Add(4 * time.Hour),
	})
	_ = store.RecordCodexReview(ctx, a5.MRID, base.Add(4*time.Hour).Add(time.Minute),
		base.Add(4*time.Hour).Add(2*time.Minute),
		"UNAVAILABLE", []ReviewerResult{{Reviewer: "codex", Verdict: "UNAVAILABLE"}})
	_ = store.RecordFinalOutcome(ctx, a5.MRID, "rejected", "reviewer_unavailable",
		time.Time{}, base.Add(4*time.Hour).Add(3*time.Minute), "", "")

	report, err := store.Report(ctx, ReportOptions{})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}

	kimi := report.ByModel["umans-kimi"]
	if kimi == nil {
		t.Fatalf("missing kimi summary: %+v", report.ByModel)
	}
	if kimi.TotalAttempts != 3 {
		t.Errorf("kimi TotalAttempts=%d, want 3", kimi.TotalAttempts)
	}
	if kimi.FirstPassCodexPassCount != 1 {
		t.Errorf("kimi FirstPassCodexPassCount=%d, want 1 (only a1 is first-pass PASS)",
			kimi.FirstPassCodexPassCount)
	}
	if kimi.FinalMergeCount != 2 {
		t.Errorf("kimi FinalMergeCount=%d, want 2 (a1 + a3)", kimi.FinalMergeCount)
	}
	if kimi.RejectionCount != 1 {
		t.Errorf("kimi RejectionCount=%d, want 1 (a2 rejected before rework)",
			kimi.RejectionCount)
	}
	if kimi.ReworkCount != 1 {
		t.Errorf("kimi ReworkCount=%d, want 1 (a3 is rework)", kimi.ReworkCount)
	}
	if kimi.ExcludedInfraUnavailableCount != 0 {
		t.Errorf("kimi ExcludedInfraUnavailableCount=%d, want 0", kimi.ExcludedInfraUnavailableCount)
	}

	glm := report.ByModel["umans-glm"]
	if glm == nil {
		t.Fatalf("missing glm summary")
	}
	if glm.TotalAttempts != 1 || glm.FirstPassCodexPassCount != 0 || glm.FinalMergeCount != 0 {
		t.Errorf("glm summary wrong: %+v", glm)
	}

	m3Sum := report.ByModel["m3"]
	if m3Sum == nil {
		t.Fatalf("missing m3 summary")
	}
	if m3Sum.ExcludedInfraUnavailableCount != 1 {
		t.Errorf("m3 ExcludedInfraUnavailableCount=%d, want 1", m3Sum.ExcludedInfraUnavailableCount)
	}

	// Totals across all models.
	if report.Totals.TotalAttempts != 5 {
		t.Errorf("totals TotalAttempts=%d, want 5", report.Totals.TotalAttempts)
	}
	if report.Totals.FinalMergeCount != 2 {
		t.Errorf("totals FinalMergeCount=%d, want 2", report.Totals.FinalMergeCount)
	}
	if report.Totals.ReworkCount != 1 {
		t.Errorf("totals ReworkCount=%d, want 1", report.Totals.ReworkCount)
	}
}

// TestReport_TimeToVerdictMedianAndP95 verifies that median and p95 of
// submit-to-Codex-verdict are computed correctly across attempts.
func TestReport_TimeToVerdictMedianAndP95(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	store := NewStore(path)
	ctx := context.Background()
	base := time.Date(2026, 6, 28, 9, 0, 0, 0, time.UTC)

	// Five attempts with submit->codex durations of 1s..5s.
	durations := []time.Duration{1, 2, 3, 4, 5}
	for i, d := range durations {
		submitted := base.Add(time.Duration(i) * time.Hour)
		finished := submitted.Add(d * time.Second)
		mrID := "mr-d-" + itoa(i)
		_, err := store.RecordMRAttempt(ctx, RecordMRAttemptInput{
			Rig: "gastown", SourceBead: "gd-" + itoa(i), MRID: mrID,
			WriterModel: "umans-kimi", SubmittedAt: submitted,
		})
		if err != nil {
			t.Fatalf("RecordMRAttempt[%d]: %v", i, err)
		}
		if err := store.RecordCodexReview(ctx, mrID, finished.Add(-time.Millisecond), finished,
			"PASS", []ReviewerResult{{Reviewer: "codex", Verdict: "PASS"}}); err != nil {
			t.Fatalf("RecordCodexReview[%d]: %v", i, err)
		}
	}

	report, err := store.Report(ctx, ReportOptions{})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	kimi := report.ByModel["umans-kimi"]
	if kimi == nil {
		t.Fatalf("missing kimi summary")
	}
	// Sorted: 1000, 2000, 3000, 4000, 5000 ms. Median = 3000 ms (5-element slice).
	if want := int64(3000); kimi.MedianSubmitToCodexVerdictMS != want {
		t.Errorf("MedianSubmitToCodexVerdictMS=%d, want %d", kimi.MedianSubmitToCodexVerdictMS, want)
	}
	// Nearest-rank p95 for n=5: rank = ceil(95/100 * 5) = ceil(4.75) = 5; element = sorted[4] = 5000.
	if want := int64(5000); kimi.P95SubmitToCodexVerdictMS != want {
		t.Errorf("P95SubmitToCodexVerdictMS=%d, want %d", kimi.P95SubmitToCodexVerdictMS, want)
	}
}

// TestReport_FilterByModel verifies that WriterModels filter restricts the
// report to the named models only.
func TestReport_FilterByModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	store := NewStore(path)
	ctx := context.Background()

	for _, model := range []string{"umans-kimi", "umans-glm", "m3"} {
		_, err := store.RecordMRAttempt(ctx, RecordMRAttemptInput{
			Rig: "gastown", SourceBead: "gf-" + model, MRID: "gf-" + model,
			WriterModel: model, SubmittedAt: time.Now(),
		})
		if err != nil {
			t.Fatalf("RecordMRAttempt %s: %v", model, err)
		}
	}

	report, err := store.Report(ctx, ReportOptions{WriterModels: []string{"umans-kimi"}})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if _, ok := report.ByModel["umans-glm"]; ok {
		t.Errorf("filter should exclude umans-glm")
	}
	if _, ok := report.ByModel["m3"]; ok {
		t.Errorf("filter should exclude m3")
	}
	if _, ok := report.ByModel["umans-kimi"]; !ok {
		t.Errorf("filter should include umans-kimi")
	}
	if report.Totals.TotalAttempts != 1 {
		t.Errorf("filtered totals TotalAttempts=%d, want 1", report.Totals.TotalAttempts)
	}
}

// TestReport_FilterBySince verifies that Since filter restricts records
// to those submitted at or after the cutoff.
func TestReport_FilterBySince(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	store := NewStore(path)
	ctx := context.Background()

	t0 := time.Date(2026, 6, 28, 9, 0, 0, 0, time.UTC)
	for i, when := range []time.Time{t0, t0.Add(time.Hour), t0.Add(2 * time.Hour)} {
		_, err := store.RecordMRAttempt(ctx, RecordMRAttemptInput{
			Rig: "gastown", SourceBead: "gs-" + itoa(i), MRID: "gs-" + itoa(i),
			WriterModel: "umans-kimi", SubmittedAt: when,
		})
		if err != nil {
			t.Fatalf("RecordMRAttempt: %v", err)
		}
	}

	report, err := store.Report(ctx, ReportOptions{Since: t0.Add(time.Hour)})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if report.Totals.TotalAttempts != 2 {
		t.Errorf("Since filter: TotalAttempts=%d, want 2", report.Totals.TotalAttempts)
	}
}

// TestIsExcludedInfra verifies the classification of failure classes
// considered "excluded" (not substantive quality).
func TestIsExcludedInfra(t *testing.T) {
	cases := map[string]bool{
		"reviewer_unavailable":     true,
		"timeout":                  true,
		"infra_noise":              true,
		"implementation_quality":   false,
		"deterministic_validation": false,
		"":                         false,
	}
	for class, want := range cases {
		if got := isExcludedInfra(class); got != want {
			t.Errorf("isExcludedInfra(%q)=%v, want %v", class, got, want)
		}
	}
}

// TestMedianInt64EvenLength verifies the median calculation for even-length
// slices (average of two middle values).
func TestMedianInt64EvenLength(t *testing.T) {
	got := medianInt64([]int64{1000, 2000, 3000, 4000})
	want := int64(2500)
	if got != want {
		t.Errorf("medianInt64([1000,2000,3000,4000])=%d, want %d", got, want)
	}
}

// TestP95Int64Empty verifies p95 of an empty slice is 0.
func TestP95Int64Empty(t *testing.T) {
	if got := p95Int64(nil); got != 0 {
		t.Errorf("p95Int64(nil)=%d, want 0", got)
	}
}

// countLines counts non-empty lines in s.
func countLines(s string) int {
	n := 0
	for _, b := range []byte(s) {
		if b == '\n' {
			n++
		}
	}
	return n
}

// trimFirstLine returns s up to (but not including) the first newline.
func trimFirstLine(s string) string {
	for i, b := range []byte(s) {
		if b == '\n' {
			return s[:i]
		}
	}
	return s
}

// itoa is a small helper to avoid strconv import noise in tests.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
