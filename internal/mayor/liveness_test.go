package mayor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHeartbeatFile(t *testing.T) {
	townRoot := "/tmp/test-town"
	want := filepath.Join(townRoot, "mayor", "heartbeat.json")
	if got := HeartbeatFile(townRoot); got != want {
		t.Errorf("HeartbeatFile() = %q, want %q", got, want)
	}
}

func TestWriteReadHeartbeat(t *testing.T) {
	tmpDir := t.TempDir()
	hb := &Heartbeat{
		Timestamp:     time.Now().UTC().Add(-30 * time.Second),
		Cycle:         7,
		LastAction:    "patrol-check",
		SessionStatus: "healthy",
	}
	if err := WriteHeartbeat(tmpDir, hb); err != nil {
		t.Fatalf("WriteHeartbeat error: %v", err)
	}

	loaded := ReadHeartbeat(tmpDir)
	if loaded == nil {
		t.Fatal("ReadHeartbeat returned nil")
	}
	if loaded.Cycle != 7 {
		t.Errorf("Cycle = %d, want 7", loaded.Cycle)
	}
	if loaded.LastAction != "patrol-check" {
		t.Errorf("LastAction = %q, want patrol-check", loaded.LastAction)
	}
	if loaded.SessionStatus != "healthy" {
		t.Errorf("SessionStatus = %q, want healthy", loaded.SessionStatus)
	}
}

func TestHeartbeat_AgeNil(t *testing.T) {
	var nilHb *Heartbeat
	if nilHb.Age() < 24*time.Hour {
		t.Error("nil heartbeat should have very large age")
	}
}

func TestHeartbeat_FreshStaleVeryStale(t *testing.T) {
	stale := 5 * time.Minute
	veryStale := 20 * time.Minute

	fresh := &Heartbeat{Timestamp: time.Now().Add(-1 * time.Minute)}
	if !fresh.IsFresh(stale) {
		t.Error("1-minute heartbeat should be fresh")
	}
	if fresh.IsStale(stale, veryStale) || fresh.IsVeryStale(veryStale) {
		t.Error("fresh heartbeat should not be stale or very stale")
	}

	mid := &Heartbeat{Timestamp: time.Now().Add(-10 * time.Minute)}
	if !mid.IsStale(stale, veryStale) {
		t.Error("10-minute heartbeat should be stale")
	}
	if mid.IsFresh(stale) || mid.IsVeryStale(veryStale) {
		t.Error("10-minute heartbeat should not be fresh or very stale")
	}

	old := &Heartbeat{Timestamp: time.Now().Add(-25 * time.Minute)}
	if !old.IsVeryStale(veryStale) {
		t.Error("25-minute heartbeat should be very stale")
	}
}

func TestTouch(t *testing.T) {
	tmpDir := t.TempDir()
	if err := Touch(tmpDir, "test-action", "healthy"); err != nil {
		t.Fatalf("Touch error: %v", err)
	}
	first := ReadHeartbeat(tmpDir)
	if first == nil || first.Cycle != 1 {
		t.Fatalf("expected first heartbeat with cycle 1, got %+v", first)
	}

	time.Sleep(10 * time.Millisecond)
	if err := Touch(tmpDir, "test-action", "healthy"); err != nil {
		t.Fatalf("Touch second error: %v", err)
	}
	second := ReadHeartbeat(tmpDir)
	if second == nil || second.Cycle != 2 {
		t.Fatalf("expected second heartbeat with cycle 2, got %+v", second)
	}
	if second.Timestamp.Before(first.Timestamp) {
		t.Error("second timestamp should not be before first")
	}
}

func TestRecordRecoveryAttempt(t *testing.T) {
	tmpDir := t.TempDir()
	attempt := &RecoveryAttempt{
		Reason: "agent-hung",
		Error:  "",
	}
	if err := RecordRecoveryAttempt(tmpDir, attempt); err != nil {
		t.Fatalf("RecordRecoveryAttempt error: %v", err)
	}

	entries, err := os.ReadDir(RecoveryAttemptsDir(tmpDir))
	if err != nil {
		t.Fatalf("reading recovery dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 recovery ledger file, got %d", len(entries))
	}

	data, err := os.ReadFile(filepath.Join(RecoveryAttemptsDir(tmpDir), entries[0].Name()))
	if err != nil {
		t.Fatalf("reading recovery ledger: %v", err)
	}
	var loaded RecoveryAttempt
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("parsing recovery ledger: %v", err)
	}
	if loaded.Reason != "agent-hung" {
		t.Errorf("Reason = %q, want agent-hung", loaded.Reason)
	}
	if loaded.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}
}

func TestCaptureGitSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	snap := CaptureGitSnapshot(tmpDir)
	if snap == nil {
		t.Fatal("CaptureGitSnapshot returned nil")
	}
	if snap.WorkingDir == "" {
		t.Error("WorkingDir should be set")
	}
	// tmpDir is not a git repo, so HEAD/branch/status will be empty.
}

func TestParseModelStatusOutput_JSON(t *testing.T) {
	got := parseModelStatusOutput([]byte(`{"model":"umans-glm-5.2","live":true}`))
	if got != "umans-glm-5.2" {
		t.Errorf("parseModelStatusOutput JSON = %q, want umans-glm-5.2", got)
	}
}

func TestParseModelStatusOutput_Text(t *testing.T) {
	got := parseModelStatusOutput([]byte("\n  claude-sonnet-4-6\n"))
	if got != "claude-sonnet-4-6" {
		t.Errorf("parseModelStatusOutput text = %q, want claude-sonnet-4-6", got)
	}
}

func TestCaptureContextSnapshot(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a fake gt binary that echoes hook and mail responses.
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	fakeGT := filepath.Join(binDir, "gt")
	script := `#!/bin/sh
if [ "$1" = "hook" ] && [ "$2" = "status" ] && [ "$3" = "mayor/" ] && [ "$4" = "--json" ]; then
  echo '{"has_work":true,"pinned_bead":{"id":"gastown-cet.6.2","title":"Mayor liveness","status":"hooked"}}'
  exit 0
fi
if [ "$1" = "mail" ] && [ "$2" = "inbox" ]; then
  echo '[{"priority":"urgent"},{"priority":"high"},{"priority":"normal"}]'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(fakeGT, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	townRoot := filepath.Join(tmpDir, "town")
	if err := os.MkdirAll(townRoot, 0755); err != nil {
		t.Fatal(err)
	}

	snap, err := CaptureContextSnapshot(townRoot, fakeGT)
	if err != nil {
		t.Fatalf("CaptureContextSnapshot error: %v", err)
	}
	if snap.HookBead != "gastown-cet.6.2" {
		t.Errorf("HookBead = %q, want gastown-cet.6.2", snap.HookBead)
	}
	if snap.HookedCount != 1 {
		t.Errorf("HookedCount = %d, want 1", snap.HookedCount)
	}
	if snap.UnreadMailCount != 3 {
		t.Errorf("UnreadMailCount = %d, want 3", snap.UnreadMailCount)
	}
	if snap.CriticalMailCount != 2 {
		t.Errorf("CriticalMailCount = %d, want 2", snap.CriticalMailCount)
	}
}

func TestMayorSessionName(t *testing.T) {
	name := MayorSessionName()
	if !strings.Contains(name, "mayor") {
		t.Errorf("MayorSessionName() = %q, expected to contain 'mayor'", name)
	}
}
