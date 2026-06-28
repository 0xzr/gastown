//go:build integration

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

type hookShowJSON struct {
	Agent      string `json:"agent"`
	BeadID     string `json:"bead_id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	SourceBead string `json:"source_bead"`
}

// TestHookShowShorthandResolvesToCanonical verifies that hook show accepts
// shorthand polecat targets (rig/name) and resolves them to canonical
// assignee IDs (rig/polecats/name) before querying hooked work.
func TestHookShowShorthandResolvesToCanonical(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping integration test")
	}

	townRoot, polecatDir, rigPrefix := setupHookTestTown(t)
	_ = townRoot

	rigDir := filepath.Join(polecatDir, "..", "..", "mayor", "rig")
	initBeadsDBWithPrefix(t, rigDir, rigPrefix)

	b := beads.New(rigDir)
	issue, err := b.Create(beads.CreateOptions{
		Title:    "Hook show target normalization test",
		Type:     "task",
		Priority: 2,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	hooked := beads.StatusHooked
	assignee := "gastown/polecats/toast"
	if err := b.Update(issue.ID, beads.UpdateOptions{
		Status:   &hooked,
		Assignee: &assignee,
	}); err != nil {
		t.Fatalf("hook issue: %v", err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(polecatDir); err != nil {
		t.Fatalf("chdir to polecat dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})

	prevJSON := moleculeJSON
	moleculeJSON = true
	t.Cleanup(func() {
		moleculeJSON = prevJSON
	})

	runShow := func(target string) hookShowJSON {
		out := captureStdout(t, func() {
			if err := runHookShow(nil, []string{target}); err != nil {
				t.Fatalf("runHookShow(%q): %v", target, err)
			}
		})
		var parsed hookShowJSON
		if err := json.Unmarshal([]byte(out), &parsed); err != nil {
			t.Fatalf("parse runHookShow(%q) output %q: %v", target, out, err)
		}
		return parsed
	}

	canonical := runShow("gastown/polecats/toast")
	if canonical.BeadID != issue.ID || canonical.Status != beads.StatusHooked {
		t.Fatalf("canonical target mismatch: got bead=%q status=%q, want bead=%q status=%q",
			canonical.BeadID, canonical.Status, issue.ID, beads.StatusHooked)
	}

	shorthand := runShow("gastown/toast")
	if shorthand.BeadID != issue.ID || shorthand.Status != beads.StatusHooked {
		t.Fatalf("shorthand target mismatch: got bead=%q status=%q, want bead=%q status=%q",
			shorthand.BeadID, shorthand.Status, issue.ID, beads.StatusHooked)
	}
	if shorthand.Agent != "gastown/polecats/toast" {
		t.Fatalf("shorthand target did not normalize: got agent=%q, want %q",
			shorthand.Agent, "gastown/polecats/toast")
	}

	inProgress := "in_progress"
	if err := b.Update(issue.ID, beads.UpdateOptions{Status: &inProgress}); err != nil {
		t.Fatalf("mark issue in progress: %v", err)
	}
	active := runShow("gastown/toast")
	if active.BeadID != issue.ID || active.Status != "in_progress" {
		t.Fatalf("in-progress target mismatch: got bead=%q status=%q, want bead=%q status=in_progress",
			active.BeadID, active.Status, issue.ID)
	}
}

// TestHookShowSourceBeadFromBranch verifies that when a polecat's normal hook
// is empty (e.g. the attached molecule/wisp was deleted), gt hook show still
// reports the authoritative source bead derived from the current branch name.
// This is the recovery path for the empty-hook done guard (gastown-dg1).
func TestHookShowSourceBeadFromBranch(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping integration test")
	}

	townRoot, polecatDir, rigPrefix := setupHookTestTown(t)
	_ = townRoot

	rigDir := filepath.Join(polecatDir, "..", "..", "mayor", "rig")
	initBeadsDBWithPrefix(t, rigDir, rigPrefix)

	b := beads.New(rigDir)
	issue, err := b.Create(beads.CreateOptions{
		Title:    "Source bead recovery test",
		Type:     "task",
		Priority: 2,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	// Assign the bead to the polecat but leave it "open" (not hooked). This
	// simulates a state where the hooked molecule was reaped but the base work
	// bead is still assigned to the agent.
	assignee := "gastown/polecats/toast"
	openStatus := "open"
	if err := b.Update(issue.ID, beads.UpdateOptions{
		Status:   &openStatus,
		Assignee: &assignee,
	}); err != nil {
		t.Fatalf("assign issue: %v", err)
	}

	// Put the polecat worktree on a branch named after the work bead.
	branch := fmt.Sprintf("polecat/toast/%s@recov", issue.ID)
	initPolecatBranch(t, polecatDir, branch)

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(polecatDir); err != nil {
		t.Fatalf("chdir to polecat dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})

	prevJSON := moleculeJSON
	moleculeJSON = true
	t.Cleanup(func() {
		moleculeJSON = prevJSON
	})

	// Clear session env so role detection falls back to cwd (toast worktree)
	// rather than inheriting the real polecat identity (quartz).
	t.Setenv("GT_ROLE", "")
	t.Setenv("GT_RIG", "")
	t.Setenv("GT_POLECAT", "")

	out := captureStdout(t, func() {
		if err := runHookShow(nil, nil); err != nil {
			t.Fatalf("runHookShow: %v", err)
		}
	})
	var parsed hookShowJSON
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse runHookShow output %q: %v", out, err)
	}

	if parsed.Status != "empty" {
		t.Fatalf("expected status empty, got %q", parsed.Status)
	}
	if parsed.BeadID != "" {
		t.Fatalf("expected no bead_id, got %q", parsed.BeadID)
	}
	if parsed.SourceBead != issue.ID {
		t.Fatalf("expected source_bead=%q, got %q", issue.ID, parsed.SourceBead)
	}
}

// TestHookShowSourceBeadMisresolvedTarget verifies that when the wrapper guard
// passes a misresolved target like "gastown/gastown" (cwd outside the worktree),
// gt hook show still recovers the source bead via GT_RIG/GT_POLECAT env.
func TestHookShowSourceBeadMisresolvedTarget(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping integration test")
	}

	townRoot, polecatDir, rigPrefix := setupHookTestTown(t)
	_ = townRoot

	rigDir := filepath.Join(polecatDir, "..", "..", "mayor", "rig")
	initBeadsDBWithPrefix(t, rigDir, rigPrefix)

	b := beads.New(rigDir)
	issue, err := b.Create(beads.CreateOptions{
		Title:    "Source bead misresolved target test",
		Type:     "task",
		Priority: 2,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	inProgress := "in_progress"
	assignee := "gastown/polecats/toast"
	if err := b.Update(issue.ID, beads.UpdateOptions{
		Status:   &inProgress,
		Assignee: &assignee,
	}); err != nil {
		t.Fatalf("assign issue: %v", err)
	}

	branch := fmt.Sprintf("polecat/toast/%s@recov", issue.ID)
	initPolecatBranch(t, polecatDir, branch)

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(polecatDir); err != nil {
		t.Fatalf("chdir to polecat dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})

	// Clear inherited role, then set the env identity that the misresolved
	// wrapper guard target should map to.
	t.Setenv("GT_ROLE", "")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "toast")

	prevJSON := moleculeJSON
	moleculeJSON = true
	t.Cleanup(func() {
		moleculeJSON = prevJSON
	})

	out := captureStdout(t, func() {
		// Pass the misresolved target observed when gt done runs from the
		// rig/town root instead of the polecat worktree.
		if err := runHookShow(nil, []string{"gastown/gastown"}); err != nil {
			t.Fatalf("runHookShow: %v", err)
		}
	})
	var parsed hookShowJSON
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse runHookShow output %q: %v", out, err)
	}

	if parsed.SourceBead != issue.ID {
		t.Fatalf("expected source_bead=%q for misresolved target, got %q", issue.ID, parsed.SourceBead)
	}
}

// initPolecatBranch creates a minimal real commit on the named branch in dir.
// This lets git operations such as CurrentBranch resolve the branch correctly.
func initPolecatBranch(t *testing.T, dir, branch string) {
	t.Helper()
	cmds := [][]string{
		{"git", "init", "--quiet"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "checkout", "-b", branch},
	}
	for _, c := range cmds {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v in %s: %v\n%s", c, dir, err, output)
		}
	}
	// Need at least one commit for git rev-parse and status plumbing to behave.
	marker := filepath.Join(dir, ".polecat-branch")
	if err := os.WriteFile(marker, []byte(branch+"\n"), 0644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add in %s: %v\n%s", dir, err, output)
	}
	cmd = exec.Command("git", "commit", "-m", "init", "--quiet")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit in %s: %v\n%s", dir, err, output)
	}
}
