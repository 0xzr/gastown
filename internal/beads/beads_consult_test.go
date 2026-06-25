package beads

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/consult"
)

func TestFormatConsultDescription_RoundTrip(t *testing.T) {
	req := &consult.Request{
		Question:        "Allow stacked-branch tip-only MRs?",
		TriggerClass:    consult.TriggerMergePolicy,
		RequestedModel:  consult.ModelOpus,
		ContextRefs:     []string{"gastown-cet.2", "hq-try2"},
		Options:         []string{"Allow", "Reject"},
		CurrentDecision: "Allow with squash",
		AskedBy:         "mayor",
		AskedAt:         "2026-06-25T00:00:00Z",
		RelatedBead:     "gastown-cet.6.4",
		Fingerprint:     "consult-fp:merge_policy:abc123",
	}
	fields := &ConsultFields{
		Request: req,
		State:   consult.StateOpen,
	}
	desc := FormatConsultDescription("Allow stacked-branch tip-only MRs?", fields)

	parsedReq, parsedFields := ParseConsultFields(desc)
	if parsedReq.Question != req.Question {
		t.Errorf("Question = %q, want %q", parsedReq.Question, req.Question)
	}
	if parsedReq.TriggerClass != req.TriggerClass {
		t.Errorf("TriggerClass = %q, want %q", parsedReq.TriggerClass, req.TriggerClass)
	}
	if parsedReq.RequestedModel != req.RequestedModel {
		t.Errorf("RequestedModel = %q, want %q", parsedReq.RequestedModel, req.RequestedModel)
	}
	if parsedReq.RelatedBead != req.RelatedBead {
		t.Errorf("RelatedBead = %q, want %q", parsedReq.RelatedBead, req.RelatedBead)
	}
	if parsedReq.Fingerprint != req.Fingerprint {
		t.Errorf("Fingerprint = %q, want %q", parsedReq.Fingerprint, req.Fingerprint)
	}
	if parsedReq.AskedBy != req.AskedBy {
		t.Errorf("AskedBy = %q, want %q", parsedReq.AskedBy, req.AskedBy)
	}
	if parsedReq.AskedAt != req.AskedAt {
		t.Errorf("AskedAt = %q, want %q", parsedReq.AskedAt, req.AskedAt)
	}
	if parsedReq.CurrentDecision != req.CurrentDecision {
		t.Errorf("CurrentDecision = %q, want %q", parsedReq.CurrentDecision, req.CurrentDecision)
	}
	if len(parsedReq.Options) != len(req.Options) {
		t.Fatalf("len(Options) = %d, want %d", len(parsedReq.Options), len(req.Options))
	}
	for i, opt := range req.Options {
		if parsedReq.Options[i] != opt {
			t.Errorf("Options[%d] = %q, want %q", i, parsedReq.Options[i], opt)
		}
	}
	if len(parsedReq.ContextRefs) != len(req.ContextRefs) {
		t.Fatalf("len(ContextRefs) = %d, want %d", len(parsedReq.ContextRefs), len(req.ContextRefs))
	}
	for i, ref := range req.ContextRefs {
		if parsedReq.ContextRefs[i] != ref {
			t.Errorf("ContextRefs[%d] = %q, want %q", i, parsedReq.ContextRefs[i], ref)
		}
	}
	if parsedFields.State != consult.StateOpen {
		t.Errorf("State = %q, want %q", parsedFields.State, consult.StateOpen)
	}
	if parsedFields.DecidedBy != "" {
		t.Errorf("DecidedBy = %q, want empty", parsedFields.DecidedBy)
	}
	if parsedFields.ClosedBy != "" {
		t.Errorf("ClosedBy = %q, want empty", parsedFields.ClosedBy)
	}
}

func TestFormatConsultDescription_NullFieldsRenderedExplicitly(t *testing.T) {
	req := &consult.Request{
		Question:       "Allow MR?",
		TriggerClass:   consult.TriggerMergePolicy,
		RequestedModel: consult.ModelOpus,
		AskedBy:        "mayor",
		AskedAt:        "2026-06-25T00:00:00Z",
	}
	desc := FormatConsultDescription("Allow MR?", &ConsultFields{Request: req})
	for _, want := range []string{
		"current_decision: null",
		"context_refs: null",
		"options: null",
		"related_bead: null",
		"fingerprint: null",
		"bead_id: null",
		"answered_by: null",
		"decision: null",
		"rationale: null",
		"confidence: null",
		"closed_by: null",
		"closed_reason: null",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("description missing %q", want)
		}
	}
}

func TestParseConsultFields_ClosedStateRoundTrip(t *testing.T) {
	req := &consult.Request{
		Question:       "Allow MR?",
		TriggerClass:   consult.TriggerMergePolicy,
		RequestedModel: consult.ModelOpus,
		AskedBy:        "mayor",
		AskedAt:        "2026-06-25T00:00:00Z",
	}
	fields := &ConsultFields{
		Request:      req,
		State:        consult.StateClosed,
		DecidedBy:    "opus",
		DecidedAt:    "2026-06-25T01:00:00Z",
		Decision:     "Allow with squash",
		Rationale:    "Stacked tip-only MRs break publisher state.",
		Confidence:   "high",
		ClosedBy:     "mayor",
		ClosedReason: "acted on consultation",
	}
	desc := FormatConsultDescription("Allow MR?", fields)
	parsedReq, parsedFields := ParseConsultFields(desc)
	if parsedReq.Question != req.Question {
		t.Errorf("Question = %q, want %q", parsedReq.Question, req.Question)
	}
	if parsedFields.State != consult.StateClosed {
		t.Errorf("State = %q, want %q", parsedFields.State, consult.StateClosed)
	}
	if parsedFields.DecidedBy != "opus" {
		t.Errorf("DecidedBy = %q, want opus", parsedFields.DecidedBy)
	}
	if parsedFields.Decision != "Allow with squash" {
		t.Errorf("Decision = %q, want Allow with squash", parsedFields.Decision)
	}
	if parsedFields.Rationale != "Stacked tip-only MRs break publisher state." {
		t.Errorf("Rationale = %q, want rationale", parsedFields.Rationale)
	}
	if parsedFields.Confidence != "high" {
		t.Errorf("Confidence = %q, want high", parsedFields.Confidence)
	}
	if parsedFields.ClosedBy != "mayor" {
		t.Errorf("ClosedBy = %q, want mayor", parsedFields.ClosedBy)
	}
	if parsedFields.ClosedReason != "acted on consultation" {
		t.Errorf("ClosedReason = %q, want acted on consultation", parsedFields.ClosedReason)
	}
}

func TestCreateConsultBead_NilFieldsRejected(t *testing.T) {
	b := New("")
	if _, err := b.CreateConsultBead("title", nil); err == nil {
		t.Fatalf("expected error for nil fields")
	}
}

func TestCreateConsultBead_InvalidRequestRejected(t *testing.T) {
	b := New("")
	fields := &ConsultFields{
		Request: &consult.Request{
			Question:       "", // empty
			TriggerClass:   consult.TriggerMergePolicy,
			RequestedModel: consult.ModelOpus,
			AskedBy:        "mayor",
		},
	}
	if _, err := b.CreateConsultBead("title", fields); err == nil {
		t.Fatalf("expected validation error for empty question")
	}
}

func TestSplitCSV(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", []string{}},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{"a, b , c", []string{"a", "b", "c"}},
		{",,", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := splitCSV(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("len(got) = %d, want %d (%v)", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestNullify(t *testing.T) {
	if nullify("") != "null" {
		t.Errorf(`nullify("") = %q, want "null"`, nullify(""))
	}
	if nullify("x") != "x" {
		t.Errorf(`nullify("x") = %q, want "x"`, nullify("x"))
	}
}

// stubBdForConsult writes each bd arg on its own line to argsPath, captures
// stdin to stdinPath, and emits a minimal valid issue JSON so unmarshal
// succeeds. It also honors a "show" subcommand by echoing a canned consult
// bead description so the state-guard tests can drive RecordConsultAnswer /
// CloseConsultBead without a real Dolt.
func stubBdForConsult(t *testing.T, argsPath, stdinPath string, showDescription string) {
	t.Helper()
	stubScript := `#!/bin/sh
for a in "$@"; do
  printf '%s\n' "$a" >> "` + argsPath + `"
done
cat > "` + stdinPath + `"
is_show=0
for a in "$@"; do
  if [ "$a" = "show" ]; then is_show=1; fi
done
if [ "$is_show" = "1" ]; then
  printf '%s\n' '[{"id":"gt-stub","title":"q","status":"open","priority":2,"type":"task","labels":["gt:consult"],"description":` + jsonQuote(showDescription) + `}]'
else
  echo '{"id":"gt-stub","title":"q","status":"open","priority":2,"type":"task","labels":["gt:consult"]}'
fi
exit 0
`
	stubDir := t.TempDir()
	stubPath := filepath.Join(stubDir, "bd")
	if err := os.WriteFile(stubPath, []byte(stubScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ResetBdAllowStaleCacheForTest()
}

// jsonQuote renders a Go string as a JSON string literal body (with surrounding
// double quotes) so it can be embedded in the stub script's echo'd JSON.
func jsonQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// TestCreateConsultBead_NotEphemeralNoWispType is the regression test for
// gastown-cet.6.4 CRITICAL #1 and MAJOR #5: CreateConsultBead previously
// passed `--wisp-type=consult` (rejected by `bd create` because "consult" is
// not a valid wisp type, so the packet was never filed) and `--ephemeral`
// (which routes the bead to the wisps table, where the reaper silently closes
// open wisps past 24h — deleting an unanswered consult before the model
// responds). Both flags must be absent so the consult lands as a durable task.
func TestCreateConsultBead_NotEphemeralNoWispType(t *testing.T) {
	argsPath := filepath.Join(t.TempDir(), "args.txt")
	stdinPath := filepath.Join(t.TempDir(), "stdin.txt")
	stubBdForConsult(t, argsPath, stdinPath, "")

	b := New(t.TempDir())
	fields := &ConsultFields{
		Request: &consult.Request{
			Question:       "Allow MR?",
			TriggerClass:   consult.TriggerMergePolicy,
			RequestedModel: consult.ModelOpus,
			AskedBy:        "mayor",
			AskedAt:        "2026-06-25T00:00:00Z",
		},
		State: consult.StateOpen,
	}
	if _, err := b.CreateConsultBead("Allow MR?", fields); err != nil {
		t.Fatalf("CreateConsultBead: %v", err)
	}

	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read stub args: %v", err)
	}
	args := string(argsData)

	if strings.Contains(args, "--ephemeral") {
		t.Errorf("consult bead must NOT be ephemeral (reaper would close unanswered consults), got:\n%s", args)
	}
	if strings.Contains(args, "--wisp-type") {
		t.Errorf("consult bead must NOT pass --wisp-type (consult is not a valid wisp type and bd create rejects it), got:\n%s", args)
	}
	if !strings.Contains(args, "--type=task") {
		t.Errorf("consult bead must be a durable task, expected --type=task, got:\n%s", args)
	}
	if !strings.Contains(args, "--labels=gt:consult") {
		t.Errorf("expected --labels=gt:consult, got:\n%s", args)
	}
}

// TestAppendNotesToBead_UsesAppendNotesFlag is the regression test for
// gastown-cet.6.4 CRITICAL #2: MirrorConsultResultOnSource previously mirrored
// a consult decision onto the related source bead via `bd update --notes=`,
// which REPLACES the notes field and erases the source bead's existing audit
// trail (data loss). It must use `--append-notes=` (concatenate with newline).
func TestAppendNotesToBead_UsesAppendNotesFlag(t *testing.T) {
	argsPath := filepath.Join(t.TempDir(), "args.txt")
	stdinPath := filepath.Join(t.TempDir(), "stdin.txt")
	stubBdForConsult(t, argsPath, stdinPath, "")

	b := New(t.TempDir())
	if err := b.appendNotesToBead("gt-src1", "consult_decision: decided=allow"); err != nil {
		t.Fatalf("appendNotesToBead: %v", err)
	}

	argsData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read stub args: %v", err)
	}
	args := string(argsData)

	if !strings.Contains(args, "--append-notes=") {
		t.Errorf("must use --append-notes= (concatenates), got:\n%s", args)
	}
	for _, line := range strings.Split(args, "\n") {
		if strings.HasPrefix(line, "--notes=") {
			t.Errorf("must NOT use --notes= (replaces notes field, data loss), got %q", line)
		}
	}
	if !strings.Contains(args, "consult_decision:") {
		t.Errorf("note content missing from args, got:\n%s", args)
	}
}

// TestAppendNotesToBead_EmptyInputsNoop verifies the guard clauses skip empty
// bead IDs and empty note lines (no bd invocation at all).
func TestAppendNotesToBead_EmptyInputsNoop(t *testing.T) {
	argsPath := filepath.Join(t.TempDir(), "args.txt")
	stdinPath := filepath.Join(t.TempDir(), "stdin.txt")
	stubBdForConsult(t, argsPath, stdinPath, "")

	b := New(t.TempDir())
	for _, tc := range []struct{ bead, note string }{
		{"", "note"},
		{"   ", "note"},
		{"gt-src1", ""},
		{"gt-src1", "   "},
	} {
		if err := b.appendNotesToBead(tc.bead, tc.note); err != nil {
			t.Errorf("appendNotesToBead(%q,%q): unexpected error %v", tc.bead, tc.note, err)
		}
	}
	if _, err := os.Stat(argsPath); err == nil {
		data, _ := os.ReadFile(argsPath)
		t.Errorf("expected no bd invocation for empty inputs, got args:\n%s", string(data))
	}
}

// renderConsultDesc builds a consult bead description in the parsed format
// for state-guard tests, parameterized by state.
func renderConsultDesc(state consult.DecisionState) string {
	fields := &ConsultFields{
		Request: &consult.Request{
			Question:       "Allow MR?",
			TriggerClass:   consult.TriggerMergePolicy,
			RequestedModel: consult.ModelOpus,
			AskedBy:        "mayor",
			AskedAt:        "2026-06-25T00:00:00Z",
		},
		State: state,
	}
	return FormatConsultDescription("Allow MR?", fields)
}

// TestRecordConsultAnswer_RejectsClosed is the regression test for
// gastown-cet.6.4 MAJOR #3: RecordConsultAnswer had no state guard, so a
// closed consult could be re-answered (state flipped closed -> answered),
// resurrecting a finalized decision. A closed consult must reject the answer.
func TestRecordConsultAnswer_RejectsClosed(t *testing.T) {
	argsPath := filepath.Join(t.TempDir(), "args.txt")
	stdinPath := filepath.Join(t.TempDir(), "stdin.txt")
	stubBdForConsult(t, argsPath, stdinPath, renderConsultDesc(consult.StateClosed))

	b := New(t.TempDir())
	resp := &consult.Response{
		DecidedBy:  "opus",
		DecidedAt:  "2026-06-25T01:00:00Z",
		Decision:   "Reject",
		Rationale:  "state guard test",
		Confidence: "high",
	}
	err := b.RecordConsultAnswer("gt-stub", resp)
	if err == nil {
		t.Fatalf("expected error recording answer on a closed consult, got nil")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Errorf("error should mention closed state, got %v", err)
	}
	// Must not have reached the update path.
	argsData, _ := os.ReadFile(argsPath)
	if strings.Contains(string(argsData), "update") {
		t.Errorf("closed consult must not be updated, got bd args:\n%s", string(argsData))
	}
}

// TestRecordConsultAnswer_AllowsOpen verifies a non-closed consult can be
// answered (the guard permits open -> answered).
func TestRecordConsultAnswer_AllowsOpen(t *testing.T) {
	argsPath := filepath.Join(t.TempDir(), "args.txt")
	stdinPath := filepath.Join(t.TempDir(), "stdin.txt")
	stubBdForConsult(t, argsPath, stdinPath, renderConsultDesc(consult.StateOpen))

	b := New(t.TempDir())
	resp := &consult.Response{
		DecidedBy:  "opus",
		DecidedAt:  "2026-06-25T01:00:00Z",
		Decision:   "Allow",
		Rationale:  "ok",
		Confidence: "high",
	}
	if err := b.RecordConsultAnswer("gt-stub", resp); err != nil {
		t.Fatalf("RecordConsultAnswer on open consult: %v", err)
	}
}

// TestCloseConsultBead_RejectsAlreadyClosed is the regression test for
// gastown-cet.6.4 MAJOR #3: CloseConsultBead had no state guard, so a closed
// consult could be re-closed (overwriting the original closer/reason and
// appending a second close record to the source bead). Re-closing must error.
func TestCloseConsultBead_RejectsAlreadyClosed(t *testing.T) {
	argsPath := filepath.Join(t.TempDir(), "args.txt")
	stdinPath := filepath.Join(t.TempDir(), "stdin.txt")
	stubBdForConsult(t, argsPath, stdinPath, renderConsultDesc(consult.StateClosed))

	b := New(t.TempDir())
	err := b.CloseConsultBead("gt-stub", "mayor", "acted", nil)
	if err == nil {
		t.Fatalf("expected error closing an already-closed consult, got nil")
	}
	if !strings.Contains(err.Error(), "already closed") {
		t.Errorf("error should mention already closed, got %v", err)
	}
	argsData, _ := os.ReadFile(argsPath)
	if strings.Contains(string(argsData), "close") {
		t.Errorf("already-closed consult must not be closed again, got bd args:\n%s", string(argsData))
	}
}

// TestCloseConsultBead_AllowsOpen verifies a non-closed consult can be closed.
func TestCloseConsultBead_AllowsOpen(t *testing.T) {
	argsPath := filepath.Join(t.TempDir(), "args.txt")
	stdinPath := filepath.Join(t.TempDir(), "stdin.txt")
	stubBdForConsult(t, argsPath, stdinPath, renderConsultDesc(consult.StateOpen))

	b := New(t.TempDir())
	if err := b.CloseConsultBead("gt-stub", "mayor", "acted", nil); err != nil {
		t.Fatalf("CloseConsultBead on open consult: %v", err)
	}
}

// statefulStubBd installs a bd stub on PATH that emulates just enough of the
// real bd for the close-race regression tests:
//
//   - "show <id> --json": returns the consult bead whose description is the
//     current contents of descPath (initial: initialDesc), so successive
//     CloseConsultBead calls observe the description as the prior call wrote it.
//   - "update <id> --body-file=- ...": reads the new description from stdin and
//     persists it to descPath (the state the next show reflects).
//   - "close <id> ...": fails (exit 1, stderr "simulated close failure") on the
//     first close attempt and succeeds (exit 0) on every subsequent one,
//     emulating a transient bd close failure (network blip / lock / Dolt retry)
//     followed by recovery.
//
// Other invocations (e.g. the --allow-stale version probe) exit 0 with minimal
// output so capability detection and JSON parsing succeed.
func statefulStubBd(t *testing.T, descPath, initialDesc string) {
	t.Helper()
	installStatefulStubBd(t, descPath, initialDesc, stubCloseFailsFirst)
}

// failingUpdateStubBd is like statefulStubBd but the close always succeeds;
// instead the "update" subcommand fails on the first call and succeeds
// thereafter. Used to verify the close-first ordering survives a failed
// description Update after a successful close (bd close is idempotent, so the
// retry re-closes as a no-op and the second Update advances the state).
func failingUpdateStubBd(t *testing.T, descPath, initialDesc string) {
	t.Helper()
	installStatefulStubBd(t, descPath, initialDesc, stubUpdateFailsFirst)
}

type stubFailMode int

const (
	stubCloseFailsFirst stubFailMode = iota
	stubUpdateFailsFirst
)

// installStatefulStubBd writes the stub script. The failMode selects which
// subcommand fails once then succeeds thereafter.
func installStatefulStubBd(t *testing.T, descPath, initialDesc string, mode stubFailMode) {
	t.Helper()
	if err := os.WriteFile(descPath, []byte(initialDesc), 0644); err != nil {
		t.Fatalf("write initial stub desc: %v", err)
	}
	closeFail := "0"
	updateFail := "0"
	if mode == stubCloseFailsFirst {
		closeFail = "1"
	} else {
		updateFail = "1"
	}
	stubScript := `#!/bin/sh
# Drain stdin (used by --body-file=-) to a temp file so update can persist it.
stdin_tmp="$(mktemp)"
cat > "$stdin_tmp"

# First non-flag arg is the subcommand (skip --allow-stale injected by harness).
sub=""
for a in "$@"; do
  case "$a" in
    --*) continue ;;
    *) sub="$a"; break ;;
  esac
done

case "$sub" in
  version)
    echo "bd stub 1.0"
    exit 0
    ;;
  show)
    # jq -Rs . reads the description file and emits a JSON string literal
    # (with surrounding quotes), correctly escaping every special character.
    quoted="$(jq -Rs . < ` + descPath + `)"
    printf '%s\n' '[{"id":"gt-stub","title":"q","status":"open","priority":2,"type":"task","labels":["gt:consult"],"description":'"$quoted"'}]'
    exit 0
    ;;
  update)
    count="` + descPath + `.updcount"
    n=0
    if [ -f "$count" ]; then n="$(cat "$count")"; fi
    n=$((n + 1))
    echo "$n" > "$count"
    if [ "` + updateFail + `" = "1" ] && [ "$n" -eq 1 ]; then
      echo "simulated update failure" 1>&2
      exit 1
    fi
    cp "$stdin_tmp" ` + descPath + `
    echo '{"id":"gt-stub","title":"q","status":"open","priority":2,"type":"task","labels":["gt:consult"]}'
    exit 0
    ;;
  close)
    count="` + descPath + `.closecount"
    n=0
    if [ -f "$count" ]; then n="$(cat "$count")"; fi
    n=$((n + 1))
    echo "$n" > "$count"
    if [ "` + closeFail + `" = "1" ] && [ "$n" -eq 1 ]; then
      echo "simulated close failure" 1>&2
      exit 1
    fi
    echo "closed"
    exit 0
    ;;
  *)
    echo '{"id":"gt-stub","title":"q","status":"open","priority":2,"type":"task","labels":["gt:consult"]}'
    exit 0
    ;;
esac
`
	stubDir := t.TempDir()
	stubPath := filepath.Join(stubDir, "bd")
	if err := os.WriteFile(stubPath, []byte(stubScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ResetBdAllowStaleCacheForTest()
}

// TestCloseConsultBead_CloseFailThenRetrySucceeds is the regression test for
// gastown-f6w (shard-E finding, commit 6faf2e93): CloseConsultBead previously
// persisted state=closed into the description BEFORE running bd close. When bd
// close failed, the description already recorded state=closed, so a retry hit
// the "already closed" state guard and was rejected — leaving the consult
// permanently open.
//
// With the close-first-then-update ordering, a failed close does not advance
// the description state, so a retry reaches the close path again and succeeds.
func TestCloseConsultBead_CloseFailThenRetrySucceeds(t *testing.T) {
	descPath := filepath.Join(t.TempDir(), "desc.txt")
	statefulStubBd(t, descPath, renderConsultDesc(consult.StateOpen))

	b := New(t.TempDir())

	// First close attempt: bd close fails. CloseConsultBead must surface the
	// error AND must NOT have advanced the description to state=closed.
	firstErr := b.CloseConsultBead("gt-stub", "mayor", "acted", nil)
	if firstErr == nil {
		t.Fatalf("first CloseConsultBead: expected error from simulated close failure, got nil")
	}
	if !strings.Contains(firstErr.Error(), "close") && !strings.Contains(firstErr.Error(), "simulated") {
		t.Errorf("first CloseConsultBead error should reflect close failure, got %v", firstErr)
	}

	// The description must still be open: the failed close must not have written
	// state=closed (the race that left consults permanently stuck).
	descAfterFirstFail, err := os.ReadFile(descPath)
	if err != nil {
		t.Fatalf("read desc after first failure: %v", err)
	}
	if !strings.Contains(string(descAfterFirstFail), "state: open") {
		t.Fatalf("after a failed close the description must still be open, got:\n%s", string(descAfterFirstFail))
	}

	// Second close attempt (the retry): bd close now succeeds and the
	// description is advanced to state=closed. This is the path that was
	// previously rejected by the "already closed" guard.
	if err := b.CloseConsultBead("gt-stub", "mayor", "acted", nil); err != nil {
		t.Fatalf("retry CloseConsultBead after a prior close failure: expected success, got %v", err)
	}

	// Verify the description was advanced to closed by the successful retry.
	descAfterRetry, err := os.ReadFile(descPath)
	if err != nil {
		t.Fatalf("read desc after retry: %v", err)
	}
	if !strings.Contains(string(descAfterRetry), "state: closed") {
		t.Errorf("after a successful retry the description must record state=closed, got:\n%s", string(descAfterRetry))
	}
}

// TestCloseConsultBead_UpdateFailsAfterCloseSuccessKeepsRetryable verifies the
// other half of the close-first ordering: when bd close succeeds but the
// subsequent description Update fails, a retry must still succeed (bd close is
// idempotent, so re-closing is a no-op and the second Update advances the
// state). This guards against regressing the ordering back to update-first,
// where a failed Update after a successful close would leave the bead closed in
// bd's status but missing the state=closed metadata — and a naive re-add of a
// state guard would then reject the retry.
func TestCloseConsultBead_UpdateFailsAfterCloseSuccessKeepsRetryable(t *testing.T) {
	descPath := filepath.Join(t.TempDir(), "desc.txt")
	// A stub whose update fails on the first call and succeeds thereafter; the
	// close always succeeds (so the only first-attempt failure is the Update).
	failingUpdateStubBd(t, descPath, renderConsultDesc(consult.StateOpen))

	b := New(t.TempDir())

	// First attempt: close succeeds, Update fails.
	firstErr := b.CloseConsultBead("gt-stub", "mayor", "acted", nil)
	if firstErr == nil {
		t.Fatalf("first CloseConsultBead: expected Update failure, got nil")
	}

	// The description must NOT have advanced to closed: the failed Update left
	// the prior (open) description in place.
	descAfterFirst, err := os.ReadFile(descPath)
	if err != nil {
		t.Fatalf("read desc after first failure: %v", err)
	}
	if !strings.Contains(string(descAfterFirst), "state: open") {
		t.Fatalf("after a failed Update the description must still be open, got:\n%s", string(descAfterFirst))
	}

	// Retry must succeed even though the prior close already landed (bd close is
	// idempotent, so the re-close is a no-op and the second Update advances the
	// state).
	if err := b.CloseConsultBead("gt-stub", "mayor", "acted", nil); err != nil {
		t.Fatalf("retry CloseConsultBead after prior Update failure: expected success, got %v", err)
	}

	descAfterRetry, err := os.ReadFile(descPath)
	if err != nil {
		t.Fatalf("read desc after retry: %v", err)
	}
	if !strings.Contains(string(descAfterRetry), "state: closed") {
		t.Errorf("after a successful retry the description must record state=closed, got:\n%s", string(descAfterRetry))
	}
}
