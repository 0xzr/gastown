package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/wisp"
)

func TestIsRigParked_WhenParked(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"

	// Set up wisp config with parked status
	configDir := filepath.Join(townRoot, wisp.WispConfigDir, wisp.ConfigSubdir)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("failed to create wisp config dir: %v", err)
	}

	configFile := filepath.Join(configDir, rigName+".json")
	data, _ := json.Marshal(wisp.ConfigFile{
		Rig:    rigName,
		Values: map[string]interface{}{"status": "parked"},
	})
	if err := os.WriteFile(configFile, data, 0o644); err != nil {
		t.Fatalf("failed to write wisp config: %v", err)
	}

	if !IsRigParked(townRoot, rigName) {
		t.Error("expected IsRigParked to return true for parked rig")
	}
}

func TestIsRigParked_WhenNotParked(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"

	// No wisp config at all — should not be parked
	if IsRigParked(townRoot, rigName) {
		t.Error("expected IsRigParked to return false when no wisp config exists")
	}
}

func TestIsRigParked_WhenUnparked(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"

	// Set up wisp config with empty status (unparked)
	configDir := filepath.Join(townRoot, wisp.WispConfigDir, wisp.ConfigSubdir)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("failed to create wisp config dir: %v", err)
	}

	configFile := filepath.Join(configDir, rigName+".json")
	data, _ := json.Marshal(wisp.ConfigFile{
		Rig:    rigName,
		Values: map[string]interface{}{},
	})
	if err := os.WriteFile(configFile, data, 0o644); err != nil {
		t.Fatalf("failed to write wisp config: %v", err)
	}

	if IsRigParked(townRoot, rigName) {
		t.Error("expected IsRigParked to return false for unparked rig")
	}
}

func TestIsRigParked_WhenDocked(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"

	// Wisp config with docked status — IsRigParked should return false
	// (docked is a separate check via IsRigDocked)
	configDir := filepath.Join(townRoot, wisp.WispConfigDir, wisp.ConfigSubdir)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("failed to create wisp config dir: %v", err)
	}

	configFile := filepath.Join(configDir, rigName+".json")
	data, _ := json.Marshal(wisp.ConfigFile{
		Rig:    rigName,
		Values: map[string]interface{}{"status": "docked"},
	})
	if err := os.WriteFile(configFile, data, 0o644); err != nil {
		t.Fatalf("failed to write wisp config: %v", err)
	}

	if IsRigParked(townRoot, rigName) {
		t.Error("expected IsRigParked to return false for docked rig (not parked)")
	}
}

func TestRigStatusConstants(t *testing.T) {
	if RigStatusKey != "status" {
		t.Errorf("expected RigStatusKey to be 'status', got %q", RigStatusKey)
	}
	if RigStatusParked != "parked" {
		t.Errorf("expected RigStatusParked to be 'parked', got %q", RigStatusParked)
	}
	if RigDockedLabel != "status:docked" {
		t.Errorf("expected RigDockedLabel to be 'status:docked', got %q", RigDockedLabel)
	}
	if RigParkedLabel != "status:parked" {
		t.Errorf("expected RigParkedLabel to be 'status:parked', got %q", RigParkedLabel)
	}
}

func TestSetParkedWispStateWritesMetadata(t *testing.T) {
	t.Setenv("GT_ROLE", "mayor")

	townRoot := t.TempDir()
	rigName := "testrig"
	wispCfg := wisp.NewConfig(townRoot, rigName)
	parkedAt := time.Now().UTC().Format(time.RFC3339)
	reason := "free capacity for another rig"

	if err := setParkedWispState(wispCfg, parkedAt, parkOwnerFromEnv(), reason); err != nil {
		t.Fatalf("set parked wisp state: %v", err)
	}

	if got := wispCfg.GetString(RigStatusKey); got != RigStatusParked {
		t.Errorf("status = %q, want %q", got, RigStatusParked)
	}
	if got := wispCfg.GetString(RigParkedByKey); got != "mayor" {
		t.Errorf("parked_by = %q, want mayor", got)
	}
	if got := wispCfg.GetString(RigParkedReasonKey); got != reason {
		t.Errorf("parked_reason = %q, want %q", got, reason)
	}
	gotAt := wispCfg.GetString(RigParkedAtKey)
	if gotAt == "" {
		t.Fatal("parked_at was not written")
	}
	if _, err := time.Parse(time.RFC3339, gotAt); err != nil {
		t.Fatalf("parked_at is not RFC3339: %q: %v", gotAt, err)
	}
}

func TestParkOwnerFromEnvDefaultsToOperator(t *testing.T) {
	t.Setenv("GT_ROLE", "")

	if got := parkOwnerFromEnv(); got != RigParkedOperator {
		t.Fatalf("parkOwnerFromEnv() = %q, want %q", got, RigParkedOperator)
	}
}

func TestParkUnparkOwnershipGate(t *testing.T) {
	t.Run("agent refused for operator-owned park", func(t *testing.T) {
		t.Setenv("GT_ROLE", "mayor")

		if !shouldRefuseOperatorOwnedUnpark(os.Getenv("GT_ROLE"), RigParkedOperator, false) {
			t.Fatal("expected agent to be refused for operator-owned park")
		}

		err := operatorOwnedParkRefusal("gastown", "2026-07-04T12:00:00Z", "operator maintenance")
		if err == nil {
			t.Fatal("expected refusal error")
		}
		msg := err.Error()
		for _, want := range []string{
			"parked by the operator",
			"2026-07-04T12:00:00Z",
			"operator maintenance",
			"leave a note on the relevant bead",
			"escalate to the operator",
			"normal shell",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("refusal message missing %q: %s", want, msg)
			}
		}
	})

	t.Run("agent allowed for agent-owned park", func(t *testing.T) {
		t.Setenv("GT_ROLE", "mayor")

		if shouldRefuseOperatorOwnedUnpark(os.Getenv("GT_ROLE"), "witness", false) {
			t.Fatal("expected agent-owned park to be allowed")
		}
	})

	t.Run("normal shell allowed for operator-owned park", func(t *testing.T) {
		t.Setenv("GT_ROLE", "")

		if shouldRefuseOperatorOwnedUnpark(os.Getenv("GT_ROLE"), RigParkedOperator, false) {
			t.Fatal("expected normal shell to be allowed")
		}
	})

	t.Run("operator override allows agent-context shell", func(t *testing.T) {
		t.Setenv("GT_ROLE", "mayor")

		if shouldRefuseOperatorOwnedUnpark(os.Getenv("GT_ROLE"), RigParkedOperator, true) {
			t.Fatal("expected --operator override to allow unpark")
		}
	})

	t.Run("missing parked_by is operator-owned", func(t *testing.T) {
		t.Setenv("GT_ROLE", "mayor")

		if !shouldRefuseOperatorOwnedUnpark(os.Getenv("GT_ROLE"), "", false) {
			t.Fatal("expected missing parked_by to be treated as operator-owned")
		}
	})
}

func TestClearParkedWispStateClearsAllParkKeys(t *testing.T) {
	t.Setenv("GT_ROLE", "")

	townRoot := t.TempDir()
	rigName := "testrig"
	wispCfg := wisp.NewConfig(townRoot, rigName)
	if err := setParkedWispState(wispCfg, time.Now().UTC().Format(time.RFC3339), RigParkedOperator, "done"); err != nil {
		t.Fatalf("set parked wisp state: %v", err)
	}
	if shouldRefuseOperatorOwnedUnpark(os.Getenv("GT_ROLE"), wispCfg.GetString(RigParkedByKey), false) {
		t.Fatal("expected normal shell to be allowed to unpark")
	}

	if err := clearParkedWispState(wispCfg); err != nil {
		t.Fatalf("clear parked wisp state: %v", err)
	}

	for _, key := range []string{RigStatusKey, RigParkedAtKey, RigParkedByKey, RigParkedReasonKey} {
		if got := wispCfg.GetString(key); got != "" {
			t.Errorf("%s = %q, want empty", key, got)
		}
	}
}
