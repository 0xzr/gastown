package mrtelemetry

import (
	"os"
	"syscall"
)

// flock acquires an exclusive lock on f's underlying file descriptor. It is a
// thin wrapper over flock(2) so multiple refinery processes (each with its
// own Recorder) coordinate appends to the same date-sharded JSONL file. Lock
// acquisition is best-effort: callers treat a lock error as non-fatal and fall
// back to a plain append, preferring a written record over a perfectly-
// coordinated one.
func flock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// funlock releases a lock previously acquired by flock.
func funlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
