// Package mrtelemetry records durable per-MR-attempt and per-review-attempt
// telemetry for the Gastown refinery / merge queue path.
//
// It exists to answer, without manual archaeology, which implementer (writer)
// models produce work that passes Codex review fastest: one row per MR attempt
// records the writer model, source bead, polecat, MR id, commit/tree, reviewer
// (Codex) verdict, validation verdict, timestamps, elapsed submit-to-Codex-
// verdict time, and a classified failure reason.
//
// Telemetry is strictly best-effort. Recording never panics and never returns
// an error that callers must handle to keep the merge path running: a missing
// or unwritable telemetry directory degrades to a no-op recorder so that a
// telemetry failure can never block or corrupt a merge.
//
// DESIGN INVARIANT — writer attribution across reassignment/rework: the
// writer_model recorded for an attempt is captured at the moment the refinery
// begins processing that attempt (claim), using the source bead's writer
// identity AT THAT TIME. A model reassignment that happens mid-flight, or a
// rework that re-dispatches the bead to a different model, does NOT retroactively
// rewrite the writer of an already-recorded attempt. The recorded writer is the
// model that authored the actual submitted MR attempt, not necessarily the
// model currently assigned to the source bead. This is the attribution rule the
// bead (gastown-wjk) requires so Mayor can compare umans-kimi vs umans-glm vs
// m3 on the current fleet without the data being muddied by reassignments.
package mrtelemetry

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultTelemetryDir is the refinery telemetry directory under the town
// runtime root. Records are written to a date-sharded JSONL file inside it,
// distinct from the reviewer-side refinery-gate-*.jsonl written by the live
// refinery-gate.sh shell script.
const DefaultTelemetryDir = "refinery-telemetry"

// AttemptsFilename returns the JSONL filename for a given UTC date. The
// "refinery-attempts-" prefix distinguishes these MR-attempt records from the
// reviewer-phase "refinery-gate-" records emitted by the live gate script.
func AttemptsFilename(t time.Time) string {
	return "refinery-attempts-" + t.UTC().Format("20060102") + ".jsonl"
}

// CodexVerdict is the outcome of the durable (Codex/multi-model) review for a
// single MR attempt. The empty string means "no review was attempted".
type CodexVerdict string

const (
	// CodexVerdictNone means the durable review gate did not run for this
	// attempt (e.g. pre-verified fast-path that skipped the gate, or the
	// attempt failed before reaching review).
	CodexVerdictNone CodexVerdict = "none"

	// CodexVerdictPass means the durable review gate produced a PASS verdict
	// (attestation written).
	CodexVerdictPass CodexVerdict = "pass"

	// CodexVerdictFail means the durable review gate produced a FAIL verdict.
	CodexVerdictFail CodexVerdict = "fail"

	// CodexVerdictUnavailable means a reviewer was selected but returned no
	// usable verdict (tooling down, adapter error). Distinct from a
	// substantive FAIL.
	CodexVerdictUnavailable CodexVerdict = "unavailable"

	// CodexVerdictNoVerdict means reviewers ran but none returned a verdict.
	CodexVerdictNoVerdict CodexVerdict = "no_verdict"
)

// FinalGateDecision is the terminal outcome of the refinery's gate pipeline
// for an attempt.
type FinalGateDecision string

const (
	// FinalMerged means the attempt merged successfully.
	FinalMerged FinalGateDecision = "merged"
	// FinalRejected means the attempt was rejected (needs rework / closed).
	FinalRejected FinalGateDecision = "rejected"
	// FinalFailed means the attempt failed non-substantively (infra, etc.)
	// and may be retried rather than reworked.
	FinalFailed FinalGateDecision = "failed"
)

// FailureClass classifies WHY an attempt did not merge, separating
// substantive implementation-quality failures from deterministic validation,
// reviewer-unavailable, timeout, convention, and infra noise. This is the
// field Mayor uses to exclude infra/unavailable/timeouts from a model's
// substantive pass-rate denominator.
type FailureClass string

const (
	// FailureNone means the attempt merged (no failure).
	FailureNone FailureClass = ""
	// FailureSubstantive means Codex review rejected on implementation quality
	// (concrete blockers). This is the signal that distinguishes models.
	FailureSubstantive FailureClass = "substantive_implementation"
	// FailureValidation means deterministic validation (tests/build/gofmt)
	// failed before peer review.
	FailureValidation FailureClass = "deterministic_validation"
	// FailureReviewerUnavailable means the reviewer tooling was unavailable
	// or returned no verdict (not a substantive FAIL).
	FailureReviewerUnavailable FailureClass = "reviewer_unavailable"
	// FailureTimeout means the review gate timed out.
	FailureTimeout FailureClass = "timeout"
	// FailureConvention means the commit message violated convention (WIP).
	FailureConvention FailureClass = "convention"
	// FailureInfra means an infrastructure error (merge slot, push failure,
	// branch not found, beads down) — excluded from a model's quality signal.
	FailureInfra FailureClass = "infra"
	// FailureConflict means a merge conflict during the squash.
	FailureConflict FailureClass = "conflict"
)

// AttemptRecord is one row per MR processing attempt. It is appended to the
// telemetry JSONL when an attempt reaches a terminal state (merged / rejected
// / failed), and is updated in memory as the refinery progresses through
// validation and review phases.
//
// All timestamps are RFC3339 in UTC. Empty/zero values are omitted only where
// noted; most fields are retained as zero-values so a single record always
// carries the full schema, making the JSONL self-describing and stable for
// downstream parsers.
type AttemptRecord struct {
	// Identity of the attempt.
	Rig        string `json:"rig"`
	SourceBead string `json:"source_bead"`
	MRID       string `json:"mr_id"`
	Attempt    int    `json:"attempt"` // 1-based ordinal for this source_bead

	// Who produced the work — captured at attempt start (ATTRIBUTION INVARIANT).
	Polecat     string `json:"polecat"`
	WriterModel string `json:"writer_model"`
	Branch      string `json:"branch"`
	CommitSHA   string `json:"commit_sha"`
	TreeSHA     string `json:"tree_sha"`

	// Timing.
	SubmittedAt         string `json:"submitted_at"`          // MR created/submitted
	RefineryStartedAt   string `json:"refinery_started_at"`    // refinery began processing
	ValidationStartedAt string `json:"validation_started_at"` // gates/tests began
	ValidationFinishedAt string `json:"validation_finished_at"`
	CodexReviewStartedAt  string `json:"codex_review_started_at"`
	CodexReviewFinishedAt string `json:"codex_review_finished_at"`
	MergedAt             string `json:"merged_at,omitempty"`
	RejectedAt           string `json:"rejected_at,omitempty"`

	// Outcomes.
	CodexVerdict      CodexVerdict      `json:"codex_verdict"`
	ValidationVerdict string           `json:"validation_verdict,omitempty"` // pass/fail/skipped
	FinalGateDecision FinalGateDecision `json:"final_gate_decision"`
	FailureClass      FailureClass     `json:"failure_class,omitempty"`

	// Forensics / artifacts.
	RawLogPath       string `json:"raw_log_path,omitempty"`
	ReworkPacketPath string `json:"rework_packet_path,omitempty"`
	ReviewerCause   string `json:"reviewer_cause,omitempty"` // machine-readable rejection key
}

// nowFunc is the time source, overridable for tests.
var nowFunc = time.Now

// Recorder appends AttemptRecords to a date-sharded JSONL file. It is safe
// for concurrent use by multiple refinery workers. A nil Recorder is valid
// and all methods are no-ops, so callers need not nil-check.
type Recorder struct {
	dir string
	mu  sync.Mutex
	// handles holds in-progress attempts keyed by attemptKey, so that phase
	// updates accumulate into the same record before it is finalized.
	handles map[attemptKey]*AttemptRecord
}

type attemptKey struct {
	sourceBead string
	mrID       string
	commitSHA  string
}

// NewRecorder returns a Recorder writing to dir. If dir cannot be created or
// is empty, the returned recorder is a no-op (all methods safe to call).
func NewRecorder(dir string) *Recorder {
	if dir == "" {
		return nil
	}
	// Best-effort: create the directory. If it fails, return a recorder whose
	// dir is empty so Append finalization is a no-op. We never return an error
	// because telemetry must not break the merge path.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return &Recorder{dir: "", handles: map[attemptKey]*AttemptRecord{}}
	}
	return &Recorder{dir: dir, handles: map[attemptKey]*AttemptRecord{}}
}

// Dir returns the recorder's directory (empty for a no-op recorder).
func (r *Recorder) Dir() string {
	if r == nil {
		return ""
	}
	return r.dir
}

// StartAttempt records the start of refinery processing for an MR attempt and
// returns a handle for subsequent phase updates. The writer_model and other
// identity fields are captured here (ATTRIBUTION INVARIANT): later
// reassignment does not change them.
//
// attemptNum is the 1-based ordinal for this source_bead; callers should
// derive it as (prior terminal attempts for source_bead) + 1 so rework
// attempts increment correctly.
func (r *Recorder) StartAttempt(rec AttemptRecord) {
	if r == nil {
		return
	}
	if rec.RefineryStartedAt == "" {
		rec.RefineryStartedAt = nowFunc().UTC().Format(time.RFC3339)
	}
	k := attemptKey{sourceBead: rec.SourceBead, mrID: rec.MRID, commitSHA: rec.CommitSHA}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.handles == nil {
		r.handles = map[attemptKey]*AttemptRecord{}
	}
	r.handles[k] = &rec
}

// Update applies a mutator to the in-progress attempt identified by
// (sourceBead, mrID, commitSHA). If no in-progress attempt matches, the
// update is dropped (best-effort). This is used to stamp phase timestamps and
// verdicts as the refinery progresses.
func (r *Recorder) Update(sourceBead, mrID, commitSHA string, mut func(*AttemptRecord)) {
	if r == nil {
		return
	}
	k := attemptKey{sourceBead: sourceBead, mrID: mrID, commitSHA: commitSHA}
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.handles[k]; ok && mut != nil {
		mut(rec)
	}
}

// Finalize writes the attempt record to the JSONL log and drops the in-memory
// handle. It is called when an attempt reaches a terminal state (merged,
// rejected, failed). The record's FinalGateDecision and terminal timestamp are
// set from the mutator before writing. Best-effort: write failures are
// swallowed (logged via the returned error only for tests that pass a writer).
func (r *Recorder) Finalize(sourceBead, mrID, commitSHA string, mut func(*AttemptRecord)) {
	if r == nil {
		return
	}
	k := attemptKey{sourceBead: sourceBead, mrID: mrID, commitSHA: commitSHA}
	r.mu.Lock()
	rec, ok := r.handles[k]
	if ok {
		delete(r.handles, k)
	}
	r.mu.Unlock()
	if !ok || rec == nil {
		return
	}
	if mut != nil {
		mut(rec)
	}
	_ = r.append(*rec)
}

// append writes one record to the date-sharded JSONL file. It uses a file
// lock to serialize concurrent appends from multiple refinery workers.
func (r *Recorder) append(rec AttemptRecord) error {
	if r == nil || r.dir == "" {
		return nil
	}
	path := filepath.Join(r.dir, AttemptsFilename(nowFunc()))
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	// O_APPEND opens atomically for small writes on POSIX; the mutex above
	// serializes this recorder's own appends. A second refinery worker uses a
	// separate Recorder, so we additionally take a flock-style lock file.
	return appendLocked(path, data)
}

// appendLocked appends data to path, coordinating with other processes via a
// companion .lock file (flock). It falls back to a plain append if locking
// fails, preferring a written record over a perfectly-coordinated one.
func appendLocked(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := flock(f); err == nil {
		defer funlock(f)
	}
	_, err = f.Write(data)
	return err
}

// readRecords reads every AttemptRecord from the JSONL files in dir matching
// the optional date range. Malformed lines are skipped (matching the live
// gate's jq behavior).
func readRecords(dir string, since, until time.Time) ([]AttemptRecord, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []AttemptRecord
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasPrefix(ent.Name(), "refinery-attempts-") || !strings.HasSuffix(ent.Name(), ".jsonl") {
			continue
		}
		// Parse date from filename: refinery-attempts-YYYYMMDD.jsonl
		dateStr := strings.TrimSuffix(strings.TrimPrefix(ent.Name(), "refinery-attempts-"), ".jsonl")
		t, err := time.ParseInLocation("20060102", dateStr, time.UTC)
		if err != nil {
			continue
		}
		day := t
		if !since.IsZero() && day.Before(since) && !day.Equal(since) {
			continue
		}
		// 'until' is exclusive of the day after; keep days on or before until's day.
		if !until.IsZero() {
			untilDay := time.Date(until.Year(), until.Month(), until.Day(), 0, 0, 0, 0, until.Location())
			if day.After(untilDay) {
				continue
			}
		}
		recs, err := readOne(filepath.Join(dir, ent.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	// Sort by timestamp then attempt for stable output.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].SubmittedAt < out[j].SubmittedAt
	})
	return out, nil
}

func readOne(path string) ([]AttemptRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []AttemptRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec AttemptRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // skip malformed
		}
		out = append(out, rec)
	}
	return out, scanner.Err()
}

// ReadRecords is the exported reader for the report command. since/until may
// be zero to mean "unbounded" on that side.
func ReadRecords(dir string, since, until time.Time) ([]AttemptRecord, error) {
	return readRecords(dir, since, until)
}

// CountPriorAttempts returns the number of attempt records already logged for
// the given source_bead. It is used to compute the 1-based attempt ordinal for
// a new attempt so rework attempts increment correctly. Returns 0 when no
// records exist or the dir is empty. Best-effort: read errors yield 0.
func CountPriorAttempts(dir, sourceBead string) (int, error) {
	if dir == "" || sourceBead == "" {
		return 0, nil
	}
	all, err := readRecords(dir, time.Time{}, time.Time{})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, rec := range all {
		if rec.SourceBead == sourceBead {
			n++
		}
	}
	return n, nil
}

// parseRFC3339 is a tolerant parser used by the summarizer; empty strings
// yield the zero time.
func parseRFC3339(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// submitToCodexVerdictDuration returns the elapsed time from RefineryStartedAt
// to CodexReviewFinishedAt for a record, or zero if either is missing.
func (rec AttemptRecord) submitToCodexVerdictDuration() time.Duration {
	start := parseRFC3339(rec.RefineryStartedAt)
	end := parseRFC3339(rec.CodexReviewFinishedAt)
	if start.IsZero() || end.IsZero() {
		return 0
	}
	d := end.Sub(start)
	if d < 0 {
		return 0
	}
	return d
}

// MedianDuration returns the median of a slice of durations. Zero for empty.
func MedianDuration(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// PercentileDuration returns the p-th percentile (0..100) of ds. p is clamped.
// Zero for empty input. Uses nearest-rank.
func PercentileDuration(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	// nearest-rank
	idx := int((p / 100.0) * float64(len(sorted)-1))
	return sorted[idx]
}

// WriterModelSummary is the per-writer-model rollup the report command emits.
type WriterModelSummary struct {
	WriterModel string `json:"writer_model"`

	TotalAttempts int `json:"total_attempts"`
	// FirstPassCodexPass is attempts whose FIRST Codex verdict on the source
	// bead was PASS (no prior rework). FirstPassCodexPassRate divides by
	// FirstPassEligible (attempts that actually reached review).
	FirstPassCodexPass     int     `json:"first_pass_codex_pass"`
	FirstPassEligible      int     `json:"first_pass_eligible"`
	FirstPassCodexPassRate float64 `json:"first_pass_codex_pass_rate"`

	CodexPassCount    int `json:"codex_pass_count"`
	CodexFailCount    int `json:"codex_fail_count"`
	CodexUnavailable  int `json:"codex_unavailable_count"`
	CodexNoVerdict    int `json:"codex_no_verdict_count"`
	CodexNoneCount    int `json:"codex_none_count"` // review never ran

	MergedCount    int `json:"merged_count"`
	RejectedCount  int `json:"rejected_count"`
	FailedCount    int `json:"failed_count"`
	FinalMergeRate float64 `json:"final_merge_rate"`

	ReworkCount    int     `json:"rework_count"`
	ReworkAttemptRate float64 `json:"rework_attempt_rate"` // fraction of attempts that were reworks (attempt>1)

	// Time-to-Codex-verdict over all attempts that reached a verdict.
	MedianTimeToVerdict time.Duration `json:"median_time_to_verdict_ms"`
	P95TimeToVerdict   time.Duration `json:"p95_time_to_verdict_ms"`

	// Excluded counts (infra/unavailable/timeout) for transparency.
	ExcludedInfra       int `json:"excluded_infra"`
	ExcludedUnavailable int `json:"excluded_unavailable"`
	ExcludedTimeout     int `json:"excluded_timeout"`
}

// SummaryOptions controls Summarize.
type SummaryOptions struct {
	// Since/Until bound the records (zero = unbounded on that side).
	Since time.Time
	Until time.Time
}

// Summarize rolls up records by writer_model. It computes first-pass Codex
// pass rate (per source_bead, the first attempt's verdict), final merge rate,
// rework counts, and median/p95 submit-to-Codex-verdict time.
//
// First-pass attribution: for each source_bead, the attempt with the lowest
// Attempt ordinal is the "first pass"; its CodexVerdict determines the
// first-pass signal. This correctly attributes a writer even when a later
// rework attempt is performed by a different model, because the first-pass
// attempt's WriterModel (captured at its start) is used.
func Summarize(records []AttemptRecord, opts SummaryOptions) []WriterModelSummary {
	// Filter by date window on SubmittedAt.
	var filtered []AttemptRecord
	for _, rec := range records {
		t := parseRFC3339(rec.SubmittedAt)
		if !t.IsZero() {
			if !opts.Since.IsZero() && t.Before(opts.Since) {
				continue
			}
			if !opts.Until.IsZero() && !t.Before(opts.Until) {
				continue
			}
		}
		filtered = append(filtered, rec)
	}

	// First-pass per source_bead.
	type firstPassInfo struct {
		writer  string
		verdict CodexVerdict
		ordinal int
	}
	firstPass := map[string]firstPassInfo{}
	for _, rec := range filtered {
		fp, ok := firstPass[rec.SourceBead]
		if !ok || rec.Attempt < fp.ordinal {
			firstPass[rec.SourceBead] = firstPassInfo{writer: rec.WriterModel, verdict: rec.CodexVerdict, ordinal: rec.Attempt}
		}
	}

	type agg struct {
		total, firstPassEligible, firstPassPass int
		codexPass, codexFail, codexUnavail, codexNoVerdict, codexNone int
		merged, rejected, failed, rework int
		excludedInfra, excludedUnavail, excludedTimeout int
		durations []time.Duration
	}
	aggs := map[string]*agg{}
	for _, rec := range filtered {
		w := rec.WriterModel
		if w == "" {
			w = "unknown"
		}
		a := aggs[w]
		if a == nil {
			a = &agg{}
			aggs[w] = a
		}
		a.total++
		if rec.Attempt > 1 {
			a.rework++
		}
		switch rec.CodexVerdict {
		case CodexVerdictPass:
			a.codexPass++
		case CodexVerdictFail:
			a.codexFail++
		case CodexVerdictUnavailable:
			a.codexUnavail++
		case CodexVerdictNoVerdict:
			a.codexNoVerdict++
		case CodexVerdictNone:
			a.codexNone++
		}
		switch rec.FinalGateDecision {
		case FinalMerged:
			a.merged++
		case FinalRejected:
			a.rejected++
		case FinalFailed:
			a.failed++
		}
		switch rec.FailureClass {
		case FailureInfra:
			a.excludedInfra++
		case FailureReviewerUnavailable:
			a.excludedUnavail++
		case FailureTimeout:
			a.excludedTimeout++
		}
		if d := rec.submitToCodexVerdictDuration(); d > 0 {
			a.durations = append(a.durations, d)
		}
	}
	// First-pass eligible/pass: an attempt is "first-pass eligible" if it is
	// the first attempt for its source_bead AND it reached Codex review
	// (verdict != none).
	for _, rec := range filtered {
		fp, ok := firstPass[rec.SourceBead]
		if !ok || rec.WriterModel != fp.writer || rec.Attempt != fp.ordinal {
			continue
		}
		w := rec.WriterModel
		if w == "" {
			w = "unknown"
		}
		a := aggs[w]
		if rec.CodexVerdict != CodexVerdictNone {
			a.firstPassEligible++
			if rec.CodexVerdict == CodexVerdictPass {
				a.firstPassPass++
			}
		}
	}

	out := make([]WriterModelSummary, 0, len(aggs))
	for w, a := range aggs {
		s := WriterModelSummary{
			WriterModel:          w,
			TotalAttempts:        a.total,
			FirstPassCodexPass:   a.firstPassPass,
			FirstPassEligible:    a.firstPassEligible,
			CodexPassCount:       a.codexPass,
			CodexFailCount:       a.codexFail,
			CodexUnavailable:     a.codexUnavail,
			CodexNoVerdict:       a.codexNoVerdict,
			CodexNoneCount:       a.codexNone,
			MergedCount:          a.merged,
			RejectedCount:        a.rejected,
			FailedCount:          a.failed,
			ReworkCount:          a.rework,
			MedianTimeToVerdict:  MedianDuration(a.durations),
			P95TimeToVerdict:     PercentileDuration(a.durations, 95),
			ExcludedInfra:        a.excludedInfra,
			ExcludedUnavailable:  a.excludedUnavail,
			ExcludedTimeout:      a.excludedTimeout,
		}
		if a.total > 0 {
			s.FinalMergeRate = float64(a.merged) / float64(a.total)
			s.ReworkAttemptRate = float64(a.rework) / float64(a.total)
		}
		if a.firstPassEligible > 0 {
			s.FirstPassCodexPassRate = float64(a.firstPassPass) / float64(a.firstPassEligible)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].WriterModel < out[j].WriterModel
	})
	return out
}

// FormatSummaryTable writes a human-readable comparison table to w.
func FormatSummaryTable(w io.Writer, summaries []WriterModelSummary) {
	if len(summaries) == 0 {
		fmt.Fprintln(w, "No telemetry records found.")
		return
	}
	fmt.Fprintf(w, "%-16s %6s %8s %8s %8s %8s %8s %12s %12s %8s\n",
		"WRITER", "ATTEMPTS", "1STPASS%", "CODEXPASS", "FAIL", "MERGED%", "REWORK", "MED_ms", "P95_ms", "EXCL")
	for _, s := range summaries {
		fmt.Fprintf(w, "%-16s %6d %7.1f%% %9d %8d %7.1f%% %8d %12d %12d %8d\n",
			s.WriterModel, s.TotalAttempts,
			s.FirstPassCodexPassRate*100, s.CodexPassCount, s.CodexFailCount,
			s.FinalMergeRate*100, s.ReworkCount,
			s.MedianTimeToVerdict.Milliseconds(), s.P95TimeToVerdict.Milliseconds(),
			s.ExcludedInfra+s.ExcludedUnavailable+s.ExcludedTimeout)
	}
}

// DurationToMillis returns milliseconds for compact display.
func DurationToMillis(d time.Duration) int64 { return d.Milliseconds() }
