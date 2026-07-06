//go:build integration

package polecat

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/testutil"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/util"
)

var polecatManagerIntegrationCounter atomic.Int32

func initBeadsDBWithPrefix(t *testing.T, dir, prefix string) {
	t.Helper()
	testutil.RequireDoltContainer(t)

	args := []string{"init", "--quiet", "--prefix", prefix, "--server-port", testutil.DoltContainerPort()}
	cmd := exec.Command("bd", args...)
	cmd.Dir = dir
	// bd init --server may start a transient dolt sql-server. Put it in its
	// own process group with Pdeathsig (Linux) so test interruption kills the
	// child instead of stranding an orphan.
	util.SetTestProcessGroup(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init failed in %s: %v\n%s", dir, err, output)
	}

	issuesPath := filepath.Join(dir, ".beads", "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(""), 0644); err != nil {
		t.Fatalf("create issues.jsonl in %s: %v", dir, err)
	}
}

func requireTmuxIntegration(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed, skipping integration test")
	}
}

func startLiveSession(t *testing.T, sessionName string) {
	t.Helper()

	tm := tmux.NewTmux()
	if err := tm.NewSessionWithCommand(sessionName, "", "sleep 60"); err != nil {
		t.Fatalf("start tmux session %s: %v", sessionName, err)
	}
	t.Cleanup(func() {
		_ = tm.KillSessionWithProcesses(sessionName)
	})
}

// TestManagerGetPrefersHookedBeadOverStaleAgentHook verifies that manager.Get
// reports the current hooked work bead when agent hook_bead is stale.
func TestManagerGetPrefersHookedBeadOverStaleAgentHook(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping integration test")
	}
	testutil.RequireDoltContainer(t)

	n := polecatManagerIntegrationCounter.Add(1)
	prefix := fmt.Sprintf("pm%d", n)

	townRoot := t.TempDir()
	rigName := "testrig"
	rigPath := filepath.Join(townRoot, rigName)
	mayorRigPath := filepath.Join(rigPath, "mayor", "rig")

	if err := os.MkdirAll(mayorRigPath, 0755); err != nil {
		t.Fatalf("mkdir mayor rig path: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(rigPath, "polecats", "toast"), 0755); err != nil {
		t.Fatalf("mkdir polecat dir: %v", err)
	}

	// Rig .beads redirects to mayor/rig/.beads so NewManager resolves correctly.
	rigBeadsDir := filepath.Join(rigPath, ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "redirect"), []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}

	// Town routing with unique prefix for this test DB.
	townBeadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir town .beads: %v", err)
	}
	routes := []beads.Route{
		{Prefix: "hq-", Path: "."},
		{Prefix: prefix + "-", Path: filepath.Join(rigName, "mayor", "rig")},
	}
	if err := beads.WriteRoutes(townBeadsDir, routes); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	initBeadsDBWithPrefix(t, mayorRigPath, prefix)

	r := &rig.Rig{Name: rigName, Path: rigPath}
	mgr := NewManager(r, git.NewGit(rigPath), nil)

	stale, err := mgr.beads.Create(beads.CreateOptions{
		Title:    "stale old issue",
		Type:     "task",
		Priority: 2,
	})
	if err != nil {
		t.Fatalf("create stale issue: %v", err)
	}
	current, err := mgr.beads.Create(beads.CreateOptions{
		Title:    "current hooked issue",
		Type:     "task",
		Priority: 2,
	})
	if err != nil {
		t.Fatalf("create current issue: %v", err)
	}

	assignee := mgr.assigneeID("toast")
	hooked := beads.StatusHooked
	if err := mgr.beads.Update(current.ID, beads.UpdateOptions{
		Status:   &hooked,
		Assignee: &assignee,
	}); err != nil {
		t.Fatalf("hook current issue: %v", err)
	}

	agentID := mgr.agentBeadID("toast")
	if _, err := mgr.beads.CreateOrReopenAgentBead(agentID, assignee, &beads.AgentFields{
		HookBead:   stale.ID,
		AgentState: string(beads.AgentStateWorking),
	}); err != nil {
		t.Fatalf("create agent bead with stale hook: %v", err)
	}

	p, err := mgr.Get("toast")
	if err != nil {
		t.Fatalf("mgr.Get(toast): %v", err)
	}

	if p.State != StateWorking {
		t.Fatalf("polecat state = %q, want %q", p.State, StateWorking)
	}
	if p.Issue != current.ID {
		t.Fatalf("polecat issue = %q, want hooked issue %q (stale hook %q)", p.Issue, current.ID, stale.ID)
	}
}

func TestManagerTreatsLiveSessionWithoutWorkAsReviewNeeded(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping integration test")
	}
	requireTmuxIntegration(t)
	testutil.RequireDoltContainer(t)

	n := polecatManagerIntegrationCounter.Add(1)
	prefix := fmt.Sprintf("pm%d", n)

	townRoot := t.TempDir()
	rigName := "testrig"
	rigPath := filepath.Join(townRoot, rigName)
	mayorRigPath := filepath.Join(rigPath, "mayor", "rig")

	if err := os.MkdirAll(mayorRigPath, 0755); err != nil {
		t.Fatalf("mkdir mayor rig path: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(rigPath, "polecats", "toast"), 0755); err != nil {
		t.Fatalf("mkdir polecat dir: %v", err)
	}

	rigBeadsDir := filepath.Join(rigPath, ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "redirect"), []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}

	townBeadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir town .beads: %v", err)
	}
	routes := []beads.Route{
		{Prefix: "hq-", Path: "."},
		{Prefix: prefix + "-", Path: filepath.Join(rigName, "mayor", "rig")},
	}
	if err := beads.WriteRoutes(townBeadsDir, routes); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	initBeadsDBWithPrefix(t, mayorRigPath, prefix)

	r := &rig.Rig{Name: rigName, Path: rigPath}
	tm := tmux.NewTmux()
	mgr := NewManager(r, git.NewGit(rigPath), tm)

	agentID := mgr.agentBeadID("toast")
	assignee := mgr.assigneeID("toast")
	if _, err := mgr.beads.CreateOrReopenAgentBead(agentID, assignee, &beads.AgentFields{
		AgentState: string(beads.AgentStateIdle),
	}); err != nil {
		t.Fatalf("create idle agent bead: %v", err)
	}

	sessionName := NewSessionManager(tm, r).SessionName("toast")
	startLiveSession(t, sessionName)

	p, err := mgr.Get("toast")
	if err != nil {
		t.Fatalf("mgr.Get(toast): %v", err)
	}
	if p.State != StateReviewNeeded {
		t.Fatalf("polecat state = %q, want %q when tmux session is alive without work", p.State, StateReviewNeeded)
	}
	if p.Issue != "" {
		t.Fatalf("polecat issue = %q, want empty when no active hooked/assigned work exists", p.Issue)
	}

	idle, err := mgr.FindIdlePolecat()
	if err != nil {
		t.Fatalf("mgr.FindIdlePolecat(): %v", err)
	}
	if idle != nil {
		t.Fatalf("FindIdlePolecat() = %q, want nil while session %s needs review", idle.Name, sessionName)
	}
}

// TestWorkstateDispositionForPolecat_LiveHookedSessionIsWorking is the
// gastown-9rl regression test: a freshly started active session with a hooked
// issue, live tmux session, fresh heartbeat, and live process must be
// classified as WORKING by the canonical workstate path, never as
// NEEDS_RECOVERY / no-liveness-data. The persisted state is StateStalled to
// simulate the false-stalled regression: direct liveness evidence must
// override stale persisted state via the now-wired LivenessSignals.
func TestWorkstateDispositionForPolecat_LiveHookedSessionIsWorking(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping integration test")
	}
	requireTmuxIntegration(t)
	testutil.RequireDoltContainer(t)

	n := polecatManagerIntegrationCounter.Add(1)
	prefix := fmt.Sprintf("pm%d", n)

	townRoot := t.TempDir()
	rigName := "testrig"
	rigPath := filepath.Join(townRoot, rigName)
	mayorRigPath := filepath.Join(rigPath, "mayor", "rig")

	if err := os.MkdirAll(mayorRigPath, 0755); err != nil {
		t.Fatalf("mkdir mayor rig path: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(rigPath, "polecats", "toast"), 0755); err != nil {
		t.Fatalf("mkdir polecat dir: %v", err)
	}

	rigBeadsDir := filepath.Join(rigPath, ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "redirect"), []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}

	townBeadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir town .beads: %v", err)
	}
	routes := []beads.Route{
		{Prefix: "hq-", Path: "."},
		{Prefix: prefix + "-", Path: filepath.Join(rigName, "mayor", "rig")},
	}
	if err := beads.WriteRoutes(townBeadsDir, routes); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	initBeadsDBWithPrefix(t, mayorRigPath, prefix)

	r := &rig.Rig{Name: rigName, Path: rigPath}
	tm := tmux.NewTmux()
	mgr := NewManager(r, git.NewGit(rigPath), tm)

	current, err := mgr.beads.Create(beads.CreateOptions{
		Title:    "current hooked issue",
		Type:     "task",
		Priority: 2,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	assignee := mgr.assigneeID("toast")
	hooked := beads.StatusHooked
	if err := mgr.beads.Update(current.ID, beads.UpdateOptions{
		Status:   &hooked,
		Assignee: &assignee,
	}); err != nil {
		t.Fatalf("hook issue: %v", err)
	}

	if _, err := mgr.beads.CreateOrReopenAgentBead(mgr.agentBeadID("toast"), assignee, &beads.AgentFields{
		HookBead:   current.ID,
		AgentState: string(beads.AgentStateWorking),
	}); err != nil {
		t.Fatalf("create agent bead: %v", err)
	}

	sessionName := NewSessionManager(tm, r).SessionName("toast")
	startLiveSession(t, sessionName)
	TouchSessionHeartbeat(townRoot, sessionName)

	// StateStalled simulates the false-stalled regression: direct liveness
	// evidence must override stale persisted state via the now-wired
	// LivenessSignals.
	disposition := mgr.WorkstateDispositionForPolecat("toast", StateStalled, current.ID)
	if disposition.Verdict != WorkstateVerdictWorking {
		t.Fatalf("Verdict = %q, want %q", disposition.Verdict, WorkstateVerdictWorking)
	}
	if disposition.Reason != "live-hooked" {
		t.Fatalf("Reason = %q, want live-hooked", disposition.Reason)
	}
	if disposition.Confidence != WorkstateConfidenceHigh {
		t.Fatalf("Confidence = %q, want %q", disposition.Confidence, WorkstateConfidenceHigh)
	}
	if disposition.NeedsRecovery {
		t.Fatalf("NeedsRecovery = true, want false")
	}

	seenLiveSignal := false
	for _, s := range disposition.Signals {
		if s == "process_alive=true" || s == "session_running=true" || s == "heartbeat_fresh=true" {
			seenLiveSignal = true
			break
		}
	}
	if !seenLiveSignal {
		t.Fatalf("Signals = %v, want a live signal", disposition.Signals)
	}
}

// TestLivenessSignals_AbsentSessionIsCleanDeadSignal is the gastown-9rl
// codex-warning regression: when tmux.HasSession reports the session does
// not exist (a clean not-found), LivenessSignals must return all-false
// signals with err=nil. Otherwise the fallback PID probe (GetPanePID) on a
// missing session reports a "no such session" error, which makes a normal
// absent-session / no-heartbeat case look like a liveness tool failure and
// causes workstateInputForPolecat to fail closed incorrectly (gastown-9rl).
func TestLivenessSignals_AbsentSessionIsCleanDeadSignal(t *testing.T) {
	requireTmuxIntegration(t)

	r := &rig.Rig{Name: "testrig", Path: t.TempDir()}
	tm := tmux.NewTmux()
	mgr := NewManager(r, git.NewGit(r.Path), tm)

	// Make sure the session does not exist (we never started one).
	sessionName := NewSessionManager(tm, r).SessionName("absent")
	_ = tm.KillSessionWithProcesses(sessionName)

	running, fresh, exists, alive, err := mgr.LivenessSignals("absent")
	if err != nil {
		t.Fatalf("LivenessSignals for absent session returned err=%v, want nil (clean dead signal)", err)
	}
	if running || fresh || exists || alive {
		t.Fatalf("LivenessSignals for absent session = (%v, %v, %v, %v, nil), want (false, false, false, false, nil)",
			running, fresh, exists, alive)
	}
}
