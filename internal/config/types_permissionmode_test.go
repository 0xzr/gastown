package config

import (
	"slices"
	"testing"
)

func TestNormalizeClaudePermissionMode(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		required string
		want     []string
	}{
		{
			name:     "adds bypassPermissions when absent",
			args:     []string{"--dangerously-skip-permissions"},
			required: "bypassPermissions",
			want:     []string{"--dangerously-skip-permissions", "--permission-mode", "bypassPermissions"},
		},
		{
			name:     "overrides explicit plan mode",
			args:     []string{"--permission-mode", "plan"},
			required: "bypassPermissions",
			want:     []string{"--permission-mode", "bypassPermissions"},
		},
		{
			name:     "overrides plan mode with equals form",
			args:     []string{"--permission-mode=plan"},
			required: "bypassPermissions",
			want:     []string{"--permission-mode", "bypassPermissions"},
		},
		{
			name:     "leaves non-permission args intact",
			args:     []string{"--model", "sonnet", "--permission-mode", "acceptAllowedTools", "--foo"},
			required: "bypassPermissions",
			want:     []string{"--model", "sonnet", "--foo", "--permission-mode", "bypassPermissions"},
		},
		{
			name:     "leaves args unchanged when required empty",
			args:     []string{"--permission-mode", "plan"},
			required: "",
			want:     []string{"--permission-mode", "plan"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeClaudePermissionMode(tt.args, tt.required)
			if !slices.Equal(got, tt.want) {
				t.Errorf("NormalizeClaudePermissionMode() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractClaudePermissionMode(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
		ok   bool
	}{
		{
			name: "separate flag and value",
			args: []string{"claude", "--permission-mode", "plan"},
			want: "plan",
			ok:   true,
		},
		{
			name: "equals form",
			args: []string{"--permission-mode=bypassPermissions"},
			want: "bypassPermissions",
			ok:   true,
		},
		{
			name: "missing",
			args: []string{"--model", "sonnet"},
			want: "",
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ExtractClaudePermissionMode(tt.args)
			if got != tt.want || ok != tt.ok {
				t.Errorf("ExtractClaudePermissionMode() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}
