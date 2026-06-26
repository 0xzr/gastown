package util

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FirstLine returns the first non-empty line from s, trimmed of whitespace.
// Used to extract the meaningful error message from subprocess stderr, which
// often includes multi-line cobra usage text after the actual error.
func FirstLine(s string) string {
	for _, line := range strings.SplitN(s, "\n", -1) {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return strings.TrimSpace(s)
}

// ExecWithOutput runs a command in the specified directory and returns stdout.
// If the command fails, stderr content is included in the error message.
func ExecWithOutput(workDir, cmd string, args ...string) (string, error) {
	c := exec.Command(cmd, args...) //nolint:gosec // G204: callers validate args
	c.Dir = workDir
	SetDetachedProcessGroup(c) // suppress console window flash on Windows

	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr

	if err := c.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("%s", errMsg)
		}
		return "", err
	}

	return strings.TrimSpace(stdout.String()), nil
}

// ExecRun runs a command in the specified directory.
// If the command fails, stderr content is included in the error message.
func ExecRun(workDir, cmd string, args ...string) error {
	c := exec.Command(cmd, args...) //nolint:gosec // G204: callers validate args
	c.Dir = workDir
	SetDetachedProcessGroup(c) // suppress console window flash on Windows

	var stderr bytes.Buffer
	c.Stderr = &stderr

	if err := c.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return fmt.Errorf("%s", errMsg)
		}
		return err
	}

	return nil
}

// goToolchainPathEntries returns bin directories that provide a Go toolchain,
// discovered from the locations operators use when the process was not launched
// from a login shell (so its PATH lacks `go`).
//
// Gas Town agents (refinery, daemon patrols) run as background sessions whose
// PATH does not contain the Go binary even when one is installed. Gate commands
// (e.g. "go test ./...", "make build") then fail with "go: not found" before any
// branch code runs — a toolchain failure, not a branch failure. Prepending
// these entries lets gates find `go` regardless of how the session launched.
//
// Locations checked (existing directories with a `go` binary only):
//   - $GOROOT/bin (explicit override, if GOROOT is set)
//   - $HOME/go-toolchain/go/bin (this host's toolchain install location)
//   - /usr/local/go/bin (standard tarball install location)
//
// Order is deliberate: operator-set GOROOT wins, then the host toolchain, then
// the conventional system install location.
func goToolchainPathEntries() []string {
	var entries []string
	seen := make(map[string]bool)

	add := func(dir string) {
		if dir == "" {
			return
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			abs = dir
		}
		if seen[abs] {
			return
		}
		// Only prepend directories that actually contain a `go` file, so an
		// absent install location cannot shadow the inherited PATH. We check
		// presence only (os.Stat), not executability: real Go toolchain
		// installs always mark `go` executable, and the gate subprocess will
		// surface a non-executable `go` as a permission-denied failure at run
		// time — still actionable, and we avoid an extra mode-bit check on
		// every gate. TestGateCommandEnv_NonExecutableGoIsDetected pins this
		// contract.
		goBin := filepath.Join(abs, "go")
		info, err := os.Stat(goBin)
		if err != nil || info.IsDir() {
			return
		}
		seen[abs] = true
		entries = append(entries, abs)
	}

	if root := os.Getenv("GOROOT"); root != "" {
		add(filepath.Join(root, "bin"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		add(filepath.Join(home, "go-toolchain", "go", "bin"))
	}
	add("/usr/local/go/bin")

	return entries
}

// GateCommandEnv returns the environment for a gate/test subprocess. When `go`
// is already resolvable on the inherited PATH the environment is unchanged;
// otherwise Go toolchain bin directories are prepended to PATH so that Go-based
// gates ("go test ./...", "make build") run instead of failing with "go: not
// found".
//
// Returns nil to signal "use the parent environment unchanged" (os/exec's
// default when cmd.Env is nil), which avoids a needless copy in the common case.
// This is the shared source of truth used by both the refinery's gate/test
// execution and the daemon's main-branch test patrol.
func GateCommandEnv() []string {
	if _, err := exec.LookPath("go"); err == nil {
		return nil // `go` already on PATH — nothing to fix
	}

	entries := goToolchainPathEntries()
	if len(entries) == 0 {
		return nil // no toolchain found anywhere; let the gate report its own error
	}

	path := os.Getenv("PATH")
	prefix := strings.Join(entries, string(os.PathListSeparator))
	if path == "" {
		path = prefix
	} else {
		path = prefix + string(os.PathListSeparator) + path
	}

	env := os.Environ()
	foundPath := false
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			env[i] = "PATH=" + path
			foundPath = true
			break
		}
	}
	if !foundPath {
		env = append(env, "PATH="+path)
	}
	return env
}
