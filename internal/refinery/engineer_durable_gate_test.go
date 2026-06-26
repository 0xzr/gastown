package refinery

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	keyPath := writeTestHMACKey(t, workDir)
	e.config.DurableReviewGate = &DurableReviewGateConfig{
		Required:    true,
		Cmd:         `echo "reviewer rejection: missing tests" >&2; exit 1`,
		HMACKeyPath: keyPath,
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
	keyPath := writeTestHMACKey(t, workDir)
	e.config.DurableReviewGate = &DurableReviewGateConfig{
		Required:    true,
		HMACKeyPath: keyPath,
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
	keyPath := writeTestHMACKey(t, workDir)
	e.config.DurableReviewGate = &DurableReviewGateConfig{
		Required:    true,
		AttestDir:   attestDir,
		HMACKeyPath: keyPath,
		// Use GT_GATE_ATTEST_DIR so the gate command writes to the same directory
		// the refinery will check after the gate runs.
		Cmd: hmacAttestationShellCmd(t),
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

// TestDoMerge_DurableReviewGate_ExportsAssignedWriter proves the durable gate
// excludes the implementer's model, not the refinery process model. The source
// issue's persisted model assignment is used when the agent bead does not carry
// assigned_agent.
func TestDoMerge_DurableReviewGate_ExportsAssignedWriter(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil

	sourceIssue := "gt-src"
	townRoot := filepath.Dir(workDir)
	writeTownMarker(t, townRoot)
	assignmentDir := filepath.Join(townRoot, ".runtime", "model-assignments")
	if err := os.MkdirAll(assignmentDir, 0755); err != nil {
		t.Fatalf("create model assignment dir: %v", err)
	}
	assignment := `{"bead":"gt-src","target":"test-rig","agent":"umans-kimi","source":"test"}`
	if err := os.WriteFile(filepath.Join(assignmentDir, sourceIssue+".json"), []byte(assignment), 0600); err != nil {
		t.Fatalf("write model assignment: %v", err)
	}

	attestDir := filepath.Join(workDir, "attestations")
	keyPath := writeTestHMACKey(t, workDir)
	e.config.DurableReviewGate = &DurableReviewGateConfig{
		Required:    true,
		AttestDir:   attestDir,
		HMACKeyPath: keyPath,
		Cmd:         `test "$GT_REVIEW_GATE_WRITER" = "umans-kimi" && ` + hmacAttestationShellCmd(t),
	}

	createFeatureBranch(t, workDir, "feat/writer", "feature.txt", "feature content")

	result := e.doMerge(context.Background(), "feat/writer", "main", sourceIssue, &MRInfo{SourceIssue: sourceIssue})
	if !result.Success {
		t.Fatalf("expected merge to succeed when durable gate receives assigned writer, got: %s", result.Error)
	}
	output := e.output.(*bytes.Buffer).String()
	if !strings.Contains(output, "writer=umans-kimi") {
		t.Errorf("expected durable gate log to include assigned writer, got:\n%s", output)
	}
}

func TestDurableReviewWriterFromAssignmentRejectsUnsafeSourceIssue(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)

	townRoot := filepath.Dir(workDir)
	writeTownMarker(t, townRoot)
	assignmentDir := filepath.Join(townRoot, ".runtime", "model-assignments")
	if err := os.MkdirAll(assignmentDir, 0755); err != nil {
		t.Fatalf("create model assignment dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assignmentDir, "evil.json"), []byte(`{"agent":"m3"}`), 0600); err != nil {
		t.Fatalf("write model assignment: %v", err)
	}

	if got := e.durableReviewWriterFromAssignment("../evil"); got != "" {
		t.Fatalf("unsafe source issue resolved writer %q, want empty", got)
	}
	if got := e.durableReviewWriterFromAssignment("gt..src"); got != "" {
		t.Fatalf("ambiguous source issue resolved writer %q, want empty", got)
	}
	if got := e.durableReviewWriter(&MRInfo{SourceIssue: "../evil"}); got != "unknown" {
		t.Fatalf("unsafe source issue writer = %q, want unknown", got)
	}
}

func TestDurableReviewWriterFromAssignmentRequiresTownMarker(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)

	townRoot := filepath.Dir(workDir)
	assignmentDir := filepath.Join(townRoot, ".runtime", "model-assignments")
	if err := os.MkdirAll(assignmentDir, 0755); err != nil {
		t.Fatalf("create model assignment dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assignmentDir, "gt-src.json"), []byte(`{"agent":"m3"}`), 0600); err != nil {
		t.Fatalf("write model assignment: %v", err)
	}

	if got := e.durableReviewWriterFromAssignment("gt-src"); got != "" {
		t.Fatalf("assignment without town marker resolved writer %q, want empty", got)
	}
	if got := e.durableReviewWriter(&MRInfo{SourceIssue: "gt-src"}); got != "unknown" {
		t.Fatalf("assignment without town marker writer = %q, want unknown", got)
	}
}

func TestDurableReviewTownRootResolvesSymlinkedRigPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permission behavior is platform-specific")
	}

	workDir, g, _ := testGitRepo(t)
	townRoot := filepath.Dir(workDir)
	writeTownMarker(t, townRoot)

	linkRoot := t.TempDir()
	linkPath := filepath.Join(linkRoot, "work-link")
	if err := os.Symlink(workDir, linkPath); err != nil {
		t.Fatalf("create rig path symlink: %v", err)
	}

	e := newTestEngineer(t, workDir, g)
	e.rig.Path = linkPath

	got, ok := e.durableReviewTownRoot()
	if !ok {
		t.Fatal("expected symlinked rig path to resolve to town root")
	}
	if got != townRoot {
		t.Fatalf("durableReviewTownRoot() = %q, want %q", got, townRoot)
	}
}

func TestGateCommandEnvWithOverridesExistingMetadata(t *testing.T) {
	t.Setenv("GT_GATE_HMAC_KEY", "/old/key")
	t.Setenv("GT_REVIEW_GATE_WRITER", "old-writer")

	env := gateCommandEnvWith(
		"GT_GATE_HMAC_KEY=/new/key",
		"GT_REVIEW_GATE_WRITER=umans-kimi",
	)

	values := map[string][]string{}
	for _, kv := range env {
		key, value, ok := strings.Cut(kv, "=")
		if ok {
			values[key] = append(values[key], value)
		}
	}
	if got := values["GT_GATE_HMAC_KEY"]; len(got) != 1 || got[0] != "/new/key" {
		t.Fatalf("GT_GATE_HMAC_KEY env = %v, want [/new/key]", got)
	}
	if got := values["GT_REVIEW_GATE_WRITER"]; len(got) != 1 || got[0] != "umans-kimi" {
		t.Fatalf("GT_REVIEW_GATE_WRITER env = %v, want [umans-kimi]", got)
	}
}

// TestDoMerge_DurableReviewGate_InvalidAttestation_BlocksMerge proves that a
// file named after the merge-candidate tree is not enough; the token contents
// must verify against the HMAC key.
func TestDoMerge_DurableReviewGate_InvalidAttestation_BlocksMerge(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil

	attestDir := filepath.Join(workDir, "attestations")
	if err := os.MkdirAll(attestDir, 0755); err != nil {
		t.Fatalf("create attest dir: %v", err)
	}
	keyPath := writeTestHMACKey(t, workDir)
	e.config.DurableReviewGate = &DurableReviewGateConfig{
		Required:    true,
		AttestDir:   attestDir,
		HMACKeyPath: keyPath,
		Cmd:         `echo "gate should not run" >&2; exit 1`,
	}

	createFeatureBranch(t, workDir, "feat/bad-attest", "feature.txt", "feature content")

	mustRun(t, workDir, "git", "checkout", "main")
	mustRun(t, workDir, "git", "merge", "--squash", "feat/bad-attest")
	mustRun(t, workDir, "git", "add", ".")
	mustRun(t, workDir, "git", "commit", "-m", "tmp: compute squashed tree")
	tree, err := g.Rev("HEAD^{tree}")
	if err != nil {
		t.Fatalf("resolve merge-candidate tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(attestDir, tree), []byte("not-a-valid-hmac-token"), 0644); err != nil {
		t.Fatalf("write invalid attestation: %v", err)
	}
	mustRun(t, workDir, "git", "reset", "--hard", "HEAD~1")

	result := e.doMerge(context.Background(), "feat/bad-attest", "main", "gt-test", nil)
	if result.Success {
		t.Fatal("expected merge to fail with invalid HMAC attestation")
	}
	if !strings.Contains(result.Error, "attestation check failed") {
		t.Errorf("expected invalid attestation check error, got: %s", result.Error)
	}
}

// TestDoMerge_DurableReviewGate_InsecureHMACKey_BlocksMerge proves that the
// refinery refuses to trust attestations when the shared HMAC key can be read
// by group/other users.
func TestDoMerge_DurableReviewGate_InsecureHMACKey_BlocksMerge(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil

	keyPath := filepath.Join(t.TempDir(), "insecure-hmac-key")
	if err := os.WriteFile(keyPath, []byte(testHMACKeyMaterial), 0644); err != nil {
		t.Fatalf("write insecure HMAC key: %v", err)
	}
	e.config.DurableReviewGate = &DurableReviewGateConfig{
		Required:    true,
		Cmd:         `echo "gate should not run" >&2; exit 1`,
		HMACKeyPath: keyPath,
	}

	createFeatureBranch(t, workDir, "feat/insecure-key", "feature.txt", "feature content")

	result := e.doMerge(context.Background(), "feat/insecure-key", "main", "gt-test", nil)
	if result.Success {
		t.Fatal("expected merge to fail when durable review HMAC key is group/other readable")
	}
	if !strings.Contains(result.Error, "HMAC key check failed") {
		t.Errorf("expected HMAC key check failure, got: %s", result.Error)
	}
	output := e.output.(*bytes.Buffer).String()
	if strings.Contains(output, "gate should not run") {
		t.Error("durable gate command ran despite insecure HMAC key")
	}
}

func TestDoMerge_DurableReviewGate_WhitespaceHMACKey_BlocksMerge(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil

	keyPath := filepath.Join(t.TempDir(), "whitespace-hmac-key")
	if err := os.WriteFile(keyPath, []byte(strings.Repeat(" ", minDurableReviewHMACKeyBytes)+"\n"), 0600); err != nil {
		t.Fatalf("write whitespace HMAC key: %v", err)
	}
	e.config.DurableReviewGate = &DurableReviewGateConfig{
		Required:    true,
		Cmd:         `echo "gate should not run" >&2; exit 1`,
		HMACKeyPath: keyPath,
	}

	createFeatureBranch(t, workDir, "feat/weak-key", "feature.txt", "feature content")

	result := e.doMerge(context.Background(), "feat/weak-key", "main", "gt-test", nil)
	if result.Success {
		t.Fatal("expected merge to fail when durable review HMAC key is whitespace-only")
	}
	if !strings.Contains(result.Error, "non-whitespace bytes") {
		t.Errorf("expected non-whitespace HMAC key failure, got: %s", result.Error)
	}
	output := e.output.(*bytes.Buffer).String()
	if strings.Contains(output, "gate should not run") {
		t.Error("durable gate command ran despite whitespace-only HMAC key")
	}
}

func TestDoMerge_DurableReviewGate_PaddedLowEntropyHMACKey_BlocksMerge(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil

	keyPath := filepath.Join(t.TempDir(), "padded-low-entropy-hmac-key")
	if err := os.WriteFile(keyPath, []byte("x"+strings.Repeat(" ", minDurableReviewHMACKeyBytes)+"\n"), 0600); err != nil {
		t.Fatalf("write padded low-entropy HMAC key: %v", err)
	}
	e.config.DurableReviewGate = &DurableReviewGateConfig{
		Required:    true,
		Cmd:         `echo "gate should not run" >&2; exit 1`,
		HMACKeyPath: keyPath,
	}

	createFeatureBranch(t, workDir, "feat/padded-low-entropy-key", "feature.txt", "feature content")

	result := e.doMerge(context.Background(), "feat/padded-low-entropy-key", "main", "gt-test", nil)
	if result.Success {
		t.Fatal("expected merge to fail when durable review HMAC key has too little non-whitespace material")
	}
	if !strings.Contains(result.Error, "at least") {
		t.Errorf("expected minimum HMAC key length failure, got: %s", result.Error)
	}
	output := e.output.(*bytes.Buffer).String()
	if strings.Contains(output, "gate should not run") {
		t.Error("durable gate command ran despite padded low-entropy HMAC key")
	}
}

func TestDoMerge_DurableReviewGate_SymlinkHMACKey_BlocksMerge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permission behavior is platform-specific")
	}
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil

	realKey := writeTestHMACKey(t, workDir)
	keyPath := filepath.Join(t.TempDir(), "hmac-key-link")
	if err := os.Symlink(realKey, keyPath); err != nil {
		t.Fatalf("create HMAC key symlink: %v", err)
	}
	e.config.DurableReviewGate = &DurableReviewGateConfig{
		Required:    true,
		Cmd:         `echo "gate should not run" >&2; exit 1`,
		HMACKeyPath: keyPath,
	}

	createFeatureBranch(t, workDir, "feat/symlink-key", "feature.txt", "feature content")

	result := e.doMerge(context.Background(), "feat/symlink-key", "main", "gt-test", nil)
	if result.Success {
		t.Fatal("expected merge to fail when durable review HMAC key is a symlink")
	}
	if !strings.Contains(result.Error, "must not be a symlink") {
		t.Errorf("expected symlink HMAC key failure, got: %s", result.Error)
	}
}

func TestDoMerge_DurableReviewGate_SymlinkAttestation_BlocksMerge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permission behavior is platform-specific")
	}
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil

	attestDir := filepath.Join(workDir, "attestations")
	if err := os.MkdirAll(attestDir, 0755); err != nil {
		t.Fatalf("create attest dir: %v", err)
	}
	keyPath := writeTestHMACKey(t, workDir)
	e.config.DurableReviewGate = &DurableReviewGateConfig{
		Required:    true,
		AttestDir:   attestDir,
		HMACKeyPath: keyPath,
		Cmd:         `echo "gate should not run" >&2; exit 1`,
	}

	createFeatureBranch(t, workDir, "feat/symlink-attest", "feature.txt", "feature content")

	mustRun(t, workDir, "git", "checkout", "main")
	mustRun(t, workDir, "git", "merge", "--squash", "feat/symlink-attest")
	mustRun(t, workDir, "git", "add", ".")
	mustRun(t, workDir, "git", "commit", "-m", "tmp: compute squashed tree")
	tree, err := g.Rev("HEAD^{tree}")
	if err != nil {
		t.Fatalf("resolve merge-candidate tree: %v", err)
	}
	realToken := filepath.Join(t.TempDir(), "real-token")
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read HMAC key: %v", err)
	}
	key = bytes.TrimRight(key, "\r\n")
	token := hex.EncodeToString(expectedDurableReviewAttestationWithKey(key, tree, "unknown"))
	if err := os.WriteFile(realToken, []byte(token+"\n"), 0644); err != nil {
		t.Fatalf("write real token: %v", err)
	}
	if err := os.Symlink(realToken, filepath.Join(attestDir, tree)); err != nil {
		t.Fatalf("create attestation symlink: %v", err)
	}
	mustRun(t, workDir, "git", "reset", "--hard", "HEAD~1")

	result := e.doMerge(context.Background(), "feat/symlink-attest", "main", "gt-test", nil)
	if result.Success {
		t.Fatal("expected merge to fail when durable review attestation is a symlink")
	}
	if !strings.Contains(result.Error, "must not be a symlink") {
		t.Errorf("expected symlink attestation failure, got: %s", result.Error)
	}
	output := e.output.(*bytes.Buffer).String()
	if strings.Contains(output, "gate should not run") {
		t.Error("durable gate command ran despite symlinked attestation")
	}
}

func TestDurableReviewGateTimeoutDefaults(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.DurableReviewGate = &DurableReviewGateConfig{Required: true}
	if got := e.durableReviewGateTimeout(); got != DefaultDurableReviewGateTimeout {
		t.Fatalf("durableReviewGateTimeout() = %v, want %v", got, DefaultDurableReviewGateTimeout)
	}
	e.config.DurableReviewGate.Timeout = 2 * time.Minute
	if got := e.durableReviewGateTimeout(); got != 2*time.Minute {
		t.Fatalf("durableReviewGateTimeout(configured) = %v", got)
	}
	if got := DefaultMergeQueueConfig().DurableReviewGate.Timeout; got != DefaultDurableReviewGateTimeout {
		t.Fatalf("DefaultMergeQueueConfig durable timeout = %v, want %v", got, DefaultDurableReviewGateTimeout)
	}
}

func TestDurableReviewWriterFromAssignmentRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permission behavior is platform-specific")
	}
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)

	townRoot := filepath.Dir(workDir)
	writeTownMarker(t, townRoot)
	assignmentDir := filepath.Join(townRoot, ".runtime", "model-assignments")
	if err := os.MkdirAll(assignmentDir, 0755); err != nil {
		t.Fatalf("create model assignment dir: %v", err)
	}
	realAssignment := filepath.Join(t.TempDir(), "assignment.json")
	if err := os.WriteFile(realAssignment, []byte(`{"agent":"umans-kimi"}`), 0600); err != nil {
		t.Fatalf("write real assignment: %v", err)
	}
	if err := os.Symlink(realAssignment, filepath.Join(assignmentDir, "gt-src.json")); err != nil {
		t.Fatalf("create assignment symlink: %v", err)
	}

	if got := e.durableReviewWriterFromAssignment("gt-src"); got != "" {
		t.Fatalf("symlinked assignment resolved writer %q, want empty", got)
	}
	if got := e.durableReviewWriter(&MRInfo{SourceIssue: "gt-src"}); got != "unknown" {
		t.Fatalf("symlinked assignment writer = %q, want unknown", got)
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
	keyPath := writeTestHMACKey(t, workDir)
	e.config.DurableReviewGate = &DurableReviewGateConfig{
		Required:    true,
		AttestDir:   attestDir,
		HMACKeyPath: keyPath,
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
	writeHMACToken(t, attestDir, keyPath, tree, "unknown")
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

func TestDoMerge_DurableReviewGate_AttestationIsBoundToWriter(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil

	attestDir := filepath.Join(workDir, "attestations")
	if err := os.MkdirAll(attestDir, 0755); err != nil {
		t.Fatalf("create attest dir: %v", err)
	}
	keyPath := writeTestHMACKey(t, workDir)
	e.config.DurableReviewGate = &DurableReviewGateConfig{
		Required:    true,
		AttestDir:   attestDir,
		HMACKeyPath: keyPath,
		Cmd:         `echo "wrong-writer attestation did not short-circuit" >&2; exit 1`,
	}

	createFeatureBranch(t, workDir, "feat/writer-bound", "feature.txt", "feature content")

	mustRun(t, workDir, "git", "checkout", "main")
	mustRun(t, workDir, "git", "merge", "--squash", "feat/writer-bound")
	mustRun(t, workDir, "git", "add", ".")
	mustRun(t, workDir, "git", "commit", "-m", "tmp: compute squashed tree")
	tree, err := g.Rev("HEAD^{tree}")
	if err != nil {
		t.Fatalf("resolve merge-candidate tree: %v", err)
	}
	writeHMACToken(t, attestDir, keyPath, tree, "codex")
	mustRun(t, workDir, "git", "reset", "--hard", "HEAD~1")

	result := e.doMerge(context.Background(), "feat/writer-bound", "main", "gt-test", nil)
	if result.Success {
		t.Fatal("expected merge to fail when attestation was signed for a different writer")
	}
	if !strings.Contains(result.Error, "wrong-writer attestation did not short-circuit") {
		t.Errorf("expected durable gate command to run after writer mismatch, got: %s", result.Error)
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
	keyPath := writeTestHMACKey(t, workDir)
	attestCmd := hmacAttestationShellCmd(t)
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
		Required:    true,
		AttestDir:   attestDir,
		HMACKeyPath: keyPath,
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

const testHMACKeyMaterial = "test-hmac-key-material-with-at-least-32-bytes"

func writeTestHMACKey(t *testing.T, workDir string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hmac-key")
	if err := os.WriteFile(path, []byte(testHMACKeyMaterial+"\n"), 0600); err != nil {
		t.Fatalf("write HMAC key: %v", err)
	}
	return path
}

func writeTownMarker(t *testing.T, townRoot string) {
	t.Helper()
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("create mayor dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write town marker: %v", err)
	}
}

func writeHMACToken(t *testing.T, attestDir, keyPath, tree, writer string) {
	t.Helper()
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read HMAC key: %v", err)
	}
	key = bytes.TrimRight(key, "\r\n")
	token := hex.EncodeToString(expectedDurableReviewAttestationWithKey(key, tree, writer))
	if err := os.WriteFile(filepath.Join(attestDir, tree), []byte(token+"\n"), 0644); err != nil {
		t.Fatalf("write HMAC attestation: %v", err)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func hmacAttestationShellCmd(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf(`mkdir -p "$GT_GATE_ATTEST_DIR" && tree="$(git rev-parse HEAD^{tree})" && GT_HMAC_ATTESTATION_HELPER=1 %s -test.run=TestHMACAttestationHelper -- "$tree" > "$GT_GATE_ATTEST_DIR/$tree"`, shellQuote(os.Args[0]))
}

func TestHMACAttestationHelper(t *testing.T) {
	if os.Getenv("GT_HMAC_ATTESTATION_HELPER") != "1" {
		return
	}
	if len(os.Args) == 0 {
		fmt.Fprintln(os.Stderr, "missing argv")
		os.Exit(2)
	}
	tree := os.Args[len(os.Args)-1]
	if tree == "" || strings.HasPrefix(tree, "-") {
		fmt.Fprintln(os.Stderr, "usage: TestHMACAttestationHelper -- <tree>")
		os.Exit(2)
	}
	keyPath := os.Getenv("GT_GATE_HMAC_KEY")
	if keyPath == "" {
		fmt.Fprintln(os.Stderr, "GT_GATE_HMAC_KEY is required")
		os.Exit(2)
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	key = bytes.TrimRight(key, "\r\n")
	writer := os.Getenv("GT_REVIEW_GATE_WRITER")
	fmt.Println(hex.EncodeToString(expectedDurableReviewAttestationWithKey(key, tree, writer)))
	os.Exit(0)
}
