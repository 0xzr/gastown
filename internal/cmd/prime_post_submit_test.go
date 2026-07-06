package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
)

// fakePostSubmitIssueShower implements polecat.IssueReader for tests.
type fakePostSubmitIssueShower struct {
	issue *beads.Issue
	err   error
}

func (f *fakePostSubmitIssueShower) Show(id string) (*beads.Issue, error) {
	return f.issue, f.err
}

// fakePostSubmitGit implements gitWorktreeChecker for tests. It exposes only
// the LOCAL git operations the interface requires: Status() and CurrentBranch().
// It deliberately has no UnpushedCommits/RemoteBranchTip surface so tests
// cannot accidentally exercise a remote git path on the stand-down directive
// (gastown-t7l).
type fakePostSubmitGit struct {
	status    *git.GitStatus
	statusErr error
	branch    string
	branchErr error
}

func (f *fakePostSubmitGit) Status() (*git.GitStatus, error) {
	return f.status, f.statusErr
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
		status: &git.GitStatus{Clean: true},
		branch: "",
	}

	cases := []struct {
		name      string
		ctx       RoleContext
		agentBD   polecat.IssueReader
		mrBD      polecat.IssueReader
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
				status: &git.GitStatus{
					Clean:    false,
					Modified: []string{"file.go"},
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

// recordingPostSubmitGit is a fakePostSubmitGit that records which methods the
// stand-down path invokes. It is the regression sentinel for gastown-t7l: the
// prior implementation reached CheckUncommittedWork -> UnpushedCommits ->
// RemoteBranchTip -> `git ls-remote` (an unbounded network call) merely to
// render a directive. The narrowed gitWorktreeChecker interface no longer
// exposes any remote-reaching method, so this recording asserts the directive
// is computed from local git state alone.
type recordingPostSubmitGit struct {
	fakePostSubmitGit
	calls []string
}

func (r *recordingPostSubmitGit) Status() (*git.GitStatus, error) {
	r.calls = append(r.calls, "Status")
	return r.fakePostSubmitGit.status, r.fakePostSubmitGit.statusErr
}

func (r *recordingPostSubmitGit) CurrentBranch() (string, error) {
	r.calls = append(r.calls, "CurrentBranch")
	return r.fakePostSubmitGit.branch, r.fakePostSubmitGit.branchErr
}

// TestPostSubmitGitStateAvoidsRemoteGit is the gastown-t7l regression. The
// stand-down directive path must never contact a remote: a post-submit gt prime
// runs after the work is already in the merge queue, and an unreachable remote
// would otherwise hang gt prime indefinitely while rendering corroboration.
// This would fail against the prior implementation, which routed through
// CheckUncommittedWork (and onward to an unbounded `git ls-remote`).
func TestPostSubmitGitStateAvoidsRemoteGit(t *testing.T) {
	t.Parallel()

	rec := &recordingPostSubmitGit{
		fakePostSubmitGit: fakePostSubmitGit{
			status: &git.GitStatus{Clean: true},
			branch: "main",
		},
	}

	clean, onMain := postSubmitGitStateWithChecker(RoleContext{
		Role:     RolePolecat,
		Rig:      "gastown",
		Polecat:  "jasper",
		TownRoot: "/tmp/town",
	}, rec)

	if !clean || !onMain {
		t.Fatalf("expected clean=true onMain=true, got clean=%v onMain=%v", clean, onMain)
	}

	// Only local-only methods may be invoked. Any remote git path would have to
	// be reachable through gitWorktreeChecker, which it no longer is — but assert
	// the call set explicitly so a future regression is caught here, not in prod.
	for _, c := range rec.calls {
		if c != "Status" && c != "CurrentBranch" {
			t.Errorf("post-submit git state invoked %q; only Status/CurrentBranch (local) are permitted", c)
		}
	}
	if len(rec.calls) == 0 {
		t.Fatal("expected at least one local git call, got none")
	}
}

// TestPostSubmitGitStateFailsClosedOnStatusError ensures a local git failure
// (e.g. a detached-HEAD worktree with no HEAD) does not panic or fabricate a
// "clean" corroboration. It must fail closed: not clean.
func TestPostSubmitGitStateFailsClosedOnStatusError(t *testing.T) {
	t.Parallel()

	rec := &recordingPostSubmitGit{
		fakePostSubmitGit: fakePostSubmitGit{
			statusErr: assertErr("local status unavailable"),
			branch:    "",
		},
	}

	clean, onMain := postSubmitGitStateWithChecker(RoleContext{
		Role:     RolePolecat,
		Rig:      "gastown",
		Polecat:  "jasper",
		TownRoot: "/tmp/town",
	}, rec)

	if clean {
		t.Errorf("expected clean=false when local status errors (fail closed), got clean=true")
	}
	// A detached HEAD (empty branch) is still the expected post-done state.
	if !onMain {
		t.Errorf("expected onMain=true for empty branch (detached HEAD), got onMain=%v", onMain)
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
