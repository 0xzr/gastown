// Package witness — liveness.go provides self-recovery primitives for the
// Witness patrol agent.
//
// Background: the Witness can sit at 100% context / degraded prompt state while
// still nominally alive (tmux session healthy, agent process running). In that
// state it stops making forward progress on the patrol loop, fails to restart
// stopped hooked polecats, and may stay stuck for hours until the Mayor notices
// and intervenes. The 2026-06-25 incident (jasper/obsidian/onyx/opal stopped
// lanes) is the canonical example.
//
// This file gives the Witness a heartbeat file that exposes patrol state
// (context saturation, last completed step, command-running duration, outstanding
// recovery obligations) plus a durable handoff file that the Witness writes
// before self-restart. A daemon-side supervisor and the Witness's own
// context-check step both consume these primitives to detect the degraded
// state and trigger a clean restart without requiring Mayor intervention.
//
// Acceptance contract: a 100%-context Witness that is not making progress
// (no heartbeat update for > VeryStaleThreshold AND last reported saturation >=
// ContextSaturationThreshold) MUST be detected and restarted with a durable
// handoff covering stopped lanes, dirty/ahead state, queued scheduler beads,
// and any in-flight cleanup command.
package witness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
)

// Default thresholds for witness self-recovery. Override via settings/config.json
// under operational.witness.
const (
	// DefaultHeartbeatStaleThreshold — heartbeat older than this is stale.
	// Set to 5m (matches Mayor/deacon) so an idle Witness that is alive and
	// patrolling still refreshes on every cycle.
	DefaultHeartbeatStaleThreshold = 5 * time.Minute

	// DefaultHeartbeatVeryStaleThreshold — heartbeat older than this means the
	// Witness is not making forward progress on patrol, even if its tmux
	// session is healthy. Triggers a supervisor-side restart decision.
	DefaultHeartbeatVeryStaleThreshold = 20 * time.Minute

	// DefaultContextSaturationThreshold — fraction (0.0-1.0) at which the
	// Witness considers itself too saturated to continue patrolling and
	// prepares a handoff. The witness's own context-check step reads the
	// saturation it has self-reported and decides whether to trigger.
	DefaultContextSaturationThreshold = 0.85

	// DefaultRecoveryCooldown — minimum interval between supervised
	// restarts of the same witness, used as a circuit breaker to prevent
	// crash loops. Set well above the heartbeat stale threshold so a
	// legitimate slow patrol cycle is not mistaken for a restart candidate.
	DefaultRecoveryCooldown = 10 * time.Minute
)

// Heartbeat represents the Witness patrol heartbeat file.
// Written by the Witness at the end of each patrol step; read by the daemon
// supervisor to detect degraded/stalled state.
type Heartbeat struct {
	// Timestamp is when the heartbeat was written.
	Timestamp time.Time `json:"timestamp"`

	// Cycle is the wake cycle number.
	Cycle int64 `json:"cycle"`

	// LastStep is the name of the last completed patrol step (e.g.
	// "inbox-check", "survey-workers", "context-check").
	LastStep string `json:"last_step,omitempty"`

	// LastAction describes what produced this heartbeat (e.g.
	// "step-inbox-check", "patrol-cycle", "session-started").
	LastAction string `json:"last_action,omitempty"`

	// ContextSaturationPercent is the Witness's self-reported context
	// saturation (0.0-1.0). Updated each cycle so the supervisor can
	// detect "stuck at high saturation".
	ContextSaturationPercent float64 `json:"context_saturation"`

	// CommandDurationMs is the wall-clock duration in milliseconds of
	// the last command the Witness ran (e.g. a long gt mail inbox). A
	// long-running command is itself a stall signal.
	CommandDurationMs int64 `json:"command_duration_ms,omitempty"`

	// OutstandingRecoveryObligations is the number of recovery tasks
	// the Witness believes it still owes (e.g. stopped polecats to
	// restart, dirty worktrees to push, in-flight cleanups). The
	// supervisor uses this to decide whether a restart is safe.
	OutstandingRecoveryObligations int `json:"outstanding_recovery_obligations"`

	// SessionStatus reports the tmux health status at the time of write.
	SessionStatus string `json:"session_status,omitempty"`

	// StoppedLanesSnapshot is a hint of the most recent stopped-lane count.
	// Authoritative lane state is in the durable handoff; this is just
	// a low-cost telemetry field.
	StoppedLanesSnapshot int `json:"stopped_lanes_snapshot,omitempty"`
}

// Age returns how old the heartbeat is. Nil heartbeats are treated as very old.
func (hb *Heartbeat) Age() time.Duration {
	if hb == nil {
		return 24 * time.Hour * 365
	}
	return time.Since(hb.Timestamp)
}

// IsFresh returns true if the heartbeat is younger than staleThreshold.
func (hb *Heartbeat) IsFresh(staleThreshold time.Duration) bool {
	if hb == nil || staleThreshold <= 0 {
		return false
	}
	return hb.Age() < staleThreshold
}

// IsStale returns true if heartbeat age is in [staleThreshold, veryStale).
func (hb *Heartbeat) IsStale(staleThreshold, veryStaleThreshold time.Duration) bool {
	if hb == nil {
		return false
	}
	age := hb.Age()
	return age >= staleThreshold && age < veryStaleThreshold
}

// IsVeryStale returns true if the heartbeat is older than veryStale.
func (hb *Heartbeat) IsVeryStale(veryStaleThreshold time.Duration) bool {
	if hb == nil {
		return true
	}
	return hb.Age() >= veryStaleThreshold
}

// IsSaturated returns true if the Witness has self-reported a saturation
// percentage at or above threshold. A nil heartbeat returns false.
func (hb *Heartbeat) IsSaturated(threshold float64) bool {
	if hb == nil || threshold <= 0 {
		return false
	}
	return hb.ContextSaturationPercent >= threshold
}

// ShouldSelfRestart reports whether the heartbeat's reported state
// indicates the Witness should self-restart. This is the predicate the
// context-check step in the patrol formula and the daemon supervisor
// both rely on. Two signals qualify:
//
//  1. Saturated + very stale: the Witness has not refreshed in
//     veryStaleThreshold and self-reports saturation >= threshold. A
//     saturated Witness that has not refreshed is by definition not
//     patrolling.
//  2. Saturated + long-running command: regardless of heartbeat
//     freshness, if the most recent patrol command ran for longer
//     than maxCommandDuration and the Witness is saturated, the
//     Witness is wedged on a single tool call and must hand off.
func (hb *Heartbeat) ShouldSelfRestart(saturationThreshold float64, veryStaleThreshold time.Duration, maxCommandDuration time.Duration) bool {
	if hb == nil {
		return false
	}
	if !hb.IsSaturated(saturationThreshold) {
		return false
	}
	if hb.IsVeryStale(veryStaleThreshold) {
		return true
	}
	if maxCommandDuration > 0 && time.Duration(hb.CommandDurationMs)*time.Millisecond > maxCommandDuration {
		return true
	}
	return false
}

// HeartbeatFile returns the path to the witness heartbeat file for a rig.
// Path: <rigPath>/witness/heartbeat.json — keeps the file inside the
// witness's own working directory so it follows the worktree if the rig
// is relocated. Mirrors the deacon layout (<town>/deacon/heartbeat.json)
// but scoped per-rig.
func HeartbeatFile(rigPath string) string {
	return filepath.Join(rigPath, "witness", "heartbeat.json")
}

// WriteHeartbeat atomically replaces the heartbeat file with hb.
func WriteHeartbeat(rigPath string, hb *Heartbeat) error {
	if rigPath == "" {
		return errors.New("witness: rigPath required for heartbeat write")
	}
	if hb == nil {
		return errors.New("witness: nil heartbeat")
	}
	if hb.Timestamp.IsZero() {
		hb.Timestamp = time.Now().UTC()
	}
	hbFile := HeartbeatFile(rigPath)
	if err := os.MkdirAll(filepath.Dir(hbFile), 0o755); err != nil {
		return fmt.Errorf("witness: creating heartbeat dir: %w", err)
	}
	data, err := json.MarshalIndent(hb, "", "  ")
	if err != nil {
		return fmt.Errorf("witness: marshaling heartbeat: %w", err)
	}
	// Write to a tmp file in the same directory then rename for atomicity.
	// This avoids the (rare) case where a partial write leaves an
	// unparseable heartbeat and a false "stale" supervisor decision.
	tmpFile, err := os.CreateTemp(filepath.Dir(hbFile), ".heartbeat-*.json.tmp")
	if err != nil {
		return fmt.Errorf("witness: creating heartbeat tmp: %w", err)
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("witness: writing heartbeat tmp: %w", err)
	}
	if _, err := tmpFile.WriteString("\n"); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("witness: trailing heartbeat newline: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("witness: closing heartbeat tmp: %w", err)
	}
	if err := os.Rename(tmpPath, hbFile); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("witness: renaming heartbeat: %w", err)
	}
	return nil
}

// ReadHeartbeat loads the witness heartbeat. Returns nil if the file does
// not exist or cannot be parsed. Parse failures are logged via the
// returned error alongside a nil pointer so callers can decide.
func ReadHeartbeat(rigPath string) (*Heartbeat, error) {
	hbFile := HeartbeatFile(rigPath)
	data, err := os.ReadFile(hbFile) //nolint:gosec // G304: path constructed from trusted rigPath
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var hb Heartbeat
	if err := json.Unmarshal(data, &hb); err != nil {
		return nil, fmt.Errorf("witness: parsing heartbeat: %w", err)
	}
	return &hb, nil
}

// Touch updates the witness heartbeat in place, incrementing the cycle and
// recording the most recent action + patrol step. Saturation, command
// duration, and obligation counts are caller-supplied so the Witness
// self-reports state from the patrol step it just finished.
func Touch(rigPath, step, action string, saturation float64, commandDuration time.Duration, obligations int) error {
	existing, _ := ReadHeartbeat(rigPath)
	cycle := int64(1)
	if existing != nil {
		cycle = existing.Cycle + 1
	}
	hb := &Heartbeat{
		Timestamp:                      time.Now().UTC(),
		Cycle:                          cycle,
		LastStep:                       step,
		LastAction:                     action,
		ContextSaturationPercent:       saturation,
		CommandDurationMs:              commandDuration.Milliseconds(),
		OutstandingRecoveryObligations: obligations,
	}
	return WriteHeartbeat(rigPath, hb)
}

// StoppedLane describes a polecat that is hooked with work but whose
// witness-controlled recovery machinery (tmux session, agent) is unavailable.
//
// The post-restart witness uses the durable assignment metadata
// (AssignedAgent, BeadKey, AssignmentSource) to perform a model-preserving
// restart. Without these fields the recovery path falls back to the rig's
// role-default agent — which is what the 2026-06-25 incident surfaced
// (obsidian silently restarted on GLM instead of its prior Kimi lane).
type StoppedLane struct {
	// Polecat is the bare polecat name (e.g. "topaz").
	Polecat string `json:"polecat"`

	// Bead is the agent bead ID associated with the polecat, if any.
	Bead string `json:"bead,omitempty"`

	// IssueID is the work bead on the polecat's hook.
	IssueID string `json:"issue_id,omitempty"`

	// Reason explains why the lane is stopped ("session-dead", "agent-hung",
	// "context-saturated-restart-pending", etc.).
	Reason string `json:"reason"`

	// ObservedAt is when the witness first noticed the stopped state.
	ObservedAt time.Time `json:"observed_at"`

	// AssignedAgent is the runtime agent the lane was last running on
	// (e.g. "claude", "codex", "kimi"). The post-restart witness must
	// pass this to `gt session start <rig>/<polecat>` so a model
	// rotation is not silently introduced. Empty when no agent
	// assignment could be recovered (legacy lanes, missing
	// model-assignments files, etc.) — the replayer must surface
	// this as an explicit "unassigned" condition rather than
	// silently falling back to the rig role default.
	AssignedAgent string `json:"assigned_agent,omitempty"`

	// BeadKey is the durable key the model-assignments file is
	// indexed by (typically the polecat's hook_bead). Used by the
	// replayer to disambiguate a lane when the agent bead's
	// assigned_agent field is empty but the wrapper wrote a
	// model-assignments/<bead>.json fallback (gastown-hkd).
	BeadKey string `json:"bead_key,omitempty"`

	// AssignmentSource records where AssignedAgent came from
	// ("agent-bead", "model-assignments", "config-default",
	// "unassigned"). The replayer must surface a non-"agent-bead"
	// or non-"model-assignments" source as needing escalation,
	// because the persisted identity cannot be trusted.
	AssignmentSource string `json:"assignment_source,omitempty"`
}

// Assignment source values for StoppedLane.AssignmentSource. Exported
// so the replayer and tests can compare against canonical strings.
const (
	AssignmentSourceAgentBead        = "agent-bead"
	AssignmentSourceModelAssignments = "model-assignments"
	AssignmentSourceConfigDefault    = "config-default"
	AssignmentSourceUnassigned       = "unassigned"
)

// DirtyLane describes a polecat with uncommitted or unpushed work that
// the witness has not yet pushed back to the ref.
type DirtyLane struct {
	Polecat          string    `json:"polecat"`
	UncommittedCount int       `json:"uncommitted_count"`
	UnpushedCount    int       `json:"unpushed_count"`
	ObservedAt       time.Time `json:"observed_at"`
}

// HandoffFile is the durable snapshot the Witness writes before
// self-restart. It is the single source of truth for "what was the
// Witness working on when it died". The post-restart Witness (and the
// daemon supervisor) read this to pick up exactly where the previous
// session left off, without re-deriving the same state.
type HandoffFile struct {
	// Timestamp is when the handoff was written.
	Timestamp time.Time `json:"timestamp"`

	// Reason is the supervision signal that triggered the handoff
	// ("context-saturated", "command-stuck", "supervised-restart").
	Reason string `json:"reason"`

	// LastStep is the patrol step the Witness had just completed.
	LastStep string `json:"last_step"`

	// LastCycle is the heartbeat cycle the Witness was on at handoff time.
	LastCycle int64 `json:"last_cycle"`

	// RigName is the rig the Witness patrols.
	RigName string `json:"rig_name"`

	// StoppedLanes are polecats the Witness was about to recover.
	StoppedLanes []StoppedLane `json:"stopped_lanes"`

	// DirtyLanes are polecats with work that needs human/refinery
	// attention (uncommitted or unpushed changes).
	DirtyLanes []DirtyLane `json:"dirty_lanes,omitempty"`

	// QueuedSchedulerBeads are scheduler beads the Witness had enqueued
	// for later patrol cycles.
	QueuedSchedulerBeads []string `json:"queued_scheduler_beads,omitempty"`

	// InFlightCleanup lists cleanup commands the Witness was executing
	// at handoff time (e.g. "gt session restart gastown/topaz"). The
	// post-restart Witness checks each entry and re-issues the command
	// if it is still incomplete.
	InFlightCleanup []string `json:"in_flight_cleanup,omitempty"`

	// ContextSaturationPercent is the saturation at handoff time.
	ContextSaturationPercent float64 `json:"context_saturation"`

	// OutstandingObligations is the number of recovery obligations the
	// Witness believed it still owed at handoff time. Mirrors
	// Heartbeat.OutstandingRecoveryObligations so the post-restart
	// Witness can pick up exactly where the previous one left off.
	OutstandingObligations int `json:"outstanding_obligations,omitempty"`

	// Notes is a free-form field for Witness observations that should
	// survive the restart (e.g. "refinery wedged on integration of X").
	Notes string `json:"notes,omitempty"`
}

// HandoffFilePath returns the durable handoff file path for a rig.
func HandoffFilePath(rigPath string) string {
	return filepath.Join(rigPath, "witness", "handoff.json")
}

// WriteHandoff atomically replaces the handoff file. The Witness must
// always write this before issuing its own restart, so the next session
// can resume without re-deriving state.
func WriteHandoff(rigPath string, hf *HandoffFile) error {
	if rigPath == "" {
		return errors.New("witness: rigPath required for handoff write")
	}
	if hf == nil {
		return errors.New("witness: nil handoff")
	}
	if hf.Timestamp.IsZero() {
		hf.Timestamp = time.Now().UTC()
	}
	hfPath := HandoffFilePath(rigPath)
	if err := os.MkdirAll(filepath.Dir(hfPath), 0o755); err != nil {
		return fmt.Errorf("witness: creating handoff dir: %w", err)
	}
	// Sort stopped lanes by polecat name so the file diff is stable
	// across writes (helps tests and review).
	sort.SliceStable(hf.StoppedLanes, func(i, j int) bool {
		return hf.StoppedLanes[i].Polecat < hf.StoppedLanes[j].Polecat
	})
	sort.SliceStable(hf.DirtyLanes, func(i, j int) bool {
		return hf.DirtyLanes[i].Polecat < hf.DirtyLanes[j].Polecat
	})
	data, err := json.MarshalIndent(hf, "", "  ")
	if err != nil {
		return fmt.Errorf("witness: marshaling handoff: %w", err)
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(hfPath), ".handoff-*.json.tmp")
	if err != nil {
		return fmt.Errorf("witness: creating handoff tmp: %w", err)
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("witness: writing handoff tmp: %w", err)
	}
	if _, err := tmpFile.WriteString("\n"); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("witness: trailing handoff newline: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("witness: closing handoff tmp: %w", err)
	}
	if err := os.Rename(tmpPath, hfPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("witness: renaming handoff: %w", err)
	}
	return nil
}

// ReadHandoff returns the most recent handoff file. Returns nil if the
// file does not exist (i.e. the Witness has never restarted).
func ReadHandoff(rigPath string) (*HandoffFile, error) {
	hfPath := HandoffFilePath(rigPath)
	data, err := os.ReadFile(hfPath) //nolint:gosec // G304: path constructed from trusted rigPath
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var hf HandoffFile
	if err := json.Unmarshal(data, &hf); err != nil {
		return nil, fmt.Errorf("witness: parsing handoff: %w", err)
	}
	return &hf, nil
}

// ClearHandoff removes the handoff file. The post-restart Witness calls
// this after it has consumed the handoff (acknowledged every stopped
// lane, in-flight cleanup, etc.) so the next restart does not pick up
// stale obligations.
func ClearHandoff(rigPath string) error {
	hfPath := HandoffFilePath(rigPath)
	err := os.Remove(hfPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LaneRecovery is the model-preserving recovery action for a single
// stopped lane. The witness (or the supervisor) executes it after a
// handoff is consumed, preserving the lane's prior agent assignment
// instead of falling back to the rig role default (gastown-o9d).
type LaneRecovery struct {
	// Polecat is the bare polecat name (e.g. "topaz").
	Polecat string `json:"polecat"`

	// BeadKey is the durable key (typically the hook_bead) the lane
	// was indexed by. May be empty for legacy lanes that never
	// recorded a hook.
	BeadKey string `json:"bead_key,omitempty"`

	// AssignedAgent is the runtime agent the replayer resolved for
	// the lane. Empty when the replayer could not recover a
	// durable assignment and the lane must be escalated.
	AssignedAgent string `json:"assigned_agent,omitempty"`

	// AssignmentSource is where AssignedAgent came from. Mirrors
	// StoppedLane.AssignmentSource.
	AssignmentSource string `json:"assignment_source,omitempty"`

	// Command is the exact `gt` invocation the post-restart witness
	// should run to bring the lane back online. Empty when the
	// lane must be escalated (no model-preserving start possible).
	Command string `json:"command,omitempty"`

	// Escalate is true when the lane needs human / Mayor
	// intervention because no durable assignment exists and a
	// raw `gt session start` would silently rotate the agent
	// (the 2026-06-25 obsidian/GLM failure mode).
	Escalate bool `json:"escalate,omitempty"`

	// Reason explains why this action is what it is. Surface
	// this in the post-restart witness's first patrol cycle so
	// recovery is observable.
	Reason string `json:"reason"`
}

// HandoffReplayResult is the per-handoff plan produced by the
// replayer. The post-restart witness (or `gt witness replay`) emits
// this so operators can audit what model-preserving restarts will be
// attempted and which lanes need escalation.
type HandoffReplayResult struct {
	// RigName is the rig the handoff belonged to.
	RigName string `json:"rig_name"`

	// HandoffPath is the on-disk handoff file consumed.
	HandoffPath string `json:"handoff_path"`

	// Actions are the model-preserving restart commands the
	// post-restart witness should execute.
	Actions []LaneRecovery `json:"actions"`

	// Escalations are lanes that need human / Mayor intervention
	// because no durable agent assignment exists. The witness
	// must NOT execute raw `gt session start` for these lanes.
	Escalations []LaneRecovery `json:"escalations,omitempty"`

	// InFlightCleanup carries through any in-flight cleanup
	// commands the handoff recorded, so the post-restart witness
	// can re-check them.
	InFlightCleanup []string `json:"in_flight_cleanup,omitempty"`
}

// AssignmentResolver is the integration point between the replayer
// and the wrapper's persistent model-assignment lookup. It is kept
// as a callback so the witness package does not import the polecat
// package (avoiding an import cycle: polecat -> witness is fine, but
// witness -> polecat would couple the two). The wrapper installs a
// resolver at runtime via NewHandoffReplayerWithResolver.
type AssignmentResolver func(rigPath, polecat, beadKey string) (agent, source string)

// HandoffReplayer consumes a handoff file and produces a model-
// preserving recovery plan. The plan is a list of restart commands
// keyed by stopped lane; the caller (post-restart witness or
// supervisor) decides whether to execute them.
//
// The replayer is split out from RestartWitness so it is unit-
// testable without a live tmux session.
type HandoffReplayer struct {
	townRoot string
	rigPath  string
	rigName  string
	gtPath   string
	Resolve  AssignmentResolver
}

// NewHandoffReplayer creates a replayer for the given rig. By default
// Resolve is nil — lanes without a pre-populated AssignedAgent are
// escalated. Use NewHandoffReplayerWithResolver to install a real
// resolver (e.g. one backed by the wrapper's model-assignments
// files).
func NewHandoffReplayer(townRoot, rigPath, rigName, gtPath string) *HandoffReplayer {
	if gtPath == "" {
		gtPath = "gt"
	}
	return &HandoffReplayer{
		townRoot: townRoot,
		rigPath:  rigPath,
		rigName:  rigName,
		gtPath:   gtPath,
	}
}

// NewHandoffReplayerWithResolver creates a replayer with a custom
// assignment resolver. Use this in production to install a resolver
// backed by ReadPersistedAssignedAgent / readModelAssignment.
func NewHandoffReplayerWithResolver(townRoot, rigPath, rigName, gtPath string, resolve AssignmentResolver) *HandoffReplayer {
	r := NewHandoffReplayer(townRoot, rigPath, rigName, gtPath)
	r.Resolve = resolve
	return r
}

// Replay consumes a handoff and returns a model-preserving recovery
// plan. The plan distinguishes between lanes that can be
// model-preserving-restarted (Actions) and lanes that need
// escalation because no durable assignment exists (Escalations).
//
// Replay is pure — it does not invoke `gt session start` or write
// any files. The caller executes the plan.
func (r *HandoffReplayer) Replay(hf *HandoffFile) (*HandoffReplayResult, error) {
	if hf == nil {
		return nil, errors.New("witness: nil handoff")
	}
	if r.rigPath == "" {
		return nil, errors.New("witness: HandoffReplayer requires non-empty rigPath")
	}
	result := &HandoffReplayResult{
		RigName:         r.rigName,
		HandoffPath:     HandoffFilePath(r.rigPath),
		InFlightCleanup: append([]string(nil), hf.InFlightCleanup...),
	}
	for _, lane := range hf.StoppedLanes {
		action := r.planLane(lane)
		if action.Escalate {
			result.Escalations = append(result.Escalations, action)
			continue
		}
		result.Actions = append(result.Actions, action)
	}
	return result, nil
}

// planLane resolves the model-preserving restart plan for a single
// stopped lane. Decision tree:
//
//  1. If the handoff's StoppedLane already has AssignedAgent +
//     AssignmentSource in {"agent-bead", "model-assignments"}, use it
//     directly. The witness's pre-restart survey populated this from
//     the durable sources, so the assignment is trusted.
//  2. Otherwise, if a resolver is installed, ask the resolver for
//     the lane's assigned agent. The resolver consults
//     model-assignments/<bead>.json as a fallback (gastown-hkd).
//  3. If still no agent, escalate. A raw `gt session start` would
//     silently rotate to the rig default — the failure mode the
//     2026-06-25 obsidian/GLM incident surfaced.
//
// The "config-default" source is treated as a non-durable identity
// and is escalated even when populated, because it means we
// recovered no persisted assignment.
func (r *HandoffReplayer) planLane(lane StoppedLane) LaneRecovery {
	rec := LaneRecovery{
		Polecat: lane.Polecat,
		BeadKey: lane.BeadKey,
	}

	// Step 1: trust the handoff's pre-populated assignment if it
	// came from a durable source.
	if lane.AssignedAgent != "" && isDurableAssignmentSource(lane.AssignmentSource) {
		// gastown-c4r finding #1: never trust a handoff-derived
		// identifier enough to shell it. Validate polecat + agent
		// against a strict schema; anything that does not match is
		// escalated rather than turned into a command string.
		if !validIdentifier(lane.Polecat) || !validIdentifier(lane.AssignedAgent) {
			rec.Escalate = true
			rec.AssignmentSource = AssignmentSourceUnassigned
			rec.Reason = "invalid polecat/agent identifier in handoff (would be shelled); escalate to Mayor"
			return rec
		}
		rec.AssignedAgent = lane.AssignedAgent
		rec.AssignmentSource = lane.AssignmentSource
		rec.Command = r.restartCommand(lane.Polecat, lane.AssignedAgent)
		rec.Reason = "model-preserving restart from handoff (source=" + lane.AssignmentSource + ")"
		return rec
	}

	// Step 2: ask the resolver for a durable fallback.
	if r.Resolve != nil {
		if agent, source := r.Resolve(r.rigPath, lane.Polecat, lane.BeadKey); agent != "" && isDurableAssignmentSource(source) {
			if !validIdentifier(lane.Polecat) || !validIdentifier(agent) {
				rec.Escalate = true
				rec.AssignmentSource = AssignmentSourceUnassigned
				rec.Reason = "invalid polecat/agent identifier from resolver (would be shelled); escalate to Mayor"
				return rec
			}
			rec.AssignedAgent = agent
			rec.AssignmentSource = source
			rec.Command = r.restartCommand(lane.Polecat, agent)
			rec.Reason = "model-preserving restart from resolver (source=" + source + ")"
			return rec
		}
	}

	// Step 3: escalate. Do NOT synthesize a config-default restart
	// because the 2026-06-25 incident proved that silently
	// rotating the agent produces the wrong model mix.
	rec.Escalate = true
	rec.AssignmentSource = AssignmentSourceUnassigned
	rec.Reason = "no durable agent assignment: would silently rotate to rig role default; escalate to Mayor"
	return rec
}

// restartCommand builds the model-preserving `gt session start`
// invocation. Uses `--agent` so the wrapper's model-preservation
// path in runSessionStart/runSessionRestart engages (gastown-hkd).
func (r *HandoffReplayer) restartCommand(polecat, agent string) string {
	gt := r.gtPath
	if gt == "" {
		gt = "gt"
	}
	return fmt.Sprintf("%s session start %s/%s --agent %s", gt, r.rigName, polecat, agent)
}

// isDurableAssignmentSource reports whether a source string names a
// durable persisted assignment (agent bead or wrapper's
// model-assignments file). "config-default" and "unassigned" are
// non-durable and trigger escalation.
func isDurableAssignmentSource(source string) bool {
	return source == AssignmentSourceAgentBead || source == AssignmentSourceModelAssignments
}

// validIdentifier is the strict schema for any value that will be
// turned into a restart-command argument (gastown-c4r finding #1).
// It accepts a conservative shell-safe subset: alphanumerics, dot,
// dash, and underscore, anchored at both ends, non-empty. This is the
// single choke point that prevents a handoff field written by a
// non-supervisor code path from injecting shell metacharacters into the
// `sh -c` apply path (the original vulnerability: restartCommand
// interpolated rigName/polecat/agent into a string that was later
// passed to `sh -c`).
func validIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '.', c == '-', c == '_':
			// allowed
		default:
			return false
		}
	}
	return true
}

// ApplyArgs returns the argv vector for a model-preserving restart of
// a single lane, built directly from the structured LaneRecovery fields
// (gastown-c4r finding #1). Callers MUST use this instead of parsing
// or shelling Action.Command: it is the only apply path that cannot be
// subverted by handoff field contents, because no shell is involved
// and every interpolated value has already passed validIdentifier at
// plan time.
//
// rigName is the rig the handoff belongs to; it is validated here as
// defense in depth (it came from Supervisor construction, but the
// supervisor is long-lived and a stale value is plausible).
//
// Returns (nil, error) if the lane is not executable (Escalate set, or
// polecat/agent failed validation) so the caller skips it instead of
// silently dropping it.
func (a LaneRecovery) ApplyArgs(gtPath, rigName string) ([]string, error) {
	if a.Escalate {
		return nil, errors.New("witness: cannot apply an escalation action")
	}
	if !validIdentifier(a.Polecat) || !validIdentifier(a.AssignedAgent) || !validIdentifier(rigName) {
		return nil, fmt.Errorf("witness: invalid identifier for apply (rig=%q polecat=%q agent=%q)", rigName, a.Polecat, a.AssignedAgent)
	}
	if gtPath == "" {
		gtPath = "gt"
	}
	return []string{gtPath, "session", "start", rigName + "/" + a.Polecat, "--agent", a.AssignedAgent}, nil
}

// RecoveryAttempt records a single supervised restart of the Witness.
type RecoveryAttempt struct {
	// Timestamp is when the restart began.
	Timestamp time.Time `json:"timestamp"`

	// Reason is the supervision signal that triggered the restart.
	Reason string `json:"reason"`

	// HandoffFile is the path of the handoff file consumed before
	// restart (empty if no handoff was written).
	HandoffFile string `json:"handoff_file,omitempty"`

	// BeforeRestart is the heartbeat state immediately before restart.
	BeforeRestart *Heartbeat `json:"before_restart,omitempty"`

	// ResumedAt is the timestamp recorded by the post-restart Witness
	// when it has cleared the handoff. Empty if the Witness never
	// resumed.
	ResumedAt *time.Time `json:"resumed_at,omitempty"`

	// Verification captures the post-restart model/agent identity.
	Verification *ModelVerification `json:"verification,omitempty"`

	// Error records restart failure, if any.
	Error string `json:"error,omitempty"`
}

// ModelVerification captures whether the post-restart Witness is using
// the expected agent/model. Mirrors mayor.ModelVerification so a
// future consolidated verification helper could replace both.
type ModelVerification struct {
	ExpectedAgent string `json:"expected_agent,omitempty"`
	ActualAgent   string `json:"actual_agent,omitempty"`
	Verified      bool   `json:"verified"`
	Error         string `json:"error,omitempty"`
}

// RecoveryAttemptsDir returns the directory where Witness recovery
// attempts are appended.
func RecoveryAttemptsDir(townRoot string) string {
	return filepath.Join(townRoot, constants.DirRuntime, "witness-recovery-attempts")
}

// RecordRecoveryAttempt appends a single recovery attempt to the
// durable ledger. Files are timestamped JSONL so multiple attempts in
// the same second still record separately.
func RecordRecoveryAttempt(townRoot string, attempt *RecoveryAttempt) error {
	if attempt == nil {
		return errors.New("witness: nil recovery attempt")
	}
	if attempt.Timestamp.IsZero() {
		attempt.Timestamp = time.Now().UTC()
	}
	dir := RecoveryAttemptsDir(townRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("witness: creating recovery dir: %w", err)
	}
	filename := fmt.Sprintf("%s.jsonl", attempt.Timestamp.UTC().Format("20060102T150405.000000000Z"))
	path := filepath.Join(dir, filename)
	data, err := json.Marshal(attempt)
	if err != nil {
		return fmt.Errorf("witness: marshaling recovery attempt: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("witness: opening recovery ledger: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("witness: writing recovery ledger: %w", err)
	}
	return nil
}

// IsRestartOnCooldown returns true if a previous supervised restart of
// the same witness happened within cooldown. Prevents crash-loop
// restarts when a Witness keeps saturating immediately on resume.
func IsRestartOnCooldown(townRoot, rigName string, cooldown time.Duration) (bool, time.Time, error) {
	if cooldown <= 0 {
		return false, time.Time{}, nil
	}
	dir := RecoveryAttemptsDir(townRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, time.Time{}, nil
		}
		return false, time.Time{}, err
	}
	// Walk newest-first.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() > entries[j].Name()
	})
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		//nolint:gosec // G304: path is the recovery ledger directory
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		// Each line is a single attempt; we want the most recent matching
		// the rig, which corresponds to the most recent file (since we
		// sort by name) — read the last non-empty line.
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			var attempt RecoveryAttempt
			if err := json.Unmarshal([]byte(line), &attempt); err != nil {
				continue
			}
			// The attempt reason is the only field that includes the
			// rig context — we stash the rig name into the Reason
			// prefix when recording (see RestartWitness). Match on
			// a sentinel so we don't false-positive on generic reasons.
			if !strings.Contains(attempt.Reason, rigName+":") {
				continue
			}
			if time.Since(attempt.Timestamp) < cooldown {
				return true, attempt.Timestamp, nil
			}
			return false, attempt.Timestamp, nil
		}
	}
	return false, time.Time{}, nil
}

// Thresholds is the resolved threshold set for witness self-recovery.
// Mirrors the operational config style used by Mayor.
type Thresholds struct {
	StaleThreshold             time.Duration
	VeryStaleThreshold         time.Duration
	ContextSaturationThreshold float64
	RecoveryCooldown           time.Duration
	MaxCommandDuration         time.Duration
}

// ResolveThresholds reads operational config (or compiled-in defaults)
// and returns the active threshold set. townRoot may be empty (defaults
// only).
func ResolveThresholds(townRoot string) Thresholds {
	t := Thresholds{
		StaleThreshold:             DefaultHeartbeatStaleThreshold,
		VeryStaleThreshold:         DefaultHeartbeatVeryStaleThreshold,
		ContextSaturationThreshold: DefaultContextSaturationThreshold,
		RecoveryCooldown:           DefaultRecoveryCooldown,
		MaxCommandDuration:         10 * time.Minute,
	}
	if townRoot == "" {
		return t
	}
	cfg := config.LoadOperationalConfig(townRoot)
	wc := cfg.GetWitnessConfig()
	t.StaleThreshold = wc.HeartbeatStaleThresholdD()
	t.VeryStaleThreshold = wc.HeartbeatVeryStaleThresholdD()
	t.ContextSaturationThreshold = wc.ContextSaturationThresholdV()
	t.RecoveryCooldown = wc.RecoveryCooldownD()
	t.MaxCommandDuration = wc.MaxCommandDurationD()
	return t
}

// ParseDurationOrZero is a small wrapper that returns 0 on parse error.
// Exported because tests outside the package use it as a parser helper,
// and small CLI surfaces may want a non-panicking duration parser.
func ParseDurationOrZero(s string) time.Duration {
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}

// LaneStateResolver is the integration point between the supervisor and
// the rig's live polecat state (gastown-c4r finding #2). It is kept as a
// callback so the witness package does not import the daemon/polecat
// packages (avoiding an import cycle). The daemon installs a resolver
// backed by DetectZombiePolecats so a supervised restart writes a
// fully-populated handoff (stopped lanes, dirty lanes) instead of the
// metadata-only synthetic handoff the original EnsureHandoff produced.
//
// Returns the stopped and dirty lanes observed for the rig at call
// time. Either slice may be nil/empty.
type LaneStateResolver func(rigPath, rigName string) (stopped []StoppedLane, dirty []DirtyLane)

// Supervisor is the bundled interface for daemon-driven witness
// supervision checks and for the Witness itself to write its own
// handoff before a self-restart. All methods are safe for concurrent
// callers within a single process.
type Supervisor struct {
	townRoot string
	rigPath  string
	rigName  string
	gtPath   string
	startMu  sync.Mutex
	// ResolveLanes, when set, supplies live stopped/dirty lane state
	// to EnsureHandoff so the durable handoff captures the rig's
	// actual outstanding obligations instead of an empty snapshot.
	ResolveLanes LaneStateResolver
}

// NewSupervisor creates a Supervisor for the witness of a specific rig.
// rigPath is the path to the rig working tree (parent of witness/).
// townRoot may be empty if only the rig-level files are needed.
func NewSupervisor(townRoot, rigPath, rigName, gtPath string) *Supervisor {
	if gtPath == "" {
		gtPath = "gt"
	}
	return &Supervisor{
		townRoot: townRoot,
		rigPath:  rigPath,
		rigName:  rigName,
		gtPath:   gtPath,
	}
}

// TownRoot returns the configured town root.
func (s *Supervisor) TownRoot() string { return s.townRoot }

// RigPath returns the configured rig path.
func (s *Supervisor) RigPath() string { return s.rigPath }

// RigName returns the configured rig name.
func (s *Supervisor) RigName() string { return s.rigName }

// GTPath returns the configured gt binary path (for the apply phase
// of replay, which builds argv via LaneRecovery.ApplyArgs).
func (s *Supervisor) GTPath() string {
	if s.gtPath == "" {
		return "gt"
	}
	return s.gtPath
}

// ReadHeartbeat loads the witness heartbeat (or nil if missing).
func (s *Supervisor) ReadHeartbeat() *Heartbeat {
	hb, _ := ReadHeartbeat(s.rigPath)
	return hb
}

// Touch records a patrol step in the heartbeat. saturation is a
// fraction in [0,1]. commandDuration is the wall-clock duration of
// the most recent command. obligations is the current number of
// recovery obligations the Witness knows about.
func (s *Supervisor) Touch(step, action string, saturation float64, commandDuration time.Duration, obligations int) error {
	return Touch(s.rigPath, step, action, saturation, commandDuration, obligations)
}

// ReadHandoff returns the most recent handoff (or nil if missing).
func (s *Supervisor) ReadHandoff() *HandoffFile {
	hf, _ := ReadHandoff(s.rigPath)
	return hf
}

// WriteHandoff writes a new handoff file.
func (s *Supervisor) WriteHandoff(hf *HandoffFile) error {
	return WriteHandoff(s.rigPath, hf)
}

// ClearHandoff removes the handoff file after the post-restart Witness
// has consumed it.
func (s *Supervisor) ClearHandoff() error {
	return ClearHandoff(s.rigPath)
}

// RecordRecovery appends a recovery attempt to the durable ledger.
func (s *Supervisor) RecordRecovery(attempt *RecoveryAttempt) error {
	return RecordRecoveryAttempt(s.townRoot, attempt)
}

// Replayer returns a HandoffReplayer bound to this supervisor's rig
// and town roots. The caller can install a custom AssignmentResolver
// on the returned replayer.
func (s *Supervisor) Replayer() *HandoffReplayer {
	return NewHandoffReplayer(s.townRoot, s.rigPath, s.rigName, s.gtPath)
}

// PlanRecovery consumes the current handoff (if any) and returns a
// model-preserving recovery plan. Returns (nil, nil) when no handoff
// is on disk. This is the entry point the post-restart witness
// (or `gt witness replay <rig>`) calls to act on a handoff without
// re-deriving state.
func (s *Supervisor) PlanRecovery() (*HandoffReplayResult, error) {
	hf, err := ReadHandoff(s.rigPath)
	if err != nil {
		return nil, err
	}
	if hf == nil {
		return nil, nil
	}
	return s.Replayer().Replay(hf)
}

// Thresholds returns the resolved threshold set for this rig.
func (s *Supervisor) Thresholds() Thresholds {
	return ResolveThresholds(s.townRoot)
}

// IsOnCooldown returns true if a recent supervised restart happened
// within the configured cooldown. Prevents crash-loop restarts.
func (s *Supervisor) IsOnCooldown() (bool, time.Time, error) {
	return IsRestartOnCooldown(s.townRoot, s.rigName, s.Thresholds().RecoveryCooldown)
}

// ShouldRestart returns true if the supervisor believes the witness
// should be restarted, based on heartbeat state and the configured
// thresholds. Cooldown is honored.
//
// This honors the SAME signals Heartbeat.ShouldSelfRestart exposes
// (gastown-c4r finding #4): a saturated witness that is very-stale OR
// wedged in a long-running command (CommandDurationMs > MaxCommandDuration)
// is a restart candidate. The previous implementation only checked
// saturation + IsVeryStale, so a saturated witness stuck in a single
// long-running tool call but still refreshing its heartbeat was never
// restarted — the canonical degraded mode this supervisor exists to
// catch. Delegating to ShouldSelfRestart keeps the two decision paths
// aligned instead of reimplementing (and dropping) the command-duration
// signal.
func (s *Supervisor) ShouldRestart() (bool, string) {
	t := s.Thresholds()
	hb := s.ReadHeartbeat()
	if hb == nil {
		// No heartbeat at all is not, on its own, grounds for restart.
		// A fresh witness (or one that just rolled over) may not have
		// written its first heartbeat yet.
		return false, ""
	}
	if !hb.ShouldSelfRestart(t.ContextSaturationThreshold, t.VeryStaleThreshold, t.MaxCommandDuration) {
		return false, ""
	}
	if onCD, last, err := s.IsOnCooldown(); err == nil && onCD {
		return false, "on-cooldown-since-" + last.UTC().Format(time.RFC3339)
	}
	// Compose reason with rig prefix so a single recovery ledger
	// serves multiple rigs.
	reason := s.rigName + ":context-saturated-stalled"
	if !hb.IsVeryStale(t.VeryStaleThreshold) {
		// Reached here via the long-command branch (not very-stale).
		// Surface that distinction so the recovery ledger records the
		// actual trigger.
		reason = s.rigName + ":command-wedged"
	}
	return true, reason
}

// VerifyAgent runs `gt model-status --json` and compares the actual
// agent/model against the expected one resolved from operational
// config. Best-effort: returns a partially-populated ModelVerification
// on any error and never panics.
func (s *Supervisor) VerifyAgent() *ModelVerification {
	result := &ModelVerification{}
	if s.rigPath != "" {
		rc := config.ResolveRoleAgentConfig("witness", s.townRoot, s.rigPath)
		if rc != nil && rc.ResolvedAgent != "" {
			result.ExpectedAgent = rc.ResolvedAgent
		}
	}
	actual, err := verifyAgentViaGT(s.gtPath)
	if err != nil {
		if result.Error == "" {
			result.Error = err.Error()
		}
		return result
	}
	result.ActualAgent = actual.ActualAgent
	result.Error = actual.Error
	if result.ExpectedAgent != "" && result.ActualAgent != "" {
		result.Verified = strings.EqualFold(result.ActualAgent, result.ExpectedAgent)
	}
	return result
}

// EnsureHandoff makes sure a handoff file is on disk for the next
// Witness to consume. If one already exists, it is left in place (the
// witness will consume and clear it on resume). Otherwise a handoff is
// synthesized from the current heartbeat so the post-restart witness
// has authoritative state to resume from.
//
// gastown-c4r finding #2: the original implementation synthesized a
// handoff carrying only heartbeat metadata and StoppedLanes: nil, so a
// supervised restart dropped stopped lanes, dirty/ahead state, and the
// recovery obligations the spec requires. When a LaneStateResolver is
// installed (daemon backs it with DetectZombiePolecats), the synthetic
// handoff is now populated with the rig's actual stopped and dirty
// lanes at restart time.
//
// Returns the path to the handoff file (or "" if no handoff was
// written because neither handoff file nor heartbeat existed).
func (s *Supervisor) EnsureHandoff(reason string) (string, error) {
	if existing, _ := ReadHandoff(s.rigPath); existing != nil {
		return HandoffFilePath(s.rigPath), nil
	}
	hb := s.ReadHeartbeat()
	if hb == nil {
		return "", nil
	}
	synth := &HandoffFile{
		Timestamp:                time.Now().UTC(),
		Reason:                   reason,
		LastStep:                 hb.LastStep,
		LastCycle:                hb.Cycle,
		RigName:                  s.rigName,
		ContextSaturationPercent: hb.ContextSaturationPercent,
	}
	// Populate outstanding obligations from live rig state when a
	// resolver is available. Stopped/dirty lanes carry the durable
	// assignment metadata the post-restart replayer needs.
	if s.ResolveLanes != nil {
		stopped, dirty := s.ResolveLanes(s.rigPath, s.rigName)
		synth.StoppedLanes = stopped
		synth.DirtyLanes = dirty
	}
	// Mirror the obligation count into the snapshot field so the
	// post-restart witness and dashboards see a consistent number.
	synth.OutstandingObligations = len(synth.StoppedLanes) + len(synth.DirtyLanes)
	if err := WriteHandoff(s.rigPath, synth); err != nil {
		return "", err
	}
	return HandoffFilePath(s.rigPath), nil
}

// RestartWitness performs a supervised restart: capture heartbeat,
// write pre-restart handoff (if not already present), stop the
// session, start a new one, verify the post-restart model, and record
// the attempt. Returns the recorded attempt for inspection.
//
// reason is the supervision signal that triggered the restart. If a
// handoff file is already present, it is consumed (left in place) and
// the reason is recorded as the trigger; otherwise a minimal handoff
// is synthesized from the current heartbeat so the post-restart
// witness has something to resume from.
func (s *Supervisor) RestartWitness(mgr *Manager, reason string) (*RecoveryAttempt, error) {
	s.startMu.Lock()
	defer s.startMu.Unlock()

	if mgr == nil {
		return nil, errors.New("witness: RestartWitness requires non-nil manager")
	}

	attempt := &RecoveryAttempt{
		Timestamp: time.Now().UTC(),
		Reason:    reason,
	}
	if hb := s.ReadHeartbeat(); hb != nil {
		attempt.BeforeRestart = hb
	}
	handoffPath, err := s.EnsureHandoff(reason)
	if err != nil {
		attempt.Error = fmt.Sprintf("ensure handoff: %v", err)
		_ = s.RecordRecovery(attempt)
		return attempt, err
	}
	attempt.HandoffFile = handoffPath

	if err := mgr.Stop(); err != nil && !errors.Is(err, ErrNotRunning) {
		attempt.Error = fmt.Sprintf("stop: %v", err)
		_ = s.RecordRecovery(attempt)
		return attempt, fmt.Errorf("witness: stop: %w", err)
	}

	if err := mgr.Start(false, "", nil); err != nil {
		attempt.Error = fmt.Sprintf("start: %v", err)
		_ = s.RecordRecovery(attempt)
		return attempt, fmt.Errorf("witness: start: %w", err)
	}

	// Verify post-restart model. Best-effort; failure is recorded but
	// does not roll back the restart (the witness is up and patrolling,
	// which is more important than perfect model match).
	verify, _ := verifyAgentViaGT(s.gtPath)
	attempt.Verification = verify
	if err := s.RecordRecovery(attempt); err != nil {
		return attempt, fmt.Errorf("witness: recording attempt: %w", err)
	}
	return attempt, nil
}

// verifyAgentViaGT runs `gt model-status --json` and parses the result.
// Best-effort: returns an empty ModelVerification on any error.
func verifyAgentViaGT(gtPath string) (*ModelVerification, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, gtPath, "model-status", "--json")
	out, err := cmd.Output()
	if err != nil {
		return &ModelVerification{Error: err.Error()}, err
	}
	// Tolerate both JSON and plain-text responses.
	trimmed := strings.TrimSpace(string(out))
	result := &ModelVerification{}
	if strings.HasPrefix(trimmed, "{") {
		var payload struct {
			Model string `json:"model"`
			Name  string `json:"name"`
		}
		if err := json.Unmarshal(out, &payload); err == nil {
			if payload.Model != "" {
				result.ActualAgent = payload.Model
			} else {
				result.ActualAgent = payload.Name
			}
		}
	} else {
		for _, line := range strings.Split(trimmed, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				result.ActualAgent = line
				break
			}
		}
	}
	return result, nil
}
