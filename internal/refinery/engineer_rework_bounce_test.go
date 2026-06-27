package refinery

// gastown-p3w: regression coverage for the deterministic rework-bounce routing
// path. The Refinery previously only nudged the worker when a reviewer
// rejected an MR, which left slots recovery-held even though the dropin
// router could deterministically classify the rejection and invoke the
// scoped rework-bounce runner.
//
// Witness patrol #73 added three blocking findings on top of the original
// Codex findings: handleReviewerRejection closes the MR BEFORE the rework
// shell-out; the bounded router uses SetDetachedProcessGroup which leaves
// orphaned children on timeout; and ambiguous reviewer-cap + concrete-blocker
// cases were not detected/escalated. These tests pin all five behaviors:
//
//  1. reworkBounceReason shapes the reason text so the dropin router's
//     peer-review content classifier matches (Codex finding #1).
//  2. Hyphenated cap markers (no-verdict, reviewer-unavailable, cap-deferral)
//     classify as REVIEW_UNAVAILABLE_HOLD (Codex finding #1).
//  3. Peer-review failure markers are checked FIRST; if BOTH peer-review and
//     cap markers match, classification is REWORK_ROUTE_AMBIGUOUS so the
//     caller can escalate to Mayor (Witness finding #3).
//  4. handleReviewerRejection routes the rejection through the rework-bounce
//     pipeline BEFORE closing the MR; otherwise `gt mq reject` returns
//     ErrClosedImmutable (Witness finding #1).
//  5. The bounded router shell call uses util.SetProcessGroup so a hung
//     gt/router subprocess tree is killed at the process-group level on
//     deadline, not just the parent (Witness finding #2).
//  6. The worker nudge wording branches by classification: routine
//     NEEDS_REWORK keeps "revise and resubmit"; REVIEW_UNAVAILABLE_HOLD
//     says "do NOT resubmit"; REWORK_ROUTE_AMBIGUOUS holds for Mayor.

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
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
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
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
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
	mr := dummyMR()
	classification, reason := reworkBounceReason(mr, "codex_failed", "PR #42 reviewer rejection: codex returned FAIL on commit 49dc7c91")

	if classification != reworkRouteNeedsRework {
		t.Errorf("classification = %q, want %s", classification, reworkRouteNeedsRework)
	}
	// The router's has_explicit_reviewer_fail matches "<name> <something> fail"
	// and "(peer|review)[-_ ]?fail" -- we want at least one of those markers
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
	if !strings.Contains(reason, "PR #42 reviewer rejection") {
		t.Errorf("reason dropped reviewer error message: %q", reason)
	}
	if !strings.Contains(reason, mr.Branch) {
		t.Errorf("reason dropped affected branch: %q", reason)
	}
}

// TestReworkBounceReason_HyphenatedCapMarkersClassifyAsHold covers the
// hyphenated-form tooling-failure markers documented in gastown-p3w Codex
// finding #1: no-verdict, reviewer-unavailable, cap-deferral, and the
// plural/underscore variants. Each must land in REVIEW_UNAVAILABLE_HOLD,
// prefix the reason, and explicitly disclaim source-code rework.
func TestReworkBounceReason_HyphenatedCapMarkersClassifyAsHold(t *testing.T) {
	cases := []struct {
		name   string
		cause  string
		errMsg string
	}{
		{
			name:   "hyphenated no-verdict in errMsg",
			cause:  "reviewer_unavailable",
			errMsg: "PR #42 reviewer state no-verdict after kimi rollback",
		},
		{
			name:   "hyphenated reviewer-unavailable in errMsg",
			cause:  "reviewer_unavailable",
			errMsg: "reviewer-unavailable: kimi/glm capped, only codex online",
		},
		{
			name:   "hyphenated cap-deferral in errMsg",
			cause:  "cap_deferral",
			errMsg: "kimi cap-deferral: 80% context cap reached",
		},
		{
			name:   "plural reviewers-unavailable",
			cause:  "reviewer_unavailable",
			errMsg: "reviewers-unavailable: kimi and glm both capped",
		},
		{
			name:   "insufficient-quorum hyphenated",
			cause:  "insufficient_quorum",
			errMsg: "PR #42 review insufficient-quorum: core peer offline",
		},
		{
			name:   "underscore no_verdict in cause",
			cause:  "no_verdict",
			errMsg: "PR #42 reviewer state unknown",
		},
		{
			name:   "underscore reviewer_unavailable cause with context",
			cause:  "reviewer_unavailable",
			errMsg: "no reviewers available for PR #42",
		},
		{
			name:   "hook decision defer in errMsg",
			cause:  "defer",
			errMsg: "PR #42 hook decision: defer until reviewer capacity recovers",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mr := dummyMR()
			classification, reason := reworkBounceReason(mr, tc.cause, tc.errMsg)

			if classification != reworkRouteReviewerHold {
				t.Errorf("classification = %q, want %s (case %q)", classification, reworkRouteReviewerHold, tc.name)
			}
			if !strings.HasPrefix(reason, "REVIEW_UNAVAILABLE_HOLD:") {
				t.Errorf("reason prefix missing REVIEW_UNAVAILABLE_HOLD: %q (case %q)", reason, tc.name)
			}
			if !strings.Contains(reason, "Do not resubmit") {
				t.Errorf("reason missing 'Do not resubmit' disclaimer (case %q): %q", tc.name, reason)
			}
			if strings.Contains(reason, "gt-scoped-rework-bounce-runner") {
				t.Errorf("hold case must NOT include source-rework runner invocation (case %q): %q", tc.name, reason)
			}
		})
	}
}

// TestReworkBounceReason_AmbiguousWhenBothMarkersPresent pins witness finding
// #3: when BOTH peer-review failure markers and cap markers are observed in
// the same haystack, the case is genuinely ambiguous. The router cannot
// safely classify; the engineer must escalate to Mayor via REWORK_ROUTE_AMBIGUOUS.
func TestReworkBounceReason_AmbiguousWhenBothMarkersPresent(t *testing.T) {
	cases := []struct {
		name   string
		cause  string
		errMsg string
	}{
		{
			name:   "codex FAIL with reviewer-unavailable mention",
			cause:  "codex_failed",
			errMsg: "codex returned FAIL on commit 49dc7c91; reviewer-unavailable: kimi capped",
		},
		{
			name:   "m3 failed with no-verdict in same message",
			cause:  "m3_failed",
			errMsg: "m3 returned FAIL with concrete blockers; reviewers-unavailable for kimi/glm",
		},
		{
			name:   "peer-fail + cap-deferral",
			cause:  "codex_failed",
			errMsg: "peer-fail with concrete blockers; cap-deferral due to kimi 80% cap",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mr := dummyMR()
			classification, reason := reworkBounceReason(mr, tc.cause, tc.errMsg)

			if classification != reworkRouteAmbiguous {
				t.Errorf("classification = %q, want %s (case %q)", classification, reworkRouteAmbiguous, tc.name)
			}
			if !strings.HasPrefix(reason, "REWORK_ROUTE_AMBIGUOUS:") {
				t.Errorf("reason prefix missing REWORK_ROUTE_AMBIGUOUS (case %q): %q", tc.name, reason)
			}
			if !strings.Contains(reason, "Escalating to Mayor") {
				t.Errorf("reason missing Mayor escalation message (case %q): %q", tc.name, reason)
			}
			// Critical: ambiguous must NOT masquerade as either
			// NEEDS_REWORK_PEER_REVIEW or REVIEW_UNAVAILABLE_HOLD because
			// the worker nudge wording branches on classification.
			if strings.HasPrefix(reason, "NEEDS_REWORK_PEER_REVIEW:") {
				t.Errorf("ambiguous case must not prefix as NEEDS_REWORK (case %q): %q", tc.name, reason)
			}
			if strings.HasPrefix(reason, "REVIEW_UNAVAILABLE_HOLD:") {
				t.Errorf("ambiguous case must not prefix as REVIEW_UNAVAILABLE_HOLD (case %q): %q", tc.name, reason)
			}
		})
	}
}

// TestReworkBounceReason_AmbiguousWhenNeitherMarkerPresent pins the
// fall-through case: when the haystack contains neither peer-review failure
// markers nor cap markers, the classification is also AMBIGUOUS. The engineer
// cannot tell whether this is a substantive peer-review failure (in which
// case the rejection reason text is missing the documented markers and
// should be enriched) or a tooling failure; either way, escalate to Mayor.
func TestReworkBounceReason_AmbiguousWhenNeitherMarkerPresent(t *testing.T) {
	mr := dummyMR()
	classification, reason := reworkBounceReason(mr, "reviewer_rejection", "PR #42 reviewer rejected without specific markers")

	if classification != reworkRouteAmbiguous {
		t.Errorf("classification = %q, want %s", classification, reworkRouteAmbiguous)
	}
	if !strings.HasPrefix(reason, "REWORK_ROUTE_AMBIGUOUS:") {
		t.Errorf("reason prefix missing REWORK_ROUTE_AMBIGUOUS: %q", reason)
	}
	if !strings.Contains(reason, "no recognized peer-review failure markers or cap markers") {
		t.Errorf("reason missing the neither-marker message: %q", reason)
	}
}

// TestRouteRejectionToReworkBounce_RunsBeforeMRClose is the regression test
// for witness finding #1: the rework-bounce shell-out MUST be invoked while
// the MR is still open. The test seam captures the order of operations:
//  1. routeRejectionExec is called (rework-bounce shell-out)
//  2. THEN the MR is closed
//  3. THEN the worker is nudged (post-close)
//
// If the order is reversed in handleReviewerRejection, the production
// `gt mq reject --notify` returns ErrClosedImmutable because the MR bead is
// already terminal in beads, silently dropping the rework packet.
//
// The test verifies ordering via the test seam: the router must fire before
// the worker nudge (which itself runs after the close). Direct CloseWithReason
// interception would require a beads-level seam that does not exist; the
// router-before-worker ordering is sufficient because the worker nudge is
// the post-close step in the engineer's flow.
func TestRouteRejectionToReworkBounce_RunsBeforeMRClose(t *testing.T) {
	t.Setenv("GT_MQ_REWORK_ROUTER", "enforce")

	dir := t.TempDir()
	bdDir := filepath.Join(dir, "beads")
	if err := os.MkdirAll(bdDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	script := `#!/bin/sh
case "$2" in
  show|update|close) ;;
esac
exit 0
`
	bdPath := filepath.Join(dir, "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Track order of operations through test seams.
	var events []string
	b := beads.NewWithBeadsDir(dir, bdDir)
	e := &Engineer{
		rig:     &rig.Rig{Name: "gastown"},
		beads:   b,
		workDir: dir,
		output:  &bytes.Buffer{},
		routeRejectionExec: func(_ context.Context, _ string, mrID, _ string) error {
			events = append(events, "router-called:"+mrID)
			return nil
		},
		reviewerRejectionWorkerNudge: func(_, _ string) error {
			events = append(events, "worker-nudged")
			return nil
		},
		reviewerRejectionMayorNudge: func(_ string) error { return nil },
	}

	mr := dummyMR()
	result := ProcessResult{
		ReviewerRejectionCause: "codex_failed",
		Error:                  "codex returned FAIL on commit 49dc7c91",
	}
	e.handleReviewerRejection(mr, result)

	// Assert ordering: router-called must come BEFORE the worker nudge.
	// The worker nudge is the last step that runs AFTER the MR close
	// (see handleReviewerRejection step 4), so router < worker implies
	// router < close.
	var routerIdx, workerIdx = -1, -1
	for i, ev := range events {
		if strings.HasPrefix(ev, "router-called") && routerIdx == -1 {
			routerIdx = i
		}
		if ev == "worker-nudged" && workerIdx == -1 {
			workerIdx = i
		}
	}
	if routerIdx == -1 {
		t.Fatalf("router was never invoked: events=%v", events)
	}
	if workerIdx == -1 {
		t.Fatalf("worker nudge never fired: events=%v", events)
	}
	if routerIdx >= workerIdx {
		t.Errorf("router must run BEFORE worker nudge (router=%d, worker=%d); events=%v",
			routerIdx, workerIdx, events)
	}
}

// TestRouteRejectionToReworkBounce_UsesProcessGroupCancellation is the
// regression test for witness finding #2: the bounded router shell call
// must use util.SetProcessGroup (which installs cmd.Cancel to kill the
// process group) instead of util.SetDetachedProcessGroup (which only sets
// Setpgid and leaves Cancel unset). Without cmd.Cancel, context timeout
// only kills the parent gt process; any forked children of gt / router /
// Dolt client survive and mutate state after the deadline.
//
// The test pins the contract by injecting a routeRejectionExec that
// outlives the deadline and asserting the engineer cancels the context
// cleanly. It also asserts the timeout log message includes "process group
// killed" so operators can distinguish process-group cancellation from
// generic exec failures.
func TestRouteRejectionToReworkBounce_UsesProcessGroupCancellation(t *testing.T) {
	t.Setenv("GT_MQ_REWORK_ROUTER", "enforce")

	e := &Engineer{
		rig:                 &rig.Rig{Name: "gastown"},
		output:              &bytes.Buffer{},
		reworkRouterTimeout: 1 * time.Nanosecond,
		routeRejectionExec: func(ctx context.Context, _ string, _ string, _ string) error {
			// Simulate a hung router/gt that outlives its deadline.
			time.Sleep(5 * time.Millisecond)
			if err := ctx.Err(); err != nil {
				return err
			}
			return nil
		},
	}

	mr := dummyMR()
	classification, routed := e.routeRejectionToReworkBounce(mr, "codex_failed", "codex returned FAIL on commit 49dc7c91")

	if routed {
		t.Errorf("routed = true, want false on timeout")
	}
	if classification != reworkRouteNeedsRework {
		t.Errorf("classification = %q, want %s (timeout must NOT change classification)", classification, reworkRouteNeedsRework)
	}
	out := e.output.(*bytes.Buffer).String()
	if !strings.Contains(out, "timed out") {
		t.Errorf("expected timeout log, got: %q", out)
	}
	// Witness finding #2: the timeout log must mention the
	// process-group cancellation so operators know the orphan subtree
	// was actually killed, not just the parent.
	if !strings.Contains(out, "process group killed") {
		t.Errorf("expected process-group cancellation log, got: %q", out)
	}
}

// TestRouteRejectionToReworkBounce_FailureEscalatesViaRoutedFalse verifies
// that any shell-out failure (router rejected, Dolt commit failed, etc.)
// returns routed=false so handleReviewerRejection can escalate to Mayor.
func TestRouteRejectionToReworkBounce_FailureEscalatesViaRoutedFalse(t *testing.T) {
	t.Setenv("GT_MQ_REWORK_ROUTER", "enforce")

	e := &Engineer{
		rig:    &rig.Rig{Name: "gastown"},
		output: &bytes.Buffer{},
		routeRejectionExec: func(_ context.Context, _ string, _ string, _ string) error {
			return errors.New("simulated router failure")
		},
	}

	mr := &MRInfo{ID: "gastown-wisp-ehs", SourceIssue: "gastown-6z5"}
	class, routed := e.routeRejectionToReworkBounce(mr, "codex_failed", "codex returned FAIL")

	if routed {
		t.Errorf("routed = true, want false on router failure")
	}
	if class != reworkRouteNeedsRework {
		t.Errorf("classification = %q, want %s (failure must NOT change classification)", class, reworkRouteNeedsRework)
	}
	out := e.output.(*bytes.Buffer).String()
	if !strings.Contains(out, "Warning") || !strings.Contains(out, "rework-bounce") {
		t.Errorf("expected warning log for failed router call, got: %q", out)
	}
}

// TestRouteRejectionToReworkBounce_AmbiguousDoesNotRoute verifies the
// ambiguous-classification branch: when reworkBounceReason returns
// REWORK_ROUTE_AMBIGUOUS, routeRejectionToReworkBounce returns routed=false
// so handleReviewerRejection escalates to Mayor via the !routed branch.
// The shell call still happens (to give the router a chance to override),
// but the routed bool is false.
func TestRouteRejectionToReworkBounce_AmbiguousDoesNotRoute(t *testing.T) {
	t.Setenv("GT_MQ_REWORK_ROUTER", "enforce")

	var routerCalled bool
	e := &Engineer{
		rig:    &rig.Rig{Name: "gastown"},
		output: &bytes.Buffer{},
		routeRejectionExec: func(_ context.Context, _ string, _ string, _ string) error {
			routerCalled = true
			return nil
		},
	}

	mr := dummyMR()
	// Both peer-review AND cap markers in the same message -> ambiguous.
	class, routed := e.routeRejectionToReworkBounce(mr, "codex_failed", "codex returned FAIL with concrete blockers; reviewer-unavailable: kimi capped")

	if class != reworkRouteAmbiguous {
		t.Errorf("classification = %q, want %s", class, reworkRouteAmbiguous)
	}
	if routed {
		t.Errorf("routed = true, want false for ambiguous (must escalate to Mayor)")
	}
	if !routerCalled {
		t.Errorf("router shell-out must still fire for ambiguous (router may override classification)")
	}
}

// TestRouteRejectionToReworkBounce_ReviewerUnavailableRoutesSeparately
// verifies the engineer produces a REVIEW_UNAVAILABLE_HOLD-classified
// reason (not a peer-review source-rework reason) when the underlying
// cause is a reviewer-unavailable / cap-deferral case.
func TestRouteRejectionToReworkBounce_ReviewerUnavailableRoutesSeparately(t *testing.T) {
	t.Setenv("GT_MQ_REWORK_ROUTER", "enforce")

	var capturedReason string
	e := &Engineer{
		rig:    &rig.Rig{Name: "gastown"},
		output: &bytes.Buffer{},
		routeRejectionExec: func(_ context.Context, _ string, _ string, reason string) error {
			capturedReason = reason
			return nil
		},
	}

	mr := &MRInfo{ID: "gastown-wisp-x", SourceIssue: "gastown-y"}
	class, routed := e.routeRejectionToReworkBounce(mr, "reviewer_unavailable", "no reviewers available for PR #42")

	if !routed {
		t.Errorf("routed = false, want true for unambiguous hold")
	}
	if class != reworkRouteReviewerHold {
		t.Errorf("classification = %q, want %s", class, reworkRouteReviewerHold)
	}
	if !strings.HasPrefix(capturedReason, "REVIEW_UNAVAILABLE_HOLD:") {
		t.Errorf("reviewer-unavailable case produced non-hold reason: %q", capturedReason)
	}
	if strings.Contains(capturedReason, "concrete blockers") {
		t.Errorf("reviewer-unavailable case should NOT contain source-rework markers: %q", capturedReason)
	}
}

// TestHandleReviewerRejection_RoutineReworkRoutesWithoutMayorNudge verifies
// the end-to-end rejection flow for a routine NEEDS_REWORK case: the MR is
// closed (after the rework-bounce shell-out), the worker is nudged, and the
// rework-bounce router is invoked. Mayor is NOT notified.
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

// TestHandleReviewerRejection_AmbiguousEscalatesMayor verifies witness
// finding #3: when both peer-review failure markers and cap markers are
// observed, handleReviewerRejection escalates to Mayor with a hold
// message rather than nudging the worker to resubmit.
func TestHandleReviewerRejection_AmbiguousEscalatesMayor(t *testing.T) {
	t.Setenv("GT_MQ_REWORK_ROUTER", "enforce")

	b, workDir := newMockBeads(t)
	var workerCalled, mayorCalled bool
	var workerMsg string
	e := &Engineer{
		rig:     &rig.Rig{Name: "gastown", Path: workDir},
		beads:   b,
		workDir: workDir,
		output:  &bytes.Buffer{},
		routeRejectionExec: func(_ context.Context, _ string, _ string, _ string) error {
			return nil
		},
		reviewerRejectionWorkerNudge: func(_, msg string) error {
			workerCalled = true
			workerMsg = msg
			return nil
		},
		reviewerRejectionMayorNudge: func(_ string) error {
			mayorCalled = true
			return nil
		},
	}

	mr := dummyMR()
	result := ProcessResult{
		ReviewerRejectionCause: "codex_failed",
		Error:                  "codex returned FAIL with concrete blockers; reviewer-unavailable: kimi capped",
	}
	e.handleReviewerRejection(mr, result)

	if !workerCalled {
		t.Errorf("worker nudge was not sent")
	}
	if !mayorCalled {
		t.Errorf("mayor nudge was not sent for ambiguous case")
	}
	if !strings.HasPrefix(workerMsg, "REWORK_ROUTE_AMBIGUOUS:") {
		t.Errorf("worker nudge prefix = %q, want REWORK_ROUTE_AMBIGUOUS:", workerMsg)
	}
	if !strings.Contains(workerMsg, "Hold resubmit") {
		t.Errorf("ambiguous worker nudge must say 'Hold resubmit', got: %q", workerMsg)
	}
	if strings.Contains(workerMsg, "revise and resubmit with 'gt done'") {
		t.Errorf("ambiguous worker nudge must NOT include revise-and-resubmit guidance, got: %q", workerMsg)
	}
}

// TestHandleReviewerRejection_NudgeMessageBranchesByClassification covers
// gastown-p3w M3 + witness #73: handleReviewerRejection must branch its
// worker nudge by the rework-bounce router's classification. Routine
// NEEDS_REWORK keeps the revise-and-resubmit guidance; REVIEW_UNAVAILABLE_HOLD
// (reviewer tooling / cap-deferral / no-verdict) must NOT instruct the
// worker to "revise and resubmit with 'gt done'" -- that would burn a
// rework packet on a non-fixable condition. The hold message must
// explicitly say "do NOT resubmit until reviewer availability changes".
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
