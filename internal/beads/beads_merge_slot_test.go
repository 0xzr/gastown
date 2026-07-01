package beads

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installMockBDMergeSlotRecorder installs a mock bd binary whose behaviour is
// driven by the slot state in $MOCK_BD_SLOT_STATE_FILE:
//
//   - "create-ok"      : `bd create` succeeds and emits a JSON bead
//   - "create-dup"     : `bd create` fails with a duplicate-style error; `bd
//     list` and `bd show` then resolve to the existing slot
//     (the race that MergeSlotEnsureExists must survive).
//   - "create-fail"    : `bd create` fails with an unexpected error; the slot
//     still does NOT exist (MergeSlotCheck returns "not
//     found") so we must surface the create error.
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
    # Default: no merge slot exists.
    printf '[]\n'
    exit 0
    ;;
  show)
    if [ "$(cat "$STATE" 2>/dev/null)" = "create-dup" ]; then
      printf '[{"id":"gt-merge-slot-existing","title":"merge-slot","status":"open","description":"{}","labels":["gt:merge-slot"]}]\n'
      exit 0
    fi
    echo "error: issue not found" >&2
    exit 1
    ;;
  update)
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

// TestMergeSlotRelease_DoesNotPromoteWaiters verifies the retro-bug fix
// (from hq-5qyhj via gastown-b6s92): releasing the merge slot must NOT
// promote Waiters[0] to be the new holder. Unconditional promotion risks
// handing the slot to a waiter that is no longer live (process crashed,
// retry timed out, gave up), stranding the slot under a holder that never
// called Acquire. The correct behavior is to clear the holder and leave
// the waiters queue intact so the next Acquire — from a current waiter or
// a new requestor — can take the slot legitimately.
func TestMergeSlotRelease_DoesNotPromoteWaiters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock for bd")
	}

	// Use the existing mock framework but extend the script: the
	// "release-with-data" state makes the slot appear pre-seeded with a
	// holder and two waiters in its Description, so MergeSlotRelease
	// sees a non-empty Waiters list and exercises the promotion path
	// (which we want to confirm is now a no-op). The mock also captures
	// the latest --description written via `bd update` so we can verify
	// the post-release state without rerunning show (the mock's bd
	// doesn't persist writes itself).
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	stateFile := filepath.Join(binDir, "bd.state")
	writeFile := filepath.Join(binDir, "bd.last_desc")

	// The Description is embedded as a JSON string and the inner double
	// quotes must be escaped so the resulting shell heredoc-style printf
	// produces a valid JSON object for `bd show`.
	script := `#!/bin/sh
LOG="$MOCK_BD_LOG"
STATE="$MOCK_BD_SLOT_STATE_FILE"
WRITE_FILE='` + writeFile + `'
printf 'args=%s\n' "$*" >> "$LOG"

cmd=""
for arg in "$@"; do
  case "$arg" in --*) ;; *) cmd="$arg"; break ;; esac
done

state_val="$(cat "$STATE" 2>/dev/null)"

case "$cmd" in
  init|config|migrate)
    exit 0
    ;;
  create)
    if [ "$state_val" = "create-dup" ]; then
      echo "error: issue with label gt:merge-slot already exists" >&2
      exit 1
    fi
    printf '{"id":"gt-merge-slot-test","title":"merge-slot","status":"open","labels":["gt:merge-slot"]}\n'
    exit 0
    ;;
  list)
    if [ "$state_val" = "release-with-data" ]; then
      printf '[{"id":"gt-merge-slot-test","title":"merge-slot","status":"open","labels":["gt:merge-slot"]}]\n'
      exit 0
    fi
    printf '[]\n'
    exit 0
    ;;
  show)
    if [ "$state_val" = "release-with-data" ]; then
      printf '%s\n' '[{"id":"gt-merge-slot-test","title":"merge-slot","status":"open","description":"{\"holder\":\"rigA/refinery\",\"waiters\":[\"w1\",\"w2\"]}","labels":["gt:merge-slot"]}]'
      exit 0
    fi
    echo "error: issue not found" >&2
    exit 1
    ;;
  update)
    # Capture --description=... so the test can verify what was written.
    for arg in "$@"; do
      case "$arg" in
        --description=*)
          printf '%s' "${arg#--description=}" > "$WRITE_FILE"
          ;;
      esac
    done
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
	setMockBDState(t, stateFile, "release-with-data")

	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	bd := NewIsolated(tmpDir)

	// Pre-flight: MergeSlotCheck observes the seeded state with holder
	// and two waiters. This confirms our mock is wired correctly.
	pre, err := bd.MergeSlotCheck()
	if err != nil {
		t.Fatalf("MergeSlotCheck (pre): %v", err)
	}
	if pre.Holder != "rigA/refinery" || len(pre.Waiters) != 2 ||
		pre.Waiters[0] != "w1" || pre.Waiters[1] != "w2" {
		t.Fatalf("mock setup invalid: holder=%q waiters=%#v", pre.Holder, pre.Waiters)
	}

	// The actual assertion: releasing the slot must clear the holder
	// and preserve the waiters queue. It must NOT promote Waiters[0]
	// into the holder field.
	if err := bd.MergeSlotRelease("rigA/refinery"); err != nil {
		t.Fatalf("MergeSlotRelease: %v", err)
	}

	// Verify the post-release state by reading what was written to
	// `bd update`. The mock captures --description=..., so we can
	// inspect the exact JSON MergeSlotRelease persisted.
	written, err := os.ReadFile(writeFile)
	if err != nil {
		t.Fatalf("read captured description: %v", err)
	}
	var post struct {
		Holder  string   `json:"holder"`
		Waiters []string `json:"waiters"`
	}
	if err := json.Unmarshal(written, &post); err != nil {
		t.Fatalf("decode captured slot data: %v (raw=%q)", err, written)
	}

	if post.Holder != "" {
		t.Errorf("holder must be empty after release; got %q (would mean Waiters[0] was promoted)", post.Holder)
	}
	if len(post.Waiters) != 2 {
		t.Fatalf("waiters must be preserved (len=2); got %#v", post.Waiters)
	}
	if post.Waiters[0] != "w1" || post.Waiters[1] != "w2" {
		t.Errorf("waiters order must be unchanged; got %#v", post.Waiters)
	}
}
