package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHookPolecatEnvCheck verifies that the polecat guard in runHook uses
// GT_ROLE as the authoritative check, so coordinators with a stale GT_POLECAT
// in their environment are not blocked from hooking (GH #1707).
func TestHookPolecatEnvCheck(t *testing.T) {
	tests := []struct {
		name      string
		role      string
		polecat   string
		wantBlock bool
	}{
		{
			name:      "bare polecat role is blocked",
			role:      "polecat",
			polecat:   "alpha",
			wantBlock: true,
		},
		{
			name:      "compound polecat role is blocked",
			role:      "gastown/polecats/Toast",
			polecat:   "Toast",
			wantBlock: true,
		},
		{
			name:      "mayor with stale GT_POLECAT is NOT blocked",
			role:      "mayor",
			polecat:   "alpha",
			wantBlock: false,
		},
		{
			name:      "compound witness with stale GT_POLECAT is NOT blocked",
			role:      "gastown/witness",
			polecat:   "alpha",
			wantBlock: false,
		},
		{
			name:      "crew with stale GT_POLECAT is NOT blocked",
			role:      "crew",
			polecat:   "alpha",
			wantBlock: false,
		},
		{
			name:      "compound crew with stale GT_POLECAT is NOT blocked",
			role:      "gastown/crew/den",
			polecat:   "alpha",
			wantBlock: false,
		},
		{
			name:      "no GT_ROLE with GT_POLECAT set is blocked",
			role:      "",
			polecat:   "alpha",
			wantBlock: true,
		},
		{
			name:      "no GT_ROLE and no GT_POLECAT is not blocked",
			role:      "",
			polecat:   "",
			wantBlock: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GT_ROLE", tt.role)
			t.Setenv("GT_POLECAT", tt.polecat)

			// We only test the polecat guard, so we call runHook with a dummy arg.
			// It will either fail at the guard or fail later (missing bead, etc.).
			// We only care whether the error is the polecat-block message.
			var blocked bool
			func() {
				defer func() {
					if r := recover(); r != nil {
						// Panic means we got past the guard — not blocked
						blocked = false
					}
				}()
				err := runHook(nil, []string{"fake-bead-id"})
				blocked = err != nil && strings.Contains(err.Error(), "polecats cannot hook")
			}()

			if blocked != tt.wantBlock {
				if tt.wantBlock {
					t.Errorf("expected polecat block but was not blocked (GT_ROLE=%q GT_POLECAT=%q)", tt.role, tt.polecat)
				} else {
					t.Errorf("unexpected polecat block with GT_ROLE=%q GT_POLECAT=%q", tt.role, tt.polecat)
				}
			}
		})
	}
}

// TestHookRejectsNonBeadArg pins down GH#3701: when cobra fails to match a
// subcommand and falls through to the bead-id positional, args that don't
// look like bead IDs should produce a clear error pointing at --help rather
// than the misleading "bead 'set' not found" emitted by bd show.
func TestHookRejectsNonBeadArg(t *testing.T) {
	// Ensure we don't trip the polecat guard.
	t.Setenv("GT_ROLE", "")
	t.Setenv("GT_POLECAT", "")

	tests := []string{"set", "list", "delete", "nonexistentword12345"}
	for _, arg := range tests {
		t.Run(arg, func(t *testing.T) {
			err := runHook(nil, []string{arg})
			if err == nil {
				t.Fatalf("runHook(%q) returned nil, want error", arg)
			}
			if !strings.Contains(err.Error(), "is not a bead ID") {
				t.Errorf("runHook(%q) error = %q, want substring %q", arg, err.Error(), "is not a bead ID")
			}
			if !strings.Contains(err.Error(), "--help") {
				t.Errorf("runHook(%q) error = %q, want it to point at --help", arg, err.Error())
			}
		})
	}
}

func TestHookAlreadyAppliedRecognizesDesiredState(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir town beads: %v", err)
	}

	binDir := t.TempDir()
	script := `#!/usr/bin/env sh
if [ "$1" = "show" ]; then
  printf '[{"id":"polybot-termn","status":"hooked","assignee":"polybot/polecats/guzzle","title":"ready","description":""}]'
  exit 0
fi
exit 1
`
	writeBDStub(t, binDir, script, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if !hookAlreadyApplied(townRoot, "polybot-termn", "polybot/polecats/guzzle") {
		t.Fatal("expected matching hooked bead to count as already applied")
	}
	if hookAlreadyApplied(townRoot, "polybot-termn", "polybot/polecats/nitro") {
		t.Fatal("wrong assignee should not count as already applied")
	}
}

func TestNormalizeHookShowTarget(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   string
	}{
		{
			name:   "shorthand polecat path resolves",
			target: "gastown/toast",
			want:   "gastown/polecats/toast",
		},
		{
			name:   "canonical polecat path stays canonical",
			target: "gastown/polecats/toast",
			want:   "gastown/polecats/toast",
		},
		{
			name:   "unknown target stays unchanged",
			target: "this-is-not-an-agent-path",
			want:   "this-is-not-an-agent-path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeHookShowTarget(tt.target)
			if got != tt.want {
				t.Fatalf("normalizeHookShowTarget(%q) = %q, want %q", tt.target, got, tt.want)
			}
		})
	}
}

// TestActualPolecatTargetFromEnv verifies that the canonical agent identity
// is derived from GT_RIG/GT_POLECAT and matches what gt done uses to
// reconstruct the polecat worktree. This is the gastown-dg1 recovery path
// for the "rig/rig" misresolution that stranded polecats after deleted
// molecules.
func TestActualPolecatTargetFromEnv(t *testing.T) {
	tests := []struct {
		name     string
		rig      string
		polecat  string
		expected string
	}{
		{"both set", "gastown", "jasper", "gastown/polecats/jasper"},
		{"empty rig", "", "jasper", ""},
		{"empty polecat", "gastown", "", ""},
		{"both empty", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GT_RIG", tt.rig)
			t.Setenv("GT_POLECAT", tt.polecat)
			got := actualPolecatTargetFromEnv()
			if got != tt.expected {
				t.Fatalf("actualPolecatTargetFromEnv() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// TestResolveSourceBeadFromBranch_NoRepoOrNoBranch verifies that
// resolveSourceBeadFromBranch is fail-closed when cwd is not a git repo or
// the branch is unparseable. It must NEVER return a fabricated source bead
// when durable evidence is absent.
func TestResolveSourceBeadFromBranch_NoRepoOrNoBranch(t *testing.T) {
	// Empty cwd → returns "". No branch parsing happens.
	if got := resolveSourceBeadFromBranch("", "gastown/polecats/jasper", nil); got != "" {
		t.Errorf("empty cwd returned %q, want \"\"", got)
	}

	// Non-git directory → returns "". We use t.TempDir() which has no .git.
	tmp := t.TempDir()
	if got := resolveSourceBeadFromBranch(tmp, "gastown/polecats/jasper", nil); got != "" {
		t.Errorf("non-git dir returned %q, want \"\"", got)
	}
}
