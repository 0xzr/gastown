package beads

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseMergeSlotData(t *testing.T) {
	tests := []struct {
		name    string
		desc    string
		want    mergeSlotData
		wantErr bool
	}{
		{
			name: "empty description returns zero value",
			desc: "",
			want: mergeSlotData{},
		},
		{
			name: "valid json parses holder and waiters",
			desc: `{"holder":"warboy","waiters":["capable","furiosa"]}`,
			want: mergeSlotData{
				Holder:  "warboy",
				Waiters: []string{"capable", "furiosa"},
			},
		},
		{
			name:    "invalid json returns error",
			desc:    `{"holder":"warboy"`,
			wantErr: true,
		},
		{
			name:    "non-json description returns error",
			desc:    "this is not json",
			wantErr: true,
		},
		{
			name:    "json with wrong types returns error",
			desc:    `{"holder":123}`,
			wantErr: true,
		},
		{
			name: "valid empty json object returns zero holder",
			desc: `{}`,
			want: mergeSlotData{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issue := &Issue{ID: "gt-slot", Description: tc.desc}
			got, err := parseMergeSlotData(issue)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseMergeSlotData() error = nil, wantErr true")
				}
				if !strings.Contains(err.Error(), "parsing merge slot data") {
					t.Errorf("parseMergeSlotData() error = %q, want it to contain 'parsing merge slot data'", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMergeSlotData() unexpected error: %v", err)
			}
			if got.Holder != tc.want.Holder {
				t.Errorf("Holder = %q, want %q", got.Holder, tc.want.Holder)
			}
			if len(got.Waiters) != len(tc.want.Waiters) {
				t.Errorf("len(Waiters) = %d, want %d", len(got.Waiters), len(tc.want.Waiters))
			} else {
				for i := range got.Waiters {
					if got.Waiters[i] != tc.want.Waiters[i] {
						t.Errorf("Waiters[%d] = %q, want %q", i, got.Waiters[i], tc.want.Waiters[i])
					}
				}
			}
		})
	}
}

func TestMergeSlotStatusFromIssue(t *testing.T) {
	t.Run("valid description", func(t *testing.T) {
		issue := &Issue{ID: "gt-slot", Description: `{"holder":"warboy","waiters":["capable"]}`}
		status := mergeSlotStatusFromIssue(issue)
		if status.ID != "gt-slot" {
			t.Errorf("ID = %q, want %q", status.ID, "gt-slot")
		}
		if status.Available {
			t.Error("Available = true, want false")
		}
		if status.Holder != "warboy" {
			t.Errorf("Holder = %q, want %q", status.Holder, "warboy")
		}
		if status.Error != "" {
			t.Errorf("Error = %q, want empty", status.Error)
		}
	})

	t.Run("empty description reports available", func(t *testing.T) {
		issue := &Issue{ID: "gt-slot", Description: ""}
		status := mergeSlotStatusFromIssue(issue)
		if !status.Available {
			t.Error("Available = false, want true")
		}
		if status.Error != "" {
			t.Errorf("Error = %q, want empty", status.Error)
		}
	})

	t.Run("corrupt description exposes parse error", func(t *testing.T) {
		issue := &Issue{ID: "gt-slot", Description: `{"holder":"warboy"`}
		status := mergeSlotStatusFromIssue(issue)
		if status.Error == "" {
			t.Fatal("Error = empty, want parse error")
		}
		if !strings.Contains(status.Error, "parsing merge slot data") {
			t.Errorf("Error = %q, want it to contain 'parsing merge slot data'", status.Error)
		}
		if status.Available {
			t.Error("Available = true on corrupt data, want false")
		}
	})
}

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
//   - "create-corrupt" : `bd create` fails with a duplicate-style error; `bd
//     show` then resolves to an existing slot with corrupt JSON
//     description (so MergeSlotEnsureExists surfaces the parse error).
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
    if [ "$(cat "$STATE" 2>/dev/null)" = "create-dup" ] || [ "$(cat "$STATE" 2>/dev/null)" = "create-corrupt" ]; then
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
    if [ "$(cat "$STATE" 2>/dev/null)" = "create-dup" ] || [ "$(cat "$STATE" 2>/dev/null)" = "create-corrupt" ]; then
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
    if [ "$(cat "$STATE" 2>/dev/null)" = "create-corrupt" ]; then
      # Slot exists but its stored description is not valid JSON.
      printf '[{"id":"gt-merge-slot-existing","title":"merge-slot","status":"open","description":"{not-json","labels":["gt:merge-slot"]}]\n'
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

// TestMergeSlotEnsureExists_CreateFailsAndSlotCorrupt covers the case where
// Create fails because the slot already exists, but the existing slot's
// description is not valid JSON. EnsureExists must surface the parse error
// instead of returning the corrupt slot ID.
func TestMergeSlotEnsureExists_CreateFailsAndSlotCorrupt(t *testing.T) {
	_, stateFile := installMockBDMergeSlotRecorder(t)
	setMockBDState(t, stateFile, "create-corrupt")

	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	bd := NewIsolated(tmpDir)
	id, err := bd.MergeSlotEnsureExists()
	if err == nil {
		t.Fatalf("expected error when existing slot has corrupt data, got id=%q", id)
	}
	if id != "" {
		t.Errorf("expected empty ID on error, got %q", id)
	}
	if !strings.Contains(err.Error(), "corrupt merge slot data") {
		t.Errorf("expected error to mention corrupt merge slot data, got: %v", err)
	}
	if !strings.Contains(err.Error(), "parsing merge slot data") {
		t.Errorf("expected error to wrap parse error, got: %v", err)
	}
}
