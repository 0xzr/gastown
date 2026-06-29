package cmd

import (
	"testing"
	"time"
)

// TestFlattenNewQueryContext_IndependentDeadlines guards against the regression
// fixed in gastown-xi8: flattenGetRowCounts previously created ONE shared 30s
// context for the table-list query and every per-table COUNT query, so a slow
// first query starved all later queries of the remaining budget.
//
// Each call to flattenNewQueryContext MUST return an independent context with
// its own deadline.
func TestFlattenNewQueryContext_IndependentDeadlines(t *testing.T) {
	ctx1, cancel1 := flattenNewQueryContext()
	defer cancel1()

	deadline1, ok := ctx1.Deadline()
	if !ok {
		t.Fatal("expected flattenNewQueryContext to return a context with a deadline")
	}

	cancel1() // Exhaust first context's budget immediately.

	ctx2, cancel2 := flattenNewQueryContext()
	defer cancel2()

	deadline2, ok := ctx2.Deadline()
	if !ok {
		t.Fatal("expected second flattenNewQueryContext call to return a context with a deadline")
	}

	if !deadline2.After(deadline1) {
		t.Errorf("expected independent deadlines: deadline1=%v, deadline2=%v", deadline1, deadline2)
	}

	if err := ctx2.Err(); err != nil {
		t.Errorf("fresh context unexpectedly already done: %v", err)
	}
}

// TestFlattenNewQueryContext_BudgetMatchesConstant ensures the per-query budget
// matches the documented maintainQueryTimeout (gastown-xi8).
func TestFlattenNewQueryContext_BudgetMatchesConstant(t *testing.T) {
	ctx, cancel := flattenNewQueryContext()
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
