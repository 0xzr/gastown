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

// jsonlItemTextEvent builds a JSONL text-event line with the given body as
// .item.text. Same JSON-escaping rules as jsonlTextEvent.
func jsonlItemTextEvent(body string) string {
	line := map[string]any{
		"type": "text",
		"item": map[string]any{
			"type": "text",
			"text": body,
		},
	}
	b, _ := json.Marshal(line)
	return string(b)
}

// jsonlStructuredPartText builds a JSONL text-event line where .part.text is
// an OBJECT (not a string). The object carries a .text field, and optionally
// a .verdict field — the regression cases below vary both to pin the
// verdict-precedence invariant on object leaves.
func jsonlStructuredPartText(obj map[string]any) string {
	line := map[string]any{
		"type": "text",
		"part": map[string]any{
			"type": "text",
			"text": obj,
		},
	}
	b, _ := json.Marshal(line)
	return string(b)
}

// jsonlStructuredItemText builds a JSONL text-event line where .item.text is
// an OBJECT (not a string). Same usage pattern as jsonlStructuredPartText.
func jsonlStructuredItemText(obj map[string]any) string {
	line := map[string]any{
		"type": "text",
		"item": map[string]any{
			"type": "text",
			"text": obj,
		},
	}
	b, _ := json.Marshal(line)
	return string(b)
}

// jsonlStructuredResult builds a JSONL text-event line where .result is an
// OBJECT (not a string). Used to exercise the verdict-precedence invariant
// on the .result carrier (extractFromResultField → carrierTextOrNested).
func jsonlStructuredResult(obj map[string]any) string {
	line := map[string]any{
		"type":   "text",
		"result": obj,
	}
	b, _ := json.Marshal(line)
	return string(b)
}

// jsonlResultString builds a JSONL text-event line where .result is a plain
// string. Used to exercise the simple-string branch of extractFromResultField.
func jsonlResultString(body string) string {
	line := map[string]any{
		"type":   "text",
		"result": body,
	}
	b, _ := json.Marshal(line)
	return string(b)
}

// jsonlSyntheticReviewResult builds the verbatim OpenCode-family synthetic
// wrapper shape recorded in /home/ubuntu/.claude/sessions/opencode-review-
// 20260626T172348-1709338.jsonl (gastown-9lc). The wrapper has hard-coded
// verdict=FAIL plus a findings[0].raw_output carrying the model's real text.
func jsonlSyntheticReviewResult(rawOutput string) string {
	line := map[string]any{
		"type": "review_result",
		"part": map[string]any{
			"type": "review-result",
			"review": map[string]any{
				"verdict": "FAIL",
				"summary": "OpenCode review output was normalized to FAIL because it did not contain parseable {verdict, findings, summary} JSON.",
				"findings": []any{
					map[string]any{
						"classification": "BLOCKING",
						"citation":       nil,
						"explanation":    "OpenCode did not return parseable review JSON.",
						"raw_output":     rawOutput,
					},
				},
			},
		},
	}
	b, _ := json.Marshal(line)
	return string(b)
}

// jsonlNonSyntheticReviewVerdict builds a non-synthetic review_result with
// a structured .part.review.verdict and a non-conflicting summary. Used to
// exercise the extractFromPartReviewVerdict path.
func jsonlNonSyntheticReviewVerdict(verdict string) string {
	line := map[string]any{
		"type": "review_result",
		"part": map[string]any{
			"type": "review-result",
			"review": map[string]any{
				"verdict": verdict,
				"summary": "non-synthetic reviewer summary",
			},
		},
	}
	b, _ := json.Marshal(line)
	return string(b)
}

// jsonlLooseFallback builds a non-synthetic text-event line whose top-level
// .verdict (or .review.verdict / .raw_output / .text) carries the verdict.
// Used to exercise extractFromLooseFallback.
func jsonlLooseFallback(field string, value any) string {
	return jsonlLooseFallbackAt(map[string]any{field: value})
}

func jsonlLooseFallbackAt(fields map[string]any) string {
	line := map[string]any{
		"type":   "text",
		"part":   map[string]any{"type": "text", "text": "unrelated chatter"},
		"review": map[string]any{"verdict": "irrelevant"},
	}
	for k, v := range fields {
		line[k] = v
	}
	b, _ := json.Marshal(line)
	return string(b)
}

// -----------------------------------------------------------------------------
// ExtractVerdictFromText tests
// -----------------------------------------------------------------------------

func TestExtractVerdictFromText_PlainToken(t *testing.T) {
	for _, c := range []struct {
		in       string
		expected ReviewerVerdict
	}{
		{"PASS", ReviewerVerdictPass},
		{"FAIL", ReviewerVerdictFail},
		{" pass ", ReviewerVerdictPass}, // whitespace trimmed
		{"'FAIL'", ReviewerVerdictFail}, // single layer of quoting stripped
		{"`PASS`", ReviewerVerdictPass}, // backticks stripped
	} {
		got := ExtractVerdictFromText(c.in)
		if got.Verdict != c.expected {
			t.Errorf("ExtractVerdictFromText(%q) = %v, want %v (source=%q)", c.in, got.Verdict, c.expected, got.Source)
		}
	}
}

func TestExtractVerdictFromText_JSONObject(t *testing.T) {
	in := `{"verdict": "PASS", "findings": []}`
	got := ExtractVerdictFromText(in)
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS (source=%q)", got.Verdict, got.Source)
	}
	if got.Source != VerdictSourceJSONObject {
		t.Errorf("source = %q, want json_object", got.Source)
	}
}

func TestExtractVerdictFromText_FencedCodeBlock(t *testing.T) {
	got := ExtractVerdictFromText(fencedJSONPassPayload)
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS (source=%q)", got.Verdict, got.Source)
	}
	if got.Source != VerdictSourceFencedCodeBlock {
		t.Errorf("source = %q, want fenced_code_block", got.Source)
	}
}

func TestExtractVerdictFromText_FencedCodeBlock_NoLangTag(t *testing.T) {
	// ``` without "json" tag — live python accepts both shapes.
	in := "```\n{\"verdict\": \"FAIL\"}\n```"
	got := ExtractVerdictFromText(in)
	if got.Verdict != ReviewerVerdictFail {
		t.Errorf("got %v, want FAIL (source=%q)", got.Verdict, got.Source)
	}
}

func TestExtractVerdictFromText_BraceMatchedJSON(t *testing.T) {
	// Verdict buried in prose — no fence, no top-level object.
	in := `Reviewer preamble. {"verdict":"PASS"} trailing chatter.`
	got := ExtractVerdictFromText(in)
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS (source=%q)", got.Verdict, got.Source)
	}
	if got.Source != VerdictSourceBraceMatchedJSON {
		t.Errorf("source = %q, want brace_matched_json", got.Source)
	}
}

func TestExtractVerdictFromText_BraceMatchedJSON_LastWins(t *testing.T) {
	// Live gate returns LAST parseable verdict. Same applies here.
	in := `{"verdict":"PASS"} then later {"verdict":"FAIL"}`
	got := ExtractVerdictFromText(in)
	if got.Verdict != ReviewerVerdictFail {
		t.Errorf("got %v, want FAIL (last parseable wins)", got.Verdict)
	}
}

func TestExtractVerdictFromText_RegexFallback(t *testing.T) {
	// Hand-written or partially-escaped verdict text — the brace-balanced
	// scan fails (because the surrounding context makes it not parseable
	// as JSON), but the regex can still pluck out "verdict":"FAIL".
	in := `not parseable as JSON, but embedded: "verdict":"FAIL" — keep going`
	got := ExtractVerdictFromText(in)
	if got.Verdict != ReviewerVerdictFail {
		t.Errorf("got %v, want FAIL (source=%q)", got.Verdict, got.Source)
	}
	if got.Source != VerdictSourceVerdictRegex {
		t.Errorf("source = %q, want verdict_regex", got.Source)
	}
}

func TestExtractVerdictFromText_UnparseableIsEmpty(t *testing.T) {
	for _, in := range []string{
		"",
		"   ",
		"no verdict here",
		"verdict: maybe?",
		`{"unrelated": "shape"}`,
	} {
		got := ExtractVerdictFromText(in)
		if got.IsSet() {
			t.Errorf("ExtractVerdictFromText(%q) unexpectedly set: %+v", in, got)
		}
	}
}

func TestExtractBraceBalancedJSON_StringEscapes(t *testing.T) {
	// Braces inside strings must not confuse the depth counter.
	in := `pre {"a":"}","b":{"verdict":"PASS"}} post`
	got := ExtractVerdictFromText(in)
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS (source=%q)", got.Verdict, got.Source)
	}
}

// -----------------------------------------------------------------------------
// ExtractVerdictFromJSONL tests — verbatim gastown-wisp-7kd / gastown-9lc
// reproduction (the original bug that downgraded m3 to UNAVAILABLE).
// -----------------------------------------------------------------------------

func TestExtractVerdictFromJSONL_OpencodeReviewVerbatimShape(t *testing.T) {
	// Step event, text event with fenced JSON PASS, then synthetic review_result.
	// The live gate's old query only looked at .part.review.raw_output
	// (top-level), which the adapter does not write — so m3 was recorded
	// as UNAVAILABLE. The Go mirror extracts the fenced PASS from
	// findings[0].raw_output and recovers the real verdict.
	log := strings.Join([]string{
		jsonlSyntheticReviewResult(fencedJSONPassPayload),
	}, "\n")

	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS — original gastown-9lc bug regression: synthetic wrapper masked model PASS verdict (source=%q)", got.Verdict, got.Source)
	}
	if got.Source != VerdictSourceLogPartReviewFindingsRawOutput {
		t.Errorf("source = %q, want log_part_review_findings_raw_output", got.Source)
	}
}

func TestExtractVerdictFromJSONL_TopLevelRawOutputAbsent_DocumentsBug(t *testing.T) {
	// Demonstrates that the previous live-gate query (`.part.review.raw_output`,
	// top-level) misses the recoverable verdict because the adapter does NOT
	// write that field. The new path (`.part.review.findings[].raw_output`)
	// correctly recovers it.
	log := strings.Join([]string{
		jsonlSyntheticReviewResult(fencedJSONPassPayload),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Source != VerdictSourceLogPartReviewFindingsRawOutput {
		t.Errorf("source = %q, want log_part_review_findings_raw_output (top-level path should NOT have matched)", got.Source)
	}
}

func TestExtractVerdictFromJSONL_StrippedTextEventRecoversFromFindingsRawOutput(t *testing.T) {
	// When the live gate strips the surrounding wrapper and only carries
	// the raw_output forward, the parser must still recover the verdict.
	// Synthesize that as a JSONL record where the only carrier is the
	// fenced JSON inside the wrapper's finding raw_output.
	log := strings.Join([]string{
		jsonlSyntheticReviewResult(fencedJSONPassPayload),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.IsPass() {
		t.Errorf("got %v, want PASS", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_TextEventPriorityOverWrapperDisagreement(t *testing.T) {
	// Higher carrier priority wins even when a later record at lower
	// priority disagrees. part.text PASS > part.review.verdict FAIL.
	log := strings.Join([]string{
		jsonlTextEvent("```json\n{\"verdict\":\"PASS\"}\n```"),
		jsonlSyntheticReviewResult(fencedJSONPassPayload),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS — higher priority (part.text) should override later lower-priority FAIL", got.Verdict)
	}
	if got.Source != VerdictSourceLogPartText {
		t.Errorf("source = %q, want log_part_text", got.Source)
	}
}

func TestExtractVerdictFromJSONL_FindingsFAILPropagates(t *testing.T) {
	// When the synthetic wrapper preserves a FAIL in findings[].raw_output,
	// the parser MUST return FAIL — never mask it with a higher-priority
	// carrier that is absent or empty.
	fencedFail := "```json\n{\"verdict\":\"FAIL\",\"findings\":[],\"summary\":\"blocked\"}\n```"
	log := strings.Join([]string{
		jsonlSyntheticReviewResult(fencedFail),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictFail {
		t.Errorf("got %v, want FAIL — synthetic wrapper preserved model FAIL, must propagate", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_EmptyLogIsEmpty(t *testing.T) {
	got, err := ExtractVerdictFromJSONL(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.IsSet() {
		t.Errorf("empty log should yield empty verdict, got %+v", got)
	}
}

func TestExtractVerdictFromJSONL_MalformedLinesSkipped(t *testing.T) {
	log := strings.Join([]string{
		"this is not json",
		"",
		jsonlSyntheticReviewResult(fencedJSONPassPayload),
		"{also not json",
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS — malformed lines should be skipped, not abort the parse", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_MultiFindingArraySynthetic(t *testing.T) {
	// OpenCode wrapper sometimes carries multiple findings. The LAST finding
	// with a parseable verdict wins.
	findingA := "```json\n{\"verdict\":\"PASS\"}\n```"
	findingB := "```json\n{\"verdict\":\"FAIL\"}\n```"
	line := map[string]any{
		"type": "review_result",
		"part": map[string]any{
			"type": "review-result",
			"review": map[string]any{
				"verdict": "FAIL",
				"summary": "OpenCode review output was normalized to FAIL because it did not contain parseable {verdict, findings, summary} JSON.",
				"findings": []any{
					map[string]any{
						"classification": "BLOCKING",
						"citation":       nil,
						"explanation":    "OpenCode did not return parseable review JSON.",
						"raw_output":     findingA,
					},
					map[string]any{
						"classification": "BLOCKING",
						"citation":       nil,
						"explanation":    "OpenCode did not return parseable review JSON.",
						"raw_output":     findingB,
					},
				},
			},
		},
	}
	b, _ := json.Marshal(line)
	got, err := ExtractVerdictFromJSONL(strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictFail {
		t.Errorf("got %v, want FAIL — LAST finding's verdict should win", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_LastRecordWinsWithinPriority(t *testing.T) {
	// Two records at the same priority (findings[].raw_output) — last
	// wins. (Both are inside the synthetic branch; the synthetic detection
	// is across the whole log, so the second record still routes to the
	// findings path.)
	fencedA := "```json\n{\"verdict\":\"PASS\"}\n```"
	fencedB := "```json\n{\"verdict\":\"FAIL\"}\n```"
	log := strings.Join([]string{
		jsonlSyntheticReviewResult(fencedA),
		jsonlSyntheticReviewResult(fencedB),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictFail {
		t.Errorf("got %v, want FAIL — last record at same priority should win", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_HigherPriorityBeatsLaterRecord(t *testing.T) {
	// part.text PASS (record 0, prio=partText) beats part.review.verdict
	// FAIL (record 1, prio=partReviewVerdict). Higher priority wins
	// regardless of record order.
	log := strings.Join([]string{
		jsonlTextEvent("PASS"),
		jsonlNonSyntheticReviewVerdict("FAIL"),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS — higher priority (part.text) should beat later lower-priority FAIL", got.Verdict)
	}
}

// -----------------------------------------------------------------------------
// Object-shaped text carriers (carrierTextOrNested) — verdict-precedence
// regression coverage for gastown-wisp-cj1.
// -----------------------------------------------------------------------------

func TestExtractVerdictFromJSONL_NestedObjectTextCarrier_PassesThroughText(t *testing.T) {
	// A text-event carrier whose .part.text is an object containing only
	// a .text field. With verdict precedence the parser checks .verdict
	// first, finds nothing, falls through to .text, and yields that
	// text's verdict. (No regression vs prior behavior for the common
	// part.text object shape.)
	log := strings.Join([]string{
		jsonlStructuredPartText(map[string]any{"text": "PASS"}),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS — text-only object carrier should pass through text", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_StructuredResult_StringVerdictPASS(t *testing.T) {
	// .result as a JSON STRING carrying PASS — straightforward case.
	log := strings.Join([]string{
		jsonlResultString("PASS"),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS (source=%q)", got.Verdict, got.Source)
	}
}

func TestExtractVerdictFromJSONL_StructuredResult_ObjectWithVerdictOnly(t *testing.T) {
	// .result as a structured object with only .verdict — straightforward case.
	log := strings.Join([]string{
		jsonlStructuredResult(map[string]any{"verdict": "FAIL"}),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictFail {
		t.Errorf("got %v, want FAIL — structured .verdict must be honored", got.Verdict)
	}
	if got.Source != VerdictSourceLogResultField {
		t.Errorf("source = %q, want log_result_field", got.Source)
	}
}

// -----------------------------------------------------------------------------
// gastown-wisp-cj1 regression tests — verdict-precedence invariant on
// structured object carriers. Prior implementation consulted .text BEFORE
// .verdict, which masked reviewer FAILs.
//
//   {"verdict":"FAIL","text":"PASS"}        -> must be FAIL (not PASS)
//   {"verdict":"FAIL","text":"no verdict"}  -> must be FAIL (not empty)
//   {"verdict":"PASS","text":"FAIL"}        -> must be PASS (verdict wins)
//   {"verdict":"FAIL","text":""}            -> must be FAIL (empty text does not mask)
//   {"verdict":"FAIL","raw_output":"PASS"}  -> must be FAIL (verdict first, raw_output not consulted)
//
// These pin the fix on .result (the carrier the witness flagged) AND on
// part.text / item.text (where the same precedence invariant applies to
// object-shaped leaves). Without verdict-precedence, every case below
// silently regresses.
// -----------------------------------------------------------------------------

func TestExtractVerdictFromJSONL_VerdictPrecedence_Result_FailTextPass(t *testing.T) {
	// The witness-flagged case: structured .result where .verdict=FAIL
	// disagrees with .text=PASS. Verdict precedence must classify as FAIL.
	log := strings.Join([]string{
		jsonlStructuredResult(map[string]any{"verdict": "FAIL", "text": "PASS"}),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictFail {
		t.Errorf("got %v, want FAIL — verdict precedence invariant: structured .verdict=FAIL must NOT be masked by .text=PASS (gastown-wisp-cj1)", got.Verdict)
	}
	if got.Source != VerdictSourceLogResultField {
		t.Errorf("source = %q, want log_result_field", got.Source)
	}
}

func TestExtractVerdictFromJSONL_VerdictPrecedence_Result_FailTextNoVerdict(t *testing.T) {
	// .verdict=FAIL but .text is the literal string "no verdict" (which
	// ExtractVerdictFromText cannot parse). Prior implementation would
	// return empty (FAIL dropped); the fix returns FAIL.
	log := strings.Join([]string{
		jsonlStructuredResult(map[string]any{"verdict": "FAIL", "text": "no verdict"}),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictFail {
		t.Errorf("got %v, want FAIL — verdict precedence invariant: structured .verdict=FAIL must be returned even when .text is unparseable (gastown-wisp-cj1)", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_VerdictPrecedence_Result_PassTextFail(t *testing.T) {
	// Symmetric case: .verdict=PASS, .text=FAIL. Verdict wins.
	log := strings.Join([]string{
		jsonlStructuredResult(map[string]any{"verdict": "PASS", "text": "FAIL"}),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS — structured .verdict=PASS wins over .text=FAIL", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_VerdictPrecedence_Result_FailTextEmpty(t *testing.T) {
	// Empty .text should not mask a structured FAIL.
	log := strings.Join([]string{
		jsonlStructuredResult(map[string]any{"verdict": "FAIL", "text": ""}),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictFail {
		t.Errorf("got %v, want FAIL — empty .text must not mask structured .verdict=FAIL", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_VerdictPrecedence_Result_FailRawOutputPass(t *testing.T) {
	// .verdict=FAIL with .raw_output=PASS — verdict wins; raw_output is
	// NOT consulted because verdict already yielded a terminal value.
	log := strings.Join([]string{
		jsonlStructuredResult(map[string]any{
			"verdict":    "FAIL",
			"raw_output": "```json\n{\"verdict\":\"PASS\"}\n```",
		}),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictFail {
		t.Errorf("got %v, want FAIL — structured .verdict takes precedence over .raw_output", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_VerdictPrecedence_PartText_FailTextPass(t *testing.T) {
	// The verdict-precedence invariant also applies to part.text when
	// the leaf is an object. .verdict=FAIL with .text=PASS must yield FAIL.
	log := strings.Join([]string{
		jsonlStructuredPartText(map[string]any{"verdict": "FAIL", "text": "PASS"}),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictFail {
		t.Errorf("got %v, want FAIL — part.text object with .verdict=FAIL must not be masked by .text=PASS", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_VerdictPrecedence_ItemText_FailTextNoVerdict(t *testing.T) {
	// item.text object: .verdict=FAIL, .text="no verdict" — must yield FAIL.
	log := strings.Join([]string{
		jsonlStructuredItemText(map[string]any{"verdict": "FAIL", "text": "no verdict"}),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictFail {
		t.Errorf("got %v, want FAIL — item.text object with .verdict=FAIL must be returned even when .text is unparseable", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_StructuredResult_FallsThroughToTextWhenVerdictMissing(t *testing.T) {
	// When .verdict is absent (or unparseable), the parser should fall
	// through to .text — preserving prior behavior for legitimate text-only
	// object carriers.
	log := strings.Join([]string{
		jsonlStructuredResult(map[string]any{"text": "PASS"}),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS — when .verdict is missing, parser must fall through to .text", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_StructuredResult_FallsThroughToRawOutput(t *testing.T) {
	// When .verdict AND .text are absent, parser falls through to
	// .raw_output. Verdict precedence does not break the existing chain.
	log := strings.Join([]string{
		jsonlStructuredResult(map[string]any{"raw_output": "PASS"}),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS — when .verdict/.text missing, parser falls through to .raw_output", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_StructuredResult_NoUsableFieldIsEmpty(t *testing.T) {
	// Object with no usable verdict/text/raw_output yields empty (fail-closed).
	log := strings.Join([]string{
		jsonlStructuredResult(map[string]any{"unrelated": "shape"}),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.IsSet() {
		t.Errorf("got %v, want empty — object with no usable carrier fields must be empty (fail-closed)", got.Verdict)
	}
}

// -----------------------------------------------------------------------------
// Non-synthetic review_result paths — extractFromPartReviewVerdict and
// extractFromLooseFallback. Verdict precedence is already correct in these
// helpers (.verdict is consulted first in both), so the tests below pin the
// expected behavior.
// -----------------------------------------------------------------------------

func TestExtractVerdictFromJSONL_NonSyntheticPartReviewVerdict_PASS(t *testing.T) {
	log := strings.Join([]string{
		jsonlNonSyntheticReviewVerdict("PASS"),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS (source=%q)", got.Verdict, got.Source)
	}
	if got.Source != VerdictSourceLogPartReviewVerdict {
		t.Errorf("source = %q, want log_part_review_verdict", got.Source)
	}
}

func TestExtractVerdictFromJSONL_NonSyntheticPartReviewVerdict_FAIL(t *testing.T) {
	log := strings.Join([]string{
		jsonlNonSyntheticReviewVerdict("FAIL"),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictFail {
		t.Errorf("got %v, want FAIL", got.Verdict)
	}
	if got.Source != VerdictSourceLogPartReviewVerdict {
		t.Errorf("source = %q, want log_part_review_verdict", got.Source)
	}
}

func TestExtractVerdictFromJSONL_NonSyntheticPartReviewVerdict_NonTerminalIsEmpty(t *testing.T) {
	// Non-PASS/FAIL values are fail-closed: empty verdict, never coerced.
	log := strings.Join([]string{
		jsonlNonSyntheticReviewVerdict("MAYBE"),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.IsSet() {
		t.Errorf("got %v, want empty — non-terminal verdict values must be fail-closed", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_SyntheticSkipsPartReviewVerdict(t *testing.T) {
	// When ANY record in the log is synthetic, part.review.verdict (which
	// for synthetic wrappers is a hard-coded FAIL signal) MUST be skipped.
	// Otherwise the wrapper's fixed FAIL would mask the real model verdict
	// preserved in findings[].raw_output.
	log := strings.Join([]string{
		jsonlSyntheticReviewResult(fencedJSONPassPayload),
		jsonlNonSyntheticReviewVerdict("FAIL"),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The synthetic wrapper's findings[].raw_output yields PASS; the
	// part.review.verdict FAIL is masked because syntheticAny=true.
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS — synthetic detection must skip part.review.verdict", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_SyntheticDetection_ByAdapterError(t *testing.T) {
	// Synthetic detection signal #1: .part.review.adapter_error ==
	// "no_parseable_review_json".
	line := map[string]any{
		"type": "review_result",
		"part": map[string]any{
			"type": "review-result",
			"review": map[string]any{
				"verdict":       "FAIL",
				"summary":       "non-canonical summary",
				"adapter_error": "no_parseable_review_json",
				"findings": []any{
					map[string]any{
						"classification": "BLOCKING",
						"raw_output":     fencedJSONPassPayload,
					},
				},
			},
		},
	}
	b, _ := json.Marshal(line)
	got, err := ExtractVerdictFromJSONL(strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS — adapter_error=no_parseable_review_json triggers synthetic path", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_SyntheticDetection_ByCanonicalSummary(t *testing.T) {
	// Synthetic detection signal #2: .part.review.summary matches the
	// canonical OpenCode failure summary.
	line := map[string]any{
		"type": "review_result",
		"part": map[string]any{
			"type": "review-result",
			"review": map[string]any{
				"verdict": "FAIL",
				"summary": "OpenCode review output was normalized to FAIL because it did not contain parseable {verdict, findings, summary} JSON.",
				"findings": []any{
					map[string]any{
						"classification": "BLOCKING",
						"explanation":    "not the canonical one",
						"raw_output":     fencedJSONPassPayload,
					},
				},
			},
		},
	}
	b, _ := json.Marshal(line)
	got, err := ExtractVerdictFromJSONL(strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS — canonical summary triggers synthetic path", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_SyntheticDetection_ByCanonicalExplanation(t *testing.T) {
	// Synthetic detection signal #3: any .part.review.findings[].explanation
	// matches the canonical OpenCode failure explanation.
	line := map[string]any{
		"type": "review_result",
		"part": map[string]any{
			"type": "review-result",
			"review": map[string]any{
				"verdict": "FAIL",
				"summary": "not the canonical one",
				"findings": []any{
					map[string]any{
						"classification": "BLOCKING",
						"citation":       nil,
						"explanation":    "OpenCode did not return parseable review JSON.",
						"raw_output":     fencedJSONPassPayload,
					},
				},
			},
		},
	}
	b, _ := json.Marshal(line)
	got, err := ExtractVerdictFromJSONL(strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS — canonical explanation triggers synthetic path", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_NonSyntheticNoSkipsPartReviewVerdict(t *testing.T) {
	// No synthetic records present — part.review.verdict is honored.
	log := strings.Join([]string{
		jsonlNonSyntheticReviewVerdict("FAIL"),
		jsonlTextEvent("PASS"),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Higher priority part.text PASS wins over lower-priority
	// part.review.verdict FAIL — this is the standard priority chain.
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS", got.Verdict)
	}
}

// -----------------------------------------------------------------------------
// Loose fallback paths — extractFromLooseFallback. Verdict precedence is
// already correct (.verdict is consulted FIRST in this helper), so these
// tests pin expected behavior.
// -----------------------------------------------------------------------------

func TestExtractVerdictFromJSONL_LooseFallback_TopLevelVerdict(t *testing.T) {
	log := strings.Join([]string{
		jsonlLooseFallback("verdict", "FAIL"),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictFail {
		t.Errorf("got %v, want FAIL (source=%q)", got.Verdict, got.Source)
	}
	if got.Source != VerdictSourceLogLooseFallback {
		t.Errorf("source = %q, want log_loose_fallback", got.Source)
	}
}

func TestExtractVerdictFromJSONL_LooseFallback_NestedReviewVerdict(t *testing.T) {
	log := strings.Join([]string{
		jsonlLooseFallbackAt(map[string]any{
			"review": map[string]any{"verdict": "PASS"},
		}),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS", got.Verdict)
	}
}

func TestExtractVerdictFromJSONL_LooseFallback_TopLevelRawOutput(t *testing.T) {
	log := strings.Join([]string{
		jsonlLooseFallback("raw_output", fencedJSONPassPayload),
	}, "\n")
	got, err := ExtractVerdictFromJSONL(strings.NewReader(log))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != ReviewerVerdictPass {
		t.Errorf("got %v, want PASS — fenced JSON in top-level .raw_output should parse", got.Verdict)
	}
}

// -----------------------------------------------------------------------------
// VerdictExtraction helpers — IsSet / IsPass / IsFail.
// -----------------------------------------------------------------------------

func TestVerdictExtraction_Helpers(t *testing.T) {
	empty := VerdictExtraction{}
	if empty.IsSet() || empty.IsPass() || empty.IsFail() {
		t.Errorf("empty VerdictExtraction helpers all false, got %+v", empty)
	}
	pass := VerdictExtraction{Verdict: ReviewerVerdictPass}
	if !pass.IsSet() || !pass.IsPass() || pass.IsFail() {
		t.Errorf("PASS helpers wrong: %+v", pass)
	}
	fail := VerdictExtraction{Verdict: ReviewerVerdictFail}
	if !fail.IsSet() || fail.IsPass() || !fail.IsFail() {
		t.Errorf("FAIL helpers wrong: %+v", fail)
	}
	other := VerdictExtraction{Verdict: ReviewerVerdictNoVerdict}
	if !other.IsSet() || other.IsPass() || other.IsFail() {
		t.Errorf("non-PASS/FAIL IsSet=true only: %+v", other)
	}
}

// -----------------------------------------------------------------------------
// priorityOfSource — pure helper. Pin its mapping.
// -----------------------------------------------------------------------------

func TestPriorityOfSource(t *testing.T) {
	cases := []struct {
		s        VerdictSource
		expected carrierPriority
	}{
		{VerdictSourceLogPartText, prioPartText},
		{VerdictSourceLogItemText, prioItemText},
		{VerdictSourceLogResultField, prioResultField},
		{VerdictSourceLogPartReviewRawOutput, prioPartReviewRawOutput},
		{VerdictSourceLogPartReviewFindingsRawOutput, prioPartReviewFindingsRawOutput},
		{VerdictSourceLogPartReviewVerdict, prioPartReviewVerdict},
		{VerdictSourceLogLooseFallback, prioLooseFallback},
		{VerdictSourceNone, prioMissing},
		{VerdictSource("unknown"), prioMissing},
	}
	for _, c := range cases {
		got := priorityOfSource(c.s)
		if got != c.expected {
			t.Errorf("priorityOfSource(%q) = %v, want %v", c.s, got, c.expected)
		}
	}
}
