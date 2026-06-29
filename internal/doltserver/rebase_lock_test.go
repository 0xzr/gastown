package doltserver

import (
	"strings"
	"testing"
)

// TestRebaseLockName verifies that the advisory lock name is deterministic
// and includes the database name. This is the regression guard for the
// cross-process locking contract: the CLI `gt dolt rebase` and the daemon
// compactor must agree on the lock name for a given database.
func TestRebaseLockName(t *testing.T) {
	cases := []struct {
		dbName string
		want   string
	}{
		{"gastown", "gt:dolt:rebase:gastown"},
		{"beads", "gt:dolt:rebase:beads"},
		{"hq", "gt:dolt:rebase:hq"},
		{"my-db_2", "gt:dolt:rebase:my-db_2"},
	}
	for _, tc := range cases {
		t.Run(tc.dbName, func(t *testing.T) {
			if got := RebaseLockName(tc.dbName); got != tc.want {
				t.Errorf("RebaseLockName(%q) = %q, want %q", tc.dbName, got, tc.want)
			}
		})
	}
}

// TestRebaseLockNameUniqueness is a sanity check that databases with different
// names do not collide on the same lock.
func TestRebaseLockNameUniqueness(t *testing.T) {
	names := []string{"gastown", "beads", "hq", "gastown-suffix", "gastown_tmp"}
	seen := make(map[string]string, len(names))
	for _, dbName := range names {
		lock := RebaseLockName(dbName)
		if other, ok := seen[lock]; ok {
			t.Errorf("collision: %q and %q both map to lock %q", other, dbName, lock)
		}
		seen[lock] = dbName
	}
}

// TestRebaseLockNamePrefix ensures the lock name uses a project-scoped prefix
// so it cannot collide with application/user advisory locks.
func TestRebaseLockNamePrefix(t *testing.T) {
	lock := RebaseLockName("gastown")
	if !strings.HasPrefix(lock, "gt:dolt:rebase:") {
		t.Errorf("RebaseLockName(%q) = %q, want prefix %q", "gastown", lock, "gt:dolt:rebase:")
	}
}
