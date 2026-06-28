// Package mrtelemetry provides durable JSONL telemetry for merge-request
// attempts in the Gastown refinery / merge queue path.
package mrtelemetry

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// RecordMRAttemptInput contains the fields supplied by callers when recording
// a new MR attempt. The Store computes the attempt number and record ID.
type RecordMRAttemptInput struct {
	Rig             string
	SourceBead      string
	MRID            string
	Polecat         string
	WriterModel     string
	WriterModelSource string
	Branch          string
	CommitSHA       string
	TreeSHA         string
	SubmittedAt     time.Time
	RawLogPath      string
	ReworkPacketPath string
}

// ReviewerResult captures the outcome for a single reviewer.
type ReviewerResult struct {
	Reviewer string `json:"reviewer"`
	Verdict  string `json:"verdict"`
	Blockers []string `json:"blockers,omitempty"`
	CauseKey string `json:"cause_key,omitempty"`
}

// MRAttempt is a single durable telemetry row for an MR/rework attempt.
// Field names use JSON snake_case for downstream analysis.
type MRAttempt struct {
	// RecordID is a unique ULID-like identifier for this row.
	RecordID string `json:"record_id"`

	// Rig is the gastown rig that processed the attempt (e.g. "gastown").
	Rig string `json:"rig"`

	// SourceBead is the issue/bead that generated this rework attempt.
	SourceBead string `json:"source_bead"`

	// MRID identifies the merge request this attempt belongs to.
	MRID string `json:"mr_id"`

	// AttemptNumber counts attempts for the same source_bead and rig,
	// starting at 1.
	AttemptNumber int `json:"attempt_number"`

	// Polecat is the worker name that produced the attempt (e.g. "jasper").
	Polecat string `json:"polecat"`

	// WriterModel is the implementing model name (e.g. "umans-kimi").
	WriterModel string `json:"writer_model"`

	// WriterModelSource indicates how WriterModel was attributed:
	// "agent_bead", "model_assignment", or "unknown".
	WriterModelSource string `json:"writer_model_source"`

	// Branch is the source branch submitted for merge.
	Branch string `json:"branch"`

	// CommitSHA is the git commit SHA of the submitted branch head.
	CommitSHA string `json:"commit_sha"`

	// TreeSHA is the git tree SHA of the submitted tree.
	TreeSHA string `json:"tree_sha"`

	// SubmittedAt is when the MR was submitted to the merge queue.
	SubmittedAt time.Time `json:"submitted_at"`

	// RefineryStartedAt is when the refinery began processing this attempt.
	RefineryStartedAt time.Time `json:"refinery_started_at,omitempty"`

	// ValidationStartedAt and ValidationFinishedAt bracket deterministic
	// validation (setup, tests, lint, build).
	ValidationStartedAt  time.Time `json:"validation_started_at,omitempty"`
	ValidationFinishedAt time.Time `json:"validation_finished_at,omitempty"`

	// ValidationPassed is true when deterministic validation succeeded.
	ValidationPassed bool `json:"validation_passed,omitempty"`

	// CodexReviewStartedAt and CodexReviewFinishedAt bracket peer review.
	CodexReviewStartedAt  time.Time `json:"codex_review_started_at,omitempty"`
	CodexReviewFinishedAt time.Time `json:"codex_review_finished_at,omitempty"`

	// CodexVerdict is the classified overall review outcome: PASS, FAIL,
	// UNAVAILABLE, NO_VERDICT, or empty.
	CodexVerdict string `json:"codex_verdict,omitempty"`

	// ReviewerResults holds per-reviewer outcomes.
	ReviewerResults []ReviewerResult `json:"reviewer_results,omitempty"`

	// FinalGateDecision is the eventual merge-queue outcome.
	FinalGateDecision string `json:"final_gate_decision,omitempty"`

	// FailureClass categorizes why an attempt failed, empty when merged or
	// not yet classified.
	FailureClass string `json:"failure_class,omitempty"`

	// MergedAt and RejectedAt are terminal timestamps.
	MergedAt  time.Time `json:"merged_at,omitempty"`
	RejectedAt time.Time `json:"rejected_at,omitempty"`

	// MergeCommitSHA is the commit SHA produced by the merge.
	MergeCommitSHA string `json:"merge_commit_sha,omitempty"`

	// PublishedCommitSHA is the SHA after any post-merge publication step.
	PublishedCommitSHA string `json:"published_commit_sha,omitempty"`

	// RawLogPath points to the raw refinery log bundle for this attempt.
	RawLogPath string `json:"raw_log_path,omitempty"`

	// ReworkPacketPath points to the prompt/rework packet when applicable.
	ReworkPacketPath string `json:"rework_packet_path,omitempty"`

	// SubmitToCodexVerdictMS is the duration from SubmittedAt to the Codex
	// review verdict, in milliseconds.
	SubmitToCodexVerdictMS int64 `json:"submit_to_codex_verdict_ms,omitempty"`

	// RefineryToCodexVerdictMS is the duration from RefineryStartedAt to the
	// Codex review verdict, in milliseconds.
	RefineryToCodexVerdictMS int64 `json:"refinery_to_codex_verdict_ms,omitempty"`
}

// Store reads and writes MRAttempt records as JSONL.
// Safe for in-process concurrent use; intended for a single refinery process
// per rig path.
type Store struct {
	mu   sync.Mutex
	path string
}

// NewStore creates a Store backed by the JSONL file at path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// DefaultStorePath returns the conventional telemetry path inside a rig.
func DefaultStorePath(rigPath string) string {
	return filepath.Join(rigPath, ".runtime", "mr-telemetry.jsonl")
}

// RecordMRAttempt appends a new attempt record. It computes AttemptNumber as
// 1 + the count of existing records with the same SourceBead and Rig, and
// generates a unique RecordID.
func (s *Store) RecordMRAttempt(ctx context.Context, input RecordMRAttemptInput) (*MRAttempt, error) {
	records, err := s.readAll()
	if err != nil {
		return nil, fmt.Errorf("reading existing telemetry: %w", err)
	}

	attemptNumber := 1
	for _, r := range records {
		if r.Rig == input.Rig && r.SourceBead == input.SourceBead {
			attemptNumber++
		}
	}

	record := MRAttempt{
		RecordID:          newRecordID(),
		Rig:               input.Rig,
		SourceBead:        input.SourceBead,
		MRID:              input.MRID,
		AttemptNumber:     attemptNumber,
		Polecat:           input.Polecat,
		WriterModel:       input.WriterModel,
		WriterModelSource: input.WriterModelSource,
		Branch:            input.Branch,
		CommitSHA:         input.CommitSHA,
		TreeSHA:           input.TreeSHA,
		SubmittedAt:       input.SubmittedAt,
		RawLogPath:        input.RawLogPath,
		ReworkPacketPath:  input.ReworkPacketPath,
	}

	records = append(records, record)
	if err := s.writeAll(records); err != nil {
		return nil, fmt.Errorf("appending telemetry record: %w", err)
	}

	return &record, nil
}

// GetByMRID returns the first record with the given MRID.
func (s *Store) GetByMRID(mrID string) (*MRAttempt, error) {
	records, err := s.readAll()
	if err != nil {
		return nil, fmt.Errorf("reading telemetry: %w", err)
	}
	for i := range records {
		if records[i].MRID == mrID {
			return &records[i], nil
		}
	}
	return nil, fmt.Errorf("MR attempt not found for mr_id %q", mrID)
}

// UpdateByMRID finds the first record with the given MRID, applies mutate,
// and rewrites the file atomically.
func (s *Store) UpdateByMRID(mrID string, mutate func(*MRAttempt)) error {
	records, err := s.readAll()
	if err != nil {
		return fmt.Errorf("reading telemetry: %w", err)
	}
	for i := range records {
		if records[i].MRID == mrID {
			mutate(&records[i])
			return s.writeAll(records)
		}
	}
	return fmt.Errorf("MR attempt not found for mr_id %q", mrID)
}

// ListAll returns all stored attempts in file order.
func (s *Store) ListAll() ([]MRAttempt, error) {
	return s.readAll()
}

// RecordRefineryStarted records when refinery processing began.
func (s *Store) RecordRefineryStarted(ctx context.Context, mrID string, now time.Time) error {
	return s.UpdateByMRID(mrID, func(a *MRAttempt) {
		a.RefineryStartedAt = now
	})
}

// RecordValidation records deterministic validation timing and outcome.
func (s *Store) RecordValidation(ctx context.Context, mrID string, started, finished time.Time, passed bool) error {
	return s.UpdateByMRID(mrID, func(a *MRAttempt) {
		a.ValidationStartedAt = started
		a.ValidationFinishedAt = finished
		a.ValidationPassed = passed
	})
}

// RecordCodexReview records Codex review timing, verdict, and per-reviewer
// results. It recomputes SubmitToCodexVerdictMS and RefineryToCodexVerdictMS
// when the timestamps are available.
func (s *Store) RecordCodexReview(ctx context.Context, mrID string, started, finished time.Time, verdict string, reviewerResults []ReviewerResult) error {
	return s.UpdateByMRID(mrID, func(a *MRAttempt) {
		a.CodexReviewStartedAt = started
		a.CodexReviewFinishedAt = finished
		a.CodexVerdict = verdict
		a.ReviewerResults = reviewerResults
		if !finished.IsZero() {
			if !a.SubmittedAt.IsZero() {
				a.SubmitToCodexVerdictMS = finished.Sub(a.SubmittedAt).Milliseconds()
			}
			if !a.RefineryStartedAt.IsZero() {
				a.RefineryToCodexVerdictMS = finished.Sub(a.RefineryStartedAt).Milliseconds()
			}
		}
	})
}

// RecordFinalOutcome records the final merge-queue decision.
func (s *Store) RecordFinalOutcome(ctx context.Context, mrID string, finalGateDecision, failureClass string, mergedAt, rejectedAt time.Time, mergeCommit, publishedCommit string) error {
	return s.UpdateByMRID(mrID, func(a *MRAttempt) {
		a.FinalGateDecision = finalGateDecision
		a.FailureClass = failureClass
		a.MergedAt = mergedAt
		a.RejectedAt = rejectedAt
		a.MergeCommitSHA = mergeCommit
		a.PublishedCommitSHA = publishedCommit
	})
}

// readAll loads every record from the JSONL file. It returns an empty slice
// when the file does not exist yet.
func (s *Store) readAll() ([]MRAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening telemetry file: %w", err)
	}
	defer f.Close()

	var records []MRAttempt
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec MRAttempt
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("parsing telemetry line %d: %w", lineNo, err)
		}
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning telemetry file: %w", err)
	}
	return records, nil
}

// writeAll writes all records atomically (temp file + rename).
func (s *Store) writeAll(records []MRAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return fmt.Errorf("creating telemetry dir: %w", err)
	}

	tmpPath := s.path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("creating temp telemetry file: %w", err)
	}

	w := bufio.NewWriter(f)
	for _, rec := range records {
		data, err := json.Marshal(rec)
		if err != nil {
			f.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("marshaling telemetry record: %w", err)
		}
		if _, err := w.Write(data); err != nil {
			f.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("writing telemetry record: %w", err)
		}
		if err := w.WriteByte('\n'); err != nil {
			f.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("writing telemetry newline: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("flushing telemetry file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing telemetry file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming telemetry file: %w", err)
	}
	return nil
}

// newRecordID generates a ULID-like identifier from the current time plus
// random hex. Not cryptographically guaranteed unique, but sufficient for
// local telemetry.
func newRecordID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%d%s", time.Now().UnixNano(), hex.EncodeToString(b[:]))
}
