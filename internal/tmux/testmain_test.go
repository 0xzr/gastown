package tmux

import (
	"fmt"
	"os"
	"testing"
)

// TestMain scopes the package-level default tmux socket to a test-only name.
// Tests that need a live server use newTestTmux, which creates an isolated
// server per test; tests that only inspect command construction or socket
// plumbing use NewTmux()/BuildCommand directly and never touch a real server.
// This prevents test sessions from appearing on the user's interactive tmux and
// avoids socket conflicts with other packages that run during `go test ./...`.
func TestMain(m *testing.M) {
	socket := fmt.Sprintf("gt-test-%d", os.Getpid())

	// Set defaultSocket so NewTmux() connects to the test socket, not the user's
	// personal server or the sentinel that indicates "no town context". No
	// shared server is started here; each integration test that needs one gets
	// its own isolated server via newTestTmux.
	SetDefaultSocket(socket)

	code := m.Run()

	// Restore the original socket state.
	SetDefaultSocket("")

	os.Exit(code)
}
