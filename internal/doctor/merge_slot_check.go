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
// slot bead. The value must match internal/beads/beads_merge_slot.go.
//
// mergeSlotBeadTitle is the exact title identifying the singleton. Mirroring
// production, the slot is the unique OPEN bead carrying both the label and
// this exact title; other beads (closed tombstones, wrong-title labels,
// non-merge-slot beads) are NOT the slot and must never be repaired by this
// check. Production getMergeSlotBead uses List then filters on
// issue.Title == mergeSlotTitle before Show — we mirror that here.
const (
	mergeSlotBeadLabel = "gt:merge-slot"
	mergeSlotBeadTitle = "merge-slot"
)

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
//
// Detection scope mirrors production: open bead with label gt:merge-slot AND
// exact title "merge-slot" (read via Show because List output may truncate
// Description). Closed beads with the label are tombstones; beads with the
// label but a different title are unrelated and must be skipped.
//
// Failure mode: if bd.List fails for a rig, the check fails CLOSED — it
// cannot verify that rig's slot and reports an error rather than silently
// declaring everything clean. The same applies if bd.Show fails for the
// singleton candidate.
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

// Run scans each rig's merge-slot bead (label=gt:merge-slot + exact
// title="merge-slot") and reports any whose Description cannot be
// unmarshalled. A missing slot is fine (the slot is lazily created on
// first acquisition). A parseable slot is fine. Any rig that could not
// be queried is reported as a failure of THIS check — fail closed.
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
	unverified := 0 // rigs whose slot we could not confirm clean

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
			// Fail closed: a List failure means we cannot confirm the slot
			// is healthy. Treat the rig as unverified and report — never
			// silently count it as "checked clean".
			problems = append(problems, fmt.Sprintf(
				"%s: could not list merge-slot bead: %v", rigName, err))
			unverified++
			continue
		}

		checked++

		// Mirror production getMergeSlotBead: filter to beads carrying
		// the slot's exact title. Skip closed tombstones (Status != "open")
		// and beads that have the label but a different title — those are
		// unrelated beads the production lookup would also ignore.
		var slotMatches []*beads.Issue
		for _, issue := range issues {
			if !strings.EqualFold(issue.Status, "open") {
				continue
			}
			if issue.Title != mergeSlotBeadTitle {
				// Wrong-title labeled bead: production would skip this; we
				// must too, otherwise --fix would rewrite an unrelated bead
				// because the rig happens to carry the gt:merge-slot label.
				continue
			}
			slotMatches = append(slotMatches, issue)
		}

		switch len(slotMatches) {
		case 0:
			// No active merge-slot for this rig (lazy creation: the slot
			// is created on first acquisition). Not a corruption case.
			continue

		case 1:
			// Unique candidate: Show to retrieve the full Description
			// (List output may be truncated). Re-verify the title from
			// Show before parsing, mirroring production.
			candidate := slotMatches[0]
			full, err := bd.Show(candidate.ID)
			if err != nil {
				problems = append(problems, fmt.Sprintf(
					"%s: could not show merge-slot bead %s: %v",
					rigName, candidate.ID, err))
				unverified++
				continue
			}
			if full.Title != mergeSlotBeadTitle {
				// Title drifted between List and Show (raced with another
				// writer). Treat as unverified rather than guess at the
				// slot's identity.
				problems = append(problems, fmt.Sprintf(
					"%s: merge-slot candidate %s changed title during Show (got %q, want %q)",
					rigName, candidate.ID, full.Title, mergeSlotBeadTitle))
				unverified++
				continue
			}

			if _, err := parseMergeSlotDescription(full.Description); err != nil {
				problems = append(problems, fmt.Sprintf(
					"%s: merge-slot bead %s has corrupt Description: %v",
					rigName, full.ID, err))
				c.affectedSlots = append(c.affectedSlots, mergeSlotIntegrityAffected{
					rigName:  rigName,
					rigPath:  rigDir,
					slotID:   full.ID,
					parseErr: err.Error(),
				})
			}

		default:
			// Multiple open beads with the slot's label AND title is an
			// ambiguous-singleton state (mirrors production's
			// "ambiguous merge slot beads" error). Flag for operator
			// resolution; do NOT auto-repair — picking either candidate
			// for rewriting would be a guess.
			ids := make([]string, 0, len(slotMatches))
			for _, m := range slotMatches {
				ids = append(ids, m.ID)
			}
			problems = append(problems, fmt.Sprintf(
				"%s: ambiguous merge-slot state — %d open beads with title=%q and label=%q (%s); must be resolved manually",
				rigName, len(slotMatches), mergeSlotBeadTitle, mergeSlotBeadLabel, strings.Join(ids, ", ")))
			// Fail closed: we cannot uniquely identify the slot, so we
			// cannot confirm it is healthy. Count as unverified so the
			// post-loop block returns StatusError.
			unverified++
		}
	}

	// Fail closed: any unverified rig means we cannot guarantee the slot
	// is healthy. Even if no actual corruption was found, return error so
	// the operator sees the unrecoverable scan result.
	if unverified > 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusError,
			Message:  fmt.Sprintf("%d rig(s) could not be verified; %d rig(s) have corrupt merge-slot Description", unverified, len(c.affectedSlots)),
			Details:  problems,
			FixHint:  "Investigate rig beads database health (gt dolt status); rerun after recovery",
			Category: c.Category(),
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
// Fix re-checks the singleton candidate via Show + title re-verification
// (mirroring production) before any Update, so we never rewrite a slot that
// is no longer corrupt AND we never rewrite a bead whose identity has
// changed since Run.
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
		// If Run found corruption, affectedSlots is now populated.
	}

	if len(c.affectedSlots) == 0 {
		return nil
	}

	for _, slot := range c.affectedSlots {
		// Last-line-of-defence guard: only fix slots whose current state is
		// actually corrupt. If anything has changed between Run and Fix (an
		// operator manually repaired, a parallel writer fixed it, etc.), we
		// skip rather than overwrite a slot we no longer know to be bad.
		//
		// Re-verify the slot is still the singleton via production's
		// title + label + Show semantics before any update.
		bd := beads.New(slot.rigPath)
		current, err := c.resolveMergeSlotSingleton(bd, slot.slotID)
		if err != nil {
			return fmt.Errorf("%s: verifying slot %s before repair: %w",
				slot.rigName, slot.slotID, err)
		}
		if current == nil {
			fmt.Fprintf(os.Stderr,
				"merge-slot-integrity fix: %s: slot %s no longer resolves as singleton (skipped)\n",
				slot.rigName, slot.slotID)
			continue
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
		if err := bd.Update(current.ID, beads.UpdateOptions{Description: &desc}); err != nil {
			return fmt.Errorf("%s: rewriting slot %s: %w",
				slot.rigName, slot.slotID, err)
		}
	}

	return nil
}

// resolveMergeSlotSingleton returns the singleton merge-slot bead (label +
// exact title, open) via Show — or nil if no singleton exists, or if the
// candidate ID does not match the current singleton. This mirrors the
// production getMergeSlotBead lookup and is the only safe way to obtain the
// full (non-truncated) Description for repair.
func (c *mergeSlotIntegrityCheck) resolveMergeSlotSingleton(bd *beads.Beads, expectedID string) (*beads.Issue, error) {
	issues, err := bd.List(beads.ListOptions{Label: mergeSlotBeadLabel})
	if err != nil {
		return nil, fmt.Errorf("listing merge slot candidates: %w", err)
	}
	var matches []*beads.Issue
	for _, issue := range issues {
		if !strings.EqualFold(issue.Status, "open") {
			continue
		}
		if issue.Title != mergeSlotBeadTitle {
			continue
		}
		matches = append(matches, issue)
	}
	if len(matches) != 1 {
		// Either the singleton vanished or the rig now has duplicates.
		// Either way, do not auto-repair.
		return nil, nil
	}
	if matches[0].ID != expectedID {
		// The candidate ID we recorded at Run is no longer the singleton.
		return nil, nil
	}
	full, err := bd.Show(matches[0].ID)
	if err != nil {
		return nil, fmt.Errorf("showing merge slot %s: %w", matches[0].ID, err)
	}
	if full.Title != mergeSlotBeadTitle {
		// Title drifted between List and Show.
		return nil, nil
	}
	return full, nil
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