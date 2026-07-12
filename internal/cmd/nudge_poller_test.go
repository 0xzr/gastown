package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestShouldSkipDrainUntilIdle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		hasPromptDetection bool
		waitErr            error
		want               bool
	}{
		{"prompt aware idle", true, nil, false},
		{"prompt aware busy", true, errors.New("timeout"), true},
		{"no prompt detection busy", false, errors.New("timeout"), false},
		{"no prompt detection idle", false, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldSkipDrainUntilIdle(tt.hasPromptDetection, tt.waitErr); got != tt.want {
				t.Errorf("shouldSkipDrainUntilIdle(%v, %v) = %v, want %v", tt.hasPromptDetection, tt.waitErr, got, tt.want)
			}
		})
	}
}

func TestActiveRefineryGate(t *testing.T) {
	t.Parallel()
	town := t.TempDir()
	gateDir := filepath.Join(town, ".runtime", "gate-running")
	if err := os.MkdirAll(gateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if activeRefineryGate(town, "polybot-refinery") {
		t.Fatal("missing pidfile reported an active gate")
	}
	if err := os.WriteFile(filepath.Join(gateDir, "polybot.pid"), []byte(fmt.Sprintf("%d 1\n", os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	if !activeRefineryGate(town, "polybot-refinery") {
		t.Fatal("live owner-gate pid was not detected")
	}
	if activeRefineryGate(town, "polybot-witness") {
		t.Fatal("non-refinery session was blocked by gate marker")
	}
	if err := os.WriteFile(filepath.Join(gateDir, "polybot.pid"), []byte("999999999 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if activeRefineryGate(town, "polybot-refinery") {
		t.Fatal("dead gate pid reported active")
	}
}
