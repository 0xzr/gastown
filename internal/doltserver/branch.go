package doltserver

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const branchOperationTimeout = 10 * time.Second

// BranchHead returns the HEAD commit hash of the named branch in dbName.
// It reads directly from the dolt_branches system table, which is branch-specific
// and avoids the TOCTOU hazard of inspecting the current session branch via
// dolt_log.
func BranchHead(ctx context.Context, db *sql.DB, dbName, branch string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, branchOperationTimeout)
	defer cancel()

	query := fmt.Sprintf("SELECT hash FROM `%s`.dolt_branches WHERE name = ?", dbName)
	var head string
	if err := db.QueryRowContext(ctx, query, branch).Scan(&head); err != nil {
		return "", fmt.Errorf("get HEAD of branch %q in %q: %w", branch, dbName, err)
	}
	return head, nil
}

// TrySwapBranch performs an atomic compare-and-swap on a Dolt branch ref.
// It updates the named branch to newHash only if its current hash equals
// expectedHash. The operation is a single UPDATE against the dolt_branches
// system table executed inside a transaction, so there is no check-then-act
// window: a concurrent write that changes the branch between our read and the
// update will cause the WHERE clause to match zero rows and the function will
// return (false, nil). Callers should treat that as a concurrency collision and
// retry/abort rather than data loss.
func TrySwapBranch(ctx context.Context, db *sql.DB, dbName, branch, expectedHash, newHash string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, branchOperationTimeout)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin branch swap tx: %w", err)
	}
	defer tx.Rollback()

	query := fmt.Sprintf("UPDATE `%s`.dolt_branches SET hash = ? WHERE name = ? AND hash = ?", dbName)
	res, err := tx.ExecContext(ctx, query, newHash, branch, expectedHash)
	if err != nil {
		return false, fmt.Errorf("update branch %q in %q: %w", branch, dbName, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected for branch swap: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit branch swap: %w", err)
	}

	return affected > 0, nil
}
