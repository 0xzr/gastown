package mrtelemetry

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

// fixedNow is a deterministic clock for tests.
func fixedNow() time.Time {
	return time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
}

// newTestRecorder returns a Recorder writing to a temp dir with a fixed clock.
func newTestRecorder(t *testing.T) (*Recorder, string) {
	t.Helper()
	dir := t.TempDir()
	r := NewRecorder(dir).WithNow(fixedNow)
	return r, dir
}

// startAndFinalize begins an attempt and finalizes it with the given decision.
func startAndFinalize(r *Recorder, s AttemptStart, decision, failureClass string) {
	h := r.BeginAttempt(s)
	if h == nil {
		panic("nil attempt handle")
	}
	h.Finalize(decision, failureClass, fixedNow())
}

func TestRecorder_DisabledWhenDirEmpty(t *testing.T) {
	r := NewRecorder("")
	h := r.BeginAttempt(AttemptStart{MRID: "m1", Attempt: 1})
	if h != nil {
		t.Fatalf("expected nil handle for disabled recorder, got %v", h)
	}
	// Append must be a no-op, not a panic.
	r.Append(AttemptRecord{MRID: "m1"})
}

func TestBeginAttempt_CapturesWriterAtStart(t *testing.T) {
	// ATTRIBUTION INVARIANT: the writer_model recorded for an attempt is the one
	// captured at BeginAttempt (claim) time. A subsequent reassignment must NOT
	// change the writer of an already-begun attempt.
	r, dir := newTestRecorder(t)
	defer os.RemoveAll(dir)

	start := AttemptStart{
		Rig:         "gastown",
		SourceBead:  "gt-abc",
		MRID:        "m1",
		Attempt:     1,
		Polecat:     "furiosa",
		WriterModel: "umans-kimi", // captured here
		Branch:      "polecat/furiosa/gt-abc@aaa",
		SubmittedAt: fixedNow().Add(-2 * time.Minute),
	}
	h := r.BeginAttempt(start)
	if h == nil {
		t.Fatal("expected non-nil handle")
	}

	// Simulate a mid-flight reassignment: a NEW attempt for the same MR with a
	// different writer. The original handle must still carry the original writer.
	h2 := r.BeginAttempt(AttemptStart{
		Rig: "gastown", SourceBead: "gt-abc", MRID: "m1", Attempt: 2,
		Polecat: "furiosa", WriterModel: "umans-glm", // reassigned
		Branch: "polecat/furiosa/gt-abc@bbb",
	})

	// Finalize both. The first attempt's writer must remain umans-kimi.
	h.Finalize(DecisionMerged, FailureClassNone, fixedNow())
	h2.Finalize(DecisionRejected, FailureClassSubstantiveImpl, fixedNow())

	records := readAllRecords(t, r)
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}

	byAttempt := map[int]AttemptRecord{}
	for _, rec := range records {
		byAttempt[rec.Attempt] = rec
	}
	if got := byAttempt[1].WriterModel; got != "umans-kimi" {
		t.Errorf("attempt 1 writer = %q, want umans-kimi (attribution invariant)", got)
	}
	if got := byAttempt[2].WriterModel; got != "umans-glm" {
		t.Errorf("attempt 2 writer = %q, want umans-glm", got)
	}
}

func TestFinalize_Idempotent(t *testing.T) {
	// A second Finalize (e.g. failure handler firing after success) must be a
	// no-op: the first terminal decision wins. This prevents double-append.
	r, dir := newTestRecorder(t)
	defer os.RemoveAll(dir)

	h := r.BeginAttempt(AttemptStart{MRID: "m1", Attempt: 1, WriterModel: "w"})
	h.Finalize(DecisionMerged, FailureClassNone, fixedNow())
	// Second finalization with a contradictory decision must not overwrite.
	h.Finalize(DecisionRejected, FailureClassSubstantiveImpl, fixedNow())

	records := readAllRecords(t, r)
	if len(records) != 1 {
		t.Fatalf("expected exactly 1 record (idempotent), got %d", len(records))
	}
	if records[0].FinalGateDecision != DecisionMerged {
		t.Errorf("decision = %q, want %q (first wins)", records[0].FinalGateDecision, DecisionMerged)
	}
	if records[0].RejectedAt != "" {
		t.Errorf("rejected_at = %q, want empty (merged record)", records[0].RejectedAt)
	}
}

func TestFinalize_SetsTerminalTimestamp(t *testing.T) {
	r, dir := newTestRecorder(t)
	defer os.RemoveAll(dir)

	// Merged -> MergedAt set, RejectedAt empty.
	startAndFinalize(r, AttemptStart{MRID: "m1", Attempt: 1}, DecisionMerged, FailureClassNone)
	// Rejected -> RejectedAt set, MergedAt empty.
	startAndFinalize(r, AttemptStart{MRID: "m2", Attempt: 1}, DecisionRejected, FailureClassSubstantiveImpl)
	// Failed -> RejectedAt set (treated as a rejection terminal).
	startAndFinalize(r, AttemptStart{MRID: "m3", Attempt: 1}, DecisionFailed, FailureClassInfra)

	records := readAllRecords(t, r)
	byMR := map[string]AttemptRecord{}
	for _, rec := range records {
		byMR[rec.MRID] = rec
	}

	if byMR["m1"].MergedAt == "" || byMR["m1"].RejectedAt != "" {
		t.Errorf("merged record: merged_at=%q rejected_at=%q", byMR["m1"].MergedAt, byMR["m1"].RejectedAt)
	}
	if byMR["m2"].RejectedAt == "" || byMR["m2"].MergedAt != "" {
		t.Errorf("rejected record: merged_at=%q rejected_at=%q", byMR["m2"].MergedAt, byMR["m2"].RejectedAt)
	}
	if byMR["m3"].RejectedAt == "" {
		t.Errorf("failed record: rejected_at empty, want set")
	}
}

func TestFinalize_CodexVerdictDefaultsToNone(t *testing.T) {
	// An attempt that never reached the codex review gate must record an
	// explicit "none" verdict (not an empty string) so the summarizer can
	// distinguish "no verdict" from "field absent".
	r, dir := newTestRecorder(t)
	defer os.RemoveAll(dir)

	startAndFinalize(r, AttemptStart{MRID: "m1", Attempt: 1}, DecisionMerged, FailureClassNone)
	records := readAllRecords(t, r)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].CodexVerdict != CodexVerdictNone {
		t.Errorf("codex_verdict = %q, want %q", records[0].CodexVerdict, CodexVerdictNone)
	}
}

func TestAttemptHandle_SetValidationAndCodexReview(t *testing.T) {
	r, dir := newTestRecorder(t)
	defer os.RemoveAll(dir)

	vStart, vFin := fixedNow().Add(-10*time.Minute), fixedNow().Add(-9*time.Minute)
	cStart, cFin := fixedNow().Add(-8*time.Minute), fixedNow().Add(-7*time.Minute)

	h := r.BeginAttempt(AttemptStart{MRID: "m1", Attempt: 1, SubmittedAt: fixedNow().Add(-11 * time.Minute)})
	h.SetValidation(vStart, vFin, ValidationVerdictPass)
	h.SetCodexReview(cStart, cFin, CodexVerdictPass)
	h.SetCommit("deadbeef", "cafebabe")
	h.Finalize(DecisionMerged, FailureClassNone, fixedNow())

	records := readAllRecords(t, r)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	rec := records[0]
	if rec.ValidationVerdict != ValidationVerdictPass {
		t.Errorf("validation_verdict = %q", rec.ValidationVerdict)
	}
	if rec.CodexVerdict != CodexVerdictPass {
		t.Errorf("codex_verdict = %q", rec.CodexVerdict)
	}
	if rec.CommitSHA != "deadbeef" || rec.TreeSHA != "cafebabe" {
		t.Errorf("commit/tree = %q/%q", rec.CommitSHA, rec.TreeSHA)
	}
	// Latency (submit -> codex verdict) is computed by the summarizer; here we
	// just confirm the timestamps round-trip.
	if rec.CodexReviewFinishedAt == "" {
		t.Error("codex_review_finished_at empty")
	}
}

func TestRecorder_AtomicAppendNoInterleave(t *testing.T) {
	// Concurrent appends must not interleave: each record is one JSONL line.
	// This guards the file-lock path.
	r, dir := newTestRecorder(t)
	defer os.RemoveAll(dir)

	const n = 50
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			startAndFinalize(r, AttemptStart{
				MRID: "m", Attempt: i + 1, WriterModel: "w",
				SubmittedAt: fixedNow(),
			}, DecisionMerged, FailureClassNone)
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}

	records := readAllRecords(t, r)
	if len(records) != n {
		t.Fatalf("expected %d records, got %d", n, len(records))
	}
	// Every record must be well-formed (already enforced by decode); verify
	// attempt numbers are a unique 1..n set.
	seen := map[int]bool{}
	for _, rec := range records {
		if seen[rec.Attempt] {
			t.Errorf("duplicate attempt %d", rec.Attempt)
		}
		seen[rec.Attempt] = true
	}
}

func TestRecorder_FilesSince(t *testing.T) {
	// The since filter restricts by file mtime. Write a record today; a since
	// window of 1h ago must include it, a since window of 1h in the future must
	// exclude it.
	r, dir := newTestRecorder(t)
	defer os.RemoveAll(dir)

	startAndFinalize(r, AttemptStart{MRID: "m1", Attempt: 1}, DecisionMerged, FailureClassNone)

	files, err := r.Files(time.Now().Add(-1 * time.Hour))
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file within 1h window, got %d", len(files))
	}
	files, err = r.Files(time.Now().Add(1 * time.Hour))
	if err != nil {
		t.Fatalf("Files future: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected 0 files within future window, got %d", len(files))
	}
}

func TestRecorder_FilesMissingDir(t *testing.T) {
	// A missing directory yields an empty slice, not an error.
	r := NewRecorder(filepath.Join(t.TempDir(), "does-not-exist"))
	files, err := r.Files(time.Time{})
	if err != nil {
		t.Fatalf("Files on missing dir: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected 0 files, got %d", len(files))
	}
}

func TestSummarize_FirstPassRateAndRework(t *testing.T) {
	// Build records directly and summarize to verify the math.
	// Scenario (writer "kimi"):
	//   attempt 1 on bead-A: codex PASS, merged     -> first-pass pass
	//   attempt 1 on bead-B: codex FAIL, rejected  -> NOT a first-pass pass
	//   attempt 2 on bead-B: codex PASS, merged     -> rework (attempt > 1)
	// Writer "glm":
	//   attempt 1 on bead-C: codex UNAVAILABLE      -> excluded reviewer-unavailable
	// first-pass rate (kimi) = 1/2 = 0.5
	// merge rate (kimi)      = 2/3
	// rework (kimi)          = 1
	now := fixedNow()
	records := []AttemptRecord{
		{WriterModel: "kimi", SourceBead: "A", MRID: "m1", Attempt: 1, CodexVerdict: CodexVerdictPass, FinalGateDecision: DecisionMerged, FailureClass: FailureClassNone, SubmittedAt: FormatTime(now.Add(-30 * time.Minute)), CodexReviewFinishedAt: FormatTime(now.Add(-25 * time.Minute)), RecordedAt: FormatTime(now)},
		{WriterModel: "kimi", SourceBead: "B", MRID: "m2", Attempt: 1, CodexVerdict: CodexVerdictFail, FinalGateDecision: DecisionRejected, FailureClass: FailureClassSubstantiveImpl, SubmittedAt: FormatTime(now.Add(-20 * time.Minute)), CodexReviewFinishedAt: FormatTime(now.Add(-15 * time.Minute)), RecordedAt: FormatTime(now)},
		{WriterModel: "kimi", SourceBead: "B", MRID: "m2", Attempt: 2, CodexVerdict: CodexVerdictPass, FinalGateDecision: DecisionMerged, FailureClass: FailureClassNone, SubmittedAt: FormatTime(now.Add(-10 * time.Minute)), CodexReviewFinishedAt: FormatTime(now.Add(-5 * time.Minute)), RecordedAt: FormatTime(now)},
		{WriterModel: "glm", SourceBead: "C", MRID: "m3", Attempt: 1, CodexVerdict: CodexVerdictUnavailable, FinalGateDecision: DecisionFailed, FailureClass: FailureClassReviewerUnavailable, RecordedAt: FormatTime(now)},
	}
	files := writeRecordsFile(t, records)
	summaries := Summarize(files, SummaryOptions{})

	if len(summaries) != 2 {
		t.Fatalf("expected 2 writer summaries, got %d", len(summaries))
	}
	byWriter := map[string]WriterSummary{}
	for _, s := range summaries {
		byWriter[s.WriterModel] = s
	}

	kimi := byWriter["kimi"]
	if kimi.TotalAttempts != 3 {
		t.Errorf("kimi total = %d, want 3", kimi.TotalAttempts)
	}
	if kimi.FirstPassCodexPassRate != 0.5 {
		t.Errorf("kimi first-pass rate = %v, want 0.5", kimi.FirstPassCodexPassRate)
	}
	if kimi.ReworkCount != 1 {
		t.Errorf("kimi rework = %d, want 1", kimi.ReworkCount)
	}
	if kimi.MergedCount != 2 {
		t.Errorf("kimi merged = %d, want 2", kimi.MergedCount)
	}
	if kimi.RejectedCount != 1 {
		t.Errorf("kimi rejected = %d, want 1", kimi.RejectedCount)
	}
	if kimi.CodexPassCount != 2 || kimi.CodexFailCount != 1 {
		t.Errorf("kimi codex pass/fail = %d/%d, want 2/1", kimi.CodexPassCount, kimi.CodexFailCount)
	}
	if kimi.SamplesSubmitToCodexVerdict != 3 {
		t.Errorf("kimi latency samples = %d, want 3", kimi.SamplesSubmitToCodexVerdict)
	}

	glm := byWriter["glm"]
	if glm.ExcludedReviewerUnavailable != 1 {
		t.Errorf("glm excluded reviewer-unavailable = %d, want 1", glm.ExcludedReviewerUnavailable)
	}
	if glm.TotalAttempts != 1 {
		t.Errorf("glm total = %d, want 1", glm.TotalAttempts)
	}
}

func TestSummarize_MedianAndP95(t *testing.T) {
	// 5 attempts with submit->verdict latencies of 60,120,180,240,300 seconds.
	// median (p50) = 180, p95 (nearest-rank) = 300.
	now := fixedNow()
	latencies := []int{60, 120, 180, 240, 300}
	var records []AttemptRecord
	for i, secs := range latencies {
		sub := now.Add(-time.Duration(secs*2) * time.Second)
		fin := now.Add(-time.Duration(secs) * time.Second)
		records = append(records, AttemptRecord{
			WriterModel: "w", SourceBead: "bead", MRID: "m", Attempt: i + 1,
			CodexVerdict: CodexVerdictPass, FinalGateDecision: DecisionMerged,
			SubmittedAt:            FormatTime(sub),
			CodexReviewFinishedAt:  FormatTime(fin),
			RecordedAt:             FormatTime(now),
		})
	}
	files := writeRecordsFile(t, records)
	summaries := Summarize(files, SummaryOptions{})

	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	s := summaries[0]
	if s.MedianSubmitToCodexVerdictSec != 180 {
		t.Errorf("median = %v, want 180", s.MedianSubmitToCodexVerdictSec)
	}
	if s.P95SubmitToCodexVerdictSec != 300 {
		t.Errorf("p95 = %v, want 300", s.P95SubmitToCodexVerdictSec)
	}
}

func TestSummarize_SinceFilter(t *testing.T) {
	// The Since filter applies to RecordedAt. Old records are excluded.
	now := fixedNow()
	records := []AttemptRecord{
		{WriterModel: "w", SourceBead: "A", Attempt: 1, RecordedAt: FormatTime(now.Add(-48 * time.Hour))},
		{WriterModel: "w", SourceBead: "B", Attempt: 1, RecordedAt: FormatTime(now.Add(-1 * time.Hour))},
	}
	files := writeRecordsFile(t, records)
	summaries := Summarize(files, SummaryOptions{Since: now.Add(-24 * time.Hour)})
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary after since-filter, got %d", len(summaries))
	}
	if summaries[0].TotalAttempts != 1 {
		t.Errorf("total = %d, want 1 (since-filtered)", summaries[0].TotalAttempts)
	}
}

func TestSummarize_WriterFilter(t *testing.T) {
	now := fixedNow()
	records := []AttemptRecord{
		{WriterModel: "kimi", Attempt: 1, RecordedAt: FormatTime(now)},
		{WriterModel: "glm", Attempt: 1, RecordedAt: FormatTime(now)},
		{WriterModel: "kimi", Attempt: 1, RecordedAt: FormatTime(now)},
	}
	files := writeRecordsFile(t, records)
	summaries := Summarize(files, SummaryOptions{Writer: "kimi"})
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary for writer filter, got %d", len(summaries))
	}
	if summaries[0].WriterModel != "kimi" || summaries[0].TotalAttempts != 2 {
		t.Errorf("writer filter: %+v", summaries[0])
	}
}

func TestSummarize_EmptyWriterBecomesUnknown(t *testing.T) {
	// Records with no writer_model are bucketed as "unknown" so they still
	// appear in the summary rather than being silently dropped.
	now := fixedNow()
	files := writeRecordsFile(t, []AttemptRecord{{Attempt: 1, RecordedAt: FormatTime(now)}})
	summaries := Summarize(files, SummaryOptions{})
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if summaries[0].WriterModel != "unknown" {
		t.Errorf("writer = %q, want unknown", summaries[0].WriterModel)
	}
}

func TestSummarize_SortedByTotalDesc(t *testing.T) {
	now := fixedNow()
	files := writeRecordsFile(t, []AttemptRecord{
		{WriterModel: "less", Attempt: 1, RecordedAt: FormatTime(now)},
		{WriterModel: "more", Attempt: 1, RecordedAt: FormatTime(now)},
		{WriterModel: "more", Attempt: 2, RecordedAt: FormatTime(now)},
	})
	summaries := Summarize(files, SummaryOptions{})
	if len(summaries) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(summaries))
	}
	if summaries[0].WriterModel != "more" || summaries[0].TotalAttempts != 2 {
		t.Errorf("expected 'more' first with 2 attempts, got %+v", summaries[0])
	}
}

func TestSummarize_NoFiles(t *testing.T) {
	// No files -> empty summary (not nil-crash).
	summaries := Summarize(nil, SummaryOptions{})
	if len(summaries) != 0 {
		t.Fatalf("expected 0 summaries for no files, got %d", len(summaries))
	}
}

func TestPercentile(t *testing.T) {
	vals := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	// Nearest-rank p50 of 10 values: rank = ceil(0.5*10) = 5 -> value 5.
	if got := percentile(vals, 0.50); got != 5 {
		t.Errorf("p50 = %v, want 5", got)
	}
	// p95: rank = ceil(0.95*10) = 10 -> value 10.
	if got := percentile(vals, 0.95); got != 10 {
		t.Errorf("p95 = %v, want 10", got)
	}
	// p0: rank = ceil(0) = 0 -> clamped to 1 -> value 1.
	if got := percentile(vals, 0); got != 1 {
		t.Errorf("p0 = %v, want 1", got)
	}
	// p100: rank = ceil(10) = 10 -> value 10.
	if got := percentile(vals, 1); got != 10 {
		t.Errorf("p100 = %v, want 10", got)
	}
	if got := percentile(nil, 0.5); got != 0 {
		t.Errorf("p50(nil) = %v, want 0", got)
	}
	// Odd-sized set: median is the middle value.
	odd := []float64{10, 20, 30, 40, 50}
	if got := percentile(odd, 0.50); got != 30 {
		t.Errorf("p50(odd) = %v, want 30", got)
	}
	if got := percentile(odd, 0.95); got != 50 {
		t.Errorf("p95(odd) = %v, want 50", got)
	}
}

func TestFormatTimeZeroIsEmpty(t *testing.T) {
	// Zero times render as "" so omitempty keeps the JSONL clean.
	if FormatTime(time.Time{}) != "" {
		t.Errorf("FormatTime(zero) = %q, want empty", FormatTime(time.Time{}))
	}
	if got := FormatTime(fixedNow()); got == "" {
		t.Error("FormatTime(now) = empty, want RFC3339")
	}
}

func TestParseTime(t *testing.T) {
	if !ParseTime("").IsZero() {
		t.Error("ParseTime('') should be zero")
	}
	if !ParseTime("not-a-time").IsZero() {
		t.Error("ParseTime(bad) should be zero")
	}
	if ParseTime(FormatTime(fixedNow())).IsZero() {
		t.Error("ParseTime(FormatTime(now)) should be non-zero")
	}
}

func TestReadAll_SkipsCorruptLines(t *testing.T) {
	// A partially-corrupt JSONL must not abort decoding OR hang: valid records
	// before and after a bad line are recovered (best-effort). This guards the
	// line-scanning implementation, which replaced a json.Decoder stream that
	// could loop forever on malformed input.
	now := fixedNow()
	good1 := AttemptRecord{MRID: "m1", Attempt: 1, RecordedAt: FormatTime(now)}
	good2 := AttemptRecord{MRID: "m2", Attempt: 1, RecordedAt: FormatTime(now)}
	bad := []byte("{not valid json")
	var buf bytes.Buffer
	b1, _ := good1.Marshal()
	buf.Write(b1)
	buf.WriteByte('\n')
	buf.Write(bad)
	buf.WriteByte('\n')
	b2, _ := good2.Marshal()
	buf.Write(b2)
	buf.WriteByte('\n')

	done := make(chan struct{})
	var recs []AttemptRecord
	var err error
	go func() {
		recs, err = ReadAll(&buf)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ReadAll hung on corrupt input")
	}
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records (corrupt line skipped), got %d", len(recs))
	}
	if recs[0].MRID != "m1" || recs[1].MRID != "m2" {
		t.Errorf("records = %q/%q, want m1/m2", recs[0].MRID, recs[1].MRID)
	}
}

func TestMatchesTelemetryFile(t *testing.T) {
	cases := map[string]bool{
		"refinery-attempts-20260702.jsonl": true,
		"refinery-attempts-.jsonl":         true, // prefix+suffix only
		"refinery-attempts-20260702.json":  false,
		"other-20260702.jsonl":             false,
		"refinery-attempts-20260702.txt":   false,
		"short.jsonl":                      false,
	}
	for name, want := range cases {
		if got := matchesTelemetryFile(name); got != want {
			t.Errorf("matchesTelemetryFile(%q) = %v, want %v", name, got, want)
		}
	}
}

// --- helpers ---

// readAllRecords reads the single daily JSONL file produced by r and decodes
// its records. Fails the test if the file is missing.
func readAllRecords(t *testing.T, r *Recorder) []AttemptRecord {
	t.Helper()
	files, err := r.Files(time.Time{})
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("no telemetry files written")
	}
	sort.Strings(files) // deterministic order across days
	var out []AttemptRecord
	for _, f := range files {
		recs, err := ReadFile(f)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", f, err)
		}
		out = append(out, recs...)
	}
	return out
}

// writeRecordsFile writes records to a temp JSONL file and returns its path,
// so the summarizer can read it via the file-based seam.
func writeRecordsFile(t *testing.T, records []AttemptRecord) []string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "refinery-attempts-20260702.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, r := range records {
		b, err := r.Marshal()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		f.Write(b)
		f.Write([]byte("\n"))
	}
	f.Close()
	return []string{path}
}

// guard against accidental drift in the round helper.
func TestRound(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{0.55555, 0.5556},
		{1.0, 1.0},
		{0.12344, 0.1234},
	}
	for _, c := range cases {
		if got := round(c.in); got != c.want {
			t.Errorf("round(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// Ensure the AttemptRecord JSON round-trips and the omitempty fields behave:
// a fully-populated record encodes with all fields present.
func TestAttemptRecord_JSONRoundTrip(t *testing.T) {
	original := AttemptRecord{
		Rig: "gastown", SourceBead: "gt-abc", MRID: "m1", Attempt: 1,
		Polecat: "furiosa", WriterModel: "umans-kimi", Branch: "b",
		SubmittedAt: FormatTime(fixedNow()),
		CodexVerdict: CodexVerdictPass, FinalGateDecision: DecisionMerged,
		FailureClass: FailureClassNone, RecordedAt: FormatTime(fixedNow()),
	}
	b, err := original.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := UnmarshalAttemptRecord(b)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(original, got) {
		t.Errorf("round-trip mismatch:\noriginal=%+v\ngot     =%+v", original, got)
	}
}
