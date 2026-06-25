package witness

// Regression tests for gastown-fvy: prove the live notification path actually
// routes through the durable throttle state used by the running witness.
//
// The existing TestDryRunReworkDeferred (gastown-3ip) only validates the
// throttle math against a temp directory. The bug it missed: a live emitter
// could call notifyMayorOfReworkBlocked but bypass EvaluateReworkDeferred
// (or write to a different state path), and the Mayor would still receive
// un-throttled REWORK_DEFERRED messages while `gt witness rework-deferred
// list` reported zero records.
//
// LiveDryRunReworkDeferred fixes that gap by running the live path against
// the EXACT state file the running daemon uses. These tests pin that
// behavior so future refactors cannot reintroduce the bypass.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/mayor"
)

// TestLiveDryRunReworkDeferred_PopulatesProductionStateFile is the gastown-fvy
// regression: prove that calling notifyMayorOfReworkBlocked against the live
// state file actually writes records to that file. A bypass emitter would
// either skip the save entirely or write to a different path; this test
// catches both.
func TestLiveDryRunReworkDeferred_PopulatesProductionStateFile(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "witness")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	statePath := filepath.Join(stateDir, "rework-deferred-throttle.json")

	result, err := LiveDryRunReworkDeferred(dir)
	if err != nil {
		t.Fatalf("LiveDryRunReworkDeferred: %v", err)
	}

	if !result.Pass {
		for _, e := range result.Errors {
			t.Errorf("live dry-run error: %s", e)
		}
		t.Fatalf("live dry-run failed")
	}

	if result.StatePath != statePath {
		t.Errorf("StatePath = %q, want %q (the live dry-run must use the production path)",
			result.StatePath, statePath)
	}
	if result.TownRoot != dir {
		t.Errorf("TownRoot = %q, want %q", result.TownRoot, dir)
	}

	// Mid-run evidence: listReworkDeferredRecords must have been populated
	// at some point. After cleanup the records are removed; the test
	// confirms the cleanup succeeded (no leftover live-dryrun- records).
	if remaining := countLiveDryRunRecords(dir); remaining != 0 {
		t.Errorf("cleanup left %d live-dryrun- records behind", remaining)
	}

	// Per-tuple evidence: every fixture emitted once, suppressed repeats,
	// and rolled up exactly once. The MailSent count is the live-path
	// signal — emit + rollup + change-emit = 3 sends per tuple; suppress
	// calls must NOT have sent.
	for _, tup := range result.Tuples {
		if tup.FirstAction != ActionEmit {
			t.Errorf("%s: FirstAction = %q, want emit", tup.Bead, tup.FirstAction)
		}
		if tup.RepeatAction != ActionSuppress {
			t.Errorf("%s: RepeatAction = %q, want suppress", tup.Bead, tup.RepeatAction)
		}
		if tup.RollupAction != ActionRollup {
			t.Errorf("%s: RollupAction = %q, want rollup", tup.Bead, tup.RollupAction)
		}
		if tup.RollupSuppressedCount != 5 {
			t.Errorf("%s: RollupSuppressedCount = %d, want 5 (gastown-3ip: real count, not 0)",
				tup.Bead, tup.RollupSuppressedCount)
		}
		// Each tuple should have sent exactly 3 messages: first emit,
		// rollup after window, status-change emit. The 5 suppress calls
		// must NOT have sent — if they did, the live path is bypassing
		// the throttle (gastown-fvy root cause).
		if tup.MailSent != 3 {
			t.Errorf("%s: MailSent = %d, want 3 (first emit + rollup + change-emit; suppress must NOT send)",
				tup.Bead, tup.MailSent)
		}
	}
}

// TestLiveDryRunReworkDeferred_SuppressCallsDoNotSendMail is the most
// pointed gastown-fvy assertion: the live path must NOT send a mail when
// the throttle says suppress. A bypass emitter that calls router.Send
// directly (skipping EvaluateReworkDeferred) would pass the throttle-math
// test but fail this one.
func TestLiveDryRunReworkDeferred_SuppressCallsDoNotSendMail(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "witness"), 0755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	router := &captureRouter{}

	// Use the live notification path directly so this test does not rely
	// on LiveDryRunReworkDeferred's per-tuple bookkeeping.
	origNow := reworkDeferredNow
	start := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	now := start
	reworkDeferredNow = func() time.Time { return now }
	t.Cleanup(func() { reworkDeferredNow = origNow })

	bead := liveDryRunPrefix + "bead"
	polecat := liveDryRunPrefix + "polecat"
	decision := &mayor.Decision{
		Type:      mayor.DecisionDefer,
		Reason:    "live-dryrun-suppress-test",
		MayorID:   "test",
		Timestamp: start,
	}

	// First call: emits and sends.
	notifyMayorOfReworkBlocked(dir, "test-rig", bead, polecat, "merge_failed", 0, decision, router)
	if got := router.count(); got != 1 {
		t.Fatalf("first call: mail count = %d, want 1", got)
	}

	// Five identical calls inside the throttle window. Each MUST be
	// suppressed and MUST NOT send mail. A bypass emitter would have
	// mail count = 6 here.
	for i := 0; i < 5; i++ {
		now = now.Add(2 * time.Minute)
		notifyMayorOfReworkBlocked(dir, "test-rig", bead, polecat, "merge_failed", 0, decision, router)
	}
	if got := router.count(); got != 1 {
		t.Fatalf("after 5 suppressed repeats: mail count = %d, want 1 (throttle bypass)",
			got)
	}

	// Cleanup so we do not leave live-dryrun- records behind.
	if removed, err := removeLiveDryRunRecords(dir); err != nil {
		t.Errorf("cleanup: %v", err)
	} else if removed != 1 {
		t.Errorf("cleanup removed %d records, want 1", removed)
	}
}

// TestLiveDryRunReworkDeferred_PrefixIsolatesFixtureFromProduction verifies
// the live-dryrun- prefix prevents fixture records from colliding with
// production records on the same state file. If the prefix were missing or
// shared, the cleanup step could remove real production data.
func TestLiveDryRunReworkDeferred_PrefixIsolatesFixtureFromProduction(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "witness"), 0755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	// Seed a production record by directly calling EvaluateReworkDeferred
	// with a non-live-dryrun- tuple.
	origNow := reworkDeferredNow
	start := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	reworkDeferredNow = func() time.Time { return start }
	t.Cleanup(func() { reworkDeferredNow = origNow })

	if dec := EvaluateReworkDeferred(dir, "real-rig", "real-bead", "real-polecat",
		"merge_failed", "real reason", mayor.DecisionDefer, 1*time.Hour); dec.Action != ActionEmit {
		t.Fatalf("seed emit: action = %q, want emit", dec.Action)
	}

	// Run the live dry-run; it must not touch the real-rig/real-bead record.
	if _, err := LiveDryRunReworkDeferred(dir); err != nil {
		t.Fatalf("live dry-run: %v", err)
	}

	records := ListReworkDeferredRecords(dir)
	var prodFound bool
	for _, rec := range records {
		if strings.HasPrefix(rec.BeadID, liveDryRunPrefix) || strings.HasPrefix(rec.PolecatName, liveDryRunPrefix) {
			t.Errorf("leftover fixture record: bead=%s polecat=%s", rec.BeadID, rec.PolecatName)
		}
		if rec.BeadID == "real-bead" {
			prodFound = true
		}
	}
	if !prodFound {
		t.Error("live dry-run removed the production record; prefix isolation is broken")
	}
}
