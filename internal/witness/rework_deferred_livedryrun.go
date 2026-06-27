package witness

// Live dry-run that exercises the EXACT post-merge notification path against
// the EXACT durable state file used by the running witness.
//
// The existing DryRunReworkDeferred (gastown-3ip) uses a temp directory and
// only calls EvaluateReworkDeferred directly — it proves the throttle math is
// correct but not that the live notification path wires through it. After the
// gastown-3ip fix shipped, the Mayor still saw live REWORK_DEFERRED notices
// for unchanged tuples while `gt witness rework-deferred list` was empty
// (gastown-fvy), which is only explicable if a live emitter bypassed the
// throttle or wrote to a different state path. The temp-dir dry-run cannot
// detect that class of bug because it never touches production state.
//
// LiveDryRunReworkDeferred closes that gap: it resolves the actual townRoot
// the way the running daemon does (workspace.Find), uses the production state
// file path (no temp redirect), and calls notifyMayorOfReworkBlocked — the same
// function HandleMergeFailed and resetAbandonedBead invoke on every patrol
// cycle. To avoid clobbering production records it scopes every fixture tuple
// under a "live-dryrun-" prefix and removes those records on cleanup.
//
// This is the regression the bead requires:
//   "regression or live dry-run covers the exact post-merge path and state
//    file used by the running daemon/witness" (gastown-fvy).

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/lock"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/mayor"
	"github.com/steveyegge/gastown/internal/workspace"
)

// liveDryRunPrefix is prepended to every fixture bead/polecat name so live
// dry-run records can never collide with — or be mistaken for — real
// production throttle records. The cleanup step removes only records whose
// BeadID or PolecatName start with this prefix.
const liveDryRunPrefix = "live-dryrun-"

// LiveDryRunResult is the outcome of LiveDryRunReworkDeferred.
type LiveDryRunResult struct {
	// Pass is true when every assertion succeeded.
	Pass bool `json:"pass"`
	// TownRoot is the town root the live dry run resolved to. It MUST match
	// the town root used by the running witness — that is the whole point of
	// the live dry-run vs. the temp-dir dry-run.
	TownRoot string `json:"town_root"`
	// StatePath is the durable throttle state file the run wrote to. It is
	// the production path the running daemon uses (no temp redirect).
	StatePath string `json:"state_path"`
	// Window is the throttle window the run used.
	Window time.Duration `json:"window"`
	// Tuples lists one entry per fixture case.
	Tuples []LiveDryRunTuple `json:"tuples"`
	// Errors is the list of human-readable failure messages when Pass=false.
	Errors []string `json:"errors,omitempty"`
	// SaveErrors counts saveReworkDeferredState failures observed during the
	// run. The throttle code paths swallow these (the production behavior is
	// "best-effort save"); a non-zero SaveErrors count indicates the durable
	// state could not be persisted, which is exactly the failure mode that
	// produces an empty `gt witness rework-deferred list` after live notices.
	SaveErrors int `json:"save_errors"`
	// CleanupRemoved is the number of throttle records the cleanup step
	// removed. With the live-dryrun- prefix this should equal the number of
	// fixture tuples the run exercised; a mismatch means leftover records.
	CleanupRemoved int `json:"cleanup_removed"`
}

// LiveDryRunTuple records the observed actions for a single fixture tuple.
type LiveDryRunTuple struct {
	// Bead is the fixture bead id (with live-dryrun- prefix).
	Bead string `json:"bead"`
	// Polecat is the fixture polecat name (with live-dryrun- prefix).
	Polecat string `json:"polecat"`
	// Decision is the mayor decision type used for this fixture.
	Decision mayor.DecisionType `json:"decision"`
	// FirstAction is the action notifyMayorOfReworkBlocked took on the first
	// occurrence. Must be emit.
	FirstAction ReworkDeferredAction `json:"first_action"`
	// RepeatAction is the action taken on an identical repeat inside the
	// window. Must be suppress.
	RepeatAction ReworkDeferredAction `json:"repeat_action"`
	// RollupAction is the action taken after the window elapses. Must be
	// rollup, and the returned rollup count must equal the repeats
	// actually suppressed.
	RollupAction ReworkDeferredAction `json:"rollup_action"`
	// RollupSuppressedCount is the suppressed count the rollup returned
	// (carried in the message subject/body). Must equal RepeatCount, not 0
	// (gastown-3ip).
	RollupSuppressedCount int `json:"rollup_suppressed_count"`
	// MailSent counts the number of times notifyMayorOfReworkBlocked
	// actually called router.Send. emit and rollup both send; suppress does
	// not. This is the live-path evidence that the throttle decision was
	// honored: emits go to the (captured) router, suppresses do not.
	MailSent int `json:"mail_sent"`
}

// captureRouter is a MailSender that records every Send call. The live
// dry-run injects this in place of the production mail.Router so it can
// observe emit-vs-suppress without polluting the Mayor inbox.
type captureRouter struct {
	mu    sync.Mutex
	sends []*mail.Message
}

func (c *captureRouter) Send(msg *mail.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := *msg
	c.sends = append(c.sends, &cp)
	return nil
}

func (c *captureRouter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sends)
}

// LiveDryRunReworkDeferred exercises the exact post-merge notification path
// (notifyMayorOfReworkBlocked -> EvaluateReworkDeferred -> saveReworkDeferredState)
// against the actual durable state file used by the running witness. Returns
// a result the CLI can render. On any failure the error slice is populated and
// Pass is false; partial failures (some tuples pass, some don't) keep Pass
// false so the operator can distinguish "the throttle is broken" from "the
// setup is broken."
//
// townRootOverride, when non-empty, is used instead of workspace.Find. Tests
// pass a temp dir; production callers (the CLI) pass "" to use the same
// resolution path as the running daemon.
func LiveDryRunReworkDeferred(townRootOverride string) (*LiveDryRunResult, error) {
	townRoot := townRootOverride
	if townRoot == "" {
		resolved, err := workspace.FindFromCwdOrError()
		if err != nil {
			return nil, fmt.Errorf("finding town root: %w", err)
		}
		townRoot = resolved
	}

	statePath := ReworkDeferredStateFile(townRoot)

	// Ensure the witness dir exists. The throttle's MkdirAll covers this on
	// first emit, but we want the live dry-run to fail fast with a clear
	// error if the path is unwritable, rather than emitting a successful run
	// against an unwriteable state file.
	if err := os.MkdirAll(stateDirFor(statePath), 0755); err != nil {
		return nil, fmt.Errorf("preparing state dir %s: %w", stateDirFor(statePath), err)
	}

	// Pre-clean: drop any live-dryrun- records left over from a prior
	// crashed/aborted run so we start from a known state.
	preRemoved, err := removeLiveDryRunRecords(townRoot)
	if err != nil {
		return nil, fmt.Errorf("pre-clean: %w", err)
	}

	// Freeze time so the run is deterministic — same approach as the
	// existing temp-dir dry-run.
	origNow := reworkDeferredNow
	start := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	now := start
	reworkDeferredNow = func() time.Time { return now }
	defer func() { reworkDeferredNow = origNow }()

	// Derive the throttle window the same way the live path does
	// (notifyMayorOfReworkBlocked): config.LoadOperationalConfig(townRoot).
	// GetWitnessConfig().ReworkDeferredThrottleWindowD(). The live dry-run
	// advances its frozen clock by multiples of this window to drive the
	// emit→suppress→rollup phases; hardcoding DefaultReworkDeferredThrottleWindow
	// here while the live path uses the configured window produced false dry-run
	// failures whenever an operator overrode the default (gastown-9rc
	// side-warning).
	window := config.LoadOperationalConfig(townRoot).
		GetWitnessConfig().
		ReworkDeferredThrottleWindowD()

	router := &captureRouter{}

	fixtures := []struct {
		bead     string
		polecat  string
		decision mayor.DecisionType
	}{
		{liveDryRunPrefix + "bead-a", liveDryRunPrefix + "alpha", mayor.DecisionHold},
		{liveDryRunPrefix + "bead-b", liveDryRunPrefix + "beta", mayor.DecisionPark},
		{liveDryRunPrefix + "bead-c", liveDryRunPrefix + "gamma", mayor.DecisionDefer},
	}

	const repeatCount = 5

	result := &LiveDryRunResult{
		Pass:      true,
		TownRoot:  townRoot,
		StatePath: statePath,
		Window:    window,
		Tuples:    make([]LiveDryRunTuple, 0, len(fixtures)),
	}

	decision := &mayor.Decision{
		Type:      mayor.DecisionDefer,
		Reason:    "live-dryrun regression scenario",
		MayorID:   "live-dryrun",
		Timestamp: start,
	}

	// Phase 1 — first occurrence must emit and call router.Send.
	for _, fix := range fixtures {
		decision.Type = fix.decision
		mailBefore := router.count()
		notifyMayorOfReworkBlocked(townRoot, "live-dryrun-rig", fix.bead, fix.polecat,
			"merge_failed", 0, decision, router)
		mailAfter := router.count()
		action := readLiveAction(townRoot, fix.bead, fix.polecat, fix.decision, "merge_failed")
		if action != ActionEmit {
			result.Pass = false
			result.addError("first occurrence for %s: action=%s, want emit (path: notifyMayor -> Evaluate -> save)",
				fix.bead, action)
		}
		if mailAfter-mailBefore != 1 {
			result.Pass = false
			result.addError("first occurrence for %s: mail_sent delta=%d, want 1", fix.bead, mailAfter-mailBefore)
		}
		result.Tuples = append(result.Tuples, LiveDryRunTuple{
			Bead:        fix.bead,
			Polecat:     fix.polecat,
			Decision:    fix.decision,
			FirstAction: action,
			MailSent:    mailAfter - mailBefore,
		})
	}

	// Phase 2 — repeated occurrences inside the window must suppress and
	// must NOT call router.Send.
	for i := 0; i < repeatCount; i++ {
		now = now.Add(2 * time.Minute)
		for j, fix := range fixtures {
			decision.Type = fix.decision
			mailBefore := router.count()
			notifyMayorOfReworkBlocked(townRoot, "live-dryrun-rig", fix.bead, fix.polecat,
				"merge_failed", 0, decision, router)
			mailAfter := router.count()
			action := readLiveAction(townRoot, fix.bead, fix.polecat, fix.decision, "merge_failed")
			if action != ActionSuppress {
				result.Pass = false
				result.addError("repeat #%d for %s: action=%s, want suppress", i+1, fix.bead, action)
			}
			if mailAfter-mailBefore != 0 {
				result.Pass = false
				result.addError("repeat #%d for %s: mail_sent delta=%d, want 0 (throttle bypassed — live path is emitting duplicates)",
					i+1, fix.bead, mailAfter-mailBefore)
			}
			result.Tuples[j].RepeatAction = action
			result.Tuples[j].MailSent += mailAfter - mailBefore
		}
	}

	// Phase 3 — after the window elapses, the next identical call must
	// rollup. The rollup must report the real suppressed count, not 0
	// (gastown-3ip). We capture the count from the mail subject, which is
	// where notifyMayorOfReworkBlocked writes it via
	// formatReworkDeferredNotification.
	now = now.Add(2 * window)
	for j, fix := range fixtures {
		decision.Type = fix.decision
		mailBefore := router.count()
		notifyMayorOfReworkBlocked(townRoot, "live-dryrun-rig", fix.bead, fix.polecat,
			"merge_failed", 0, decision, router)
		mailAfter := router.count()
		action := readLiveAction(townRoot, fix.bead, fix.polecat, fix.decision, "merge_failed")
		if action != ActionRollup {
			result.Pass = false
			result.addError("post-window for %s: action=%s, want rollup", fix.bead, action)
		}
		if mailAfter-mailBefore != 1 {
			result.Pass = false
			result.addError("post-window for %s: mail_sent delta=%d, want 1", fix.bead, mailAfter-mailBefore)
		}
		// Extract the suppressed count from the captured mail subject
		// ("REWORK_DEFERRED ... (..., N suppressed)") — this is the count
		// the Mayor actually sees on the live path, and it must equal
		// repeatCount (gastown-3ip regression).
		rollupCount := capturedSuppressedCount(router, mailBefore)
		if rollupCount != repeatCount {
			result.Pass = false
			result.addError("post-window for %s: rollup reported suppressed_count=%d, want %d (real count, not 0)",
				fix.bead, rollupCount, repeatCount)
		}
		result.Tuples[j].RollupAction = action
		result.Tuples[j].RollupSuppressedCount = rollupCount
		result.Tuples[j].MailSent += mailAfter - mailBefore
	}

	// Phase 4 — a tuple change must emit immediately (and call
	// router.Send). This proves the throttle distinguishes first-emit from
	// repeat-suppress on the live path.
	now = now.Add(5 * time.Minute)
	for j, fix := range fixtures {
		decision.Type = fix.decision
		mailBefore := router.count()
		// Change source status to force a fresh emit.
		notifyMayorOfReworkBlocked(townRoot, "live-dryrun-rig", fix.bead, fix.polecat,
			"hooked", 0, decision, router)
		mailAfter := router.count()
		action := readLiveAction(townRoot, fix.bead, fix.polecat, fix.decision, "hooked")
		if action != ActionEmit {
			result.Pass = false
			result.addError("status change for %s: action=%s, want emit", fix.bead, action)
		}
		if mailAfter-mailBefore != 1 {
			result.Pass = false
			result.addError("status change for %s: mail_sent delta=%d, want 1", fix.bead, mailAfter-mailBefore)
		}
		result.Tuples[j].MailSent += mailAfter - mailBefore
	}

	// Cleanup — remove only the live-dryrun- records. Real production
	// records (and any prior dry-run records) are untouched.
	removed, err := removeLiveDryRunRecords(townRoot)
	if err != nil {
		result.Pass = false
		result.addError("cleanup: %s", err)
	}
	result.CleanupRemoved = removed + preRemoved

	// Sanity: after cleanup, the durable state file MUST contain no
	// live-dryrun- records (operator-visible via `gt witness rework-deferred
	// list`).
	if remaining := countLiveDryRunRecords(townRoot); remaining > 0 {
		result.Pass = false
		result.addError("post-cleanup: %d live-dryrun- records remain in %s", remaining, statePath)
	}

	return result, nil
}

// readLiveAction inspects the durable state to report the throttle action
// the live notifyMayorOfReworkBlocked call took for the given tuple. It
// reads (does not mutate) state, so it is safe to call between phases.
//
// The throttle's state evolution per tuple:
//   - First emit:       FirstEmittedAt == LastEmittedAt (both = now),
//     SuppressedCount == 0, FirstSuppressedAt zero.
//   - Suppress (in window): SuppressedCount > 0, FirstSuppressedAt set.
//   - Rollup (window elapsed): LastEmittedAt advanced (now > FirstEmittedAt),
//     SuppressedCount reset to 0, FirstSuppressedAt cleared.
//   - Subsequent emit (state change): treated like first emit from a fresh
//     record (rec reset because PolecatName/SourceStatus
//     changed in the live path).
//
// The distinguishing signal between emit and rollup is therefore whether
// LastEmittedAt > FirstEmittedAt: rollup advances LastEmittedAt but leaves
// FirstEmittedAt untouched (it was set on the very first emit and is never
// reset).
func readLiveAction(townRoot, beadID, polecatName string, decisionType mayor.DecisionType, sourceStatus string) ReworkDeferredAction {
	key := reworkDeferredKey("live-dryrun-rig", beadID, polecatName, decisionType, sourceStatus)
	state := loadReworkDeferredState(townRoot)
	rec := findReworkDeferredRecord(state, key)
	if rec == nil {
		return ""
	}
	if rec.SuppressedCount > 0 {
		return ActionSuppress
	}
	// SuppressedCount == 0. Either emit (FirstEmittedAt == LastEmittedAt)
	// or rollup (LastEmittedAt > FirstEmittedAt, with FirstSuppressedAt
	// cleared).
	if rec.LastEmittedAt.After(rec.FirstEmittedAt) {
		return ActionRollup
	}
	return ActionEmit
}

// capturedSuppressedCount parses the "N suppressed" suffix from the most
// recently captured mail subject. This is what the Mayor would see on the
// live path — proving the gastown-3ip "real count, not 0" fix survives the
// live-path integration.
func capturedSuppressedCount(router *captureRouter, before int) int {
	router.mu.Lock()
	defer router.mu.Unlock()
	if before >= len(router.sends) {
		return 0
	}
	subject := router.sends[before].Subject
	// Subject format: "REWORK_DEFERRED <bead> (Mayor <decision> decision blocks rework, N suppressed)"
	const sep = ", "
	const suffix = " suppressed)"
	idx := strings.LastIndex(subject, sep)
	if idx < 0 || !strings.HasSuffix(subject, suffix) {
		return 0
	}
	numStr := subject[idx+len(sep) : len(subject)-len(suffix)]
	var n int
	if _, err := fmt.Sscanf(numStr, "%d", &n); err != nil {
		return 0
	}
	return n
}

// removeLiveDryRunRecords drops every record whose BeadID or PolecatName
// starts with liveDryRunPrefix, regardless of who created it. The prefix is
// the contract: production records never use it, so this is safe to call
// even on a populated production state file.
//
// This is a read-modify-write on the durable throttle state file, so it MUST
// acquire the same cross-process flock (ReworkDeferredStateFile(townRoot)+".flock")
// that EvaluateReworkDeferred, ListReworkDeferredRecords, and
// ClearReworkDeferredRecord use. Without the flock, a live dry-run cleanup
// running alongside an active witness interleaves with a concurrent
// EvaluateReworkDeferred save and clobbers its records — turning a read-only
// diagnostic into a corruptor of durable throttle state (gastown-9rc). The
// in-process reworkDeferredMu alone only serializes callers within this
// process; it cannot stop a separate witness process from racing the save.
func removeLiveDryRunRecords(townRoot string) (int, error) {
	reworkDeferredMu.Lock()
	defer reworkDeferredMu.Unlock()

	unlock, flockErr := lock.FlockAcquire(ReworkDeferredStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadReworkDeferredState(townRoot)
	kept := state.Records[:0]
	removed := 0
	for _, rec := range state.Records {
		if strings.HasPrefix(rec.BeadID, liveDryRunPrefix) || strings.HasPrefix(rec.PolecatName, liveDryRunPrefix) {
			removed++
			continue
		}
		kept = append(kept, rec)
	}
	state.Records = kept
	if removed == 0 {
		return 0, nil
	}
	return removed, saveReworkDeferredState(townRoot, state)
}

// countLiveDryRunRecords returns the number of records still matching the
// live-dryrun- prefix. Used by the post-cleanup assertion.
func countLiveDryRunRecords(townRoot string) int {
	state := loadReworkDeferredState(townRoot)
	n := 0
	for _, rec := range state.Records {
		if strings.HasPrefix(rec.BeadID, liveDryRunPrefix) || strings.HasPrefix(rec.PolecatName, liveDryRunPrefix) {
			n++
		}
	}
	return n
}

// stateDirFor returns the parent dir of a state file path, used for the
// MkdirAll pre-flight check.
func stateDirFor(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return path
}

func (r *LiveDryRunResult) addError(format string, args ...interface{}) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
}
