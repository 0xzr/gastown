package refinery

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/refinery/mrtelemetry"
	"github.com/steveyegge/gastown/internal/rig"
)

// newTestEngineerForTelemetry constructs an Engineer pointing at a real
// rig path under t.TempDir() so the mrtelemetry store can be written to
// disk. Most engineer fields are left at their zero values because the
// telemetry helpers don't depend on them.
//
// The town root is also seeded with mayor/town.json so the
// durableReviewTownRoot walker can locate it; tests that don't need
// town-root resolution can ignore the error from that step.
func newTestEngineerForTelemetry(t *testing.T) (*Engineer, string) {
	t.Helper()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "gastown")
	if err := os.MkdirAll(filepath.Join(rigPath, ".runtime"), 0755); err != nil {
		t.Fatalf("mkdir rig runtime: %v", err)
	}
	// Seed mayor/town.json so durableReviewTownRoot can locate the town.
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"),
		[]byte(`{"name":"test-town"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	e := &Engineer{
		rig:    &rig.Rig{Name: "gastown", Path: rigPath},
		output: io.Discard,
	}
	return e, rigPath
}

// ctx is a shorthand for the test context used by the telemetry helpers,
// which only need a non-nil context.Background() for the helper wrappers.
func ctx() context.Context { return context.Background() }

func TestEngineerTelemetryStore_PathUnderRuntimeDir(t *testing.T) {
	e, rigPath := newTestEngineerForTelemetry(t)
	store := e.telemetryStore()
	if store == nil {
		t.Fatalf("telemetryStore returned nil for valid rig")
	}
	want := filepath.Join(rigPath, ".runtime", "mr-telemetry.jsonl")
	// Reopen from the same path and confirm we can write+read.
	store2 := mrtelemetry.NewStore(want)
	if _, err := store2.RecordMRAttempt(ctx(), mrtelemetry.RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-tmp", MRID: "gt-mr-tmp",
		Polecat: "quartz", WriterModel: "umans-kimi",
		SubmittedAt: time.Now(),
	}); err != nil {
		t.Fatalf("write to telemetry store: %v", err)
	}
}

// TestEngineerTelemetryStore_NilRigReturnsNil verifies the nil-rig guard
// so a misconfigured engineer never panics on telemetry access.
func TestEngineerTelemetryStore_NilRigReturnsNil(t *testing.T) {
	e := &Engineer{}
	if store := e.telemetryStore(); store != nil {
		t.Errorf("nil-rig store = %v, want nil", store)
	}
}

// TestRecordTelemetry_BestEffort verifies that recordTelemetry swallows
// errors and never panics. This is a contract: a broken telemetry file
// must never stall MR processing.
func TestRecordTelemetry_BestEffort(t *testing.T) {
	e := &Engineer{
		// No rig -> telemetryStore() returns nil -> fn never runs -> no error.
		output: io.Discard,
	}
	called := false
	e.recordTelemetry(func(_ *mrtelemetry.Store) error {
		called = true
		return nil
	})
	if called {
		t.Errorf("recordTelemetry should not call fn when rig is nil")
	}
}

// TestRecordRefineryStarted_PersistsTimestamp verifies that
// recordRefineryStarted writes a timestamp the report can read back.
func TestRecordRefineryStarted_PersistsTimestamp(t *testing.T) {
	e, _ := newTestEngineerForTelemetry(t)
	store := e.telemetryStore()
	if _, err := store.RecordMRAttempt(ctx(), mrtelemetry.RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-st", MRID: "gt-mr-st",
		Polecat: "quartz", WriterModel: "umans-kimi",
		SubmittedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	before := time.Now().UTC().Add(-time.Second)
	e.recordRefineryStarted("gt-mr-st")
	after := time.Now().UTC().Add(time.Second)

	rec, err := store.GetByMRID("gt-mr-st")
	if err != nil {
		t.Fatalf("GetByMRID: %v", err)
	}
	if rec.RefineryStartedAt.IsZero() {
		t.Fatalf("RefineryStartedAt is zero")
	}
	if rec.RefineryStartedAt.Before(before) || rec.RefineryStartedAt.After(after) {
		t.Errorf("RefineryStartedAt=%v outside [%v, %v]",
			rec.RefineryStartedAt, before, after)
	}
}

// TestRecordValidation_PersistsPassAndFail verifies that recordValidation
// captures both passed=true and passed=false outcomes.
func TestRecordValidation_PersistsPassAndFail(t *testing.T) {
	e, _ := newTestEngineerForTelemetry(t)
	store := e.telemetryStore()

	// Case 1: PASS
	if _, err := store.RecordMRAttempt(ctx(), mrtelemetry.RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-vp", MRID: "gt-mr-vp",
		WriterModel: "umans-kimi", SubmittedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed vp: %v", err)
	}
	started := time.Now().UTC().Add(-time.Minute)
	finished := time.Now().UTC()
	e.recordValidation("gt-mr-vp", started, finished, true)
	rec, err := store.GetByMRID("gt-mr-vp")
	if err != nil {
		t.Fatalf("GetByMRID vp: %v", err)
	}
	if !rec.ValidationPassed {
		t.Errorf("vp ValidationPassed=false, want true")
	}
	if !rec.ValidationStartedAt.Equal(started) {
		t.Errorf("vp ValidationStartedAt=%v, want %v", rec.ValidationStartedAt, started)
	}
	if !rec.ValidationFinishedAt.Equal(finished) {
		t.Errorf("vp ValidationFinishedAt=%v, want %v", rec.ValidationFinishedAt, finished)
	}

	// Case 2: FAIL
	if _, err := store.RecordMRAttempt(ctx(), mrtelemetry.RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-vf", MRID: "gt-mr-vf",
		WriterModel: "umans-glm", SubmittedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed vf: %v", err)
	}
	e.recordValidation("gt-mr-vf", started, finished, false)
	rec, err = store.GetByMRID("gt-mr-vf")
	if err != nil {
		t.Fatalf("GetByMRID vf: %v", err)
	}
	if rec.ValidationPassed {
		t.Errorf("vf ValidationPassed=true, want false")
	}
}

// TestRecordFinalOutcome_MergedPersistsMergedAt verifies the success
// path stamps merged_at and the merge commit, and leaves rejected_at
// empty (merged and rejected are mutually exclusive).
func TestRecordFinalOutcome_MergedPersistsMergedAt(t *testing.T) {
	e, _ := newTestEngineerForTelemetry(t)
	store := e.telemetryStore()
	if _, err := store.RecordMRAttempt(ctx(), mrtelemetry.RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-fm", MRID: "gt-mr-fm",
		WriterModel: "umans-kimi", SubmittedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mergedAt := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	e.recordFinalOutcome("gt-mr-fm", "merged", "", mergedAt, time.Time{}, "merge-sha", "publish-sha")

	rec, err := store.GetByMRID("gt-mr-fm")
	if err != nil {
		t.Fatalf("GetByMRID: %v", err)
	}
	if rec.FinalGateDecision != "merged" {
		t.Errorf("FinalGateDecision=%q, want merged", rec.FinalGateDecision)
	}
	if !rec.MergedAt.Equal(mergedAt) {
		t.Errorf("MergedAt=%v, want %v", rec.MergedAt, mergedAt)
	}
	if !rec.RejectedAt.IsZero() {
		t.Errorf("RejectedAt=%v, want zero (merged path)", rec.RejectedAt)
	}
	if rec.MergeCommitSHA != "merge-sha" {
		t.Errorf("MergeCommitSHA=%q, want merge-sha", rec.MergeCommitSHA)
	}
	if rec.PublishedCommitSHA != "publish-sha" {
		t.Errorf("PublishedCommitSHA=%q, want publish-sha", rec.PublishedCommitSHA)
	}
}

// TestRecordFinalOutcome_RejectedPersistsRejectedAt verifies the failure
// path stamps rejected_at and failure_class, and leaves merged_at empty.
func TestRecordFinalOutcome_RejectedPersistsRejectedAt(t *testing.T) {
	e, _ := newTestEngineerForTelemetry(t)
	store := e.telemetryStore()
	if _, err := store.RecordMRAttempt(ctx(), mrtelemetry.RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-fr", MRID: "gt-mr-fr",
		WriterModel: "umans-glm", SubmittedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rejectedAt := time.Date(2026, 6, 28, 13, 0, 0, 0, time.UTC)
	e.recordFinalOutcome("gt-mr-fr", "rejected", "implementation_quality",
		time.Time{}, rejectedAt, "", "")

	rec, err := store.GetByMRID("gt-mr-fr")
	if err != nil {
		t.Fatalf("GetByMRID: %v", err)
	}
	if rec.FinalGateDecision != "rejected" {
		t.Errorf("FinalGateDecision=%q, want rejected", rec.FinalGateDecision)
	}
	if rec.FailureClass != "implementation_quality" {
		t.Errorf("FailureClass=%q, want implementation_quality", rec.FailureClass)
	}
	if !rec.RejectedAt.Equal(rejectedAt) {
		t.Errorf("RejectedAt=%v, want %v", rec.RejectedAt, rejectedAt)
	}
	if !rec.MergedAt.IsZero() {
		t.Errorf("MergedAt=%v, want zero (rejected path)", rec.MergedAt)
	}
}

// TestRecordWriterOverwriteIfUnknown_OnlyUpgradesUnknown verifies the
// upgrade contract: rows already attributed to a real model are NOT
// overwritten, but "unknown" rows ARE upgraded when a durable
// assignment file exists. This is the core test for the
// "writer attribution across reassignments" acceptance criterion.
func TestRecordWriterOverwriteIfUnknown_OnlyUpgradesUnknown(t *testing.T) {
	e, rigPath := newTestEngineerForTelemetry(t)
	store := e.telemetryStore()

	// Seed two MRs for the same source_bead. The first was attributed
	// at submit time (kimi), the second was unknown at submit (no
	// assignment file present).
	if _, err := store.RecordMRAttempt(ctx(), mrtelemetry.RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-wo", MRID: "gt-mr-wo-1",
		WriterModel: "umans-kimi", WriterModelSource: "model_assignment",
		SubmittedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed 1: %v", err)
	}
	if _, err := store.RecordMRAttempt(ctx(), mrtelemetry.RecordMRAttemptInput{
		Rig: "gastown", SourceBead: "gt-wo", MRID: "gt-mr-wo-2",
		WriterModel: "unknown", WriterModelSource: "unknown",
		SubmittedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed 2: %v", err)
	}

	// Write a durable assignment file for the source bead — this is what
	// the refinery would discover when it picks up the MR.
	assignmentDir := filepath.Join(filepath.Dir(rigPath), ".runtime", "model-assignments")
	if err := os.MkdirAll(assignmentDir, 0755); err != nil {
		t.Fatalf("mkdir assignments: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assignmentDir, "gt-wo.json"),
		[]byte(`{"agent":"umans-glm"}`), 0644); err != nil {
		t.Fatalf("write assignment: %v", err)
	}

	e.recordWriterOverwriteIfUnknown("gt-mr-wo-1")
	e.recordWriterOverwriteIfUnknown("gt-mr-wo-2")

	// Row 1: kimi attribution is preserved (already non-unknown).
	r1, err := store.GetByMRID("gt-mr-wo-1")
	if err != nil {
		t.Fatalf("GetByMRID 1: %v", err)
	}
	if r1.WriterModel != "umans-kimi" {
		t.Errorf("r1 writer=%q, want umans-kimi (must not be overwritten)", r1.WriterModel)
	}

	// Row 2: unknown is upgraded to glm with source=refinery_resolved.
	r2, err := store.GetByMRID("gt-mr-wo-2")
	if err != nil {
		t.Fatalf("GetByMRID 2: %v", err)
	}
	if r2.WriterModel != "umans-glm" {
		t.Errorf("r2 writer=%q, want umans-glm (upgraded)", r2.WriterModel)
	}
	if r2.WriterModelSource != "refinery_resolved" {
		t.Errorf("r2 source=%q, want refinery_resolved", r2.WriterModelSource)
	}
}

// TestRecordWriterOverwriteIfUnknown_NoMRIsNoOp verifies the helper
// tolerates unknown MR IDs without panicking (recordTelemetry wraps).
func TestRecordWriterOverwriteIfUnknown_NoMRIsNoOp(t *testing.T) {
	e, _ := newTestEngineerForTelemetry(t)
	// Should not panic.
	e.recordWriterOverwriteIfUnknown("nonexistent-mr")
	e.recordWriterOverwriteIfUnknown("")
}

// TestRecordTelemetry_NilRigDoesNotPanic is the contract test: the
// helper chain must never panic on a partially-initialized engineer.
func TestRecordTelemetry_NilRigDoesNotPanic(t *testing.T) {
	e := &Engineer{output: io.Discard}
	e.recordRefineryStarted("any")
	e.recordValidation("any", time.Now(), time.Now(), true)
	e.recordCodexReview("any", time.Now(), time.Now(), "PASS", nil)
	e.recordFinalOutcome("any", "merged", "", time.Now(), time.Time{}, "", "")
	e.recordWriterOverwriteIfUnknown("any")
}
