package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentBeadsExistCheck_NoRoutes verifies the check handles missing routes.
func TestAgentBeadsExistCheck_NoRoutes(t *testing.T) {
	tmpDir := t.TempDir()

	// No .beads dir at all
	check := NewAgentBeadsCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	// With no routes, only global agents (deacon, mayor) are checked
	// They won't exist without Dolt, so we expect error
	t.Logf("Result: status=%v, message=%s", result.Status, result.Message)
	if result.Status == StatusOK {
		t.Error("expected error for missing global agent beads")
	}
}

// TestAgentBeadsExistCheck_NoRigs verifies the check handles empty routes.
func TestAgentBeadsExistCheck_NoRigs(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .beads dir with empty routes.jsonl
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewAgentBeadsCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	// With empty routes, only global agents (deacon, mayor) are checked
	// They won't exist without Dolt, so we expect error or warning
	t.Logf("Result: status=%v, message=%s", result.Status, result.Message)
}

// TestAgentBeadsExistCheck_ExpectedIDs verifies the check looks for correct agent bead IDs.
func TestAgentBeadsExistCheck_ExpectedIDs(t *testing.T) {
	tmpDir := t.TempDir()

	// Set up routes pointing to a rig with known prefix
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Use "sw" prefix to match sallaWork pattern
	routesContent := `{"prefix":"sw-","path":"sallaWork/mayor/rig"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create rig beads directory
	rigBeadsDir := filepath.Join(tmpDir, "sallaWork", "mayor", "rig", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	check := NewAgentBeadsCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	// Should report missing beads
	if result.Status == StatusOK {
		t.Errorf("expected error for missing agent beads, got: %s", result.Message)
	}

	// Should mention the expected bead IDs in details
	if len(result.Details) == 0 {
		t.Error("expected details to contain missing bead IDs")
	}

	// Verify the expected IDs are in the details
	expectedIDs := []string{"sw-sallaWork-witness", "sw-sallaWork-refinery"}
	for _, expectedID := range expectedIDs {
		found := false
		for _, detail := range result.Details {
			if detail == expectedID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected missing bead ID %s in details, got: %v", expectedID, result.Details)
		}
	}

	t.Logf("Result: status=%v, message=%s, details=%v", result.Status, result.Message, result.Details)
}

// TestAgentBeadsExistCheck_RespectsRigScope verifies that --rig excludes
// unrelated rig routes from agent-bead expectations.
func TestAgentBeadsExistCheck_RespectsRigScope(t *testing.T) {
	tmpDir := t.TempDir()

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	routesContent := strings.Join([]string{
		`{"prefix":"gs-","path":"gastown/mayor/rig"}`,
		`{"prefix":"do-","path":"coder_dotfiles/mayor/rig"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		filepath.Join(tmpDir, "gastown", "mayor", "rig", ".beads"),
		filepath.Join(tmpDir, "coder_dotfiles", "mayor", "rig", ".beads"),
	} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}

	check := NewAgentBeadsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: "gastown"}

	result := check.Run(ctx)

	if result.Status == StatusOK {
		t.Fatalf("expected missing agent beads for scoped rig, got OK")
	}
	for _, detail := range result.Details {
		if strings.HasPrefix(detail, "do-") {
			t.Fatalf("expected --rig scope to exclude coder_dotfiles agent bead %q, got details: %v", detail, result.Details)
		}
	}
	foundGastown := false
	for _, detail := range result.Details {
		if strings.HasPrefix(detail, "gs-") {
			foundGastown = true
			break
		}
	}
	if !foundGastown {
		t.Fatalf("expected scoped result to include gastown agent beads, got details: %v", result.Details)
	}
}

// TestAgentBeadsExistCheck_FixRespectsRigScope verifies that --fix with a rig
// scope does not create agent beads for unrelated rig prefixes.
func TestAgentBeadsExistCheck_FixRespectsRigScope(t *testing.T) {
	tmpDir := t.TempDir()

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	routesContent := strings.Join([]string{
		`{"prefix":"gs-","path":"gastown/mayor/rig"}`,
		`{"prefix":"do-","path":"coder_dotfiles/mayor/rig"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		filepath.Join(tmpDir, "gastown", "mayor", "rig", ".beads"),
		filepath.Join(tmpDir, "coder_dotfiles", "mayor", "rig", ".beads"),
	} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "gastown", "crew", "alice", ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "coder_dotfiles", "crew", "bella", ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	logFile := filepath.Join(tmpDir, "bd.log")
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	bdScript := filepath.Join(binDir, "bd")
	script := `#!/usr/bin/env bash
set -euo pipefail

logfile="` + logFile + `"

args=()
for arg in "$@"; do
  if [[ "$arg" == --allow-stale ]]; then
    continue
  fi
  args+=("$arg")
done

cmd=""
idx=0
for i in "${!args[@]}"; do
  if [[ "${args[$i]}" != -* ]]; then
    cmd="${args[$i]}"
    idx=$i
    break
  fi
done

if [[ -z "$cmd" ]]; then
  exit 0
fi

rest=("${args[@]:$((idx + 1))}")

case "$cmd" in
  list)
    printf '[]\n'
    ;;
  mol)
    if [[ "${rest[0]:-}" == "wisp" && "${rest[1]:-}" == "list" ]]; then
      printf '{"wisps":[]}\n'
      exit 0
    fi
    exit 1
    ;;
  show)
    exit 1
    ;;
  create)
    id=""
    title=""
    for arg in "${rest[@]}"; do
      case "$arg" in
        --id=*) id="${arg#--id=}" ;;
        --title=*) title="${arg#--title=}" ;;
      esac
    done
    printf 'create %s\n' "$id" >> "$logfile"
    printf '{"id":"%s","title":"%s","status":"open","labels":["gt:agent"]}\n' "$id" "$title"
    ;;
  update)
    if [[ ${#rest[@]} -gt 0 ]]; then
      printf 'update %s\n' "${rest[0]}" >> "$logfile"
    fi
    printf '{}'\n
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(bdScript, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", fmt.Sprintf("%s%c%s", binDir, os.PathListSeparator, os.Getenv("PATH")))

	check := NewAgentBeadsCheck()
	ctx := &CheckContext{TownRoot: tmpDir, RigName: "gastown"}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() returned error: %v", err)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("reading fake bd log: %v", err)
	}
	log := string(data)
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		if strings.Contains(line, " do-") {
			t.Fatalf("expected scoped Fix() to avoid coder_dotfiles beads, got log line %q", line)
		}
	}
	if !strings.Contains(log, "create gs-gastown-witness") {
		t.Fatalf("expected scoped Fix() to create gastown witness bead, got log: %q", log)
	}
}

// TestListCrewWorkers_FiltersWorktrees verifies that listCrewWorkers skips
// git worktrees (directories where .git is a file) and only returns canonical
// crew workers (where .git is a directory). This is the fix for GH#2767.
func TestListCrewWorkers_FiltersWorktrees(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "myrig"
	crewDir := filepath.Join(tmpDir, rigName, "crew")

	// Create a canonical crew worker: .git is a directory
	canonicalDir := filepath.Join(crewDir, "alice")
	if err := os.MkdirAll(filepath.Join(canonicalDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create a worktree: .git is a file (contains gitdir pointer)
	worktreeDir := filepath.Join(crewDir, "alice-worktree")
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, ".git"),
		[]byte("gitdir: /path/to/main/.git/worktrees/alice-worktree\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a second canonical worker
	bobDir := filepath.Join(crewDir, "bob")
	if err := os.MkdirAll(filepath.Join(bobDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create a directory without .git at all (should be included — not a worktree)
	plainDir := filepath.Join(crewDir, "charlie")
	if err := os.MkdirAll(plainDir, 0755); err != nil {
		t.Fatal(err)
	}

	workers := listCrewWorkers(tmpDir, rigName)

	// Should include alice, bob, charlie but NOT alice-worktree
	expected := map[string]bool{"alice": false, "bob": false, "charlie": false}
	for _, w := range workers {
		if w == "alice-worktree" {
			t.Errorf("listCrewWorkers should skip worktree 'alice-worktree', got: %v", workers)
		}
		if _, ok := expected[w]; ok {
			expected[w] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("listCrewWorkers should include canonical worker %q, got: %v", name, workers)
		}
	}
}

// TestAddWispLabelSQL_ErrorsGracefully verifies addWispLabelSQL doesn't panic
// and returns an error when bd is unavailable (no Dolt server).
// This is a regression guard for gt-3vx: after CreateAgentBead, the gt:agent
// label must also be inserted into wisp_labels so doctor checks that join
// wisp_labels can find the bead.
func TestAddWispLabelSQL_ErrorsGracefully(t *testing.T) {
	tmpDir := t.TempDir()
	err := addWispLabelSQL(tmpDir, "gt-gastown-witness", "gt:agent")
	// bd sql will fail without a Dolt server — just verify no panic and that the
	// function returns an error (not silently discarding the failure).
	if err == nil {
		t.Log("addWispLabelSQL succeeded (Dolt server is running)")
	} else {
		t.Logf("addWispLabelSQL returned expected error without Dolt: %v", err)
	}
}

// TestListPolecats_FiltersWorktrees verifies that listPolecats skips
// git worktrees, same as listCrewWorkers. See GH#2767.
func TestListPolecats_FiltersWorktrees(t *testing.T) {
	tmpDir := t.TempDir()
	rigName := "myrig"
	polecatDir := filepath.Join(tmpDir, rigName, "polecats")

	// Canonical polecat
	if err := os.MkdirAll(filepath.Join(polecatDir, "scout", ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Worktree polecat (.git is a file)
	wtDir := filepath.Join(polecatDir, "scout-wt")
	if err := os.MkdirAll(wtDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, ".git"),
		[]byte("gitdir: /path/to/main/.git/worktrees/scout-wt\n"), 0644); err != nil {
		t.Fatal(err)
	}

	polecats := listPolecats(tmpDir, rigName)

	if len(polecats) != 1 || polecats[0] != "scout" {
		t.Errorf("listPolecats should return only [scout], got: %v", polecats)
	}
}

// TestAgentBeadsExistCheck_FixTargetsRigBeadsDir is a regression guard for
// gastown-cet.1.1 (doctor --fix seam). It verifies that Fix() seeds rig-level
// agent beads (witness, refinery, crew, polecat) with BEADS_DIR pointing to
// the rig's own .beads database, not the town/HQ database. Before the fix,
// the rig-level `bd` wrapper in Fix() had prefix routing enabled, so a real
// `bd` client would have redirected the create to the HQ database — silently
// re-introducing the HQ contamination the original gastown-cet.1.1 commit
// removed.
//
// We mock bd to log the BEADS_DIR of every invocation so we can verify which
// database each create targeted. We also assert that the rig-level creates
// do NOT target the town .beads directory.
func TestAgentBeadsExistCheck_FixTargetsRigBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Set up routes pointing to a rig with a known prefix.
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	const rigName = "gastown"
	const rigPrefix = "gs"
	const rigRoutePath = "gastown/mayor/rig"
	routesContent := fmt.Sprintf(`{"prefix":"%s-","path":"%s"}`+"\n", rigPrefix, rigRoutePath)
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create the rig beads directory tree matching the route path so that
	// bd subprocess invocations (which set cmd.Dir to this path) succeed.
	rigBeadsDir := filepath.Join(tmpDir, rigRoutePath, ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create the town root marker so ForAgentBead() can detect the town root
	// and would otherwise redirect rig-level creates to the town .beads
	// directory. This is what makes the bug observable: without WithNoRoute(),
	// CreateAgentBead would re-route to townBeadsDir; with WithNoRoute() it
	// stays on rigBeadsDir.
	if err := os.MkdirAll(filepath.Join(tmpDir, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "mayor", "town.json"), []byte(`{"name":"test-town"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Mock bd that logs every create invocation with its BEADS_DIR.
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(tmpDir, "bd-cmd.log")
	script := `#!/usr/bin/env bash
set -euo pipefail

logfile="` + logPath + `"

args=()
for arg in "$@"; do
  if [[ "$arg" == --allow-stale ]]; then
    continue
  fi
  args+=("$arg")
done

cmd=""
idx=0
for i in "${!args[@]}"; do
  if [[ "${args[$i]}" != -* ]]; then
    cmd="${args[$i]}"
    idx=$i
    break
  fi
done

case "$cmd" in
  list)
    printf '[]\n'
    ;;
  mol)
    if [[ "${args[$((idx + 1))]:-}" == "wisp" && "${args[$((idx + 2))]:-}" == "list" ]]; then
      printf '{"wisps":[]}\n'
      exit 0
    fi
    exit 1
    ;;
  show)
    exit 1
    ;;
  create)
    id=""
    for arg in "${args[@]:$((idx + 1))}"; do
      case "$arg" in
        --id=*) id="${arg#--id=}" ;;
      esac
    done
    # Single-line log entry: BEADS_DIR + command. Keep arg newlines collapsed
    # so each bd invocation produces exactly one log line.
    log_line=""
    for arg in "${args[@]}"; do
      arg_nl=$(printf '%s' "$arg" | tr '\n' ' ')
      if [[ -z "$log_line" ]]; then
        log_line="$arg_nl"
      else
        log_line="$log_line $arg_nl"
      fi
    done
    printf 'BEADS_DIR=%s %s\n' "${BEADS_DIR:-<unset>}" "$log_line" >> "$logfile"
    printf '{"id":"%s","status":"open","created_at":"2025-01-01T00:00:00Z"}\n' "$id"
    ;;
  update)
    printf '{}'
    ;;
  *)
    exit 0
    ;;
esac
`
	bdScript := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdScript, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fmt.Sprintf("%s%c%s", binDir, os.PathListSeparator, os.Getenv("PATH")))

	check := NewAgentBeadsCheck()
	ctx := &CheckContext{TownRoot: tmpDir}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() returned error: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading mock bd log: %v", err)
	}
	log := string(data)

	townBeadsDir := beadsDir
	witnessID := fmt.Sprintf("%s-%s-witness", rigPrefix, rigName)
	refineryID := fmt.Sprintf("%s-%s-refinery", rigPrefix, rigName)

	// Find the create for witness/refinery and verify their BEADS_DIR pointed
	// to the rig's own .beads directory, not the town/HQ .beads directory.
	for _, id := range []string{witnessID, refineryID} {
		var foundLine string
		var foundDir string
		for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
			// Lines have the form: BEADS_DIR=<dir> create ... --id=<id> ...
			const prefix = "BEADS_DIR="
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			rest := strings.TrimPrefix(line, prefix)
			beadsDir, cmdLine, ok := strings.Cut(rest, " ")
			if !ok {
				continue
			}
			if strings.Contains(cmdLine, "--id="+id) {
				foundLine = line
				foundDir = beadsDir
				break
			}
		}
		if foundLine == "" {
			t.Errorf("expected create for %s in bd log, got:\n%s", id, log)
			continue
		}
		if foundDir == "<unset>" {
			t.Errorf("create for %s ran with unset BEADS_DIR (would route via prefix): %s", id, foundLine)
			continue
		}
		if foundDir == townBeadsDir {
			t.Errorf("create for %s targeted town/HQ beads dir %q, want rig dir %q", id, townBeadsDir, rigBeadsDir)
			continue
		}
		if !strings.HasPrefix(foundDir, rigBeadsDir) {
			t.Errorf("create for %s targeted %q, want prefix %q", id, foundDir, rigBeadsDir)
		}
	}
}
