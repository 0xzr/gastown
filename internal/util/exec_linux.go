//go:build linux

package util

import (
	"os/exec"
	"syscall"
)

// SetTestProcessGroup configures a command to run in its own process group and
// asks the kernel to kill it if the parent test binary dies abruptly. This is
// the same Pdeathsig discipline used by the M1.6 doltserver test helper; it
// prevents dolt sql-server children spawned by short-lived test commands (e.g.
// bd init) from outliving a SIGKILL'd go test parent.
func SetTestProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
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
