package daemon

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/util"
)

const (
	defaultCheckpointDogInterval = 10 * time.Minute
)

// CheckpointDogConfig holds configuration for the checkpoint_dog patrol.
type CheckpointDogConfig struct {
	// Enabled controls whether the checkpoint dog runs.
	Enabled bool `json:"enabled"`

	// IntervalStr is how often to run, as a string (e.g., "10m").
	IntervalStr string `json:"interval,omitempty"`
}

// checkpointDogInterval returns the configured interval, or the default (10m).
func checkpointDogInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.CheckpointDog != nil {
		if config.Patrols.CheckpointDog.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.CheckpointDog.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultCheckpointDogInterval
}

// runtimeExcludeDirs are directories to unstage after git add -A.
// These contain runtime/ephemeral data that should not be checkpointed.
var runtimeExcludeDirs = []string{
	".claude/",
	".beads/",
	".runtime/",
	"__pycache__/",
}

// wipBranchPrefix is the local ref namespace used when a polecat's worktree
// is on the default branch. Auto-checkpoints must never land on the default
// branch, so we commit to wip/<polecat> instead.
const wipBranchPrefix = "wip/"

// runCheckpointDog auto-commits WIP changes in active polecat worktrees.
// This protects against data loss when sessions crash or hit context limits.
//
// ## ZFC Exemption
// The checkpoint dog executes git operations directly (same pattern as
// compactor_dog's SQL operations). The daemon pours a molecule for
// observability, then runs git commands via exec.Command.
func (d *Daemon) runCheckpointDog() {
	if !d.isPatrolActive("checkpoint_dog") {
		return
	}

	d.logger.Printf("checkpoint_dog: starting cycle")

	mol := d.pourDogMolecule(constants.MolDogCheckpoint, nil)
	defer mol.close()

	rigs := d.getKnownRigs()
	totalScanned := 0
	totalCheckpointed := 0

	for _, rigName := range rigs {
		scanned, checkpointed := d.checkpointRigPolecats(rigName)
		totalScanned += scanned
		totalCheckpointed += checkpointed
	}

	mol.closeStep("scan")
	mol.closeStep("checkpoint")

	d.logger.Printf("checkpoint_dog: cycle complete — scanned %d worktrees, checkpointed %d",
		totalScanned, totalCheckpointed)
	mol.closeStep("report")
}

// checkpointRigPolecats checkpoints dirty polecat worktrees in a single rig.
// Returns (scanned, checkpointed) counts.
func (d *Daemon) checkpointRigPolecats(rigName string) (int, int) {
	polecatsDir := filepath.Join(d.config.TownRoot, rigName, "polecats")
	polecats, err := listPolecatWorktrees(polecatsDir)
	if err != nil {
		return 0, 0
	}

	scanned := 0
	checkpointed := 0

	for _, polecatName := range polecats {
		scanned++

		// Check if tmux session is alive — only checkpoint active sessions.
		// Dead sessions can't benefit from checkpoints.
		sessionName := session.PolecatSessionName(session.PrefixFor(rigName), polecatName)
		alive, err := d.tmux.HasSession(sessionName)
		if err != nil {
			d.logger.Printf("checkpoint_dog: error checking session %s: %v", sessionName, err)
			continue
		}
		if !alive {
			continue
		}

		// Polecat layout: prefer <polecatsDir>/<name>/<rigName>/ (the new
		// nested layout where the outer <name>/ dir is a container with
		// per-polecat scaffolding and the inner dir is the actual git
		// worktree). Fall back to <polecatsDir>/<name>/ for the legacy
		// flat layout still supported by polecat.Manager. Both candidates
		// must contain `.git` — never fall back to a parent dir, since
		// the original bug here was exactly that: an empty <name>/
		// container caused git to walk up to the top-level workspace's
		// .git and commit "WIP: checkpoint (auto)" on the workspace's
		// branch (usually main) instead of the polecat's branch.
		// (gt-checkpoint-workdir fix.)
		workDir := resolveCheckpointWorkDir(polecatsDir, polecatName, rigName)
		if workDir == "" {
			continue // Neither layout has a usable .git — skip silently.
		}
		if d.checkpointWorktree(workDir, rigName, polecatName) {
			checkpointed++
		}
	}

	return scanned, checkpointed
}

// checkpointDefaultBranch resolves the repository's default branch, preferring
// the remote's HEAD and falling back to main/master. Returns "" when no
// default branch can be determined (e.g., a test repo with no origin).
func checkpointDefaultBranch(workDir string) string {
	if out, err := runGitCmd(workDir, "symbolic-ref", "refs/remotes/origin/HEAD"); err == nil && out != "" {
		parts := strings.Split(out, "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	for _, candidate := range []string{"main", "master"} {
		if _, err := runGitCmd(workDir, "rev-parse", "--verify", "origin/"+candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// checkpointCurrentBranch returns the current branch name, or "" if it cannot
// be resolved (e.g., detached HEAD without a branch name).
func checkpointCurrentBranch(workDir string) string {
	out, err := runGitCmd(workDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return out
}

// checkpointWIPBranchName returns the feature branch used for auto-checkpoints
// when the polecat is on the default branch.
func checkpointWIPBranchName(polecatName string) string {
	return wipBranchPrefix + polecatName
}

// isProtectedCheckpointBranch reports whether branch is the repository default
// branch on which WIP checkpoints are forbidden.
//
// If the default branch cannot be resolved (e.g. the remote is unreachable or
// missing), the function fails closed and treats the branch as protected.
// This prevents checkpoint_dog from committing WIP directly to what is
// likely main/master simply because it could not prove otherwise.
func isProtectedCheckpointBranch(workDir, branch string) bool {
	if branch == "" {
		return false
	}
	defaultBranch := checkpointDefaultBranch(workDir)
	if defaultBranch == "" {
		return true
	}
	return branch == defaultBranch
}

// ensureCheckpointBranch returns the branch that should receive the WIP
// checkpoint. If the worktree is on the default branch, it creates/resets and
// switches to wip/<polecatName> so the checkpoint cannot land on main. If the
// worktree is already on a feature branch, that branch is returned unchanged.
func (d *Daemon) ensureCheckpointBranch(workDir, polecatName string) (string, error) {
	current := checkpointCurrentBranch(workDir)
	if current == "" {
		return "", fmt.Errorf("could not determine current branch")
	}
	if !isProtectedCheckpointBranch(workDir, current) {
		return current, nil
	}

	defaultBranch := checkpointDefaultBranch(workDir)
	if defaultBranch == "" {
		return "", fmt.Errorf("could not determine default branch")
	}

	wip := checkpointWIPBranchName(polecatName)
	// Create or reset the WIP branch to the current default branch tip and
	// switch to it. Using -B keeps the staged changes intact while guaranteeing
	// the branch is at the same commit as the default branch, so the checkout
	// cannot conflict with the worktree contents.
	if _, err := runGitCmd(workDir, "checkout", "-B", wip, defaultBranch); err != nil {
		return "", fmt.Errorf("checkout -B %s %s: %w", wip, defaultBranch, err)
	}
	return wip, nil
}

// checkpointWorktree creates a WIP checkpoint commit for a single worktree.
// Returns true if a checkpoint was created.
func (d *Daemon) checkpointWorktree(workDir, rigName, polecatName string) bool {
	// Check git status (exclude runtime dirs from consideration)
	statusOut, err := runGitCmd(workDir, "status", "--porcelain")
	if err != nil {
		d.logger.Printf("checkpoint_dog: git status failed in %s/%s: %v", rigName, polecatName, err)
		return false
	}
	if strings.TrimSpace(statusOut) == "" {
		return false // Clean worktree
	}

	// Stage everything
	if _, err := runGitCmd(workDir, "add", "-A"); err != nil {
		d.logger.Printf("checkpoint_dog: git add -A failed in %s/%s: %v", rigName, polecatName, err)
		return false
	}

	// Unstage runtime/ephemeral directories
	for _, dir := range runtimeExcludeDirs {
		// git reset HEAD -- <dir> is safe even if dir doesn't exist (exits 0)
		_, _ = runGitCmd(workDir, "reset", "HEAD", "--", dir)
	}

	// Unstage deletions of tracked files. A checkpoint should preserve work
	// (additions + modifications), never commit deletions of tracked files.
	// This prevents the bug where a polecat's working tree has a missing
	// tracked file and the checkpoint commits the deletion (gt-pvx fix).
	if delOut, err := runGitCmd(workDir, "diff", "--cached", "--name-only", "--diff-filter=D"); err == nil {
		if dels := strings.TrimSpace(delOut); dels != "" {
			for _, f := range strings.Split(dels, "\n") {
				if f != "" {
					_, _ = runGitCmd(workDir, "reset", "HEAD", "--", f)
				}
			}
		}
	}

	// Check if anything is staged after exclusions
	diffOut, err := runGitCmd(workDir, "diff", "--cached", "--quiet")
	if err == nil && strings.TrimSpace(diffOut) == "" {
		// --quiet exits 0 if no diff → nothing staged
		return false
	}

	// Never commit a WIP checkpoint directly to the default branch. If the
	// polecat is on main (or master), move to a dedicated wip/<polecat>
	// feature branch first.
	commitBranch, err := d.ensureCheckpointBranch(workDir, polecatName)
	if err != nil {
		d.logger.Printf("checkpoint_dog: refusing to commit on default branch in %s/%s: %v", rigName, polecatName, err)
		return false
	}

	// Commit the checkpoint
	if _, err := runGitCmd(workDir, "commit", "-m", "WIP: checkpoint (auto)"); err != nil {
		d.logger.Printf("checkpoint_dog: git commit failed in %s/%s: %v", rigName, polecatName, err)
		return false
	}

	d.logger.Printf("checkpoint_dog: created WIP checkpoint in %s/%s on branch %s", rigName, polecatName, commitBranch)
	return true
}

// isGitWorktree reports whether the given directory is the root of a git
// worktree (has its own `.git` file or directory). Used to guard checkpoint
// commits against the "wrong-dir" failure mode where git operations in a
// non-worktree directory walk up the filesystem tree and commit on the
// parent workspace's branch.
func isGitWorktree(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// resolveCheckpointWorkDir picks the actual git-worktree directory for a
// polecat, supporting both the new nested layout (polecats/<name>/<rigName>/)
// and the legacy flat layout (polecats/<name>/) that polecat.Manager still
// recognizes for backward compatibility. Returns "" if neither candidate is
// a git worktree, in which case the caller MUST skip the polecat — never
// fall back to a parent directory, since git would walk up to the top-level
// workspace's .git and commit on the wrong branch (this is the bug this
// helper exists to prevent).
func resolveCheckpointWorkDir(polecatsDir, polecatName, rigName string) string {
	nested := filepath.Join(polecatsDir, polecatName, rigName)
	if isGitWorktree(nested) {
		return nested
	}
	flat := filepath.Join(polecatsDir, polecatName)
	if isGitWorktree(flat) {
		return flat
	}
	return ""
}

// runGitCmd executes a git command in the given directory and returns stdout.
func runGitCmd(workDir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workDir
	util.SetDetachedProcessGroup(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("%s: %s", err, errMsg)
		}
		return "", err
	}

	return strings.TrimSpace(stdout.String()), nil
}
