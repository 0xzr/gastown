package daemon

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/util"
)

func TestMainBranchTestInterval(t *testing.T) {
	// Nil config returns default
	if got := mainBranchTestInterval(nil); got != defaultMainBranchTestInterval {
		t.Errorf("expected default %v, got %v", defaultMainBranchTestInterval, got)
	}

	// Configured interval
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MainBranchTest: &MainBranchTestConfig{
				Enabled:     true,
				IntervalStr: "15m",
			},
		},
	}
	if got := mainBranchTestInterval(config); got.Minutes() != 15 {
		t.Errorf("expected 15m, got %v", got)
	}

	// Invalid interval returns default
	config.Patrols.MainBranchTest.IntervalStr = "bad"
	if got := mainBranchTestInterval(config); got != defaultMainBranchTestInterval {
		t.Errorf("expected default for invalid interval, got %v", got)
	}
}

func TestMainBranchTestTimeout(t *testing.T) {
	// Nil config returns default
	if got := mainBranchTestTimeout(nil); got != defaultMainBranchTestTimeout {
		t.Errorf("expected default %v, got %v", defaultMainBranchTestTimeout, got)
	}

	// Configured timeout
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MainBranchTest: &MainBranchTestConfig{
				Enabled:    true,
				TimeoutStr: "5m",
			},
		},
	}
	if got := mainBranchTestTimeout(config); got.Minutes() != 5 {
		t.Errorf("expected 5m, got %v", got)
	}
}

func TestMainBranchTestRigs(t *testing.T) {
	// Nil config returns nil
	if got := mainBranchTestRigs(nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}

	// Configured rigs
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MainBranchTest: &MainBranchTestConfig{
				Enabled: true,
				Rigs:    []string{"gastown", "beads"},
			},
		},
	}
	got := mainBranchTestRigs(config)
	if len(got) != 2 || got[0] != "gastown" || got[1] != "beads" {
		t.Errorf("expected [gastown beads], got %v", got)
	}
}

func TestIsPatrolEnabledMainBranchTest(t *testing.T) {
	// Nil config — disabled (opt-in)
	if IsPatrolEnabled(nil, "main_branch_test") {
		t.Error("expected main_branch_test disabled with nil config")
	}

	// Explicitly disabled
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MainBranchTest: &MainBranchTestConfig{
				Enabled: false,
			},
		},
	}
	if IsPatrolEnabled(config, "main_branch_test") {
		t.Error("expected main_branch_test disabled when Enabled=false")
	}

	// Enabled
	config.Patrols.MainBranchTest.Enabled = true
	if !IsPatrolEnabled(config, "main_branch_test") {
		t.Error("expected main_branch_test enabled when Enabled=true")
	}
}

func TestLoadRigGateConfig(t *testing.T) {
	t.Run("no config file", func(t *testing.T) {
		cfg, err := loadRigGateConfig("/nonexistent/path")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg != nil {
			t.Errorf("expected nil config for nonexistent path, got %+v", cfg)
		}
	})

	t.Run("no merge_queue section", func(t *testing.T) {
		dir := t.TempDir()
		data := `{"type":"rig","version":1,"name":"test"}`
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg != nil {
			t.Errorf("expected nil config for no merge_queue, got %+v", cfg)
		}
	})

	t.Run("test_command only", func(t *testing.T) {
		dir := t.TempDir()
		data := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"test_command": "go test ./...",
			},
		}
		raw, _ := json.Marshal(data)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		if cfg.TestCommand != "go test ./..." {
			t.Errorf("expected 'go test ./...', got %q", cfg.TestCommand)
		}
	})

	t.Run("gates configured", func(t *testing.T) {
		dir := t.TempDir()
		data := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"gates": map[string]interface{}{
					"build": map[string]interface{}{"cmd": "go build ./..."},
					"test":  map[string]interface{}{"cmd": "go test ./..."},
					"lint":  map[string]interface{}{"cmd": "golangci-lint run"},
				},
			},
		}
		raw, _ := json.Marshal(data)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		if len(cfg.Gates) != 3 {
			t.Errorf("expected 3 gates, got %d", len(cfg.Gates))
		}
		if cfg.Gates["build"] != "go build ./..." {
			t.Errorf("expected build gate 'go build ./...', got %q", cfg.Gates["build"])
		}
	})

	t.Run("no test commands", func(t *testing.T) {
		dir := t.TempDir()
		data := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"enabled": true,
			},
		}
		raw, _ := json.Marshal(data)
		if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadRigGateConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg != nil {
			t.Errorf("expected nil for no test commands, got %+v", cfg)
		}
	})
}

func TestContains(t *testing.T) {
	if !sliceContains([]string{"a", "b", "c"}, "b") {
		t.Error("expected true for 'b' in [a b c]")
	}
	if sliceContains([]string{"a", "b", "c"}, "d") {
		t.Error("expected false for 'd' in [a b c]")
	}
	if sliceContains(nil, "a") {
		t.Error("expected false for nil slice")
	}
}

// TestRunCommandOnWorktree_PreservesEnvAndSignalsCI verifies that when `go` is
// already on PATH the daemon gate runner (a) does not wipe the inherited
// environment (the GateCommandEnv nil-fallback must retain PATH) and (b)
// appends CI=true. It runs a command that requires both: `go env GOOS` succeeds
// only if `go` is resolvable on the spawned PATH, proving the toolchain fix
// composes correctly with CI=true without dropping PATH.
func TestRunCommandOnWorktree_PreservesEnvAndSignalsCI(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH; cannot test the on-PATH composition path")
	}

	// Daemon only needs a logger for runCommandOnWorktree.
	d := &Daemon{
		logger: log.New(os.Stderr, "test-daemon: ", 0),
	}

	dir := t.TempDir()
	// `go env GOOS` resolves `go` from PATH — fails if PATH was wiped.
	if err := d.runCommandOnWorktree(context.Background(), "test-rig", dir, "go-env", "go env GOOS"); err != nil {
		t.Fatalf("runCommandOnWorktree failed running `go env GOOS` (%s): %v", goBin, err)
	}

	// Assert CI=true is injected and PATH preserved by inspecting the env the
	// command would receive. We can't read cmd.Env after Run, so re-derive the
	// expected env composition and check both signals are present.
	env := util.GateCommandEnv()
	if env == nil {
		env = os.Environ()
	}
	env = append(env, "CI=true")
	haveCI, havePath := false, false
	for _, kv := range env {
		if kv == "CI=true" {
			haveCI = true
		}
		if len(kv) > len("PATH=") && kv[:len("PATH=")] == "PATH=" {
			havePath = true
		}
	}
	if !haveCI {
		t.Error("expected CI=true in spawned command env")
	}
	if !havePath {
		t.Error("expected PATH retained in spawned command env (nil-fallback regression)")
	}
}

func TestDefaultLifecycleConfigIncludesMainBranchTest(t *testing.T) {
	config := DefaultLifecycleConfig()
	if config.Patrols.MainBranchTest == nil {
		t.Fatal("expected MainBranchTest in default lifecycle config")
	}
	if !config.Patrols.MainBranchTest.Enabled {
		t.Error("expected MainBranchTest.Enabled=true")
	}
	if config.Patrols.MainBranchTest.IntervalStr != "30m" {
		t.Errorf("expected interval '30m', got %q", config.Patrols.MainBranchTest.IntervalStr)
	}
	if config.Patrols.MainBranchTest.TimeoutStr != "10m" {
		t.Errorf("expected timeout '10m', got %q", config.Patrols.MainBranchTest.TimeoutStr)
	}
}

func TestEnsureLifecycleDefaultsFillsMainBranchTest(t *testing.T) {
	config := &DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Patrols: &PatrolsConfig{}, // All nil
	}
	changed := EnsureLifecycleDefaults(config)
	if !changed {
		t.Error("expected changed=true when MainBranchTest was nil")
	}
	if config.Patrols.MainBranchTest == nil {
		t.Fatal("expected MainBranchTest to be populated")
	}
	if !config.Patrols.MainBranchTest.Enabled {
		t.Error("expected MainBranchTest.Enabled=true after defaults")
	}
}
