package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/wisp"
	"github.com/steveyegge/gastown/internal/witness"
)

// RigStatusKey is the wisp config key for rig operational status.
const RigStatusKey = "status"

// RigStatusParked is the value indicating a rig is parked.
const RigStatusParked = "parked"

const (
	RigParkedAtKey     = "parked_at"
	RigParkedByKey     = "parked_by"
	RigParkedReasonKey = "parked_reason"
	RigParkedOperator  = "operator"
	RigParkedLabel     = "status:parked"
)

var (
	rigParkReason       string
	rigUnparkAsOperator bool
)

var rigParkCmd = &cobra.Command{
	Use:   "park <rig>...",
	Short: "Park one or more rigs (stops agents, daemon won't auto-restart)",
	Long: `Park rigs to temporarily disable them.

Parking a rig:
  - Stops the witness if running
  - Stops the refinery if running
  - Sets status=parked in the wisp layer with park ownership metadata
  - Adds status:parked to the rig identity bead when beads is healthy
  - The daemon respects this status and won't auto-restart agents

This is a Level 1 (local/ephemeral) operation:
  - Only affects this town
  - Disappears on wisp cleanup
  - Use 'gt rig unpark' to resume normal operation

Examples:
  gt rig park gastown
  gt rig park beads gastown mayor`,
	Args: cobra.MinimumNArgs(1),
	RunE: runRigPark,
}

var rigUnparkCmd = &cobra.Command{
	Use:   "unpark <rig>...",
	Short: "Unpark one or more rigs (allow daemon to auto-restart agents)",
	Long: `Unpark rigs to resume normal operation.

Unparking a rig:
  - Removes parked status and park ownership metadata from the wisp layer
  - Removes status:parked from the rig identity bead when beads is healthy
  - Allows the daemon to auto-restart agents
  - Does NOT automatically start agents (use 'gt rig start' for that)

Agent sessions may not unpark operator-owned parks. Use --operator only when a
human is intentionally running this command from inside an agent-context shell.

Examples:
  gt rig unpark gastown
  gt rig unpark beads gastown mayor`,
	Args: cobra.MinimumNArgs(1),
	RunE: runRigUnpark,
}

func init() {
	rigParkCmd.Flags().StringVar(&rigParkReason, "reason", "", "Optional reason recorded with the parked state")
	rigUnparkCmd.Flags().BoolVar(&rigUnparkAsOperator, "operator", false, "Human-only override for unparking an operator-owned park from an agent-context shell")

	rigCmd.AddCommand(rigParkCmd)
	rigCmd.AddCommand(rigUnparkCmd)
}

func runRigPark(cmd *cobra.Command, args []string) error {
	var errs []error

	for _, rigName := range args {
		if err := parkOneRig(rigName, rigParkReason); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", rigName, err))
		}
	}

	if len(errs) > 0 {
		for _, err := range errs {
			fmt.Printf("%s %v\n", style.Error.Render("✗"), err)
		}
		return fmt.Errorf("failed to park %d rig(s)", len(errs))
	}

	return nil
}

func parkOneRig(rigName, reason string) error {
	// Get rig and town root
	townRoot, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	fmt.Printf("Parking rig %s...\n", style.Bold.Render(rigName))

	var stoppedAgents []string

	t := tmux.NewTmux()

	// Stop witness if running
	witnessSession := session.WitnessSessionName(session.PrefixFor(rigName))
	witnessRunning, _ := t.HasSession(witnessSession)
	if witnessRunning {
		fmt.Printf("  Stopping witness...\n")
		witMgr := witness.NewManager(r)
		if err := witMgr.Stop(); err != nil {
			fmt.Printf("  %s Failed to stop witness: %v\n", style.Warning.Render("!"), err)
		} else {
			stoppedAgents = append(stoppedAgents, "Witness stopped")
		}
	}

	// Stop refinery if running
	refinerySession := session.RefinerySessionName(session.PrefixFor(rigName))
	refineryRunning, _ := t.HasSession(refinerySession)
	if refineryRunning {
		fmt.Printf("  Stopping refinery...\n")
		refMgr := refinery.NewManager(r)
		if err := refMgr.Stop(); err != nil {
			fmt.Printf("  %s Failed to stop refinery: %v\n", style.Warning.Render("!"), err)
		} else {
			stoppedAgents = append(stoppedAgents, "Refinery stopped")
		}
	}

	// Set parked status in wisp layer
	wispCfg := wisp.NewConfig(townRoot, rigName)
	parkedAt := time.Now().UTC().Format(time.RFC3339)
	if err := setParkedWispState(wispCfg, parkedAt, parkOwnerFromEnv(), reason); err != nil {
		return err
	}

	if err := addPersistentParkedLabel(r); err != nil {
		fmt.Printf("  %s WARNING: could not persist parked label %s: %v\n",
			style.Warning.Render("!"), RigParkedLabel, err)
	}

	// Output
	fmt.Printf("%s Rig %s parked\n", style.Success.Render("✓"), rigName)
	for _, msg := range stoppedAgents {
		fmt.Printf("  %s\n", msg)
	}
	fmt.Printf("  Daemon will not auto-restart\n")

	return nil
}

func setParkedWispState(wispCfg *wisp.Config, parkedAt, parkedBy, reason string) error {
	if err := wispCfg.Set(RigStatusKey, RigStatusParked); err != nil {
		return fmt.Errorf("setting parked status: %w", err)
	}
	if err := wispCfg.Set(RigParkedAtKey, parkedAt); err != nil {
		return fmt.Errorf("setting parked_at: %w", err)
	}
	if err := wispCfg.Set(RigParkedByKey, parkedBy); err != nil {
		return fmt.Errorf("setting parked_by: %w", err)
	}
	if err := wispCfg.Set(RigParkedReasonKey, reason); err != nil {
		return fmt.Errorf("setting parked_reason: %w", err)
	}
	return nil
}

func runRigUnpark(cmd *cobra.Command, args []string) error {
	var errs []error

	for _, rigName := range args {
		if err := unparkOneRig(rigName); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", rigName, err))
		}
	}

	if len(errs) > 0 {
		for _, err := range errs {
			fmt.Printf("%s %v\n", style.Error.Render("✗"), err)
		}
		return fmt.Errorf("failed to unpark %d rig(s)", len(errs))
	}

	return nil
}

func unparkOneRig(rigName string) error {
	// Get rig and town root
	townRoot, r, err := getRig(rigName)
	if err != nil {
		return err
	}

	wispCfg := wisp.NewConfig(townRoot, rigName)
	parkedBy := parkOwnerOrOperator(wispCfg.GetString(RigParkedByKey))
	if shouldRefuseOperatorOwnedUnpark(os.Getenv("GT_ROLE"), parkedBy, rigUnparkAsOperator) {
		return operatorOwnedParkRefusal(rigName, wispCfg.GetString(RigParkedAtKey), wispCfg.GetString(RigParkedReasonKey))
	}

	// Remove parked status from wisp layer
	if err := clearParkedWispState(wispCfg); err != nil {
		return err
	}

	if err := removePersistentParkedLabel(r); err != nil {
		fmt.Printf("  %s WARNING: could not remove parked label %s: %v\n",
			style.Warning.Render("!"), RigParkedLabel, err)
	}

	fmt.Printf("%s Rig %s unparked\n", style.Success.Render("✓"), rigName)
	fmt.Printf("  Daemon can now auto-restart agents\n")
	fmt.Printf("  Use '%s' to start agents immediately\n", style.Dim.Render("gt rig start "+rigName))

	return nil
}

func clearParkedWispState(wispCfg *wisp.Config) error {
	for _, key := range []string{RigStatusKey, RigParkedAtKey, RigParkedByKey, RigParkedReasonKey} {
		if err := wispCfg.Unset(key); err != nil {
			return fmt.Errorf("clearing %s: %w", key, err)
		}
	}
	return nil
}

func parkOwnerFromEnv() string {
	return parkOwnerOrOperator(os.Getenv("GT_ROLE"))
}

func parkOwnerOrOperator(owner string) string {
	if owner == "" {
		return RigParkedOperator
	}
	return owner
}

func shouldRefuseOperatorOwnedUnpark(callerRole, parkedBy string, operatorOverride bool) bool {
	if operatorOverride {
		return false
	}
	return callerRole != "" && parkOwnerOrOperator(parkedBy) == RigParkedOperator
}

func operatorOwnedParkRefusal(rigName, parkedAt, reason string) error {
	msg := fmt.Sprintf("rig %s was parked by the operator", rigName)
	if parkedAt != "" {
		msg += " at " + parkedAt
	}
	if reason != "" {
		msg += " (reason: " + reason + ")"
	}
	msg += "; leave a note on the relevant bead and escalate to the operator instead of unparking. The operator can unpark from a normal shell, or use 'gt rig unpark --operator " + rigName + "' when intentionally running from an agent-context shell"
	return fmt.Errorf("%s", msg)
}

func addPersistentParkedLabel(r *rig.Rig) error {
	bd, rigBeadID, err := rigIdentityBeadClient(r)
	if err != nil {
		return err
	}

	prefix := "gt"
	if r.Config != nil && r.Config.Prefix != "" {
		prefix = r.Config.Prefix
	}
	rigBead, err := bd.EnsureRigBead(r.Name, &beads.RigFields{
		Repo:   r.GitURL,
		Prefix: prefix,
		State:  beads.RigStateActive,
	})
	if err != nil {
		return err
	}

	if rigBead.ID != "" {
		rigBeadID = rigBead.ID
	}
	return bd.Update(rigBeadID, beads.UpdateOptions{
		AddLabels: []string{RigParkedLabel},
	})
}

func removePersistentParkedLabel(r *rig.Rig) error {
	bd, rigBeadID, err := rigIdentityBeadClient(r)
	if err != nil {
		return err
	}
	if _, err := bd.Show(rigBeadID); err != nil {
		return err
	}
	return bd.Update(rigBeadID, beads.UpdateOptions{
		RemoveLabels: []string{RigParkedLabel},
	})
}

func rigIdentityBeadClient(r *rig.Rig) (*beads.Beads, string, error) {
	if r == nil {
		return nil, "", fmt.Errorf("nil rig")
	}
	prefix := "gt"
	if r.Config != nil && r.Config.Prefix != "" {
		prefix = r.Config.Prefix
	}
	beadsDir := beads.ResolveBeadsDir(r.Path)
	bd := beads.NewWithBeadsDir(r.Path, beadsDir)
	return bd, beads.RigBeadIDWithPrefix(prefix, r.Name), nil
}

// IsRigParked checks if a rig is parked.
// Checks the wisp layer (ephemeral) first, then falls back to the rig
// identity bead's status:parked label (persistent). This ensures parked
// state survives wisp cleanup. (Fixes upstream #2079)
func IsRigParked(townRoot, rigName string) bool {
	// Check wisp layer first (fast, local)
	wispCfg := wisp.NewConfig(townRoot, rigName)
	if wispCfg.GetString(RigStatusKey) == RigStatusParked {
		return true
	}

	// Fall back to persistent bead label
	return hasRigBeadLabel(townRoot, rigName, "status:parked")
}
