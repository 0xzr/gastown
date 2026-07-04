package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateAcceptsGitDirectory(t *testing.T) {
	root := t.TempDir()
	gitdir := filepath.Join(root, ".git")
	if err := os.Mkdir(gitdir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitdir, "HEAD"), []byte("ref: refs/heads/main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := Validate(root, IntegrityOptions{Require: true}); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestValidateRejectsPartialGitDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	err := Validate(root, IntegrityOptions{Require: true})
	if !errors.Is(err, ErrIntegrityViolation) {
		t.Fatalf("Validate() error = %v, want ErrIntegrityViolation", err)
	}
}

func TestValidateAcceptsLinkedWorktreeGitfile(t *testing.T) {
	root := t.TempDir()
	gitdir := filepath.Join(root, "repo.git", "worktrees", "alpha")
	writeLinkedWorktree(t, root, gitdir, true)

	if err := Validate(filepath.Join(root, "nested"), IntegrityOptions{Require: true}); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestValidateRejectsMissingRequiredMetadata(t *testing.T) {
	root := t.TempDir()

	err := Validate(root, IntegrityOptions{Require: true})
	if !errors.Is(err, ErrIntegrityViolation) {
		t.Fatalf("Validate() error = %v, want ErrIntegrityViolation", err)
	}
}

func TestValidateAllowsMissingOptionalMetadata(t *testing.T) {
	root := t.TempDir()

	// Bound the search at root so a stray ancestor .git (e.g. a leftover
	// /tmp/.git above t.TempDir()) cannot be mistaken for this worktree's own
	// metadata. Production callers always pass TownRoot; mirror that here.
	if err := Validate(root, IntegrityOptions{TownRoot: root}); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

// TestValidateIgnoresStrayAncestorGitDir locks in isolation from environmental
// pollution: a stray incomplete .git in an ancestor of the town root (observed
// in the wild as a leftover /tmp/.git) must not be reported as the caller's own
// corrupted metadata when TownRoot bounds the search to the workspace.
func TestValidateIgnoresStrayAncestorGitDir(t *testing.T) {
	townRoot := t.TempDir()
	ancestor := t.TempDir()
	if err := os.Mkdir(filepath.Join(ancestor, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	// townRoot lives under ancestor via t.TempDir() nesting only by chance;
	// build an explicit parent/child so the stray .git is a true ancestor.
	if err := os.MkdirAll(filepath.Join(ancestor, "workspace"), 0755); err != nil {
		t.Fatal(err)
	}
	// Re-point townRoot to the explicit child so ancestor/.git is an ancestor.
	townRoot = filepath.Join(ancestor, "workspace")

	if err := Validate(townRoot, IntegrityOptions{TownRoot: townRoot}); err != nil {
		t.Fatalf("Validate() error = %v, want nil for town root under stray ancestor .git", err)
	}
}

func TestValidateRejectsMalformedGitfile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("not a gitdir\n"), 0644); err != nil {
		t.Fatal(err)
	}

	err := Validate(root, IntegrityOptions{Require: true})
	if !errors.Is(err, ErrIntegrityViolation) {
		t.Fatalf("Validate() error = %v, want ErrIntegrityViolation", err)
	}
}

func TestValidateRejectsMissingGitdirTarget(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "repo.git", "worktrees", "alpha")
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+missing+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	err := Validate(root, IntegrityOptions{Require: true})
	if !errors.Is(err, ErrIntegrityViolation) {
		t.Fatalf("Validate() error = %v, want ErrIntegrityViolation", err)
	}
}

func TestValidateRejectsPartialGitdirMetadata(t *testing.T) {
	root := t.TempDir()
	gitdir := filepath.Join(root, "repo.git", "worktrees", "alpha")
	writeLinkedWorktree(t, root, gitdir, false)

	err := Validate(root, IntegrityOptions{Require: true})
	if !errors.Is(err, ErrIntegrityViolation) {
		t.Fatalf("Validate() error = %v, want ErrIntegrityViolation", err)
	}
}

func TestValidateHonorsTownRootBoundary(t *testing.T) {
	townRoot := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	err := Validate(townRoot, IntegrityOptions{TownRoot: townRoot, Require: true})
	if !errors.Is(err, ErrIntegrityViolation) {
		t.Fatalf("Validate() error = %v, want ErrIntegrityViolation", err)
	}
}

func writeLinkedWorktree(t *testing.T, root, gitdir string, withHead bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(gitdir, 0755); err != nil {
		t.Fatal(err)
	}
	if withHead {
		if err := os.WriteFile(filepath.Join(gitdir, "HEAD"), []byte("ref: refs/heads/main\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+gitdir+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
}
