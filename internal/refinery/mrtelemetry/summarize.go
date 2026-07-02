package mrtelemetry

import (
	"math"
	"sort"
	"time"
)

// SummaryOptions controls which records the summarizer includes and how it
// computes aggregates.
type SummaryOptions struct {
	// Since restricts to records finalized at or after this time. Zero means
	// no lower bound (all records).
	Since time.Time

	// Writer filters to a single writer model. Empty means all writers.
	Writer string

	// Rig filters to a single rig. Empty means all rigs.
	Rig string
}

// WriterSummary is the per-writer_model aggregate the Mayor uses to compare
// models (e.g. umans-kimi vs umans-glm vs m3).
type WriterSummary struct {
	WriterModel string `json:"writer_model"`

	// Volume
	TotalAttempts int `json:"total_attempts"`

	// Codex review outcomes
	FirstPassCodexPassRate float64 `json:"first_pass_codex_pass_rate"` // attempt==1 + codex PASS / first attempts
	CodexPassCount         int     `json:"codex_pass_count"`
	CodexFailCount         int     `json:"codex_fail_count"`
	CodexUnavailableCount  int     `json:"codex_unavailable_count"`
	CodexNoVerdictCount    int     `json:"codex_no_verdict_count"`

	// Merge outcomes
	FinalMergeRate float64 `json:"final_merge_rate"`
	MergedCount    int     `json:"merged_count"`
	RejectedCount  int     `json:"rejected_count"`
	ReworkCount    int     `json:"rework_count"` // terminal attempts with attempt > 1 for the same source bead

	// Excluded (non-substantive) counts — separated from substantive Codex FAIL.
	ExcludedInfraCount        int `json:"excluded_infra_count"`
	ExcludedReviewerUnavailable int `json:"excluded_reviewer_unavailable_count"`
	ExcludedTimeoutCount      int `json:"excluded_timeout_count"`

	// Latency: submit -> codex verdict (for attempts that reached a codex verdict).
	MedianSubmitToCodexVerdictSec float64 `json:"median_submit_to_codex_verdict_sec"`
	P95SubmitToCodexVerdictSec   float64 `json:"p95_submit_to_codex_verdict_sec"`
	SamplesSubmitToCodexVerdict  int     `json:"samples_submit_to_codex_verdict"`
}

// Summarize aggregates records into per-writer summaries. Records are read
// from the given files (typically Recorder.Files). It is best-effort: files
// that cannot be read are skipped.
func Summarize(files []string, opts SummaryOptions) []WriterSummary {
	records := readRecords(files)
	return summarizeRecords(records, opts)
}

// readRecords loads and filters records from the given JSONL files. Unreadable
// or partially-corrupt files are skipped (best-effort).
func readRecords(files []string) []AttemptRecord {
	var out []AttemptRecord
	for _, path := range files {
		f, err := osOpen(path)
		if err != nil {
			continue
		}
		recs, _ := ReadAll(f)
		_ = f.Close()
		out = append(out, recs...)
	}
	return out
}

// summarizeRecords computes per-writer aggregates from a flat record slice,
// applying the SummaryOptions filters. The result is sorted by TotalAttempts
// descending so the most active writers appear first.
func summarizeRecords(records []AttemptRecord, opts SummaryOptions) []WriterSummary {
	// Track per-source-bead attempt counts for rework detection.
	type beadStats struct {
		maxAttempt     int
		terminalCount int
	}
	bead := map[string]*beadStats{}

	// Per-writer accumulators.
	type acc struct {
		writer           string
		total            int
		codexPass        int
		codexFail        int
		codexUnavailable int
		codexNoVerdict   int
		codexNone        int
		firstAttempts    int
		firstPass        int
		merged           int
		rejected         int
		rework           int
		exInfra          int
		exReviewerUnavail int
		exTimeout        int
		latencies        []float64
	}
	byWriter := map[string]*acc{}

	getAcc := func(w string) *acc {
		if a, ok := byWriter[w]; ok {
			return a
		}
		a := &acc{writer: w}
		byWriter[w] = a
		return a
	}

	for _, r := range records {
		if !opts.Since.IsZero() {
			t := ParseTime(r.RecordedAt)
			if t.IsZero() || t.Before(opts.Since) {
				continue
			}
		}
		if opts.Writer != "" && r.WriterModel != opts.Writer {
			continue
		}
		if opts.Rig != "" && r.Rig != opts.Rig {
			continue
		}

		w := r.WriterModel
		if w == "" {
			w = "unknown"
		}
		a := getAcc(w)
		a.total++

		// First-pass codex pass: only count attempt #1 records (the first
		// terminal attempt on a source bead). Among those, codex PASS counts
		// as a first-pass pass.
		if r.Attempt <= 1 {
			a.firstAttempts++
			if r.CodexVerdict == CodexVerdictPass {
				a.firstPass++
			}
		}

		switch r.CodexVerdict {
		case CodexVerdictPass:
			a.codexPass++
		case CodexVerdictFail:
			a.codexFail++
		case CodexVerdictUnavailable:
			a.codexUnavailable++
		case CodexVerdictNoVerdict:
			a.codexNoVerdict++
		case CodexVerdictNone, "":
			a.codexNone++
		}

		switch r.FinalGateDecision {
		case DecisionMerged:
			a.merged++
		case DecisionRejected, DecisionFailed:
			a.rejected++
		}

		// Rework: a terminal attempt with attempt > 1 for the same source bead.
		if r.Attempt > 1 && r.SourceBead != "" {
			a.rework++
		}

		// Excluded (non-substantive) counts by failure class.
		switch r.FailureClass {
		case FailureClassInfra:
			a.exInfra++
		case FailureClassReviewerUnavailable:
			a.exReviewerUnavail++
		case FailureClassTimeout:
			a.exTimeout++
		}

		// Latency: submit -> codex verdict, for attempts that reached a verdict.
		if r.CodexVerdict == CodexVerdictPass || r.CodexVerdict == CodexVerdictFail {
			sub := ParseTime(r.SubmittedAt)
			verdict := ParseTime(r.CodexReviewFinishedAt)
			if !sub.IsZero() && !verdict.IsZero() && verdict.After(sub) {
				a.latencies = append(a.latencies, verdict.Sub(sub).Seconds())
			}
		}

		// Track bead-level stats for rework cross-check.
		if r.SourceBead != "" {
			bs := bead[r.SourceBead]
			if bs == nil {
				bs = &beadStats{}
				bead[r.SourceBead] = bs
			}
			if r.Attempt > bs.maxAttempt {
				bs.maxAttempt = r.Attempt
			}
			// Terminal = has a final decision.
			if r.FinalGateDecision != "" {
				bs.terminalCount++
			}
		}
	}

	out := make([]WriterSummary, 0, len(byWriter))
	for _, a := range byWriter {
		s := WriterSummary{
			WriterModel:                  a.writer,
			TotalAttempts:                a.total,
			CodexPassCount:               a.codexPass,
			CodexFailCount:               a.codexFail,
			CodexUnavailableCount:        a.codexUnavailable,
			CodexNoVerdictCount:          a.codexNoVerdict,
			MergedCount:                  a.merged,
			RejectedCount:                a.rejected,
			ReworkCount:                  a.rework,
			ExcludedInfraCount:           a.exInfra,
			ExcludedReviewerUnavailable:  a.exReviewerUnavail,
			ExcludedTimeoutCount:         a.exTimeout,
			SamplesSubmitToCodexVerdict:  len(a.latencies),
		}
		if a.firstAttempts > 0 {
			s.FirstPassCodexPassRate = round(float64(a.firstPass) / float64(a.firstAttempts))
		}
		if a.total > 0 {
			s.FinalMergeRate = round(float64(a.merged) / float64(a.total))
		}
		if len(a.latencies) > 0 {
			s.MedianSubmitToCodexVerdictSec = round(percentile(a.latencies, 0.50))
			s.P95SubmitToCodexVerdictSec = round(percentile(a.latencies, 0.95))
		}
		out = append(out, s)
	}

	// Sort by total attempts desc, then writer model asc for stable output.
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalAttempts != out[j].TotalAttempts {
			return out[i].TotalAttempts > out[j].TotalAttempts
		}
		return out[i].WriterModel < out[j].WriterModel
	})
	return out
}

// percentile returns the q-th percentile (0..1) of the values using the
// nearest-rank method: the value at the ceil(q*n)-th position (1-indexed) of the
// sorted data. q is clamped to [0,1]. The input is not modified.
//
// Example: for [1..10], p50 -> ceil(0.5*10)=5 -> 5; p95 -> ceil(0.95*10)=10 -> 10.
// For [60,120,180,240,300], p50 -> ceil(2.5)=3 -> 180; p95 -> ceil(4.75)=5 -> 300.
func percentile(values []float64, q float64) float64 {
	n := len(values)
	if n == 0 {
		return 0
	}
	if q < 0 {
		q = 0
	} else if q > 1 {
		q = 1
	}
	sorted := make([]float64, n)
	copy(sorted, values)
	sort.Float64s(sorted)
	// Nearest-rank: rank = ceil(q * n), 1-indexed, clamped to [1, n].
	rank := int(math.Ceil(q * float64(n)))
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

// round limits a float to 4 decimal places for stable JSON output.
func round(f float64) float64 {
	return float64(int(f*10000+0.5)) / 10000
}

// osOpen is a seam for tests (swappable file opener). Defaults to os.Open.
var osOpen = func(path string) (readCloser, error) {
	return openFile(path)
}
