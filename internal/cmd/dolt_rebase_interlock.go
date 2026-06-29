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
	"os"
	"strings"
	"time"
)

// interlockReleaseTimeout bounds the cleanup window when restoring
// dolt_branch_control rows. The release path MUST use a fresh context
// (not the operation context, which may have been cancelled or expired
// during a long rebase) — otherwise the row-restore can fail and leave
// main write-perms stuck at read-only, blocking all subsequent writers.
const interlockReleaseTimeout = 30 * time.Second

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
// DEPLOYMENT PREREQUISITE (enforced — see shared-root detection below):
// Access.Match is per-(database, branch, user, host) — NOT per-session. If
// the rebase session and a concurrent writer share (user, host), the admin
// grant inadvertently covers the writer and the interlock silently fails
// open. Production deployments MUST provision a dedicated MySQL user
// (proposed: `gtrebase`) for the rebase command. Polecats, refinery,
// deacon, witness, mayor, and plugins connect as distinct users.
//
// Safe to call from any goroutine; serializes via Dolt's internal
// controller mutex on dolt_branch_control writes.
func acquireRebaseInterlock(ctx context.Context, db *sql.DB, dbName string) (snapshot []branchControlRow, releaseFn func() error, err error) {
	// 1. Snapshot existing rows for THIS database only. Per-dolt_branch_control
	// is per-database — other DBs' tables are not touched and need not be
	// snapshotted. The snapshot covers both rows targeted at `dbName` and any
	// global wildcard rows (database='*' or '%') that apply here, so restore
	// can put back exactly what we delete.
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

	// 2a. SHARED-ROOT BYPASS GUARD (gastown-d88 finding #3).
	//
	// Access.Match is per-(database, branch, user, host) with no session
	// discrimination. If we are connecting as a privileged system account
	// (`root`, `dolt`, `mysql`) from a wildcard or localhost host, every
	// other writer sharing that account would inherit our admin grant,
	// silently defeating the interlock. Refuse unless explicitly overridden
	// via env var (tests use this escape hatch; production deployments must
	// provision a dedicated user).
	if err := checkSharedRootBypass(user, host); err != nil {
		return nil, nil, err
	}

	// 3. Wipe existing rows for branch='main' SCOPED TO THIS DATABASE.
	// Cross-DB ACL damage fix (gastown-d88 finding #2): the previous
	// implementation filtered only on branch='main', which (within
	// dbName's table) deleted both per-dbName rows AND any global '*' / '%'
	// rows — and on other DBs sharing the same Dolt server, an unrelated
	// global main row could be wiped because the snapshot only covered our
	// DB. Now we scope strictly by `database = ?`: only rows whose database
	// column matches dbName (the row we own for the interlock) are deleted.
	// Global wildcard rows in this DB are also kept (they apply to all
	// databases and would need a coordinated multi-DB restore — out of
	// scope for surgical rebase).
	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		"DELETE FROM `%s`.dolt_branch_control WHERE `database` = ? AND branch = 'main' AND NOT (user = ? AND host = ?)",
		dbName), dbName, user, host); err != nil {
		return nil, nil, fmt.Errorf("delete existing main rows: %w", err)
	}

	// 4. Insert two rows: admin grant for our session + read-only for everyone else.
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
	//
	// DEAD-CONTEXT FIX (gastown-d88 finding #4): the operation context
	// `ctx` may have expired or been cancelled by the time this runs (long
	// rebase, parent cancellation, deadline exceeded). If release used
	// that dead context, the row-restore ExecContext calls would fail
	// silently — leaving dolt_branch_control stuck at our deny-everyone
	// state and blocking every subsequent main writer. Use a fresh context
	// with its own bounded timeout so release is robust to op-context
	// failure.
	releaseFn = func() error {
		relCtx, cancel := context.WithTimeout(context.Background(), interlockReleaseTimeout)
		defer cancel()

		// Delete the two rows we inserted. SCOPED BY DATABASE so we only
		// touch the rows for dbName — never delete another DB's main rows.
		if _, err := db.ExecContext(relCtx, fmt.Sprintf(
			"DELETE FROM `%s`.dolt_branch_control WHERE `database` = ? AND branch = 'main'",
			dbName), dbName); err != nil {
			return fmt.Errorf("delete interlock rows: %w", err)
		}
		// Restore snapshot rows that targeted `dbName` and were branch='main'.
		// Global wildcard rows (database='*' or '%') in the snapshot are
		// restored only if they had branch='main' AND were in dbName's
		// table — which is the case since the snapshot reads from
		// dbName.dolt_branch_control. This preserves any pre-existing
		// global main grants while restoring the per-DB state.
		for _, r := range snapshot {
			if r.branch != "main" {
				continue
			}
			if r.database != dbName {
				continue // Don't restore wildcard rows we didn't delete.
			}
			if _, err := db.ExecContext(relCtx, fmt.Sprintf(
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

// checkSharedRootBypass returns an error if (user, host) describes a
// privileged system account that concurrent writers might also be using —
// the deployment prerequisite for the interlock to be effective.
//
// The set of "privileged system accounts" covers the common default Dolt
// admin accounts. The host must also be a wildcard or localhost for the
// gate to be bypassable (a unique host like a dedicated socket path is
// fine). Escape hatch: GT_DOLT_REBASE_ALLOW_SHARED_ROOT=1 skips this check
// for tests and emergency overrides.
func checkSharedRootBypass(user, host string) error {
	if user != "root" && user != "dolt" && user != "mysql" {
		return nil
	}
	if host != "%" && host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return nil
	}
	if os.Getenv("GT_DOLT_REBASE_ALLOW_SHARED_ROOT") == "1" {
		return nil
	}
	return fmt.Errorf(
		"refusing to acquire rebase interlock as %s@%s — Access.Match is per-(database, branch, user, host), so a shared Dolt admin account lets concurrent writers inherit our admin grant and silently bypass the gate (gastown-d88 design / gastown-5nz retro-bug P0). "+
			"Provision a dedicated MySQL user for the rebase command (e.g. gtrebase@localhost with GRANT ALL) per the gastown-d88 design; "+
			"set GT_DOLT_REBASE_ALLOW_SHARED_ROOT=1 to override this guard for tests only",
		user, host)
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
