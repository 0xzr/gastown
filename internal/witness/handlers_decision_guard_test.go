package witness

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/mayor"
)

// stubDecisionForBead replaces the package-level mayor-decision resolver for the
// duration of a test. Returning (nil, nil) means "no active decision".
func stubDecisionForBead(t *testing.T, decision *mayor.Decision) {
	t.Helper()
	orig := activeMayorDecisionForBead
	activeMayorDecisionForBead = func(townRoot, beadID string) (*mayor.Decision, error) {
		if decision == nil {
			return nil, nil
		}
		if beadID == decision.BeadID {
			return decision, nil
		}
		return nil, nil
	}
	t.Cleanup(func() { activeMayorDecisionForBead = orig })
}

// The guard's notify path runs with a nil router, so it falls through to a
// tmux nudge against a nonexistent mayor session (fails harmlessly). Tests
// assert on HandlerResult, which is set before the notify call returns.

func TestHandleMergeFailed_BlockedByMayorDefer_SuppressesReworkNudge(t *testing.T) {
	// Not parallel: overrides package-level activeMayorDecisionForBead.
	// Regression for hq-12zq: an active Mayor DEFER on the source bead must
	// prevent the rework nudge to a live polecat after a refinery rejection.
	stubDecisionForBead(t, &mayor.Decision{
		BeadID:    "polybot-uiu",
		Type:      mayor.DecisionDefer,
		Reason:    "PARK/DEFER per priority realignment",
		MayorID:   "mayor/acp",
		Timestamp: time.Date(2026, 6, 24, 15, 16, 0, 0, time.UTC),
	})

	msg := &mail.Message{
		ID:      "m1",
		Subject: "MERGE_FAILED rust",
		Body: strings.Join([]string{
			"Branch: polecat/rust/polybot-uiu",
			"Issue: polybot-uiu",
			"FailureType: peer-review",
			"Error: rejected by codex peer review",
		}, "\n"),
	}

	result := HandleMergeFailed("/tmp/test-hq12zq", "polybot", msg, nil)
	if !result.Handled {
		t.Fatal("expected Handled=true even when rework is held")
	}
	if !strings.Contains(result.Action, "held") {
		t.Errorf("expected Action to report rework held, got %q", result.Action)
	}
	if !strings.Contains(result.Action, "polybot-uiu") {
		t.Errorf("expected Action to reference the deferred bead, got %q", result.Action)
	}
	if !strings.Contains(result.Action, "defer") {
		t.Errorf("expected Action to reference the Mayor decision type, got %q", result.Action)
	}
	// A held rework is not an error.
	if result.Error != nil {
		t.Errorf("held rework must not set an error, got %v", result.Error)
	}
}

func TestHandleMergeFailed_BlockedByMayorHold(t *testing.T) {
	stubDecisionForBead(t, &mayor.Decision{
		BeadID: "gt-hold1",
		Type:   mayor.DecisionHold,
	})

	msg := &mail.Message{
		ID:      "m2",
		Subject: "MERGE_FAILED alpha",
		Body: strings.Join([]string{
			"Branch: polecat/alpha/feature",
			"Issue: gt-hold1",
			"FailureType: test",
			"Error: tests failed",
		}, "\n"),
	}

	result := HandleMergeFailed("/tmp", "gastown", msg, nil)
	if !result.Handled || !strings.Contains(result.Action, "hold") {
		t.Errorf("expected hold to suppress rework, got Handled=%v Action=%q err=%v",
			result.Handled, result.Action, result.Error)
	}
}

func TestHandleMergeFailed_BlockedByMayorPark(t *testing.T) {
	stubDecisionForBead(t, &mayor.Decision{
		BeadID: "gt-park1",
		Type:   mayor.DecisionPark,
	})

	msg := &mail.Message{
		ID:      "m3",
		Subject: "MERGE_FAILED beta",
		Body: strings.Join([]string{
			"Issue: gt-park1",
			"Branch: polecat/beta/x",
			"FailureType: build",
			"Error: build broke",
		}, "\n"),
	}

	result := HandleMergeFailed("/tmp", "gastown", msg, nil)
	if !result.Handled || !strings.Contains(result.Action, "park") {
		t.Errorf("expected park to suppress rework, got Handled=%v Action=%q",
			result.Handled, result.Action)
	}
}

func TestHandleMergeFailed_ResumeDoesNotHoldRework(t *testing.T) {
	// Not parallel: overrides package-level activeMayorDecisionForBead.
	// A resume (no active block) must NOT short-circuit with a "held" action.
	// The downstream tmux nudge runs against a nonexistent session and fails
	// harmlessly; the contract under test is that resume does not hold rework.
	stubDecisionForBead(t, nil) // no active decision

	msg := &mail.Message{
		ID:      "m4",
		Subject: "MERGE_FAILED gamma",
		Body: strings.Join([]string{
			"Issue: gt-resume1",
			"Branch: polecat/gamma/y",
			"FailureType: lint",
			"Error: lint",
		}, "\n"),
	}

	result := HandleMergeFailed("/tmp", "gastown", msg, nil)
	if strings.Contains(result.Action, "held") {
		t.Errorf("resume must not hold rework, got Action=%q", result.Action)
	}
}

// TestHandleMergeFailed_StaleDecisionOnClosedBead_SkipsEmit verifies that a
// MERGE_FAILED for a closed/merged bead with a lingering Mayor decision does
// not emit a REWORK_DEFERRED notice. The decision is stale and the rework path
// is no longer relevant (gastown-okmd0).
func TestHandleMergeFailed_StaleDecisionOnClosedBead_SkipsEmit(t *testing.T) {
	stubDecisionForBead(t, &mayor.Decision{
		BeadID: "polybot-uiu",
		Type:   mayor.DecisionHold,
	})

	orig := bdForMergeFailedStatus
	bdForMergeFailedStatus = &BdCli{
		Exec: func(workDir string, args ...string) (string, error) {
			return `[{"status":"closed"}]`, nil
		},
		Run: func(workDir string, args ...string) error { return nil },
	}
	t.Cleanup(func() { bdForMergeFailedStatus = orig })

	msg := &mail.Message{
		ID:      "m6",
		Subject: "MERGE_FAILED rust",
		Body: strings.Join([]string{
			"Branch: polecat/rust/polybot-uiu",
			"Issue: polybot-uiu",
			"FailureType: peer-review",
			"Error: rejected",
		}, "\n"),
	}

	result := HandleMergeFailed("/tmp/test-stale", "polybot", msg, nil)
	if !result.Handled {
		t.Fatal("expected Handled=true")
	}
	if !strings.Contains(result.Action, "stale") {
		t.Errorf("expected Action to report stale decision, got %q", result.Action)
	}
	if strings.Contains(result.Action, "not nudged") && !strings.Contains(result.Action, "stale") {
		t.Errorf("expected stale reason in Action, got %q", result.Action)
	}
}

func TestHandleMergeFailed_NoIssueID_SkipsGuard(t *testing.T) {
	// A MERGE_FAILED without an Issue field has no decision key. The guard is
	// skipped entirely and the normal path runs. We assert the guard was never
	// consulted.
	guardFired := false
	orig := activeMayorDecisionForBead
	activeMayorDecisionForBead = func(townRoot, beadID string) (*mayor.Decision, error) {
		guardFired = true
		return nil, nil
	}
	t.Cleanup(func() { activeMayorDecisionForBead = orig })

	msg := &mail.Message{
		ID:      "m5",
		Subject: "MERGE_FAILED delta",
		Body: strings.Join([]string{
			"Branch: polecat/delta/z",
			"FailureType: test",
			"Error: no issue field",
		}, "\n"),
	}

	_ = HandleMergeFailed("/tmp", "gastown", msg, nil)
	if guardFired {
		t.Error("guard should not be consulted when payload has no IssueID")
	}
}
