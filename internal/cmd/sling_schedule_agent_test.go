package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

type recordingSlingContextUpdater struct {
	err       error
	calls     int
	contextID string
	fields    *capacity.SlingContextFields
}

func (u *recordingSlingContextUpdater) UpdateSlingContextFields(contextID string, fields *capacity.SlingContextFields) error {
	u.calls++
	u.contextID = contextID
	snapshot := *fields
	u.fields = &snapshot
	return u.err
}

func TestReconcileScheduledAgentReplacesStaleRoute(t *testing.T) {
	updater := &recordingSlingContextUpdater{}
	fields := &capacity.SlingContextFields{
		Version:          1,
		WorkBeadID:       "polybot-55kk",
		TargetRig:        "polybot",
		Formula:          "mol-polecat-work",
		EnqueuedAt:       "2026-07-09T21:58:00Z",
		Agent:            "umans-glm",
		DispatchFailures: 3,
		LastFailure:      "old GLM route failed",
	}

	previous, updated, err := reconcileScheduledAgent(
		updater, "polybot-wisp-old", fields, "codex-impl-xhigh",
	)
	if err != nil {
		t.Fatalf("reconcileScheduledAgent: %v", err)
	}
	if !updated {
		t.Fatal("updated = false, want true")
	}
	if previous != "umans-glm" {
		t.Fatalf("previous agent = %q, want umans-glm", previous)
	}
	if updater.calls != 1 || updater.contextID != "polybot-wisp-old" {
		t.Fatalf("update calls = %d for %q, want 1 for polybot-wisp-old", updater.calls, updater.contextID)
	}
	if updater.fields.Agent != "codex-impl-xhigh" {
		t.Fatalf("persisted agent = %q, want codex-impl-xhigh", updater.fields.Agent)
	}
	if updater.fields.DispatchFailures != 0 || updater.fields.LastFailure != "" {
		t.Fatalf("persisted dispatch failure state = (%d, %q), want cleared", updater.fields.DispatchFailures, updater.fields.LastFailure)
	}
	if updater.fields.EnqueuedAt != "2026-07-09T21:58:00Z" || updater.fields.Formula != "mol-polecat-work" {
		t.Fatalf("unrelated scheduling fields changed: %+v", updater.fields)
	}
	if fields.Agent != "codex-impl-xhigh" || fields.DispatchFailures != 0 || fields.LastFailure != "" {
		t.Fatalf("in-memory fields not reconciled after persistence: %+v", fields)
	}
}

func TestReconcileScheduledAgentPreservesIdempotentNoOp(t *testing.T) {
	tests := []struct {
		name      string
		existing  string
		requested string
	}{
		{name: "same explicit agent", existing: "codex-impl-xhigh", requested: "codex-impl-xhigh"},
		{name: "no explicit agent", existing: "umans-glm", requested: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updater := &recordingSlingContextUpdater{}
			fields := &capacity.SlingContextFields{
				Agent:            tt.existing,
				DispatchFailures: 2,
				LastFailure:      "preserve on no-op",
			}

			_, updated, err := reconcileScheduledAgent(updater, "ctx", fields, tt.requested)
			if err != nil {
				t.Fatalf("reconcileScheduledAgent: %v", err)
			}
			if updated || updater.calls != 0 {
				t.Fatalf("updated = %v, calls = %d; want unchanged no-op", updated, updater.calls)
			}
			if fields.DispatchFailures != 2 || fields.LastFailure != "preserve on no-op" {
				t.Fatalf("no-op changed failure state: %+v", fields)
			}
		})
	}
}

func TestReconcileScheduledAgentFailsClosedOnPersistenceError(t *testing.T) {
	updater := &recordingSlingContextUpdater{err: errors.New("database unavailable")}
	fields := &capacity.SlingContextFields{
		Agent:            "umans-glm",
		DispatchFailures: 3,
		LastFailure:      "old route failed",
	}

	_, updated, err := reconcileScheduledAgent(updater, "ctx", fields, "codex-impl-xhigh")
	if err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("error = %v, want database unavailable", err)
	}
	if updated {
		t.Fatal("updated = true after persistence failure")
	}
	if fields.Agent != "umans-glm" || fields.DispatchFailures != 3 || fields.LastFailure != "old route failed" {
		t.Fatalf("failed persistence mutated original fields: %+v", fields)
	}
}
