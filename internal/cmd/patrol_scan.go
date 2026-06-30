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
	"github.com/steveyegge/gastown/internal/alerts"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	patrolScanJSON    bool
	patrolScanNotify  bool
	patrolScanRig     string
	patrolScanVerbose bool
)

var patrolScanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Scan polecats for zombies, stalls, and completions",
	Long: `Run proactive detection across all polecats in a rig.

This command bridges the witness library detection functions to the CLI,
providing a single command for the survey-workers patrol step.

Detections:
  - Zombies: Dead sessions with active agent state, dead agent processes,
    stuck done-intent, closed beads with live sessions
  - Stalls: Agents stuck at startup prompts
  - Completions: Agent bead metadata indicating gt done was called

Actions taken automatically:
  - Zombie restart: Sessions are restarted (not nuked) to preserve worktrees
  - Cleanup wisps: Created for dirty state tracking
  - Completion routing: MR cleanup wisps created, refinery nudged

Use --notify to send mail when zombies with active work are detected.
Long-running scan phases emit progress diagnostics to stderr so JSON stdout
remains machine-readable while operators can see where a slow patrol is stuck.

Examples:
  gt patrol scan                    # Scan current rig
  gt patrol scan --rig gastown      # Scan specific rig
  gt patrol scan --json             # Machine-readable output
  gt patrol scan --notify           # Send mail on zombie detection`,
	RunE: runPatrolScan,
}

func init() {
	patrolScanCmd.Flags().BoolVar(&patrolScanJSON, "json", false, "Output as JSON")
	patrolScanCmd.Flags().BoolVar(&patrolScanNotify, "notify", false, "Send mail to witness/mayor when active-work zombies are detected")
	patrolScanCmd.Flags().StringVar(&patrolScanRig, "rig", "", "Rig to scan (default: infer from cwd or GT_RIG)")
	patrolScanCmd.Flags().BoolVarP(&patrolScanVerbose, "verbose", "v", false, "Verbose output")

	patrolCmd.AddCommand(patrolScanCmd)
}

var patrolScanProgressInterval = 10 * time.Second

// PatrolScanOutput is the JSON output format for patrol scan results.
type PatrolScanOutput struct {
	Rig         string                    `json:"rig"`
	Timestamp   string                    `json:"timestamp"`
	Zombies     *PatrolScanZombieOutput   `json:"zombies"`
	Stalls      *PatrolScanStallOutput    `json:"stalls,omitempty"`
	Completions *PatrolScanCompleteOutput `json:"completions,omitempty"`
	// Fleet is the per-bucket polecat composition: implementation,
	// post-submit gate, recovery-held, idle. Patrol consumers (witness,
	// mayor) read this to answer "is anything actually in flight?" without
	// collapsing MQ gates into "no active polecats". (gastown-72v)
	Fleet    *witness.FleetState     `json:"fleet,omitempty"`
	Receipts []witness.PatrolReceipt `json:"receipts,omitempty"`
}

// PatrolScanZombieOutput holds zombie detection results.
type PatrolScanZombieOutput struct {
	Checked int                    `json:"checked"`
	Found   int                    `json:"found"`
	Zombies []PatrolScanZombieItem `json:"zombies,omitempty"`
	Errors  []string               `json:"errors,omitempty"`
}

// PatrolScanZombieItem is a single zombie detection in scan output.
type PatrolScanZombieItem struct {
	Polecat        string `json:"polecat"`
	Classification string `json:"classification"`
	AgentState     string `json:"agent_state"`
	HookBead       string `json:"hook_bead,omitempty"`
	CleanupStatus  string `json:"cleanup_status,omitempty"`
	Action         string `json:"action"`
	WasActive      bool   `json:"was_active"`
	Error          string `json:"error,omitempty"`
}

// PatrolScanStallOutput holds stall detection results.
type PatrolScanStallOutput struct {
	Checked int                   `json:"checked"`
	Found   int                   `json:"found"`
	Stalls  []PatrolScanStallItem `json:"stalls,omitempty"`
}

// PatrolScanStallItem is a single stall detection in scan output.
type PatrolScanStallItem struct {
	Polecat   string `json:"polecat"`
	StallType string `json:"stall_type"`
	Action    string `json:"action"`
	Error     string `json:"error,omitempty"`
}

// PatrolScanCompleteOutput holds completion discovery results.
type PatrolScanCompleteOutput struct {
	Checked   int                      `json:"checked"`
	Found     int                      `json:"found"`
	Completed []PatrolScanCompleteItem `json:"completed,omitempty"`
}

// PatrolScanCompleteItem is a single completion discovery in scan output.
type PatrolScanCompleteItem struct {
	Polecat        string `json:"polecat"`
	ExitType       string `json:"exit_type"`
	IssueID        string `json:"issue_id,omitempty"`
	MRID           string `json:"mr_id,omitempty"`
	Branch         string `json:"branch,omitempty"`
	Action         string `json:"action"`
	WispCreated    string `json:"wisp_created,omitempty"`
	CompletionTime string `json:"completion_time,omitempty"`
}

func runPatrolScan(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Determine rig name
	rigName := patrolScanRig
	if rigName == "" {
		// Try GT_RIG env, then infer from cwd
		rigName = os.Getenv("GT_RIG")
		if rigName == "" {
			rigName, err = inferRigFromCwd(townRoot)
			if err != nil {
				return fmt.Errorf("could not determine rig: %w\nUse --rig to specify", err)
			}
		}
	}

	bd := witness.DefaultBdCli()
	router := mail.NewRouter(townRoot)
	workDir := townRoot

	timestamp := time.Now().UTC().Format(time.RFC3339)

	// Run all three detection passes.
	// Note: DetectZombiePolecats takes a router param but does NOT send mail
	// internally — it only uses the router for workspace context. Notifications
	// are sent exclusively below via --notify, avoiding double-send.
	diagnostics := cmd.ErrOrStderr()
	zombieResult := runPatrolScanPhase(diagnostics, "zombie detection", func() *witness.DetectZombiePolecatsResult {
		return witness.DetectZombiePolecats(bd, workDir, rigName, router)
	})
	stallResult := runPatrolScanPhase(diagnostics, "stall detection", func() *witness.DetectStalledPolecatsResult {
		return witness.DetectStalledPolecats(workDir, rigName)
	})
	completionResult := runPatrolScanPhase(diagnostics, "completion discovery", func() *witness.DiscoverCompletionsResult {
		return witness.DiscoverCompletions(bd, workDir, rigName, router)
	})

	// Best-effort fleet composition summary (gastown-72v): we want the witness
	// patrol output to present active MQ gates separately from implementation
	// polecats and recovery-held slots, so consumers don't summarize the fleet
	// as "no active polecats" while a refinery gate is draining. The summary
	// requires a working bead store and the rig's polecats directory; if either
	// is unavailable we omit the bucket rather than fail the patrol.
	var fleet *witness.FleetState
	if fstate, ferr := witness.DetectFleetState(&witness.BdCliPolecatBeadSource{Cli: bd, WorkDir: townRoot}, townRoot, rigName); ferr == nil {
		fleet = fstate
	} else {
		fmt.Fprintf(diagnostics, "gt patrol scan: fleet composition unavailable: %v\n", ferr)
	}

	// Build patrol receipts for zombies
	receipts := witness.BuildPatrolReceipts(rigName, zombieResult)

	// Notify when zombies with active work are detected.
	// Always notify the mayor for active-work zombies (dead polecats with hooked
	// beads) — this is the primary mechanism for detecting failed work. (GH #3584)
	// Use --notify=false to suppress (e.g., in dry-run/testing contexts).
	if zombieResult != nil {
		activeZombies := countActiveWorkZombies(zombieResult)
		if activeZombies > 0 {
			// Aggregate alerts into canonical root-cause tracking beads before
			// sending notification summaries. This prevents flooding independent
			// beads for repeated POLECAT_DIED / ZOMBIE_DETECTED alerts.
			rigPath := filepath.Join(townRoot, rigName)
			alertClient := beads.NewWithBeadsDir(rigPath, filepath.Join(rigPath, ".beads"))
			sendZombieNotification(router, alertClient, rigName, zombieResult, activeZombies)
		}
	}

	if patrolScanJSON {
		return outputPatrolScanJSON(os.Stdout, rigName, timestamp, zombieResult, stallResult, completionResult, fleet, receipts)
	}

	return outputPatrolScanHuman(rigName, zombieResult, stallResult, completionResult, receipts)
}

func runPatrolScanPhase[T any](diagnostics io.Writer, name string, fn func() T) T {
	start := time.Now()
	if diagnostics != nil {
		fmt.Fprintf(diagnostics, "gt patrol scan: starting %s\n", name)
	}

	done := make(chan T, 1)
	go func() {
		done <- fn()
	}()

	if patrolScanProgressInterval <= 0 {
		result := <-done
		if diagnostics != nil {
			fmt.Fprintf(diagnostics, "gt patrol scan: finished %s in %s\n", name, formatPatrolScanElapsed(time.Since(start)))
		}
		return result
	}

	ticker := time.NewTicker(patrolScanProgressInterval)
	defer ticker.Stop()

	for {
		select {
		case result := <-done:
			if diagnostics != nil {
				fmt.Fprintf(diagnostics, "gt patrol scan: finished %s in %s\n", name, formatPatrolScanElapsed(time.Since(start)))
			}
			return result
		case <-ticker.C:
			if diagnostics != nil {
				fmt.Fprintf(diagnostics, "gt patrol scan: still running %s after %s\n", name, formatPatrolScanElapsed(time.Since(start)))
			}
		}
	}
}

func formatPatrolScanElapsed(elapsed time.Duration) string {
	if elapsed < time.Second {
		return elapsed.Round(time.Millisecond).String()
	}
	return elapsed.Round(time.Second).String()
}

func countActiveWorkZombies(result *witness.DetectZombiePolecatsResult) int {
	count := 0
	for _, z := range result.Zombies {
		if z.WasActive {
			count++
		}
	}
	return count
}

// notificationSender abstracts mail delivery so sendZombieNotification can be
// unit-tested without a real Router.
type notificationSender interface {
	Send(msg *mail.Message) error
}

func sendZombieNotification(router notificationSender, alertClient alerts.BeadsClient, rigName string, result *witness.DetectZombiePolecatsResult, activeCount int) {
	var lines []string
	lines = append(lines, fmt.Sprintf("Patrol scan detected %d active-work zombie(s) in rig %s:", activeCount, rigName))
	lines = append(lines, "")
	var affectedAgents []string
	for _, z := range result.Zombies {
		if !z.WasActive {
			continue
		}
		affectedAgents = append(affectedAgents, fmt.Sprintf("%s/%s", rigName, z.PolecatName))
		line := fmt.Sprintf("- %s: %s (hook=%s, action=%s)",
			z.PolecatName, string(z.Classification), z.HookBead, z.Action)
		if z.Error != nil {
			line += fmt.Sprintf(" [error: %v]", z.Error)
		}
		lines = append(lines, line)
	}

	body := strings.Join(lines, "\n") +
		"\n\nResling instructions:\n" +
		"  gt sling <bead-id> <rig> --create --force"

	agg := alerts.NewAggregator(alertClient)
	now := time.Now().UTC()

	// Create or update the canonical ZOMBIE_DETECTED tracking bead. This is the
	// witness-facing operational alert — repeated occurrences collapse to one
	// bead keyed by (alert class, rig).
	zombieKey := alerts.RootCauseKey{Class: alerts.ClassZombieDetected, Scope: rigName}
	zombieRes, err := agg.Record(zombieKey, alerts.Evidence{
		Timestamp: now,
		Severity:  "high",
		Agents:    affectedAgents,
		Body:      body,
	})
	if err != nil {
		// Fail open: still send the notification so operators are not left blind.
		fmt.Fprintf(os.Stderr, "warning: failed to aggregate ZOMBIE_DETECTED alert: %v\n", err)
	}

	// Create or update the canonical POLECAT_DIED tracking bead. This is the
	// mayor-facing escalation alert — it is a distinct root cause from the zombie
	// detection alert above and is tracked separately.
	polecatKey := alerts.RootCauseKey{Class: alerts.ClassPolecatDied, Scope: rigName}
	polecatRes, err := agg.Record(polecatKey, alerts.Evidence{
		Timestamp: now,
		Severity:  "high",
		Agents:    affectedAgents,
		Body:      body,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to aggregate POLECAT_DIED alert: %v\n", err)
	}

	// Send lightweight notification summaries that reference the canonical
	// tracking beads. The durable alert record lives in the tracking bead;
	// these messages are only for awareness.
	notifyBody := func(label, issueID string) string {
		if issueID == "" {
			return body
		}
		return fmt.Sprintf("Canonical tracking bead: %s\n\n%s", issueID, body)
	}

	witnessSubject := fmt.Sprintf("ZOMBIE_DETECTED: %d active-work zombie(s) in %s", activeCount, rigName)
	witnessMsg := &mail.Message{
		From:    fmt.Sprintf("%s/witness", rigName),
		To:      fmt.Sprintf("%s/witness", rigName),
		Subject: witnessSubject,
		Body:    notifyBody("Zombie tracking bead", alertResIssueID(zombieRes)),
	}
	_ = router.Send(witnessMsg)

	mayorSubject := fmt.Sprintf("POLECAT_DIED: %d polecat(s) died with active work in %s", activeCount, rigName)
	mayorMsg := &mail.Message{
		From:    fmt.Sprintf("%s/witness", rigName),
		To:      "mayor/",
		Subject: mayorSubject,
		Body:    notifyBody("Polecat-died tracking bead", alertResIssueID(polecatRes)),
	}
	_ = router.Send(mayorMsg)
}

func alertResIssueID(res *alerts.RecordResult) string {
	if res == nil {
		return ""
	}
	return res.IssueID
}

func outputPatrolScanJSON(w io.Writer, rigName, timestamp string, zombieResult *witness.DetectZombiePolecatsResult, stallResult *witness.DetectStalledPolecatsResult, completionResult *witness.DiscoverCompletionsResult, fleet *witness.FleetState, receipts []witness.PatrolReceipt) error {
	output := PatrolScanOutput{
		Rig:       rigName,
		Timestamp: timestamp,
		Fleet:     fleet,
		Receipts:  receipts,
	}

	// Zombies
	if zombieResult != nil {
		zo := &PatrolScanZombieOutput{
			Checked: zombieResult.Checked,
			Found:   len(zombieResult.Zombies),
		}
		for _, z := range zombieResult.Zombies {
			item := PatrolScanZombieItem{
				Polecat:        z.PolecatName,
				Classification: string(z.Classification),
				AgentState:     z.AgentState,
				HookBead:       z.HookBead,
				CleanupStatus:  z.CleanupStatus,
				Action:         z.Action,
				WasActive:      z.WasActive,
			}
			if z.Error != nil {
				item.Error = z.Error.Error()
			}
			zo.Zombies = append(zo.Zombies, item)
		}
		for _, e := range zombieResult.Errors {
			zo.Errors = append(zo.Errors, e.Error())
		}
		output.Zombies = zo
	}

	// Stalls
	if stallResult != nil {
		so := &PatrolScanStallOutput{
			Checked: stallResult.Checked,
			Found:   len(stallResult.Stalled),
		}
		for _, s := range stallResult.Stalled {
			item := PatrolScanStallItem{
				Polecat:   s.PolecatName,
				StallType: s.StallType,
				Action:    s.Action,
			}
			if s.Error != nil {
				item.Error = s.Error.Error()
			}
			so.Stalls = append(so.Stalls, item)
		}
		output.Stalls = so
	}

	// Completions
	if completionResult != nil {
		co := &PatrolScanCompleteOutput{
			Checked: completionResult.Checked,
			Found:   len(completionResult.Discovered),
		}
		for _, d := range completionResult.Discovered {
			item := PatrolScanCompleteItem{
				Polecat:        d.PolecatName,
				ExitType:       d.ExitType,
				IssueID:        d.IssueID,
				MRID:           d.MRID,
				Branch:         d.Branch,
				Action:         d.Action,
				WispCreated:    d.WispCreated,
				CompletionTime: d.CompletionTime,
			}
			co.Completed = append(co.Completed, item)
		}
		output.Completions = co
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}

func outputPatrolScanHuman(rigName string, zombieResult *witness.DetectZombiePolecatsResult, stallResult *witness.DetectStalledPolecatsResult, completionResult *witness.DiscoverCompletionsResult, _ []witness.PatrolReceipt) error {
	fmt.Printf("%s Patrol scan: %s\n\n", style.Bold.Render("🔍"), rigName)

	// Zombies
	if zombieResult != nil {
		fmt.Printf("%s Zombie Detection: checked %d polecat(s)\n",
			style.Bold.Render("👻"), zombieResult.Checked)

		if len(zombieResult.Zombies) == 0 {
			fmt.Printf("  %s\n", style.Dim.Render("No zombies detected"))
		} else {
			for _, z := range zombieResult.Zombies {
				icon := "⚠"
				if z.WasActive {
					icon = "🚨"
				}
				fmt.Printf("  %s %s: %s\n", icon, z.PolecatName, z.Classification)
				fmt.Printf("    State: %s", z.AgentState)
				if z.HookBead != "" {
					fmt.Printf("  Hook: %s", z.HookBead)
				}
				if z.CleanupStatus != "" {
					fmt.Printf("  Cleanup: %s", z.CleanupStatus)
				}
				fmt.Println()
				fmt.Printf("    Action: %s\n", z.Action)
				if z.Error != nil {
					fmt.Printf("    %s\n", style.Dim.Render(fmt.Sprintf("Error: %v", z.Error)))
				}
			}
		}

		if len(zombieResult.Errors) > 0 && patrolScanVerbose {
			fmt.Printf("  Errors: %d\n", len(zombieResult.Errors))
			for _, e := range zombieResult.Errors {
				fmt.Printf("    - %v\n", e)
			}
		}

		if len(zombieResult.ConvoyFailures) > 0 {
			fmt.Printf("  Convoy failures: %d\n", len(zombieResult.ConvoyFailures))
		}
		fmt.Println()
	}

	// Stalls
	if stallResult != nil && (len(stallResult.Stalled) > 0 || patrolScanVerbose) {
		fmt.Printf("%s Stall Detection: checked %d polecat(s)\n",
			style.Bold.Render("⏳"), stallResult.Checked)

		if len(stallResult.Stalled) == 0 {
			fmt.Printf("  %s\n", style.Dim.Render("No stalls detected"))
		} else {
			for _, s := range stallResult.Stalled {
				fmt.Printf("  ⚠ %s: %s → %s\n", s.PolecatName, s.StallType, s.Action)
				if s.Error != nil {
					fmt.Printf("    %s\n", style.Dim.Render(fmt.Sprintf("Error: %v", s.Error)))
				}
			}
		}
		fmt.Println()
	}

	// Completions
	if completionResult != nil && (len(completionResult.Discovered) > 0 || patrolScanVerbose) {
		fmt.Printf("%s Completion Discovery: checked %d polecat(s)\n",
			style.Bold.Render("✅"), completionResult.Checked)

		if len(completionResult.Discovered) == 0 {
			fmt.Printf("  %s\n", style.Dim.Render("No completions discovered"))
		} else {
			for _, d := range completionResult.Discovered {
				fmt.Printf("  ● %s: exit=%s", d.PolecatName, d.ExitType)
				if d.IssueID != "" {
					fmt.Printf("  issue=%s", d.IssueID)
				}
				if d.MRID != "" {
					fmt.Printf("  mr=%s", d.MRID)
				}
				fmt.Println()
				fmt.Printf("    Action: %s\n", d.Action)
			}
		}
		fmt.Println()
	}

	// Summary
	zombieCount := 0
	activeCount := 0
	if zombieResult != nil {
		zombieCount = len(zombieResult.Zombies)
		activeCount = countActiveWorkZombies(zombieResult)
	}
	stallCount := 0
	if stallResult != nil {
		stallCount = len(stallResult.Stalled)
	}
	completionCount := 0
	if completionResult != nil {
		completionCount = len(completionResult.Discovered)
	}

	if zombieCount == 0 && stallCount == 0 && completionCount == 0 {
		fmt.Printf("%s All clear — no issues detected\n", style.Success.Render("✓"))
	} else {
		fmt.Printf("Summary: %d zombie(s) (%d active-work), %d stall(s), %d completion(s)\n",
			zombieCount, activeCount, stallCount, completionCount)
	}

	return nil
}
