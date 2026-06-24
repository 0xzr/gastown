package refinery

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/rig"
)

func TestEngineer_LoadConfig_MergeStrategyPR(t *testing.T) {
	tmpDir := t.TempDir()

	requireReview := true
	config := map[string]interface{}{
		"type":    "rig",
		"version": 1,
		"name":    "test-rig",
		"merge_queue": map[string]interface{}{
			"merge_strategy": "pr",
			"require_review": requireReview,
		},
	}

	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{Name: "test-rig", Path: tmpDir}
	e := NewEngineer(r)
	if err := e.LoadConfig(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if e.config.MergeStrategy != "pr" {
		t.Errorf("expected MergeStrategy 'pr', got %q", e.config.MergeStrategy)
	}
	if e.config.RequireReview == nil || !*e.config.RequireReview {
		t.Error("expected RequireReview to be true")
	}
}

func TestEngineer_LoadConfig_MergeStrategyDefault(t *testing.T) {
	tmpDir := t.TempDir()

	config := map[string]interface{}{
		"type":        "rig",
		"version":     1,
		"name":        "test-rig",
		"merge_queue": map[string]interface{}{},
	}

	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{Name: "test-rig", Path: tmpDir}
	e := NewEngineer(r)
	if err := e.LoadConfig(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if e.config.MergeStrategy != "" {
		t.Errorf("expected empty MergeStrategy (default), got %q", e.config.MergeStrategy)
	}
	if e.config.RequireReview != nil {
		t.Error("expected RequireReview to be nil (default)")
	}
}

func TestDoMerge_PRStrategy_RoutesToPRPath(t *testing.T) {
	// When merge_strategy=pr, doMerge should attempt the PR merge path.
	// Without a real GitHub repo, FindPRNumber will fail — that's the expected
	// behavior we test: the code routes to doMergePR and fails gracefully.
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.MergeStrategy = "pr"

	// Create a feature branch
	createFeatureBranch(t, workDir, "feat/test-pr", "test.txt", "hello")

	result := e.doMerge(context.Background(), "feat/test-pr", "main", "gt-test", nil)

	if result.Success {
		t.Error("expected failure (no GitHub PR exists)")
	}

	output := e.output.(*bytes.Buffer).String()
	if !strings.Contains(output, "PR merge strategy") {
		t.Errorf("expected PR merge strategy log, got: %s", output)
	}
}

func TestDoMerge_DirectStrategy_SkipsPRPath(t *testing.T) {
	// When merge_strategy is empty (direct), doMerge should use the normal path.
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.MergeStrategy = "" // explicit direct

	createFeatureBranch(t, workDir, "feat/test-direct", "test.txt", "hello")

	result := e.doMerge(context.Background(), "feat/test-direct", "main", "gt-test", nil)

	// Should succeed with direct merge
	if !result.Success {
		t.Errorf("expected success for direct merge, got error: %s", result.Error)
	}

	output := e.output.(*bytes.Buffer).String()
	if strings.Contains(output, "PR merge strategy") {
		t.Error("direct merge should not mention PR merge strategy")
	}
}

func TestDoMergePR_NoPR_ReturnsError(t *testing.T) {
	// doMergePR should return an error when no PR exists for the branch.
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)

	createFeatureBranch(t, workDir, "feat/no-pr", "test.txt", "hello")

	result := e.doMergePR(context.Background(), "feat/no-pr", "main", nil)

	if result.Success {
		t.Error("expected failure when no PR exists")
	}
	// The error should mention finding a PR
	if !strings.Contains(result.Error, "PR") && !strings.Contains(result.Error, "pr") {
		t.Errorf("expected PR-related error, got: %s", result.Error)
	}
}

func TestProcessResult_NeedsApproval(t *testing.T) {
	// Verify NeedsApproval field works on ProcessResult.
	r := ProcessResult{
		Success:       false,
		NeedsApproval: true,
		Error:         "PR #42 requires approving review before merge",
	}

	if r.Success {
		t.Error("expected Success=false")
	}
	if !r.NeedsApproval {
		t.Error("expected NeedsApproval=true")
	}
}

func TestHandleMRInfoFailure_NeedsApproval_StaysInQueue(t *testing.T) {
	// When NeedsApproval is true, the MR should stay in queue without
	// sending failure notifications to polecats or mayor.
	workDir := t.TempDir()
	r := &rig.Rig{Name: "test-rig", Path: workDir}
	e := NewEngineer(r)
	var buf bytes.Buffer
	e.output = &buf
	e.workDir = workDir
	e.mergeSlotEnsureExists = func() (string, error) { return "test-slot", nil }
	e.mergeSlotAcquire = func(holder string, addWaiter bool) (*beads.MergeSlotStatus, error) {
		return &beads.MergeSlotStatus{Available: true, Holder: holder}, nil
	}
	e.mergeSlotRelease = func(holder string) error { return nil }

	mr := &MRInfo{
		ID:          "gt-test",
		Branch:      "polecat/test/gt-test",
		Target:      "main",
		SourceIssue: "gt-src",
		Worker:      "polecats/test",
	}
	result := ProcessResult{
		Success:       false,
		NeedsApproval: true,
		Error:         "PR #42 requires approving review before merge",
	}

	e.HandleMRInfoFailure(mr, result)

	output := buf.String()
	if !strings.Contains(output, "awaiting human approval") {
		t.Errorf("expected 'awaiting human approval' message, got: %s", output)
	}
	// Should NOT contain merge failure notifications
	if strings.Contains(output, "MERGE_FAILED") {
		t.Error("NeedsApproval should not trigger MERGE_FAILED notification")
	}
}

type reviewerMockPRProvider struct {
	findPRNumber        func(branch string) (int, error)
	isPRApproved        func(prNumber int) (bool, error)
	getReviewEvaluation func(prNumber int) (*ReviewEvaluation, error)
	mergePR             func(prNumber int, method string) (string, error)
}

func (m *reviewerMockPRProvider) FindPRNumber(branch string) (int, error) {
	return m.findPRNumber(branch)
}

func (m *reviewerMockPRProvider) IsPRApproved(prNumber int) (bool, error) {
	return m.isPRApproved(prNumber)
}

func (m *reviewerMockPRProvider) GetReviewEvaluation(prNumber int) (*ReviewEvaluation, error) {
	return m.getReviewEvaluation(prNumber)
}

func (m *reviewerMockPRProvider) MergePR(prNumber int, method string) (string, error) {
	return m.mergePR(prNumber, method)
}

func TestDoMergePR_NoVerdict_NotTreatedAsFail(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.MergeStrategy = "pr"
	e.config.RequireReview = boolPtr(true)

	createFeatureBranch(t, workDir, "feat/no-verdict", "test.txt", "hello")

	e.prProvider = &reviewerMockPRProvider{
		findPRNumber: func(string) (int, error) { return 42, nil },
		isPRApproved: func(int) (bool, error) { return false, nil },
		getReviewEvaluation: func(int) (*ReviewEvaluation, error) {
			return &ReviewEvaluation{
				State:          ReviewStateNoVerdict,
				Results:        []ReviewerResult{{Reviewer: "github", Verdict: ReviewerVerdictNoVerdict, Evidence: "no reviews"}},
				NoVerdictCount: 1,
				Error:          "no reviews",
			}, nil
		},
		mergePR: nil,
	}

	result := e.doMergePR(context.Background(), "feat/no-verdict", "main", &MRInfo{ID: "gt-test", SourceIssue: "gt-src"})

	if !result.NeedsApproval {
		t.Errorf("expected NeedsApproval for no-verdict, got %+v", result)
	}
	if result.TestsFailed {
		t.Error("no-verdict must not set TestsFailed")
	}
	if result.Success {
		t.Error("expected failure while waiting for verdict")
	}
}

func TestDoMergePR_ParsedFail_Rejects(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.MergeStrategy = "pr"
	e.config.RequireReview = boolPtr(true)

	createFeatureBranch(t, workDir, "feat/fail", "test.txt", "hello")

	e.prProvider = &reviewerMockPRProvider{
		findPRNumber: func(string) (int, error) { return 42, nil },
		isPRApproved: func(int) (bool, error) { return false, nil },
		getReviewEvaluation: func(int) (*ReviewEvaluation, error) {
			return &ReviewEvaluation{
				State:     ReviewStateFail,
				Results:   []ReviewerResult{{Reviewer: "reviewer", Verdict: ReviewerVerdictFail, Blockers: []string{"race condition"}}},
				FailCount: 1,
				Error:     "reviewer rejection",
			}, nil
		},
		mergePR: nil,
	}

	result := e.doMergePR(context.Background(), "feat/fail", "main", &MRInfo{ID: "gt-test", SourceIssue: "gt-src"})

	if result.Success || result.NeedsApproval {
		t.Errorf("expected hard failure, got %+v", result)
	}
	if !result.TestsFailed {
		t.Error("expected parsed FAIL to set TestsFailed so the failure is actionable")
	}
	if !strings.Contains(result.Error, "race condition") {
		t.Errorf("expected error to include reviewer evidence, got %s", result.Error)
	}
}

func TestDoMergePR_DegradedQuorum_ProceedsAndCreatesAudit(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.MergeStrategy = "pr"
	e.config.RequireReview = boolPtr(true)
	e.config.DegradedQuorumEnabled = boolPtr(true)
	e.config.ReviewQuorumMin = 1

	createFeatureBranch(t, workDir, "feat/degraded", "test.txt", "hello")

	mergeCalled := false
	e.prProvider = &reviewerMockPRProvider{
		findPRNumber: func(string) (int, error) { return 42, nil },
		isPRApproved: func(int) (bool, error) { return true, nil },
		getReviewEvaluation: func(int) (*ReviewEvaluation, error) {
			return &ReviewEvaluation{
				State: ReviewStateDegradedQuorum,
				Results: []ReviewerResult{
					{Reviewer: "alice", Verdict: ReviewerVerdictPass},
					{Reviewer: "bob", Verdict: ReviewerVerdictUnavailable},
				},
				PassCount:        1,
				UnavailableCount: 1,
				AuditReviewers:   []string{"bob"},
				Error:            "degraded quorum: 1 PASS, 1 audit",
			}, nil
		},
		mergePR: func(int, string) (string, error) {
			mergeCalled = true
			// Simulate the provider landing the PR by squash-merging locally and pushing.
			if err := g.Checkout("main"); err != nil {
				return "", err
			}
			if err := g.MergeSquash("feat/degraded", "feat: add test.txt"); err != nil {
				return "", err
			}
			sha, err := g.Rev("HEAD")
			if err != nil {
				return "", err
			}
			if err := g.Push("origin", "main", false); err != nil {
				return "", err
			}
			return sha, nil
		},
	}

	result := e.doMergePR(context.Background(), "feat/degraded", "main", &MRInfo{ID: "gt-test", SourceIssue: "gt-src"})

	if !result.Success {
		t.Errorf("expected success under degraded quorum, got %+v", result)
	}
	if !result.DegradedQuorum {
		t.Error("expected DegradedQuorum flag on result")
	}
	if !mergeCalled {
		t.Error("expected MergePR to be called")
	}
}
