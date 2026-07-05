//go:build windows

package refinery

import (
	"fmt"
	"os/exec"
)

func durableReviewExitErrorDetails(exitErr *exec.ExitError) (reason string, exitCode *int, signalName string, gateExit string) {
	if code := exitErr.ExitCode(); code >= 0 {
		return "exec-failure", durableReviewIntPtr(code), "", fmt.Sprintf("%d", code)
	}
	return "exec-failure", nil, "", "unknown"
}
