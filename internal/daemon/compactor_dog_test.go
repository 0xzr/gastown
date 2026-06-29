package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSurgicalRebaseLock_SerializesSameDB verifies that two goroutines cannot
// hold the per-database surgical rebase lock at the same time. This is the
// regression guard for gastown-5nz: without serialization, a second surgical
// rebase could slip through the TOCTOU window between the HEAD verification
// and the branch swap.
func TestSurgicalRebaseLock_SerializesSameDB(t *testing.T) {
	tmpDir := t.TempDir()
	dbName := "testdb"

	acquired1 := make(chan struct{})
	release1 := make(chan struct{})
	done2 := make(chan struct{})

	go func() {
		release, err := acquireSurgicalRebaseLock(tmpDir, dbName)
		if err != nil {
			t.Errorf("first acquire failed: %v", err)
			close(acquired1)
			return
		}
		close(acquired1)
		<-release1
		release()
	}()

	<-acquired1

	go func() {
		defer close(done2)
		release, err := acquireSurgicalRebaseLock(tmpDir, dbName)
		if err != nil {
			t.Errorf("second acquire failed: %v", err)
			return
		}
		defer release()
	}()

	select {
	case <-done2:
		t.Fatal("second goroutine acquired lock while first still held it")
	case <-time.After(200 * time.Millisecond):
		// Expected: second goroutine is blocked waiting for the lock.
	}

	close(release1)

	select {
	case <-done2:
	case <-time.After(2 * time.Second):
		t.Fatal("second goroutine did not acquire lock after first released")
	}
}

// TestSurgicalRebaseLock_DifferentDBsAreIndependent verifies that the lock is
// scoped per database; operations on different databases must not contend.
func TestSurgicalRebaseLock_DifferentDBsAreIndependent(t *testing.T) {
	tmpDir := t.TempDir()

	release1, err := acquireSurgicalRebaseLock(tmpDir, "db-a")
	if err != nil {
		t.Fatalf("acquire db-a: %v", err)
	}
	defer release1()

	done := make(chan struct{})
	go func() {
		defer close(done)
		release2, err := acquireSurgicalRebaseLock(tmpDir, "db-b")
		if err != nil {
			t.Errorf("acquire db-b: %v", err)
			return
		}
		defer release2()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("different database should not block on the surgical rebase lock")
	}
}

// TestSurgicalRebaseLock_PathSanitization verifies that database names
// containing path separators are sanitized so the lock file stays under the
// intended directory.
func TestSurgicalRebaseLock_PathSanitization(t *testing.T) {
	tmpDir := t.TempDir()

	release, err := acquireSurgicalRebaseLock(tmpDir, "rig/db:name")
	if err != nil {
		t.Fatalf("acquire lock for unsafe db name: %v", err)
	}
	defer release()

	lockPath := filepath.Join(tmpDir, ".runtime", "locks", surgicalRebaseLockDir, "rig_db_name.flock")
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("expected sanitized lock file at %s: %v", lockPath, err)
	}
}
