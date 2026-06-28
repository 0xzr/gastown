package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestMRContextForDeferral covers the deferral decision matrix. It drives the
// production helper directly (not a mirrored copy) so the contract is enforced
// by the same code run by gt done (gastown-5oy rework guidance).
func TestMRContextForDeferral(t *testing.T) {
	tests := []struct {
		name       string
		exitType   string
		mrID       string
		mrFailed   bool
		pushFailed bool
		want       bool
	}{
		{
			name:     "successful MR submission defers close",
			exitType: ExitCompleted,
			mrID:     "gastown-wisp-abc",
			want:     true,
		},
		{
			name:     "completed with no MR does not defer",
			exitType: ExitCompleted,
			mrID:     "",
			want:     false,
		},
		{
			name:       "completed with MR creation failure does not defer",
			exitType:   ExitCompleted,
			mrID:       "gastown-wisp-abc",
			mrFailed:   true,
			pushFailed: false,
			want:       false,
		},
		{
			name:       "completed with push failure does not defer",
			exitType:   ExitCompleted,
			mrID:       "gastown-wisp-abc",
			mrFailed:   false,
			pushFailed: true,
			want:       false,
		},
		{
			name:       "completed with both MR and push failures does not defer",
			exitType:   ExitCompleted,
			mrID:       "gastown-wisp-abc",
			mrFailed:   true,
			pushFailed: true,
			want:       false,
		},
		{
			name:     "escalated does not defer even with successful MR context",
			exitType: ExitEscalated,
			mrID:     "gastown-wisp-abc",
			want:     false,
		},
		{
			name:     "deferred exit does not defer close to refinery",
			exitType: ExitDeferred,
			mrID:     "gastown-wisp-abc",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mrContextForDeferral(tt.exitType, tt.mrID, tt.mrFailed, tt.pushFailed)
			if got != tt.want {
				t.Errorf("mrContextForDeferral(%q, %q, %v, %v) = %v, want %v",
					tt.exitType, tt.mrID, tt.mrFailed, tt.pushFailed, got, tt.want)
			}
		})
	}
}

// TestDoneDefersSourceBeadCloseToRefinery verifies that updateAgentStateOnDone
// leaves the source bead open when a successful MR was submitted, while still
// closing the attached molecule (wisp) so formula steps complete.
func TestDoneDefersSourceBeadCloseToRefinery(t *testing.T) {
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

	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nux)
        echo '[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","hook_bead":"gt-source-123","agent_state":"working"}]'
        ;;
      gt-source-123)
        echo '[{"id":"gt-source-123","title":"Source bead","status":"in_progress","description":"attached_molecule: gt-wisp-xyz"}]'
        ;;
      gt-wisp-xyz)
        echo '[{"id":"gt-wisp-xyz","title":"mol-polecat-work","status":"open","ephemeral":true}]'
        ;;
    esac
    ;;
  list)
    echo '[]'
    ;;
  close)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%s"
    done
    ;;
  agent|update|slot)
    exit 0
    ;;
esac
exit 0
`, closesLog)

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

	mrID := "gastown-wisp-abc"
	updateAgentStateOnDone(filepath.Join(townRoot, "gastown"), townRoot, ExitCompleted, "gt-source-123", mrID, false, false)

	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("failed to read close log: %v", err)
	}
	closes := string(closesBytes)

	if strings.Contains(closes, "gt-source-123") {
		t.Errorf("source bead gt-source-123 was closed when MR was successful; it should be deferred to Refinery\nCloses:\n%s", closes)
	}
	if !strings.Contains(closes, "gt-wisp-xyz") {
		t.Errorf("attached molecule gt-wisp-xyz was NOT closed; formula steps must still complete\nCloses:\n%s", closes)
	}
}

// TestDoneClosesSourceBeadOnNoMR verifies the non-deferral path: when no MR
// exists, the source bead is closed normally by gt done.
func TestDoneClosesSourceBeadOnNoMR(t *testing.T) {
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

	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nux)
        echo '[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","hook_bead":"gt-source-456","agent_state":"working"}]'
        ;;
      gt-source-456)
        echo '[{"id":"gt-source-456","title":"Source bead","status":"in_progress","description":"attached_molecule: gt-wisp-xyz"}]'
        ;;
      gt-wisp-xyz)
        echo '[{"id":"gt-wisp-xyz","title":"mol-polecat-work","status":"open","ephemeral":true}]'
        ;;
    esac
    ;;
  list)
    echo '[]'
    ;;
  close)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%s"
    done
    ;;
  agent|update|slot)
    exit 0
    ;;
esac
exit 0
`, closesLog)

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

	updateAgentStateOnDone(filepath.Join(townRoot, "gastown"), townRoot, ExitCompleted, "gt-source-456", "", false, false)

	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("failed to read close log: %v", err)
	}
	closes := string(closesBytes)

	if !strings.Contains(closes, "gt-source-456") {
		t.Errorf("source bead gt-source-456 was NOT closed when no MR existed\nCloses:\n%s", closes)
	}
	if !strings.Contains(closes, "gt-wisp-xyz") {
		t.Errorf("attached molecule gt-wisp-xyz was NOT closed\nCloses:\n%s", closes)
	}
}

// TestDoneClosesSourceBeadOnMRFailure verifies that an MR submission failure
// prevents deferral: gt done closes the source bead itself so it does not
// remain open waiting for a refinery merge that will not happen.
func TestDoneClosesSourceBeadOnMRFailure(t *testing.T) {
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

	bdScript := fmt.Sprintf(`#!/bin/sh
while [ "$1" = "--allow-stale" ]; do shift; done
cmd="$1"
shift || true
case "$cmd" in
  show)
    beadID="$1"
    case "$beadID" in
      gt-gastown-polecat-nux)
        echo '[{"id":"gt-gastown-polecat-nux","title":"Polecat nux","status":"open","hook_bead":"gt-source-789","agent_state":"working"}]'
        ;;
      gt-source-789)
        echo '[{"id":"gt-source-789","title":"Source bead","status":"in_progress","description":"attached_molecule: gt-wisp-xyz"}]'
        ;;
      gt-wisp-xyz)
        echo '[{"id":"gt-wisp-xyz","title":"mol-polecat-work","status":"open","ephemeral":true}]'
        ;;
    esac
    ;;
  list)
    echo '[]'
    ;;
  close)
    for arg in "$@"; do
      case "$arg" in --*) continue ;; esac
      echo "$arg" >> "%s"
    done
    ;;
  agent|update|slot)
    exit 0
    ;;
esac
exit 0
`, closesLog)

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

	updateAgentStateOnDone(filepath.Join(townRoot, "gastown"), townRoot, ExitCompleted, "gt-source-789", "gastown-wisp-abc", true, false)

	closesBytes, err := os.ReadFile(closesLog)
	if err != nil {
		t.Fatalf("failed to read close log: %v", err)
	}
	closes := string(closesBytes)

	if !strings.Contains(closes, "gt-source-789") {
		t.Errorf("source bead gt-source-789 was NOT closed when MR submission failed\nCloses:\n%s", closes)
	}
	if !strings.Contains(closes, "gt-wisp-xyz") {
		t.Errorf("attached molecule gt-wisp-xyz was NOT closed\nCloses:\n%s", closes)
	}
}
