package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	reworkDeferredJSON bool
)

var witnessReworkDeferredCmd = &cobra.Command{
	Use:     "rework-deferred",
	Aliases: []string{"rd"},
	Short:   "Inspect and test the REWORK_DEFERRED throttle",
	Long: `Inspect and test the REWORK_DEFERRED throttle.

The throttle suppresses repeated identical REWORK_DEFERRED notifications for
blocked rework so the Mayor is not flooded when an active DEFER/HOLD/PARK
decision prevents automated rework.

Subcommands:
  dry-run      Run the regression scenario against live throttle code (temp state)
  live-dry-run Run the regression scenario against the EXACT state file used by
               the running witness (gastown-fvy: catches bypass emitters and
               wrong state-path writes that the temp-dir dry-run cannot detect)
  list         Show currently throttled tuples from the durable state file`,
	RunE: requireSubcommand,
}

var witnessReworkDeferredDryRunCmd = &cobra.Command{
	Use:   "dry-run",
	Short: "Run the regression dry-run scenario",
	Long: `Run the regression scenario that proves the REWORK_DEFERRED throttle works.

Exercises the exact acceptance-criteria tuples:
  - polybot-uiu / gt-hold1
  - polybot-uiu / gt-park1
  - polybot-uiu / gt-work999

First occurrences emit, identical repeats inside the 1-hour window are
suppressed, and the first call after the window elapses is a rollup. State
changes emit immediately. The run uses a temporary state directory; production
throttle state is never modified.

Exit code is 0 when the dry run passes, 1 on failure.`,
	RunE: runWitnessReworkDeferredDryRun,
}

var witnessReworkDeferredListCmd = &cobra.Command{
	Use:   "list",
	Short: "List throttled REWORK_DEFERRED tuples",
	Long: `List throttled REWORK_DEFERRED tuples from the durable state file.

Output is sorted by bead ID then polecat name. Use --json for machine-readable
output.`,
	RunE: runWitnessReworkDeferredList,
}

var witnessReworkDeferredLiveDryRunCmd = &cobra.Command{
	Use:   "live-dry-run",
	Short: "Run the regression scenario against the live throttle state file",
	Long: `Run the regression scenario against the EXACT state file and EXACT
notification path used by the running witness (gastown-fvy).

The temp-dir dry-run above proves the throttle math; this run proves the live
path actually routes through it. It writes to the production state file
(<townRoot>/witness/rework-deferred-throttle.json) using fixture tuples that
are prefixed "live-dryrun-" so they never collide with production records.
Those records are removed on cleanup; any leftover fixture records are
reported as a failure.

The run uses a captured mail sender instead of the real router, so the
Mayor inbox is NOT touched — but every emit/rollup call site in the live
path is exercised end to end. This catches:

  - Live emitters that bypass EvaluateReworkDeferred (no state written,
    Mayor still gets duplicate notices — the gastown-fvy failure mode).
  - Wrong state-path writes (state file appears, but at a different path
    the daemon does not consult).
  - Stale binaries or wrappers that pre-date the gastown-3ip rollup fix
    (rollup reports "0 suppressed").

Exit code is 0 when the live dry run passes, 1 on failure.`,
	RunE: runWitnessReworkDeferredLiveDryRun,
}

func init() {
	witnessReworkDeferredDryRunCmd.Flags().BoolVar(&reworkDeferredJSON, "json", false, "Output dry-run result as JSON")
	witnessReworkDeferredListCmd.Flags().BoolVar(&reworkDeferredJSON, "json", false, "Output list as JSON")
	witnessReworkDeferredLiveDryRunCmd.Flags().BoolVar(&reworkDeferredJSON, "json", false, "Output live-dry-run result as JSON")

	witnessReworkDeferredCmd.AddCommand(witnessReworkDeferredDryRunCmd)
	witnessReworkDeferredCmd.AddCommand(witnessReworkDeferredListCmd)
	witnessReworkDeferredCmd.AddCommand(witnessReworkDeferredLiveDryRunCmd)
	witnessCmd.AddCommand(witnessReworkDeferredCmd)
}

func runWitnessReworkDeferredDryRun(cmd *cobra.Command, args []string) error {
	result, err := witness.DryRunReworkDeferred()
	if err != nil {
		return fmt.Errorf("running rework-deferred dry-run: %w", err)
	}

	if reworkDeferredJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			return fmt.Errorf("encoding JSON: %w", err)
		}
		if !result.Pass {
			return fmt.Errorf("dry run failed")
		}
		return nil
	}

	if result.Pass {
		fmt.Printf("%s REWORK_DEFERRED throttle dry-run passed\n", style.SuccessPrefix)
	} else {
		fmt.Printf("%s REWORK_DEFERRED throttle dry-run failed\n", style.ErrorPrefix)
	}
	fmt.Printf("  Window: %s\n", result.Window)
	for _, t := range result.Tuples {
		fmt.Printf("  - %s (%s): first=%s repeat=%s rollup=%s suppressed=%d rollup_suppressed=%d\n",
			t.Bead, t.Decision, t.FirstAction, t.RepeatAction, t.RollupAction, t.SuppressedCount, t.RollupSuppressedCount)
	}
	for _, e := range result.Errors {
		fmt.Printf("    %s %s\n", style.ErrorPrefix, e)
	}

	if !result.Pass {
		return fmt.Errorf("dry run failed")
	}
	return nil
}

func runWitnessReworkDeferredList(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	records := witness.ListReworkDeferredRecords(townRoot)
	if reworkDeferredJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(records)
	}

	if len(records) == 0 {
		fmt.Printf("%s No REWORK_DEFERRED throttle records found.\n", style.Dim.Render("•"))
		return nil
	}

	fmt.Printf("%s REWORK_DEFERRED throttle records (%d):\n", style.Bold.Render("→"), len(records))
	for _, rec := range records {
		fmt.Printf("  - %s/%s %s decision=%s status=%s suppressed=%d last=%s\n",
			rec.RigName, rec.PolecatName, rec.BeadID, rec.MayorDecision,
			rec.SourceStatus, rec.SuppressedCount, rec.LastEmittedAt.Format(timeFormat))
	}
	return nil
}

func runWitnessReworkDeferredLiveDryRun(cmd *cobra.Command, args []string) error {
	result, err := witness.LiveDryRunReworkDeferred("")
	if err != nil {
		return fmt.Errorf("running rework-deferred live-dry-run: %w", err)
	}

	if reworkDeferredJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			return fmt.Errorf("encoding JSON: %w", err)
		}
		if !result.Pass {
			return fmt.Errorf("live dry-run failed")
		}
		return nil
	}

	if result.Pass {
		fmt.Printf("%s REWORK_DEFERRED live dry-run passed\n", style.SuccessPrefix)
	} else {
		fmt.Printf("%s REWORK_DEFERRED live dry-run failed\n", style.ErrorPrefix)
	}
	fmt.Printf("  TownRoot: %s\n", result.TownRoot)
	fmt.Printf("  StatePath: %s\n", result.StatePath)
	fmt.Printf("  Window: %s\n", result.Window)
	for _, t := range result.Tuples {
		fmt.Printf("  - %s/%s (%s): first=%s repeat=%s rollup=%s rollup_suppressed=%d mail_sent=%d\n",
			t.Bead, t.Polecat, t.Decision, t.FirstAction, t.RepeatAction, t.RollupAction, t.RollupSuppressedCount, t.MailSent)
	}
	if result.CleanupRemoved > 0 {
		fmt.Printf("  Cleanup: removed %d live-dryrun- records (state file untouched for non-live-dryrun- tuples)\n", result.CleanupRemoved)
	}
	if result.SaveErrors > 0 {
		fmt.Printf("  %s saveReworkDeferredState returned %d errors during the run\n", style.ErrorPrefix, result.SaveErrors)
	}
	for _, e := range result.Errors {
		fmt.Printf("    %s %s\n", style.ErrorPrefix, e)
	}

	if !result.Pass {
		return fmt.Errorf("live dry-run failed")
	}
	return nil
}

// timeFormat matches the time.RFC3339 short form used elsewhere.
const timeFormat = "2006-01-02T15:04:05Z"
