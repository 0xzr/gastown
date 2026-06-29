package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/reaper"
)

// shortHash returns at most 8 characters of a hash for display.
func shortHash(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}

const (
	defaultCompactorDogInterval = 24 * time.Hour
	// defaultCompactorCommitThreshold is the minimum commit count before compaction triggers.
	// 2000 commits prevents the escalation feedback loop where each compaction
	// failure creates beads/escalations that add more commits than the compactor
	// can drain at 500. Configurable via daemon.json.
	defaultCompactorCommitThreshold = 2000
	// compactorQueryTimeout is the timeout for individual SQL queries during compaction.
	compactorQueryTimeout = 30 * time.Second
	// compactorGCTimeout is the timeout for CALL dolt_gc() after compaction.
	compactorGCTimeout = 5 * time.Minute
	// compactorPushTimeout is the timeout for DOLT_PUSH after compaction.
	compactorPushTimeout = 2 * time.Minute
	// compactorBranchName is the temporary branch used during compaction.
	compactorBranchName = "gt-compaction"
	// surgicalMaxRetries is the number of times to retry surgical rebase after
	// a concurrent write error. DOLT_REBASE fail-closes if a concurrent writer
	// advances main mid-rebase ("rebase aborted due to changes in branch main")
	// — no data is lost. One retry is usually sufficient since the collision
	// window is small.
	surgicalMaxRetries = 1
	// compactBaseBranch / compactWorkBranch are the legacy temporary branch
	// names from the destructive-swap surgical rebase. The in-place rebase
	// (gastown-5nz) no longer creates them, but cleanup still deletes them
	// so a failed prior run on the old code doesn't leave residue.
	compactBaseBranch = "compact-base"
	compactWorkBranch = "compact-work"
	// doltRebaseMainBranch is the internal branch Dolt creates when an
	// interactive rebase is started on `main`. Auto-deleted on a successful
	// --continue, but NOT on an abort — so failed runs leave it behind and
	// block the next rebase with "A branch named 'dolt_rebase_main' already
	// exists".
	doltRebaseMainBranch = "dolt_rebase_main"
)

// CompactorDogConfig holds configuration for the compactor_dog patrol.
type CompactorDogConfig struct {
	Enabled     bool   `json:"enabled"`
	IntervalStr string `json:"interval,omitempty"`
	// Threshold is the minimum commit count before compaction triggers.
	// Defaults to 2000 if not set.
	Threshold int `json:"threshold,omitempty"`
	// Databases lists specific database names to compact.
	// If empty, falls back to wisp_reaper config, then auto-discovery.
	Databases []string `json:"databases,omitempty"`
	// Mode selects the compaction strategy: "flatten" (default) or "surgical".
	// Flatten squashes all history into 1 commit. Surgical keeps recent
	// commits individual while squashing old ones via interactive rebase.
	Mode string `json:"mode,omitempty"`
	// KeepRecent is the number of recent commits to preserve as individual
	// picks during surgical rebase. Only used when Mode is "surgical".
	// Defaults to 50 if not set.
	KeepRecent int `json:"keep_recent,omitempty"`
}

// compactorDogInterval returns the configured interval, or the default (24h).
func compactorDogInterval(config *DaemonPatrolConfig) time.Duration {
	if config != nil && config.Patrols != nil && config.Patrols.CompactorDog != nil {
		if config.Patrols.CompactorDog.IntervalStr != "" {
			if d, err := time.ParseDuration(config.Patrols.CompactorDog.IntervalStr); err == nil && d > 0 {
				return d
			}
		}
	}
	return defaultCompactorDogInterval
}

// compactorDogThreshold returns the configured commit threshold, or the default (500).
func compactorDogThreshold(config *DaemonPatrolConfig) int {
	if config != nil && config.Patrols != nil && config.Patrols.CompactorDog != nil {
		if config.Patrols.CompactorDog.Threshold > 0 {
			return config.Patrols.CompactorDog.Threshold
		}
	}
	return defaultCompactorCommitThreshold
}

// compactorDogMode returns the configured compaction mode ("flatten" or "surgical").
func compactorDogMode(config *DaemonPatrolConfig) string {
	if config != nil && config.Patrols != nil && config.Patrols.CompactorDog != nil {
		if config.Patrols.CompactorDog.Mode == "surgical" {
			return "surgical"
		}
	}
	return "flatten"
}

// compactorDogKeepRecent returns the configured keep-recent count, or the default (50).
func compactorDogKeepRecent(config *DaemonPatrolConfig) int {
	if config != nil && config.Patrols != nil && config.Patrols.CompactorDog != nil {
		if config.Patrols.CompactorDog.KeepRecent > 0 {
			return config.Patrols.CompactorDog.KeepRecent
		}
	}
	return 50
}

// runCompactorDog checks each production database's commit count and compacts
// any that exceed the threshold. Two modes:
//
// Flatten mode (default): soft-resets to root commit on main, commits all data
// as a single commit. Safe with concurrent writes.
//
// Surgical mode: interactive rebase that squashes old commits while preserving
// recent N as individual picks. NOT safe with concurrent writes — retries once.
//
// After successful compaction, runs dolt gc to reclaim unreferenced chunks.
//
// ZFC Exemption: This dog executes imperatively in Go rather than via agent-driven
// formula execution. The mol-dog-compactor formula is used for observability
// tracking only (pourDogMolecule + closeStep/failStep). Agent execution is
// impractical because: (1) operations require database/sql connections, (2)
// transactional state spans multiple queries, (3) cleanup-on-failure error paths,
// (4) concurrent write retry with error classification, (5) row count integrity
// verification. See mol-dog-compactor.formula.toml for full rationale.
func (d *Daemon) runCompactorDog() {
	if !d.isPatrolActive("compactor_dog") {
		return
	}

	threshold := compactorDogThreshold(d.patrolConfig)
	mode := compactorDogMode(d.patrolConfig)
	d.logger.Printf("compactor_dog: starting compaction cycle (threshold=%d, mode=%s)", threshold, mode)
	if mode == "surgical" {
		d.logger.Printf("compactor_dog: WARNING: surgical mode uses DOLT_REBASE which is not safe with concurrent writes — will retry on graph-change errors")
	}

	mol := d.pourDogMolecule(constants.MolDogCompactor, nil)
	defer mol.close()

	databases := d.compactorDatabases()
	if len(databases) == 0 {
		d.logger.Printf("compactor_dog: no databases to compact")
		mol.failStep("inspect", "no databases found")
		return
	}

	mol.closeStep("inspect")

	compacted := 0
	skipped := 0
	errors := 0

	for _, dbName := range databases {
		commitCount, err := d.compactorCountCommits(dbName)
		if err != nil {
			d.logger.Printf("compactor_dog: %s: error counting commits: %v", dbName, err)
			errors++
			continue
		}

		if commitCount < threshold {
			d.logger.Printf("compactor_dog: %s: %d commits (below threshold %d), skipping",
				dbName, commitCount, threshold)
			skipped++
			continue
		}

		d.logger.Printf("compactor_dog: %s: %d commits (threshold %d) — compacting (mode=%s)",
			dbName, commitCount, threshold, mode)

		// Pre-flight: fetch from remote and verify local ≥ remote before
		// compacting. Flatten rewrites the commit graph, so force-push after
		// compaction would overwrite any remote-only commits. Skip compaction
		// if the remote has diverged.
		diverged, fetchErr := d.compactorFetchAndVerify(dbName)
		if fetchErr != nil {
			d.logger.Printf("compactor_dog: %s: pre-flight fetch failed: %v (skipping)", dbName, fetchErr)
			skipped++
			continue
		}
		if diverged {
			d.logger.Printf("compactor_dog: %s: remote has diverged — skipping compaction to avoid data loss", dbName)
			skipped++
			continue
		}

		var compactErr error
		if mode == "surgical" {
			keepRecent := compactorDogKeepRecent(d.patrolConfig)
			compactErr = d.surgicalRebase(dbName, keepRecent)
		} else {
			compactErr = d.compactDatabase(dbName)
		}
		if compactErr != nil {
			d.logger.Printf("compactor_dog: %s: compaction FAILED: %v", dbName, compactErr)
			d.escalate("compactor_dog", fmt.Sprintf("Compaction failed for %s: %v", dbName, compactErr))
			errors++
		} else {
			compacted++
			// Run gc after successful compaction to reclaim unreferenced chunks.
			// Order matters: rebase first (compactDatabase), gc second.
			if err := d.compactorRunGC(dbName); err != nil {
				d.logger.Printf("compactor_dog: %s: gc after compaction failed: %v", dbName, err)
			}
			// Force-push to DoltHub remote after compaction. Flatten rewrites
			// the commit graph, so standard push always fails with non-fast-forward.
			// Safe because compactorFetchAndVerify confirmed local ≥ remote
			// before compaction, and compactDatabase verified integrity.
			if err := d.compactorForcePush(dbName); err != nil {
				d.logger.Printf("compactor_dog: %s: force-push failed: %v", dbName, err)
			}
		}
	}

	if errors > 0 {
		mol.failStep("compact", fmt.Sprintf("%d databases had errors", errors))
	} else {
		mol.closeStep("compact")
	}

	mol.closeStep("verify")

	d.logger.Printf("compactor_dog: cycle complete — compacted=%d skipped=%d errors=%d",
		compacted, skipped, errors)
	mol.closeStep("report")
}

// compactorDatabases returns the list of databases to consider for compaction.
// Checks its own config first, falls back to wisp_reaper config, then auto-discovery.
func (d *Daemon) compactorDatabases() []string {
	if d.patrolConfig != nil && d.patrolConfig.Patrols != nil {
		if cd := d.patrolConfig.Patrols.CompactorDog; cd != nil {
			if len(cd.Databases) > 0 {
				return cd.Databases
			}
		}
		if d.patrolConfig.Patrols.WispReaper != nil {
			if dbs := d.patrolConfig.Patrols.WispReaper.Databases; len(dbs) > 0 {
				return dbs
			}
		}
	}
	return reaper.DefaultDatabases
}

// compactorCountCommits counts the number of commits in the database's dolt_log.
func (d *Daemon) compactorCountCommits(dbName string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), compactorQueryTimeout)
	defer cancel()

	db, err := d.compactorOpenDB(dbName)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	var count int
	query := fmt.Sprintf("SELECT COUNT(*) FROM `%s`.dolt_log", dbName)
	if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return 0, fmt.Errorf("count dolt_log: %w", err)
	}
	return count, nil
}

// compactDatabase performs the full flatten operation on a single database.
// Uses direct SQL on the running server — no branches, no downtime.
// Per Tim Sehn (2026-02-28): DOLT_RESET --soft + DOLT_COMMIT is safe on a
// running server. Concurrent writes are safe — merge base shifts but diff
// is just the txn.
func (d *Daemon) compactDatabase(dbName string) error {
	db, err := d.compactorOpenDB(dbName)
	if err != nil {
		return err
	}
	defer db.Close()

	// Step 1: Record pre-flight state — row counts for integrity verification.
	preCounts, err := d.compactorGetRowCounts(db, dbName)
	if err != nil {
		return fmt.Errorf("pre-flight row counts: %w", err)
	}
	d.logger.Printf("compactor_dog: %s: pre-flight tables=%d", dbName, len(preCounts))

	// Step 2: Find the root commit (earliest in history).
	rootHash, err := d.compactorGetRootCommit(db, dbName)
	if err != nil {
		return fmt.Errorf("find root commit: %w", err)
	}
	d.logger.Printf("compactor_dog: %s: root commit=%s", dbName, shortHash(rootHash))

	// Step 3: USE database for session-scoped operations.
	ctx, cancel := context.WithTimeout(context.Background(), compactorQueryTimeout)
	defer cancel()
	if _, err := db.ExecContext(ctx, fmt.Sprintf("USE `%s`", dbName)); err != nil {
		return fmt.Errorf("use database: %w", err)
	}

	// Step 4: Soft-reset to root commit on main — all data remains staged.
	// This is trivially cheap: just moves the parent pointer (Tim Sehn).
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_RESET('--soft', '%s')", rootHash)); err != nil {
		return fmt.Errorf("soft reset to root: %w", err)
	}
	d.logger.Printf("compactor_dog: %s: soft-reset to root %s", dbName, shortHash(rootHash))

	// Step 5: Commit all data as a single commit.
	commitMsg := fmt.Sprintf("compaction: flatten %s history to single commit", dbName)
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_COMMIT('-Am', '%s')", commitMsg)); err != nil {
		return fmt.Errorf("commit flattened data: %w", err)
	}
	d.logger.Printf("compactor_dog: %s: committed flattened data", dbName)

	// Step 6: Verify integrity — row counts must not decrease (data loss).
	// Concurrent writes may increase counts during compaction — this is safe
	// since the flattened commit includes those rows.
	postCounts, err := d.compactorGetRowCounts(db, dbName)
	if err != nil {
		return fmt.Errorf("post-compact row counts: %w", err)
	}

	for table, preCount := range preCounts {
		postCount, ok := postCounts[table]
		if !ok {
			return fmt.Errorf("integrity check: table %q missing after compaction", table)
		}
		if postCount < preCount {
			return fmt.Errorf("integrity check: table %q lost rows: pre=%d post=%d", table, preCount, postCount)
		}
		if postCount > preCount {
			d.logger.Printf("compactor_dog: %s: table %q gained %d rows during compaction (concurrent write, safe)",
				dbName, table, postCount-preCount)
		}
	}
	d.logger.Printf("compactor_dog: %s: integrity verified (%d tables match)", dbName, len(preCounts))

	// Step 7: Verify final commit count.
	finalCount, err := d.compactorCountCommits(dbName)
	if err != nil {
		d.logger.Printf("compactor_dog: %s: warning: could not verify final commit count: %v", dbName, err)
	} else {
		d.logger.Printf("compactor_dog: %s: compaction complete — %d commits remain", dbName, finalCount)
	}

	return nil
}

// surgicalRebase performs interactive rebase on a single database:
// squashes old commits while keeping the most recent N as individual picks.
// This is an alternative to the flatten algorithm that preserves recent history.
//
// CONCURRENT WRITE SAFETY (gastown-5nz): The rebase runs in-place on `main` —
// DOLT_REBASE('--continue') atomically updates refs/heads/main, so there is no
// destructive branch-swap and no TOCTOU window. If a concurrent writer commits
// to main mid-rebase, Dolt fail-closes with "rebase aborted due to changes in
// branch main" (no data lost; the concurrent commit remains on main). This
// function retries once on that error, which is usually sufficient since the
// collision window is small. If retries are exhausted, the error is returned
// to the caller for escalation.
// Ref: Tim Sehn (2026-02-28) confirmed DOLT_REBASE fails on concurrent writes.
func (d *Daemon) surgicalRebase(dbName string, keepRecent int) error {
	var lastErr error
	for attempt := 0; attempt <= surgicalMaxRetries; attempt++ {
		if attempt > 0 {
			d.logger.Printf("compactor_dog: %s: surgical rebase retry %d/%d after concurrent write error",
				dbName, attempt, surgicalMaxRetries)
			// Brief pause before retry to let the concurrent write finish.
			time.Sleep(2 * time.Second)
		}
		lastErr = d.surgicalRebaseOnce(dbName, keepRecent)
		if lastErr == nil {
			return nil
		}
		if !isConcurrentWriteError(lastErr) {
			return lastErr
		}
		d.logger.Printf("compactor_dog: %s: concurrent write detected during surgical rebase: %v", dbName, lastErr)
	}
	return fmt.Errorf("surgical rebase failed after %d retries due to concurrent writes: %w", surgicalMaxRetries, lastErr)
}

// isConcurrentWriteError returns true if the error indicates Dolt detected a
// concurrent write during rebase (commit graph changed underneath the operation).
//
// With the in-place rebase (gastown-5nz), DOLT_REBASE('--continue') fail-closes
// with "rebase aborted due to changes in branch main" when a concurrent writer
// advanced main mid-rebase — the canonical concurrent-write signal.
func isConcurrentWriteError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// Dolt reports graph-change errors during rebase when concurrent writes occur.
	// "changes in branch main" is the in-place rebase's fail-closed message.
	// "rebase execution failed"/"concurrency abort" are our own wrappers.
	return strings.Contains(msg, "changes in branch main") ||
		strings.Contains(msg, "rebase execution failed") ||
		strings.Contains(msg, "concurrency abort") ||
		strings.Contains(msg, "graph") ||
		strings.Contains(msg, "cannot rebase")
}

// surgicalRebaseOnce performs a single attempt at surgical rebase.
func (d *Daemon) surgicalRebaseOnce(dbName string, keepRecent int) error {
	db, err := d.compactorOpenDB(dbName)
	if err != nil {
		return err
	}
	defer db.Close()

	// Pre-flight: record state. preHead is informational only — the
	// authoritative concurrent-write gate is Dolt itself (see continue below).
	preHead, err := d.compactorGetHead(db, dbName)
	if err != nil {
		return fmt.Errorf("pre-flight HEAD: %w", err)
	}
	preCounts, err := d.compactorGetRowCounts(db, dbName)
	if err != nil {
		return fmt.Errorf("pre-flight row counts: %w", err)
	}
	d.logger.Printf("compactor_dog: %s: surgical rebase pre-flight HEAD=%s, tables=%d, keep_recent=%d",
		dbName, shortHash(preHead), len(preCounts), keepRecent)

	rootHash, err := d.compactorGetRootCommit(db, dbName)
	if err != nil {
		return fmt.Errorf("find root commit: %w", err)
	}

	// Pin the pool to a single connection. The interactive rebase plan
	// lives in the session-scoped `dolt_rebase` system table, so every plan
	// mutation (start, UPDATE action='squash', --continue) MUST run on the
	// SAME connection. SetMaxOpenConns(1) guarantees one underlying conn for
	// every ExecContext/QueryRowContext below.
	//
	// This also closes the TOCTOU window (gastown-5nz retro-bug P0): there
	// is no destructive `DOLT_BRANCH -D main` + `-m compact-work → main`
	// swap in this flow. DOLT_REBASE('--continue') atomically updates
	// refs/heads/main and fail-closes ("rebase aborted due to changes in
	// branch main") if a concurrent writer advances main mid-rebase. No
	// application-level interlock or per-user Dolt provisioning required.
	// (Empirically verified against Dolt 2.1.8.)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if _, err := db.ExecContext(ctx, fmt.Sprintf("USE `%s`", dbName)); err != nil {
		return fmt.Errorf("use database: %w", err)
	}

	// Clean up leftover branches from a previous failed run: the legacy
	// compact-base/compact-work names AND Dolt's internal dolt_rebase_main
	// branch (left behind when a prior rebase aborted — not auto-cleaned).
	d.surgicalCleanup(db, compactBaseBranch, compactWorkBranch)

	// Ensure we are on main. Running the interactive rebase on main is what
	// makes DOLT_REBASE('--continue') atomically update refs/heads/main
	// (eliminating the destructive-swap TOCTOU window).
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		return fmt.Errorf("checkout main before rebase: %w", err)
	}

	// Step 1: Start interactive rebase directly on main against the root
	// commit. Dolt creates an internal `dolt_rebase_main` branch, switches
	// onto it, and populates the session-scoped `dolt_rebase` plan table.
	// No compact-base/compact-work fork, no destructive swap.
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_REBASE('--interactive', '%s')", rootHash)); err != nil {
		d.surgicalAbortAndCleanup(db, compactBaseBranch, compactWorkBranch)
		return fmt.Errorf("start interactive rebase: %w", err)
	}
	d.logger.Printf("compactor_dog: %s: interactive rebase started on main", dbName)

	// Step 2: Read rebase plan bounds and mark old commits as squash.
	// Dolt returns rebase_order as DECIMAL — the MySQL driver delivers it as
	// []uint8 (e.g. "1.00") which cannot be scanned directly into int.
	var minOrderStr, maxOrderStr string
	if err := db.QueryRowContext(ctx, "SELECT MIN(rebase_order), MAX(rebase_order) FROM dolt_rebase").Scan(&minOrderStr, &maxOrderStr); err != nil {
		d.surgicalAbortAndCleanup(db, compactBaseBranch, compactWorkBranch)
		return fmt.Errorf("read rebase bounds: %w", err)
	}
	minOrder, maxOrder, err := parseRebaseOrder2(minOrderStr, maxOrderStr)
	if err != nil {
		d.surgicalAbortAndCleanup(db, compactBaseBranch, compactWorkBranch)
		return fmt.Errorf("parse rebase bounds: %w", err)
	}

	squashThreshold := maxOrder - keepRecent
	if squashThreshold <= minOrder {
		d.logger.Printf("compactor_dog: %s: nothing to squash (all commits recent), aborting rebase", dbName)
		d.surgicalAbortAndCleanup(db, compactBaseBranch, compactWorkBranch)
		return nil
	}

	result, err := db.ExecContext(ctx, fmt.Sprintf(
		"UPDATE dolt_rebase SET action = 'squash' WHERE rebase_order > %d AND rebase_order <= %d",
		minOrder, squashThreshold))
	if err != nil {
		d.surgicalAbortAndCleanup(db, compactBaseBranch, compactWorkBranch)
		return fmt.Errorf("update rebase plan: %w", err)
	}
	affected, _ := result.RowsAffected()
	d.logger.Printf("compactor_dog: %s: marked %d commits as squash", dbName, affected)

	// Step 3: Execute the rebase. DOLT_REBASE('--continue') atomically
	// updates refs/heads/main, switches back to main, and deletes the
	// internal dolt_rebase_main branch. If a concurrent writer advanced
	// main between the rebase start and here, Dolt fail-closes with
	// "rebase aborted due to changes in branch main" — no data is lost and
	// the concurrent commit remains on main. The caller retries on that
	// error (isConcurrentWriteError).
	if _, err := db.ExecContext(ctx, "CALL DOLT_REBASE('--continue')"); err != nil {
		d.surgicalAbortAndCleanup(db, compactBaseBranch, compactWorkBranch)
		return fmt.Errorf("rebase execution failed: %w", err)
	}
	d.logger.Printf("compactor_dog: %s: rebase executed successfully — refs/heads/main updated atomically", dbName)

	// Step 4: Verify integrity — row counts must not decrease (data loss).
	// main already holds the rebased history (no separate swap), so this
	// reads the final state directly. Concurrent writes that landed during
	// the rebase are rejected by Dolt, so counts should match pre-flight.
	postCounts, err := d.compactorGetRowCounts(db, dbName)
	if err != nil {
		d.logger.Printf("compactor_dog: %s: WARNING: could not verify row counts after rebase: %v", dbName, err)
	} else {
		for table, preCount := range preCounts {
			postCount, ok := postCounts[table]
			if !ok {
				return fmt.Errorf("integrity: table %q missing after rebase", table)
			}
			if postCount < preCount {
				return fmt.Errorf("integrity: table %q lost rows: pre=%d post=%d", table, preCount, postCount)
			}
		}
		d.logger.Printf("compactor_dog: %s: integrity verified (%d tables)", dbName, len(preCounts))
	}

	finalCount, _ := d.compactorCountCommits(dbName)
	d.logger.Printf("compactor_dog: %s: surgical rebase complete — %d commits remain", dbName, finalCount)
	return nil
}

// surgicalCleanup switches back to main and removes rebase branches.
//
//nolint:unparam // baseBranch always "compact-base" — API kept flexible for future callers
func (d *Daemon) surgicalCleanup(db *sql.DB, baseBranch, workBranch string) {
	ctx, cancel := context.WithTimeout(context.Background(), compactorQueryTimeout)
	defer cancel()
	_, _ = db.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')")
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", workBranch))
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", baseBranch))
	// Clean Dolt's internal rebase branch (left behind on abort).
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", doltRebaseMainBranch))
}

// surgicalAbortAndCleanup aborts an in-progress rebase, then cleans up.
func (d *Daemon) surgicalAbortAndCleanup(db *sql.DB, baseBranch, workBranch string) { //nolint:unparam // baseBranch is always "compact-base" but kept for API symmetry with surgicalCleanup
	ctx, cancel := context.WithTimeout(context.Background(), compactorQueryTimeout)
	defer cancel()
	_, _ = db.ExecContext(ctx, "CALL DOLT_REBASE('--abort')")
	_, _ = db.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')")
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", workBranch))
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", baseBranch))
	// Clean Dolt's internal rebase branch (left behind on abort).
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", doltRebaseMainBranch))
}

// parseRebaseOrder2 parses min/max rebase_order DECIMAL strings to ints.
// Dolt's dolt_rebase table returns rebase_order as DECIMAL which the MySQL
// driver delivers as []uint8 (e.g. "1.00"), not directly scannable to int.
func parseRebaseOrder2(minStr, maxStr string) (int, int, error) {
	minF, err := strconv.ParseFloat(minStr, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid min rebase_order %q: %w", minStr, err)
	}
	maxF, err := strconv.ParseFloat(maxStr, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid max rebase_order %q: %w", maxStr, err)
	}
	return int(math.Round(minF)), int(math.Round(maxF)), nil
}

// compactorCleanup attempts to switch back to main and delete the temp branch.
// Called on error during compaction to leave the database in a clean state.
func (d *Daemon) compactorCleanup(db *sql.DB, dbName string) {
	ctx, cancel := context.WithTimeout(context.Background(), compactorQueryTimeout)
	defer cancel()

	d.logger.Printf("compactor_dog: %s: cleaning up compaction branch", dbName)

	// Try to get back to main.
	_, _ = db.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')")
	// Delete the compaction branch.
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", compactorBranchName))
}

// compactorOpenDB opens a connection to the Dolt server for the given database.
func (d *Daemon) compactorOpenDB(dbName string) (*sql.DB, error) {
	dsn := fmt.Sprintf("root@tcp(%s:%d)/%s?parseTime=true&timeout=5s&readTimeout=30s&writeTimeout=30s",
		"127.0.0.1", d.doltServerPort(), dbName)
	return sql.Open("mysql", dsn)
}

// compactorGetHead returns the current HEAD commit hash of the main branch.
func (d *Daemon) compactorGetHead(db *sql.DB, dbName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), compactorQueryTimeout)
	defer cancel()

	var hash string
	query := fmt.Sprintf("SELECT DOLT_HASHOF('main') FROM `%s`.dual", dbName)
	if err := db.QueryRowContext(ctx, query).Scan(&hash); err != nil {
		// Fallback: try without dual table.
		query = fmt.Sprintf("SELECT commit_hash FROM `%s`.dolt_log ORDER BY date DESC LIMIT 1", dbName)
		if err := db.QueryRowContext(ctx, query).Scan(&hash); err != nil {
			return "", err
		}
	}
	return hash, nil
}

// compactorGetRootCommit returns the hash of the earliest commit in the database.
func (d *Daemon) compactorGetRootCommit(db *sql.DB, dbName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), compactorQueryTimeout)
	defer cancel()

	var hash string
	query := fmt.Sprintf("SELECT commit_hash FROM `%s`.dolt_log ORDER BY date ASC LIMIT 1", dbName)
	if err := db.QueryRowContext(ctx, query).Scan(&hash); err != nil {
		return "", err
	}
	return hash, nil
}

// compactorGetRowCounts returns a map of table -> row count for all user tables.
func (d *Daemon) compactorGetRowCounts(db *sql.DB, dbName string) (map[string]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), compactorQueryTimeout)
	defer cancel()

	// Get list of user tables (excluding dolt system tables).
	query := fmt.Sprintf("SELECT table_name FROM information_schema.tables WHERE table_schema = '%s' AND table_name NOT LIKE 'dolt_%%'", dbName)
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}

	counts := make(map[string]int, len(tables))
	for _, table := range tables {
		var count int
		countQuery := fmt.Sprintf("SELECT COUNT(*) FROM `%s`.`%s`", dbName, table)
		if err := db.QueryRowContext(ctx, countQuery).Scan(&count); err != nil {
			return nil, fmt.Errorf("count %s: %w", table, err)
		}
		counts[table] = count
	}

	return counts, nil
}

// compactorRunGC runs dolt gc via SQL on the running server after compaction.
// GC reclaims unreferenced chunks left behind by the flatten operation.
// Auto-GC is on by default since Dolt 1.75.0 (triggers at 50MB journal),
// but we run it explicitly after compaction for immediate cleanup.
func (d *Daemon) compactorRunGC(dbName string) error {
	db, err := d.compactorOpenDB(dbName)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), compactorGCTimeout)
	defer cancel()

	start := time.Now()
	if _, err := db.ExecContext(ctx, "CALL dolt_gc()"); err != nil {
		elapsed := time.Since(start)
		if ctx.Err() == context.DeadlineExceeded {
			d.logger.Printf("compactor_dog: gc: %s: TIMEOUT after %v", dbName, elapsed)
			return fmt.Errorf("gc timeout after %v", elapsed)
		}
		d.logger.Printf("compactor_dog: gc: %s: failed after %v: %v", dbName, elapsed, err)
		return fmt.Errorf("dolt_gc: %w", err)
	}

	d.logger.Printf("compactor_dog: gc: %s: completed in %v", dbName, time.Since(start))
	return nil
}

// compactorFetchAndVerify fetches from the remote and checks that the local
// history contains the remote HEAD. Returns (diverged=true, nil) if the remote
// has commits not in local history — compaction must be skipped to avoid data
// loss on force-push. Returns (false, nil) if local ≥ remote or no remote.
// Mirrors the shell script's pre-flight check (run.sh lines 263-300).
func (d *Daemon) compactorFetchAndVerify(dbName string) (diverged bool, err error) {
	db, err := d.compactorOpenDB(dbName)
	if err != nil {
		return false, err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), compactorQueryTimeout)
	defer cancel()

	// Discover remote.
	var remoteName string
	if err := db.QueryRowContext(ctx, "SELECT name FROM dolt_remotes LIMIT 1").Scan(&remoteName); err != nil {
		return false, nil // No remote — nothing to verify
	}

	// Fetch from remote.
	fetchCtx, fetchCancel := context.WithTimeout(context.Background(), compactorPushTimeout)
	defer fetchCancel()
	if _, err := db.ExecContext(fetchCtx, "CALL DOLT_FETCH(?)", remoteName); err != nil {
		return false, fmt.Errorf("DOLT_FETCH %s: %w", remoteName, err)
	}

	// Get remote HEAD.
	var remoteHead string
	remoteRef := remoteName + "/main"
	err = db.QueryRowContext(ctx,
		"SELECT commit_hash FROM dolt_remote_branches WHERE name = ?", remoteRef,
	).Scan(&remoteHead)
	if err != nil {
		return false, nil // Remote ref not found — skip check
	}

	// Check if remote HEAD is an ancestor of local history.
	var isAncestor int
	err = db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_log WHERE commit_hash = ?", remoteHead,
	).Scan(&isAncestor)
	if err != nil {
		return false, fmt.Errorf("ancestor check: %w", err)
	}

	if isAncestor == 0 {
		return true, nil // Diverged — remote has commits we don't have
	}

	d.logger.Printf("compactor_dog: fetch: %s: local ≥ %s (verified)", dbName, remoteName)
	return false, nil
}

// compactorForcePush pushes the compacted database to its DoltHub remote.
// Flatten rewrites the commit graph, so a force-push is required — standard
// push always fails with non-fast-forward. This mirrors the shell script's
// DOLT_PUSH('--force', remote) at line 397 of plugins/compactor-dog/run.sh.
// Non-fatal: remote may not be configured, and push failures don't affect
// local data integrity.
func (d *Daemon) compactorForcePush(dbName string) error {
	db, err := d.compactorOpenDB(dbName)
	if err != nil {
		return err
	}
	defer db.Close()

	// Discover the remote name (if any).
	ctx, cancel := context.WithTimeout(context.Background(), compactorQueryTimeout)
	defer cancel()

	var remoteName string
	err = db.QueryRowContext(ctx, "SELECT name FROM dolt_remotes LIMIT 1").Scan(&remoteName)
	if err != nil || remoteName == "" {
		return nil // No remote configured — skip silently
	}

	// Force-push to remote.
	pushCtx, pushCancel := context.WithTimeout(context.Background(), compactorPushTimeout)
	defer pushCancel()

	start := time.Now()
	_, err = db.ExecContext(pushCtx, "CALL DOLT_PUSH('--force', ?)", remoteName)
	if err != nil {
		return fmt.Errorf("DOLT_PUSH --force to %s: %w", remoteName, err)
	}

	d.logger.Printf("compactor_dog: push: %s → %s: force-pushed in %v", dbName, remoteName, time.Since(start))
	return nil
}
