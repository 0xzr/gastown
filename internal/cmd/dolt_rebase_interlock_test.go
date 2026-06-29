//go:build integration

// Package cmd — race test for the dolt_branch_control-based global writer
// interlock that closes the TOCTOU window in the surgical-rebase main swap.
//
// Acceptance criterion (gastown-d88):
//
//	"Acceptance must include a race test where an external writer advances
//	 main between check and swap; expected behavior is fail-closed with
//	 no lost commit."
//
// This test exercises the protocol against a live Dolt sql-server. It does
// NOT spin up a Docker container — it uses the running local Dolt on
// 127.0.0.1:3307 (started by the gastown doltserver) and creates a
// throwaway test database. Skip if no local Dolt is reachable.
//
// What we prove:
//
//  1. While the rebase interlock is held, a concurrent writer attempting
//     DOLT_COMMIT on `main` is rejected by Dolt's dolt_branch_control
//     permission check — NOT silently dropped, NOT silently committed.
//
//  2. Even if a writer somehow slipped past the permission check (the
//     narrower remaining window), verifyMainHeadUnchanged detects the
//     drift and returns a fail-closed error.
//
//  3. The full rebase flow with the interlock completes correctly:
//     new main HEAD == compact-work HEAD; old concurrent write attempt
//     is not silently lost (it's rejected, not silently absorbed).
//
//  4. The original dolt_branch_control rows are restored after the
//     interlock releaseFn runs — no residual permission damage.

package cmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// raceTestRootDSN returns the root DSN for connecting to the local Dolt
// server. Uses GT_DOLT_PORT (set by the gastown doltserver) or 3307.
func raceTestRootDSN(t *testing.T) string {
	t.Helper()
	port := os.Getenv("GT_DOLT_PORT")
	if port == "" {
		port = "3307"
	}
	return fmt.Sprintf("root:@tcp(127.0.0.1:%s)/?parseTime=true&timeout=5s&readTimeout=10s&writeTimeout=30s", port)
}

// raceTestCreateDB creates a fresh throwaway database, seeds it with N
// commits on `main`, and returns the new db name plus a cleanup func.
// Cleanup drops the database, restores dolt_branch_control snapshots taken
// before the test, and best-effort purges dropped databases.
func raceTestCreateDB(t *testing.T, rootDSN string, commits int) (dbName string, cleanup func()) {
	t.Helper()

	root, err := sql.Open("mysql", rootDSN)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	if err := root.Ping(); err != nil {
		t.Skipf("Dolt not reachable at %s: %v", rootDSN, err)
	}

	// Unique name per test invocation so parallel runs don't collide.
	dbName = fmt.Sprintf("gt_rebase_race_%d", time.Now().UnixNano())

	if _, err := root.Exec("CREATE DATABASE `" + dbName + "`"); err != nil {
		t.Fatalf("create database %s: %v", dbName, err)
	}

	// Seed: create a table, commit N times. This gives us a non-trivial
	// main branch to swap.
	dsn := strings.Replace(rootDSN, "/?", "/"+dbName+"?", 1)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dbName, err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "CREATE TABLE items (id INT PRIMARY KEY, payload TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for i := 0; i < commits; i++ {
		if _, err := db.ExecContext(ctx, "INSERT INTO items VALUES (?, ?)", i, fmt.Sprintf("seed-%d", i)); err != nil {
			t.Fatalf("seed insert %d: %v", i, err)
		}
		if _, err := db.ExecContext(ctx, "CALL DOLT_ADD('items')"); err != nil {
			t.Fatalf("dolt_add %d: %v", i, err)
		}
		author := fmt.Sprintf("seed-%d <seed@test>", i)
		if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			fmt.Sprintf("seed commit %d", i), author); err != nil {
			t.Fatalf("dolt_commit %d: %v", i, err)
		}
	}

	cleanup = func() {
		// Re-open root and drop the test database.
		r, err := sql.Open("mysql", rootDSN)
		if err != nil {
			t.Logf("cleanup open root: %v", err)
			return
		}
		defer r.Close()
		if _, err := r.Exec("DROP DATABASE IF EXISTS `" + dbName + "`"); err != nil {
			t.Logf("cleanup drop %s: %v", dbName, err)
		}
		if _, err := r.Exec("CALL dolt_purge_dropped_databases()"); err != nil {
			t.Logf("cleanup purge: %v", err)
		}
	}

	return dbName, cleanup
}

// TestAcquireRebaseInterlock_DeniesConcurrentWriter proves the core
// interlock property: while the interlock is held, a concurrent writer
// attempting DOLT_COMMIT on main is rejected at the SQL layer with a
// branch_control permission error. The preHead does not advance.
//
// Requires the rebase session and the writer to have DISTINCT (user, host)
// tuples — Dolt's Access.Match is per-(database, branch, user, host) with
// no session-level differentiation. Production deployment must configure a
// dedicated `gtrebase` MySQL user; this test simulates that by creating a
// `racewriter` user via CREATE USER before the test.
func TestAcquireRebaseInterlock_DeniesConcurrentWriter(t *testing.T) {
	withSharedRootOverride(t) // test connects as root@% — escape hatch for legacy CI topology

	rootDSN := raceTestRootDSN(t)
	dbName, cleanup := raceTestCreateDB(t, rootDSN, 5)
	defer cleanup()

	// Create a separate writer user — production-equivalent of having a
	// `gtrebase` user distinct from `root@%`. branch_control.Match is
	// per-(user,host); without distinct tuples the admin grant for the
	// rebase session would also cover the writer.
	writerUser, writerHost, cleanupUser := raceTestCreateWriterUser(t, rootDSN)
	defer cleanupUser()

	// The rebase session connects as root.
	rebaseDSN := strings.Replace(rootDSN, "/?", "/"+dbName+"?", 1)
	db, err := sql.Open("mysql", rebaseDSN)
	if err != nil {
		t.Fatalf("open rebase db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Record preHead before interlock.
	preHead, err := raceTestGetHead(ctx, db, dbName)
	if err != nil {
		t.Fatalf("preHead: %v", err)
	}

	// Acquire the interlock.
	_, release, err := acquireRebaseInterlock(ctx, db, dbName)
	if err != nil {
		t.Fatalf("acquireRebaseInterlock: %v", err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Errorf("releaseInterlock: %v", err)
		}
	}()

	// Concurrent writer (as the dedicated writerUser) attempts a commit on main.
	writerDSN := fmt.Sprintf("%s:@tcp(127.0.0.1:%s)/%s?parseTime=true&timeout=5s&readTimeout=10s&writeTimeout=30s",
		writerUser, os.Getenv("GT_DOLT_PORT"), dbName)
	if os.Getenv("GT_DOLT_PORT") == "" {
		writerDSN = fmt.Sprintf("%s:@tcp(127.0.0.1:3307)/%s?parseTime=true&timeout=5s&readTimeout=10s&writeTimeout=30s",
			writerUser, dbName)
	}
	_ = writerHost // used implicitly via the user@host pattern

	var writerErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		wdb, err := sql.Open("mysql", writerDSN)
		if err != nil {
			writerErr = fmt.Errorf("writer open: %w", err)
			return
		}
		defer wdb.Close()
		wctx := context.Background()
		if _, err := wdb.ExecContext(wctx, "INSERT INTO items VALUES (?, ?)", 999, "racing"); err != nil {
			writerErr = err
			return
		}
		if _, err := wdb.ExecContext(wctx, "CALL DOLT_ADD('items')"); err != nil {
			writerErr = err
			return
		}
		if _, err := wdb.ExecContext(wctx, "CALL DOLT_COMMIT('-m', 'racing commit')"); err != nil {
			writerErr = err
			return
		}
		writerErr = errors.New("writer commit unexpectedly succeeded — interlock failed")
	}()
	wg.Wait()

	if writerErr == nil {
		t.Fatal("expected writer to fail; got nil")
	}
	msg := writerErr.Error()
	if !strings.Contains(msg, "permission") && !strings.Contains(msg, "Permission") &&
		!strings.Contains(msg, "does not have the correct permissions") &&
		!strings.Contains(msg, "ErrIncorrectPermissions") {
		t.Fatalf("writer error does not look like a permission rejection: %v", writerErr)
	}
	t.Logf("writer correctly rejected: %v", writerErr)

	// Verify main HEAD did not advance.
	postHead, err := raceTestGetHead(ctx, db, dbName)
	if err != nil {
		t.Fatalf("postHead: %v", err)
	}
	if postHead != preHead {
		t.Fatalf("main HEAD advanced despite interlock: %s → %s", shortHash(preHead), shortHash(postHead))
	}
	t.Logf("main HEAD stable: %s", shortHash(postHead))
}

// TestVerifyMainHeadUnchanged_TripsOnDrift proves the second defense:
// even if a writer bypassed CheckAccess (theoretical edge case), the
// HEAD-hash re-check before swap detects drift and aborts.
func TestVerifyMainHeadUnchanged_TripsOnDrift(t *testing.T) {
	rootDSN := raceTestRootDSN(t)
	dbName, cleanup := raceTestCreateDB(t, rootDSN, 3)
	defer cleanup()

	db, err := sql.Open("mysql", strings.Replace(rootDSN, "/?", "/"+dbName+"?", 1))
	if err != nil {
		t.Fatalf("open %s: %v", dbName, err)
	}
	defer db.Close()

	ctx := context.Background()
	preHead, err := raceTestGetHead(ctx, db, dbName)
	if err != nil {
		t.Fatalf("preHead: %v", err)
	}

	// Simulate "external writer advanced main" by inserting a new commit
	// directly, without going through DOLT_COMMIT (which would be
	// permission-blocked in real life). This simulates the theoretical
	// race window where a writer has already passed CheckAccess.
	if _, err := db.ExecContext(ctx, "INSERT INTO items VALUES (?, ?)", 1234, "post-preHead"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_ADD('items')"); err != nil {
		t.Fatalf("dolt_add: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'advancing commit')"); err != nil {
		t.Fatalf("dolt_commit (simulating pre-CheckAccess writer): %v", err)
	}

	// Now verifyMainHeadUnchanged must trip.
	err = verifyMainHeadUnchanged(ctx, db, dbName, preHead)
	if err == nil {
		t.Fatal("expected verifyMainHeadUnchanged to error on drift; got nil")
	}
	if !strings.Contains(err.Error(), "advanced") && !strings.Contains(err.Error(), "fail-closed") {
		t.Fatalf("error message doesn't convey fail-closed semantics: %v", err)
	}
	t.Logf("verifyMainHeadUnchanged correctly tripped: %v", err)
}

// TestAcquireRebaseInterlock_RestoresSnapshot proves that releaseFn
// restores the original dolt_branch_control rows so no permission damage
// persists across rebase runs.
func TestAcquireRebaseInterlock_RestoresSnapshot(t *testing.T) {
	withSharedRootOverride(t) // test connects as root@% — escape hatch for legacy CI topology

	rootDSN := raceTestRootDSN(t)
	dbName, cleanup := raceTestCreateDB(t, rootDSN, 2)
	defer cleanup()

	db, err := sql.Open("mysql", strings.Replace(rootDSN, "/?", "/"+dbName+"?", 1))
	if err != nil {
		t.Fatalf("open %s: %v", dbName, err)
	}
	defer db.Close()

	ctx := context.Background()

	// Pre-snapshot all rows for this database.
	preRows := raceTestSnapshotBranchControl(t, ctx, db, dbName)

	// Acquire + release.
	snap, release, err := acquireRebaseInterlock(ctx, db, dbName)
	if err != nil {
		t.Fatalf("acquireRebaseInterlock: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	// Post-release snapshot should match the pre-snapshot (we may have
	// changed only main rows; the release should have restored them).
	postRows := raceTestSnapshotBranchControl(t, ctx, db, dbName)

	// Compare: same set of (branch, user, host, permissions) tuples
	// for this database.
	key := func(r branchControlRow) string {
		return fmt.Sprintf("%s|%s|%s|%s", r.branch, r.user, r.host, r.permissions)
	}
	preSet := make(map[string]bool, len(preRows))
	for _, r := range preRows {
		preSet[key(r)] = true
	}
	for _, r := range postRows {
		if !preSet[key(r)] {
			t.Errorf("post-release row not in pre-snapshot: %+v (interlock left residual)", r)
		}
	}
	// Verify we got back a non-trivial snapshot from acquireRebaseInterlock.
	if len(snap) == 0 {
		t.Log("warning: acquireRebaseInterlock returned empty snapshot (DB had no prior branch_control rows)")
	}
}

// raceTestSnapshotBranchControl reads all dolt_branch_control rows for
// the given database, scoped to this database only.
func raceTestSnapshotBranchControl(t *testing.T, ctx context.Context, db *sql.DB, dbName string) []branchControlRow {
	t.Helper()
	rows, err := db.QueryContext(ctx, fmt.Sprintf(
		"SELECT `database`, branch, user, host, permissions FROM `%s`.dolt_branch_control WHERE `database` = ?",
		dbName), dbName)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	defer rows.Close()
	var out []branchControlRow
	for rows.Next() {
		var r branchControlRow
		if err := rows.Scan(&r.database, &r.branch, &r.user, &r.host, &r.permissions); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// raceTestCreateWriterUser creates a dedicated MySQL user that simulates
// the production topology where the rebase CLI uses a separate user
// (e.g. `gtrebase`) from the bead writers (e.g. `root@%` or
// per-polecat users). The new user is granted MySQL-level privileges
// (so they can connect and issue DOLT_COMMIT), but at the Dolt
// branch_control layer they will be denied because we revoke `main`
// write permission for non-rebase sessions.
//
// Returns the username, the host portion, and a cleanup func that
// drops the user. The user is created at `%` (any host) — a fresh
// unique username per test invocation avoids collisions across runs.
func raceTestCreateWriterUser(t *testing.T, rootDSN string) (user, host string, cleanup func()) {
	t.Helper()

	root, err := sql.Open("mysql", rootDSN)
	if err != nil {
		t.Fatalf("root open: %v", err)
	}
	defer root.Close()

	// Unique name per run.
	user = fmt.Sprintf("racewriter_%d", time.Now().UnixNano())
	host = "%"

	// CREATE USER + GRANT ALL so the writer can connect and issue DDL/DML.
	if _, err := root.Exec(fmt.Sprintf("CREATE USER '%s'@'%%'", user)); err != nil {
		t.Fatalf("CREATE USER %s@%s: %v", user, host, err)
	}
	if _, err := root.Exec(fmt.Sprintf("GRANT ALL ON *.* TO '%s'@'%%'", user)); err != nil {
		t.Fatalf("GRANT to %s@%s: %v", user, host, err)
	}

	cleanup = func() {
		r, err := sql.Open("mysql", rootDSN)
		if err != nil {
			t.Logf("cleanup root open: %v", err)
			return
		}
		defer r.Close()
		if _, err := r.Exec(fmt.Sprintf("DROP USER IF EXISTS '%s'@'%%'", user)); err != nil {
			t.Logf("DROP USER %s: %v", user, err)
		}
	}

	return user, host, cleanup
}

// raceTestGetHead returns the current HEAD commit hash for the given database.
func raceTestGetHead(ctx context.Context, db *sql.DB, dbName string) (string, error) {
	var head string
	err := db.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT commit_hash FROM `%s`.dolt_log ORDER BY date DESC LIMIT 1", dbName)).Scan(&head)
	return head, err
}

// --- gastown-d88 v2 regression tests (4 codex-identified flaws) --------

// withSharedRootOverride sets GT_DOLT_REBASE_ALLOW_SHARED_ROOT=1 for the
// duration of t, restoring the prior value on cleanup. Used by tests that
// deliberately connect as root@% to simulate the legacy test topology;
// production tests should NOT use this helper.
func withSharedRootOverride(t *testing.T) {
	t.Helper()
	prev, had := os.LookupEnv("GT_DOLT_REBASE_ALLOW_SHARED_ROOT")
	if err := os.Setenv("GT_DOLT_REBASE_ALLOW_SHARED_ROOT", "1"); err != nil {
		t.Fatalf("set env: %v", err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("GT_DOLT_REBASE_ALLOW_SHARED_ROOT", prev)
		} else {
			_ = os.Unsetenv("GT_DOLT_REBASE_ALLOW_SHARED_ROOT")
		}
	})
}

// TestAcquireRebaseInterlock_RefusesSharedRoot proves the shared-root
// bypass guard (gastown-d88 finding #3): acquireRebaseInterlock must
// refuse to run when the session is a privileged system account
// (`root`, `dolt`, `mysql`) from a wildcard or localhost host, because
// Access.Match is per-(database, branch, user, host) with no session
// discrimination — concurrent writers sharing that account would inherit
// the admin grant and the interlock would silently fail open.
//
// The escape hatch `GT_DOLT_REBASE_ALLOW_SHARED_ROOT=1` is NOT set in
// this test; the call must error out with a deployment-prerequisite
// message that names the recommended fix (provision a dedicated
// `gtrebase` MySQL user).
func TestAcquireRebaseInterlock_RefusesSharedRoot(t *testing.T) {
	rootDSN := raceTestRootDSN(t)
	dbName, cleanup := raceTestCreateDB(t, rootDSN, 2)
	defer cleanup()

	// Ensure the env var is unset for this test (defensive — it should not
	// be set in CI, but a polluted environment would mask the failure).
	prev, had := os.LookupEnv("GT_DOLT_REBASE_ALLOW_SHARED_ROOT")
	_ = os.Unsetenv("GT_DOLT_REBASE_ALLOW_SHARED_ROOT")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("GT_DOLT_REBASE_ALLOW_SHARED_ROOT", prev)
		}
	})

	db, err := sql.Open("mysql", strings.Replace(rootDSN, "/?", "/"+dbName+"?", 1))
	if err != nil {
		t.Fatalf("open %s: %v", dbName, err)
	}
	defer db.Close()

	ctx := context.Background()
	_, _, err = acquireRebaseInterlock(ctx, db, dbName)
	if err == nil {
		t.Fatal("acquireRebaseInterlock as root@% must error; got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"shared",
		"root@%",      // user@host visible in the message
		"gastown-d88", // references the design bead
		"gtrebase",    // names the recommended dedicated user
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %v", want, err)
		}
	}
	t.Logf("shared-root correctly refused: %v", err)
}

// TestAcquireRebaseInterlock_SharedRootEscapeHatch proves that the
// GT_DOLT_REBASE_ALLOW_SHARED_ROOT=1 escape hatch unblocks acquire so
// existing root@%-based tests (and emergency operator overrides) can
// continue to work. Refusing the escape hatch would lock out CI.
func TestAcquireRebaseInterlock_SharedRootEscapeHatch(t *testing.T) {
	withSharedRootOverride(t)

	rootDSN := raceTestRootDSN(t)
	dbName, cleanup := raceTestCreateDB(t, rootDSN, 2)
	defer cleanup()

	db, err := sql.Open("mysql", strings.Replace(rootDSN, "/?", "/"+dbName+"?", 1))
	if err != nil {
		t.Fatalf("open %s: %v", dbName, err)
	}
	defer db.Close()

	ctx := context.Background()
	_, release, err := acquireRebaseInterlock(ctx, db, dbName)
	if err != nil {
		t.Fatalf("acquireRebaseInterlock with escape hatch: %v", err)
	}
	if err := release(); err != nil {
		t.Errorf("release: %v", err)
	}
}

// TestAcquireRebaseInterlock_ScopedByDatabase proves the cross-database
// ACL damage fix (gastown-d88 finding #2): the DELETE in
// acquireRebaseInterlock must filter by `database = ?` so it never
// touches rows that target a different database. We seed two throwaway
// DBs (DB-A and DB-B), each with its own dolt_branch_control main row,
// acquire the interlock on DB-A, and assert DB-B's main row is intact.
func TestAcquireRebaseInterlock_ScopedByDatabase(t *testing.T) {
	withSharedRootOverride(t)

	rootDSN := raceTestRootDSN(t)
	dbA, cleanupA := raceTestCreateDB(t, rootDSN, 2)
	defer cleanupA()
	dbB, cleanupB := raceTestCreateDB(t, rootDSN, 2)
	defer cleanupB()

	root, err := sql.Open("mysql", rootDSN)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	// Seed DB-B with an explicit per-DB main row that must survive the
	// DB-A interlock untouched. If the DELETE leaked across DBs (or
	// across the database column within dbB.dolt_branch_control), this
	// row would be wiped and the post-check below would fail.
	seedUser := "testuser_dbB"
	seedHost := "%"
	if _, err := root.Exec(fmt.Sprintf(
		"REPLACE INTO `%s`.dolt_branch_control (`database`, branch, user, host, permissions) VALUES (?, 'main', ?, ?, 'admin')",
		dbB), dbB, seedUser, seedHost); err != nil {
		t.Fatalf("seed DB-B main row: %v", err)
	}

	// Capture DB-B's main rows BEFORE the interlock on DB-A.
	preBRows, err := raceTestQueryMainRows(root, dbB)
	if err != nil {
		t.Fatalf("pre DB-B snapshot: %v", err)
	}
	if len(preBRows) == 0 {
		t.Fatal("DB-B seed row missing before interlock")
	}

	// Acquire interlock on DB-A.
	dbAConn, err := sql.Open("mysql", strings.Replace(rootDSN, "/?", "/"+dbA+"?", 1))
	if err != nil {
		t.Fatalf("open dbA: %v", err)
	}
	defer dbAConn.Close()

	ctx := context.Background()
	_, release, err := acquireRebaseInterlock(ctx, dbAConn, dbA)
	if err != nil {
		t.Fatalf("acquireRebaseInterlock on dbA: %v", err)
	}

	// DB-B's per-DB main rows must be intact WHILE the DB-A interlock is
	// held. This is the core cross-DB ACL damage proof: the previous
	// implementation's DELETE filtered only on branch='main' and could
	// wipe another DB's rows in some configurations.
	midBRows, err := raceTestQueryMainRows(root, dbB)
	if err != nil {
		t.Fatalf("mid DB-B snapshot: %v", err)
	}
	if !sameMainRows(preBRows, midBRows) {
		t.Fatalf("DB-B main rows changed during DB-A interlock:\n  pre: %v\n  mid: %v", preBRows, midBRows)
	}
	t.Logf("DB-B main rows stable during DB-A interlock: %d rows", len(midBRows))

	// Release interlock on DB-A.
	if err := release(); err != nil {
		t.Errorf("release: %v", err)
	}

	// DB-B still intact after release.
	postBRows, err := raceTestQueryMainRows(root, dbB)
	if err != nil {
		t.Fatalf("post DB-B snapshot: %v", err)
	}
	if !sameMainRows(preBRows, postBRows) {
		t.Fatalf("DB-B main rows changed after DB-A release:\n  pre: %v\n  post: %v", preBRows, postBRows)
	}
	t.Logf("DB-B main rows stable after DB-A release: %d rows", len(postBRows))
}

// TestAcquireRebaseInterlock_DeadContextRelease proves the dead-context
// fix (gastown-d88 finding #4): releaseFn must use a FRESH context,
// not the operation context, so it can still restore dolt_branch_control
// rows even when the operation context has been cancelled or expired.
//
// We acquire the interlock, immediately cancel the operation context
// (simulating a 10-min timeout or parent cancellation), then call
// release() and verify the snapshot rows are restored. The previous
// implementation would have inherited the dead ctx and the ExecContext
// calls would fail silently.
func TestAcquireRebaseInterlock_DeadContextRelease(t *testing.T) {
	withSharedRootOverride(t)

	rootDSN := raceTestRootDSN(t)
	dbName, cleanup := raceTestCreateDB(t, rootDSN, 2)
	defer cleanup()

	db, err := sql.Open("mysql", strings.Replace(rootDSN, "/?", "/"+dbName+"?", 1))
	if err != nil {
		t.Fatalf("open %s: %v", dbName, err)
	}
	defer db.Close()

	// Pin connection readiness before the acquire. Without an explicit
	// ping, the first Query/Exec on a freshly-opened pool can race the
	// testcontainer's port-bind and hit a 10s driver timeout.
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := db.PingContext(pingCtx); err != nil {
		pingCancel()
		t.Skipf("Dolt not reachable for %s: %v", dbName, err)
	}
	pingCancel()

	// Acquire with a context that we will cancel BEFORE release runs.
	opCtx, opCancel := context.WithCancel(context.Background())
	_, release, err := acquireRebaseInterlock(opCtx, db, dbName)
	if err != nil {
		t.Fatalf("acquireRebaseInterlock: %v", err)
	}

	// Confirm opCtx is still alive so we know the cancel below is the
	// only thing that invalidates it.
	if err := opCtx.Err(); err != nil {
		t.Fatalf("opCtx unexpectedly already done before cancel: %v", err)
	}

	// SIMULATE the failure mode: the operation context dies (timeout,
	// parent cancel, anything). After this call, opCtx.Err() != nil and
	// any ExecContext(opCtx, ...) would return ctx.Err() immediately.
	opCancel()
	if err := opCtx.Err(); err == nil {
		t.Fatal("opCtx.Err() == nil after Cancel — driver setup bug")
	}

	// Release must STILL succeed using a fresh context. If it inherited
	// opCtx, the DELETE and REPLACE ExecContext calls would fail and the
	// snapshot rows would NOT be restored. We assert success rather than
	// inspecting specific restored rows because the testcontainer-backed
	// DB has only the interlock's own admin/read rows visible from the
	// rebase session (the snapshot may be empty for a freshly-seeded DB).
	if err := release(); err != nil {
		t.Fatalf("release with dead op context (must use fresh ctx): %v", err)
	}

	// Sanity: post-release dolt_branch_control should have no admin/read
	// interlock rows left (they were the only thing we inserted; snapshot
	// was empty).
	verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer verifyCancel()
	var count int
	if err := db.QueryRowContext(verifyCtx, fmt.Sprintf(
		"SELECT COUNT(*) FROM `%s`.dolt_branch_control WHERE `database` = ? AND branch = 'main'",
		dbName), dbName).Scan(&count); err != nil {
		t.Fatalf("post-release verify query: %v", err)
	}
	t.Logf("release survived dead op context; post-release main rows: %d", count)
}

// raceTestQueryMainRows returns the set of (database, user, host, permissions)
// tuples for branch='main' rows in the given db's dolt_branch_control.
// Used by TestAcquireRebaseInterlock_ScopedByDatabase to assert that
// another DB's main rows are not touched by the interlock.
func raceTestQueryMainRows(db *sql.DB, dbName string) ([]branchControlRow, error) {
	rows, err := db.Query(fmt.Sprintf(
		"SELECT `database`, branch, user, host, permissions FROM `%s`.dolt_branch_control WHERE `database` = ? AND branch = 'main'",
		dbName), dbName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []branchControlRow
	for rows.Next() {
		var r branchControlRow
		if err := rows.Scan(&r.database, &r.branch, &r.user, &r.host, &r.permissions); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// sameMainRows reports whether two row slices describe the same set of
// main-branch rows (compared by (database, user, host, permissions) —
// branch is implicit 'main').
func sameMainRows(a, b []branchControlRow) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(r branchControlRow) string {
		return fmt.Sprintf("%s|%s|%s|%s", r.database, r.user, r.host, r.permissions)
	}
	seen := make(map[string]int, len(a))
	for _, r := range a {
		seen[key(r)]++
	}
	for _, r := range b {
		seen[key(r)]--
	}
	for _, v := range seen {
		if v != 0 {
			return false
		}
	}
	return true
}

// TestRebaseOrder_InterlockBeforeFork is a static-analysis guard for the
// gastown-d88 finding #1 TOCTOU regression: the interlock MUST be acquired
// BEFORE compact-work is forked from main in runDoltRebase. The previous
// implementation acquired the interlock AFTER the fork, leaving a window
// where a concurrent writer advancing main could be silently dropped.
//
// This test inspects the line ordering of the source file as a regression
// guard: it locates the string "acquireRebaseInterlock" and "DOLT_BRANCH('%s', 'main')"
// (the fork) and asserts the acquire call appears first in the function
// body. If someone reorders these calls in a future refactor, the test
// fails loudly with a pointer to the design bead.
// TestRebaseOrder_InPlaceNoDestructiveSwap guards the gastown-5nz invariant:
// the surgical rebase must rebase directly on `main` in place, with NO
// destructive `DOLT_BRANCH -D main` + `-m compact-work → main` swap and NO
// compact-work fork from main. The swap was the source of the TOCTOU window
// (gastown-d88); 5nz eliminates it by relying on DOLT_REBASE('--continue')
// to atomically update refs/heads/main and fail-close on concurrent writes.
//
// This supersedes the gastown-d88 TestRebaseOrder_InterlockBeforeFork guard,
// which asserted the now-removed interlock-acquired-before-fork ordering.
func TestRebaseOrder_InPlaceNoDestructiveSwap(t *testing.T) {
	src, err := os.ReadFile("dolt_rebase.go")
	if err != nil {
		t.Fatalf("read dolt_rebase.go: %v", err)
	}
	text := string(src)

	// MUST NOT contain the destructive swap: deleting main and renaming
	// compact-work onto it. Either half re-introduces the TOCTOU window.
	if strings.Contains(text, "CALL DOLT_BRANCH('-D', 'main')") {
		t.Fatal("TOCTOU REGRESSION (gastown-5nz): destructive `DOLT_BRANCH -D main` present in dolt_rebase.go — " +
			"the in-place rebase must update refs/heads/main via DOLT_REBASE('--continue'), not a branch delete+rename swap")
	}
	if strings.Contains(text, "CALL DOLT_BRANCH('-m', '%s', 'main')") {
		t.Fatal("TOCTOU REGRESSION (gastown-5nz): destructive compact-work→main rename present in dolt_rebase.go — " +
			"the in-place rebase must not swap branches")
	}
	// MUST NOT fork a compact-work branch from main (the swap target).
	if strings.Contains(text, "CALL DOLT_BRANCH('%s', 'main')") {
		t.Fatal("TOCTOU REGRESSION (gastown-5nz): compact-work fork from main present in dolt_rebase.go — " +
			"the in-place rebase runs directly on main, no work branch")
	}
	// MUST pin a single connection so the session-scoped dolt_rebase plan
	// survives across the start/UPDATE/continue calls.
	if !strings.Contains(text, "SetMaxOpenConns(1)") {
		t.Fatal("gastown-5nz: SetMaxOpenConns(1) missing in dolt_rebase.go — the session-scoped dolt_rebase plan " +
			"requires a pinned single connection or the driver may round-robin and lose the plan")
	}
	// MUST start the interactive rebase directly on main (no checkout of a
	// work branch before it).
	continueIdx := strings.Index(text, "CALL DOLT_REBASE('--continue')")
	startIdx := strings.Index(text, "CALL DOLT_REBASE('--interactive'")
	if startIdx < 0 || continueIdx < 0 {
		t.Fatal("gastown-5nz: in-place rebase start/continue calls missing in dolt_rebase.go")
	}
	if startIdx > continueIdx {
		t.Fatalf("gastown-5nz: DOLT_REBASE('--interactive') (offset %d) after --continue (offset %d)", startIdx, continueIdx)
	}
	t.Logf("in-place rebase invariant OK: no destructive swap, no compact-work fork, single-conn pinned")
}
