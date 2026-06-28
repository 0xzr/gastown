package cmd

import (
	"path/filepath"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
)

// postSubmitEmptyHookState describes a polecat whose hook is empty but whose
// previous work has already been submitted to the Refinery as an open MR.
// This is distinct from a fresh no-work session: running gt done again would
// be rejected with empty_hook_no_evidence, so the startup protocol must tell
// the polecat to stand down instead.
type postSubmitEmptyHookState struct {
	AgentBeadID   string
	ActiveMR      string
	MRStatus      string
	SourceIssue   string
	Branch        string
	WorktreeClean bool
	OnMain        bool
}

// gitWorktreeChecker is the subset of git.Git needed for post-submit detection.
//
// It deliberately exposes only LOCAL git operations. The stand-down directive
// is rendered whenever a polecat has an open MR and an empty hook — it must not
// hang on remote/network git state merely to print corroboration. Reaching
// CheckUncommittedWork here transitively ran an unbounded `git ls-remote`
// (UnpushedCommits -> RemoteBranchTip), which could hang gt prime after
// submit (gastown-t7l). Status() and CurrentBranch() read only the local
// worktree and fail closed on error.
type gitWorktreeChecker interface {
	Status() (*git.GitStatus, error)
	CurrentBranch() (string, error)
}

// detectPostSubmitEmptyHook reports whether a polecat session has no hooked
// work but still has an open merge request in flight from a previous gt done.
// When true, gt prime must not instruct the agent to run gt done again.
func detectPostSubmitEmptyHook(ctx RoleContext) (*postSubmitEmptyHookState, bool) {
	return detectPostSubmitEmptyHookWithReaders(ctx,
		beads.New(ctx.WorkDir).ForAgentBead(),
		beads.New(rigBeadsRoot(ctx)),
		git.NewGit(ctx.WorkDir),
	)
}

// detectPostSubmitEmptyHookWithReaders is the testable implementation of
// detectPostSubmitEmptyHook. It accepts interfaces for beads and git access so
// unit tests do not need a live Dolt server or full git repository.
func detectPostSubmitEmptyHookWithReaders(ctx RoleContext, agentBD issueShower, mrBD issueShower, g gitWorktreeChecker) (*postSubmitEmptyHookState, bool) {
	if ctx.Role != RolePolecat {
		return nil, false
	}
	if ctx.TownRoot == "" || ctx.Rig == "" || ctx.Polecat == "" {
		return nil, false
	}

	agentBeadID := getAgentBeadID(ctx)
	if agentBeadID == "" {
		return nil, false
	}

	// Agent beads live in the town DB regardless of rig prefix.
	agentBead, err := agentBD.Show(agentBeadID)
	if err != nil || agentBead == nil {
		return nil, false
	}

	fields := beads.ParseAgentFields(agentBead.Description)
	if fields == nil {
		return nil, false
	}

	// The key discriminator is an active MR that is still live in the merge
	// queue. Terminal MRs (merged, rejected, superseded) mean the Refinery is
	// done with this work; a stale active_mr should be cleaned up by the
	// Witness reaper, not by re-running gt done.
	activeMR := fields.ActiveMR
	if activeMR == "" {
		return nil, false
	}

	state := &postSubmitEmptyHookState{
		AgentBeadID: agentBeadID,
		ActiveMR:    activeMR,
		SourceIssue: fields.LastSourceIssue,
		Branch:      fields.Branch,
	}

	mr, err := mrBD.Show(activeMR)
	if err != nil || mr == nil {
		// Active MR is set but cannot be read. Conservatively treat as in-flight
		// so we do not tell the agent to run gt done and risk rejection.
		state.MRStatus = "unknown"
		state.WorktreeClean, state.OnMain = postSubmitGitStateWithChecker(ctx, g)
		return state, true
	}

	if beads.IssueStatus(mr.Status).IsTerminal() {
		return nil, false
	}

	state.MRStatus = mr.Status
	state.WorktreeClean, state.OnMain = postSubmitGitStateWithChecker(ctx, g)
	return state, true
}

// postSubmitGitState returns whether the polecat worktree is clean and whether
// it is on the default branch. These are corroborating signals for the
// post-submit empty-hook state.
func postSubmitGitState(ctx RoleContext) (clean bool, onMain bool) {
	return postSubmitGitStateWithChecker(ctx, git.NewGit(ctx.WorkDir))
}

// postSubmitGitStateWithChecker is the testable implementation of
// postSubmitGitState.
func postSubmitGitStateWithChecker(ctx RoleContext, g gitWorktreeChecker) (clean bool, onMain bool) {
	// Local-only: git status --porcelain. Never touches the remote, so a
	// post-submit gt prime cannot hang on network git state while rendering a
	// stand-down directive (gastown-t7l). Fail closed (not clean) on error.
	if status, err := g.Status(); err == nil {
		clean = status.Clean || status.CleanExcludingRuntime()
	}

	defaultBranch := postSubmitDefaultBranch(ctx)
	branch, err := g.CurrentBranch()
	if err == nil {
		// Empty branch name means a detached HEAD, which is the expected state
		// after gt done syncs the worktree to origin/main.
		onMain = branch == defaultBranch || branch == "master" || branch == ""
	}

	return clean, onMain
}

// postSubmitDefaultBranch returns the configured default branch for the rig.
func postSubmitDefaultBranch(ctx RoleContext) string {
	defaultBranch := "main"
	if ctx.Rig != "" && ctx.TownRoot != "" {
		if cfg, err := rig.LoadRigConfig(filepath.Join(ctx.TownRoot, ctx.Rig)); err == nil && cfg.DefaultBranch != "" {
			defaultBranch = cfg.DefaultBranch
		}
	}
	return defaultBranch
}
