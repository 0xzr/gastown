// gastown-cet.2.3 regression tests.
//
// Covers two P0 root-cause classes that this bead closes:
//
//	hq-try2 — Stacked-branch MR packaging. A polecat branch with N>1 commits
//	          ahead of the merge-base must not create an MR whose advertised
//	          commit_sha omits the earlier commits. Either the branch is
//	          rejected pre-creation with actionable remediation, or the MR
//	          carries a self-contained/squashed diff. The fix is the
//	          ErrStackedBranch guard in mr_lifecycle.go invoked from both
//	          `gt done` and `gt mq submit`.
//
//	hq-6sdu — Local-only "merged" status hides missing upstream publication.
//	          A refinery merge to a local file remote (file://...) must not
//	          leave source beads looking shipped when the configured
//	          upstream (e.g. GitHub origin/main) has not advanced. The fix
//	          is the published_commit/terminal_state classification in
//	          beads.ClassifyMRTerminalState so source beads stay pending until
//	          upstream sync is verified.
//
// These tests are CHARACTERIZATION tests: they exercise the new helpers
// directly without needing a real refinery run. Each test is a small,
// hermetic git repo or a synthetic *beads.Issue that captures one
// observable behavior of the fix.
package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	gitpkg "github.com/steveyegge/gastown/internal/git"
)

// ---------------------------------------------------------------------------
// hq-try2: stacked-branch detection
// ---------------------------------------------------------------------------

// TestCheckStackedBranch_TwoCommitBranch exercises the canonical hq-try2
// shape: a branch whose tip depends on an earlier unmerged commit. The
// tip SHA alone cannot be merged — the earlier commit is missing — so
// CheckStackedBranch must return ErrStackedBranch with the right counts.
//
// This is the regression case: a 3-commit polybot branch produced an MR
// advertising only the tip commit, the refinery cherry-picked that commit,
// and the MR conflicted at the pre-gate. The durable fix is to reject the
// branch BEFORE creating the MR.
func TestCheckStackedBranch_TwoCommitBranch(t *testing.T) {
	repo := setupTwoCommitBranchRepo(t)

	g := gitpkg.NewGit(repo)
	info, err := CheckStackedBranch(g, "feature/stacked", "main")
	if err == nil {
		t.Fatalf("CheckStackedBranch did not flag a 2-commit branch as stacked; info=%+v", info)
	}
	var stacked *ErrStackedBranch
	if !errors.As(err, &stacked) {
		t.Fatalf("expected ErrStackedBranch, got %T: %v", err, err)
	}
	if stacked.CommitsAhead != 2 {
		t.Errorf("CommitsAhead=%d, want 2", stacked.CommitsAhead)
	}
	if stacked.Branch != "feature/stacked" {
		t.Errorf("Branch=%q, want feature/stacked", stacked.Branch)
	}
	// Sanity: error message names the branch and remediation steps. The
	// user-facing string is the single source of truth for "how do I fix
	// this?" — gt done / gt mq submit tests assert on substrings below.
	msg := err.Error()
	for _, want := range []string{"stacked branch", "feature/stacked", "Squash", "git push"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q: %s", want, msg)
		}
	}
}

// TestCheckStackedBranch_ThreeCommitBranch covers the literal hq-try2
// reproduction (polybot-h85.10.18 had three commits on the submitted
// branch). The test confirms the guard rejects at N>=2, not just N==2.
func TestCheckStackedBranch_ThreeCommitBranch(t *testing.T) {
	repo := setupThreeCommitBranchRepo(t)

	g := gitpkg.NewGit(repo)
	_, err := CheckStackedBranch(g, "feature/polybot-stack", "main")
	if err == nil {
		t.Fatal("expected ErrStackedBranch for 3-commit branch, got nil")
	}
	var stacked *ErrStackedBranch
	if !errors.As(err, &stacked) {
		t.Fatalf("expected ErrStackedBranch, got %T: %v", err, err)
	}
	if stacked.CommitsAhead != 3 {
		t.Errorf("CommitsAhead=%d, want 3", stacked.CommitsAhead)
	}
}

// TestCheckStackedBranch_SingleCommitPasses confirms the guard does not
// reject single-commit branches — that is the common case and must keep
// working. Without this, every polecat that produces one commit per
// branch would suddenly start failing gt done.
func TestCheckStackedBranch_SingleCommitPasses(t *testing.T) {
	repo := setupSingleCommitBranchRepo(t)

	g := gitpkg.NewGit(repo)
	info, err := CheckStackedBranch(g, "feature/clean", "main")
	if err != nil {
		t.Fatalf("CheckStackedBranch rejected a single-commit branch: %v", err)
	}
	if info == nil {
		t.Fatal("CheckStackedBranch returned nil info without error")
	}
	if info.Stacked {
		t.Errorf("Stacked=true for a single-commit branch; info=%+v", info)
	}
	if info.CommitsAhead != 1 {
		t.Errorf("CommitsAhead=%d, want 1", info.CommitsAhead)
	}
}

func TestCheckStackedBranchForSubmitUsesFreshOriginTarget(t *testing.T) {
	repo := setupStaleLocalMainRepo(t)

	g := gitpkg.NewGit(repo)
	_, err := CheckStackedBranch(g, "feature/clean", "main")
	var stacked *ErrStackedBranch
	if !errors.As(err, &stacked) {
		t.Fatalf("local main should look falsely stacked before the submit helper refreshes origin; got %T: %v", err, err)
	}
	_, err = CheckStackedBranch(g, "feature/clean", "origin/main")
	if !errors.As(err, &stacked) {
		t.Fatalf("stale origin/main should look falsely stacked before the submit helper refreshes origin; got %T: %v", err, err)
	}

	info, err := checkStackedBranchForSubmitInfo(g, "feature/clean", "main")
	if err != nil {
		t.Fatalf("checkStackedBranchForSubmitInfo rejected clean branch against origin/main: %v", err)
	}
	if info == nil {
		t.Fatal("checkStackedBranchForSubmitInfo returned nil info without error")
	}
	if info.Stacked {
		t.Fatalf("Stacked=true for clean branch against fresh origin/main; info=%+v", info)
	}
	if info.CommitsAhead != 1 {
		t.Fatalf("CommitsAhead=%d, want 1 against fresh origin/main", info.CommitsAhead)
	}
	originMain := strings.TrimSpace(runMRTestGitOutput(t, repo, "rev-parse", "origin/main"))
	if info.MergeBase != originMain {
		t.Fatalf("MergeBase=%s, want origin/main %s", info.MergeBase, originMain)
	}
}

func TestDoneStackedBranchGuardChecksAdvertisedHeadNotRewrittenAlias(t *testing.T) {
	repo := setupStaleLocalMainRepo(t)
	writeMRTestFile(t, repo, "feature.go", "package x\n// second dependent change\n")
	runMRTestGit(t, repo, "add", "feature.go")
	runMRTestGit(t, repo, "commit", "-m", "second dependent change")

	// No local durable publication alias exists. This reproduces gt done
	// rewriting an ephemeral work branch to ...-rw1 before creating that ref.
	if err := exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet",
		"refs/heads/polecat/thunder/issue-rw1").Run(); err == nil {
		t.Fatal("test setup unexpectedly created rewritten branch alias")
	}

	g := gitpkg.NewGit(repo)
	info, err := checkDoneHeadForSubmitInfo(g, "main")
	var stacked *ErrStackedBranch
	if !errors.As(err, &stacked) {
		t.Fatalf("gt done HEAD guard did not reject two advertised commits; info=%+v err=%v", info, err)
	}
	if stacked.Branch != "HEAD" {
		t.Fatalf("guard checked %q, want exact advertised HEAD", stacked.Branch)
	}
	if stacked.CommitsAhead != 2 {
		t.Fatalf("CommitsAhead=%d, want 2", stacked.CommitsAhead)
	}
}

func TestStackedBranchSubmitGuardFailsClosedWhenBranchCannotResolve(t *testing.T) {
	repo := setupStaleLocalMainRepo(t)
	g := gitpkg.NewGit(repo)

	info, err := checkStackedBranchForSubmitInfo(g, "missing-publication-alias", "main")
	if err == nil {
		t.Fatalf("unresolvable packaging ref proceeded with info=%+v", info)
	}
	if !strings.Contains(err.Error(), "verify single-commit packaging") ||
		!strings.Contains(err.Error(), "missing-publication-alias") {
		t.Fatalf("unresolvable-ref error is not actionable: %v", err)
	}
}

// TestCheckStackedBranch_NoOpBranchPasses: if the branch tip is identical
// to the merge-base (no new commits), it must NOT be flagged as stacked.
// This is the "nothing to merge" case and should fall through cleanly.
func TestCheckStackedBranch_NoOpBranchPasses(t *testing.T) {
	repo := setupNoOpBranchRepo(t)

	g := gitpkg.NewGit(repo)
	info, err := CheckStackedBranch(g, "feature/noop", "main")
	if err != nil {
		t.Fatalf("CheckStackedBranch rejected a no-op branch: %v", err)
	}
	if info.Stacked {
		t.Errorf("Stacked=true for a no-op branch; info=%+v", info)
	}
	if info.CommitsAhead != 0 {
		t.Errorf("CommitsAhead=%d, want 0 for no-op", info.CommitsAhead)
	}
}

// TestCheckStackedBranch_TipEqualsTarget: branch tip == target ref. No
// commits ahead, nothing to merge, definitely not stacked.
func TestCheckStackedBranch_TipEqualsTarget(t *testing.T) {
	repo := setupSingleCommitBranchRepo(t)
	// Use main as both branch and target — same SHA.
	g := gitpkg.NewGit(repo)
	info, err := CheckStackedBranch(g, "main", "main")
	if err != nil {
		t.Fatalf("CheckStackedBranch rejected same-ref case: %v", err)
	}
	if info.Stacked {
		t.Errorf("Stacked=true when branch==target")
	}
}

// TestErrStackedBranchErrorFormat pins the user-facing remediation text.
// gt done and gt mq submit propagate this string verbatim, so changes to
// the format must be deliberate. The substrings asserted here are what
// polecats see in their terminal when they hit a stacked branch — the
// whole point of the fix is that they get actionable guidance, not just
// "MR rejected."
func TestErrStackedBranchErrorFormat(t *testing.T) {
	stacked := &ErrStackedBranch{
		Branch:       "polecat/quartz/gt-abc",
		Target:       "origin/main",
		CommitsAhead: 3,
		MergeBase:    "deadbeef",
		TipSHA:       "cafebabe",
	}
	msg := stacked.Error()
	for _, want := range []string{
		"polecat/quartz/gt-abc",
		"3 commits",
		"origin/main",
		"merge-base",
		"deadbeef",
		"Refinery cherry-picks a single commit_sha",
		"Squash",
		"git merge --squash origin/main",
		"git push origin polecat/quartz/gt-abc",
		"re-run `gt done`",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("ErrStackedBranch.Error() missing %q\nfull message:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "origin/origin/main") {
		t.Errorf("ErrStackedBranch.Error() double-prefixed target:\n%s", msg)
	}
}

// ---------------------------------------------------------------------------
// hq-6sdu: terminal-state classification for source-bead guard
// ---------------------------------------------------------------------------

// TestClassifyMRTerminalState_OpenIsPendingRefinery: while the MR is
// still queued or in_progress, the source bead MUST remain pending — that
// is the entire point of gastown-cet.2.3 acceptance criteria #2.
func TestClassifyMRTerminalState_OpenIsPendingRefinery(t *testing.T) {
	mr := &beads.Issue{Status: "open"}
	if got := beads.ClassifyMRTerminalState(mr); got != beads.MRTerminalPendingRefinery {
		t.Errorf("open MR classified as %q, want %q", got, beads.MRTerminalPendingRefinery)
	}
	if beads.CanCloseSourceBead(mr) {
		t.Error("CanCloseSourceBead=true for open MR — source would close prematurely")
	}
}

// TestClassifyMRTerminalState_InProgressIsPendingRefinery: in-progress MRs
// are still pending from the source-bead's perspective.
func TestClassifyMRTerminalState_InProgressIsPendingRefinery(t *testing.T) {
	mr := &beads.Issue{Status: "in_progress"}
	if got := beads.ClassifyMRTerminalState(mr); got != beads.MRTerminalPendingRefinery {
		t.Errorf("in_progress MR classified as %q, want %q", got, beads.MRTerminalPendingRefinery)
	}
	if beads.CanCloseSourceBead(mr) {
		t.Error("CanCloseSourceBead=true for in_progress MR — source would close prematurely")
	}
}

// TestClassifyMRTerminalState_ClosedMergedNoPublished: the hq-6sdu class.
// Refinery closed the MR with reason=merged but no PublishedCommit. This
// is the exact shape that lets the source bead be reported as shipped
// when in fact the upstream never saw the change. Source beads MUST stay
// pending.
func TestClassifyMRTerminalState_ClosedMergedNoPublished(t *testing.T) {
	mr := &beads.Issue{
		Status: "closed",
		Description: `branch: polecat/quartz/gt-abc
target: main
source_issue: gt-abc
commit_sha: b5a6a81600000000000000000000000000000000
merge_commit: f752592e00000000000000000000000000000000
close_reason: merged`,
	}
	got := beads.ClassifyMRTerminalState(mr)
	if got != beads.MRTerminalMergedLocalNotPublished {
		t.Fatalf("closed+merged MR with no PublishedCommit classified as %q, want %q", got, beads.MRTerminalMergedLocalNotPublished)
	}
	if beads.CanCloseSourceBead(mr) {
		t.Error("CanCloseSourceBead=true for merged-local-not-published MR — hq-6sdu regression")
	}
}

// TestClassifyMRTerminalState_ClosedMergedPublished: once the refinery
// records PublishedCommit (upstream sync verified), the MR reaches the
// terminal `published` state and the source bead is safe to close.
func TestClassifyMRTerminalState_ClosedMergedPublished(t *testing.T) {
	mr := &beads.Issue{
		Status: "closed",
		Description: `branch: polecat/quartz/gt-abc
target: main
source_issue: gt-abc
commit_sha: b5a6a81600000000000000000000000000000000
merge_commit: f752592e00000000000000000000000000000000
close_reason: merged
published_commit: 7b076fc1000000000000000000000000000000000
published_remote: origin
published_at: 2026-06-24T17:30:00Z`,
	}
	got := beads.ClassifyMRTerminalState(mr)
	if got != beads.MRTerminalPublished {
		t.Fatalf("classified as %q, want %q", got, beads.MRTerminalPublished)
	}
	if !beads.CanCloseSourceBead(mr) {
		t.Error("CanCloseSourceBead=false for published MR — source would stay open forever")
	}
	if !beads.IsMRTerminalPublished(mr) {
		t.Error("IsMRTerminalPublished=false for published MR")
	}
}

// TestClassifyMRTerminalState_ClosedRejected: refinery rejection must
// keep the source reworkable. The terminal state should be
// rejected-needs-rework, and CanCloseSourceBead must be false.
func TestClassifyMRTerminalState_ClosedRejected(t *testing.T) {
	mr := &beads.Issue{
		Status: "closed",
		Description: `branch: polecat/quartz/gt-abc
target: main
close_reason: rejected`,
	}
	got := beads.ClassifyMRTerminalState(mr)
	if got != beads.MRTerminalRejectedNeedsRework {
		t.Errorf("classified as %q, want %q", got, beads.MRTerminalRejectedNeedsRework)
	}
	if beads.CanCloseSourceBead(mr) {
		t.Error("CanCloseSourceBead=true for rejected MR — source would close on rejection")
	}
}

// TestClassifyMRTerminalState_ClosedConflict: refinery conflict reasons
// also map to rejected-needs-rework. This is the most common failure
// mode in practice and must keep the source bead reworkable.
func TestClassifyMRTerminalState_ClosedConflict(t *testing.T) {
	mr := &beads.Issue{
		Status:      "closed",
		Description: "close_reason: conflict\n",
	}
	got := beads.ClassifyMRTerminalState(mr)
	if got != beads.MRTerminalRejectedNeedsRework {
		t.Errorf("classified as %q, want %q", got, beads.MRTerminalRejectedNeedsRework)
	}
}

// TestClassifyMRTerminalState_ClosedSuperseded: a superseded MR means a
// newer MR replaces it. Source must be reworked (re-attached) rather
// than silently closed.
func TestClassifyMRTerminalState_ClosedSuperseded(t *testing.T) {
	mr := &beads.Issue{
		Status:      "closed",
		Description: "close_reason: superseded\n",
	}
	got := beads.ClassifyMRTerminalState(mr)
	if got != beads.MRTerminalRejectedNeedsRework {
		t.Errorf("classified as %q, want %q", got, beads.MRTerminalRejectedNeedsRework)
	}
}

// TestClassifyMRTerminalState_ClosedUnknownReason: a closed MR with no
// recognized close reason must default to needs-rework so the source
// does NOT silently close. This is the safe default — better to leave a
// source open than to close it without evidence.
func TestClassifyMRTerminalState_ClosedUnknownReason(t *testing.T) {
	mr := &beads.Issue{
		Status:      "closed",
		Description: "close_reason: weather-is-nice\n",
	}
	got := beads.ClassifyMRTerminalState(mr)
	if got != beads.MRTerminalRejectedNeedsRework {
		t.Errorf("classified as %q, want %q (default safe state)", got, beads.MRTerminalRejectedNeedsRework)
	}
}

// TestClassifyMRTerminalState_NilMR: defensive — nil issue must not
// panic and must not permit closing the source. Returning "" forces
// callers to take the conservative branch.
func TestClassifyMRTerminalState_NilMR(t *testing.T) {
	if got := beads.ClassifyMRTerminalState(nil); got != "" {
		t.Errorf("nil MR classified as %q, want empty", got)
	}
	if beads.CanCloseSourceBead(nil) {
		t.Error("CanCloseSourceBead(nil)=true — must be conservative on nil")
	}
	if beads.IsMRTerminalPublished(nil) {
		t.Error("IsMRTerminalPublished(nil)=true — must be conservative on nil")
	}
}

// TestClassifyMRTerminalState_LegacyCloseReasonInProse: legacy MRs may
// store the close reason as a bare "close_reason: <value>" line at any
// position. The fallback parser must pick that up so historical MRs are
// still classified correctly.
func TestClassifyMRTerminalState_LegacyCloseReasonInProse(t *testing.T) {
	mr := &beads.Issue{
		Status: "closed",
		Description: `Some prose at the top.

close_reason: merged`,
	}
	got := beads.ClassifyMRTerminalState(mr)
	if got != beads.MRTerminalMergedLocalNotPublished {
		t.Errorf("legacy close_reason classified as %q, want %q", got, beads.MRTerminalMergedLocalNotPublished)
	}
}

// ---------------------------------------------------------------------------
// Combined regression: the acceptance criteria from gastown-cet.2.3
// ---------------------------------------------------------------------------

// TestAcceptanceCriteria_HQTry2AndHQ6SDU is the umbrella regression
// covering the full bead. It assembles a stacked-branch MR (hq-try2)
// whose close reason is "merged" but which has no PublishedCommit
// (hq-6sdu) — exactly the production failure pattern — and asserts:
//
//  1. A pre-MR stacked-branch guard rejects the branch with ErrStackedBranch.
//  2. Once the MR is closed-without-publication, source beads must NOT
//     be considered closable. The full chain — pending, pending, then
//     merged-local-not-published — keeps dependents blocked.
//
// If either assertion regresses, hq-try2 or hq-6sdu has reopened.
func TestAcceptanceCriteria_HQTry2AndHQ6SDU(t *testing.T) {
	// (1) Stacked-branch guard rejects pre-MR.
	repo := setupThreeCommitBranchRepo(t)
	g := gitpkg.NewGit(repo)
	_, err := CheckStackedBranch(g, "feature/polybot-stack", "main")
	var stacked *ErrStackedBranch
	if !errors.As(err, &stacked) {
		t.Fatalf("stacked-branch guard did not fire on hq-try2 shape: %v", err)
	}
	if stacked.CommitsAhead < 2 {
		t.Errorf("stacked branch reported %d commits ahead; expected >=2", stacked.CommitsAhead)
	}

	// (2) Source-bead guard treats the corresponding closed MR as
	//     merged-local-not-published and refuses to close the source.
	mr := &beads.Issue{
		Status: "closed",
		Description: fmt.Sprintf(`branch: polecat/quartz/polybot-stack
target: main
source_issue: gastown-cet.2.3
commit_sha: %s
merge_commit: f752592e
close_reason: merged`, stacked.TipSHA),
	}
	if beads.CanCloseSourceBead(mr) {
		t.Error("source bead would close despite no PublishedCommit — hq-6sdu regression")
	}
	if beads.IsMRTerminalPublished(mr) {
		t.Error("MR reported as published with no PublishedCommit — hq-6sdu regression")
	}
	if beads.ClassifyMRTerminalState(mr) != beads.MRTerminalMergedLocalNotPublished {
		t.Errorf("classification=%q, want %q", beads.ClassifyMRTerminalState(mr), beads.MRTerminalMergedLocalNotPublished)
	}
}

// ---------------------------------------------------------------------------
// Git fixtures: small repos built per-test so the helpers are hermetic.
// ---------------------------------------------------------------------------

func setupSingleCommitBranchRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runMRTestGit(t, repo, "init")
	runMRTestGit(t, repo, "config", "user.email", "test@example.com")
	runMRTestGit(t, repo, "config", "user.name", "Test User")
	writeMRTestFile(t, repo, "README.md", "main\n")
	runMRTestGit(t, repo, "add", "README.md")
	runMRTestGit(t, repo, "commit", "-m", "initial")
	runMRTestGit(t, repo, "checkout", "-b", "feature/clean")
	writeMRTestFile(t, repo, "feature.go", "package x\n")
	runMRTestGit(t, repo, "add", "feature.go")
	runMRTestGit(t, repo, "commit", "-m", "add feature")
	return repo
}

func setupTwoCommitBranchRepo(t *testing.T) string {
	t.Helper()
	repo := setupSingleCommitBranchRepo(t)
	writeMRTestFile(t, repo, "feature.go", "package x\n// tweak\n")
	runMRTestGit(t, repo, "add", "feature.go")
	runMRTestGit(t, repo, "commit", "-m", "tweak feature")
	runMRTestGit(t, repo, "branch", "-m", "feature/stacked")
	return repo
}

func setupThreeCommitBranchRepo(t *testing.T) string {
	t.Helper()
	repo := setupSingleCommitBranchRepo(t)
	runMRTestGit(t, repo, "branch", "-m", "feature/polybot-stack")
	writeMRTestFile(t, repo, "feature.go", "package x\n// tweak 1\n")
	runMRTestGit(t, repo, "add", "feature.go")
	runMRTestGit(t, repo, "commit", "-m", "tweak 1")
	writeMRTestFile(t, repo, "feature.go", "package x\n// tweak 1\n// tweak 2\n")
	runMRTestGit(t, repo, "add", "feature.go")
	runMRTestGit(t, repo, "commit", "-m", "tweak 2")
	return repo
}

func setupNoOpBranchRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runMRTestGit(t, repo, "init")
	runMRTestGit(t, repo, "config", "user.email", "test@example.com")
	runMRTestGit(t, repo, "config", "user.name", "Test User")
	writeMRTestFile(t, repo, "README.md", "main\n")
	runMRTestGit(t, repo, "add", "README.md")
	runMRTestGit(t, repo, "commit", "-m", "initial")
	runMRTestGit(t, repo, "checkout", "-b", "feature/noop")
	runMRTestGit(t, repo, "checkout", "main") // tip == main, no commits on branch
	return repo
}

func setupStaleLocalMainRepo(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote.git")
	runMRTestGit(t, tmp, "init", "--bare", remote)

	seed := filepath.Join(tmp, "seed")
	runMRTestGit(t, tmp, "clone", remote, seed)
	runMRTestGit(t, seed, "config", "user.email", "test@example.com")
	runMRTestGit(t, seed, "config", "user.name", "Test User")
	writeMRTestFile(t, seed, "README.md", "base\n")
	runMRTestGit(t, seed, "add", "README.md")
	runMRTestGit(t, seed, "commit", "-m", "base")
	runMRTestGit(t, seed, "branch", "-M", "main")
	runMRTestGit(t, seed, "push", "-u", "origin", "main")
	runMRTestGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")

	local := filepath.Join(tmp, "local")
	runMRTestGit(t, tmp, "clone", remote, local)
	runMRTestGit(t, local, "config", "user.email", "test@example.com")
	runMRTestGit(t, local, "config", "user.name", "Test User")

	upstream := filepath.Join(tmp, "upstream")
	runMRTestGit(t, tmp, "clone", remote, upstream)
	runMRTestGit(t, upstream, "config", "user.email", "test@example.com")
	runMRTestGit(t, upstream, "config", "user.name", "Test User")
	for i := 1; i <= 3; i++ {
		writeMRTestFile(t, upstream, "README.md", fmt.Sprintf("base\nupstream %d\n", i))
		runMRTestGit(t, upstream, "add", "README.md")
		runMRTestGit(t, upstream, "commit", "-m", fmt.Sprintf("upstream %d", i))
	}
	runMRTestGit(t, upstream, "push", "origin", "main")

	runMRTestGit(t, local, "fetch", upstream, "main:refs/tmp/latest-main")
	runMRTestGit(t, local, "checkout", "-b", "feature/clean", "refs/tmp/latest-main")
	writeMRTestFile(t, local, "feature.go", "package x\n")
	runMRTestGit(t, local, "add", "feature.go")
	runMRTestGit(t, local, "commit", "-m", "add feature")
	return local
}

func runMRTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func runMRTestGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeMRTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
