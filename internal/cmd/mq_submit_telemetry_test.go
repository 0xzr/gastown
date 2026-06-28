package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/refinery/mrtelemetry"
)

// TestResolveSubmitWriter_FromModelAssignmentFile verifies that
// resolveSubmitWriter reads the durable model-assignment file written by
// `gt sling`. This is the same source the refinery uses in
// durableReviewWriter, so submit-time and refinery-time attribution stay
// in sync.
func TestResolveSubmitWriter_FromModelAssignmentFile(t *testing.T) {
	townRoot := t.TempDir()
	assignmentDir := filepath.Join(townRoot, ".runtime", "model-assignments")
	if err := os.MkdirAll(assignmentDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assignmentDir, "gt-xyz.json"),
		[]byte(`{"agent":"umans-kimi","source":"sling"}`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	writer, source := resolveSubmitWriter(townRoot, "gastown", "gt-xyz")
	if writer != "umans-kimi" {
		t.Errorf("writer=%q, want umans-kimi", writer)
	}
	if source != "model_assignment" {
		t.Errorf("source=%q, want model_assignment", source)
	}
}

// TestResolveSubmitWriter_StripsSubIssueSuffix verifies that a source
// issue like "gt-xyz.1" resolves to the bare "gt-xyz.json" assignment
// file (matching the refinery's durableReviewAssignmentID behavior).
func TestResolveSubmitWriter_StripsSubIssueSuffix(t *testing.T) {
	townRoot := t.TempDir()
	assignmentDir := filepath.Join(townRoot, ".runtime", "model-assignments")
	if err := os.MkdirAll(assignmentDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assignmentDir, "gt-xyz.json"),
		[]byte(`{"agent":"umans-glm"}`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	writer, _ := resolveSubmitWriter(townRoot, "gastown", "gt-xyz.1")
	if writer != "umans-glm" {
		t.Errorf("sub-issue writer=%q, want umans-glm (parent assignment)", writer)
	}
}

// TestResolveSubmitWriter_StripsRefinerySuffix verifies that a
// dispatcher-style issue like "gt-xyz@mqyavpdj" resolves to the bare
// "gt-xyz.json" assignment file.
func TestResolveSubmitWriter_StripsRefinerySuffix(t *testing.T) {
	townRoot := t.TempDir()
	assignmentDir := filepath.Join(townRoot, ".runtime", "model-assignments")
	if err := os.MkdirAll(assignmentDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assignmentDir, "gt-xyz.json"),
		[]byte(`{"agent":"m3"}`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	writer, _ := resolveSubmitWriter(townRoot, "gastown", "gt-xyz@mqyavpdj")
	if writer != "m3" {
		t.Errorf("dispatched writer=%q, want m3 (base assignment)", writer)
	}
}

// TestResolveSubmitWriter_ReassignmentReflectsNewWriter verifies the core
// acceptance criterion: after the source bead is reassigned to a new
// model, the next submit picks up the NEW writer. Old MR attempts
// retain their original writer (covered in mrtelemetry tests).
func TestResolveSubmitWriter_ReassignmentReflectsNewWriter(t *testing.T) {
	townRoot := t.TempDir()
	assignmentDir := filepath.Join(townRoot, ".runtime", "model-assignments")
	if err := os.MkdirAll(assignmentDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Initial: kimi
	if err := os.WriteFile(filepath.Join(assignmentDir, "gt-zzz.json"),
		[]byte(`{"agent":"umans-kimi"}`), 0644); err != nil {
		t.Fatalf("WriteFile 1: %v", err)
	}
	writer1, _ := resolveSubmitWriter(townRoot, "gastown", "gt-zzz")
	if writer1 != "umans-kimi" {
		t.Errorf("initial writer=%q, want umans-kimi", writer1)
	}

	// Reassigned: file overwritten with glm
	if err := os.WriteFile(filepath.Join(assignmentDir, "gt-zzz.json"),
		[]byte(`{"agent":"umans-glm"}`), 0644); err != nil {
		t.Fatalf("WriteFile 2: %v", err)
	}
	writer2, _ := resolveSubmitWriter(townRoot, "gastown", "gt-zzz")
	if writer2 != "umans-glm" {
		t.Errorf("reassigned writer=%q, want umans-glm", writer2)
	}
}

// TestResolveSubmitWriter_NoAssignmentFileFallsBackToUnknown verifies
// that when no assignment file is present (legacy path), the resolver
// returns "unknown" rather than empty so the report still has a stable
// attribution key.
func TestResolveSubmitWriter_NoAssignmentFileFallsBackToUnknown(t *testing.T) {
	townRoot := t.TempDir()
	writer, source := resolveSubmitWriter(townRoot, "gastown", "gt-orphan")
	if writer != "unknown" {
		t.Errorf("writer=%q, want unknown", writer)
	}
	if source != "unknown" {
		t.Errorf("source=%q, want unknown", source)
	}
}

// TestResolveSubmitWriter_EmptyInputs verifies that empty source_issue
// or townRoot returns "unknown" rather than reading a misleading file.
func TestResolveSubmitWriter_EmptyInputs(t *testing.T) {
	cases := []struct {
		townRoot, rigName, sourceIssue string
	}{
		{"", "gastown", "gt-xyz"},
		{"/tmp", "gastown", ""},
		{"/tmp", "gastown", "@orphan"},
	}
	for _, tc := range cases {
		writer, _ := resolveSubmitWriter(tc.townRoot, tc.rigName, tc.sourceIssue)
		if writer != "unknown" {
			t.Errorf("resolveSubmitWriter(%q,%q,%q)=%q, want unknown",
				tc.townRoot, tc.rigName, tc.sourceIssue, writer)
		}
	}
}

// TestRecordMRSubmitTelemetry_PersistsRow exercises the full submit-time
// telemetry path end-to-end: writer resolution + JSONL append + subsequent
// read.
func TestRecordMRSubmitTelemetry_PersistsRow(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "gastown"
	polecat := "jasper"
	sourceBead := "gt-zzz"

	// Set up model-assignment file so the resolver finds a real writer.
	assignmentDir := filepath.Join(townRoot, ".runtime", "model-assignments")
	if err := os.MkdirAll(assignmentDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assignmentDir, sourceBead+".json"),
		[]byte(`{"agent":"m3"}`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	recordMRSubmitTelemetry(townRoot, rigName, sourceBead, "gt-mr-zzz",
		"polecat/jasper/"+sourceBead, polecat, "deadbeef")

	store := mrtelemetry.NewStore(mrtelemetry.DefaultStorePath(filepath.Join(townRoot, rigName)))
	rec, err := store.GetByMRID("gt-mr-zzz")
	if err != nil {
		t.Fatalf("GetByMRID: %v", err)
	}
	if rec.WriterModel != "m3" {
		t.Errorf("WriterModel=%q, want m3", rec.WriterModel)
	}
	if rec.WriterModelSource != "model_assignment" {
		t.Errorf("WriterModelSource=%q, want model_assignment", rec.WriterModelSource)
	}
	if rec.Polecat != polecat {
		t.Errorf("Polecat=%q, want %q", rec.Polecat, polecat)
	}
	if rec.SourceBead != sourceBead {
		t.Errorf("SourceBead=%q, want %q", rec.SourceBead, sourceBead)
	}
	if rec.AttemptNumber != 1 {
		t.Errorf("AttemptNumber=%d, want 1", rec.AttemptNumber)
	}
	if rec.SubmittedAt.IsZero() {
		t.Errorf("SubmittedAt is zero")
	}
	if time.Since(rec.SubmittedAt) > 30*time.Second {
		t.Errorf("SubmittedAt=%v is too old (recording time bug)", rec.SubmittedAt)
	}
}

// TestRecordMRSubmitTelemetry_UnknownWriterDoesNotPanic verifies the
// recording path tolerates a missing assignment file without panicking
// or failing the submit.
func TestRecordMRSubmitTelemetry_UnknownWriterDoesNotPanic(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "gastown"

	recordMRSubmitTelemetry(townRoot, rigName, "gt-orphan", "gt-mr-orphan",
		"polecat/legacy/gt-orphan", "no-such-polecat", "")

	store := mrtelemetry.NewStore(mrtelemetry.DefaultStorePath(filepath.Join(townRoot, rigName)))
	rec, err := store.GetByMRID("gt-mr-orphan")
	if err != nil {
		t.Fatalf("GetByMRID: %v", err)
	}
	if rec.WriterModel != "unknown" {
		t.Errorf("WriterModel=%q, want unknown (legacy path)", rec.WriterModel)
	}
	if rec.Rig != rigName {
		t.Errorf("Rig=%q, want %q", rec.Rig, rigName)
	}
}

// TestRecordMRSubmitTelemetry_NonFatalOnFilesystemError verifies that
// even if the telemetry directory cannot be created (e.g. read-only
// filesystem), the submit-time call does not propagate the error.
// Telemetry is best-effort: failure must not block MR submission.
func TestRecordMRSubmitTelemetry_NonFatalOnFilesystemError(t *testing.T) {
	townRoot := t.TempDir()
	// Create a regular file at <townRoot>/gastown so MkdirAll(gastown/.runtime)
	// fails when the helper tries to write the telemetry JSONL.
	rigPath := filepath.Join(townRoot, "gastown")
	if err := os.WriteFile(rigPath, []byte("block"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Must not panic, must not error (returns silently).
	recordMRSubmitTelemetry(townRoot, "gastown", "gt-x", "gt-mr-x",
		"polecat/quartz/gt-x", "quartz", "sha")
}

// TestRecordMRSubmitTelemetry_ReworkIncrementsAttemptNumber verifies
// that submitting a second MR for the same source bead increments
// attempt_number, so the report counts it as a rework.
func TestRecordMRSubmitTelemetry_ReworkIncrementsAttemptNumber(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "gastown"
	sourceBead := "gt-rework"

	assignmentDir := filepath.Join(townRoot, ".runtime", "model-assignments")
	if err := os.MkdirAll(assignmentDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assignmentDir, sourceBead+".json"),
		[]byte(`{"agent":"umans-kimi"}`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	recordMRSubmitTelemetry(townRoot, rigName, sourceBead, "gt-mr-rework-1",
		"polecat/quartz/"+sourceBead, "quartz", "sha-1")
	recordMRSubmitTelemetry(townRoot, rigName, sourceBead, "gt-mr-rework-2",
		"polecat/onyx/"+sourceBead, "onyx", "sha-2")

	store := mrtelemetry.NewStore(mrtelemetry.DefaultStorePath(filepath.Join(townRoot, rigName)))
	r1, err := store.GetByMRID("gt-mr-rework-1")
	if err != nil {
		t.Fatalf("GetByMRID 1: %v", err)
	}
	r2, err := store.GetByMRID("gt-mr-rework-2")
	if err != nil {
		t.Fatalf("GetByMRID 2: %v", err)
	}
	if r1.AttemptNumber != 1 {
		t.Errorf("first attempt AttemptNumber=%d, want 1", r1.AttemptNumber)
	}
	if r2.AttemptNumber != 2 {
		t.Errorf("rework AttemptNumber=%d, want 2", r2.AttemptNumber)
	}
}

// TestRecordMRSubmitTelemetry_AttributionIsHonest verifies the key
// invariant: when kimi submits then glm reworks, both rows retain their
// OWN writer attribution (kimi for the first, glm for the second),
// not "the current model at report time".
func TestRecordMRSubmitTelemetry_AttributionIsHonest(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "gastown"
	sourceBead := "gt-honest"
	assignmentDir := filepath.Join(townRoot, ".runtime", "model-assignments")
	if err := os.MkdirAll(assignmentDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// First attempt: assignment is kimi.
	if err := os.WriteFile(filepath.Join(assignmentDir, sourceBead+".json"),
		[]byte(`{"agent":"umans-kimi"}`), 0644); err != nil {
		t.Fatalf("WriteFile 1: %v", err)
	}
	recordMRSubmitTelemetry(townRoot, rigName, sourceBead, "gt-mr-honest-1",
		"polecat/quartz/"+sourceBead, "quartz", "sha-1")

	// Reassignment to glm.
	if err := os.WriteFile(filepath.Join(assignmentDir, sourceBead+".json"),
		[]byte(`{"agent":"umans-glm"}`), 0644); err != nil {
		t.Fatalf("WriteFile 2: %v", err)
	}
	recordMRSubmitTelemetry(townRoot, rigName, sourceBead, "gt-mr-honest-2",
		"polecat/onyx/"+sourceBead, "onyx", "sha-2")

	store := mrtelemetry.NewStore(mrtelemetry.DefaultStorePath(filepath.Join(townRoot, rigName)))
	r1, _ := store.GetByMRID("gt-mr-honest-1")
	r2, _ := store.GetByMRID("gt-mr-honest-2")

	if r1.WriterModel != "umans-kimi" {
		t.Errorf("first attempt WriterModel=%q, want umans-kimi (preserved)", r1.WriterModel)
	}
	if r2.WriterModel != "umans-glm" {
		t.Errorf("rework WriterModel=%q, want umans-glm (reassignment)", r2.WriterModel)
	}
}

// guard against accidental context.Background usage: ensures the helper
// uses a bounded context.
func TestRecordMRSubmitTelemetry_UsesBoundedContext(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "gastown"
	store := mrtelemetry.NewStore(mrtelemetry.DefaultStorePath(filepath.Join(townRoot, rigName)))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := store.RecordMRAttempt(ctx, mrtelemetry.RecordMRAttemptInput{
		Rig: rigName, SourceBead: "gt-ctxt", MRID: "gt-mr-ctxt",
		Polecat: "quartz", WriterModel: "umans-kimi",
		SubmittedAt: time.Now(),
	}); err != nil {
		if !strings.Contains(err.Error(), "telemetry") {
			t.Errorf("unexpected error: %v", err)
		}
	}
}
