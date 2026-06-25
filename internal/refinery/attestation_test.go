package refinery

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// attestationTestEnv isolates attestation key/dir state for a test. It sets the
// GT_GATE_HMAC_KEY / GT_GATE_ATTEST_DIR env vars to a temp key file + dir and
// restores the originals on cleanup, so tests never touch the real server key.
type attestationTestEnv struct {
	t        *testing.T
	keyPath  string
	dir      string
	keyBytes []byte
	oldKey   string
	oldDir   string
}

// newAttestationTestEnv writes a fresh key file (with a trailing newline, exactly
// as the bash producer expects) and points the env vars at it.
func newAttestationTestEnv(t *testing.T) *attestationTestEnv {
	t.Helper()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "test-hmac-key")
	// 64-char key + trailing newline = 65 bytes, matching the real key file shape.
	key := strings.Repeat("k", 64) + "\n"
	if err := os.WriteFile(keyPath, []byte(key), 0o600); err != nil {
		t.Fatalf("write test key: %v", err)
	}
	attestDir := filepath.Join(dir, "attestations")
	env := &attestationTestEnv{
		t: t, keyPath: keyPath, dir: attestDir, keyBytes: []byte(key),
		oldKey: os.Getenv("GT_GATE_HMAC_KEY"),
		oldDir: os.Getenv("GT_GATE_ATTEST_DIR"),
	}
	t.Setenv("GT_GATE_HMAC_KEY", keyPath)
	t.Setenv("GT_GATE_ATTEST_DIR", attestDir)
	return env
}

// wantToken reproduces the bash Phase-4 producer byte-for-byte:
//
//	hmac.new(open(keypath,"rb").read(), tree_sha.encode(), hashlib.sha256).hexdigest()
func wantToken(treeSHA string, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(treeSHA))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestComputeAttestationToken_MatchesBashProducer(t *testing.T) {
	env := newAttestationTestEnv(t)
	treeSHA := "01daa891abcdef0123456789abcdef0123456789"

	got := computeAttestationToken(treeSHA, env.keyBytes)
	want := wantToken(treeSHA, env.keyBytes)
	if got != want {
		t.Fatalf("token mismatch:\n got %s\n want %s", got, want)
	}
}

func TestVerifyAttestation_RoundTrip(t *testing.T) {
	newAttestationTestEnv(t)
	treeSHA := "abc123def456abc123def456abc123def456abc1"

	if err := WriteAttestationToken(treeSHA); err != nil {
		t.Fatalf("WriteAttestationToken: %v", err)
	}
	if err := VerifyAttestation(treeSHA); err != nil {
		t.Fatalf("VerifyAttestation after write: %v", err)
	}
	if !HasAttestation(treeSHA) {
		t.Fatalf("HasAttestation = false, want true")
	}
}

func TestVerifyAttestation_MissingToken_FailClosed(t *testing.T) {
	// "Opus unavailable / core peer unavailable / pre-verified bypass" all
	// surface to the Go side as the same fail-closed outcome: no token file was
	// ever written for the tree. The merge must be blocked — never silently pass.
	newAttestationTestEnv(t)
	treeSHA := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	err := VerifyAttestation(treeSHA)
	if err == nil {
		t.Fatalf("VerifyAttestation with no token: expected error, got nil")
	}
	if err != ErrAttestationMissing {
		t.Fatalf("VerifyAttestation missing = %v, want ErrAttestationMissing", err)
	}
	if HasAttestation(treeSHA) {
		t.Fatalf("HasAttestation = true for missing token, want false")
	}
}

func TestVerifyAttestation_EmptyTree_FailClosed(t *testing.T) {
	newAttestationTestEnv(t)
	if err := VerifyAttestation(""); err == nil {
		t.Fatalf("VerifyAttestation(empty) expected error, got nil")
	}
}

func TestVerifyAttestation_InvalidToken_FailClosed(t *testing.T) {
	// A token exists but does not verify against the key for this tree — e.g.
	// a stale token for a different tree, or tampering. The merge must block.
	env := newAttestationTestEnv(t)
	treeSHA := "f00dfacef00dfacef00dfacef00dfacef00dface"
	if err := os.MkdirAll(env.dir, 0o700); err != nil {
		t.Fatalf("mkdir attest dir: %v", err)
	}
	// Write a token that is valid-looking but for a DIFFERENT tree.
	wrongTree := "0bad110bad110bad110bad110bad110bad110bad"
	badToken := wantToken(wrongTree, env.keyBytes)
	if err := os.WriteFile(filepath.Join(env.dir, treeSHA), []byte(badToken+"\n"), 0o600); err != nil {
		t.Fatalf("write bad token: %v", err)
	}

	err := VerifyAttestation(treeSHA)
	if err == nil {
		t.Fatalf("VerifyAttestation with wrong-tree token: expected error, got nil")
	}
	if err != ErrAttestationInvalid && !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("VerifyAttestation wrong-tree = %v, want ErrAttestationInvalid", err)
	}
	if HasAttestation(treeSHA) {
		t.Fatalf("HasAttestation = true for invalid token, want false")
	}
}

func TestVerifyAttestation_KeyChange_InvalidatesOldToken(t *testing.T) {
	// Rotating the server key must invalidate tokens minted under the old key,
	// so a leaked old key cannot be used to forge proof for new merges.
	env := newAttestationTestEnv(t)
	treeSHA := "cafebabecafebabecafebabecafebabecafeba"
	if err := WriteAttestationToken(treeSHA); err != nil {
		t.Fatalf("write token: %v", err)
	}
	if err := VerifyAttestation(treeSHA); err != nil {
		t.Fatalf("verify before rotation: %v", err)
	}

	// Rotate the key: new key file at a different path.
	newKeyPath := filepath.Join(filepath.Dir(env.keyPath), "rotated-key")
	newKey := strings.Repeat("z", 64) + "\n"
	if err := os.WriteFile(newKeyPath, []byte(newKey), 0o600); err != nil {
		t.Fatalf("write rotated key: %v", err)
	}
	t.Setenv("GT_GATE_HMAC_KEY", newKeyPath)

	err := VerifyAttestation(treeSHA)
	if err == nil {
		t.Fatalf("VerifyAttestation after key rotation: expected invalidation, got nil")
	}
}

func TestReadAttestationToken_TrimWhitespace(t *testing.T) {
	// The producer writes "<token>\n"; Read must trim so comparison is exact.
	env := newAttestationTestEnv(t)
	treeSHA := "123456712345671234567123456712345671234567"
	token := wantToken(treeSHA, env.keyBytes)
	if err := os.MkdirAll(env.dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.dir, treeSHA), []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadAttestationToken(treeSHA)
	if err != nil {
		t.Fatalf("ReadAttestationToken: %v", err)
	}
	if got != token {
		t.Fatalf("read token = %q, want %q", got, token)
	}
}
