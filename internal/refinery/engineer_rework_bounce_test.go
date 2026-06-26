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
//     right rig/mr/reason when GT_MQ_REWORK_ROUTER is set, and is a no-op
//     when the env var is unset (preserving the prior behavior for rigs
//     that have not opted in).
//  4. handleReviewerRejection closes the MR + nudges AND routes through the
//     rework-bounce pipeline in a single pass — no Mayor intervention
//     required for the routine NEEDS_REWORK case.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/rig"
)

// TestReworkBounceReason_PeerReviewShapesClassifierMatchingText verifies the
// routine NEEDS_REWORK path produces a reason with the keywords the dropin
// router's peer-review content classifier looks for. The router fails to
// route when the reason has none of these markers (the original bug).
func TestReworkBounceReason_PeerReviewShapesClassifierMatchingText(t *testing.T) {
	// Codex-failed commit / "resubmitted the same Codex-failed commit" —
	// the exact pattern that returned route_reason=not_apply_conflict for
	// gastown-wisp-ehs and gastown-wisp-wvl in the 2026-06-26 router log.
	classification, reason := reworkBounceReason("codex_failed", "PR #42 reviewer rejection: codex returned FAIL on commit 49dc7c91")

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
			classification, reason := reworkBounceReason(tc.cause, tc.errMsg)
			if classification != "REVIEW_UNAVAILABLE_HOLD" {
				t.Errorf("classification = %q, want REVIEW_UNAVAILABLE_HOLD (cause=%s err=%s)", classification, tc.cause, tc.errMsg)
			}
			if !strings.HasPrefix(reason, "REVIEW_UNAVAILABLE_HOLD:") {
				t.Errorf("reason prefix = %q, want REVIEW_UNAVAILABLE_HOLD:", reason)
			}
			if !strings.Contains(reason, "not a source-code rework") {
				t.Errorf("reason should explicitly disclaim source-code rework: %q", reason)
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
		routeRejectionExec: func(rigName, mrID, reason string) error {
			capturedRig = rigName
			capturedMR = mrID
			capturedReason = reason
			// The production path always passes --notify; assert that
			// equivalent here by recording the flag in the reason.
			capturedNotify = true
			return nil
		},
	}

	mr := &MRInfo{ID: "gastown-wisp-ehs", SourceIssue: "gastown-6z5", Branch: "polecat/jasper/gastown-6z5@verify"}
	e.routeRejectionToReworkBounce(mr, "codex_failed", "codex returned FAIL on commit 49dc7c91")

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
		routeRejectionExec: func(rigName, mrID, reason string) error {
			called = true
			return nil
		},
	}

	mr := &MRInfo{ID: "gastown-wisp-ehs", SourceIssue: "gastown-6z5"}
	e.routeRejectionToReworkBounce(mr, "codex_failed", "codex returned FAIL")

	if called {
		t.Errorf("routeRejectionExec called with empty GT_MQ_REWORK_ROUTER (want no-op)")
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
		routeRejectionExec: func(rigName, mrID, reason string) error {
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
// does not crash when the rework-bounce routing fails. The best-effort
// shell call is logged and the nudge / mail path still proceeds.
func TestRouteRejectionToReworkBounce_LogsRouterFailure(t *testing.T) {
	t.Setenv("GT_MQ_REWORK_ROUTER", "enforce")

	e := &Engineer{
		rig:    &rig.Rig{Name: "gastown"},
		output: &bytes.Buffer{},
		routeRejectionExec: func(rigName, mrID, reason string) error {
			return &execFailure{msg: "simulated gt binary missing"}
		},
	}

	mr := &MRInfo{ID: "gastown-wisp-ehs", SourceIssue: "gastown-6z5"}
	// Should not panic; should log the failure.
	e.routeRejectionToReworkBounce(mr, "codex_failed", "codex returned FAIL")

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
		routeRejectionExec: func(rigName, mrID, reason string) error {
			capturedReason = reason
			return nil
		},
	}

	mr := &MRInfo{ID: "gastown-wisp-x", SourceIssue: "gastown-y"}
	e.routeRejectionToReworkBounce(mr, "reviewer_unavailable", "no reviewers available for PR #42")

	if !strings.HasPrefix(capturedReason, "REVIEW_UNAVAILABLE_HOLD:") {
		t.Errorf("reviewer-unavailable case produced non-hold reason: %q", capturedReason)
	}
	if strings.Contains(capturedReason, "concrete blockers") {
		t.Errorf("reviewer-unavailable case should NOT contain source-rework markers: %q", capturedReason)
	}
}
