//go:build !windows && !linux

package util

import (
	"os/exec"
	"syscall"
)

// SetTestProcessGroup configures a command to run in its own process group.
// On non-Linux Unix, the kernel does not support Pdeathsig, so we isolate the
// process group; see exec_linux.go for the Linux variant that also requests
// parent-death cleanup.
func SetTestProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// exec.CommandContext installs a non-nil Cancel hook; exec.Command leaves it
	// nil and Go rejects a non-nil Cancel on a non-context command. Only wrap an
	// existing Cancel hook so this helper is safe for both creation paths.
	if cmd.Cancel != nil {
		oldCancel := cmd.Cancel
		cmd.Cancel = func() error {
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			return oldCancel()
		}
	}
}
