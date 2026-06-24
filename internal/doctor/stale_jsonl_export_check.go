package doctor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// StaleJSONLExportCheck detects stale JSONL bead exports that could clobber
// live Dolt state, and non-canonical ("forbidden") rig clones that contain
// JSONL export artifacts.
//
// The canonical source of truth for beads is the Dolt server. JSONL exports are
// git-tracked backups produced by the daemon. When a regenerate/reclone writes
// a stale export (e.g., issue marked CLOSED while the live database has been
// reopened as OPEN), bd can auto-import the stale JSONL and clobber live state.
//
// This check guards two failure modes:
//  1. Canonical rig .beads directories whose issues.jsonl status diverges from
//     the live Dolt database (regression for gastown-stale-export-clobber).
//  2. Non-canonical .beads clones (worktrees, sub-clones, etc.) that contain
//     issues.jsonl or export-state.json artifacts that bd could auto-import.
type StaleJSONLExportCheck struct {
	BaseCheck
	queryIssueStatuses func(rigPath string) (map[string]string, error)
}

// NewStaleJSONLExportCheck creates a new stale JSONL export check.
func NewStaleJSONLExportCheck() *StaleJSONLExportCheck {
	return &StaleJSONLExportCheck{
		BaseCheck: BaseCheck{
			CheckName:        "stale-jsonl-export",
			CheckDescription: "Detect stale JSONL exports and forbidden non-canonical bead clones",
			CheckCategory:    CategoryConfig,
		},
		queryIssueStatuses: defaultQueryIssueStatuses,
	}
}

// jsonlIssue is the minimal shape we parse from issues.jsonl.
type jsonlIssue struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Run scans all .beads directories under the town root and reports stale or
// forbidden JSONL exports.
func (c *StaleJSONLExportCheck) Run(ctx *CheckContext) *CheckResult {
	beadsDirs, err := c.findBeadsDirs(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusWarning,
			Message:  fmt.Sprintf("Could not scan .beads directories: %v", err),
			Category: c.Category(),
		}
	}

	canonical := c.canonicalBeadsDirs(ctx.TownRoot)

	var details []string
	var found bool

	for _, dir := range beadsDirs {
		rel, _ := filepath.Rel(ctx.TownRoot, dir)
		if isCanonicalBeadsDir(dir, canonical) {
			problems, err := c.checkCanonicalDir(ctx.TownRoot, dir)
			if err != nil {
				details = append(details, fmt.Sprintf("%s: could not verify canonical export: %v", rel, err))
				found = true
				continue
			}
			if len(problems) > 0 {
				found = true
				details = append(details, problems...)
			}
			continue
		}

		problems := c.checkForbiddenClone(dir)
		if len(problems) > 0 {
			found = true
			details = append(details, problems...)
		}
	}

	if found {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusError,
			Message:  "Stale JSONL export(s) or forbidden non-canonical bead clone(s) detected",
			Details:  details,
			FixHint:  "Remove stale JSONL exports from non-canonical clones; run 'bd reopen' on clobbered beads from the canonical rig directory",
			Category: c.Category(),
		}
	}

	return &CheckResult{
		Name:     c.Name(),
		Status:   StatusOK,
		Message:  "No stale JSONL exports or forbidden bead clones detected",
		Category: c.Category(),
	}
}

// checkCanonicalDir compares the issues.jsonl export in a canonical .beads
// directory against the live Dolt database status. Returns details for each
// bead whose JSONL status differs from live state.
func (c *StaleJSONLExportCheck) checkCanonicalDir(townRoot, beadsDir string) ([]string, error) {
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if _, err := os.Stat(issuesPath); os.IsNotExist(err) {
		return nil, nil
	}

	jsonlStatuses, err := c.readJSONLStatuses(issuesPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", issuesPath, err)
	}
	if len(jsonlStatuses) == 0 {
		return nil, nil
	}

	rigPath := c.rigPathForCanonicalBeadsDir(townRoot, beadsDir)
	if rigPath == "" {
		return nil, fmt.Errorf("could not determine rig path for %s", beadsDir)
	}

	liveStatuses, err := c.queryIssueStatuses(rigPath)
	if err != nil {
		return nil, fmt.Errorf("querying live database for %s: %w", rigPath, err)
	}

	relBeads, _ := filepath.Rel(townRoot, beadsDir)
	var details []string
	for id, jsonlStatus := range jsonlStatuses {
		liveStatus, ok := liveStatuses[id]
		if !ok {
			// Bead present in JSONL but not in live DB — could be a deleted/merged
			// bead left in export. Treat as stale export artifact.
			details = append(details, fmt.Sprintf("%s: %s present in issues.jsonl (%s) but missing from live Dolt DB", relBeads, id, jsonlStatus))
			continue
		}
		if normalizeStatus(jsonlStatus) != normalizeStatus(liveStatus) {
			details = append(details, fmt.Sprintf("%s: %s status mismatch — JSONL=%s live=%s", relBeads, id, jsonlStatus, liveStatus))
		}
	}

	return details, nil
}

// checkForbiddenClone reports JSONL/export-state artifacts in a non-canonical
// .beads directory that could be auto-imported by bd.
func (c *StaleJSONLExportCheck) checkForbiddenClone(beadsDir string) []string {
	relBeads, _ := filepath.Rel(filepath.Dir(filepath.Dir(beadsDir)), beadsDir)
	_ = relBeads
	var details []string

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if info, err := os.Stat(issuesPath); err == nil && !info.IsDir() {
		details = append(details, fmt.Sprintf("%s: non-canonical .beads contains issues.jsonl export (potential stale import source)", beadsDir))
	}

	exportStateJSON := filepath.Join(beadsDir, "export-state.json")
	if info, err := os.Stat(exportStateJSON); err == nil && !info.IsDir() {
		details = append(details, fmt.Sprintf("%s: non-canonical .beads contains export-state.json (potential stale import source)", beadsDir))
	}

	exportStateDir := filepath.Join(beadsDir, "export-state")
	if info, err := os.Stat(exportStateDir); err == nil && info.IsDir() {
		details = append(details, fmt.Sprintf("%s: non-canonical .beads contains export-state/ directory (potential stale import source)", beadsDir))
	}

	return details
}

// readJSONLStatuses reads issue statuses from a JSONL file.
func (c *StaleJSONLExportCheck) readJSONLStatuses(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	statuses := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var issue jsonlIssue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			continue
		}
		if issue.ID != "" {
			statuses[issue.ID] = issue.Status
		}
	}
	return statuses, scanner.Err()
}

// defaultQueryIssueStatuses queries the live Dolt database for issue statuses.
func defaultQueryIssueStatuses(rigPath string) (map[string]string, error) {
	cmd := exec.Command("bd", "sql", "--csv", "SELECT id, status FROM issues") //nolint:gosec // G204: query is a constant
	cmd.Dir = rigPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("bd sql: %w (%s)", err, strings.TrimSpace(string(output)))
	}

	statuses := make(map[string]string)
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for i, line := range lines {
		if i == 0 {
			continue // skip CSV header
		}
		parts := strings.SplitN(line, ",", 2)
		if len(parts) != 2 {
			continue
		}
		id := strings.TrimSpace(parts[0])
		status := strings.TrimSpace(parts[1])
		if id != "" {
			statuses[id] = status
		}
	}
	return statuses, nil
}

// findBeadsDirs returns all .beads directories under the town root, excluding
// system directories.
func (c *StaleJSONLExportCheck) findBeadsDirs(townRoot string) ([]string, error) {
	var dirs []string
	err := filepath.Walk(townRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // best-effort: skip unreadable paths
		}
		if !info.IsDir() {
			return nil
		}
		if filepath.Base(path) != ".beads" {
			return nil
		}
		// Skip the town-level .beads directory? No — it is canonical and may
		// contain HQ issue exports, so include it.
		// Skip hidden runtime directories that are not rigs.
		rel, _ := filepath.Rel(townRoot, path)
		if rel == "" {
			return nil
		}
		// Skip .git and .dolt-data subtrees to avoid false positives.
		if strings.Contains(path, string(filepath.Separator)+".git"+string(filepath.Separator)) ||
			strings.Contains(path, string(filepath.Separator)+".dolt-data"+string(filepath.Separator)) {
			return filepath.SkipDir
		}
		dirs = append(dirs, path)
		// Do not descend into .beads itself.
		return filepath.SkipDir
	})
	return dirs, err
}

// canonicalBeadsDirs returns the canonical .beads directory paths for the town.
// Canonical locations are <town>/.beads, <rig>/.beads, and <rig>/mayor/rig/.beads.
func (c *StaleJSONLExportCheck) canonicalBeadsDirs(townRoot string) map[string]bool {
	canonical := map[string]bool{
		filepath.Join(townRoot, ".beads"): true,
	}

	entries, err := os.ReadDir(townRoot)
	if err != nil {
		return canonical
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == ".beads" || name == "mayor" || name == ".git" || strings.HasPrefix(name, ".") {
			continue
		}
		canonical[filepath.Join(townRoot, name, ".beads")] = true
		canonical[filepath.Join(townRoot, name, "mayor", "rig", ".beads")] = true
	}

	return canonical
}

func isCanonicalBeadsDir(dir string, canonical map[string]bool) bool {
	clean := filepath.Clean(dir)
	return canonical[clean]
}

// rigPathForCanonicalBeadsDir returns the rig root directory for a canonical
// .beads location. For <rig>/.beads it returns <rig>; for <rig>/mayor/rig/.beads
// it also returns <rig>.
func (c *StaleJSONLExportCheck) rigPathForCanonicalBeadsDir(townRoot, beadsDir string) string {
	clean := filepath.Clean(beadsDir)

	// Case 1: <rig>/mayor/rig/.beads — go up three levels from .beads.
	parent := filepath.Dir(clean)
	if filepath.Base(parent) == "rig" {
		grandparent := filepath.Dir(parent)
		if filepath.Base(grandparent) == "mayor" {
			rigPath := filepath.Dir(grandparent)
			if dirExists(rigPath) {
				return rigPath
			}
		}
	}

	// Case 2: <rig>/.beads
	rigPath := filepath.Dir(clean)
	if dirExists(rigPath) {
		return rigPath
	}

	return ""
}

func normalizeStatus(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
