//go:build windows

package mrtelemetry

import "os"

// withFileLock is a no-op fallback on Windows. The refinery is single-threaded
// per rig in practice, so concurrent appends within a process are already
// serialized by the Recorder mutex; cross-process contention is rare on
// Windows dev machines. Locking can be added via LockFileEx if needed.
func withFileLock(f *os.File, fn func() error) error {
	return fn()
}
