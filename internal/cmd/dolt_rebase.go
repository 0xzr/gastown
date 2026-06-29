package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	doltRebaseConfirm    bool
	doltRebaseKeepRecent int
	doltRebaseDryRun     bool
)

var doltRebaseCmd = &cobra.Command{
	Use:   "rebase <database>",
	Short: "Surgical compaction: squash old commits, keep recent ones",
	Long: `Surgically compact a Dolt database using interactive rebase.

Unlike 'gt dolt flatten' (which destroys ALL history), surgical rebase
keeps recent commits individual while squashing old history into one.

Algorithm (based on Dolt's DOLT_REBASE):
  1. Starts interactive rebase directly on main — populates dolt_rebase table
  2. Marks old commits as 'squash', keeps recent N as 'pick'
  3. Executes the rebase plan (atomically updates refs/heads/main)
  4. Verifies row-count integrity
  5. Cleans up temporary branches

CONCURRENT-WRITE SAFETY: the rebase runs in place on main — there is no
destructive branch swap, so there is no TOCTOU window. DOLT_REBASE('--continue')
atomically updates refs/heads/main and fail-closes ("rebase aborted due to
changes in branch main") if an agent commits to this database mid-rebase; the
concurrent commit is preserved, not lost. Re-run the command if it fails due
to concurrent writes. Flatten mode (gt dolt flatten) is also safe with
concurrent writes.

Use --keep-recent to control how many recent commits to preserve.
Use --dry-run to see the plan without executing it.

Requires --yes-i-am-sure flag as safety interlock.`,
	Args: cobra.ExactArgs(1),
	RunE: runDoltRebase,
}

func init() {
	doltRebaseCmd.Flags().BoolVar(&doltRebaseConfirm, "yes-i-am-sure", false,
		"Required safety flag to confirm compaction")
	doltRebaseCmd.Flags().IntVar(&doltRebaseKeepRecent, "keep-recent", 50,
		"Number of recent commits to keep as individual picks")
	doltRebaseCmd.Flags().BoolVar(&doltRebaseDryRun, "dry-run", false,
		"Show the rebase plan without executing it")
	doltCmd.AddCommand(doltRebaseCmd)
}

func runDoltRebase(cmd *cobra.Command, args []string) error {
	dbName := args[0]

	if !doltRebaseConfirm && !doltRebaseDryRun {
		return fmt.Errorf("this command rewrites commit history. Pass --yes-i-am-sure to proceed (or --dry-run to preview)")
	}

	if doltRebaseKeepRecent < 0 {
		return fmt.Errorf("--keep-recent must be non-negative (got %d)", doltRebaseKeepRecent)
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	running, _, err := doltserver.IsRunning(townRoot)
	if err != nil || !running {
		return fmt.Errorf("Dolt server is not running — start with 'gt dolt start'")
	}

	config := doltserver.DefaultConfig(townRoot)
	// wa-d6f: socket-first DSN (TCP fallback) — eliminates TIME_WAIT churn.
	dsn := buildDoltDSNFromConfig(config, dbName, dsnOpts{
		ParseTime:    true,
		Timeout:      "5s",
		ReadTimeout:  "60s",
		WriteTimeout: "300s",
	})

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("connecting to database %s: %w", dbName, err)
	}
	defer db.Close()

	// Pin the pool to a single connection. The interactive rebase plan lives
	// in the session-scoped `dolt_rebase` system table, so every plan mutation
	// (start, UPDATE action='squash', --continue) MUST run on the SAME
	// connection. SetMaxOpenConns(1) guarantees the sql.DB hands out one
	// underlying conn for every ExecContext/QueryRowContext below — without
	// it, the driver may round-robin to a fresh conn and `dolt_rebase` looks
	// empty, causing the rebase to no-op or fail.
	//
	// This also closes the TOCTOU window: there is no destructive
	// `DOLT_BRANCH -D main` + `-m compact-work → main` swap in this flow.
	// DOLT_REBASE('--continue') atomically updates refs/heads/main and
	// fail-closes ("rebase aborted due to changes in branch main") if a
	// concurrent writer advances main mid-rebase — no application-level
	// interlock or per-user Dolt provisioning required.
	// (gastown-5nz retro-bug P0; empirically verified against Dolt 2.1.8.)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Verify database exists.
	var dummy int
	if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&dummy); err != nil {
		return fmt.Errorf("database %q not reachable: %w", dbName, err)
	}

	fmt.Printf("%s Pre-flight checks for %s (surgical rebase)\n", style.Bold.Render("●"), style.Bold.Render(dbName))

	// Count commits.
	var commitCount int
	if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM `%s`.dolt_log", dbName)).Scan(&commitCount); err != nil {
		return fmt.Errorf("counting commits: %w", err)
	}
	fmt.Printf("  Commits: %d\n", commitCount)
	fmt.Printf("  Keep recent: %d\n", doltRebaseKeepRecent)

	// Need at least 3 commits: root (pick) + at least 1 to squash + 1 to keep.
	minCommits := doltRebaseKeepRecent + 2
	if commitCount < minCommits {
		fmt.Printf("  %s Too few commits (%d) for surgical rebase with --keep-recent=%d (need at least %d)\n",
			style.Bold.Render("✓"), commitCount, doltRebaseKeepRecent, minCommits)
		return nil
	}

	// Record pre-flight row counts.
	preCounts, err := flattenGetRowCounts(db, dbName)
	if err != nil {
		return fmt.Errorf("recording row counts: %w", err)
	}
	fmt.Printf("  Tables: %d\n", len(preCounts))
	for table, count := range preCounts {
		fmt.Printf("    %s: %d rows\n", table, count)
	}

	// Record pre-flight HEAD for the log. The authoritative concurrent-write
	// gate is now Dolt itself: DOLT_REBASE('--continue') fail-closes with
	// "rebase aborted due to changes in branch main" if main advanced during
	// the rebase. preHead is informational only (no destructive swap to guard).
	preHead, err := flattenGetHead(db, dbName)
	if err != nil {
		return fmt.Errorf("getting HEAD: %w", err)
	}
	fmt.Printf("  HEAD: %s\n", preHead[:12])
	_ = preHead

	// Get root commit.
	var rootHash string
	if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT commit_hash FROM `%s`.dolt_log ORDER BY date ASC LIMIT 1", dbName)).Scan(&rootHash); err != nil {
		return fmt.Errorf("finding root commit: %w", err)
	}
	fmt.Printf("  Root: %s\n", rootHash[:12])

	// USE database for all subsequent operations.
	if _, err := db.ExecContext(ctx, fmt.Sprintf("USE `%s`", dbName)); err != nil {
		return fmt.Errorf("use database: %w", err)
	}

	// Clean up any leftover branches from a previous failed run. This
	// covers both the legacy compact-base/compact-work names AND the
	// internal dolt_rebase_main branch Dolt creates during an interactive
	// rebase. dolt_rebase_main is auto-deleted on a SUCCESSFUL --continue
	// but is NOT auto-cleaned when the rebase aborts (e.g. concurrent-write
	// abort), so a failed prior run can leave it behind and block the next
	// `DOLT_REBASE('--interactive')` with "A branch named 'dolt_rebase_main'
	// already exists".
	rebaseCleanup(db, "compact-base", "compact-work")

	fmt.Printf("\n%s Starting surgical rebase...\n", style.Bold.Render("●"))

	// Ensure we are on main before starting the rebase. The interactive
	// rebase rebases the CURRENT branch onto the given base; running it on
	// main is what makes Dolt atomically update refs/heads/main on
	// --continue (the in-place primitive that eliminates the TOCTOU window).
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		return fmt.Errorf("checkout main before rebase: %w", err)
	}

	// Step 1: Start interactive rebase directly on main against the root
	// commit. Dolt creates an internal `dolt_rebase_main` branch, switches
	// onto it, and populates the session-scoped `dolt_rebase` plan table.
	// No compact-base / compact-work fork is created — there is no
	// destructive branch swap to race against a concurrent writer.
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_REBASE('--interactive', '%s')", rootHash)); err != nil {
		rebaseAbortAndCleanup(db, "compact-base", "compact-work")
		return fmt.Errorf("start interactive rebase: %w", err)
	}
	fmt.Printf("  Interactive rebase started on main (dolt_rebase table populated)\n")

	// Step 2: Inspect the rebase plan.
	var totalPlan int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_rebase").Scan(&totalPlan); err != nil {
		rebaseAbortAndCleanup(db, "compact-base", "compact-work")
		return fmt.Errorf("counting rebase entries: %w", err)
	}
	fmt.Printf("  Rebase plan: %d commits\n", totalPlan)

	// Calculate how many to squash: everything except first (must stay pick) and last N.
	// Dolt returns rebase_order as DECIMAL — the MySQL driver delivers it as
	// []uint8 (e.g. "1.00") which cannot be scanned directly into int.
	// Scan as string, parse to float, then truncate to int.
	var minOrderStr, maxOrderStr string
	if err := db.QueryRowContext(ctx, "SELECT MIN(rebase_order), MAX(rebase_order) FROM dolt_rebase").Scan(&minOrderStr, &maxOrderStr); err != nil {
		rebaseAbortAndCleanup(db, "compact-base", "compact-work")
		return fmt.Errorf("getting rebase order range: %w", err)
	}
	minOrder, maxOrder, err := parseRebaseOrderRange(minOrderStr, maxOrderStr)
	if err != nil {
		rebaseAbortAndCleanup(db, "compact-base", "compact-work")
		return fmt.Errorf("parsing rebase order range: %w", err)
	}

	squashThreshold := maxOrder - doltRebaseKeepRecent
	toSquash := 0
	if squashThreshold > minOrder {
		toSquash = squashThreshold - minOrder
	}

	fmt.Printf("  Squashing: %d old commits (keeping first as pick + last %d)\n",
		toSquash, doltRebaseKeepRecent)

	if toSquash == 0 {
		fmt.Printf("  %s Nothing to squash — all commits are recent\n", style.Bold.Render("✓"))
		rebaseAbortAndCleanup(db, "compact-base", "compact-work")
		return nil
	}

	if doltRebaseDryRun {
		// Show the plan.
		fmt.Printf("\n%s Dry-run rebase plan:\n", style.Bold.Render("●"))
		rows, err := db.QueryContext(ctx, "SELECT rebase_order, action, commit_hash, commit_message FROM dolt_rebase ORDER BY rebase_order")
		if err != nil {
			rebaseAbortAndCleanup(db, "compact-base", "compact-work")
			return fmt.Errorf("reading rebase plan: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var orderStr string
			var action, hash, msg string
			if err := rows.Scan(&orderStr, &action, &hash, &msg); err != nil {
				continue
			}
			order, err := parseRebaseOrder(orderStr)
			if err != nil {
				continue
			}
			marker := "pick"
			if order > minOrder && order <= squashThreshold {
				marker = "squash"
			}
			if len(msg) > 60 {
				msg = msg[:60] + "..."
			}
			if len(hash) > 8 {
				hash = hash[:8]
			}
			fmt.Printf("  %3d  %-7s  %s  %s\n", order, marker, hash, msg)
		}

		fmt.Printf("\n  Would squash %d commits, keep %d recent + 1 root pick\n",
			toSquash, doltRebaseKeepRecent)
		rebaseAbortAndCleanup(db, "compact-base", "compact-work")
		return nil
	}

	// Step 3: Modify the plan — mark old commits as squash.
	// First commit (minOrder) MUST stay 'pick' — squash needs a parent to fold into.
	// Keep last N commits as 'pick'.
	result, err := db.ExecContext(ctx, fmt.Sprintf(
		"UPDATE dolt_rebase SET action = 'squash' WHERE rebase_order > %d AND rebase_order <= %d",
		minOrder, squashThreshold))
	if err != nil {
		rebaseAbortAndCleanup(db, "compact-base", "compact-work")
		return fmt.Errorf("updating rebase plan: %w", err)
	}
	affected, _ := result.RowsAffected()
	fmt.Printf("  Marked %d commits as squash\n", affected)

	// Step 4: Execute the rebase plan. DOLT_REBASE('--continue') atomically
	// updates refs/heads/main, switches back to main, and deletes the
	// internal dolt_rebase_main branch. If a concurrent writer advanced
	// main between the rebase start and here, Dolt fail-closes with
	// "rebase aborted due to changes in branch main" — no data is lost and
	// the concurrent commit remains on main. No swap, no TOCTOU window.
	fmt.Printf("  Executing rebase (this may take a while)...\n")
	if _, err := db.ExecContext(ctx, "CALL DOLT_REBASE('--continue')"); err != nil {
		// Rebase aborted (concurrent-write abort or conflicts). Dolt has
		// already rolled back to main; clean up any residual branch.
		rebaseAbortAndCleanup(db, "compact-base", "compact-work")
		return fmt.Errorf("rebase execution failed (possible concurrent write or conflicts — automatic abort): %w", err)
	}
	fmt.Printf("  %s Rebase executed successfully — refs/heads/main updated atomically\n", style.Bold.Render("✓"))

	// Step 5: Verify integrity — row counts must match pre-flight. main is
	// already the rebased history (no separate swap), so this reads the
	// final state directly.
	postCounts, err := flattenGetRowCounts(db, dbName)
	if err != nil {
		fmt.Printf("  %s WARNING: could not verify row counts after rebase: %v\n",
			style.Bold.Render("!"), err)
		fmt.Printf("  Verify manually with 'gt dolt status'\n")
	} else {
		integrityOK := true
		for table, preCount := range preCounts {
			postCount, ok := postCounts[table]
			if !ok {
				fmt.Printf("  %s INTEGRITY WARNING: table %q missing after rebase (was %d rows)\n",
					style.Bold.Render("!"), table, preCount)
				integrityOK = false
			} else if preCount != postCount {
				fmt.Printf("  %s INTEGRITY WARNING: %q row count changed: pre=%d post=%d\n",
					style.Bold.Render("!"), table, preCount, postCount)
				integrityOK = false
			}
		}
		if integrityOK {
			fmt.Printf("  %s Integrity verified (%d tables match)\n", style.Bold.Render("✓"), len(preCounts))
		} else {
			fmt.Printf("  %s Some integrity checks failed — review above warnings\n", style.Bold.Render("!"))
		}
	}

	// Verify final state.
	var finalCount int
	if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM `%s`.dolt_log", dbName)).Scan(&finalCount); err == nil {
		fmt.Printf("  Final commit count: %d\n", finalCount)
	}

	fmt.Printf("\n%s Surgical rebase complete: %d → %d commits (kept %d recent)\n",
		style.Bold.Render("✓"), commitCount, finalCount, doltRebaseKeepRecent)
	return nil
}

// doltRebaseMainBranch is the internal branch Dolt creates when an
// interactive rebase is started on `main`. It is auto-deleted on a
// successful `DOLT_REBASE('--continue')` but NOT on an abort, so failed
// runs leave it behind and block the next rebase with
// "A branch named 'dolt_rebase_main' already exists".
const doltRebaseMainBranch = "dolt_rebase_main"

// rebaseCleanup removes leftover branches from a previous failed rebase.
func rebaseCleanup(db *sql.DB, baseBranch, workBranch string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Try to get back to main first.
	_, _ = db.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')")
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", workBranch))
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", baseBranch))
	// Clean Dolt's internal rebase branch (left behind on abort).
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", doltRebaseMainBranch))
}

// rebaseAbortAndCleanup aborts an in-progress rebase then cleans up branches.
//
//nolint:unparam // baseBranch always "compact-base" — API kept flexible for future callers
func rebaseAbortAndCleanup(db *sql.DB, baseBranch, workBranch string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = db.ExecContext(ctx, "CALL DOLT_REBASE('--abort')")
	_, _ = db.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')")
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", workBranch))
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", baseBranch))
	// Clean Dolt's internal rebase branch (left behind on abort).
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", doltRebaseMainBranch))
}

// parseRebaseOrder converts a rebase_order value (returned by Dolt as DECIMAL
// string, e.g. "1.00") to an int.
func parseRebaseOrder(s string) (int, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid rebase_order %q: %w", s, err)
	}
	return int(math.Round(f)), nil
}

// parseRebaseOrderRange parses min/max rebase_order strings to ints.
func parseRebaseOrderRange(minStr, maxStr string) (int, int, error) {
	minVal, err := parseRebaseOrder(minStr)
	if err != nil {
		return 0, 0, err
	}
	maxVal, err := parseRebaseOrder(maxStr)
	if err != nil {
		return 0, 0, err
	}
	return minVal, maxVal, nil
}
