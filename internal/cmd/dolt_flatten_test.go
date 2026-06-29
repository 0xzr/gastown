package cmd

import (
	"strings"
	"testing"
)

// TestDoltFlattenRejectsMaliciousDBName verifies that runDoltFlatten rejects
// a malicious database name before opening a connection or checking server
// state (gastown-wes regression).
func TestDoltFlattenRejectsMaliciousDBName(t *testing.T) {
	malicious := "x`; DROP DATABASE foo; --"
	err := runDoltFlatten(nil, []string{malicious})
	if err == nil {
		t.Fatal("expected error for malicious database name, got nil")
	}
	if !strings.Contains(err.Error(), "invalid database name") {
		t.Fatalf("expected invalid database name error, got: %v", err)
	}
}

// TestDoltFlattenAcceptsValidDBName verifies that a safe database name passes
// validation (it will fail later for other reasons, but not on the name check).
func TestDoltFlattenAcceptsValidDBName(t *testing.T) {
	// Validation is the first step; a safe name will not return an invalid-name
	// error. We don't need the server to be running to prove validation passed.
	err := runDoltFlatten(nil, []string{"safe_db-name"})
	if err != nil && strings.Contains(err.Error(), "invalid database name") {
		t.Fatalf("unexpected invalid database name error for safe name: %v", err)
	}
}
