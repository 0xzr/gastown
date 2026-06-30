package refinery

import (
	"testing"

	"github.com/steveyegge/gastown/internal/refinery/mrtelemetry"
)

// TestClassifyFailure exercises the ProcessResult -> FailureClass mapping the
// refinery stamps onto each terminal telemetry attempt (gastown-wjk). The
// classification is what lets Mayor separate substantive implementation-quality
// failures (which distinguish models) from infra/unavailable/timeout noise.
func TestClassifyFailure(t *testing.T) {
	tests := []struct {
		name                string
		result              ProcessResult
		isReviewerRejection bool
		want                mrtelemetry.FailureClass
	}{
		{
			name:                "reviewer rejection is substantive",
			result:              ProcessResult{NeedsRework: true, ReviewerRejectionCause: "race_condition"},
			isReviewerRejection: true,
			want:                mrtelemetry.FailureSubstantive,
		},
		{
			name:                "test failure is deterministic validation",
			result:              ProcessResult{TestsFailed: true},
			isReviewerRejection: false,
			want:                mrtelemetry.FailureValidation,
		},
		{
			name:                "merge slot timeout",
			result:              ProcessResult{SlotTimeout: true},
			isReviewerRejection: false,
			want:                mrtelemetry.FailureTimeout,
		},
		{
			name:                "convention failure (WIP commit)",
			result:              ProcessResult{ConventionFailed: true},
			isReviewerRejection: false,
			want:                mrtelemetry.FailureConvention,
		},
		{
			name:                "merge conflict",
			result:              ProcessResult{Conflict: true},
			isReviewerRejection: false,
			want:                mrtelemetry.FailureConflict,
		},
		{
			name:                "branch not found is infra",
			result:              ProcessResult{BranchNotFound: true},
			isReviewerRejection: false,
			want:                mrtelemetry.FailureInfra,
		},
		{
			name:                "non-terminal error string is infra",
			result:              ProcessResult{Error: "push failed: permission denied"},
			isReviewerRejection: false,
			want:                mrtelemetry.FailureInfra,
		},
		{
			name:                "successful merge has no failure class",
			result:              ProcessResult{Success: true, MergeCommit: "abc123"},
			isReviewerRejection: false,
			want:                mrtelemetry.FailureNone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyFailure(tt.result, tt.isReviewerRejection)
			if got != tt.want {
				t.Errorf("classifyFailure(%+v, reviewer=%v) = %q, want %q",
					tt.result, tt.isReviewerRejection, got, tt.want)
			}
		})
	}
}

// TestClassifyFailureReviewerPrecedence confirms a reviewer rejection wins the
// substantive classification even when other failure flags would otherwise map
// to validation/infra — because the reviewer verdict is the authoritative
// signal for model quality.
func TestClassifyFailureReviewerPrecedence(t *testing.T) {
	got := classifyFailure(ProcessResult{
		TestsFailed:    true,
		Conflict:       true,
		NeedsRework:    true,
		ReviewerRejectionCause: "missing_test",
	}, true)
	if got != mrtelemetry.FailureSubstantive {
		t.Errorf("reviewer rejection must take precedence: got %q, want %q", got, mrtelemetry.FailureSubstantive)
	}
}
