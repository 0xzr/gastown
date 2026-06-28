package refinery

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

// review is a compact constructor for a raw gh-style review used by the
// collapse/classify characterization tests (gastown-cet.12.6.1).
func review(reviewer, state, submittedAt, body string) git.PRReview {
	return git.PRReview{Reviewer: reviewer, State: state, SubmittedAt: submittedAt, Body: body}
}

// evaluateGitHubReviews runs the same collapse + classify + Evaluate path the
// GitHub provider uses, without shelling to gh. It is the characterization
// seam for the final-diff selection fix.
func evaluateGitHubReviews(reviews []git.PRReview) ReviewEvaluation {
	results := classifyCollapsedReviews(collapseReviews(reviews), DiffBasis{})
	return EvaluateReviews(results, DegradedQuorumRule{})
}

// TestCollapseReviews_StaleChangesRequestedDoesNotBlockApproval covers the
// regression at the heart of gastown-cet.12.6.1: a reviewer who requested
// changes and then later approved must not block the merge. Before the fix the
// stale CHANGES_REQUESTED was counted as an independent hard FAIL.
func TestCollapseReviews_StaleChangesRequestedDoesNotBlockApproval(t *testing.T) {
	reviews := []git.PRReview{
		review("alice", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race condition"),
		review("alice", "APPROVED", "2026-06-21T10:00:00Z", "lgtm"),
	}
	ev := evaluateGitHubReviews(reviews)
	if ev.State != ReviewStatePass {
		t.Fatalf("stale CHANGES_REQUESTED superseded by APPROVED must PASS, got %s: %s", ev.State, ev.Error)
	}
	if ev.FailCount != 0 {
		t.Errorf("expected FailCount=0 (stale rejection collapsed away), got %d", ev.FailCount)
	}
	if ev.PassCount != 1 {
		t.Errorf("expected PassCount=1, got %d", ev.PassCount)
	}
}

// TestCollapseReviews_LatestChangesRequestedWins confirms the symmetric case:
// when a reviewer approves and then requests changes, the latest
// CHANGES_REQUESTED is the effective verdict and the merge is blocked.
func TestCollapseReviews_LatestChangesRequestedWins(t *testing.T) {
	reviews := []git.PRReview{
		review("bob", "APPROVED", "2026-06-20T10:00:00Z", "looks fine"),
		review("bob", "CHANGES_REQUESTED", "2026-06-22T10:00:00Z", "- blocker: missing test"),
	}
	ev := evaluateGitHubReviews(reviews)
	if ev.State != ReviewStateFail {
		t.Fatalf("APPROVED then CHANGES_REQUESTED must FAIL on the latest verdict, got %s: %s", ev.State, ev.Error)
	}
	if ev.FailCount != 1 {
		t.Errorf("expected FailCount=1, got %d", ev.FailCount)
	}
}

// TestCollapseReviews_OrderIndependentOfSlicePosition confirms the collapse is
// driven by SubmittedAt, not the order gh happens to return reviews. The same
// history given out-of-order must produce the same verdict.
func TestCollapseReviews_OrderIndependentOfSlicePosition(t *testing.T) {
	chronological := []git.PRReview{
		review("carol", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race"),
		review("carol", "APPROVED", "2026-06-21T10:00:00Z", "fixed"),
	}
	reversed := []git.PRReview{
		review("carol", "APPROVED", "2026-06-21T10:00:00Z", "fixed"),
		review("carol", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race"),
	}
	a := evaluateGitHubReviews(chronological)
	b := evaluateGitHubReviews(reversed)
	if a.State != b.State {
		t.Fatalf("verdict must be order-independent: chronological=%s reversed=%s", a.State, b.State)
	}
	if a.State != ReviewStatePass {
		t.Errorf("expected PASS (latest APPROVED wins), got %s", a.State)
	}
}

// TestCollapseReviews_DismissedClearsPriorApproval covers GitHub's DISMISSED
// state: dismissing a review clears the reviewer's prior decision, so a
// dismissed approval falls back to no-verdict rather than passing.
func TestCollapseReviews_DismissedClearsPriorApproval(t *testing.T) {
	reviews := []git.PRReview{
		review("dave", "APPROVED", "2026-06-20T10:00:00Z", "lgtm"),
		review("dave", "DISMISSED", "2026-06-21T10:00:00Z", ""),
	}
	ev := evaluateGitHubReviews(reviews)
	if ev.State != ReviewStateNoVerdict {
		t.Fatalf("DISMISSED must clear prior approval -> NO_VERDICT, got %s: %s", ev.State, ev.Error)
	}
	if ev.PassCount != 0 {
		t.Errorf("expected PassCount=0 after dismissal, got %d", ev.PassCount)
	}
	if ev.FailCount != 0 {
		t.Errorf("expected FailCount=0, got %d", ev.FailCount)
	}
}

// TestCollapseReviews_DismissedThenApprovedRestoresDecision confirms a
// reviewer can re-establish a decision after one was dismissed.
func TestCollapseReviews_DismissedThenApprovedRestoresDecision(t *testing.T) {
	reviews := []git.PRReview{
		review("erin", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race"),
		review("erin", "DISMISSED", "2026-06-21T10:00:00Z", ""),
		review("erin", "APPROVED", "2026-06-22T10:00:00Z", "all good now"),
	}
	ev := evaluateGitHubReviews(reviews)
	if ev.State != ReviewStatePass {
		t.Fatalf("CHANGES_REQUESTED -> DISMISSED -> APPROVED must PASS, got %s: %s", ev.State, ev.Error)
	}
}

// TestCollapseReviews_CommentedDoesNotOverrideTerminal confirms COMMENTED is a
// non-decision: it must not override a prior APPROVED or CHANGES_REQUESTED.
func TestCollapseReviews_CommentedDoesNotOverrideTerminal(t *testing.T) {
	t.Run("approved_then_commented_still_pass", func(t *testing.T) {
		reviews := []git.PRReview{
			review("frank", "APPROVED", "2026-06-20T10:00:00Z", "lgtm"),
			review("frank", "COMMENTED", "2026-06-21T10:00:00Z", "nit: rename var"),
		}
		ev := evaluateGitHubReviews(reviews)
		if ev.State != ReviewStatePass {
			t.Fatalf("APPROVED then COMMENTED must stay PASS, got %s: %s", ev.State, ev.Error)
		}
	})
	t.Run("changes_then_commented_still_fail", func(t *testing.T) {
		reviews := []git.PRReview{
			review("grace", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: missing test"),
			review("grace", "COMMENTED", "2026-06-21T10:00:00Z", "also see line 4"),
		}
		ev := evaluateGitHubReviews(reviews)
		if ev.State != ReviewStateFail {
			t.Fatalf("CHANGES_REQUESTED then COMMENTED must stay FAIL, got %s: %s", ev.State, ev.Error)
		}
	})
}

// TestCollapseReviews_MultipleReviewersCollapsesPerReviewer confirms each
// reviewer collapses to one result and the overall verdict aggregates across
// distinct reviewers (two approvers -> PASS with PassCount=2).
func TestCollapseReviews_MultipleReviewersCollapsesPerReviewer(t *testing.T) {
	reviews := []git.PRReview{
		review("alice", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race"),
		review("bob", "APPROVED", "2026-06-20T09:00:00Z", "lgtm"),
		review("alice", "APPROVED", "2026-06-21T10:00:00Z", "fixed now"),
		review("bob", "COMMENTED", "2026-06-22T09:00:00Z", "nice"),
	}
	ev := evaluateGitHubReviews(reviews)
	if ev.State != ReviewStatePass {
		t.Fatalf("two approvers (alice reapproved, bob approved) must PASS, got %s: %s", ev.State, ev.Error)
	}
	if ev.PassCount != 2 {
		t.Errorf("expected PassCount=2, got %d", ev.PassCount)
	}
}

// TestCollapseReviews_EachReviewerOnce confirms the collapse emits exactly one
// effective review per reviewer regardless of history length.
func TestCollapseReviews_EachReviewerOnce(t *testing.T) {
	reviews := []git.PRReview{
		review("alice", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race"),
		review("alice", "COMMENTED", "2026-06-20T11:00:00Z", "more"),
		review("alice", "APPROVED", "2026-06-21T10:00:00Z", "ok"),
		review("bob", "APPROVED", "2026-06-20T10:00:00Z", "ok"),
	}
	collapsed := collapseReviews(reviews)
	if len(collapsed) != 2 {
		t.Fatalf("expected 2 collapsed reviews (one per reviewer), got %d: %+v", len(collapsed), collapsed)
	}
	// The effective state for alice must be APPROVED (her latest terminal).
	alice := collapsed[0]
	bob := collapsed[1]
	if alice.Reviewer != "alice" || alice.State != "APPROVED" {
		t.Errorf("alice effective review = %+v, want APPROVED", alice)
	}
	if bob.Reviewer != "bob" || bob.State != "APPROVED" {
		t.Errorf("bob effective review = %+v, want APPROVED", bob)
	}
}

// TestCollapseReviews_NoTimestampFallsBackToSliceOrder confirms that when
// submittedAt is absent (older gh or stripped output), the collapse falls back
// to input order, so the latest-by-position review still wins.
func TestCollapseReviews_NoTimestampFallsBackToSliceOrder(t *testing.T) {
	// No SubmittedAt on any review: position determines "latest".
	reviews := []git.PRReview{
		{Reviewer: "alice", State: "CHANGES_REQUESTED", Body: "- blocker: race"},
		{Reviewer: "alice", State: "APPROVED", Body: "ok"},
	}
	ev := evaluateGitHubReviews(reviews)
	if ev.State != ReviewStatePass {
		t.Fatalf("position-latest APPROVED must win when no timestamps, got %s: %s", ev.State, ev.Error)
	}
}

// TestCollapseReviews_EmptyAndSingle cover the degenerate inputs.
func TestCollapseReviews_EmptyAndSingle(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if got := collapseReviews(nil); got != nil {
			t.Errorf("expected nil for no reviews, got %+v", got)
		}
		ev := evaluateGitHubReviews(nil)
		if ev.State != ReviewStateNoVerdict {
			t.Errorf("expected NO_VERDICT for no reviews, got %s", ev.State)
		}
	})
	t.Run("single_approved", func(t *testing.T) {
		ev := evaluateGitHubReviews([]git.PRReview{review("alice", "APPROVED", "2026-06-20T10:00:00Z", "ok")})
		if ev.State != ReviewStatePass {
			t.Errorf("expected PASS for single approval, got %s", ev.State)
		}
	})
}

// TestCollapseReviews_CaseInsensitiveReviewer confirms reviews by "Alice" and
// "alice" collapse to one reviewer's history (GitHub logins are case-insensitive
// for matching purposes).
func TestCollapseReviews_CaseInsensitiveReviewer(t *testing.T) {
	reviews := []git.PRReview{
		review("Alice", "CHANGES_REQUESTED", "2026-06-20T10:00:00Z", "- blocker: race"),
		review("alice", "APPROVED", "2026-06-21T10:00:00Z", "ok"),
	}
	collapsed := collapseReviews(reviews)
	if len(collapsed) != 1 {
		t.Fatalf("expected Alice/alice collapsed to one reviewer, got %d: %+v", len(collapsed), collapsed)
	}
	ev := evaluateGitHubReviews(reviews)
	if ev.State != ReviewStatePass {
		t.Errorf("expected PASS (case-insensitive collapse), got %s", ev.State)
	}
}

// ---------------------------------------------------------------------------
// mergeCandidateBasis target-branch tests (gastown-cet.12.6.6)
//
// The bug: mergeCandidateBasis hardcoded "origin/main" as the merge target,
// mis-identifying the final merge candidate for PRs targeting release/ or
// other non-default branches. The fix routes the base through the PR's
// actual target branch via a resolver seam. These tests pin the success path
// (non-main target → non-main SHA) and the fail-closed paths (resolver error /
// empty result → error, no fallback to origin/main), using a real temp git
// repo so the RemoteBranchTip lookup exercises actual git refs.
// ---------------------------------------------------------------------------

// initProviderTestRepo creates a fresh git repo with a real bare `origin`
// remote so RemoteBranchTip (which uses `git ls-remote`) returns real SHAs.
// Returns the workdir, the bare remote path, and the HEAD SHA on main.
// Pushes both `main` and the optional release branch so callers can assert
// the basis picks the right remote ref (gastown-cet.12.6.6).
func initProviderTestRepo(t *testing.T, releaseBranch string) (workDir, headSHA, releaseSHA string) {
	t.Helper()
	dir := t.TempDir()
	bareDir := t.TempDir()

	// Initialize bare remote
	cmds := [][]string{
		{"init", "--bare", "--initial-branch=main", bareDir},
	}
	for _, args := range cmds {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Initialize workdir repo
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test User"},
		{"remote", "add", "origin", bareDir},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := writeFileCheck(t, dir, "README.md", "# Test\n"); err != nil {
		t.Fatalf("write README: %v", err)
	}
	for _, args := range [][]string{
		{"add", "."},
		{"commit", "-m", "initial"},
		{"push", "-u", "origin", "main"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	releaseSHA = ""
	if releaseBranch != "" {
		// Create a distinct commit on a release branch so its tip SHA
		// differs from main. ls-remote will then return a different SHA per
		// branch — the basis must pick the right one (gastown-cet.12.6.6).
		// Strategy: branch off the initial main commit, add a release-only
		// commit there, and push — leaving main at the original initial SHA
		// so basis.Head (HEAD-on-main) stays stable across branches.
		if out, err := runGitCmd(dir, "checkout", "-b", releaseBranch); err != nil {
			t.Fatalf("git checkout -b %s: %v\n%s", releaseBranch, err, out)
		}
		if err := writeFileCheck(t, dir, "release.txt", "release content\n"); err != nil {
			t.Fatalf("write release: %v", err)
		}
		for _, args := range [][]string{
			{"add", "release.txt"},
			{"commit", "-m", "release commit"},
			{"push", "-u", "origin", releaseBranch},
		} {
			c := exec.Command("git", args...)
			c.Dir = dir
			if out, err := c.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		// Return to main so the HEAD SHA is the main-branch tip (what the
		// refinery will compare against). The test asserts basis.Head == mainSHA.
		if out, err := runGitCmd(dir, "checkout", "main"); err != nil {
			t.Fatalf("git checkout main: %v\n%s", err, out)
		}
		releaseSHA = strings.TrimSpace(string(mustGitOutput(t, dir, "rev-parse", releaseBranch)))
	}
	// Capture mainSHA LAST so the value always reflects the post-checkout
	// HEAD on main (gastown-cet.12.6.6). When a release branch was created,
	// this still points at the initial commit because the release commit was
	// added on a separate branch without advancing main.
	mainSHA := strings.TrimSpace(string(mustGitOutput(t, dir, "rev-parse", "HEAD")))
	if releaseBranch != "" && releaseSHA == mainSHA {
		t.Fatalf("release SHA equals main SHA — distinct commits required")
	}
	return dir, mainSHA, releaseSHA
}

// setRemoteRef plants refs/remotes/origin/<branch> = sha so RemoteBranchTip
// returns that SHA without needing a real network fetch.
func setRemoteRef(t *testing.T, workDir, branch, sha string) {
	t.Helper()
	c := exec.Command("git", "update-ref", "refs/remotes/origin/"+branch, sha)
	c.Dir = workDir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git update-ref origin/%s: %v\n%s", branch, err, out)
	}
}

// mustGitOutput runs `git <args>` in dir and returns stdout. Failures
// abort the test. Used by initProviderTestRepo (gastown-cet.12.6.6 tests).
func mustGitOutput(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

// runGitCmd runs `git <args>` in dir and returns combined output + err.
// Does not abort; callers decide how to handle failures
// (gastown-cet.12.6.6 tests).
func runGitCmd(dir string, args ...string) ([]byte, error) {
	c := exec.Command("git", args...)
	c.Dir = dir
	return c.CombinedOutput()
}

// writeFileCheck is the writeFile variant that returns an error rather than
// calling t.Fatal directly, used by initProviderTestRepo so the helper can
// report failures with surrounding context (gastown-cet.12.6.6 tests).
func writeFileCheck(t *testing.T, dir, name, body string) error {
	t.Helper()
	return os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
}

// fakeBaseResolver returns a canned target branch (or an error) so tests can
// drive mergeCandidateBasis without invoking gh. Tests assert that the
// resolved branch is what the basis is built from, not the hardcoded main.
type fakeBaseResolver struct {
	branch string
	err    error
}

func (f *fakeBaseResolver) resolve(int) (string, error) { return f.branch, f.err }

// fakeBitbucketBaseResolver is the Bitbucket-side counterpart of
// fakeBaseResolver: it returns a canned destination branch (or an error) so
// bitbucket provider tests can drive mergeCandidateBasis without shelling out.
type fakeBitbucketBaseResolver struct {
	branch string
	err    error
}

func (f *fakeBitbucketBaseResolver) resolve(_, _ string, _ int) (string, error) {
	return f.branch, f.err
}

// TestGithubMergeCandidateBasis_NonMainTarget is the central regression test
// for gastown-cet.12.6.6: a PR whose target branch is NOT main must produce
// a basis whose Base is the SHA of origin/<target>, not the SHA of
// origin/main. The test creates distinct commits on each branch and pushes
// them to a real bare remote so RemoteBranchTip returns distinct SHAs.
func TestGithubMergeCandidateBasis_NonMainTarget(t *testing.T) {
	workDir, headSHA, releaseSHA := initProviderTestRepo(t, "release/2026-07")

	g := git.NewGit(workDir)
	p := &githubPRProvider{
		git:           g,
		resolveTarget: (&fakeBaseResolver{branch: "release/2026-07"}).resolve,
	}

	basis, err := p.mergeCandidateBasis(42)
	if err != nil {
		t.Fatalf("mergeCandidateBasis: %v", err)
	}
	if !basis.IsMergeCandidate() {
		t.Fatalf("basis must be merge_candidate, got kind=%q", basis.Kind)
	}
	if basis.Base != releaseSHA {
		t.Errorf("basis.Base=%q, want %q (origin/release/2026-07 SHA); the bug was hardcoding origin/main", basis.Base, releaseSHA)
	}
	if basis.Base == headSHA {
		t.Errorf("basis.Base must NOT be origin/main SHA %q when target is release/2026-07", headSHA)
	}
	if basis.Head != headSHA {
		t.Errorf("basis.Head=%q, want %q (HEAD)", basis.Head, headSHA)
	}
}

// TestGithubMergeCandidateBasis_MainTargetKeepsMain ensures the fix is
// non-regressing for the common case: a PR targeting main still produces a
// basis whose Base is the SHA of origin/main.
func TestGithubMergeCandidateBasis_MainTargetKeepsMain(t *testing.T) {
	workDir, headSHA, _ := initProviderTestRepo(t, "")

	g := git.NewGit(workDir)
	p := &githubPRProvider{
		git:           g,
		resolveTarget: (&fakeBaseResolver{branch: "main"}).resolve,
	}

	basis, err := p.mergeCandidateBasis(42)
	if err != nil {
		t.Fatalf("mergeCandidateBasis: %v", err)
	}
	if basis.Base != headSHA {
		t.Errorf("basis.Base=%q, want %q (origin/main SHA)", basis.Base, headSHA)
	}
}

// TestGithubMergeCandidateBasis_ResolverErrorIsUnavailable pins the
// fail-closed behavior: when the provider cannot resolve the PR's target
// (gh unavailable, auth failure, timeout), mergeCandidateBasis returns an
// error and must not silently fall back to origin/main. A transient resolver
// failure must not produce an authoritative PASS basis for a PR that actually
// targets a different branch (gastown-cet.12.6.6).
func TestGithubMergeCandidateBasis_ResolverErrorIsUnavailable(t *testing.T) {
	workDir, headSHA, _ := initProviderTestRepo(t, "")

	g := git.NewGit(workDir)
	p := &githubPRProvider{
		git:           g,
		resolveTarget: (&fakeBaseResolver{err: fmt.Errorf("gh unavailable")}).resolve,
	}

	basis, err := p.mergeCandidateBasis(42)
	if err == nil {
		t.Fatalf("expected error on resolver failure, got basis=%+v", basis)
	}
	if basis.Base == headSHA {
		t.Errorf("basis must not fall back to origin/main SHA %q on resolver error", headSHA)
	}
	if basis.Base != "" {
		t.Errorf("basis.Base=%q, want empty on resolver error", basis.Base)
	}
}

// TestGithubMergeCandidateBasis_EmptyTargetIsUnavailable covers the case
// where the provider call succeeds but returns no branch (e.g., an oddly
// configured PR with no baseRefName). Without an authoritative target the
// basis must fail closed and must not fall back to origin/main
// (gastown-cet.12.6.6).
func TestGithubMergeCandidateBasis_EmptyTargetIsUnavailable(t *testing.T) {
	workDir, headSHA, _ := initProviderTestRepo(t, "")

	g := git.NewGit(workDir)
	p := &githubPRProvider{
		git:           g,
		resolveTarget: (&fakeBaseResolver{branch: ""}).resolve,
	}

	basis, err := p.mergeCandidateBasis(42)
	if err == nil {
		t.Fatalf("expected error on empty target, got basis=%+v", basis)
	}
	if basis.Base == headSHA {
		t.Errorf("basis must not fall back to origin/main SHA %q on empty target", headSHA)
	}
	if basis.Base != "" {
		t.Errorf("basis.Base=%q, want empty on empty target", basis.Base)
	}
}

// TestBitbucketMergeCandidateBasis_NonMainTarget is the bitbucket-side
// mirror of TestGithubMergeCandidateBasis_NonMainTarget: a PR whose
// destination branch is NOT main must produce a basis whose Base is the SHA
// of origin/<destination>, not the SHA of origin/main.
func TestBitbucketMergeCandidateBasis_NonMainTarget(t *testing.T) {
	workDir, headSHA, releaseSHA := initProviderTestRepo(t, "release/2026-07")

	g := git.NewGit(workDir)
	p := &bitbucketPRProvider{
		git:           g,
		workspace:     "ws",
		repoSlug:      "repo",
		resolveTarget: (&fakeBitbucketBaseResolver{branch: "release/2026-07"}).resolve,
	}

	basis, err := p.mergeCandidateBasis(99)
	if err != nil {
		t.Fatalf("mergeCandidateBasis: %v", err)
	}
	if basis.Base != releaseSHA {
		t.Errorf("basis.Base=%q, want %q (origin/release/2026-07 SHA); the bug was hardcoding origin/main", basis.Base, releaseSHA)
	}
	if basis.Base == headSHA {
		t.Errorf("basis.Base must NOT be origin/main SHA %q when destination is release/2026-07", headSHA)
	}
}

// TestBitbucketMergeCandidateBasis_ResolverErrorIsUnavailable ensures a
// Bitbucket resolver error does not silently fall back to origin/main and
// cannot produce an authoritative PASS basis (gastown-cet.12.6.6).
func TestBitbucketMergeCandidateBasis_ResolverErrorIsUnavailable(t *testing.T) {
	workDir, headSHA, _ := initProviderTestRepo(t, "")

	g := git.NewGit(workDir)
	p := &bitbucketPRProvider{
		git:           g,
		workspace:     "ws",
		repoSlug:      "repo",
		resolveTarget: (&fakeBitbucketBaseResolver{err: fmt.Errorf("bitbucket API unavailable")}).resolve,
	}

	basis, err := p.mergeCandidateBasis(99)
	if err == nil {
		t.Fatalf("expected error on resolver failure, got basis=%+v", basis)
	}
	if basis.Base == headSHA {
		t.Errorf("basis must not fall back to origin/main SHA %q on resolver error", headSHA)
	}
	if basis.Base != "" {
		t.Errorf("basis.Base=%q, want empty on resolver error", basis.Base)
	}
}

// TestBitbucketMergeCandidateBasis_EmptyTargetIsUnavailable ensures an empty
// Bitbucket destination result does not silently fall back to origin/main and
// cannot produce an authoritative PASS basis (gastown-cet.12.6.6).
func TestBitbucketMergeCandidateBasis_EmptyTargetIsUnavailable(t *testing.T) {
	workDir, headSHA, _ := initProviderTestRepo(t, "")

	g := git.NewGit(workDir)
	p := &bitbucketPRProvider{
		git:           g,
		workspace:     "ws",
		repoSlug:      "repo",
		resolveTarget: (&fakeBitbucketBaseResolver{branch: ""}).resolve,
	}

	basis, err := p.mergeCandidateBasis(99)
	if err == nil {
		t.Fatalf("expected error on empty target, got basis=%+v", basis)
	}
	if basis.Base == headSHA {
		t.Errorf("basis must not fall back to origin/main SHA %q on empty target", headSHA)
	}
	if basis.Base != "" {
		t.Errorf("basis.Base=%q, want empty on empty target", basis.Base)
	}
}
