//go:build integration

package cmd

import (
	"database/sql"
	"fmt"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/testutil"
)

// branchSwapTestDB is the database used for atomic branch swap tests.
const branchSwapTestDB = "cmd_branch_swap_test"

// setupBranchSwapTestDB creates a fresh database with two commits on main and
// returns a connection to the Dolt container plus a cleanup function.
func setupBranchSwapTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	testutil.RequireDoltContainer(t)

	dsn := fmt.Sprintf("root@tcp(%s)/", testutil.DoltContainerAddr())
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open dolt connection: %v", err)
	}

	if _, err := db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", branchSwapTestDB)); err != nil {
		t.Fatalf("drop stale test db: %v", err)
	}
	if _, err := db.Exec(fmt.Sprintf("CREATE DATABASE `%s`", branchSwapTestDB)); err != nil {
		t.Fatalf("create test db: %v", err)
	}

	testDB, err := sql.Open("mysql", dsn+branchSwapTestDB)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	_ = db.Close()

	mustExecBranchSwap(t, testDB, "CREATE TABLE t (id INT PRIMARY KEY)")
	mustExecBranchSwap(t, testDB, "INSERT INTO t VALUES (1)")
	mustExecBranchSwap(t, testDB, "CALL DOLT_COMMIT('-Am','first commit')")

	mustExecBranchSwap(t, testDB, "INSERT INTO t VALUES (2)")
	mustExecBranchSwap(t, testDB, "CALL DOLT_COMMIT('-Am','second commit')")

	return testDB, func() { _ = testDB.Close() }
}

func mustExecBranchSwap(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func TestBranchSwap_AtomicSwapSuccess(t *testing.T) {
	db, cleanup := setupBranchSwapTestDB(t)
	defer cleanup()

	mainHead, err := doltserver.BranchHead(t.Context(), db, branchSwapTestDB, "main")
	if err != nil {
		t.Fatalf("BranchHead(main): %v", err)
	}

	// Create a feature branch from main and advance it.
	mustExecBranchSwap(t, db, "CALL DOLT_BRANCH('feature','main')")
	mustExecBranchSwap(t, db, "CALL DOLT_CHECKOUT('feature')")
	mustExecBranchSwap(t, db, "INSERT INTO t VALUES (3)")
	mustExecBranchSwap(t, db, "CALL DOLT_COMMIT('-Am','feature commit')")
	featureHead, err := doltserver.BranchHead(t.Context(), db, branchSwapTestDB, "feature")
	if err != nil {
		t.Fatalf("BranchHead(feature): %v", err)
	}
	if featureHead == mainHead {
		t.Fatalf("feature head (%s) should differ from main head (%s)", featureHead, mainHead)
	}

	swapped, err := doltserver.TrySwapBranch(t.Context(), db, branchSwapTestDB, "main", mainHead, featureHead)
	if err != nil {
		t.Fatalf("TrySwapBranch: %v", err)
	}
	if !swapped {
		t.Fatal("TrySwapBranch returned false, expected true")
	}

	newMainHead, err := doltserver.BranchHead(t.Context(), db, branchSwapTestDB, "main")
	if err != nil {
		t.Fatalf("BranchHead(main) after swap: %v", err)
	}
	if newMainHead != featureHead {
		t.Fatalf("main head after swap = %s, want %s", newMainHead, featureHead)
	}
}

func TestBranchSwap_FailsWhenExpectedHashStale(t *testing.T) {
	db, cleanup := setupBranchSwapTestDB(t)
	defer cleanup()

	mainHead, err := doltserver.BranchHead(t.Context(), db, branchSwapTestDB, "main")
	if err != nil {
		t.Fatalf("BranchHead(main): %v", err)
	}

	// Advance main with another commit so the old expected hash no longer matches.
	mustExecBranchSwap(t, db, "CALL DOLT_CHECKOUT('main')")
	mustExecBranchSwap(t, db, "INSERT INTO t VALUES (99)")
	mustExecBranchSwap(t, db, "CALL DOLT_COMMIT('-Am','concurrent advance')")

	swapped, err := doltserver.TrySwapBranch(t.Context(), db, branchSwapTestDB, "main", mainHead, "cafef00dcafef00dcafef00dcafef00dcafef00d")
	if err != nil {
		t.Fatalf("TrySwapBranch with stale hash returned error: %v", err)
	}
	if swapped {
		t.Fatal("TrySwapBranch returned true with stale expected hash, expected false")
	}
}
