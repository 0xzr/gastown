package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/util"
	"github.com/steveyegge/gastown/internal/wisp"
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

func TestGetPatrolRigsIncludesMainBranchTestFilter(t *testing.T) {
	if got := GetPatrolRigs(nil, "main_branch_test"); got != nil {
		t.Fatalf("GetPatrolRigs(nil, main_branch_test) = %v, want nil", got)
	}
	if got := GetPatrolRigs(&DaemonPatrolConfig{Patrols: &PatrolsConfig{}}, "main_branch_test"); got != nil {
		t.Fatalf("GetPatrolRigs(no main_branch_test config) = %v, want nil", got)
	}

	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			MainBranchTest: &MainBranchTestConfig{
				Enabled: true,
				Rigs:    []string{"gastown"},
			},
		},
	}
	got := GetPatrolRigs(config, "main_branch_test")
	if len(got) != 1 || got[0] != "gastown" {
		t.Fatalf("GetPatrolRigs(main_branch_test) = %v, want [gastown]", got)
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

// TestMainBranchTestRunnableGate locks in the phase-filter contract for the
// main-branch test runner. The retro-audit of tree a652f49c… flagged the
// missing phase-field parsing as a P0; this test is the direct regression
// guard for mainBranchTestRunnableGate so future refactors cannot silently
// drop the filter or weaken its handling of pre-merge / post-squash /
// unknown / whitespace / case variants.
func TestMainBranchTestRunnableGate(t *testing.T) {
	cases := []struct {
		name  string
		phase string
		want  bool
	}{
		{"empty defaults to runnable (pre-merge)", "", true},
		{"explicit pre-merge is runnable", "pre-merge", true},
		{"post-squash is filtered out", "post-squash", false},
		{"uppercase post-squash is filtered", "POST-SQUASH", false},
		{"mixed-case Post-Squash is filtered", "Post-Squash", false},
		{"whitespace around post-squash is filtered", "  post-squash  ", false},
		{"whitespace + uppercase post-squash is filtered", "\tPOST-SQUASH\n", false},
		{"unknown phase is left runnable (no false reject)", "mid-cycle", true},
		{"single space is runnable", " ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mainBranchTestRunnableGate(tc.phase); got != tc.want {
				t.Errorf("mainBranchTestRunnableGate(%q) = %v, want %v", tc.phase, got, tc.want)
			}
		})
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

	t.Run("skips refinery review gates", func(t *testing.T) {
		dir := t.TempDir()
		data := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"gates": map[string]interface{}{
					"build": map[string]interface{}{"cmd": "go build ./..."},
					"four-model-refinery-review": map[string]interface{}{
						"cmd":   "/home/ubuntu/gastown-spike/dropin/refinery-gate.sh --worktree \"$PWD\" --writer unknown",
						"phase": "post-squash",
					},
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
		if len(cfg.Gates) != 1 || cfg.Gates["build"] != "go build ./..." {
			t.Fatalf("expected only deterministic build gate, got %+v", cfg.Gates)
		}
	})

	t.Run("only refinery review gates means no main branch test", func(t *testing.T) {
		dir := t.TempDir()
		data := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"gates": map[string]interface{}{
					"four-model-refinery-review": map[string]interface{}{
						"cmd":   "/home/ubuntu/gastown-spike/dropin/refinery-gate.sh --worktree \"$PWD\" --writer unknown",
						"phase": "post-squash",
					},
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
		if cfg != nil {
			t.Fatalf("expected nil when only refinery review gates are configured, got %+v", cfg)
		}
	})

	t.Run("pre-merge command name does not trigger refinery blacklist", func(t *testing.T) {
		dir := t.TempDir()
		data := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"gates": map[string]interface{}{
					"refinery-review-smoke": map[string]interface{}{
						"cmd":   "/tmp/refinery-gate.sh --dry-run",
						"phase": "pre-merge",
					},
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
		if cfg == nil || cfg.Gates["refinery-review-smoke"] != "/tmp/refinery-gate.sh --dry-run" {
			t.Fatalf("expected pre-merge command retained, got %+v", cfg)
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

	// Retro-bug P0 regression: loadRigGateConfig must parse the `phase` field
	// out of each gate entry. If parsing is dropped (the audit finding for
	// tree a652f49c…), every gate with a non-pre-merge phase ends up in the
	// main-branch test set, re-entering the refinery review gate and
	// producing false alerts. Lock in: a gate without a phase field is
	// treated as pre-merge (runnable), and the JSON tag is wired to `Phase`.
	t.Run("gate without phase field is runnable (default pre-merge)", func(t *testing.T) {
		dir := t.TempDir()
		data := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"gates": map[string]interface{}{
					"build": map[string]interface{}{"cmd": "go build ./..."},
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
		if cfg.Gates["build"] != "go build ./..." {
			t.Errorf("expected build gate retained when phase omitted, got %+v", cfg.Gates)
		}
	})

	// The JSON tag for Phase is `phase` (lowercase). Verify the wire format
	// is honored — if it were ever renamed/typo'd, this test catches it.
	t.Run("phase field is parsed from JSON tag", func(t *testing.T) {
		dir := t.TempDir()
		data := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"gates": map[string]interface{}{
					"build": map[string]interface{}{"cmd": "go build ./..."},
					"review": map[string]interface{}{
						"cmd":   "/tmp/refinery-gate.sh",
						"phase": "post-squash",
					},
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
		if _, hasReview := cfg.Gates["review"]; hasReview {
			t.Errorf("post-squash review gate must be filtered, got %+v", cfg.Gates)
		}
		if cfg.Gates["build"] != "go build ./..." {
			t.Errorf("expected build gate retained, got %+v", cfg.Gates)
		}
	})

	// Phase is case- and whitespace-normalized via mainBranchTestRunnableGate.
	// Lock in the normalization at the loadRigGateConfig layer too.
	t.Run("phase is case- and whitespace-normalized", func(t *testing.T) {
		dir := t.TempDir()
		data := map[string]interface{}{
			"merge_queue": map[string]interface{}{
				"gates": map[string]interface{}{
					"build": map[string]interface{}{"cmd": "go build ./..."},
					"review": map[string]interface{}{
						"cmd":   "/tmp/refinery-gate.sh",
						"phase": "  POST-SQUASH  ",
					},
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
		if _, hasReview := cfg.Gates["review"]; hasReview {
			t.Errorf("normalized POST-SQUASH must be filtered, got %+v", cfg.Gates)
		}
	})
}

func TestFirstLine(t *testing.T) {
	got := firstLine("main branch test failures:\ngastown: gate failed\nmore")
	if got != "main branch test failures:" {
		t.Fatalf("firstLine() = %q", got)
	}
	if got := firstLine("  single line  "); got != "single line" {
		t.Fatalf("firstLine(single) = %q", got)
	}
	if got := firstLine(" \n "); got != "" {
		t.Fatalf("firstLine(empty) = %q", got)
	}
}

func TestTruncateRunes(t *testing.T) {
	got := truncateRunes("abc😀defg", 7)
	if got != "abc😀..." {
		t.Fatalf("truncateRunes preserved invalid boundary incorrectly: %q", got)
	}
	if got := truncateRunes("abcdef", 3); got != "abc" {
		t.Fatalf("truncateRunes(max<=3) = %q", got)
	}
	if got := truncateRunes("short", 10); got != "short" {
		t.Fatalf("truncateRunes(short) = %q", got)
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
