package doctor

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/steveyegge/gastown/internal/git"
)

// TownRootBranchCheck verifies that the town root directory is on the default branch.
// The town root should always stay on the default branch to avoid confusion and
// broken gt commands. Accidental branch switches can happen when git commands
// run in the wrong directory.
//
// The "expected" branch is resolved at Run time from origin (via
// RemoteDefaultBranch), so this works regardless of whether the project uses
// "main", "master", "develop", or any other default.
type TownRootBranchCheck struct {
	FixableCheck
	currentBranch  string // Cached during Run for use in Fix
	expectedBranch string // Cached during Run for use in Fix
}

// NewTownRootBranchCheck creates a new town root branch check.
func NewTownRootBranchCheck() *TownRootBranchCheck {
	return &TownRootBranchCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "town-root-branch",
				CheckDescription: "Verify town root is on main branch",
				CheckCategory:    CategoryCore,
			},
		},
	}
}

// Run checks if the town root is on the expected default branch.
func (c *TownRootBranchCheck) Run(ctx *CheckContext) *CheckResult {
	// Get current branch
	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = ctx.TownRoot
	out, err := cmd.Output()
	if err != nil {
		// Not a git repo - skip this check (handled by town-git check)
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Town root is not a git repository (skipped)",
		}
	}

	branch := strings.TrimSpace(string(out))
	c.currentBranch = branch

	// Resolve expected branch from the town root's origin remote.
	// This honors whatever default the project actually uses (main/master/develop/...).
	expected := git.NewGit(ctx.TownRoot).RemoteDefaultBranch()
	if expected == "" {
		expected = "main" // absolute fallback
	}
	c.expectedBranch = expected

	// Empty branch means detached HEAD
	if branch == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Town root is in detached HEAD state",
			Details: []string{
				fmt.Sprintf("The town root should be on the %s branch", expected),
				"Detached HEAD can cause gt commands to fail",
			},
			FixHint: fmt.Sprintf("Run 'gt doctor --fix' or manually: cd ~/gt && git checkout %s", expected),
		}
	}

	// Accept the expected default branch or the legacy main/master/gt_managed values.
	if branch == expected || branch == "main" || branch == "master" || branch == "gt_managed" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("Town root is on %s branch", branch),
		}
	}

	// On wrong branch - this is the problem we're trying to prevent
	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusError,
		Message: fmt.Sprintf("Town root is on wrong branch: %s", branch),
		Details: []string{
			fmt.Sprintf("The town root (~/gt) must stay on the %s branch", expected),
			fmt.Sprintf("Currently on: %s", branch),
			"This can cause gt commands to fail (missing rigs.json, etc.)",
			"The branch switch was likely accidental (git command in wrong dir)",
		},
		FixHint: fmt.Sprintf("Run 'gt doctor --fix' or manually: cd ~/gt && git checkout %s", expected),
	}
}

// Fix switches the town root back to the expected default branch.
//
// The expected branch is computed by RemoteDefaultBranch() against the town
// root's origin remote, so this respects custom default branches (develop, etc.)
// rather than hardcoding "main"/"master".
func (c *TownRootBranchCheck) Fix(ctx *CheckContext) error {
	// Only fix if we're not already on the expected branch (or a legacy safe value).
	expected := c.expectedBranch
	if expected == "" {
		expected = git.NewGit(ctx.TownRoot).RemoteDefaultBranch()
		if expected == "" {
			expected = "main"
		}
		c.expectedBranch = expected
	}
	if c.currentBranch == expected || c.currentBranch == "main" || c.currentBranch == "master" || c.currentBranch == "gt_managed" {
		return nil
	}

	// Check for uncommitted changes that would block checkout
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = ctx.TownRoot
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}

	if strings.TrimSpace(string(out)) != "" {
		return fmt.Errorf("cannot switch to %s: uncommitted changes in town root (stash or commit first)", expected)
	}

	// Switch to the configured default branch.
	cmd = exec.Command("git", "checkout", expected) //nolint:gosec // G204: branch name from git config
	cmd.Dir = ctx.TownRoot
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to checkout %s: %w", expected, err)
	}

	return nil
}
