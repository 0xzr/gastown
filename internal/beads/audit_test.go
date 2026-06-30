package beads

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	beadsdk "github.com/steveyegge/beads"
)

func testBeadsWithAuditLog(t *testing.T) (*Beads, *mockStorage, string) {
	t.Helper()
	tmpDir := t.TempDir()
	store := newMockStorage()
	b := &Beads{workDir: tmpDir, store: store, isolated: true}
	return b, store, tmpDir
}

func setupPinnedBeadWithAttachment(t *testing.T, store *mockStorage, id, moleculeID string) {
	t.Helper()
	store.issues[id] = &beadsdk.Issue{
		ID:     id,
		Title:  "Pinned Handoff",
		Status: beadsdk.Status(StatusPinned),
		Description: FormatAttachmentFields(&AttachmentFields{
			AttachedMolecule: moleculeID,
			AttachedAt:       "2024-01-01T00:00:00Z",
		}),
	}
}

func auditLogPath(t *testing.T, tmpDir string) string {
	t.Helper()
	return filepath.Join(tmpDir, ".beads", "audit.log")
}

func readAuditEntries(t *testing.T, tmpDir string) []DetachAuditEntry {
	t.Helper()
	data, err := os.ReadFile(auditLogPath(t, tmpDir))
	if err != nil {
		t.Fatalf("reading audit log: %v", err)
	}
	var entries []DetachAuditEntry
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var entry DetachAuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("unmarshaling audit entry: %v", err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func TestDetachMoleculeWithAudit_WritesAuditAfterSuccessfulUpdate(t *testing.T) {
	b, store, tmpDir := testBeadsWithAuditLog(t)
	setupPinnedBeadWithAttachment(t, store, "test-1", "mol-1")

	issue, err := b.DetachMoleculeWithAudit("test-1", DetachOptions{
		Agent:  "test-agent",
		Reason: "testing",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ParseAttachmentFields(issue) != nil {
		t.Errorf("expected attachment to be cleared after detach")
	}

	entries := readAuditEntries(t, tmpDir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}

	entry := entries[0]
	if entry.PinnedBeadID != "test-1" {
		t.Errorf("PinnedBeadID = %q, want %q", entry.PinnedBeadID, "test-1")
	}
	if entry.DetachedMolecule != "mol-1" {
		t.Errorf("DetachedMolecule = %q, want %q", entry.DetachedMolecule, "mol-1")
	}
	if entry.DetachedBy != "test-agent" {
		t.Errorf("DetachedBy = %q, want %q", entry.DetachedBy, "test-agent")
	}
	if entry.Reason != "testing" {
		t.Errorf("Reason = %q, want %q", entry.Reason, "testing")
	}
	if entry.PreviousState != StatusPinned {
		t.Errorf("PreviousState = %q, want %q", entry.PreviousState, StatusPinned)
	}
	if entry.Operation != "detach" {
		t.Errorf("Operation = %q, want %q", entry.Operation, "detach")
	}
}

func TestDetachMoleculeWithAudit_NoAuditWhenUpdateFails(t *testing.T) {
	b, store, tmpDir := testBeadsWithAuditLog(t)
	setupPinnedBeadWithAttachment(t, store, "test-1", "mol-1")

	store.updateErr = errors.New("update failed")

	_, err := b.DetachMoleculeWithAudit("test-1", DetachOptions{
		Agent:  "test-agent",
		Reason: "testing",
	})
	if err == nil {
		t.Fatal("expected error when update fails, got nil")
	}

	if _, statErr := os.Stat(auditLogPath(t, tmpDir)); !os.IsNotExist(statErr) {
		t.Fatalf("audit log should not be written when update fails, but exists at %s", auditLogPath(t, tmpDir))
	}
}

func TestDetachMoleculeWithAudit_NoAttachmentIsNoop(t *testing.T) {
	b, store, tmpDir := testBeadsWithAuditLog(t)
	store.issues["test-1"] = &beadsdk.Issue{
		ID:          "test-1",
		Title:       "Pinned Handoff",
		Status:      beadsdk.Status(StatusPinned),
		Description: "no attachment here",
	}

	issue, err := b.DetachMoleculeWithAudit("test-1", DetachOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if issue == nil {
		t.Fatal("expected returned issue, got nil")
	}

	if _, statErr := os.Stat(auditLogPath(t, tmpDir)); !os.IsNotExist(statErr) {
		t.Fatalf("audit log should not be written when there is no attachment")
	}
}
