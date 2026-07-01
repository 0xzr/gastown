package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initMainBranchRepo creates a git repo with a `main` branch and one initial
// commit. It also creates and checks out a `wrong-branch` so the Fix() under
// test has somewhere to come back from. Returns the repo path.
func initMainBranchRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()

	cmds := [][]string{
		{"git", "init", "-b", "main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "config", "commit.gpgsign", "false"},
	}
	for _, args := range cmds {
		c := exec.Command(args[0], args[1:]...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s failed: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "README.md"},
		{"git", "commit", "-m", "initial"},
	} {
		c := exec.Command(args[0], args[1:]...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s failed: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	// Move to a non-main branch so Fix() has work to do.
	c := exec.Command("git", "checkout", "-b", "wrong-branch")
	c.Dir = repo
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b wrong-branch failed: %v\n%s", err, out)
	}
	return repo
}

// runGitOutput runs git in dir and returns combined output AND error so callers
// can decide whether a non-zero exit is expected.
func runGitOutput(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	return string(out), err
}

// currentBranch returns the current branch name in dir.
func currentBranch(t *testing.T, dir string) string {
	t.Helper()
	out, err := runGitOutput(t, dir, "branch", "--show-current")
	if err != nil {
		t.Fatalf("git branch --show-current: %v\n%s", err, out)
	}
	return strings.TrimSpace(out)
}

// TestTownRootBranchCheck_Fix_AllowsRuntimeArtifacts is the regression test
// for gastown-25nzx: TownRootBranchCheck.Fix() previously refused to switch
// to main whenever `git status --porcelain` reported any output, including
// untracked toolchain state under .beads/, .runtime/, etc. It must allow the
// fix to proceed when only runtime artifacts are dirty.
func TestTownRootBranchCheck_Fix_AllowsRuntimeArtifacts(t *testing.T) {
	repo := initMainBranchRepo(t)

	// Drop an untracked runtime artifact (gitignored in a real town root).
	// This is the common shape of "porcelain output" that should NOT block
	// `gt doctor --fix`.
	runtimeDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "session.lock"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Sanity: porcelain reports the runtime artifact as untracked.
	porcOut, err := runGitOutput(t, repo, "status", "--porcelain")
	if err != nil {
		t.Fatalf("git status --porcelain: %v\n%s", err, porcOut)
	}
	if !strings.Contains(porcOut, ".beads") {
		t.Fatalf("expected porcelain to mention .beads, got:\n%s", porcOut)
	}

	check := NewTownRootBranchCheck()
	ctx := &CheckContext{TownRoot: repo}

	// Run first to populate c.currentBranch.
	_ = check.Run(ctx)

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() refused despite only-runtime dirt: %v", err)
	}
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("after Fix() branch = %q, want %q", got, "main")
	}
}

// TestTownRootBranchCheck_Fix_RefusesOnRealChanges confirms we still refuse
// when there are non-runtime uncommitted changes that would block checkout.
func TestTownRootBranchCheck_Fix_RefusesOnRealChanges(t *testing.T) {
	repo := initMainBranchRepo(t)

	// Modify the tracked README — this is a real change that git checkout
	// main would refuse to clobber.
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Changed\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownRootBranchCheck()
	ctx := &CheckContext{TownRoot: repo}
	_ = check.Run(ctx)

	err := check.Fix(ctx)
	if err == nil {
		t.Fatalf("Fix() should have refused on non-runtime dirty file, but succeeded; branch is now %q", currentBranch(t, repo))
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("expected 'uncommitted changes' in error, got: %v", err)
	}
	if got := currentBranch(t, repo); got != "wrong-branch" {
		t.Fatalf("branch should still be 'wrong-branch', got %q", got)
	}
}

// TestTownRootBranchCheck_Fix_CleanTree exercises the happy path: no dirty
// state at all, Fix() should move us back to main.
func TestTownRootBranchCheck_Fix_CleanTree(t *testing.T) {
	repo := initMainBranchRepo(t)

	check := NewTownRootBranchCheck()
	ctx := &CheckContext{TownRoot: repo}
	_ = check.Run(ctx)

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() on clean tree failed: %v", err)
	}
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("after Fix() branch = %q, want %q", got, "main")
	}
}

// TestTownRootBranchCheck_Fix_NoopOnMain confirms we don't try to switch
// away when already on a known-good branch.
func TestTownRootBranchCheck_Fix_NoopOnMain(t *testing.T) {
	repo := initMainBranchRepo(t)

	// We're on wrong-branch; switch to main first.
	if _, err := runGitOutput(t, repo, "checkout", "main"); err != nil {
		t.Fatalf("setup: checkout main: %v", err)
	}

	check := NewTownRootBranchCheck()
	ctx := &CheckContext{TownRoot: repo}
	_ = check.Run(ctx)

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() on already-main failed: %v", err)
	}
	if got := currentBranch(t, repo); got != "main" {
		t.Fatalf("branch should remain 'main', got %q", got)
	}
}
