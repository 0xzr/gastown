package git

import (
	"strings"
	"testing"
	"time"
)

// TestRunProviderCommand_TimesOutOnStalledCommand pins the gastown-6z5 rework
// requirement that an external review-provider command (gh/curl) cannot hang the
// refinery merge gate: a command that exceeds the bounded deadline is killed
// and returns a timeout error rather than blocking forever. The caller maps
// that error to a fail-closed (UNAVAILABLE / deferred) verdict, never PASS.
//
// reviewProviderTimeout is shrunk to a deterministic window so the test does
// not depend on the 45s production value; production code must not mutate it.
func TestRunProviderCommand_TimesOutOnStalledCommand(t *testing.T) {
	t.Cleanup(func() { reviewProviderTimeout = 45 * time.Second })
	reviewProviderTimeout = 100 * time.Millisecond

	g := NewGit("")

	start := time.Now()
	_, err := g.runProviderCommand("sleep", "10")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error from a stalled provider command, got nil")
	}
	// A distinct, machine-checkable timeout signal lets callers classify
	// fail-closed. We assert on the message because the error is not a typed
	// sentinel; the phrase "timed out" is the contract runProviderCommand
	// emits on context deadline (mirroring runWithTimeout's message).
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected error to report a timeout, got: %v", err)
	}
	// The command must have been killed near the deadline, not after the full
	// sleep window — this is what proves the refinery does not hang.
	if elapsed > 5*time.Second {
		t.Fatalf("stalled provider command was not killed promptly: took %v", elapsed)
	}
}

// TestRunProviderCommand_FailsClosedOnNonZeroExit confirms a provider command
// that exits non-zero (e.g. gh returning an API/auth error) surfaces an error
// rather than a parseable-but-wrong result. Callers translate any such error to
// an UNAVAILABLE verdict instead of authoritatively approving the merge.
func TestRunProviderCommand_FailsClosedOnNonZeroExit(t *testing.T) {
	t.Cleanup(func() { reviewProviderTimeout = 45 * time.Second })
	reviewProviderTimeout = 5 * time.Second

	g := NewGit("")
	// `false` always exits 1; a stand-in for a provider that errors out.
	_, err := g.runProviderCommand("false")
	if err == nil {
		t.Fatal("expected an error from a failing provider command, got nil")
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Fatalf("a failing provider command should not be reported as a timeout: %v", err)
	}
}

// TestRunProviderCommand_PassesThroughValidOutput is the sanity case: a
// well-formed provider command's stdout is returned for parsing, so the
// happy path (a real gh/curl result) is unaffected by the bounded context.
func TestRunProviderCommand_PassesThroughValidOutput(t *testing.T) {
	t.Cleanup(func() { reviewProviderTimeout = 45 * time.Second })
	reviewProviderTimeout = 5 * time.Second

	g := NewGit("")
	out, err := g.runProviderCommand("printf", "%s", `{"baseRefName":"main"}`)
	if err != nil {
		t.Fatalf("expected success from a valid provider command, got: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != `{"baseRefName":"main"}` {
		t.Fatalf("expected provider stdout to pass through unchanged, got: %q", got)
	}
}
