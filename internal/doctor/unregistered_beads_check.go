package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/steveyegge/gastown/internal/doltserver"
)

// UnregisteredBeadsDirsCheck detects directories in the town root that have
// .beads/metadata.json files pointing to Dolt databases but aren't registered
// in rigs.json. These orphan directories cause phantom database creation on
// the Dolt server whenever any bd command probes them.
//
// Also checks the deacon's beads config for database mismatches — the deacon
// should use the same database as the town-level beads (hq).
type UnregisteredBeadsDirsCheck struct {
	BaseCheck
}

// NewUnregisteredBeadsDirsCheck creates a new unregistered beads dirs check.
func NewUnregisteredBeadsDirsCheck() *UnregisteredBeadsDirsCheck {
	return &UnregisteredBeadsDirsCheck{
		BaseCheck: BaseCheck{
			CheckName:        "unregistered-beads-dirs",
			CheckDescription: "Detect directories with beads metadata that aren't registered rigs",
			CheckCategory:    CategoryCleanup,
		},
	}
}

// knownSystemDirs are directories at town root that are expected to exist
// without being registered in rigs.json.
var knownSystemDirs = map[string]bool{
	"mayor":     true,
	"deacon":    true,
	".beads":    true,
	".dolt-data": true,
	".runtime":  true,
	".git":      true,
	".github":   true,
}

// knownLegacyStoreDirs returns the set of top-level directories that hold
// .beads metadata for intentionally-retained shared-server databases. They are
// not rigs and should not be flagged as unregistered. The canonical list is
// maintained in internal/doltserver/doltserver.go:protectedSharedServerDatabases.
func knownLegacyStoreDirs() map[string]bool {
	dirs := make(map[string]bool)
	for _, name := range doltserver.ProtectedSharedServerDatabaseNames() {
		dirs[name] = true
	}
	return dirs
}

// Run checks for unregistered directories with beads metadata.
func (c *UnregisteredBeadsDirsCheck) Run(ctx *CheckContext) *CheckResult {
	// Load registered rig names from rigs.json
	registeredRigs := loadRegisteredRigNames(ctx.TownRoot)

	// Read town-level database name for deacon mismatch detection
	townDB := readDoltDatabase(filepath.Join(ctx.TownRoot, ".beads"))

	var details []string
	legacyDirs := knownLegacyStoreDirs()

	// Scan town root for directories with .beads/metadata.json
	entries, err := os.ReadDir(ctx.TownRoot)
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Could not read town root directory",
			Details: []string{err.Error()},
		}
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()

		// Skip known system dirs, legacy empty stores, and registered rigs
		if knownSystemDirs[name] || legacyDirs[name] || registeredRigs[name] {
			continue
		}

		// Check if this directory has .beads/metadata.json
		db := readDoltDatabase(filepath.Join(ctx.TownRoot, name, ".beads"))
		if db != "" {
			details = append(details, fmt.Sprintf(
				"%s/ has .beads/metadata.json pointing to database %q (not a registered rig)",
				name, db))
		}
	}

	// Check deacon database mismatch
	if townDB != "" {
		deaconDB := readDoltDatabase(filepath.Join(ctx.TownRoot, "deacon", ".beads"))
		if deaconDB != "" && deaconDB != townDB {
			details = append(details, fmt.Sprintf(
				"deacon/.beads/metadata.json points to %q but town beads uses %q",
				deaconDB, townDB))
		}
	}

	if len(details) > 0 {
		sort.Strings(details)
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d unregistered directory(ies) with beads metadata", len(details)),
			Details: details,
			FixHint: "Remove stale directories or register them as rigs with 'gt rig add'",
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "No unregistered beads directories found",
	}
}

// loadRegisteredRigNames reads rig names from mayor/rigs.json.
func loadRegisteredRigNames(townRoot string) map[string]bool {
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	data, err := os.ReadFile(rigsPath)
	if err != nil {
		return nil
	}
	var config struct {
		Rigs map[string]json.RawMessage `json:"rigs"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}
	names := make(map[string]bool, len(config.Rigs))
	for name := range config.Rigs {
		names[name] = true
	}
	return names
}

// readDoltDatabase reads the dolt_database field from a .beads/metadata.json.
// Returns empty string if the file doesn't exist or can't be parsed.
func readDoltDatabase(beadsDir string) string {
	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		return ""
	}
	var meta struct {
		DoltDatabase string `json:"dolt_database"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return ""
	}
	return meta.DoltDatabase
}
