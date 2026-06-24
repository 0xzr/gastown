// Package cmd: mr_lifecycle.go provides shared helpers for stacked-branch
// detection and source-bead terminal-state classification.
//
// Background (gastown-cet.2.3 / hq-try2 / hq-6sdu):
//
//   - A polecat branch with multiple commits ahead of the target creates an MR
//     that advertises only the tip commit. The refinery then cherry-picks the
//     tip without the earlier commits in the stack, producing pre-gate
//     conflicts. The fix: detect stacked branches BEFORE MR creation and
//     reject with actionable remediation (squash or self-contained resubmit).
//
//   - A source bead must not be closed while its MR is still pending, rejected,
//     deferred, or merged locally but not externally published. The MR's
//     terminal state distinguishes four values:
//     pending-refinery            - MR is queued/in_progress, not yet merged
//     rejected-needs-rework       - Refinery rejected; source should be
//     reopened as reworkable
//     merged-local-not-published  - Refinery merged to local file remote
//     but no upstream sync yet (hq-6sdu)
//     published                   - Source shipped to upstream (terminal)
//
// This file is the single source of truth for those checks. Both `gt done`
// and `gt mq submit` call the helpers here before creating or closing MRs.
package cmd

import (
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
)

// MR terminal-state constants (gastown-cet.2.3, workstream B).
//
// These are stored as the `terminal_state` field on the MR bead. They are
// distinct from beads `Status` (open/in_progress/closed) and from `CloseReason`
// (merged/rejected/conflict/superseded) — those record the moment of closure,
// while terminal_state records the *observable lifecycle position* from the
// outside. Source beads consult terminal_state to decide whether their work is
// safe to close.
const (
	// MRTerminalPendingRefinery means the MR is queued or in progress and
	// has not reached a terminal outcome yet. Source beads must remain
	// pending and dependents must remain blocked while this is the state.
	MRTerminalPendingRefinery = "pending-refinery"

	// MRTerminalRejectedNeedsRework means the refinery rejected the MR and
	// the source issue should be reopened in a reworkable state with the
	// reviewer packet retained as evidence. Dependents stay blocked.
	MRTerminalRejectedNeedsRework = "rejected-needs-rework"

	// MRTerminalMergedLocalNotPublished means the refinery merged the branch
	// to the local file remote but the configured upstream (e.g. GitHub
	// origin/main) has not yet advanced to the merged commit. Until the
	// upstream matches, source beads must NOT be considered externally
	// shipped (hq-6sdu).
	MRTerminalMergedLocalNotPublished = "merged-local-not-published"

	// MRTerminalPublished means the merged commit is reachable from the
	// configured upstream target. This is the only state that allows the
	// source bead to close and dependents to unblock.
	MRTerminalPublished = "published"
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

// ClassifyMRTerminalState returns the terminal state for an MR issue given
// its current beads Status, CloseReason, and structured fields.
//
// The classification rules are:
//
//	beads.Status=open   → pending-refinery (queued, not yet claimed)
//	beads.Status=in_progress → pending-refinery (claimed, mid-flight)
//	beads.Status=closed + CloseReason in {conflict, ""} → rejected-needs-rework
//	beads.Status=closed + CloseReason=merged + PublishedCommit empty → merged-local-not-published
//	beads.Status=closed + CloseReason=merged + PublishedCommit set  → published
//	beads.Status=closed + CloseReason=superseded → rejected-needs-rework
//	                                                       (the source needs a new MR,
//	                                                        not a fresh issue)
//	beads.Status=closed + CloseReason=rejected → rejected-needs-rework
//
// Returns "" when the MR is in an unknown state — callers should treat that
// conservatively (e.g. leave source pending).
func ClassifyMRTerminalState(mrIssue *beads.Issue) string {
	if mrIssue == nil {
		return ""
	}
	fields := beads.ParseMRFields(mrIssue)
	switch mrIssue.Status {
	case "open", "in_progress":
		return MRTerminalPendingRefinery
	case "closed":
		// Inspect CloseReason from structured fields first, fall back to
		// the description text. Some legacy MRs store the reason inline.
		reason := ""
		if fields != nil {
			reason = fields.CloseReason
		}
		if reason == "" {
			reason = extractCloseReasonFromDescription(mrIssue.Description)
		}
		switch reason {
		case "merged":
			// Distinguish local-only merge from upstream-published.
			if fields != nil && fields.PublishedCommit != "" {
				return MRTerminalPublished
			}
			return MRTerminalMergedLocalNotPublished
		case "rejected", "conflict", "superseded":
			return MRTerminalRejectedNeedsRework
		default:
			// Unknown close reason — treat as needs-rework so the source
			// is not silently closed.
			return MRTerminalRejectedNeedsRework
		}
	}
	return ""
}

// IsMRTerminalPublished returns true when the MR has reached the
// `published` terminal state — i.e. the merged commit is reachable from the
// configured upstream. This is the only state in which the source bead and
// its dependents are safe to close/unblock.
//
// Other terminal states (rejected-needs-rework, merged-local-not-published,
// pending-refinery) all return false. Callers that want a strict "is the
// MR done enough to ship" predicate should use this helper.
func IsMRTerminalPublished(mrIssue *beads.Issue) bool {
	return ClassifyMRTerminalState(mrIssue) == MRTerminalPublished
}

// CanCloseSourceBead returns true when the MR is in a state that permits the
// source bead to be closed. Today that means published; future work may
// relax this for `merged-local-not-published` only when paired with an
// explicit operator override.
//
// Pass nil to get a conservative false — callers must always have the MR
// issue at hand.
func CanCloseSourceBead(mrIssue *beads.Issue) bool {
	if mrIssue == nil {
		return false
	}
	return ClassifyMRTerminalState(mrIssue) == MRTerminalPublished
}

// extractCloseReasonFromDescription is a tolerant fallback for legacy MRs
// whose close reason was written as prose instead of a structured field.
// Matches "close_reason: <value>" anywhere on a single line.
func extractCloseReasonFromDescription(desc string) string {
	for _, line := range strings.Split(desc, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "close_reason:") {
			return strings.TrimSpace(line[len("close_reason:"):])
		}
		if strings.HasPrefix(lower, "close-reason:") {
			return strings.TrimSpace(line[len("close-reason:"):])
		}
		if strings.HasPrefix(lower, "closereason:") {
			return strings.TrimSpace(line[len("closereason:"):])
		}
	}
	return ""
}
