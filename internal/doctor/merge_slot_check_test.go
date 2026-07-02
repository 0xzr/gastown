package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installMergeSlotMockBD installs a mock bd binary and returns paths used by
// the per-rig state and log inspection. The mock's behaviour is driven by:
//
//   - MOCK_BD_RIG_ROOT    : directory of per-rig state files (one per rig,
//     contents are one of "valid" / "corrupt" / "absent"
//     / "closed-corrupt" / "dupe-open").
//   - MOCK_BD_LOG         : log file capturing every invocation + the
//     --description that Fix passes to `bd update`.
//
// Both are wired via t.Setenv so subsequent test code can mutate either
// fixture without reinstalling the mock.
func installMergeSlotMockBD(t *testing.T) (rigRoot, logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock for bd")
	}

	binDir := t.TempDir()
	logPath = filepath.Join(binDir, "bd.log")
	rigRoot = t.TempDir()

	// Mock bd. Resolves the rig name from the working directory so the
	// doctor check can locate the per-rig state file the test wrote.
	script := `#!/bin/sh
LOG="$MOCK_BD_LOG"
RIG_ROOT="$MOCK_BD_RIG_ROOT"
printf 'args=%s\n' "$*" >> "$LOG"

cmd=""
for arg in "$@"; do
  case "$arg" in --*) ;; *) cmd="$arg"; break ;; esac
done

workdir="$(pwd)"
rigname="$(basename "$workdir")"
state="$RIG_ROOT/$rigname"

case "$cmd" in
  init|config|migrate)
    exit 0
    ;;
  list)
    case "$(cat "$state" 2>/dev/null)" in
      absent|"")
        printf '[]\n'
        ;;
      closed-corrupt)
        printf '[{"id":"%s-slot","title":"merge-slot","status":"closed","description":"{not-json","labels":["gt:merge-slot"]}]\n' "$rigname"
        ;;
      dupe-open)
        printf '[{"id":"%s-slot-a","title":"merge-slot","status":"open","description":"{not-json","labels":["gt:merge-slot"]},{"id":"%s-slot-b","title":"merge-slot","status":"open","description":"{}","labels":["gt:merge-slot"]}]\n' "$rigname" "$rigname"
        ;;
      valid)
        printf '[{"id":"%s-slot","title":"merge-slot","status":"open","description":"{\"holder\":\"warboy\",\"waiters\":[\"capable\"]}","labels":["gt:merge-slot"]}]\n' "$rigname"
        ;;
      corrupt)
        printf '[{"id":"%s-slot","title":"merge-slot","status":"open","description":"{not-json","labels":["gt:merge-slot"]}]\n' "$rigname"
        ;;
    esac
    exit 0
    ;;
  show)
    case "$(cat "$state" 2>/dev/null)" in
      valid)
        # Show returns valid JSON so the parse on Fix's verification passes
        # when the operator has repaired the slot out-of-band.
        printf '[{"id":"%s-slot","title":"merge-slot","status":"open","description":"{\"holder\":\"\"}","labels":["gt:merge-slot"]}]\n' "$rigname"
        ;;
      corrupt)
        printf '[{"id":"%s-slot","title":"merge-slot","status":"open","description":"{not-json","labels":["gt:merge-slot"]}]\n' "$rigname"
        ;;
      *)
        echo "error: issue not found" >&2
        exit 1
        ;;
    esac
    exit 0
    ;;
  update)
    desc=""
    for arg in "$@"; do
      case "$arg" in
        --description=*) desc="${arg#--description=}";;
      esac
    done
    printf 'update_desc=%s\n' "$desc" >> "$LOG"
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
	t.Setenv("MOCK_BD_RIG_ROOT", rigRoot)
	return rigRoot, logPath
}

// setMergeSlotRigState writes the per-rig state consumed by the mock bd.
func setMergeSlotRigState(t *testing.T, rigRoot, rigName, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(rigRoot, rigName), []byte(value), 0644); err != nil {
		t.Fatalf("set mock bd rig state %s: %v", rigName, err)
	}
}

// makeTownForMergeSlotTest builds a minimal town root with one registered
// rig, returning the town root and rig directory.
func makeTownForMergeSlotTest(t *testing.T, rigName string) (string, string) {
	t.Helper()
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, rigName)
	if err := os.MkdirAll(filepath.Join(rigDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir rig/.beads: %v", err)
	}
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	rigsContent := `{"version":1,"rigs":{"` + rigName + `":{"git_url":"file:///dev/null","added_at":"2026-07-01T00:00:00Z"}}}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsContent), 0644); err != nil {
		t.Fatalf("write rigs.json: %v", err)
	}
	return townRoot, rigDir
}

// chdirToRig enters the rig directory (mock bd reads PWD to discover the
// rig name). Restored via t.Cleanup.
func chdirToRig(t *testing.T, rigDir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(rigDir); err != nil {
		t.Fatalf("chdir rig: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

// corruptMatches filters Details lines to those describing a corrupt slot.
func corruptMatches(details []string) []string {
	var out []string
	for _, d := range details {
		if strings.Contains(d, "corrupt Description") {
			out = append(out, d)
		}
	}
	return out
}

func TestNewMergeSlotIntegrityCheck_Smoke(t *testing.T) {
	check := NewMergeSlotIntegrityCheck()
	if check.Name() != "merge-slot-integrity" {
		t.Errorf("Name() = %q, want %q", check.Name(), "merge-slot-integrity")
	}
	if check.Description() == "" {
		t.Error("Description() should be non-empty")
	}
	if !check.CanFix() {
		t.Error("CanFix() should return true (this check has a Fix method)")
	}
	if check.Category() != CategoryRig {
		t.Errorf("Category() = %q, want %q", check.Category(), CategoryRig)
	}
}

func TestParseMergeSlotDescription(t *testing.T) {
	cases := []struct {
		name    string
		desc    string
		wantErr bool
	}{
		{name: "empty description is valid", desc: ""},
		{name: "valid JSON parses", desc: `{"holder":"warboy","waiters":["capable","furiosa"]}`},
		{name: "truncated JSON errors", desc: `{"holder":"warboy"`, wantErr: true},
		{name: "non-JSON errors", desc: "this is not json", wantErr: true},
		{name: "wrong types error", desc: `{"holder":123}`, wantErr: true},
		{name: "empty object is valid", desc: `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseMergeSlotDescription(tc.desc)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), "parsing merge slot data") {
					t.Errorf("error = %q, want substring 'parsing merge slot data'", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestMergeSlotIntegrityCheck_NoRigs(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}

	check := NewMergeSlotIntegrityCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("Status = %v, want StatusOK", result.Status)
	}
	if !strings.Contains(result.Message, "No rigs") {
		t.Errorf("Message = %q, want substring 'No rigs'", result.Message)
	}
}

func TestMergeSlotIntegrityCheck_ValidSlot(t *testing.T) {
	rigRoot, _ := installMergeSlotMockBD(t)
	setMergeSlotRigState(t, rigRoot, "omega", "valid")

	townRoot, rigDir := makeTownForMergeSlotTest(t, "omega")
	chdirToRig(t, rigDir)

	check := NewMergeSlotIntegrityCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("Status = %v, want StatusOK. Message=%s Details=%v",
			result.Status, result.Message, result.Details)
	}
}

func TestMergeSlotIntegrityCheck_CorruptSlot(t *testing.T) {
	rigRoot, _ := installMergeSlotMockBD(t)
	setMergeSlotRigState(t, rigRoot, "omega", "corrupt")

	townRoot, rigDir := makeTownForMergeSlotTest(t, "omega")
	chdirToRig(t, rigDir)

	check := NewMergeSlotIntegrityCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Errorf("Status = %v, want StatusError. Message=%s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "1 rig") {
		t.Errorf("Message = %q, want substring '1 rig'", result.Message)
	}
	matches := corruptMatches(result.Details)
	if len(matches) != 1 {
		t.Errorf("Details has %d corrupt matches, want 1: %v", len(matches), result.Details)
	}
	if len(check.affectedSlots) != 1 {
		t.Errorf("affectedSlots len = %d, want 1", len(check.affectedSlots))
	}
}

func TestMergeSlotIntegrityCheck_AbsentSlotIsOK(t *testing.T) {
	// Lazy creation: a rig without a merge-slot bead at all is fine.
	rigRoot, _ := installMergeSlotMockBD(t)
	setMergeSlotRigState(t, rigRoot, "omega", "absent")

	townRoot, rigDir := makeTownForMergeSlotTest(t, "omega")
	chdirToRig(t, rigDir)

	check := NewMergeSlotIntegrityCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("Status = %v, want StatusOK. Message=%s Details=%v",
			result.Status, result.Message, result.Details)
	}
}

func TestMergeSlotIntegrityCheck_ClosedCorruptSlotIsIgnored(t *testing.T) {
	// A closed slot with a corrupt Description is a tombstone (left over
	// from a previous close+recreate recovery). It is NOT the active slot,
	// so we must not flag it.
	rigRoot, _ := installMergeSlotMockBD(t)
	setMergeSlotRigState(t, rigRoot, "omega", "closed-corrupt")

	townRoot, rigDir := makeTownForMergeSlotTest(t, "omega")
	chdirToRig(t, rigDir)

	check := NewMergeSlotIntegrityCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("Status = %v, want StatusOK. Message=%s Details=%v",
			result.Status, result.Message, result.Details)
	}
}

func TestMergeSlotIntegrityCheck_DuplicateOpenSlotsFlagged(t *testing.T) {
	rigRoot, _ := installMergeSlotMockBD(t)
	setMergeSlotRigState(t, rigRoot, "omega", "dupe-open")

	townRoot, rigDir := makeTownForMergeSlotTest(t, "omega")
	chdirToRig(t, rigDir)

	check := NewMergeSlotIntegrityCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Errorf("Status = %v, want StatusError. Message=%s Details=%v",
			result.Status, result.Message, result.Details)
	}
	// First slot is corrupt (added to affectedSlots); second open slot is
	// reported as a duplicate but NOT added to affectedSlots (Fix would
	// only repair the canonical first one).
	if len(check.affectedSlots) != 1 {
		t.Errorf("affectedSlots len = %d, want 1 (Fix targets the canonical slot only)", len(check.affectedSlots))
	}
	var sawDupe bool
	for _, d := range result.Details {
		if strings.Contains(d, "multiple open") {
			sawDupe = true
		}
	}
	if !sawDupe {
		t.Errorf("Details should include duplicate-label report, got: %v", result.Details)
	}
}

func TestMergeSlotIntegrityCheck_FixRewritesCorruptSlot(t *testing.T) {
	rigRoot, logPath := installMergeSlotMockBD(t)
	setMergeSlotRigState(t, rigRoot, "omega", "corrupt")

	townRoot, rigDir := makeTownForMergeSlotTest(t, "omega")
	chdirToRig(t, rigDir)

	check := NewMergeSlotIntegrityCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	if result := check.Run(ctx); result.Status != StatusError {
		t.Fatalf("Run Status = %v, want StatusError", result.Status)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix error: %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(logData), "update_desc="+mergeSlotEmptyDescription) {
		t.Errorf("Fix did not invoke bd update with empty payload. log:\n%s", logData)
	}
}

func TestMergeSlotIntegrityCheck_FixSkipsAlreadyRepaired(t *testing.T) {
	// When the operator repairs the slot out-of-band between Run and Fix,
	// the Show in Fix will see valid JSON and we must skip rather than
	// overwrite. (Mock's "show" branch returns the valid payload when the
	// rig state is "valid" — but we wired that via a separate rig name so
	// the Run's affectedSlots entry still references "omega". To exercise
	// the skip path, point the bd mock at a state where list+show both
	// return valid JSON.)
	rigRoot, logPath := installMergeSlotMockBD(t)
	// For Run we need corrupt (so it's added to affectedSlots). For Show
	// we need the same rig to be valid. Mock currently keys state off
	// rig name only; simulate operator-repaired by making the rig "valid"
	// before Fix.
	setMergeSlotRigState(t, rigRoot, "omega", "valid")

	townRoot, rigDir := makeTownForMergeSlotTest(t, "omega")
	chdirToRig(t, rigDir)

	check := NewMergeSlotIntegrityCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	// Inject a synthetic affected slot so Fix has something to consider.
	check.affectedSlots = []mergeSlotIntegrityAffected{{
		rigName:  "omega",
		rigPath:  rigDir,
		slotID:   "omega-slot",
		parseErr: "synthetic",
	}}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix error: %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	// Should NOT have called update because the slot was already valid.
	if strings.Contains(string(logData), "update_desc=") {
		t.Errorf("Fix should have skipped a no-longer-corrupt slot. log:\n%s", logData)
	}
}

func TestMergeSlotIntegrityCheck_FixWithoutRunIsNoOp(t *testing.T) {
	// If Fix is called without a preceding Run (and no state is set), it
	// must run Run first; if no corruption is detected, Fix is a no-op.
	townRoot, _ := makeTownForMergeSlotTest(t, "omega")

	check := NewMergeSlotIntegrityCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	// Without a mock bd on PATH the run will fail; instead drive Run
	// manually with a clean state via the rigRoot pattern below.
	rigRoot, logPath := installMergeSlotMockBD(t)
	setMergeSlotRigState(t, rigRoot, "omega", "valid")

	// Now from the rigDir cwd, Run sees a valid slot. But we want to test
	// the no-op path — call Fix without any prior Run state.
	_ = rigRoot
	_ = logPath

	// First, change into the rig's .beads so bd can find it (the mock bd
	// keys off basename of pwd).
	beadsDir := filepath.Join(townRoot, "omega", ".beads")
	chdirToRig(t, beadsDir)

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix error: %v", err)
	}
	if len(check.affectedSlots) != 0 {
		t.Errorf("affectedSlots should remain empty after a clean Fix, got %d", len(check.affectedSlots))
	}
}

func TestMergeSlotIntegrityCheck_SingleRigMode(t *testing.T) {
	// When --rig is specified, only that rig's slot is checked.
	rigRoot, _ := installMergeSlotMockBD(t)
	setMergeSlotRigState(t, rigRoot, "omega", "corrupt")
	setMergeSlotRigState(t, rigRoot, "zeta", "corrupt")

	townRoot, omegaDir := makeTownForMergeSlotTest(t, "omega")
	// Add a second rig with the same structure, also marked corrupt in mock.
	zetaDir := filepath.Join(townRoot, "zeta")
	if err := os.MkdirAll(filepath.Join(zetaDir, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir zeta: %v", err)
	}
	// Register both rigs in rigs.json.
	rigsContent := `{"version":1,"rigs":{"omega":{"git_url":"file:///dev/null","added_at":"2026-07-01T00:00:00Z"},"zeta":{"git_url":"file:///dev/null","added_at":"2026-07-01T00:00:00Z"}}}`
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "rigs.json"), []byte(rigsContent), 0644); err != nil {
		t.Fatalf("write rigs.json: %v", err)
	}

	chdirToRig(t, omegaDir)

	check := NewMergeSlotIntegrityCheck()
	ctx := &CheckContext{TownRoot: townRoot, RigName: "omega"}

	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Errorf("Status = %v, want StatusError. Message=%s Details=%v",
			result.Status, result.Message, result.Details)
	}
	// In single-rig mode only one corrupt rig should be reported.
	if len(check.affectedSlots) != 1 {
		t.Errorf("affectedSlots len = %d, want 1 in single-rig mode", len(check.affectedSlots))
	}
	if check.affectedSlots[0].rigName != "omega" {
		t.Errorf("affectedSlots[0].rigName = %q, want omega", check.affectedSlots[0].rigName)
	}
}
