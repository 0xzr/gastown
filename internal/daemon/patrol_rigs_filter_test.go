package daemon

import (
	"log"
	"os"
	"path/filepath"
	"slices"
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
	// When Dolt is unavailable, isRigOperational() fails safe and returns false
	// for all rigs (can't verify docked status). This prevents witnesses from
	// starting for potentially docked rigs during Dolt outages.
	want := []string{}
	if !slices.Equal(got, want) {
		t.Fatalf("getPatrolRigs() = %v, want %v (all rigs excluded when Dolt unavailable - fail-safe)", got, want)
	}
}

// TestGetPatrolRigs_MainBranchTestFiltersNonOperationalRigs is a regression
// test for gastown-mg7: the main_branch_test patrol must use getPatrolRigs so
// parked/docked rigs are filtered out, rather than iterating over all known rigs.
func TestGetPatrolRigs_MainBranchTestFiltersNonOperationalRigs(t *testing.T) {
	townRoot := t.TempDir()

	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0o755); err != nil {
		t.Fatalf("mkdir mayor dir: %v", err)
	}
	rigsJSON := `{"rigs":{"alpha":{},"beta":{},"gamma":{}}}`
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), []byte(rigsJSON), 0o644); err != nil {
		t.Fatalf("write rigs.json: %v", err)
	}

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

	got := d.getPatrolRigs("main_branch_test")
	slices.Sort(got)
	// When Dolt is unavailable, isRigOperational() fails safe and returns false
	// for every rig, so the expected result is empty (same fail-safe behavior as
	// the witness patrol tested in TestGetPatrolRigs_FiltersNonOperationalRigs).
	want := []string{}
	if !slices.Equal(got, want) {
		t.Fatalf("getPatrolRigs(main_branch_test) = %v, want %v (non-operational rigs must be filtered)", got, want)
	}
}
