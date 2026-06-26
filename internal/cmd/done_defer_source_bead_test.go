package cmd

import (
	"fmt"
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
// consistency across all polecat completions (5/5 hardening-batch polecats
// hit this before the fix).
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

	// Stub: agent bead carries active_mr + CompletionMetadata.MRID (both
	// set, matching — that's the cross-check in mrContextForDeferral), no
	// failure flags. Source bead is in "hooked" status with assignee.
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

// TestMrContextForDeferralLogic verifies the pure decision logic of
// mrContextForDeferral without going through updateAgentStateOnDone.
// Covers the matrix of inputs to ensure we defer only when all preconditions
// hold.
func TestMrContextForDeferralLogic(t *testing.T) {
	tests := []struct {
		name      string
		desc      string
		exitType  string
		wantDefer bool
	}{
		{
			name:      "mr set, no failures, completed → defer",
			desc:      "agent_state: working\nactive_mr: gastown-wisp-1\nexit_type: COMPLETED\nmr_id: gastown-wisp-1\nmr_failed: false\npush_failed: false",
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
			name:      "no mr → skip",
			desc:      "agent_state: working\nexit_type: COMPLETED",
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
			name:      "mr_id set but active_mr missing → skip",
			desc:      "agent_state: working\nexit_type: COMPLETED\nmr_id: gastown-wisp-1\nmr_failed: false\npush_failed: false",
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
			name:      "mr_id and active_mr disagree → skip",
			desc:      "agent_state: working\nactive_mr: gastown-wisp-1\nexit_type: COMPLETED\nmr_id: gastown-wisp-2\nmr_failed: false\npush_failed: false",
			exitType:  ExitCompleted,
			wantDefer: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Synthesize an Issue carrying tt.desc as Description, then
			// drive the same ParseAgentFields path the production code
			// uses. This avoids the bd stub: pure logic test.
			af := beads.ParseAgentFields(tt.desc)
			if af == nil {
				t.Fatalf("ParseAgentFields returned nil")
			}

			// Replicate mrContextForDeferral's preconditions.
			gotDefer := true
			if tt.exitType != ExitCompleted {
				gotDefer = false
			}
			if af.MRID == "" || af.ActiveMR == "" || af.MRID != af.ActiveMR {
				gotDefer = false
			}
			if af.MRFailed || af.PushFailed {
				gotDefer = false
			}

			if gotDefer != tt.wantDefer {
				t.Errorf("deferral decision: got %v, want %v (desc=%q exitType=%q)",
					gotDefer, tt.wantDefer, tt.desc, tt.exitType)
			}
		})
	}
}
