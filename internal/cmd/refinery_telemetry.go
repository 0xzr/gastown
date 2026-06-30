package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/refinery/mrtelemetry"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Telemetry command flags
var (
	refineryTelemetryJSON    bool
	refineryTelemetrySummary bool
	refineryTelemetryWriter  string
	refineryTelemetrySince    string
	refineryTelemetryUntil    string
	refineryTelemetryLimit    int
)

var refineryTelemetryCmd = &cobra.Command{
	Use:   "telemetry [rig]",
	Short: "Show per-MR-attempt refinery telemetry (writer model, Codex verdict, time-to-verdict)",
	Long: `Show durable per-MR-attempt and per-review-attempt telemetry recorded by the
Gastown refinery / merge queue path. Each row is one MR processing attempt with
the writer model that authored it, the Codex (durable multi-model) review
verdict, the validation verdict, timestamps, elapsed submit-to-Codex-verdict
time, and a classified failure reason.

Use --summary for a per-writer-model comparison (first-pass Codex pass rate,
final merge rate, rework count, median/p95 time-to-verdict, excluded
infra/unavailable counts) so Mayor can compare umans-kimi vs umans-glm vs m3 on
the current fleet.

The writer_model recorded for an attempt is the one that authored the actual
submitted MR attempt (captured at refinery claim), not necessarily the model
currently assigned to the source bead — so reassignments and rework do not
muddy the attribution.

Examples:
  gt refinery telemetry                 # recent attempts
  gt refinery telemetry --summary       # per-model comparison
  gt refinery telemetry --summary --since 7d
  gt refinery telemetry --writer umans-glm
  gt refinery telemetry --json`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRefineryTelemetry,
}

func init() {
	refineryTelemetryCmd.Flags().BoolVar(&refineryTelemetrySummary, "summary", false, "Summarize by writer model instead of listing attempts")
	refineryTelemetryCmd.Flags().BoolVar(&refineryTelemetryJSON, "json", false, "Output as JSON")
	refineryTelemetryCmd.Flags().StringVar(&refineryTelemetryWriter, "writer", "", "Filter to a single writer model")
	refineryTelemetryCmd.Flags().StringVar(&refineryTelemetrySince, "since", "", "Only records on/after this time (e.g. 7d, 24h, or 2026-06-01)")
	refineryTelemetryCmd.Flags().StringVar(&refineryTelemetryUntil, "until", "", "Only records before this time (e.g. 2026-06-30)")
	refineryTelemetryCmd.Flags().IntVar(&refineryTelemetryLimit, "limit", 50, "Max attempts to list (0 = unlimited)")
	refineryCmd.AddCommand(refineryTelemetryCmd)
}

// parseTelemetryTime parses a --since/--until value. Supports duration
// shorthand (7d, 24h, 30m) relative to now for --since, and an absolute
// RFC3339 or YYYY-MM-DD date. Returns the zero time for an empty value.
func parseTelemetryTime(val string, since bool) (time.Time, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return time.Time{}, nil
	}
	// Duration shorthand: "7d", "24h", "30m". Only meaningful for --since.
	if d, ok := parseDurationShorthand(val); ok {
		if since {
			return time.Now().Add(-d), nil
		}
		return time.Time{}, fmt.Errorf("--until does not accept duration shorthand %q (use an absolute date)", val)
	}
	// Absolute date YYYY-MM-DD or full RFC3339.
	layouts := []string{time.RFC3339, "2006-01-02"}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, val, time.Local); err == nil {
			if layout == "2006-01-02" && since {
				// Start of that day.
				return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()), nil
			}
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q (use 7d/24h/30m or YYYY-MM-DD or RFC3339)", val)
}

// parseDurationShorthand parses a duration like "7d" or "24h" or "30m",
// extending Go's time.ParseDuration with a "d" (day) suffix.
func parseDurationShorthand(s string) (time.Duration, bool) {
	if s == "" {
		return 0, false
	}
	// days
	if strings.HasSuffix(s, "d") {
		n, err := parseInt(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, false
		}
		return time.Duration(n) * 24 * time.Hour, true
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, false
	}
	return d, true
}

func parseInt(s string) (int, error) {
	var n int
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not an int")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// telemetryDirForRig resolves the refinery telemetry directory from the town
// root found via cwd (or the provided rig). Returns ("", error) if the town
// root cannot be resolved.
func telemetryDirForRig(rigName string) (string, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return "", fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	dir := filepath.Join(townRoot, ".runtime", mrtelemetry.DefaultTelemetryDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create telemetry dir: %w", err)
	}
	return dir, nil
}

func runRefineryTelemetry(cmd *cobra.Command, args []string) error {
	rigName := ""
	if len(args) > 0 {
		rigName = args[0]
	}
	_ = rigName // telemetry is town-wide; rig is informational only

	dir, err := telemetryDirForRig(rigName)
	if err != nil {
		return err
	}

	since, err := parseTelemetryTime(refineryTelemetrySince, true)
	if err != nil {
		return err
	}
	until, err := parseTelemetryTime(refineryTelemetryUntil, false)
	if err != nil {
		return err
	}

	records, err := mrtelemetry.ReadRecords(dir, since, until)
	if err != nil {
		return fmt.Errorf("read telemetry: %w", err)
	}

	// Optional writer filter.
	if refineryTelemetryWriter != "" {
		filtered := records[:0]
		for _, r := range records {
			if strings.EqualFold(r.WriterModel, refineryTelemetryWriter) {
				filtered = append(filtered, r)
			}
		}
		records = filtered
	}

	if refineryTelemetrySummary {
		summaries := mrtelemetry.Summarize(records, mrtelemetry.SummaryOptions{Since: since, Until: until})
		return emitTelemetrySummary(summaries)
	}

	// List mode: newest first, bounded by --limit.
	sort.Slice(records, func(i, j int) bool {
		return records[i].SubmittedAt > records[j].SubmittedAt
	})
	if refineryTelemetryLimit > 0 && len(records) > refineryTelemetryLimit {
		records = records[:refineryTelemetryLimit]
	}
	return emitTelemetryList(records)
}

func emitTelemetrySummary(summaries []mrtelemetry.WriterModelSummary) error {
	if refineryTelemetryJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(summaries)
	}
	if len(summaries) == 0 {
		fmt.Println("No telemetry records found.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "WRITER\tATTEMPTS\t1ST-PASS%\tCODEX-PASS\tCODEX-FAIL\tMERGED%\tREWORK\tMEDIAN-MS\tP95-MS\tEXCLUDED")
	for _, s := range summaries {
		excluded := s.ExcludedInfra + s.ExcludedUnavailable + s.ExcludedTimeout
		fmt.Fprintf(w, "%s\t%d\t%.1f%%\t%d\t%d\t%.1f%%\t%d\t%d\t%d\t%d\n",
			s.WriterModel, s.TotalAttempts,
			s.FirstPassCodexPassRate*100, s.CodexPassCount, s.CodexFailCount,
			s.FinalMergeRate*100, s.ReworkCount,
			s.MedianTimeToVerdict.Milliseconds(), s.P95TimeToVerdict.Milliseconds(),
			excluded)
	}
	return w.Flush()
}

func emitTelemetryList(records []mrtelemetry.AttemptRecord) error {
	if refineryTelemetryJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(records)
	}
	if len(records) == 0 {
		fmt.Println("No telemetry records found.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SUBMITTED\tWRITER\tMR\tATTEMPT\tCODEX\tVALID\tFINAL\tFAILURE\tDURATION-MS")
	for _, r := range records {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%d\n",
			shortTelemetryTS(r.SubmittedAt),
			r.WriterModel, r.MRID, r.Attempt,
			r.CodexVerdict, emptyOr(r.ValidationVerdict, "skipped"),
			r.FinalGateDecision, r.FailureClass,
			r.SubmitToVerdictDuration().Milliseconds())
	}
	return w.Flush()
}

// shortTelemetryTS trims an RFC3339 timestamp to its date + HH:MM:SS for compact
// table display. Falls back to the raw value on parse failure.
func shortTelemetryTS(s string) string {
	if s == "" {
		return "-"
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Local().Format("01-02 15:04:05")
	}
	return s
}

func emptyOr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
