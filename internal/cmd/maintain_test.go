package cmd

import (
	"testing"
	"time"
)

func TestMaintainCommand_Registered(t *testing.T) {
	var found bool
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "maintain" {
			found = true

			if f := cmd.Flags().Lookup("force"); f == nil {
				t.Error("expected --force flag")
			}
			if f := cmd.Flags().Lookup("dry-run"); f == nil {
				t.Error("expected --dry-run flag")
			}
			if f := cmd.Flags().Lookup("threshold"); f == nil {
				t.Error("expected --threshold flag")
			} else if f.DefValue != "100" {
				t.Errorf("expected threshold default 100, got %s", f.DefValue)
			}

			if cmd.GroupID != GroupServices {
				t.Errorf("expected GroupServices, got %s", cmd.GroupID)
			}
			break
		}
	}
	if !found {
		t.Fatal("maintain command not registered on rootCmd")
	}
}

func TestMaintainThreshold(t *testing.T) {
	tests := []struct {
		commits   int
		threshold int
		flatten   bool
	}{
		{0, 100, false},
		{50, 100, false},
		{99, 100, false},
		{100, 100, true},
		{200, 100, true},
		{1000, 100, true},
		{5, 5, true},
		{4, 5, false},
	}
	for _, tt := range tests {
		flatten := tt.commits >= tt.threshold
		if flatten != tt.flatten {
			t.Errorf("commits=%d threshold=%d: got flatten=%v, want %v",
				tt.commits, tt.threshold, flatten, tt.flatten)
		}
	}
}

func TestMaintainDBInfo(t *testing.T) {
	// Verify the struct can hold expected values.
	info := maintainDBInfo{
		name:        "gastown",
		commitCount: 500,
		hasBackup:   true,
	}
	if info.name != "gastown" {
		t.Errorf("expected name gastown, got %s", info.name)
	}
	if info.commitCount != 500 {
		t.Errorf("expected 500 commits, got %d", info.commitCount)
	}
	if !info.hasBackup {
		t.Error("expected hasBackup true")
	}
}

func TestMaintainConstants(t *testing.T) {
	if defaultMaintainThreshold != 100 {
		t.Errorf("expected default threshold 100, got %d", defaultMaintainThreshold)
	}
}

// TestMaintainFreshQueryContext_IndependentDeadlines guards against the
// regression fixed in gastown-xi8: maintainFlattenDB previously created ONE
// shared 30s context and reused it across 4+ distinct SQL operations, so a
// slow first query would starve subsequent queries of the remaining budget.
//
// Each call to maintainFreshQueryContext MUST return an independent context
// with its own deadline. Verifying this directly prevents re-introduction of
// the shared-context anti-pattern.
func TestMaintainFreshQueryContext_IndependentDeadlines(t *testing.T) {
	ctx1, cancel1 := maintainFreshQueryContext()
	defer cancel1()

	deadline1, ok := ctx1.Deadline()
	if !ok {
		t.Fatal("expected maintainFreshQueryContext to return a context with a deadline")
	}

	cancel1() // exhaust first context's budget immediately.

	// Second call must produce a fresh context with its own deadline that is
	// AFTER the (now-cancelled) first deadline — not the same shared deadline.
	ctx2, cancel2 := maintainFreshQueryContext()
	defer cancel2()

	deadline2, ok := ctx2.Deadline()
	if !ok {
		t.Fatal("expected second maintainFreshQueryContext call to return a context with a deadline")
	}

	if !deadline2.After(deadline1) {
		t.Errorf("expected independent deadlines: deadline1=%v, deadline2=%v", deadline1, deadline2)
	}

	// And ctx2 must NOT already be expired (the regression would have left
	// only a few hundred ms on the shared budget).
	if err := ctx2.Err(); err != nil {
		t.Errorf("fresh context unexpectedly already done: %v", err)
	}
}

// TestMaintainFreshQueryContext_BudgetMatchesConstant ensures the per-query
// budget matches the documented maintainQueryTimeout (gastown-xi8).
func TestMaintainFreshQueryContext_BudgetMatchesConstant(t *testing.T) {
	ctx, cancel := maintainFreshQueryContext()
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline")
	}
	remaining := time.Until(deadline)
	// Allow generous slack for scheduler delay; budget must be ~maintainQueryTimeout.
	if remaining < maintainQueryTimeout-100*time.Millisecond {
		t.Errorf("deadline too soon: remaining=%v, want ~%v", remaining, maintainQueryTimeout)
	}
	if remaining > maintainQueryTimeout+time.Second {
		t.Errorf("deadline too far: remaining=%v, want ~%v", remaining, maintainQueryTimeout)
	}
}
