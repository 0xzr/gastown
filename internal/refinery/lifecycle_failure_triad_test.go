package refinery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Characterization tests for the GT merge/refinery lifecycle failure triad.
//
// These tests pin GT 1.2.0 behavior so future fixes can be verified against a
// reproducible baseline. They intentionally assert the current (buggy) behavior;
// once the corresponding guards are implemented, these tests should be updated
// or superseded by tests asserting the new safe behavior.
//
// Incident classes:
//   - hq-try2: stacked branch tips create MRs even though they depend on an
//     unmerged base branch, silently landing base-branch changes.
//   - hq-6sdu: a local-only merge (auto_push=false) is reported as successful
//     even though the change was never published to origin.
//   - hq-6af: a PR reviewer with no output/verdict blocks merge instead of
//     being classified as unavailable/audit-gap.

// boolPtr returns a pointer to a bool.
func boolPtr(b bool) *bool { return &b }

// mockPRProvider is a test double used to simulate VCS PR state without
// egressing to GitHub or Bitbucket.
type mockPRProvider struct {
	findNumber int
	approved   bool
	mergeErr   error
}

func (m *mockPRProvider) FindPRNumber(branch string) (int, error) {
	return m.findNumber, nil
}

func (m *mockPRProvider) MergePR(prNumber int, method string) (string, error) {
	return "", m.mergeErr
}

func (m *mockPRProvider) GetReviewEvaluation(prNumber int) (*ReviewEvaluation, error) {
	if m.approved {
		return &ReviewEvaluation{
			State:     ReviewStatePass,
			Results:   []ReviewerResult{{Reviewer: "mock", Verdict: ReviewerVerdictPass}},
			PassCount: 1,
		}, nil
	}
	return &ReviewEvaluation{
		State:          ReviewStateNoVerdict,
		Results:        []ReviewerResult{{Reviewer: "mock", Verdict: ReviewerVerdictNoVerdict}},
		NoVerdictCount: 1,
		Error:          "no verdict",
	}, nil
}

func TestHqTry2_StackedBranchTipOnly_MergesWithoutContainmentGuard(t *testing.T) {
	// GT 1.2.0 characterization (hq-try2): a stacked tip branch whose history
	// includes an unmerged base branch is accepted and merged into main. The
	// base-branch changes land silently even though the base branch itself was
	// never reviewed or merged as an MR.
	//
	// Future safe behavior: the refinery should reject non-self-contained stacked
	// branches before merge, requiring squash or a self-contained resubmission.
	if testing.Short() {
		t.Skip("characterization test exercises real git merge")
	}

	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil

	run(t, workDir, "git", "checkout", "-b", "feature/hq-try2-base", "main")
	writeFile(t, workDir, "base-change.txt", "base content")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat: base branch change (hq-try2)")

	// Tip branch is created from the unmerged base branch.
	run(t, workDir, "git", "checkout", "-b", "feature/hq-try2-tip", "feature/hq-try2-base")
	writeFile(t, workDir, "tip-change.txt", "tip content")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat: tip branch change (hq-try2)")
	run(t, workDir, "git", "checkout", "main")

	result := e.doMerge(context.Background(), "feature/hq-try2-tip", "main", "gt-hq-try2", nil)
	if !result.Success {
		t.Fatalf("expected stacked tip branch to merge successfully under GT 1.2.0, got: %s", result.Error)
	}

	// The base-branch file should have landed on main even though the base
	// branch itself was never merged. This is the failure being characterized.
	basePath := filepath.Join(workDir, "base-change.txt")
	if _, err := os.Stat(basePath); err != nil {
		t.Fatalf("expected stacked merge to silently include base-branch changes; base file missing: %v", err)
	}
	data, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "base content") {
		t.Fatalf("expected base-branch content on main, got: %s", string(data))
	}
}

func TestHq6sdu_LocalMergeWithoutPush_ReportedAsShipped(t *testing.T) {
	// GT 1.2.0 characterization (hq-6sdu): with auto_push disabled, the refinery
	// performs a local squash merge and reports success. The source bead can be
	// closed even though origin/main does not contain the merge commit.
	//
	// Future safe behavior: the refinery must verify that the terminal merge
	// commit appears on the configured publication target before reporting
	// success and closing the source bead.
	if testing.Short() {
		t.Skip("characterization test exercises real git merge")
	}

	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil

	createFeatureBranch(t, workDir, "feature/hq-6sdu", "feature.txt", "feature content")

	originMainBefore, err := g.RemoteBranchTip("origin", "main")
	if err != nil {
		t.Fatalf("get origin/main tip before merge: %v", err)
	}

	result := e.doMerge(context.Background(), "feature/hq-6sdu", "main", "gt-hq-6sdu", nil)
	if !result.Success {
		t.Fatalf("expected local merge to report success under GT 1.2.0, got: %s", result.Error)
	}
	if result.MergeCommit == "" {
		t.Fatal("expected a local merge commit SHA")
	}

	originMainAfter, err := g.RemoteBranchTip("origin", "main")
	if err != nil {
		t.Fatalf("get origin/main tip after merge: %v", err)
	}

	// The bad behavior: the merge succeeded locally but origin/main is
	// unchanged, so the work has not actually been published.
	if originMainAfter != originMainBefore {
		t.Fatalf("expected origin/main to remain unchanged (local-only merge), but it moved from %s to %s", shortSHA(originMainBefore), shortSHA(originMainAfter))
	}
	if originMainAfter == result.MergeCommit {
		t.Fatalf("expected origin/main tip to differ from local merge commit %s", shortSHA(result.MergeCommit))
	}
}

func TestHq6af_NoVerdictReviewer_BlocksMergeAsApprovalMissing(t *testing.T) {
	// GT 1.2.0 characterization (hq-6af): a PR reviewer who left output without
	// an explicit approving verdict is treated as "approval missing", which blocks
	// merge when require_review is enabled.
	//
	// Future safe behavior: a no-output/no-verdict reviewer should be classified
	// as unavailable/audit-gap and surfaced through a degraded-quorum rule
	// rather than being treated as a hard approval failure.
	if testing.Short() {
		t.Skip("characterization test exercises merge path with mock PR provider")
	}

	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.MergeStrategy = "pr"
	e.config.RequireReview = boolPtr(true)
	e.config.RunTests = false
	e.config.Gates = nil
	e.prProvider = &mockPRProvider{
		findNumber: 42,
		approved:   false, // comment-only / no-verdict review
		mergeErr:   errMergeWouldHaveBeenCalled,
	}

	createFeatureBranch(t, workDir, "feature/hq-6af", "feature.txt", "feature content")

	result := e.doMerge(context.Background(), "feature/hq-6af", "main", "gt-hq-6af", nil)
	if result.Success {
		t.Fatal("expected no-verdict reviewer to block merge under GT 1.2.0")
	}
	if !result.NeedsApproval {
		t.Fatalf("expected NeedsApproval=true for no-verdict reviewer, got result: %+v", result)
	}
}

// errMergeWouldHaveBeenCalled is a sentinel error so a mock PR provider that is
// accidentally asked to merge will fail the test.
var errMergeWouldHaveBeenCalled = errFeatureTest("mock MergePR should not be called when approval is missing")

type errFeatureTest string

func (e errFeatureTest) Error() string { return string(e) }
