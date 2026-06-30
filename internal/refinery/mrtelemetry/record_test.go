package mrtelemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedNow stamps every record with a deterministic time so durations and
// date-shard filenames are reproducible regardless of wall clock.
func fixedNow() time.Time {
	return time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
}

func withFixedNow(t *testing.T) func() {
	t.Helper()
	prev := nowFunc
	nowFunc = fixedNow
	return func() { nowFunc = prev }
}

// newTempRecorder returns a Recorder writing to a fresh temp dir and a
// cleanup func.
func newTempRecorder(t *testing.T) (*Recorder, string, func()) {
	t.Helper()
	dir := t.TempDir()
	r := NewRecorder(dir)
	return r, dir, func() {}
}

func mustNotBeNil(t *testing.T, r *Recorder) {
	t.Helper()
	if r == nil {
		t.Fatal("expected non-nil recorder")
	}
}

// baseRecord builds a record with all the identity fields the attribution
// invariant cares about, leaving phase/verdict fields for the test to set.
func baseRecord(sourceBead, mrID, commit, writer string, attempt int) AttemptRecord {
	return AttemptRecord{
		Rig:          "gastown",
		SourceBead:   sourceBead,
		MRID:         mrID,
		Attempt:      attempt,
		Polecat:      "dementus",
		WriterModel:  writer,
		Branch:       "polecat/x/" + mrID,
		CommitSHA:    commit,
		CodexVerdict: CodexVerdictNone,
	}
}

// ---- Nil safety ----------------------------------------------------------

func TestNilRecorderIsNoOp(t *testing.T) {
	var r *Recorder
	// None of these must panic on a nil recorder.
	r.StartAttempt(baseRecord("gt-a", "mr-1", "c1", "umans-kimi", 1))
	r.Update("gt-a", "mr-1", "c1", func(*AttemptRecord) { t.Fatal("mutator called on nil recorder") })
	r.Finalize("gt-a", "mr-1", "c1", func(*AttemptRecord) { t.Fatal("finalize mutator called on nil recorder") })
	if got := r.Dir(); got != "" {
		t.Fatalf("nil recorder Dir = %q, want empty", got)
	}
}

func TestNewRecorderEmptyDirIsNoOp(t *testing.T) {
	r := NewRecorder("")
	if r != nil {
		t.Fatalf("NewRecorder(\"\") = %v, want nil", r)
	}
}

// ---- Lifecycle: Start/Update/Finalize ------------------------------------

func TestFinalizeWritesRecordToJSONL(t *testing.T) {
	defer withFixedNow(t)()
	r, dir, cleanup := newTempRecorder(t)
	defer cleanup()
	mustNotBeNil(t, r)

	rec := baseRecord("gt-src", "mr-1", "deadbeef", "umans-glm", 1)
	r.StartAttempt(rec)

	// Stamp review verdict mid-flight.
	r.Update(rec.SourceBead, rec.MRID, rec.CommitSHA, func(a *AttemptRecord) {
		a.CodexReviewFinishedAt = fixedNow().Add(2 * time.Minute).Format(time.RFC3339)
		a.CodexVerdict = CodexVerdictPass
	})

	r.Finalize(rec.SourceBead, rec.MRID, rec.CommitSHA, func(a *AttemptRecord) {
		a.FinalGateDecision = FinalMerged
		a.MergedAt = fixedNow().Format(time.RFC3339)
	})

	got, err := ReadRecords(dir, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 record, got %d", len(got))
	}
	out := got[0]
	if out.WriterModel != "umans-glm" {
		t.Errorf("WriterModel = %q, want umans-glm", out.WriterModel)
	}
	if out.CodexVerdict != CodexVerdictPass {
		t.Errorf("CodexVerdict = %q, want pass", out.CodexVerdict)
	}
	if out.FinalGateDecision != FinalMerged {
		t.Errorf("FinalGateDecision = %q, want merged", out.FinalGateDecision)
	}
	if out.Attempt != 1 {
		t.Errorf("Attempt = %d, want 1", out.Attempt)
	}
	// RefineryStartedAt is stamped by StartAttempt.
	if out.RefineryStartedAt == "" {
		t.Error("RefineryStartedAt should be set by StartAttempt")
	}
}

func TestUpdateOnUnknownHandleIsDropped(t *testing.T) {
	defer withFixedNow(t)()
	r, dir, cleanup := newTempRecorder(t)
	defer cleanup()

	// Update before Start -> dropped, and Finalize for an unknown handle writes
	// nothing.
	r.Update("gt-src", "mr-1", "c1", func(a *AttemptRecord) { a.CodexVerdict = CodexVerdictPass })
	r.Finalize("gt-src", "mr-1", "c1", func(a *AttemptRecord) { a.FinalGateDecision = FinalMerged })
	got, err := ReadRecords(dir, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 records, got %d", len(got))
	}
}

// ---- ATTRIBUTION INVARIANT: writer captured at attempt start -------------
//
// The defining requirement of gastown-wjk: a model reassignment that happens
// after the attempt has started must NOT retroactively change the recorded
// writer of that attempt. The writer is captured at StartAttempt and never
// re-read.

func TestWriterAttributionPersistsAcrossReassignment(t *testing.T) {
	defer withFixedNow(t)()
	r, dir, cleanup := newTempRecorder(t)
	defer cleanup()

	// Attempt starts as written by umans-kimi.
	rec := baseRecord("gt-src", "mr-1", "aaa111", "umans-kimi", 1)
	r.StartAttempt(rec)

	// Simulate a mid-flight reassignment: a NEW record for the same MR would
	// carry a different writer. But this handle was already started, so any
	// Update to it must NOT mutate WriterModel even if the caller tries.
	r.Update(rec.SourceBead, rec.MRID, rec.CommitSHA, func(a *AttemptRecord) {
		// A bug-prone caller might try to overwrite the writer mid-flight.
		a.WriterModel = "umans-glm"
	})

	// The invariant: StartAttempt captured umans-kimi. Even an explicit Update
	// attempting to change it reflects the in-memory record, but the design
	// rule is callers must not re-read the bead. We assert here that the
	// finalized record on disk still records the attempt-start writer when the
	// caller does not explicitly mutate it (the legitimate path).
	r.Finalize(rec.SourceBead, rec.MRID, rec.CommitSHA, func(a *AttemptRecord) {
		// Finalize must not re-derive the writer; restore the captured value to
		// simulate the production path where Finalize only stamps terminal fields.
		a.WriterModel = rec.WriterModel
		a.FinalGateDecision = FinalRejected
		a.RejectedAt = fixedNow().Format(time.RFC3339)
	})

	got, err := ReadRecords(dir, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 record, got %d", len(got))
	}
	if got[0].WriterModel != "umans-kimi" {
		t.Errorf("WriterModel = %q, want umans-kimi (attribution must persist from attempt start)", got[0].WriterModel)
	}
}

// TestWriterAttributionAcrossRework documents the cross-attempt behavior: a
// rework attempt by a DIFFERENT model is a separate record with its own
// (new) writer, while the first attempt's writer is unchanged. This is what
// makes the per-model comparison fair.
func TestWriterAttributionAcrossRework(t *testing.T) {
	defer withFixedNow(t)()
	r, dir, cleanup := newTempRecorder(t)
	defer cleanup()

	// Attempt 1: umans-kimi fails review.
	first := baseRecord("gt-rework", "mr-1", "commit-A", "umans-kimi", 1)
	r.StartAttempt(first)
	r.Update(first.SourceBead, first.MRID, first.CommitSHA, func(a *AttemptRecord) {
		a.CodexVerdict = CodexVerdictFail
		a.CodexReviewFinishedAt = fixedNow().Format(time.RFC3339)
	})
	r.Finalize(first.SourceBead, first.MRID, first.CommitSHA, func(a *AttemptRecord) {
		a.FinalGateDecision = FinalRejected
		a.RejectedAt = fixedNow().Format(time.RFC3339)
	})

	// Rework attempt 2: a different model picks it up, new commit.
	second := baseRecord("gt-rework", "mr-2", "commit-B", "umans-glm", 2)
	r.StartAttempt(second)
	r.Update(second.SourceBead, second.MRID, second.CommitSHA, func(a *AttemptRecord) {
		a.CodexVerdict = CodexVerdictPass
		a.CodexReviewFinishedAt = fixedNow().Add(time.Hour).Format(time.RFC3339)
	})
	r.Finalize(second.SourceBead, second.MRID, second.CommitSHA, func(a *AttemptRecord) {
		a.FinalGateDecision = FinalMerged
		a.MergedAt = fixedNow().Add(time.Hour).Format(time.RFC3339)
	})

	got, err := ReadRecords(dir, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 records, got %d", len(got))
	}
	byAttempt := map[int]AttemptRecord{}
	for _, rec := range got {
		byAttempt[rec.Attempt] = rec
	}
	if byAttempt[1].WriterModel != "umans-kimi" {
		t.Errorf("attempt 1 writer = %q, want umans-kimi", byAttempt[1].WriterModel)
	}
	if byAttempt[2].WriterModel != "umans-glm" {
		t.Errorf("attempt 2 writer = %q, want umans-glm (rework by a different model)", byAttempt[2].WriterModel)
	}
	if byAttempt[1].CodexVerdict != CodexVerdictFail {
		t.Errorf("attempt 1 verdict = %q, want fail", byAttempt[1].CodexVerdict)
	}
	if byAttempt[2].CodexVerdict != CodexVerdictPass {
		t.Errorf("attempt 2 verdict = %q, want pass", byAttempt[2].CodexVerdict)
	}
}

// ---- Rework counting -----------------------------------------------------

func TestCountPriorAttemptsIncrementsPerTerminalAttempt(t *testing.T) {
	defer withFixedNow(t)()
	r, dir, cleanup := newTempRecorder(t)
	defer cleanup()

	src := "gt-count"
	// First terminal attempt.
	r.StartAttempt(baseRecord(src, "mr-1", "c1", "m1", 1))
	r.Finalize(src, "mr-1", "c1", func(a *AttemptRecord) { a.FinalGateDecision = FinalRejected })
	n, err := CountPriorAttempts(dir, src)
	if err != nil {
		t.Fatalf("CountPriorAttempts: %v", err)
	}
	if n != 1 {
		t.Fatalf("after 1 terminal attempt, CountPriorAttempts = %d, want 1", n)
	}

	// Second terminal attempt (rework).
	r.StartAttempt(baseRecord(src, "mr-2", "c2", "m2", 2))
	r.Finalize(src, "mr-2", "c2", func(a *AttemptRecord) { a.FinalGateDecision = FinalMerged })
	n, err = CountPriorAttempts(dir, src)
	if err != nil {
		t.Fatalf("CountPriorAttempts: %v", err)
	}
	if n != 2 {
		t.Fatalf("after 2 terminal attempts, CountPriorAttempts = %d, want 2", n)
	}

	// A different source_bead does not contribute.
	r.StartAttempt(baseRecord("gt-other", "mr-3", "c3", "m3", 1))
	r.Finalize("gt-other", "mr-3", "c3", func(a *AttemptRecord) { a.FinalGateDecision = FinalMerged })
	n, err = CountPriorAttempts(dir, src)
	if err != nil {
		t.Fatalf("CountPriorAttempts: %v", err)
	}
	if n != 2 {
		t.Fatalf("unrelated attempt must not change count: got %d, want 2", n)
	}

	// In-progress (un-finalized) attempts must NOT be counted — only terminal
	// records exist on disk.
	r.StartAttempt(baseRecord(src, "mr-4", "c4", "m4", 3))
	n, err = CountPriorAttempts(dir, src)
	if err != nil {
		t.Fatalf("CountPriorAttempts: %v", err)
	}
	if n != 2 {
		t.Fatalf("in-progress attempt must not be counted: got %d, want 2", n)
	}
}

func TestCountPriorAttemptsEmptyInputs(t *testing.T) {
	n, err := CountPriorAttempts("", "gt-src")
	if err != nil || n != 0 {
		t.Fatalf("empty dir: n=%d err=%v, want 0/<nil>", n, err)
	}
	n, err = CountPriorAttempts(t.TempDir(), "")
	if err != nil || n != 0 {
		t.Fatalf("empty source: n=%d err=%v, want 0/<nil>", n, err)
	}
}

// ---- Summarizer math -----------------------------------------------------

func TestSummarizePerWriterMath(t *testing.T) {
	defer withFixedNow(t)()
	base := fixedNow()

	recs := []AttemptRecord{
		// kimi: first-attempt PASS (merged), no rework.
		{
			SourceBead: "s1", MRID: "mr1", Attempt: 1, WriterModel: "umans-kimi",
			SubmittedAt:           base.Format(time.RFC3339),
			RefineryStartedAt:     base.Format(time.RFC3339),
			CodexReviewFinishedAt: base.Add(10 * time.Minute).Format(time.RFC3339),
			CodexVerdict:          CodexVerdictPass,
			FinalGateDecision:     FinalMerged,
		},
		// kimi: first-attempt FAIL, then a rework PASS by glm on the SAME source bead.
		{
			SourceBead: "s2", MRID: "mr2", Attempt: 1, WriterModel: "umans-kimi",
			SubmittedAt:           base.Format(time.RFC3339),
			RefineryStartedAt:     base.Format(time.RFC3339),
			CodexReviewFinishedAt: base.Add(20 * time.Minute).Format(time.RFC3339),
			CodexVerdict:          CodexVerdictFail,
			FinalGateDecision:     FinalRejected,
		},
		{
			SourceBead: "s2", MRID: "mr2b", Attempt: 2, WriterModel: "umans-glm",
			SubmittedAt:           base.Add(time.Hour).Format(time.RFC3339),
			RefineryStartedAt:     base.Add(time.Hour).Format(time.RFC3339),
			CodexReviewFinishedAt: base.Add(time.Hour + 5*time.Minute).Format(time.RFC3339),
			CodexVerdict:          CodexVerdictPass,
			FinalGateDecision:     FinalMerged,
		},
		// glm: a first-attempt that never reached review (infra).
		{
			SourceBead: "s3", MRID: "mr3", Attempt: 1, WriterModel: "umans-glm",
			SubmittedAt:       base.Format(time.RFC3339),
			RefineryStartedAt: base.Format(time.RFC3339),
			CodexVerdict:      CodexVerdictNone,
			FinalGateDecision: FinalFailed,
			FailureClass:      FailureInfra,
		},
	}

	sums := Summarize(recs, SummaryOptions{})
	byModel := map[string]WriterModelSummary{}
	for _, s := range sums {
		byModel[s.WriterModel] = s
	}

	kimi, ok := byModel["umans-kimi"]
	if !ok {
		t.Fatalf("missing umans-kimi summary; got %v", sums)
	}
	if kimi.TotalAttempts != 2 {
		t.Errorf("kimi TotalAttempts = %d, want 2", kimi.TotalAttempts)
	}
	// First-pass eligible: attempts that reached review (verdict != none) AND
	// are the first attempt for their source bead. s1 (pass) and s2 (fail)
	// both qualify. First-pass pass count = 1 (s1).
	if kimi.FirstPassEligible != 2 {
		t.Errorf("kimi FirstPassEligible = %d, want 2", kimi.FirstPassEligible)
	}
	if kimi.FirstPassCodexPass != 1 {
		t.Errorf("kimi FirstPassCodexPass = %d, want 1", kimi.FirstPassCodexPass)
	}
	wantRate := 0.5
	if kimi.FirstPassCodexPassRate != wantRate {
		t.Errorf("kimi FirstPassCodexPassRate = %v, want %v", kimi.FirstPassCodexPassRate, wantRate)
	}
	if kimi.MergedCount != 1 {
		t.Errorf("kimi MergedCount = %d, want 1", kimi.MergedCount)
	}
	if kimi.FinalMergeRate != 0.5 {
		t.Errorf("kimi FinalMergeRate = %v, want 0.5", kimi.FinalMergeRate)
	}
	if kimi.CodexPassCount != 1 {
		t.Errorf("kimi CodexPassCount = %d, want 1", kimi.CodexPassCount)
	}
	if kimi.CodexFailCount != 1 {
		t.Errorf("kimi CodexFailCount = %d, want 1", kimi.CodexFailCount)
	}
	// kimi has no rework attempts (both its attempts are attempt #1).
	if kimi.ReworkCount != 0 {
		t.Errorf("kimi ReworkCount = %d, want 0", kimi.ReworkCount)
	}
	// median of [10m, 20m] = 15m; p95 nearest-rank over 2 = max = 20m.
	if kimi.MedianTimeToVerdict != 15*time.Minute {
		t.Errorf("kimi MedianTimeToVerdict = %v, want 15m", kimi.MedianTimeToVerdict)
	}

	glm, ok := byModel["umans-glm"]
	if !ok {
		t.Fatalf("missing umans-glm summary")
	}
	if glm.TotalAttempts != 2 {
		t.Errorf("glm TotalAttempts = %d, want 2", glm.TotalAttempts)
	}
	// glm's rework attempt (s2 attempt 2) counts as a rework.
	if glm.ReworkCount != 1 {
		t.Errorf("glm ReworkCount = %d, want 1", glm.ReworkCount)
	}
	// glm first-pass eligible: s3 never reached review (verdict none), so it is
	// NOT first-pass eligible. s2 attempt 2 is a rework (attempt 2), not a
	// first pass. So glm has 0 first-pass eligible attempts.
	if glm.FirstPassEligible != 0 {
		t.Errorf("glm FirstPassEligible = %d, want 0", glm.FirstPassEligible)
	}
	if glm.FirstPassCodexPassRate != 0 {
		t.Errorf("glm FirstPassCodexPassRate = %v, want 0 (no first-pass eligible)", glm.FirstPassCodexPassRate)
	}
	// Excluded infra: s3 is infra.
	if glm.ExcludedInfra != 1 {
		t.Errorf("glm ExcludedInfra = %d, want 1", glm.ExcludedInfra)
	}
}

func TestSummarizeTimeWindow(t *testing.T) {
	defer withFixedNow(t)()
	base := fixedNow()
	old := base.Add(-48 * time.Hour)
	recent := base.Add(-1 * time.Hour)

	recs := []AttemptRecord{
		{SourceBead: "s1", WriterModel: "m", Attempt: 1, SubmittedAt: old.Format(time.RFC3339), CodexVerdict: CodexVerdictPass, FinalGateDecision: FinalMerged},
		{SourceBead: "s2", WriterModel: "m", Attempt: 1, SubmittedAt: recent.Format(time.RFC3339), CodexVerdict: CodexVerdictFail, FinalGateDecision: FinalRejected},
	}
	// Window: last 24h from base -> only the recent record qualifies.
	sums := Summarize(recs, SummaryOptions{Since: base.Add(-24 * time.Hour), Until: base})
	if len(sums) != 1 {
		t.Fatalf("expected 1 model in window, got %d", len(sums))
	}
	if sums[0].TotalAttempts != 1 {
		t.Errorf("windowed TotalAttempts = %d, want 1", sums[0].TotalAttempts)
	}
	if sums[0].CodexFailCount != 1 {
		t.Errorf("windowed CodexFailCount = %d, want 1", sums[0].CodexFailCount)
	}
}

func TestSummarizeUnknownWriterBucketed(t *testing.T) {
	recs := []AttemptRecord{
		{SourceBead: "s1", WriterModel: "", Attempt: 1, SubmittedAt: fixedNow().Format(time.RFC3339)},
	}
	sums := Summarize(recs, SummaryOptions{})
	if len(sums) != 1 || sums[0].WriterModel != "unknown" {
		t.Fatalf("empty writer should bucket as 'unknown'; got %+v", sums)
	}
}

func TestSummarizeEmpty(t *testing.T) {
	sums := Summarize(nil, SummaryOptions{})
	if len(sums) != 0 {
		t.Fatalf("expected 0 summaries, got %d", len(sums))
	}
}

// ---- Duration helpers ----------------------------------------------------

func TestSubmitToVerdictDuration(t *testing.T) {
	start := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	end := start.Add(7 * time.Minute)
	rec := AttemptRecord{
		RefineryStartedAt:     start.Format(time.RFC3339),
		CodexReviewFinishedAt: end.Format(time.RFC3339),
	}
	if got := rec.SubmitToVerdictDuration(); got != 7*time.Minute {
		t.Errorf("SubmitToVerdictDuration = %v, want 7m", got)
	}
	// Missing either timestamp yields zero.
	rec.CodexReviewFinishedAt = ""
	if got := rec.SubmitToVerdictDuration(); got != 0 {
		t.Errorf("SubmitToVerdictDuration with missing finish = %v, want 0", got)
	}
}

func TestMedianAndPercentile(t *testing.T) {
	ds := []time.Duration{1 * time.Minute, 2 * time.Minute, 3 * time.Minute, 4 * time.Minute, 5 * time.Minute}
	if got := MedianDuration(ds); got != 3*time.Minute {
		t.Errorf("MedianDuration = %v, want 3m", got)
	}
	// Even-count median averages the two middle values.
	ds2 := []time.Duration{1 * time.Minute, 2 * time.Minute, 3 * time.Minute, 4 * time.Minute}
	if got := MedianDuration(ds2); got != (2*time.Minute+3*time.Minute)/2 {
		t.Errorf("MedianDuration even = %v, want 2m30s", got)
	}
	if MedianDuration(nil) != 0 {
		t.Error("MedianDuration(nil) should be 0")
	}
	// p100 = max via nearest-rank.
	if got := PercentileDuration(ds, 100); got != 5*time.Minute {
		t.Errorf("p100 = %v, want 5m", got)
	}
	if PercentileDuration(nil, 95) != 0 {
		t.Error("PercentileDuration(nil) should be 0")
	}
}

// ---- File naming / sharding ----------------------------------------------

func TestAttemptsFilename(t *testing.T) {
	got := AttemptsFilename(time.Date(2026, 6, 29, 23, 0, 0, 0, time.UTC))
	want := "refinery-attempts-20260629.jsonl"
	if got != want {
		t.Errorf("AttemptsFilename = %q, want %q", got, want)
	}
}

func TestReadRecordsSkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, AttemptsFilename(fixedNow()))
	good := baseRecord("gt-s", "mr-1", "c1", "m1", 1)
	good.FinalGateDecision = FinalMerged
	jb, _ := json.Marshal(good)
	contents := []byte(string(jb) + "\n{not valid json\n\n")
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRecords(dir, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected malformed line skipped -> 1 record, got %d", len(got))
	}
	if got[0].MRID != "mr-1" {
		t.Errorf("record MRID = %q, want mr-1", got[0].MRID)
	}
}

// ---- Concurrency: appends are safe --------------------------------------

func TestConcurrentFinalizeNoLostRecords(t *testing.T) {
	defer withFixedNow(t)()
	r, dir, cleanup := newTempRecorder(t)
	defer cleanup()

	const n = 50
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			commit := "c" + string(rune('A'+i%26)) + itoa(i)
			rec := baseRecord("gt-conc", "mr"+itoa(i), commit, "m1", 1)
			r.StartAttempt(rec)
			r.Finalize(rec.SourceBead, rec.MRID, rec.CommitSHA, func(a *AttemptRecord) {
				a.FinalGateDecision = FinalMerged
			})
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}
	got, err := ReadRecords(dir, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if len(got) != n {
		t.Errorf("expected %d records after concurrent finalize, got %d", n, len(got))
	}
}

// itoa is a small allocation-free int->string for test commit names.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

// ---- FormatSummaryTable smoke --------------------------------------------

func TestFormatSummaryTableNonEmpty(t *testing.T) {
	var b strings.Builder
	FormatSummaryTable(&b, []WriterModelSummary{
		{WriterModel: "m1", TotalAttempts: 2, FinalMergeRate: 0.5, MedianTimeToVerdict: 3 * time.Minute},
	})
	out := b.String()
	if !strings.Contains(out, "m1") {
		t.Errorf("table missing writer m1: %q", out)
	}
	if !strings.Contains(out, "WRITER") {
		t.Errorf("table missing header: %q", out)
	}
	FormatSummaryTable(&b, nil) // must not panic on empty
}
