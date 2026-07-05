package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrackingDependsOnID_CrossRigWrapsExternal(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte("{\"prefix\":\"ag-\",\"path\":\"agentcompany/.beads\"}\n"), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	got := trackingDependsOnID(townRoot, "ag-95s.1")
	want := "external:ag:ag-95s.1"
	if got != want {
		t.Fatalf("trackingDependsOnID() = %q, want %q", got, want)
	}
}

func TestTrackingDependsOnID_HQStaysLocal(t *testing.T) {
	townRoot := t.TempDir()
	got := trackingDependsOnID(townRoot, "hq-cv-test")
	if got != "hq-cv-test" {
		t.Fatalf("trackingDependsOnID() = %q, want %q", got, "hq-cv-test")
	}
}

func TestFallbackTrackingRelationUsesTownRouting(t *testing.T) {
	townRoot := t.TempDir()
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0o755); err != nil {
		t.Fatalf("mkdir town beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"), []byte(`{"prefix":"polybot-","path":"polybot"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "bd.log")
	script := strings.ReplaceAll(`#!/usr/bin/env sh
printf 'BEADS_DIR=%s ARGS=%s\n' "${BEADS_DIR:-}" "$*" >> "LOGPATH"
if [ -n "${BEADS_DIR:-}" ]; then
  echo 'fallback should not pin BEADS_DIR' >&2
  exit 1
fi
exit 0
`, "LOGPATH", logPath)
	writeBDStub(t, binDir, script, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BEADS_DIR", filepath.Join(townRoot, "polybot", ".beads"))

	if err := fallbackTrackingRelation(townRoot, "hq-wisp-ctx", "polybot-optiv", true, os.ErrInvalid); err != nil {
		t.Fatalf("fallbackTrackingRelation: %v; log:\n%s", err, readTestFile(t, logPath))
	}

	log := readTestFile(t, logPath)
	if !strings.Contains(log, "BEADS_DIR= ARGS=dep add hq-wisp-ctx external:polybot:polybot-optiv --type=tracks") {
		t.Fatalf("fallback did not route through town root without BEADS_DIR; log:\n%s", log)
	}
}
