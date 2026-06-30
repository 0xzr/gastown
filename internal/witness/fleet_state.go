package witness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// FleetState summarizes polecat fleet composition for a single rig.
//
// Composition is the witness's authority on what the fleet is doing. The four
// buckets are mutually exclusive per polecat (a polecat is classified by its
// strongest signal) and together cover every polecat the witness encounters:
//
//   - ActiveImplementation: polecat has an assigned hooked work bead (or a
//     live session with assigned work in flight). The "is the agent busy?"
//     signal. Source of truth: the work bead's own status=hooked +
//     assignee=<polecat>, falling back to legacy agent-bead HookBead only
//     when it resolves to a currently hooked bead (gastown-72v).
//   - PostSubmitGate: polecat's session is gone or post-submit and the merge
//     queue has an OPEN merge-request bead pointing at it. The MR gate
//     detection MUST include ephemeral wisps, because gt mq submit creates
//     MRs as wisps — b.List() alone misses them (gastown-72v).
//   - RecoveryHeld: polecat is in a stale recovery lane (work held pending
//     recovery decision) but not actively implementing and not gated by an
//     MR. Distinct from PostSubmitGate so witness/mayor can tell "draining
//     through refinery" from "stuck, needs recovery."
//   - Idle: polecat completed work and is ready for reuse; no hook, no MR.
//
// IsEmpty is true only when ActiveImplementation, PostSubmitGate, and
// RecoveryHeld are all empty AND there are no polecat sessions at all. A
// live session with a refinery gate in front of it is NOT fleet-empty —
// that is the gastown-72v misclassification we are preventing.
type FleetState struct {
	Rig                  string
	ActiveImplementation []string
	PostSubmitGate       []string
	RecoveryHeld         []string
	Idle                 []string
	// IsEmpty is true only when no polecats are present and no implementation,
	// gate, or recovery-held signals are observed. A live MR gate is the
	// canonical non-empty signal even when all sessions are gone.
	IsEmpty bool
}

// HasActiveWork reports whether the fleet has any signal that is not
// "truly empty" — i.e. an implementing session, an open MR gate, or a
// recovery-held lane. This is the answer to "is the fleet doing something?"
// that witness/mayor summaries should report.
func (f *FleetState) HasActiveWork() bool {
	if f == nil {
		return false
	}
	return !f.IsEmpty
}

// PolecatBeadSource is the minimal surface needed to enumerate work beads
// for a polecat. It is satisfied by *beads.Beads in production and by test
// fakes in unit tests; declaring it here keeps DetectFleetState testable
// without spinning up Dolt.
type PolecatBeadSource interface {
	List(opts beads.ListOptions) ([]*beads.Issue, error)
	ListByAssignee(assignee string) ([]*beads.Issue, error)
	// ListMergeRequests returns open merge-request beads from BOTH the issues
	// and the wisps tables. Callers must use this rather than List, because
	// MRs created by gt mq submit are ephemeral wisps and List would miss
	// them entirely (gastown-72v finding).
	ListMergeRequests(opts beads.ListOptions) ([]*beads.Issue, error)
	Show(id string) (*beads.Issue, error)
}

// BdCliPolecatBeadSource adapts a *BdCli to PolecatBeadSource. Each call
// shells out to the bd CLI with the work directory as cwd, so production
// callers (e.g. gt patrol scan) can drive DetectFleetState through the same
// dependency-injection seam the witness handlers use, without dragging a
// Dolt-backed *beads.Beads into every test path.
//
// Errors from bd are surfaced verbatim so DetectFleetState's error-handling
// classification remains accurate.
type BdCliPolecatBeadSource struct {
	Cli     *BdCli
	WorkDir string
}

// List shells out to `bd list --json` with the provided options.
func (b *BdCliPolecatBeadSource) List(opts beads.ListOptions) ([]*beads.Issue, error) {
	return bdListIssues(b.Cli, b.WorkDir, opts)
}

// ListByAssignee shells out to `bd list --assignee=X --json`.
func (b *BdCliPolecatBeadSource) ListByAssignee(assignee string) ([]*beads.Issue, error) {
	opts := beads.ListOptions{Status: "all", Assignee: assignee}
	return bdListIssues(b.Cli, b.WorkDir, opts)
}

// ListMergeRequests shells out to `bd list --label=gt:merge-request --json`,
// which sees both the issues and wisps tables — the gastown-72v contract.
func (b *BdCliPolecatBeadSource) ListMergeRequests(opts beads.ListOptions) ([]*beads.Issue, error) {
	if opts.Label == "" {
		opts.Label = "gt:merge-request"
	}
	return bdListIssues(b.Cli, b.WorkDir, opts)
}

// Show shells out to `bd show X --json`.
func (b *BdCliPolecatBeadSource) Show(id string) (*beads.Issue, error) {
	out, err := b.Cli.Exec(b.WorkDir, "show", id, "--json")
	if err != nil {
		return nil, err
	}
	var issues []*beads.Issue
	if err := json.Unmarshal([]byte(out), &issues); err != nil {
		return nil, fmt.Errorf("parse show output for %s: %w", id, err)
	}
	if len(issues) == 0 {
		return nil, fmt.Errorf("issue not found: %s", id)
	}
	return issues[0], nil
}

// bdListIssues is the shared body for List/ListByAssignee/ListMergeRequests.
// It shells out once to `bd list --json` and parses the array response.
func bdListIssues(cli *BdCli, workDir string, opts beads.ListOptions) ([]*beads.Issue, error) {
	args := []string{"list", "--json"}
	if opts.Status != "" {
		args = append(args, "--status="+opts.Status)
	}
	if opts.Label != "" {
		args = append(args, "--label="+opts.Label)
	}
	if opts.Assignee != "" {
		args = append(args, "--assignee="+opts.Assignee)
	}
	out, err := cli.Exec(workDir, args...)
	if err != nil {
		return nil, err
	}
	var issues []*beads.Issue
	if err := json.Unmarshal([]byte(out), &issues); err != nil {
		return nil, fmt.Errorf("parse list output: %w", err)
	}
	return issues, nil
}

// DetectFleetState summarizes the polecat fleet composition for a single
// rig. It walks the rig's polecats directory, classifies each polecat by
// its strongest signal, and consults the merge-queue for live MR gates.
//
// MR gate detection uses ListMergeRequests (which includes ephemeral wisps)
// rather than List, so ephemeral refinery gates are not missed — this is
// the gastown-72v fix.
//
// Active implementation detection aligns with the assigned hooked-bead model
// used by polecat.Manager.loadFromBeads: a work bead with status=hooked and
// assignee=<polecat> is authoritative, and the legacy AgentFields.HookBead
// is only trusted when it resolves to a currently hooked bead. This
// prevents classifying assigned hooked work with an empty agent-bead
// HookBead as "idle" (gastown-72v).
//
// townRoot is the path to the Gas Town root (parent of the rig dir).
// rigName is the rig whose polecats directory should be summarized.
func DetectFleetState(bd PolecatBeadSource, townRoot, rigName string) (*FleetState, error) {
	state := &FleetState{Rig: rigName}

	polecatsDir := filepath.Join(townRoot, rigName, "polecats")
	entries, err := os.ReadDir(polecatsDir)
	if err != nil {
		// No polecats dir = no polecats = empty fleet. Surface an error so
		// the caller can decide between "rig doesn't exist" and "empty rig"
		// without us silently masking the difference.
		return state, fmt.Errorf("read polecats dir %s: %w", polecatsDir, err)
	}

	// Pre-compute MR gate map (assignee -> MR count) using ListMergeRequests,
	// which sees BOTH issues-table and wisps-table MR beads. This is the
	// gastown-72v MR detection that does not miss ephemeral gates.
	gatesByAssignee, err := loadMRGateMap(bd)
	if err != nil {
		// Bead-store errors should not silently drop all gates; fall back
		// to no-gate detection and let the classification decide based on
		// session+work evidence.
		gatesByAssignee = map[string]int{}
	}
	// Seed PostSubmitGate with the assignees we found via ListMergeRequests,
	// then dedupe at the end so polecats in both the directory AND the gate
	// map are not appended twice (gastown-72v dedup invariant).
	state.PostSubmitGate = collectGatedPolecats(gatesByAssignee)

	t := tmux.NewTmux()
	assigneePrefix := fmt.Sprintf("%s/polecats/", rigName)
	prefix := beads.GetPrefixForRig(townRoot, rigName)

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		polecatName := entry.Name()
		assignee := assigneePrefix + polecatName
		sessionName := session.PolecatSessionName(session.PrefixFor(rigName), polecatName)
		agentBeadID := beads.PolecatBeadIDWithPrefix(prefix, rigName, polecatName)

		snap := fetchAgentBeadSnapshot(DefaultBdCli(), townRoot, agentBeadID)
		hookBead := ""
		activeMR := ""
		agentState := ""
		if snap != nil {
			hookBead = snap.HookBead
			activeMR = snap.ActiveMR
			agentState = snap.AgentState
		}

		sessionAlive := false
		if alive, err := t.HasSession(sessionName); err == nil {
			sessionAlive = alive
		}

		// Classification order (strongest first):
		//  1. Active implementation: a hooked work bead assigned to this
		//     polecat, OR a live session with assigned work — authoritatively
		//     the work bead's own state (gastown-72v assigned hooked-bead model).
		//  2. Post-submit gate: an open MR gate points at this polecat, even
		//     if the session is gone or post-submit. (gastown-72v ephemeral
		//     MR gate detection via ListMergeRequests.)
		//  3. Recovery-held: stale recovery lane without an open MR gate.
		//  4. Idle: no work, no gate, no recovery.
		if isActiveImplementation(bd, assignee, hookBead, sessionAlive, agentState) {
			state.ActiveImplementation = append(state.ActiveImplementation, polecatName)
			continue
		}
		if gatesByAssignee[assignee] > 0 || activeMR != "" {
			state.PostSubmitGate = append(state.PostSubmitGate, polecatName)
			continue
		}
		if isRecoveryHeld(agentState, sessionAlive) {
			state.RecoveryHeld = append(state.RecoveryHeld, polecatName)
			continue
		}
		state.Idle = append(state.Idle, polecatName)
	}

	// IsEmpty is the gastown-72v invariant: a fleet with an open MR gate
	// (even with all sessions gone) is NOT empty. Likewise a recovery-held
	// lane or an active implementation polecat.
	state.IsEmpty = len(state.ActiveImplementation) == 0 &&
		len(state.PostSubmitGate) == 0 &&
		len(state.RecoveryHeld) == 0 &&
		len(entries) == 0

	// Dedupe PostSubmitGate (gastown-72v): the per-loop append above can
	// re-add a polecat that collectGatedPolecats already seeded from the
	// MR-gate map. Sorted order is preserved across dedup so witnesses see
	// stable bucket order even when gates flicker.
	state.PostSubmitGate = dedupSorted(state.PostSubmitGate)
	return state, nil
}

// dedupSorted removes duplicate entries from a sorted-or-unsorted slice
// while preserving first-seen order, then sorts the result. Returns nil
// for an empty input.
func dedupSorted(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(s))
	out := make([]string, 0, len(s))
	for _, v := range s {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sortStrings(out)
	return out
}

// loadMRGateMap queries the bead store for open merge-request beads and
// returns a map keyed by assignee. MRs are detected via ListMergeRequests
// (which includes wisps) so ephemeral refinery gates are not missed.
// Returns the assignee->count map and any store error.
func loadMRGateMap(bd PolecatBeadSource) (map[string]int, error) {
	if bd == nil {
		return map[string]int{}, fmt.Errorf("nil bead source")
	}
	mrs, err := bd.ListMergeRequests(beads.ListOptions{Status: "open"})
	if err != nil {
		return map[string]int{}, fmt.Errorf("list merge requests: %w", err)
	}
	gates := make(map[string]int, len(mrs))
	for _, mr := range mrs {
		if mr == nil {
			continue
		}
		assignee := strings.TrimSpace(mr.Assignee)
		if assignee == "" {
			continue
		}
		gates[assignee]++
	}
	return gates, nil
}

// collectGatedPolecats returns polecat names that have at least one open
// MR gate, sorted deterministically. It reads the assignee->count map and
// splits assignees of the form "<rig>/polecats/<name>" to extract the
// polecat name; non-conforming assignees are ignored.
func collectGatedPolecats(gates map[string]int) []string {
	if len(gates) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	var names []string
	for assignee := range gates {
		idx := strings.LastIndex(assignee, "/polecats/")
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(assignee[idx+len("/polecats/"):])
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sortStrings(names)
	return names
}

func sortStrings(s []string) {
	// Local sort to avoid pulling in sort just for one call site; the
	// slice is at most a few entries long.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// isActiveImplementation reports whether the polecat has an authoritative
// "currently working" signal. Priority:
//  1. An assigned hooked work bead (work bead status=hooked + assignee=polecat).
//     This is the gastown-72v fix: the work bead is the source of truth, not
//     AgentFields.HookBead.
//  2. A live session where the legacy HookBead resolves to a currently
//     hooked bead for this assignee (compatibility fallback).
//  3. A non-idle live session with an assigned work bead in any active
//     status (in_progress/open). Catches the "empty hook_bead, work in
//     flight" case the gastown-72v codex finding flagged.
func isActiveImplementation(bd PolecatBeadSource, assignee, hookBead string, sessionAlive bool, agentState string) bool {
	if bd == nil {
		return false
	}
	// 1. Authoritative: work bead status=hooked + assignee=polecat.
	hooked, err := bd.List(beads.ListOptions{
		Status:   beads.StatusHooked,
		Assignee: assignee,
		Priority: -1,
	})
	if err == nil && len(hooked) > 0 {
		return true
	}
	// 2. Compatibility fallback: legacy HookBead resolves to a hooked bead
	//    for this assignee.
	if hookBead != "" {
		if issue, err := bd.Show(hookBead); err == nil &&
			issue != nil &&
			issue.Status == beads.StatusHooked &&
			issue.Assignee == assignee {
			return true
		}
	}
	// 3. Live session with any active assigned work (in_progress or open).
	//    This is the gastown-72v assigned-work-with-empty-hook_bead case:
	//    a polecat whose agent bead has been freshly created (HookBead="")
	//    but already has an in-flight issue assigned.
	if sessionAlive && beads.AgentState(agentState) != beads.AgentStateIdle {
		assigned, lerr := bd.ListByAssignee(assignee)
		if lerr == nil {
			for _, issue := range assigned {
				if issue == nil {
					continue
				}
				if issue.Status == "in_progress" || issue.Status == "open" || issue.Status == beads.StatusHooked {
					return true
				}
			}
		}
	}
	return false
}

// isRecoveryHeld reports whether the polecat is in a stale recovery lane
// (session alive but non-working, or self-reported stuck/zombie) without
// any open MR gate. Recovery-held lanes block normal fleet reporting
// because the polecat is occupied pending a recovery decision.
func isRecoveryHeld(agentState string, sessionAlive bool) bool {
	switch beads.AgentState(agentState) {
	case beads.AgentStateStuck, beads.AgentStateEscalated, beads.AgentStateAwaitingGate, beads.AgentStatePaused:
		return true
	}
	// Live session with no working signal is not necessarily recovery-held;
	// the work-bead check above already separates active from idle. A live
	// session with no work and no agent state is just idle.
	_ = sessionAlive
	return false
}

// DetectFleetStateFromCwd runs DetectFleetState using the town root
// resolved from the current working directory. It is a thin convenience
// wrapper for callers (e.g. gt fleet-status) that do not already hold a
// town-root path.
func DetectFleetStateFromCwd(bd PolecatBeadSource, workDir, rigName string) (*FleetState, error) {
	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		townRoot = workDir
	}
	return DetectFleetState(bd, townRoot, rigName)
}
