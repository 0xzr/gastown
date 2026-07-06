//go:build !windows

package util

import (
	"os/exec"
	"syscall"
)

// SetProcessGroup configures a command to run in its own process group so that
// signals to the parent group don't reach the child. If the command was created
// with CommandContext, the existing Cancel hook is wrapped so context
// cancellation kills the entire process tree, preventing orphaned children.
func SetProcessGroup(cmd *exec.Cmd) {
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

// SetDetachedProcessGroup configures a command to run in its own process
// group without installing a cancellation hook.
func SetDetachedProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
