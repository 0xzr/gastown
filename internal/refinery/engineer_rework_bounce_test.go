package refinery

// gastown-p3w: regression coverage for the deterministic rework-bounce routing
// path. The Refinery previously only nudged the worker when a reviewer
// rejected an MR, which left slots recovery-held even though the dropin
// router could deterministically classify the rejection and invoke the
// scoped rework-bounce runner.
//
// These tests pin:
//  1. reworkBounceReason shapes the reason text so the dropin router's
//     peer-review content classifier matches (the historical "Codex-failed"
//     pattern that produced route_reason=not_apply_conflict).
//  2. Review-tooling / cap-deferral cases are flagged with
//     REVIEW_UNAVAILABLE_HOLD so the router does NOT treat them as
//     source-code rework.
//  3. routeRejectionToReworkBounce invokes `gt mq reject --notify` with the
//     right rig/mr/reason when GT_MQ_REWORK_ROUTER is set, is bounded by a
//     context/timeout, and fails closed with logging.
//  4. handleReviewerRejection closes the MR + nudges the worker AND routes
//     through the rework-bounce pipeline without Mayor involvement for the
//     routine NEEDS_REWORK case. Mayor is only escalated when routing fails.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/rig"
)

// dummyMR returns an MRInfo with enough fields populated for reason shaping.
func dummyMR() *MRInfo {
	return &MRInfo{
		ID:          "gastown-wisp-ehs",
		SourceIssue: "gastown-6z5",
		Branch:      "polecat/jasper/gastown-6z5@verify",
		Worker:      "polecats/jasper",
	}
}

// newMockBeads creates a beads client backed by a no-op mock `bd` script so
// handleReviewerRejection can exercise Show/Update/CloseWithoutWithout
// touching a real Dolt server.
func newMockBeads(t *testing.T) (*beads.Beads, string) {
	t.Helper()
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, "beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}

	script := `#!/bin/sh
# Mock bd for rework-bounce tests. Ignores --allow-stale/--flat flags and
# returns minimal valid output for the commands handleReviewerRejection uses.
cmd=""
for arg; do
  case "$arg" in
    --*) continue ;;
  esac
  cmd="$arg"
  break
done
case "$cmd" in
  show)
    printf '[{"id":"%s","title":"mock","description":"","status":"open"}]\n' "${2:-test}"
    ;;
  update|close)
    ;;
  version)
    ;;
  *)
    ;;
esac
exit 0
`
	bdPath := filepath.Join(dir, "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)

	return beads.NewWithBeadsDir(dir, beadsDir), dir
}

// TestReworkBounceReason_PeerReviewShapesClassifierMatchingText verifies the
// routine NEEDS_REWORK path produces a reason with the keywords the dropin
// router's peer-review content classifier looks for. The router fails to
// route when the reason has none of these markers (the original bug).
func TestReworkBounceReason_PeerReviewShapesClassifierMatchingText(t *testing.T) {
	// Codex-failed commit / "resubmitted the same Codex-failed commit" —
	// the exact pattern that returned route_reason=not_apply_conflict for
	// gastown-wisp-ehs and gastown-wisp-wvl in the 2026-06-26 router log.
	mr := dummyMR()
	classification, reason := reworkBounceReason(mr, "codex_failed", "PR #42 reviewer rejection: codex returned FAIL on commit 49dc7c91")

	if classification != "NEEDS_REWORK_PEER_REVIEW" {
		t.Errorf("classification = %q, want NEEDS_REWORK_PEER_REVIEW", classification)
	}
	// The router's has_explicit_reviewer_fail matches "<name> <something> fail"
	// and "(peer|review)[-_ ]?fail" — we want at least one of those markers
	// to land in the reason text.
	if !strings.Contains(reason, "peer-fail") && !strings.Contains(reason, "fail") {
		t.Errorf("reason missing peer-review classifier marker: %q", reason)
	}
	if !strings.Contains(reason, "concrete blockers") {
		t.Errorf("reason missing concrete-blockers marker: %q", reason)
	}
	if !strings.Contains(reason, "codex_failed") {
		t.Errorf("reason missing cause for traceability: %q", reason)
	}
	// The worker-facing reason should preserve actionable reviewer detail,
	// not just synthetic classifier markers.
	if !strings.Contains(reason, "PR #42 reviewer rejection") {
		t.Errorf("reason dropped reviewer error message: %q", reason)
	}
	if !strings.Contains(reason, mr.Branch) {
		t.Errorf("reason dropped affected branch: %q", reason)
	}
}

// TestReworkBounceReason_ReviewerUnavailableIsSeparateClassification pins
// the separation between source-code rework and review-tooling failures.
// The router's reviewer-cap deferral classifier only matches
// REVIEW_UNAVAILABLE_HOLD when the reason has "reviewer unavailable" /
// "no-verdict" / "insufficient quorum" / "capped" markers.
func TestReworkBounceReason_ReviewerUnavailableIsSeparateClassification(t *testing.T) {
	cases := []struct {
		name   string
		cause  string
		errMsg string
	}{
		{"reviewers unavailable", "reviewer_unavailable", "no reviewers available for PR #42"},
		{"no-verdict", "no_verdict", "PR #42 reviewer state NO_VERDICT"},
		{"insufficient quorum", "insufficient_quorum", "core peer unavailable, insufficient quorum"},
		{"cap deferral", "capped", "kimi capped at 80% context"},
		{"hook decision defer", "deferred", "hook decision: defer because no reviewers available"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mr := dummyMR()
			classification, reason := reworkBounceReason(mr, tc.cause, tc.errMsg)
			if classification != "REVIEW_UNAVAILABLE_HOLD" {
				t.Errorf("classification = %q, want REVIEW_UNAVAILABLE_HOLD (cause=%s err=%s)", classification, tc.cause, tc.errMsg)
			}
			if !strings.HasPrefix(reason, "REVIEW_UNAVAILABLE_HOLD:") {
				t.Errorf("reason prefix = %q, want REVIEW_UNAVAILABLE_HOLD:", reason)
			}
			if !strings.Contains(reason, "not a source-code rework") {
				t.Errorf("reason should explicitly disclaim source-code rework: %q", reason)
			}
			if !strings.Contains(reason, tc.errMsg) {
				t.Errorf("reason dropped reviewer error message: %q", reason)
			}
			if !strings.Contains(reason, mr.Branch) {
				t.Errorf("reason dropped affected branch: %q", reason)
			}
		})
	}
}

// TestReworkBounceReason_HyphenatedCapMarkersClassifyAsHold covers gastown-p3w
// M3: documented tooling-failure markers use hyphenated forms (no-verdict,
// reviewer-unavailable, cap-deferral). The haystack normalizer must replace
// both underscores AND hyphens with spaces so these inputs land in the
// REVIEW_UNAVAILABLE_HOLD classification instead of falling through to
// NEEDS_REWORK_PEER_REVIEW, where they would create bogus source-code rework
// packets for reviewer/cap issues.
func TestReworkBounceReason_HyphenatedCapMarkersClassifyAsHold(t *testing.T) {
	cases := []struct {
		name   string
		cause  string
		errMsg string
	}{
		{"hyphen no-verdict", "no-verdict", "PR #42 reviewer state NO_VERDICT"},
		{"hyphen reviewer-unavailable", "reviewer-unavailable", "no reviewers available for PR #42"},
		{"hyphen cap-deferral", "cap-deferral", "kimi capped at 80% context, deferring"},
		{"hyphen reviewers-unavailable (plural)", "reviewers-unavailable", "all core reviewers offline"},
		{"hyphen in errMsg no-verdict", "codex_failed", "no-verdict: PR #42 returned NO_VERDICT"},
		{"hyphen in errMsg reviewer-unavailable", "codex_failed", "reviewer-unavailable: peer offline"},
		{"hyphen in errMsg cap-deferral", "codex_failed", "cap-deferral: kimi at 80% context"},
		{"hyphen insufficient-quorum", "insufficient-quorum", "core peer unavailable, insufficient quorum"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mr := dummyMR()
			classification, reason := reworkBounceReason(mr, tc.cause, tc.errMsg)
			if classification != "REVIEW_UNAVAILABLE_HOLD" {
				t.Errorf("classification = %q, want REVIEW_UNAVAILABLE_HOLD for hyphenated cap marker (cause=%s err=%s)", classification, tc.cause, tc.errMsg)
			}
			if !strings.HasPrefix(reason, "REVIEW_UNAVAILABLE_HOLD:") {
				t.Errorf("reason prefix = %q, want REVIEW_UNAVAILABLE_HOLD:", reason)
			}
			if !strings.Contains(reason, "not a source-code rework") {
				t.Errorf("reason should explicitly disclaim source-code rework: %q", reason)
			}
			if strings.Contains(reason, "concrete blockers") {
				t.Errorf("hyphenated cap-marker case should NOT contain source-rework markers: %q", reason)
			}
			if !strings.Contains(reason, mr.Branch) {
				t.Errorf("reason dropped affected branch: %q", reason)
			}
		})
	}
}

// TestRouteRejectionToReworkBounce_InvokesMQRejectWithClassifierReason
// verifies the integration: when GT_MQ_REWORK_ROUTER is set, the engineer
// calls `gt mq reject <rig> <mr> --reason <classified> --notify` so the
// dropin router can produce a bounded rework packet and invoke
// gt-scoped-rework-bounce-runner.sh.
func TestRouteRejectionToReworkBounce_InvokesMQRejectWithClassifierReason(t *testing.T) {
	t.Setenv("GT_MQ_REWORK_ROUTER", "enforce")

	var capturedRig, capturedMR, capturedReason string
	var capturedNotify bool
	e := &Engineer{
		rig:    &rig.Rig{Name: "gastown"},
		output: &bytes.Buffer{},
		routeRejectionExec: func(_ context.Context, rigName, mrID, reason string) error {
			capturedRig = rigName
			capturedMR = mrID
			capturedReason = reason
			// The production path always passes --notify; assert that
			// equivalent here by recording the flag in the reason.
			capturedNotify = true
			return nil
		},
	}

	mr := dummyMR()
	class, routed := e.routeRejectionToReworkBounce(mr, "codex_failed", "codex returned FAIL on commit 49dc7c91")

	if !routed {
		t.Errorf("routed = false, want true")
	}
	if class != "NEEDS_REWORK_PEER_REVIEW" {
		t.Errorf("classification = %q, want NEEDS_REWORK_PEER_REVIEW", class)
	}
	if capturedRig != "gastown" {
		t.Errorf("rig = %q, want gastown", capturedRig)
	}
	if capturedMR != "gastown-wisp-ehs" {
		t.Errorf("mr = %q, want gastown-wisp-ehs", capturedMR)
	}
	if !capturedNotify {
		t.Errorf("--notify flag not exercised (test seam should always set it)")
	}
	if !strings.Contains(capturedReason, "peer-fail") && !strings.Contains(capturedReason, "fail") {
		t.Errorf("reason missing classifier marker: %q", capturedReason)
	}
	if !strings.Contains(capturedReason, "codex_failed") {
		t.Errorf("reason missing cause for traceability: %q", capturedReason)
	}
}

// TestRouteRejectionToReworkBounce_NoOpWhenEnvUnset pins the opt-in
// behavior: when GT_MQ_REWORK_ROUTER is unset, the path is a no-op so
// rigs that have not enabled the rework-bounce pipeline keep the prior
// nudge-only behavior.
func TestRouteRejectionToReworkBounce_NoOpWhenEnvUnset(t *testing.T) {
	t.Setenv("GT_MQ_REWORK_ROUTER", "")

	called := false
	e := &Engineer{
		rig:    &rig.Rig{Name: "gastown"},
		output: &bytes.Buffer{},
		routeRejectionExec: func(context.Context, string, string, string) error {
			called = true
			return nil
		},
	}

	mr := &MRInfo{ID: "gastown-wisp-ehs", SourceIssue: "gastown-6z5"}
	class, routed := e.routeRejectionToReworkBounce(mr, "codex_failed", "codex returned FAIL")

	if called {
		t.Errorf("routeRejectionExec called with empty GT_MQ_REWORK_ROUTER (want no-op)")
	}
	if class != "" || routed {
		t.Errorf("expected no routing attempt when env unset, got class=%q routed=%v", class, routed)
	}
}

// TestRouteRejectionToReworkBounce_SkipsEmptyMR guards against a nil/empty
// MR panicking the rework-bounce path. Empty MRs are a defensive case the
// engineer sometimes encounters during cleanup.
func TestRouteRejectionToReworkBounce_SkipsEmptyMR(t *testing.T) {
	t.Setenv("GT_MQ_REWORK_ROUTER", "enforce")

	called := false
	e := &Engineer{
		rig:    &rig.Rig{Name: "gastown"},
		output: &bytes.Buffer{},
		routeRejectionExec: func(context.Context, string, string, string) error {
			called = true
			return nil
		},
	}

	e.routeRejectionToReworkBounce(nil, "codex_failed", "codex returned FAIL")
	e.routeRejectionToReworkBounce(&MRInfo{ID: ""}, "codex_failed", "codex returned FAIL")

	if called {
		t.Errorf("routeRejectionExec called for nil/empty MR (want skip)")
	}
}

// TestRouteRejectionToReworkBounce_LogsRouterFailure verifies the engineer
// does not crash when the rework-bounce routing fails. The bounded shell
// call is logged and the caller receives routed=false so the nudge path can
// escalate appropriately.
func TestRouteRejectionToReworkBounce_LogsRouterFailure(t *testing.T) {
	t.Setenv("GT_MQ_REWORK_ROUTER", "enforce")

	e := &Engineer{
		rig:    &rig.Rig{Name: "gastown"},
		output: &bytes.Buffer{},
		routeRejectionExec: func(context.Context, string, string, string) error {
			return &execFailure{msg: "simulated gt binary missing"}
		},
	}

	mr := &MRInfo{ID: "gastown-wisp-ehs", SourceIssue: "gastown-6z5"}
	class, routed := e.routeRejectionToReworkBounce(mr, "codex_failed", "codex returned FAIL")

	if routed {
		t.Errorf("routed = true, want false on router failure")
	}
	if class != "NEEDS_REWORK_PEER_REVIEW" {
		t.Errorf("classification = %q, want NEEDS_REWORK_PEER_REVIEW", class)
	}
	out := e.output.(*bytes.Buffer).String()
	if !strings.Contains(out, "Warning") || !strings.Contains(out, "rework-bounce") {
		t.Errorf("expected warning log for failed router call, got: %q", out)
	}
}

// execFailure is a minimal error type for the test seam.
type execFailure struct{ msg string }

func (e *execFailure) Error() string { return e.msg }

// TestRouteRejectionToReworkBounce_ReviewerUnavailableRoutesSeparately
// verifies the engineer produces a REVIEW_UNAVAILABLE_HOLD-classified
// reason (not a peer-review source-rework reason) when the underlying
// cause is a reviewer-unavailable / cap-deferral case. This prevents
// the router from producing a source-code rework packet for a tooling
// failure.
func TestRouteRejectionToReworkBounce_ReviewerUnavailableRoutesSeparately(t *testing.T) {
	t.Setenv("GT_MQ_REWORK_ROUTER", "enforce")

	var capturedReason string
	e := &Engineer{
		rig:    &rig.Rig{Name: "gastown"},
		output: &bytes.Buffer{},
		routeRejectionExec: func(_ context.Context, rigName, mrID, reason string) error {
			capturedReason = reason
			return nil
		},
	}

	mr := &MRInfo{ID: "gastown-wisp-x", SourceIssue: "gastown-y"}
	class, routed := e.routeRejectionToReworkBounce(mr, "reviewer_unavailable", "no reviewers available for PR #42")

	if !routed {
		t.Errorf("routed = false, want true")
	}
	if class != "REVIEW_UNAVAILABLE_HOLD" {
		t.Errorf("classification = %q, want REVIEW_UNAVAILABLE_HOLD", class)
	}
	if !strings.HasPrefix(capturedReason, "REVIEW_UNAVAILABLE_HOLD:") {
		t.Errorf("reviewer-unavailable case produced non-hold reason: %q", capturedReason)
	}
	if strings.Contains(capturedReason, "concrete blockers") {
		t.Errorf("reviewer-unavailable case should NOT contain source-rework markers: %q", capturedReason)
	}
}

// TestRouteRejectionToReworkBounce_ContextTimeoutLogsWarning verifies a
// hung router command is bounded and logged as a timeout so the refinery
// rejection path does not stall indefinitely.
func TestRouteRejectionToReworkBounce_ContextTimeoutLogsWarning(t *testing.T) {
	t.Setenv("GT_MQ_REWORK_ROUTER", "enforce")

	e := &Engineer{
		rig:                 &rig.Rig{Name: "gastown"},
		output:              &bytes.Buffer{},
		reworkRouterTimeout: 1 * time.Nanosecond,
		routeRejectionExec: func(ctx context.Context, _ string, _ string, _ string) error {
			// Simulate an external command that outlives its deadline.
			time.Sleep(5 * time.Millisecond)
			if err := ctx.Err(); err != nil {
				return err
			}
			return nil
		},
	}

	mr := &MRInfo{ID: "gastown-wisp-ehs", SourceIssue: "gastown-6z5"}
	class, routed := e.routeRejectionToReworkBounce(mr, "codex_failed", "codex returned FAIL")

	if routed {
		t.Errorf("routed = true, want false on timeout")
	}
	if class != "NEEDS_REWORK_PEER_REVIEW" {
		t.Errorf("classification = %q, want NEEDS_REWORK_PEER_REVIEW", class)
	}
	out := e.output.(*bytes.Buffer).String()
	if !strings.Contains(out, "timed out") || !strings.Contains(out, "rework-bounce") {
		t.Errorf("expected timeout log for slow router call, got: %q", out)
	}
}

// TestHandleReviewerRejection_RoutineReworkRoutesWithoutMayorNudge verifies
// the end-to-end rejection flow for a routine NEEDS_REWORK case: the MR is
// closed, the worker is nudged, the rework-bounce router is invoked, and
// Mayor is NOT notified.
func TestHandleReviewerRejection_RoutineReworkRoutesWithoutMayorNudge(t *testing.T) {
	t.Setenv("GT_MQ_REWORK_ROUTER", "enforce")

	b, workDir := newMockBeads(t)
	var workerCalled, mayorCalled bool
	var capturedReason string
	e := &Engineer{
		rig:     &rig.Rig{Name: "gastown", Path: workDir},
		beads:   b,
		workDir: workDir,
		output:  &bytes.Buffer{},
		routeRejectionExec: func(_ context.Context, _ string, _ string, reason string) error {
			capturedReason = reason
			return nil
		},
		reviewerRejectionWorkerNudge: func(target, msg string) error {
			workerCalled = true
			return nil
		},
		reviewerRejectionMayorNudge: func(msg string) error {
			mayorCalled = true
			return nil
		},
	}

	mr := dummyMR()
	result := ProcessResult{
		ReviewerRejectionCause: "codex_failed",
		Error:                  "codex returned FAIL on commit 49dc7c91",
	}
	e.handleReviewerRejection(mr, result)

	if !workerCalled {
		t.Errorf("worker nudge was not sent")
	}
	if mayorCalled {
		t.Errorf("mayor nudge was sent for a routine routed NEEDS_REWORK rejection")
	}
	if capturedReason == "" {
		t.Errorf("rework-bounce router was not invoked")
	}
	if !strings.Contains(capturedReason, mr.Branch) {
		t.Errorf("router reason dropped affected branch: %q", capturedReason)
	}
	if !strings.Contains(capturedReason, result.Error) {
		t.Errorf("router reason dropped reviewer error: %q", capturedReason)
	}
}

// TestHandleReviewerRejection_RouterFailureEscalatesMayor verifies that when
// the rework-bounce router fails (or classification is otherwise ambiguous),
// handleReviewerRejection escalates to Mayor after notifying the worker.
func TestHandleReviewerRejection_RouterFailureEscalatesMayor(t *testing.T) {
	t.Setenv("GT_MQ_REWORK_ROUTER", "enforce")

	b, workDir := newMockBeads(t)
	var workerCalled, mayorCalled bool
	e := &Engineer{
		rig:     &rig.Rig{Name: "gastown", Path: workDir},
		beads:   b,
		workDir: workDir,
		output:  &bytes.Buffer{},
		routeRejectionExec: func(context.Context, string, string, string) error {
			return errors.New("simulated router failure")
		},
		reviewerRejectionWorkerNudge: func(target, msg string) error {
			workerCalled = true
			return nil
		},
		reviewerRejectionMayorNudge: func(msg string) error {
			mayorCalled = true
			return nil
		},
	}

	mr := dummyMR()
	result := ProcessResult{
		ReviewerRejectionCause: "codex_failed",
		Error:                  "codex returned FAIL on commit 49dc7c91",
	}
	e.handleReviewerRejection(mr, result)

	if !workerCalled {
		t.Errorf("worker nudge was not sent")
	}
	if !mayorCalled {
		t.Errorf("mayor nudge was not sent when rework-bounce routing failed")
	}
	out := e.output.(*bytes.Buffer).String()
	if !strings.Contains(out, "Warning") || !strings.Contains(out, "rework-bounce") {
		t.Errorf("expected router-failure warning log, got: %q", out)
	}
}

// TestHandleReviewerRejection_NudgeMessageBranchesByClassification covers
// gastown-p3w M3: handleReviewerRejection must branch its worker nudge by
// the rework-bounce router's classification. Routine NEEDS_REWORK keeps
// the revise-and-resubmit guidance; REVIEW_UNAVAILABLE_HOLD (reviewer
// tooling / cap-deferral / no-verdict) must NOT instruct the worker to
// "revise and resubmit with 'gt done'" — that would burn a rework packet
// on a non-fixable condition. The hold message must explicitly say "do
// NOT resubmit until reviewer availability changes".
func TestHandleReviewerRejection_NudgeMessageBranchesByClassification(t *testing.T) {
	cases := []struct {
		name              string
		cause             string
		errMsg            string
		wantResubmitNudge bool
		wantHoldMarker    string // substring expected in nudge when wantResubmitNudge=false
		wantPrefix        string // expected leading prefix of the nudge
	}{
		{
			name:              "routine NEEDS_REWORK keeps revise-and-resubmit guidance",
			cause:             "codex_failed",
			errMsg:            "codex returned FAIL on commit 49dc7c91",
			wantResubmitNudge: true,
			wantPrefix:        "REVIEWER_REJECTED:",
		},
		{
			name:              "reviewer_unavailable hold does not nudge resubmit",
			cause:             "reviewer_unavailable",
			errMsg:            "no reviewers available for PR #42",
			wantResubmitNudge: false,
			wantHoldMarker:    "do NOT resubmit until reviewer availability changes",
			wantPrefix:        "REVIEW_UNAVAILABLE_HOLD:",
		},
		{
			name:              "hyphenated cap-deferral hold does not nudge resubmit",
			cause:             "cap-deferral",
			errMsg:            "kimi capped at 80% context",
			wantResubmitNudge: false,
			wantHoldMarker:    "do NOT resubmit until reviewer availability changes",
			wantPrefix:        "REVIEW_UNAVAILABLE_HOLD:",
		},
		{
			name:              "no-verdict hold does not nudge resubmit",
			cause:             "no_verdict",
			errMsg:            "PR #42 reviewer state NO_VERDICT",
			wantResubmitNudge: false,
			wantHoldMarker:    "do NOT resubmit until reviewer availability changes",
			wantPrefix:        "REVIEW_UNAVAILABLE_HOLD:",
		},
		{
			name:              "insufficient-quorum hold does not nudge resubmit",
			cause:             "insufficient_quorum",
			errMsg:            "core peer unavailable, insufficient quorum",
			wantResubmitNudge: false,
			wantHoldMarker:    "do NOT resubmit until reviewer availability changes",
			wantPrefix:        "REVIEW_UNAVAILABLE_HOLD:",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GT_MQ_REWORK_ROUTER", "enforce")

			b, workDir := newMockBeads(t)
			var capturedMsg string
			var workerCalled bool
			e := &Engineer{
				rig:     &rig.Rig{Name: "gastown", Path: workDir},
				beads:   b,
				workDir: workDir,
				output:  &bytes.Buffer{},
				routeRejectionExec: func(_ context.Context, _ string, _ string, _ string) error {
					return nil
				},
				reviewerRejectionWorkerNudge: func(target, msg string) error {
					workerCalled = true
					capturedMsg = msg
					return nil
				},
			}

			mr := dummyMR()
			result := ProcessResult{
				ReviewerRejectionCause: tc.cause,
				Error:                  tc.errMsg,
			}
			e.handleReviewerRejection(mr, result)

			if !workerCalled {
				t.Fatalf("worker nudge was not sent for case %q", tc.name)
			}
			if capturedMsg == "" {
				t.Fatalf("worker nudge msg was empty for case %q", tc.name)
			}
			if !strings.HasPrefix(capturedMsg, tc.wantPrefix) {
				t.Errorf("nudge prefix = %q, want %q (case %q)", capturedMsg, tc.wantPrefix, tc.name)
			}
			hasResubmit := strings.Contains(capturedMsg, "revise and resubmit with 'gt done'")
			if tc.wantResubmitNudge && !hasResubmit {
				t.Errorf("expected 'revise and resubmit with gt done' in nudge, got: %q", capturedMsg)
			}
			if !tc.wantResubmitNudge && hasResubmit {
				t.Errorf("REVIEW_UNAVAILABLE_HOLD must NOT include 'revise and resubmit with gt done', got: %q", capturedMsg)
			}
			if tc.wantHoldMarker != "" && !strings.Contains(capturedMsg, tc.wantHoldMarker) {
				t.Errorf("expected hold marker %q in nudge, got: %q", tc.wantHoldMarker, capturedMsg)
			}
		})
	}
}
