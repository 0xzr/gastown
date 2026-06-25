package convoy

import "testing"

// TestIsActiveJSONStatus pins the active-status predicate used by both
// `gt convoy stranded --json` (cmd/convoy.go) and the daemon's
// ConvoyManager.scan (daemon/convoy_manager.go). The daemon must NEVER
// feed or completion-check a convoy whose status is anything other than
// "open" (or empty for legacy compatibility).
//
// See gastown-cet.14.
func TestIsActiveJSONStatus(t *testing.T) {
	cases := []struct {
		status string
		want   bool
		why    string
	}{
		// Active — only "open" is active.
		{"open", true, "open convoys are active scan targets"},

		// Legacy / unknown — empty status treated as active for backward
		// compatibility with older `gt convoy stranded --json` producers.
		{"", true, "empty status is treated as legacy/unknown and active"},

		// Inactive — anything that means "do not scan this convoy".
		{"closed", false, "closed convoys must never be fed or checked"},
		{"tombstone", false, "tombstoned convoys are terminal"},
		{"deferred", false, "deferred convoys are explicitly paused"},
		{"staged_ready", false, "staged convoys have not been launched yet"},
		{"staged_warnings", false, "staged convoys with warnings are not yet launched"},
		{"in_progress", false, "in_progress is not a valid convoy state but must be safe to skip"},
		{"blocked", false, "blocked is not a valid convoy state but must be safe to skip"},
		{"pinned", false, "pinned is not a valid convoy state but must be safe to skip"},
		{"hooked", false, "hooked is not a valid convoy state but must be safe to skip"},

		// Future / unknown statuses must be conservative (skip).
		{"custom_status", false, "unknown future statuses must default to inactive"},
		{"OPEN", false, "status comparisons must be case-sensitive"},
		{" open ", false, "whitespace must not fool the predicate"},
	}

	for _, c := range cases {
		got := IsActiveJSONStatus(c.status)
		if got != c.want {
			t.Errorf("IsActiveJSONStatus(%q) = %v, want %v (%s)", c.status, got, c.want, c.why)
		}
	}
}

func TestIsActiveBeadsStatus(t *testing.T) {
	// The SDK-side counterpart to IsActiveJSONStatus must agree on the
	// "open" verdict and the conservative default for unknown values.
	if !IsActiveBeadsStatus("open") {
		t.Error(`IsActiveBeadsStatus("open") must return true`)
	}
	if !IsActiveBeadsStatus("") {
		t.Error(`IsActiveBeadsStatus("") must return true (empty = unknown)`)
	}
	if IsActiveBeadsStatus("closed") {
		t.Error(`IsActiveBeadsStatus("closed") must return false`)
	}
	if IsActiveBeadsStatus("deferred") {
		t.Error(`IsActiveBeadsStatus("deferred") must return false`)
	}
	if IsActiveBeadsStatus("staged_ready") {
		t.Error(`IsActiveBeadsStatus("staged_ready") must return false`)
	}
}
