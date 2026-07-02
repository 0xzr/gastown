// Package mrtelemetry records per-MR-attempt and per-review-attempt telemetry
// for the Gastown refinery / merge queue path.
//
// The goal is durable, queryable data on which implementer (writer) models
// produce work that passes Codex review fastest, separating substantive
// implementation failures from deterministic validation failures, reviewer
// unavailability, timeouts, and infra noise.
//
// Design constraints:
//
//   - Best-effort. Telemetry must NEVER break the merge path. Every write is
//     guarded; a write failure is logged and dropped, not propagated.
//   - Append-only JSONL, one file per day, written under
//     <townRoot>/.runtime/refinery-telemetry/refinery-attempts-YYYYMMDD.jsonl.
//     This is distinct from the reviewer-side refinery-gate-*.jsonl written by
//     the shell gate (separate concern, separate repo).
//   - Atomic append via an exclusive file lock so concurrent refinery
//     goroutines/processes do not interleave or corrupt records.
//   - Writer attribution is captured AT ATTEMPT START (when the MR is claimed),
//     not at merge time, so a model reassignment mid-flight does not retrobably
//     change the recorded writer of an already-submitted attempt.
package mrtelemetry

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// AttemptRecord is a single row per MR attempt + review attempt, written once
// when the attempt reaches a terminal state (merged, rejected, failed).
//
// Timestamps are RFC3339 UTC strings on disk (zero value renders as "") so
// the JSONL is greppable and jq-friendly; callers set them via the time fields.
type AttemptRecord struct {
	// Identity
	Rig         string `json:"rig"`
	SourceBead  string `json:"source_bead"`
	MRID        string `json:"mr_id"`
	Attempt     int    `json:"attempt"` // 1-based; count of prior terminal attempts for same source_bead + 1
	Polecat     string `json:"polecat"`
	WriterModel string `json:"writer_model"` // captured at attempt start (attribution invariant)
	Branch      string `json:"branch"`
	CommitSHA   string `json:"commit_sha,omitempty"`
	TreeSHA     string `json:"tree_sha,omitempty"`

	// Submission / refinery timing
	SubmittedAt       string `json:"submitted_at,omitempty"`        // MR entered the queue
	RefineryStartedAt string `json:"refinery_started_at,omitempty"` // refinery began processing this attempt

	// Validation (deterministic gates: build/test/lint/typecheck)
	ValidationStartedAt  string `json:"validation_started_at,omitempty"`
	ValidationFinishedAt string `json:"validation_finished_at,omitempty"`
	ValidationVerdict    string `json:"validation_verdict,omitempty"` // PASS | FAIL | SKIPPED | ""

	// Codex / durable multi-model review gate
	CodexReviewStartedAt  string `json:"codex_review_started_at,omitempty"`
	CodexReviewFinishedAt string `json:"codex_review_finished_at,omitempty"`
	CodexVerdict          string `json:"codex_verdict,omitempty"` // PASS | FAIL | UNAVAILABLE | NO_VERDICT | none

	// Terminal state
	FinalGateDecision string `json:"final_gate_decision,omitempty"` // merged | rejected | failed | no_merge | needs_approval | slot_timeout
	MergedAt          string `json:"merged_at,omitempty"`
	RejectedAt        string `json:"rejected_at,omitempty"`

	// Classification
	FailureClass string `json:"failure_class,omitempty"` // substantive_implementation | deterministic_validation | reviewer_unavailable | timeout | infra | convention | conflict | none

	// Provenance
	RawLogPath       string `json:"raw_log_path,omitempty"`
	ReworkPacketPath string `json:"rework_packet_path,omitempty"`

	// RecordedAt is when this record was finalized and written.
	RecordedAt string `json:"recorded_at"`
}

// Failure class constants. These map ProcessResult flags to a bounded taxonomy
// so Mayor can separate substantive Codex FAIL from deterministic validation,
// reviewer unavailability, timeouts, and infra noise.
const (
	FailureClassNone                  = "none"
	FailureClassSubstantiveImpl       = "substantive_implementation"
	FailureClassDeterministicValidation = "deterministic_validation"
	FailureClassReviewerUnavailable    = "reviewer_unavailable"
	FailureClassTimeout                = "timeout"
	FailureClassInfra                  = "infra"
	FailureClassConvention             = "convention"
	FailureClassConflict              = "conflict"
)

// Final gate decision constants.
const (
	DecisionMerged        = "merged"
	DecisionRejected      = "rejected"
	DecisionFailed        = "failed"
	DecisionNoMerge       = "no_merge"
	DecisionNeedsApproval = "needs_approval"
	DecisionSlotTimeout   = "slot_timeout"
)

// Codex verdict constants. These mirror refinery.ReviewerVerdict plus a
// "none" sentinel for attempts that never reached the codex review gate.
const (
	CodexVerdictPass        = "PASS"
	CodexVerdictFail        = "FAIL"
	CodexVerdictUnavailable = "UNAVAILABLE"
	CodexVerdictNoVerdict   = "NO_VERDICT"
	CodexVerdictNone        = "none"
)

// Validation verdict constants (deterministic gates: build/test/lint/typecheck).
// These are distinct from the codex review verdict — a validation PASS means the
// deterministic gates passed, not that the codex reviewer approved.
const (
	ValidationVerdictPass   = "PASS"
	ValidationVerdictFail   = "FAIL"
	ValidationVerdictSkipped = "SKIPPED"
)

// FormatTime renders a time as RFC3339 UTC, or "" for the zero time. Returning
// an empty string for unset timestamps keeps the JSONL clean (omitempty drops
// empty strings) and avoids "0001-01-01T00:00:00Z" noise.
func FormatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Marshal returns the JSON encoding of r as a single line (no trailing newline).
// It is used by the Recorder to write one JSON object per line.
func (r AttemptRecord) Marshal() ([]byte, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("marshal telemetry attempt record: %w", err)
	}
	return b, nil
}

// UnmarshalAttemptRecord parses a single JSONL line into an AttemptRecord.
func UnmarshalAttemptRecord(line []byte) (AttemptRecord, error) {
	var r AttemptRecord
	if err := json.Unmarshal(line, &r); err != nil {
		return AttemptRecord{}, fmt.Errorf("unmarshal telemetry attempt record: %w", err)
	}
	return r, nil
}

// ReadAll decodes every attempt record from a JSONL reader, one record per
// line. Lines that fail to parse are skipped (telemetry is append-only and
// best-effort; a corrupt line must not abort the summarizer). Returns the valid
// records in file order.
//
// It scans line-by-line rather than using a single json.Decoder stream because
// a json.Decoder that hits a syntax error mid-stream can get stuck re-reading
// the same malformed token, which would hang the summarizer on a single bad
// line. Line-delimited decoding isolates corruption to the offending line.
func ReadAll(r io.Reader) ([]AttemptRecord, error) {
	scanner := bufio.NewScanner(r)
	// Allow long lines (gate logs / rework packets can be sizable). Default
	// token limit is 64KiB; bump to 1MiB to be safe.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var out []AttemptRecord
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec AttemptRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			// Best-effort recovery: skip unparseable line, continue.
			continue
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return out, err
	}
	return out, nil
}
