//go:build integration

// Package cmd — race test for the in-place surgical rebase (gastown-5nz).
//
// The in-place rebase eliminates the destructive
// `DOLT_BRANCH -D main` + `-m compact-work → main` swap that created the
// TOCTOU window (gastown-d88 / gastown-5nz retro-bug P0). Instead the
// interactive rebase runs directly on `main`: `DOLT_REBASE('--continue')`
// atomically updates refs/heads/main and fail-closes if a concurrent writer
// advanced main mid-rebase.
//
// Acceptance criterion (mirrors gastown-d88's):
//
//	"A race test where an external writer advances main between [rebase start
//	 and continue]; expected behavior is fail-closed with no lost commit."
//
// This test proves against a live Dolt sql-server (the running local Dolt on
// 127.0.0.1:3307): while an interactive rebase is mid-flight on main, a
// separate-session writer commits to main; the --continue MUST fail-closed
// AND the concurrent commit MUST remain on main (no data loss). Skips if no
// local Dolt is reachable.

package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// runInPlaceRebase is a minimal in-process mirror of runDoltRebase's
// in-place rebase flow (gastown-5nz), without the CLI argument parsing. It
// pins a single connection, starts the interactive rebase on main, marks the
// requested plan range as squash, and runs --continue. It does NOT perform a
// destructive branch swap. Returns the continue error (nil on success).
func runInPlaceRebase(ctx context.Context, db *sql.DB, dbName, rootHash string, squashFromOrder, squashToOrder int) error {
	// Pin single conn — dolt_rebase plan is session-scoped.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if _, err := db.ExecContext(ctx, fmt.Sprintf("USE `%s`", dbName)); err != nil {
		return fmt.Errorf("use db: %w", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		return fmt.Errorf("checkout main: %w", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_REBASE('--interactive', '%s')", rootHash)); err != nil {
		return fmt.Errorf("start rebase: %w", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(
		"UPDATE dolt_rebase SET action = 'squash' WHERE rebase_order > %d AND rebase_order <= %d",
		squashFromOrder, squashToOrder)); err != nil {
		return fmt.Errorf("update plan: %w", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_REBASE('--continue')"); err != nil {
		return fmt.Errorf("rebase execution failed: %w", err)
	}
	return nil
}

// TestInPlaceRebase_FailClosedOnConcurrentWrite proves the core 5nz
// invariant: a concurrent writer that commits to main during an in-place
// rebase causes DOLT_REBASE('--continue') to fail-closed, and the concurrent
// commit is NOT lost (it remains on main).
func TestInPlaceRebase_FailClosedOnConcurrentWrite(t *testing.T) {
	rootDSN := raceTestRootDSN(t)
	dbName, cleanup := raceTestCreateDB(t, rootDSN, 5)
	defer cleanup()

	// Root commit to rebase onto.
	root, err := sql.Open("mysql", rootDSN)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()
	var rootHash string
	if err := root.QueryRowContext(context.Background(),
		fmt.Sprintf("SELECT commit_hash FROM `%s`.dolt_log ORDER BY date ASC LIMIT 1", dbName)).Scan(&rootHash); err != nil {
		t.Fatalf("find root: %v", err)
	}

	// Rebase session: pin single conn.
	rebaseDSN := strings.Replace(rootDSN, "/?", "/"+dbName+"?", 1)
	rebaseDB, err := sql.Open("mysql", rebaseDSN)
	if err != nil {
		t.Fatalf("open rebase db: %v", err)
	}
	defer rebaseDB.Close()

	// Capture main HEAD before the rebase (informational).
	preHead := raceTestMainHead(t, root, dbName)

	// Run the rebase on a goroutine. After starting the rebase + marking the
	// plan, it waits so the writer can commit mid-flight, then continues.
	type result struct{ err error }
	done := make(chan result, 1)
	planReady := make(chan struct{}, 1)
	writerDone := make(chan struct{}, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		if _, err := rebaseDB.ExecContext(ctx, fmt.Sprintf("USE `%s`", dbName)); err != nil {
			done <- result{err: fmt.Errorf("use db: %w", err)}
			return
		}
		if _, err := rebaseDB.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
			done <- result{err: fmt.Errorf("checkout main: %w", err)}
			return
		}
		if _, err := rebaseDB.ExecContext(ctx, fmt.Sprintf("CALL DOLT_REBASE('--interactive', '%s')", rootHash)); err != nil {
			done <- result{err: fmt.Errorf("start rebase: %w", err)}
			return
		}
		if _, err := rebaseDB.ExecContext(ctx,
			"UPDATE dolt_rebase SET action = 'squash' WHERE rebase_order > 1 AND rebase_order <= 3"); err != nil {
			done <- result{err: fmt.Errorf("update plan: %w", err)}
			return
		}
		// Signal the plan is ready, then hold so the writer can commit.
		planReady <- struct{}{}
		<-writerDone
		// Now continue — the writer has already advanced main.
		_, err := rebaseDB.ExecContext(ctx, "CALL DOLT_REBASE('--continue')")
		done <- result{err: err}
	}()

	// Wait for the rebase plan to be ready.
	select {
	case <-planReady:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for rebase plan to be ready")
	}

	// Concurrent writer commits to main from a SEPARATE session during the
	// rebase window.
	writerDSN := strings.Replace(rootDSN, "/?", "/"+dbName+"?", 1)
	writerDB, err := sql.Open("mysql", writerDSN)
	if err != nil {
		t.Fatalf("open writer db: %v", err)
	}
	defer writerDB.Close()
	wctx, wcancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer wcancel()
	if _, err := writerDB.ExecContext(wctx, fmt.Sprintf("USE `%s`", dbName)); err != nil {
		t.Fatalf("writer use db: %v", err)
	}
	if _, err := writerDB.ExecContext(wctx,
		"INSERT INTO items VALUES (99, 'CONCURRENT-WRITE')"); err != nil {
		t.Fatalf("writer insert: %v", err)
	}
	if _, err := writerDB.ExecContext(wctx, "CALL DOLT_ADD('items')"); err != nil {
		t.Fatalf("writer add: %v", err)
	}
	if _, err := writerDB.ExecContext(wctx,
		"CALL DOLT_COMMIT('-m', 'CONCURRENT WRITE 99')"); err != nil {
		t.Fatalf("writer commit: %v", err)
	}
	writerDone <- struct{}{}

	// Collect the continue result.
	res := <-done

	// ASSERT 1: --continue fail-closed. It must NOT have succeeded (a success
	// would mean the concurrent commit was silently folded away).
	if res.err == nil {
		t.Fatalf("DOLT_REBASE --continue SUCCEEDED despite a concurrent write to main — expected fail-closed abort")
	}
	if !isConcurrentWriteErrorMsg(res.err.Error()) {
		t.Fatalf("continue error did not classify as concurrent-write: %v", res.err)
	}
	t.Logf("continue fail-closed as expected: %v", res.err)

	// ASSERT 2: no lost commit. The concurrent write MUST be on main now.
	postHead := raceTestMainHead(t, root, dbName)
	if postHead == preHead {
		t.Fatalf("main HEAD unchanged after concurrent write — commit was lost (preHead=%s)", preHead)
	}
	var haveRaceRow bool
	if err := root.QueryRowContext(context.Background(),
		fmt.Sprintf("SELECT COUNT(*) FROM `%s`.items WHERE id = 99", dbName)).Scan(&haveRaceRow); err != nil {
		t.Fatalf("check race row: %v", err)
	}
	if !haveRaceRow {
		t.Fatalf("concurrent-write row id=99 MISSING from main — data was lost")
	}
	t.Logf("concurrent commit preserved on main (postHead=%s) — no data loss", shortHash(postHead))

	// ASSERT 3: no torn state. active branch must be back on main, and no
	// leftover dolt_rebase_main branch should block a future rebase. Open a
	// fresh connection bound to the test DB (the root DSN has no default db).
	abDB, err := sql.Open("mysql", strings.Replace(rootDSN, "/?", "/"+dbName+"?", 1))
	if err != nil {
		t.Fatalf("open active_branch db: %v", err)
	}
	defer abDB.Close()
	var active string
	if err := abDB.QueryRowContext(context.Background(), "SELECT active_branch()").Scan(&active); err != nil {
		t.Fatalf("read active_branch: %v", err)
	}
	if active != "main" {
		t.Fatalf("active branch after abort = %q, want main (torn state)", active)
	}
	// Clean up any leftover dolt_rebase_main so it doesn't block sibling tests.
	_, _ = abDB.ExecContext(context.Background(), "CALL DOLT_BRANCH('-D', 'dolt_rebase_main')")
}

// TestInPlaceRebase_SucceedsWhenNoConcurrentWrite proves the happy path: with
// no concurrent writer, the in-place rebase atomically updates main and
// preserves all rows.
func TestInPlaceRebase_SucceedsWhenNoConcurrentWrite(t *testing.T) {
	rootDSN := raceTestRootDSN(t)
	dbName, cleanup := raceTestCreateDB(t, rootDSN, 5)
	defer cleanup()

	root, err := sql.Open("mysql", rootDSN)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()
	var rootHash string
	if err := root.QueryRowContext(context.Background(),
		fmt.Sprintf("SELECT commit_hash FROM `%s`.dolt_log ORDER BY date ASC LIMIT 1", dbName)).Scan(&rootHash); err != nil {
		t.Fatalf("find root: %v", err)
	}

	preHead := raceTestMainHead(t, root, dbName)
	preCount := raceTestRowCount(t, root, dbName, "items")

	rebaseDSN := strings.Replace(rootDSN, "/?", "/"+dbName+"?", 1)
	rebaseDB, err := sql.Open("mysql", rebaseDSN)
	if err != nil {
		t.Fatalf("open rebase db: %v", err)
	}
	defer rebaseDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := runInPlaceRebase(ctx, rebaseDB, dbName, rootHash, 1, 3); err != nil {
		t.Fatalf("in-place rebase failed on quiet main: %v", err)
	}

	// main advanced (rebased history) and rows are intact.
	postHead := raceTestMainHead(t, root, dbName)
	if postHead == preHead {
		t.Fatalf("main HEAD unchanged after successful rebase — rebase did not land")
	}
	postCount := raceTestRowCount(t, root, dbName, "items")
	if postCount != preCount {
		t.Fatalf("row count changed: pre=%d post=%d (data loss/gain on quiet main)", preCount, postCount)
	}
	t.Logf("in-place rebase succeeded: %s -> %s, %d rows intact", shortHash(preHead), shortHash(postHead), postCount)
}

// raceTestMainHead returns main's HEAD via DOLT_HASHOF('main').
func raceTestMainHead(t *testing.T, db *sql.DB, dbName string) string {
	t.Helper()
	var head string
	if err := db.QueryRowContext(context.Background(),
		fmt.Sprintf("SELECT DOLT_HASHOF('main') FROM `%s`.dual", dbName)).Scan(&head); err != nil {
		// Fallback to dolt_log.
		if err := db.QueryRowContext(context.Background(),
			fmt.Sprintf("SELECT commit_hash FROM `%s`.dolt_log ORDER BY date DESC LIMIT 1", dbName)).Scan(&head); err != nil {
			t.Fatalf("read main head: %v", err)
		}
	}
	return head
}

// raceTestRowCount returns the row count of table in dbName.
func raceTestRowCount(t *testing.T, db *sql.DB, dbName, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(),
		fmt.Sprintf("SELECT COUNT(*) FROM `%s`.`%s`", dbName, table)).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// isConcurrentWriteErrorMsg mirrors daemon.isConcurrentWriteError (test-local
// copy to avoid an internal/daemon import cycle).
func isConcurrentWriteErrorMsg(msg string) bool {
	return strings.Contains(msg, "changes in branch main") ||
		strings.Contains(msg, "rebase execution failed") ||
		strings.Contains(msg, "graph") ||
		strings.Contains(msg, "cannot rebase")
}
