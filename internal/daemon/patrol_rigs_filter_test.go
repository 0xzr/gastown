package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/wisp"
)

// Regression test for gt-arz:
// getPatrolRigs should filter parked/docked rigs at list-building time.
func TestGetPatrolRigs_FiltersNonOperationalRigs(t *testing.T) {
	townRoot := t.TempDir()

	// Seed known rigs.
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0o755); err != nil {
		t.Fatalf("mkdir mayor dir: %v", err)
	}
	rigsJSON := `{"rigs":{"alpha":{},"beta":{},"gamma":{}}}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0o644); err != nil {
		t.Fatalf("write rigs.json: %v", err)
	}
	setupRigOperationalTest(t, townRoot, "alpha", "al")
	installRigOperationalBDStub(t, "Issue not found: al-rig-alpha")

	// Mark beta/gamma as non-operational via wisp status.
	if err := wisp.NewConfig(townRoot, "beta").Set("status", "parked"); err != nil {
		t.Fatalf("set beta parked: %v", err)
	}
	if err := wisp.NewConfig(townRoot, "gamma").Set("status", "docked"); err != nil {
		t.Fatalf("set gamma docked: %v", err)
	}

	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(os.Stderr, "[test] ", 0),
	}

	got := d.getPatrolRigs("witness")
	slices.Sort(got)
	want := []string{"alpha"}
	if !slices.Equal(got, want) {
		t.Fatalf("getPatrolRigs() = %v, want %v", got, want)
	}
}

// TestRunMainBranchTests_ExcludesNonOperationalRigs is a regression test for
// gastown-mg7. The historical bug: runMainBranchTests iterated over
// d.getKnownRigs(), which returns every rig in mayor/rigs.json (including
// parked/docked ones), so parked/docked rigs were subjected to main-branch
// test runs they should have been excluded from. The fix is to enumerate via
// d.getPatrolRigs("main_branch_test") instead, which filters non-operational
// rigs at list-building time.
//
// This test drives runMainBranchTests() through its real entry point rather
// than calling getPatrolRigs() in isolation. The wisp-based exclusion is only
// logged by getPatrolRigs(); if runMainBranchTests is ever reverted to
// d.getKnownRigs(), those messages will not appear in the daemon log and
// this test will fail with a clear signal.
//
// To make the test deterministic in a CI sandbox without Dolt: all three rigs
// are filtered before any per-rig worktree operations are attempted, so the
// test does not need to set up a bare repo or worktree for any rig. The
// expected end state is "no operational rigs found", which the patrol logs
// before it would otherwise try to spawn a per-rig worktree.
func TestRunMainBranchTests_ExcludesNonOperationalRigs(t *testing.T) {
	townRoot := t.TempDir()

	// Seed three known rigs in mayor/rigs.json.
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0o755); err != nil {
		t.Fatalf("mkdir mayor dir: %v", err)
	}
	rigsJSON := `{"rigs":{"alpha":{},"beta":{},"gamma":{}}}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0o644); err != nil {
		t.Fatalf("write rigs.json: %v", err)
	}
	setupRigOperationalTest(t, townRoot, "alpha", "al")
	installRigOperationalBDStub(t, "dolt server unavailable")

	// Mark beta as parked and gamma as docked via wisp status. alpha has no
	// wisp status; it falls through to the rig-bead lookup, which is stubbed
	// to fail like a backend outage and is excluded via the Dolt-unavailable
	// fail-safe. Together this guarantees that EVERY known rig is filtered,
	// so the patrol hits its "no operational rigs" branch and never tries
	// to spawn a worktree against a bare repo that does not exist.
	if err := wisp.NewConfig(townRoot, "beta").Set("status", "parked"); err != nil {
		t.Fatalf("set beta parked: %v", err)
	}
	if err := wisp.NewConfig(townRoot, "gamma").Set("status", "docked"); err != nil {
		t.Fatalf("set gamma docked: %v", err)
	}

	var logbuf bytes.Buffer
	d := &Daemon{
		ctx:    context.Background(),
		config: &Config{TownRoot: townRoot},
		logger: log.New(&logbuf, "", 0),
		patrolConfig: &DaemonPatrolConfig{
			Patrols: &PatrolsConfig{
				MainBranchTest: &MainBranchTestConfig{Enabled: true},
			},
		},
		disabledPatrols: map[string]bool{},
	}

	// Sanity check: the regression target must still be reachable. If
	// getKnownRigs() ever stops listing parked/docked rigs, the rest of
	// this test would silently pass even if runMainBranchTests reverted
	// to it. Locking the buggy-list expectation in makes the test fail
	// fast if either side of the comparison drifts.
	known := d.getKnownRigs()
	slices.Sort(known)
	wantKnown := []string{"alpha", "beta", "gamma"}
	if !slices.Equal(known, wantKnown) {
		t.Fatalf("getKnownRigs() = %v, want %v (sanity: parked/docked rigs must appear in the buggy enumeration path)", known, wantKnown)
	}

	// Drive the real entry point. If runMainBranchTests is ever reverted to
	// d.getKnownRigs() (the historical bug), no wisp-exclusion log lines
	// will be produced and the assertions below will fail.
	d.runMainBranchTests()

	logs := logbuf.String()

	// Regression guard: wisp-based exclusion messages must be logged for the
	// parked/docked rigs. These messages are only emitted by
	// getPatrolRigs(), never by getKnownRigs().
	if !strings.Contains(logs, "Excluding beta from main_branch_test patrol: rig is parked") {
		t.Errorf("expected beta excluded via wisp park in main_branch_test patrol; logs:\n%s", logs)
	}
	if !strings.Contains(logs, "Excluding gamma from main_branch_test patrol: rig is docked") {
		t.Errorf("expected gamma excluded via wisp dock in main_branch_test patrol; logs:\n%s", logs)
	}

	// alpha has no wisp status; isRigOperational falls through to the
	// rig-bead lookup which fails in the test env (no Dolt), so it must be
	// excluded with the Dolt-unavailable fail-safe reason — not by wisp.
	// This separates the wisp-filter path from the bead-lookup path.
	if !strings.Contains(logs, "Excluding alpha from main_branch_test patrol: cannot verify rig status (Dolt unavailable)") {
		t.Errorf("expected alpha excluded via Dolt-unavailable fail-safe; logs:\n%s", logs)
	}

	// With no operational rigs, the patrol must short-circuit before any
	// per-rig worktree / git fetch is attempted. This is the property that
	// protects parked/docked rigs from being subjected to main-branch tests.
	if !strings.Contains(logs, "main_branch_test: no operational rigs found") {
		t.Errorf("expected 'no operational rigs found' short-circuit; logs:\n%s", logs)
	}

	// Negative guard: parked/docked rigs must not appear in any
	// per-rig test invocation logs. getKnownRigs() would have caused
	// runMainBranchTests to attempt a per-rig worktree and gate run for
	// beta and gamma — neither of which exist on disk in this test, so
	// the absence of those log lines is the proof that getPatrolRigs()
	// (not getKnownRigs()) drove the enumeration.
	for _, rig := range []string{"alpha", "beta", "gamma"} {
		needle := fmt.Sprintf("main_branch_test: %s: passed", rig)
		failNeedle := fmt.Sprintf("main_branch_test: %s: FAILED", rig)
		if strings.Contains(logs, needle) || strings.Contains(logs, failNeedle) {
			t.Errorf("did not expect per-rig test invocation for %s; logs:\n%s", rig, logs)
		}
	}
}
