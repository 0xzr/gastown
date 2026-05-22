package polecat

import (
	"errors"

	"github.com/steveyegge/gastown/internal/beads"
)

// IssueShower is the subset of beads lookup needed to classify active MRs.
type IssueShower interface {
	Show(issueID string) (*beads.Issue, error)
}

// ActiveMRBlocksReuse reports whether active_mr still represents submitted work
// that should be preserved. Missing/terminal MRs no longer block; lookup errors
// fail closed.
func ActiveMRBlocksReuse(bd IssueShower, mrID string) bool {
	if mrID == "" {
		return false
	}
	if bd == nil {
		return true
	}
	mr, err := bd.Show(mrID)
	if err != nil {
		return !errors.Is(err, beads.ErrNotFound)
	}
	if mr == nil {
		return false
	}
	return !beads.IssueStatus(mr.Status).IsTerminal()
}

// WorkstateInputFromAgentFields maps durable agent bead fields into the
// canonical evaluator input. Callers add git/MQ observations separately.
func WorkstateInputFromAgentFields(state State, hookBead string, fields *beads.AgentFields, activeMRBlocks bool) WorkstateInput {
	input := WorkstateInput{State: state, CleanupStatus: CleanupUnknown}
	if fields == nil {
		return input
	}
	if hookBead == "" {
		hookBead = fields.HookBead
	}
	input.HookBead = hookBead
	input.ActiveMR = fields.ActiveMR
	input.ActiveMRBlocks = activeMRBlocks
	input.PushFailed = fields.PushFailed
	input.MRFailed = fields.MRFailed
	if fields.CleanupStatus != "" {
		input.CleanupStatus = CleanupStatus(fields.CleanupStatus)
	}
	return input
}
