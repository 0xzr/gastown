package mayor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/lock"
)

// DecisionType is an explicit Mayor decision about automated rework handling
// for a source bead. It is durable and binding: when an active defer/hold/park
// decision exists, the rework router must not spawn or nudge a polecat for
// that bead unless a newer explicit override (resume) is recorded.
type DecisionType string

const (
	// DecisionDefer blocks automated rework; the bead is intentionally deprioritized.
	DecisionDefer DecisionType = "defer"

	// DecisionHold blocks automated rework; awaiting further Mayor input.
	DecisionHold DecisionType = "hold"

	// DecisionPark blocks automated rework; the bead is paused indefinitely.
	DecisionPark DecisionType = "park"

	// DecisionResume explicitly overrides a prior defer/hold/park and allows
	// automated rework to resume.
	DecisionResume DecisionType = "resume"
)

// BlocksRework returns true for decisions that prevent automated rework spawn/nudge.
func (d DecisionType) BlocksRework() bool {
	switch d {
	case DecisionDefer, DecisionHold, DecisionPark:
		return true
	default:
		return false
	}
}

// IsValid returns true if the decision type is recognized.
func (d DecisionType) IsValid() bool {
	switch d {
	case DecisionDefer, DecisionHold, DecisionPark, DecisionResume:
		return true
	default:
		return false
	}
}

// String returns the string representation.
func (d DecisionType) String() string {
	return string(d)
}

// Decision records a single explicit Mayor decision about a source bead.
type Decision struct {
	// BeadID is the source bead this decision applies to (e.g., "gastown-cet.7").
	BeadID string `json:"bead_id"`

	// Type is the Mayor decision.
	Type DecisionType `json:"type"`

	// Reason is an optional human-readable explanation.
	Reason string `json:"reason,omitempty"`

	// MayorID identifies the Mayor actor (e.g., "mayor/acp" or a human name).
	MayorID string `json:"mayor_id,omitempty"`

	// Timestamp is when the decision was recorded.
	Timestamp time.Time `json:"timestamp"`
}

// DecisionsState is the durable store of Mayor decisions about source beads.
// It is intentionally append-only: each decision is recorded so the log can
// be audited. Active decision resolution looks at the most recent decision per
// bead and treats resume as an explicit override.
type DecisionsState struct {
	Decisions []*Decision `json:"decisions"`

	// mu protects in-memory mutations. Cross-process safety is provided by
	// flock around read-modify-write operations.
	mu sync.Mutex `json:"-"`
}

// ErrDecisionNotFound indicates no decision exists for the requested bead.
var ErrDecisionNotFound = errors.New("no decision found for bead")

// DecisionsFile returns the path to the durable Mayor decision state file.
func DecisionsFile(townRoot string) string {
	return filepath.Join(townRoot, "mayor", "decisions.json")
}

// LoadDecisions loads the durable Mayor decision state from disk.
// Returns an empty state if the file does not exist.
func LoadDecisions(townRoot string) (*DecisionsState, error) {
	path := DecisionsFile(townRoot)

	// Ensure directory exists so future saves succeed even when empty.
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, fmt.Errorf("creating mayor decisions directory: %w", err)
	}

	data, err := os.ReadFile(path) //nolint:gosec // G304: path constructed from trusted townRoot
	if err != nil {
		if os.IsNotExist(err) {
			return &DecisionsState{Decisions: []*Decision{}}, nil
		}
		return nil, fmt.Errorf("reading mayor decisions: %w", err)
	}

	var state DecisionsState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing mayor decisions: %w", err)
	}

	if state.Decisions == nil {
		state.Decisions = []*Decision{}
	}

	return &state, nil
}

// SaveDecisions persists the decision state atomically (write-then-rename).
func SaveDecisions(townRoot string, state *DecisionsState) error {
	if state == nil {
		return errors.New("cannot save nil decisions state")
	}

	path := DecisionsFile(townRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return fmt.Errorf("creating mayor decisions directory: %w", err)
	}

	state.mu.Lock()
	data, err := json.MarshalIndent(state, "", "  ")
	state.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshaling mayor decisions: %w", err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("writing mayor decisions: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		// Best-effort cleanup; do not shadow the rename error.
		_ = os.Remove(tmpPath)
		return fmt.Errorf("committing mayor decisions: %w", err)
	}

	return nil
}

// loadAndLock loads the decision state while holding an exclusive flock.
// The caller must call unlock to release the lock.
func loadAndLock(townRoot string) (state *DecisionsState, unlock func(), err error) {
	path := DecisionsFile(townRoot)

	// Ensure the directory exists before acquiring the flock: OpenFile on the
	// .flock path fails with ENOENT if the parent dir is absent. This matters
	// on first-ever use, when mayor/ has not been created yet.
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, nil, fmt.Errorf("creating mayor decisions directory: %w", err)
	}

	unlock, flockErr := lock.FlockAcquire(path + ".flock")
	if flockErr != nil {
		return nil, nil, fmt.Errorf("acquiring decisions lock: %w", flockErr)
	}

	state, err = LoadDecisions(townRoot)
	if err != nil {
		unlock()
		return nil, nil, err
	}

	return state, unlock, nil
}

// ActiveDecision returns the decision currently in effect for a source bead.
// The most recent decision wins; an explicit resume cancels a prior
// defer/hold/park and returns (nil, ErrDecisionNotFound). Returns
// ErrDecisionNotFound when no decision exists.
func (s *DecisionsState) ActiveDecision(beadID string) (*Decision, error) {
	if s == nil {
		return nil, ErrDecisionNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	latest := s.latestLocked(beadID)
	if latest == nil {
		return nil, ErrDecisionNotFound
	}
	if latest.Type.BlocksRework() {
		return latest, nil
	}
	// resume or other non-blocking decision: treat as no active blocker.
	return nil, ErrDecisionNotFound
}

// PriorBlockingDecision returns the most recent blocking (defer/hold/park)
// decision recorded for a bead, regardless of whether it was later overridden
// by a resume. It is used to report whether a resume actually overrode a prior
// block. Returns ErrDecisionNotFound when no blocking decision was ever recorded.
func (s *DecisionsState) PriorBlockingDecision(beadID string) (*Decision, error) {
	if s == nil {
		return nil, ErrDecisionNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var latest *Decision
	for _, d := range s.Decisions {
		if d == nil {
			continue
		}
		if strings.TrimSpace(d.BeadID) != beadID {
			continue
		}
		if !d.Type.BlocksRework() {
			continue
		}
		if latest == nil || d.Timestamp.After(latest.Timestamp) {
			latest = d
		}
	}
	if latest == nil {
		return nil, ErrDecisionNotFound
	}
	return latest, nil
}

// latestLocked returns the most recent decision for a bead without acquiring
// the mutex. The caller must hold s.mu.
func (s *DecisionsState) latestLocked(beadID string) *Decision {
	beadID = strings.TrimSpace(beadID)
	if beadID == "" {
		return nil
	}
	var latest *Decision
	for _, d := range s.Decisions {
		if d == nil {
			continue
		}
		if strings.TrimSpace(d.BeadID) != beadID {
			continue
		}
		// Most recent decision for this bead wins. On equal timestamps,
		// later in the slice wins (consistent with append-only ordering).
		if latest == nil || d.Timestamp.After(latest.Timestamp) {
			latest = d
		}
	}
	return latest
}

// RecordDecision records a new explicit Mayor decision and persists the state.
// It returns the recorded Decision (with timestamp set) and an error if the
// decision type is invalid or persistence fails.
func RecordDecision(townRoot, beadID, mayorID string, decisionType DecisionType, reason string) (*Decision, error) {
	beadID = strings.TrimSpace(beadID)
	if beadID == "" {
		return nil, errors.New("beadID is required")
	}
	if !decisionType.IsValid() {
		return nil, fmt.Errorf("invalid decision type %q", decisionType)
	}

	state, unlock, err := loadAndLock(townRoot)
	if err != nil {
		return nil, err
	}
	defer unlock()

	d := &Decision{
		BeadID:    beadID,
		Type:      decisionType,
		Reason:    strings.TrimSpace(reason),
		MayorID:   strings.TrimSpace(mayorID),
		Timestamp: time.Now().UTC(),
	}

	state.mu.Lock()
	state.Decisions = append(state.Decisions, d)
	state.mu.Unlock()

	if err := SaveDecisions(townRoot, state); err != nil {
		return nil, err
	}

	return d, nil
}
