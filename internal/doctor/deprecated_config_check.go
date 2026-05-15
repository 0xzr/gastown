package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/config"
)

// DeprecatedMergeQueueKeysCheck detects stale deprecated keys in merge_queue config.
// These keys are silently ignored by json.Unmarshal, so rigs with them may have
// config values that appear set but have no effect.
type DeprecatedMergeQueueKeysCheck struct {
	FixableCheck
	// affectedFiles maps settings file path → list of deprecated keys found
	affectedFiles map[string][]string
}

// NewDeprecatedMergeQueueKeysCheck creates a new deprecated merge queue keys check.
func NewDeprecatedMergeQueueKeysCheck() *DeprecatedMergeQueueKeysCheck {
	return &DeprecatedMergeQueueKeysCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "deprecated-merge-queue-keys",
				CheckDescription: "Check for deprecated keys in merge_queue config",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// Run scans all rigs for deprecated merge_queue keys in settings/config.json
// and rig-root config.json. Runtime code ignores these keys, so doctor must
// either migrate or remove them to make the workspace quiet and unambiguous.
func (c *DeprecatedMergeQueueKeysCheck) Run(ctx *CheckContext) *CheckResult {
	rigs := findAllRigs(ctx.TownRoot)
	if len(rigs) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No rigs found",
		}
	}

	c.affectedFiles = make(map[string][]string)
	var details []string

	for _, rigPath := range rigs {
		for _, configPath := range []string{
			filepath.Join(rigPath, "settings", "config.json"),
			filepath.Join(rigPath, "config.json"),
		} {
			found := findDeprecatedKeys(configPath)
			if len(found) == 0 {
				continue
			}
			c.affectedFiles[configPath] = found
			rigName := filepath.Base(rigPath)
			for _, key := range found {
				details = append(details, fmt.Sprintf("%s/%s: merge_queue.%s is deprecated and has no effect", rigName, configDisplayPath(configPath), key))
			}
		}
	}

	if len(c.affectedFiles) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("No deprecated merge_queue keys in %d rig(s)", len(rigs)),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("Found deprecated merge_queue keys in %d rig(s)", len(c.affectedFiles)),
		Details: details,
		FixHint: "Run 'gt doctor --fix' to migrate/remove deprecated merge_queue keys",
	}
}

// Fix migrates safe deprecated values and removes deprecated keys from all affected files.
func (c *DeprecatedMergeQueueKeysCheck) Fix(ctx *CheckContext) error {
	for configPath, keys := range c.affectedFiles {
		if err := migrateDeprecatedKeys(configPath, keys); err != nil {
			return fmt.Errorf("fixing %s: %w", configPath, err)
		}
	}
	// Clear cache so re-run picks up fixed state
	c.affectedFiles = nil
	return nil
}

// findDeprecatedKeys reads a settings file and returns any deprecated merge_queue keys found.
func findDeprecatedKeys(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var raw struct {
		MergeQueue map[string]json.RawMessage `json:"merge_queue"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || raw.MergeQueue == nil {
		return nil
	}

	var found []string
	for _, key := range config.DeprecatedMergeQueueKeys {
		if _, ok := raw.MergeQueue[key]; ok {
			found = append(found, key)
		}
	}
	return found
}

// migrateDeprecatedKeys reads a config file, migrates safe deprecated values,
// removes deprecated keys from merge_queue, and writes it back preserving other fields.
func migrateDeprecatedKeys(path string, keys []string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// Parse into generic structure to preserve all other fields
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parsing settings: %w", err)
	}

	mqRaw, ok := settings["merge_queue"]
	if !ok {
		return nil // Nothing to fix
	}

	var mq map[string]json.RawMessage
	if err := json.Unmarshal(mqRaw, &mq); err != nil {
		return fmt.Errorf("parsing merge_queue: %w", err)
	}
	rigRoot := rigRootForConfig(path)

	// target_branch migrated to rig-root default_branch when default_branch is absent.
	if targetRaw, ok := mq["target_branch"]; ok {
		var targetBranch string
		if err := json.Unmarshal(targetRaw, &targetBranch); err == nil && targetBranch != "" {
			if isRootConfig(path) {
				if _, hasDefault := settings["default_branch"]; !hasDefault {
					settings["default_branch"] = json.RawMessage(jsonQuote(targetBranch))
				}
			} else if err := setRootDefaultBranchIfAbsent(filepath.Join(rigRoot, "config.json"), targetBranch); err != nil {
				return err
			}
		}
	}

	// integration_branches migrated to the two replacement booleans when absent.
	if integrationRaw, ok := mq["integration_branches"]; ok {
		var enabled bool
		if err := json.Unmarshal(integrationRaw, &enabled); err == nil {
			if isRootConfig(path) {
				if err := setSettingsIntegrationFlagsIfAbsent(filepath.Join(rigRoot, "settings", "config.json"), enabled); err != nil {
					return err
				}
			} else {
				if _, has := mq["integration_branch_polecat_enabled"]; !has {
					mq["integration_branch_polecat_enabled"] = json.RawMessage(fmt.Sprintf("%t", enabled))
				}
				if _, has := mq["integration_branch_refinery_enabled"]; !has {
					mq["integration_branch_refinery_enabled"] = json.RawMessage(fmt.Sprintf("%t", enabled))
				}
			}
		}
	}

	for _, key := range keys {
		delete(mq, key)
	}
	if len(mq) == 0 {
		delete(settings, "merge_queue")
	} else {
		// Re-marshal merge_queue back into settings
		mqData, err := json.Marshal(mq)
		if err != nil {
			return fmt.Errorf("marshaling merge_queue: %w", err)
		}
		settings["merge_queue"] = mqData
	}

	// Write back with indentation
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling settings: %w", err)
	}

	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func isRootConfig(path string) bool {
	return filepath.Base(path) == "config.json" && filepath.Base(filepath.Dir(path)) != "settings"
}

func rigRootForConfig(path string) string {
	if isRootConfig(path) {
		return filepath.Dir(path)
	}
	return filepath.Dir(filepath.Dir(path))
}

func configDisplayPath(path string) string {
	if isRootConfig(path) {
		return "config.json"
	}
	return filepath.Join("settings", "config.json")
}

func setRootDefaultBranchIfAbsent(path, targetBranch string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parsing root config: %w", err)
	}
	if _, hasDefault := root["default_branch"]; hasDefault {
		return nil
	}
	root["default_branch"] = json.RawMessage(jsonQuote(targetBranch))
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling root config: %w", err)
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func setSettingsIntegrationFlagsIfAbsent(path string, enabled bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			data = []byte(`{}`)
		} else {
			return err
		}
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parsing settings config: %w", err)
	}
	var mq map[string]json.RawMessage
	if raw, ok := settings["merge_queue"]; ok {
		if err := json.Unmarshal(raw, &mq); err != nil {
			return fmt.Errorf("parsing settings merge_queue: %w", err)
		}
	} else {
		mq = make(map[string]json.RawMessage)
	}
	if _, has := mq["integration_branch_polecat_enabled"]; !has {
		mq["integration_branch_polecat_enabled"] = json.RawMessage(fmt.Sprintf("%t", enabled))
	}
	if _, has := mq["integration_branch_refinery_enabled"]; !has {
		mq["integration_branch_refinery_enabled"] = json.RawMessage(fmt.Sprintf("%t", enabled))
	}
	mqData, err := json.Marshal(mq)
	if err != nil {
		return fmt.Errorf("marshaling settings merge_queue: %w", err)
	}
	settings["merge_queue"] = mqData
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling settings config: %w", err)
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func jsonQuote(s string) []byte {
	quoted, _ := json.Marshal(s)
	return quoted
}
