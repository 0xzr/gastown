package polecat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteSessionStartTelemetry(t *testing.T) {
	dir := t.TempDir()

	tel := &SessionTelemetry{
		SessionID:   "gt-test-1",
		PolecatName: "obsidian",
		RigName:     "gastown",
		HookBead:    "gastown-cet.16",
		Command:     "gt prime",
		Model:       "claude-sonnet-4-6",
	}

	if err := WriteSessionStartTelemetry(dir, tel); err != nil {
		t.Fatalf("WriteSessionStartTelemetry failed: %v", err)
	}

	loaded, err := LoadSessionTelemetry(dir, "gt-test-1")
	if err != nil {
		t.Fatalf("LoadSessionTelemetry failed: %v", err)
	}
	if loaded.SessionID != "gt-test-1" {
		t.Errorf("SessionID = %q, want %q", loaded.SessionID, "gt-test-1")
	}
	if loaded.PolecatName != "obsidian" {
		t.Errorf("PolecatName = %q, want %q", loaded.PolecatName, "obsidian")
	}
	if loaded.HookBead != "gastown-cet.16" {
		t.Errorf("HookBead = %q, want %q", loaded.HookBead, "gastown-cet.16")
	}
	if loaded.StartTime.IsZero() {
		t.Error("StartTime was not set")
	}
}

func TestRecordSessionExit(t *testing.T) {
	dir := t.TempDir()

	start := time.Now().UTC().Add(-time.Minute)
	if err := WriteSessionStartTelemetry(dir, &SessionTelemetry{
		SessionID:     "gt-test-exit",
		PolecatName:   "obsidian",
		StartTime:     start,
		TranscriptDir: dir,
	}); err != nil {
		t.Fatalf("WriteSessionStartTelemetry failed: %v", err)
	}

	exitErr := RecordSessionExit(dir, "gt-test-exit", "pane-died", 137, "KILL", "signal-kill")
	if exitErr != nil {
		t.Fatalf("RecordSessionExit failed: %v", exitErr)
	}

	loaded, err := LoadSessionTelemetry(dir, "gt-test-exit")
	if err != nil {
		t.Fatalf("LoadSessionTelemetry failed: %v", err)
	}
	if loaded.ExitCode != 137 {
		t.Errorf("ExitCode = %d, want 137", loaded.ExitCode)
	}
	if loaded.Signal != "KILL" {
		t.Errorf("Signal = %q, want KILL", loaded.Signal)
	}
	if loaded.SupervisorSource != "pane-died" {
		t.Errorf("SupervisorSource = %q, want pane-died", loaded.SupervisorSource)
	}
	if loaded.ExitReason != "signal-kill" {
		t.Errorf("ExitReason = %q, want signal-kill", loaded.ExitReason)
	}
	if loaded.ExitTime.IsZero() {
		t.Error("ExitTime was not set")
	}
}

func TestRecordSessionExit_CreatesMinimalRecordWhenStartMissing(t *testing.T) {
	dir := t.TempDir()

	if err := RecordSessionExit(dir, "gt-test-orphan", "witness-patrol", 1, "TERM", "no-start-record"); err != nil {
		t.Fatalf("RecordSessionExit failed: %v", err)
	}

	loaded, err := LoadSessionTelemetry(dir, "gt-test-orphan")
	if err != nil {
		t.Fatalf("LoadSessionTelemetry failed: %v", err)
	}
	if loaded.SessionID != "gt-test-orphan" {
		t.Errorf("SessionID = %q, want %q", loaded.SessionID, "gt-test-orphan")
	}
	if loaded.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", loaded.ExitCode)
	}
}

func TestLatestTranscriptTail(t *testing.T) {
	dir := t.TempDir()

	// Older file should be ignored.
	oldPath := filepath.Join(dir, "old.jsonl")
	if err := os.WriteFile(oldPath, []byte("old line 1\nold line 2\n"), 0644); err != nil {
		t.Fatalf("write old file: %v", err)
	}
	oldTime := time.Now().UTC().Add(-time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatalf("set old time: %v", err)
	}

	// Newer file contains the tail we expect.
	newPath := filepath.Join(dir, "new.jsonl")
	content := strings.Repeat("line\n", 60) + "final line\n"
	if err := os.WriteFile(newPath, []byte(content), 0644); err != nil {
		t.Fatalf("write new file: %v", err)
	}

	path, tail := latestTranscriptTail(dir, time.Now().UTC().Add(-time.Minute), 10)
	if path != newPath {
		t.Errorf("path = %q, want %q", path, newPath)
	}
	if !strings.Contains(tail, "final line") {
		t.Errorf("tail missing final line: %q", tail)
	}
	if strings.Contains(tail, "old line") {
		t.Errorf("tail unexpectedly contains old file content: %q", tail)
	}
}

func TestUpdateSessionTelemetryLastPaneTail(t *testing.T) {
	dir := t.TempDir()

	if err := WriteSessionStartTelemetry(dir, &SessionTelemetry{
		SessionID:   "gt-test-tail",
		PolecatName: "obsidian",
		StartTime:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("WriteSessionStartTelemetry failed: %v", err)
	}

	if err := UpdateSessionTelemetryLastPaneTail(dir, "gt-test-tail", "last pane output"); err != nil {
		t.Fatalf("UpdateSessionTelemetryLastPaneTail failed: %v", err)
	}

	loaded, err := LoadSessionTelemetry(dir, "gt-test-tail")
	if err != nil {
		t.Fatalf("LoadSessionTelemetry failed: %v", err)
	}
	if loaded.LastPaneTail != "last pane output" {
		t.Errorf("LastPaneTail = %q, want %q", loaded.LastPaneTail, "last pane output")
	}
}
