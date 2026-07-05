//go:build !windows

package refinery

import (
	"fmt"
	"os/exec"
	"syscall"
)

func durableReviewExitErrorDetails(exitErr *exec.ExitError) (reason string, exitCode *int, signalName string, gateExit string) {
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
		if status.Signaled() {
			signalName = status.Signal().String()
			return "signal", nil, signalName, signalName
		}
		code := status.ExitStatus()
		return "exec-failure", durableReviewIntPtr(code), "", fmt.Sprintf("%d", code)
	}
	if code := exitErr.ExitCode(); code >= 0 {
		return "exec-failure", durableReviewIntPtr(code), "", fmt.Sprintf("%d", code)
	}
	return "exec-failure", nil, "", "unknown"
}
