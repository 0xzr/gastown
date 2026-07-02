package polecat

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/steveyegge/gastown/internal/testutil"
	"github.com/steveyegge/gastown/internal/tmux"
)

func TestMain(m *testing.M) {
	var tmuxSocket string
	if _, err := exec.LookPath("tmux"); err == nil {
		tmuxSocket = fmt.Sprintf("gt-test-polecat-%d", os.Getpid())
		tmux.SetDefaultSocket(tmuxSocket)
	}

	code := m.Run()

	if tmuxSocket != "" {
		_ = tmux.NewTmuxWithSocket(tmuxSocket).KillServerAndRemoveSocket()
		tmux.SetDefaultSocket("")
	}
	testutil.TerminateDoltContainer()
	os.Exit(code)
}
