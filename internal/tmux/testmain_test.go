package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// testServerErr is set by TestMain when the shared test tmux server could not
// be brought up (after retries). When non-nil, newTestTmux skips the test with
// a clear reason instead of letting the first tmux operation fail with a
// spurious "no server running" — a flake that historically caused
// MERGE_FAILED gate rejections of unrelated MRs (gastown-e6n). It is nil on the
// green path (server up, or tmux absent and tests skip individually).
var testServerErr error

// TestMain sets up a dedicated tmux server for the package's integration tests.
// All tests that call newTestTmux() share this isolated server, which is torn
// down after all tests complete. This prevents test sessions from appearing on
// the user's interactive tmux and avoids socket conflicts with other packages.
func TestMain(m *testing.M) {
	socket := fmt.Sprintf("gt-test-%d", os.Getpid())

	// Set defaultSocket so NewTmux() connects to the test server, not the
	// user's personal server or the sentinel that indicates "no town context".
	SetDefaultSocket(socket)

	// Start a sentinel session to keep the server alive for the entire test run.
	// Without this, tests that kill their last session inadvertently take down
	// the server, leaving a stale socket that prevents subsequent new-session
	// calls from restarting it (tmux sees the socket file but no listener).
	// The sentinel uses a name no individual test touches, so it outlives all
	// per-test sessions. TestMain kills the whole server at the end.
	//
	// The bootstrap is retried and verified (startTestServer): a single
	// fire-and-forget new-session can lose the startup race under sandbox/CI
	// load and leave a socket with no listener, after which every newTestTmux()
	// would hit "no server running" and fail a different test each run. If the
	// server truly cannot start, testServerErr is set and newTestTmux skips.
	testServerErr = startTestServer(socket, "gt-test-sentinel")

	code := m.Run()

	// Kill the test tmux server and restore the original socket state.
	_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	SetDefaultSocket("")

	os.Exit(code)
}

// startTestServer boots the shared test tmux server with a sentinel session and
// verifies it is reachable before returning. It retries a handful of times with
// a short backoff because tmux server startup occasionally loses the race on the
// first new-session under load (the socket file is created before the listener
// is ready). Returns nil once the server responds, or an error describing the
// last failure if it never came up — callers (newTestTmux) then skip rather
// than fail. On a healthy machine the first attempt succeeds and there is no
// added latency.
func startTestServer(socket, sentinel string) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		// tmux not installed; individual tests skip via hasTmux() in newTestTmux.
		return nil
	}
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		// new-session brings the server up; the error is ignored because a
		// "duplicate session" result still proves the server is reachable (a
		// prior process left it running on this socket).
		_ = exec.Command("tmux", "-u", "-L", socket, "new-session", "-d", "-s", sentinel).Run()
		// list-sessions succeeds (exit 0) only when a server is actually
		// listening on the socket — the precise readiness signal we need.
		if err := exec.Command("tmux", "-L", socket, "list-sessions").Run(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		// Back off before retrying so the server has time to come up.
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("tmux test server on socket %q did not come up after %d attempts: %w", socket, maxAttempts, lastErr)
}
