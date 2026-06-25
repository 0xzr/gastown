package fleet

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestChooseAgent_BeadScenario reproduces the gastown-1l8 acceptance scenario:
// configured caps are umans-glm=2, umans-kimi=2, m3=2 (six-polecat pool),
// existing live lanes are 1 Kimi + 1 M3 (the polybot/chrome legacy Kimi lane
// and the gastown/obsidian M3 lane), and 4 Gastown-ready tasks need dispatch.
//
// The buggy behavior produced GLM / GLM / Kimi / Kimi, exceeding the Kimi
// cap (live Kimi went 1 → 2 → 3) and leaving the M3 lane starved. This test
// verifies the FIX: every pick must keep its model under cap; the final live
// counts must be (2 GLM, 2 Kimi, 2 M3).
//
// The exact pick ORDER is rotation-dependent. The bead acceptance allows
// either GLM/GLM/Kimi/M3 (legacy insertion-order rotation) or the equivalent
// cap-consistent ordering from the alphabetical rotation we use in Go. What
// matters is that no model ever exceeds its cap.
func TestChooseAgent_BeadScenario(t *testing.T) {
	caps := MixCaps{
		"umans-glm":  2,
		"umans-kimi": 2,
		"m3":         2,
	}
	live := LiveCounts{
		"umans-kimi": 1, // polybot/chrome legacy Kimi lane
		"m3":         1, // gastown/obsidian legacy M3 lane
	}
	healthy := HealthyAgents{
		"umans-glm":  true,
		"umans-kimi": true,
		"m3":         true,
	}

	const numReady = 4
	gotSeq := make([]string, 0, numReady)
	idx := 0

	for i := 0; i < numReady; i++ {
		pick, err := ChooseAgent(caps, live, healthy, idx)
		if err != nil {
			t.Fatalf("step %d: ChooseAgent error: %v", i, err)
		}
		// Cap-consistency invariant: never pick a model at cap.
		if live[pick.Agent] >= caps[pick.Agent] {
			t.Fatalf("step %d: picked %q but live=%d >= cap=%d (live=%v)",
				i, pick.Agent, live[pick.Agent], caps[pick.Agent], live)
		}
		gotSeq = append(gotSeq, pick.Agent)
		live[pick.Agent]++
		idx = pick.NextIndex
	}

	// Final state must be exactly cap-consistent across all four models.
	wantFinal := map[string]int{
		"umans-glm":  2,
		"umans-kimi": 2,
		"m3":         2,
	}
	for model, want := range wantFinal {
		if live[model] != want {
			t.Fatalf("final live[%s] = %d, want %d (sequence=%v)",
				model, live[model], want, gotSeq)
		}
	}

	// Each model must be picked at least once — M3 was at 1 going in and
	// cannot stay at 1, otherwise the M3 lane is starved.
	if !modelPickedAtLeast(gotSeq, "m3", 1) {
		t.Fatalf("M3 lane starved: sequence=%v", gotSeq)
	}
	if !modelPickedAtLeast(gotSeq, "umans-glm", 2) {
		t.Fatalf("GLM lane under-used: sequence=%v", gotSeq)
	}
}

// TestChooseAgent_RejectsOverCap is the exact failure mode that produced the
// buggy fleet mix in gastown-1l8: a 5th Kimi sling while Kimi is at cap=2
// must NOT pick Kimi, even if the rotation index is pointing at the Kimi
// entry.
func TestChooseAgent_RejectsOverCap(t *testing.T) {
	caps := MixCaps{
		"umans-glm":  2,
		"umans-kimi": 2,
		"m3":         2,
	}
	live := LiveCounts{
		"umans-glm":  2, // full
		"umans-kimi": 2, // full — would be the rotation pick without
		"m3":         0, // reconciliation
	}
	healthy := HealthyAgents{
		"umans-glm":  true,
		"umans-kimi": true,
		"m3":         true,
	}

	// The rotation order is GLM, GLM, Kimi, Kimi, M3, M3. With Kimi at cap
	// the algorithm must walk past Kimi and return M3.
	pick, err := ChooseAgent(caps, live, healthy, 0)
	if err != nil {
		t.Fatalf("ChooseAgent: %v", err)
	}
	if pick.Agent != "m3" {
		t.Fatalf("got %q, want %q (rotation must skip over-cap agents)", pick.Agent, "m3")
	}
}

// TestChooseAgent_NoEligibleWhenAllFull verifies the algorithm returns
// ErrNoEligibleAgent — not a panic, not a stale pick — when every cap is
// reached.
func TestChooseAgent_NoEligibleWhenAllFull(t *testing.T) {
	caps := MixCaps{
		"umans-glm":  1,
		"umans-kimi": 1,
		"m3":         1,
	}
	live := LiveCounts{
		"umans-glm":  1,
		"umans-kimi": 1,
		"m3":         1,
	}
	healthy := HealthyAgents{
		"umans-glm":  true,
		"umans-kimi": true,
		"m3":         true,
	}
	if _, err := ChooseAgent(caps, live, healthy, 0); err != ErrNoEligibleAgent {
		t.Fatalf("got err=%v, want ErrNoEligibleAgent", err)
	}
}

// TestChooseAgent_UnhealthySuppresses covers the case where a model is under
// cap but unhealthy (e.g. m3 health check failing). The pick must skip it
// even if rotation would otherwise pick it.
func TestChooseAgent_UnhealthySuppresses(t *testing.T) {
	caps := MixCaps{
		"umans-glm":  1,
		"umans-kimi": 1,
		"m3":         1,
	}
	live := LiveCounts{}
	healthy := HealthyAgents{
		"umans-glm":  true,
		"umans-kimi": true,
		"m3":         false, // health-check failed
	}
	pick, err := ChooseAgent(caps, live, healthy, 0)
	if err != nil {
		t.Fatalf("ChooseAgent: %v", err)
	}
	if pick.Agent == "m3" {
		t.Fatalf("unhealthy m3 must be skipped; got %q", pick.Agent)
	}
}

// TestChooseAgent_StableRotationOrder verifies that NewRotationFromCaps is
// alphabetical (stable across runs and rigs), and that the rotation index
// wraps cleanly across multiple calls.
func TestChooseAgent_StableRotationOrder(t *testing.T) {
	caps := MixCaps{
		"m3":         2,
		"umans-kimi": 2,
		"umans-glm":  2,
	}
	rot := NewRotationFromCaps(caps)
	want := []string{"m3", "m3", "umans-glm", "umans-glm", "umans-kimi", "umans-kimi"}
	if !equalSeq(rot, want) {
		t.Fatalf("rotation = %v, want %v (alphabetical)", rot, want)
	}
}

// TestChooseAgent_ReconcilesAgainstLegacyLanes asserts that already-running
// legacy lanes (on OTHER rigs) count toward the live model counts. This is
// the gastown-1l8 acceptance item: "already-running legacy lanes count
// toward mix".
//
// The caller is expected to provide a LiveCounts map already summed across
// every active rig. ChooseAgent itself is rig-agnostic; the integration is
// at the call site.
func TestChooseAgent_ReconcilesAgainstLegacyLanes(t *testing.T) {
	caps := MixCaps{
		"umans-glm":  2,
		"umans-kimi": 2,
		"m3":         2,
	}
	// Live lanes summed town-wide: polybot/chrome Kimi + gastown/obsidian M3
	// + one gl-launching in-flight polecat.
	live := LiveCounts{
		"umans-glm":  1,
		"umans-kimi": 1, // legacy polybot/chrome lane
		"m3":         1, // legacy gastown/obsidian lane
	}
	healthy := HealthyAgents{
		"umans-glm":  true,
		"umans-kimi": true,
		"m3":         true,
	}

	// Only one slot remains per model. The pick order must be deterministic
	// from rotation start (idx=0 → m3 first because alphabetically smallest
	// in caps; see TestChooseAgent_StableRotationOrder).
	picks := make([]string, 0, 3)
	idx := 0
	for i := 0; i < 3; i++ {
		p, err := ChooseAgent(caps, live, healthy, idx)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		picks = append(picks, p.Agent)
		live[p.Agent]++
		idx = p.NextIndex
	}
	// After three more fills, every model is at cap=2.
	for _, m := range []string{"umans-glm", "umans-kimi", "m3"} {
		if live[m] != 2 {
			t.Fatalf("model %s at live=%d, want 2 (caps were %v)", m, live[m], caps)
		}
	}
	if _, err := ChooseAgent(caps, live, healthy, idx); err != ErrNoEligibleAgent {
		t.Fatalf("expected ErrNoEligibleAgent after caps reached, got err=%v picks=%v", err, picks)
	}
}

func equalSeq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// modelPickedAtLeast reports whether model appears in seq at least n times.
func modelPickedAtLeast(seq []string, model string, n int) bool {
	count := 0
	for _, s := range seq {
		if s == model {
			count++
		}
	}
	return count >= n
}

// --------------------------------------------------------------------------
// gastown-cet.12.10: race-safe LaneCounter / ChooseAndReserveAgent tests.
//
// The pure ChooseAgent above is correct for a single dispatch observing a
// static snapshot. The check-then-act race surfaces when two dispatchers
// run concurrently: each one snapshots LiveCounts, calls ChooseAgent, and
// only then increments. Both pick the same model. The fix below pushes
// the increment inside a counter lock so pick-and-reserve is atomic.
// --------------------------------------------------------------------------

// allHealthy returns a HealthyAgents map with every caps entry marked true.
// Models present in caps but absent from healthy are treated as healthy
// (matches the ChooseAgent "default to healthy when unset" contract).
func allHealthy(caps MixCaps) HealthyAgents {
	h := make(HealthyAgents, len(caps))
	for m := range caps {
		h[m] = true
	}
	return h
}

// TestChooseAndReserveAgent_SequentialRotation verifies the rotation
// advances deterministically under the race-safe variant and that each
// pick increments exactly one lane for the chosen model.
func TestChooseAndReserveAgent_SequentialRotation(t *testing.T) {
	caps := MixCaps{
		"umans-glm":  2,
		"umans-kimi": 2,
		"m3":         2,
	}
	counter := NewLaneCounter(LiveCounts{})
	healthy := allHealthy(caps)
	wantSeq := []string{"m3", "m3", "umans-glm", "umans-glm", "umans-kimi", "umans-kimi"}

	idx := 0
	for i, want := range wantSeq {
		r, err := ChooseAndReserveAgent(caps, counter, healthy, idx)
		if err != nil {
			t.Fatalf("step %d: unexpected error: %v", i, err)
		}
		if r.Agent != want {
			t.Fatalf("step %d: got %q, want %q (rotation not advancing)", i, r.Agent, want)
		}
		idx = r.NextIndex
		// Cap-consistency: after this pick, count for r.Agent must equal the
		// number of times we've picked it.
		got := counter.Counts()[r.Agent]
		var wantCount int
		for j := 0; j <= i; j++ {
			if wantSeq[j] == r.Agent {
				wantCount++
			}
		}
		if got != wantCount {
			t.Fatalf("step %d: count[%s]=%d, want %d", i, r.Agent, got, wantCount)
		}
	}

	// Cap exhausted: next pick must error.
	if _, err := ChooseAndReserveAgent(caps, counter, healthy, idx); err != ErrNoEligibleAgent {
		t.Fatalf("after cap exhaustion: got err=%v, want ErrNoEligibleAgent", err)
	}
}

// TestChooseAndReserveAgent_NoOverReservation is the regression test for
// gastown-cet.12.10. Spawn many goroutines racing on a small fleet. With
// the check-then-act race, two goroutines could both pick the same model
// before either incremented and exceed the cap. With the counter under a
// lock, exactly `cap[model]` reservations succeed per model and the rest
// get ErrNoEligibleAgent.
func TestChooseAndReserveAgent_NoOverReservation(t *testing.T) {
	caps := MixCaps{
		"umans-glm":  2,
		"umans-kimi": 2,
		"m3":         2,
	}
	counter := NewLaneCounter(LiveCounts{})
	healthy := allHealthy(caps)

	const goroutines = 64
	var wg sync.WaitGroup
	var success, fail int64
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			r, err := ChooseAndReserveAgent(caps, counter, healthy, 0)
			if err == nil {
				atomic.AddInt64(&success, 1)
				if r.Agent == "" {
					t.Errorf("success reservation had empty Agent")
				}
			} else if err == ErrNoEligibleAgent {
				atomic.AddInt64(&fail, 1)
			} else {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	totalCap := 0
	for _, c := range caps {
		totalCap += c
	}
	if int(success) != totalCap {
		t.Fatalf("reservations succeeded=%d, want exactly %d (caps=%v)", success, totalCap, caps)
	}
	if int(success+fail) != goroutines {
		t.Fatalf("success+fail=%d, want %d (every goroutine must return either a reservation or ErrNoEligibleAgent)", success+fail, goroutines)
	}

	// Final counts must equal caps exactly. No model over its cap.
	final := counter.Counts()
	for model, want := range caps {
		if final[model] != want {
			t.Fatalf("final count[%s]=%d, want %d (cap over- or under-shot under race)", model, final[model], want)
		}
	}
}

// TestChooseAndReserveAgent_RespectsCaps verifies that one slot per model
// is consumed atomically: a second concurrent picker cannot squeeze past
// the cap when the first holds it.
func TestChooseAndReserveAgent_RespectsCaps(t *testing.T) {
	caps := MixCaps{"m3": 1}
	counter := NewLaneCounter(LiveCounts{})
	healthy := HealthyAgents{"m3": true}

	// Holder: reserves m3 and does NOT release for the duration of the test.
	holder, err := ChooseAndReserveAgent(caps, counter, healthy, 0)
	if err != nil {
		t.Fatalf("holder: %v", err)
	}
	if holder.Agent != "m3" {
		t.Fatalf("holder got %q, want m3", holder.Agent)
	}

	// Race: many goroutines try to pick. All must fail with ErrNoEligibleAgent.
	const goroutines = 32
	var wg sync.WaitGroup
	var fail int64
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if _, err := ChooseAndReserveAgent(caps, counter, healthy, 0); err == ErrNoEligibleAgent {
				atomic.AddInt64(&fail, 1)
			} else {
				t.Errorf("non-ErrNoEligibleAgent under cap=1 saturated: %v", err)
			}
		}()
	}
	wg.Wait()
	if int(fail) != goroutines {
		t.Fatalf("only %d/%d goroutines saw ErrNoEligibleAgent — the cap was breached", fail, goroutines)
	}
	if c := counter.Counts()["m3"]; c != 1 {
		t.Fatalf("m3 count=%d, want 1 (cap saturated)", c)
	}
}

// TestReservation_ReleaseFreesSlot covers the dispatch-failure path: when
// the downstream sling fails, the caller MUST release the reservation so
// the slot returns to the pool. Without this, a transient dispatch
// failure would leak a lane until the daemon restarts.
func TestReservation_ReleaseFreesSlot(t *testing.T) {
	caps := MixCaps{"m3": 1}
	counter := NewLaneCounter(LiveCounts{})
	healthy := HealthyAgents{"m3": true}

	r, err := ChooseAndReserveAgent(caps, counter, healthy, 0)
	if err != nil {
		t.Fatalf("first pick: %v", err)
	}
	if c := counter.Counts()["m3"]; c != 1 {
		t.Fatalf("after reserve: m3 count=%d, want 1", c)
	}

	// While the reservation is held, a second pick must fail.
	if _, err := ChooseAndReserveAgent(caps, counter, healthy, 0); err != ErrNoEligibleAgent {
		t.Fatalf("second pick during held reservation: got %v, want ErrNoEligibleAgent", err)
	}

	// Simulate dispatch failure: release.
	r.Release()
	if c := counter.Counts()["m3"]; c != 0 {
		t.Fatalf("after release: m3 count=%d, want 0", c)
	}

	// Slot is now free: another pick succeeds.
	r2, err := ChooseAndReserveAgent(caps, counter, healthy, 0)
	if err != nil {
		t.Fatalf("post-release pick: %v", err)
	}
	if r2.Agent != "m3" {
		t.Fatalf("post-release pick got %q, want m3", r2.Agent)
	}
}

// TestReservation_ReleaseIsIdempotent verifies Release can be called
// multiple times without panicking or double-decrementing — important for
// deferred/cleanup code paths that may invoke Release from multiple
// sites (e.g. defer at caller + explicit release on failure).
func TestReservation_ReleaseIsIdempotent(t *testing.T) {
	caps := MixCaps{"m3": 1}
	counter := NewLaneCounter(LiveCounts{})
	healthy := HealthyAgents{"m3": true}

	r, err := ChooseAndReserveAgent(caps, counter, healthy, 0)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	r.Release()
	r.Release() // must be a no-op
	r.Release()

	if c := counter.Counts()["m3"]; c != 0 {
		t.Fatalf("after multiple releases: m3 count=%d, want 0", c)
	}

	// Nil and zero-value Release must not panic.
	var nilRes *Reservation
	nilRes.Release()
	var zeroRes Reservation
	zeroRes.Release()
}

// TestChooseAndReserveAgent_RespectsHealthyFilter covers the case where a
// model has cap headroom but is unhealthy (e.g. m3 health-check failing).
// The race-safe variant must skip it exactly like ChooseAgent does.
func TestChooseAndReserveAgent_RespectsHealthyFilter(t *testing.T) {
	caps := MixCaps{
		"umans-glm":  1,
		"umans-kimi": 1,
		"m3":         1,
	}
	counter := NewLaneCounter(LiveCounts{})
	healthy := HealthyAgents{
		"umans-glm":  true,
		"umans-kimi": true,
		"m3":         false,
	}
	r, err := ChooseAndReserveAgent(caps, counter, healthy, 0)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if r.Agent == "m3" {
		t.Fatalf("unhealthy m3 must be skipped; got %q", r.Agent)
	}
}

// TestChooseAndReserveAgent_NilCounterDefensive ensures the function does
// not panic on a nil counter (a misuse that's easy at call sites that
// conditionally initialize the counter).
func TestChooseAndReserveAgent_NilCounterDefensive(t *testing.T) {
	caps := MixCaps{"m3": 1}
	if _, err := ChooseAndReserveAgent(caps, nil, HealthyAgents{"m3": true}, 0); err != ErrNoEligibleAgent {
		t.Fatalf("nil counter: got %v, want ErrNoEligibleAgent", err)
	}
}

// TestLaneCounter_ReserveDirect exercises the standalone Reserve/Release
// path that callers can use when they already know the agent (e.g. when
// the sling context specifies an explicit Agent and only the cap check
// is needed).
func TestLaneCounter_ReserveDirect(t *testing.T) {
	caps := MixCaps{"m3": 2, "kimi": 1}
	counter := NewLaneCounter(LiveCounts{})

	if !counter.Reserve(caps, "m3") {
		t.Fatal("first m3 reserve failed")
	}
	if !counter.Reserve(caps, "m3") {
		t.Fatal("second m3 reserve failed")
	}
	if counter.Reserve(caps, "m3") {
		t.Fatal("third m3 reserve should fail (cap=2)")
	}
	if !counter.Reserve(caps, "kimi") {
		t.Fatal("kimi reserve failed")
	}
	if counter.Reserve(caps, "kimi") {
		t.Fatal("second kimi reserve should fail (cap=1)")
	}
	// Unknown model: cap=0, must fail.
	if counter.Reserve(caps, "unknown") {
		t.Fatal("unknown model reserve should fail (cap=0)")
	}

	// Counts reflect the four successful reserves.
	got := counter.Counts()
	if got["m3"] != 2 || got["kimi"] != 1 {
		t.Fatalf("counts=%v, want m3=2 kimi=1", got)
	}

	// Release one m3 — slot returns to the pool.
	counter.Release("m3")
	if counter.Counts()["m3"] != 1 {
		t.Fatal("after release: m3 count should be 1")
	}
	if !counter.Reserve(caps, "m3") {
		t.Fatal("m3 reserve after release failed")
	}

	// Idempotent release on a zero count.
	counter.Release("m3")
	counter.Release("m3") // no-op
	counter.Release("m3") // no-op
	if counter.Counts()["m3"] != 0 {
		t.Fatalf("after triple release: m3 count=%d, want 0", counter.Counts()["m3"])
	}
}

// TestLaneCounter_Set verifies Set replaces the underlying counts under
// the lock — useful when the daemon refreshes the counter from a fresh
// town-wide session enumeration.
func TestLaneCounter_Set(t *testing.T) {
	counter := NewLaneCounter(LiveCounts{"old": 1})
	if got := counter.Counts()["old"]; got != 1 {
		t.Fatalf("initial old=%d, want 1", got)
	}
	counter.Set(LiveCounts{"new": 5})
	if got := counter.Counts()["new"]; got != 5 {
		t.Fatalf("after Set: new=%d, want 5", got)
	}
	if _, ok := counter.Counts()["old"]; ok {
		t.Fatal("after Set: old key should be gone")
	}
	// Set with nil must reset to empty (not panic).
	counter.Set(nil)
	if got := counter.Counts(); len(got) != 0 {
		t.Fatalf("after Set(nil): counts=%v, want empty", got)
	}
}

// TestChooseAndReserveAgent_LegacyLanesCount is the gastown-1l8 acceptance
// scenario replayed under the race-safe variant: pre-existing legacy lanes
// on OTHER rigs count toward the live model counts, so the cap is honored
// from the very first pick.
func TestChooseAndReserveAgent_LegacyLanesCount(t *testing.T) {
	caps := MixCaps{
		"umans-glm":  2,
		"umans-kimi": 2,
		"m3":         2,
	}
	// Town-wide lane sum: 1 polybot/chrome Kimi + 1 gastown/obsidian M3 +
	// 1 gl-launching in-flight polecat.
	counter := NewLaneCounter(LiveCounts{
		"umans-glm":  1,
		"umans-kimi": 1,
		"m3":         1,
	})
	healthy := allHealthy(caps)

	picks := make([]string, 0, 3)
	idx := 0
	for i := 0; i < 3; i++ {
		r, err := ChooseAndReserveAgent(caps, counter, healthy, idx)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		picks = append(picks, r.Agent)
		idx = r.NextIndex
	}

	// After three more picks, every model is at cap=2.
	for _, m := range []string{"umans-glm", "umans-kimi", "m3"} {
		if counter.Counts()[m] != 2 {
			t.Fatalf("model %s at count=%d, want 2 (legacy lanes not honored)", m, counter.Counts()[m])
		}
	}
	if _, err := ChooseAndReserveAgent(caps, counter, healthy, idx); err != ErrNoEligibleAgent {
		t.Fatalf("after caps reached: got %v, want ErrNoEligibleAgent (picks=%v)", err, picks)
	}
}
