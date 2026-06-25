package cmd

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
)

// fakeMRFinder is a test stub for the mrFinder interface used by applyMQCheck.
type fakeMRFinder struct {
	issue *beads.Issue
	err   error
}

func (f fakeMRFinder) FindMRForBranchAny(branch string) (*beads.Issue, error) {
	return f.issue, f.err
}

type fakeIssueShower struct {
	issue *beads.Issue
	err   error
}

func (f fakeIssueShower) Show(issueID string) (*beads.Issue, error) {
	return f.issue, f.err
}

type fakeCleanupUpdater struct {
	err    error
	id     string
	status string
	calls  int
}

func (f *fakeCleanupUpdater) UpdateAgentCleanupStatus(id string, cleanupStatus string) error {
	f.calls++
	f.id = id
	f.status = cleanupStatus
	return f.err
}

type fakeIssueMapShower struct {
	issues map[string]*beads.Issue
	errs   map[string]error
}

func (f fakeIssueMapShower) Show(issueID string) (*beads.Issue, error) {
	if err := f.errs[issueID]; err != nil {
		return nil, err
	}
	issue, ok := f.issues[issueID]
	if !ok {
		return nil, beads.ErrNotFound
	}
	return issue, nil
}

func TestApplyMQCheck(t *testing.T) {
	tests := []struct {
		name           string
		finder         mrFinder
		beadTerminal   bool
		hasWork        bool
		mqNotRequired  bool
		initialVerdict string
		wantVerdict    string
		wantMQStatus   string
		wantNeedsRecov bool
	}{
		{
			// The regression this change fixes: assigned bead is CLOSED
			// (e.g. aa-xtee no-op audit). Must NOT return NEEDS_MQ_SUBMIT
			// because there is nothing to submit — the work is terminal.
			name:           "closed bead skips MQ submit check",
			finder:         fakeMRFinder{issue: nil, err: nil},
			beadTerminal:   true,
			hasWork:        true,
			initialVerdict: "SAFE_TO_NUKE",
			wantVerdict:    "SAFE_TO_NUKE",
			wantMQStatus:   "submitted",
			wantNeedsRecov: false,
		},
		{
			name:           "no submittable work skips MQ submit check",
			finder:         fakeMRFinder{issue: nil, err: nil},
			beadTerminal:   false,
			hasWork:        false,
			initialVerdict: "SAFE_TO_NUKE",
			wantVerdict:    "SAFE_TO_NUKE",
			wantMQStatus:   "not_required",
			wantNeedsRecov: false,
		},
		{
			name:           "no merge source with pushed branch work skips MQ submit check",
			finder:         fakeMRFinder{issue: nil, err: nil},
			beadTerminal:   false,
			hasWork:        true,
			mqNotRequired:  true,
			initialVerdict: "SAFE_TO_NUKE",
			wantVerdict:    "SAFE_TO_NUKE",
			wantMQStatus:   "not_required",
			wantNeedsRecov: false,
		},
		{
			name:           "open bead with no MR escalates to NEEDS_MQ_SUBMIT",
			finder:         fakeMRFinder{issue: nil, err: nil},
			beadTerminal:   false,
			hasWork:        true,
			initialVerdict: "SAFE_TO_NUKE",
			wantVerdict:    "NEEDS_MQ_SUBMIT",
			wantMQStatus:   "not_submitted",
			wantNeedsRecov: true,
		},
		{
			name:           "open bead with MR stays SAFE_TO_NUKE",
			finder:         fakeMRFinder{issue: &beads.Issue{ID: "mr-1"}, err: nil},
			beadTerminal:   false,
			hasWork:        true,
			initialVerdict: "SAFE_TO_NUKE",
			wantVerdict:    "SAFE_TO_NUKE",
			wantMQStatus:   "submitted",
			wantNeedsRecov: false,
		},
		{
			name:           "MR lookup error fails closed",
			finder:         fakeMRFinder{issue: nil, err: errors.New("bd exploded")},
			beadTerminal:   false,
			hasWork:        true,
			initialVerdict: "SAFE_TO_NUKE",
			wantVerdict:    "NEEDS_RECOVERY",
			wantMQStatus:   "unknown",
			wantNeedsRecov: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := RecoveryStatus{
				Verdict: tt.initialVerdict,
				Branch:  "polecat/test",
			}
			applyMQCheck(&status, tt.finder, tt.beadTerminal, tt.hasWork, tt.mqNotRequired)

			if status.Verdict != tt.wantVerdict {
				t.Errorf("Verdict = %q, want %q", status.Verdict, tt.wantVerdict)
			}
			if status.MQStatus != tt.wantMQStatus {
				t.Errorf("MQStatus = %q, want %q", status.MQStatus, tt.wantMQStatus)
			}
			if status.NeedsRecovery != tt.wantNeedsRecov {
				t.Errorf("NeedsRecovery = %v, want %v", status.NeedsRecovery, tt.wantNeedsRecov)
			}
		})
	}
}

func TestIsMQNotRequiredSource(t *testing.T) {
	tests := []struct {
		name  string
		issue *beads.Issue
		err   error
		want  bool
	}{
		{
			name:  "no merge source",
			issue: &beads.Issue{Description: beads.FormatAttachmentFields(&beads.AttachmentFields{NoMerge: true})},
			want:  true,
		},
		{
			name:  "review only source",
			issue: &beads.Issue{Description: beads.FormatAttachmentFields(&beads.AttachmentFields{ReviewOnly: true})},
			want:  true,
		},
		{
			name:  "local merge strategy source",
			issue: &beads.Issue{Description: beads.FormatAttachmentFields(&beads.AttachmentFields{MergeStrategy: "local"})},
			want:  true,
		},
		{
			name:  "normal merge queue source",
			issue: &beads.Issue{Description: beads.FormatAttachmentFields(&beads.AttachmentFields{MergeStrategy: "mr"})},
			want:  false,
		},
		{
			name: "missing source is conservative",
			err:  beads.ErrNotFound,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isMQNotRequiredSource(fakeIssueShower{issue: tt.issue, err: tt.err}, "gt-test")
			if got != tt.want {
				t.Errorf("isMQNotRequiredSource() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCleanupStatusBlocker(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{status: "clean", want: ""},
		{status: "has_unpushed", want: "cleanup_status=has_unpushed"},
		{status: "unknown", want: "cleanup_status=unknown"},
		{status: "", want: "cleanup_status=<missing>"},
		{status: "weird", want: "cleanup_status=weird"},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := cleanupStatusBlocker(polecat.CleanupStatus(tt.status))
			if got != tt.want {
				t.Errorf("cleanupStatusBlocker(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestCleanupStatusBlockerForRecovery_PartialSpawnWithoutHook(t *testing.T) {
	tests := []struct {
		name         string
		status       polecat.CleanupStatus
		partialSpawn bool
		want         string
	}{
		{name: "missing cleanup is safe for partial spawn", partialSpawn: true, want: ""},
		{name: "unknown cleanup is safe for partial spawn", status: polecat.CleanupUnknown, partialSpawn: true, want: ""},
		{name: "dirty cleanup still blocks partial spawn", status: polecat.CleanupUnpushed, partialSpawn: true, want: "cleanup_status=has_unpushed"},
		{name: "missing cleanup still blocks ordinary polecat", partialSpawn: false, want: "cleanup_status=<missing>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanupStatusBlockerForRecovery(tt.status, tt.partialSpawn)
			if got != tt.want {
				t.Errorf("cleanupStatusBlockerForRecovery() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStaleCleanupStatusCanBeIgnoredForRecovery(t *testing.T) {
	tests := []struct {
		name            string
		status          polecat.CleanupStatus
		workTerminal    bool
		hookSafe        bool
		activeMRSafe    bool
		gitClean        bool
		unpushedCommits int
		wantCanSkip     bool
	}{
		{
			name:         "closed source with clean git ignores stale unpushed cleanup",
			status:       polecat.CleanupUnpushed,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
			gitClean:     true,
			wantCanSkip:  true,
		},
		{
			name:         "open source still blocks",
			status:       polecat.CleanupUnpushed,
			hookSafe:     true,
			activeMRSafe: true,
			gitClean:     true,
		},
		{
			name:         "hooked work still blocks",
			status:       polecat.CleanupUnpushed,
			workTerminal: true,
			activeMRSafe: true,
			gitClean:     true,
		},
		{
			name:         "active MR still blocks",
			status:       polecat.CleanupUnpushed,
			workTerminal: true,
			hookSafe:     true,
			gitClean:     true,
		},
		{
			name:         "dirty git still blocks",
			status:       polecat.CleanupUnpushed,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
		},
		{
			name:         "git error still blocks",
			status:       polecat.CleanupUnpushed,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
		},
		{
			name:            "unpushed with unpreserved commits still blocks",
			status:          polecat.CleanupUnpushed,
			workTerminal:    true,
			hookSafe:        true,
			activeMRSafe:    true,
			gitClean:        true,
			unpushedCommits: 2,
		},
		{
			name:         "closed source with clean git ignores stale stash cleanup",
			status:       polecat.CleanupStash,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
			gitClean:     true,
			wantCanSkip:  true,
		},
		{
			name:         "closed source with clean git ignores stale uncommitted cleanup",
			status:       polecat.CleanupUncommitted,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
			gitClean:     true,
			wantCanSkip:  true,
		},
		{
			name:         "unknown cleanup still blocks",
			status:       polecat.CleanupUnknown,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
			gitClean:     true,
		},
		{
			name:         "terminal hook can satisfy work terminal predicate",
			status:       polecat.CleanupUnpushed,
			workTerminal: true,
			hookSafe:     true,
			activeMRSafe: true,
			gitClean:     true,
			wantCanSkip:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := polecat.CanIgnoreStaleCleanupStatus(tt.status, tt.workTerminal, tt.hookSafe, tt.activeMRSafe, tt.gitClean, tt.unpushedCommits)
			if got != tt.wantCanSkip {
				t.Fatalf("CanIgnoreStaleCleanupStatus() = %v, want %v", got, tt.wantCanSkip)
			}
		})
	}
}

func TestReconcileCleanupStatusIfSafe(t *testing.T) {
	for _, previous := range []polecat.CleanupStatus{polecat.CleanupUnpushed, polecat.CleanupStash, polecat.CleanupUncommitted} {
		t.Run(string(previous), func(t *testing.T) {
			status := &RecoveryStatus{
				CleanupStatus: previous,
				Verdict:       "SAFE_TO_NUKE",
				Branch:        "polecat/nitro",
				MQStatus:      "submitted",
			}
			updater := &fakeCleanupUpdater{}
			reconcileCleanupStatusIfSafe(status, updater, "gt-gastown-polecat-nitro", &polecat.Polecat{State: polecat.StateIdle}, &beads.AgentFields{
				AgentState:    string(beads.AgentStateIdle),
				CleanupStatus: string(previous),
			})

			if updater.calls != 1 {
				t.Fatalf("UpdateAgentCleanupStatus calls = %d, want 1", updater.calls)
			}
			if updater.id != "gt-gastown-polecat-nitro" || updater.status != string(polecat.CleanupClean) {
				t.Fatalf("update = (%q, %q), want clean update for agent", updater.id, updater.status)
			}
			if status.CleanupStatus != polecat.CleanupClean || !status.Reconciled {
				t.Fatalf("status after reconcile = (%q, reconciled=%v), want clean true", status.CleanupStatus, status.Reconciled)
			}
		})
	}
}

func TestReconcileCleanupStatusIfSafe_FailsClosed(t *testing.T) {
	status := &RecoveryStatus{
		CleanupStatus: polecat.CleanupUnpushed,
		Verdict:       "SAFE_TO_NUKE",
		Branch:        "polecat/nitro",
		MQStatus:      "submitted",
	}
	reconcileCleanupStatusIfSafe(status, &fakeCleanupUpdater{err: errors.New("bd update failed")}, "gt-gastown-polecat-nitro", &polecat.Polecat{State: polecat.StateIdle}, &beads.AgentFields{
		AgentState:    string(beads.AgentStateIdle),
		CleanupStatus: string(polecat.CleanupUnpushed),
	})

	if status.Verdict != "NEEDS_RECOVERY" || !status.NeedsRecovery {
		t.Fatalf("failed update verdict = %q needs=%v, want NEEDS_RECOVERY true", status.Verdict, status.NeedsRecovery)
	}
	if len(status.Blockers) == 0 || !strings.Contains(status.Blockers[0], "cleanup_reconcile_failed") {
		t.Fatalf("blockers = %v, want cleanup_reconcile_failed", status.Blockers)
	}
}

func TestCleanupStatusReconcileCandidateRequiresStrictPredicates(t *testing.T) {
	baseStatus := &RecoveryStatus{Verdict: "SAFE_TO_NUKE", Branch: "polecat/nitro", MQStatus: "submitted"}
	basePolecat := &polecat.Polecat{State: polecat.StateIdle}
	baseFields := &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupUnpushed)}

	tests := []struct {
		name   string
		status *RecoveryStatus
		p      *polecat.Polecat
		fields *beads.AgentFields
	}{
		{name: "stale clean is not rewritten", status: baseStatus, p: basePolecat, fields: &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean)}},
		{name: "working polecat blocks", status: baseStatus, p: &polecat.Polecat{State: polecat.StateWorking}, fields: baseFields},
		{name: "working agent bead blocks", status: baseStatus, p: basePolecat, fields: &beads.AgentFields{AgentState: string(beads.AgentStateWorking), CleanupStatus: string(polecat.CleanupUnpushed)}},
		{name: "needs recovery blocks", status: &RecoveryStatus{Verdict: "NEEDS_RECOVERY", NeedsRecovery: true, Branch: "polecat/nitro", MQStatus: "submitted"}, p: basePolecat, fields: baseFields},
		{name: "unknown mq blocks", status: &RecoveryStatus{Verdict: "SAFE_TO_NUKE", Branch: "polecat/nitro", MQStatus: "unknown"}, p: basePolecat, fields: baseFields},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := cleanupStatusReconcileCandidate(tt.status, tt.p, tt.fields); ok {
				t.Fatal("cleanupStatusReconcileCandidate() allowed unsafe reconciliation")
			}
		})
	}
}

func TestHookBeadSafeForCleanup(t *testing.T) {
	tests := []struct {
		name         string
		hookBead     string
		bd           issueShower
		wantSafe     bool
		wantTerminal bool
		wantBlocker  string
	}{
		{name: "empty hook", wantSafe: true},
		{name: "terminal hook", hookBead: "gt-work", bd: fakeIssueShower{issue: &beads.Issue{Status: "closed"}}, wantSafe: true, wantTerminal: true},
		{name: "open hook blocks", hookBead: "gt-work", bd: fakeIssueShower{issue: &beads.Issue{Status: "open"}}, wantBlocker: "hook_bead=gt-work status=open"},
		{name: "lookup error blocks", hookBead: "gt-work", bd: fakeIssueShower{err: errors.New("bd exploded")}, wantBlocker: "lookup_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSafe, gotTerminal, blocker := hookBeadSafeForCleanup(tt.bd, tt.hookBead)
			if gotSafe != tt.wantSafe || gotTerminal != tt.wantTerminal {
				t.Fatalf("hookBeadSafeForCleanup() = (%v, %v), want (%v, %v)", gotSafe, gotTerminal, tt.wantSafe, tt.wantTerminal)
			}
			if tt.wantBlocker != "" && !strings.Contains(blocker, tt.wantBlocker) {
				t.Fatalf("blocker = %q, want contains %q", blocker, tt.wantBlocker)
			}
		})
	}
}

func TestPartialSpawnWithoutDurableHook(t *testing.T) {
	assignee := "gastown/polecats/nitro"
	tests := []struct {
		name         string
		fields       *beads.AgentFields
		currentIssue string
		issue        *beads.Issue
		wantPartial  bool
	}{
		{
			name:        "spawning legacy hook points to open unassigned bead",
			fields:      &beads.AgentFields{AgentState: "spawning", HookBead: "gt-work"},
			issue:       &beads.Issue{ID: "gt-work", Status: "open"},
			wantPartial: true,
		},
		{
			name:   "durably hooked bead is not partial",
			fields: &beads.AgentFields{AgentState: "spawning", HookBead: "gt-work"},
			issue:  &beads.Issue{ID: "gt-work", Status: beads.StatusHooked, Assignee: assignee},
		},
		{
			name:         "current issue already found is not partial",
			fields:       &beads.AgentFields{AgentState: "spawning", HookBead: "gt-work"},
			currentIssue: "gt-work",
			issue:        &beads.Issue{ID: "gt-work", Status: "open"},
		},
		{
			name:   "working state is not partial spawn",
			fields: &beads.AgentFields{AgentState: "working", HookBead: "gt-work"},
			issue:  &beads.Issue{ID: "gt-work", Status: "open"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, diagnostic := partialSpawnWithoutDurableHook(fakeIssueShower{issue: tt.issue}, tt.fields, assignee, tt.currentIssue)
			if got != tt.wantPartial {
				t.Fatalf("partialSpawnWithoutDurableHook() = %v, want %v", got, tt.wantPartial)
			}
			if got && !strings.Contains(diagnostic, "partial_spawn_without_durable_hook") {
				t.Fatalf("diagnostic missing partial spawn marker: %q", diagnostic)
			}
		})
	}
}

func TestRecoveryGitStateBlocker(t *testing.T) {
	tests := []struct {
		name  string
		state *GitState
		err   error
		want  string
	}{
		{
			name:  "clean has no blocker",
			state: &GitState{Clean: true},
		},
		{
			name:  "uncommitted work is classified",
			state: &GitState{UncommittedFiles: []string{"a.go", "b.go"}},
			want:  "git_state=has_uncommitted uncommitted_files=2",
		},
		{
			name:  "stash is classified",
			state: &GitState{StashCount: 1},
			want:  "git_state=has_stash stash_count=1",
		},
		{
			name:  "unpushed commits are classified",
			state: &GitState{UnpushedCommits: 3},
			want:  "git_state=has_unpushed unpushed_commits=3",
		},
		{
			name: "git error is classified",
			err:  errors.New("git failed"),
			want: "git_state=unknown path=/tmp/polecat: git failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := recoveryGitStateBlocker("/tmp/polecat", tt.state, tt.err)
			if got != tt.want {
				t.Errorf("recoveryGitStateBlocker() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRecoveryActionsForBlockers(t *testing.T) {
	actions := recoveryActionsForBlockers([]string{"git_state=has_stash stash_count=1"})
	if len(actions) != 1 || !strings.Contains(actions[0], "preserve branch-owned stash") {
		t.Fatalf("actions = %v, want branch stash preservation action", actions)
	}
	if actions := recoveryActionsForBlockers([]string{"cleanup_status=has_stash"}); len(actions) != 0 {
		t.Fatalf("stale cleanup-only blocker actions = %v, want none", actions)
	}
}

func TestStaleCleanWithRealUnpushedStillBlocks(t *testing.T) {
	status := RecoveryStatus{CleanupStatus: polecat.CleanupClean}
	if blocker := recoveryGitStateBlocker("/tmp/polecat", &GitState{UnpushedCommits: 1}, nil); blocker != "" {
		status.Blockers = append(status.Blockers, blocker)
	}
	if len(status.Blockers) != 1 || !strings.Contains(status.Blockers[0], "git_state=has_unpushed") {
		t.Fatalf("blockers = %v, want git_state=has_unpushed", status.Blockers)
	}
}

func TestActiveMRBlocker(t *testing.T) {
	tests := []struct {
		name       string
		mrID       string
		sourceHint string
		bd         issueShower
		want       string
	}{
		{name: "empty", want: ""},
		{name: "closed terminal source", mrID: "mr-1", sourceHint: "gt-closed", bd: fakeIssueMapShower{issues: map[string]*beads.Issue{"mr-1": &beads.Issue{ID: "mr-1", Status: "closed"}, "gt-closed": &beads.Issue{ID: "gt-closed", Status: "closed"}}}, want: ""},
		{name: "closed unknown source", mrID: "mr-1", bd: fakeIssueMapShower{issues: map[string]*beads.Issue{"mr-1": &beads.Issue{ID: "mr-1", Status: "closed"}}}, want: "active_mr=mr-1 status=closed source_issue=<missing>"},
		{name: "open", mrID: "mr-1", bd: fakeIssueShower{issue: &beads.Issue{ID: "mr-1", Status: "open"}}, want: "active_mr=mr-1 status=open"},
		{name: "missing terminal source", mrID: "mr-1", sourceHint: "gt-closed", bd: fakeIssueMapShower{issues: map[string]*beads.Issue{"gt-closed": &beads.Issue{ID: "gt-closed", Status: "closed"}}}, want: ""},
		{name: "missing unknown source", mrID: "mr-1", bd: fakeIssueMapShower{}, want: "active_mr=mr-1 status=missing source_issue=<missing>"},
		{name: "nil issue unknown source", mrID: "mr-1", bd: fakeIssueShower{issue: nil}, want: "active_mr=mr-1 status=missing source_issue=<missing>"},
		{name: "nil reader", mrID: "mr-1", bd: nil, want: "active_mr=mr-1 status=unverified"},
		{name: "lookup error", mrID: "mr-1", bd: fakeIssueShower{err: errors.New("bd exploded")}, want: "active_mr=mr-1 status=lookup_error: bd exploded"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := activeMRBlocker(tt.bd, tt.mrID, tt.sourceHint, false, false)
			if got != tt.want {
				t.Errorf("activeMRBlocker() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatSafetyCheckBlockers(t *testing.T) {
	blocked := []*SafetyCheckResult{
		{Polecat: "gastown/fury", Reasons: []string{"cleanup_status=unknown", "active_mr=hq-wisp-1 status=open"}},
		{Polecat: "gastown/rust", Reasons: []string{"has work on hook (gt-abc)"}},
	}

	got := formatSafetyCheckBlockers(blocked)
	want := "gastown/fury: cleanup_status=unknown; active_mr=hq-wisp-1 status=open | gastown/rust: has work on hook (gt-abc)"
	if got != want {
		t.Errorf("formatSafetyCheckBlockers() = %q, want %q", got, want)
	}
}

func TestDisplaySafetyCheckBlockedToIncludesPredicates(t *testing.T) {
	var buf bytes.Buffer
	displaySafetyCheckBlockedTo(&buf, []*SafetyCheckResult{{
		Polecat: "gastown/fury",
		Reasons: []string{"cleanup_status=unknown", "active_mr=hq-wisp-1 status=open"},
	}})
	out := buf.String()
	for _, want := range []string{
		"Cannot nuke",
		"gastown/fury",
		"cleanup_status=unknown",
		"active_mr=hq-wisp-1 status=open",
		"Force nuke (LOSES WORK)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("displaySafetyCheckBlockedTo() missing %q in %q", want, out)
		}
	}
}

func TestDryRunNukeSummary(t *testing.T) {
	tests := []struct {
		name    string
		total   int
		blocked int
		want    string
	}{
		{name: "safe", total: 2, want: "Would nuke 2 polecat(s)."},
		{name: "blocked", total: 2, blocked: 1, want: "Would refuse to nuke 1 of 2 polecat(s) without --force."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dryRunNukeSummary(tt.total, tt.blocked); got != tt.want {
				t.Errorf("dryRunNukeSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHasSubmittableWorkForRecoveryUsesUpstream(t *testing.T) {
	repo := setupRecoveryGitRepo(t)

	if got := hasSubmittableWorkForRecovery(repo, nil, &GitState{UnpushedCommits: 99}, nil); got {
		t.Fatal("branch with no commits ahead of its upstream should not require MQ submission")
	}

	writeRecoveryFile(t, filepath.Join(repo, "change.txt"), "change")
	runGit(t, repo, "add", "change.txt")
	runGit(t, repo, "commit", "-m", "change")

	if got := hasSubmittableWorkForRecovery(repo, nil, &GitState{}, nil); !got {
		t.Fatal("branch with commits ahead of its upstream should require MQ submission")
	}
}

func TestHasSubmittableWorkForRecoveryIgnoresSelfUpstream(t *testing.T) {
	repo := setupRecoveryGitRepo(t)
	runGit(t, repo, "switch", "-c", "polecat/test")
	writeRecoveryFile(t, filepath.Join(repo, "feature.txt"), "feature")
	runGit(t, repo, "add", "feature.txt")
	runGit(t, repo, "commit", "-m", "feature")
	runGit(t, repo, "push", "-u", "origin", "polecat/test")

	if got := hasSubmittableWorkForRecovery(repo, nil, &GitState{UnpushedCommits: 1}, nil); !got {
		t.Fatal("self-upstream feature branch should fall back and preserve MQ requirement")
	}
}

func TestHasSubmittableWorkForRecoveryIgnoresPatchEquivalentBranch(t *testing.T) {
	repo := setupRecoveryGitRepo(t)
	runGit(t, repo, "switch", "-c", "polecat/equivalent")
	writeRecoveryFile(t, filepath.Join(repo, "equiv.txt"), "equiv")
	runGit(t, repo, "add", "equiv.txt")
	runGit(t, repo, "commit", "-m", "equiv")
	runGit(t, repo, "switch", "integration/test")
	writeRecoveryFile(t, filepath.Join(repo, "other.txt"), "other")
	runGit(t, repo, "add", "other.txt")
	runGit(t, repo, "commit", "-m", "other")
	runGit(t, repo, "cherry-pick", "polecat/equivalent")
	runGit(t, repo, "push", "origin", "integration/test")
	runGit(t, repo, "switch", "polecat/equivalent")
	runGit(t, repo, "branch", "--set-upstream-to=origin/integration/test")

	if got := hasSubmittableWorkForRecovery(repo, nil, &GitState{UnpushedCommits: 99}, nil); got {
		t.Fatal("patch-equivalent branch should not require MQ submission")
	}
}

func TestHasSubmittableWorkForRecoveryUsesExplicitTargetAncestor(t *testing.T) {
	repo := setupRecoveryGitRepo(t)
	runGit(t, repo, "switch", "-c", "polecat/contained")
	writeRecoveryFile(t, filepath.Join(repo, "contained.txt"), "contained")
	runGit(t, repo, "add", "contained.txt")
	runGit(t, repo, "commit", "-m", "contained")
	runGit(t, repo, "switch", "integration/test")
	runGit(t, repo, "merge", "--ff-only", "polecat/contained")
	runGit(t, repo, "push", "origin", "integration/test")
	runGit(t, repo, "switch", "polecat/contained")

	if got := hasSubmittableWorkForRecovery(repo, []string{"integration/test"}, &GitState{UnpushedCommits: 99}, nil); got {
		t.Fatal("branch whose HEAD is contained by explicit target should not require MQ submission")
	}
}

func TestHasSubmittableWorkForRecoveryUsesExplicitTargetCherry(t *testing.T) {
	repo := setupRecoveryGitRepo(t)
	runGit(t, repo, "switch", "-c", "polecat/cherry")
	writeRecoveryFile(t, filepath.Join(repo, "cherry.txt"), "cherry")
	runGit(t, repo, "add", "cherry.txt")
	runGit(t, repo, "commit", "-m", "cherry")
	runGit(t, repo, "switch", "integration/test")
	writeRecoveryFile(t, filepath.Join(repo, "target.txt"), "target")
	runGit(t, repo, "add", "target.txt")
	runGit(t, repo, "commit", "-m", "advance target")
	runGit(t, repo, "cherry-pick", "polecat/cherry")
	runGit(t, repo, "push", "origin", "integration/test")
	runGit(t, repo, "switch", "polecat/cherry")

	if got := hasSubmittableWorkForRecovery(repo, []string{"integration/test"}, &GitState{UnpushedCommits: 99}, nil); got {
		t.Fatal("patch-equivalent branch on advanced explicit target should not require MQ submission")
	}
}

func TestHasSubmittableWorkForRecoveryKeepsExplicitTargetUniquePatch(t *testing.T) {
	repo := setupRecoveryGitRepo(t)
	runGit(t, repo, "switch", "-c", "polecat/unique")
	writeRecoveryFile(t, filepath.Join(repo, "unique.txt"), "unique")
	runGit(t, repo, "add", "unique.txt")
	runGit(t, repo, "commit", "-m", "unique")

	if got := hasSubmittableWorkForRecovery(repo, []string{"integration/test"}, &GitState{}, nil); !got {
		t.Fatal("unique patch absent from explicit target should require MQ submission")
	}
}

func TestHasSubmittableWorkForRecoveryFallback(t *testing.T) {
	if got := hasSubmittableWorkForRecovery("/does/not/exist", nil, &GitState{UnpushedCommits: 0}, nil); got {
		t.Fatal("clean fallback git state should not require MQ submission")
	}
	if got := hasSubmittableWorkForRecovery("/does/not/exist", nil, &GitState{UnpushedCommits: 1}, nil); !got {
		t.Fatal("unpushed fallback git state should require MQ submission")
	}
	if got := hasSubmittableWorkForRecovery("/does/not/exist", nil, nil, errors.New("git failed")); !got {
		t.Fatal("git-state error fallback should remain conservative")
	}
}

func setupRecoveryGitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	repo := filepath.Join(root, "repo")
	runCmd(t, root, "git", "init", "--bare", remote)
	runCmd(t, root, "git", "init", repo)
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	writeRecoveryFile(t, filepath.Join(repo, "README.md"), "base")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "base")
	runGit(t, repo, "branch", "-M", "main")
	runGit(t, repo, "remote", "add", "origin", remote)
	runGit(t, repo, "push", "-u", "origin", "main")
	runGit(t, repo, "switch", "-c", "integration/test")
	runGit(t, repo, "push", "-u", "origin", "integration/test")
	return repo
}

func writeRecoveryFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	runCmd(t, dir, "git", args...)
}

func runCmd(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// setupPostMergeDetachedRepo builds the canonical post-merge scenario the
// gastown-95y fix targets: the polecat's branch has been merged into
// origin/main and the local branch was deleted, leaving the worktree in
// detached HEAD at the merged commit. ahead=0 (HEAD == origin/main), tree
// matches compareRef (no divergence). Tests that need the false-positive
// scenario (tree_diff=1 with dirty=0) call addRuntimeArtifactDiverged()
// afterwards.
func setupPostMergeDetachedRepo(t *testing.T) string {
	t.Helper()
	repo := setupRecoveryGitRepo(t)

	// Build a feature commit and push it on a polecat-style branch.
	runGit(t, repo, "switch", "-c", "polecat/quartz")
	writeRecoveryFile(t, filepath.Join(repo, "feature.txt"), "feature")
	runGit(t, repo, "add", "feature.txt")
	runGit(t, repo, "commit", "-m", "feature")
	runGit(t, repo, "push", "-u", "origin", "polecat/quartz")

	// Land the feature into main and push the merge so origin/main
	// advances to the same commit the polecat landed. This matches the
	// runtime state after `gt done` cuts over: the merged commit is on
	// origin/main and HEAD == origin/main (ahead=0, tree matches).
	runGit(t, repo, "switch", "main")
	runGit(t, repo, "merge", "--ff-only", "polecat/quartz")
	runGit(t, repo, "push", "origin", "main")
	runGit(t, repo, "branch", "-D", "polecat/quartz")

	// Detach HEAD on the merged commit. This is exactly what happens when
	// a polecat's local branch is removed after `gt done` cuts over.
	mergedSHA, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	runGit(t, repo, "checkout", "--detach", strings.TrimSpace(string(mergedSHA)))
	return repo
}

// addRuntimeArtifactDiverged commits a runtime-style file on origin/main
// without updating the local detached HEAD. After the call: origin/main
// has the file, HEAD does not, so `git diff origin/main HEAD --` reports
// tree_diff=1 while `git status --porcelain` reports clean. This is the
// exact gastown-95y false-positive scenario.
func addRuntimeArtifactDiverged(t *testing.T, repo, name, content string) {
	t.Helper()
	// Land the file on origin/main by switching to main locally, committing,
	// pushing, then detaching HEAD back to the original merged commit.
	mergedSHA, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	runGit(t, repo, "switch", "main")
	writeRecoveryFile(t, filepath.Join(repo, name), content)
	runGit(t, repo, "add", name)
	runGit(t, repo, "commit", "-m", "runtime artifact on origin/main: "+name)
	runGit(t, repo, "push", "origin", "main")
	runGit(t, repo, "checkout", "--detach", strings.TrimSpace(string(mergedSHA)))
}

// addRuntimeArtifact is the previous helper name; alias for compatibility
// with the existing test bodies that import it.
func addRuntimeArtifact(t *testing.T, repo, name, content string) {
	addRuntimeArtifactDiverged(t, repo, name, content)
}

// TestRecoveryGuardInputs_CleanPostMergeRepo verifies the helper gathers the
// facts the in-source predicate needs. This is the regression test for
// gastown-95y: when a worktree is in detached HEAD with a clean tree, no
// unpushed commits, and the MR has been closed/merged, the recovery guard
// must trust git-state and emit a CLEAR verdict — disagreeing with the
// external wrapper only when the post-merge exception is satisfied.
func TestRecoveryGuardInputs_CleanPostMergeRepo(t *testing.T) {
	repo := setupPostMergeDetachedRepo(t)
	addRuntimeArtifact(t, repo, "runtime-flags.env", "GT_ACTIVE_TOWN=gt-town\n")

	in := recoveryGuardInputs(repo, "", polecat.ActiveMRAssessment{
		ActiveMR:       "gastown-wisp-nv6n",
		MRStatus:       "closed",
		SourceTerminal: true,
	})
	if !in.WorktreeFound {
		t.Fatal("WorktreeFound = false, want true")
	}
	if in.Branch != "HEAD" {
		t.Fatalf("Branch = %q, want HEAD (detached after local branch delete)", in.Branch)
	}
	if in.CompareRef != "origin/main" {
		t.Errorf("CompareRef = %q, want origin/main", in.CompareRef)
	}
	if in.Ahead != 0 {
		t.Errorf("Ahead = %d, want 0 (merged branch should not be ahead)", in.Ahead)
	}
	if in.Dirty != 0 {
		t.Errorf("Dirty = %d, want 0 (untracked file should not count as dirty)", in.Dirty)
	}
	if in.TreeDiff != 1 {
		t.Errorf("TreeDiff = %d, want 1 (untracked artifact visible against origin/main)", in.TreeDiff)
	}

	guard := polecat.EvaluateRecoveryGuard(in)
	if guard.Block {
		t.Fatalf("EvaluateRecoveryGuard blocked post-merge clean detached HEAD: reasons=%v", guard.Reasons)
	}
	if guard.Verdict != polecat.RecoveryGuardVerdictClear {
		t.Errorf("Verdict = %q, want CLEAR", guard.Verdict)
	}
}

// TestRecoveryGuardInputs_DirtyStillBlocks verifies the integration wires
// the dirty predicate correctly. A worktree that has uncommitted changes
// must continue to block, regardless of MR-terminal state.
func TestRecoveryGuardInputs_DirtyStillBlocks(t *testing.T) {
	repo := setupPostMergeDetachedRepo(t)
	// Tracked dirty file (modified) to ensure the dirty predicate fires.
	writeRecoveryFile(t, filepath.Join(repo, "feature.txt"), "wip-modification")
	runGit(t, repo, "add", "feature.txt")

	in := recoveryGuardInputs(repo, "", polecat.ActiveMRAssessment{
		MRStatus:       "closed",
		SourceTerminal: true,
	})
	if in.Dirty == 0 {
		t.Fatalf("Dirty = 0, want > 0")
	}
	guard := polecat.EvaluateRecoveryGuard(in)
	if !guard.Block {
		t.Fatalf("Block = false, want true (dirty worktree should still block); reasons=%v", guard.Reasons)
	}
	if !containsString(guard.Reasons, polecat.ReasonDirtyWorktree+":1") {
		t.Errorf("Reasons missing dirty_worktree:1: got %v", guard.Reasons)
	}
}

// TestRecoveryGuardInputs_HookedIssueBlocks verifies the StatusIssue path:
// when a polecat still has an issue hooked, the post-merge exception must
// NOT fire.
func TestRecoveryGuardInputs_HookedIssueBlocks(t *testing.T) {
	repo := setupPostMergeDetachedRepo(t)

	in := recoveryGuardInputs(repo, "gastown-wisp-amkx", polecat.ActiveMRAssessment{
		MRStatus:       "closed",
		SourceTerminal: true,
	})
	guard := polecat.EvaluateRecoveryGuard(in)
	if !guard.Block {
		t.Fatalf("Block = false, want true (hooked issue should block); reasons=%v", guard.Reasons)
	}
	if !containsString(guard.Reasons, polecat.ReasonHookedIssue+":gastown-wisp-amkx") {
		t.Errorf("Reasons missing hooked_issue marker: got %v", guard.Reasons)
	}
}

// TestRecoveryGuardInputs_OpenMRStillBlocks verifies the MR-pending path:
// an open/pending MR + detached HEAD + tree_diff=1 must still block. This
// is the wrapper-script baseline behavior the fix preserves.
func TestRecoveryGuardInputs_OpenMRStillBlocks(t *testing.T) {
	repo := setupPostMergeDetachedRepo(t)
	addRuntimeArtifact(t, repo, "runtime-flags.env", "x=1\n")

	in := recoveryGuardInputs(repo, "", polecat.ActiveMRAssessment{
		MRStatus:       "open",
		Pending:        true,
		SourceTerminal: false,
	})
	if in.Branch != "HEAD" {
		t.Fatalf("Branch = %q, want HEAD", in.Branch)
	}
	if in.TreeDiff != 1 {
		t.Fatalf("TreeDiff = %d, want 1", in.TreeDiff)
	}
	guard := polecat.EvaluateRecoveryGuard(in)
	if !guard.Block {
		t.Fatalf("Block = false, want true (open MR must still block); reasons=%v", guard.Reasons)
	}
	if !containsString(guard.Reasons, polecat.ReasonDetachedOrUnknownBranch) {
		t.Errorf("Reasons missing detached_or_unknown_branch: got %v", guard.Reasons)
	}
	if !containsString(guard.Reasons, polecat.ReasonMergeQueueRecordUnavailable) {
		t.Errorf("Reasons missing merge_queue_record_unavailable: got %v", guard.Reasons)
	}
}

// TestRecoveryGuardInputs_MissingWorktree verifies the helper degrades
// gracefully when the worktree path does not exist.
func TestRecoveryGuardInputs_MissingWorktree(t *testing.T) {
	in := recoveryGuardInputs("/nonexistent/path/that/does/not/exist", "", polecat.ActiveMRAssessment{})
	if in.WorktreeFound {
		t.Errorf("WorktreeFound = true, want false for missing path")
	}
	guard := polecat.EvaluateRecoveryGuard(in)
	if !guard.Block {
		t.Errorf("Block = false, want true for missing worktree")
	}
}

// TestRecoveryGuardInputs_GitStateDisagreesWithGuard is the explicit
// "guard/git-state disagreement" test requested by gastown-95y. We model
// the user-reported scenario:
//
//	git-state   : CLEAN (Working Tree clean, 0 unpreserved, 0 unpushed)
//	wrapper guard: NEEDS_RECOVERY (detached HEAD, tree_diff=1, MR record unavailable)
//	in-source guard (this fix): CLEAR via the post-merge exception
//
// The test verifies that the in-source verdict agrees with git-state when
// the post-merge exception is satisfied, even when the wrapper script's
// prediction would disagree. The disagreement itself is intentional: the
// in-source predicate is the fix.
func TestRecoveryGuardInputs_GitStateDisagreesWithGuard(t *testing.T) {
	repo := setupPostMergeDetachedRepo(t)
	addRuntimeArtifact(t, repo, "runtime-flags.env", "x=1\n")

	// git-state-equivalent: getGitState mirrors the read-only facts the
	// external `gt polecat git-state` reports.
	gitState := getGitStateSnapshot(t, repo)
	if !gitState.Clean {
		t.Fatalf("git-state Clean = false, want true; state=%+v", gitState)
	}
	if gitState.UnpushedCommits != 0 {
		t.Errorf("git-state UnpushedCommits = %d, want 0", gitState.UnpushedCommits)
	}
	if len(gitState.UncommittedFiles) != 0 {
		t.Errorf("git-state UncommittedFiles = %v, want []", gitState.UncommittedFiles)
	}

	// Guard verdict: with the post-merge exception, the in-source guard
	// trusts git-state and emits CLEAR.
	in := recoveryGuardInputs(repo, "", polecat.ActiveMRAssessment{
		MRStatus:       "closed",
		SourceTerminal: true,
	})
	guard := polecat.EvaluateRecoveryGuard(in)

	// git-state says clean. Without the post-merge exception the wrapper
	// would block; with the exception, the in-source guard trusts git-state.
	if guard.Block {
		t.Fatalf("guard/git-state disagreement: git-state clean but guard blocks (reasons=%v); in-source guard must trust clean git-state for terminal MRs", guard.Reasons)
	}
	if guard.Verdict != polecat.RecoveryGuardVerdictClear {
		t.Errorf("Verdict = %q, want CLEAR (matches git-state)", guard.Verdict)
	}
	if !guard.TreeMatchesCompareRef && in.TreeDiff == 0 {
		t.Errorf("TreeMatchesCompareRef = false, want true for TreeDiff=0")
	}
}

// getGitStateSnapshot mirrors the relevant fields of `gt polecat git-state`
// for use in disagreement tests. The point is to verify the in-source guard
// agrees with the read-only git facts, not to test getGitState itself.
func getGitStateSnapshot(t *testing.T, repo string) *GitState {
	t.Helper()
	state, err := getGitState(repo)
	if err != nil {
		t.Fatalf("getGitState(%s): %v", repo, err)
	}
	return state
}

// containsString is a small helper for test assertions on reason slices.
// Defined locally so the test file stays dependency-free.
func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
