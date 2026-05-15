package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDeprecatedMergeQueueKeysCheck_Clean(t *testing.T) {
	townRoot := setupTownWithSettings(t, map[string]interface{}{
		"merge_queue": map[string]interface{}{
			"enabled":     true,
			"on_conflict": "assign_back",
		},
	})

	check := NewDeprecatedMergeQueueKeysCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK for clean config, got %v: %s", result.Status, result.Message)
	}
}

func TestDeprecatedMergeQueueKeysCheck_DetectsTargetBranch(t *testing.T) {
	townRoot := setupTownWithSettings(t, map[string]interface{}{
		"merge_queue": map[string]interface{}{
			"enabled":       true,
			"target_branch": "develop",
		},
	})

	check := NewDeprecatedMergeQueueKeysCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning for deprecated target_branch, got %v: %s", result.Status, result.Message)
	}
}

func TestDeprecatedMergeQueueKeysCheck_DetectsIntegrationBranches(t *testing.T) {
	townRoot := setupTownWithSettings(t, map[string]interface{}{
		"merge_queue": map[string]interface{}{
			"enabled":              true,
			"integration_branches": true,
		},
	})

	check := NewDeprecatedMergeQueueKeysCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning for deprecated integration_branches, got %v: %s", result.Status, result.Message)
	}
}

func TestDeprecatedMergeQueueKeysCheck_DetectsBothKeys(t *testing.T) {
	townRoot := setupTownWithSettings(t, map[string]interface{}{
		"merge_queue": map[string]interface{}{
			"enabled":              true,
			"target_branch":        "develop",
			"integration_branches": true,
		},
	})

	check := NewDeprecatedMergeQueueKeysCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning, got %v: %s", result.Status, result.Message)
	}
	// Should mention both keys in details
	if len(result.Details) < 2 {
		t.Errorf("expected at least 2 detail lines, got %d: %v", len(result.Details), result.Details)
	}
}

func TestDeprecatedMergeQueueKeysCheck_NoMergeQueue(t *testing.T) {
	townRoot := setupTownWithSettings(t, map[string]interface{}{
		"type":    "rig-settings",
		"version": 1,
	})

	check := NewDeprecatedMergeQueueKeysCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when no merge_queue section, got %v: %s", result.Status, result.Message)
	}
}

func TestDeprecatedMergeQueueKeysCheck_NoSettingsFile(t *testing.T) {
	townRoot := setupTownMinimal(t)

	check := NewDeprecatedMergeQueueKeysCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when no settings file, got %v: %s", result.Status, result.Message)
	}
}

func TestDeprecatedMergeQueueKeysCheck_Fix(t *testing.T) {
	townRoot := setupTownWithSettings(t, map[string]interface{}{
		"type":    "rig-settings",
		"version": 1,
		"merge_queue": map[string]interface{}{
			"enabled":              true,
			"target_branch":        "develop",
			"integration_branches": true,
			"run_tests":            true,
			"test_command":         "go test ./...",
		},
	})

	check := NewDeprecatedMergeQueueKeysCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	// Run first to detect
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning before fix, got %v", result.Status)
	}

	// Fix should remove deprecated keys
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() error: %v", err)
	}

	// Re-run should pass
	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("expected StatusOK after fix, got %v: %s", result.Status, result.Message)
	}

	// Verify non-deprecated keys are preserved
	settingsPath := filepath.Join(findAllRigs(townRoot)[0], "settings", "config.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading fixed file: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parsing fixed file: %v", err)
	}

	var mq map[string]json.RawMessage
	if err := json.Unmarshal(raw["merge_queue"], &mq); err != nil {
		t.Fatalf("parsing fixed merge_queue: %v", err)
	}

	// Deprecated keys should be gone
	if _, ok := mq["target_branch"]; ok {
		t.Error("target_branch should have been removed by Fix()")
	}
	if _, ok := mq["integration_branches"]; ok {
		t.Error("integration_branches should have been removed by Fix()")
	}

	// Non-deprecated keys should remain
	if _, ok := mq["enabled"]; !ok {
		t.Error("enabled should be preserved after Fix()")
	}
	if _, ok := mq["run_tests"]; !ok {
		t.Error("run_tests should be preserved after Fix()")
	}
	if _, ok := mq["test_command"]; !ok {
		t.Error("test_command should be preserved after Fix()")
	}
}

func TestDeprecatedMergeQueueKeysCheck_FixMigratesSettingsValues(t *testing.T) {
	townRoot := setupTownWithSettings(t, map[string]interface{}{
		"type":    "rig-settings",
		"version": 1,
		"merge_queue": map[string]interface{}{
			"target_branch":        "develop",
			"integration_branches": false,
		},
	})
	rigPath := findAllRigs(townRoot)[0]
	writeJSONFile(t, filepath.Join(rigPath, "config.json"), map[string]interface{}{
		"type":    "rig",
		"version": 1,
		"name":    "testrig",
	})

	check := NewDeprecatedMergeQueueKeysCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	if result := check.Run(ctx); result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning before fix, got %v", result.Status)
	}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() error: %v", err)
	}

	root := readJSONFile(t, filepath.Join(rigPath, "config.json"))
	if got := jsonString(t, root["default_branch"]); got != "develop" {
		t.Fatalf("default_branch = %q, want develop", got)
	}
	settings := readJSONFile(t, filepath.Join(rigPath, "settings", "config.json"))
	mq := jsonMap(t, settings["merge_queue"])
	if _, ok := mq["target_branch"]; ok {
		t.Fatal("target_branch should be removed")
	}
	if _, ok := mq["integration_branches"]; ok {
		t.Fatal("integration_branches should be removed")
	}
	if got := jsonBool(t, mq["integration_branch_polecat_enabled"]); got {
		t.Fatal("integration_branch_polecat_enabled = true, want false")
	}
	if got := jsonBool(t, mq["integration_branch_refinery_enabled"]); got {
		t.Fatal("integration_branch_refinery_enabled = true, want false")
	}
	if result := check.Run(ctx); result.Status != StatusOK {
		t.Fatalf("expected StatusOK after fix, got %v: %s", result.Status, result.Message)
	}
}

func TestDeprecatedMergeQueueKeysCheck_DetectsAndFixesRootConfig(t *testing.T) {
	townRoot := setupTownMinimal(t)
	rigPath := findAllRigs(townRoot)[0]
	writeJSONFile(t, filepath.Join(rigPath, "config.json"), map[string]interface{}{
		"type":    "rig",
		"version": 1,
		"name":    "testrig",
		"merge_queue": map[string]interface{}{
			"target_branch":        "release",
			"integration_branches": false,
		},
	})

	check := NewDeprecatedMergeQueueKeysCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	if result := check.Run(ctx); result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning before fix, got %v", result.Status)
	}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() error: %v", err)
	}

	root := readJSONFile(t, filepath.Join(rigPath, "config.json"))
	if got := jsonString(t, root["default_branch"]); got != "release" {
		t.Fatalf("default_branch = %q, want release", got)
	}
	if _, ok := root["merge_queue"]; ok {
		t.Fatal("empty deprecated merge_queue should be removed from root config")
	}
	settings := readJSONFile(t, filepath.Join(rigPath, "settings", "config.json"))
	mq := jsonMap(t, settings["merge_queue"])
	if got := jsonBool(t, mq["integration_branch_polecat_enabled"]); got {
		t.Fatal("integration_branch_polecat_enabled = true, want false")
	}
	if got := jsonBool(t, mq["integration_branch_refinery_enabled"]); got {
		t.Fatal("integration_branch_refinery_enabled = true, want false")
	}
}

func TestDeprecatedMergeQueueKeysCheck_MultiRig(t *testing.T) {
	townRoot := t.TempDir()

	// Rig 1: clean config (no deprecated keys)
	createRigWithSettings(t, townRoot, "cleanrig", map[string]interface{}{
		"merge_queue": map[string]interface{}{
			"enabled": true,
		},
	})

	// Rig 2: has deprecated keys
	createRigWithSettings(t, townRoot, "dirtyrig", map[string]interface{}{
		"merge_queue": map[string]interface{}{
			"enabled":              true,
			"target_branch":        "develop",
			"integration_branches": true,
		},
	})

	check := NewDeprecatedMergeQueueKeysCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning, got %v: %s", result.Status, result.Message)
	}
	// Should report 1 affected rig, not 2
	if want := "Found deprecated merge_queue keys in 1 rig(s)"; result.Message != want {
		t.Errorf("message = %q, want %q", result.Message, want)
	}
	if len(result.Details) != 2 {
		t.Errorf("expected 2 detail lines (one per deprecated key), got %d: %v", len(result.Details), result.Details)
	}
}

// setupTownWithSettings creates a minimal town with one rig that has the given settings.
func setupTownWithSettings(t *testing.T, settings map[string]interface{}) string {
	t.Helper()
	townRoot := t.TempDir()

	// Create a rig directory with a marker so findAllRigs finds it
	rigPath := filepath.Join(townRoot, "testrig")
	if err := os.MkdirAll(filepath.Join(rigPath, "crew"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create settings directory and config
	settingsDir := filepath.Join(rigPath, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	return townRoot
}

// setupTownMinimal creates a minimal town with one rig but no settings file.
func setupTownMinimal(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()

	rigPath := filepath.Join(townRoot, "testrig")
	if err := os.MkdirAll(filepath.Join(rigPath, "crew"), 0o755); err != nil {
		t.Fatal(err)
	}

	return townRoot
}

// createRigWithSettings creates a named rig under townRoot with the given settings.
func createRigWithSettings(t *testing.T, townRoot, rigName string, settings map[string]interface{}) {
	t.Helper()
	rigPath := filepath.Join(townRoot, rigName)
	if err := os.MkdirAll(filepath.Join(rigPath, "crew"), 0o755); err != nil {
		t.Fatal(err)
	}
	settingsDir := filepath.Join(rigPath, "settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeJSONFile(t *testing.T, path string, value map[string]interface{}) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readJSONFile(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func jsonMap(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func jsonString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var result string
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func jsonBool(t *testing.T, raw json.RawMessage) bool {
	t.Helper()
	var result bool
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
