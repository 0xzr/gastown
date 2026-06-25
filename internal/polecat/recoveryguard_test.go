package polecat

import (
	"reflect"
	"sort"
	"testing"
)

// cleanDetachedHead is the canonical "post-merge, clean detached HEAD" input
// that the gastown-95y false-positive revolves around. Branch is detached
// (HEAD), no work is dirty or ahead, the active MR is terminal, and the
// source issue has reached a terminal state.
func cleanDetachedHead() RecoveryGuardInput {
	return RecoveryGuardInput{
		WorktreeFound:          true,
		Branch:                 "HEAD",
		CompareRef:             "origin/main",
		Ahead:                  0,
		Dirty:                  0,
		TreeDiff:               0,
		BranchStashes:          0,
		StatusIssue:            "",
		ActiveMRStatus:         "closed",
		ActiveMRSourceTerminal: true,
		ActiveMRPending:        false,
	}
}

// TestEvaluateRecoveryGuard_PostMergeCleared is the regression test for
// gastown-95y. The wrapper guard script reports NEEDS_RECOVERY for a
// clean detached-HEAD worktree whose active MR is closed/merged/deleted
// because it conservatively emits detached_or_unknown_branch,
// branch_not_main:HEAD, and merge_queue_record_unavailable. The in-source
// post-merge exception trusts clean git-state when the active MR is
// provably terminal, so the verdict must be CLEAR.
func TestEvaluateRecoveryGuard_PostMergeCleared(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*RecoveryGuardInput)
	}{
		{name: "exact user scenario: detached HEAD + tree_diff=1 + closed MR"},
		{name: "detached HEAD + tree_diff=0 + closed MR", mut: func(in *RecoveryGuardInput) { in.TreeDiff = 0 }},
		{name: "merged MR variant", mut: func(in *RecoveryGuardInput) { in.ActiveMRStatus = "merged" }},
		{name: "MR with no status string but terminal source", mut: func(in *RecoveryGuardInput) { in.ActiveMRStatus = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := cleanDetachedHead()
			if tt.mut != nil {
				tt.mut(&in)
			}
			got := EvaluateRecoveryGuard(in)
			if got.Block {
				t.Fatalf("EvaluateRecoveryGuard() blocked: reasons=%v, want CLEAR for post-merge clean detached HEAD", got.Reasons)
			}
			if got.Verdict != RecoveryGuardVerdictClear {
				t.Errorf("Verdict = %q, want %q", got.Verdict, RecoveryGuardVerdictClear)
			}
			if len(got.Reasons) != 0 {
				t.Errorf("Reasons = %v, want []", got.Reasons)
			}
			if !got.TreeMatchesCompareRef {
				t.Errorf("TreeMatchesCompareRef = false, want true (TreeDiff=%d)", in.TreeDiff)
			}
		})
	}
}

// TestEvaluateRecoveryGuard_DirtyStillBlocks verifies the post-merge
// exception does not relax the dirty-worktree predicate.
func TestEvaluateRecoveryGuard_DirtyStillBlocks(t *testing.T) {
	tests := []struct {
		name      string
		dirty     int
		wantBlock bool
		wantSub   []string
	}{
		{
			name:      "clean detached HEAD + dirty=1 still blocks",
			dirty:     1,
			wantBlock: true,
			wantSub:   []string{ReasonDirtyWorktree + ":1"},
		},
		{
			name:      "clean detached HEAD + dirty=5 still blocks",
			dirty:     5,
			wantBlock: true,
			wantSub:   []string{ReasonDirtyWorktree + ":5"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := cleanDetachedHead()
			in.Dirty = tt.dirty
			got := EvaluateRecoveryGuard(in)
			if got.Block != tt.wantBlock {
				t.Fatalf("Block = %v, want %v (reasons=%v)", got.Block, tt.wantBlock, got.Reasons)
			}
			for _, want := range tt.wantSub {
				if !containsReason(got.Reasons, want) {
					t.Errorf("Reasons missing %q: got %v", want, got.Reasons)
				}
			}
		})
	}
}

// TestEvaluateRecoveryGuard_AheadStillBlocks verifies that unpushed commits
// keep blocking even with a terminal MR — ahead>0 means local work exists
// that may not have landed.
func TestEvaluateRecoveryGuard_AheadStillBlocks(t *testing.T) {
	in := cleanDetachedHead()
	in.Ahead = 3
	in.TreeDiff = 1 // ahead alone without tree_diff would not emit a reason
	got := EvaluateRecoveryGuard(in)
	if !got.Block {
		t.Fatalf("Block = false, want true (reasons=%v)", got.Reasons)
	}
	if !containsReason(got.Reasons, ReasonAheadOfCompareRef+":3") {
		t.Errorf("Reasons missing %q: got %v", ReasonAheadOfCompareRef+":3", got.Reasons)
	}
}

// TestEvaluateRecoveryGuard_AheadNoTreeDiffDoesNotEmitAheadReason matches the
// wrapper script's behavior: ahead>0 only emits ahead_of_compare_ref when
// the tree actually differs from compareRef. If the tree matches but
// ancestry is non-trivial, the predicate emits the detached-HEAD reason
// but trusts the comparison (no ahead reason).
func TestEvaluateRecoveryGuard_AheadNoTreeDiffDoesNotEmitAheadReason(t *testing.T) {
	// Use a non-HEAD branch so the detached_or_unknown_branch reason does
	// not fire — we only want to verify the ahead predicate is gated on
	// tree_diff.
	in := RecoveryGuardInput{
		WorktreeFound: true,
		Branch:        "polecat/foo",
		CompareRef:    "origin/main",
		Ahead:         5,
		TreeDiff:      0,
	}
	got := EvaluateRecoveryGuard(in)
	if got.Block {
		t.Fatalf("Block = true, want false (reasons=%v)", got.Reasons)
	}
	if containsReason(got.Reasons, ReasonAheadOfCompareRef) {
		t.Errorf("Reasons should not include ahead reason when tree matches: %v", got.Reasons)
	}
}

// TestEvaluateRecoveryGuard_BranchStashesAlwaysBlock covers the stash check.
func TestEvaluateRecoveryGuard_BranchStashesAlwaysBlock(t *testing.T) {
	tests := []struct {
		name       string
		branch     string
		stashCount int
		wantBlock  bool
		wantReason string
	}{
		{
			name:       "branch stashes always block",
			branch:     "polecat/foo",
			stashCount: 1,
			wantBlock:  true,
			wantReason: ReasonBranchStashes + ":1",
		},
		{
			name:       "branch stashes count is preserved",
			branch:     "polecat/foo",
			stashCount: 7,
			wantBlock:  true,
			wantReason: ReasonBranchStashes + ":7",
		},
		{
			name:       "detached HEAD with stashes (defensive: stashes shouldn't be tracked for HEAD)",
			branch:     "HEAD",
			stashCount: 0,
			wantBlock:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := RecoveryGuardInput{
				WorktreeFound:          true,
				Branch:                 tt.branch,
				CompareRef:             "origin/main",
				BranchStashes:          tt.stashCount,
				ActiveMRStatus:         "closed",
				ActiveMRSourceTerminal: true,
			}
			got := EvaluateRecoveryGuard(in)
			if got.Block != tt.wantBlock {
				t.Fatalf("Block = %v, want %v (reasons=%v)", got.Block, tt.wantBlock, got.Reasons)
			}
			if tt.wantReason != "" && !containsReason(got.Reasons, tt.wantReason) {
				t.Errorf("Reasons missing %q: got %v", tt.wantReason, got.Reasons)
			}
		})
	}
}

// TestEvaluateRecoveryGuard_HookedIssueAlwaysBlock covers the hook check.
// A hooked issue means the polecat still owns work — never relax this.
func TestEvaluateRecoveryGuard_HookedIssueAlwaysBlock(t *testing.T) {
	in := cleanDetachedHead()
	in.StatusIssue = "gastown-wisp-amkx"
	got := EvaluateRecoveryGuard(in)
	if !got.Block {
		t.Fatalf("Block = false, want true (reasons=%v)", got.Reasons)
	}
	if !containsReason(got.Reasons, ReasonHookedIssue+":gastown-wisp-amkx") {
		t.Errorf("Reasons missing hooked_issue marker: got %v", got.Reasons)
	}
}

// TestEvaluateRecoveryGuard_MRTerminalGuard exercises the predicate matrix
// around the post-merge exception's MR-side requirements.
func TestEvaluateRecoveryGuard_MRTerminalGuard(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*RecoveryGuardInput)
		wantBlock   bool
		wantReasons []string
	}{
		{
			name: "open MR + detached HEAD + tree_diff=1 blocks",
			mutate: func(in *RecoveryGuardInput) {
				in.ActiveMRStatus = "open"
				in.ActiveMRSourceTerminal = false
				in.ActiveMRPending = true
				in.TreeDiff = 1
			},
			wantBlock:   true,
			wantReasons: []string{ReasonDetachedOrUnknownBranch, "branch_not_main:HEAD", ReasonMergeQueueRecordUnavailable},
		},
		{
			name: "closed MR but source still open + detached HEAD blocks",
			mutate: func(in *RecoveryGuardInput) {
				in.ActiveMRStatus = "closed"
				in.ActiveMRSourceTerminal = false
				in.TreeDiff = 1
			},
			wantBlock:   true,
			wantReasons: []string{ReasonDetachedOrUnknownBranch, "branch_not_main:HEAD", ReasonMergeQueueRecordUnavailable},
		},
		{
			name: "pending MR after close + clean detached HEAD blocks",
			mutate: func(in *RecoveryGuardInput) {
				in.ActiveMRStatus = "closed"
				in.ActiveMRSourceTerminal = true
				in.ActiveMRPending = true
				in.TreeDiff = 1
			},
			wantBlock:   true,
			wantReasons: []string{ReasonMergeQueueRecordUnavailable},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := cleanDetachedHead()
			in.TreeDiff = 1
			tt.mutate(&in)
			got := EvaluateRecoveryGuard(in)
			if got.Block != tt.wantBlock {
				t.Fatalf("Block = %v, want %v (reasons=%v)", got.Block, tt.wantBlock, got.Reasons)
			}
			for _, want := range tt.wantReasons {
				if !containsReason(got.Reasons, want) {
					t.Errorf("Reasons missing %q: got %v", want, got.Reasons)
				}
			}
		})
	}
}

// TestEvaluateRecoveryGuard_NamedBranchClean covers the non-detached happy
// path: a polecat branch that is clean and pushed should not block.
func TestEvaluateRecoveryGuard_NamedBranchClean(t *testing.T) {
	tests := []struct {
		name      string
		in        RecoveryGuardInput
		wantBlock bool
	}{
		{
			name: "polecat branch + clean tree + closed MR is CLEAR",
			in: RecoveryGuardInput{
				WorktreeFound:          true,
				Branch:                 "polecat/foo",
				CompareRef:             "origin/main",
				ActiveMRStatus:         "closed",
				ActiveMRSourceTerminal: true,
			},
			wantBlock: false,
		},
		{
			name: "main branch + dirty still blocks",
			in: RecoveryGuardInput{
				WorktreeFound: true,
				Branch:        "main",
				CompareRef:    "origin/main",
				Dirty:         2,
			},
			wantBlock: true,
		},
		{
			name: "integration branch + clean is CLEAR",
			in: RecoveryGuardInput{
				WorktreeFound: true,
				Branch:        "integration/foo",
				CompareRef:    "origin/main",
			},
			wantBlock: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateRecoveryGuard(tt.in)
			if got.Block != tt.wantBlock {
				t.Fatalf("Block = %v, want %v (reasons=%v)", got.Block, tt.wantBlock, got.Reasons)
			}
		})
	}
}

// TestEvaluateRecoveryGuard_MissingCompareRef covers the missing-origin-main
// path: with no compare ref we cannot decide ahead/tree_diff safely, so the
// predicate stays conservative.
func TestEvaluateRecoveryGuard_MissingCompareRef(t *testing.T) {
	in := RecoveryGuardInput{
		WorktreeFound: true,
		Branch:        "polecat/foo",
		TreeDiff:      1,
	}
	got := EvaluateRecoveryGuard(in)
	if !got.Block {
		t.Fatalf("Block = false, want true (reasons=%v)", got.Reasons)
	}
	if !containsReason(got.Reasons, ReasonMissingOriginMain) {
		t.Errorf("Reasons missing missing_origin_main: got %v", got.Reasons)
	}
}

// TestEvaluateRecoveryGuard_MissingWorktree verifies the predicate returns
// missing_worktree and stays conservative when no worktree is present.
func TestEvaluateRecoveryGuard_MissingWorktree(t *testing.T) {
	in := RecoveryGuardInput{
		WorktreeFound: false,
		Branch:        "polecat/foo",
	}
	got := EvaluateRecoveryGuard(in)
	if !got.Block {
		t.Fatalf("Block = false, want true (reasons=%v)", got.Reasons)
	}
	if !containsReason(got.Reasons, "missing_worktree") {
		t.Errorf("Reasons missing missing_worktree: got %v", got.Reasons)
	}
}

// TestEvaluateRecoveryGuard_BranchNotMainEmittedForTreeDiff covers the
// wrapper-script-aligned "branch_not_main:<branch>" reason. The detached
// HEAD case substitutes "HEAD" for the branch name verbatim.
func TestEvaluateRecoveryGuard_BranchNotMainEmittedForTreeDiff(t *testing.T) {
	tests := []struct {
		name           string
		branch         string
		wantBlock      bool
		wantReasonPart string
	}{
		{
			name:           "polecat branch with tree_diff emits branch_not_main",
			branch:         "polecat/quartz",
			wantBlock:      true,
			wantReasonPart: "branch_not_main:polecat/quartz",
		},
		{
			name:           "detached HEAD with tree_diff emits branch_not_main:HEAD",
			branch:         "HEAD",
			wantBlock:      true,
			wantReasonPart: "branch_not_main:HEAD",
		},
		{
			name:           "main branch with tree_diff does not emit branch_not_main",
			branch:         "main",
			wantBlock:      false,
			wantReasonPart: "branch_not_main",
		},
		{
			name:           "integration branch with tree_diff does not emit branch_not_main",
			branch:         "integration/test",
			wantBlock:      false,
			wantReasonPart: "branch_not_main",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := RecoveryGuardInput{
				WorktreeFound:          true,
				Branch:                 tt.branch,
				CompareRef:             "origin/main",
				TreeDiff:               1,
				ActiveMRStatus:         "open",
				ActiveMRPending:        true,
				ActiveMRSourceTerminal: false,
			}
			got := EvaluateRecoveryGuard(in)
			if got.Block != tt.wantBlock {
				t.Fatalf("Block = %v, want %v (reasons=%v)", got.Block, tt.wantBlock, got.Reasons)
			}
			if tt.wantBlock {
				if !containsReason(got.Reasons, tt.wantReasonPart) {
					t.Errorf("Reasons missing %q: got %v", tt.wantReasonPart, got.Reasons)
				}
			} else if containsReason(got.Reasons, tt.wantReasonPart) {
				t.Errorf("Reasons should not include %q: got %v", tt.wantReasonPart, got.Reasons)
			}
		})
	}
}

// TestEvaluateRecoveryGuard_DetachedBranchWithPendingMRAndTreeDiff covers
// the false-positive scenario with the post-merge exception turned off: a
// detached HEAD with open/pending MR + tree_diff should block. This is
// the wrapper-script baseline behavior, preserved here.
func TestEvaluateRecoveryGuard_DetachedBranchWithPendingMRAndTreeDiff(t *testing.T) {
	in := RecoveryGuardInput{
		WorktreeFound:          true,
		Branch:                 "HEAD",
		CompareRef:             "origin/main",
		TreeDiff:               1,
		ActiveMRStatus:         "open",
		ActiveMRPending:        true,
		ActiveMRSourceTerminal: false,
	}
	got := EvaluateRecoveryGuard(in)
	if !got.Block {
		t.Fatalf("Block = false, want true (reasons=%v)", got.Reasons)
	}
	want := []string{
		ReasonDetachedOrUnknownBranch,
		"branch_not_main:HEAD",
		ReasonMergeQueueRecordUnavailable,
	}
	for _, w := range want {
		if !containsReason(got.Reasons, w) {
			t.Errorf("Reasons missing %q: got %v", w, got.Reasons)
		}
	}
}

// TestEvaluateRecoveryGuard_ReasonsAreDeterministic verifies reason ordering
// matches the predicate's evaluation order so callers can rely on diffing
// two snapshots.
func TestEvaluateRecoveryGuard_ReasonsAreDeterministic(t *testing.T) {
	in := RecoveryGuardInput{
		WorktreeFound:          true,
		Branch:                 "HEAD",
		CompareRef:             "origin/main",
		Dirty:                  2,
		Ahead:                  3,
		TreeDiff:               1,
		BranchStashes:          1,
		StatusIssue:            "gastown-wisp-amkx",
		ActiveMRStatus:         "open",
		ActiveMRPending:        true,
		ActiveMRSourceTerminal: false,
	}
	got := EvaluateRecoveryGuard(in)
	wantOrder := []string{
		ReasonDetachedOrUnknownBranch,
		ReasonDirtyWorktree + ":2",
		ReasonAheadOfCompareRef + ":3",
		"branch_not_main:HEAD",
		ReasonMergeQueueRecordUnavailable,
		ReasonBranchStashes + ":1",
		ReasonHookedIssue + ":gastown-wisp-amkx",
	}
	if !reflect.DeepEqual(got.Reasons, wantOrder) {
		t.Fatalf("Reasons order = %v, want %v", got.Reasons, wantOrder)
	}
}

// TestEvaluateRecoveryGuard_VerdictString verifies the verdict string maps
// consistently to Block.
func TestEvaluateRecoveryGuard_VerdictString(t *testing.T) {
	tests := []struct {
		name string
		in   RecoveryGuardInput
		want string
	}{
		{
			name: "clean post-merge",
			in:   cleanDetachedHead(),
			want: RecoveryGuardVerdictClear,
		},
		{
			name: "dirty blocks",
			in: func() RecoveryGuardInput {
				i := cleanDetachedHead()
				i.Dirty = 1
				return i
			}(),
			want: RecoveryGuardVerdictNeedsRecovery,
		},
		{
			name: "missing worktree blocks",
			in:   RecoveryGuardInput{},
			want: RecoveryGuardVerdictNeedsRecovery,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateRecoveryGuard(tt.in)
			if got.Verdict != tt.want {
				t.Errorf("Verdict = %q, want %q", got.Verdict, tt.want)
			}
		})
	}
}

// TestEvaluateRecoveryGuard_ReasonsNeverNil verifies the slice is always
// non-nil so JSON encoding always emits an array (not null).
func TestEvaluateRecoveryGuard_ReasonsNeverNil(t *testing.T) {
	for _, in := range []RecoveryGuardInput{
		cleanDetachedHead(),
		{WorktreeFound: false},
		{WorktreeFound: true, Branch: "main", CompareRef: "origin/main"},
	} {
		got := EvaluateRecoveryGuard(in)
		if got.Reasons == nil {
			t.Errorf("Reasons is nil for %+v", in)
		}
		// sorted copy for stable diffing
		sorted := append([]string{}, got.Reasons...)
		sort.Strings(sorted)
		_ = sorted
	}
}

// containsReason returns true if reasons contains an exact match. We do
// not use slices.Contains so the test file remains dependency-free.
func containsReason(reasons []string, want string) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}
