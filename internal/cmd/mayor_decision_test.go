package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/mayor"
)

// setupTestTown creates a minimal Gas Town workspace marker so
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

func TestMayorDecision_DeferBlocksAndShowLists(t *testing.T) {
	townRoot := setupTestTownForDecision(t)
	t.Cleanup(resetDecisionFlags)

	mayorDecisionReason = "deprioritized"
	if err := runMayorDecisionRecord("polybot-uiu", mayor.DecisionDefer); err != nil {
		t.Fatalf("record defer: %v", err)
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

	// List should include the recorded bead.
	out := captureStdout(t, func() {
		if err := runMayorDecisionList(&cobra.Command{}, nil); err != nil {
			t.Fatalf("list: %v", err)
		}
	})
	if !strings.Contains(out, "polybot-uiu") || !strings.Contains(out, "defer") {
		t.Errorf("list output missing bead/type, got: %s", out)
	}
}

func TestMayorDecision_ResumeOverridesDefer(t *testing.T) {
	townRoot := setupTestTownForDecision(t)
	t.Cleanup(resetDecisionFlags)

	if err := runMayorDecisionRecord("gt-resume-cli", mayor.DecisionHold); err != nil {
		t.Fatalf("record hold: %v", err)
	}
	if err := runMayorDecisionRecord("gt-resume-cli", mayor.DecisionResume); err != nil {
		t.Fatalf("record resume: %v", err)
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
	out := captureStdout(t, func() {
		if err := runMayorDecisionShow(&cobra.Command{}, []string{"gt-none"}); err != nil {
			t.Fatalf("show: %v", err)
		}
	})
	if !strings.Contains(out, "No active Mayor decision") {
		t.Errorf("expected no-decision message, got: %s", out)
	}
}

func TestMayorDecision_InvalidTypeRejected(t *testing.T) {
	setupTestTownForDecision(t)
	if err := runMayorDecisionRecord("gt-x", mayor.DecisionType("bogus")); err == nil {
		t.Error("expected error recording invalid decision type")
	}
}
