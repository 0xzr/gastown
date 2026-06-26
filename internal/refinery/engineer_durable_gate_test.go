package refinery

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDoMerge_DurableReviewGate_MissingCommand_BlocksMerge proves that a
// required durable review gate with no configured command fails closed: even
// when all local quality gates pass, the merge cannot proceed.
func TestDoMerge_DurableReviewGate_MissingCommand_BlocksMerge(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil
	// Re-enable durable gate but leave command empty. Required + no command must fail closed.
	e.config.DurableReviewGate = &DurableReviewGateConfig{Required: true}

	createFeatureBranch(t, workDir, "feat/no-cmd", "feature.txt", "feature content")

	result := e.doMerge(context.Background(), "feat/no-cmd", "main", "gt-test", nil)
	if result.Success {
		t.Fatal("expected merge to fail when durable review gate command is missing")
	}
	if !result.TestsFailed {
		t.Errorf("expected TestsFailed=true for missing durable gate command, got %+v", result)
	}
	if !strings.Contains(result.Error, "no command configured") {
		t.Errorf("expected missing command error, got: %s", result.Error)
	}

	output := e.output.(*bytes.Buffer).String()
	if !strings.Contains(output, "Durable review gate required") {
		t.Errorf("expected durable gate required log, got:\n%s", output)
	}
}

// TestDoMerge_DurableReviewGate_CommandFailure_BlocksMerge proves that a
// durable review gate command exiting non-zero blocks the merge.
func TestDoMerge_DurableReviewGate_CommandFailure_BlocksMerge(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil
	e.config.DurableReviewGate = &DurableReviewGateConfig{
		Required: true,
		Cmd:      `echo "reviewer rejection: missing tests" >&2; exit 1`,
	}

	createFeatureBranch(t, workDir, "feat/gate-fail", "feature.txt", "feature content")

	result := e.doMerge(context.Background(), "feat/gate-fail", "main", "gt-test", nil)
	if result.Success {
		t.Fatal("expected merge to fail when durable review gate rejects")
	}
	if !result.TestsFailed {
		t.Errorf("expected TestsFailed=true for durable gate rejection, got %+v", result)
	}
	if !strings.Contains(result.Error, "reviewer rejection: missing tests") {
		t.Errorf("expected gate error output in result, got: %s", result.Error)
	}
}

// TestDoMerge_DurableReviewGate_PassesWithoutAttestation_BlocksMerge proves
// that a durable review gate command exiting 0 is not enough: the merge only
// proceeds if the command also produced an HMAC attestation for the merge
// candidate tree.
func TestDoMerge_DurableReviewGate_PassesWithoutAttestation_BlocksMerge(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil
	e.config.DurableReviewGate = &DurableReviewGateConfig{
		Required: true,
		// Exit 0 but do not write the attestation file.
		Cmd: `echo "durable review passed (malicious)"`,
	}

	createFeatureBranch(t, workDir, "feat/no-attest", "feature.txt", "feature content")

	result := e.doMerge(context.Background(), "feat/no-attest", "main", "gt-test", nil)
	if result.Success {
		t.Fatal("expected merge to fail when durable review gate omits attestation")
	}
	if !result.TestsFailed {
		t.Errorf("expected TestsFailed=true for missing attestation, got %+v", result)
	}
	if !strings.Contains(result.Error, "HMAC attestation missing") {
		t.Errorf("expected missing HMAC attestation error, got: %s", result.Error)
	}
}

// TestDoMerge_DurableReviewGate_WritesAttestation_AllowsMerge proves the happy
// path: a durable review gate command that exits 0 and writes an attestation
// file for the merge-candidate tree allows the merge to proceed.
func TestDoMerge_DurableReviewGate_WritesAttestation_AllowsMerge(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil

	attestDir := filepath.Join(workDir, "attestations")
	e.config.DurableReviewGate = &DurableReviewGateConfig{
		Required:  true,
		AttestDir: attestDir,
		// Use GT_GATE_ATTEST_DIR so the gate command writes to the same directory
		// the refinery will check after the gate runs.
		Cmd: `mkdir -p "$GT_GATE_ATTEST_DIR" && git rev-parse HEAD^{tree} > "$GT_GATE_ATTEST_DIR/$(git rev-parse HEAD^{tree})"`,
	}

	createFeatureBranch(t, workDir, "feat/attested", "feature.txt", "feature content")

	result := e.doMerge(context.Background(), "feat/attested", "main", "gt-test", nil)
	if !result.Success {
		t.Fatalf("expected merge to succeed when durable gate writes attestation, got: %s", result.Error)
	}

	output := e.output.(*bytes.Buffer).String()
	if !strings.Contains(output, "Durable review attestation recorded") {
		t.Errorf("expected attestation recorded log, got:\n%s", output)
	}

	// The attestation file should exist and be named after the merge-candidate tree.
	tree, err := g.Rev("HEAD^{tree}")
	if err != nil {
		t.Fatalf("resolve merge-candidate tree: %v", err)
	}
	attestationPath := filepath.Join(attestDir, tree)
	if _, err := os.Stat(attestationPath); err != nil {
		t.Errorf("expected attestation file %s to exist: %v", attestationPath, err)
	}
}

// TestDoMerge_DurableReviewGate_ExistingAttestation_SkipsCommand proves that
// when an HMAC attestation already exists for the merge-candidate tree, the
// durable gate command does not need to run again and the merge proceeds.
func TestDoMerge_DurableReviewGate_ExistingAttestation_SkipsCommand(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil

	attestDir := filepath.Join(workDir, "attestations")
	if err := os.MkdirAll(attestDir, 0755); err != nil {
		t.Fatalf("create attest dir: %v", err)
	}
	e.config.DurableReviewGate = &DurableReviewGateConfig{
		Required:  true,
		AttestDir: attestDir,
		// This command would fail if it ran. If the merge succeeds, we know the
		// existing attestation short-circuited the gate.
		Cmd: `echo "gate should not run" >&2; exit 1`,
	}

	createFeatureBranch(t, workDir, "feat/pre-attested", "feature.txt", "feature content")

	// Pre-compute the merge candidate tree by squashing the feature branch onto
	// main in a throwaway commit, then write the attestation before doMerge.
	mustRun(t, workDir, "git", "checkout", "main")
	mustRun(t, workDir, "git", "merge", "--squash", "feat/pre-attested")
	mustRun(t, workDir, "git", "add", ".")
	mustRun(t, workDir, "git", "commit", "-m", "tmp: compute squashed tree")
	tree, err := g.Rev("HEAD^{tree}")
	if err != nil {
		t.Fatalf("resolve merge-candidate tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(attestDir, tree), []byte(fmt.Sprintf("attestation for %s", tree)), 0644); err != nil {
		t.Fatalf("write attestation: %v", err)
	}
	// Remove the throwaway commit so doMerge can perform the real squash merge.
	mustRun(t, workDir, "git", "reset", "--hard", "HEAD~1")

	result := e.doMerge(context.Background(), "feat/pre-attested", "main", "gt-test", nil)
	if !result.Success {
		t.Fatalf("expected merge to succeed with pre-existing attestation, got: %s", result.Error)
	}

	output := e.output.(*bytes.Buffer).String()
	if !strings.Contains(output, "Durable review attestation present") {
		t.Errorf("expected existing attestation log, got:\n%s", output)
	}
	if strings.Contains(output, "gate should not run") {
		t.Error("durable gate command ran despite pre-existing attestation")
	}
}

// TestDoMerge_DurableReviewGate_Disabled_AllowsMerge proves that setting
// Required=false lets the direct merge path proceed without attestation.
func TestDoMerge_DurableReviewGate_Disabled_AllowsMerge(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil
	e.config.DurableReviewGate = &DurableReviewGateConfig{Required: false}

	createFeatureBranch(t, workDir, "feat/disabled", "feature.txt", "feature content")

	result := e.doMerge(context.Background(), "feat/disabled", "main", "gt-test", nil)
	if !result.Success {
		t.Fatalf("expected merge to succeed when durable gate is disabled, got: %s", result.Error)
	}

	output := e.output.(*bytes.Buffer).String()
	if strings.Contains(output, "Durable review gate required") {
		t.Error("durable gate should not run when disabled")
	}
}

// TestDoMerge_DurableReviewGate_LegacyFallback_ReusesPostSquashGate proves that
// when durable_review_gate is required but has no explicit command, the refinery
// reuses an existing post-squash gate that invokes refinery-gate.sh. This keeps
// Gastown's current config operational without requiring a duplicated command.
// The test passes skipGates=true to exercise the pre-verified fast path: the
// normal gates are skipped, but the durable review gate still runs and blocks
// bypass.
func TestDoMerge_DurableReviewGate_LegacyFallback_ReusesPostSquashGate(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false

	attestDir := filepath.Join(workDir, "attestations")
	attestCmd := `mkdir -p "$GT_GATE_ATTEST_DIR" && git rev-parse HEAD^{tree} > "$GT_GATE_ATTEST_DIR/$(git rev-parse HEAD^{tree})"`
	e.config.Gates = map[string]*GateConfig{
		"four-model-refinery-review": {
			Cmd:     "echo 'invoking refinery-gate.sh' && " + attestCmd,
			Timeout: 5 * time.Minute,
			Phase:   GatePhasePostSquash,
		},
	}
	// Required durable review gate with no explicit command: must fall back to
	// the post-squash refinery-gate.sh command.
	e.config.DurableReviewGate = &DurableReviewGateConfig{
		Required:  true,
		AttestDir: attestDir,
	}

	createFeatureBranch(t, workDir, "feat/fallback", "feature.txt", "feature content")

	// skipGates=true simulates a pre-verified MR. The durable gate must still run.
	result := e.doMerge(context.Background(), "feat/fallback", "main", "gt-test", nil, true)
	if !result.Success {
		t.Fatalf("expected pre-verified merge to succeed with legacy fallback, got: %s", result.Error)
	}

	output := e.output.(*bytes.Buffer).String()
	if !strings.Contains(output, "Durable review gate passed") {
		t.Errorf("expected durable gate to run and pass, got:\n%s", output)
	}
	if !strings.Contains(output, "Durable review attestation recorded") {
		t.Errorf("expected durable gate to record attestation after fallback, got:\n%s", output)
	}

	tree, err := g.Rev("HEAD^{tree}")
	if err != nil {
		t.Fatalf("resolve merge-candidate tree: %v", err)
	}
	attestationPath := filepath.Join(attestDir, tree)
	if _, err := os.Stat(attestationPath); err != nil {
		t.Errorf("expected fallback attestation file %s to exist: %v", attestationPath, err)
	}
}

// TestRunDurableReviewGate_EmptyDiff_BlocksMerge proves the empty-diff guard
// (gastown-cet.12.4): when the merge-candidate diff between the branch and
// the target is empty, the durable review gate fails closed regardless of
// what the configured reviewer command returns. A reviewer that produces
// zero findings on a zero-content diff performed no actual review, so the
// gate must not grant an HMAC attestation that would later be treated as
// evidence of approval.
//
// The incident this test pins: m3 returned PASS on the empty gtviz initial
// commit (2abdc645), and the gate treated that zero-content PASS as
// approval, enabling a degraded-quorum bypass merge. The fix hardens the
// gate to refuse to run on an empty diff — the reviewer command is never
// invoked.
//
// We invoke runDurableReviewGate directly rather than through doMerge so the
// guard is exercised at the unit level. In the full doMerge flow, an empty
// diff also fails at the squash-merge step (nothing to commit), but the
// guard is the durable reviewer-specific defense and must work in isolation
// — for example, when the branch tree equals the target tree but the
// squash-merge step somehow succeeds (whitespace-only changes, identical
// tree after rebase, etc.).
func TestRunDurableReviewGate_EmptyDiff_BlocksMerge(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil
	// This command would succeed and write an attestation if it ran. If
	// the gate returns failure, we know the empty-diff guard short-circuited
	// before the gate command was invoked.
	attestDir := filepath.Join(workDir, "attestations")
	e.config.DurableReviewGate = &DurableReviewGateConfig{
		Required:  true,
		AttestDir: attestDir,
		Cmd: `mkdir -p "$GT_GATE_ATTEST_DIR" && git rev-parse HEAD^{tree} > "$GT_GATE_ATTEST_DIR/$(git rev-parse HEAD^{tree})"`,
	}

	// Branch and target point at the same commit — diff is empty.
	mustRun(t, workDir, "git", "branch", "feat/empty", "main")

	result := e.runDurableReviewGate(context.Background(), "feat/empty", "main", nil)
	if result.Success {
		t.Fatal("expected gate to fail when merge-candidate diff is empty")
	}
	if !result.TestsFailed {
		t.Errorf("expected TestsFailed=true for empty-diff refusal, got %+v", result)
	}
	if !strings.Contains(result.Error, "empty diff") {
		t.Errorf("expected empty-diff error in result, got: %s", result.Error)
	}

	output := e.output.(*bytes.Buffer).String()
	if !strings.Contains(output, "empty diff") {
		t.Errorf("expected empty-diff log message, got:\n%s", output)
	}
	// The reviewer command must not have been invoked at all — the
	// empty-diff guard runs before the gate command. We assert this by
	// checking that no attestation file was created.
	entries, err := os.ReadDir(attestDir)
	if err != nil && !os.IsNotExist(err) {
		t.Errorf("could not read attest dir: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			t.Errorf("empty-diff guard must not allow attestation file %s to be written", entry.Name())
		}
	}
	_ = g // keep linter happy about unused variable in some test setups
}

// TestIsEmptyReviewDiff_BranchAndTargetVariations pins the helper that drives
// the empty-diff guard: a missing branch or target is treated as "unknown"
// (returns false) so legitimate first-commit reviews are not blocked. An
// identical branch and target is treated as "empty" (returns true) and the
// gate refuses to grant an attestation.
func TestIsEmptyReviewDiff_BranchAndTargetVariations(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false

	t.Run("missing_branch_returns_false", func(t *testing.T) {
		empty, err := e.isEmptyReviewDiff("", "main")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if empty {
			t.Error("missing branch must not be reported as empty diff")
		}
	})
	t.Run("missing_target_returns_false", func(t *testing.T) {
		empty, err := e.isEmptyReviewDiff("main", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if empty {
			t.Error("missing target must not be reported as empty diff")
		}
	})
	t.Run("identical_refs_returns_true", func(t *testing.T) {
		// main...main triple-dot is empty by definition.
		empty, err := e.isEmptyReviewDiff("main", "main")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !empty {
			t.Error("identical branch and target must be reported as empty diff")
		}
	})
}

func mustRun(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmdOut, err := runWithError(dir, name, args...)
	if err != nil {
		t.Fatalf("command %s %v failed: %v\n%s", name, args, err, cmdOut)
	}
	return cmdOut
}

func runWithError(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
