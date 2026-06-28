package polecat

import (
	"testing"
	"time"
)

func TestState_IsWorking(t *testing.T) {
	tests := []struct {
		state  State
		expect bool
	}{
		{StateWorking, true},
		{StateDone, false},
		{StateStuck, false},
		{StateStalled, false},
		{StateZombie, false},
		{State("unknown"), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			if got := tt.state.IsWorking(); got != tt.expect {
				t.Errorf("State(%q).IsWorking() = %v, want %v", tt.state, got, tt.expect)
			}
		})
	}
}

func TestPolecat_Summary(t *testing.T) {
	now := time.Now()
	p := &Polecat{
		Name:      "alpha",
		Rig:       "gastown",
		State:     StateWorking,
		ClonePath: "/some/path",
		Branch:    "polecat/alpha",
		Issue:     "gt-123",
		CreatedAt: now,
		UpdatedAt: now,
	}

	s := p.Summary()
	if s.Name != "alpha" {
		t.Errorf("Summary.Name = %q, want %q", s.Name, "alpha")
	}
	if s.State != StateWorking {
		t.Errorf("Summary.State = %q, want %q", s.State, StateWorking)
	}
	if s.Issue != "gt-123" {
		t.Errorf("Summary.Issue = %q, want %q", s.Issue, "gt-123")
	}
}

func TestPolecat_Summary_NoIssue(t *testing.T) {
	p := &Polecat{
		Name:  "beta",
		State: StateDone,
	}

	s := p.Summary()
	if s.Issue != "" {
		t.Errorf("Summary.Issue = %q, want empty", s.Issue)
	}
}

func TestState_IsStalled(t *testing.T) {
	tests := []struct {
		state  State
		expect bool
	}{
		{StateStalled, true},
		{StateWorking, false},
		{StateIdle, false},
		{StateDone, false},
		{StateStuck, false},
		{StateZombie, false},
		// gastown-72v: post-submit gates must NOT be classified as stalls.
		// They are owned by the refinery, not by a dead session.
		{StatePendingMR, false},
		{State("unknown"), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			if got := tt.state.IsStalled(); got != tt.expect {
				t.Errorf("State(%q).IsStalled() = %v, want %v", tt.state, got, tt.expect)
			}
		})
	}
}

func TestState_IsPendingMR(t *testing.T) {
	tests := []struct {
		state  State
		expect bool
	}{
		{StatePendingMR, true},
		{StateStalled, false},
		{StateWorking, false},
		{StateIdle, false},
		{StateDone, false},
		{StateReviewNeeded, false},
		{StateZombie, false},
		{State("unknown"), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			if got := tt.state.IsPendingMR(); got != tt.expect {
				t.Errorf("State(%q).IsPendingMR() = %v, want %v", tt.state, got, tt.expect)
			}
		})
	}
}

// TestState_PendingMR_DistinctFromStalled is the regression guard for
// gastown-72v: a polecat whose tmux heartbeat is stale but whose agent bead
// has an open MR in the refinery must be classified as StatePendingMR, NOT
// StateStalled. Lumping the two together caused witness/fleet summaries to
// report an empty fleet while a live MR gate was still draining.
func TestState_PendingMR_DistinctFromStalled(t *testing.T) {
	if StatePendingMR.IsStalled() {
		t.Errorf("StatePendingMR.IsStalled() = true, want false (gastown-72v)")
	}
	if StateStalled.IsPendingMR() {
		t.Errorf("StateStalled.IsPendingMR() = true, want false (gastown-72v)")
	}
	if string(StatePendingMR) == string(StateStalled) {
		t.Errorf("StatePendingMR string %q must not collide with StateStalled %q", StatePendingMR, StateStalled)
	}
}

func TestCleanupStatus_IsSafe(t *testing.T) {
	tests := []struct {
		status CleanupStatus
		expect bool
	}{
		{CleanupClean, true},
		{CleanupUncommitted, false},
		{CleanupStash, false},
		{CleanupUnpushed, false},
		{CleanupUnknown, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.IsSafe(); got != tt.expect {
				t.Errorf("CleanupStatus(%q).IsSafe() = %v, want %v", tt.status, got, tt.expect)
			}
		})
	}
}

func TestCleanupStatus_RequiresRecovery(t *testing.T) {
	tests := []struct {
		status CleanupStatus
		expect bool
	}{
		{CleanupClean, false},
		{CleanupUncommitted, true},
		{CleanupStash, true},
		{CleanupUnpushed, true},
		{CleanupUnknown, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.RequiresRecovery(); got != tt.expect {
				t.Errorf("CleanupStatus(%q).RequiresRecovery() = %v, want %v", tt.status, got, tt.expect)
			}
		})
	}
}

func TestCleanupStatus_CanForceRemove(t *testing.T) {
	tests := []struct {
		status CleanupStatus
		expect bool
	}{
		{CleanupClean, true},
		{CleanupUncommitted, true},
		{CleanupStash, false},
		{CleanupUnpushed, true},
		{CleanupUnknown, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.CanForceRemove(); got != tt.expect {
				t.Errorf("CleanupStatus(%q).CanForceRemove() = %v, want %v", tt.status, got, tt.expect)
			}
		})
	}
}
