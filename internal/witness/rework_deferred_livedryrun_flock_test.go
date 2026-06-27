package witness

// Cross-process regression tests for gastown-9rc: removeLiveDryRunRecords must
// acquire the same cross-process flock that EvaluateReworkDeferred,
// ListReworkDeferredRecords, and ClearReworkDeferredRecord use.
//
// The bug is fundamentally CROSS-PROCESS. removeLiveDryRunRecords already
// holds the in-process reworkDeferredMu mutex, which serializes every caller
// *within this process*. An in-process goroutine race would therefore pass
// even on the unpatched code — that is precisely the false-green shape this
// bead exists to kill. To exercise the real failure mode (a separate witness
// process saving REWORK_DEFERRED state while the live dry-run cleanup runs),
// these tests re-exec the test binary as a helper process that holds the flock
// mid-read-modify-write, using the same os.Args[0] -test.run + GT_*_HELPER env
// convention as internal/doltserver and internal/beads.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/lock"
	"github.com/steveyegge/gastown/internal/mayor"
)

// helperEnvPrefix is the env namespace for the cross-process flock helper.
const helperEnvPrefix = "GT_9RC_FLOCK_HELPER"

// flock helper marker file names, exchanged via markerDir.
const (
	markerHoldingFlock      = "holding_flock"
	markerParentAttempted   = "parent_cleanup_attempted"
	markerHelperDone        = "helper_done"
	markerHelperSavedRecord = "helper_saved"
)

// helperWaitTimeout bounds how long the helper waits for the parent and vice
// versa. Generous enough for a loaded CI box, short enough to fail fast on
// deadlock instead of hanging the suite.
const helperWaitTimeout = 15 * time.Second

// TestRemoveLiveDryRunRecords_AcquiresCrossProcessFlock is the gastown-9rc
// regression. It proves removeLiveDryRunRecords acquires the cross-process
// flock by showing it BLOCKS while a separate process (simulating an active
// witness mid-RMW) holds that flock, and that once the witness releases it the
// cleanup proceeds without clobbering the witness's just-saved record (no lost
// update) and without duplicating or dropping records.
//
// On UNPATCHED code removeLiveDryRunRecords never acquires the flock, so it
// returns immediately while the helper still holds the lock — the
// "still blocked" assertion fails. The test is therefore deterministic: it
// fails on the buggy code and passes once the flock is acquired.
func TestRemoveLiveDryRunRecords_AcquiresCrossProcessFlock(t *testing.T) {
	if os.Getenv(helperEnvPrefix) == "1" {
		runFlockHoldingHelper(t)
		return
	}

	// Use a real temp townRoot so both this process and the re-exec'd helper
	// resolve the SAME state file path via the default ReworkDeferredStateFile
	// (we deliberately do NOT redirect that var here — a redirected var is
	// in-memory and invisible to the helper process).
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "witness"), 0o755); err != nil {
		t.Fatalf("mkdir witness dir: %v", err)
	}
	markerDir := t.TempDir()
	stateFile := ReworkDeferredStateFile(townRoot)
	flockFile := stateFile + ".flock"

	// Seed one production record through the real EvaluateReworkDeferred path
	// (honors the bead: "races removeLiveDryRunRecords against
	// EvaluateReworkDeferred"). This is record P.
	start := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	origNow := reworkDeferredNow
	reworkDeferredNow = func() time.Time { return start }
	t.Cleanup(func() { reworkDeferredNow = origNow })

	const (
		rigP    = "real-rig"
		beadP   = "real-bead-P"
		polecat = "real-polecat"
	)
	if dec := EvaluateReworkDeferred(townRoot, rigP, beadP, polecat, "merge_failed",
		"seed production record P", mayor.DecisionDefer, 1*time.Hour); dec.Action != ActionEmit {
		t.Fatalf("seed P: Action = %q, want emit", dec.Action)
	}

	// Spawn the helper. It acquires the flock, loads state {P}, pauses, and
	// only saves a second production record Q after the parent has attempted
	// cleanup — simulating a witness mid-RMW whose save must not be clobbered.
	helper := reexecFlockHelper(t, townRoot, markerDir)
	helperDone := make(chan error, 1)
	go func() {
		helperDone <- helper.Wait()
	}()
	t.Cleanup(func() {
		if helper.Process != nil {
			_ = helper.Process.Signal(os.Interrupt)
		}
	})

	// Wait until the helper confirms it holds the flock + has loaded {P}.
	if err := waitForMarker(markerDir, markerHoldingFlock, helperWaitTimeout); err != nil {
		t.Fatalf("helper never reported holding the flock: %v", err)
	}

	// Drive removeLiveDryRunRecords in a goroutine. On PATCHED code it blocks
	// on FlockAcquire (helper holds it) before reading the state file; on
	// UNPATCHED code it returns immediately having read {P} and never waited.
	type cleanupResult struct {
		removed int
		err     error
	}
	done := make(chan cleanupResult, 1)
	go func() {
		removed, err := removeLiveDryRunRecords(townRoot)
		done <- cleanupResult{removed: removed, err: err}
	}()

	select {
	case res := <-done:
		t.Fatalf("removeLiveDryRunRecords returned (removed=%d err=%v) while the witness held the flock; it did NOT acquire the cross-process flock (gastown-9rc regression)", res.removed, res.err)
	case <-time.After(800 * time.Millisecond):
		// Still blocked — expected: the cleanup is waiting on the flock the
		// helper holds. This is the assertion that fails on unpatched code.
	}

	// Tell the helper to save Q and release the flock.
	if err := touchMarker(markerDir, markerParentAttempted); err != nil {
		t.Fatalf("signal helper: %v", err)
	}

	// Helper saves Q, releases flock, writes helper_done.
	if err := waitForMarker(markerDir, markerHelperSavedRecord, helperWaitTimeout); err != nil {
		t.Fatalf("helper never saved Q: %v", err)
	}
	if err := waitForMarker(markerDir, markerHelperDone, helperWaitTimeout); err != nil {
		t.Fatalf("helper never finished: %v", err)
	}

	// Now that the flock is released, removeLiveDryRunRecords must complete.
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("removeLiveDryRunRecords: %v", res.err)
		}
		// No live-dryrun- records existed, so nothing should have been removed.
		if res.removed != 0 {
			t.Errorf("removed = %d, want 0 (no live-dryrun- records were seeded)", res.removed)
		}
	case <-time.After(helperWaitTimeout):
		t.Fatal("removeLiveDryRunRecords did not return after the flock was released")
	}

	// The helper process must have exited cleanly.
	select {
	case err := <-helperDone:
		if err != nil {
			t.Fatalf("helper process exited with error: %v", err)
		}
	case <-time.After(helperWaitTimeout):
		t.Fatal("helper process did not exit")
	}

	// No record loss, no duplication: both production records P and Q survive,
	// no live-dryrun- records remain, and there are exactly two distinct
	// records (the helper's save of Q was not clobbered by the cleanup).
	records := ListReworkDeferredRecords(townRoot)
	var foundP, foundQ, foundDryrun bool
	seenKeys := make(map[string]int, len(records))
	for _, rec := range records {
		seenKeys[rec.Key]++
		if rec.BeadID == beadP {
			foundP = true
		}
		if rec.BeadID == "real-bead-Q" {
			foundQ = true
		}
		if strings.HasPrefix(rec.BeadID, liveDryRunPrefix) || strings.HasPrefix(rec.PolecatName, liveDryRunPrefix) {
			foundDryrun = true
		}
	}
	if !foundP {
		t.Errorf("production record P (%s) was lost: the cleanup clobbered the seeded record", beadP)
	}
	if !foundQ {
		t.Errorf("production record Q was lost: removeLiveDryRunRecords clobbered the witness's concurrent save " +
			"(this is the lost-update the cross-process flock prevents)")
	}
	if foundDryrun {
		t.Errorf("live-dryrun- records remain after cleanup")
	}
	for key, n := range seenKeys {
		if n > 1 {
			t.Errorf("record key %s duplicated %d times (no duplication invariant violated)", key, n)
		}
	}
	if len(records) != 2 {
		t.Errorf("record count = %d, want 2 (P + Q; no loss, no duplication): %+v", len(records), records)
	}

	// Sanity: the flock file exists (both code paths create it) and the helper
	// is no longer holding it — a fresh acquire must succeed immediately.
	unlock, err := lock.FlockAcquire(flockFile)
	if err != nil {
		t.Fatalf("could not re-acquire flock after test: %v", err)
	}
	unlock()
}

// TestRemoveLiveDryRunRecords_RemovesOnlyPrefixedRecordsUnderNoContention is
// the positive counterpart: with no concurrent witness, removeLiveDryRunRecords
// must remove exactly the live-dryrun- records and leave production records
// intact. This pins that adding the flock did not change the cleanup semantics.
func TestRemoveLiveDryRunRecords_RemovesOnlyPrefixedRecordsUnderNoContention(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "witness"), 0o755); err != nil {
		t.Fatalf("mkdir witness dir: %v", err)
	}
	start := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	origNow := reworkDeferredNow
	reworkDeferredNow = func() time.Time { return start }
	t.Cleanup(func() { reworkDeferredNow = origNow })

	// One production record and two live-dryrun- records.
	prod := EvaluateReworkDeferred(townRoot, "real-rig", "real-bead", "real-polecat",
		"merge_failed", "prod", mayor.DecisionDefer, 1*time.Hour)
	if prod.Action != ActionEmit {
		t.Fatalf("seed prod: Action = %q, want emit", prod.Action)
	}
	EvaluateReworkDeferred(townRoot, "live-rig", liveDryRunPrefix+"bead-a",
		liveDryRunPrefix+"alpha", "merge_failed", "dryrun a", mayor.DecisionHold, 1*time.Hour)
	EvaluateReworkDeferred(townRoot, "live-rig", liveDryRunPrefix+"bead-b",
		liveDryRunPrefix+"beta", "merge_failed", "dryrun b", mayor.DecisionPark, 1*time.Hour)

	removed, err := removeLiveDryRunRecords(townRoot)
	if err != nil {
		t.Fatalf("removeLiveDryRunRecords: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}

	records := ListReworkDeferredRecords(townRoot)
	if len(records) != 1 {
		t.Fatalf("after cleanup: %d records, want 1 (production only)", len(records))
	}
	if records[0].BeadID != "real-bead" {
		t.Errorf("surviving record BeadID = %q, want real-bead", records[0].BeadID)
	}
}

// runFlockHoldingHelper is the helper-process entry point. It simulates an
// active witness mid read-modify-write on the durable throttle state file:
// acquires the cross-process flock, loads state, signals the parent, and only
// saves a new production record (Q) after the parent has attempted cleanup.
// This is the exact interleaving that loses Q when removeLiveDryRunRecords
// skips the flock: the cleanup would read {P}, then the witness saves {P,Q},
// then the cleanup writes {P} — clobbering Q.
func runFlockHoldingHelper(t *testing.T) {
	townRoot := os.Getenv(helperEnvPrefix + "_TOWN_ROOT")
	markerDir := os.Getenv(helperEnvPrefix + "_MARKER_DIR")
	if townRoot == "" || markerDir == "" {
		// Not a helper invocation (shouldn't happen — gated on env == "1").
		return
	}
	stateFile := ReworkDeferredStateFile(townRoot)
	flockFile := stateFile + ".flock"

	// Acquire the cross-process flock the same way EvaluateReworkDeferred does.
	unlock, err := lock.FlockAcquire(flockFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper: acquire flock: %v\n", err)
		os.Exit(2)
	}
	defer unlock()

	// Load state mid-RMW (the witness has read {P} and not yet saved Q).
	state := loadReworkDeferredState(townRoot)
	if err := touchMarker(markerDir, markerHoldingFlock); err != nil {
		fmt.Fprintf(os.Stderr, "helper: signal holding: %v\n", err)
		os.Exit(3)
	}

	// Wait for the parent to attempt cleanup. On PATCHED code the parent is
	// blocked on the flock we hold; on UNPATCHED code the parent has already
	// returned. Either way we proceed to save Q.
	if err := waitForMarker(markerDir, markerParentAttempted, helperWaitTimeout); err != nil {
		fmt.Fprintf(os.Stderr, "helper: parent never attempted cleanup: %v\n", err)
		os.Exit(4)
	}

	// Save a new production record Q (same shape EvaluateReworkDeferred
	// creates), appending to the in-memory state we loaded while holding the
	// flock.
	now := time.Now().UTC()
	q := &ReworkDeferredRecord{
		Key:               reworkDeferredKey("real-rig", "real-bead-Q", "real-polecat", mayor.DecisionHold, "merge_failed"),
		RigName:           "real-rig",
		BeadID:            "real-bead-Q",
		PolecatName:       "real-polecat",
		MayorDecision:     string(mayor.DecisionHold),
		SourceStatus:      "merge_failed",
		FirstEmittedAt:    now,
		LastEmittedAt:     now,
		LastEmittedReason: "helper-saved concurrent production record Q",
		SuppressedCount:   0,
	}
	state.Records = append(state.Records, q)
	if err := saveReworkDeferredState(townRoot, state); err != nil {
		fmt.Fprintf(os.Stderr, "helper: save Q: %v\n", err)
		os.Exit(5)
	}
	if err := touchMarker(markerDir, markerHelperSavedRecord); err != nil {
		fmt.Fprintf(os.Stderr, "helper: signal saved: %v\n", err)
		os.Exit(6)
	}

	// Releasing the flock (defer) lets the parent's blocked cleanup proceed.
	if err := touchMarker(markerDir, markerHelperDone); err != nil {
		fmt.Fprintf(os.Stderr, "helper: signal done: %v\n", err)
		os.Exit(7)
	}
}

// reexecFlockHelper builds and starts the re-exec'd helper process, mirroring
// the internal/doltserver and internal/beads convention: run the test binary
// with -test.run restricted to the helper entry, gated on a helper env var,
// with a sanitized environment so the child does not inherit stray test flags
// or town state.
func reexecFlockHelper(t *testing.T, townRoot, markerDir string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRemoveLiveDryRunRecords_AcquiresCrossProcessFlock$")
	cmd.Env = sanitizedFlockHelperEnv(townRoot, markerDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start flock helper process: %v", err)
	}
	return cmd
}

// sanitizedFlockHelperEnv returns a minimal environment for the helper: only
// PATH/HOME and the GT_9RC_FLOCK_HELPER control vars. Stripping the rest
// prevents the child from inheriting the parent's -test.* args (passed as env
// by some harnesses) or town-specific state (GT_ROOT etc.) that could change
// its behavior.
func sanitizedFlockHelperEnv(townRoot, markerDir string) []string {
	env := []string{
		helperEnvPrefix + "=1",
		helperEnvPrefix + "_TOWN_ROOT=" + townRoot,
		helperEnvPrefix + "_MARKER_DIR=" + markerDir,
	}
	for _, item := range os.Environ() {
		switch {
		case strings.HasPrefix(item, "PATH="),
			strings.HasPrefix(item, "HOME="):
			env = append(env, item)
		}
	}
	// Guarantee PATH/HOME are present even if the parent lacked them.
	has := func(k string) bool {
		prefix := k + "="
		for _, e := range env {
			if strings.HasPrefix(e, prefix) {
				return true
			}
		}
		return false
	}
	if !has("PATH") {
		env = append(env, "PATH="+os.Getenv("PATH"))
	}
	if !has("HOME") {
		if h, err := os.UserHomeDir(); err == nil {
			env = append(env, "HOME="+h)
		}
	}
	return env
}

// touchMarker creates a marker file under markerDir, signaling the other
// process that a step has completed.
func touchMarker(markerDir, name string) error {
	return os.WriteFile(filepath.Join(markerDir, name), []byte("1"), 0o644)
}

// waitForMarker polls for a marker file's existence up to the timeout.
func waitForMarker(markerDir, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	path := filepath.Join(markerDir, name)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for marker %q in %s", name, markerDir)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
