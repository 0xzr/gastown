package cmd

import (
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

func TestShouldFireCrossRigEscalation_Debounces(t *testing.T) {
	resetCrossRigEscalationStateForTest()
	t.Cleanup(resetCrossRigEscalationStateForTest)

	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	if !shouldFireCrossRigEscalation("walletui", "hq", now) {
		t.Fatalf("first call must fire")
	}
	// Second call inside the debounce window must NOT fire.
	if shouldFireCrossRigEscalation("walletui", "hq", now.Add(30*time.Minute)) {
		t.Fatalf("second call inside debounce window must not fire")
	}
	// After the debounce window elapses, fire again.
	if !shouldFireCrossRigEscalation("walletui", "hq", now.Add(crossRigEscalationDebounce+time.Minute)) {
		t.Fatalf("call past debounce window must fire")
	}
}

func TestShouldFireCrossRigEscalation_KeyedByRigAndPrefix(t *testing.T) {
	resetCrossRigEscalationStateForTest()
	t.Cleanup(resetCrossRigEscalationStateForTest)

	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)

	if !shouldFireCrossRigEscalation("walletui", "hq", now) {
		t.Fatalf("walletui/hq first call must fire")
	}
	// Different rig — should fire independently.
	if !shouldFireCrossRigEscalation("furiosa", "hq", now) {
		t.Fatalf("furiosa/hq must fire (different rig)")
	}
	// Different prefix on same rig — should fire independently.
	if !shouldFireCrossRigEscalation("walletui", "wisp", now) {
		t.Fatalf("walletui/wisp must fire (different prefix)")
	}
	// Same (rig, prefix) repeats — debounced.
	if shouldFireCrossRigEscalation("walletui", "hq", now.Add(time.Minute)) {
		t.Fatalf("walletui/hq repeat must not fire")
	}
}

func TestShouldCloseContextAfterDispatchFailure_ExplicitAgentStartup(t *testing.T) {
	b := capacity.PendingBead{
		ID:         "ctx-1",
		WorkBeadID: "polybot-abc",
		Context: &capacity.SlingContextFields{
			Agent: "codex-impl",
		},
	}

	closeNow, reason := shouldCloseContextAfterDispatchFailure(
		b,
		errors.New("sling failed: codex-impl startup failed instantly"),
	)
	if !closeNow {
		t.Fatal("explicit-agent startup failure should close the stale context")
	}
	if reason != dispatchFailureCloseReasonAgentStartup {
		t.Fatalf("reason = %q, want %q", reason, dispatchFailureCloseReasonAgentStartup)
	}
}

func TestShouldCloseContextAfterDispatchFailure_WithoutExplicitAgentKeepsRetry(t *testing.T) {
	b := capacity.PendingBead{
		ID:         "ctx-1",
		WorkBeadID: "polybot-abc",
		Context:    &capacity.SlingContextFields{},
	}

	closeNow, reason := shouldCloseContextAfterDispatchFailure(
		b,
		errors.New("sling failed: startup failed instantly"),
	)
	if closeNow || reason != "" {
		t.Fatalf("implicit-agent failure should keep normal retry policy, got close=%v reason=%q", closeNow, reason)
	}
}

func TestShouldCloseContextAfterDispatchFailure_TransientExplicitAgentFailureKeepsRetry(t *testing.T) {
	b := capacity.PendingBead{
		ID:         "ctx-1",
		WorkBeadID: "polybot-abc",
		Context: &capacity.SlingContextFields{
			Agent: "umans-glm",
		},
	}

	closeNow, reason := shouldCloseContextAfterDispatchFailure(
		b,
		errors.New("temporary bead lookup timeout"),
	)
	if closeNow || reason != "" {
		t.Fatalf("transient failure should keep normal retry policy, got close=%v reason=%q", closeNow, reason)
	}
}
