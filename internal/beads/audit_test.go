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

// newAuditTestBeads builds a Beads wrapper rooted at a per-test temp dir so the
// audit log lands in tmpDir/.beads/audit.log. The mock storage gives us
// precise control over Update failures, which is the heart of the retro-bug
// regression we want to pin down.
func newAuditTestBeads(t *testing.T) (*Beads, *mockStorage, string) {
	t.Helper()
	tmpDir := t.TempDir()
	store := newMockStorage()
	b := &Beads{workDir: tmpDir, store: store, isolated: true}
	return b, store, tmpDir
}

// seedPinnedBeadWithAttachment inserts a pinned bead that already carries the
// attachment metadata DetachMoleculeWithAudit expects to find.
func seedPinnedBeadWithAttachment(t *testing.T, store *mockStorage, id, moleculeID string) {
	t.Helper()
	store.issues[id] = &beadsdk.Issue{
		ID:     id,
		Title:  "Pinned Handoff",
		Status: beadsdk.Status(StatusPinned),
		Description: FormatAttachmentFields(&AttachmentFields{
			AttachedMolecule: moleculeID,
			AttachedAt:       "2026-06-30T00:00:00Z",
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
			t.Fatalf("unmarshaling audit entry %q: %v", line, err)
		}
		entries = append(entries, entry)
	}
	return entries
}

// TestDetachMoleculeWithAudit_HappyPath writes one entry, clears attachment,
// and surfaces the previous status so forensic readers can tell what changed.
func TestDetachMoleculeWithAudit_HappyPath(t *testing.T) {
	b, store, tmpDir := newAuditTestBeads(t)
	seedPinnedBeadWithAttachment(t, store, "test-1", "mol-1")

	issue, err := b.DetachMoleculeWithAudit("test-1", DetachOptions{
		Agent:  "test-agent",
		Reason: "happy-path",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ParseAttachmentFields(issue) != nil {
		t.Errorf("expected attachment to be cleared, got %+v", ParseAttachmentFields(issue))
	}

	entries := readAuditEntries(t, tmpDir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}

	got := entries[0]
	if got.PinnedBeadID != "test-1" {
		t.Errorf("PinnedBeadID = %q, want %q", got.PinnedBeadID, "test-1")
	}
	if got.DetachedMolecule != "mol-1" {
		t.Errorf("DetachedMolecule = %q, want %q", got.DetachedMolecule, "mol-1")
	}
	if got.DetachedBy != "test-agent" {
		t.Errorf("DetachedBy = %q, want %q", got.DetachedBy, "test-agent")
	}
	if got.Reason != "happy-path" {
		t.Errorf("Reason = %q, want %q", got.Reason, "happy-path")
	}
	if got.PreviousState != StatusPinned {
		t.Errorf("PreviousState = %q, want %q", got.PreviousState, StatusPinned)
	}
	if got.Operation != "detach" {
		t.Errorf("Operation = %q, want %q", got.Operation, "detach")
	}
	if got.Timestamp == "" {
		t.Error("expected non-empty Timestamp")
	}
}

// TestDetachMoleculeWithAudit_NoAuditWhenUpdateFails is the core regression
// test for the retro-bug: when Update errors, no audit entry may be written.
// Before the fix, LogDetachAudit ran before Update, so the log recorded a
// detach that never happened — making it impossible to trust the audit trail.
func TestDetachMoleculeWithAudit_NoAuditWhenUpdateFails(t *testing.T) {
	b, store, tmpDir := newAuditTestBeads(t)
	seedPinnedBeadWithAttachment(t, store, "test-1", "mol-1")

	store.updateErr = errors.New("simulated update failure")

	_, err := b.DetachMoleculeWithAudit("test-1", DetachOptions{
		Agent:  "test-agent",
		Reason: "should-not-be-logged",
	})
	if err == nil {
		t.Fatal("expected error from DetachMoleculeWithAudit when Update fails, got nil")
	}
	if !strings.Contains(err.Error(), "simulated update failure") {
		t.Errorf("expected wrapped update error, got: %v", err)
	}

	if _, statErr := os.Stat(auditLogPath(t, tmpDir)); !os.IsNotExist(statErr) {
		t.Fatalf("audit log must not exist when Update fails, but stat=%v path=%s",
			statErr, auditLogPath(t, tmpDir))
	}

	// Verify the bead's attachment was not cleared: the operation did not
	// happen, so the description must still carry the molecule reference.
	issue, showErr := b.Show("test-1")
	if showErr != nil {
		t.Fatalf("re-fetch after failed update: %v", showErr)
	}
	if ParseAttachmentFields(issue) == nil {
		t.Error("attachment was cleared even though Update failed")
	}
}

// TestDetachMoleculeWithAudit_NoAttachmentIsNoop covers the early-return path:
// with nothing attached, no Update call fires and no audit entry is written.
func TestDetachMoleculeWithAudit_NoAttachmentIsNoop(t *testing.T) {
	b, store, tmpDir := newAuditTestBeads(t)
	store.issues["test-1"] = &beadsdk.Issue{
		ID:          "test-1",
		Title:       "Pinned Handoff",
		Status:      beadsdk.Status(StatusPinned),
		Description: "no attachment markers here",
	}

	issue, err := b.DetachMoleculeWithAudit("test-1", DetachOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if issue == nil {
		t.Fatal("expected returned issue, got nil")
	}
	if _, statErr := os.Stat(auditLogPath(t, tmpDir)); !os.IsNotExist(statErr) {
		t.Fatalf("audit log must not be written when there is no attachment (stat=%v)", statErr)
	}
}
