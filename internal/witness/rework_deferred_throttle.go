package witness

// Throttle duplicate REWORK_DEFERRED notifications sent to the Mayor when the
// automated rework router is blocked by an active defer/hold/park decision.
//
// Without throttling, every patrol cycle that re-detects the same blocked
// (rig, bead, polecat, decision, source status) tuple emits a fresh mail message,
// wasting the Mayor's context on a signal that has not changed. The first
// occurrence is always emitted (the Mayor needs to know the block is in effect),
// and any change to the tuple also emits immediately so the Mayor sees the new
// state. Identical repeats inside the throttle window are suppressed and counted;
// when the window elapses, the next emit is a rollup that includes the suppressed
// count so no evidence is lost.

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/lock"
	"github.com/steveyegge/gastown/internal/mayor"
)

// ReworkDeferredAction describes what callers should do with a REWORK_DEFERRED
// notification that the throttle has evaluated.
type ReworkDeferredAction string

const (
	// ActionEmit means the caller should send a fresh REWORK_DEFERRED message
	// (first occurrence or a state change).
	ActionEmit ReworkDeferredAction = "emit"
	// ActionSuppress means the caller's message is identical to a recent emit
	// and the throttle window has not elapsed; the message is dropped but the
	// suppressed count is incremented.
	ActionSuppress ReworkDeferredAction = "suppress"
	// ActionRollup means the throttle window has elapsed; the caller should
	// emit a rollup message summarizing the suppressed count and then reset
	// the counter.
	ActionRollup ReworkDeferredAction = "rollup"
)

// ReworkDeferredRecord is the durable per-tuple bookkeeping for one
// (rig, bead, polecat, decision, source status) tuple. It is keyed by a hash of
// those five fields so that callers can look up state in O(1) without scanning
// the full record list.
type ReworkDeferredRecord struct {
	// Key is the canonical hash of the tuple.
	Key string `json:"key"`

	// RigName is the rig that produced the notification.
	RigName string `json:"rig_name"`
	// BeadID is the source bead the decision applies to.
	BeadID string `json:"bead_id"`
	// PolecatName is the polecat whose rework was blocked.
	PolecatName string `json:"polecat_name"`
	// MayorDecision is the active decision type (defer/hold/park) that blocked
	// the rework.
	MayorDecision string `json:"mayor_decision"`
	// SourceStatus is the witness-side source of the blocked-rework attempt
	// (e.g., "merge_failed", "hooked", "in_progress"). The throttle emits
	// immediately when this changes, since it usually means the upstream
	// failure mode changed.
	SourceStatus string `json:"source_status"`

	// FirstEmittedAt is when the first (un-throttled) message was sent.
	FirstEmittedAt time.Time `json:"first_emitted_at"`
	// LastEmittedAt is when the most recent un-throttled message was sent.
	// Rollups reset SuppressedCount and update this to "now".
	LastEmittedAt time.Time `json:"last_emitted_at"`
	// LastEmittedReason is the reason string from the most recent emit. It is
	// recorded (not used as a key) so that audit logs can show why the
	// original block fired.
	LastEmittedReason string `json:"last_emitted_reason,omitempty"`

	// SuppressedCount is the number of identical-repeat attempts that were
	// dropped by the throttle since the most recent un-throttled emit.
	SuppressedCount int `json:"suppressed_count"`
	// FirstSuppressedAt is the timestamp of the first suppression in the
	// current window; rollup messages use it to describe the window the
	// rollup covers.
	FirstSuppressedAt time.Time `json:"first_suppressed_at,omitempty"`
}

// ReworkDeferredState is the durable store for REWORK_DEFERRED throttle records.
// Records are keyed by hash and held in insertion order in the slice for
// deterministic serialization. Records whose throttle window has long since
// elapsed are not actively pruned — the file is small (one entry per blocked
// tuple) and pruning on read would add complexity without changing behavior
// (the next emit creates a fresh record via the "change detected" path).
type ReworkDeferredState struct {
	Records    []*ReworkDeferredRecord `json:"records"`
	LastUpdate time.Time               `json:"last_updated"`

	// mu serializes in-process access. Cross-process safety comes from the
	// flock on the state file (see reworkDeferredMu below).
	mu sync.Mutex `json:"-"`
}

// reworkDeferredMu serializes in-process access to the throttle state file.
// flock on the state file is the cross-process companion.
var reworkDeferredMu sync.Mutex

// reworkDeferredNow is overridable in tests.
var reworkDeferredNow = time.Now

// ReworkDeferredStateFile is the durable path for throttle bookkeeping. Exposed
// as a variable so tests can redirect to a temp directory.
var ReworkDeferredStateFile = func(townRoot string) string {
	return filepath.Join(townRoot, "witness", "rework-deferred-throttle.json")
}

func loadReworkDeferredState(townRoot string) *ReworkDeferredState {
	path := ReworkDeferredStateFile(townRoot)
	data, err := os.ReadFile(path) //nolint:gosec // G304: path from trusted townRoot
	if err != nil {
		return &ReworkDeferredState{Records: []*ReworkDeferredRecord{}}
	}
	var state ReworkDeferredState
	if err := json.Unmarshal(data, &state); err != nil {
		return &ReworkDeferredState{Records: []*ReworkDeferredRecord{}}
	}
	if state.Records == nil {
		state.Records = []*ReworkDeferredRecord{}
	}
	return &state
}

func saveReworkDeferredState(townRoot string, state *ReworkDeferredState) error {
	path := ReworkDeferredStateFile(townRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating witness dir: %w", err)
	}
	state.LastUpdate = reworkDeferredNow().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling rework-deferred throttle state: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}

// reworkDeferredKey builds a stable hash for the (rig, bead, polecat, decision,
// source status) tuple. Empty fields are still part of the key so callers cannot
// accidentally collapse distinct tuples.
func reworkDeferredKey(rigName, beadID, polecatName string, decisionType mayor.DecisionType, sourceStatus string) string {
	h := sha1.New() //nolint:gosec // non-cryptographic identifier
	fmt.Fprintf(h, "rig=%s|bead=%s|polecat=%s|decision=%s|status=%s",
		rigName, beadID, polecatName, decisionType, sourceStatus)
	return hex.EncodeToString(h.Sum(nil))
}

// findReworkDeferredRecord looks up the record for the given key. The state
// must already be locked.
func findReworkDeferredRecord(state *ReworkDeferredState, key string) *ReworkDeferredRecord {
	for _, rec := range state.Records {
		if rec.Key == key {
			return rec
		}
	}
	return nil
}

// ReworkDeferredDecision is the result of evaluating whether a fresh
// REWORK_DEFERRED mail message should be sent. Callers use Action to decide
// whether to send and how to phrase the body; Record is the (possibly new or
// updated) durable record describing the tuple.
type ReworkDeferredDecision struct {
	// Action is what the caller should do with the notification.
	Action ReworkDeferredAction
	// Record is the durable record for the tuple. For Action==emit/rollup it
	// has just been updated; for Action==suppress it has just had its
	// SuppressedCount incremented.
	Record *ReworkDeferredRecord
	// Window is the throttle window used for this decision. Returned so
	// callers can include it in rollup messages without re-deriving it.
	Window time.Duration
}

// EvaluateReworkDeferred applies the throttle and returns the action the caller
// should take. The durable state is updated in place; the returned Record is a
// snapshot describing what the caller should report. For a rollup the snapshot
// carries the suppressed count from the just-closed window (so rollup messages
// read "N suppressed"), while the durable record is reset to zero for the next
// window. Errors loading or saving state are non-fatal: on read failure the
// throttle is open (the caller emits) and on save failure the in-memory update
// is still visible to the caller.
//
// Throttle rules:
//   - First occurrence: emit, persist a fresh record.
//   - Tuple change (decision/polecat/source status): emit, reset the record.
//   - Identical repeat inside the throttle window: suppress, increment
//     SuppressedCount, persist.
//   - Identical repeat after the throttle window: rollup, reset
//     SuppressedCount to 0, persist.
func EvaluateReworkDeferred(townRoot, rigName, beadID, polecatName, sourceStatus, reason string, decisionType mayor.DecisionType, window time.Duration) ReworkDeferredDecision {
	if window <= 0 {
		// A non-positive window disables throttling entirely; emit every time.
		return ReworkDeferredDecision{Action: ActionEmit, Window: window}
	}

	reworkDeferredMu.Lock()
	defer reworkDeferredMu.Unlock()

	unlock, flockErr := lock.FlockAcquire(ReworkDeferredStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	key := reworkDeferredKey(rigName, beadID, polecatName, decisionType, sourceStatus)
	now := reworkDeferredNow().UTC()

	state := loadReworkDeferredState(townRoot)
	rec := findReworkDeferredRecord(state, key)

	// No record, or tuple change → emit a fresh message and reset bookkeeping.
	if rec == nil ||
		rec.MayorDecision != string(decisionType) ||
		rec.PolecatName != polecatName ||
		rec.SourceStatus != sourceStatus {
		rec = &ReworkDeferredRecord{
			Key:               key,
			RigName:           rigName,
			BeadID:            beadID,
			PolecatName:       polecatName,
			MayorDecision:     string(decisionType),
			SourceStatus:      sourceStatus,
			FirstEmittedAt:    now,
			LastEmittedAt:     now,
			LastEmittedReason: reason,
			SuppressedCount:   0,
		}
		state.Records = append(state.Records, rec)
		_ = saveReworkDeferredState(townRoot, state) // non-fatal
		return ReworkDeferredDecision{Action: ActionEmit, Record: rec, Window: window}
	}

	// Identical tuple. Decide between suppress and rollup based on the
	// elapsed time since the last un-throttled emit.
	elapsed := now.Sub(rec.LastEmittedAt)
	if elapsed < window {
		rec.SuppressedCount++
		if rec.FirstSuppressedAt.IsZero() {
			rec.FirstSuppressedAt = now
		}
		_ = saveReworkDeferredState(townRoot, state) // non-fatal
		return ReworkDeferredDecision{Action: ActionSuppress, Record: rec, Window: window}
	}

	// Window elapsed. Roll up: emit a message that includes the suppressed
	// count and reset the counter. The rollup is itself an emit, so
	// LastEmittedReason is preserved for audit.
	//
	// The caller formats the rollup subject/body from the returned record's
	// SuppressedCount, so that count must reflect the number actually
	// suppressed during the just-closed window. We therefore snapshot the
	// pre-reset count, reset the durable counter in place (SuppressedCount=0,
	// FirstSuppressedAt zeroed, LastEmittedAt advanced to now), and return a
	// copy that carries the real count. Resetting the durable record in place
	// — rather than swapping in a fresh struct — preserves the slice slot and
	// key so the tuple's identity stays stable across windows.
	suppressedForRollup := rec.SuppressedCount
	rec.LastEmittedAt = now
	rec.SuppressedCount = 0
	rec.FirstSuppressedAt = time.Time{}
	_ = saveReworkDeferredState(townRoot, state) // non-fatal

	// Returned record is a copy so callers cannot observe the just-reset
	// durable counter through it. It carries the real suppressed count so
	// "N suppressed" rollups are accurate (gastown-3ip).
	reported := *rec
	reported.SuppressedCount = suppressedForRollup
	return ReworkDeferredDecision{Action: ActionRollup, Record: &reported, Window: window}
}

// ListReworkDeferredRecords returns a snapshot of all throttle records, sorted
// by BeadID then PolecatName. It is intended for diagnostics (e.g., `gt witness
// rework-deferred list`) and for tests that need to inspect the durable state
// without touching the file directly.
func ListReworkDeferredRecords(townRoot string) []*ReworkDeferredRecord {
	reworkDeferredMu.Lock()
	defer reworkDeferredMu.Unlock()

	unlock, flockErr := lock.FlockAcquire(ReworkDeferredStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadReworkDeferredState(townRoot)
	out := make([]*ReworkDeferredRecord, 0, len(state.Records))
	for _, rec := range state.Records {
		copyRec := *rec
		out = append(out, &copyRec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].BeadID != out[j].BeadID {
			return out[i].BeadID < out[j].BeadID
		}
		return out[i].PolecatName < out[j].PolecatName
	})
	return out
}

// ClearReworkDeferredRecord removes the record for the given key, if any. It is
// used by tests to reset state between cases. Production code should not call
// this — once a tuple is throttled, the next emit/rollup is the natural
// lifecycle event.
func ClearReworkDeferredRecord(townRoot, key string) error {
	reworkDeferredMu.Lock()
	defer reworkDeferredMu.Unlock()

	unlock, flockErr := lock.FlockAcquire(ReworkDeferredStateFile(townRoot) + ".flock")
	if flockErr == nil {
		defer unlock()
	}

	state := loadReworkDeferredState(townRoot)
	filtered := state.Records[:0]
	removed := false
	for _, rec := range state.Records {
		if rec.Key == key {
			removed = true
			continue
		}
		filtered = append(filtered, rec)
	}
	state.Records = filtered
	if !removed {
		return nil
	}
	return saveReworkDeferredState(townRoot, state)
}
