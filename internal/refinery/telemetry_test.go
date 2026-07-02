package refinery

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/refinery/mrtelemetry"
)

// TestClassifyTerminalFailure verifies the bounded failure-class taxonomy the
// Mayor uses to separate substantive Codex FAIL from deterministic validation,
// reviewer unavailability, timeouts, infra, and convention issues. Each
// ProcessResult flag must map to the documented decision + failure class.
func TestClassifyTerminalFailure(t *testing.T) {
	cases := []struct {
		name           string
		result         ProcessResult
		wantDecision   string
		wantFailureCls string
	}{
		{
			name:           "reviewer-rejection-is-substantive",
			result:         ProcessResult{NeedsRework: true},
			wantDecision:   mrtelemetry.DecisionRejected,
			wantFailureCls: mrtelemetry.FailureClassSubstantiveImpl,
		},
		{
			name:           "slot-timeout",
			result:         ProcessResult{SlotTimeout: true},
			wantDecision:   mrtelemetry.DecisionSlotTimeout,
			wantFailureCls: mrtelemetry.FailureClassTimeout,
		},
		{
			name:           "convention-failed",
			result:         ProcessResult{ConventionFailed: true},
			wantDecision:   mrtelemetry.DecisionFailed,
			wantFailureCls: mrtelemetry.FailureClassConvention,
		},
		{
			name:           "conflict",
			result:         ProcessResult{Conflict: true},
			wantDecision:   mrtelemetry.DecisionFailed,
			wantFailureCls: mrtelemetry.FailureClassConflict,
		},
		{
			name:           "tests-failed-is-deterministic-validation",
			result:         ProcessResult{TestsFailed: true},
			wantDecision:   mrtelemetry.DecisionFailed,
			wantFailureCls: mrtelemetry.FailureClassDeterministicValidation,
		},
		{
			name:           "branch-not-found-is-infra",
			result:         ProcessResult{BranchNotFound: true},
			wantDecision:   mrtelemetry.DecisionFailed,
			wantFailureCls: mrtelemetry.FailureClassInfra,
		},
		{
			name:           "no-merge-is-not-a-failure",
			result:         ProcessResult{NoMerge: true},
			wantDecision:   mrtelemetry.DecisionNoMerge,
			wantFailureCls: mrtelemetry.FailureClassNone,
		},
		{
			name:           "needs-approval-is-not-a-failure",
			result:         ProcessResult{NeedsApproval: true},
			wantDecision:   mrtelemetry.DecisionNeedsApproval,
			wantFailureCls: mrtelemetry.FailureClassNone,
		},
		{
			name:           "unspecified-defaults-to-infra-noise",
			result:         ProcessResult{},
			wantDecision:   mrtelemetry.DecisionFailed,
			wantFailureCls: mrtelemetry.FailureClassInfra,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, failureClass := classifyTerminalFailure(tc.result)
			if decision != tc.wantDecision {
				t.Errorf("decision = %q, want %q", decision, tc.wantDecision)
			}
			if failureClass != tc.wantFailureCls {
				t.Errorf("failureClass = %q, want %q", failureClass, tc.wantFailureCls)
			}
		})
	}
}

// TestClassifyTerminalFailure_Precedence verifies that a NeedsRework rejection
// is classified as substantive even when other failure flags are also set — the
// reviewer's explicit rejection is the authoritative signal the Mayor wants
// separated from infra noise.
func TestClassifyTerminalFailure_Precedence(t *testing.T) {
	// NeedsRework takes precedence over TestsFailed: a reviewer rejection is
	// substantive even if tests also failed.
	decision, failureClass := classifyTerminalFailure(ProcessResult{
		NeedsRework: true,
		TestsFailed: true,
		Conflict:    true,
	})
	if decision != mrtelemetry.DecisionRejected {
		t.Errorf("decision = %q, want %q", decision, mrtelemetry.DecisionRejected)
	}
	if failureClass != mrtelemetry.FailureClassSubstantiveImpl {
		t.Errorf("failureClass = %q, want %q", failureClass, mrtelemetry.FailureClassSubstantiveImpl)
	}
}

// TestCodexVerdictFromResult verifies that a reviewer rejection maps to a codex
// FAIL, and any other outcome leaves the verdict unset (the codex review
// instrumentation records the actual verdict directly when the gate ran).
func TestCodexVerdictFromResult(t *testing.T) {
	if got := codexVerdictFromResult(ProcessResult{NeedsRework: true}); got != mrtelemetry.CodexVerdictFail {
		t.Errorf("NeedsRework verdict = %q, want %q", got, mrtelemetry.CodexVerdictFail)
	}
	if got := codexVerdictFromResult(ProcessResult{TestsFailed: true}); got != "" {
		t.Errorf("non-rejection verdict = %q, want empty", got)
	}
}

// TestCodexVerdictFromDurableGate verifies the codex verdict derived from the
// durable review gate's ProcessResult and gate context:
//   - Success            -> PASS
//   - NeedsRework        -> FAIL
//   - DeadlineExceeded   -> UNAVAILABLE
//   - Other failure       -> NO_VERDICT
func TestCodexVerdictFromDurableGate(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		if got := codexVerdictFromDurableGate(ProcessResult{Success: true}, context.Background()); got != mrtelemetry.CodexVerdictPass {
			t.Errorf("success verdict = %q, want %q", got, mrtelemetry.CodexVerdictPass)
		}
	})
	t.Run("needs-rework", func(t *testing.T) {
		if got := codexVerdictFromDurableGate(ProcessResult{NeedsRework: true}, context.Background()); got != mrtelemetry.CodexVerdictFail {
			t.Errorf("needs-rework verdict = %q, want %q", got, mrtelemetry.CodexVerdictFail)
		}
	})
	t.Run("deadline-exceeded", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
		defer cancel()
		<-time.After(2 * time.Nanosecond) // ensure deadline exceeded
		if got := codexVerdictFromDurableGate(ProcessResult{}, ctx); got != mrtelemetry.CodexVerdictUnavailable {
			t.Errorf("deadline verdict = %q, want %q", got, mrtelemetry.CodexVerdictUnavailable)
		}
	})
	t.Run("other-failure", func(t *testing.T) {
		if got := codexVerdictFromDurableGate(ProcessResult{TestsFailed: true}, context.Background()); got != mrtelemetry.CodexVerdictNoVerdict {
			t.Errorf("other-failure verdict = %q, want %q", got, mrtelemetry.CodexVerdictNoVerdict)
		}
	})
	t.Run("nil-context", func(t *testing.T) {
		// A nil context must not panic; other failures fall through to
		// NO_VERDICT without dereferencing the context.
		if got := codexVerdictFromDurableGate(ProcessResult{}, nil); got != mrtelemetry.CodexVerdictNoVerdict {
			t.Errorf("nil-ctx verdict = %q, want %q", got, mrtelemetry.CodexVerdictNoVerdict)
		}
	})
}
