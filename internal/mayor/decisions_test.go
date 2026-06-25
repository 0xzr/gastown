package mayor

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeFileState writes a raw decisions.json body to disk for tests that need
// to control exact timestamps and ordering.
func writeFileState(t *testing.T, townRoot, body string) {
	t.Helper()
	dir := filepath.Dir(DecisionsFile(townRoot))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(DecisionsFile(townRoot), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestDecisionType_BlocksRework(t *testing.T) {
	t.Parallel()
	cases := []struct {
		d    DecisionType
		want bool
	}{
		{DecisionDefer, true},
		{DecisionHold, true},
		{DecisionPark, true},
		{DecisionResume, false},
	}
	for _, c := range cases {
		if got := c.d.BlocksRework(); got != c.want {
			t.Errorf("%q.BlocksRework() = %v, want %v", c.d, got, c.want)
		}
	}
}

func TestLoadDecisions_MissingFile(t *testing.T) {
	t.Parallel()
	state, err := LoadDecisions(t.TempDir())
	if err != nil {
		t.Fatalf("LoadDecisions on missing file: %v", err)
	}
	if state == nil || state.Decisions == nil {
		t.Fatal("expected non-nil empty state")
	}
	if len(state.Decisions) != 0 {
		t.Errorf("expected 0 decisions, got %d", len(state.Decisions))
	}
}

func TestRecordAndSave_RoundTrip(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	d, err := RecordDecision(townRoot, "gt-abc", "mayor/acp", DecisionDefer, "deprioritized")
	if err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	if d.BeadID != "gt-abc" || d.Type != DecisionDefer {
		t.Errorf("recorded decision mismatch: %+v", d)
	}
	if d.Timestamp.IsZero() {
		t.Error("timestamp not set")
	}

	// Reload from disk and verify persistence.
	state, err := LoadDecisions(townRoot)
	if err != nil {
		t.Fatalf("LoadDecisions: %v", err)
	}
	if len(state.Decisions) != 1 {
		t.Fatalf("expected 1 persisted decision, got %d", len(state.Decisions))
	}
	if state.Decisions[0].Reason != "deprioritized" {
		t.Errorf("reason = %q", state.Decisions[0].Reason)
	}
}

func TestActiveDecision_MostRecentWins(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	older := time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	body := `{
  "decisions": [
    {"bead_id":"gt-x","type":"defer","reason":"first","mayor_id":"m1","timestamp":"2026-06-24T10:00:00Z"},
    {"bead_id":"gt-x","type":"hold","reason":"second","mayor_id":"m2","timestamp":"2026-06-24T12:00:00Z"}
  ]
}`
	_ = older
	_ = newer
	writeFileState(t, townRoot, body)

	state, err := LoadDecisions(townRoot)
	if err != nil {
		t.Fatalf("LoadDecisions: %v", err)
	}
	d, err := state.ActiveDecision("gt-x")
	if err != nil {
		t.Fatalf("ActiveDecision: %v", err)
	}
	if d.Type != DecisionHold {
		t.Errorf("active = %q, want hold (most recent)", d.Type)
	}
}

func TestActiveDecision_ResumeOverridesBlock(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	body := `{
  "decisions": [
    {"bead_id":"gt-y","type":"park","reason":"parked","mayor_id":"m1","timestamp":"2026-06-24T10:00:00Z"},
    {"bead_id":"gt-y","type":"resume","reason":"authorized","mayor_id":"m2","timestamp":"2026-06-24T12:00:00Z"}
  ]
}`
	writeFileState(t, townRoot, body)

	state, err := LoadDecisions(townRoot)
	if err != nil {
		t.Fatalf("LoadDecisions: %v", err)
	}
	d, err := state.ActiveDecision("gt-y")
	if err != ErrDecisionNotFound {
		t.Fatalf("expected ErrDecisionNotFound after resume, got %v (%+v)", err, d)
	}
}

func TestActiveDecision_NoDecision(t *testing.T) {
	t.Parallel()
	state, _ := LoadDecisions(t.TempDir())
	if _, err := state.ActiveDecision("gt-none"); err != ErrDecisionNotFound {
		t.Errorf("expected ErrDecisionNotFound, got %v", err)
	}
}

func TestActiveDecision_EmptyBeadID(t *testing.T) {
	t.Parallel()
	state, _ := LoadDecisions(t.TempDir())
	if _, err := state.ActiveDecision("   "); err != ErrDecisionNotFound {
		t.Errorf("expected ErrDecisionNotFound for empty bead, got %v", err)
	}
}

func TestPriorBlockingDecision(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	body := `{
  "decisions": [
    {"bead_id":"gt-z","type":"hold","reason":"hold1","mayor_id":"m1","timestamp":"2026-06-24T10:00:00Z"},
    {"bead_id":"gt-z","type":"resume","reason":"ok","mayor_id":"m2","timestamp":"2026-06-24T12:00:00Z"}
  ]
}`
	writeFileState(t, townRoot, body)

	state, err := LoadDecisions(townRoot)
	if err != nil {
		t.Fatalf("LoadDecisions: %v", err)
	}

	// Active is resume (no block), but a prior block exists.
	if _, err := state.ActiveDecision("gt-z"); err != ErrDecisionNotFound {
		t.Fatalf("expected resume to clear active block, got %v", err)
	}
	prior, err := state.PriorBlockingDecision("gt-z")
	if err != nil {
		t.Fatalf("PriorBlockingDecision: %v", err)
	}
	if prior.Type != DecisionHold {
		t.Errorf("prior = %q, want hold", prior.Type)
	}

	// Bead with no blocking history.
	if _, err := state.PriorBlockingDecision("gt-other"); err != ErrDecisionNotFound {
		t.Errorf("expected ErrDecisionNotFound, got %v", err)
	}
}

func TestRecordDecision_InvalidType(t *testing.T) {
	t.Parallel()
	if _, err := RecordDecision(t.TempDir(), "gt-abc", "m", "bogus", ""); err == nil {
		t.Error("expected error for invalid decision type")
	}
}

func TestRecordDecision_EmptyBeadID(t *testing.T) {
	t.Parallel()
	if _, err := RecordDecision(t.TempDir(), "", "m", DecisionDefer, ""); err == nil {
		t.Error("expected error for empty beadID")
	}
}

func TestSaveDecisions_NilState(t *testing.T) {
	t.Parallel()
	if err := SaveDecisions(t.TempDir(), nil); err == nil {
		t.Error("expected error saving nil state")
	}
}

// TestActiveDecision_ResumeOldestThanBlock ensures a stale resume does not
// override a newer block: the most recent decision always wins.
func TestActiveDecision_StaleResumeDoesNotOverrideNewerBlock(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	body := `{
  "decisions": [
    {"bead_id":"gt-w","type":"resume","reason":"old","mayor_id":"m1","timestamp":"2026-06-24T08:00:00Z"},
    {"bead_id":"gt-w","type":"defer","reason":"new","mayor_id":"m2","timestamp":"2026-06-24T18:00:00Z"}
  ]
}`
	writeFileState(t, townRoot, body)

	state, err := LoadDecisions(townRoot)
	if err != nil {
		t.Fatalf("LoadDecisions: %v", err)
	}
	d, err := state.ActiveDecision("gt-w")
	if err != nil {
		t.Fatalf("expected active defer, got %v", err)
	}
	if d.Type != DecisionDefer {
		t.Errorf("active = %q, want defer (newer than stale resume)", d.Type)
	}
}
