package polecat

// Recovery guard predicates for polecat cleanup.
//
// EvaluateRecoveryGuard mirrors the read-only safety predicate used by the
// external polecat recovery-guard script. The in-source verdict is exposed
// via gt polecat check-recovery so callers (and tests) can reason about
// post-merge detached-HEAD worktrees without consulting the external
// script.
//
// The post-merge exception (PostMergeCleared) is the only narrowing of the
// external predicate, and it is intentionally narrow: dirty working trees,
// unpreserved commits ahead of the comparison ref, branch stashes, and
// hooked issues still block unconditionally. The exception fires only when
//   - the active MR is closed (status closed / merged / superseded / etc.),
//   - the MR's source issue is terminal,
//   - the MR is not pending a live decision,
//   - the worktree is clean (no uncommitted changes),
//   - there are no unpushed/unpreserved commits (ahead == 0),
//   - there are no branch-owned stashes, and
//   - no issue is currently hooked to the polecat.
//
// Without those guarantees the predicate must remain conservative: a
// detached HEAD with even one unaccounted-for file difference, one unpushed
// commit, or one open hook can represent unsubmitted work that nuke would
// discard.

import "strings"

// RecoveryGuardInput captures the read-only facts the predicate evaluates.
// Field names mirror the JSON the external recovery-guard script emits so
// downstream tooling can compare the two sources side-by-side.
type RecoveryGuardInput struct {
	// WorktreeFound is true when the polecat's worktree directory was located.
	// A missing worktree cannot be evaluated safely.
	WorktreeFound bool
	// Branch is the symbolic branch name from `git rev-parse --abbrev-ref HEAD`.
	// "HEAD" indicates a detached HEAD state.
	Branch string
	// CompareRef is the resolved comparison ref (e.g. "origin/main").
	CompareRef string
	// Ahead is the number of commits HEAD is ahead of CompareRef.
	Ahead int
	// Dirty is the number of uncommitted entries in `git status --porcelain`.
	Dirty int
	// TreeDiff is 1 when `git diff CompareRef HEAD --` reports any file
	// difference, otherwise 0. A non-zero TreeDiff covers everything from
	// runtime artifacts to genuine uncommitted code.
	TreeDiff int
	// BranchStashes is the count of stashes tagged with the current branch.
	BranchStashes int
	// StatusIssue is the issue currently hooked to the polecat, if any.
	StatusIssue string
	// ActiveMRStatus is the status field of the active merge-request bead
	// (e.g. "closed", "open", "merged").
	ActiveMRStatus string
	// ActiveMRSourceTerminal reports whether the MR's source issue reached a
	// terminal state. Closed MRs whose source is still open are rework
	// cases (rejected/conflict/superseded) and handled separately.
	ActiveMRSourceTerminal bool
	// ActiveMRPending is true when the MR still represents live merge-queue
	// work that must not be discarded.
	ActiveMRPending bool
}

// RecoveryGuardResult is the verdict emitted by EvaluateRecoveryGuard. The
// shape mirrors the JSON returned by the external recovery-guard script so
// the two can be diffed.
type RecoveryGuardResult struct {
	Block                 bool     `json:"block"`
	Verdict               string   `json:"verdict"`
	Reasons               []string `json:"reasons"`
	TreeMatchesCompareRef bool     `json:"tree_matches_compare_ref"`
}

// Recovery-guard verdict constants. Exposed so tests and callers can compare
// against stable strings rather than literals.
const (
	RecoveryGuardVerdictNeedsRecovery = "NEEDS_RECOVERY"
	RecoveryGuardVerdictClear         = "CLEAR"
)

// Post-merge exception reason strings. The external script uses opaque
// identifiers; we keep them aligned so downstream tooling sees a consistent
// vocabulary.
const (
	ReasonDetachedOrUnknownBranch     = "detached_or_unknown_branch"
	ReasonBranchNotMain               = "branch_not_main"             // suffix :<branch>
	ReasonDirtyWorktree               = "dirty_worktree"              // suffix :<count>
	ReasonAheadOfCompareRef           = "ahead_of_compare_ref"        // suffix :<count>
	ReasonMissingOriginMain           = "missing_origin_main"
	ReasonNoMergeQueueRecordForBranch = "no_merge_queue_record_for_branch"
	ReasonMergeQueueRecordUnavailable = "merge_queue_record_unavailable"
	ReasonBranchStashes               = "branch_stashes"              // suffix :<count>
	ReasonHookedIssue                 = "hooked_issue"                // suffix :<issue>
)

// EvaluateRecoveryGuard returns the guard verdict for a polecat. A missing
// worktree, dirty tree, unpushed commits, branch stashes, and a hooked issue
// always block; the post-merge exception only relaxes the detached-HEAD and
// merge-queue predicates when the active MR is provably terminal and no
// other work is at risk.
func EvaluateRecoveryGuard(in RecoveryGuardInput) RecoveryGuardResult {
	reasons := make([]string, 0, 4)

	addReason := func(reason string) {
		reasons = append(reasons, reason)
	}

	// A missing worktree cannot be evaluated; stay conservative.
	if !in.WorktreeFound {
		addReason("missing_worktree")
		return finalizeGuard(true, reasons, in.TreeDiff)
	}

	postMergeCleared := isPostMergeCleared(in)

	// detached_or_unknown_branch: detached HEAD or empty branch name. Skipped
	// when the post-merge exception holds, because the detached state is the
	// expected outcome of a post-merge worktree.
	if in.Branch == "" || strings.EqualFold(in.Branch, "HEAD") {
		if !postMergeCleared {
			addReason(ReasonDetachedOrUnknownBranch)
		}
	}

	// dirty_worktree: porcelain count > 0. This is non-negotiable — nuke
	// would discard real uncommitted work.
	if in.Dirty > 0 {
		addReason(reasonWithCount(ReasonDirtyWorktree, in.Dirty))
	}

	// missing_origin_main: no comparison ref to evaluate against. The
	// external script adds this whenever origin/main and origin/master both
	// fail to resolve. With no compare ref we cannot decide ahead/tree_diff
	// safely, so block.
	if strings.TrimSpace(in.CompareRef) == "" {
		addReason(ReasonMissingOriginMain)
	} else {
		// ahead_of_compare_ref: HEAD is ahead of the resolved compare ref.
		// Even when the MR is closed, ahead > 0 means local commits exist
		// that have not landed — do not let the post-merge exception swallow
		// those.
		if in.Ahead > 0 {
			if in.TreeDiff > 0 {
				addReason(reasonWithCount(ReasonAheadOfCompareRef, in.Ahead))
			}
		}
	}

	// branch_not_main:<branch> + merge-queue record checks: only evaluated
	// for non-default branches. We treat detached HEAD as non-main for this
	// purpose (it is, in fact, not main), so the check still applies.
	if in.Branch != "" && !isRecoveryGuardBaseBranch(in.Branch) {
		if in.TreeDiff > 0 || in.Dirty > 0 {
			if !postMergeCleared {
				addReason(branchNotMainReason(in.Branch))
			}
		}
		// The MQ record check is structurally similar to the external
		// script's behavior: only inspect MQ when there is something to
		// compare. When the worktree has no tree_diff and no dirty entries,
		// the external script skips the MQ check entirely — we mirror that.
		if (in.TreeDiff > 0 || in.Dirty > 0) && !postMergeCleared {
			// The external script reports merge_queue_record_unavailable when
			// the MQ list returns no JSON, and no_merge_queue_record_for_branch
			// when the list returned but did not contain this polecat's
			// branch/target. Callers surface the appropriate variant via
			// ActiveMRPending; we treat an unknown MQ state as unavailable
			// unless the in-source MR assessment proves it terminal.
			if !in.ActiveMRSourceTerminal || in.ActiveMRPending {
				addReason(ReasonMergeQueueRecordUnavailable)
			}
		}
	}

	// branch_stashes:<n> — stashes tag the current branch and would be lost.
	if in.BranchStashes > 0 {
		addReason(reasonWithCount(ReasonBranchStashes, in.BranchStashes))
	}

	// hooked_issue:<issue> — polecat still owns work; do not discard.
	if in.StatusIssue != "" {
		addReason(reasonWithCount(ReasonHookedIssue, 0))
		reasons[len(reasons)-1] = hookedIssueReason(in.StatusIssue)
	}

	return finalizeGuard(len(reasons) > 0, reasons, in.TreeDiff)
}

// isPostMergeCleared reports whether the input satisfies the narrow
// post-merge exception: the active MR is provably terminal, no work is
// hooked to the polecat, the worktree is clean, no unpushed commits remain,
// and no branch-owned stashes exist.
func isPostMergeCleared(in RecoveryGuardInput) bool {
	if !in.WorktreeFound {
		return false
	}
	// MR must be terminal at both the MR-status and source-issue levels.
	// Pending=true means the external script's MQ check found something
	// live; ActiveMRSourceTerminal guards against rework MRs whose source
	// stays open by design.
	if in.ActiveMRPending {
		return false
	}
	if !in.ActiveMRSourceTerminal {
		return false
	}
	// WIP protections: any of these means real work exists.
	if in.Dirty > 0 || in.Ahead > 0 || in.BranchStashes > 0 || in.StatusIssue != "" {
		return false
	}
	return true
}

// finalizeGuard packages the verdict into a RecoveryGuardResult, mapping
// reasons to block/clear and normalizing TreeMatchesCompareRef.
func finalizeGuard(block bool, reasons []string, treeDiff int) RecoveryGuardResult {
	if reasons == nil {
		reasons = []string{}
	}
	verdict := RecoveryGuardVerdictClear
	if block {
		verdict = RecoveryGuardVerdictNeedsRecovery
	}
	return RecoveryGuardResult{
		Block:                 block,
		Verdict:               verdict,
		Reasons:               reasons,
		TreeMatchesCompareRef: treeDiff == 0,
	}
}

// reasonWithCount formats "<reason>:<n>" matching the external script's
// reason vocabulary (e.g. "ahead_of_origin_main:3").
func reasonWithCount(reason string, count int) string {
	return reason + ":" + itoa(count)
}

// branchNotMainReason formats the "branch_not_main:<branch>" reason. The
// external script emits the literal branch name (which can be "HEAD" for
// detached HEADs); we preserve that verbatim so callers see the same
// identifier regardless of source.
func branchNotMainReason(branch string) string {
	return ReasonBranchNotMain + ":" + branch
}

// hookedIssueReason formats the "hooked_issue:<issue>" reason.
func hookedIssueReason(issue string) string {
	return ReasonHookedIssue + ":" + issue
}

// isRecoveryGuardBaseBranch mirrors cmd.isRecoveryBaseBranch: the only
// branches exempt from the branch_not_main / MQ-record checks are main,
// master, and integration/* (which the recovery base classifier treats as a
// default-branch proxy).
func isRecoveryGuardBaseBranch(branch string) bool {
	if branch == "main" || branch == "master" {
		return true
	}
	return strings.HasPrefix(branch, "integration/")
}