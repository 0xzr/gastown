package cmd

import (
	"strings"
	"testing"
)

func TestValidateBeadContentForFormula(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		description string
		formula     string
		wantErr     bool
		errSubstr   string
	}{
		{
			name:        "code task under mol-polecat-work is allowed",
			title:       "Fix authentication bug",
			description: "Update the login handler in internal/auth to validate tokens before use.",
			formula:     "mol-polecat-work",
			wantErr:     false,
		},
		{
			name:        "deacon patrol bead rejected from mol-polecat-work",
			title:       "Loop or exit for respawn",
			description: "Run gt deacon heartbeat and gt patrol report --steps. Await signal via gt mol step await-signal --agent-bead hq-deacon. Fresh Deacon session handoff.",
			formula:     "mol-polecat-work",
			wantErr:     true,
			errSubstr:   "bead references deacon",
		},
		{
			name:        "deacon session keyword rejected",
			title:       "Respawn decision",
			description: "Start a fresh Deacon session and run the 26-step patrol formula.",
			formula:     "mol-polecat-work",
			wantErr:     true,
			errSubstr:   "signal: \"deacon session\"",
		},
		{
			name:        "case insensitive matching",
			title:       "DEACON patrol",
			description: "Run GT PATROL report.",
			formula:     "mol-polecat-work",
			wantErr:     true,
			errSubstr:   "bead references deacon",
		},
		{
			name:        "mentions deacon generically are allowed in code task",
			title:       "Telemetry for deacon health",
			description: "Expose a metric that records whether the deacon subsystem is healthy. No deacon daemon commands needed.",
			formula:     "mol-polecat-work",
			wantErr:     false,
		},
		{
			name:        "unknown formula is skipped",
			title:       "Loop or exit for respawn",
			description: "Run gt deacon heartbeat.",
			formula:     "mol-unknown-formula",
			wantErr:     false,
		},
		{
			name:        "empty formula is skipped",
			title:       "Loop or exit for respawn",
			description: "Run gt deacon heartbeat.",
			formula:     "",
			wantErr:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBeadContentForFormula(tc.title, tc.description, tc.formula)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateBeadContentForFormula(%q, %q, %q) = nil, want error containing %q",
					tc.title, tc.description, tc.formula, tc.errSubstr)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateBeadContentForFormula(%q, %q, %q) = %v, want nil",
					tc.title, tc.description, tc.formula, err)
			}
			if tc.wantErr && err != nil && tc.errSubstr != "" {
				if !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("error = %q, want it to contain %q", err.Error(), tc.errSubstr)
				}
			}
		})
	}
}

func TestDetectConflictingRole_NoFalsePositiveOnOwnRole(t *testing.T) {
	// A polecat task that mentions polecat-specific words should not be flagged
	// just because we don't have polecat indicators.
	role, signal, found := detectConflictingRole(
		"Implement feature",
		"Refactor internal/cmd for polecat dispatch.",
		"polecat",
	)
	if found {
		t.Fatalf("expected no conflict, found role=%q signal=%q", role, signal)
	}
}
