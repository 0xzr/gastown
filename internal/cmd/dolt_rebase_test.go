package cmd

import (
	"strings"
	"testing"
)

// TestDoltRebaseRejectsMaliciousDBName verifies that runDoltRebase rejects
// a malicious database name before opening a connection or checking server
// state (gastown-wes regression).
func TestDoltRebaseRejectsMaliciousDBName(t *testing.T) {
	malicious := "x`; DROP DATABASE foo; --"
	err := runDoltRebase(nil, []string{malicious})
	if err == nil {
		t.Fatal("expected error for malicious database name, got nil")
	}
	if !strings.Contains(err.Error(), "invalid database name") {
		t.Fatalf("expected invalid database name error, got: %v", err)
	}
}

// TestDoltRebaseAcceptsValidDBName verifies that a safe database name passes
// validation (it will fail later for other reasons, but not on the name check).
func TestDoltRebaseAcceptsValidDBName(t *testing.T) {
	err := runDoltRebase(nil, []string{"safe_db-name"})
	if err != nil && strings.Contains(err.Error(), "invalid database name") {
		t.Fatalf("unexpected invalid database name error for safe name: %v", err)
	}
}
