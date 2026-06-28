package mrtelemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// ReportOptions controls which records are included in a report.
type ReportOptions struct {
	// Since filters to records submitted at or after this time. Zero means all time.
	Since time.Time

	// WriterModels filters to the given model names. Empty means all models.
	WriterModels []string
}

// ModelSummary aggregates telemetry for one writer model.
type ModelSummary struct {
	TotalAttempts                int     `json:"total_attempts"`
	FirstPassCodexPassCount      int     `json:"first_pass_codex_pass_count"`
	FirstPassCodexPassRate       float64 `json:"first_pass_codex_pass_rate"`
	FinalMergeCount              int     `json:"final_merge_count"`
	FinalMergeRate               float64 `json:"final_merge_rate"`
	RejectionCount               int     `json:"rejection_count"`
	ReworkCount                  int     `json:"rework_count"`
	ExcludedInfraUnavailableCount int     `json:"excluded_infra_unavailable_count"`
	MedianSubmitToCodexVerdictMS int64   `json:"median_submit_to_codex_verdict_ms"`
	P95SubmitToCodexVerdictMS    int64   `json:"p95_submit_to_codex_verdict_ms"`
}

// Report aggregates MR telemetry by writer model.
type Report struct {
	ByModel map[string]*ModelSummary `json:"by_model"`
	Totals  *ModelSummary            `json:"totals"`
}

// Report generates an aggregated report from the store.
func (s *Store) Report(ctx context.Context, opts ReportOptions) (*Report, error) {
	records, err := s.readAll()
	if err != nil {
		return nil, fmt.Errorf("reading telemetry: %w", err)
	}

	allowedModels := make(map[string]bool, len(opts.WriterModels))
	for _, m := range opts.WriterModels {
		allowedModels[m] = true
	}

	byModel := make(map[string]*ModelSummary)
	totals := &ModelSummary{}

	for _, rec := range records {
		if !opts.Since.IsZero() && rec.SubmittedAt.Before(opts.Since) {
			continue
		}
		if len(allowedModels) > 0 && !allowedModels[rec.WriterModel] {
			continue
		}

		summary, ok := byModel[rec.WriterModel]
		if !ok {
			summary = &ModelSummary{}
			byModel[rec.WriterModel] = summary
		}

		summary.TotalAttempts++
		totals.TotalAttempts++

		if rec.AttemptNumber == 1 && rec.CodexVerdict == "PASS" {
			summary.FirstPassCodexPassCount++
			totals.FirstPassCodexPassCount++
		}

		if rec.FinalGateDecision == "merged" {
			summary.FinalMergeCount++
			totals.FinalMergeCount++
		}

		if rec.FinalGateDecision == "rejected" {
			summary.RejectionCount++
			totals.RejectionCount++
		}

		if rec.AttemptNumber > 1 {
			summary.ReworkCount++
			totals.ReworkCount++
		}

		if isExcludedInfra(rec.FailureClass) {
			summary.ExcludedInfraUnavailableCount++
			totals.ExcludedInfraUnavailableCount++
		}
	}

	// Compute durations per model and totals.
	durationsByModel := make(map[string][]int64)
	var allDurations []int64
	for _, rec := range records {
		if !opts.Since.IsZero() && rec.SubmittedAt.Before(opts.Since) {
			continue
		}
		if len(allowedModels) > 0 && !allowedModels[rec.WriterModel] {
			continue
		}
		if rec.SubmitToCodexVerdictMS <= 0 {
			continue
		}
		durationsByModel[rec.WriterModel] = append(durationsByModel[rec.WriterModel], rec.SubmitToCodexVerdictMS)
		allDurations = append(allDurations, rec.SubmitToCodexVerdictMS)
	}

	for model, summary := range byModel {
		summary.MedianSubmitToCodexVerdictMS = medianInt64(durationsByModel[model])
		summary.P95SubmitToCodexVerdictMS = p95Int64(durationsByModel[model])
		summary.FirstPassCodexPassRate = rate(summary.FirstPassCodexPassCount, summary.TotalAttempts)
		summary.FinalMergeRate = rate(summary.FinalMergeCount, summary.TotalAttempts)
	}

	totals.MedianSubmitToCodexVerdictMS = medianInt64(allDurations)
	totals.P95SubmitToCodexVerdictMS = p95Int64(allDurations)
	totals.FirstPassCodexPassRate = rate(totals.FirstPassCodexPassCount, totals.TotalAttempts)
	totals.FinalMergeRate = rate(totals.FinalMergeCount, totals.TotalAttempts)

	return &Report{
		ByModel: byModel,
		Totals:  totals,
	}, nil
}

// isExcludedInfra returns true for failure classes that should be excluded
// from substantive quality comparisons.
func isExcludedInfra(failureClass string) bool {
	switch failureClass {
	case "reviewer_unavailable", "timeout", "infra_noise":
		return true
	}
	return false
}

// rate returns a/b as a float64, or 0 when b is zero.
func rate(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total)
}

// medianInt64 returns the median of a sorted copy of values, or 0 if empty.
// Uses nearest-rank: average of two middle values for even-length slices.
func medianInt64(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]int64, len(values))
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// p95Int64 returns the 95th percentile of a sorted copy of values, or 0 if empty.
// Uses nearest-rank.
func p95Int64(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]int64, len(values))
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	n := len(sorted)
	// Nearest-rank: rank = ceil(p/100 * n).
	rank := (95*n + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

// FormatReport writes a text table of the report to w.
func FormatReport(r *Report, w io.Writer) {
	// Collect model names in deterministic order.
	models := make([]string, 0, len(r.ByModel))
	for m := range r.ByModel {
		models = append(models, m)
	}
	sort.Strings(models)

	// Header.
	header := "writer | attempts | first-pass | merge-rate | rejections | reworks | median_ms | p95_ms | excluded_infra\n"
	fmt.Fprint(w, header)

	writeSummary := func(name string, s *ModelSummary) {
		fmt.Fprintf(w, "%s | %s | %s | %s | %s | %s | %s | %s | %s\n",
			pad(name, 18),
			fmtInt(s.TotalAttempts),
			fmtPct(s.FirstPassCodexPassRate),
			fmtPct(s.FinalMergeRate),
			fmtInt(s.RejectionCount),
			fmtInt(s.ReworkCount),
			fmtInt64(s.MedianSubmitToCodexVerdictMS),
			fmtInt64(s.P95SubmitToCodexVerdictMS),
			fmtInt(s.ExcludedInfraUnavailableCount),
		)
	}

	for _, m := range models {
		writeSummary(m, r.ByModel[m])
	}
	if r.Totals != nil {
		writeSummary("TOTAL", r.Totals)
	}
}

// MarshalJSON serializes a report to indented JSON.
func (r *Report) MarshalJSON() ([]byte, error) {
	// Alias avoids infinite recursion with the same signature.
	type alias Report
	return json.MarshalIndent((*alias)(r), "", "  ")
}

// fmtInt formats a non-negative int, returning "-" for zero/unset values.
func fmtInt(v int) string {
	if v == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", v)
}

// fmtInt64 formats a non-negative int64, returning "-" for zero/unset values.
func fmtInt64(v int64) string {
	if v == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", v)
}

// fmtPct formats a rate as a percentage, returning "-" for zero rates.
func fmtPct(v float64) string {
	if v == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", v*100)
}

// pad right-pads s to width, trimming if necessary.
func pad(s string, width int) string {
	if len(s) >= width {
		return s[:width]
	}
	return s + strings.Repeat(" ", width-len(s))
}
