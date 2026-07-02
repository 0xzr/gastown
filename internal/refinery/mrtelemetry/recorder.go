package mrtelemetry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Recorder writes AttemptRecords to append-only daily JSONL files under
// <Dir>/refinery-attempts-YYYYMMDD.jsonl. It is safe for concurrent use:
// each Append takes a process-local mutex and an exclusive file lock so
// concurrent refinery goroutines (or processes sharing the directory) do not
// interleave records.
//
// Best-effort: every Append recovers from panic and swallows write errors
// (logging them to ErrOut if set). Telemetry must never break the merge path.
type Recorder struct {
	// Dir is the telemetry directory (e.g. <townRoot>/.runtime/refinery-telemetry).
	// When empty, Append is a no-op (recorder disabled).
	Dir string

	// ErrOut receives non-fatal write errors for diagnostics. May be nil.
	ErrOut io.Writer

	// now returns the current time. Defaults to time.Now; overridable in tests.
	now func() time.Time

	mu sync.Mutex
	// attempts holds in-progress attempt records keyed by attemptKey. The
	// Recorder is typically single-threaded per rig (the refinery processes
	// one MR at a time), but the map guards against re-entrancy and concurrent
	// finalization.
	attempts map[string]*AttemptRecord
}

// attemptKey is the in-memory handle key: mr_id + "#" + attempt number.
func attemptKey(mrID string, attempt int) string {
	return fmt.Sprintf("%s#%d", mrID, attempt)
}

// NewRecorder returns a Recorder writing to dir. A nil or empty dir yields a
// no-op recorder (Append/BeginAttempt/Finalize are safe no-ops).
func NewRecorder(dir string) *Recorder {
	r := &Recorder{
		Dir:      dir,
		now:      time.Now,
		attempts: make(map[string]*AttemptRecord),
	}
	return r
}

// WithErrOut sets the error diagnostic writer and returns the recorder.
func (r *Recorder) WithErrOut(w io.Writer) *Recorder {
	if r == nil {
		return r
	}
	r.ErrOut = w
	return r
}

// WithNow overrides the now function (for tests).
func (r *Recorder) WithNow(now func() time.Time) *Recorder {
	if r == nil {
		return r
	}
	r.now = now
	return r
}

// disabled reports whether the recorder is a no-op.
func (r *Recorder) disabled() bool {
	return r == nil || r.Dir == ""
}

// logErr writes a non-fatal error to ErrOut if set.
func (r *Recorder) logErr(format string, args ...any) {
	if r == nil || r.ErrOut == nil {
		return
	}
	fmt.Fprintf(r.ErrOut, "[mrtelemetry] "+format+"\n", args...)
}

// AttemptStart captures the immutable identity of an attempt at claim time.
// AttemptNumber should be 1 + the count of prior terminal attempts for the
// same source bead. WriterModel is captured HERE (attribution invariant) so
// a mid-flight reassignment does not retrobably change the recorded writer.
type AttemptStart struct {
	Rig         string
	SourceBead  string
	MRID        string
	Attempt     int
	Polecat     string
	WriterModel string
	Branch      string
	CommitSHA   string
	TreeSHA     string
	SubmittedAt time.Time
}

// BeginAttempt records the attempt's start-of-life identity in memory and
// returns a handle the caller mutates as the attempt progresses. The handle is
// also retrievable via the (mr_id, attempt) key so different instrumentation
// points (runGates, runDurableReviewGate, Handle*Success/Failure) can
// accumulate timestamps into the same record.
//
// It is nil-safe: a nil receiver yields a nil handle (callers must check).
func (r *Recorder) BeginAttempt(s AttemptStart) *AttemptHandle {
	if r.disabled() {
		return nil
	}
	if s.Attempt < 1 {
		s.Attempt = 1
	}
	rec := &AttemptRecord{
		Rig:               s.Rig,
		SourceBead:        s.SourceBead,
		MRID:              s.MRID,
		Attempt:           s.Attempt,
		Polecat:           s.Polecat,
		WriterModel:       s.WriterModel,
		Branch:            s.Branch,
		CommitSHA:         s.CommitSHA,
		TreeSHA:           s.TreeSHA,
		SubmittedAt:       FormatTime(s.SubmittedAt),
		RefineryStartedAt: FormatTime(r.now()),
		FinalGateDecision: "",
		FailureClass:      FailureClassNone,
	}
	key := attemptKey(s.MRID, s.Attempt)
	r.mu.Lock()
	r.attempts[key] = rec
	r.mu.Unlock()
	return &AttemptHandle{rec: rec, key: key, recorder: r}
}

// AttemptHandle is a mutable, in-progress attempt record. Holders call the Set*
// methods to accumulate timestamps/verdicts as the attempt progresses, then
// Finalize to append the record to disk.
type AttemptHandle struct {
	rec      *AttemptRecord
	key      string
	recorder *Recorder
	// finalized guards against double-append (success + failure both fire).
	finalized bool
	mu        sync.Mutex
}

// SetValidation marks the validation (deterministic gate) phase start/finish
// and verdict. Either timestamp may be zero (skipped).
func (h *AttemptHandle) SetValidation(started, finished time.Time, verdict string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !started.IsZero() {
		h.rec.ValidationStartedAt = FormatTime(started)
	}
	if !finished.IsZero() {
		h.rec.ValidationFinishedAt = FormatTime(finished)
	}
	if verdict != "" {
		h.rec.ValidationVerdict = verdict
	}
}

// SetCodexReview marks the codex/durable-review phase start/finish and verdict.
func (h *AttemptHandle) SetCodexReview(started, finished time.Time, verdict string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !started.IsZero() {
		h.rec.CodexReviewStartedAt = FormatTime(started)
	}
	if !finished.IsZero() {
		h.rec.CodexReviewFinishedAt = FormatTime(finished)
	}
	if verdict != "" {
		h.rec.CodexVerdict = verdict
	}
}

// SetCommit records the commit/tree SHAs once known (may be set after Begin).
func (h *AttemptHandle) SetCommit(commitSHA, treeSHA string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if commitSHA != "" {
		h.rec.CommitSHA = commitSHA
	}
	if treeSHA != "" {
		h.rec.TreeSHA = treeSHA
	}
}

// SetRawLogPath records the path to raw gate/review logs for the attempt.
func (h *AttemptHandle) SetRawLogPath(p string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rec.RawLogPath = p
}

// SetReworkPacket records the rework packet path (when applicable).
func (h *AttemptHandle) SetReworkPacket(p string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rec.ReworkPacketPath = p
}

// SetCodexVerdict sets only the codex verdict (used when the review gate is
// skipped/attested-short-circuit but a verdict is still known).
func (h *AttemptHandle) SetCodexVerdict(verdict string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if verdict != "" {
		h.rec.CodexVerdict = verdict
	}
}

// Finalize writes the completed record to disk. The decision and failureClass
// classify the terminal state; mergedAt/rejectedAt set the terminal timestamp.
//
// Finalize is idempotent: a second call (e.g. failure after success) is a
// no-op. The first terminal call wins so the record reflects the authoritative
// outcome the refinery acted on.
func (h *AttemptHandle) Finalize(decision, failureClass string, terminalAt time.Time) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.finalized {
		h.mu.Unlock()
		return
	}
	h.finalized = true
	rec := *h.rec
	h.mu.Unlock()

	if decision != "" {
		rec.FinalGateDecision = decision
	}
	if failureClass != "" {
		rec.FailureClass = failureClass
	} else if rec.FailureClass == "" {
		rec.FailureClass = FailureClassNone
	}
	switch decision {
	case DecisionMerged:
		rec.MergedAt = FormatTime(terminalAt)
	case DecisionRejected, DecisionFailed:
		rec.RejectedAt = FormatTime(terminalAt)
	}
	// If codex verdict was never set, record "none" so it is explicit.
	if rec.CodexVerdict == "" {
		rec.CodexVerdict = CodexVerdictNone
	}
	rec.RecordedAt = FormatTime(h.recorder.now())

	// Remove from in-memory map.
	h.recorder.mu.Lock()
	delete(h.recorder.attempts, h.key)
	h.recorder.mu.Unlock()

	h.recorder.Append(rec)
}

// Append writes a single fully-formed record to the daily JSONL file. It is
// best-effort: any error is logged to ErrOut and swallowed. The recorder is
// nil-safe.
func (r *Recorder) Append(rec AttemptRecord) {
	if r.disabled() {
		return
	}
	// Best-effort recovery: a panic during encoding must not crash the merge path.
	defer func() {
		if rec_ := recover(); rec_ != nil {
			r.logErr("panic appending telemetry record: %v", rec_)
		}
	}()

	if rec.RecordedAt == "" {
		rec.RecordedAt = FormatTime(r.now())
	}

	line, err := rec.Marshal()
	if err != nil {
		r.logErr("marshal: %v", err)
		return
	}
	line = append(line, '\n')

	if err := r.ensureDir(); err != nil {
		r.logErr("ensure dir: %v", err)
		return
	}
	path := r.dailyPath()
	if err := appendLocked(path, line); err != nil {
		r.logErr("append %s: %v", path, err)
		return
	}
}

// dailyPath returns today's JSONL file path.
func (r *Recorder) dailyPath() string {
	return filepath.Join(r.Dir, "refinery-attempts-"+r.now().UTC().Format("20060102")+".jsonl")
}

// Files returns the paths of telemetry JSONL files in the directory, optionally
// restricted to those modified within the since window. Used by the summarizer
// and CLI. Returns an empty slice (not an error) if the directory is missing.
func (r *Recorder) Files(since time.Time) ([]string, error) {
	if r.disabled() {
		return nil, nil
	}
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !matchesTelemetryFile(name) {
			continue
		}
		if !since.IsZero() {
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(since) {
				continue
			}
		}
		out = append(out, filepath.Join(r.Dir, name))
	}
	return out, nil
}

// matchesTelemetryFile reports whether a filename is a refinery-attempts JSONL.
func matchesTelemetryFile(name string) bool {
	const prefix, suffix = "refinery-attempts-", ".jsonl"
	if len(name) < len(prefix)+len(suffix) {
		return false
	}
	return name[:len(prefix)] == prefix && name[len(name)-len(suffix):] == suffix
}

// ensureDir creates the telemetry directory (and parents) if missing.
func (r *Recorder) ensureDir() error {
	if r.Dir == "" {
		return nil
	}
	return os.MkdirAll(r.Dir, 0o755)
}

// appendLocked appends data to path, holding an exclusive lock on the file for
// the duration of the write. The file is created (0o644) if it does not exist.
func appendLocked(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open telemetry file: %w", err)
	}
	defer f.Close()
	return withFileLock(f, func() error {
		_, err := f.Write(data)
		return err
	})
}

// ParseTime parses an RFC3339 telemetry timestamp, returning the zero time on
// empty/unparseable input. Used by the summarizer.
func ParseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// asJSON is a small helper for the CLI to render a value as indented JSON.
func asJSON(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return []byte("{}")
	}
	return b
}
