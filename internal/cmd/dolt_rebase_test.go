package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDoltRebaseLock_SerializesSameDB verifies that the manual `gt dolt rebase`
// command competes for the same per-database advisory lock as the daemon's
// compactor dog, preventing cross-tool TOCTOU races on the branch swap.
func TestDoltRebaseLock_SerializesSameDB(t *testing.T) {
	tmpDir := t.TempDir()
	dbName := "testdb"

	release1, err := acquireDoltRebaseLock(tmpDir, dbName)
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}

	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		release2, err := acquireDoltRebaseLock(tmpDir, dbName)
		if err != nil {
			t.Errorf("second acquire failed: %v", err)
			return
		}
		defer release2()
	}()

	select {
	case <-done2:
		t.Fatal("second goroutine acquired lock while first still held it")
	case <-time.After(200 * time.Millisecond):
		// Expected: second goroutine is blocked waiting for the lock.
	}

	release1()

	select {
	case <-done2:
	case <-time.After(2 * time.Second):
		t.Fatal("second goroutine did not acquire lock after first released")
	}
}

// TestDoltRebaseLock_DifferentDBsAreIndependent verifies that the lock is
// scoped per database.
func TestDoltRebaseLock_DifferentDBsAreIndependent(t *testing.T) {
	tmpDir := t.TempDir()

	release1, err := acquireDoltRebaseLock(tmpDir, "db-a")
	if err != nil {
		t.Fatalf("acquire db-a: %v", err)
	}
	defer release1()

	done := make(chan struct{})
	go func() {
		defer close(done)
		release2, err := acquireDoltRebaseLock(tmpDir, "db-b")
		if err != nil {
			t.Errorf("acquire db-b: %v", err)
			return
		}
		defer release2()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("different database should not block on the dolt rebase lock")
	}
}

// TestDoltRebaseLock_PathSanitization verifies that database names with path
// separators are sanitized so the lock file stays under the intended directory.
func TestDoltRebaseLock_PathSanitization(t *testing.T) {
	tmpDir := t.TempDir()

	release, err := acquireDoltRebaseLock(tmpDir, "rig/db:name")
	if err != nil {
		t.Fatalf("acquire lock for unsafe db name: %v", err)
	}
	defer release()

	lockPath := filepath.Join(tmpDir, ".runtime", "locks", "compactor-surgical", "rig_db_name.flock")
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("expected sanitized lock file at %s: %v", lockPath, err)
	}
}
