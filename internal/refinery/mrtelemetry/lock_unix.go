//go:build !windows

package mrtelemetry

import (
	"os"
	"syscall"
)

// withFileLock acquires an exclusive (write) lock on the file for the duration
// of fn, then releases it. Used to serialize concurrent appends to the same
// telemetry JSONL file across goroutines and processes.
//
// On Unix this uses flock(2) on the file descriptor. The lock is advisory; all
// writers must cooperate by calling this function.
func withFileLock(f *os.File, fn func() error) error {
	fd := f.Fd()
	if err := syscall.Flock(int(fd), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() {
		_ = syscall.Flock(int(fd), syscall.LOCK_UN)
	}()
	return fn()
}
