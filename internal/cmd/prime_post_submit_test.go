package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
)

// fakePostSubmitIssueShower implements issueShower for tests.
type fakePostSubmitIssueShower struct {
	issue *beads.Issue
	err   error
}

func (f *fakePostSubmitIssueShower) Show(id string) (*beads.Issue, error) {
	return f.issue, f.err
}

// fakePostSubmitGit implements gitWorktreeChecker for tests.
type fakePostSubmitGit struct {
	workStatus *git.UncommittedWorkStatus
	workErr    error
	branch     string
	branchErr  error
}

func (f *fakePostSubmitGit) CheckUncommittedWork() (*git.UncommittedWorkStatus, error) {
	return f.workStatus, f.workErr
}

func (f *fakePostSubmitGit) CurrentBranch() (string, error) {
	return f.branch, f.branchErr
}

func TestDetectPostSubmitEmptyHook(t *testing.T) {
	t.Parallel()

	polecatCtx := RoleContext{
		Role:     RolePolecat,
		Rig:      "gastown",
		Polecat:  "jasper",
		TownRoot: "/tmp/town",
		WorkDir:  "/tmp/town/gastown/polecats/jasper",
	}

	agentWithActiveMR := &beads.Issue{
		ID: "gt-gastown-polecat-jasper",
		Description: `Polecat worker jasper in gastown - autonomous worker with persistent identity.

role_type: polecat
rig: gastown
agent_state: idle
hook_bead: null
cleanup_status: clean
active_mr: gt-mr-epj
exit_type: COMPLETED
mr_id: gt-mr-epj
branch: polecat/jasper/gastown-abc@xyz
last_source_issue: gastown-abc
completion_time: 2026-06-27T20:00:00Z
`,
	}

	agentWithoutActiveMR := &beads.Issue{
		ID: "gt-gastown-polecat-jasper",
		Description: `role_type: polecat
rig: gastown
agent_state: idle
hook_bead: null
cleanup_status: clean
active_mr: null
`,
	}

	openMR := &beads.Issue{ID: "gt-mr-epj", Status: string(beads.StatusOpen)}
	closedMR := &beads.Issue{ID: "gt-mr-epj", Status: string(beads.StatusClosed)}
	cleanGit := &fakePostSubmitGit{
		workStatus: &git.UncommittedWorkStatus{HasUncommittedChanges: false},
		branch:     "",
	}

	cases := []struct {
		name      string
		ctx       RoleContext
		agentBD   issueShower
		mrBD      issueShower
		g         gitWorktreeChecker
		wantMatch bool
		wantState func(*postSubmitEmptyHookState) bool
	}{
		{
			name:      "polecat with open active_mr and clean worktree",
			ctx:       polecatCtx,
			agentBD:   &fakePostSubmitIssueShower{issue: agentWithActiveMR},
			mrBD:      &fakePostSubmitIssueShower{issue: openMR},
			g:         cleanGit,
			wantMatch: true,
			wantState: func(s *postSubmitEmptyHookState) bool {
				return s.ActiveMR == "gt-mr-epj" && s.MRStatus == "open" &&
					s.SourceIssue == "gastown-abc" && s.Branch == "polecat/jasper/gastown-abc@xyz" &&
					s.WorktreeClean && s.OnMain
			},
		},
		{
			name:      "non-polecat ignored",
			ctx:       RoleContext{Role: RoleWitness, Rig: "gastown", Polecat: "", TownRoot: "/tmp/town"},
			agentBD:   &fakePostSubmitIssueShower{issue: agentWithActiveMR},
			mrBD:      &fakePostSubmitIssueShower{issue: openMR},
			g:         cleanGit,
			wantMatch: false,
		},
		{
			name:      "missing agent bead",
			ctx:       polecatCtx,
			agentBD:   &fakePostSubmitIssueShower{err: beads.ErrNotFound},
			mrBD:      &fakePostSubmitIssueShower{issue: openMR},
			g:         cleanGit,
			wantMatch: false,
		},
		{
			name:      "no active_mr",
			ctx:       polecatCtx,
			agentBD:   &fakePostSubmitIssueShower{issue: agentWithoutActiveMR},
			mrBD:      &fakePostSubmitIssueShower{issue: openMR},
			g:         cleanGit,
			wantMatch: false,
		},
		{
			name:      "terminal active_mr (merged/rejected)",
			ctx:       polecatCtx,
			agentBD:   &fakePostSubmitIssueShower{issue: agentWithActiveMR},
			mrBD:      &fakePostSubmitIssueShower{issue: closedMR},
			g:         cleanGit,
			wantMatch: false,
		},
		{
			name:      "mr unreadable treated as in-flight",
			ctx:       polecatCtx,
			agentBD:   &fakePostSubmitIssueShower{issue: agentWithActiveMR},
			mrBD:      &fakePostSubmitIssueShower{err: beads.ErrNotFound},
			g:         cleanGit,
			wantMatch: true,
			wantState: func(s *postSubmitEmptyHookState) bool {
				return s.ActiveMR == "gt-mr-epj" && s.MRStatus == "unknown" && s.WorktreeClean && s.OnMain
			},
		},
		{
			name:    "dirty worktree still detects in-flight",
			ctx:     polecatCtx,
			agentBD: &fakePostSubmitIssueShower{issue: agentWithActiveMR},
			mrBD:    &fakePostSubmitIssueShower{issue: openMR},
			g: &fakePostSubmitGit{
				workStatus: &git.UncommittedWorkStatus{
					HasUncommittedChanges: true,
					ModifiedFiles:         []string{"file.go"},
				},
				branch: "main",
			},
			wantMatch: true,
			wantState: func(s *postSubmitEmptyHookState) bool {
				return s.ActiveMR == "gt-mr-epj" && !s.WorktreeClean && s.OnMain
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, ok := detectPostSubmitEmptyHookWithReaders(tc.ctx, tc.agentBD, tc.mrBD, tc.g)
			if ok != tc.wantMatch {
				t.Fatalf("detectPostSubmitEmptyHookWithReaders() ok = %v, want %v", ok, tc.wantMatch)
			}
			if !tc.wantMatch {
				return
			}
			if state == nil {
				t.Fatal("expected non-nil state")
			}
			if tc.wantState != nil && !tc.wantState(state) {
				t.Fatalf("state = %+v, did not match wantState", state)
			}
		})
	}
}

func TestOutputPostSubmitStandDownDirective(t *testing.T) {
	t.Parallel()

	output := captureStdout(t, func() {
		outputPostSubmitStandDownDirective(RoleContext{
			Role:    RolePolecat,
			Rig:     "gastown",
			Polecat: "jasper",
		}, &postSubmitEmptyHookState{
			ActiveMR:      "gt-mr-epj",
			MRStatus:      "open",
			SourceIssue:   "gastown-abc",
			Branch:        "polecat/jasper/gastown-abc@xyz",
			WorktreeClean: true,
			OnMain:        true,
		})
	})

	mustContain := []string{
		"STAND DOWN",
		"gt-mr-epj",
		"gastown-abc",
		"polecat/jasper/gastown-abc@xyz",
		"Do NOT run",
		"gt done",
	}
	for _, want := range mustContain {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q\noutput:\n%s", want, output)
		}
	}

	// It must NOT instruct the polecat to run gt done.
	if strings.Contains(output, "run `gt done` IMMEDIATELY") ||
		strings.Contains(output, "run `gt done` and exit") {
		t.Errorf("output should not instruct running gt done:\n%s", output)
	}
}
