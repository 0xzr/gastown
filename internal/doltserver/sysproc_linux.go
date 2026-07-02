//go:build linux

package doltserver

import (
	"os/exec"
	"syscall"
)

// setTestProcessGroup puts a test-owned server in its own process group and
// asks the kernel to kill it if the test binary dies abruptly. Pdeathsig is
// Linux-specific (not present on darwin/freebsd), so this variant is split out
// from sysproc_nolinux.go behind a linux build tag.
func setTestProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
