package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

func TestShouldFireCrossRigEscalation_Debounces(t *testing.T) {
	resetCrossRigEscalationStateForTest()
	t.Cleanup(resetCrossRigEscalationStateForTest)

	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	if !shouldFireCrossRigEscalation("walletui", "hq", now) {
		t.Fatalf("first call must fire")
	}
	// Second call inside the debounce window must NOT fire.
	if shouldFireCrossRigEscalation("walletui", "hq", now.Add(30*time.Minute)) {
		t.Fatalf("second call inside debounce window must not fire")
	}
	// After the debounce window elapses, fire again.
	if !shouldFireCrossRigEscalation("walletui", "hq", now.Add(crossRigEscalationDebounce+time.Minute)) {
		t.Fatalf("call past debounce window must fire")
	}
}

func TestBatchFetchBeadInfoByIDsRetriesMissesWithTownRouting(t *testing.T) {
	townRoot := t.TempDir()
	townBeads := filepath.Join(townRoot, ".beads")
	rigBeads := filepath.Join(townRoot, "polybot", ".beads")
	if err := os.MkdirAll(townBeads, 0o755); err != nil {
		t.Fatalf("mkdir town beads: %v", err)
	}
	if err := os.MkdirAll(rigBeads, 0o755); err != nil {
		t.Fatalf("mkdir rig beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(`{"prefix":"polybot-","path":"polybot"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "bd.log")
	script := strings.ReplaceAll(`#!/usr/bin/env sh
printf 'BEADS_DIR=%s ARGS=%s\n' "${BEADS_DIR:-}" "$*" >> "LOGPATH"
case "$*" in
  *"show"*"polybot-optiv"*)
    if [ -n "${BEADS_DIR:-}" ]; then
      echo 'not found in pinned db' >&2
      exit 1
    fi
    printf '[{"id":"polybot-optiv","status":"open","title":"ready source","labels":["feature"]}]'
    exit 0
    ;;
esac
printf 'unexpected bd command: %s\n' "$*" >&2
exit 1
`, "LOGPATH", logPath)
	writeBDStub(t, binDir, script, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got := batchFetchBeadInfoByIDs(townRoot, []string{"polybot-optiv"})
	info, found := got["polybot-optiv"]
	if !found {
		t.Fatalf("expected routed fallback to find polybot-optiv; log:\n%s", readTestFile(t, logPath))
	}
	if info.Status != "open" || info.Title != "ready source" {
		t.Fatalf("unexpected info: %+v", info)
	}

	log := readTestFile(t, logPath)
	if !strings.Contains(log, "BEADS_DIR="+rigBeads) {
		t.Fatalf("expected first lookup to pin rig beads dir %q; log:\n%s", rigBeads, log)
	}
	if !strings.Contains(log, "BEADS_DIR= ARGS=") {
		t.Fatalf("expected routed fallback without BEADS_DIR; log:\n%s", log)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestShouldFireCrossRigEscalation_KeyedByRigAndPrefix(t *testing.T) {
	resetCrossRigEscalationStateForTest()
	t.Cleanup(resetCrossRigEscalationStateForTest)

	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)

	if !shouldFireCrossRigEscalation("walletui", "hq", now) {
		t.Fatalf("walletui/hq first call must fire")
	}
	// Different rig — should fire independently.
	if !shouldFireCrossRigEscalation("furiosa", "hq", now) {
		t.Fatalf("furiosa/hq must fire (different rig)")
	}
	// Different prefix on same rig — should fire independently.
	if !shouldFireCrossRigEscalation("walletui", "wisp", now) {
		t.Fatalf("walletui/wisp must fire (different prefix)")
	}
	// Same (rig, prefix) repeats — debounced.
	if shouldFireCrossRigEscalation("walletui", "hq", now.Add(time.Minute)) {
		t.Fatalf("walletui/hq repeat must not fire")
	}
}

func TestShouldCloseContextAfterDispatchFailure_ExplicitAgentStartup(t *testing.T) {
	b := capacity.PendingBead{
		ID:         "ctx-1",
		WorkBeadID: "polybot-abc",
		Context: &capacity.SlingContextFields{
			Agent: "codex-impl",
		},
	}

	closeNow, reason := shouldCloseContextAfterDispatchFailure(
		b,
		errors.New("sling failed: codex-impl startup failed instantly"),
	)
	if !closeNow {
		t.Fatal("explicit-agent startup failure should close the stale context")
	}
	if reason != dispatchFailureCloseReasonAgentStartup {
		t.Fatalf("reason = %q, want %q", reason, dispatchFailureCloseReasonAgentStartup)
	}
}

func TestShouldCloseContextAfterDispatchFailure_WithoutExplicitAgentKeepsRetry(t *testing.T) {
	b := capacity.PendingBead{
		ID:         "ctx-1",
		WorkBeadID: "polybot-abc",
		Context:    &capacity.SlingContextFields{},
	}

	closeNow, reason := shouldCloseContextAfterDispatchFailure(
		b,
		errors.New("sling failed: startup failed instantly"),
	)
	if closeNow || reason != "" {
		t.Fatalf("implicit-agent failure should keep normal retry policy, got close=%v reason=%q", closeNow, reason)
	}
}

func TestShouldCloseContextAfterDispatchFailure_TransientExplicitAgentFailureKeepsRetry(t *testing.T) {
	b := capacity.PendingBead{
		ID:         "ctx-1",
		WorkBeadID: "polybot-abc",
		Context: &capacity.SlingContextFields{
			Agent: "umans-glm",
		},
	}

	closeNow, reason := shouldCloseContextAfterDispatchFailure(
		b,
		errors.New("temporary bead lookup timeout"),
	)
	if closeNow || reason != "" {
		t.Fatalf("transient failure should keep normal retry policy, got close=%v reason=%q", closeNow, reason)
	}
}
