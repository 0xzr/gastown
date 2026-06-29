package doltserver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
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

// TestAcquireRebaseLock_AcquiresAndReleases verifies that a successful GET_LOCK
// produces a release function that issues RELEASE_LOCK on the same connection.
func TestAcquireRebaseLock_AcquiresAndReleases(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT GET_LOCK\\(\\?, \\?\\)").
		WithArgs("gt:dolt:rebase:gastown", 5).
		WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(1))
	mock.ExpectExec("SELECT RELEASE_LOCK\\(\\?\\)").
		WithArgs("gt:dolt:rebase:gastown").
		WillReturnResult(sqlmock.NewResult(0, 0))

	release, err := AcquireRebaseLock(context.Background(), db, "gastown", 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	release(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestAcquireRebaseLock_Timeout verifies that GET_LOCK returning 0 is reported
// as ErrRebaseLockTimeout.
func TestAcquireRebaseLock_Timeout(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT GET_LOCK\\(\\?, \\?\\)").
		WithArgs("gt:dolt:rebase:beads", 30).
		WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(0))

	_, err = AcquireRebaseLock(context.Background(), db, "beads", 30*time.Second)
	if !errors.Is(err, ErrRebaseLockTimeout) {
		t.Fatalf("expected ErrRebaseLockTimeout, got %v", err)
	}
	if !strings.Contains(err.Error(), "gt:dolt:rebase:beads") {
		t.Fatalf("error should mention lock name, got %q", err.Error())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestAcquireRebaseLock_UnexpectedReturn verifies that any GET_LOCK value other
// than 0 or 1 is reported as a non-timeout error.
func TestAcquireRebaseLock_UnexpectedReturn(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT GET_LOCK\\(\\?, \\?\\)").
		WithArgs("gt:dolt:rebase:hq", 1).
		WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(9))

	_, err = AcquireRebaseLock(context.Background(), db, "hq", 1*time.Second)
	if err == nil {
		t.Fatal("expected error for unexpected GET_LOCK return")
	}
	if errors.Is(err, ErrRebaseLockTimeout) {
		t.Fatal("unexpected timeout error for return value 9")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestAcquireRebaseLock_QueryError verifies that errors from the underlying
// GET_LOCK query are propagated.
func TestAcquireRebaseLock_QueryError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	defer db.Close()

	wantErr := errors.New("connection refused")
	mock.ExpectQuery("SELECT GET_LOCK\\(\\?, \\?\\)").
		WithArgs("gt:dolt:rebase:gastown", 1).
		WillReturnError(wantErr)

	_, err = AcquireRebaseLock(context.Background(), db, "gastown", 1*time.Second)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "GET_LOCK") {
		t.Fatalf("error should mention GET_LOCK, got %q", err.Error())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestAcquireRebaseLock_NegativeTimeout Forever verifies that a negative
// timeout is converted to GET_LOCK's "wait forever" value (-1).
func TestAcquireRebaseLock_NegativeTimeoutForever(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("open sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT GET_LOCK\\(\\?, \\?\\)").
		WithArgs("gt:dolt:rebase:gastown", -1).
		WillReturnRows(sqlmock.NewRows([]string{"got"}).AddRow(1))

	release, err := AcquireRebaseLock(context.Background(), db, "gastown", -1*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	release(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
