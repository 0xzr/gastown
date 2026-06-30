package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/alerts"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/witness"
)

type progressDiagnostics struct {
	bytes.Buffer
	sawProgress chan struct{}
	once        sync.Once
}

func (d *progressDiagnostics) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "still running") {
		d.once.Do(func() { close(d.sawProgress) })
	}
	return d.Buffer.Write(p)
}

func TestPatrolScanOutputJSON(t *testing.T) {
	output := PatrolScanOutput{
		Rig:       "gastown",
		Timestamp: "2026-03-17T12:00:00Z",
		Zombies: &PatrolScanZombieOutput{
			Checked: 3,
			Found:   1,
			Zombies: []PatrolScanZombieItem{
				{
					Polecat:        "alpha",
					Classification: "session-dead-active",
					AgentState:     "working",
					HookBead:       "gas-abc",
					Action:         "restarted",
					WasActive:      true,
				},
			},
		},
		Fleet: &witness.FleetState{
			Rig:                  "gastown",
			ActiveImplementation: []string{"alpha"},
			PostSubmitGate:       []string{"beta"},
			RecoveryHeld:         nil,
			Idle:                 []string{"gamma"},
			IsEmpty:              false,
		},
		Receipts: []witness.PatrolReceipt{
			{
				Rig:               "gastown",
				Polecat:           "alpha",
				Verdict:           witness.PatrolVerdictStale,
				RecommendedAction: "restarted",
				Evidence: witness.PatrolReceiptEvidence{
					AgentState:     "working",
					Classification: witness.ZombieSessionDeadActive,
					HookBead:       "gas-abc",
				},
			},
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal output: %v", err)
	}

	var parsed PatrolScanOutput
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	if parsed.Rig != "gastown" {
		t.Errorf("Rig = %q, want %q", parsed.Rig, "gastown")
	}
	if parsed.Zombies.Found != 1 {
		t.Errorf("Zombies.Found = %d, want 1", parsed.Zombies.Found)
	}
	if parsed.Zombies.Checked != 3 {
		t.Errorf("Zombies.Checked = %d, want 3", parsed.Zombies.Checked)
	}
	if len(parsed.Zombies.Zombies) != 1 {
		t.Fatalf("len(Zombies) = %d, want 1", len(parsed.Zombies.Zombies))
	}
	z := parsed.Zombies.Zombies[0]
	if z.Polecat != "alpha" {
		t.Errorf("zombie Polecat = %q, want %q", z.Polecat, "alpha")
	}
	if z.Classification != "session-dead-active" {
		t.Errorf("zombie Classification = %q, want %q", z.Classification, "session-dead-active")
	}
	if !z.WasActive {
		t.Error("zombie WasActive = false, want true")
	}
	if len(parsed.Receipts) != 1 {
		t.Fatalf("len(Receipts) = %d, want 1", len(parsed.Receipts))
	}
	if parsed.Receipts[0].Verdict != witness.PatrolVerdictStale {
		t.Errorf("receipt Verdict = %q, want %q", parsed.Receipts[0].Verdict, witness.PatrolVerdictStale)
	}

	// Fleet must round-trip with the bucket separation gastown-72v requires.
	if parsed.Fleet == nil {
		t.Fatal("Fleet = nil; want populated. Witness patrol output must present MQ gates separately from active impl")
	}
	if got, want := parsed.Fleet.PostSubmitGate, []string{"beta"}; !equalStrings(got, want) {
		t.Errorf("Fleet.PostSubmitGate = %v, want %v", got, want)
	}
	if got, want := parsed.Fleet.ActiveImplementation, []string{"alpha"}; !equalStrings(got, want) {
		t.Errorf("Fleet.ActiveImplementation = %v, want %v", got, want)
	}
	if parsed.Fleet.IsEmpty {
		t.Error("Fleet.IsEmpty = true; want false (gates + impl present)")
	}
}

// TestOutputPatrolScanJSON_CapturesBuffer verifies the io.Writer seam
// (introduced for gastown-72v) keeps the JSON serialization testable
// without ever touching os.Stdout. Parallel capture from os.Stdout races
// with the test runner's output, which is exactly the failure mode the
// rejection called out. We capture into a bytes.Buffer instead — no
// t.Parallel, no os.Stdout. (gastown-72v)
func TestOutputPatrolScanJSON_CapturesBuffer(t *testing.T) {
	var buf bytes.Buffer
	zombie := &witness.DetectZombiePolecatsResult{
		Checked: 1,
		Zombies: []witness.ZombieResult{
			{PolecatName: "alpha", WasActive: true, Classification: witness.ZombieSessionDeadActive, Action: "restarted"},
		},
	}
	fleet := &witness.FleetState{
		Rig:            "gastown",
		PostSubmitGate: []string{"alpha"},
		IsEmpty:        false,
	}
	if err := outputPatrolScanJSON(&buf, "gastown", "2026-06-29T00:00:00Z", zombie, nil, nil, fleet, nil); err != nil {
		t.Fatalf("outputPatrolScanJSON: %v", err)
	}
	got := buf.String()
	for _, want := range []string{
		`"rig": "gastown"`,
		`"fleet"`,
		`"PostSubmitGate"`,
		`"alpha"`,
		`"IsEmpty": false`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output %q missing %s", got, want)
		}
	}
}

// TestOutputPatrolScanJSON_NilFleetIsOmitted covers the ops failure mode
// where DetectFleetState failed (Dolt down, missing polecats dir); the
// patrol must still emit valid JSON without a fleet field.
func TestOutputPatrolScanJSON_NilFleetIsOmitted(t *testing.T) {
	var buf bytes.Buffer
	if err := outputPatrolScanJSON(&buf, "gastown", "2026-06-29T00:00:00Z", nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("outputPatrolScanJSON: %v", err)
	}
	if strings.Contains(buf.String(), `"Fleet"`) {
		t.Errorf("expected no Fleet field when fleet is nil; got %s", buf.String())
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCountActiveWorkZombies(t *testing.T) {
	result := &witness.DetectZombiePolecatsResult{
		Zombies: []witness.ZombieResult{
			{PolecatName: "alpha", WasActive: true},
			{PolecatName: "beta", WasActive: false},
			{PolecatName: "gamma", WasActive: true},
		},
	}

	got := countActiveWorkZombies(result)
	if got != 2 {
		t.Errorf("countActiveWorkZombies() = %d, want 2", got)
	}
}

func TestCountActiveWorkZombies_Empty(t *testing.T) {
	result := &witness.DetectZombiePolecatsResult{}
	got := countActiveWorkZombies(result)
	if got != 0 {
		t.Errorf("countActiveWorkZombies() = %d, want 0", got)
	}
}

func TestRunPatrolScanPhaseEmitsProgressDiagnostics(t *testing.T) {
	oldInterval := patrolScanProgressInterval
	patrolScanProgressInterval = 10 * time.Millisecond
	defer func() { patrolScanProgressInterval = oldInterval }()

	diagnostics := &progressDiagnostics{sawProgress: make(chan struct{})}
	release := make(chan struct{})
	go func() {
		select {
		case <-diagnostics.sawProgress:
		case <-time.After(time.Second):
		}
		close(release)
	}()

	got := runPatrolScanPhase(diagnostics, "slow phase", func() string {
		<-release
		return "ok"
	})

	if got != "ok" {
		t.Fatalf("runPatrolScanPhase result = %q, want ok", got)
	}

	output := diagnostics.String()
	select {
	case <-diagnostics.sawProgress:
	default:
		t.Fatalf("diagnostics %q never emitted progress", output)
	}
	for _, want := range []string{
		"gt patrol scan: starting slow phase",
		"gt patrol scan: still running slow phase after",
		"gt patrol scan: finished slow phase in",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("diagnostics %q missing %q", output, want)
		}
	}
}

func TestRunPatrolScanPhaseZeroIntervalSkipsProgressTicks(t *testing.T) {
	oldInterval := patrolScanProgressInterval
	patrolScanProgressInterval = 0
	defer func() { patrolScanProgressInterval = oldInterval }()

	var diagnostics bytes.Buffer
	got := runPatrolScanPhase(&diagnostics, "fast phase", func() int {
		return 42
	})

	if got != 42 {
		t.Fatalf("runPatrolScanPhase result = %d, want 42", got)
	}

	output := diagnostics.String()
	if strings.Contains(output, "still running") {
		t.Fatalf("diagnostics should not include progress tick when interval is disabled: %q", output)
	}
	for _, want := range []string{
		"gt patrol scan: starting fast phase",
		"gt patrol scan: finished fast phase in",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("diagnostics %q missing %q", output, want)
		}
	}
}

func TestPatrolScanZombieItemSerialization(t *testing.T) {
	item := PatrolScanZombieItem{
		Polecat:        "obsidian",
		Classification: "agent-dead-in-session",
		AgentState:     "working",
		HookBead:       "gas-xyz",
		CleanupStatus:  "has_uncommitted",
		Action:         "restarted-dirty (cleanup_status=has_uncommitted, wisp=gas-wisp-123)",
		WasActive:      true,
		Error:          "restart failed: tmux error",
	}

	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("failed to marshal item: %v", err)
	}

	var parsed PatrolScanZombieItem
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal item: %v", err)
	}

	if parsed.Polecat != "obsidian" {
		t.Errorf("Polecat = %q, want %q", parsed.Polecat, "obsidian")
	}
	if parsed.CleanupStatus != "has_uncommitted" {
		t.Errorf("CleanupStatus = %q, want %q", parsed.CleanupStatus, "has_uncommitted")
	}
	if parsed.Error != "restart failed: tmux error" {
		t.Errorf("Error = %q, want %q", parsed.Error, "restart failed: tmux error")
	}
}

// alertFakeBeadsClient is a minimal in-memory alerts.BeadsClient for testing
// sendZombieNotification without touching a real Dolt database.
type alertFakeBeadsClient struct {
	issues []*beads.Issue
	nextID int
}

func (f *alertFakeBeadsClient) Search(opts beads.SearchOptions) ([]*beads.Issue, error) {
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
	return out, nil
}

func (f *alertFakeBeadsClient) Create(opts beads.CreateOptions) (*beads.Issue, error) {
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

func (f *alertFakeBeadsClient) Update(id string, opts beads.UpdateOptions) error {
	for _, issue := range f.issues {
		if issue.ID != id {
			continue
		}
		if opts.Description != nil {
			issue.Description = *opts.Description
		}
		if opts.Priority != nil {
			issue.Priority = *opts.Priority
		}
		if len(opts.SetLabels) > 0 {
			issue.Labels = opts.SetLabels
		}
		return nil
	}
	return fmt.Errorf("issue not found: %s", id)
}

type alertFakeSender struct {
	messages []*mail.Message
}

func (f *alertFakeSender) Send(msg *mail.Message) error {
	f.messages = append(f.messages, msg)
	return nil
}

func TestSendZombieNotification_CreatesCanonicalAlertBeads(t *testing.T) {
	client := &alertFakeBeadsClient{}
	sender := &alertFakeSender{}

	result := &witness.DetectZombiePolecatsResult{
		Zombies: []witness.ZombieResult{
			{
				PolecatName:    "onyx",
				WasActive:      true,
				Classification: witness.ZombieSessionDeadActive,
				HookBead:       "gt-123",
				Action:         "restarted",
			},
		},
	}

	sendZombieNotification(sender, client, "gastown", result, 1)

	if len(client.issues) != 2 {
		t.Fatalf("expected 2 canonical alert beads (ZOMBIE_DETECTED + POLECAT_DIED), got %d", len(client.issues))
	}

	labels := []string{
		alerts.RootCauseKey{Class: alerts.ClassZombieDetected, Scope: "gastown"}.Label(),
		alerts.RootCauseKey{Class: alerts.ClassPolecatDied, Scope: "gastown"}.Label(),
	}
	for i, wantLabel := range labels {
		if !beads.HasLabel(client.issues[i], wantLabel) {
			t.Errorf("issue %d missing label %q, labels=%v", i, wantLabel, client.issues[i].Labels)
		}
	}

	// Verify the description carries the affected agent and occurrence metadata.
	polecatDied := client.issues[1]
	if !strings.Contains(polecatDied.Description, "gastown/onyx") {
		t.Errorf("POLECAT_DIED bead missing affected agent: %s", polecatDied.Description)
	}

	// Both witness and mayor should receive a notification referencing the bead.
	if len(sender.messages) != 2 {
		t.Errorf("expected 2 notification messages, got %d", len(sender.messages))
	}
}

func TestSendZombieNotification_AggregatesRepeatedAlerts(t *testing.T) {
	client := &alertFakeBeadsClient{}
	sender := &alertFakeSender{}

	for i := 0; i < 3; i++ {
		result := &witness.DetectZombiePolecatsResult{
			Zombies: []witness.ZombieResult{
				{PolecatName: fmt.Sprintf("onyx-%d", i), WasActive: true, Action: "restarted"},
			},
		}
		sendZombieNotification(sender, client, "gastown", result, 1)
	}

	if len(client.issues) != 2 {
		t.Fatalf("expected 2 canonical alert beads after repeated scans, got %d", len(client.issues))
	}

	for _, issue := range client.issues {
		if !strings.Contains(issue.Description, "**Total occurrences**: 3") {
			t.Errorf("expected 3 occurrences in %s: %s", issue.Title, issue.Description)
		}
	}
}
