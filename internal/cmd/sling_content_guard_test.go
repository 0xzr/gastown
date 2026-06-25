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
		{
			// gastown-wat: a bare "gt patrol" substring previously caused
			// any polecat code task mentioning "gt patrol ..." to be
			// wrongly refused as Deacon content. "gt patrol" is multi-role
			// (deacon/witness/refinery), so it is not a deacon-only signal.
			name:        "polecat code task mentioning gt patrol scan is allowed",
			title:       "Fix gt patrol scan timeout handling",
			description: "The gt patrol scan subcommand times out under load; add a retry loop.",
			formula:     "mol-polecat-work",
			wantErr:     false,
		},
		{
			name:        "polecat code task mentioning gt patrol report is allowed",
			title:       "Emit metrics from gt patrol report",
			description: "Parse gt patrol report output and forward counters to the dashboard.",
			formula:     "mol-polecat-work",
			wantErr:     false,
		},
		{
			name:        "polecat code task mentioning gt patrol timeout is allowed",
			title:       "Tune gt patrol timeout",
			description: "Bump the gt patrol timeout so long rigs are not prematurely reaped.",
			formula:     "mol-polecat-work",
			wantErr:     false,
		},
		{
			name:        "empty title and description under polecat formula is allowed",
			title:       "",
			description: "",
			formula:     "mol-polecat-work",
			wantErr:     false,
		},
		{
			name:        "multiple deacon signals in one bead rejected",
			title:       "Deacon patrol and gt deacon heartbeat",
			description: "Run gt deacon heartbeat then a deacon patrol cycle.",
			formula:     "mol-polecat-work",
			wantErr:     true,
			errSubstr:   "bead references deacon",
		},
		{
			name:        "mixed-case gt patrol mention in code task is allowed",
			title:       "Refactor GT PATROL dispatch",
			description: "Clean up the Gt Patrol dispatch path in internal/cmd.",
			formula:     "mol-polecat-work",
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

// TestValidateBeadContentForFormula_PolecatPatrolTaskMentioningPatrol guards
// against gastown-wat: the bare "gt patrol" substring must not be treated as a
// deacon-only signal, so a legitimate polecat code task that mentions
// "gt patrol ..." is not refused from mol-polecat-work.
func TestValidateBeadContentForFormula_PolecatPatrolTaskMentioningPatrol(t *testing.T) {
	cases := []struct {
		name        string
		title       string
		description string
	}{
		{"scan subcommand", "Fix gt patrol scan crash", "Reproduce the gt patrol scan crash when rigs is empty."},
		{"report subcommand", "Add gt patrol report JSON output", "Expose gt patrol report as JSON for downstream tooling."},
		{"timeout tuning", "Tune gt patrol timeout", "Raise the gt patrol timeout ceiling."},
		{"bare mention in prose", "Patrol logger", "Log when gt patrol runs so we can audit cadence."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateBeadContentForFormula(tc.title, tc.description, "mol-polecat-work"); err != nil {
				t.Fatalf("ValidateBeadContentForFormula(%q, %q) returned unexpected error: %v", tc.title, tc.description, err)
			}
		})
	}
}

// TestDetectConflictingRole_NonPolecatTargetDoesNotFlagDeaconContent ensures that
// when the target role is not "polecat", deacon content is not treated as a
// conflict (the guard only fires for roles other than the target).
func TestDetectConflictingRole_NonPolecatTargetDoesNotFlagDeaconContent(t *testing.T) {
	role, signal, found := detectConflictingRole(
		"Deacon patrol",
		"Run gt deacon heartbeat and a deacon patrol.",
		"deacon",
	)
	if found {
		t.Fatalf("expected no conflict for own role targetRole=deacon, got role=%q signal=%q", role, signal)
	}
}
