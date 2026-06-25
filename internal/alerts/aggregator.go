package alerts

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// Evidence captures a single occurrence of an alert.
type Evidence struct {
	// Timestamp is when this occurrence was recorded.
	Timestamp time.Time

	// Severity is the preserved escalation priority (e.g., "urgent", "high").
	Severity string

	// Agents is the list of agents affected in this occurrence.
	Agents []string

	// Body is the human-readable evidence for this occurrence.
	Body string
}

// RecordResult is returned by Aggregator.Record.
type RecordResult struct {
	// IssueID is the canonical tracking bead.
	IssueID string

	// Created is true when a new tracking bead was created.
	Created bool

	// Occurrences is the total occurrence count after recording.
	Occurrences int
}

// BeadsClient is the subset of the beads API used by the aggregator.
// *beads.Beads implements this interface.
type BeadsClient interface {
	Search(opts beads.SearchOptions) ([]*beads.Issue, error)
	Create(opts beads.CreateOptions) (*beads.Issue, error)
	Update(id string, opts beads.UpdateOptions) error
}

// Aggregator collapses repeated equivalent alerts into a single tracking bead
// per root-cause key while preserving severity and latest evidence.
type Aggregator struct {
	client BeadsClient
	now    func() time.Time
}

// NewAggregator creates an aggregator backed by the given beads client.
func NewAggregator(client BeadsClient) *Aggregator {
	return &Aggregator{
		client: client,
		now:    time.Now,
	}
}

// NewAggregatorWithClock creates an aggregator with a pluggable clock for tests.
func NewAggregatorWithClock(client BeadsClient, now func() time.Time) *Aggregator {
	return &Aggregator{client: client, now: now}
}

// Record records a new occurrence of an alert. If an open tracking bead for
// the root-cause key exists, it is updated in place; otherwise a new bead is
// created. Distinct root causes (different keys) always remain separate beads.
func (a *Aggregator) Record(key RootCauseKey, ev Evidence) (*RecordResult, error) {
	existing, err := a.findTrackingBead(key)
	if err != nil {
		return nil, fmt.Errorf("finding tracking bead for %s: %w", key, err)
	}

	if existing != nil {
		return a.updateExisting(existing, key, ev)
	}

	return a.createNew(key, ev)
}

// findTrackingBead returns the newest open tracking bead for key, if any.
// Multiple matches should not happen in normal operation, but if they do the
// newest is returned so the caller can update consistently.
func (a *Aggregator) findTrackingBead(key RootCauseKey) (*beads.Issue, error) {
	issues, err := a.client.Search(beads.SearchOptions{
		Status: "open",
		Label:  key.Label(),
		Limit:  10,
	})
	if err != nil {
		return nil, err
	}

	var newest *beads.Issue
	for _, issue := range issues {
		if issue == nil || issue.Status != "open" {
			continue
		}
		if !beads.HasLabel(issue, key.Label()) {
			continue
		}
		if newest == nil || issue.CreatedAt > newest.CreatedAt {
			newest = issue
		}
	}
	return newest, nil
}

func (a *Aggregator) createNew(key RootCauseKey, ev Evidence) (*RecordResult, error) {
	now := truncateToSeconds(a.now().UTC())
	state := newAlertState(key, now, ev)
	title := fmt.Sprintf("[ALERT] %s in %s", key.Class, key.Scope)

	issue, err := a.client.Create(beads.CreateOptions{
		Title:       title,
		Description: state.Render(),
		Labels:      key.AllLabels(severityLabel(ev.Severity)),
		Priority:    severityToPriority(ev.Severity),
	})
	if err != nil {
		return nil, fmt.Errorf("creating tracking bead for %s: %w", key, err)
	}

	return &RecordResult{
		IssueID:     issue.ID,
		Created:     true,
		Occurrences: 1,
	}, nil
}

func (a *Aggregator) updateExisting(issue *beads.Issue, key RootCauseKey, ev Evidence) (*RecordResult, error) {
	now := truncateToSeconds(a.now().UTC())
	state := parseAlertState(issue.Description, key)
	state.Record(now, ev)

	labels := updateSeverityLabel(issue.Labels, state.Severity)
	stateLabels := dedupeLabels(labels)

	desc := state.Render()
	priority := severityToPriority(state.Severity)
	opts := beads.UpdateOptions{
		Description: &desc,
		SetLabels:   stateLabels,
		Priority:    &priority,
	}
	if err := a.client.Update(issue.ID, opts); err != nil {
		return nil, fmt.Errorf("updating tracking bead %s: %w", issue.ID, err)
	}

	return &RecordResult{
		IssueID:     issue.ID,
		Created:     false,
		Occurrences: state.Occurrences,
	}, nil
}

func severityLabel(severity string) string {
	return "alert:severity:" + severity
}

func severityToPriority(severity string) int {
	switch strings.ToLower(severity) {
	case "urgent":
		return 0
	case "high":
		return 1
	case "low":
		return 3
	default:
		return 2 // normal
	}
}

// higherSeverity returns the more severe of two severity strings.
// Severity ordering: urgent > high > normal > low. Empty severity is
// treated as less severe than any explicit value.
func higherSeverity(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if severityToPriority(a) <= severityToPriority(b) {
		return a
	}
	return b
}

func updateSeverityLabel(labels []string, severity string) []string {
	prefix := "alert:severity:"
	var out []string
	for _, l := range labels {
		if !strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return append(out, prefix+severity)
}

func dedupeLabels(labels []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, l := range labels {
		if seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

func truncateToSeconds(t time.Time) time.Time {
	return t.Truncate(time.Second)
}

// alertState is the parseable content stored in a tracking bead description.
type alertState struct {
	Key         RootCauseKey `json:"key"`
	Occurrences int          `json:"occurrences"`
	FirstSeen   time.Time    `json:"first_seen"`
	LastSeen    time.Time    `json:"last_seen"`
	Severity    string       `json:"severity"`
	Agents      []string     `json:"agents"`
	LatestBody  string       `json:"latest_body"`
}

const (
	stateHeaderMarker = "<!-- alert-state "
	stateFooterMarker = " -->"
)

var stateLineRe = regexp.MustCompile(`(?m)^<!-- alert-state (\{.*?\}) -->$`)

func newAlertState(key RootCauseKey, now time.Time, ev Evidence) alertState {
	return alertState{
		Key:         key,
		Occurrences: 1,
		FirstSeen:   now,
		LastSeen:    now,
		Severity:    ev.Severity,
		Agents:      copyAgents(ev.Agents),
		LatestBody:  ev.Body,
	}
}

func (s *alertState) Record(now time.Time, ev Evidence) {
	s.Occurrences++
	s.LastSeen = now
	s.LatestBody = ev.Body
	s.Severity = higherSeverity(s.Severity, ev.Severity)
	s.Agents = mergeAgents(s.Agents, ev.Agents)
}

func (s *alertState) Render() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("## Alert: %s in %s\n\n", s.Key.Class, s.Key.Scope))
	b.WriteString("This tracking bead aggregates repeated equivalent alerts.\n\n")
	b.WriteString("### Occurrence summary\n\n")
	b.WriteString(fmt.Sprintf("- **Root cause key**: %s\n", s.Key))
	b.WriteString(fmt.Sprintf("- **Total occurrences**: %d\n", s.Occurrences))
	b.WriteString(fmt.Sprintf("- **First seen**: %s\n", s.FirstSeen.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("- **Last seen**: %s\n", s.LastSeen.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("- **Severity preserved**: %s\n", s.Severity))
	if len(s.Agents) > 0 {
		b.WriteString(fmt.Sprintf("- **Affected agents**: %s\n", strings.Join(s.Agents, ", ")))
	}
	b.WriteString("\n### Latest evidence\n\n")
	b.WriteString(s.LatestBody)
	b.WriteString("\n")

	// Append a compact machine-readable JSON blob so future updates are robust
	// against cosmetic description edits.
	blob, _ := json.Marshal(s)
	b.WriteString(fmt.Sprintf("\n%s%s%s\n", stateHeaderMarker, string(blob), stateFooterMarker))

	return b.String()
}

func parseAlertState(desc string, key RootCauseKey) alertState {
	defaultState := alertState{Key: key}
	matches := stateLineRe.FindStringSubmatch(desc)
	if len(matches) < 2 {
		return defaultState
	}

	var state alertState
	if err := json.Unmarshal([]byte(matches[1]), &state); err != nil {
		return defaultState
	}
	if state.Key.String() != key.String() {
		state.Key = key
	}
	if state.Occurrences <= 0 {
		state.Occurrences = 1
	}
	return state
}

func copyAgents(a []string) []string {
	out := make([]string, len(a))
	copy(out, a)
	return out
}

func mergeAgents(existing, incoming []string) []string {
	seen := make(map[string]bool, len(existing)+len(incoming))
	for _, a := range existing {
		seen[a] = true
	}
	for _, a := range incoming {
		seen[a] = true
	}
	var out []string
	for a := range seen {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}
