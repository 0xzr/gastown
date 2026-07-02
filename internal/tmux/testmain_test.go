package tmux

import (
	"os"
	"testing"
)

// TestMain sets a safe default socket for any test that uses NewTmux() directly.
// Tests that need an actual tmux server should use newTestTmux(), which creates
// a per-test isolated socket so parallel tests cannot kill a shared server and
// cause intermittent "no tmux server running" failures.
func TestMain(m *testing.M) {
	// Set a non-default socket name so tests never accidentally connect to the
	// user's interactive tmux server. Any test that needs a real server must use
	// newTestTmux(t), which supplies its own isolated socket and cleanup.
	socket := "gt-test-default"
	SetDefaultSocket(socket)

	code := m.Run()

	_ = NewTmuxWithSocket(socket).KillServerAndRemoveSocket()
	SetDefaultSocket("")
	os.Exit(code)
}
