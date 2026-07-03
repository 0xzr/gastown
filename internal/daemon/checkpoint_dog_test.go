package daemon

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckpointDogInterval_Default(t *testing.T) {
	interval := checkpointDogInterval(nil)
	if interval != defaultCheckpointDogInterval {
		t.Errorf("expected default interval %v, got %v", defaultCheckpointDogInterval, interval)
	}
}

func TestCheckpointDogInterval_NilPatrols(t *testing.T) {
	config := &DaemonPatrolConfig{}
	interval := checkpointDogInterval(config)
	if interval != defaultCheckpointDogInterval {
		t.Errorf("expected default interval %v, got %v", defaultCheckpointDogInterval, interval)
	}
}

func TestCheckpointDogInterval_NilCheckpointDog(t *testing.T) {
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{},
	}
	interval := checkpointDogInterval(config)
	if interval != defaultCheckpointDogInterval {
		t.Errorf("expected default interval %v, got %v", defaultCheckpointDogInterval, interval)
	}
}

func TestCheckpointDogInterval_Configured(t *testing.T) {
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CheckpointDog: &CheckpointDogConfig{
				Enabled:     true,
				IntervalStr: "5m",
			},
		},
	}
	interval := checkpointDogInterval(config)
	if interval != 5*time.Minute {
		t.Errorf("expected 5m, got %v", interval)
	}
}

func TestCheckpointDogInterval_InvalidFallsBack(t *testing.T) {
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CheckpointDog: &CheckpointDogConfig{
				Enabled:     true,
				IntervalStr: "not-a-duration",
			},
		},
	}
	interval := checkpointDogInterval(config)
	if interval != defaultCheckpointDogInterval {
		t.Errorf("expected default interval for invalid config, got %v", interval)
	}
}

func TestCheckpointDogInterval_ZeroFallsBack(t *testing.T) {
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CheckpointDog: &CheckpointDogConfig{
				Enabled:     true,
				IntervalStr: "0s",
			},
		},
	}
	interval := checkpointDogInterval(config)
	if interval != defaultCheckpointDogInterval {
		t.Errorf("expected default interval for zero config, got %v", interval)
	}
}

func TestCheckpointDogEnabled(t *testing.T) {
	// Nil config → disabled (opt-in patrol)
	if IsPatrolEnabled(nil, "checkpoint_dog") {
		t.Error("expected checkpoint_dog disabled for nil config")
	}

	// Explicitly enabled
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			CheckpointDog: &CheckpointDogConfig{
				Enabled: true,
			},
		},
	}
	if !IsPatrolEnabled(config, "checkpoint_dog") {
		t.Error("expected checkpoint_dog enabled")
	}

	// Explicitly disabled
	config.Patrols.CheckpointDog.Enabled = false
	if IsPatrolEnabled(config, "checkpoint_dog") {
		t.Error("expected checkpoint_dog disabled when Enabled=false")
	}
}

func TestResolveCheckpointWorkDir_NestedLayout(t *testing.T) {
	// New polecat layout: polecats/<name>/<rigName>/.git is the worktree.
	tmp := t.TempDir()
	rig := "myrig"
	polecat := "alice"
	polecatsDir := filepath.Join(tmp, "polecats")
	worktree := filepath.Join(polecatsDir, polecat, rig)
	if err := os.MkdirAll(filepath.Join(worktree, ".git"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got := resolveCheckpointWorkDir(polecatsDir, polecat, rig)
	if got != worktree {
		t.Errorf("got %q, want %q", got, worktree)
	}
}

func TestResolveCheckpointWorkDir_LegacyFlatLayout(t *testing.T) {
	// Legacy layout: polecats/<name>/.git directly. polecat.Manager still
	// recognizes this; checkpoint_dog must too rather than silently skip.
	tmp := t.TempDir()
	rig := "myrig"
	polecat := "bob"
	polecatsDir := filepath.Join(tmp, "polecats")
	worktree := filepath.Join(polecatsDir, polecat)
	if err := os.MkdirAll(filepath.Join(worktree, ".git"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got := resolveCheckpointWorkDir(polecatsDir, polecat, rig)
	if got != worktree {
		t.Errorf("got %q, want %q (legacy flat layout)", got, worktree)
	}
}

func TestResolveCheckpointWorkDir_NoGitNeitherLevel(t *testing.T) {
	// Critical regression case: polecat container exists but has no .git
	// at either level. Function MUST return "" so the caller skips, NOT
	// fall back to a parent dir (which would have the workspace's .git
	// and cause the wrong-branch checkpoint bug this code prevents).
	tmp := t.TempDir()
	rig := "myrig"
	polecat := "carol"
	polecatsDir := filepath.Join(tmp, "polecats")
	if err := os.MkdirAll(filepath.Join(polecatsDir, polecat, rig), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Simulate top-level workspace .git that git would walk up to find.
	// resolveCheckpointWorkDir must NOT return a path that lets git walk
	// to this — it should return "" so the caller skips entirely.
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
		t.Fatalf("setup parent .git: %v", err)
	}
	got := resolveCheckpointWorkDir(polecatsDir, polecat, rig)
	if got != "" {
		t.Errorf("got %q, want empty (skip — no polecat-level .git)", got)
	}
}

func TestResolveCheckpointWorkDir_PrefersNestedOverFlat(t *testing.T) {
	// If both levels have .git (transitional state during a migration),
	// prefer the nested (newer) layout.
	tmp := t.TempDir()
	rig := "myrig"
	polecat := "dave"
	polecatsDir := filepath.Join(tmp, "polecats")
	flat := filepath.Join(polecatsDir, polecat)
	nested := filepath.Join(flat, rig)
	for _, d := range []string{flat, nested} {
		if err := os.MkdirAll(filepath.Join(d, ".git"), 0o755); err != nil {
			t.Fatalf("setup %s: %v", d, err)
		}
	}
	got := resolveCheckpointWorkDir(polecatsDir, polecat, rig)
	if got != nested {
		t.Errorf("got %q, want nested %q", got, nested)
	}
}

func TestIsGitWorktree(t *testing.T) {
	tmp := t.TempDir()
	if isGitWorktree(tmp) {
		t.Error("empty dir should not be a worktree")
	}
	// .git as directory (full clone)
	dirGit := filepath.Join(tmp, "fullclone")
	if err := os.MkdirAll(filepath.Join(dirGit, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !isGitWorktree(dirGit) {
		t.Error(".git directory should count as worktree")
	}
	// .git as file (linked worktree — git uses a file pointing to commondir)
	fileGit := filepath.Join(tmp, "linked")
	if err := os.MkdirAll(fileGit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fileGit, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isGitWorktree(fileGit) {
		t.Error(".git file (linked worktree) should count as worktree")
	}
}

// checkpointTestRepoNoOrigin creates a temporary git repository with an initial
// commit on "main" and no remote "origin". This simulates the state where the
// remote default branch cannot be resolved.
func checkpointTestRepoNoOrigin(t *testing.T) (workDir string, polecatName string) {
	t.Helper()
	tmpDir := t.TempDir()
	polecatName = "opal"
	workDir = filepath.Join(tmpDir, "work")

	runCheckpointCmd(t, tmpDir, "git", "init", "--initial-branch=main", workDir)
	runCheckpointCmd(t, workDir, "git", "config", "user.email", "test@test.com")
	runCheckpointCmd(t, workDir, "git", "config", "user.name", "Test")
	checkpointWriteFile(t, workDir, "README.md", "# Test\n")
	runCheckpointCmd(t, workDir, "git", "add", ".")
	runCheckpointCmd(t, workDir, "git", "commit", "-m", "initial commit")
	return workDir, polecatName
}

// checkpointTestRepo creates a temporary git repository with an initial commit
// on a branch named "main" and a remote "origin" pointing to a bare repo.
func checkpointTestRepo(t *testing.T) (workDir string, polecatName string) {
	t.Helper()
	tmpDir := t.TempDir()
	polecatName = "opal"
	bareDir := filepath.Join(tmpDir, "origin.git")
	workDir = filepath.Join(tmpDir, "work")

	runCheckpointCmd(t, tmpDir, "git", "init", "--bare", "--initial-branch=main", bareDir)
	runCheckpointCmd(t, tmpDir, "git", "clone", bareDir, workDir)
	runCheckpointCmd(t, workDir, "git", "config", "user.email", "test@test.com")
	runCheckpointCmd(t, workDir, "git", "config", "user.name", "Test")
	runCheckpointCmd(t, workDir, "git", "checkout", "-b", "main")
	checkpointWriteFile(t, workDir, "README.md", "# Test\n")
	runCheckpointCmd(t, workDir, "git", "add", ".")
	runCheckpointCmd(t, workDir, "git", "commit", "-m", "initial commit")
	runCheckpointCmd(t, workDir, "git", "push", "-u", "origin", "main")
	return workDir, polecatName
}

func runCheckpointCmd(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %s %v failed: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func checkpointWriteFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func checkpointHeadBranch(t *testing.T, workDir string) string {
	t.Helper()
	return runCheckpointCmd(t, workDir, "git", "rev-parse", "--abbrev-ref", "HEAD")
}

func checkpointHeadMessage(t *testing.T, workDir string) string {
	t.Helper()
	return runCheckpointCmd(t, workDir, "git", "log", "-1", "--format=%s")
}

func TestCheckpointWorktree_OnDefaultBranch_LandsOnWIPBranch(t *testing.T) {
	workDir, polecatName := checkpointTestRepo(t)

	d := &Daemon{logger: log.New(os.Stdout, "", 0)}

	// Dirty the worktree while on main.
	checkpointWriteFile(t, workDir, "feature.go", "package main\n")

	if got := checkpointHeadBranch(t, workDir); got != "main" {
		t.Fatalf("expected to start on main, got %q", got)
	}

	if !d.checkpointWorktree(workDir, "gastown", polecatName) {
		t.Fatal("expected checkpoint to be created")
	}

	// The checkpoint must have moved to wip/<polecat>.
	if got := checkpointHeadBranch(t, workDir); got != "wip/opal" {
		t.Errorf("expected branch wip/opal, got %q", got)
	}
	if msg := checkpointHeadMessage(t, workDir); msg != "WIP: checkpoint (auto)" {
		t.Errorf("expected WIP checkpoint message, got %q", msg)
	}

	// main must remain clean (no WIP commit on it).
	runCheckpointCmd(t, workDir, "git", "checkout", "main")
	if msg := checkpointHeadMessage(t, workDir); msg != "initial commit" {
		t.Errorf("main was polluted by WIP commit; got message %q", msg)
	}
}

func TestCheckpointWorktree_OnFeatureBranch_KeepsCurrentBranch(t *testing.T) {
	workDir, polecatName := checkpointTestRepo(t)

	d := &Daemon{logger: log.New(os.Stdout, "", 0)}

	// Create and switch to a feature branch.
	runCheckpointCmd(t, workDir, "git", "checkout", "-b", "polecat/opal/gastown-vi6@test")
	checkpointWriteFile(t, workDir, "feature.go", "package main\n")

	if !d.checkpointWorktree(workDir, "gastown", polecatName) {
		t.Fatal("expected checkpoint to be created")
	}

	// The checkpoint should stay on the existing feature branch.
	if got := checkpointHeadBranch(t, workDir); got != "polecat/opal/gastown-vi6@test" {
		t.Errorf("expected to stay on feature branch, got %q", got)
	}
	if msg := checkpointHeadMessage(t, workDir); msg != "WIP: checkpoint (auto)" {
		t.Errorf("expected WIP checkpoint message, got %q", msg)
	}
}

func TestIsProtectedCheckpointBranch_DefaultResolutionFails_IsProtected(t *testing.T) {
	workDir, _ := checkpointTestRepoNoOrigin(t)

	// With no origin, checkpointDefaultBranch returns "". The branch must be
	// treated as protected (fail-closed), not as unprotected (fail-open).
	if !isProtectedCheckpointBranch(workDir, "main") {
		t.Error("expected main to be protected when default branch cannot be resolved")
	}
}

func TestEnsureCheckpointBranch_NoOrigin_ReturnsError(t *testing.T) {
	workDir, polecatName := checkpointTestRepoNoOrigin(t)

	d := &Daemon{logger: log.New(os.Stdout, "", 0)}
	_, err := d.ensureCheckpointBranch(workDir, polecatName)
	if err == nil {
		t.Fatal("expected error when default branch cannot be resolved")
	}
	if !strings.Contains(err.Error(), "could not determine default branch") {
		t.Errorf("expected default-branch error, got: %v", err)
	}
}

func TestCheckpointWorktree_NoOrigin_DoesNotCommitOnDefault(t *testing.T) {
	workDir, polecatName := checkpointTestRepoNoOrigin(t)

	d := &Daemon{logger: log.New(os.Stdout, "", 0)}

	// Dirty the worktree while on main with no origin.
	checkpointWriteFile(t, workDir, "feature.go", "package main\n")

	if got := checkpointHeadBranch(t, workDir); got != "main" {
		t.Fatalf("expected to start on main, got %q", got)
	}

	// The checkpoint must be refused, not committed directly to main.
	if d.checkpointWorktree(workDir, "gastown", polecatName) {
		t.Fatal("expected checkpoint to be refused when default branch is unreachable")
	}

	// main must remain clean (no WIP commit).
	if msg := checkpointHeadMessage(t, workDir); msg != "initial commit" {
		t.Errorf("main was polluted by WIP commit; got message %q", msg)
	}

	// The staged changes should still be present (not committed).
	statusOut := runCheckpointCmd(t, workDir, "git", "status", "--porcelain")
	if !strings.Contains(statusOut, "feature.go") {
		t.Errorf("expected feature.go to remain staged/uncommitted, got status:\n%s", statusOut)
	}
}
