package cmd

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// TestDoltFlatten_RejectsMaliciousDBName is a regression test for gastown-wes
// (retro-bug P0). dbName is interpolated unescaped into multiple SQL
// statements below runDoltFlatten — if validation is missing or bypassed,
// a CLI-arg like `x` -- ; DROP DATABASE foo` escapes backticks and lets the
// operator stomp on production data. The guard must reject BEFORE any Dolt
// connection is opened.
//
// We exercise the cobra tree (rootCmd → doltCmd → doltFlattenCmd) rather than
// calling runDoltFlatten directly so the test catches future regressions in
// how the command is wired (ExactArgs, RunE binding, etc.).
func TestDoltFlatten_RejectsMaliciousDBName(t *testing.T) {
	// Each test case is a dbName that, if smuggled past the validator, would
	// break out of the surrounding backtick identifier or single-quote string
	// literal in the Sprintf calls inside runDoltFlatten.
	cases := []string{
		"`x` -- ; DROP DATABASE foo",
		"x'; DROP TABLE issues; --",
		"x' OR '1'='1",
		"db;DROP DATABASE foo",
		"x' UNION SELECT 1--",
		"db name",
		"db/name",
		"db;DROP",
		"db\nname",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := executeCmd(t, "dolt", "flatten", "--yes-i-am-sure", name)
			if err == nil {
				t.Fatalf("dolt flatten %q: expected error, got success. output:\n%s", name, out)
			}
			if !strings.Contains(err.Error(), "invalid database name") {
				t.Errorf("dolt flatten %q: error %q should mention invalid database name", name, err)
			}
			// We must NEVER see "dolt server is not running" — that's a
			// downstream check that fires only AFTER the validator. If it shows
			// up, validation ran in the wrong order (or didn't run at all) and
			// the regex is silently bypassed.
			if strings.Contains(out, "Dolt server is not running") {
				t.Errorf("dolt flatten %q reached doltserver.IsRunning; validation order is wrong. output:\n%s",
					name, out)
			}
		})
	}
}

// TestDoltRebase_RejectsMaliciousDBName mirrors TestDoltFlatten for rebase.
// Same retro-bug — same SQLi surface — different Sprintf callsites.
func TestDoltRebase_RejectsMaliciousDBName(t *testing.T) {
	cases := []string{
		"`x` -- ; DROP DATABASE foo",
		"x'; DROP TABLE issues; --",
		"x' OR '1'='1",
		"db;DROP DATABASE foo",
		"x' UNION SELECT 1--",
		"db name",
		"db/name",
		"db;DROP",
		"db\nname",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := executeCmd(t, "dolt", "rebase", "--yes-i-am-sure", name)
			if err == nil {
				t.Fatalf("dolt rebase %q: expected error, got success. output:\n%s", name, out)
			}
			if !strings.Contains(err.Error(), "invalid database name") {
				t.Errorf("dolt rebase %q: error %q should mention invalid database name", name, err)
			}
			if strings.Contains(out, "Dolt server is not running") {
				t.Errorf("dolt rebase %q reached doltserver.IsRunning; validation order is wrong. output:\n%s",
					name, out)
			}
		})
	}
}

// validNameRe matches the output-line scanner used to detect the
// "Dolt server is not running" sentinel only when it actually fires
// (not when the validator references it). The sentinel prints verbatim with no
// leading whitespace, and we just substring-check above — no regex needed, but
// keep this hook documented in case future assertions want stricter matching.
var _ = regexp.MustCompile(`Dolt server is not running`)

// executeCmd runs `gt <args...>` through the cobra tree and returns both
// stdout+stderr captured together (for sentinel detection) and the cobra
// error. Modeled on executeMayorDecision from mayor_decision_test.go
// (commit gastown-l9j): the cobra tree catches regressions in command wiring
// (ExactArgs, RunE binding, subcommand registration) that direct-func tests
// cannot.
//
// We use the package-local `rootCmd` rather than a fresh tree because the
// production binary wires every other gt subcommand onto rootCmd at package
// init; rebuilding it would lose that wiring and exercise a different beast
// than the one the user actually runs.
func executeCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(args)

	err := rootCmd.Execute()
	out := buf.String()
	// Reset for the next subtest — cobra.SetArgs/App.SetArgs is sticky.
	rootCmd.SetArgs(nil)
	rootCmd.SetOut(nil)
	rootCmd.SetErr(nil)
	return out, err
}
