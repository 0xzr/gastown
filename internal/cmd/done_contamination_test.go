package cmd

import "testing"

func TestDoneContaminationBaseRef(t *testing.T) {
	tests := []struct {
		name           string
		defaultBranch  string
		explicitTarget string
		want           string
	}{
		{
			name:           "defaults to rig branch",
			defaultBranch:  "main",
			explicitTarget: "",
			want:           "origin/main",
		},
		{
			name:           "uses explicit target branch",
			defaultBranch:  "main",
			explicitTarget: "upstream-rebuild-main",
			want:           "origin/upstream-rebuild-main",
		},
		{
			name:           "avoids double origin prefix",
			defaultBranch:  "main",
			explicitTarget: "origin/upstream-rebuild-main",
			want:           "origin/upstream-rebuild-main",
		},
		{
			name:           "canonicalizes full remote ref",
			defaultBranch:  "main",
			explicitTarget: "refs/remotes/origin/main",
			want:           "origin/main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := doneContaminationBaseRef(tt.defaultBranch, tt.explicitTarget)
			if got != tt.want {
				t.Fatalf("doneContaminationBaseRef(%q, %q) = %q, want %q", tt.defaultBranch, tt.explicitTarget, got, tt.want)
			}
		})
	}
}

func TestCanonicalMergeTarget(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   string
	}{
		{name: "plain branch", target: "main", want: "main"},
		{name: "origin branch", target: "origin/main", want: "main"},
		{name: "heads ref", target: "refs/heads/main", want: "main"},
		{name: "remote ref", target: "refs/remotes/origin/main", want: "main"},
		{name: "integration branch", target: "origin/integration/gt-epic", want: "integration/gt-epic"},
		{name: "trims whitespace", target: " origin/main ", want: "main"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := canonicalMergeTarget(tt.target)
			if got != tt.want {
				t.Fatalf("canonicalMergeTarget(%q) = %q, want %q", tt.target, got, tt.want)
			}
		})
	}
}

// TestIsValidMergeTarget covers hq-faz: gt done must never resolve the MR
// target as the rig name itself (which would advertise an "<rig>/<rig>" MR).
func TestIsValidMergeTarget(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		rigName string
		want    bool
	}{
		{name: "normal branch is valid", target: "main", rigName: "gastown", want: true},
		{name: "integration branch is valid", target: "integration/gt-epic", rigName: "gastown", want: true},
		{name: "origin-prefixed branch is valid", target: "origin/main", rigName: "gastown", want: true},
		{name: "empty target is invalid", target: "", rigName: "gastown", want: false},
		{name: "target equals rig name is invalid", target: "gastown", rigName: "gastown", want: false},
		{name: "origin-prefixed rig name is invalid", target: "origin/gastown", rigName: "gastown", want: false},
		{name: "whitespace-only target is invalid", target: "   ", rigName: "gastown", want: false},
		{name: "rig name with different rig is valid", target: "gastown", rigName: "other-rig", want: true},
		{name: "empty rig name does not reject normal branch", target: "main", rigName: "", want: true},
		{name: "origin-only is invalid", target: "origin/", rigName: "gastown", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidMergeTarget(tt.target, tt.rigName)
			if got != tt.want {
				t.Fatalf("isValidMergeTarget(%q, %q) = %v, want %v", tt.target, tt.rigName, got, tt.want)
			}
		})
	}
}

// TestDefaultBranchValidationInvariant covers gastown-1gd (Defect 3):
// runDone guards an unsafe --target fallback by validating the rig's
// configured defaultBranch against isValidMergeTarget up front. The
// misconfiguration cases (defaultBranch == rigName, defaultBranch == "")
// must all be rejected by the predicate the upstream guard relies on, so
// runDone bails out with a clear error instead of silently submitting an
// MR targeting "<rig>/<rig>" or origin/. This test pins the invariant
// the runDone guard depends on; if isValidMergeTarget ever starts accepting
// these inputs, the guard becomes a no-op and this regression surfaces.
func TestDefaultBranchValidationInvariant(t *testing.T) {
	tests := []struct {
		name          string
		defaultBranch string
		rigName       string
		// wantValid reports whether isValidMergeTarget should accept the
		// defaultBranch as a safe MR target. The runDone guard rejects
		// only when this returns false.
		wantValid bool
	}{
		{name: "main defaultBranch on standard rig is valid",
			defaultBranch: "main", rigName: "gastown", wantValid: true},
		{name: "custom defaultBranch distinct from rig name is valid",
			defaultBranch: "trunk", rigName: "gastown", wantValid: true},
		{name: "defaultBranch equal to rig name is invalid",
			defaultBranch: "gastown", rigName: "gastown", wantValid: false},
		{name: "origin/ prefixed rig name defaultBranch is invalid",
			defaultBranch: "origin/gastown", rigName: "gastown", wantValid: false},
		{name: "empty defaultBranch is invalid",
			defaultBranch: "", rigName: "gastown", wantValid: false},
		{name: "whitespace-only defaultBranch is invalid",
			defaultBranch: "   ", rigName: "gastown", wantValid: false},
		// Edge: rig with single-letter name and a default branch with
		// the same letter would still be rejected — the equality check
		// is full-string, not prefix-based. This guards against future
		// refactors that might mistakenly use HasPrefix.
		{name: "rig-name substring on a longer branch is valid",
			defaultBranch: "gastown-main", rigName: "gastown", wantValid: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidMergeTarget(tt.defaultBranch, tt.rigName)
			if got != tt.wantValid {
				t.Fatalf("isValidMergeTarget(defaultBranch=%q, rigName=%q) = %v, want %v — runDone guard depends on this predicate to refuse an unsafe defaultBranch fallback (gastown-1gd)",
					tt.defaultBranch, tt.rigName, got, tt.wantValid)
			}
		})
	}
}
