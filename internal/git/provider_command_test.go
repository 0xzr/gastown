package git

import (
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestRunProviderCommand_TimesOutOnStalledCommand drives the cancellation path
// with a tiny deadline against a `sleep` invocation that would otherwise exceed
// it. It verifies:
//   - runProviderCommand returns ErrProviderTimeout (so callers can map to a
//     fail-closed verdict without conflating timeouts with ordinary errors).
//   - the underlying command is killed near the deadline, NOT after the full
//     sleep duration. This is the invariant that prevents orphan subprocesses
//     from leaking past the bounded provider window (gastown-6z5).
func TestRunProviderCommand_TimesOutOnStalledCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group kill semantics differ on windows")
	}
	deadline := 80 * time.Millisecond
	sleepDur := 5 * time.Second

	start := time.Now()
	result, err := runProviderCommandWithTimeout("sleep", []string{formatSleepDuration(sleepDur)}, "", "test sleep", deadline)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected timeout error, got nil (result=%q)", string(result.Stdout))
	}
	if !errors.Is(err, ErrProviderTimeout) {
		t.Fatalf("expected ErrProviderTimeout, got %v", err)
	}
	if !strings.Contains(err.Error(), "test sleep") {
		t.Errorf("expected opLabel in error, got %q", err.Error())
	}
	// Process-group kill must have fired well before the sleep duration. Allow
	// generous slack for CI scheduling, but anything near the sleep duration
	// means the kill did not propagate to the child.
	if elapsed >= sleepDur {
		t.Fatalf("command ran for %v, expected kill near %v deadline", elapsed, deadline)
	}
	if elapsed > deadline+5*time.Second {
		t.Fatalf("cancellation took %v past %v deadline", elapsed-deadline, deadline)
	}
}

// TestRunProviderCommand_FailsClosedOnNonZeroExit exercises the non-timeout
// error branch: a command that exits non-zero should surface an error that
// names the caller's opLabel but does NOT include the raw args. This is the
// redaction invariant that keeps secret-bearing flags (e.g.
// "Authorization: Bearer <token>") out of error messages and downstream logs.
func TestRunProviderCommand_FailsClosedOnNonZeroExit(t *testing.T) {
	const secret = "TOPSECRET-BEARER-XYZ"
	// `false` is a portable command that always exits 1 with no output.
	_, err := runProviderCommandWithTimeout("false", nil, "", "test false op", 2*time.Second, secret)
	if err == nil {
		t.Fatal("expected error from non-zero exit, got nil")
	}
	if errors.Is(err, ErrProviderTimeout) {
		t.Fatalf("non-zero exit must not be classified as timeout, got %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked redacted token: %v", err)
	}
	if !strings.Contains(err.Error(), "test false op") {
		t.Errorf("expected opLabel in error, got %q", err.Error())
	}
}

// TestRunProviderCommand_ScrubsTokenFromNonZeroStderr pins the regression from
// gastown-6z5: non-timeout errors must build their detail from scrubbed stderr,
// not the raw stderr buffer, because Bitbucket curl calls pass BITBUCKET_TOKEN
// in an Authorization header and rely on this helper for redaction.
func TestRunProviderCommand_ScrubsTokenFromNonZeroStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh -c")
	}
	const secret = "TOPSECRET-BEARER-XYZ"
	script := "printf '%s\n' 'bitbucket echoed Authorization: Bearer " + secret + "' >&2; exit 1"

	result, err := runProviderCommandWithTimeout("sh", []string{"-c", script}, "", "bitbucket find-pr", 2*time.Second, secret)
	if err == nil {
		t.Fatal("expected error from non-zero exit, got nil")
	}
	if errors.Is(err, ErrProviderTimeout) {
		t.Fatalf("non-zero exit must not be classified as timeout, got %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked redacted token: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("expected [REDACTED] marker in error, got %q", err.Error())
	}
	if result == nil {
		t.Fatal("expected sanitized result alongside error")
	}
	if strings.Contains(string(result.Stderr), secret) {
		t.Fatalf("stderr leaked redacted token: %q", string(result.Stderr))
	}
	if !strings.Contains(string(result.Stderr), "[REDACTED]") {
		t.Fatalf("expected [REDACTED] marker in stderr, got %q", string(result.Stderr))
	}
}

// TestRunProviderCommand_PassesThroughValidOutput verifies the happy path: a
// command that exits 0 with non-empty stdout returns the stdout bytes
// unmodified and a nil error.
func TestRunProviderCommand_PassesThroughValidOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh -c echo")
	}
	result, err := runProviderCommandWithTimeout("sh", []string{"-c", "printf 'hello\\nworld'"}, "", "test echo op", 2*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result.Stdout) != "hello\nworld" {
		t.Fatalf("unexpected stdout: %q", string(result.Stdout))
	}
}

// TestRunProviderCommand_ScrubsTokensFromOutput verifies that any token
// supplied as a redacted argument is replaced by "[REDACTED]" in the returned
// stdout/stderr before the caller can put it into a review evidence field or
// merge output path.
func TestRunProviderCommand_ScrubsTokensFromOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh -c echo")
	}
	const secret = "TOPSECRET-BEARER-XYZ"
	result, err := runProviderCommandWithTimeout("sh", []string{"-c", "echo " + secret}, "", "test scrub op", 2*time.Second, secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(result.Stdout), secret) {
		t.Fatalf("stdout leaked redacted token: %q", string(result.Stdout))
	}
	if !strings.Contains(string(result.Stdout), "[REDACTED]") {
		t.Fatalf("expected [REDACTED] marker in stdout, got %q", string(result.Stdout))
	}
}

// formatSleepDuration renders a duration the way `sleep` accepts across the
// supported platforms (an integer number of seconds is sufficient for our
// test, and avoids the fractional-seconds syntax that GNU sleep requires).
func formatSleepDuration(d time.Duration) string {
	secs := int64(d / time.Second)
	if secs < 1 {
		secs = 1
	}
	return itoa(secs)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
