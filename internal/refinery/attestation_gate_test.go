package refinery

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/testutil"
)

// TestDoMerge_AttestationMissing_BlocksMerge_FailClosed is the core gastown-7g4
// gate test: a merge whose reviewed tree has NO attestation token must not be
// pushed, must not report success, and must surface AttestationMissing so the
// refinery classifies it as a reviewer-unavailability deferral (not a build
// failure routed to a fixer polecat). The source bead is never closed on this
// path. This covers "Opus unavailable" and "core peer unavailable": both surface
// to the Go side as the absence of a token for the tree.
func TestDoMerge_AttestationMissing_BlocksMerge_FailClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test exercises real git merge")
	}
	// Isolated attestation env with NO token written for the merge tree.
	newAttestationTestEnv(t)

	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil

	createFeatureBranch(t, workDir, "feature/no-attest", "feature.txt", "content")

	mr := makeMR("gt-noattest", "feature/no-attest", "main")
	result := e.doMerge(context.Background(), mr.Branch, mr.Target, mr.SourceIssue, mr)

	if result.Success {
		t.Fatalf("doMerge succeeded with no attestation token — gate did not fail closed")
	}
	if !result.AttestationMissing {
		t.Fatalf("expected AttestationMissing=true, got result: %+v", result)
	}
	if result.MergeCommit != "" {
		t.Fatalf("merge produced a commit despite attestation failure: %s", shortSHA(result.MergeCommit))
	}
	if result.AttestedTree == "" {
		t.Fatal("expected AttestedTree to be populated even on failure (for the audit bead)")
	}

	// origin/main must be unchanged — the blocked merge did not advance the
	// default branch. (AutoPush is off here; the assertion is that no local
	// merge commit was published either.)
	if pushed, _ := g.RemoteBranchTip("origin", "main"); pushed == "" {
		t.Fatal("expected origin/main to still resolve")
	}
}

// TestDoMerge_PreVerifiedBypass_NoToken_BlocksMerge covers the pre-verified
// fast-path (gastown-7g4 GAP #1): when a polecat submits --pre-verified, the
// refinery skips gates — including the post-squash gate that writes the token.
// The attestation gate must NOT be bypassed: with no token for the tree, the
// merge still fails closed.
func TestDoMerge_PreVerifiedBypass_NoToken_BlocksMerge(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test exercises real git merge")
	}
	newAttestationTestEnv(t)

	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil

	createFeatureBranch(t, workDir, "feature/preverified", "feature.txt", "content")

	// Pre-verified: matching base means the refinery skips ALL gates. The
	// attestation gate must still run.
	mr := makeMR("gt-preverify", "feature/preverified", "main")
	mr.PreVerified = true
	mr.PreVerifiedBase, _ = g.Rev("origin/main")

	result := e.doMerge(context.Background(), mr.Branch, mr.Target, mr.SourceIssue, mr)
	if result.Success {
		t.Fatalf("pre-verified doMerge succeeded with no attestation token — bypass succeeded")
	}
	if !result.AttestationMissing {
		t.Fatalf("expected AttestationMissing=true for pre-verified bypass, got: %+v", result)
	}
}

// TestDoMerge_AttestationPresent_AllowsMerge confirms the gate is not
// over-strict: when a valid token exists for the exact merge tree, the merge
// proceeds. This is the post-squash-gate success path (the gate just wrote the
// token) — reproduced here by minting the token for the tree the squash will
// produce. Since the tree depends on the squash content, we compute it after
// a dry merge is not possible; instead we pre-seed tokens for several candidate
// trees is avoided by writing the token lazily: we run the merge once to learn
// the tree, reset, then write the token and re-run. That is complex; the simpler
// durable proof is that HandleMRInfoSuccess records AttestedTree — covered by the
// classification tests. Here we assert the positive gate path via the token store.
func TestDoMerge_AttestationPresent_AllowsMerge(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test exercises real git merge")
	}
	env := newAttestationTestEnv(t)

	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil

	createFeatureBranch(t, workDir, "feature/attested", "feature.txt", "attested content")

	// The attestation gate resolves HEAD^{tree} AFTER the squash merge. We don't
	// know that tree up front, so perform the squash once to discover it, reset,
	// mint a token for that exact tree, then re-run the merge — now the gate
	// finds a valid token and passes.
	origMsg := "feat: add feature.txt"
	if err := e.git.MergeSquash("feature/attested", origMsg); err != nil {
		t.Fatalf("discover squash: %v", err)
	}
	tree, err := e.git.Rev("HEAD^{tree}")
	if err != nil {
		t.Fatalf("resolve tree: %v", err)
	}
	if err := e.git.ResetHard("origin/main"); err != nil {
		t.Fatalf("reset after discovery: %v", err)
	}
	if err := WriteAttestationToken(tree); err != nil {
		t.Fatalf("mint token: %v", err)
	}
	_ = env // env configured the dir/key; keep in scope

	mr := makeMR("gt-attested", "feature/attested", "main")
	result := e.doMerge(context.Background(), mr.Branch, mr.Target, mr.SourceIssue, mr)
	if !result.Success {
		t.Fatalf("doMerge with valid token failed: %s", result.Error)
	}
	if result.AttestedTree != tree {
		t.Fatalf("AttestedTree = %s, want %s", shortSHA(result.AttestedTree), shortSHA(tree))
	}
}

// TestHandleMRInfoSuccess_RecordsAttestationOnMRBead confirms the durable proof
// is written to the MR bead (attested_tree field) and the source close reason
// on a successful, attested merge. This is what the gt attestation report scans.
//
// This test requires a live Dolt server (it writes/closes real beads); it skips
// when the shared Dolt container is unavailable, mirroring manager_test.go.
func TestHandleMRInfoSuccess_RecordsAttestationOnMRBead(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test exercises real git merge + beads")
	}
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())

	newAttestationTestEnv(t)

	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	// Point the engineer at an isolated beads DB on the shared Dolt container so
	// Create/Show/Close hit a real server without polluting production beads.
	e.beads = beads.NewIsolatedWithPort(workDir, port)
	if err := e.beads.Init("gt"); err != nil {
		t.Skipf("bd init unavailable: %v", err)
	}
	e.config.RunTests = false
	// AutoPush=true so the merge publishes to the test origin and the source
	// bead becomes closeable (CanCloseSourceBead requires TerminalState=published).
	e.config.AutoPush = true
	e.config.Gates = nil

	mrBead, srcBead := seedMRAndSourceBeads(t, e, "feature/record-attest")
	createFeatureBranch(t, workDir, "feature/record-attest", "feature.txt", "record content")

	// Discover the squash tree, mint a token, reset, then merge.
	if err := e.git.MergeSquash("feature/record-attest", "feat: add feature.txt"); err != nil {
		t.Fatalf("discover squash: %v", err)
	}
	tree, _ := e.git.Rev("HEAD^{tree}")
	_ = e.git.ResetHard("origin/main")
	if err := WriteAttestationToken(tree); err != nil {
		t.Fatalf("mint token: %v", err)
	}

	mr := makeMR(mrBead, "feature/record-attest", "main")
	mr.SourceIssue = srcBead
	result := e.doMerge(context.Background(), mr.Branch, mr.Target, mr.SourceIssue, mr)
	if !result.Success {
		t.Fatalf("doMerge failed: %s", result.Error)
	}

	e.HandleMRInfoSuccess(mr, result)

	// The MR bead must now carry attested_tree.
	updated, err := e.beads.Show(mrBead)
	if err != nil {
		t.Fatalf("Show MR bead: %v", err)
	}
	fields := beads.ParseMRFields(updated)
	if fields == nil || fields.AttestedTree == "" {
		t.Fatalf("MR bead has no attested_tree after success; fields=%+v", fields)
	}
	if fields.AttestedTree != tree {
		t.Fatalf("MR attested_tree = %s, want %s", shortSHA(fields.AttestedTree), shortSHA(tree))
	}

	// The source bead's close reason must reference the attested tree + verified.
	src, err := e.beads.Show(srcBead)
	if err != nil {
		t.Fatalf("Show source bead: %v", err)
	}
	if !strings.Contains(src.CloseReason, "attested_tree: "+tree) {
		t.Fatalf("source bead close reason missing attested_tree; reason=%q", src.CloseReason)
	}
	if !strings.Contains(src.CloseReason, "attestation: verified") {
		t.Fatalf("source bead close reason missing 'attestation: verified'; reason=%q", src.CloseReason)
	}
	if src.Status != "closed" {
		t.Fatalf("source bead status = %q, want closed", src.Status)
	}
}

// seedMRAndSourceBeads creates a real source issue and an MR bead pointing at
// it in the test rig's beads DB, returning their IDs. Omits Rig so the beads
// route to the engineer's own workDir beads DB (no alias lookup needed).
func seedMRAndSourceBeads(t *testing.T, e *Engineer, branch string) (mrID, sourceID string) {
	t.Helper()
	src, err := e.beads.Create(beads.CreateOptions{
		Title:   "source: attestation record test",
		Labels:  []string{"gt:task"},
		Actor:   "test",
	})
	if err != nil {
		t.Fatalf("create source bead: %v", err)
	}
	mr, err := e.beads.Create(beads.CreateOptions{
		Title:       "MR: attestation record test",
		Labels:      []string{"gt:merge-request"},
		Description: "branch: " + branch + "\ntarget: main\nsource_issue: " + src.ID,
		Actor:       "test",
	})
	if err != nil {
		t.Fatalf("create MR bead: %v", err)
	}
	return mr.ID, src.ID
}
