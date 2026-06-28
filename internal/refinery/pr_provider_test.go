package refinery

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// fakeGitHubGit is a stub of githubGitOps for unit-testing the GitHub
// provider's GetReviewEvaluation without shelling to gh
// (gastown-cet.12.6.3).
type fakeGitHubGit struct {
	reviewsFn       func(prNumber int) ([]git.PRReview, error)
	reviewDecisionFn func(prNumber int) (string, error)
	remoteTipFn     func(remote, branch string) (string, error)
	revFn           func(ref string) (string, error)

	// Track calls for assertions.
	reviewsCalls       int
	reviewDecisionCalls int
}

func (f *fakeGitHubGit) GetPRReviews(prNumber int) ([]git.PRReview, error) {
	f.reviewsCalls++
	if f.reviewsFn != nil {
		return f.reviewsFn(prNumber)
	}
	return nil, nil
}

func (f *fakeGitHubGit) GetPRReviewDecision(prNumber int) (string, error) {
	f.reviewDecisionCalls++
	if f.reviewDecisionFn != nil {
		return f.reviewDecisionFn(prNumber)
	}
	return "", nil
}

func (f *fakeGitHubGit) RemoteBranchTip(remote, branch string) (string, error) {
	if f.remoteTipFn != nil {
		return f.remoteTipFn(remote, branch)
	}
	return "", nil
}

func (f *fakeGitHubGit) Rev(ref string) (string, error) {
	if f.revFn != nil {
		return f.revFn(ref)
	}
	return "", nil
}

func (f *fakeGitHubGit) FindPRNumber(branch string) (int, error) {
	return 0, nil
}

func (f *fakeGitHubGit) IsPRApproved(prNumber int) (bool, error) {
	return false, nil
}

func (f *fakeGitHubGit) GhPrMerge(prNumber int, method string) (string, error) {
	return "", nil
}

// TestGitHubProvider_NetworkErrorMapsToUnavailable covers the network-error
// branch: when GetPRReviews fails (timeout, auth, network), the provider must
// map to ReviewStateUnavailable rather than failing the merge or returning
// NO_VERDICT (gastown-cet.12.6.3).
func TestGitHubProvider_NetworkErrorMapsToUnavailable(t *testing.T) {
	netErr := errors.New("dial tcp: connection refused")
	fake := &fakeGitHubGit{
		reviewsFn: func(int) ([]git.PRReview, error) {
			return nil, netErr
		},
	}
	p := newGitHubPRProviderWithOps(fake).(interface {
		GetReviewEvaluation(int) (*ReviewEvaluation, error)
	})

	ev, err := p.GetReviewEvaluation(42)
	if err != nil {
		t.Fatalf("network errors must be classified in-band, not returned: %v", err)
	}
	if ev.State != ReviewStateUnavailable {
		t.Errorf("expected ReviewStateUnavailable on network error, got %s", ev.State)
	}
	if ev.UnavailableCount != 1 {
		t.Errorf("expected UnavailableCount=1, got %d", ev.UnavailableCount)
	}
	if ev.Results[0].Verdict != ReviewerVerdictUnavailable {
		t.Errorf("expected per-reviewer verdict UNAVAILABLE, got %s", ev.Results[0].Verdict)
	}
	if ev.Results[0].Evidence == "" {
		t.Error("expected evidence to capture the underlying error")
	}
	if ev.Error == "" {
		t.Error("expected top-level Error to capture the failure")
	}
}

// TestGitHubProvider_NoReviewsAndChangesRequestedDecisionCovers the
// branch-protection path: when no individual reviews are returned but the
// overall PR review decision is CHANGES_REQUESTED, the provider must surface a
// hard FAIL rather than NO_VERDICT (gastown-cet.12.6.3).
func TestGitHubProvider_NoReviewsAndChangesRequestedDecision(t *testing.T) {
	fake := &fakeGitHubGit{
		reviewsFn: func(int) ([]git.PRReview, error) {
			return []git.PRReview{}, nil
		},
		reviewDecisionFn: func(int) (string, error) {
			return "CHANGES_REQUESTED", nil
		},
	}
	p := newGitHubPRProviderWithOps(fake).(interface {
		GetReviewEvaluation(int) (*ReviewEvaluation, error)
	})

	ev, err := p.GetReviewEvaluation(42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.State != ReviewStateFail {
		t.Errorf("expected ReviewStateFail for branch-protection CHANGES_REQUESTED, got %s", ev.State)
	}
	if ev.FailCount != 1 {
		t.Errorf("expected FailCount=1, got %d", ev.FailCount)
	}
	if !strings.Contains(ev.Error, "CHANGES_REQUESTED") {
		t.Errorf("expected error to mention CHANGES_REQUESTED, got %q", ev.Error)
	}
	// ReviewDecision must have been consulted exactly once on the empty path.
	if fake.reviewDecisionCalls != 1 {
		t.Errorf("expected GetPRReviewDecision to be called once for empty reviews, got %d", fake.reviewDecisionCalls)
	}
}

// TestGitHubProvider_NoReviewsNoDecisionIsNoVerdict covers the
// neither-individual-reviews-nor-decision case: the provider must return
// NO_VERDICT (not FAIL, not UNAVAILABLE) so the merge can defer cleanly
// (gastown-cet.12.6.3).
func TestGitHubProvider_NoReviewsNoDecisionIsNoVerdict(t *testing.T) {
	fake := &fakeGitHubGit{
		reviewsFn: func(int) ([]git.PRReview, error) {
			return nil, nil
		},
		reviewDecisionFn: func(int) (string, error) {
			return "", nil
		},
	}
	p := newGitHubPRProviderWithOps(fake).(interface {
		GetReviewEvaluation(int) (*ReviewEvaluation, error)
	})

	ev, err := p.GetReviewEvaluation(42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.State != ReviewStateNoVerdict {
		t.Errorf("expected ReviewStateNoVerdict, got %s", ev.State)
	}
	if ev.NoVerdictCount != 1 {
		t.Errorf("expected NoVerdictCount=1, got %d", ev.NoVerdictCount)
	}
}

// TestGitHubProvider_LatestReviewWinsPerReviewer covers the
// multiple-reviews-per-reviewer selection at the provider boundary. The
// provider must call collapseReviews before classify, so a stale
// CHANGES_REQUESTED superseded by APPROVED produces PASS, not FAIL
// (gastown-cet.12.6.3).
func TestGitHubProvider_LatestReviewWinsPerReviewer(t *testing.T) {
	fake := &fakeGitHubGit{
		reviewsFn: func(int) ([]git.PRReview, error) {
			return []git.PRReview{
				{Reviewer: "alice", State: "CHANGES_REQUESTED", SubmittedAt: "2026-06-20T10:00:00Z", Body: "- blocker: race"},
				{Reviewer: "alice", State: "APPROVED", SubmittedAt: "2026-06-21T10:00:00Z", Body: "fixed"},
			}, nil
		},
	}
	p := newGitHubPRProviderWithOps(fake).(interface {
		GetReviewEvaluation(int) (*ReviewEvaluation, error)
	})

	ev, err := p.GetReviewEvaluation(42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.State != ReviewStatePass {
		t.Errorf("expected PASS (latest APPROVED wins per-reviewer), got %s: %s", ev.State, ev.Error)
	}
	if ev.PassCount != 1 {
		t.Errorf("expected PassCount=1, got %d", ev.PassCount)
	}
}

// fakeBitbucketGit is a stub of bitbucketGitOps for unit-testing the
// Bitbucket provider's GetReviewEvaluation without HTTP calls
// (gastown-cet.12.6.3).
type fakeBitbucketGit struct {
	participantsFn func(workspace, repoSlug string, prID int) ([]git.BitbucketParticipant, error)
	remoteTipFn    func(remote, branch string) (string, error)
	revFn          func(ref string) (string, error)

	participantsCalls int
}

func (f *fakeBitbucketGit) GetBitbucketPRParticipants(workspace, repoSlug string, prID int) ([]git.BitbucketParticipant, error) {
	f.participantsCalls++
	if f.participantsFn != nil {
		return f.participantsFn(workspace, repoSlug, prID)
	}
	return nil, nil
}

func (f *fakeBitbucketGit) RemoteBranchTip(remote, branch string) (string, error) {
	if f.remoteTipFn != nil {
		return f.remoteTipFn(remote, branch)
	}
	return "", nil
}

func (f *fakeBitbucketGit) Rev(ref string) (string, error) {
	if f.revFn != nil {
		return f.revFn(ref)
	}
	return "", nil
}

func (f *fakeBitbucketGit) FindBitbucketPRNumber(workspace, repoSlug, branch string) (int, error) {
	return 0, nil
}

func (f *fakeBitbucketGit) IsBitbucketPRApproved(workspace, repoSlug string, prID int) (bool, error) {
	return false, nil
}

func (f *fakeBitbucketGit) BitbucketPRMerge(workspace, repoSlug string, prID int, strategy string) (string, error) {
	return "", nil
}

func (f *fakeBitbucketGit) RemoteURL(remote string) (string, error) {
	return "", nil
}

// TestBitbucketProvider_NetworkErrorMapsToUnavailable confirms Bitbucket
// provider handles HTTP/network failures the same way as GitHub: in-band
// ReviewStateUnavailable, never an out-of-band error
// (gastown-cet.12.6.3).
func TestBitbucketProvider_NetworkErrorMapsToUnavailable(t *testing.T) {
	netErr := errors.New("dial tcp: lookup api.bitbucket.org: no such host")
	fake := &fakeBitbucketGit{
		participantsFn: func(string, string, int) ([]git.BitbucketParticipant, error) {
			return nil, netErr
		},
	}
	p := newBitbucketPRProviderWithOps(fake, "ws", "repo").(*bitbucketPRProvider)

	ev, err := p.GetReviewEvaluation(42)
	if err != nil {
		t.Fatalf("network errors must be classified in-band, not returned: %v", err)
	}
	if ev.State != ReviewStateUnavailable {
		t.Errorf("expected ReviewStateUnavailable, got %s", ev.State)
	}
	if ev.UnavailableCount != 1 {
		t.Errorf("expected UnavailableCount=1, got %d", ev.UnavailableCount)
	}
	if ev.Results[0].Verdict != ReviewerVerdictUnavailable {
		t.Errorf("expected per-reviewer verdict UNAVAILABLE, got %s", ev.Results[0].Verdict)
	}
}

// TestBitbucketProvider_NonReviewerRolesSkipped covers the role-based
// filter: participants with role other than REVIEWER (e.g. PARTICIPANT or
// WATCHER) must not appear in the reviewer results. They are skipped via
// `continue` in the provider so the degraded-quorum and verdict logic
// downstream does not count them (gastown-cet.12.6.3).
func TestBitbucketProvider_NonReviewerRolesSkipped(t *testing.T) {
	fake := &fakeBitbucketGit{
		participantsFn: func(string, string, int) ([]git.BitbucketParticipant, error) {
			return []git.BitbucketParticipant{
				{User: "watcher1", Role: "PARTICIPANT", Approved: false},
				{User: "watcher2", Role: "PARTICIPANT", Approved: true}, // approved but not a reviewer
				{User: "alice", Role: "REVIEWER", Approved: true},
			}, nil
		},
	}
	p := newBitbucketPRProviderWithOps(fake, "ws", "repo").(*bitbucketPRProvider)

	ev, err := p.GetReviewEvaluation(42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Two non-reviewer participants must be filtered out; only alice counts.
	if len(ev.Results) != 1 {
		t.Errorf("expected 1 reviewer result (non-REVIEWER skipped), got %d: %+v", len(ev.Results), ev.Results)
	}
	if len(ev.Results) > 0 && ev.Results[0].Reviewer != "alice" {
		t.Errorf("expected alice as the sole reviewer, got %q", ev.Results[0].Reviewer)
	}
	if ev.State != ReviewStatePass {
		t.Errorf("expected PASS (alice approved), got %s: %s", ev.State, ev.Error)
	}
	if ev.PassCount != 1 {
		t.Errorf("expected PassCount=1, got %d", ev.PassCount)
	}
}

// TestBitbucketProvider_NonApprovingReviewerIsNoVerdict covers the
// Bitbucket-specific classification: the participants API does not expose
// CHANGES_REQUESTED, so a REVIEWER whose Approved=false must be classified
// as NO_VERDICT (deferral), not as FAIL (gastown-cet.12.6.3).
func TestBitbucketProvider_NonApprovingReviewerIsNoVerdict(t *testing.T) {
	fake := &fakeBitbucketGit{
		participantsFn: func(string, string, int) ([]git.BitbucketParticipant, error) {
			return []git.BitbucketParticipant{
				{User: "bob", Role: "REVIEWER", Approved: false},
			}, nil
		},
	}
	p := newBitbucketPRProviderWithOps(fake, "ws", "repo").(*bitbucketPRProvider)

	ev, err := p.GetReviewEvaluation(42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.State != ReviewStateNoVerdict {
		t.Errorf("non-approving REVIEWER must be NO_VERDICT (deferral), got %s", ev.State)
	}
	if ev.NoVerdictCount != 1 {
		t.Errorf("expected NoVerdictCount=1, got %d", ev.NoVerdictCount)
	}
	if ev.FailCount != 0 {
		t.Errorf("non-approving reviewer must NOT count as FAIL, got FailCount=%d", ev.FailCount)
	}
	if len(ev.Results) != 1 || ev.Results[0].Verdict != ReviewerVerdictNoVerdict {
		t.Errorf("expected per-reviewer verdict NO_VERDICT, got %+v", ev.Results)
	}
}

// TestBitbucketProvider_AllNonReviewersProducesNoReviewers covers the
// edge case where every participant is filtered out by role: the provider
// must synthesize a single NO_VERDICT sentinel result rather than handing
// the empty slice to EvaluateReviews (which would also produce NO_VERDICT,
// but the provider's documented error path is the explicit
// "no reviewers" form).
func TestBitbucketProvider_AllNonReviewersProducesNoReviewers(t *testing.T) {
	fake := &fakeBitbucketGit{
		participantsFn: func(string, string, int) ([]git.BitbucketParticipant, error) {
			return []git.BitbucketParticipant{
				{User: "watcher", Role: "PARTICIPANT", Approved: true},
			}, nil
		},
	}
	p := newBitbucketPRProviderWithOps(fake, "ws", "repo").(*bitbucketPRProvider)

	ev, err := p.GetReviewEvaluation(42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.State != ReviewStateNoVerdict {
		t.Errorf("expected NO_VERDICT, got %s", ev.State)
	}
	if !strings.Contains(ev.Error, "no reviewers") {
		t.Errorf("expected sentinel 'no reviewers' error, got %q", ev.Error)
	}
}

// TestBitbucketProvider_EmptyParticipantsIsNoVerdict covers the boundary
// where the participants API returns an empty slice.
func TestBitbucketProvider_EmptyParticipantsIsNoVerdict(t *testing.T) {
	fake := &fakeBitbucketGit{
		participantsFn: func(string, string, int) ([]git.BitbucketParticipant, error) {
			return nil, nil
		},
	}
	p := newBitbucketPRProviderWithOps(fake, "ws", "repo").(*bitbucketPRProvider)

	ev, err := p.GetReviewEvaluation(42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.State != ReviewStateNoVerdict {
		t.Errorf("expected NO_VERDICT for empty participants, got %s", ev.State)
	}
	if !strings.Contains(ev.Error, "no participants") {
		t.Errorf("expected 'no participants' sentinel, got %q", ev.Error)
	}
}