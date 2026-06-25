package polecat

import "strings"

const (
	WorkstateVerdictWorking       = "WORKING"
	WorkstateVerdictSafeToNuke    = "SAFE_TO_NUKE"
	WorkstateVerdictPendingMR     = "PENDING_MR"
	WorkstateVerdictNeedsRecovery = "NEEDS_RECOVERY"
	WorkstateVerdictNeedsMQSubmit = "NEEDS_MQ_SUBMIT"

	// WorkstateConfidenceHigh is used when the classifier has direct live signals
	// (session running, heartbeat fresh, process alive, active hook) agreeing on a
	// verdict. It should not be emitted when liveness data is missing entirely.
	WorkstateConfidenceHigh = "high"
	// WorkstateConfidenceMedium is used when the verdict relies on persisted state
	// or partial liveness data. Callers should still act, but may want to re-check.
	WorkstateConfidenceMedium = "medium"
	// WorkstateConfidenceLow is used when the classifier has conflicting or
	// missing signals and falls back to conservative State-based behavior.
	WorkstateConfidenceLow = "low"
)

// WorkstateInput contains the lifecycle, git, and merge-queue facts needed to
// classify a polecat consistently across list, recovery, witness, and capacity.
type WorkstateInput struct {
	State                          State
	HookBead                       string
	CleanupStatus                  CleanupStatus
	IgnoreCleanupStatus            bool
	PartialSpawnWithoutDurableHook bool
	PushFailed                     bool
	MRFailed                       bool
	Branch                         string
	GitDirty                       bool
	GitDirtyReason                 string
	StashCount                     int
	UnpushedCommits                int
	GitCheckFailed                 bool
	GitCheckFailedReason           string
	ActiveMR                       string
	ActiveMRBlocker                string
	MQCheckRequired                bool
	HasSubmittableWork             bool
	MQNotRequired                  bool
	AssignedBeadTerminal           bool
	MRSubmitted                    bool
	MQLookupFailed                 bool
	// SessionRunning is true when the tmux session for this polecat currently exists.
	SessionRunning bool `json:"session_running,omitempty"`
	// HeartbeatFresh is true when the polecat heartbeat file exists and is fresh.
	HeartbeatFresh bool `json:"heartbeat_fresh,omitempty"`
	// HeartbeatExists is true when a heartbeat file exists for this polecat.
	HeartbeatExists bool `json:"heartbeat_exists,omitempty"`
	// ProcessAlive is true when the session's agent process is confirmed alive.
	ProcessAlive bool `json:"process_alive,omitempty"`
}

// WorkstateDisposition is the canonical polecat lifecycle decision. It is pure
// policy: callers gather facts, this classifier decides how every subsystem
// should present and count the polecat.
type WorkstateDisposition struct {
	Verdict              string   `json:"verdict"`
	Reason               string   `json:"reason,omitempty"`
	Reusable             bool     `json:"reusable"`
	SafeToNuke           bool     `json:"safe_to_nuke"`
	NeedsRecovery        bool     `json:"needs_recovery"`
	NeedsMQSubmit        bool     `json:"needs_mq_submit"`
	MQStatus             string   `json:"mq_status,omitempty"`
	CountsTowardCapacity bool     `json:"counts_toward_capacity"`
	ReuseStatus          string   `json:"reuse_status,omitempty"`
	Blockers             []string `json:"blockers,omitempty"`
	// Confidence is "high", "medium", or "low" and reflects how much direct
	// liveness evidence supported the verdict. Added for gastown-cet.9.
	Confidence string `json:"confidence,omitempty"`
	// Signals lists the liveness predicates that triggered the verdict. It is
	// empty for idle-path decisions that do not depend on live-session signals.
	Signals []string `json:"signals,omitempty"`
}

// DecideWorkstate returns the canonical disposition for a polecat.
func DecideWorkstate(in WorkstateInput) WorkstateDisposition {
	if in.State != StateIdle {
		return decideNonIdleWorkstate(in)
	}

	d := WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke}
	block := func(reason, blocker string) {
		if d.Reason == "" {
			d.Reason = reason
		}
		if blocker != "" {
			d.Blockers = append(d.Blockers, blocker)
		}
	}

	if in.HookBead != "" && !in.PartialSpawnWithoutDurableHook {
		block("hook-still-set", "has work on hook ("+in.HookBead+")")
	}
	if in.PushFailed {
		block("push-failed", "push_failed=true")
	}
	if in.MRFailed {
		block("mr-failed", "mr_failed=true")
	}
	if !in.IgnoreCleanupStatus && !in.CleanupStatus.IsSafe() {
		reason := "cleanup-" + string(in.CleanupStatus)
		blocker := "cleanup_status=" + string(in.CleanupStatus)
		if in.CleanupStatus == "" {
			reason = "cleanup-unknown"
			blocker = "cleanup_status=<missing>"
		} else if in.CleanupStatus == CleanupUnknown {
			reason = "cleanup-unknown"
		}
		block(reason, blocker)
	}
	if in.GitCheckFailed {
		blocker := in.GitCheckFailedReason
		if blocker == "" {
			blocker = "git_state=unknown"
		}
		block("git-check-failed", blocker)
	}
	if in.GitDirty {
		blocker := in.GitDirtyReason
		if blocker == "" {
			blocker = "git_state=has_uncommitted"
		}
		block("git-dirty", blocker)
	}
	if in.StashCount > 0 {
		block("git-stash", "git_state=has_stash stash_count="+itoa(in.StashCount))
	}
	if in.UnpushedCommits > 0 {
		block("git-unpushed", "git_state=has_unpushed unpushed_commits="+itoa(in.UnpushedCommits))
	}
	activeMRBlocks := in.ActiveMRBlocker != ""
	if activeMRBlocks {
		block("active-mr-open", in.ActiveMRBlocker)
	}

	if len(d.Blockers) > 0 {
		if activeMRBlocks && len(d.Blockers) == 1 {
			d.Verdict = WorkstateVerdictPendingMR
			d.ReuseStatus = "idle-pr-open"
			return d
		}
		d.Verdict = WorkstateVerdictNeedsRecovery
		d.NeedsRecovery = true
		d.CountsTowardCapacity = true
		d.ReuseStatus = "idle-recovery-needed"
		return d
	}

	if in.MQCheckRequired {
		if !in.HasSubmittableWork || in.MQNotRequired {
			d.MQStatus = "not_required"
		} else if in.AssignedBeadTerminal || in.MRSubmitted {
			d.MQStatus = "submitted"
		} else if in.MQLookupFailed {
			d.MQStatus = "unknown"
		} else {
			d.Verdict = WorkstateVerdictNeedsMQSubmit
			d.Reason = "mq-not-submitted"
			d.NeedsRecovery = true
			d.NeedsMQSubmit = true
			d.MQStatus = "not_submitted"
			d.CountsTowardCapacity = true
			d.ReuseStatus = "idle-recovery-needed"
			d.Blockers = append(d.Blockers, "mq_status=not_submitted")
			return d
		}
	}

	d.Reusable = true
	d.SafeToNuke = true
	d.Reason = "reusable"
	if strings.HasPrefix(in.Branch, "polecat/") {
		d.ReuseStatus = "idle-preserved"
	} else {
		d.ReuseStatus = "idle-clean"
	}
	return d
}

// decideNonIdleWorkstate classifies a polecat whose persisted State is not idle.
// It uses live session/heartbeat/process signals to disambiguate a genuinely
// working agent from a dead or stale one. A live, hooked polecat is WORKING
// regardless of whether its persisted state happens to say stalled/done/etc.
// A non-idle polecat without live evidence stays in recovery.
func decideNonIdleWorkstate(in WorkstateInput) WorkstateDisposition {
	d := WorkstateDisposition{CountsTowardCapacity: true}
	stateSignal := "state=" + string(in.State)
	live := isPolecatLive(in)
	hooked := in.HookBead != ""

	switch in.State {
	case StateWorking:
		d.Verdict = WorkstateVerdictWorking
		d.Reason = "working"
		d.Signals = []string{stateSignal}
		if live {
			d.Confidence = WorkstateConfidenceHigh
			d.Signals = append(d.Signals, liveSignals(in)...)
		} else if hasAnyLiveSignal(in) {
			d.Confidence = WorkstateConfidenceMedium
			d.Signals = append(d.Signals, allSignals(in)...)
		} else {
			d.Confidence = WorkstateConfidenceMedium
		}
	default:
		if live && hooked {
			// Live session + active hook = working even if persisted state is
			// review-needed, stalled, done, etc. This is the gastown-cet.9 fix.
			d.Verdict = WorkstateVerdictWorking
			d.Reason = "live-hooked"
			d.Confidence = WorkstateConfidenceHigh
			d.Signals = append([]string{stateSignal}, liveSignals(in)...)
			d.Signals = append(d.Signals, "hook_active")
		} else if live {
			// Session is live but there is no active hook. The agent is up and
			// may be between assignments; do not treat it as a recovery case.
			d.Verdict = WorkstateVerdictWorking
			d.Reason = "live-review"
			d.Confidence = WorkstateConfidenceMedium
			d.Signals = append([]string{stateSignal}, liveSignals(in)...)
		} else {
			d.Verdict = WorkstateVerdictNeedsRecovery
			d.Reason = "stale-session"
			d.NeedsRecovery = true
			d.ReuseStatus = "idle-recovery-needed"
			d.Confidence = WorkstateConfidenceHigh
			d.Signals = append([]string{stateSignal}, allSignals(in)...)
			if !hasAnyLiveSignal(in) {
				// No liveness data at all: fall back to the previous coarse reason
				// so callers see a stable classifier output during rollout.
				d.Reason = "not-idle"
				d.Confidence = WorkstateConfidenceLow
			}
		}
	}
	return d
}

// isPolecatLive returns true when direct liveness evidence confirms the agent
// process is alive. ProcessAlive already incorporates heartbeat freshness when
// a heartbeat file exists (isSessionProcessDead), so an alive process implies a
// fresh heartbeat or a pre-heartbeat session.
func isPolecatLive(in WorkstateInput) bool {
	return in.ProcessAlive || (in.SessionRunning && in.HeartbeatFresh)
}

// hasAnyLiveSignal returns true if any liveness predicate was supplied.
func hasAnyLiveSignal(in WorkstateInput) bool {
	return in.SessionRunning || in.HeartbeatFresh || in.ProcessAlive || in.HeartbeatExists
}

// liveSignals returns the signal strings for conditions that are true.
func liveSignals(in WorkstateInput) []string {
	return signalList(in, true)
}

// allSignals returns every liveness signal with its boolean value.
func allSignals(in WorkstateInput) []string {
	return signalList(in, false)
}

func signalList(in WorkstateInput, onlyTrue bool) []string {
	var signals []string
	add := func(name string, value bool) {
		if onlyTrue && !value {
			return
		}
		signals = append(signals, name+"="+boolString(value))
	}
	add("session_running", in.SessionRunning)
	add("heartbeat_exists", in.HeartbeatExists)
	add("heartbeat_fresh", in.HeartbeatFresh)
	add("process_alive", in.ProcessAlive)
	return signals
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// CanIgnoreStaleCleanupStatus returns true when a dirty persisted
// cleanup_status is older than the direct predicates proving no work is at risk.
// The status remains unsafe globally; callers must opt into this reconciliation
// path only after gathering live git, hook, work, and active-MR facts.
func CanIgnoreStaleCleanupStatus(status CleanupStatus, workTerminal, hookSafe, activeMRSafe, gitSafe bool) bool {
	if !workTerminal || !hookSafe || !activeMRSafe || !gitSafe {
		return false
	}
	switch status {
	case CleanupUncommitted, CleanupStash, CleanupUnpushed:
		return true
	default:
		return false
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
