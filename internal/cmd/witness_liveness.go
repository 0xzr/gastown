package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Witness self-recovery subcommand flags (gastown-o9d).
var (
	witnessHandoffReason      string
	witnessHandoffDryRun      bool
	witnessHandoffJSON        bool
	witnessHandoffSaturation  float64
	witnessHandoffObligations int

	witnessLivenessJSON bool

	witnessReplayJSON       bool
	witnessReplayApply      bool
	witnessReplayClearAfter bool

	witnessHeartbeatJSON            bool
	witnessHeartbeatStep            string
	witnessHeartbeatAction          string
	witnessHeartbeatSaturation      float64
	witnessHeartbeatCommandDuration time.Duration
	witnessHeartbeatObligations     int
)

// witnessHandoffCmd writes a durable handoff file for the witness of a
// single rig. The Witness's own context-check step (and any
// supervisor-side restart) calls this before a self-restart so the
// post-restart Witness can resume exactly where the previous session
// left off: stopped lanes (polecats that need restart), dirty lanes
// (uncommitted or unpushed work), queued scheduler beads, in-flight
// cleanup commands, and a free-form notes field.
//
// The handoff file lives at <rigPath>/witness/handoff.json and is
// cleared by the post-restart Witness once it has consumed the
// obligations. Without this, the 2026-06-25 incident pattern (Witness
// sat at 100% context and Mayor had to manually restart four
// polecats one by one) repeats every time a Witness saturates.
var witnessHandoffCmd = &cobra.Command{
	Use:   "handoff <rig>",
	Short: "Write a durable handoff file for the witness (gastown-o9d)",
	Long: `Write a durable handoff file describing the witness's current
recovery obligations, then exit.

The handoff file (<rig>/witness/handoff.json) is the single source of
truth for "what was the witness working on when it died". The
post-restart witness reads it, replays the obligations, and clears
the file once every stopped lane and in-flight cleanup is acknowledged.

Use --reason to tag the handoff with a supervision signal (default:
"self-restart"). Use --saturation to record the witness's own
self-reported context usage. Use --obligations to record the current
outstanding recovery obligation count.

Examples:
  gt witness handoff gastown --reason context-saturated
  gt witness handoff gastown --reason supervised-restart --saturation 0.95
  gt witness handoff gastown --dry-run
  gt witness handoff gastown --json`,
	Args: cobra.ExactArgs(1),
	RunE: runWitnessHandoff,
}

func init() {
	witnessHandoffCmd.Flags().StringVar(&witnessHandoffReason, "reason", "self-restart",
		"Reason tag for the handoff (e.g. context-saturated, supervised-restart, command-stuck)")
	witnessHandoffCmd.Flags().BoolVar(&witnessHandoffDryRun, "dry-run", false,
		"Print the handoff that would be written but do not write or restart")
	witnessHandoffCmd.Flags().BoolVar(&witnessHandoffJSON, "json", false,
		"Emit the handoff record as JSON instead of human-readable text")
	witnessHandoffCmd.Flags().Float64Var(&witnessHandoffSaturation, "saturation", 0,
		"Context saturation percentage (0.0-1.0) to record in the handoff")
	witnessHandoffCmd.Flags().IntVar(&witnessHandoffObligations, "obligations", 0,
		"Outstanding recovery obligation count to record in the handoff")

	witnessLivenessCmd.Flags().BoolVar(&witnessLivenessJSON, "json", false,
		"Emit liveness state as JSON instead of human-readable text")

	witnessReplayCmd.Flags().BoolVar(&witnessReplayJSON, "json", false,
		"Emit the recovery plan as JSON instead of human-readable text")
	witnessReplayCmd.Flags().BoolVar(&witnessReplayApply, "apply", false,
		"Execute the model-preserving restart commands (default: dry-run plan only)")
	witnessReplayCmd.Flags().BoolVar(&witnessReplayClearAfter, "clear-after", false,
		"After applying (or printing) the plan, remove the handoff file")

	witnessHeartbeatCmd.Flags().BoolVar(&witnessHeartbeatJSON, "json", false,
		"Emit the written heartbeat as JSON instead of human-readable text")
	witnessHeartbeatCmd.Flags().StringVar(&witnessHeartbeatStep, "step", "heartbeat",
		"Last completed patrol step to record (e.g. context-check, survey-workers)")
	witnessHeartbeatCmd.Flags().StringVar(&witnessHeartbeatAction, "action", "patrol-cycle",
		"Action that produced this heartbeat (e.g. step-inbox-check, patrol-cycle)")
	witnessHeartbeatCmd.Flags().Float64Var(&witnessHeartbeatSaturation, "saturation", 0,
		"Context saturation percentage (0.0-1.0) the witness self-reports")
	witnessHeartbeatCmd.Flags().DurationVar(&witnessHeartbeatCommandDuration, "command-duration", 0,
		"Wall-clock duration of the last patrol command (e.g. 320ms, 90s)")
	witnessHeartbeatCmd.Flags().IntVar(&witnessHeartbeatObligations, "obligations", 0,
		"Outstanding recovery obligation count to record")

	witnessCmd.AddCommand(witnessHandoffCmd)
	witnessCmd.AddCommand(witnessLivenessCmd)
	witnessCmd.AddCommand(witnessReplayCmd)
	witnessCmd.AddCommand(witnessHeartbeatCmd)
}

func runWitnessHandoff(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	townRoot, err := workspace.Find(".")
	if err != nil || townRoot == "" {
		// Best-effort: fall back to current working directory's parent
		// so the command still works in a partial checkout.
		cwd, _ := os.Getwd()
		townRoot = filepath.Dir(cwd)
	}
	rigPath := filepath.Join(townRoot, rigName)

	// Make sure the witness subdir exists so the handoff write is
	// atomic (parent must exist for tmp+rename).
	witnessDir := filepath.Join(rigPath, "witness")
	if err := os.MkdirAll(witnessDir, 0o755); err != nil {
		return fmt.Errorf("ensuring witness dir: %w", err)
	}

	// Capture the current heartbeat (if any) so the handoff reflects
	// the witness's own state at handoff time.
	sup := witness.NewSupervisor(townRoot, rigPath, rigName, "gt")
	hb := sup.ReadHeartbeat()

	saturation := witnessHandoffSaturation
	lastStep := ""
	lastCycle := int64(0)
	if hb != nil {
		if saturation == 0 {
			saturation = hb.ContextSaturationPercent
		}
		lastStep = hb.LastStep
		lastCycle = hb.Cycle
	}

	hf := &witness.HandoffFile{
		Timestamp:                time.Now().UTC(),
		Reason:                   witnessHandoffReason,
		LastStep:                 lastStep,
		LastCycle:                lastCycle,
		RigName:                  rigName,
		ContextSaturationPercent: saturation,
		OutstandingObligations:   witnessHandoffObligations,
		Notes:                    fmt.Sprintf("obligations=%d", witnessHandoffObligations),
	}

	if witnessHandoffDryRun {
		if witnessHandoffJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(hf)
		}
		fmt.Printf("%s Would write handoff (dry-run):\n", style.Dim.Render("○"))
		fmt.Printf("  Reason:       %s\n", hf.Reason)
		fmt.Printf("  Rig:          %s\n", hf.RigName)
		fmt.Printf("  Last step:    %q\n", hf.LastStep)
		fmt.Printf("  Saturation:   %.2f\n", hf.ContextSaturationPercent)
		fmt.Printf("  Obligations:  %d\n", hf.OutstandingObligations)
		return nil
	}

	if err := witness.WriteHandoff(rigPath, hf); err != nil {
		return fmt.Errorf("writing handoff: %w", err)
	}

	if witnessHandoffJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"handoff": hf,
			"path":    witness.HandoffFilePath(rigPath),
		})
	}

	fmt.Printf("%s Wrote witness handoff for %s\n", style.Bold.Render("✓"), rigName)
	fmt.Printf("  Path:        %s\n", witness.HandoffFilePath(rigPath))
	fmt.Printf("  Reason:      %s\n", hf.Reason)
	fmt.Printf("  Saturation:  %.2f\n", hf.ContextSaturationPercent)
	fmt.Printf("  Obligations: %d\n", hf.OutstandingObligations)
	return nil
}

// witnessLivenessCmd prints the current witness self-recovery state:
// heartbeat freshness, saturation, outstanding obligations, last
// handoff, and the most recent recovery attempt. Useful for
// post-incident review and for verifying the self-recovery
// infrastructure is wired correctly.
var witnessLivenessCmd = &cobra.Command{
	Use:   "liveness <rig>",
	Short: "Show witness self-recovery liveness state (gastown-o9d)",
	Long: `Show the witness self-recovery liveness state for a single rig.

Reads the per-rig heartbeat, handoff file, and most recent recovery
attempt, then prints a unified summary. Use --json for machine-readable
output suitable for scripts and dashboards.

Examples:
  gt witness liveness gastown
  gt witness liveness gastown --json`,
	Args: cobra.ExactArgs(1),
	RunE: runWitnessLiveness,
}

func runWitnessLiveness(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	townRoot, err := workspace.Find(".")
	if err != nil || townRoot == "" {
		cwd, _ := os.Getwd()
		townRoot = filepath.Dir(cwd)
	}
	rigPath := filepath.Join(townRoot, rigName)

	sup := witness.NewSupervisor(townRoot, rigPath, rigName, "gt")
	hb := sup.ReadHeartbeat()
	hf := sup.ReadHandoff()
	thresholds := sup.Thresholds()
	should, reason := sup.ShouldRestart()
	cooldown, lastRestart, _ := sup.IsOnCooldown()

	view := livenessView{
		Rig:                 rigName,
		RigPath:             rigPath,
		HeartbeatPath:       witness.HeartbeatFile(rigPath),
		HandoffPath:         witness.HandoffFilePath(rigPath),
		Heartbeat:           hb,
		Handoff:             hf,
		ShouldRestart:       should,
		ShouldRestartReason: reason,
		OnCooldown:          cooldown,
		LastRestart:         lastRestart,
		Thresholds:          thresholds,
	}

	if witnessLivenessJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(view)
	}

	fmt.Printf("%s Witness liveness: %s\n\n", style.Bold.Render("📡"), rigName)
	if hb == nil {
		fmt.Printf("  %s No heartbeat file at %s\n",
			style.Dim.Render("○"), witness.HeartbeatFile(rigPath))
	} else {
		fmt.Printf("  Cycle:        %d\n", hb.Cycle)
		fmt.Printf("  Last step:    %q\n", hb.LastStep)
		fmt.Printf("  Last action:  %q\n", hb.LastAction)
		fmt.Printf("  Saturation:   %.2f\n", hb.ContextSaturationPercent)
		fmt.Printf("  Cmd duration: %dms\n", hb.CommandDurationMs)
		fmt.Printf("  Obligations:  %d\n", hb.OutstandingRecoveryObligations)
		fmt.Printf("  Status:       %s\n", hb.SessionStatus)
		fmt.Printf("  Timestamp:    %s (%s old)\n",
			hb.Timestamp.Format(time.RFC3339), hb.Age().Round(time.Second))
	}
	if hf != nil {
		fmt.Printf("\n  Handoff present:\n")
		fmt.Printf("    Reason:     %s\n", hf.Reason)
		fmt.Printf("    Last step:  %q\n", hf.LastStep)
		fmt.Printf("    Stopped:    %d lane(s)\n", len(hf.StoppedLanes))
		fmt.Printf("    Dirty:      %d lane(s)\n", len(hf.DirtyLanes))
		fmt.Printf("    In flight:  %d cleanup(s)\n", len(hf.InFlightCleanup))
	} else {
		fmt.Printf("\n  %s No handoff file\n", style.Dim.Render("○"))
	}
	fmt.Printf("\n  Thresholds:\n")
	fmt.Printf("    Stale:        %s\n", thresholds.StaleThreshold)
	fmt.Printf("    Very stale:   %s\n", thresholds.VeryStaleThreshold)
	fmt.Printf("    Saturation:   %.2f\n", thresholds.ContextSaturationThreshold)
	fmt.Printf("    Cooldown:     %s\n", thresholds.RecoveryCooldown)
	fmt.Printf("    Max cmd:      %s\n", thresholds.MaxCommandDuration)
	fmt.Printf("\n  Decision:\n")
	if should {
		fmt.Printf("    %s should restart: %s\n", style.Bold.Render("→"), reason)
	} else {
		fmt.Printf("    no restart needed\n")
	}
	if cooldown {
		fmt.Printf("    %s on cooldown until %s\n",
			style.Dim.Render("⏳"),
			lastRestart.Add(thresholds.RecoveryCooldown).Format(time.RFC3339))
	}
	return nil
}

// livenessView is the JSON-serializable view used by
// `gt witness liveness --json`. Includes the resolved thresholds so
// dashboards can show "current saturation vs threshold" without
// re-reading the operational config.
type livenessView struct {
	Rig                 string               `json:"rig"`
	RigPath             string               `json:"rig_path"`
	HeartbeatPath       string               `json:"heartbeat_path"`
	HandoffPath         string               `json:"handoff_path"`
	Heartbeat           *witness.Heartbeat   `json:"heartbeat,omitempty"`
	Handoff             *witness.HandoffFile `json:"handoff,omitempty"`
	ShouldRestart       bool                 `json:"should_restart"`
	ShouldRestartReason string               `json:"should_restart_reason,omitempty"`
	OnCooldown          bool                 `json:"on_cooldown"`
	LastRestart         time.Time            `json:"last_restart,omitempty"`
	Thresholds          witness.Thresholds   `json:"thresholds"`
}

// witnessReplayCmd consumes the durable handoff for a rig's Witness
// and emits a model-preserving recovery plan. The plan is a list of
// (polecat, agent, restart-command) tuples derived from the
// StoppedLane assignment metadata. Lanes without a durable
// assignment are surfaced as escalations rather than silently
// rotating to the rig role default (the 2026-06-25 obsidian/GLM
// failure mode).
//
// Default mode is dry-run: the plan is printed (or emitted as JSON)
// but no `gt session start` is invoked. Pass --apply to execute the
// restart commands; pass --clear-after to remove the handoff file
// once the plan has been emitted.
var witnessReplayCmd = &cobra.Command{
	Use:   "replay <rig>",
	Short: "Replay a witness handoff into a model-preserving recovery plan (gastown-o9d)",
	Long: `Consume the durable handoff file for a rig's Witness and emit a
model-preserving recovery plan. The plan is derived from each
StoppedLane's durable assignment metadata (agent-bead or wrapper
model-assignments file).

Lanes with a durable assignment are restarted with an explicit
--agent flag so the lane keeps its prior model. Lanes without a
durable assignment are surfaced as escalations instead of being
silently rotated to the rig role default — this is the failure mode
the 2026-06-25 obsidian/GLM incident surfaced.

By default the plan is printed but not executed. Pass --apply to
run the model-preserving restart commands. Pass --clear-after to
remove the handoff file once the plan has been emitted.

Examples:
  gt witness replay gastown                # dry-run, human output
  gt witness replay gastown --json         # dry-run, JSON plan
  gt witness replay gastown --apply        # execute the restarts
  gt witness replay gastown --apply --clear-after`,
	Args: cobra.ExactArgs(1),
	RunE: runWitnessReplay,
}

func runWitnessReplay(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	townRoot, err := workspace.Find(".")
	if err != nil || townRoot == "" {
		cwd, _ := os.Getwd()
		townRoot = filepath.Dir(cwd)
	}
	rigPath := filepath.Join(townRoot, rigName)

	sup := witness.NewSupervisor(townRoot, rigPath, rigName, "gt")
	plan, err := sup.PlanRecovery()
	if err != nil {
		return fmt.Errorf("planning recovery: %w", err)
	}
	if plan == nil {
		if witnessReplayJSON {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{
				"rig":     rigName,
				"actions": []any{},
				"note":    "no handoff on disk",
			})
		}
		fmt.Printf("%s No handoff on disk for %s — nothing to replay.\n",
			style.Dim.Render("○"), rigName)
		return nil
	}

	if witnessReplayJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(plan); err != nil {
			return err
		}
	} else {
		fmt.Printf("%s Witness recovery plan: %s\n\n", style.Bold.Render("▶"), rigName)
		if len(plan.Actions) == 0 {
			fmt.Printf("  %s no model-preserving actions\n", style.Dim.Render("○"))
		} else {
			fmt.Printf("  %d model-preserving restart(s):\n", len(plan.Actions))
			for _, a := range plan.Actions {
				fmt.Printf("    • %s/%s → %s (source=%s)\n",
					rigName, a.Polecat, a.AssignedAgent, a.AssignmentSource)
				fmt.Printf("      $ %s\n", a.Command)
			}
		}
		if len(plan.Escalations) > 0 {
			fmt.Printf("\n  %d escalation(s) — no durable assignment:\n", len(plan.Escalations))
			for _, e := range plan.Escalations {
				fmt.Printf("    ! %s/%s — %s\n", rigName, e.Polecat, e.Reason)
			}
		}
		if len(plan.InFlightCleanup) > 0 {
			fmt.Printf("\n  %d in-flight cleanup(s) recorded:\n", len(plan.InFlightCleanup))
			for _, c := range plan.InFlightCleanup {
				fmt.Printf("    - %s\n", c)
			}
		}
	}

	if !witnessReplayApply {
		if !witnessReplayJSON {
			fmt.Printf("\n  %s dry-run — pass --apply to execute\n",
				style.Dim.Render("○"))
		}
		return nil
	}

	// Apply phase. Run each model-preserving restart. We deliberately
	// do NOT execute escalations: the operator must decide.
	//
	// gastown-c4r finding #1: the previous implementation ran
	// `sh -c <a.Command>` where a.Command was a string interpolated
	// from handoff fields — a command-injection vector for any
	// non-supervisor code path that writes the handoff. We now build
	// argv directly from the structured LaneRecovery fields via
	// ApplyArgs, which validates every identifier against a strict
	// schema and never invokes a shell. The plan-time Command string
	// is kept only for human/JSON display, never executed.
	applyErrs := []string{}
	for _, a := range plan.Actions {
		args, err := a.ApplyArgs(sup.GTPath(), rigName)
		if err != nil {
			applyErrs = append(applyErrs, fmt.Sprintf("%s/%s: %v", rigName, a.Polecat, err))
			continue
		}
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil {
			applyErrs = append(applyErrs,
				fmt.Sprintf("%s/%s: %v (%s)", rigName, a.Polecat, err, strings.TrimSpace(string(out))))
		}
	}
	if len(applyErrs) > 0 {
		return fmt.Errorf("replay apply had %d failure(s): %s",
			len(applyErrs), strings.Join(applyErrs, "; "))
	}

	if witnessReplayClearAfter {
		if err := witness.ClearHandoff(rigPath); err != nil {
			return fmt.Errorf("clearing handoff: %w", err)
		}
	}
	return nil
}

// witnessHeartbeatCmd writes/refreshes the witness heartbeat file
// (gastown-c4r finding #3). The witness patrol formula MUST call this
// every cycle (or every N steps) so the daemon supervisor's
// ShouldRestart() has a fresh heartbeat to poll. Without it, the
// heartbeat writer (witness.Touch/WriteHeartbeat) has no production
// caller reachable from the witness's own patrol loop (a Claude session
// that only invokes `gt`), so ShouldRestart always returns false for the
// canonical "stalled witness" failure mode this commit was meant to
// handle.
//
// Mirrors `gt deacon heartbeat` — the proven precedent for a patrol
// agent recording liveness on each wake cycle.
var witnessHeartbeatCmd = &cobra.Command{
	Use:   "heartbeat <rig>",
	Short: "Update the witness heartbeat (gastown-o9d)",
	Long: `Update the witness self-recovery heartbeat for a single rig.

The heartbeat is the file the daemon supervisor polls to decide whether
the Witness is making forward progress. A saturated + very-stale (or
long-command-wedged) heartbeat triggers a supervised self-restart with a
durable handoff.

Call this at the end of each patrol step (or at least each patrol
cycle) so the supervisor never mistakes a live, patrolling witness for a
stalled one.

Examples:
  gt witness heartbeat gastown                           # Touch with defaults
  gt witness heartbeat gastown --step context-check --action patrol-cycle
  gt witness heartbeat gastown --saturation 0.93 --obligations 2
  gt witness heartbeat gastown --command-duration 320ms
  gt witness heartbeat gastown --json`,
	Args: cobra.ExactArgs(1),
	RunE: runWitnessHeartbeat,
}

func runWitnessHeartbeat(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	townRoot, err := workspace.Find(".")
	if err != nil || townRoot == "" {
		cwd, _ := os.Getwd()
		townRoot = filepath.Dir(cwd)
	}
	rigPath := filepath.Join(townRoot, rigName)

	sup := witness.NewSupervisor(townRoot, rigPath, rigName, "gt")
	if err := sup.Touch(witnessHeartbeatStep, witnessHeartbeatAction,
		witnessHeartbeatSaturation, witnessHeartbeatCommandDuration, witnessHeartbeatObligations); err != nil {
		return fmt.Errorf("updating witness heartbeat: %w", err)
	}

	hb := sup.ReadHeartbeat()
	if witnessHeartbeatJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(hb)
	}

	fmt.Printf("%s Witness heartbeat updated: %s\n", style.Bold.Render("✓"), rigName)
	if hb != nil {
		fmt.Printf("  Cycle:        %d\n", hb.Cycle)
		fmt.Printf("  Step:         %q\n", hb.LastStep)
		fmt.Printf("  Saturation:   %.2f\n", hb.ContextSaturationPercent)
		fmt.Printf("  Obligations:  %d\n", hb.OutstandingRecoveryObligations)
		fmt.Printf("  Path:         %s\n", witness.HeartbeatFile(rigPath))
	}
	return nil
}

// Suppress unused-import warnings if future refactors drop a call site.
var _ = rig.Rig{}
