package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/util"
)

// reviewProviderTimeout bounds the wall-clock time a single provider
// subprocess (gh / curl on the reviewer / merge-gate path) may run before it is
// cancelled. A stalled provider (auth relogin, paginate loop, unreachable API)
// must not hang the refinery merge queue.
//
// Overridable in tests via SetReviewProviderTimeout.
var reviewProviderTimeout = 45 * time.Second

// SetReviewProviderTimeout overrides the default provider-command timeout.
// Tests use this to drive the cancellation path with a tiny deadline.
func SetReviewProviderTimeout(d time.Duration) { reviewProviderTimeout = d }

// ErrProviderTimeout is returned by runProviderCommand when the provider
// subprocess exceeds the bounded timeout. It is a distinct sentinel so callers
// can map timeout failures to a fail-closed verdict (UNAVAILABLE / UnknownBasis /
// commit_history) without conflating them with ordinary non-zero exits or
// unparseable output. It is NEVER wrapped with the raw command arguments so
// authorization headers (e.g. "Authorization: Bearer <BITBUCKET_TOKEN>") cannot
// leak into error messages, review evidence, or merge output paths.
var ErrProviderTimeout = errors.New("provider command timed out")

// ProviderCommandResult holds the sanitized result of a provider subprocess.
// stdout and stderr are scrubbed of any value in redactedTokens so a token
// echoed by a misbehaving provider cannot leak into downstream evaluation.
type ProviderCommandResult struct {
	Stdout []byte
	Stderr []byte
}

// runProviderCommand runs an external provider command (gh, curl) with a
// bounded timeout and a process-group kill on context cancellation. It is the
// shared helper for every gh / curl call on the reviewer / merge-gate path so
// the timeout and redaction invariants are enforced uniformly.
//
// The function deliberately avoids embedding the raw command arguments in any
// returned error. Provider commands routinely pass secrets as flags
// (e.g. "Authorization: Bearer <token>") and an *exec.ExitError surfaces the
// full argv in its String() — formatting such an error would leak the secret
// into any log line, review evidence field, or merge output. Instead the
// returned error names the operation label supplied by the caller (a short
// human-readable description of what was being queried, NOT the args) and
// includes the trimmed first line of stderr when the provider produced any.
//
// On timeout, the entire process group is killed (so a parent gh that forked
// a child curl cannot leak the child), and ErrProviderTimeout is returned
// without any command-arg fragment.
func runProviderCommand(name string, args []string, workDir string, opLabel string, redactedTokens ...string) (*ProviderCommandResult, error) {
	return runProviderCommandWithTimeout(name, args, workDir, opLabel, reviewProviderTimeout, redactedTokens...)
}

// runProviderCommandWithTimeout is the timeout-explicit form for tests and for
// callers that need a per-call deadline other than the package default.
func runProviderCommandWithTimeout(name string, args []string, workDir string, opLabel string, timeout time.Duration, redactedTokens ...string) (*ProviderCommandResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	// SetProcessGroup configures the cmd to run in its own process group and
	// installs cmd.Cancel so context cancellation kills the entire process
	// group (SIGKILL to -pgid). This is what prevents an orphan child when
	// the provider (gh / curl) forks a subprocess.
	util.SetProcessGroup(cmd)
	if workDir != "" {
		cmd.Dir = workDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	scrubbedOut := scrubSecrets(stdout.Bytes(), redactedTokens)
	scrubbedErr := scrubSecrets(stderr.Bytes(), redactedTokens)

	if err != nil {
		// Context-cancellation / deadline-exceeded is reported as a distinct
		// sentinel. Note: cmd.Run() may surface either context.DeadlineExceeded
		// (timeout fired) or context.Canceled (caller cancelled); both indicate
		// the bounded window elapsed, so we map them to ErrProviderTimeout only
		// when the configured timeout actually fired. (Other goroutines calling
		// cancel() would not be present in this code path.)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return &ProviderCommandResult{Stdout: scrubbedOut, Stderr: scrubbedErr}, fmt.Errorf("%w: %s", ErrProviderTimeout, opLabel)
		}
		// Any other error: non-zero exit, signal, "executable file not found",
		// etc. Do NOT include the args in the formatted message. opLabel is
		// the caller's sanitized description; stderr is the trimmed first
		// line of the provider's own diagnostic, scrubbed of any token.
		detail := strings.TrimSpace(firstLine(string(scrubbedErr)))
		if detail == "" {
			return &ProviderCommandResult{Stdout: scrubbedOut, Stderr: scrubbedErr}, fmt.Errorf("provider command %q failed: %v", opLabel, err)
		}
		return &ProviderCommandResult{Stdout: scrubbedOut, Stderr: scrubbedErr}, fmt.Errorf("provider command %q failed: %s", opLabel, detail)
	}

	return &ProviderCommandResult{Stdout: scrubbedOut, Stderr: scrubbedErr}, nil
}

// scrubSecrets replaces any occurrence of a redacted token with a constant
// "[REDACTED]" marker. Tokens are matched verbatim (no regex, no quoting) so
// a stray token echoed by a misbehaving provider cannot leak through review
// evidence or merge output paths.
func scrubSecrets(b []byte, tokens []string) []byte {
	if len(tokens) == 0 {
		return b
	}
	out := string(b)
	for _, t := range tokens {
		if t == "" {
			continue
		}
		out = strings.ReplaceAll(out, t, "[REDACTED]")
	}
	return []byte(out)
}

// firstLine returns the first non-empty trimmed line of s, or "" if s is
// entirely empty / whitespace. It mirrors util.FirstLine but is duplicated
// here to keep this file self-contained for the provider-command path.
func firstLine(s string) string {
	for _, line := range strings.SplitN(s, "\n", -1) {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}
