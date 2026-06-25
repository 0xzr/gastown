// Package beads — consult bead management.
//
// A consult bead is the durable record of a Mayor -> Codex/Opus question.
// It mirrors the shape of an escalation bead (key: value lines in the
// description) but adds structured fields for trigger class, requested
// model, options, current decision, context refs, and the consulted
// model's response.
package beads

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/consult"
)

// ConsultFields holds the structured fields written into a consult bead's
// description. The "state" / response / close metadata are rendered with
// the same "key: value" format used by EscalationFields so bd show output
// stays consistent.
type ConsultFields struct {
	Request      *consult.Request
	State        consult.DecisionState
	DecidedBy    string
	DecidedAt    string
	Decision     string
	Rationale    string
	Confidence   string
	ClosedBy     string
	ClosedReason string
}

// FormatConsultDescription renders the full description for a consult bead.
// The first line is the bead title; subsequent lines are key: value pairs
// that can be parsed back with ParseConsultFields.
func FormatConsultDescription(title string, fields *ConsultFields) string {
	if fields == nil || fields.Request == nil {
		return title
	}
	r := fields.Request
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
	state := fields.State
	if state == "" {
		state = consult.StateOpen
	}
	lines = append(lines, fmt.Sprintf("state: %s", state))
	lines = append(lines, fmt.Sprintf("answered_by: %s", nullify(fields.DecidedBy)))
	lines = append(lines, fmt.Sprintf("answered_at: %s", nullify(fields.DecidedAt)))
	lines = append(lines, fmt.Sprintf("decision: %s", nullify(fields.Decision)))
	lines = append(lines, fmt.Sprintf("rationale: %s", nullify(fields.Rationale)))
	lines = append(lines, fmt.Sprintf("confidence: %s", nullify(fields.Confidence)))
	lines = append(lines, fmt.Sprintf("closed_by: %s", nullify(fields.ClosedBy)))
	lines = append(lines, fmt.Sprintf("closed_reason: %s", nullify(fields.ClosedReason)))
	return strings.Join(lines, "\n")
}

// ParseConsultFields re-extracts a Request plus close metadata from a
// rendered consult bead description. The options section is rendered as
// a numbered list under an "options:" header; we accumulate those lines
// into a flat Options slice on the Request.
func ParseConsultFields(description string) (*consult.Request, *ConsultFields) {
	r := &consult.Request{}
	f := &ConsultFields{Request: r}
	inOptions := false
	for _, raw := range strings.Split(description, "\n") {
		// Preserve indentation so we can detect numbered list items.
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		// Numbered options look like "  1. Allow". They have a "." but no
		// ":" before the dot, so the colon check below would skip them —
		// handle them explicitly while we are inside an options block.
		if inOptions {
			// Exit options mode as soon as we see a non-indented line that
			// isn't a numbered option (i.e. has a ":" key: value header).
			if !strings.HasPrefix(raw, " ") && !strings.HasPrefix(raw, "\t") {
				inOptions = false
				// fall through to normal parsing of this line
			} else if dotIdx := strings.Index(trimmed, "."); dotIdx > 0 {
				before := strings.TrimSpace(trimmed[:dotIdx])
				if _, err := strconv.Atoi(before); err == nil {
					optionText := strings.TrimSpace(trimmed[dotIdx+1:])
					if optionText != "" {
						r.Options = append(r.Options, optionText)
					}
					continue
				}
			}
		}
		colonIdx := strings.Index(trimmed, ":")
		if colonIdx == -1 {
			continue
		}
		key := strings.TrimSpace(trimmed[:colonIdx])
		value := strings.TrimSpace(trimmed[colonIdx+1:])
		if value == "null" {
			value = ""
		}
		switch strings.ToLower(key) {
		case "trigger_class":
			r.TriggerClass = consult.TriggerClass(value)
		case "requested_model":
			r.RequestedModel = consult.RequestedModel(value)
		case "question":
			r.Question = value
		case "current_decision":
			r.CurrentDecision = value
		case "context_refs":
			if value != "" {
				r.ContextRefs = splitCSV(value)
			}
		case "options":
			// options: null → empty; options: (with list following) → enter list mode.
			if value == "" {
				inOptions = true
			}
		case "asked_by":
			r.AskedBy = value
		case "asked_at":
			r.AskedAt = value
		case "related_bead":
			r.RelatedBead = value
		case "fingerprint":
			r.Fingerprint = value
		case "bead_id":
			r.BeadID = value
		case "state":
			f.State = consult.DecisionState(value)
		case "answered_by":
			f.DecidedBy = value
		case "answered_at":
			f.DecidedAt = value
		case "decision":
			f.Decision = value
		case "rationale":
			f.Rationale = value
		case "confidence":
			f.Confidence = value
		case "closed_by":
			f.ClosedBy = value
		case "closed_reason":
			f.ClosedReason = value
		}
	}
	return r, f
}

func nullify(s string) string {
	if s == "" {
		return "null"
	}
	return s
}

// CreateConsultBead creates a new consult bead. The bead is labeled
// gt:consult and carries a "trigger:<class>" label for filtering.
//
// The bead is a DURABLE task, not an ephemeral wisp. A consult packet is the
// audit record of a Mayor -> Codex/Opus decision: it must survive unanswered
// until the consulted model responds and the Mayor closes it. Ephemeral wisps
// are routed to the wisps table and swept by the reaper (default 24h max-age),
// which would silently close an unanswered consult before a response arrives.
// It is also NOT filed with --wisp-type: "consult" is not a valid wisp type
// (only heartbeat/ping/patrol/gc_report/recovery/error/escalation are), so a
// --wisp-type=consult flag is rejected by `bd create` and the packet is never
// filed at all. Both flags are therefore omitted.
func (b *Beads) CreateConsultBead(title string, fields *ConsultFields) (string, error) {
	if IsFlagLikeTitle(title) {
		return "", fmt.Errorf("%w (got %q)", ErrFlagTitle, title)
	}
	if fields == nil || fields.Request == nil {
		return "", errors.New("consult: fields/request required")
	}
	if err := fields.Request.Validate(); err != nil {
		return "", err
	}
	description := FormatConsultDescription(title, fields)
	args := []string{"create", "--json",
		"--title=" + title,
		"--body-file=-",
		"--type=task",
		"--labels=gt:consult",
	}
	if fields.Request.TriggerClass != "" {
		args = append(args, "--labels=trigger:"+string(fields.Request.TriggerClass))
	}
	if fields.Request.RequestedModel != "" {
		args = append(args, "--labels=model:"+string(fields.Request.RequestedModel))
	}
	if fields.Request.Fingerprint != "" {
		args = append(args, "--labels="+fields.Request.Fingerprint)
	}
	if actor := b.getActor(); actor != "" {
		args = append(args, "--actor="+actor)
	}
	out, err := b.runWithStdin([]byte(description), args...)
	if err != nil {
		return "", err
	}
	var issue Issue
	if err := json.Unmarshal(out, &issue); err != nil {
		return "", fmt.Errorf("parsing bd create output: %w", err)
	}
	return issue.ID, nil
}

// GetConsultBeadIssue fetches the raw Issue for a consult bead ID.
func (b *Beads) GetConsultBeadIssue(id string) (*Issue, error) {
	issue, err := b.forIssueID(id).Show(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if !HasLabel(issue, "gt:consult") {
		return nil, fmt.Errorf("issue %s is not a consult bead (missing gt:consult label)", id)
	}
	return issue, nil
}

// GetConsultBead fetches and parses a consult bead, returning the issue,
// the request, the response (if any), the closer, and the close reason.
func (b *Beads) GetConsultBead(id string) (*Issue, *consult.Request, *consult.Response, string, string, error) {
	issue, err := b.GetConsultBeadIssue(id)
	if err != nil {
		return nil, nil, nil, "", "", err
	}
	if issue == nil {
		return nil, nil, nil, "", "", nil
	}
	req, fields := ParseConsultFields(issue.Description)
	resp := &consult.Response{
		DecidedBy:  fields.DecidedBy,
		DecidedAt:  fields.DecidedAt,
		Decision:   fields.Decision,
		Rationale:  fields.Rationale,
		Confidence: fields.Confidence,
	}
	return issue, req, resp, fields.ClosedBy, fields.ClosedReason, nil
}

// RecordConsultAnswer updates a consult bead with the response from the
// consulted model. It does not close the bead; the Mayor closes it
// separately with CloseConsultBead.
//
// State guard: a consult that is already closed cannot be re-answered. Closing
// is terminal; allowing a closed bead to flip back to "answered" would
// resurrect a finalized decision and corrupt the audit trail. Answering an
// already-answered bead (idempotent re-record from the same model) is allowed.
func (b *Beads) RecordConsultAnswer(id string, resp *consult.Response) error {
	if resp == nil {
		return errors.New("consult: nil response")
	}
	if err := resp.Validate(); err != nil {
		return err
	}
	issue, err := b.GetConsultBeadIssue(id)
	if err != nil {
		return err
	}
	if issue == nil {
		return ErrNotFound
	}
	_, fields := ParseConsultFields(issue.Description)
	if fields.State == consult.StateClosed {
		return fmt.Errorf("consult %s: cannot record answer on a closed consult", id)
	}
	fields.State = consult.StateAnswered
	fields.DecidedBy = resp.DecidedBy
	fields.DecidedAt = resp.DecidedAt
	fields.Decision = resp.Decision
	fields.Rationale = resp.Rationale
	fields.Confidence = resp.Confidence
	description := FormatConsultDescription(issue.Title, fields)
	target := b.forIssueID(id)
	return target.Update(id, UpdateOptions{
		Description: &description,
		AddLabels:   []string{"consult:answered"},
	})
}

// CloseConsultBead marks a consult bead closed and stamps the close
// metadata into the description. The resp argument is optional — when the
// bead has not been answered, the close metadata is recorded without
// decision fields.
//
// State guard: closing is terminal. A consult that is already closed cannot be
// re-closed (re-closing would overwrite the original closer/reason and, via
// the mirrored note, append a second close record to the source bead). An
// open or answered consult may be closed.
//
// Ordering: the bd close is issued BEFORE the description is rewritten to
// state=closed. If the close fails (network blip, lock contention, Dolt
// retry) the description still records a non-closed state, so a retry of
// CloseConsultBead passes the state guard and reaches the close path again
// instead of being rejected as "already closed" (which would leave the
// consult permanently open). bd close is idempotent — re-closing a bead the
// close already succeeded on returns no error — so re-running close on the
// retry (when the earlier close succeeded but the subsequent Update failed)
// also converges cleanly.
func (b *Beads) CloseConsultBead(id, closedBy, closedReason string, resp *consult.Response) error {
	issue, err := b.GetConsultBeadIssue(id)
	if err != nil {
		return err
	}
	if issue == nil {
		return ErrNotFound
	}
	_, fields := ParseConsultFields(issue.Description)
	if fields.State == consult.StateClosed {
		return fmt.Errorf("consult %s: already closed", id)
	}
	target := b.forIssueID(id)
	if _, err := target.run("close", id, "--reason="+closedReason); err != nil {
		// Close failed: do NOT advance the description state. Leave the
		// bead open/answered so a retry reaches this path again.
		return err
	}
	fields.State = consult.StateClosed
	fields.ClosedBy = closedBy
	fields.ClosedReason = closedReason
	if resp != nil {
		fields.DecidedBy = resp.DecidedBy
		fields.DecidedAt = resp.DecidedAt
		fields.Decision = resp.Decision
		fields.Rationale = resp.Rationale
		fields.Confidence = resp.Confidence
	}
	description := FormatConsultDescription(issue.Title, fields)
	return target.Update(id, UpdateOptions{
		Description: &description,
		AddLabels:   []string{"consult:closed", "resolved"},
	})
}

// MirrorConsultResultOnSource writes a compact consult_decision notes line
// onto the related source bead so the source bead's audit trail carries
// the consulted model's decision. The source bead is resolved via the
// prefix routing in routes.jsonl, so this works across rigs.
//
// The function is best-effort: it returns an error so callers can decide
// whether to log a warning or abort. Mirroring is intentionally separate
// from closing the consult bead so a missing source bead does not roll
// back the close.
func (b *Beads) MirrorConsultResultOnSource(relatedBead string, resp *consult.Response, closer, reason, consultID string) error {
	if strings.TrimSpace(relatedBead) == "" {
		return nil
	}
	if resp == nil || resp.DecidedBy == "" {
		note := fmt.Sprintf("consult_close: %s closed_by=%s reason=%s", consultID, closer, reason)
		return b.appendNotesToBead(relatedBead, note)
	}
	confidence := resp.Confidence
	if confidence == "" {
		confidence = "unspecified"
	}
	decision := resp.Decision
	if decision == "" {
		decision = "(none recorded)"
	}
	rationale := strings.ReplaceAll(strings.TrimSpace(resp.Rationale), "\n", " ")
	note := fmt.Sprintf(
		"consult_decision: %s consulted_model=%s decision=%s confidence=%s rationale=%s consult_bead=%s closed_by=%s reason=%s",
		consultID, resp.DecidedBy, decision, confidence, rationale, consultID, closer, reason)
	return b.appendNotesToBead(relatedBead, note)
}

// appendNotesToBead appends newLine to the source bead via
// `bd update --append-notes=`. The bd CLI has two distinct flags:
//   - --notes= REPLACES the notes field entirely (data loss)
//   - --append-notes= concatenates with a newline separator (append-only)
//
// A consult decision mirrored onto its related source bead must NOT overwrite
// that bead's existing notes, so --append-notes is required. (The prior
// implementation used --notes=, which erased the source bead's audit trail.)
//
// The target is resolved via forIssueID so a cross-rig related_bead (e.g. an
// hq-* bead mirrored from a rig consult) is written to the correct database
// instead of the consult's own rig database.
func (b *Beads) appendNotesToBead(beadID, newLine string) error {
	if strings.TrimSpace(beadID) == "" || strings.TrimSpace(newLine) == "" {
		return nil
	}
	args := []string{"update", beadID, "--append-notes=" + newLine}
	target := b.forIssueID(beadID)
	if _, err := target.run(args...); err != nil {
		return fmt.Errorf("updating source bead %s: %w", beadID, err)
	}
	return nil
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Guard against unused imports if someone trims the file later.
var _ = strconv.Atoi
var _ = time.RFC3339
