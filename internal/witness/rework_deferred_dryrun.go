package witness

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/mayor"
)

// ReworkDeferredDryRunResult summarizes the outcome of a witness-side dry run
// that proves the REWORK_DEFERRED throttle suppresses repeated identical tuples
// and still emits on first occurrence and tuple changes.
type ReworkDeferredDryRunResult struct {
	// Pass is true when every assertion in the dry run succeeded.
	Pass bool `json:"pass"`
	// Window is the throttle window used for the run.
	Window time.Duration `json:"window"`
	// Tuples is one entry per regression case exercised.
	Tuples []ReworkDeferredDryRunTuple `json:"tuples"`
	// Errors lists human-readable failures when Pass is false.
	Errors []string `json:"errors,omitempty"`
	// StatePath is the temp dir where durable state was written.
	StatePath string `json:"state_path,omitempty"`
}

// ReworkDeferredDryRunTuple is the dry-run outcome for a single tuple.
type ReworkDeferredDryRunTuple struct {
	Bead           string                `json:"bead"`
	Decision       mayor.DecisionType    `json:"decision"`
	FirstAction    ReworkDeferredAction  `json:"first_action"`
	RepeatAction   ReworkDeferredAction  `json:"repeat_action"`
	RollupAction   ReworkDeferredAction  `json:"rollup_action"`
	SuppressedCount int                  `json:"suppressed_count"`
}

// DryRunReworkDeferred exercises the exact regression shape named in the
// acceptance criteria (polybot-uiu rig repeatedly emitting for gt-hold1,
// gt-park1, gt-work999) against real throttle state. A temp directory is used
// so production / witness state is never touched.
func DryRunReworkDeferred() (*ReworkDeferredDryRunResult, error) {
	dir, err := os.MkdirTemp("", "rework-deferred-dryrun-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp state dir: %w", err)
	}
	defer os.RemoveAll(dir)

	// Ensure state file lives inside a "witness" subdir to match the default
	// layout expected by loadReworkDeferredState.
	stateDir := filepath.Join(dir, "witness")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("creating witness state dir: %w", err)
	}

	// Point the throttle at our temp directory for the duration of the run.
	origStateFile := ReworkDeferredStateFile
	ReworkDeferredStateFile = func(_ string) string {
		return filepath.Join(stateDir, "rework-deferred-throttle.json")
	}
	defer func() { ReworkDeferredStateFile = origStateFile }()

	// Freeze time so the test is deterministic.
	origNow := reworkDeferredNow
	start := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	now := start
	reworkDeferredNow = func() time.Time { return now }
	defer func() { reworkDeferredNow = origNow }()

	window := config.DefaultReworkDeferredThrottleWindow

	tuples := []struct {
		bead     string
		decision mayor.DecisionType
	}{
		{"gt-hold1", mayor.DecisionHold},
		{"gt-park1", mayor.DecisionPark},
		{"gt-work999", mayor.DecisionDefer},
	}

	result := &ReworkDeferredDryRunResult{
		Pass:      true,
		Window:    window,
		StatePath: stateDir,
		Tuples:    make([]ReworkDeferredDryRunTuple, 0, len(tuples)),
	}

	const reason = "dry-run regression scenario"
	const rigName = "polybot-uiu"
	const polecatName = "alpha"
	const sourceStatus = "merge_failed"

	// First wave: each tuple must emit.
	for _, tup := range tuples {
		dec := EvaluateReworkDeferred(dir, rigName, tup.bead, polecatName, sourceStatus,
			reason, tup.decision, window)
		if dec.Action != ActionEmit {
			result.Pass = false
			result.addError("first occurrence for %s: got %s, want emit", tup.bead, dec.Action)
		}
		result.Tuples = append(result.Tuples, ReworkDeferredDryRunTuple{
			Bead:          tup.bead,
			Decision:      tup.decision,
			FirstAction:   dec.Action,
		})
	}

	// Repeated patrol cycles inside the window must be suppressed.
	const repeatCount = 10
	for i := 0; i < repeatCount; i++ {
		now = now.Add(2 * time.Minute)
		for j, tup := range tuples {
			dec := EvaluateReworkDeferred(dir, rigName, tup.bead, polecatName, sourceStatus,
				reason, tup.decision, window)
			want := ActionSuppress
			if i == repeatCount-1 {
				want = ActionSuppress // still inside window; rollup comes after window
			}
			if dec.Action != want {
				result.Pass = false
				result.addError("repeat #%d for %s: got %s, want %s", i+1, tup.bead, dec.Action, want)
			}
			result.Tuples[j].RepeatAction = dec.Action
			result.Tuples[j].SuppressedCount = dec.Record.SuppressedCount
		}
	}

	// After the window elapses, the next identical call must rollup.
	now = now.Add(50 * time.Minute)
	for j, tup := range tuples {
		dec := EvaluateReworkDeferred(dir, rigName, tup.bead, polecatName, sourceStatus,
			reason, tup.decision, window)
		if dec.Action != ActionRollup {
			result.Pass = false
			result.addError("post-window for %s: got %s, want rollup", tup.bead, dec.Action)
		}
		result.Tuples[j].RollupAction = dec.Action
	}

	// State change must emit immediately regardless of the window.
	now = now.Add(5 * time.Minute)
	for _, tup := range tuples {
		changedStatus := "hooked"
		dec := EvaluateReworkDeferred(dir, rigName, tup.bead, polecatName, changedStatus,
			reason, tup.decision, window)
		if dec.Action != ActionEmit {
			result.Pass = false
			result.addError("status change for %s: got %s, want emit", tup.bead, dec.Action)
		}
	}

	return result, nil
}

func (r *ReworkDeferredDryRunResult) addError(format string, args ...interface{}) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
}
