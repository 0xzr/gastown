package cmd

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/refinery"
)

// hmacHex reproduces the attestation token (HMAC-SHA256 of treeSHA keyed by the
// raw key bytes) so a test can mint a token for a tree without importing the
// unexported refinery.computeAttestationToken.
func hmacHex(treeSHA string, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(treeSHA))
	return hex.EncodeToString(mac.Sum(nil))
}

// cmdAttestationEnv isolates attestation key/dir state for a cmd-package test
// (mirrors refinery.attestationTestEnv without crossing package boundaries).
func cmdAttestationEnv(t *testing.T) (dir, keyPath string) {
	t.Helper()
	dir = t.TempDir()
	keyPath = filepath.Join(dir, "hmac-key")
	key := strings.Repeat("k", 64) + "\n"
	if err := os.WriteFile(keyPath, []byte(key), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	attestDir := filepath.Join(dir, "attestations")
	t.Setenv("GT_GATE_HMAC_KEY", keyPath)
	t.Setenv("GT_GATE_ATTEST_DIR", attestDir)
	return attestDir, keyPath
}

func TestClassifyAttestation_NoAtestedTree_NoMergeCommit(t *testing.T) {
	cmdAttestationEnv(t)
	fields := &beads.MRFields{CloseReason: "merged"}

	row, unattested := classifyAttestation(fields)
	if !unattested {
		t.Fatal("expected unattested=true for merged MR with no tree/commit")
	}
	if row.Reason != "no-attested-tree" {
		t.Fatalf("reason = %q, want no-attested-tree", row.Reason)
	}
}

func TestClassifyAttestation_HasAttestedTree_ButTokenMissing(t *testing.T) {
	// The MR recorded an attested_tree, but no token exists for it (e.g. the
	// panel never produced proof, or the store was wiped). This is the
	// "Opus unavailable / core peer unavailable" outcome surfacing in the report.
	cmdAttestationEnv(t)
	fields := &beads.MRFields{
		CloseReason: "merged",
		AttestedTree: "aa11bb22cc33dd44aa11bb22cc33dd44aa11bb22",
	}

	row, unattested := classifyAttestation(fields)
	if !unattested {
		t.Fatal("expected unattested=true when token is missing")
	}
	if row.Reason != "token-missing" {
		t.Fatalf("reason = %q, want token-missing", row.Reason)
	}
}

func TestClassifyAttestation_HasAttestedTree_InvalidToken(t *testing.T) {
	// A token file exists but verifies against a different tree — tampering or
	// a stale token. The report must flag it as invalid, not pass it.
	dir, _ := cmdAttestationEnv(t)
	tree := "bb22cc33dd44ee55bb22cc33dd44ee55bb22cc33"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Token valid for a DIFFERENT tree under the same key.
	other := strings.Repeat("00", 20)
	keyBytes, _ := os.ReadFile(os.Getenv("GT_GATE_HMAC_KEY"))
	otherToken := hmacHex(other, keyBytes)
	if err := os.WriteFile(filepath.Join(dir, tree), []byte(otherToken+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	fields := &beads.MRFields{CloseReason: "merged", AttestedTree: tree}

	row, unattested := classifyAttestation(fields)
	if !unattested {
		t.Fatal("expected unattested=true for invalid token")
	}
	if row.Reason != "token-invalid" {
		t.Fatalf("reason = %q, want token-invalid", row.Reason)
	}
}

func TestClassifyAttestation_ValidToken_NotListed(t *testing.T) {
	// A completed MR with a valid token for its attested_tree is NOT unattested
	// — the report must not flag it.
	dir, _ := cmdAttestationEnv(t)
	tree := "cc33dd44ee55ff66cc33dd44ee55ff66cc33dd44"
	if err := refinery.WriteAttestationToken(tree); err != nil {
		t.Fatalf("write token: %v", err)
	}
	_ = dir
	fields := &beads.MRFields{CloseReason: "merged", AttestedTree: tree}

	if _, unattested := classifyAttestation(fields); unattested {
		t.Fatal("expected unattested=false for valid token")
	}
}

func TestClassifyAttestation_NonMergedCloseReason_Skipped(t *testing.T) {
	// An MR closed for rejection/conflict/superseded is not "completed work" —
	// it never landed on main, so it is not subject to attestation. The caller
	// (collectUnattestedMRs) filters on CloseReason=="merged", but classify is
	// also robust if invoked directly with a non-merged reason: a present valid
	// token still reports not-unattested (proof exists either way).
	dir, _ := cmdAttestationEnv(t)
	tree := "dd44ee55ff660011dd44ee55ff660011dd44ee55"
	if err := refinery.WriteAttestationToken(tree); err != nil {
		t.Fatalf("write token: %v", err)
	}
	_ = dir
	fields := &beads.MRFields{CloseReason: "rejected", AttestedTree: tree}

	if _, unattested := classifyAttestation(fields); unattested {
		t.Fatal("expected not-unattested for rejected MR with valid token")
	}
}
