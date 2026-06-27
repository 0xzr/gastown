package refinery

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// TestReworkBounceReason_HyphenatedCapMarkersClassifyAsHold pins the
// hyphen-normalization fix from gastown-p3w Codex finding #1: documented
// hyphenated tooling-failure markers (no-verdict, reviewer-unavailable,
// cap-deferral) and their underscore / plural variants must all classify
// as REVIEW_UNAVAILABLE_HOLD so they do not fall through into
// NEEDS_REWORK_PEER_REVIEW and create bogus source-code rework packets.
func TestReworkBounceReason_HyphenatedCapMarkersClassifyAsHold(t *testing.T) {
	mr := &MRInfo{ID: "mr-xw8-test", Branch: "feat/p3w-hyphen"}
	cases := []struct {
		name   string
		cause  string
		errMsg string
	}{
		{"hyphenated-no-verdict", "reviewer_rejection", "PR #1 reviewer rejection: codex returned no-verdict on diff"},
		{"hyphenated-reviewer-unavailable", "reviewer_unavailable", "PR #2 reviewer rejection: reviewer-unavailable; quorum not met"},
		{"hyphenated-cap-deferral", "cap_deferral", "PR #3 cap-deferral: per-rig review cap reached"},
		{"hyphenated-insufficient-quorum", "reviewer_rejection", "PR #4 insufficient-quorum: only 1 reviewer available"},
		{"underscore-no_verdict", "no_verdict", "PR #5 no_verdict returned"},
		{"plural-reviewers-unavailable", "reviewer_rejection", "PR #6 reviewers-unavailable across pool"},
		{"underscore-reviewer_unavailable", "reviewer_unavailable", "PR #7 reviewer_unavailable"},
		{"underscore-cap_deferral", "cap_deferral", "PR #8 cap_deferral policy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			class, reason := reworkBounceReason(mr, tc.cause, tc.errMsg, false)
			if class != reworkRouteReviewerHold {
				t.Errorf("class=%q want %q (reason=%q)", class, reworkRouteReviewerHold, reason)
			}
			if !strings.HasPrefix(reason, "REVIEW_UNAVAILABLE_HOLD:") {
				t.Errorf("reason must start with REVIEW_UNAVAILABLE_HOLD:, got %q", reason)
			}
		})
	}
}

// TestReworkBounceReason_NeedsReworkFallbackClassifiesAsPeerReview pins
// the gastown-p3w xw8 rejection fix: when the caller's NeedsRework flag
// is set (the field contract means a reviewer explicitly rejected with
// concrete blockers), the case must classify as NEEDS_REWORK_PEER_REVIEW
// even when the cause/errMsg text does not contain a documented
// peer-review marker. Historically, reviewer CauseKey values like
// "race_condition", "missing_test", and "api_break" would fall through
// to REWORK_ROUTE_AMBIGUOUS and misuse Mayor's attention for a routine
// substantive peer-review rejection.
func TestReworkBounceReason_NeedsReworkFallbackClassifiesAsPeerReview(t *testing.T) {
	mr := &MRInfo{ID: "mr-xw8-needsrework", Branch: "feat/p3w-causekey"}
	cases := []struct {
		name   string
		cause  string
		errMsg string
	}{
		{"race_condition_causekey", "race_condition", "PR #1 reviewer rejection: race condition"},
		{"missing_test_causekey", "missing_test", "PR #2 reviewer rejection: missing test"},
		{"api_break_causekey", "api_break", "PR #3 reviewer rejection: api break"},
		{"empty_diff_degenerate_pass", "empty_diff_degenerate_pass", "PR #4 reviewer rejection: degenerate pass"},
		{"empty_error_with_needsrework", "reviewer_rejection", "reviewer returned fail"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			class, reason := reworkBounceReason(mr, tc.cause, tc.errMsg, true)
			if class != reworkRouteNeedsRework {
				t.Errorf("class=%q want %q (reason=%q)", class, reworkRouteNeedsRework, reason)
			}
			if !strings.HasPrefix(reason, "NEEDS_REWORK_PEER_REVIEW:") {
				t.Errorf("reason must start with NEEDS_REWORK_PEER_REVIEW:, got %q", reason)
			}
		})
	}
}

// TestReworkBounceReason_CapMarkersBeatNeedsRework verifies that even
// when the caller asserts isPeerReviewRejection, an explicit cap marker
// in the cause/errMsg still routes to REVIEW_UNAVAILABLE_HOLD -- the cap
// signal is the dominant signal in the haystack because tooling
// failures are not source-code rework.
func TestReworkBounceReason_CapMarkersBeatNeedsRework(t *testing.T) {
	mr := &MRInfo{ID: "mr-xw8-capwins", Branch: "feat/p3w-capwins"}
	// Cause key is "race_condition" (would normally be peer-review) but the
	// errMsg contains "reviewer-unavailable" which is a cap marker.
	class, reason := reworkBounceReason(mr, "race_condition",
		"PR #1 reviewer-unavailable; reviewers-unavailable across pool", true)
	if class != reworkRouteReviewerHold {
		t.Errorf("class=%q want %q (cap marker must dominate peer-review signal)", class, reworkRouteReviewerHold)
	}
	if !strings.HasPrefix(reason, "REVIEW_UNAVAILABLE_HOLD:") {
		t.Errorf("reason must start with REVIEW_UNAVAILABLE_HOLD:, got %q", reason)
	}
}

// TestReworkBounceReason_PeerReviewMarkersClassifyRoutine covers the
// happy-path text classifiers (matches the historical "codex failed",
// "blockers:", "verdict:fail" patterns) so a substantive peer-review
// rejection with the documented markers routes to NEEDS_REWORK_PEER_REVIEW.
func TestReworkBounceReason_PeerReviewMarkersClassifyRoutine(t *testing.T) {
	mr := &MRInfo{ID: "mr-xw8-routine", Branch: "feat/p3w-routine"}
	cases := []struct {
		name   string
		cause  string
		errMsg string
	}{
		{"codex-failed", "codex", "PR #1 codex failed: missing test"},
		{"m3-failed", "m3", "PR #2 m3 failed: race condition"},
		{"verdict-fail-blockers", "codex", "PR #3 verdict=fail concrete blockers: race condition"},
		{"umans-kimi-failed", "umans-kimi", "PR #4 umans-kimi failed"},
		{"peer-fail-text", "codex", "PR #5 peer-fail concrete blockers: race condition"},
		{"return-fail", "codex", "PR #6 codex return fail"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			class, reason := reworkBounceReason(mr, tc.cause, tc.errMsg, false)
			if class != reworkRouteNeedsRework {
				t.Errorf("class=%q want %q (reason=%q)", class, reworkRouteNeedsRework, reason)
			}
			if !strings.HasPrefix(reason, "NEEDS_REWORK_PEER_REVIEW:") {
				t.Errorf("reason must start with NEEDS_REWORK_PEER_REVIEW:, got %q", reason)
			}
		})
	}
}

// TestReworkBounceReason_AmbiguousWhenNeitherMatched covers the
// nothing-matched case: no cap marker, no peer-review marker, and the
// caller did not assert NeedsRework. The case must classify as
// REWORK_ROUTE_AMBIGUOUS so the caller escalates to Mayor for human
// classification.
func TestReworkBounceReason_AmbiguousWhenNeitherMatched(t *testing.T) {
	mr := &MRInfo{ID: "mr-xw8-ambiguous", Branch: "feat/p3w-ambiguous"}
	class, reason := reworkBounceReason(mr, "reviewer_rejection",
		"PR #1 reviewer rejection: something we cannot classify", false)
	if class != reworkRouteAmbiguous {
		t.Errorf("class=%q want %q (reason=%q)", class, reworkRouteAmbiguous, reason)
	}
	if !strings.HasPrefix(reason, "REWORK_ROUTE_AMBIGUOUS:") {
		t.Errorf("reason must start with REWORK_ROUTE_AMBIGUOUS:, got %q", reason)
	}
}

// TestRouteRejectionToReworkBounce_NoNotifyFlag pins the gastown-p3w xw8
// rejection fix: the rework-bounce shell-out must invoke `gt mq reject`
// WITHOUT --notify. Manager.notifyWorkerRejected (manager.go:660) sends
// a hardcoded "revise and resubmit" nudge that contradicts the
// classification-aware nudge emitted by handleReviewerRejection, so the
// flag must not be present.
func TestRouteRejectionToReworkBounce_NoNotifyFlag(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.workDir = workDir
	e.output = &bytes.Buffer{}
	t.Setenv("GT_MQ_REWORK_ROUTER", "shadow")

	var capturedArgs []string
	e.routeRejectionExec = func(ctx context.Context, args ...string) error {
		capturedArgs = args
		return nil
	}
	e.reworkRouterTimeout = 1 // 1ns so a test failure from SetProcessGroup surfaces fast

	mr := &MRInfo{ID: "mr-xw8-notify", Branch: "feat/p3w-notify", SourceIssue: "gt-p3w", Worker: "polecats/quartz"}

	class, routed := e.routeRejectionToReworkBounce(mr, "race_condition",
		"PR #1 reviewer rejection: race condition", true)

	if class != reworkRouteNeedsRework {
		t.Errorf("class=%q want %q", class, reworkRouteNeedsRework)
	}
	if !routed {
		t.Errorf("routed=false, want true for routine NEEDS_REWORK")
	}
	// args layout: ["mq", "reject", "<rig>", "<mrID>", "--reason", "<text>"]
	if len(capturedArgs) < 6 {
		t.Fatalf("capturedArgs too short: %v", capturedArgs)
	}
	if capturedArgs[0] != "mq" || capturedArgs[1] != "reject" {
		t.Errorf("expected first args mq/reject, got %v", capturedArgs[:2])
	}
	if capturedArgs[2] != "test-rig" {
		t.Errorf("expected rig name test-rig, got %q", capturedArgs[2])
	}
	if capturedArgs[3] != "mr-xw8-notify" {
		t.Errorf("expected mr id mr-xw8-notify, got %q", capturedArgs[3])
	}
	if capturedArgs[4] != "--reason" {
		t.Errorf("expected --reason flag at position 4, got %q", capturedArgs[4])
	}
	// CRITICAL: --notify must NOT be present. Manager.notifyWorkerRejected
	// would send a hardcoded "resubmit" nudge ahead of the
	// classification-aware nudge emitted by handleReviewerRejection.
	for _, a := range capturedArgs {
		if a == "--notify" {
			t.Errorf("capturedArgs must NOT contain --notify (would contradict classification-aware nudge), got %v", capturedArgs)
		}
	}
}

// TestRouteRejectionToReworkBounce_AmbiguousDoesNotRoute covers the
// ambiguous classification: the router may still shell out (router may
// override the classification) but routed=false signals the caller to
// escalate to Mayor.
func TestRouteRejectionToReworkBounce_AmbiguousDoesNotRoute(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.workDir = workDir
	e.output = &bytes.Buffer{}
	t.Setenv("GT_MQ_REWORK_ROUTER", "shadow")

	var execCalled bool
	e.routeRejectionExec = func(ctx context.Context, args ...string) error {
		execCalled = true
		return nil
	}
	e.reworkRouterTimeout = 1

	mr := &MRInfo{ID: "mr-xw8-ambig", Branch: "feat/p3w-ambig", SourceIssue: "gt-p3w", Worker: "polecats/quartz"}

	// "reviewer_rejection" cause with a generic message that matches no
	// markers and NeedsRework=false => REWORK_ROUTE_AMBIGUOUS.
	class, routed := e.routeRejectionToReworkBounce(mr, "reviewer_rejection",
		"PR #1 reviewer rejection: something we cannot classify", false)

	if class != reworkRouteAmbiguous {
		t.Errorf("class=%q want %q", class, reworkRouteAmbiguous)
	}
	if routed {
		t.Errorf("routed=true, want false for AMBIGUOUS classification")
	}
	if !execCalled {
		t.Errorf("router shell-out should still fire (router may override), got execCalled=false")
	}
}

// TestRouteRejectionToReworkBounce_ReviewerUnavailableRoutesSeparately
// pins that a cap-only case produces a REVIEW_UNAVAILABLE_HOLD
// classification so the router writes a HOLD reason (not a source-rework
// reason). routed=true because the bounded HOLD path is actionable.
func TestRouteRejectionToReworkBounce_ReviewerUnavailableRoutesSeparately(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.workDir = workDir
	e.output = &bytes.Buffer{}
	t.Setenv("GT_MQ_REWORK_ROUTER", "shadow")

	var capturedArgs []string
	e.routeRejectionExec = func(ctx context.Context, args ...string) error {
		capturedArgs = args
		return nil
	}
	e.reworkRouterTimeout = 1

	mr := &MRInfo{ID: "mr-xw8-hold", Branch: "feat/p3w-hold", SourceIssue: "gt-p3w", Worker: "polecats/quartz"}

	class, routed := e.routeRejectionToReworkBounce(mr, "reviewer_unavailable",
		"PR #1 reviewer-unavailable; quorum not met", false)

	if class != reworkRouteReviewerHold {
		t.Errorf("class=%q want %q", class, reworkRouteReviewerHold)
	}
	if !routed {
		t.Errorf("routed=false, want true for HOLD classification (router can still write a HOLD packet)")
	}
	// The --reason text must start with REVIEW_UNAVAILABLE_HOLD: so the
	// router's classifier routes it as a hold, not a source-code rework.
	reasonIdx := -1
	for i, a := range capturedArgs {
		if a == "--reason" && i+1 < len(capturedArgs) {
			reasonIdx = i + 1
			break
		}
	}
	if reasonIdx < 0 {
		t.Fatalf("--reason flag not found in capturedArgs: %v", capturedArgs)
	}
	if !strings.HasPrefix(capturedArgs[reasonIdx], "REVIEW_UNAVAILABLE_HOLD:") {
		t.Errorf("reason must start with REVIEW_UNAVAILABLE_HOLD:, got %q", capturedArgs[reasonIdx])
	}
}

// TestHandleReviewerRejection_NudgeMessageBranchesByClassification pins
// the worker-nudge wording for each classification. The wording is the
// single source of truth for what the worker should do next. To drive
// each classification through handleReviewerRejection, we set up the
// rework-bounce router env + exec seam and choose cause/errMsg text that
// produces the desired classification.
func TestHandleReviewerRejection_NudgeMessageBranchesByClassification(t *testing.T) {
	cases := []struct {
		name            string
		cause           string
		errMsg          string
		needsRework     bool
		wantClass       string
		wantContains    string
		wantNotContains []string
		wantMayorNudged bool
	}{
		{
			name:            "routine-needs-rework",
			cause:           "race_condition",
			errMsg:          "PR #1 reviewer rejection: race condition",
			needsRework:     true,
			wantClass:       reworkRouteNeedsRework,
			wantContains:    "revise and resubmit with 'gt done'",
			wantNotContains: []string{"do NOT resubmit"},
			wantMayorNudged: false,
		},
		{
			name:            "reviewer-unavailable-hold",
			cause:           "reviewer_unavailable",
			errMsg:          "PR #1 reviewer-unavailable; reviewers-unavailable across pool",
			needsRework:     false,
			wantClass:       reworkRouteReviewerHold,
			wantContains:    "do NOT resubmit until reviewer availability changes",
			wantNotContains: []string{"revise and resubmit with 'gt done'"},
			wantMayorNudged: false,
		},
		{
			name:            "ambiguous-defer-to-mayor",
			cause:           "reviewer_rejection",
			errMsg:          "PR #1 something we cannot classify",
			needsRework:     false,
			wantClass:       reworkRouteAmbiguous,
			wantContains:    "Hold resubmit",
			wantNotContains: []string{"revise and resubmit with 'gt done'"},
			wantMayorNudged: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workDir, g, _ := testGitRepo(t)
			e := newTestEngineer(t, workDir, g)
			e.workDir = workDir
			e.output = &bytes.Buffer{}

			// Capture worker nudge.
			var workerMsg string
			var workerTarget string
			e.reviewerRejectionWorkerNudge = func(target, msg string) error {
				workerTarget = target
				workerMsg = msg
				return nil
			}
			// Capture mayor nudge.
			mayorNudged := false
			e.reviewerRejectionMayorNudge = func(msg string) error {
				mayorNudged = true
				return nil
			}
			// Enable the rework-bounce router so the classification path runs.
			t.Setenv("GT_MQ_REWORK_ROUTER", "shadow")
			e.routeRejectionExec = func(ctx context.Context, args ...string) error { return nil }
			e.reworkRouterTimeout = 1

			mr := &MRInfo{
				ID:          "mr-xw8-nudge",
				Branch:      "feat/p3w-nudge",
				SourceIssue: "gt-p3w",
				Worker:      "polecats/quartz",
			}

			result := ProcessResult{
				NeedsRework:            tc.needsRework,
				ReviewerRejectionCause: tc.cause,
				Error:                  tc.errMsg,
			}

			e.handleReviewerRejection(mr, result)

			if !strings.Contains(workerMsg, tc.wantContains) {
				t.Errorf("worker nudge missing %q, got: %q", tc.wantContains, workerMsg)
			}
			for _, ban := range tc.wantNotContains {
				if strings.Contains(workerMsg, ban) {
					t.Errorf("worker nudge must not contain %q, got: %q", ban, workerMsg)
				}
			}
			if workerTarget != "test-rig/quartz" {
				t.Errorf("worker target=%q, want test-rig/quartz", workerTarget)
			}
			if mayorNudged != tc.wantMayorNudged {
				t.Errorf("mayorNudged=%v want %v (routeClass=%s)", mayorNudged, tc.wantMayorNudged, tc.wantClass)
			}
		})
	}
}

// TestHandleReviewerRejection_RoutineReworkRoutesWithoutMayorNudge pins
// the end-to-end routine case: NEEDS_REWORK_PEER_REWORK classification
// triggers the rework-bounce shell-out, the worker is nudged with the
// revise-and-resubmit guidance, and Mayor is NOT escalated.
func TestHandleReviewerRejection_RoutineReworkRoutesWithoutMayorNudge(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.workDir = workDir
	e.output = &bytes.Buffer{}
	t.Setenv("GT_MQ_REWORK_ROUTER", "shadow")

	var workerMsg string
	e.reviewerRejectionWorkerNudge = func(target, msg string) error {
		workerMsg = msg
		return nil
	}
	mayorNudged := false
	e.reviewerRejectionMayorNudge = func(msg string) error {
		mayorNudged = true
		return nil
	}
	e.routeRejectionExec = func(ctx context.Context, args ...string) error { return nil }
	e.reworkRouterTimeout = 1

	mr := &MRInfo{
		ID:          "mr-xw8-routine",
		Branch:      "feat/p3w-routine",
		SourceIssue: "gt-p3w",
		Worker:      "polecats/quartz",
	}

	result := ProcessResult{
		NeedsRework:            true,
		ReviewerRejectionCause: "race_condition",
		Error:                  "PR #1 reviewer rejection: race condition",
	}

	e.handleReviewerRejection(mr, result)

	if !strings.Contains(workerMsg, "revise and resubmit with 'gt done'") {
		t.Errorf("worker nudge must contain revise-and-resubmit guidance, got: %q", workerMsg)
	}
	if mayorNudged {
		t.Errorf("routine NEEDS_REWORK must NOT escalate to Mayor (router handles it)")
	}
}

// TestHandleReviewerRejection_RouterFailureEscalatesMayor covers the
// shell-out failure path: when the router exec returns an error, the
// rejection must still be classified and the Mayor escalated.
func TestHandleReviewerRejection_RouterFailureEscalatesMayor(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.workDir = workDir
	e.output = &bytes.Buffer{}
	t.Setenv("GT_MQ_REWORK_ROUTER", "shadow")

	e.reviewerRejectionWorkerNudge = func(target, msg string) error { return nil }
	mayorNudged := false
	var mayorMsg string
	e.reviewerRejectionMayorNudge = func(msg string) error {
		mayorNudged = true
		mayorMsg = msg
		return nil
	}
	e.routeRejectionExec = func(ctx context.Context, args ...string) error {
		return errors.New("simulated router failure")
	}
	e.reworkRouterTimeout = 1

	mr := &MRInfo{
		ID:          "mr-xw8-routerfail",
		Branch:      "feat/p3w-routerfail",
		SourceIssue: "gt-p3w",
		Worker:      "polecats/quartz",
	}

	result := ProcessResult{
		NeedsRework:            true,
		ReviewerRejectionCause: "race_condition",
		Error:                  "PR #1 reviewer rejection: race condition",
	}

	e.handleReviewerRejection(mr, result)

	if !mayorNudged {
		t.Errorf("router failure must escalate to Mayor")
	}
	if !strings.Contains(mayorMsg, "routed=false") {
		t.Errorf("mayor nudge must indicate routed=false, got: %q", mayorMsg)
	}
}

// TestHandleReviewerRejection_NoRouterNoMayorNudgeForRoutine covers the
// case where GT_MQ_REWORK_ROUTER is unset (legacy rigs that have not
// opted in): the rework-bounce shell-out is a no-op, classification is
// empty, the worker gets the routine revise-and-resubmit nudge, and
// Mayor IS nudged (because routed=false, even though this is a routine
// case, we preserve the legacy Mayor escalation).
func TestHandleReviewerRejection_NoRouterNoMayorNudgeForRoutine(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.workDir = workDir
	e.output = &bytes.Buffer{}
	t.Setenv("GT_MQ_REWORK_ROUTER", "")

	var workerMsg string
	e.reviewerRejectionWorkerNudge = func(target, msg string) error {
		workerMsg = msg
		return nil
	}
	mayorNudged := false
	e.reviewerRejectionMayorNudge = func(msg string) error {
		mayorNudged = true
		return nil
	}

	mr := &MRInfo{
		ID:          "mr-xw8-norouter",
		Branch:      "feat/p3w-norouter",
		SourceIssue: "gt-p3w",
		Worker:      "polecats/quartz",
	}

	result := ProcessResult{
		NeedsRework:            true,
		ReviewerRejectionCause: "race_condition",
		Error:                  "PR #1 reviewer rejection: race condition",
	}

	e.handleReviewerRejection(mr, result)

	if !strings.Contains(workerMsg, "revise and resubmit with 'gt done'") {
		t.Errorf("worker nudge must contain revise-and-resubmit guidance, got: %q", workerMsg)
	}
	if !mayorNudged {
		t.Errorf("legacy rigs (no router) must still escalate to Mayor")
	}
}

// TestMatchAnyMarker sanity-checks the substring marker helper that the
// other tests rely on.
func TestMatchAnyMarker(t *testing.T) {
	if !matchAnyMarker("hello world", []string{"world"}) {
		t.Error("expected world to match 'hello world'")
	}
	if matchAnyMarker("hello world", []string{"universe"}) {
		t.Error("expected universe NOT to match 'hello world'")
	}
	if !matchAnyMarker("", []string{""}) {
		t.Error("empty haystack matches empty marker")
	}
	if matchAnyMarker("hello", []string{}) {
		t.Error("empty marker list must not match")
	}
}

// Ensure the package-level constants we expose are stable strings the
// router can pattern-match. If these change, update the router dropin.
func TestReworkRouteClassificationConstants(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{reworkRouteNeedsRework, "NEEDS_REWORK_PEER_REVIEW"},
		{reworkRouteReviewerHold, "REVIEW_UNAVAILABLE_HOLD"},
		{reworkRouteAmbiguous, "REWORK_ROUTE_AMBIGUOUS"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("constant drift: got %q want %q", tc.got, tc.want)
		}
	}
}

// Ensure the GT_MQ_REWORK_ROUTER env hook is honored: when unset, the
// rework-bounce shell-out is a no-op so legacy rigs continue to work.
func TestRouteRejectionToReworkBounce_NoEnvNoShellOut(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.workDir = workDir
	e.output = &bytes.Buffer{}
	// Ensure the env var is unset for this test (t.Setenv with empty value
	// sets it; we need to use os.Unsetenv explicitly).
	if err := os.Unsetenv("GT_MQ_REWORK_ROUTER"); err != nil {
		t.Fatal(err)
	}

	execCalled := false
	e.routeRejectionExec = func(ctx context.Context, args ...string) error {
		execCalled = true
		return nil
	}

	mr := &MRInfo{ID: "mr-xw8-noenv", Branch: "feat/p3w-noenv"}
	class, routed := e.routeRejectionToReworkBounce(mr, "race_condition", "PR #1 reviewer rejection", true)

	if class != "" {
		t.Errorf("class=%q want empty when env is unset", class)
	}
	if routed {
		t.Errorf("routed=true, want false when env is unset")
	}
	if execCalled {
		t.Errorf("exec must NOT be called when env is unset")
	}
}
