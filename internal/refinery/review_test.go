package refinery

import (
	"strings"
	"testing"
)

func TestEvaluateReviews_NoResults(t *testing.T) {
	ev := EvaluateReviews(nil, DegradedQuorumRule{Enabled: true})
	if ev.State != ReviewStateNoVerdict {
		t.Errorf("expected NO_VERDICT for nil results, got %s", ev.State)
	}
}

func TestEvaluateReviews_AllPass(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictPass},
		{Reviewer: "bob", Verdict: ReviewerVerdictPass},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{})
	if ev.State != ReviewStatePass {
		t.Errorf("expected PASS, got %s", ev.State)
	}
	if ev.PassCount != 2 {
		t.Errorf("expected pass count 2, got %d", ev.PassCount)
	}
}

func TestEvaluateReviews_FailWithBlockers(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictPass},
		{Reviewer: "bob", Verdict: ReviewerVerdictFail, Blockers: []string{"race condition"}},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{Enabled: true, MinPassReviews: 1})
	if ev.State != ReviewStateFail {
		t.Errorf("expected FAIL, got %s", ev.State)
	}
	if !strings.Contains(ev.Error, "race condition") {
		t.Errorf("expected error to mention blocker, got %s", ev.Error)
	}
}

func TestEvaluateReviews_FailStillRejectsWithDegradedQuorum(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictPass},
		{Reviewer: "bob", Verdict: ReviewerVerdictFail, Blockers: []string{"memory leak"}},
		{Reviewer: "carol", Verdict: ReviewerVerdictUnavailable},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{Enabled: true, MinPassReviews: 1})
	if ev.State != ReviewStateFail {
		t.Errorf("expected FAIL even with degraded quorum, got %s", ev.State)
	}
}

func TestEvaluateReviews_NoVerdictNotFail(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictNoVerdict},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{})
	if ev.State != ReviewStateNoVerdict {
		t.Errorf("expected NO_VERDICT, got %s", ev.State)
	}
	if ev.FailCount != 0 {
		t.Errorf("expected no fails, got %d", ev.FailCount)
	}
}

func TestEvaluateReviews_UnavailableNotFail(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictUnavailable},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{})
	if ev.State != ReviewStateUnavailable {
		t.Errorf("expected UNAVAILABLE, got %s", ev.State)
	}
	if ev.FailCount != 0 {
		t.Errorf("expected no fails, got %d", ev.FailCount)
	}
}

func TestEvaluateReviews_DegradedQuorum(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictPass},
		{Reviewer: "bob", Verdict: ReviewerVerdictNoVerdict},
		{Reviewer: "carol", Verdict: ReviewerVerdictUnavailable},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{Enabled: true, MinPassReviews: 1})
	if ev.State != ReviewStateDegradedQuorum {
		t.Errorf("expected DEGRADED_QUORUM, got %s", ev.State)
	}
	if len(ev.AuditReviewers) != 2 {
		t.Errorf("expected 2 audit reviewers, got %d", len(ev.AuditReviewers))
	}
}

func TestEvaluateReviews_DegradedQuorumNotEnabled(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictPass},
		{Reviewer: "bob", Verdict: ReviewerVerdictNoVerdict},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{Enabled: false})
	if ev.State != ReviewStateNoVerdict {
		t.Errorf("expected blocking NO_VERDICT when disabled, got %s", ev.State)
	}
}

func TestEvaluateReviews_DegradedQuorumInsufficientPass(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictPass},
		{Reviewer: "bob", Verdict: ReviewerVerdictNoVerdict},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{Enabled: true, MinPassReviews: 2})
	if ev.State != ReviewStateNoVerdict {
		t.Errorf("expected NO_VERDICT when pass count below quorum, got %s", ev.State)
	}
}

func TestEvaluateReviews_RequiredReviewerNoVerdictBlocks(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictPass},
		{Reviewer: "bob", Verdict: ReviewerVerdictNoVerdict},
	}
	rule := DegradedQuorumRule{Enabled: true, MinPassReviews: 1, RequiredReviewers: []string{"bob"}}
	ev := EvaluateReviews(results, rule)
	if ev.State != ReviewStateNoVerdict {
		t.Errorf("expected NO_VERDICT when required reviewer is no-verdict, got %s", ev.State)
	}
}

func TestEvaluateReviews_RequiredReviewerUnavailableAudits(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictPass},
		{Reviewer: "bob", Verdict: ReviewerVerdictUnavailable},
	}
	rule := DegradedQuorumRule{Enabled: true, MinPassReviews: 1, RequiredReviewers: []string{"bob"}}
	ev := EvaluateReviews(results, rule)
	if ev.State != ReviewStateDegradedQuorum {
		t.Errorf("expected DEGRADED_QUORUM when required reviewer unavailable, got %s", ev.State)
	}
	if len(ev.AuditReviewers) != 1 || ev.AuditReviewers[0] != "bob" {
		t.Errorf("expected audit reviewer bob, got %v", ev.AuditReviewers)
	}
}

func TestEvaluateReviews_DefaultMinPassReviews(t *testing.T) {
	results := []ReviewerResult{
		{Reviewer: "alice", Verdict: ReviewerVerdictPass},
		{Reviewer: "bob", Verdict: ReviewerVerdictNoVerdict},
	}
	ev := EvaluateReviews(results, DegradedQuorumRule{Enabled: true})
	if ev.State != ReviewStateDegradedQuorum {
		t.Errorf("expected DEGRADED_QUORUM with default min=1, got %s", ev.State)
	}
}
