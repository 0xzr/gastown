package refinery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	getReviewEvaluation func(prNumber int) (*ReviewEvaluation, error)
	mergePR             func(prNumber int, method string) (string, error)
}

func (m *reviewerMockPRProvider) FindPRNumber(branch string) (int, error) {
	return m.findPRNumber(branch)
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

func TestDoMergePR_ParsedFail_RoutesToNeedsRework(t *testing.T) {
	// gastown-cet.8: a concrete parsed reviewer FAIL with blockers routes to
	// NeedsRework (rejected-needs-rework), NOT the generic TestsFailed/build
	// failure path and NOT the NeedsApproval hold (review-unavailable). The
	// worker must be told to revise, not that the build broke.
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.MergeStrategy = "pr"
	e.config.RequireReview = boolPtr(true)

	createFeatureBranch(t, workDir, "feat/fail", "test.txt", "hello")

	e.prProvider = &reviewerMockPRProvider{
		findPRNumber: func(string) (int, error) { return 42, nil },
		getReviewEvaluation: func(int) (*ReviewEvaluation, error) {
			return &ReviewEvaluation{
				State:     ReviewStateFail,
				Results:   []ReviewerResult{{Reviewer: "reviewer", Verdict: ReviewerVerdictFail, Blockers: []string{"race condition"}, CauseKey: "race_condition"}},
				FailCount: 1,
				CauseKey:  "race_condition",
				Error:     "reviewer rejection",
			}, nil
		},
		mergePR: nil,
	}

	result := e.doMergePR(context.Background(), "feat/fail", "main", &MRInfo{ID: "gt-test", SourceIssue: "gt-src"})

	if result.Success || result.NeedsApproval {
		t.Errorf("expected hard rejection (not success, not approval hold), got %+v", result)
	}
	if !result.NeedsRework {
		t.Errorf("expected NeedsRework=true for parsed reviewer FAIL, got %+v", result)
	}
	if result.TestsFailed {
		t.Error("parsed reviewer FAIL must NOT set TestsFailed (that is the build-failure path)")
	}
	if result.ReviewerRejectionCause != "race_condition" {
		t.Errorf("expected ReviewerRejectionCause=race_condition, got %q", result.ReviewerRejectionCause)
	}
	if !strings.Contains(result.Error, "race condition") {
		t.Errorf("expected error to include reviewer evidence, got %s", result.Error)
	}
}

func TestDoMergePR_ParsedFail_DefaultCauseWhenNoneSupplied(t *testing.T) {
	// gastown-cet.8: a concrete FAIL without an explicit cause key must still
	// route to NeedsRework with a stable default cause, not be mislabeled as a
	// review-unavailable hold (the codex-FAIL mislabeling incident class).
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.MergeStrategy = "pr"
	e.config.RequireReview = boolPtr(true)

	createFeatureBranch(t, workDir, "feat/fail-nocause", "test.txt", "hello")

	e.prProvider = &reviewerMockPRProvider{
		findPRNumber: func(string) (int, error) { return 42, nil },
		getReviewEvaluation: func(int) (*ReviewEvaluation, error) {
			return &ReviewEvaluation{
				State:     ReviewStateFail,
				Results:   []ReviewerResult{{Reviewer: "codex", Verdict: ReviewerVerdictFail, Blockers: []string{"missing test"}}},
				FailCount: 1,
				Error:     "reviewer rejection: codex: missing test",
			}, nil
		},
		mergePR: nil,
	}

	result := e.doMergePR(context.Background(), "feat/fail-nocause", "main", &MRInfo{ID: "gt-test", SourceIssue: "gt-src"})

	if result.NeedsApproval {
		t.Errorf("codex concrete FAIL must NOT be mislabeled REVIEW_UNAVAILABLE_HOLD (NeedsApproval), got %+v", result)
	}
	if !result.NeedsRework {
		t.Fatalf("expected NeedsRework=true for codex FAIL, got %+v", result)
	}
	if result.ReviewerRejectionCause != "reviewer_rejection" {
		t.Errorf("expected default ReviewerRejectionCause=reviewer_rejection, got %q", result.ReviewerRejectionCause)
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

// TestDoMergePR_DegradedQuorum_MergeFail_NoAuditBead guards gastown-cet.12.6.2:
// when the provider merge fails, the reviewer audit bead must NOT be recorded.
// Recording it before the merge succeeds orphaned an open audit task against a
// failed MR with no merge to audit and no revoke path.
func TestDoMergePR_DegradedQuorum_MergeFail_NoAuditBead(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.MergeStrategy = "pr"
	e.config.RequireReview = boolPtr(true)
	e.config.DegradedQuorumEnabled = boolPtr(true)
	e.config.ReviewQuorumMin = 1

	createFeatureBranch(t, workDir, "feat/degraded-fail", "test.txt", "hello")

	auditCalled := false
	e.recordReviewerAuditBeadFunc = func(mr *MRInfo, ev *ReviewEvaluation) (string, error) {
		auditCalled = true
		return "gt-audit-fake", nil
	}

	e.prProvider = &reviewerMockPRProvider{
		findPRNumber: func(string) (int, error) { return 42, nil },
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
		// Simulate the provider's PR merge API failing.
		mergePR: func(int, string) (string, error) {
			return "", fmt.Errorf("provider merge API: 409 conflict")
		},
	}

	result := e.doMergePR(context.Background(), "feat/degraded-fail", "main", &MRInfo{ID: "gt-test", SourceIssue: "gt-src"})

	if result.Success {
		t.Fatalf("expected merge failure, got success %+v", result)
	}
	if auditCalled {
		t.Error("audit bead must NOT be recorded when the provider merge fails")
	}
	if result.AuditBead != "" {
		t.Errorf("AuditBead must be empty on merge failure, got %q", result.AuditBead)
	}
}

// TestDoMergePR_DegradedQuorum_PushVerifyFail_NoAuditBead guards the second
// failure surface of gastown-cet.12.6.2: the provider merge lands but the
// push-verification (VerifyPushedCommit) fails. The audit bead still must not
// be recorded because the merge was not verified as published.
func TestDoMergePR_DegradedQuorum_PushVerifyFail_NoAuditBead(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.MergeStrategy = "pr"
	e.config.RequireReview = boolPtr(true)
	e.config.DegradedQuorumEnabled = boolPtr(true)
	e.config.ReviewQuorumMin = 1

	createFeatureBranch(t, workDir, "feat/degraded-verify", "test.txt", "hello")

	auditCalled := false
	e.recordReviewerAuditBeadFunc = func(mr *MRInfo, ev *ReviewEvaluation) (string, error) {
		auditCalled = true
		return "gt-audit-fake", nil
	}

	e.prProvider = &reviewerMockPRProvider{
		findPRNumber: func(string) (int, error) { return 42, nil },
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
		// Provider merge "succeeds" (returns a commit SHA) but that SHA is not
		// actually reachable on origin/main, so VerifyPushedCommit rejects it.
		mergePR: func(int, string) (string, error) {
			// Return a commit SHA that is not the origin/main tip.
			return "0123456789abcdef0123456789abcdef01234567", nil
		},
	}

	result := e.doMergePR(context.Background(), "feat/degraded-verify", "main", &MRInfo{ID: "gt-test", SourceIssue: "gt-src"})

	if result.Success {
		t.Fatalf("expected push-verification failure, got success %+v", result)
	}
	if auditCalled {
		t.Error("audit bead must NOT be recorded when push verification fails")
	}
	if result.AuditBead != "" {
		t.Errorf("AuditBead must be empty on push-verify failure, got %q", result.AuditBead)
	}
}

// TestDoMergePR_DegradedQuorum_Success_RecordsAuditBeadAfterMerge guards the
// positive contract of gastown-cet.12.6.2: when the merge DOES succeed, the
// audit bead is recorded AFTER the merge and threaded onto ProcessResult so the
// source-issue closure stamps the audit bead reference.
func TestDoMergePR_DegradedQuorum_Success_RecordsAuditBeadAfterMerge(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.MergeStrategy = "pr"
	e.config.RequireReview = boolPtr(true)
	e.config.DegradedQuorumEnabled = boolPtr(true)
	e.config.ReviewQuorumMin = 1

	createFeatureBranch(t, workDir, "feat/degraded-ok", "test.txt", "hello")

	auditCalled := false
	var auditMR *MRInfo
	e.recordReviewerAuditBeadFunc = func(mr *MRInfo, ev *ReviewEvaluation) (string, error) {
		auditCalled = true
		auditMR = mr
		return "gt-audit-success", nil
	}

	e.prProvider = &reviewerMockPRProvider{
		findPRNumber: func(string) (int, error) { return 42, nil },
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
			if err := g.Checkout("main"); err != nil {
				return "", err
			}
			if err := g.MergeSquash("feat/degraded-ok", "feat: add test.txt"); err != nil {
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

	result := e.doMergePR(context.Background(), "feat/degraded-ok", "main", &MRInfo{ID: "gt-test", SourceIssue: "gt-src"})

	if !result.Success {
		t.Fatalf("expected success under degraded quorum, got %+v", result)
	}
	if !auditCalled {
		t.Error("audit bead must be recorded after a successful degraded-quorum merge")
	}
	if result.AuditBead != "gt-audit-success" {
		t.Errorf("expected AuditBead=gt-audit-success, got %q", result.AuditBead)
	}
	if auditMR == nil || auditMR.ID != "gt-test" {
		t.Errorf("audit bead should be recorded against the MR, got %+v", auditMR)
	}
}
