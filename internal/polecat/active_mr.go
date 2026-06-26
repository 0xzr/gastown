package polecat

import (
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// IssueReader is the subset of beads lookup needed to classify active_mr.
type IssueReader interface {
	Show(issueID string) (*beads.Issue, error)
}

// ActiveMRInput describes the active merge-request context for a polecat.
type ActiveMRInput struct {
	ActiveMR        string
	SourceIssueHint string
	RequireGitSafe  bool
	GitSafe         bool
}

// ActiveMRAssessment is the shared active_mr classification used by recovery,
// reuse, and witness paths. Pending is fail-closed: lookup/source uncertainty
// remains blocking unless the stale MR and terminal source are both proven.
//
// The exception is a terminal MR closed for rework (rejected, conflict, or
// superseded): such an MR is no longer live in the merge queue, so the slot
// must be recyclable for rework even though the source issue stays open
// (hq-yyz / hq-6na). Reconcilable signals that a stale active_mr can be
// safely cleared so capacity is not blocked by dead merge-queue work.
type ActiveMRAssessment struct {
	ActiveMR       string
	Pending        bool
	Reason         string
	MRStatus       string
	SourceIssue    string
	SourceTerminal bool
	Stale          bool
	CloseReason    string
	Reconcilable   bool
}

// AssessActiveMR returns whether active_mr still represents work pending in the
// merge queue. Missing/terminal MRs are stale only when the source issue is
// known terminal and, if requested, direct git state is safe.
func AssessActiveMR(reader IssueReader, in ActiveMRInput) ActiveMRAssessment {
	mrID := strings.TrimSpace(in.ActiveMR)
	if mrID == "" {
		return ActiveMRAssessment{}
	}
	result := ActiveMRAssessment{ActiveMR: mrID, Pending: true}
	if reader == nil {
		result.Reason = fmt.Sprintf("active_mr=%s status=unverified", mrID)
		return result
	}

	mr, err := reader.Show(mrID)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) {
			return assessStaleActiveMR(reader, in, result, "missing", nil)
		}
		result.Reason = fmt.Sprintf("active_mr=%s status=lookup_error: %v", mrID, err)
		return result
	}
	if mr == nil {
		return assessStaleActiveMR(reader, in, result, "missing", nil)
	}

	result.MRStatus = mr.Status
	if !beads.IssueStatus(mr.Status).IsTerminal() {
		result.Reason = fmt.Sprintf("active_mr=%s status=%s", mrID, mr.Status)
		return result
	}
	return assessStaleActiveMR(reader, in, result, mr.Status, mr)
}

func assessStaleActiveMR(reader IssueReader, in ActiveMRInput, result ActiveMRAssessment, mrStatus string, mr *beads.Issue) ActiveMRAssessment {
	result.MRStatus = mrStatus
	result.Stale = true
	closeReason := mrCloseReason(mr)
	result.CloseReason = closeReason
	sourceIssue := sourceIssueForActiveMR(in.SourceIssueHint, mr)
	result.SourceIssue = sourceIssue

	// A terminal MR closed for rework (rejected, conflict, or superseded) is no
	// longer live in the merge queue. Its source issue stays open on purpose so
	// the polecat can redo the work, but that open source must not keep the slot
	// blocked on a dead MR (hq-yyz / hq-6na). The active_mr is reconcilable:
	// safe to clear once git state confirms no live work is at risk.
	if isReworkCloseReason(closeReason) {
		result.Reconcilable = true
		if in.RequireGitSafe && !in.GitSafe {
			result.Reason = fmt.Sprintf("active_mr=%s status=%s close_reason=%s source_issue=%s git_state=unsafe", result.ActiveMR, mrStatus, closeReason, sourceIssue)
			return result
		}
		result.Pending = false
		result.Reason = ""
		return result
	}

	// gastown-p3w: a stale active_mr that points at an MR bead no longer present
	// in beads (status=missing, mr=nil) is the Jasper-like case. The MR has been
	// removed from the merge queue entirely (manual close, restart, or
	// housekeeping), so there is no live merge-queue work to wait on. When the
	// source issue is still open, the slot is recyclable for the next rework
	// dispatch the same way an MR closed with close_reason=rejected/conflict/
	// superseded is. The slot must not be left as a generic NEEDS_RECOVERY
	// capacity hold with no deterministic next action.
	if mrStatus == "missing" {
		// gastown-p3w: a stale active_mr that points at an MR bead no longer
		// present in beads (status=missing, mr=nil) is the Jasper-like case.
		// The MR has been removed from the merge queue entirely (manual
		// close, restart, or housekeeping), so there is no live merge-queue
		// work to wait on. We only classify the slot as rework-reconcilable
		// when we have positive evidence the source issue is verified
		// open — an unknown or terminal source keeps the fail-closed
		// verdict so we never silently recycle a slot whose work is in
		// question.
		if openStatus := sourceIssueOpenStatus(reader, sourceIssue); openStatus != "" {
			result.Reconcilable = true
			if in.RequireGitSafe && !in.GitSafe {
				result.Reason = fmt.Sprintf("active_mr=%s status=missing source_issue=%s git_state=unsafe", result.ActiveMR, sourceIssue)
				return result
			}
			result.Pending = false
			result.Reason = fmt.Sprintf("active_mr=%s status=missing source_issue=%s source_status=%s (rework-reconcilable)", result.ActiveMR, sourceIssue, openStatus)
			return result
		}
		// Source is unknown, missing, terminal, or unverified — fall
		// through to the source-terminal gate. terminalSourceIssue will
		// produce a Pending=true verdict with a precise blocker reason
		// ("source_status=missing" / "source_issue=<missing>" / etc.) so
		// the slot can be escalated to Mayor with actionable evidence
		// instead of being silently recycled.
		terminal, reason := terminalSourceIssue(reader, sourceIssue)
		result.SourceTerminal = terminal
		if !terminal {
			result.Reason = fmt.Sprintf("active_mr=%s status=missing %s", result.ActiveMR, reason)
			return result
		}
		if in.RequireGitSafe && !in.GitSafe {
			result.Reason = fmt.Sprintf("active_mr=%s status=missing source_issue=%s git_state=unsafe", result.ActiveMR, sourceIssue)
			return result
		}
		result.Pending = false
		result.Reason = ""
		return result
	}

	terminal, reason := terminalSourceIssue(reader, sourceIssue)
	result.SourceTerminal = terminal
	if !terminal {
		result.Reason = fmt.Sprintf("active_mr=%s status=%s %s", result.ActiveMR, mrStatus, reason)
		return result
	}
	if in.RequireGitSafe && !in.GitSafe {
		result.Reason = fmt.Sprintf("active_mr=%s status=%s source_issue=%s git_state=unsafe", result.ActiveMR, mrStatus, sourceIssue)
		return result
	}
	result.Pending = false
	result.Reason = ""
	return result
}

// mrCloseReason extracts the close_reason from a terminal MR bead's description,
// falling back to the bead's own close metadata when MR fields are absent.
func mrCloseReason(mr *beads.Issue) string {
	if mr == nil {
		return ""
	}
	if fields := beads.ParseMRFields(mr); fields != nil && fields.CloseReason != "" {
		return fields.CloseReason
	}
	return ""
}

// isReworkCloseReason reports whether a close reason means the MR died short of
// a durable merge and the source issue is expected to stay open for rework.
// "merged" is NOT a rework reason: PostMerge closes the source, so source
// terminality is the correct gate for merged MRs.
func isReworkCloseReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "rejected", "conflict", "superseded":
		return true
	default:
		return false
	}
}

func sourceIssueForActiveMR(hint string, mr *beads.Issue) string {
	if mr != nil {
		if fields := beads.ParseMRFields(mr); fields != nil {
			if source := normalizeSourceIssue(fields.SourceIssue); source != "" {
				return source
			}
		}
	}
	return normalizeSourceIssue(hint)
}

func normalizeSourceIssue(source string) string {
	source = strings.TrimSpace(source)
	if strings.EqualFold(source, "null") {
		return ""
	}
	return source
}

func terminalSourceIssue(reader IssueReader, sourceIssue string) (bool, string) {
	if sourceIssue == "" {
		return false, "source_issue=<missing>"
	}
	if reader == nil {
		return false, fmt.Sprintf("source_issue=%s source_status=unverified", sourceIssue)
	}
	issue, err := reader.Show(sourceIssue)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) {
			return false, fmt.Sprintf("source_issue=%s source_status=missing", sourceIssue)
		}
		return false, fmt.Sprintf("source_issue=%s source_status=lookup_error: %v", sourceIssue, err)
	}
	if issue == nil {
		return false, fmt.Sprintf("source_issue=%s source_status=missing", sourceIssue)
	}
	if beads.IssueStatus(issue.Status).IsTerminal() {
		return true, ""
	}
	return false, fmt.Sprintf("source_issue=%s source_status=%s", sourceIssue, issue.Status)
}

// sourceIssueOpenStatus reports whether the source issue is verified open
// (looked up, present, and in a non-terminal state). Returns "" when the
// source is empty, missing, or unverified. Used by the missing-MR path to
// gate the rework-reconcilable classification on positive evidence that the
// polecat still owns an open rework assignment.
func sourceIssueOpenStatus(reader IssueReader, sourceIssue string) string {
	if sourceIssue == "" {
		return ""
	}
	if reader == nil {
		return ""
	}
	issue, err := reader.Show(sourceIssue)
	if err != nil || issue == nil {
		return ""
	}
	if beads.IssueStatus(issue.Status).IsTerminal() {
		return ""
	}
	return issue.Status
}
