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
