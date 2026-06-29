package doltserver

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"
)

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
// operations run on the same database connection. The easiest way is to call
// db.SetMaxOpenConns(1) on a freshly opened *sql.DB before invoking this
// function, or to pass a *sql.Conn obtained with db.Conn(ctx).
//
// The returned release function should be deferred by the caller. It is
// idempotent and safe to call on an already released or broken connection.
func AcquireRebaseLock(ctx context.Context, db *sql.DB, dbName string, timeout time.Duration) (func(context.Context), error) {
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
		return nil, fmt.Errorf("rebase lock %s is held by another process (GET_LOCK returned %d)", lockName, got)
	}

	release := func(ctx context.Context) {
		_, _ = db.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", lockName)
	}
	return release, nil
}
