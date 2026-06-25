package fleet

import "testing"

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
