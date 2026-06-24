// Package beads: mr_lifecycle.go provides terminal-state classification
// shared between the CLI submit/done guards and the refinery itself.
//
// Background (gastown-cet.2.3 / hq-try2 / hq-6sdu):
//
// The MR bead's beads.Status field (open/in_progress/closed) records the
// refinery's pipeline position, while the CloseReason field (merged/
// rejected/conflict/superseded) records the moment of closure. Neither
// of those answers the question the source bead actually asks: "is my
// merged work durably published to the configured upstream, or only
// sitting in a local file remote that no one is watching?"
//
// ClassifyMRTerminalState answers that question by combining the bead's
// Status and CloseReason with the new PublishedCommit field. The
// returned string is one of:
//
//	pending-refinery             — open or in_progress; nothing terminal yet
//	rejected-needs-rework        — closed for non-merged reasons; reopen source
//	merged-local-not-published   — closed with reason=merged but no PublishedCommit
//	published                    — closed with reason=merged AND PublishedCommit set
//
// The split between "merged" and "published" is the durable fix for
// hq-6sdu: refinery merges that land in a local file remote must not
// look shipped until the configured upstream advances.
package beads

import "strings"

// MR terminal-state values (gastown-cet.2.3, workstream B).
//
// These are constants rather than a typed enum because the values are
// persisted as strings in the MR bead's `terminal_state` field, and
// `beads.Issue` is a generic value type that does not own refinery-side
// types. Centralizing them here keeps submit/done/refinery/status in sync.
const (
	// MRTerminalPendingRefinery — MR is queued/in_progress, not yet merged.
	MRTerminalPendingRefinery = "pending-refinery"

	// MRTerminalRejectedNeedsRework — refinery rejected; source should be
	// reopened as reworkable. Dependents stay blocked.
	MRTerminalRejectedNeedsRework = "rejected-needs-rework"

	// MRTerminalMergedLocalNotPublished — refinery merged to local file
	// remote but no upstream sync yet (hq-6sdu).
	MRTerminalMergedLocalNotPublished = "merged-local-not-published"

	// MRTerminalPublished — merged commit is reachable from the configured
	// upstream target. This is the only state in which the source bead
	// is safe to close and dependents are safe to unblock.
	MRTerminalPublished = "published"
)

// ClassifyMRTerminalState returns the terminal state for an MR issue given
// its current beads Status, CloseReason, and structured fields.
//
// See package doc for the full mapping. Returns "" for unknown states —
// callers should treat that conservatively (e.g. leave the source pending).
func ClassifyMRTerminalState(mrIssue *Issue) string {
	if mrIssue == nil {
		return ""
	}
	fields := ParseMRFields(mrIssue)
	switch mrIssue.Status {
	case "open", "in_progress":
		return MRTerminalPendingRefinery
	case "closed":
		reason := ""
		if fields != nil {
			reason = fields.CloseReason
		}
		if reason == "" {
			reason = extractCloseReasonFromDescription(mrIssue.Description)
		}
		switch reason {
		case "merged":
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
// `published` terminal state — i.e. the merged commit is reachable from
// the configured upstream. This is the only state in which the source
// bead and its dependents are safe to close/unblock.
func IsMRTerminalPublished(mrIssue *Issue) bool {
	return ClassifyMRTerminalState(mrIssue) == MRTerminalPublished
}

// CanCloseSourceBead returns true when the MR is in a state that permits
// the source bead to be closed. Today that means published; future work
// may relax this for `merged-local-not-published` only when paired with
// an explicit operator override.
//
// Pass nil to get a conservative false — callers must always have the MR
// issue at hand.
func CanCloseSourceBead(mrIssue *Issue) bool {
	if mrIssue == nil {
		return false
	}
	return ClassifyMRTerminalState(mrIssue) == MRTerminalPublished
}

// extractCloseReasonFromDescription is a tolerant fallback for legacy
// MRs whose close reason was written as prose instead of a structured
// field. Matches "close_reason: <value>" anywhere on a single line.
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
