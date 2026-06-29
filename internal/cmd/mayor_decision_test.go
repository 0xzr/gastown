package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/mayor"
)

// setupTestTownForDecision creates a minimal Gas Town workspace marker so
// workspace.FindFromCwdOrError resolves the temp dir as a town root.
// It returns the town root and chdir's into it (restored on cleanup).
func setupTestTownForDecision(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()

	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0o750); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	townConfig := &config.TownConfig{
		Type:       "town",
		Version:    config.CurrentTownVersion,
		Name:       "test-town",
		PublicName: "Test Town",
		CreatedAt:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := config.SaveTownConfig(filepath.Join(mayorDir, "town.json"), townConfig); err != nil {
		t.Fatalf("save town.json: %v", err)
	}

	originalWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(originalWd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	return townRoot
}

func resetDecisionFlags() {
	mayorDecisionReason = ""
	mayorDecisionMayor = ""
}

// executeMayorDecision runs `gt mayor decision <args...>` through the full
// cobra command tree (rootCmd → mayorCmd → mayorDecisionCmd → leaf) and
// returns the captured stdout/stderr plus any error from
// traversal/parsing/RunE.
//
// Cobra forwards Execute() to the root command when called on a non-root
// subcommand (see cobra command.go ExecuteC), so we must invoke rootCmd with
// the full "mayor decision ..." arg path to actually exercise the wiring.
//
// Exercising the cobra tree — rather than calling runMayorDecisionRecord /
// runMayorDecisionList / runMayorDecisionShow directly — ensures the test
// catches:
//   - unwired or missing subcommands under mayorDecisionCmd
//   - arg-parsing regressions (e.g., ExactArgs, Required flags)
//   - flag-binding regressions on --reason / --mayor
//
// The previous direct-func tests proved the data layer (decisions.go) but
// could not catch a broken wiring, which is why gastown-cet.7 split-verdict
// flagged this as test debt (gastown-l9j / gastown-cet.7 test adequacy).
//
// Output capture: cobra's SetOut/SetErr only redirect cobra's internal
// writers. The run* functions also call fmt.Printf to os.Stdout directly, so
// we must redirect os.Stdout/os.Stderr as well. We use two separate
// bytes.Buffers — one for cobra's writers, one for os-level stdio drained
// in a goroutine — because concurrent writes to a single bytes.Buffer from
// cobra and the drain goroutine race and silently corrupt the buffer.
func executeMayorDecision(t *testing.T, args ...string) (string, error) {
	t.Helper()

	// Reset package-level flag vars so a prior Execute doesn't leak into this run.
	// These are bound to the leaf cobra commands via Flags().StringVar in init().
	resetDecisionFlags()

	fullArgs := append([]string{"mayor", "decision"}, args...)
	rootCmd.SetArgs(fullArgs)

	var cobraBuf bytes.Buffer
	rootCmd.SetOut(&cobraBuf)
	rootCmd.SetErr(&cobraBuf)

	// Capture os.Stdout/os.Stderr as well (run* funcs use fmt.Printf directly,
	// and persistentPreRun uses fmt.Fprintf(os.Stderr, ...)). We must drain
	// concurrently — otherwise any subprocess spawned during Execute (e.g., a
	// `bd version` subprocess) that inherits the pipe will block once the 4KB
	// OS buffer fills, deadlocking the test run.
	var osBuf bytes.Buffer
	origStdout, origStderr := os.Stdout, os.Stderr
	stdr, stdw, _ := os.Pipe()
	os.Stdout = stdw
	os.Stderr = stdw

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&osBuf, stdr)
		close(done)
	}()

	err := rootCmd.Execute()

	_ = stdw.Close()
	os.Stdout = origStdout
	os.Stderr = origStderr
	<-done
	_ = stdr.Close()

	// Clear args so subsequent Execute calls don't inherit them.
	rootCmd.SetArgs(nil)

	// Combine cobra + os-level output. Either alone is incomplete: cobra
	// emits errors/help via its Err writer; run* funcs print results via
	// fmt.Printf → os.Stdout.
	return cobraBuf.String() + osBuf.String(), err
}

func TestMayorDecision_DeferBlocksAndShowLists(t *testing.T) {
	townRoot := setupTestTownForDecision(t)
	t.Cleanup(resetDecisionFlags)

	// Record defer via the cobra tree; --reason exercises the --reason flag binding.
	out, err := executeMayorDecision(t, "defer", "polybot-uiu", "--reason", "deprioritized")
	if err != nil {
		t.Fatalf("gt mayor decision defer via cobra: %v\n%s", err, out)
	}

	// Active decision present and blocking.
	state, err := mayor.LoadDecisions(townRoot)
	if err != nil {
		t.Fatalf("LoadDecisions: %v", err)
	}
	d, err := state.ActiveDecision("polybot-uiu")
	if err != nil {
		t.Fatalf("expected active defer, got %v", err)
	}
	if d.Type != mayor.DecisionDefer || d.Reason != "deprioritized" {
		t.Errorf("unexpected decision: %+v", d)
	}

	// List via the cobra tree — must also go through the wiring, not the func.
	listOut, err := executeMayorDecision(t, "list")
	if err != nil {
		t.Fatalf("gt mayor decision list via cobra: %v\n%s", err, listOut)
	}
	if !strings.Contains(listOut, "polybot-uiu") || !strings.Contains(listOut, "defer") {
		t.Errorf("list output missing bead/type, got: %s", listOut)
	}
}

func TestMayorDecision_ResumeOverridesDefer(t *testing.T) {
	townRoot := setupTestTownForDecision(t)
	t.Cleanup(resetDecisionFlags)

	if _, err := executeMayorDecision(t, "hold", "gt-resume-cli"); err != nil {
		t.Fatalf("hold via cobra: %v", err)
	}
	if _, err := executeMayorDecision(t, "resume", "gt-resume-cli"); err != nil {
		t.Fatalf("resume via cobra: %v", err)
	}

	state, err := mayor.LoadDecisions(townRoot)
	if err != nil {
		t.Fatalf("LoadDecisions: %v", err)
	}
	if _, err := state.ActiveDecision("gt-resume-cli"); err != mayor.ErrDecisionNotFound {
		t.Errorf("expected resume to clear active block, got %v", err)
	}
	// Prior blocking decision still recorded for audit.
	if _, err := state.PriorBlockingDecision("gt-resume-cli"); err != nil {
		t.Errorf("expected prior block to remain recorded, got %v", err)
	}
}

func TestMayorDecision_ShowNoDecision(t *testing.T) {
	setupTestTownForDecision(t)
	out, err := executeMayorDecision(t, "show", "gt-none")
	if err != nil {
		t.Fatalf("gt mayor decision show via cobra: %v\n%s", err, out)
	}
	if !strings.Contains(out, "No active Mayor decision") {
		t.Errorf("expected no-decision message, got: %s", out)
	}
}

// TestMayorDecision_InvalidArgCountRejected exercises the cobra tree's
// ExactArgs validation. The leaf subcommands all declare cobra.ExactArgs(1)
// for the bead-id; passing extra args must surface as a parse error rather
// than silently dropping the trailing args. This is the cobra-tree analogue
// of the previous direct-func "invalid type" test — both guard against a
// regression that would let malformed input reach the decision store.
func TestMayorDecision_InvalidArgCountRejected(t *testing.T) {
	setupTestTownForDecision(t)
	out, err := executeMayorDecision(t, "defer", "gt-x", "extra-positional-arg")
	if err == nil {
		t.Errorf("expected error for 'defer' with extra positional arg, got nil\noutput: %s", out)
	}
	// Cobra prints the accepts-N-args error to its Err writer, which we
	// captured into `out`.
	if !strings.Contains(out, "accepts 1 arg") {
		t.Errorf("expected error output to mention 'accepts 1 arg', got: %s", out)
	}
}
