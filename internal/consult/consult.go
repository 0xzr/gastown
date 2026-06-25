// Package consult implements the Mayor's durable consult/escalation path to
// stronger models (Codex, Opus) for high-risk decisions.
//
// Background
//
// The Mayor is the cross-rig coordinator in Gas Town. Its day-to-day dispatch
// must stay lightweight (sling work, file escalations). However, certain
// classes of decision are too consequential for the Mayor's default model:
//
//   - merge policy changes that affect the whole town;
//   - Witness/Refinery override or manual intervention;
//   - repeated recovery loops (same fingerprint fires N times);
//   - ambiguous operator directives that need a stronger model to parse;
//   - low-confidence model output where the Mayor would otherwise guess.
//
// For these, the Mayor must consult. The consult packet is a durable bead so
// the question, context, options, and resulting decision all survive polecat
// restarts and Dolt commits. It is intentionally lighter than `gt escalate`:
// no external notifications by default (no email/SMS/Slack), routing is via
// gt mail to one of two configurable addresses (Codex and Opus), and the
// packet is closed with a decision that is mirrored back onto the source bead.
//
// Same-failure loop detection
//
// The `LoopDetector` watches for repeated identical fingerprints in either
// the escalation log or the consult log. When a fingerprint fires
// `LoopThreshold` times within `LoopWindow`, the detector returns
// `LoopActionConsult` (or `LoopActionEscalate` at higher counts) so the
// caller can stop blindly re-running the same dispatch.
package consult

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// TriggerClass identifies the kind of high-risk situation that warrants a
// consult. The class is recorded on the consult bead so the receiving model
// can scope its reasoning.
type TriggerClass string

const (
	// TriggerMergePolicy covers changes to the town's merge / refinery rules.
	TriggerMergePolicy TriggerClass = "merge_policy"
	// TriggerWitnessRefineryOverride covers manual Witness or Refinery
	// overrides (manual bypass, emergency cleanup, hook reset, etc.).
	TriggerWitnessRefineryOverride TriggerClass = "witness_refinery_override"
	// TriggerRecoveryLoop covers "same failure keeps firing" patterns
	// surfaced either by the LoopDetector or by human observation.
	TriggerRecoveryLoop TriggerClass = "recovery_loop"
	// TriggerAmbiguousDirective covers operator messages that the Mayor
	// cannot parse with confidence.
	TriggerAmbiguousDirective TriggerClass = "ambiguous_directive"
	// TriggerLowConfidenceOutput covers model output that the Mayor
	// itself judges unreliable.
	TriggerLowConfidenceOutput TriggerClass = "low_confidence_output"
)

// IsValidTriggerClass returns true if c is a recognized trigger class.
func IsValidTriggerClass(c TriggerClass) bool {
	switch c {
	case TriggerMergePolicy,
		TriggerWitnessRefineryOverride,
		TriggerRecoveryLoop,
		TriggerAmbiguousDirective,
		TriggerLowConfidenceOutput:
		return true
	default:
		return false
	}
}

// RequestedModel identifies which stronger model the consult is being routed
// to. The two supported models are Codex and Opus; this is intentionally
// narrow so the policy stays reviewable.
type RequestedModel string

const (
	// ModelCodex is the Codex / GPT-5-class model consult target.
	ModelCodex RequestedModel = "codex"
	// ModelOpus is the Anthropic Opus-class model consult target.
	ModelOpus RequestedModel = "opus"
)

// IsValidRequestedModel returns true if m is a recognized consult target.
func IsValidRequestedModel(m RequestedModel) bool {
	switch m {
	case ModelCodex, ModelOpus:
		return true
	default:
		return false
	}
}

// DecisionState is the lifecycle state of a consult packet.
type DecisionState string

const (
	// StateOpen indicates the consult has been filed and is awaiting response.
	StateOpen DecisionState = "open"
	// StateAnswered indicates the consulted model has produced a decision.
	StateAnswered DecisionState = "answered"
	// StateClosed indicates the Mayor has closed the packet (with or without
	// acting on the consulted model's answer).
	StateClosed DecisionState = "closed"
)

// Request is the structured packet a Mayor sends when requesting a consult.
// The fields are mirrored into the description of the consult bead as
// "key: value" lines so the bead stays readable in bd show.
type Request struct {
	// Question is the actual decision the Mayor needs help with. Required.
	Question string `json:"question"`

	// TriggerClass identifies the kind of high-risk situation.
	TriggerClass TriggerClass `json:"trigger_class"`

	// RequestedModel is which stronger model to route the question to.
	RequestedModel RequestedModel `json:"requested_model"`

	// ContextRefs is an ordered list of bead IDs or MR IDs the consult
	// receiver should read first (e.g., the source bead, the failed MR,
	// related escalations). Optional.
	ContextRefs []string `json:"context_refs,omitempty"`

	// CurrentDecision is the Mayor's own current best guess, including
	// confidence. Optional but recommended.
	CurrentDecision string `json:"current_decision,omitempty"`

	// Options is a numbered list of alternatives the Mayor is considering.
	// The consulted model picks one (or proposes a new option).
	Options []string `json:"options,omitempty"`

	// AskedBy is the Mayor / agent identity filing the consult. Required.
	AskedBy string `json:"asked_by"`

	// AskedAt is the RFC3339 timestamp the consult was filed.
	AskedAt string `json:"asked_at"`

	// RelatedBead is the source bead this consult is about (e.g., the
	// merge-policy change bead). Optional but strongly recommended.
	RelatedBead string `json:"related_bead,omitempty"`

	// Fingerprint is a stable duplicate-suppression key (e.g.,
	// "merge-policy:stacked-branches:v1"). Optional.
	Fingerprint string `json:"fingerprint,omitempty"`

	// BeadID is filled in after the consult bead is created.
	BeadID string `json:"bead_id,omitempty"`
}

// Response is the structured answer from the consulted model. It is
// recorded on the consult bead when the packet is answered.
type Response struct {
	// DecidedBy is the model identifier (e.g., "codex" or "opus") that
	// produced the response.
	DecidedBy string `json:"decided_by"`
	// DecidedAt is the RFC3339 timestamp of the response.
	DecidedAt string `json:"decided_at"`
	// Decision is the chosen option (verbatim) or a new option the model
	// is proposing.
	Decision string `json:"decision"`
	// Rationale is the consulted model's reasoning. Required.
	Rationale string `json:"rationale"`
	// Confidence is "high", "medium", or "low" — the consulted model's
	// self-reported confidence.
	Confidence string `json:"confidence,omitempty"`
}

// LoopAction is what the LoopDetector recommends the caller do when a
// fingerprint has fired repeatedly.
type LoopAction string

const (
	// LoopActionNone means the fingerprint has not crossed the threshold;
	// the caller should proceed normally.
	LoopActionNone LoopAction = "none"
	// LoopActionConsult means the fingerprint is hot; the caller should
	// file a consult instead of repeating the original action.
	LoopActionConsult LoopAction = "consult"
	// LoopActionEscalate means the fingerprint is so hot that even a
	// consult is unlikely to resolve it; the caller should escalate.
	LoopActionEscalate LoopAction = "escalate"
)

// String returns the lowercase string representation.
func (a LoopAction) String() string { return string(a) }

// LoopPolicy configures the LoopDetector. Zero values are valid and mean
// "no detection" — caller should pre-fill defaults before use.
type LoopPolicy struct {
	// Threshold is how many times the same fingerprint may fire within
	// Window before the detector starts recommending LoopActionConsult.
	// Default: 3.
	Threshold int `json:"threshold"`
	// Window is the rolling time window over which Threshold is counted.
	// Default: 30m.
	Window time.Duration `json:"window"`
	// EscalateAt is the count at which the detector switches from
	// LoopActionConsult to LoopActionEscalate. Default: 6 (2× Threshold).
	EscalateAt int `json:"escalate_at"`
}

// DefaultLoopPolicy returns the recommended defaults for LoopPolicy.
func DefaultLoopPolicy() LoopPolicy {
	return LoopPolicy{
		Threshold:  3,
		Window:     30 * time.Minute,
		EscalateAt: 6,
	}
}

// LoopEvent is a single occurrence of a fingerprint being observed by the
// LoopDetector. It is appended to a durable log on disk so the detector
// survives Mayor restarts.
type LoopEvent struct {
	// Fingerprint is the stable key (e.g., "recovery:push-failed").
	Fingerprint string `json:"fingerprint"`
	// Source identifies what produced the event (e.g., "escalate",
	// "consult", "witness:rework"). Required.
	Source string `json:"source"`
	// BeadID is the bead that recorded the original event, if any.
	BeadID string `json:"bead_id,omitempty"`
	// OccurredAt is the RFC3339 timestamp.
	OccurredAt time.Time `json:"occurred_at"`
}

// LoopState is the durable append-only log of LoopEvents. Cross-process
// safety is provided by flock around read-modify-write operations.
type LoopState struct {
	Events []LoopEvent `json:"events"`
	mu     sync.Mutex  `json:"-"`
}

// LoopDecision is the structured result returned by LoopDetector.Check.
type LoopDecision struct {
	// Fingerprint is the fingerprint that was checked.
	Fingerprint string `json:"fingerprint"`
	// Count is how many matching events fell within Window.
	Count int `json:"count"`
	// Window is the window that was used.
	Window time.Duration `json:"window"`
	// Threshold is the threshold that was used.
	Threshold int `json:"threshold"`
	// Action is the recommended action.
	Action LoopAction `json:"action"`
	// RecentEvents is the (already-trimmed) list of events that triggered
	// the recommendation. Useful for surfacing in the consult packet.
	RecentEvents []LoopEvent `json:"recent_events,omitempty"`
}

// Errors returned by this package.
var (
	ErrEmptyQuestion    = errors.New("consult: question is required")
	ErrInvalidTrigger   = errors.New("consult: invalid trigger class")
	ErrInvalidModel     = errors.New("consult: invalid requested model")
	ErrEmptyAskedBy     = errors.New("consult: asked_by is required")
	ErrInvalidThreshold = errors.New("consult: loop threshold must be > 0")
)

// Validate enforces the structural constraints of a Request. The caller
// is still responsible for filling AskedAt and BeadID before submission.
func (r *Request) Validate() error {
	if r == nil {
		return errors.New("consult: nil request")
	}
	if strings.TrimSpace(r.Question) == "" {
		return ErrEmptyQuestion
	}
	if !IsValidTriggerClass(r.TriggerClass) {
		return fmt.Errorf("%w: %q", ErrInvalidTrigger, r.TriggerClass)
	}
	if !IsValidRequestedModel(r.RequestedModel) {
		return fmt.Errorf("%w: %q", ErrInvalidModel, r.RequestedModel)
	}
	if strings.TrimSpace(r.AskedBy) == "" {
		return ErrEmptyAskedBy
	}
	return nil
}

// Validate enforces the structural constraints of a Response.
func (r *Response) Validate() error {
	if r == nil {
		return errors.New("consult: nil response")
	}
	if strings.TrimSpace(r.DecidedBy) == "" {
		return errors.New("consult: response decided_by is required")
	}
	if strings.TrimSpace(r.DecidedAt) == "" {
		return errors.New("consult: response decided_at is required")
	}
	if strings.TrimSpace(r.Rationale) == "" {
		return errors.New("consult: response rationale is required")
	}
	return nil
}

// Fingerprint returns a stable duplicate-suppression key derived from the
// free-text question and trigger class. The Mayor or operator may set
// Request.Fingerprint directly when they want a custom key; otherwise the
// gt consult command falls back to this helper.
//
// The hash is content-derived so callers do not need to invent stable
// labels. Inputs are trimmed and lowercased to keep the key stable across
// trivial whitespace/case variations.
func Fingerprint(question string, trigger TriggerClass) string {
	key := strings.ToLower(strings.TrimSpace(string(trigger))) + "|" + strings.TrimSpace(question)
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("consult-fp:%s:%x", strings.TrimSpace(string(trigger)), sum[:6])
}

// FormatRequestDescription renders a Request as the description block of a
// consult bead. The format mirrors beads_escalation.go (key: value lines)
// so bd show output stays consistent across escalation and consult packets.
func FormatRequestDescription(title string, r *Request) string {
	if r == nil {
		return title
	}
	var lines []string
	lines = append(lines, title)
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("trigger_class: %s", r.TriggerClass))
	lines = append(lines, fmt.Sprintf("requested_model: %s", r.RequestedModel))
	lines = append(lines, fmt.Sprintf("question: %s", r.Question))
	if r.CurrentDecision != "" {
		lines = append(lines, fmt.Sprintf("current_decision: %s", r.CurrentDecision))
	} else {
		lines = append(lines, "current_decision: null")
	}
	if len(r.ContextRefs) > 0 {
		lines = append(lines, fmt.Sprintf("context_refs: %s", strings.Join(r.ContextRefs, ",")))
	} else {
		lines = append(lines, "context_refs: null")
	}
	if len(r.Options) > 0 {
		lines = append(lines, "options:")
		for i, opt := range r.Options {
			lines = append(lines, fmt.Sprintf("  %d. %s", i+1, opt))
		}
	} else {
		lines = append(lines, "options: null")
	}
	lines = append(lines, fmt.Sprintf("asked_by: %s", r.AskedBy))
	lines = append(lines, fmt.Sprintf("asked_at: %s", r.AskedAt))
	if r.RelatedBead != "" {
		lines = append(lines, fmt.Sprintf("related_bead: %s", r.RelatedBead))
	} else {
		lines = append(lines, "related_bead: null")
	}
	if r.Fingerprint != "" {
		lines = append(lines, fmt.Sprintf("fingerprint: %s", r.Fingerprint))
	} else {
		lines = append(lines, "fingerprint: null")
	}
	if r.BeadID != "" {
		lines = append(lines, fmt.Sprintf("bead_id: %s", r.BeadID))
	} else {
		lines = append(lines, "bead_id: null")
	}
	lines = append(lines, "state: open")
	lines = append(lines, "answered_by: null")
	lines = append(lines, "answered_at: null")
	lines = append(lines, "decision: null")
	lines = append(lines, "rationale: null")
	lines = append(lines, "confidence: null")
	lines = append(lines, "closed_by: null")
	lines = append(lines, "closed_reason: null")
	return strings.Join(lines, "\n")
}

// FormatAnswerSection renders a Response as a key: value block suitable for
// appending to (or replacing within) an existing consult description.
func FormatAnswerSection(resp *Response, closedBy, closedReason string) string {
	if resp == nil {
		resp = &Response{}
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("state: %s", StateClosed))
	lines = append(lines, fmt.Sprintf("answered_by: %s", resp.DecidedBy))
	lines = append(lines, fmt.Sprintf("answered_at: %s", resp.DecidedAt))
	lines = append(lines, fmt.Sprintf("decision: %s", resp.Decision))
	lines = append(lines, fmt.Sprintf("rationale: %s", resp.Rationale))
	lines = append(lines, fmt.Sprintf("confidence: %s", resp.Confidence))
	lines = append(lines, fmt.Sprintf("closed_by: %s", closedBy))
	lines = append(lines, fmt.Sprintf("closed_reason: %s", closedReason))
	return strings.Join(lines, "\n")
}

// ParseAnswerSection extracts a Response and close metadata from a
// rendered consult description. It is best-effort: missing fields are
// returned as zero values. Callers should check resp.DecidedBy == "" to
// decide whether the consult has actually been answered.
func ParseAnswerSection(description string) (*Response, string, string) {
	resp := &Response{}
	var closedBy, closedReason string
	for _, line := range strings.Split(description, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		colonIdx := strings.Index(line, ":")
		if colonIdx == -1 {
			continue
		}
		key := strings.TrimSpace(line[:colonIdx])
		value := strings.TrimSpace(line[colonIdx+1:])
		if value == "null" {
			value = ""
		}
		switch strings.ToLower(key) {
		case "answered_by":
			resp.DecidedBy = value
		case "answered_at":
			resp.DecidedAt = value
		case "decision":
			resp.Decision = value
		case "rationale":
			resp.Rationale = value
		case "confidence":
			resp.Confidence = value
		case "closed_by":
			closedBy = value
		case "closed_reason":
			closedReason = value
		}
	}
	return resp, closedBy, closedReason
}

// LoopFile returns the path to the durable LoopState file.
func LoopFile(townRoot string) string {
	return filepath.Join(townRoot, "mayor", "consult_loops.json")
}

// LoadLoopState loads LoopState from disk. Returns an empty state if the
// file does not exist.
func LoadLoopState(townRoot string) (*LoopState, error) {
	path := LoopFile(townRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("creating consult loops directory: %w", err)
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is derived from trusted townRoot
	if err != nil {
		if os.IsNotExist(err) {
			return &LoopState{Events: []LoopEvent{}}, nil
		}
		return nil, fmt.Errorf("reading consult loops: %w", err)
	}
	var state LoopState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing consult loops: %w", err)
	}
	if state.Events == nil {
		state.Events = []LoopEvent{}
	}
	return &state, nil
}

// SaveLoopState atomically persists the loop log (write-then-rename).
func SaveLoopState(townRoot string, state *LoopState) error {
	if state == nil {
		return errors.New("consult: cannot save nil loop state")
	}
	path := LoopFile(townRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("creating consult loops directory: %w", err)
	}
	state.mu.Lock()
	data, err := json.MarshalIndent(state, "", "  ")
	state.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshaling consult loops: %w", err)
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("writing consult loops: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("committing consult loops: %w", err)
	}
	return nil
}

// RecordLoopEvent appends a LoopEvent to the durable log. It does not
// enforce thresholds; the caller should follow up with LoopDetector.Check
// after recording to learn whether the new event crossed a threshold.
//
// RecordLoopEvent is intentionally best-effort: errors are returned but
// the caller (typically a witness / deacon patrol) is expected to log and
// proceed rather than block dispatch.
func RecordLoopEvent(townRoot, fingerprint, source, beadID string, at time.Time) error {
	if strings.TrimSpace(fingerprint) == "" {
		return errors.New("consult: fingerprint is required")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	state, err := LoadLoopState(townRoot)
	if err != nil {
		return err
	}
	state.mu.Lock()
	state.Events = append(state.Events, LoopEvent{
		Fingerprint: fingerprint,
		Source:      source,
		BeadID:      beadID,
		OccurredAt:  at,
	})
	state.mu.Unlock()
	return SaveLoopState(townRoot, state)
}

// LoopDetector inspects the durable loop log and returns a recommendation.
// It is stateless (apart from the log on disk) so it is safe to share
// across goroutines.
type LoopDetector struct {
	TownRoot string
	Policy   LoopPolicy
}

// NewLoopDetector returns a detector with the supplied policy. A zero
// policy is replaced with DefaultLoopPolicy so callers can pass
// LoopPolicy{} safely.
func NewLoopDetector(townRoot string, policy LoopPolicy) *LoopDetector {
	if policy.Threshold <= 0 {
		policy.Threshold = DefaultLoopPolicy().Threshold
	}
	if policy.Window <= 0 {
		policy.Window = DefaultLoopPolicy().Window
	}
	if policy.EscalateAt <= 0 {
		policy.EscalateAt = DefaultLoopPolicy().EscalateAt
	}
	return &LoopDetector{TownRoot: townRoot, Policy: policy}
}

// Check returns a LoopDecision for the supplied fingerprint. It is safe
// to call with an empty fingerprint (returns LoopActionNone).
func (d *LoopDetector) Check(fingerprint string, now time.Time) (LoopDecision, error) {
	decision := LoopDecision{
		Fingerprint: fingerprint,
		Window:      d.Policy.Window,
		Threshold:   d.Policy.Threshold,
		Action:      LoopActionNone,
	}
	if strings.TrimSpace(fingerprint) == "" {
		return decision, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	state, err := LoadLoopState(d.TownRoot)
	if err != nil {
		return decision, err
	}
	cutoff := now.Add(-d.Policy.Window)
	matching := make([]LoopEvent, 0, len(state.Events))
	for _, ev := range state.Events {
		if ev.Fingerprint != fingerprint {
			continue
		}
		if ev.OccurredAt.Before(cutoff) {
			continue
		}
		matching = append(matching, ev)
	}
	sort.Slice(matching, func(i, j int) bool {
		return matching[i].OccurredAt.Before(matching[j].OccurredAt)
	})
	decision.Count = len(matching)
	decision.RecentEvents = matching
	switch {
	case decision.Count >= d.Policy.EscalateAt:
		decision.Action = LoopActionEscalate
	case decision.Count >= d.Policy.Threshold:
		decision.Action = LoopActionConsult
	default:
		decision.Action = LoopActionNone
	}
	return decision, nil
}

// RecordAndCheck is a convenience wrapper that records an event and then
// checks the resulting threshold. The beadID may be empty.
func (d *LoopDetector) RecordAndCheck(fingerprint, source, beadID string, now time.Time) (LoopDecision, error) {
	if err := RecordLoopEvent(d.TownRoot, fingerprint, source, beadID, now); err != nil {
		return LoopDecision{}, err
	}
	return d.Check(fingerprint, now)
}