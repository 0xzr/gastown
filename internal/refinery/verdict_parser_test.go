package refinery

import (
	"encoding/json"
	"strings"
	"testing"
)

// fencedJSONPassPayload is the fenced JSON the model emitted for the
// gastown-9lc case. Embedded as a constant so the test fixture is
// self-contained; helpers below JSON-encode it when embedding inside
// a string field so the surrounding JSONL is valid (newlines and quotes
// are escaped per RFC 8259).
const fencedJSONPassPayload = "```json\n{\n  \"verdict\": \"PASS\",\n  \"findings\": [],\n  \"summary\": \"Looks good.\"\n}\n```"

// jsonlTextEvent builds a JSONL text-event line with the given body as
// .part.text. The body is JSON-escaped so embedded newlines and quotes
// produce valid JSON.
func jsonlTextEvent(body string) string {
	line := map[string]any{
		"type": "text",
		"part": map[string]any{
			"type": "text",
			"text": body,
		},
	}
	b, _ := json.Marshal(line)
	return string(b)
}

// jsonlReviewResultWrapper builds a JSONL review_result line with the
// adapter's synthetic no-parse wrapper. rawOutput is placed inside
// findings[0].raw_output (the OpenCode-family shape); if empty, the
// findings array is empty.
func jsonlReviewResultWrapper(rawOutput string) string {
	review := map[string]any{
		"verdict": "FAIL",
		"summary": "OpenCode review output was normalized to FAIL because it did not contain parseable {verdict, findings, summary} JSON.",
	}
	if rawOutput != "" {
		review["findings"] = []map[string]any{
			{
				"classification": "BLOCKING",
				"citation":       nil,
				"explanation":    "OpenCode did not return parseable review JSON.",
				"raw_output":     rawOutput,
			},
		}
		review["adapter_error"] = "no_parseable_review_json"
	} else {
		review["findings"] = []map[string]any{
			{"classification": "BLOCKING", "explanation": "no raw output"},
		}
	}
	line := map[string]any{
		"type": "review_result",
		"part": map[string]any{
			"type":   "review-result",
			"review": review,
		},
	}
	b, _ := json.Marshal(line)
	return string(b)
}

// rawOpenCodeReviewLog is the verbatim (truncated for size, but structurally
// identical) shape produced by /home/ubuntu/polybot-v3/.metaswarm/adapters/
// opencode.sh for gastown-wisp-7kd's gate on 2026-06-26:
//
//  1. text        — the model emits the review wrapped in a ```json ... ```
//     fence, with top-level verdict PASS.
//  2. review_result — the wrapper's synthetic no-parse envelope: verdict=FAIL,
//     adapter_error=no_parseable_review_json, summary and the
//     "OpenCode did not return parseable review JSON."
//     explanation. The model's REAL text is preserved under
//     `.part.review.findings[0].raw_output` — THIS is where
//     the recoverable verdict lives.
//
// Pre-fix behavior (gastown-9lc): the live gate only queried the top-level
// `.part.review.raw_output`, which the adapter does not write, and so
// recorded m3: UNAVAILABLE / no verdict for a model that said PASS.
//
// Post-fix behavior: this test asserts both carrier paths extract PASS, and
// that ExtractVerdictFromJSONL returns PASS for the whole log.
func rawOpenCodeReviewLogFixture() string {
	return jsonlTextEvent(fencedJSONPassPayload) + "\n" +
		jsonlReviewResultWrapper(fencedJSONPassPayload) + "\n"
}

// TestExtractVerdictFromText_PlainToken covers the simplest case: a bare
// PASS / FAIL token (whitespace and a single quote stripped).
func TestExtractVerdictFromText_PlainToken(t *testing.T) {
	cases := []struct {
		in   string
		want ReviewerVerdict
	}{
		{"PASS", ReviewerVerdictPass},
		{"pass", ReviewerVerdictPass},
		{"  PASS  ", ReviewerVerdictPass},
		{`"FAIL"`, ReviewerVerdictFail},
		{"`FAIL`", ReviewerVerdictFail},
		{"fail", ReviewerVerdictFail},
	}
	for _, c := range cases {
		got := ExtractVerdictFromText(c.in)
		if got.Verdict != c.want {
			t.Errorf("ExtractVerdictFromText(%q) verdict = %q, want %q", c.in, got.Verdict, c.want)
		}
		if got.Source != VerdictSourcePlainToken {
			t.Errorf("ExtractVerdictFromText(%q) source = %q, want %q", c.in, got.Source, VerdictSourcePlainToken)
		}
	}
}

// TestExtractVerdictFromText_JSONObject covers whole-text JSON.
func TestExtractVerdictFromText_JSONObject(t *testing.T) {
	in := `{"verdict":"PASS","findings":[],"summary":"ok"}`
	got := ExtractVerdictFromText(in)
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("verdict = %q, want PASS", got.Verdict)
	}
	if got.Source != VerdictSourceJSONObject {
		t.Errorf("source = %q, want %q", got.Source, VerdictSourceJSONObject)
	}

	failIn := `{"verdict":"FAIL","blockers":["race"]}`
	got = ExtractVerdictFromText(failIn)
	if got.Verdict != ReviewerVerdictFail {
		t.Errorf("verdict = %q, want FAIL", got.Verdict)
	}
}

// TestExtractVerdictFromText_FencedJSON covers ```json ... ``` blocks.
// This is the gastown-9lc primary recovery path.
func TestExtractVerdictFromText_FencedJSON(t *testing.T) {
	in := "Here is the review:\n" + fencedJSONPassPayload + "\nDone."
	got := ExtractVerdictFromText(in)
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("verdict = %q, want PASS", got.Verdict)
	}
	if got.Source != VerdictSourceFencedCodeBlock {
		t.Errorf("source = %q, want %q", got.Source, VerdictSourceFencedCodeBlock)
	}

	// ``` without language tag also matches the live python regex.
	in2 := "```\n{\"verdict\":\"FAIL\"}\n```"
	got = ExtractVerdictFromText(in2)
	if got.Verdict != ReviewerVerdictFail {
		t.Errorf("verdict = %q, want FAIL", got.Verdict)
	}

	// Last fenced verdict wins (mirrors live python).
	in3 := "```json\n{\"verdict\":\"FAIL\"}\n```\n\n```json\n{\"verdict\":\"PASS\"}\n```"
	got = ExtractVerdictFromText(in3)
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("verdict = %q, want PASS (last fenced wins)", got.Verdict)
	}
}

// TestExtractVerdictFromText_BraceMatched covers streams with embedded JSON
// objects (no fences). The last balanced object wins.
func TestExtractVerdictFromText_BraceMatched(t *testing.T) {
	in := `noise {"verdict":"FAIL","x":1} more noise {"verdict":"PASS"}`
	got := ExtractVerdictFromText(in)
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("verdict = %q, want PASS (last brace wins)", got.Verdict)
	}
	if got.Source != VerdictSourceBraceMatchedJSON {
		t.Errorf("source = %q, want %q", got.Source, VerdictSourceBraceMatchedJSON)
	}
}

// TestExtractVerdictFromText_Regex covers the last-resort fallback for
// hand-written verdicts that won't parse as JSON (e.g. unescaped quotes).
func TestExtractVerdictFromText_Regex(t *testing.T) {
	in := `the model said "verdict":"PASS" but the JSON was malformed: {`
	got := ExtractVerdictFromText(in)
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("verdict = %q, want PASS", got.Verdict)
	}
	if got.Source != VerdictSourceVerdictRegex {
		t.Errorf("source = %q, want %q", got.Source, VerdictSourceVerdictRegex)
	}
}

// TestExtractVerdictFromText_NoVerdict covers inputs that must NOT yield a
// verdict (preserves fail-closed).
func TestExtractVerdictFromText_NoVerdict(t *testing.T) {
	cases := []string{
		"",
		"   ",
		"no verdict here",
		`{"foo":"bar"}`,       // verdict field missing
		`{"verdict":"MAYBE"}`, // not PASS / FAIL
		`{"verdict":42}`,      // wrong type
	}
	for _, in := range cases {
		got := ExtractVerdictFromText(in)
		if got.IsSet() {
			t.Errorf("ExtractVerdictFromText(%q) verdict = %q, want empty", in, got.Verdict)
		}
	}
}

// TestExtractVerdictFromJSONL_FullLogShape_Regression is the primary
// gastown-9lc regression test. It feeds the verbatim raw log shape and
// asserts ExtractVerdictFromJSONL returns PASS (with the expected source).
//
// Before the fix, this returned "" (no verdict) because the live gate only
// queried .part.review.raw_output — a field the adapter does not write.
// After the fix, the parser also inspects .part.review.findings[].raw_output
// and recovers the model's PASS verdict.
func TestExtractVerdictFromJSONL_FullLogShape_Regression(t *testing.T) {
	got, err := ExtractVerdictFromJSONL(strings.NewReader(rawOpenCodeReviewLogFixture()))
	if err != nil {
		t.Fatalf("ExtractVerdictFromJSONL: %v", err)
	}
	if !got.IsSet() {
		t.Fatalf("got empty verdict; want PASS — the gastown-9lc regression")
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Fatalf("verdict = %q, want PASS", got.Verdict)
	}
	// Highest-priority carrier that yields a verdict wins. The text event
	// (part.text) appears before the review_result event, so it must win.
	if got.Source != VerdictSourceLogPartText {
		t.Errorf("source = %q, want %q (text event fires first)", got.Source, VerdictSourceLogPartText)
	}
}

// TestExtractVerdictFromJSONL_StrippedTextEvent_FindingsRawOutput covers the
// case where the text event is absent and the ONLY place the model's PASS
// survives is `.part.review.findings[].raw_output`. This is the precise
// gastown-9lc scenario once the live gate's text-event jq path fails (e.g.,
// when a different wrapper strips the text event before persisting the log).
func TestExtractVerdictFromJSONL_StrippedTextEvent_FindingsRawOutput(t *testing.T) {
	got, err := ExtractVerdictFromJSONL(strings.NewReader(jsonlReviewResultWrapper(fencedJSONPassPayload) + "\n"))
	if err != nil {
		t.Fatalf("ExtractVerdictFromJSONL: %v", err)
	}
	if !got.IsSet() {
		t.Fatalf("got empty verdict; want PASS recovered from findings[].raw_output — the gastown-9lc fix")
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("verdict = %q, want PASS", got.Verdict)
	}
	if got.Source != VerdictSourceLogPartReviewFindingsRawOutput {
		t.Errorf("source = %q, want %q", got.Source, VerdictSourceLogPartReviewFindingsRawOutput)
	}
}

// TestExtractVerdictFromJSONL_NoTopLevelRawOutput documents the bug: the
// adapter does NOT write `.part.review.raw_output` at the top level (it
// writes it inside findings[]). A naïve jq query on the top-level path
// returns empty — exactly the gastown-9lc failure mode.
func TestExtractVerdictFromJSONL_NoTopLevelRawOutput(t *testing.T) {
	log := jsonlReviewResultWrapper(fencedJSONPassPayload) + "\n"
	entries := parseLogLines(t, log)
	if len(entries) == 0 {
		t.Fatal("no entries parsed")
	}
	if got := extractFromPartReviewRawOutput(entries[0]); got.IsSet() {
		t.Errorf("top-level .part.review.raw_output returned %q; expected empty (the adapter writes findings[].raw_output, not top-level)", got.Verdict)
	}
	// But the findings[] path MUST recover PASS — this is the fix.
	if got := extractFromPartReviewFindingsRawOutput(entries[0]); !got.IsSet() || got.Verdict != ReviewerVerdictPass {
		t.Errorf("findings[].raw_output did not recover PASS; got %+v", got)
	}
}

// TestExtractVerdictFromJSONL_TextEventWinsOverWrapper verifies the priority
// order: a real PASS in part.text is preferred over a synthetic FAIL in
// review_result.findings[].raw_output, even when both are present.
//
// In the bug-report log both carry the same PASS, but the priority rule
// matters when they disagree (e.g. wrapper says FAIL but the model said PASS
// inside the text event). The live gate preserves this ordering; so does
// the Go mirror.
func TestExtractVerdictFromJSONL_TextEventWinsOverWrapper(t *testing.T) {
	textWithPASS := "```json\n{\n  \"verdict\": \"PASS\"\n}\n```"
	log := jsonlTextEvent(textWithPASS) + "\n" + jsonlReviewResultWrapper(fencedJSONPassPayload) + "\n"
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("ExtractVerdictFromJSONL: %v", err)
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("verdict = %q, want PASS (text event must win)", got.Verdict)
	}
	if got.Source != VerdictSourceLogPartText {
		t.Errorf("source = %q, want %q", got.Source, VerdictSourceLogPartText)
	}
}

// TestExtractVerdictFromJSONL_ParsedFailInFindingsStillRejects verifies that
// the gastown-9lc fix does NOT relax the FAIL→REJECT semantic. If the
// findings[].raw_output carries an explicit FAIL, the recovered verdict is
// FAIL and the gate must reject (upstream EvaluateCoreReviewerQuorum honors
// this). This is the fail-closed invariant the bead calls out.
func TestExtractVerdictFromJSONL_ParsedFailInFindingsStillRejects(t *testing.T) {
	fencedFAIL := "```json\n{\n  \"verdict\": \"FAIL\",\n  \"blockers\": [\"race\"]\n}\n```"
	log := jsonlReviewResultWrapper(fencedFAIL) + "\n"
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("ExtractVerdictFromJSONL: %v", err)
	}
	if got.Verdict != ReviewerVerdictFail {
		t.Fatalf("verdict = %q, want FAIL (findings[] FAIL must propagate)", got.Verdict)
	}
	if got.Source != VerdictSourceLogPartReviewFindingsRawOutput {
		t.Errorf("source = %q, want %q", got.Source, VerdictSourceLogPartReviewFindingsRawOutput)
	}
}

// TestExtractVerdictFromJSONL_NoVerdictAnywhere covers the truly empty case.
// Unparseable / missing verdict stays empty (UNAVAILABLE / NO_VERDICT
// territory in the gate). This is the fail-closed invariant.
func TestExtractVerdictFromJSONL_NoVerdictAnywhere(t *testing.T) {
	// Build a JSONL with a text event carrying non-verdict prose plus a
	// review_result whose wrapper carries no raw_output.
	log := jsonlTextEvent("hello world, no verdict here") + "\n" +
		jsonlReviewResultWrapper("") + "\n"
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("ExtractVerdictFromJSONL: %v", err)
	}
	if got.IsSet() {
		t.Errorf("verdict = %q, want empty (no verdict in any carrier)", got.Verdict)
	}
}

// TestExtractVerdictFromJSONL_EmptyLog covers the trivially empty input.
func TestExtractVerdictFromJSONL_EmptyLog(t *testing.T) {
	got, err := ExtractVerdictFromJSONL(strings.NewReader(""))
	if err != nil {
		t.Fatalf("ExtractVerdictFromJSONL: %v", err)
	}
	if got.IsSet() {
		t.Errorf("verdict = %q, want empty", got.Verdict)
	}
}

// TestExtractBraceBalancedJSON covers edge cases that broke the live python
// brace scanner: nested strings, escaped quotes, embedded braces in strings.
func TestExtractBraceBalancedJSON(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"{}", []string{"{}"}},
		{"}{}", []string{"{}"}},                    // leading close brace ignored; second {} balanced
		{`{"a":1}`, []string{`{"a":1}`}},           //nolint:gocritic // intentional test string
		{`{"a":"b\"c"}`, []string{`{"a":"b\"c"}`}}, // escaped quote inside string
		{`{"a":"}{"}`, []string{`{"a":"}{"}`}},     // braces inside string ignored
	}
	for i, c := range cases {
		got := extractBraceBalancedJSON(c.in)
		if len(got) != len(c.want) {
			t.Errorf("case %d (%q): got %d slices, want %d", i, c.in, len(got), len(c.want))
			continue
		}
		for j := range got {
			if got[j] != c.want[j] {
				t.Errorf("case %d slice %d: got %q, want %q", i, j, got[j], c.want[j])
			}
		}
	}
}

// TestDecodeVerdictFromJSON covers the strict-JSON probe. Non-object inputs
// must NOT yield a verdict (gates never infer from raw strings here).
func TestDecodeVerdictFromJSON(t *testing.T) {
	cases := []struct {
		blob   string
		want   ReviewerVerdict
		wantOK bool
	}{
		{`{"verdict":"PASS"}`, ReviewerVerdictPass, true},
		{`{"verdict":"FAIL"}`, ReviewerVerdictFail, true},
		{`{"verdict":"pass"}`, ReviewerVerdictPass, true}, // case-insensitive
		{`{"verdict": "PASS" }`, ReviewerVerdictPass, true},
		{`{"verdict":"MAYBE"}`, "", false},
		{`{"verdict":""}`, "", false},
		{`{"verdict":42}`, "", false}, // wrong type
		{`{"summary":"ok"}`, "", false},
		{`[]`, "", false},
		{`"PASS"`, "", false},
		{``, "", false},
	}
	for _, c := range cases {
		got, ok := decodeVerdictFromJSON(c.blob)
		if ok != c.wantOK || got != c.want {
			t.Errorf("decodeVerdictFromJSON(%q) = (%q, %v); want (%q, %v)", c.blob, got, ok, c.want, c.wantOK)
		}
	}
}

// TestExtractVerdictFromJSONL_MalformedLinesIgnored covers the scanner's
// robustness: bad JSON lines are skipped, not surfaced as errors.
func TestExtractVerdictFromJSONL_MalformedLinesIgnored(t *testing.T) {
	log := "not json\n" +
		jsonlTextEvent(fencedJSONPassPayload) + "\n" +
		`{"oops": malformed` + "\n"
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("ExtractVerdictFromJSONL: %v", err)
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("verdict = %q, want PASS", got.Verdict)
	}
}

// TestExtractVerdictFromJSONL_MultiFinding_PicksFirstWithVerdict covers a
// wrapper that emits multiple findings, only one of which carries a
// parseable verdict. The first findings[] entry that yields a verdict wins.
func TestExtractVerdictFromJSONL_MultiFinding_PicksFirstWithVerdict(t *testing.T) {
	// Build a wrapper with two findings manually — the second carries the
	// fenced PASS; the first carries prose that yields no verdict.
	review := map[string]any{
		"verdict": "FAIL",
		"findings": []map[string]any{
			{"classification": "WARNING", "raw_output": "noise, no verdict"},
			{"classification": "BLOCKING", "raw_output": fencedJSONPassPayload},
		},
		"summary": "wrapper failed to parse; preserved raw",
	}
	line, _ := json.Marshal(map[string]any{
		"type": "review_result",
		"part": map[string]any{
			"type":   "review-result",
			"review": review,
		},
	})
	got, err := ExtractVerdictFromJSONL(strings.NewReader(string(line) + "\n"))
	if err != nil {
		t.Fatalf("ExtractVerdictFromJSONL: %v", err)
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("verdict = %q, want PASS (second finding carries PASS)", got.Verdict)
	}
	if got.Source != VerdictSourceLogPartReviewFindingsRawOutput {
		t.Errorf("source = %q, want %q", got.Source, VerdictSourceLogPartReviewFindingsRawOutput)
	}
}

// parseLogLines is a small helper that decodes one JSONL line per record
// into a map. Tests use it to exercise single-entry helpers directly.
func parseLogLines(t *testing.T, log string) []map[string]json.RawMessage {
	t.Helper()
	var out []map[string]json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		out = append(out, entry)
	}
	return out
}
