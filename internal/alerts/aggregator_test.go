package alerts

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// fakeBeadsClient is an in-memory BeadsClient for unit tests.
type fakeBeadsClient struct {
	issues []*beads.Issue
	nextID int
}

func newFakeBeadsClient() *fakeBeadsClient {
	return &fakeBeadsClient{}
}

func (f *fakeBeadsClient) Search(opts beads.SearchOptions) ([]*beads.Issue, error) {
	var out []*beads.Issue
	for _, issue := range f.issues {
		if opts.Status != "" && issue.Status != opts.Status {
			continue
		}
		if opts.Label != "" && !beads.HasLabel(issue, opts.Label) {
			continue
		}
		out = append(out, issue)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt > out[j].CreatedAt
	})
	if opts.Limit > 0 && len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return out, nil
}

func (f *fakeBeadsClient) Create(opts beads.CreateOptions) (*beads.Issue, error) {
	f.nextID++
	issue := &beads.Issue{
		ID:          fmt.Sprintf("gt-alert-%03d", f.nextID),
		Title:       opts.Title,
		Description: opts.Description,
		Status:      "open",
		Priority:    opts.Priority,
		Labels:      opts.Labels,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	f.issues = append(f.issues, issue)
	return issue, nil
}

func (f *fakeBeadsClient) Update(id string, opts beads.UpdateOptions) error {
	for _, issue := range f.issues {
		if issue.ID != id {
			continue
		}
		if opts.Title != nil {
			issue.Title = *opts.Title
		}
		if opts.Status != nil {
			issue.Status = *opts.Status
		}
		if opts.Priority != nil {
			issue.Priority = *opts.Priority
		}
		if opts.Description != nil {
			issue.Description = *opts.Description
		}
		if len(opts.SetLabels) > 0 {
			issue.Labels = opts.SetLabels
		}
		return nil
	}
	return fmt.Errorf("issue not found: %s", id)
}

func fixedClock(start time.Time) func() time.Time {
	t := start
	return func() time.Time {
		t = t.Add(time.Minute)
		return t
	}
}

func TestAggregator_NewAlertCreatesTrackingBead(t *testing.T) {
	client := newFakeBeadsClient()
	clock := fixedClock(time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC))
	agg := NewAggregatorWithClock(client, clock)

	key := RootCauseKey{Class: ClassPolecatDied, Scope: "gastown"}
	ev := Evidence{
		Timestamp: clock(),
		Severity:  "high",
		Agents:    []string{"gastown/onyx"},
		Body:      "Polecat onyx died with hook gt-123",
	}

	result, err := agg.Record(key, ev)
	if err != nil {
		t.Fatalf("Record failed: %v", err)
	}
	if !result.Created {
		t.Error("Created = false, want true")
	}
	if result.Occurrences != 1 {
		t.Errorf("Occurrences = %d, want 1", result.Occurrences)
	}

	issue := client.issues[0]
	if !beads.HasLabel(issue, key.Label()) {
		t.Errorf("missing label %q", key.Label())
	}
	if !beads.HasLabel(issue, "gt:alert") {
		t.Error("missing gt:alert label")
	}
	if !beads.HasLabel(issue, "alert:severity:high") {
		t.Error("missing severity label")
	}
	if !strings.Contains(issue.Description, "**Total occurrences**: 1") {
		t.Errorf("description missing initial occurrence count: %s", issue.Description)
	}
	if !strings.Contains(issue.Description, ev.Body) {
		t.Errorf("description missing latest evidence: %s", issue.Description)
	}
}

func TestAggregator_DuplicateAlertUpdatesSameBead(t *testing.T) {
	client := newFakeBeadsClient()
	clock := fixedClock(time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC))
	agg := NewAggregatorWithClock(client, clock)

	key := RootCauseKey{Class: ClassZombieDetected, Scope: "gastown"}

	first, err := agg.Record(key, Evidence{
		Timestamp: clock(),
		Severity:  "high",
		Agents:    []string{"gastown/alpha"},
		Body:      "First zombie occurrence",
	})
	if err != nil {
		t.Fatalf("first Record failed: %v", err)
	}
	if !first.Created {
		t.Fatal("first record should create bead")
	}

	second, err := agg.Record(key, Evidence{
		Timestamp: clock(),
		Severity:  "high",
		Agents:    []string{"gastown/beta"},
		Body:      "Second zombie occurrence",
	})
	if err != nil {
		t.Fatalf("second Record failed: %v", err)
	}
	if second.Created {
		t.Error("second record should update existing bead, not create new one")
	}
	if second.IssueID != first.IssueID {
		t.Errorf("IssueID changed: %s -> %s", first.IssueID, second.IssueID)
	}
	if second.Occurrences != 2 {
		t.Errorf("Occurrences = %d, want 2", second.Occurrences)
	}
	if len(client.issues) != 1 {
		t.Fatalf("expected 1 tracking bead, got %d", len(client.issues))
	}

	issue := client.issues[0]
	if !strings.Contains(issue.Description, "**Total occurrences**: 2") {
		t.Errorf("description missing updated occurrence count: %s", issue.Description)
	}
	if !strings.Contains(issue.Description, "Second zombie occurrence") {
		t.Errorf("description missing latest evidence: %s", issue.Description)
	}
	if !strings.Contains(issue.Description, "**Affected agents**: gastown/alpha, gastown/beta") &&
		!strings.Contains(issue.Description, "**Affected agents**: gastown/beta, gastown/alpha") {
		t.Errorf("description missing merged affected agents: %s", issue.Description)
	}
}

func TestAggregator_DistinctKeysRemainSeparate(t *testing.T) {
	client := newFakeBeadsClient()
	clock := fixedClock(time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC))
	agg := NewAggregatorWithClock(client, clock)

	key1 := RootCauseKey{Class: ClassPolecatDied, Scope: "gastown"}
	key2 := RootCauseKey{Class: ClassZombieDetected, Scope: "gastown"}

	if _, err := agg.Record(key1, Evidence{Severity: "high", Body: "polecat died"}); err != nil {
		t.Fatalf("Record key1 failed: %v", err)
	}
	if _, err := agg.Record(key2, Evidence{Severity: "high", Body: "zombie detected"}); err != nil {
		t.Fatalf("Record key2 failed: %v", err)
	}

	if len(client.issues) != 2 {
		t.Fatalf("expected 2 tracking beads, got %d", len(client.issues))
	}
}

func TestAggregator_DistinctScopesRemainSeparate(t *testing.T) {
	client := newFakeBeadsClient()
	clock := fixedClock(time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC))
	agg := NewAggregatorWithClock(client, clock)

	key1 := RootCauseKey{Class: ClassPolecatDied, Scope: "gastown"}
	key2 := RootCauseKey{Class: ClassPolecatDied, Scope: "longeye"}

	if _, err := agg.Record(key1, Evidence{Severity: "high", Body: "gastown polecat died"}); err != nil {
		t.Fatalf("Record key1 failed: %v", err)
	}
	if _, err := agg.Record(key2, Evidence{Severity: "high", Body: "longeye polecat died"}); err != nil {
		t.Fatalf("Record key2 failed: %v", err)
	}

	if len(client.issues) != 2 {
		t.Fatalf("expected 2 tracking beads, got %d", len(client.issues))
	}
}

func TestAggregator_ClosedBeadIgnoredForNewAlert(t *testing.T) {
	client := newFakeBeadsClient()
	clock := fixedClock(time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC))

	// Seed a closed tracking bead for the same key.
	closed := &beads.Issue{
		ID:        "gt-alert-001",
		Title:     "old",
		Status:    "closed",
		Labels:    RootCauseKey{Class: ClassPolecatDied, Scope: "gastown"}.AllLabels(),
		CreatedAt: time.Date(2026, 6, 24, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}
	client.issues = append(client.issues, closed)

	agg := NewAggregatorWithClock(client, clock)
	key := RootCauseKey{Class: ClassPolecatDied, Scope: "gastown"}
	result, err := agg.Record(key, Evidence{Severity: "high", Body: "new occurrence"})
	if err != nil {
		t.Fatalf("Record failed: %v", err)
	}
	if !result.Created {
		t.Error("closed bead should not be reused; expected new bead")
	}
}

func TestAggregator_PreservesHighestSeverity(t *testing.T) {
	client := newFakeBeadsClient()
	clock := fixedClock(time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC))
	agg := NewAggregatorWithClock(client, clock)

	key := RootCauseKey{Class: ClassPolecatDied, Scope: "gastown"}
	if _, err := agg.Record(key, Evidence{Severity: "normal", Body: "first"}); err != nil {
		t.Fatalf("Record failed: %v", err)
	}
	if _, err := agg.Record(key, Evidence{Severity: "urgent", Body: "second"}); err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	issue := client.issues[0]
	if issue.Priority != 0 {
		t.Errorf("Priority = %d, want 0 (urgent)", issue.Priority)
	}
	if !beads.HasLabel(issue, "alert:severity:urgent") {
		t.Errorf("missing updated severity label: %v", issue.Labels)
	}
}

func TestAggregator_PreservesHighestSeverity_DoesNotDegrade(t *testing.T) {
	client := newFakeBeadsClient()
	clock := fixedClock(time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC))
	agg := NewAggregatorWithClock(client, clock)

	key := RootCauseKey{Class: ClassPolecatDied, Scope: "gastown"}
	if _, err := agg.Record(key, Evidence{Severity: "urgent", Body: "first"}); err != nil {
		t.Fatalf("Record failed: %v", err)
	}
	if _, err := agg.Record(key, Evidence{Severity: "normal", Body: "second"}); err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	issue := client.issues[0]
	if issue.Priority != 0 {
		t.Errorf("Priority = %d, want 0 (urgent)", issue.Priority)
	}
	if !beads.HasLabel(issue, "alert:severity:urgent") {
		t.Errorf("severity degraded to normal; labels=%v", issue.Labels)
	}
	if !strings.Contains(issue.Description, "**Severity preserved**: urgent") {
		t.Errorf("description does not preserve highest severity: %s", issue.Description)
	}
}

func TestParseSubject(t *testing.T) {
	cases := []struct {
		subject string
		want    AlertClass
		ok      bool
	}{
		{"POLECAT_DIED: 1 polecat in gastown", ClassPolecatDied, true},
		{"ZOMBIE_DETECTED: 1 zombie in gastown", ClassZombieDetected, true},
		{"Recovery needed: gastown", "", false},
		{"RECOVERY_NEEDED: foo", "", false},
	}

	for _, tc := range cases {
		cls, ok := ParseSubject(tc.subject)
		if ok != tc.ok {
			t.Errorf("ParseSubject(%q) ok=%v, want %v", tc.subject, ok, tc.ok)
			continue
		}
		if cls != tc.want {
			t.Errorf("ParseSubject(%q) class=%q, want %q", tc.subject, cls, tc.want)
		}
	}
}

func TestKeyFromSubject(t *testing.T) {
	key, ok := KeyFromSubject("POLECAT_DIED: 2 polecats in gastown", "gastown")
	if !ok {
		t.Fatal("expected ok")
	}
	if key.Class != ClassPolecatDied {
		t.Errorf("Class = %q, want polecat-died", key.Class)
	}
	if key.Scope != "gastown" {
		t.Errorf("Scope = %q, want gastown", key.Scope)
	}
	if key.String() != "polecat-died:gastown" {
		t.Errorf("String() = %q, want polecat-died:gastown", key.String())
	}
}
