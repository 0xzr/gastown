//go:build !windows && !linux

package doltserver

import (
	"os/exec"
	"syscall"
)

// setTestProcessGroup puts a test-owned server in its own process group.
// On non-Linux Unix (darwin, freebsd) the kernel does not support Pdeathsig,
// so we just isolate the process group; see sysproc_linux.go for the
// Linux-only variant that also requests parent-death cleanup.
func setTestProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
