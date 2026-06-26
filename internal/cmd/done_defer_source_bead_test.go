package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestDoneDeferSourceBeadCloseToRefinery verifies the gastown-5oy fix:
// when the agent bead's CompletionMetadata carries MRID (with no failures),
// updateAgentStateOnDone must NOT close the source bead here. Instead it
// transitions the bead to a "merge-pending" state — assignee cleared
// (witness zombie patrol won't re-dispatch), awaiting-merge-stamp:<mr-id>
// label added (traceability), status untouched. The refinery closes the
// bead with proper "Merged in <MR-id>" provenance when it merges the MR
// (see internal/refinery/manager.go:634 and engineer.go:2254).
//
// Before the fix: gt done called bd.Close() (no reason) which left the
// source bead with default "Closed" close_reason. The refinery then saw
// the bead was already terminal and skipped the provenance stamp
// (manager.go:639-642, engineer.go:2266-2268), breaking merge-provenance
// consistency across all polecat completions (13/13 polecats hit this on
// 2026-06-26 patrol #73 before the fix).
func TestDoneDeferSourceBeadCloseToRefinery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown"}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog := filepath.Join(townRoot, "closes.log")
	updatesLog := filepath.Join(townRoot, "updates.log")

	// Stub: agent bead carries CompletionMetadata.MRID (with no failures).
	// active_mr is present and matching — the happy path. The deferral
	// contract is keyed off CompletionMetadata, not active_mr.
	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nux)
        cat <<'JSON'
[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","description":"agent_state: working\nhook_bead: gt-source-1\nactive_mr: gastown-wisp-abc\nexit_type: COMPLETED\nmr_id: gastown-wisp-abc\nmr_failed: false\npush_failed: false"}]
JSON
        ;;
      gt-source-1)
        echo '[{"id":"gt-source-1","title":"Source bead","status":"hooked","assignee":"gastown/polecats/nux"}]'
        ;;
    esac
    ;;
  update)
    echo "$@" >> "%s"
    ;;
  close)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%s"
    done
    ;;
  agent|list|slot)
    exit 0
    ;;
esac
exit 0
`, updatesLog, closesLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GT_ROLE", "polecat")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "nux")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Call updateAgentStateOnDone — deferral path expected (agent bead
	// has MRID with no failures, exit=COMPLETED).
	updateAgentStateOnDone(
		filepath.Join(townRoot, "gastown"),
		townRoot,
		ExitCompleted,
		"gt-source-1",
	)

	// Source bead must NOT be closed (refinery owns the close).
	if _, err := os.Stat(closesLog); err == nil {
		closesBytes, _ := os.ReadFile(closesLog)
		if strings.Contains(string(closesBytes), "gt-source-1") {
			t.Errorf("source bead gt-source-1 was closed — deferral failed.\nCloses:\n%s", string(closesBytes))
		}
	}

	// Deferral helper must have run: source bead update with cleared
	// assignee + awaiting-merge-stamp:<mr-id> label.
	updatesBytes, err := os.ReadFile(updatesLog)
	if err != nil {
		t.Fatalf("no updates recorded — deferral helper did not run: %v", err)
	}
	updates := string(updatesBytes)

	if !strings.Contains(updates, "gt-source-1") {
		t.Errorf("expected update referencing gt-source-1, got:\n%s", updates)
	}
	if !strings.Contains(updates, "--assignee=") {
		t.Errorf("expected --assignee= flag (cleared assignee), got:\n%s", updates)
	}
	if !strings.Contains(updates, "awaiting-merge-stamp:gastown-wisp-abc") {
		t.Errorf("expected awaiting-merge-stamp:gastown-wisp-abc label, got:\n%s", updates)
	}
}

// TestDoneDeferSourceBeadCloseSkippedOnFailure covers the failure-mode
// behavior: when the agent bead indicates the MR submission or push failed
// (MRFailed=true / PushFailed=true), deferral must NOT trigger — the
// refinery won't process anything for failed submissions, so leaving the
// source bead open would strand it forever. The original close path runs.
func TestDoneDeferSourceBeadCloseSkippedOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown"}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog := filepath.Join(townRoot, "closes.log")
	updatesLog := filepath.Join(townRoot, "updates.log")

	// Stub: MRID set but mr_failed=true (or push_failed=true). Deferral
	// must be skipped — bead closed normally.
	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nux)
        cat <<'JSON'
[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","description":"agent_state: working\nhook_bead: gt-source-2\nactive_mr: gastown-wisp-failed\nexit_type: COMPLETED\nmr_id: gastown-wisp-failed\nmr_failed: true\npush_failed: false"}]
JSON
        ;;
      gt-source-2)
        echo '[{"id":"gt-source-2","title":"Source bead","status":"hooked","assignee":"gastown/polecats/nux"}]'
        ;;
    esac
    ;;
  update)
    echo "$@" >> "%s"
    ;;
  close)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%s"
    done
    ;;
  agent|list|slot)
    exit 0
    ;;
esac
exit 0
`, updatesLog, closesLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GT_ROLE", "polecat")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "nux")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	updateAgentStateOnDone(
		filepath.Join(townRoot, "gastown"),
		townRoot,
		ExitCompleted,
		"gt-source-2",
	)

	// Source bead MUST be closed (deferral skipped due to mr_failed=true).
	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("expected source bead gt-source-2 to be closed, but no closes logged: %v", err)
	}
	if !strings.Contains(string(closesBytes), "gt-source-2") {
		t.Errorf("expected source bead gt-source-2 in closes log, got:\n%s", string(closesBytes))
	}

	// Deferral helper (which writes to updates.log) must NOT have run.
	if _, err := os.Stat(updatesLog); err == nil {
		updatesBytes, _ := os.ReadFile(updatesLog)
		if strings.Contains(string(updatesBytes), "awaiting-merge-stamp:") {
			t.Errorf("deferral helper ran despite mr_failed=true — update log:\n%s", string(updatesBytes))
		}
	}
}

// TestDoneDeferSourceBeadCloseSkippedWithoutMR covers the no-MR case:
// when the agent bead has no MRID (e.g., no-merge / direct / no-MR
// strategies), deferral must NOT trigger — those strategies don't go
// through the refinery's MR pipeline, so deferring would strand the bead.
func TestDoneDeferSourceBeadCloseSkippedWithoutMR(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown"}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog := filepath.Join(townRoot, "closes.log")
	updatesLog := filepath.Join(townRoot, "updates.log")

	// Stub: no MRID — polecat completed with a direct merge, no MR created.
	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nux)
        cat <<'JSON'
[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","description":"agent_state: working\nhook_bead: gt-source-3\nexit_type: COMPLETED"}]
JSON
        ;;
      gt-source-3)
        echo '[{"id":"gt-source-3","title":"Source bead","status":"hooked","assignee":"gastown/polecats/nux"}]'
        ;;
    esac
    ;;
  update)
    echo "$@" >> "%s"
    ;;
  close)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%s"
    done
    ;;
  agent|list|slot)
    exit 0
    ;;
esac
exit 0
`, updatesLog, closesLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GT_ROLE", "polecat")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "nux")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	updateAgentStateOnDone(
		filepath.Join(townRoot, "gastown"),
		townRoot,
		ExitCompleted,
		"gt-source-3",
	)

	// Source bead MUST be closed (no MR → no deferral).
	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("expected source bead gt-source-3 to be closed, but no closes logged: %v", err)
	}
	if !strings.Contains(string(closesBytes), "gt-source-3") {
		t.Errorf("expected source bead gt-source-3 in closes log, got:\n%s", string(closesBytes))
	}

	// Deferral helper must NOT have run.
	if _, err := os.Stat(updatesLog); err == nil {
		updatesBytes, _ := os.ReadFile(updatesLog)
		if strings.Contains(string(updatesBytes), "awaiting-merge-stamp:") {
			t.Errorf("deferral helper ran without an MR — update log:\n%s", string(updatesBytes))
		}
	}
}

// TestDoneDeferSourceBeadActiveMRMissing covers the gastown-5oy codex
// rework requirement: when CompletionMetadata.MRID is set with no failures
// but active_mr is MISSING (UpdateAgentActiveMR warn-only update failed,
// or MR discovered through idempotent/existing-MR path), deferral MUST
// still trigger using MRID. Without this, the deferral path silently
// regresses to bare bd.Close() on transient UpdateAgentActiveMR failures
// and the original generic-close bug returns.
//
// Per codex FAIL on the prior rework (gastown-wisp-0ia, ddba906c): the
// production mrContextForDeferral must key off CompletionMetadata, NOT
// active_mr. active_mr is best-effort traceability, never a precondition.
func TestDoneDeferSourceBeadActiveMRMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown"}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog := filepath.Join(townRoot, "closes.log")
	updatesLog := filepath.Join(townRoot, "updates.log")

	// Stub: CompletionMetadata.MRID set with no failures, but active_mr
	// is intentionally absent. Deferral MUST still trigger using MRID.
	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nux)
        cat <<'JSON'
[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","description":"agent_state: working\nhook_bead: gt-source-4\nexit_type: COMPLETED\nmr_id: gastown-wisp-noactivemr\nmr_failed: false\npush_failed: false"}]
JSON
        ;;
      gt-source-4)
        echo '[{"id":"gt-source-4","title":"Source bead","status":"hooked","assignee":"gastown/polecats/nux"}]'
        ;;
    esac
    ;;
  update)
    echo "$@" >> "%s"
    ;;
  close)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%s"
    done
    ;;
  agent|list|slot)
    exit 0
    ;;
esac
exit 0
`, updatesLog, closesLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GT_ROLE", "polecat")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "nux")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	updateAgentStateOnDone(
		filepath.Join(townRoot, "gastown"),
		townRoot,
		ExitCompleted,
		"gt-source-4",
	)

	// Source bead must NOT be closed (refinery owns the close).
	if _, err := os.Stat(closesLog); err == nil {
		closesBytes, _ := os.ReadFile(closesLog)
		if strings.Contains(string(closesBytes), "gt-source-4") {
			t.Errorf("source bead gt-source-4 was closed despite MRID being set with no failures — active_mr missing must not block deferral.\nCloses:\n%s", string(closesBytes))
		}
	}

	// Deferral helper must have run: awaiting-merge-stamp:<mr-id> label.
	updatesBytes, err := os.ReadFile(updatesLog)
	if err != nil {
		t.Fatalf("no updates recorded — deferral helper did not run despite MRID set: %v", err)
	}
	updates := string(updatesBytes)
	if !strings.Contains(updates, "awaiting-merge-stamp:gastown-wisp-noactivemr") {
		t.Errorf("expected deferral to use MRID even with active_mr missing. Updates:\n%s", updates)
	}
}

// TestDoneDeferSourceBeadActiveMRStale covers the gastown-5oy codex
// rework requirement: when CompletionMetadata.MRID is set with no failures
// but active_mr is MISMATCHED/STALE (warn-only UpdateAgentActiveMR wrote a
// previous MR id that no longer matches), deferral MUST still trigger
// using MRID (the authoritative signal). active_mr is best-effort
// traceability and drift must be logged, not enforced.
func TestDoneDeferSourceBeadActiveMRStale(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown"}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	closesLog := filepath.Join(townRoot, "closes.log")
	updatesLog := filepath.Join(townRoot, "updates.log")

	// Capture stderr so we can verify the drift is logged (not enforced).
	stderrLog := filepath.Join(townRoot, "stderr.log")

	// Stub: CompletionMetadata.MRID set with no failures, but active_mr
	// is stale (a previous MR id). Deferral MUST still trigger using MRID.
	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nux)
        cat <<'JSON'
[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","description":"agent_state: working\nhook_bead: gt-source-5\nactive_mr: gastown-wisp-stale\nexit_type: COMPLETED\nmr_id: gastown-wisp-current\nmr_failed: false\npush_failed: false"}]
JSON
        ;;
      gt-source-5)
        echo '[{"id":"gt-source-5","title":"Source bead","status":"hooked","assignee":"gastown/polecats/nux"}]'
        ;;
    esac
    ;;
  update)
    echo "$@" >> "%s"
    ;;
  close)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%s"
    done
    ;;
  agent|list|slot)
    exit 0
    ;;
esac
exit 0
`, updatesLog, closesLog)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GT_ROLE", "polecat")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "nux")
	t.Setenv("GT_CREW", "")
	t.Setenv("TMUX_PANE", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// Capture stderr to verify drift logging.
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = origStderr
	})

	updateAgentStateOnDone(
		filepath.Join(townRoot, "gastown"),
		townRoot,
		ExitCompleted,
		"gt-source-5",
	)

	// Stop capturing stderr and drain into the log file.
	_ = w.Close()
	stderrBytes, _ := io.ReadAll(r)
	if err := os.WriteFile(stderrLog, stderrBytes, 0644); err != nil {
		t.Fatalf("write stderr log: %v", err)
	}

	// Source bead must NOT be closed.
	if _, err := os.Stat(closesLog); err == nil {
		closesBytes, _ := os.ReadFile(closesLog)
		if strings.Contains(string(closesBytes), "gt-source-5") {
			t.Errorf("source bead gt-source-5 was closed despite MRID being set with no failures — stale active_mr must not block deferral.\nCloses:\n%s", string(closesBytes))
		}
	}

	// Deferral helper must have run using the authoritative MRID.
	updatesBytes, err := os.ReadFile(updatesLog)
	if err != nil {
		t.Fatalf("no updates recorded — deferral helper did not run despite MRID set: %v", err)
	}
	updates := string(updatesBytes)
	if !strings.Contains(updates, "awaiting-merge-stamp:gastown-wisp-current") {
		t.Errorf("expected deferral to use authoritative MRID when active_mr is stale. Updates:\n%s", updates)
	}
	// Critical: must NOT use the stale active_mr id.
	if strings.Contains(updates, "awaiting-merge-stamp:gastown-wisp-stale") {
		t.Errorf("deferral used stale active_mr instead of authoritative MRID. Updates:\n%s", updates)
	}

	// Drift should be logged on stderr (best-effort, not enforced).
	stderrStr := string(stderrBytes)
	if !strings.Contains(stderrStr, "active_mr=") || !strings.Contains(stderrStr, "does not match") {
		t.Errorf("expected drift log on stderr, got:\n%s", stderrStr)
	}
}

// TestMrContextForDeferral_Production drives the PRODUCTION mrContextForDeferral
// (not a mirrored helper) through the full matrix of inputs the gastown-5oy
// codex rework prompt enumerates. Calling the real function (rather than
// inlining its decision logic) catches drift between tests and production —
// the codex FAIL on the prior rework (gastown-wisp-0ia, ddba906c) was caused
// by a mirrored test helper that disagreed with production's mrContextForDeferral.
//
// The production function reads the agent bead via *beads.Beads.Show(), which
// shells out to `bd show --json`. We stub `bd` in PATH so Show() returns the
// synthesized description. mrContextForDeferral then runs end-to-end through
// ParseAgentFields + the production decision logic.
func TestMrContextForDeferral_Production(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "locks"), 0755); err != nil {
		t.Fatalf("mkdir .beads/locks: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0755); err != nil {
		t.Fatalf("mkdir gastown: %v", err)
	}
	routes := strings.Join([]string{
		`{"prefix":"gt-","path":"gastown"}`,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	tests := []struct {
		name      string
		desc      string
		exitType  string
		wantDefer bool
	}{
		{
			name:      "mr set, no failures, active_mr matches → defer",
			desc:      "agent_state: working\nactive_mr: gastown-wisp-1\nexit_type: COMPLETED\nmr_id: gastown-wisp-1\nmr_failed: false\npush_failed: false",
			exitType:  ExitCompleted,
			wantDefer: true,
		},
		{
			name:      "mr set, no failures, active_mr MISSING → defer (codex rework)",
			desc:      "agent_state: working\nexit_type: COMPLETED\nmr_id: gastown-wisp-1\nmr_failed: false\npush_failed: false",
			exitType:  ExitCompleted,
			wantDefer: true,
		},
		{
			name:      "mr set, no failures, active_mr STALE/MISMATCHED → defer using MRID (codex rework)",
			desc:      "agent_state: working\nactive_mr: gastown-wisp-stale\nexit_type: COMPLETED\nmr_id: gastown-wisp-current\nmr_failed: false\npush_failed: false",
			exitType:  ExitCompleted,
			wantDefer: true,
		},
		{
			name:      "mr set but mr_failed → skip",
			desc:      "agent_state: working\nactive_mr: gastown-wisp-1\nexit_type: COMPLETED\nmr_id: gastown-wisp-1\nmr_failed: true\npush_failed: false",
			exitType:  ExitCompleted,
			wantDefer: false,
		},
		{
			name:      "mr set but push_failed → skip",
			desc:      "agent_state: working\nactive_mr: gastown-wisp-1\nexit_type: COMPLETED\nmr_id: gastown-wisp-1\nmr_failed: false\npush_failed: true",
			exitType:  ExitCompleted,
			wantDefer: false,
		},
		{
			name:      "no mr_id → skip",
			desc:      "agent_state: working\nexit_type: COMPLETED",
			exitType:  ExitCompleted,
			wantDefer: false,
		},
		{
			name:      "active_mr set but mr_id missing → skip",
			desc:      "agent_state: working\nactive_mr: gastown-wisp-1\nexit_type: COMPLETED\nmr_failed: false\npush_failed: false",
			exitType:  ExitCompleted,
			wantDefer: false,
		},
		{
			name:      "exit deferred → skip",
			desc:      "agent_state: working\nactive_mr: gastown-wisp-1\nexit_type: DEFERRED\nmr_id: gastown-wisp-1\nmr_failed: false\npush_failed: false",
			exitType:  ExitDeferred,
			wantDefer: false,
		},
		{
			name:      "exit escalated → skip",
			desc:      "agent_state: working\nactive_mr: gastown-wisp-1\nexit_type: ESCALATED\nmr_id: gastown-wisp-1\nmr_failed: false\npush_failed: false",
			exitType:  ExitEscalated,
			wantDefer: false,
		},
		{
			name:      "exit empty → skip",
			desc:      "agent_state: working\nactive_mr: gastown-wisp-1\nmr_id: gastown-wisp-1\nmr_failed: false\npush_failed: false",
			exitType:  "",
			wantDefer: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build a bd stub that returns the synthesized description for
			// the agent bead id. JSON-encode the description so the
			// production parser sees an identical structure to a real
			// `bd show --json` call. Escape ORDER matters: backslashes
			// first (so a fresh newline-escape isn't double-escaped), then
			// newlines (real LF → two-char \n JSON escape), then quotes.
			escapedDesc := strings.ReplaceAll(tt.desc, "\\", "\\\\")
			escapedDesc = strings.ReplaceAll(escapedDesc, "\n", "\\n")
			escapedDesc = strings.ReplaceAll(escapedDesc, "\"", "\\\"")

			// Also write the invocation to /tmp so we can debug failures
			// even after t.TempDir cleanup.
			invLog := "/tmp/bd-invocations-matrix.log"
			_ = os.WriteFile(invLog, nil, 0644) // truncate

			// Write the JSON response to a temp file and have the stub
			// cat it. Avoids shell-quoting issues with multi-line content.
			jsonFile := filepath.Join(townRoot, "show-response.json")
			jsonBody := fmt.Sprintf(`[{"id":"gt-test-agent","title":"Test agent","status":"open","description":"%s"}]`, escapedDesc)
			if err := os.WriteFile(jsonFile, []byte(jsonBody), 0644); err != nil {
				t.Fatalf("write json response: %v", err)
			}

			bdScript := fmt.Sprintf(`#!/bin/sh
echo "INVOKE: $@" >> "%s"
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    cat "%s"
    ;;
  *)
    exit 0
    ;;
esac
exit 0
`, invLog, jsonFile)

			bdPath := filepath.Join(binDir, "bd")
			if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
				t.Fatalf("write bd stub: %v", err)
			}

			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("GT_ROLE", "polecat")
			t.Setenv("GT_RIG", "gastown")
			t.Setenv("GT_POLECAT", "nux")

			cwd, err := os.Getwd()
			if err != nil {
				t.Fatalf("getwd: %v", err)
			}
			t.Cleanup(func() { _ = os.Chdir(cwd) })
			if err := os.Chdir(filepath.Join(townRoot, "gastown")); err != nil {
				t.Fatalf("chdir: %v", err)
			}

			// Drive the PRODUCTION mrContextForDeferral function via a
			// real *beads.Beads. Beads.Isolated suppresses inherited
			// BD_ACTOR/BEADS_DB env so we hit our stub.
			agentBd := beads.NewIsolated(filepath.Join(townRoot, "gastown"))
			mrID, deferrable := mrContextForDeferral(agentBd, "gt-test-agent", tt.exitType)

			if deferrable != tt.wantDefer {
				t.Errorf("mrContextForDeferral defer decision: got %v, want %v (desc=%q exitType=%q)",
					deferrable, tt.wantDefer, tt.desc, tt.exitType)
			}
			// When deferring, mrID must match the authoritative MRID from
			// CompletionMetadata — never the stale/missing active_mr.
			if deferrable {
				fields := beads.ParseAgentFields(tt.desc)
				if fields == nil {
					t.Fatalf("ParseAgentFields returned nil")
				}
				if mrID != fields.MRID {
					t.Errorf("mrContextForDeferral mrID: got %q, want %q (CompletionMetadata.MRID)",
						mrID, fields.MRID)
				}
			}
		})
	}
}
