package refinery

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Attestation is the durable multi-model review attestation produced by the
// refinery gate (gastown-spike/dropin/refinery-gate.sh, Phase 4) and verified
// by the Go merge tooling before a merge may be pushed or a source bead closed.
//
// The token is an HMAC-SHA256 of the reviewed git tree SHA, keyed by a
// server-only secret. A skipped review panel produces no token, and a token
// cannot be fabricated without the key — so the presence of a valid token for a
// given tree is durable proof that the full multi-model review (deterministic
// gate + writer-excluded core peers + Opus/final verifier) cleared that exact
// tree. This mirrors the bash producer byte-for-byte so a token written by one
// is accepted by the other.
//
// Key material:
//   - secret: $GT_GATE_HMAC_KEY (path to key file) or ~/.gt-gate-hmac-key
//   - token store: $GT_GATE_ATTEST_DIR or ~/.gt-gate-attestations/<tree-sha>
//
// The key FILE is read in full (including any trailing newline — the bash
// producer reads the raw bytes with open(...,"rb").read()), and HMAC'd over the
// tree SHA encoded as ASCII, matching:
//
//	hmac.new(open(keypath,"rb").read(), tree_sha.encode(), hashlib.sha256).hexdigest()

const (
	// defaultAttestationKeyFile is the server-only HMAC key file.
	defaultAttestationKeyFile = ".gt-gate-hmac-key"

	// defaultAttestationDir is the per-tree token store.
	defaultAttestationDir = ".gt-gate-attestations"
)

// ErrAttestationMissing is returned when no attestation token exists for a tree.
// This is the fail-closed outcome: a missing token means the multi-model review
// never ran (or never completed) for that tree, so the merge must be blocked.
var ErrAttestationMissing = errors.New("attestation token missing for tree")

// ErrAttestationInvalid is returned when a token exists but does not verify
// against the key for the given tree. This indicates tampering or a stale token
// for a different tree/key.
var ErrAttestationInvalid = errors.New("attestation token invalid for tree")

// attestationKeyPath resolves the HMAC key file path. The GT_GATE_HMAC_KEY env
// var (a path to the key file, matching the bash producer) takes precedence;
// otherwise the home-dir default is used.
func attestationKeyPath() (string, error) {
	if p := os.Getenv("GT_GATE_HMAC_KEY"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("attestation key: cannot resolve home dir: %w", err)
	}
	return filepath.Join(home, defaultAttestationKeyFile), nil
}

// attestationDir resolves the per-tree token store directory. The
// GT_GATE_ATTEST_DIR env var takes precedence; otherwise the home-dir default.
func attestationDir() (string, error) {
	if d := os.Getenv("GT_GATE_ATTEST_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("attestation dir: cannot resolve home dir: %w", err)
	}
	return filepath.Join(home, defaultAttestationDir), nil
}

// loadAttestationKey reads the raw HMAC key bytes from the key file. The file is
// read verbatim (including a trailing newline, exactly as the bash producer does
// with open(path,"rb").read()) so that tokens computed here verify against tokens
// written by refinery-gate.sh.
func loadAttestationKey() ([]byte, error) {
	p, err := attestationKeyPath()
	if err != nil {
		return nil, err
	}
	key, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read attestation key %s: %w", p, err)
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("attestation key %s is empty", p)
	}
	return key, nil
}

// computeAttestationToken computes the HMAC-SHA256 attestation token for a tree
// SHA, keyed by the raw key-file bytes. This is the Go mirror of the bash
// Phase-4 producer:
//
//	hmac.new(key_bytes, tree_sha.encode(), hashlib.sha256).hexdigest()
//
// It is exported to tests so a token can be minted against a test key without
// invoking the bash gate.
func computeAttestationToken(treeSHA string, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(treeSHA))
	return hex.EncodeToString(mac.Sum(nil))
}

// ReadAttestationToken reads the stored token for treeSHA from the token store.
// Returns ErrAttestationMissing when no token file exists for the tree.
func ReadAttestationToken(treeSHA string) (string, error) {
	dir, err := attestationDir()
	if err != nil {
		return "", err
	}
	token, err := os.ReadFile(filepath.Join(dir, treeSHA))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrAttestationMissing
		}
		return "", fmt.Errorf("read attestation token for tree %s: %w", treeSHA, err)
	}
	return strings.TrimSpace(string(token)), nil
}

// VerifyAttestation confirms that a valid attestation token exists for treeSHA
// in the token store and that it matches the HMAC of the tree under the current
// key. A missing or non-matching token is a hard failure: callers must block the
// merge/close and file an audit bead rather than proceeding.
//
// Returns nil only when a token is present AND verifies. This is the
// machine-checkable proof that the exact reviewed tree cleared the full
// multi-model gate.
func VerifyAttestation(treeSHA string) error {
	if treeSHA == "" {
		return fmt.Errorf("%w: empty tree sha", ErrAttestationMissing)
	}
	stored, err := ReadAttestationToken(treeSHA)
	if err != nil {
		return err
	}
	key, err := loadAttestationKey()
	if err != nil {
		return err
	}
	want := computeAttestationToken(treeSHA, key)
	if !hmac.Equal([]byte(stored), []byte(want)) {
		return fmt.Errorf("%w: %s", ErrAttestationInvalid, treeSHA)
	}
	return nil
}

// HasAttestation is a boolean convenience over VerifyAttestation for reporting
// (e.g. the "completed work lacking attestation proof" listing).
func HasAttestation(treeSHA string) bool {
	return VerifyAttestation(treeSHA) == nil
}

// WriteAttestationToken writes a token for treeSHA to the token store. This is
// normally done by the bash refinery-gate.sh Phase 4; the Go side is the
// verifier. This function exists so a self-contained refinery (no bash gate
// configured) can still mint a token after a Go-driven review, and so tests can
// seed the store. It is fail-closed: a write error is returned, never swallowed.
func WriteAttestationToken(treeSHA string) error {
	if treeSHA == "" {
		return fmt.Errorf("%w: cannot write token for empty tree sha", ErrAttestationMissing)
	}
	key, err := loadAttestationKey()
	if err != nil {
		return err
	}
	dir, err := attestationDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create attestation dir %s: %w", dir, err)
	}
	token := computeAttestationToken(treeSHA, key)
	if err := os.WriteFile(filepath.Join(dir, treeSHA), []byte(token+"\n"), 0o600); err != nil {
		return fmt.Errorf("write attestation token for tree %s: %w", treeSHA, err)
	}
	return nil
}
