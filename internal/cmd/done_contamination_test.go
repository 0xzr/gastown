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

// TestValidateDefaultMergeTarget covers gastown-1gd: when the resolved target
// is invalid and falls back to the configured default branch, the default
// branch itself must be validated. An unsafe default (empty or equal to the
// rig name) must surface a clear error rather than silently submitting an MR
// to a bogus target.
func TestValidateDefaultMergeTarget(t *testing.T) {
	tests := []struct {
		name          string
		defaultBranch string
		rigName       string
		wantErr       bool
	}{
		{name: "normal default branch", defaultBranch: "main", rigName: "gastown"},
		{name: "integration default branch", defaultBranch: "integration/gt-epic", rigName: "gastown"},
		{name: "default equals rig name", defaultBranch: "gastown", rigName: "gastown", wantErr: true},
		{name: "empty default branch", defaultBranch: "", rigName: "gastown", wantErr: true},
		{name: "origin-prefixed rig name", defaultBranch: "origin/gastown", rigName: "gastown", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDefaultMergeTarget(tt.defaultBranch, tt.rigName)
			if tt.wantErr {
				if err == nil {
					t.Fatal("validateDefaultMergeTarget() = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("validateDefaultMergeTarget() = %v, want nil", err)
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
