package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaleJSONLExportCheck_Run(t *testing.T) {
	t.Run("no beads directories returns OK", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmpDir, "mayor"), 0755); err != nil {
			t.Fatal(err)
		}

		check := NewStaleJSONLExportCheck()
		ctx := &CheckContext{TownRoot: tmpDir}
		result := check.Run(ctx)

		if result.Status != StatusOK {
			t.Errorf("expected StatusOK, got %v: %s", result.Status, result.Message)
		}
	})

	t.Run("canonical beads with matching statuses returns OK", func(t *testing.T) {
		tmpDir := t.TempDir()
		rigPath := filepath.Join(tmpDir, "myrig")
		beadsDir := filepath.Join(rigPath, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		writeIssuesJSONL(t, beadsDir, []jsonlIssue{
			{ID: "myrig-abc", Status: "open"},
			{ID: "myrig-def", Status: "closed"},
		})

		check := NewStaleJSONLExportCheck()
		check.queryIssueStatuses = func(rigPath string) (map[string]string, error) {
			return map[string]string{
				"myrig-abc": "open",
				"myrig-def": "closed",
			}, nil
		}

		ctx := &CheckContext{TownRoot: tmpDir}
		result := check.Run(ctx)

		if result.Status != StatusOK {
			t.Errorf("expected StatusOK, got %v: %s", result.Status, result.Message)
		}
	})

	t.Run("canonical beads with stale CLOSED export reports error", func(t *testing.T) {
		tmpDir := t.TempDir()
		rigPath := filepath.Join(tmpDir, "myrig")
		beadsDir := filepath.Join(rigPath, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		writeIssuesJSONL(t, beadsDir, []jsonlIssue{
			{ID: "myrig-abc", Status: "closed"}, // stale: live is open
		})

		check := NewStaleJSONLExportCheck()
		check.queryIssueStatuses = func(rigPath string) (map[string]string, error) {
			return map[string]string{"myrig-abc": "open"}, nil
		}

		ctx := &CheckContext{TownRoot: tmpDir}
		result := check.Run(ctx)

		if result.Status != StatusError {
			t.Errorf("expected StatusError, got %v: %s", result.Status, result.Message)
		}
		if len(result.Details) == 0 || !strings.Contains(result.Details[0], "status mismatch") {
			t.Errorf("expected stale status detail, got %v", result.Details)
		}
	})

	t.Run("canonical mayor/rig beads with stale export reports error", func(t *testing.T) {
		tmpDir := t.TempDir()
		rigPath := filepath.Join(tmpDir, "myrig")
		beadsDir := filepath.Join(rigPath, "mayor", "rig", ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		writeIssuesJSONL(t, beadsDir, []jsonlIssue{
			{ID: "myrig-xyz", Status: "closed"},
		})

		check := NewStaleJSONLExportCheck()
		check.queryIssueStatuses = func(rigPath string) (map[string]string, error) {
			return map[string]string{"myrig-xyz": "open"}, nil
		}

		ctx := &CheckContext{TownRoot: tmpDir}
		result := check.Run(ctx)

		if result.Status != StatusError {
			t.Errorf("expected StatusError, got %v: %s", result.Status, result.Message)
		}
	})

	t.Run("non-canonical forbidden clone with issues.jsonl reports error", func(t *testing.T) {
		tmpDir := t.TempDir()
		rigPath := filepath.Join(tmpDir, "myrig")
		forbiddenBeads := filepath.Join(rigPath, "crew", "worker", ".beads")
		if err := os.MkdirAll(forbiddenBeads, 0755); err != nil {
			t.Fatal(err)
		}
		writeIssuesJSONL(t, forbiddenBeads, []jsonlIssue{
			{ID: "myrig-old", Status: "closed"},
		})

		check := NewStaleJSONLExportCheck()
		check.queryIssueStatuses = func(rigPath string) (map[string]string, error) {
			return nil, fmt.Errorf("should not query live DB for forbidden clone")
		}

		ctx := &CheckContext{TownRoot: tmpDir}
		result := check.Run(ctx)

		if result.Status != StatusError {
			t.Errorf("expected StatusError, got %v: %s", result.Status, result.Message)
		}
		found := false
		for _, d := range result.Details {
			if strings.Contains(d, "non-canonical .beads contains issues.jsonl") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected forbidden clone detail, got %v", result.Details)
		}
	})

	t.Run("non-canonical forbidden clone with export-state.json reports error", func(t *testing.T) {
		tmpDir := t.TempDir()
		rigPath := filepath.Join(tmpDir, "myrig")
		forbiddenBeads := filepath.Join(rigPath, "polecats", "quartz", ".beads")
		if err := os.MkdirAll(forbiddenBeads, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(forbiddenBeads, "export-state.json"), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}

		check := NewStaleJSONLExportCheck()
		ctx := &CheckContext{TownRoot: tmpDir}
		result := check.Run(ctx)

		if result.Status != StatusError {
			t.Errorf("expected StatusError, got %v: %s", result.Status, result.Message)
		}
		found := false
		for _, d := range result.Details {
			if strings.Contains(d, "export-state.json") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected export-state.json detail, got %v", result.Details)
		}
	})

	t.Run("canonical redirect-only worktree is allowed", func(t *testing.T) {
		tmpDir := t.TempDir()
		rigPath := filepath.Join(tmpDir, "myrig")
		canonicalBeads := filepath.Join(rigPath, ".beads")
		worktreeBeads := filepath.Join(rigPath, "polecats", "quartz", ".beads")
		if err := os.MkdirAll(canonicalBeads, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(worktreeBeads, 0755); err != nil {
			t.Fatal(err)
		}
		// Redirect-only worktree should not be flagged.
		if err := os.WriteFile(filepath.Join(worktreeBeads, "redirect"), []byte("../../.beads\n"), 0644); err != nil {
			t.Fatal(err)
		}
		writeIssuesJSONL(t, canonicalBeads, []jsonlIssue{
			{ID: "myrig-abc", Status: "open"},
		})

		check := NewStaleJSONLExportCheck()
		check.queryIssueStatuses = func(rigPath string) (map[string]string, error) {
			return map[string]string{"myrig-abc": "open"}, nil
		}

		ctx := &CheckContext{TownRoot: tmpDir}
		result := check.Run(ctx)

		if result.Status != StatusOK {
			t.Errorf("expected StatusOK, got %v: %s", result.Status, result.Message)
		}
	})
}

func TestStaleJSONLExportCheck_CanonicalBeadsDirs(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "myrig", ".beads"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "otherrig", "mayor", "rig", ".beads"), 0755); err != nil {
		t.Fatal(err)
	}

	check := NewStaleJSONLExportCheck()
	canonical := check.canonicalBeadsDirs(tmpDir)

	expected := []string{
		filepath.Join(tmpDir, ".beads"),
		filepath.Join(tmpDir, "myrig", ".beads"),
		filepath.Join(tmpDir, "myrig", "mayor", "rig", ".beads"),
		filepath.Join(tmpDir, "otherrig", ".beads"),
		filepath.Join(tmpDir, "otherrig", "mayor", "rig", ".beads"),
	}
	for _, path := range expected {
		if !isCanonicalBeadsDir(path, canonical) {
			t.Errorf("expected %s to be canonical", path)
		}
	}
}

func TestStaleJSONLExportCheck_RigPathForCanonicalBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()
	rigPath := filepath.Join(tmpDir, "myrig")
	if err := os.MkdirAll(filepath.Join(rigPath, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rigPath, "mayor", "rig", ".beads"), 0755); err != nil {
		t.Fatal(err)
	}

	check := NewStaleJSONLExportCheck()

	tests := []struct {
		beadsDir string
		want     string
	}{
		{filepath.Join(rigPath, ".beads"), rigPath},
		{filepath.Join(rigPath, "mayor", "rig", ".beads"), rigPath},
	}
	for _, tc := range tests {
		got := check.rigPathForCanonicalBeadsDir(tmpDir, tc.beadsDir)
		if got != tc.want {
			t.Errorf("rigPathForCanonicalBeadsDir(%q) = %q, want %q", tc.beadsDir, got, tc.want)
		}
	}
}

func writeIssuesJSONL(t *testing.T, beadsDir string, issues []jsonlIssue) {
	t.Helper()
	path := filepath.Join(beadsDir, "issues.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	defer f.Close()
	for _, issue := range issues {
		line := fmt.Sprintf(`{"id":"%s","status":"%s"}`, issue.ID, issue.Status)
		if _, err := fmt.Fprintln(f, line); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
}
