package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/refinery/mrtelemetry"
	"github.com/steveyegge/gastown/internal/workspace"
)

// mrReport command flags.
var (
	mrReportSinceDays int      // Filter to records submitted in the last N days (0 = all).
	mrReportSinceRFC  string   // RFC3339 timestamp for since cutoff (overrides --since-days).
	mrReportModels    []string // Filter to the named writer models.
	mrReportJSON      bool     // Emit JSON instead of a text table.
	mrReportRig       string   // Override rig detection (defaults to cwd).
)

func init() {
	mrReportCmd.Flags().IntVar(&mrReportSinceDays, "since-days", 0,
		"only include records submitted within the last N days (0 = all time)")
	mrReportCmd.Flags().StringVar(&mrReportSinceRFC, "since", "",
		"only include records submitted at or after this RFC3339 timestamp (overrides --since-days)")
	mrReportCmd.Flags().StringSliceVar(&mrReportModels, "model", nil,
		"filter to the named writer model (repeatable; e.g. --model=umans-kimi)")
	mrReportCmd.Flags().BoolVar(&mrReportJSON, "json", false,
		"emit the report as JSON instead of a text table")
	mrReportCmd.Flags().StringVar(&mrReportRig, "rig", "",
		"override rig detection (defaults to current directory)")

	refineryCmd.AddCommand(mrReportCmd)
}

// mrReportCmd summarizes per-writer-model MR telemetry for a rig.
// It reads `.runtime/mr-telemetry.jsonl` (written by the refinery when
// MRs are submitted, validated, peer-reviewed, and either merged or
// rejected) and prints a table comparing writer models by first-pass
// Codex pass rate, final merge rate, rework count, time-to-Codex-verdict,
// and excluded-infra counts.
//
// Use --since-days / --since / --model to scope the report. The default
// scope is "all time, all models", which is useful when the fleet has
// just started collecting telemetry; once there is enough history, scope
// to a recent window so the comparison reflects current model performance.
var mrReportCmd = &cobra.Command{
	Use:   "report [rig]",
	Short: "Summarize MR telemetry by writer model",
	Long: `Summarize per-writer-model MR telemetry for a rig.

Reads .runtime/mr-telemetry.jsonl and reports per-model:
  - total attempts
  - first-pass Codex pass rate (attempt 1, CodexVerdict=PASS)
  - final merge rate (FinalGateDecision=merged)
  - rejection count (FinalGateDecision=rejected)
  - rework count (attempt_number > 1)
  - median and p95 submit-to-Codex-verdict elapsed
  - excluded infra/unavailable count (reviewer_unavailable, timeout, infra_noise)

Useful for comparing umans-kimi vs umans-glm vs m3 on the current fleet.

If rig is not specified, infers it from the current directory.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runMRReport,
}

func runMRReport(cmd *cobra.Command, args []string) error {
	rigName := mrReportRig
	if rigName == "" && len(args) > 0 {
		rigName = args[0]
	}
	if rigName == "" {
		inferred, err := findCurrentRigFromCwd()
		if err != nil {
			return fmt.Errorf("not in a Gas Town workspace (use --rig or pass rig as argument): %w", err)
		}
		rigName = inferred
	}

	store, err := openMRTelemetryStore(rigName)
	if err != nil {
		return err
	}

	opts, err := buildMRReportOptions()
	if err != nil {
		return err
	}

	report, err := store.Report(cmd.Context(), opts)
	if err != nil {
		return fmt.Errorf("build report: %w", err)
	}

	if mrReportJSON {
		return writeMRReportJSON(cmd.OutOrStdout(), report)
	}
	mrtelemetry.FormatReport(report, cmd.OutOrStdout())
	return nil
}

// openMRTelemetryStore resolves the rig path from the town root and opens
// the mrtelemetry.Store backed by `.runtime/mr-telemetry.jsonl`.
func openMRTelemetryStore(rigName string) (*mrtelemetry.Store, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return nil, fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	rigPath := filepath.Join(townRoot, rigName)
	if _, err := os.Stat(rigPath); err != nil {
		return nil, fmt.Errorf("rig %q not found at %s: %w", rigName, rigPath, err)
	}
	store := mrtelemetry.NewStore(mrtelemetry.DefaultStorePath(rigPath))
	return store, nil
}

// buildMRReportOptions applies --since / --since-days / --model filters.
func buildMRReportOptions() (mrtelemetry.ReportOptions, error) {
	opts := mrtelemetry.ReportOptions{
		WriterModels: append([]string(nil), mrReportModels...),
	}

	switch {
	case mrReportSinceRFC != "":
		t, err := time.Parse(time.RFC3339, mrReportSinceRFC)
		if err != nil {
			return opts, fmt.Errorf("invalid --since timestamp %q (want RFC3339): %w", mrReportSinceRFC, err)
		}
		opts.Since = t
	case mrReportSinceDays > 0:
		opts.Since = time.Now().AddDate(0, 0, -mrReportSinceDays)
	}
	return opts, nil
}

// writeMRReportJSON emits the report as indented JSON. We do this in cmd
// (rather than reusing the package's MarshalJSON) so the command owns the
// CLI-level formatting decisions (header, top-level shape) and the package
// stays focused on data.
func writeMRReportJSON(w io.Writer, r *mrtelemetry.Report) error {
	// Add a small CLI-level summary header without changing the Report struct.
	out := struct {
		ByModel     map[string]*mrtelemetry.ModelSummary `json:"by_model"`
		Totals      *mrtelemetry.ModelSummary            `json:"totals"`
		GeneratedAt time.Time                            `json:"generated_at"`
	}{
		ByModel:     r.ByModel,
		Totals:      r.Totals,
		GeneratedAt: time.Now().UTC(),
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, strings.TrimRight(string(data), "\n"))
	return err
}

// findCurrentRigFromCwd resolves the rig name from the current working
// directory using workspace.FindFromCwd. Returns empty string if not in a
// workspace (caller should prompt for explicit --rig).
func findCurrentRigFromCwd() (string, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return "", err
	}
	rigName, _, err := findCurrentRig(townRoot)
	if err != nil {
		return "", err
	}
	return rigName, nil
}
