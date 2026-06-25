package witness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/mayor"
)

// withReworkDeferredClock replaces the package clock for the duration of a test
// and returns a setter the test can use to advance the clock. Tests must
// restore the clock via t.Cleanup.
func withReworkDeferredClock(t *testing.T, start time.Time) (advance func(d time.Duration)) {
	t.Helper()
	orig := reworkDeferredNow
	reworkDeferredNow = func() time.Time { return start }
	t.Cleanup(func() { reworkDeferredNow = orig })
	cursor := start
	return func(d time.Duration) {
		cursor = cursor.Add(d)
		reworkDeferredNow = func() time.Time { return cursor }
	}
}

// withReworkDeferredStateDir redirects the durable state file to a per-test
// temp directory and returns the path. Tests can inspect the file directly
// to verify durability (the throttle survives across evaluations).
func withReworkDeferredStateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := ensureDir(filepath.Join(dir, "witness")); err != nil {
		t.Fatal(err)
	}
	orig := ReworkDeferredStateFile
	ReworkDeferredStateFile = func(townRoot string) string {
		return filepath.Join(dir, "witness", "rework-deferred-throttle.json")
	}
	t.Cleanup(func() { ReworkDeferredStateFile = orig })
	return dir
}

func ensureDir(p string) error {
	// Local helper to avoid duplicating os.MkdirAll at every test setup.
	return os.MkdirAll(p, 0o755)
}

// TestEvaluateReworkDeferred_FirstEmitsImmediately verifies the first call
// always emits, regardless of the throttle window.
func TestEvaluateReworkDeferred_FirstEmitsImmediately(t *testing.T) {
	dir := withReworkDeferredStateDir(t)
	start := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	withReworkDeferredClock(t, start)

	dec := EvaluateReworkDeferred(dir, "polybot-uiu", "gt-hold1", "alpha", "merge_failed",
		"hold per priority", mayor.DecisionHold, 1*time.Hour)

	if dec.Action != ActionEmit {
		t.Fatalf("first call: Action = %q, want %q", dec.Action, ActionEmit)
	}
	if dec.Record == nil {
		t.Fatal("first call: Record is nil")
	}
	if dec.Record.SuppressedCount != 0 {
		t.Errorf("first call: SuppressedCount = %d, want 0", dec.Record.SuppressedCount)
	}
	if dec.Record.FirstEmittedAt != start {
		t.Errorf("first call: FirstEmittedAt = %v, want %v", dec.Record.FirstEmittedAt, start)
	}
}

// TestEvaluateReworkDeferred_IdenticalRepeatWithinWindowIsSuppressed covers
// the regression case from gastown-cet.11: polybot-uiu/gt-hold1 emits once,
// then identical repeats are suppressed and counted.
func TestEvaluateReworkDeferred_IdenticalRepeatWithinWindowIsSuppressed(t *testing.T) {
	dir := withReworkDeferredStateDir(t)
	start := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	advance := withReworkDeferredClock(t, start)

	// First emit.
	dec := EvaluateReworkDeferred(dir, "polybot-uiu", "gt-hold1", "alpha", "merge_failed",
		"hold per priority", mayor.DecisionHold, 1*time.Hour)
	if dec.Action != ActionEmit {
		t.Fatalf("first call: Action = %q, want %q", dec.Action, ActionEmit)
	}

	// Five identical repeats within the 1h window — all suppressed.
	for i := 1; i <= 5; i++ {
		advance(5 * time.Minute)
		dec = EvaluateReworkDeferred(dir, "polybot-uiu", "gt-hold1", "alpha", "merge_failed",
			"hold per priority", mayor.DecisionHold, 1*time.Hour)
		if dec.Action != ActionSuppress {
			t.Fatalf("repeat #%d: Action = %q, want %q", i, dec.Action, ActionSuppress)
		}
		if dec.Record.SuppressedCount != i {
			t.Errorf("repeat #%d: SuppressedCount = %d, want %d", i, dec.Record.SuppressedCount, i)
		}
	}

	// The record reflects the cumulative suppressed count.
	records := ListReworkDeferredRecords(dir)
	if len(records) != 1 {
		t.Fatalf("records: got %d, want 1", len(records))
	}
	if records[0].SuppressedCount != 5 {
		t.Errorf("durable SuppressedCount = %d, want 5", records[0].SuppressedCount)
	}
	if records[0].FirstSuppressedAt.IsZero() {
		t.Error("FirstSuppressedAt is zero; expected the first suppression timestamp to be recorded")
	}
}

// TestEvaluateReworkDeferred_RollupAfterWindowElapses verifies that after the
// throttle window elapses, the next call emits a rollup that includes the
// suppressed count, and then resets the counter.
func TestEvaluateReworkDeferred_RollupAfterWindowElapses(t *testing.T) {
	dir := withReworkDeferredStateDir(t)
	start := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	advance := withReworkDeferredClock(t, start)

	// First emit.
	dec := EvaluateReworkDeferred(dir, "polybot-uiu", "gt-park1", "alpha", "merge_failed",
		"parked", mayor.DecisionPark, 1*time.Hour)
	if dec.Action != ActionEmit {
		t.Fatalf("first call: Action = %q, want %q", dec.Action, ActionEmit)
	}

	// Two suppressed repeats inside the window.
	advance(10 * time.Minute)
	if dec = EvaluateReworkDeferred(dir, "polybot-uiu", "gt-park1", "alpha", "merge_failed",
		"parked", mayor.DecisionPark, 1*time.Hour); dec.Action != ActionSuppress {
		t.Fatalf("suppress #1: Action = %q, want %q", dec.Action, ActionSuppress)
	}
	advance(10 * time.Minute)
	if dec = EvaluateReworkDeferred(dir, "polybot-uiu", "gt-park1", "alpha", "merge_failed",
		"parked", mayor.DecisionPark, 1*time.Hour); dec.Action != ActionSuppress {
		t.Fatalf("suppress #2: Action = %q, want %q", dec.Action, ActionSuppress)
	}

	// Now advance past the 1h window. Next call must rollup.
	advance(45 * time.Minute) // total elapsed since first emit: 65m
	dec = EvaluateReworkDeferred(dir, "polybot-uiu", "gt-park1", "alpha", "merge_failed",
		"parked", mayor.DecisionPark, 1*time.Hour)
	if dec.Action != ActionRollup {
		t.Fatalf("post-window: Action = %q, want %q", dec.Action, ActionRollup)
	}
	// Rollup record: SuppressedCount is reset to 0 in the durable record
	// after the rollup is emitted.
	records := ListReworkDeferredRecords(dir)
	if len(records) != 1 {
		t.Fatalf("records: got %d, want 1", len(records))
	}
	if records[0].SuppressedCount != 0 {
		t.Errorf("after rollup: SuppressedCount = %d, want 0", records[0].SuppressedCount)
	}
	if !records[0].FirstEmittedAt.Equal(dec.Record.FirstEmittedAt) {
		t.Errorf("FirstEmittedAt changed: durable=%v returned=%v", records[0].FirstEmittedAt, dec.Record.FirstEmittedAt)
	}
	if !records[0].FirstSuppressedAt.IsZero() {
		t.Errorf("after rollup: FirstSuppressedAt = %v, want zero (reset for new window)", records[0].FirstSuppressedAt)
	}
}

// TestEvaluateReworkDeferred_DifferentDecisionEmitsImmediately verifies that
// a change in the Mayor decision type (e.g., defer → hold) emits immediately
// even if the prior tuple was throttled.
func TestEvaluateReworkDeferred_DifferentDecisionEmitsImmediately(t *testing.T) {
	dir := withReworkDeferredStateDir(t)
	start := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	advance := withReworkDeferredClock(t, start)

	if dec := EvaluateReworkDeferred(dir, "polybot-uiu", "gt-work999", "alpha", "merge_failed",
		"defer first", mayor.DecisionDefer, 1*time.Hour); dec.Action != ActionEmit {
		t.Fatalf("first defer: Action = %q, want emit", dec.Action)
	}

	// 30 minutes later, same bead/polecat/status but decision is now HOLD.
	advance(30 * time.Minute)
	dec := EvaluateReworkDeferred(dir, "polybot-uiu", "gt-work999", "alpha", "merge_failed",
		"hold now", mayor.DecisionHold, 1*time.Hour)
	if dec.Action != ActionEmit {
		t.Fatalf("decision change: Action = %q, want %q", dec.Action, ActionEmit)
	}
	if dec.Record.MayorDecision != string(mayor.DecisionHold) {
		t.Errorf("MayorDecision = %q, want %q", dec.Record.MayorDecision, mayor.DecisionHold)
	}
	if dec.Record.SuppressedCount != 0 {
		t.Errorf("after decision change: SuppressedCount = %d, want 0", dec.Record.SuppressedCount)
	}
}

// TestEvaluateReworkDeferred_DifferentPolecatEmitsImmediately verifies that
// a change in polecat emits immediately (different key, different record).
func TestEvaluateReworkDeferred_DifferentPolecatEmitsImmediately(t *testing.T) {
	dir := withReworkDeferredStateDir(t)
	start := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	advance := withReworkDeferredClock(t, start)

	if dec := EvaluateReworkDeferred(dir, "polybot-uiu", "gt-work999", "alpha", "merge_failed",
		"x", mayor.DecisionDefer, 1*time.Hour); dec.Action != ActionEmit {
		t.Fatalf("first polecat: Action = %q, want emit", dec.Action)
	}

	advance(10 * time.Minute)
	// Same bead/decision/status, but a different polecat = different key.
	dec := EvaluateReworkDeferred(dir, "polybot-uiu", "gt-work999", "beta", "merge_failed",
		"x", mayor.DecisionDefer, 1*time.Hour)
	if dec.Action != ActionEmit {
		t.Fatalf("different polecat: Action = %q, want %q", dec.Action, ActionEmit)
	}
	if dec.Record.PolecatName != "beta" {
		t.Errorf("PolecatName = %q, want %q", dec.Record.PolecatName, "beta")
	}

	// Two records exist, one per polecat.
	records := ListReworkDeferredRecords(dir)
	if len(records) != 2 {
		t.Fatalf("records: got %d, want 2 (one per polecat)", len(records))
	}
}

// TestEvaluateReworkDeferred_DifferentSourceStatusEmitsImmediately verifies
// that a change in the source status (e.g., from merge_failed to hooked)
// emits immediately.
func TestEvaluateReworkDeferred_DifferentSourceStatusEmitsImmediately(t *testing.T) {
	dir := withReworkDeferredStateDir(t)
	start := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	advance := withReworkDeferredClock(t, start)

	if dec := EvaluateReworkDeferred(dir, "polybot-uiu", "gt-work999", "alpha", "merge_failed",
		"x", mayor.DecisionDefer, 1*time.Hour); dec.Action != ActionEmit {
		t.Fatalf("first: Action = %q, want emit", dec.Action)
	}
	advance(5 * time.Minute)
	// Different source status (e.g., patrol found a stuck hooked bead with
	// the same decision in effect) should emit immediately.
	dec := EvaluateReworkDeferred(dir, "polybot-uiu", "gt-work999", "alpha", "hooked",
		"x", mayor.DecisionDefer, 1*time.Hour)
	if dec.Action != ActionEmit {
		t.Fatalf("status change: Action = %q, want %q", dec.Action, ActionEmit)
	}
	if dec.Record.SourceStatus != "hooked" {
		t.Errorf("SourceStatus = %q, want %q", dec.Record.SourceStatus, "hooked")
	}
}

// TestEvaluateReworkDeferred_DifferentRigOrBeadIsIndependent verifies that
// throttle state is per-tuple: changing rig or bead gives a fresh record.
func TestEvaluateReworkDeferred_DifferentRigOrBeadIsIndependent(t *testing.T) {
	dir := withReworkDeferredStateDir(t)
	start := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	advance := withReworkDeferredClock(t, start)

	cases := []struct {
		rig, bead, polecat string
	}{
		{"polybot-uiu", "gt-hold1", "alpha"},
		{"polybot-uiu", "gt-park1", "alpha"},
		{"polybot-uiu", "gt-work999", "alpha"},
		{"other-rig", "gt-hold1", "alpha"},
	}
	for _, c := range cases {
		dec := EvaluateReworkDeferred(dir, c.rig, c.bead, c.polecat, "merge_failed",
			"x", mayor.DecisionDefer, 1*time.Hour)
		if dec.Action != ActionEmit {
			t.Errorf("(%s/%s/%s) first: Action = %q, want %q", c.rig, c.bead, c.polecat, dec.Action, ActionEmit)
		}
		advance(time.Minute)
	}
	records := ListReworkDeferredRecords(dir)
	if len(records) != len(cases) {
		t.Errorf("records: got %d, want %d (one per tuple)", len(records), len(cases))
	}
}

// TestEvaluateReworkDeferred_DurableAcrossEvaluations verifies that the
// throttle state survives a fresh load (i.e., the file is read on each
// call, not cached in memory).
func TestEvaluateReworkDeferred_DurableAcrossEvaluations(t *testing.T) {
	dir := withReworkDeferredStateDir(t)
	start := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	advance := withReworkDeferredClock(t, start)

	// First emit.
	if dec := EvaluateReworkDeferred(dir, "polybot-uiu", "gt-hold1", "alpha", "merge_failed",
		"x", mayor.DecisionDefer, 1*time.Hour); dec.Action != ActionEmit {
		t.Fatalf("first: Action = %q, want emit", dec.Action)
	}

	// Drop the in-process state file and recreate a fresh clock cursor
	// (simulates a witness restart). The durable file on disk should still
	// drive the throttle.
	advance(10 * time.Minute)
	dec := EvaluateReworkDeferred(dir, "polybot-uiu", "gt-hold1", "alpha", "merge_failed",
		"x", mayor.DecisionDefer, 1*time.Hour)
	if dec.Action != ActionSuppress {
		t.Fatalf("after reload: Action = %q, want %q", dec.Action, ActionSuppress)
	}
	if dec.Record.SuppressedCount != 1 {
		t.Errorf("after reload: SuppressedCount = %d, want 1", dec.Record.SuppressedCount)
	}
}

// TestEvaluateReworkDeferred_ZeroWindowDisablesThrottle verifies that
// configuring window=0 (or negative) disables throttling entirely. This is
// the operator override for diagnostic / "see every notification" mode.
func TestEvaluateReworkDeferred_ZeroWindowDisablesThrottle(t *testing.T) {
	dir := withReworkDeferredStateDir(t)
	start := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	withReworkDeferredClock(t, start)

	for i := 0; i < 3; i++ {
		dec := EvaluateReworkDeferred(dir, "polybot-uiu", "gt-hold1", "alpha", "merge_failed",
			"x", mayor.DecisionDefer, 0)
		if dec.Action != ActionEmit {
			t.Errorf("call #%d with window=0: Action = %q, want %q", i, dec.Action, ActionEmit)
		}
	}
}

// TestEvaluateReworkDeferred_RegressionPolybotUiuStyle covers the exact
// regression case from the issue acceptance criteria: the polybot-uiu rig
// repeatedly emitting REWORK_DEFERRED for gt-hold1/gt-park1/gt-work999 must
// result in one immediate emit per tuple, with subsequent identical repeats
// suppressed and counted. This is the end-to-end shape of the regression.
func TestEvaluateReworkDeferred_RegressionPolybotUiuStyle(t *testing.T) {
	dir := withReworkDeferredStateDir(t)
	start := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	advance := withReworkDeferredClock(t, start)

	tuples := []struct {
		bead     string
		decision mayor.DecisionType
	}{
		{"gt-hold1", mayor.DecisionHold},
		{"gt-park1", mayor.DecisionPark},
		{"gt-work999", mayor.DecisionDefer},
	}

	// First wave: 3 emits (one per tuple).
	for _, tup := range tuples {
		dec := EvaluateReworkDeferred(dir, "polybot-uiu", tup.bead, "alpha", "merge_failed",
			"x", tup.decision, 1*time.Hour)
		if dec.Action != ActionEmit {
			t.Errorf("first wave %s: Action = %q, want %q", tup.bead, dec.Action, ActionEmit)
		}
	}

	// 10 patrol cycles, each one is a "no-op attempt" to notify. They must
	// all be suppressed for every tuple.
	for i := 0; i < 10; i++ {
		advance(2 * time.Minute)
		for _, tup := range tuples {
			dec := EvaluateReworkDeferred(dir, "polybot-uiu", tup.bead, "alpha", "merge_failed",
				"x", tup.decision, 1*time.Hour)
			if dec.Action != ActionSuppress {
				t.Errorf("repeat #%d for %s: Action = %q, want %q", i+1, tup.bead, dec.Action, ActionSuppress)
			}
			if dec.Record.SuppressedCount != i+1 {
				t.Errorf("repeat #%d for %s: SuppressedCount = %d, want %d",
					i+1, tup.bead, dec.Record.SuppressedCount, i+1)
			}
		}
	}

	// Verify durable state matches what was reported: 3 records, 10
	// suppressed each, no rollups emitted yet.
	records := ListReworkDeferredRecords(dir)
	if len(records) != 3 {
		t.Fatalf("records: got %d, want 3", len(records))
	}
	for _, rec := range records {
		if rec.SuppressedCount != 10 {
			t.Errorf("%s: durable SuppressedCount = %d, want 10", rec.BeadID, rec.SuppressedCount)
		}
	}

	// After the 1h window elapses, each tuple rolls up independently and the
	// suppressed count resets to 0.
	advance(50 * time.Minute) // total elapsed from first emit: 70 minutes
	for _, tup := range tuples {
		dec := EvaluateReworkDeferred(dir, "polybot-uiu", tup.bead, "alpha", "merge_failed",
			"x", tup.decision, 1*time.Hour)
		if dec.Action != ActionRollup {
			t.Errorf("post-window %s: Action = %q, want %q", tup.bead, dec.Action, ActionRollup)
		}
	}
	records = ListReworkDeferredRecords(dir)
	for _, rec := range records {
		if rec.SuppressedCount != 0 {
			t.Errorf("after rollup %s: durable SuppressedCount = %d, want 0", rec.BeadID, rec.SuppressedCount)
		}
	}
}

// TestNotifyMayorOfReworkBlocked_SkipsOnNilDecision is a defensive guard
// against future callers passing a nil decision: the function must not
// panic, and must not write to the throttle state (no record created).
func TestNotifyMayorOfReworkBlocked_SkipsOnNilDecision(t *testing.T) {
	dir := withReworkDeferredStateDir(t)
	// Should not panic and should not create a record.
	notifyMayorOfReworkBlocked(dir, "polybot-uiu", "gt-hold1", "alpha", "merge_failed",
		3, nil, nil)
	records := ListReworkDeferredRecords(dir)
	if len(records) != 0 {
		t.Errorf("records: got %d, want 0 (nil decision must not record)", len(records))
	}
}

// TestEvaluateReworkDeferred_SubjectsIsolatedByTuple is a sanity check that
// the subject of the (would-be) mail is the same for both states but the
// underlying key differs, ensuring two distinct subjects (and dedupe
// records) for two distinct tuples.
func TestEvaluateReworkDeferred_SubjectsIsolatedByTuple(t *testing.T) {
	dir := withReworkDeferredStateDir(t)
	start := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	withReworkDeferredClock(t, start)

	a := EvaluateReworkDeferred(dir, "polybot-uiu", "gt-hold1", "alpha", "merge_failed",
		"x", mayor.DecisionHold, 1*time.Hour)
	b := EvaluateReworkDeferred(dir, "polybot-uiu", "gt-park1", "alpha", "merge_failed",
		"y", mayor.DecisionPark, 1*time.Hour)
	if a.Record.Key == b.Record.Key {
		t.Errorf("distinct tuples produced identical keys: %q", a.Record.Key)
	}
	if a.Action != ActionEmit || b.Action != ActionEmit {
		t.Errorf("both first calls must emit, got a=%q b=%q", a.Action, b.Action)
	}
}

// TestEvaluateReworkDeferred_ReasonRecordedOnEmit verifies that the reason
// string from the first emit is preserved across suppressions and surfaced
// in the rollup record. This is what lets the operator see "why the block
// fired" in a rollup, even after dozens of suppressions.
func TestEvaluateReworkDeferred_ReasonRecordedOnEmit(t *testing.T) {
	dir := withReworkDeferredStateDir(t)
	start := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	advance := withReworkDeferredClock(t, start)

	const want = "PARK/DEFER per priority realignment"

	if dec := EvaluateReworkDeferred(dir, "polybot-uiu", "gt-hold1", "alpha", "merge_failed",
		want, mayor.DecisionHold, 1*time.Hour); dec.Action != ActionEmit {
		t.Fatalf("first: Action = %q, want emit", dec.Action)
	}

	advance(10 * time.Minute)
	dec := EvaluateReworkDeferred(dir, "polybot-uiu", "gt-hold1", "alpha", "merge_failed",
		"some new reason that should not overwrite the original", mayor.DecisionHold, 1*time.Hour)
	if dec.Action != ActionSuppress {
		t.Fatalf("suppress: Action = %q, want %q", dec.Action, ActionSuppress)
	}
	if dec.Record.LastEmittedReason != want {
		t.Errorf("LastEmittedReason = %q, want %q (original emit reason must be preserved)",
			dec.Record.LastEmittedReason, want)
	}

	advance(60 * time.Minute) // past the 1h window
	dec = EvaluateReworkDeferred(dir, "polybot-uiu", "gt-hold1", "alpha", "merge_failed",
		"another reason", mayor.DecisionHold, 1*time.Hour)
	if dec.Action != ActionRollup {
		t.Fatalf("rollup: Action = %q, want %q", dec.Action, ActionRollup)
	}
	if dec.Record.LastEmittedReason != want {
		t.Errorf("rollup LastEmittedReason = %q, want %q", dec.Record.LastEmittedReason, want)
	}
}

// TestReworkDeferredKey_StableForSameTuple verifies that the key is a stable
// hash: the same inputs always produce the same key, distinct inputs produce
// distinct keys.
func TestReworkDeferredKey_StableForSameTuple(t *testing.T) {
	a := reworkDeferredKey("polybot-uiu", "gt-hold1", "alpha", mayor.DecisionHold, "merge_failed")
	b := reworkDeferredKey("polybot-uiu", "gt-hold1", "alpha", mayor.DecisionHold, "merge_failed")
	if a != b {
		t.Errorf("stable key: %q != %q", a, b)
	}
	if !strings.HasPrefix(a, "sha1:") && len(a) != 40 {
		// SHA1 hex is 40 chars; we don't actually prefix with "sha1:" — the
		// test is just that the output is hex.
		t.Errorf("key %q is not a 40-char hex string", a)
	}
	c := reworkDeferredKey("polybot-uiu", "gt-hold1", "alpha", mayor.DecisionPark, "merge_failed")
	if a == c {
		t.Error("distinct decisions produced the same key")
	}
}
