package beads

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installMockBDMergeSlotRecorder installs a mock bd binary whose behaviour is
// driven by the slot state in $MOCK_BD_SLOT_STATE_FILE:
//
//   - "create-ok"       : `bd create` succeeds and emits a JSON bead
//   - "create-dup"      : `bd create` fails with a duplicate-style error; `bd
//     list` and `bd show` then resolve to the existing slot
//     (the race that MergeSlotEnsureExists must survive).
//   - "create-fail"     : `bd create` fails with an unexpected error; the slot
//     still does NOT exist (MergeSlotCheck returns "not
//     found") so we must surface the create error.
//   - "held-update-fail": slot already exists, held by another actor, and
//     `bd update` fails. Exercises the waiter-add branch of
//     MergeSlotAcquire (gastown-3y5kz).
func installMockBDMergeSlotRecorder(t *testing.T) (logPath, stateFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock for bd")
	}

	binDir := t.TempDir()
	logPath = filepath.Join(binDir, "bd.log")
	stateFile = filepath.Join(binDir, "bd.state")

	script := `#!/bin/sh
LOG="$MOCK_BD_LOG"
STATE="$MOCK_BD_SLOT_STATE_FILE"
printf 'args=%s\n' "$*" >> "$LOG"

cmd=""
for arg in "$@"; do
  case "$arg" in --*) ;; *) cmd="$arg"; break ;; esac
done

case "$cmd" in
  init|config|migrate)
    exit 0
    ;;
  create)
    if [ "$(cat "$STATE" 2>/dev/null)" = "create-dup" ]; then
      # Race: slot already exists (e.g. another caller created it). bd fails.
      echo "error: issue with label gt:merge-slot already exists" >&2
      exit 1
    fi
    if [ "$(cat "$STATE" 2>/dev/null)" = "create-fail" ]; then
      # Real failure (e.g. Dolt hiccup). Slot will not exist after.
      echo "error: dolt unavailable" >&2
      exit 1
    fi
    printf '{"id":"gt-merge-slot-test","title":"merge-slot","status":"open","labels":["gt:merge-slot"]}\n'
    exit 0
    ;;
  list)
    if [ "$(cat "$STATE" 2>/dev/null)" = "create-dup" ]; then
      # Slot exists because another caller created it.
      printf '[{"id":"gt-merge-slot-existing","title":"merge-slot","status":"open","labels":["gt:merge-slot"]}]\n'
      exit 0
    fi
    if [ "$(cat "$STATE" 2>/dev/null)" = "held-update-fail" ]; then
      # Slot exists and is held by another actor — exercises the waiter-add
      # branch in MergeSlotAcquire (gastown-3y5kz).
      printf '[{"id":"gt-merge-slot-existing","title":"merge-slot","status":"open","labels":["gt:merge-slot"]}]\n'
      exit 0
    fi
    # Default: no merge slot exists.
    printf '[]\n'
    exit 0
    ;;
  show)
    if [ "$(cat "$STATE" 2>/dev/null)" = "create-dup" ]; then
      printf '[{"id":"gt-merge-slot-existing","title":"merge-slot","status":"open","description":"{}","labels":["gt:merge-slot"]}]\n'
      exit 0
    fi
    if [ "$(cat "$STATE" 2>/dev/null)" = "held-update-fail" ]; then
      printf '[{"id":"gt-merge-slot-existing","title":"merge-slot","status":"open","description":"{\"holder\":\"other-actor\",\"waiters\":[]}","labels":["gt:merge-slot"]}]\n'
      exit 0
    fi
    echo "error: issue not found" >&2
    exit 1
    ;;
  update)
    if [ "$(cat "$STATE" 2>/dev/null)" = "held-update-fail" ]; then
      echo "error: dolt unavailable" >&2
      exit 1
    fi
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MOCK_BD_LOG", logPath)
	t.Setenv("MOCK_BD_SLOT_STATE_FILE", stateFile)
	return logPath, stateFile
}

func setMockBDState(t *testing.T, stateFile, value string) {
	t.Helper()
	if err := os.WriteFile(stateFile, []byte(value), 0644); err != nil {
		t.Fatalf("set mock bd state: %v", err)
	}
}

// TestMergeSlotEnsureExists_FreshCreate covers the cold-path: the slot does
// not yet exist, so Create succeeds and EnsureExists returns the new ID.
func TestMergeSlotEnsureExists_FreshCreate(t *testing.T) {
	_, stateFile := installMockBDMergeSlotRecorder(t)
	setMockBDState(t, stateFile, "create-ok")

	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	bd := NewIsolated(tmpDir)
	id, err := bd.MergeSlotEnsureExists()
	if err != nil {
		t.Fatalf("MergeSlotEnsureExists (fresh): %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty slot ID")
	}
}

// TestMergeSlotEnsureExists_CreateLosesRaceThenFallsBack covers the race:
// two callers both attempt Create. The first wins; the second's Create fails
// with a duplicate error. EnsureExists must fall back to a lookup and return
// the existing slot's ID, not surface the create error.
func TestMergeSlotEnsureExists_CreateLosesRaceThenFallsBack(t *testing.T) {
	_, stateFile := installMockBDMergeSlotRecorder(t)
	setMockBDState(t, stateFile, "create-dup")

	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	bd := NewIsolated(tmpDir)
	id, err := bd.MergeSlotEnsureExists()
	if err != nil {
		t.Fatalf("MergeSlotEnsureExists (race): %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty slot ID after race fallback")
	}
}

// TestMergeSlotEnsureExists_CreateFailsAndSlotMissing covers the failure
// case: Create fails for a non-race reason (e.g. Dolt unavailable) AND the
// slot still does not exist. EnsureExists must return a wrapped error
// containing the create failure, not silently return "".
func TestMergeSlotEnsureExists_CreateFailsAndSlotMissing(t *testing.T) {
	_, stateFile := installMockBDMergeSlotRecorder(t)
	setMockBDState(t, stateFile, "create-fail")

	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	bd := NewIsolated(tmpDir)
	id, err := bd.MergeSlotEnsureExists()
	if err == nil {
		t.Fatalf("expected error when Create fails and slot is missing, got id=%q", id)
	}
	if id != "" {
		t.Errorf("expected empty ID on error, got %q", id)
	}
	if !strings.Contains(err.Error(), "dolt unavailable") {
		t.Errorf("expected error to wrap create failure, got: %v", err)
	}
}

// TestMergeSlotAcquire_AddWaiterUpdateFails_SurfacesError is the regression
// test for gastown-3y5kz: MergeSlotAcquire's waiter-add branch previously
// discarded the b.Update error with `_ =`. If the update failed, the caller
// would see a nil error despite the waiter not being persisted, silently
// losing waiters from the queue.
//
// With the fix, MergeSlotAcquire must return a wrapped error when the
// waiter-add Update fails, so callers can surface the failure (e.g. log + retry)
// instead of believing they were added to the queue.
func TestMergeSlotAcquire_AddWaiterUpdateFails_SurfacesError(t *testing.T) {
	_, stateFile := installMockBDMergeSlotRecorder(t)
	setMockBDState(t, stateFile, "held-update-fail")

	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	bd := NewIsolated(tmpDir)
	// Acquire as a different actor than the holder ("other-actor") so we
	// enter the "slot held by someone else" branch with addWaiter=true.
	status, err := bd.MergeSlotAcquire("test-actor", true)
	if err == nil {
		t.Fatalf("expected error when waiter-add Update fails, got status=%+v", status)
	}
	if !strings.Contains(err.Error(), "adding merge slot waiter") {
		t.Errorf("expected error to mention 'adding merge slot waiter', got: %v", err)
	}
	if !strings.Contains(err.Error(), "dolt unavailable") {
		t.Errorf("expected error to wrap underlying bd error, got: %v", err)
	}
	// Caller must not receive a misleading "I added you to the waiters"
	// status on failure.
	if status != nil {
		t.Errorf("expected nil status on waiter-add failure, got %+v", status)
	}
}
