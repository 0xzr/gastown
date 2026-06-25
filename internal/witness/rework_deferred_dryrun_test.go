package witness

import (
	"testing"
)

// TestDryRunReworkDeferred verifies the regression dry-run scenario passes and
// populates the expected tuples. This is a coarse smoke test that proves the
// dry-run harness and the throttle agree on first-emit/suppress/rollup
// behavior for the acceptance-criteria tuples (gt-hold1, gt-park1, gt-work999).
func TestDryRunReworkDeferred(t *testing.T) {
	result, err := DryRunReworkDeferred()
	if err != nil {
		t.Fatalf("DryRunReworkDeferred failed: %v", err)
	}
	if !result.Pass {
		t.Fatalf("dry run did not pass: %v", result.Errors)
	}

	if len(result.Tuples) != 3 {
		t.Fatalf("expected 3 tuples, got %d", len(result.Tuples))
	}

	for _, tup := range result.Tuples {
		if tup.FirstAction != ActionEmit {
			t.Errorf("%s: first action = %s, want emit", tup.Bead, tup.FirstAction)
		}
		if tup.RepeatAction != ActionSuppress {
			t.Errorf("%s: repeat action = %s, want suppress", tup.Bead, tup.RepeatAction)
		}
		if tup.RollupAction != ActionRollup {
			t.Errorf("%s: rollup action = %s, want rollup", tup.Bead, tup.RollupAction)
		}
		if tup.SuppressedCount != 10 {
			t.Errorf("%s: suppressed count = %d, want 10", tup.Bead, tup.SuppressedCount)
		}
		// The rollup must report the real suppressed count (10), not the
		// just-reset durable zero. This is the gastown-3ip regression: the
		// dry run previously asserted only the rollup action, so a false-green
		// "0 suppressed" rollup passed the dry run. Pin the returned count.
		if tup.RollupSuppressedCount != 10 {
			t.Errorf("%s: rollup suppressed count = %d, want 10 (real count, not 0)",
				tup.Bead, tup.RollupSuppressedCount)
		}
		if tup.RollupSuppressedCount != tup.SuppressedCount {
			t.Errorf("%s: rollup suppressed (%d) != suppressed (%d); rollup must carry the real count",
				tup.Bead, tup.RollupSuppressedCount, tup.SuppressedCount)
		}
	}
}
