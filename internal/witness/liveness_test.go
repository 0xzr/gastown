package witness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeRig creates a temp rig directory tree with the witness/ subdir.
// Returns the rig path and a cleanup.
func makeRig(t *testing.T) (string, func()) {
	t.Helper()
	tmp := t.TempDir()
	witnessDir := filepath.Join(tmp, "witness")
	if err := os.MkdirAll(witnessDir, 0o755); err != nil {
		t.Fatalf("mkdir witness: %v", err)
	}
	return tmp, func() {}
}

func TestHeartbeatFile(t *testing.T) {
	tmp, _ := makeRig(t)
	want := filepath.Join(tmp, "witness", "heartbeat.json")
	if got := HeartbeatFile(tmp); got != want {
		t.Errorf("HeartbeatFile() = %q, want %q", got, want)
	}
}

func TestWriteReadHeartbeat(t *testing.T) {
	tmp, _ := makeRig(t)
	hb := &Heartbeat{
		Timestamp:                      time.Now().UTC().Add(-30 * time.Second),
		Cycle:                          7,
		LastStep:                       "survey-workers",
		LastAction:                     "patrol-cycle",
		ContextSaturationPercent:       0.42,
		CommandDurationMs:              1234,
		OutstandingRecoveryObligations: 2,
		SessionStatus:                  "healthy",
		StoppedLanesSnapshot:           1,
	}
	if err := WriteHeartbeat(tmp, hb); err != nil {
		t.Fatalf("WriteHeartbeat error: %v", err)
	}
	loaded, err := ReadHeartbeat(tmp)
	if err != nil {
		t.Fatalf("ReadHeartbeat error: %v", err)
	}
	if loaded == nil {
		t.Fatal("ReadHeartbeat returned nil")
	}
	if loaded.Cycle != 7 {
		t.Errorf("Cycle = %d, want 7", loaded.Cycle)
	}
	if loaded.LastStep != "survey-workers" {
		t.Errorf("LastStep = %q, want survey-workers", loaded.LastStep)
	}
	if loaded.ContextSaturationPercent != 0.42 {
		t.Errorf("ContextSaturationPercent = %v, want 0.42", loaded.ContextSaturationPercent)
	}
	if loaded.CommandDurationMs != 1234 {
		t.Errorf("CommandDurationMs = %d, want 1234", loaded.CommandDurationMs)
	}
	if loaded.OutstandingRecoveryObligations != 2 {
		t.Errorf("OutstandingRecoveryObligations = %d, want 2", loaded.OutstandingRecoveryObligations)
	}
	if loaded.StoppedLanesSnapshot != 1 {
		t.Errorf("StoppedLanesSnapshot = %d, want 1", loaded.StoppedLanesSnapshot)
	}
}

func TestReadHeartbeatMissing(t *testing.T) {
	tmp, _ := makeRig(t)
	hb, err := ReadHeartbeat(tmp)
	if err != nil {
		t.Fatalf("ReadHeartbeat on missing file should return nil err, got %v", err)
	}
	if hb != nil {
		t.Errorf("ReadHeartbeat on missing file = %+v, want nil", hb)
	}
}

func TestReadHeartbeatCorrupt(t *testing.T) {
	tmp, _ := makeRig(t)
	hbFile := HeartbeatFile(tmp)
	if err := os.WriteFile(hbFile, []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	_, err := ReadHeartbeat(tmp)
	if err == nil {
		t.Error("ReadHeartbeat on corrupt file should return error")
	}
}

func TestTouchIncrementsCycle(t *testing.T) {
	tmp, _ := makeRig(t)
	if err := Touch(tmp, "inbox-check", "step", 0.1, 100*time.Millisecond, 0); err != nil {
		t.Fatalf("Touch error: %v", err)
	}
	first, err := ReadHeartbeat(tmp)
	if err != nil || first == nil {
		t.Fatalf("ReadHeartbeat: %+v err=%v", first, err)
	}
	if first.Cycle != 1 {
		t.Errorf("first Cycle = %d, want 1", first.Cycle)
	}
	if err := Touch(tmp, "survey-workers", "step", 0.2, 200*time.Millisecond, 1); err != nil {
		t.Fatalf("Touch second error: %v", err)
	}
	second, _ := ReadHeartbeat(tmp)
	if second.Cycle != 2 {
		t.Errorf("second Cycle = %d, want 2", second.Cycle)
	}
	if second.LastStep != "survey-workers" {
		t.Errorf("second LastStep = %q, want survey-workers", second.LastStep)
	}
	if second.ContextSaturationPercent != 0.2 {
		t.Errorf("second saturation = %v, want 0.2", second.ContextSaturationPercent)
	}
	if second.OutstandingRecoveryObligations != 1 {
		t.Errorf("second obligations = %d, want 1", second.OutstandingRecoveryObligations)
	}
}

func TestHeartbeatFreshStaleVeryStale(t *testing.T) {
	stale := 5 * time.Minute
	veryStale := 20 * time.Minute

	fresh := &Heartbeat{Timestamp: time.Now().Add(-1 * time.Minute)}
	if !fresh.IsFresh(stale) {
		t.Error("1m heartbeat should be fresh")
	}
	if fresh.IsStale(stale, veryStale) || fresh.IsVeryStale(veryStale) {
		t.Error("fresh heartbeat should not be stale or very stale")
	}

	mid := &Heartbeat{Timestamp: time.Now().Add(-10 * time.Minute)}
	if !mid.IsStale(stale, veryStale) {
		t.Error("10m heartbeat should be stale")
	}

	old := &Heartbeat{Timestamp: time.Now().Add(-25 * time.Minute)}
	if !old.IsVeryStale(veryStale) {
		t.Error("25m heartbeat should be very stale")
	}
}

func TestHeartbeatAgeNil(t *testing.T) {
	var nilHb *Heartbeat
	if nilHb.Age() < 24*time.Hour {
		t.Error("nil heartbeat should have very large age")
	}
}

func TestHeartbeatIsSaturated(t *testing.T) {
	hb := &Heartbeat{ContextSaturationPercent: 0.9}
	if !hb.IsSaturated(0.85) {
		t.Error("0.9 should be saturated at 0.85 threshold")
	}
	if hb.IsSaturated(0.95) {
		t.Error("0.9 should NOT be saturated at 0.95 threshold")
	}
	if !hb.IsSaturated(0.9) {
		t.Error("0.9 should be saturated at exactly 0.9 (>=) ")
	}
}

func TestHeartbeatShouldSelfRestart(t *testing.T) {
	saturation := 0.85
	veryStale := 20 * time.Minute
	maxCmd := 10 * time.Minute

	// Fresh + saturated → no restart.
	hb := &Heartbeat{
		Timestamp:                time.Now().Add(-1 * time.Minute),
		ContextSaturationPercent: 0.95,
	}
	if hb.ShouldSelfRestart(saturation, veryStale, maxCmd) {
		t.Error("fresh saturated heartbeat should not trigger restart")
	}

	// Very stale + not saturated → no restart.
	hb2 := &Heartbeat{
		Timestamp:                time.Now().Add(-25 * time.Minute),
		ContextSaturationPercent: 0.5,
	}
	if hb2.ShouldSelfRestart(saturation, veryStale, maxCmd) {
		t.Error("very stale but not saturated should not trigger restart")
	}

	// Very stale + saturated → restart.
	hb3 := &Heartbeat{
		Timestamp:                time.Now().Add(-25 * time.Minute),
		ContextSaturationPercent: 0.95,
	}
	if !hb3.ShouldSelfRestart(saturation, veryStale, maxCmd) {
		t.Error("very stale + saturated should trigger restart")
	}

	// Fresh + saturated + long command → restart (maxCmd 0 disables).
	hb4 := &Heartbeat{
		Timestamp:                time.Now().Add(-1 * time.Minute),
		ContextSaturationPercent: 0.95,
		CommandDurationMs:        int64((15 * time.Minute).Milliseconds()),
	}
	if !hb4.ShouldSelfRestart(saturation, veryStale, maxCmd) {
		t.Error("saturated with long command should trigger restart regardless of staleness")
	}
}

func TestHandoffFilePath(t *testing.T) {
	tmp, _ := makeRig(t)
	want := filepath.Join(tmp, "witness", "handoff.json")
	if got := HandoffFilePath(tmp); got != want {
		t.Errorf("HandoffFilePath() = %q, want %q", got, want)
	}
}

func TestWriteReadClearHandoff(t *testing.T) {
	tmp, _ := makeRig(t)
	hf := &HandoffFile{
		Reason:    "context-saturated",
		LastStep:  "context-check",
		LastCycle: 12,
		RigName:   "gastown",
		StoppedLanes: []StoppedLane{
			{Polecat: "jasper", Bead: "b1", IssueID: "gastown-c76", Reason: "session-dead", ObservedAt: time.Now().UTC()},
			{Polecat: "obsidian", Bead: "b2", IssueID: "gastown-cet.16", Reason: "agent-hung", ObservedAt: time.Now().UTC()},
		},
		DirtyLanes: []DirtyLane{
			{Polecat: "topaz", UncommittedCount: 0, UnpushedCount: 1, ObservedAt: time.Now().UTC()},
		},
		QueuedSchedulerBeads:     []string{"gastown-sched-1"},
		InFlightCleanup:          []string{"gt session restart gastown/jasper"},
		ContextSaturationPercent: 0.95,
		Notes:                    "Refinery idle, witness patrol stuck at context-check",
	}
	if err := WriteHandoff(tmp, hf); err != nil {
		t.Fatalf("WriteHandoff error: %v", err)
	}
	loaded, err := ReadHandoff(tmp)
	if err != nil {
		t.Fatalf("ReadHandoff error: %v", err)
	}
	if loaded == nil {
		t.Fatal("ReadHandoff returned nil")
	}
	if loaded.RigName != "gastown" {
		t.Errorf("RigName = %q, want gastown", loaded.RigName)
	}
	if len(loaded.StoppedLanes) != 2 {
		t.Fatalf("StoppedLanes len = %d, want 2", len(loaded.StoppedLanes))
	}
	// StoppedLanes must be sorted by polecat name (j before o).
	if loaded.StoppedLanes[0].Polecat != "jasper" {
		t.Errorf("StoppedLanes[0] = %q, want jasper", loaded.StoppedLanes[0].Polecat)
	}
	if loaded.StoppedLanes[1].Polecat != "obsidian" {
		t.Errorf("StoppedLanes[1] = %q, want obsidian", loaded.StoppedLanes[1].Polecat)
	}
	if len(loaded.InFlightCleanup) != 1 {
		t.Errorf("InFlightCleanup len = %d, want 1", len(loaded.InFlightCleanup))
	}
	if loaded.ContextSaturationPercent != 0.95 {
		t.Errorf("saturation = %v, want 0.95", loaded.ContextSaturationPercent)
	}

	// ClearHandoff removes the file.
	if err := ClearHandoff(tmp); err != nil {
		t.Fatalf("ClearHandoff: %v", err)
	}
	loaded, err = ReadHandoff(tmp)
	if err != nil {
		t.Fatalf("ReadHandoff after clear: %v", err)
	}
	if loaded != nil {
		t.Errorf("ReadHandoff after clear = %+v, want nil", loaded)
	}
}

func TestRecordRecoveryAttempt(t *testing.T) {
	tmp := t.TempDir()
	attempt := &RecoveryAttempt{
		Reason: "gastown:context-saturated-stalled",
		BeforeRestart: &Heartbeat{
			Timestamp:                time.Now().UTC().Add(-25 * time.Minute),
			ContextSaturationPercent: 0.96,
		},
	}
	if err := RecordRecoveryAttempt(tmp, attempt); err != nil {
		t.Fatalf("RecordRecoveryAttempt error: %v", err)
	}
	entries, err := os.ReadDir(RecoveryAttemptsDir(tmp))
	if err != nil {
		t.Fatalf("reading recovery dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 ledger file, got %d", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(RecoveryAttemptsDir(tmp), entries[0].Name()))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	var loaded RecoveryAttempt
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("parse ledger: %v", err)
	}
	if loaded.Reason != "gastown:context-saturated-stalled" {
		t.Errorf("Reason = %q", loaded.Reason)
	}
	if loaded.BeforeRestart == nil || loaded.BeforeRestart.ContextSaturationPercent != 0.96 {
		t.Error("BeforeRestart not preserved")
	}
	if loaded.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}
}

func TestIsRestartOnCooldown(t *testing.T) {
	tmp := t.TempDir()
	cooldown := 10 * time.Minute
	rigName := "gastown"

	// No prior attempts → not on cooldown.
	onCD, _, err := IsRestartOnCooldown(tmp, rigName, cooldown)
	if err != nil {
		t.Fatalf("IsRestartOnCooldown empty: %v", err)
	}
	if onCD {
		t.Error("empty ledger should not be on cooldown")
	}

	// Record an attempt just now → on cooldown.
	attempt := &RecoveryAttempt{
		Timestamp: time.Now().UTC(),
		Reason:    rigName + ":context-saturated-stalled",
	}
	if err := RecordRecoveryAttempt(tmp, attempt); err != nil {
		t.Fatalf("record: %v", err)
	}
	onCD, when, err := IsRestartOnCooldown(tmp, rigName, cooldown)
	if err != nil {
		t.Fatalf("IsRestartOnCooldown after record: %v", err)
	}
	if !onCD {
		t.Error("recent attempt should put us on cooldown")
	}
	if when.IsZero() {
		t.Error("cooldown timestamp should be returned")
	}

	// Different rig → not on cooldown.
	onCD, _, _ = IsRestartOnCooldown(tmp, "longeye", cooldown)
	if onCD {
		t.Error("different rig should not match the cooldown reason")
	}

	// Old attempt (well beyond cooldown) → not on cooldown.
	tmp2 := t.TempDir()
	old := &RecoveryAttempt{
		Timestamp: time.Now().UTC().Add(-30 * time.Minute),
		Reason:    rigName + ":context-saturated-stalled",
	}
	if err := RecordRecoveryAttempt(tmp2, old); err != nil {
		t.Fatalf("record old: %v", err)
	}
	onCD, _, _ = IsRestartOnCooldown(tmp2, rigName, cooldown)
	if onCD {
		t.Error("30m-old attempt with 10m cooldown should not be on cooldown")
	}
}

func TestResolveThresholdsDefaults(t *testing.T) {
	got := ResolveThresholds("")
	if got.StaleThreshold != DefaultHeartbeatStaleThreshold {
		t.Errorf("StaleThreshold = %v, want %v", got.StaleThreshold, DefaultHeartbeatStaleThreshold)
	}
	if got.VeryStaleThreshold != DefaultHeartbeatVeryStaleThreshold {
		t.Errorf("VeryStaleThreshold = %v, want %v", got.VeryStaleThreshold, DefaultHeartbeatVeryStaleThreshold)
	}
	if got.ContextSaturationThreshold != DefaultContextSaturationThreshold {
		t.Errorf("ContextSaturationThreshold = %v, want %v", got.ContextSaturationThreshold, DefaultContextSaturationThreshold)
	}
	if got.RecoveryCooldown != DefaultRecoveryCooldown {
		t.Errorf("RecoveryCooldown = %v, want %v", got.RecoveryCooldown, DefaultRecoveryCooldown)
	}
}

func TestSupervisorShouldRestart_NoHeartbeat(t *testing.T) {
	sup := NewSupervisor("", t.TempDir(), "gastown", "")
	// No heartbeat file → no restart.
	should, _ := sup.ShouldRestart()
	if should {
		t.Error("missing heartbeat should not trigger restart")
	}
}

func TestSupervisorShouldRestart_FreshHeartbeat(t *testing.T) {
	rig := t.TempDir()
	sup := NewSupervisor("", rig, "gastown", "")
	// Fresh + saturated → no restart.
	if err := Touch(rig, "context-check", "step", 0.95, 0, 0); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	should, _ := sup.ShouldRestart()
	if should {
		t.Error("fresh saturated heartbeat should not trigger restart")
	}
}

func TestSupervisorShouldRestart_StaleSaturated(t *testing.T) {
	rig := t.TempDir()
	sup := NewSupervisor(t.TempDir(), rig, "gastown", "")
	// Write a saturated heartbeat, then backdate it past very-stale.
	hb := &Heartbeat{
		Timestamp:                time.Now().Add(-25 * time.Minute),
		ContextSaturationPercent: 0.95,
		LastStep:                 "context-check",
	}
	if err := WriteHeartbeat(rig, hb); err != nil {
		t.Fatalf("WriteHeartbeat: %v", err)
	}
	should, reason := sup.ShouldRestart()
	if !should {
		t.Error("stale + saturated heartbeat should trigger restart")
	}
	if !strings.Contains(reason, "gastown:") {
		t.Errorf("reason should include rig prefix, got %q", reason)
	}
}

func TestSupervisorShouldRestart_CooldownBlocks(t *testing.T) {
	rig := t.TempDir()
	town := t.TempDir()
	sup := NewSupervisor(town, rig, "gastown", "")
	// Write stale + saturated heartbeat.
	hb := &Heartbeat{
		Timestamp:                time.Now().Add(-25 * time.Minute),
		ContextSaturationPercent: 0.95,
	}
	if err := WriteHeartbeat(rig, hb); err != nil {
		t.Fatalf("WriteHeartbeat: %v", err)
	}
	// Record a recent attempt for the same rig.
	_ = RecordRecoveryAttempt(town, &RecoveryAttempt{
		Timestamp: time.Now().UTC(),
		Reason:    "gastown:context-saturated-stalled",
	})
	should, reason := sup.ShouldRestart()
	if should {
		t.Errorf("cooldown should block restart, got should=true reason=%q", reason)
	}
	if !strings.HasPrefix(reason, "on-cooldown-since-") {
		t.Errorf("reason should indicate cooldown, got %q", reason)
	}
}

func TestSupervisorRoundTrip(t *testing.T) {
	rig := t.TempDir()
	sup := NewSupervisor(t.TempDir(), rig, "gastown", "")
	if err := sup.Touch("inbox-check", "patrol-cycle", 0.42, 250*time.Millisecond, 1); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	hb := sup.ReadHeartbeat()
	if hb == nil || hb.LastStep != "inbox-check" {
		t.Errorf("Touch+ReadHeartbeat round-trip failed: %+v", hb)
	}
	if err := sup.WriteHandoff(&HandoffFile{
		Reason:   "test",
		RigName:  "gastown",
		LastStep: "inbox-check",
	}); err != nil {
		t.Fatalf("WriteHandoff: %v", err)
	}
	hf := sup.ReadHandoff()
	if hf == nil || hf.RigName != "gastown" {
		t.Errorf("WriteHandoff+ReadHandoff round-trip failed: %+v", hf)
	}
	if err := sup.ClearHandoff(); err != nil {
		t.Fatalf("ClearHandoff: %v", err)
	}
	if hf := sup.ReadHandoff(); hf != nil {
		t.Errorf("ClearHandoff should remove file, still got %+v", hf)
	}
}

func TestVerifyAgentViaGT_MissingBinary(t *testing.T) {
	// Point gtPath at a non-existent binary; expect a non-panicking
	// result with an Error field set.
	result, _ := verifyAgentViaGT("/no/such/binary-xyz")
	if result == nil {
		t.Fatal("verifyAgentViaGT returned nil result")
	}
	if result.Error == "" {
		t.Error("verifyAgentViaGT with missing binary should set Error")
	}
}

func TestHandoffIncludesAllRequiredFields(t *testing.T) {
	// Acceptance criterion: "writes a durable handoff including stopped
	// lanes, dirty/ahead state, queued scheduler beads, and any
	// in-flight cleanup command". This test pins the schema so a
	// future refactor cannot drop a field silently.
	tmp, _ := makeRig(t)
	hf := &HandoffFile{
		Reason:   "context-saturated",
		LastStep: "context-check",
		RigName:  "gastown",
		StoppedLanes: []StoppedLane{
			{Polecat: "jasper", Reason: "session-dead", ObservedAt: time.Now().UTC()},
			{Polecat: "obsidian", Reason: "agent-hung", ObservedAt: time.Now().UTC()},
			{Polecat: "onyx", Reason: "session-dead", ObservedAt: time.Now().UTC()},
			{Polecat: "opal", Reason: "session-dead", ObservedAt: time.Now().UTC()},
		},
		DirtyLanes: []DirtyLane{
			{Polecat: "topaz", UnpushedCount: 2, ObservedAt: time.Now().UTC()},
		},
		QueuedSchedulerBeads: []string{
			"gastown-sched-1",
			"gastown-sched-2",
		},
		InFlightCleanup: []string{
			"gt session restart gastown/jasper",
			"gt session restart gastown/obsidian",
		},
		ContextSaturationPercent: 0.97,
	}
	if err := WriteHandoff(tmp, hf); err != nil {
		t.Fatalf("WriteHandoff: %v", err)
	}
	loaded, err := ReadHandoff(tmp)
	if err != nil {
		t.Fatalf("ReadHandoff: %v", err)
	}
	if len(loaded.StoppedLanes) != 4 {
		t.Errorf("StoppedLanes lost: got %d, want 4", len(loaded.StoppedLanes))
	}
	if len(loaded.DirtyLanes) != 1 {
		t.Errorf("DirtyLanes lost: got %d, want 1", len(loaded.DirtyLanes))
	}
	if len(loaded.QueuedSchedulerBeads) != 2 {
		t.Errorf("QueuedSchedulerBeads lost: got %d, want 2", len(loaded.QueuedSchedulerBeads))
	}
	if len(loaded.InFlightCleanup) != 2 {
		t.Errorf("InFlightCleanup lost: got %d, want 2", len(loaded.InFlightCleanup))
	}
	// Verify the 2026-06-25 incident lanes are present.
	laneSet := map[string]bool{}
	for _, l := range loaded.StoppedLanes {
		laneSet[l.Polecat] = true
	}
	for _, want := range []string{"jasper", "obsidian", "onyx", "opal"} {
		if !laneSet[want] {
			t.Errorf("expected %s in stopped lanes (per 2026-06-25 incident)", want)
		}
	}
}

// TestHandoffAtomicity verifies the handoff write uses a tmp file + rename
// so a crash mid-write cannot leave a partial file. We simulate this by
// listing the witness dir after a write and confirming no .tmp leftovers.
func TestHandoffAtomicity(t *testing.T) {
	tmp, _ := makeRig(t)
	for i := 0; i < 5; i++ {
		if err := WriteHandoff(tmp, &HandoffFile{Reason: "test", RigName: "gastown"}); err != nil {
			t.Fatalf("WriteHandoff: %v", err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(tmp, "witness"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("found leftover tmp file %s", e.Name())
		}
	}
}

func TestParseDurationOrZero(t *testing.T) {
	if got := ParseDurationOrZero(""); got != 0 {
		t.Errorf("empty = %v, want 0", got)
	}
	if got := ParseDurationOrZero("not-a-duration"); got != 0 {
		t.Errorf("invalid = %v, want 0", got)
	}
	if got := ParseDurationOrZero("5m"); got != 5*time.Minute {
		t.Errorf("5m = %v, want 5m", got)
	}
}

func TestEnsureHandoff_SynthesizesFromHeartbeat(t *testing.T) {
	// We can't easily spin up a real Witness tmux session in a unit
	// test, so we cover the synthetic-handoff path by populating only
	// a heartbeat and verifying the supervisor creates a handoff
	// when none exists. RestartWitness also calls EnsureHandoff, but
	// testing it directly avoids the manager dependency.
	rig := t.TempDir()
	town := t.TempDir()
	sup := NewSupervisor(town, rig, "gastown", "/no/such/gt")

	hb := &Heartbeat{
		Timestamp:                time.Now().Add(-25 * time.Minute),
		ContextSaturationPercent: 0.95,
		LastStep:                 "context-check",
	}
	if err := WriteHeartbeat(rig, hb); err != nil {
		t.Fatalf("WriteHeartbeat: %v", err)
	}

	// No prior handoff — supervisor must synthesize one.
	path, err := sup.EnsureHandoff("test:synthetic-handoff")
	if err != nil {
		t.Fatalf("EnsureHandoff: %v", err)
	}
	if path == "" {
		t.Fatal("EnsureHandoff returned empty path with heartbeat present")
	}
	hf, err := ReadHandoff(rig)
	if err != nil {
		t.Fatalf("ReadHandoff: %v", err)
	}
	if hf == nil {
		t.Fatal("synthesized handoff not written")
	}
	if hf.LastStep != "context-check" {
		t.Errorf("synth handoff LastStep = %q, want context-check", hf.LastStep)
	}
	if hf.ContextSaturationPercent != 0.95 {
		t.Errorf("synth handoff saturation = %v, want 0.95", hf.ContextSaturationPercent)
	}

	// Second call must NOT overwrite the existing handoff.
	hf.Reason = "preserved"
	if err := WriteHandoff(rig, hf); err != nil {
		t.Fatalf("rewrite handoff: %v", err)
	}
	if _, err := sup.EnsureHandoff("test:should-not-overwrite"); err != nil {
		t.Fatalf("EnsureHandoff second call: %v", err)
	}
	hf2, _ := ReadHandoff(rig)
	if hf2.Reason != "preserved" {
		t.Errorf("EnsureHandoff overwrote existing handoff: reason=%q", hf2.Reason)
	}
}

// TestSimulatedStalledWitnessRecoversWithoutMayor is the smoke
// harness for the 2026-06-25 incident acceptance criterion. It
// simulates:
//
//   - A 100% context Witness that has stopped making forward progress
//     (very stale heartbeat, saturation = 1.0).
//   - Multiple stopped polecats with hooked work (the jasper/obsidian/onyx/
//     opal lanes from the incident).
//   - Obsidian has a missing model assignment (the actual failure
//     mode surfaced on 2026-06-25).
//   - Verifies the supervisor's ShouldRestart reports yes, the
//     handoff file captures all stopped lanes, and the recovery
//     plan model-preserves every lane with a durable assignment
//     while escalating obsidian so the operator (or a follow-up
//     tool) can resolve the missing assignment manually.
//
// We do not actually restart a real Witness tmux session here; that
// requires the integration test fixture. The unit-level guarantee
// is that the recovery primitives see the right signals, serialize
// the right state to the durable handoff file, and produce a
// recovery plan that does not silently rotate any agent.

// Sanity: empty townRoot or rigPath return safe defaults everywhere
// (defensive against tests that don't set up the full directory tree).
func TestDefaultsSafeWithEmptyDirs(t *testing.T) {
	sup := NewSupervisor("", "", "gastown", "")
	if sup.TownRoot() != "" || sup.RigPath() != "" || sup.RigName() != "gastown" {
		t.Errorf("supervisor not initialized: %+v", sup)
	}
	if sup.ReadHeartbeat() != nil {
		t.Error("ReadHeartbeat on empty rig should return nil")
	}
	if sup.ReadHandoff() != nil {
		t.Error("ReadHandoff on empty rig should return nil")
	}
	if err := sup.ClearHandoff(); err != nil {
		t.Errorf("ClearHandoff on empty rig should not error: %v", err)
	}
}

// TestStoppedLaneRoundTripsAssignmentMetadata pins the durable
// assignment-metadata fields on StoppedLane so a future refactor
// cannot drop them silently. Acceptance criterion: handoff must
// carry durable assignment metadata for stopped lanes (gastown-o9d).
func TestStoppedLaneRoundTripsAssignmentMetadata(t *testing.T) {
	tmp, _ := makeRig(t)
	now := time.Now().UTC()
	hf := &HandoffFile{
		Reason:   "context-saturated",
		RigName:  "gastown",
		LastStep: "context-check",
		StoppedLanes: []StoppedLane{
			{
				Polecat:          "topaz",
				Bead:             "gt-gastown-polecat-topaz",
				IssueID:          "gastown-o9d",
				Reason:           "session-dead",
				ObservedAt:       now,
				AssignedAgent:    "kimi",
				BeadKey:          "gastown-o9d",
				AssignmentSource: AssignmentSourceModelAssignments,
			},
		},
	}
	if err := WriteHandoff(tmp, hf); err != nil {
		t.Fatalf("WriteHandoff: %v", err)
	}
	loaded, err := ReadHandoff(tmp)
	if err != nil || loaded == nil {
		t.Fatalf("ReadHandoff: %+v err=%v", loaded, err)
	}
	if len(loaded.StoppedLanes) != 1 {
		t.Fatalf("StoppedLanes lost: got %d, want 1", len(loaded.StoppedLanes))
	}
	got := loaded.StoppedLanes[0]
	if got.AssignedAgent != "kimi" {
		t.Errorf("AssignedAgent = %q, want kimi", got.AssignedAgent)
	}
	if got.BeadKey != "gastown-o9d" {
		t.Errorf("BeadKey = %q, want gastown-o9d", got.BeadKey)
	}
	if got.AssignmentSource != AssignmentSourceModelAssignments {
		t.Errorf("AssignmentSource = %q, want %q", got.AssignmentSource, AssignmentSourceModelAssignments)
	}
}

// TestHandoffReplayer_ModelPreservingFromHandoff is the central
// regression test for the 2026-06-25 obsidian/GLM incident. When a
// handoff records a durable agent assignment for a stopped lane, the
// replayer must emit a restart command that pins the agent —
// preventing a silent rotation to the rig role default.
func TestHandoffReplayer_ModelPreservingFromHandoff(t *testing.T) {
	rig := t.TempDir()
	town := t.TempDir()
	rep := NewHandoffReplayer(town, rig, "gastown", "/usr/bin/gt")

	hf := &HandoffFile{
		Reason:   "context-saturated",
		RigName:  "gastown",
		LastStep: "context-check",
		StoppedLanes: []StoppedLane{
			{
				Polecat:          "obsidian",
				IssueID:          "gastown-cet.16",
				Reason:           "agent-hung",
				ObservedAt:       time.Now().UTC(),
				AssignedAgent:    "kimi",
				BeadKey:          "gastown-cet.16",
				AssignmentSource: AssignmentSourceModelAssignments,
			},
		},
	}
	plan, err := rep.Replay(hf)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("Actions len = %d, want 1 (model-preserving restart)", len(plan.Actions))
	}
	if len(plan.Escalations) != 0 {
		t.Errorf("Escalations should be empty when assignment is durable, got %d", len(plan.Escalations))
	}
	got := plan.Actions[0]
	if got.Polecat != "obsidian" {
		t.Errorf("Polecat = %q, want obsidian", got.Polecat)
	}
	if got.AssignedAgent != "kimi" {
		t.Errorf("AssignedAgent = %q, want kimi (model-preserving)", got.AssignedAgent)
	}
	want := "/usr/bin/gt session start gastown/obsidian --agent kimi"
	if got.Command != want {
		t.Errorf("Command = %q, want %q", got.Command, want)
	}
	if got.Escalate {
		t.Error("Escalate should be false when assignment is durable")
	}
}

// TestHandoffReplayer_EscalatesMissingAssignment is the exact
// 2026-06-25 failure mode: a stopped lane with no durable
// assignment. The replayer must NOT silently rotate the agent —
// it must surface the lane as an escalation. This is what the
// bead's follow-up evidence demanded ("raw/manual gt session start
// is not sufficient when assignment metadata is missing").
func TestHandoffReplayer_EscalatesMissingAssignment(t *testing.T) {
	rig := t.TempDir()
	town := t.TempDir()
	rep := NewHandoffReplayer(town, rig, "gastown", "")

	hf := &HandoffFile{
		Reason:   "context-saturated",
		RigName:  "gastown",
		LastStep: "context-check",
		StoppedLanes: []StoppedLane{
			// Legacy lane with no durable assignment metadata
			// (mirrors the 2026-06-25 obsidian situation where
			// the wrapper had not yet written model-assignments
			// for that bead).
			{Polecat: "obsidian", IssueID: "gastown-cet.16", Reason: "agent-hung", ObservedAt: time.Now().UTC()},
		},
	}
	plan, err := rep.Replay(hf)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(plan.Actions) != 0 {
		t.Errorf("Actions should be empty for unassigned lane, got %d", len(plan.Actions))
	}
	if len(plan.Escalations) != 1 {
		t.Fatalf("Escalations len = %d, want 1", len(plan.Escalations))
	}
	got := plan.Escalations[0]
	if got.Polecat != "obsidian" {
		t.Errorf("Escalation Polecat = %q, want obsidian", got.Polecat)
	}
	if got.AssignedAgent != "" {
		t.Errorf("Escalation should have empty AssignedAgent, got %q", got.AssignedAgent)
	}
	if got.Command != "" {
		t.Errorf("Escalation should have empty Command (do not silently rotate), got %q", got.Command)
	}
	if !got.Escalate {
		t.Error("Escalate should be true for unassigned lane")
	}
	if got.AssignmentSource != AssignmentSourceUnassigned {
		t.Errorf("AssignmentSource = %q, want %q", got.AssignmentSource, AssignmentSourceUnassigned)
	}
}

// TestHandoffReplayer_EscalatesConfigDefaultAssignment: even a
// populated "config-default" source is non-durable (it just means
// the rig's role default) and must be escalated. The whole point
// of the durable-assignment gate is to prevent silent model
// rotations.
func TestHandoffReplayer_EscalatesConfigDefaultAssignment(t *testing.T) {
	rep := NewHandoffReplayer(t.TempDir(), t.TempDir(), "gastown", "")
	hf := &HandoffFile{
		StoppedLanes: []StoppedLane{
			{
				Polecat:          "onyx",
				AssignedAgent:    "claude",
				AssignmentSource: AssignmentSourceConfigDefault,
			},
		},
	}
	plan, err := rep.Replay(hf)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(plan.Actions) != 0 {
		t.Errorf("Actions should be empty when source is config-default, got %d", len(plan.Actions))
	}
	if len(plan.Escalations) != 1 || !plan.Escalations[0].Escalate {
		t.Errorf("expected escalation for config-default source, got %+v", plan.Escalations)
	}
}

// TestValidIdentifier is the regression guard for gastown-c4r finding #1:
// the only values permitted to become restart-command arguments are those
// passing this schema. Anything that could break out of an argv token —
// shell metacharacters, whitespace, path traversal, command separators —
// must be rejected. The original vulnerability interpolated these fields
// into a string later passed to `sh -c`; even though the apply path no
// longer shells, validIdentifier is still the choke point planLane uses
// to decide escalate-vs-execute.
func TestValidIdentifier(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"simple", "onyx", true},
		{"alphanumeric", "polecat42", true},
		{"with dot", "gastown-cet.16", true},
		{"with dash", "obsidian-xl", true},
		{"with underscore", "kimi_k2", true},
		{"leading dash", "-x", true}, // schema permits; apply-time still safe (argv token)
		{"shell injection semicolon", "claude;rm -rf /", false},
		{"shell injection pipe", "claude|cat /etc/passwd", false},
		{"shell injection backtick", "claude`whoami`", false},
		{"shell injection dollar", "claude$(id)", false},
		{"shell injection and", "claude&&reboot", false},
		{"whitespace", "claude sonnet", false},
		{"newline", "claude\n--agent", false},
		{"path traversal", "../etc/passwd", false},
		{"subshell", "claude(whoami)", false},
		{"redirect", "claude>/etc/shadow", false},
		{"unicode", "claudeé", false},
		{"null byte", "claude\x00", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validIdentifier(tc.in); got != tc.want {
				t.Errorf("validIdentifier(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestLaneRecovery_ApplyArgs is the regression guard for gastown-c4r
// finding #1: the apply phase builds argv directly from structured
// fields and never invokes a shell. A tainted lane must refuse to
// produce an argv (returns an error) rather than emit a vector an
// exec.Command could run. A clean lane produces the static
// allow-listed form with no shell and no field interpolation into a
// string.
func TestLaneRecovery_ApplyArgs(t *testing.T) {
	// Clean lane → static argv form, gt path preserved.
	clean := LaneRecovery{Polecat: "onyx", AssignedAgent: "codex"}
	got, err := clean.ApplyArgs("/usr/bin/gt", "gastown")
	if err != nil {
		t.Fatalf("clean ApplyArgs: %v", err)
	}
	want := []string{"/usr/bin/gt", "session", "start", "gastown/onyx", "--agent", "codex"}
	if len(got) != len(want) {
		t.Fatalf("argv len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// Escalate set → never executable.
	esc := LaneRecovery{Polecat: "onyx", AssignedAgent: "codex", Escalate: true}
	if _, err := esc.ApplyArgs("gt", "gastown"); err == nil {
		t.Error("ApplyArgs on an escalation must error, not return argv")
	}

	// Tainted polecat → refused even if agent is clean.
	tainted := LaneRecovery{Polecat: "onyx;rm -rf /", AssignedAgent: "codex"}
	if _, err := tainted.ApplyArgs("gt", "gastown"); err == nil {
		t.Error("ApplyArgs must refuse a tainted polecat identifier")
	}

	// Tainted agent → refused.
	taintedAgent := LaneRecovery{Polecat: "onyx", AssignedAgent: "codex$(pwn)"}
	if _, err := taintedAgent.ApplyArgs("gt", "gastown"); err == nil {
		t.Error("ApplyArgs must refuse a tainted agent identifier")
	}

	// Tainted rig name → refused (defense in depth).
	if _, err := clean.ApplyArgs("gt", "gastown;rm -rf /"); err == nil {
		t.Error("ApplyArgs must refuse a tainted rig name")
	}

	// Empty gt path → defaults to "gt" (the supervisor default).
	gotDefault, err := clean.ApplyArgs("", "gastown")
	if err != nil {
		t.Fatalf("ApplyArgs with empty gtPath: %v", err)
	}
	if gotDefault[0] != "gt" {
		t.Errorf("argv[0] = %q, want \"gt\" default", gotDefault[0])
	}
}

// TestHandoffReplayer_TaintedAssignmentEscalates proves the #1 hotfix
// end-to-end at the plan layer: a handoff lane whose pre-populated
// AssignedAgent contains shell metacharacters — written by a
// non-supervisor code path — is escalated to the Mayor rather than
// turned into a restart Command string. Before the fix, this field was
// interpolated into a string the apply phase passed to `sh -c`.
func TestHandoffReplayer_TaintedAssignmentEscalates(t *testing.T) {
	rep := NewHandoffReplayer(t.TempDir(), t.TempDir(), "gastown", "/usr/bin/gt")
	hf := &HandoffFile{
		StoppedLanes: []StoppedLane{
			{
				Polecat:          "onyx",
				AssignedAgent:    "claude;rm -rf /", // injected field
				AssignmentSource: AssignmentSourceAgentBead,
			},
		},
	}
	plan, err := rep.Replay(hf)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(plan.Actions) != 0 {
		t.Errorf("Actions should be empty for tainted assignment, got %d (%+v)",
			len(plan.Actions), plan.Actions)
	}
	if len(plan.Escalations) != 1 || !plan.Escalations[0].Escalate {
		t.Fatalf("expected 1 escalation for tainted assignment, got %+v", plan.Escalations)
	}
	esc := plan.Escalations[0]
	if esc.Polecat != "onyx" {
		t.Errorf("escalation Polecat = %q, want onyx", esc.Polecat)
	}
	// The injection payload must not survive into the reason or any
	// command-shaped field.
	if strings.Contains(esc.Reason, "rm -rf") {
		t.Errorf("escalation Reason leaked injection payload: %q", esc.Reason)
	}
	// And no action's Command may carry the payload (defensive: there
	// are zero actions, but assert the invariant explicitly).
	for _, a := range plan.Actions {
		if strings.Contains(a.Command, "rm -rf") {
			t.Errorf("action Command leaked injection payload: %q", a.Command)
		}
	}
}

// TestHandoffReplayer_ResolverTaintedEscalates proves the #1 hotfix
// holds on the resolver path too: even if a resolver returns a durable
// source, a tainted agent value is escalated rather than shelled.
func TestHandoffReplayer_ResolverTaintedEscalates(t *testing.T) {
	rep := NewHandoffReplayerWithResolver(
		t.TempDir(), t.TempDir(), "gastown", "",
		func(rigPath, polecat, beadKey string) (string, string) {
			// Resolver returns an injected agent with a durable source.
			return "claude$(reboot)", AssignmentSourceModelAssignments
		},
	)
	hf := &HandoffFile{
		StoppedLanes: []StoppedLane{
			{Polecat: "onyx", BeadKey: "gastown-x"},
		},
	}
	plan, err := rep.Replay(hf)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(plan.Actions) != 0 {
		t.Errorf("Actions should be empty for tainted resolver agent, got %d", len(plan.Actions))
	}
	if len(plan.Escalations) != 1 || !plan.Escalations[0].Escalate {
		t.Fatalf("expected escalation for tainted resolver agent, got %+v", plan.Escalations)
	}
}

// TestHandoffReplayer_ResolverFallback: when the handoff has no
// pre-populated assignment but a custom AssignmentResolver is
// installed (e.g. backed by the wrapper's readModelAssignment),
// the replayer must consult it and use the resolved agent.
func TestHandoffReplayer_ResolverFallback(t *testing.T) {
	rep := NewHandoffReplayerWithResolver(
		t.TempDir(), t.TempDir(), "gastown", "/bin/gt",
		func(rigPath, polecat, beadKey string) (string, string) {
			if polecat == "onyx" {
				return "codex", AssignmentSourceModelAssignments
			}
			return "", ""
		},
	)
	hf := &HandoffFile{
		StoppedLanes: []StoppedLane{
			// No pre-populated assignment — resolver must supply it.
			{Polecat: "onyx", BeadKey: "gastown-x"},
		},
	}
	plan, err := rep.Replay(hf)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("Actions len = %d, want 1 (resolver-supplied)", len(plan.Actions))
	}
	got := plan.Actions[0]
	if got.AssignedAgent != "codex" {
		t.Errorf("AssignedAgent = %q, want codex", got.AssignedAgent)
	}
	if got.AssignmentSource != AssignmentSourceModelAssignments {
		t.Errorf("AssignmentSource = %q, want %q", got.AssignmentSource, AssignmentSourceModelAssignments)
	}
	if !strings.Contains(got.Command, "--agent codex") {
		t.Errorf("Command = %q, must pin --agent codex", got.Command)
	}
}

// TestHandoffReplayer_ResolverFallsBackEscalates: a resolver that
// returns an empty agent must not produce an action — the lane
// must be escalated instead.
func TestHandoffReplayer_ResolverFallsBackEscalates(t *testing.T) {
	rep := NewHandoffReplayerWithResolver(
		t.TempDir(), t.TempDir(), "gastown", "",
		func(rigPath, polecat, beadKey string) (string, string) {
			return "", ""
		},
	)
	hf := &HandoffFile{
		StoppedLanes: []StoppedLane{
			{Polecat: "opal"},
		},
	}
	plan, err := rep.Replay(hf)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(plan.Escalations) != 1 {
		t.Fatalf("Escalations len = %d, want 1", len(plan.Escalations))
	}
	if plan.Escalations[0].Polecat != "opal" {
		t.Errorf("Polecat = %q, want opal", plan.Escalations[0].Polecat)
	}
}

// TestHandoffReplayer_EmptyAndNilHandoff guard rails.
func TestHandoffReplayer_EmptyAndNilHandoff(t *testing.T) {
	rep := NewHandoffReplayer(t.TempDir(), t.TempDir(), "gastown", "")
	if _, err := rep.Replay(nil); err == nil {
		t.Error("Replay(nil) should error")
	}
	plan, err := rep.Replay(&HandoffFile{})
	if err != nil {
		t.Fatalf("Replay(empty): %v", err)
	}
	if len(plan.Actions) != 0 || len(plan.Escalations) != 0 {
		t.Errorf("empty handoff should produce empty plan, got %+v", plan)
	}
	if plan.RigName != "gastown" {
		t.Errorf("plan.RigName = %q, want gastown", plan.RigName)
	}
	if plan.HandoffPath == "" {
		t.Error("plan.HandoffPath should be set even for empty handoff")
	}
}

// TestHandoffReplayer_RigPathRequired confirms the replayer
// refuses to operate with an empty rigPath (defensive: silently
// acting on the wrong rig is a major failure mode).
func TestHandoffReplayer_RigPathRequired(t *testing.T) {
	rep := NewHandoffReplayer("", "", "gastown", "")
	_, err := rep.Replay(&HandoffFile{StoppedLanes: []StoppedLane{{Polecat: "x"}}})
	if err == nil {
		t.Error("Replay with empty rigPath should error")
	}
}

// TestSupervisorPlanRecovery wires the replayer into the supervisor
// and confirms the on-disk handoff round-trips into a recovery plan.
func TestSupervisorPlanRecovery(t *testing.T) {
	rig := t.TempDir()
	town := t.TempDir()
	sup := NewSupervisor(town, rig, "gastown", "/bin/gt")

	// No handoff → nil plan.
	plan, err := sup.PlanRecovery()
	if err != nil {
		t.Fatalf("PlanRecovery no handoff: %v", err)
	}
	if plan != nil {
		t.Errorf("PlanRecovery with no handoff should return nil, got %+v", plan)
	}

	// Write a handoff with two lanes: one with durable assignment,
	// one without.
	hf := &HandoffFile{
		Reason:   "context-saturated",
		RigName:  "gastown",
		LastStep: "context-check",
		StoppedLanes: []StoppedLane{
			{
				Polecat:          "jasper",
				IssueID:          "gastown-c76",
				Reason:           "session-dead",
				ObservedAt:       time.Now().UTC(),
				AssignedAgent:    "claude",
				BeadKey:          "gastown-c76",
				AssignmentSource: AssignmentSourceAgentBead,
			},
			{
				// obsidian — unassigned, must be escalated
				// (the 2026-06-25 obsidian/GLM failure mode).
				Polecat:    "obsidian",
				IssueID:    "gastown-cet.16",
				Reason:     "agent-hung",
				ObservedAt: time.Now().UTC(),
			},
		},
		InFlightCleanup: []string{
			"gt session restart gastown/jasper",
		},
	}
	if err := WriteHandoff(rig, hf); err != nil {
		t.Fatalf("WriteHandoff: %v", err)
	}

	plan, err = sup.PlanRecovery()
	if err != nil {
		t.Fatalf("PlanRecovery: %v", err)
	}
	if plan == nil {
		t.Fatal("PlanRecovery with handoff on disk returned nil")
	}
	if len(plan.Actions) != 1 || plan.Actions[0].Polecat != "jasper" {
		t.Errorf("Actions should contain jasper, got %+v", plan.Actions)
	}
	if plan.Actions[0].AssignedAgent != "claude" {
		t.Errorf("jasper AssignedAgent = %q, want claude", plan.Actions[0].AssignedAgent)
	}
	if len(plan.Escalations) != 1 || plan.Escalations[0].Polecat != "obsidian" {
		t.Errorf("Escalations should contain obsidian, got %+v", plan.Escalations)
	}
	if len(plan.InFlightCleanup) != 1 {
		t.Errorf("InFlightCleanup lost: got %d, want 1", len(plan.InFlightCleanup))
	}
}

// TestIsDurableAssignmentSource pins the source classification so a
// future refactor cannot silently re-classify a non-durable source
// as durable.
func TestIsDurableAssignmentSource(t *testing.T) {
	cases := map[string]bool{
		AssignmentSourceAgentBead:        true,
		AssignmentSourceModelAssignments: true,
		AssignmentSourceConfigDefault:    false,
		AssignmentSourceUnassigned:       false,
		"":                               false,
		"unknown":                        false,
	}
	for src, want := range cases {
		if got := isDurableAssignmentSource(src); got != want {
			t.Errorf("isDurableAssignmentSource(%q) = %v, want %v", src, got, want)
		}
	}
}

// TestSimulatedStalledWitnessRecoversWithoutMayor is the smoke
// harness for the 2026-06-25 incident acceptance criterion. It
// simulates:
//
//   - A 100% context Witness that has stopped making forward progress
//     (very stale heartbeat, saturation = 1.0).
//   - Multiple stopped polecats with hooked work (the jasper/obsidian/onyx/
//     opal lanes from the incident).
//   - Obsidian has a missing model assignment (the actual failure
//     mode surfaced on 2026-06-25).
//   - Verifies the supervisor's ShouldRestart reports yes, the
//     handoff file captures all stopped lanes, and the recovery
//     plan model-preserves every lane with a durable assignment
//     while escalating obsidian so the operator (or a follow-up
//     tool) can resolve the missing assignment manually.
//
// We do not actually restart a real Witness tmux session here; that
// requires the integration test fixture. The unit-level guarantee
// is that the recovery primitives see the right signals, serialize
// the right state to the durable handoff file, and produce a
// recovery plan that does not silently rotate any agent.
func TestSimulatedStalledWitnessRecoversWithoutMayor(t *testing.T) {
	rig := t.TempDir()
	town := t.TempDir()
	sup := NewSupervisor(town, rig, "gastown", "")

	// 1. Witness writes a 100% saturated, very stale heartbeat.
	hb := &Heartbeat{
		Timestamp:                      time.Now().Add(-25 * time.Minute),
		ContextSaturationPercent:       1.0,
		LastStep:                       "context-check",
		OutstandingRecoveryObligations: 4,
		StoppedLanesSnapshot:           4,
	}
	if err := WriteHeartbeat(rig, hb); err != nil {
		t.Fatalf("WriteHeartbeat: %v", err)
	}

	// 2. Witness writes a handoff with the 2026-06-25 lanes.
	// jasper and onyx/opal have durable assignments; obsidian
	// does NOT (mirrors the 2026-06-25 failure where the wrapper
	// had not yet written model-assignments for that bead).
	hf := &HandoffFile{
		Reason:                   "context-saturated",
		LastStep:                 "context-check",
		RigName:                  "gastown",
		ContextSaturationPercent: 1.0,
		StoppedLanes: []StoppedLane{
			{
				Polecat: "jasper", IssueID: "gastown-c76", Reason: "session-dead",
				ObservedAt:       time.Now().UTC(),
				AssignedAgent:    "claude",
				BeadKey:          "gastown-c76",
				AssignmentSource: AssignmentSourceAgentBead,
			},
			{
				// obsidian — missing assignment (the 2026-06-25 failure)
				Polecat: "obsidian", IssueID: "gastown-cet.16", Reason: "agent-hung",
				ObservedAt: time.Now().UTC(),
			},
			{
				Polecat: "onyx", Reason: "session-dead",
				ObservedAt:       time.Now().UTC(),
				AssignedAgent:    "kimi",
				BeadKey:          "gastown-cet.7",
				AssignmentSource: AssignmentSourceModelAssignments,
			},
			{
				Polecat: "opal", Reason: "session-dead",
				ObservedAt:       time.Now().UTC(),
				AssignedAgent:    "claude",
				BeadKey:          "gastown-hxo",
				AssignmentSource: AssignmentSourceModelAssignments,
			},
		},
		InFlightCleanup: []string{
			"gt session restart gastown/jasper",
			"gt session restart gastown/obsidian",
		},
	}
	if err := WriteHandoff(rig, hf); err != nil {
		t.Fatalf("WriteHandoff: %v", err)
	}

	// 3. The supervisor (daemon side) sees the stale+saturated
	// heartbeat and decides to restart.
	should, reason := sup.ShouldRestart()
	if !should {
		t.Fatal("supervisor should flag stalled witness for restart")
	}
	if !strings.Contains(reason, "context-saturated") {
		t.Errorf("reason should mention context-saturated, got %q", reason)
	}

	// 4. The handoff file still has all 4 stopped lanes (the
	// post-restart Witness consumes them on resume).
	loaded, err := ReadHandoff(rig)
	if err != nil || loaded == nil {
		t.Fatalf("ReadHandoff: %+v err=%v", loaded, err)
	}
	if len(loaded.StoppedLanes) != 4 {
		t.Errorf("expected 4 stopped lanes in handoff, got %d", len(loaded.StoppedLanes))
	}
	if loaded.LastStep != "context-check" {
		t.Errorf("handoff LastStep = %q, want context-check", loaded.LastStep)
	}

	// 5. The post-restart path: replay the handoff through the
	// replayer. Three lanes get model-preserving restart commands;
	// obsidian is escalated because it has no durable assignment.
	plan, err := sup.PlanRecovery()
	if err != nil {
		t.Fatalf("PlanRecovery: %v", err)
	}
	if plan == nil {
		t.Fatal("PlanRecovery returned nil with handoff on disk")
	}

	// Build a quick lookup of all actions and escalations.
	actionPolecats := map[string]LaneRecovery{}
	for _, a := range plan.Actions {
		actionPolecats[a.Polecat] = a
	}
	escPolecats := map[string]LaneRecovery{}
	for _, e := range plan.Escalations {
		escPolecats[e.Polecat] = e
	}

	// jasper: model-preserving restart (claude).
	if a, ok := actionPolecats["jasper"]; !ok {
		t.Error("jasper should appear in Actions (has durable assignment)")
	} else {
		if a.AssignedAgent != "claude" {
			t.Errorf("jasper AssignedAgent = %q, want claude", a.AssignedAgent)
		}
		if !strings.Contains(a.Command, "--agent claude") {
			t.Errorf("jasper command should pin --agent claude, got %q", a.Command)
		}
	}
	// onyx: model-preserving restart (kimi).
	if a, ok := actionPolecats["onyx"]; !ok {
		t.Error("onyx should appear in Actions (has durable assignment)")
	} else if a.AssignedAgent != "kimi" {
		t.Errorf("onyx AssignedAgent = %q, want kimi", a.AssignedAgent)
	}
	// opal: model-preserving restart (claude).
	if a, ok := actionPolecats["opal"]; !ok {
		t.Error("opal should appear in Actions (has durable assignment)")
	} else if a.AssignedAgent != "claude" {
		t.Errorf("opal AssignedAgent = %q, want claude", a.AssignedAgent)
	}
	// obsidian: MUST be escalated, not silently rotated.
	if _, ok := actionPolecats["obsidian"]; ok {
		t.Error("obsidian must NOT appear in Actions (would silently rotate to rig role default)")
	}
	if e, ok := escPolecats["obsidian"]; !ok {
		t.Error("obsidian should be in Escalations (no durable assignment)")
	} else if !e.Escalate {
		t.Error("obsidian escalation should have Escalate=true")
	} else if e.Command != "" {
		t.Errorf("obsidian escalation Command should be empty, got %q", e.Command)
	}

	// 6. InFlightCleanup survives the round trip.
	if len(plan.InFlightCleanup) != 2 {
		t.Errorf("InFlightCleanup lost: got %d, want 2", len(plan.InFlightCleanup))
	}
}

// prevent unused-import warning for context when other tests don't use it.
var _ = context.Background
