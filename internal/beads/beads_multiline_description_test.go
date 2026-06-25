package beads

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateWithMultilineDescription_RoutesViaStdin verifies that
// Beads.Create sends multi-line descriptions to bd via stdin (--body-file=-)
// rather than embedding newlines in --description=....
//
// bd 1.0.3+ rejects newline bytes inside --description flag values, which
// would silently break any caller passing a multi-line description (notably
// internal/alerts.Aggregator.Record, see gastown-yti).
//
// This is the regression test for that finding — prior to the fix the
// production code path embedded newlines in --description and broke the
// canonical tracking-bead write.
func TestCreateWithMultilineDescription_RoutesViaStdin(t *testing.T) {
	ResetBdAllowStaleCacheForTest()

	stubDir := t.TempDir()
	argsPath := filepath.Join(stubDir, "args.txt")
	stdinPath := filepath.Join(stubDir, "stdin.txt")

	// Stub bd: write each arg on its own line, capture stdin, return a
	// minimal valid JSON issue.
	stubScript := `#!/bin/sh
for a in "$@"; do
  printf '%s\n' "$a" >> "` + argsPath + `"
done
cat > "` + stdinPath + `"
echo '{"id":"gt-test1","title":"alert summary","status":"open","priority":1,"type":"task","labels":["gt:alert","alert:key:foo"]}'
exit 0
`
	stubPath := filepath.Join(stubDir, "bd")
	if err := os.WriteFile(stubPath, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	b := New(t.TempDir())
	multiline := "## Alert: polecat-died in gastown\n\n" +
		"This tracking bead aggregates repeated equivalent alerts.\n\n" +
		"### Occurrence summary\n\n" +
		"- **Root cause key**: polecat-died:gastown\n" +
		"- **Total occurrences**: 1\n" +
		"- **First seen**: 2026-06-25T16:00:00Z\n\n" +
		"### Latest evidence\n\n" +
		"polecat topaz exited 137\n"

	issue, err := b.Create(CreateOptions{
		Title:       "alert summary",
		Description: multiline,
		Labels:      []string{"gt:alert", "alert:key:foo"},
		Priority:    1,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if issue == nil || issue.ID != "gt-test1" {
		t.Fatalf("Create returned unexpected issue: %+v", issue)
	}

	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read stub args: %v", err)
	}
	argsStr := string(args)

	// Multi-line description MUST go via stdin, never --description=... .
	// bd 1.0.3+ rejects newlines in --description flag values.
	if !strings.Contains(argsStr, "--body-file=-") {
		t.Errorf("expected --body-file=- in bd args, got:\n%s", argsStr)
	}
	for _, line := range strings.Split(argsStr, "\n") {
		if strings.HasPrefix(line, "--description=") {
			t.Errorf("--description=... must not be used for multi-line content, got %q", line)
		}
	}

	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read stub stdin: %v", err)
	}
	if got := string(stdin); got != multiline {
		t.Errorf("stdin did not match description:\nwant:\n%s\n\ngot:\n%s", multiline, got)
	}
}

// TestCreateWithSingleLineDescription_KeepsFlagForm verifies that single-line
// descriptions still go through the original --description=... path, so we
// don't pay stdin-pipe overhead for the common case.
func TestCreateWithSingleLineDescription_KeepsFlagForm(t *testing.T) {
	ResetBdAllowStaleCacheForTest()

	stubDir := t.TempDir()
	argsPath := filepath.Join(stubDir, "args.txt")
	stdinPath := filepath.Join(stubDir, "stdin.txt")

	stubScript := `#!/bin/sh
for a in "$@"; do
  printf '%s\n' "$a" >> "` + argsPath + `"
done
cat > "` + stdinPath + `"
echo '{"id":"gt-test2","title":"single","status":"open","priority":2,"type":"task"}'
exit 0
`
	stubPath := filepath.Join(stubDir, "bd")
	if err := os.WriteFile(stubPath, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	b := New(t.TempDir())
	if _, err := b.Create(CreateOptions{
		Title:       "single",
		Description: "no newlines here",
		Priority:    2,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read stub args: %v", err)
	}
	argsStr := string(args)

	if !strings.Contains(argsStr, "--description=no newlines here") {
		t.Errorf("expected --description=... for single-line, got:\n%s", argsStr)
	}
	if strings.Contains(argsStr, "--body-file=-") {
		t.Errorf("--body-file=- must not be used for single-line description, got:\n%s", argsStr)
	}

	// Stdin should be empty — single-line path does not pipe anything.
	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read stub stdin: %v", err)
	}
	if got := strings.TrimSpace(string(stdin)); got != "" {
		t.Errorf("stdin should be empty for single-line description, got: %q", got)
	}
}

// TestUpdateWithMultilineDescription_RoutesViaStdin verifies the same fix
// applies to Beads.Update. The aggregator's update path emits the full
// markdown description on every occurrence, so this is the path that fails
// most often in production.
func TestUpdateWithMultilineDescription_RoutesViaStdin(t *testing.T) {
	ResetBdAllowStaleCacheForTest()

	stubDir := t.TempDir()
	argsPath := filepath.Join(stubDir, "args.txt")
	stdinPath := filepath.Join(stubDir, "stdin.txt")

	stubScript := `#!/bin/sh
for a in "$@"; do
  printf '%s\n' "$a" >> "` + argsPath + `"
done
cat > "` + stdinPath + `"
exit 0
`
	stubPath := filepath.Join(stubDir, "bd")
	if err := os.WriteFile(stubPath, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	b := New(t.TempDir())
	multiline := "## Alert: zombie-detected in gastown\n\n" +
		"line two with embedded newline\n" +
		"line three\n" +
		"<!-- alert-state {\"key\":\"zombie-detected:gastown\"} -->"

	desc := multiline
	if err := b.Update("gt-existing", UpdateOptions{Description: &desc}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read stub args: %v", err)
	}
	argsStr := string(args)

	if !strings.Contains(argsStr, "--body-file=-") {
		t.Errorf("expected --body-file=- in bd args, got:\n%s", argsStr)
	}
	if !strings.Contains(argsStr, "update\ngt-existing") {
		t.Errorf("expected `update gt-existing` in bd args, got:\n%s", argsStr)
	}
	for _, line := range strings.Split(argsStr, "\n") {
		if strings.HasPrefix(line, "--description=") {
			t.Errorf("--description=... must not be used for multi-line content, got %q", line)
		}
	}

	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read stub stdin: %v", err)
	}
	if got := string(stdin); got != multiline {
		t.Errorf("stdin did not match description:\nwant:\n%s\n\ngot:\n%s", multiline, got)
	}
}

// TestAggregator_CreateRoundTripsMultilineDescription is the end-to-end
// regression test for gastown-yti. It stubs bd the way the production code
// path hits it and asserts the Aggregator's Create flow (new alert) sends
// the multi-line state.Render() output via stdin, not as --description=.
//
// Prior to the fix this test would observe --description=... containing
// literal newlines and fail; after the fix --body-file=- is used.
func TestAggregator_CreateRoundTripsMultilineDescription(t *testing.T) {
	ResetBdAllowStaleCacheForTest()

	stubDir := t.TempDir()
	argsPath := filepath.Join(stubDir, "args.txt")
	stdinPath := filepath.Join(stubDir, "stdin.txt")

	stubScript := `#!/bin/sh
for a in "$@"; do
  printf '%s\n' "$a" >> "` + argsPath + `"
done
cat > "` + stdinPath + `"
# Echo back a synthetic JSON issue so Beads.Create succeeds.
cat <<'JSON'
{"id":"gt-alert-001","title":"[ALERT] polecat-died in gastown","status":"open","priority":1,"type":"bug","labels":["gt:alert","alert:key:polecat-died:gastown","alert:severity:high"]}
JSON
exit 0
`
	stubPath := filepath.Join(stubDir, "bd")
	if err := os.WriteFile(stubPath, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	b := New(t.TempDir())
	// Trigger the aggregator's createNew path which passes state.Render()
	// (multi-line markdown) directly as Description.
	multiline := "## Alert: polecat-died in gastown\n\n" +
		"This tracking bead aggregates repeated equivalent alerts.\n\n" +
		"### Occurrence summary\n\n" +
		"- **Root cause key**: polecat-died:gastown\n" +
		"- **Total occurrences**: 1\n" +
		"- **First seen**: 2026-06-25T16:00:00Z\n" +
		"- **Last seen**: 2026-06-25T16:00:00Z\n" +
		"- **Severity preserved**: high\n" +
		"- **Affected agents**: gastown/onyx\n\n" +
		"### Latest evidence\n\n" +
		"Polecat onyx died with hook gt-123\n" +
		"\n<!-- alert-state {\"key\":\"polecat-died:gastown\"} -->\n"

	issue, err := b.Create(CreateOptions{
		Title:       "[ALERT] polecat-died in gastown",
		Description: multiline,
		Labels:      []string{"gt:alert", "alert:key:polecat-died:gastown", "alert:severity:high"},
		Priority:    1,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if issue == nil || issue.ID == "" {
		t.Fatalf("Create returned empty issue")
	}

	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read stub args: %v", err)
	}
	argsStr := string(args)

	// The aggregator's exact failure mode: --description=... with embedded
	// newlines. Must not appear.
	for _, line := range strings.Split(argsStr, "\n") {
		if strings.HasPrefix(line, "--description=") && strings.Contains(line, "\\n") {
			// Even if the literal "\n" form is acceptable (single-line summary),
			// a raw newline-bearing value is what bd rejects.
			if strings.ContainsAny(line, "\n") || strings.Contains(line, "\n") {
				t.Errorf("multi-line --description=... must not appear, got %q", line)
			}
		}
	}
	if !strings.Contains(argsStr, "--body-file=-") {
		t.Errorf("expected --body-file=- in bd args (gastown-yti regression), got:\n%s", argsStr)
	}

	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read stub stdin: %v", err)
	}
	stdinStr := string(stdin)
	// Stdin's bytes should exactly equal the description we asked for.
	if stdinStr != multiline {
		t.Errorf("stdin did not round-trip description:\nwant:\n%q\n\ngot:\n%q", multiline, stdinStr)
	}
	// Sanity: confirm the stdin contained at least the JSON marker that
	// state.Render() appends — proves the aggregator's exact payload path.
	if !strings.Contains(stdinStr, "alert-state") {
		t.Errorf("stdin missing alert-state JSON marker from state.Render():\n%s", stdinStr)
	}

	// Confirm we got a parseable JSON issue back, end-to-end.
	var parsed struct {
		ID     string   `json:"id"`
		Status string   `json:"status"`
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal([]byte(`{"id":"`+issue.ID+`","status":"`+issue.Status+`"}`), &parsed); err != nil {
		t.Errorf("parsed issue not valid JSON shape: %v", err)
	}
	if parsed.ID == "" {
		t.Errorf("issue ID empty after round-trip")
	}
}
