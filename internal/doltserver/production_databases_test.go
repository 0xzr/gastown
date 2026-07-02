package doltserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestConfiguredProductionDatabasesUsesRegisteredRigMetadata(t *testing.T) {
	townRoot := t.TempDir()
	writeTestMetadata(t, filepath.Join(townRoot, ".beads"), "hq")
	writeTestMetadata(t, filepath.Join(townRoot, "gastown", ".beads"), "gastown")
	writeTestMetadata(t, filepath.Join(townRoot, "polybot", ".beads"), "polybot")
	writeTestMetadata(t, filepath.Join(townRoot, "gtviz", ".beads"), "gtviz")

	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	if err := os.MkdirAll(filepath.Dir(rigsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	rigs := map[string]any{
		"version": 1,
		"rigs": map[string]any{
			"gastown": map[string]any{
				"git_url": "https://example.com/gastown.git",
				"beads":   map[string]string{"prefix": "gastown"},
			},
			"polybot": map[string]any{
				"git_url": "https://example.com/polybot.git",
				"beads":   map[string]string{"prefix": "polybot"},
			},
		},
	}
	data, err := json.Marshal(rigs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rigsPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	got := ConfiguredProductionDatabases(townRoot)
	want := []string{"hq", "gastown", "polybot"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfiguredProductionDatabases() = %#v, want %#v", got, want)
	}
}

func TestConfiguredProductionDatabasesFallsBackToRigPrefix(t *testing.T) {
	townRoot := t.TempDir()
	writeTestMetadata(t, filepath.Join(townRoot, ".beads"), "hq")

	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	if err := os.MkdirAll(filepath.Dir(rigsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	rigs := map[string]any{
		"version": 1,
		"rigs": map[string]any{
			"laneassist": map[string]any{
				"git_url": "https://example.com/laneassist.git",
				"beads":   map[string]string{"prefix": "la-"},
			},
		},
	}
	data, err := json.Marshal(rigs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rigsPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	got := ConfiguredProductionDatabases(townRoot)
	want := []string{"hq", "la"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfiguredProductionDatabases() = %#v, want %#v", got, want)
	}
}

func writeTestMetadata(t *testing.T, beadsDir, db string) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{
		"backend":       "dolt",
		"database":      "dolt",
		"dolt_mode":     "server",
		"dolt_database": db,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}
