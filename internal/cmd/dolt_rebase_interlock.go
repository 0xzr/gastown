package cmd

// dolt_rebase_interlock.go implements the dolt_branch_control-based global
// writer interlock that closes the TOCTOU window between the pre-rebase HEAD
// check and the destructive `DOLT_BRANCH -D main + -m compact-work → main`
// swap in the surgical rebase flow.
//
// Background: gastown-5nz retro-bug P0 — Dolt's `DOLT_BRANCH -f -m` is NOT
// atomic (verified against Dolt 0.40.5 source: `actions.RenameBranch` is
// `CopyBranchOnDB` + `CopyWorkingSet` + `DeleteBranch` per the in-source
// `// TODO: This function smears the branch updates across multiple commits
// of the datas.Database.` comment in env/actions/branch.go:40). There is no
// `expected-old-commit` / `prevHash` parameter at the SQL procedure level.
// The destructive swap can therefore lose data if any non-lock-holding writer
// advances main between the HEAD check and the swap.
//
// Solution: `dolt_branch_control` is a SQL-writable system table whose
// `CheckAccess` is invoked at every write site (DOLT_COMMIT, DOLT_RESET,
// DOLT_REBASE, DOLT_PUSH, DOLT_MERGE, DOLT_CHECKOUT, DOLT_ADD, DOLT_RM, ...).
// By holding a per-(database, branch, user, host) permission revocation
// during the rebase window, Dolt itself rejects any concurrent writer that
// tries to commit to `main` — no application-level bypass is possible.
//
// Design decision recorded in bead gastown-d88 (--design field).

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// branchControlRow is a snapshot of one row in dolt_branch_control for the
// target database, used to restore state after the interlock is released.
type branchControlRow struct {
	database    string
	branch      string
	user        string
	host        string
	permissions string
}

// acquireRebaseInterlock revokes write permission on `main` for everyone
// except the calling session's (user, host), and grants that session
// `admin` so it can perform the destructive swap. Returns a snapshot of
// the original rows so the caller can restore them on exit.
//
// The most-specific row wins per Access.Match, so the explicit
// (user, host) admin row beats the wildcard (%@%) read row for the
// rebase session while still denying every other (user, host) tuple.
//
// Safe to call from any goroutine; serializes via Dolt's internal
// controller mutex on dolt_branch_control writes.
func acquireRebaseInterlock(ctx context.Context, db *sql.DB, dbName string) (snapshot []branchControlRow, releaseFn func() error, err error) {
	// 1. Snapshot existing rows for this database.
	rows, err := db.QueryContext(ctx, fmt.Sprintf(
		"SELECT `database`, branch, user, host, permissions FROM `%s`.dolt_branch_control WHERE `database` = ? OR `database` = '*' OR `database` = '%%'",
		dbName), dbName)
	if err != nil {
		return nil, nil, fmt.Errorf("snapshot dolt_branch_control: %w", err)
	}
	for rows.Next() {
		var r branchControlRow
		if err := rows.Scan(&r.database, &r.branch, &r.user, &r.host, &r.permissions); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("scan branch_control row: %w", err)
		}
		snapshot = append(snapshot, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate branch_control: %w", err)
	}

	// 2. Determine our (user, host) so we can grant ourselves admin.
	var currentUser string
	if err := db.QueryRowContext(ctx, "SELECT CURRENT_USER()").Scan(&currentUser); err != nil {
		return nil, nil, fmt.Errorf("detect current_user: %w", err)
	}
	user, host, err := splitUserHost(currentUser)
	if err != nil {
		return nil, nil, fmt.Errorf("parse current_user %q: %w", currentUser, err)
	}

	// 3. Wipe existing rows for branch='main' on this database. We keep the
	//    caller session's specific row (if any) so admin grants aren't
	//    clobbered, then replace the rest with a single deny row.
	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		"DELETE FROM `%s`.dolt_branch_control WHERE branch = 'main' AND NOT (user = ? AND host = ?)",
		dbName), user, host); err != nil {
		return nil, nil, fmt.Errorf("delete existing main rows: %w", err)
	}

	// 4. Insert two rows: deny default + admin grant for our session.
	//    Upsert via REPLACE to be idempotent across re-acquisitions.
	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		"REPLACE INTO `%s`.dolt_branch_control (`database`, branch, user, host, permissions) VALUES (?, 'main', ?, ?, 'admin'), (?, 'main', '%%', '%%', 'read')",
		dbName), dbName, user, host, dbName); err != nil {
		return nil, nil, fmt.Errorf("install interlock rows: %w", err)
	}

	// 5. Brief drain — best-effort; authoritative gate is the post-acquire
	//    preHead re-check. New transactions will see the new permissions at
	//    their next CheckAccess call; in-flight ones may complete, hence
	//    the re-check.
	time.Sleep(100 * time.Millisecond)

	// Build releaseFn that restores the snapshot.
	releaseFn = func() error {
		// Delete the two rows we inserted.
		if _, err := db.ExecContext(ctx, fmt.Sprintf(
			"DELETE FROM `%s`.dolt_branch_control WHERE branch = 'main'",
			dbName)); err != nil {
			return fmt.Errorf("delete interlock rows: %w", err)
		}
		// Restore snapshot rows.
		for _, r := range snapshot {
			if r.branch != "main" {
				continue // Only restore main rows we may have clobbered
			}
			if _, err := db.ExecContext(ctx, fmt.Sprintf(
				"REPLACE INTO `%s`.dolt_branch_control (`database`, branch, user, host, permissions) VALUES (?, ?, ?, ?, ?)",
				dbName), r.database, r.branch, r.user, r.host, r.permissions); err != nil {
				return fmt.Errorf("restore row (%s,%s,%s,%s): %w",
					r.database, r.branch, r.user, r.host, err)
			}
		}
		return nil
	}

	return snapshot, releaseFn, nil
}

// splitUserHost parses a MySQL CURRENT_USER() result like 'root@localhost'
// or 'root@%' into its user and host components. Empty host defaults to '%'.
func splitUserHost(s string) (user, host string, err error) {
	if s == "" {
		return "", "", fmt.Errorf("empty CURRENT_USER")
	}
	at := strings.LastIndex(s, "@")
	if at < 0 {
		// No '@' — treat whole string as user, host='%'.
		return s, "%", nil
	}
	return s[:at], s[at+1:], nil
}

// verifyMainHeadUnchanged reads main's HEAD commit hash and returns an
// error if it does not match the expected preHead. This is the authoritative
// concurrency gate that runs BOTH immediately after the interlock is
// acquired (to catch in-flight transactions that completed during the drain)
// AND immediately before the destructive swap.
func verifyMainHeadUnchanged(ctx context.Context, db *sql.DB, dbName, preHead string) error {
	var head string
	if err := db.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT commit_hash FROM `%s`.dolt_log ORDER BY date DESC LIMIT 1", dbName)).Scan(&head); err != nil {
		return fmt.Errorf("read main HEAD: %w", err)
	}
	if head != preHead {
		return fmt.Errorf("main HEAD advanced during rebase (expected %s, got %s) — fail-closed abort", shortHash(preHead), shortHash(head))
	}
	return nil
}
