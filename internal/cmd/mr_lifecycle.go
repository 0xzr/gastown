// Package cmd: mr_lifecycle.go provides shared helpers for stacked-branch
// detection.
//
// Background (gastown-cet.2.3 / hq-try2):
//
//   - A polecat branch with multiple commits ahead of the target creates an MR
//     that advertises only the tip commit. The refinery then cherry-picks the
//     tip without the earlier commits in the stack, producing pre-gate
//     conflicts. The fix: detect stacked branches BEFORE MR creation and
//     reject with actionable remediation (squash or self-contained resubmit).
//
// Source-bead terminal-state classification lives in the beads package
// (internal/beads/mr_lifecycle.go) because the refinery consumes beads issue
// state directly. This file only exports the stacked-branch guard. Both
// `gt done` and `gt mq submit` call CheckStackedBranch here before creating an
// MR.
package cmd

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/git"
)

// StackedBranchInfo describes the result of a stacked-branch check.
//
// Stacked means: there is more than one commit on the submitted branch ahead
// of the merge-base with the target branch. The refinery cherry-picks a
// single commit_sha, so a stacked branch produces a partial diff and breaks
// the MR. The fix is to either squash the branch into a single commit or
// rebase it onto an already-merged base.
type StackedBranchInfo struct {
	// Stacked is true when the branch has more than one commit ahead of the
	// merge-base with the target.
	Stacked bool

	// CommitsAhead is the number of commits on the branch ahead of the
	// merge-base with target. A value > 1 indicates a stacked branch.
	CommitsAhead int

	// MergeBase is the merge-base commit SHA (empty if the branches have not
	// diverged, in which case Stacked is false).
	MergeBase string

	// TipSHA is the branch tip commit SHA at the time of the check.
	TipSHA string
}

// ErrStackedBranch is returned by the submit/done guards when a stacked
// branch is detected. It implements `error` and is the canonical signal that
// the caller should fail fast with actionable remediation.
type ErrStackedBranch struct {
	Branch       string
	Target       string
	CommitsAhead int
	MergeBase    string
	TipSHA       string
}

// Error renders the actionable remediation message. The message is the
// single source of truth for the user-facing "how do I fix this" guidance
// for hq-try2-class failures; tests assert on substrings of this output.
func (e *ErrStackedBranch) Error() string {
	return fmt.Sprintf(
		"refusing to package stacked branch %q: %d commits ahead of %s merge-base %s (tip %s).\n"+
			"Refinery cherry-picks a single commit_sha and the earlier commits will not be included.\n"+
			"Fix one of the following:\n"+
			"  1. Squash the branch into a single commit before resubmitting:\n"+
			"       git fetch origin && git checkout %s && git merge --squash origin/%s\n"+
			"       git commit -m \"<your message>\" && git push origin %s\n"+
			"  2. Or, if the stacked commits intentionally depend on each other,\n"+
			"     split the work so each polecat issue produces a single-commit branch.\n"+
			"After the branch is single-commit, re-run `gt done` (or `gt mq submit`).",
		e.Branch, e.CommitsAhead, e.Target, e.MergeBase, e.TipSHA,
		e.Branch, e.Target, e.Branch,
	)
}

// CheckStackedBranch returns ErrStackedBranch if the branch has more than one
// commit ahead of the merge-base with the target ref. Returns nil if the
// branch is single-commit, identical to the target, or otherwise safe to
// package as a single commit SHA.
//
// targetRef may be a local ref (e.g. "main") or a remote-tracking ref
// (e.g. "origin/main"). The caller is responsible for passing the same
// target that the MR will target; if the MR is targeting an integration
// branch, that ref should be used here.
//
// Detecting via rev-list <merge-base>..<branch> --count ensures we count
// only the commits actually on the submitted branch, ignoring any commits
// the branch shares with target. This matches the refinery's cherry-pick
// semantics: only the tip commit is applied, so any commits that are not
// reachable from the tip alone will be silently dropped (hq-try2).
func CheckStackedBranch(g *git.Git, branch, targetRef string) (*StackedBranchInfo, error) {
	info := &StackedBranchInfo{}

	// Resolve refs so we get concrete SHAs. Refusing to resolve is fatal:
	// we cannot tell a stacked branch from a clean one without the SHAs.
	branchSHA, err := g.Rev("refs/heads/" + branch)
	if err != nil {
		// Try as-is (caller may have passed a fully-qualified ref).
		branchSHA, err = g.Rev(branch)
		if err != nil {
			return nil, fmt.Errorf("resolve branch ref %q: %w", branch, err)
		}
	}
	info.TipSHA = branchSHA

	targetSHA, err := g.Rev(targetRef)
	if err != nil {
		return nil, fmt.Errorf("resolve target ref %q: %w", targetRef, err)
	}

	// If the branch tip is identical to the target, there is nothing to merge.
	if branchSHA == targetSHA {
		return info, nil
	}

	mergeBase, err := g.MergeBase(targetRef, branch)
	if err != nil {
		return nil, fmt.Errorf("compute merge-base of %s and %s: %w", targetRef, branch, err)
	}
	info.MergeBase = mergeBase

	// If the branch is fully merged into target (merge-base == tip), it's
	// a no-op rather than a stacked branch.
	if mergeBase == branchSHA {
		return info, nil
	}

	n, err := g.CommitsAheadOf(mergeBase, branch)
	if err != nil {
		return nil, fmt.Errorf("count commits on branch: %w", err)
	}
	info.CommitsAhead = n
	info.Stacked = n > 1
	if info.Stacked {
		return info, &ErrStackedBranch{
			Branch:       branch,
			Target:       targetRef,
			CommitsAhead: n,
			MergeBase:    mergeBase,
			TipSHA:       branchSHA,
		}
	}
	return info, nil
}

// FormatStackedBranchScopeKeys returns the stacked-branch scope-key lines that
// should be appended to an MR bead description. When the submitted branch is
// stacked, the MR must advertise the merge-base and commit count so the
// refinery can detect a partial-diff replay attempt (gastown-cet.2.3
// acceptance criterion #1). Returns the empty string when there is nothing
// meaningful to record.
func FormatStackedBranchScopeKeys(info *StackedBranchInfo) string {
	var extras string
	if info == nil {
		return extras
	}
	if info.MergeBase != "" {
		extras += fmt.Sprintf("\nbase_sha: %s", info.MergeBase)
	}
	if info.CommitsAhead > 0 {
		extras += fmt.Sprintf("\ncommits_ahead: %d", info.CommitsAhead)
	}
	return extras
}
