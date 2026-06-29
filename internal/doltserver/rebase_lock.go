package doltserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// ErrRebaseLockTimeout is returned when the advisory lock cannot be obtained
// before the caller-supplied timeout expires.
var ErrRebaseLockTimeout = errors.New("rebase advisory lock timeout")

// rebaseLockRunner is the minimal database surface needed to acquire and release
// a MySQL advisory lock. *sql.DB, *sql.Conn and *sql.Tx all satisfy it, so
// callers can bind the lock to a session-scoped connection.
type rebaseLockRunner interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// RebaseLockName returns the shared MySQL advisory lock name used to serialize
// surgical rebase operations for a single Dolt database.
//
// The same name is used by both the manual `gt dolt rebase` command and the
// daemon compactor so that they cannot step on each other's shared temporary
// branches (compact-base / compact-work) or rebase state.
func RebaseLockName(dbName string) string {
	return fmt.Sprintf("gt:dolt:rebase:%s", dbName)
}

// AcquireRebaseLock acquires a per-database MySQL advisory lock via GET_LOCK.
//
// The lock is session-scoped, so callers must ensure that all subsequent
// operations run on the same database connection. The safest way is to pass a
// *sql.Conn obtained with db.Conn(ctx) for the lifetime of the operation, or to
// call db.SetMaxOpenConns(1) on a freshly opened *sql.DB so every operation
// reuses the single pooled connection.
//
// The returned release function should be deferred by the caller. It is
// idempotent and safe to call on an already released or broken connection.
func AcquireRebaseLock(ctx context.Context, db rebaseLockRunner, dbName string, timeout time.Duration) (func(context.Context), error) {
	lockName := RebaseLockName(dbName)

	// GET_LOCK takes an integer timeout in seconds, so round the duration
	// rather than silently truncating sub-second values.
	timeoutSec := int(math.Round(timeout.Seconds()))
	if timeoutSec < 0 {
		// GET_LOCK timeout of -1 means wait forever.
		timeoutSec = -1
	}

	var got int64
	if err := db.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", lockName, timeoutSec).Scan(&got); err != nil {
		return nil, fmt.Errorf("GET_LOCK %s: %w", lockName, err)
	}
	if got != 1 {
		if got == 0 {
			return nil, fmt.Errorf("%w: %s is held by another process", ErrRebaseLockTimeout, lockName)
		}
		return nil, fmt.Errorf("rebase lock %s unavailable (GET_LOCK returned %d)", lockName, got)
	}

	release := func(ctx context.Context) {
		_, _ = db.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", lockName)
	}
	return release, nil
}
