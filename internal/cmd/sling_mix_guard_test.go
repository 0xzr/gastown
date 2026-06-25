package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

// TestValidateExplicitAgentForMixCaps covers the four behavioral states of
// the gastown-cet.16.2 guard:
//
//  1. Caps inactive (no caps configured) → no check, regardless of agent.
//  2. Caps active but require_explicit_agent=false → no check (advisory mode).
//  3. Caps active + flag + empty agent → ErrNoExplicitAgent.
//  4. Caps active + flag + agent in caps → pass.
//  5. Caps active + flag + agent NOT in caps → ErrAgentNotInMixCaps.
func TestValidateExplicitAgentForMixCaps(t *testing.T) {
	capsActive := &capacity.SchedulerConfig{
		ModelMixCaps: map[string]int{
			"umans-glm":  2,
			"umans-kimi": 2,
			"m3":         2,
		},
		RequireExplicitAgent: true,
	}

	capsPresentButFlagOff := &capacity.SchedulerConfig{
		ModelMixCaps: map[string]int{
			"umans-glm": 2,
		},
		// RequireExplicitAgent deliberately false: caps are informational only.
	}

	tests := []struct {
		name      string
		agent     string
		cfg       *capacity.SchedulerConfig
		wantErr   error
		errSubstr string
	}{
		{
			name:    "no caps configured, empty agent passes",
			agent:   "",
			cfg:     &capacity.SchedulerConfig{},
			wantErr: nil,
		},
		{
			name:    "nil config passes (caller layer must guard)",
			agent:   "",
			cfg:     nil,
			wantErr: nil,
		},
		{
			name:    "caps present but flag off, empty agent passes",
			agent:   "",
			cfg:     capsPresentButFlagOff,
			wantErr: nil,
		},
		{
			name:    "caps present but flag off, agent set passes",
			agent:   "umans-glm",
			cfg:     capsPresentButFlagOff,
			wantErr: nil,
		},
		{
			name:      "caps active and flag on, empty agent refuses",
			agent:     "",
			cfg:       capsActive,
			wantErr:   ErrNoExplicitAgent,
			errSubstr: "no explicit model assignment",
		},
		{
			name:      "caps active and flag on, whitespace agent refuses",
			agent:     "   ",
			cfg:       capsActive,
			wantErr:   ErrNoExplicitAgent,
			errSubstr: "no explicit model assignment",
		},
		{
			name:    "caps active and flag on, allowed agent passes",
			agent:   "umans-kimi",
			cfg:     capsActive,
			wantErr: nil,
		},
		{
			name:      "caps active and flag on, agent not in caps refuses",
			agent:     "codex",
			cfg:       capsActive,
			wantErr:   ErrAgentNotInMixCaps,
			errSubstr: `agent="codex"`,
		},
		{
			name:      "caps active and flag on, agent not in caps mentions allowed list",
			agent:     "codex",
			cfg:       capsActive,
			wantErr:   ErrAgentNotInMixCaps,
			errSubstr: "umans-glm",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateExplicitAgentForMixCaps(tc.agent, tc.cfg)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidateExplicitAgentForMixCaps(%q, %+v) = %v, want nil",
						tc.agent, tc.cfg, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateExplicitAgentForMixCaps(%q, %+v) = nil, want error wrapping %v",
					tc.agent, tc.cfg, tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateExplicitAgentForMixCaps(%q, %+v) = %v, want error wrapping %v",
					tc.agent, tc.cfg, err, tc.wantErr)
			}
			if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.errSubstr)
			}
		})
	}
}

// TestSchedulerConfig_MixCapsHelpers verifies the IsModelMixCapped /
// GetModelMixCaps / GetRequireExplicitAgent helpers stay in sync with the
// raw fields and handle nil safely.
func TestSchedulerConfig_MixCapsHelpers(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		var c *capacity.SchedulerConfig
		if c.IsModelMixCapped() {
			t.Error("nil config should not be model-mix capped")
		}
		if c.GetRequireExplicitAgent() {
			t.Error("nil config should not require explicit agent")
		}
		if got := c.GetModelMixCaps(); len(got) != 0 {
			t.Errorf("nil config caps = %v, want empty", got)
		}
	})

	t.Run("zero-value config", func(t *testing.T) {
		c := &capacity.SchedulerConfig{}
		if c.IsModelMixCapped() {
			t.Error("zero-value config should not be model-mix capped")
		}
		if c.GetRequireExplicitAgent() {
			t.Error("zero-value config should not require explicit agent")
		}
	})

	t.Run("caps set, flag unset", func(t *testing.T) {
		c := &capacity.SchedulerConfig{
			ModelMixCaps: map[string]int{"umans-glm": 2},
		}
		if !c.IsModelMixCapped() {
			t.Error("expected capped")
		}
		if c.GetRequireExplicitAgent() {
			t.Error("expected flag off")
		}
		if got := c.GetModelMixCaps(); got["umans-glm"] != 2 {
			t.Errorf("caps = %v, want umans-glm=2", got)
		}
	})

	t.Run("both set", func(t *testing.T) {
		c := &capacity.SchedulerConfig{
			ModelMixCaps:         map[string]int{"umans-glm": 2},
			RequireExplicitAgent: true,
		}
		if !c.IsModelMixCapped() {
			t.Error("expected capped")
		}
		if !c.GetRequireExplicitAgent() {
			t.Error("expected flag on")
		}
	})
}

// TestValidateExplicitAgentForMixCaps_SixLaneFleetScenario reproduces the
// gastown-cet.16.2 acceptance scenario: a six-polecat pool configured with
// caps umans-glm=2, umans-kimi=2, m3=2 must never silently dispatch a queued
// bead without an explicit allowed model assignment. The original incident
// was a 5 Kimi / 1 M3 drift caused by native scheduler run draining the
// queue through Claude Code's default Kimi path.
func TestValidateExplicitAgentForMixCaps_SixLaneFleetScenario(t *testing.T) {
	cfg := &capacity.SchedulerConfig{
		ModelMixCaps: map[string]int{
			"umans-glm":  2,
			"umans-kimi": 2,
			"m3":         2,
		},
		RequireExplicitAgent: true,
	}

	// Simulate the 5 Kimi + 1 M3 drift: a queue of beads, none of which
	// carries an explicit agent, must be refused wholesale.
	queued := []string{"", "", "", "", "", ""} // 6 queued beads, no agent
	for i, agent := range queued {
		err := ValidateExplicitAgentForMixCaps(agent, cfg)
		if !errors.Is(err, ErrNoExplicitAgent) {
			t.Errorf("bead %d (agent=%q): got %v, want ErrNoExplicitAgent", i, agent, err)
		}
	}

	// The maintainer's fix path: re-sling with --agent=<allowed-model>.
	allowedAgents := []string{"umans-glm", "umans-kimi", "m3"}
	for _, agent := range allowedAgents {
		if err := ValidateExplicitAgentForMixCaps(agent, cfg); err != nil {
			t.Errorf("allowed agent %q was rejected: %v", agent, err)
		}
	}

	// Anything else is still refused under caps.
	for _, agent := range []string{"codex", "claude-default", "random"} {
		err := ValidateExplicitAgentForMixCaps(agent, cfg)
		if !errors.Is(err, ErrAgentNotInMixCaps) {
			t.Errorf("agent %q: got %v, want ErrAgentNotInMixCaps", agent, err)
		}
	}
}
