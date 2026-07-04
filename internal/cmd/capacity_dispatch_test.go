package cmd

import (
	"errors"
	"fmt"
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

func TestPermanentFailureClass(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "nil error",
			err:  nil,
			want: "",
		},
		{
			name: "transient dispatch error",
			err:  errors.New("dolt timeout"),
			want: "",
		},
		{
			name: "cross-rig prefix refused",
			err:  capacity.ErrCrossRigPrefix,
			want: "cross-rig-prefix",
		},
		{
			name: "wrapped cross-rig prefix refused",
			err:  fmt.Errorf("sling failed: %w", capacity.ErrCrossRigPrefix),
			want: "cross-rig-prefix",
		},
		{
			name: "respawn limit reached",
			err:  errors.New("respawn limit reached for gt-abc (3 attempts)"),
			want: "respawn-limit",
		},
		{
			name: "wrapped respawn limit reached",
			err:  fmt.Errorf("sling failed: respawn limit reached for gt-abc (3 attempts). This bead keeps failing"),
			want: "respawn-limit",
		},
		{
			name: "existing molecule(s)",
			err:  errors.New("bead gt-abc has existing molecule(s) (use --force)"),
			want: "existing-molecule",
		},
		{
			name: "wrapped existing molecule(s)",
			err:  fmt.Errorf("sling failed: bead gt-abc has existing molecule(s) (use --force)"),
			want: "existing-molecule",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := permanentFailureClass(tt.err); got != tt.want {
				t.Errorf("permanentFailureClass(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}
