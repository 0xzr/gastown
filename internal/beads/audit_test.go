package beads

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	beadsdk "github.com/steveyegge/beads"
)

// newAuditTestBeads returns a Beads instance configured for in-process audit-log tests.
// It points getResolvedBeadsDir at a fresh temp directory so each test sees its own
// audit.log, and uses a mock storage for Show/Update.
func newAuditTestBeads(t *testing.T) (*Beads, *mockStorage, string) {
	t.Helper()
	beadsDir := t.TempDir()
	store := newMockStorage()
	b := &Beads{
		workDir:  beadsDir,
		beadsDir: beadsDir,
		store:    store,
		noRoute:  true,
		isolated: true,
	}
	return b, store, beadsDir
}

// seedPinnedWithMolecule creates a pinned bead carrying an attached_molecule
// description. Returns the bead ID.
func seedPinnedWithMolecule(t *testing.T, store *mockStorage) string {
	t.Helper()
	id := "test-pinned-1"
	store.issues[id] = &beadsdk.Issue{
		ID:          id,
		Title:       "handoff bead",
		Status:      beadsdk.Status(StatusPinned),
		Description: "attached_molecule: test-mol-root\nattached_at: 2026-06-30T00:00:00Z\n",
	}
	return id
}

// TestDetachMoleculeWithAudit_HappyPath verifies the basic detach flow:
// after the call, the bead's attachment is cleared and an audit entry exists
// recording the detached molecule ID.
func TestDetachMoleculeWithAudit_HappyPath(t *testing.T) {
	b, store, _ := newAuditTestBeads(t)
	id := seedPinnedWithMolecule(t, store)

	updated, err := b.DetachMoleculeWithAudit(id, DetachOptions{
		Operation: "burn",
		Agent:     "nux",
		Reason:    "test burn",
	})
	if err != nil {
		t.Fatalf("DetachMoleculeWithAudit: %v", err)
	}
	if updated == nil {
		t.Fatalf("expected non-nil updated issue")
	}
	if ParseAttachmentFields(updated) != nil {
		t.Errorf("expected attachment fields cleared, got %+v", ParseAttachmentFields(updated))
	}

	// Audit log must be written with the correct entry.
	// getResolvedBeadsDir resolves via ResolveBeadsDir(beadsDir), which appends
	// .beads if the path is not already a .beads directory. Account for that.
	auditDir := b.getResolvedBeadsDir()
	auditPath := filepath.Join(auditDir, "audit.log")
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("audit.log not written: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 audit line, got %d: %s", len(lines), data)
	}
	var entry DetachAuditEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("audit entry not valid JSON: %v: %s", err, lines[0])
	}
	if entry.PinnedBeadID != id {
		t.Errorf("audit PinnedBeadID = %q, want %q", entry.PinnedBeadID, id)
	}
	if entry.DetachedMolecule != "test-mol-root" {
		t.Errorf("audit DetachedMolecule = %q, want %q", entry.DetachedMolecule, "test-mol-root")
	}
	if entry.Operation != "burn" {
		t.Errorf("audit Operation = %q, want %q", entry.Operation, "burn")
	}
	if entry.PreviousState != StatusPinned {
		t.Errorf("audit PreviousState = %q, want %q", entry.PreviousState, StatusPinned)
	}
}

// TestDetachMoleculeWithAudit_NoAttachment verifies that detaching a bead
// without any attachment is a no-op and does NOT write an audit entry.
func TestDetachMoleculeWithAudit_NoAttachment(t *testing.T) {
	b, store, _ := newAuditTestBeads(t)
	id := "test-pinned-2"
	store.issues[id] = &beadsdk.Issue{
		ID:          id,
		Title:       "no attachment",
		Status:      beadsdk.Status(StatusPinned),
		Description: "no attachment fields here\n",
	}

	if _, err := b.DetachMoleculeWithAudit(id, DetachOptions{Agent: "nux"}); err != nil {
		t.Fatalf("DetachMoleculeWithAudit: %v", err)
	}

	if _, err := os.Stat(filepath.Join(b.getResolvedBeadsDir(), "audit.log")); !os.IsNotExist(err) {
		t.Errorf("audit.log should NOT be written when there is nothing to detach (stat err = %v)", err)
	}
}

// TestDetachMoleculeWithAudit_UpdateFailsNoAudit is the retro-bug regression test
// for gastown-o4cws. If the underlying Update call fails, the audit log MUST
// NOT be written - the audit log must reflect actual state, not intent.
//
// Previously, LogDetachAudit was called BEFORE the Update, so a failed Update
// would leave an audit entry claiming a detach that never happened.
func TestDetachMoleculeWithAudit_UpdateFailsNoAudit(t *testing.T) {
	b, store, _ := newAuditTestBeads(t)
	id := seedPinnedWithMolecule(t, store)

	// Force Update to fail.
	store.updateErr = errors.New("simulated db failure")

	_, err := b.DetachMoleculeWithAudit(id, DetachOptions{Agent: "nux"})
	if err == nil {
		t.Fatalf("expected error from DetachMoleculeWithAudit when Update fails")
	}
	if !strings.Contains(err.Error(), "updating pinned bead") {
		t.Errorf("error should mention 'updating pinned bead', got: %v", err)
	}

	// The critical assertion: audit.log MUST NOT exist.
	auditPath := filepath.Join(b.getResolvedBeadsDir(), "audit.log")
	if _, statErr := os.Stat(auditPath); !os.IsNotExist(statErr) {
		var contents string
		if data, readErr := os.ReadFile(auditPath); readErr == nil {
			contents = string(data)
		}
		t.Errorf("audit.log was written despite Update failure - this is the gastown-o4cws bug.\n"+
			"audit.log contents (should be empty/absent): %q", contents)
	}

	// Sanity: the bead's description should still carry the attachment (unchanged).
	issue, showErr := b.Show(id)
	if showErr != nil {
		t.Fatalf("Show after failed detach: %v", showErr)
	}
	if ParseAttachmentFields(issue) == nil {
		t.Errorf("attachment should still be present after failed Update")
	}
	_ = context.Background() // keep import used if future tests need it
}
