package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
)

// mergeSlotBeadLabel is the rig-scoped label identifying the singleton merge
// slot bead. Duplicated here (instead of imported from the beads package) so
// the doctor package stays free of cross-package constants; the value must
// match internal/beads/beads_merge_slot.go.
const mergeSlotBeadLabel = "gt:merge-slot"

// mergeSlotEmptyDescription is the canonical empty merge-slot payload. After
// gastown-p61un a corrupt Description cannot be safely self-healed from a
// known-good initial value (it's a torn-write-corruption state), but we CAN
// replace the corrupted blob with this empty shell. The slot then reports as
// available and any subsequent Acquire creates a fresh, consistent state.
// The previous (unparseable) holder/waiter information is lost — this is the
// documented "recreate the slot bead fresh" recovery path.
const mergeSlotEmptyDescription = `{"holder":""}`

// mergeSlotIntegrityCheck detects rigs whose merge-slot bead's Description
// cannot be parsed as JSON (torn-write corruption or other damage to a
// serialization primitive). After gastown-p61un, MergeSlotAcquire/Release
// refuse to act on such a slot — the safe behavior, since a blind
// auto-recovery from unknown bytes risks reintroducing a stale "available"
// state when the real intent was "held". The trade-off is that recovery
// becomes manual.
//
// This check reports the corrupt state per rig and provides a Fix that
// rewrites the slot's Description to an empty shell ({"holder":""}). The
// slot is then unambiguously available. Any holder or waiters lost to the
// corruption are unrecoverable — the corrupted bytes cannot be trusted as
// state — but the rig's merge queue continues to function.
//
// Why rewrite-in-place rather than close+create a new bead? The merge slot
// is a per-rig singleton; recreating leaves an orphaned (closed) slot bead
// with the same label, complicating lookups. Rewriting preserves the bead
// ID while restoring a known-good payload.
type mergeSlotIntegrityCheck struct {
	FixableCheck
	// affectedSlots is populated by Run and consumed by Fix. Holding this
	// state on the check (rather than via the CheckResult) lets Fix operate
	// without re-shelling out to find the same beads a second time.
	affectedSlots []mergeSlotIntegrityAffected
}

type mergeSlotIntegrityAffected struct {
	rigName  string
	rigPath  string
	slotID   string
	parseErr string
}

// NewMergeSlotIntegrityCheck constructs a fresh check.
func NewMergeSlotIntegrityCheck() *mergeSlotIntegrityCheck {
	return &mergeSlotIntegrityCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "merge-slot-integrity",
				CheckDescription: "Detect rigs whose merge-slot bead has a corrupt Description",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// Run scans each rig's merge-slot bead (label=gt:merge-slot) and reports any
// whose Description cannot be unmarshalled. A missing slot is fine (the slot
// is lazily created on first acquisition). A parseable slot is fine.
func (c *mergeSlotIntegrityCheck) Run(ctx *CheckContext) *CheckResult {
	c.affectedSlots = nil // Reset so each Run is independent

	rigDirs := c.findRigDirectories(ctx)

	if len(rigDirs) == 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "No rigs to check",
			Category: c.Category(),
		}
	}

	var problems []string
	checked := 0

	for _, rigDir := range rigDirs {
		rigName := filepath.Base(rigDir)

		// Skip rigs without a .beads directory; their merge slot does not
		// exist and there is nothing to corrupt.
		beadsDir := filepath.Join(rigDir, ".beads")
		if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
			continue
		}

		bd := beads.New(rigDir)
		issues, err := bd.List(beads.ListOptions{Label: mergeSlotBeadLabel})
		if err != nil {
			// Listing failed (Dolt hiccup or unreadable DB). Don't treat
			// that as corruption — surface a warning and skip.
			problems = append(problems, fmt.Sprintf("%s: could not list merge-slot bead: %v", rigName, err))
			continue
		}

		checked++

		var seenSlot bool
		for _, issue := range issues {
			// Closed slot beads (left over from a previous close+recreate
			// recovery or manual operator action) are irrelevant — only the
			// currently-active open slot is the corruption target.
			if !strings.EqualFold(issue.Status, "open") {
				continue
			}
			if seenSlot {
				// Multiple open slots with the gt:merge-slot label is itself
				// a data hygiene problem; flag it for operator attention
				// (close+recreate is the manual fix).
				problems = append(problems, fmt.Sprintf(
					"%s: multiple open merge-slot beads (id=%s + others); duplicate-label state must be resolved manually",
					rigName, issue.ID))
				continue
			}
			seenSlot = true

			// Try to parse the description.
			if _, err := parseMergeSlotDescription(issue.Description); err != nil {
				problems = append(problems, fmt.Sprintf(
					"%s: merge-slot bead %s has corrupt Description: %v",
					rigName, issue.ID, err))
				c.affectedSlots = append(c.affectedSlots, mergeSlotIntegrityAffected{
					rigName:  rigName,
					rigPath:  rigDir,
					slotID:   issue.ID,
					parseErr: err.Error(),
				})
			}
		}
	}

	if len(c.affectedSlots) == 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  fmt.Sprintf("All %d rig merge-slot beads parse cleanly", checked),
			Category: c.Category(),
		}
	}

	return &CheckResult{
		Name:     c.Name(),
		Status:   StatusError,
		Message:  fmt.Sprintf("%d rig(s) have corrupt merge-slot Description", len(c.affectedSlots)),
		Details:  problems,
		FixHint:  "Run 'gt doctor --fix' to rewrite corrupt slot Descriptions to a valid empty payload",
		Category: c.Category(),
	}
}

// Fix rewrites each corrupt slot Description to the canonical empty payload.
// It refuses to touch slots that are not currently flagged as corrupt (even
// if Run was not called or did not detect them) — re-running Run first is
// the safe path.
//
// Logs each rewrite loudly to stderr so silent loss of holder state during
// recovery is operator-visible.
func (c *mergeSlotIntegrityCheck) Fix(ctx *CheckContext) error {
	// Re-run check to populate affectedSlots if a previous Run did not
	// populate state (e.g. a fresh Doctor after edits).
	if len(c.affectedSlots) == 0 {
		result := c.Run(ctx)
		if result.Status == StatusOK {
			return nil
		}
	}

	if len(c.affectedSlots) == 0 {
		return nil
	}

	for _, slot := range c.affectedSlots {
		// Last-line-of-defence guard: only fix slots whose current state is
		// actually corrupt. If anything has changed between Run and Fix (an
		// operator manually repaired, a parallel writer fixed it, etc.), we
		// skip rather than overwrite a slot we no longer know to be bad.
		bd := beads.New(slot.rigPath)
		current, err := bd.Show(slot.slotID)
		if err != nil {
			return fmt.Errorf("%s: verifying slot %s before repair: %w",
				slot.rigName, slot.slotID, err)
		}
		if _, err := parseMergeSlotDescription(current.Description); err == nil {
			// Slot no longer corrupt — operator repaired it manually.
			fmt.Fprintf(os.Stderr,
				"merge-slot-integrity fix: %s: slot %s is no longer corrupt (skipped)\n",
				slot.rigName, slot.slotID)
			continue
		}

		fmt.Fprintf(os.Stderr,
			"WARNING: merge-slot-integrity fix: %s: rewriting slot %s Description to empty shell (corruption=%s)\n",
			slot.rigName, slot.slotID, slot.parseErr)

		desc := mergeSlotEmptyDescription
		if err := bd.Update(slot.slotID, beads.UpdateOptions{Description: &desc}); err != nil {
			return fmt.Errorf("%s: rewriting slot %s: %w",
				slot.rigName, slot.slotID, err)
		}
	}

	return nil
}

// parseMergeSlotDescription decodes a merge-slot Description into the
// internal shape. Returns an error when the description is present but not
// valid JSON, mirroring parseMergeSlotData in internal/beads. We duplicate
// the helper (rather than exporting it from the beads package) so the doctor
// package stays dependency-light and the corruption check runs even if the
// beads package changes its internal representation.
func parseMergeSlotDescription(desc string) (struct {
	Holder  string   `json:"holder"`
	Waiters []string `json:"waiters,omitempty"`
}, error) {
	if desc == "" {
		// An empty description is valid: it represents the empty/default
		// merge slot. (Not corrupt, just minimal.)
		return struct {
			Holder  string   `json:"holder"`
			Waiters []string `json:"waiters,omitempty"`
		}{}, nil
	}
	var data struct {
		Holder  string   `json:"holder"`
		Waiters []string `json:"waiters,omitempty"`
	}
	if err := json.Unmarshal([]byte(desc), &data); err != nil {
		return struct {
			Holder  string   `json:"holder"`
			Waiters []string `json:"waiters,omitempty"`
		}{}, fmt.Errorf("parsing merge slot data: %w", err)
	}
	return data, nil
}

// findRigDirectories returns the set of rig directories the check should
// scan. Combines the rigs.json registry, routes.jsonl registration, and a
// filesystem sweep for unregistered-but-populated rig directories. When
// --rig is specified the check focuses on that single rig only.
//
// Modeled on findRigDirectories in rig_routes_jsonl_check.go to keep the
// "which rigs exist?" determination consistent across doctor checks.
func (c *mergeSlotIntegrityCheck) findRigDirectories(ctx *CheckContext) []string {
	// Single-rig mode: only inspect that rig's directory.
	if ctx.RigName != "" {
		rigPath := filepath.Join(ctx.TownRoot, ctx.RigName)
		if _, err := os.Stat(rigPath); err == nil {
			return []string{rigPath}
		}
		return nil
	}

	var rigDirs []string
	seen := make(map[string]bool)

	addRig := func(path string) {
		if seen[path] {
			return
		}
		if _, err := os.Stat(path); err == nil {
			rigDirs = append(rigDirs, path)
			seen[path] = true
		}
	}

	// Source 1: rigs.json registry (canonical rig list)
	rigsPath := filepath.Join(ctx.TownRoot, "mayor", "rigs.json")
	if rigsConfig, err := config.LoadRigsConfig(rigsPath); err == nil {
		for rigName := range rigsConfig.Rigs {
			addRig(filepath.Join(ctx.TownRoot, rigName))
		}
	}

	// Source 2: routes.jsonl (catches rigs registered via beads routing but
	// missing from rigs.json — should be rare but possible during partial
	// setup or recovery).
	townBeadsDir := filepath.Join(ctx.TownRoot, ".beads")
	if routes, err := beads.LoadRoutes(townBeadsDir); err == nil {
		for _, route := range routes {
			if route.Path == "." || route.Path == "" {
				continue
			}
			parts := strings.Split(route.Path, "/")
			if len(parts) > 0 && parts[0] != "" {
				addRig(filepath.Join(ctx.TownRoot, parts[0]))
			}
		}
	}

	// Source 3: directories with .beads subdirs (unregistered rigs). Skip
	// well-known non-rig directories to avoid noise.
	skipDirs := map[string]bool{
		"mayor":    true,
		"deacon":   true,
		"daemon":   true,
		".beads":   true,
		"witness":  true, // town-level witness lives outside any rig
		"refinery": true, // refinery is a town-level utility role
	}
	if entries, err := os.ReadDir(ctx.TownRoot); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if skipDirs[name] {
				continue
			}
			if strings.HasPrefix(name, ".") {
				continue
			}
			addRig(filepath.Join(ctx.TownRoot, name))
		}
	}

	return rigDirs
}
