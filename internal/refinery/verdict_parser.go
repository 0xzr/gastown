// Package refinery — verdict_parser.go is the source-controlled, durable Go
// mirror of the live refinery-gate.sh review-verdict recovery logic. The live
// gate (gastown-spike/dropin/refinery-gate.sh) extracts a verdict from raw
// reviewer output via jq + python and feeds it to the core multi-model
// quorum mirror in review.go.
//
// Background (gastown-9lc): during a live gate, MiniMax/M3 returned a fenced
// JSON review with top-level verdict PASS, but refinery-gate.sh recorded
// m3: UNAVAILABLE / no verdict. The OpenCode-family adapter had emitted a
// synthetic wrapper review_result with verdict=FAIL/adapter_error=
// no_parseable_review_json and preserved the model's real text under
// `.part.review.findings[].raw_output`, which the gate's recovery query
// (`select(.type == "review_result") | .part.review.raw_output`) does not
// actually look at — it queries a top-level field the adapter does not write.
//
// The Go mirror in this file fixes the recovery shape by:
//   - extracting fenced JSON, plain PASS/FAIL tokens, JSON objects,
//     brace-matched JSON substrings, and a regex fallback from arbitrary
//     text (ExtractVerdictFromText); and
//   - inspecting every well-known carrier field of a JSONL log entry
//     (ExtractVerdictFromJSONL / extractFromLogEntry), INCLUDING
//     `.part.review.findings[].raw_output`, which the live gate missed.
//
// Fail-closed invariants — preserved from the live gate — are:
//   - Unparseable / no-verdict output returns "" (NO_VERDICT / UNAVAILABLE
//     territory); it does not silently turn into PASS.
//   - A real FAIL is honored: an extracted FAIL is never masked by a peer
//     being unavailable, and EvaluateCoreReviewerQuorum rejects on FAIL.
//   - The reviewer-output verdict feeds only the per-reviewer ReviewerResult
//     classification; the merge still requires every selected non-writer
//     core peer plus the durable HMAC attestation (review.go mirrors).
package refinery

import (
	"bufio"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

// VerdictSource identifies which carrier field / extraction step produced the
// returned verdict. It is reported for traceability so callers can audit
// which raw-log shape carried the final answer.
type VerdictSource string

const (
	VerdictSourceNone VerdictSource = ""

	// Text-level sources (ExtractVerdictFromText).
	VerdictSourcePlainToken       VerdictSource = "plain_token"
	VerdictSourceJSONObject       VerdictSource = "json_object"
	VerdictSourceFencedCodeBlock  VerdictSource = "fenced_code_block"
	VerdictSourceBraceMatchedJSON VerdictSource = "brace_matched_json"
	VerdictSourceVerdictRegex     VerdictSource = "verdict_regex"

	// JSONL carrier-field sources (ExtractVerdictFromJSONL).
	VerdictSourceLogPartText                    VerdictSource = "log_part_text"
	VerdictSourceLogItemText                    VerdictSource = "log_item_text"
	VerdictSourceLogResultField                 VerdictSource = "log_result_field"
	VerdictSourceLogPartReviewRawOutput         VerdictSource = "log_part_review_raw_output"
	VerdictSourceLogPartReviewFindingsRawOutput VerdictSource = "log_part_review_findings_raw_output"
)

// VerdictExtraction is the result of scanning a single text blob or a single
// JSONL log entry. Verdict is "" when no parseable verdict was found; callers
// must not interpret "" as PASS.
type VerdictExtraction struct {
	Verdict ReviewerVerdict
	Source  VerdictSource
}

// IsSet reports whether a verdict was extracted.
func (v VerdictExtraction) IsSet() bool { return v.Verdict != "" }

// IsPass / IsFail are convenience predicates. IsFail honors both ReviewerVerdictFail
// and a synthesized PASS-on-empty-diff reclassification (see review.go), but
// the parser itself never synthesizes the latter; it only extracts raw PASS/FAIL.
func (v VerdictExtraction) IsPass() bool { return v.Verdict == ReviewerVerdictPass }
func (v VerdictExtraction) IsFail() bool { return v.Verdict == ReviewerVerdictFail }

// fenceRe matches a fenced code block of the form ```json ... ``` or ``` ... ```.
// DOTALL is implicit through [\s\S]. We accept optional "json" / "JSON" language
// tags to match the live python regex (`r"```(?:json)?\s*(.*?)```"`).
var fenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

// verdictRe matches the case-insensitive "verdict":"PASS" or "verdict":"FAIL"
// fallback. Used only as a last resort after structured parsing fails.
var verdictRe = regexp.MustCompile(`(?i)"verdict"\s*:\s*"(PASS|FAIL)"`)

// ExtractVerdictFromText scans a single text blob for a {verdict: PASS|FAIL}
// payload and reports which extraction step succeeded. The order of preference
// matches the live gate's python (plain token → JSON object → fenced block →
// brace-matched JSON scan → regex) so this function is a faithful Go mirror of
// extract_review_verdict_text() in refinery-gate.sh.
//
// The text is taken as-is; callers should pass the raw carrier-field value
// (e.g. .part.text, .part.review.findings[].raw_output) exactly as it was
// decoded from JSON.
func ExtractVerdictFromText(text string) VerdictExtraction {
	// Step 1: bare PASS / FAIL token (after stripping whitespace and a
	// single layer of quoting).
	plain := strings.ToUpper(strings.Trim(strings.TrimSpace(text), "\"'`"))
	if plain == string(ReviewerVerdictPass) || plain == string(ReviewerVerdictFail) {
		return VerdictExtraction{Verdict: ReviewerVerdict(plain), Source: VerdictSourcePlainToken}
	}

	// Step 2: whole text is a single JSON object.
	if v, ok := decodeVerdictFromJSON(text); ok {
		return VerdictExtraction{Verdict: v, Source: VerdictSourceJSONObject}
	}

	// Step 3: fenced ```json ... ``` blocks. The live gate returns the LAST
	// parseable verdict; we mirror that to keep behavior consistent.
	var fenced VerdictExtraction
	for _, m := range fenceRe.FindAllStringSubmatch(text, -1) {
		if len(m) < 2 {
			continue
		}
		body := strings.TrimSpace(m[1])
		if v, ok := decodeVerdictFromJSON(body); ok {
			fenced = VerdictExtraction{Verdict: v, Source: VerdictSourceFencedCodeBlock}
		}
	}
	if fenced.IsSet() {
		return fenced
	}

	// Step 4: brace-matched JSON scan. Walks the string tracking brace depth
	// and string-state, then tries to parse every balanced {...} slice.
	// Last parseable verdict wins, matching the live python.
	var brace VerdictExtraction
	for _, sub := range extractBraceBalancedJSON(text) {
		if v, ok := decodeVerdictFromJSON(sub); ok {
			brace = VerdictExtraction{Verdict: v, Source: VerdictSourceBraceMatchedJSON}
		}
	}
	if brace.IsSet() {
		return brace
	}

	// Step 5: regex fallback for hand-written or partially-escaped verdicts.
	if m := verdictRe.FindStringSubmatch(text); len(m) >= 2 {
		return VerdictExtraction{
			Verdict: ReviewerVerdict(strings.ToUpper(m[1])),
			Source:  VerdictSourceVerdictRegex,
		}
	}

	return VerdictExtraction{}
}

// decodeVerdictFromJSON attempts to parse blob as a JSON object and read its
// top-level "verdict" field. It returns (PASS|FAIL, true) only when the field
// is a non-empty string equal (case-insensitively) to PASS or FAIL.
func decodeVerdictFromJSON(blob string) (ReviewerVerdict, bool) {
	blob = strings.TrimSpace(blob)
	if blob == "" || blob[0] != '{' {
		return "", false
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(blob), &probe); err != nil {
		return "", false
	}
	rawVerdict, ok := probe["verdict"]
	if !ok {
		return "", false
	}
	var verdictStr string
	if err := json.Unmarshal(rawVerdict, &verdictStr); err != nil {
		return "", false
	}
	up := strings.ToUpper(strings.TrimSpace(verdictStr))
	switch up {
	case string(ReviewerVerdictPass):
		return ReviewerVerdictPass, true
	case string(ReviewerVerdictFail):
		return ReviewerVerdictFail, true
	default:
		return "", false
	}
}

// extractBraceBalancedJSON returns every balanced {...} substring from text.
// String-state is tracked correctly so braces inside JSON strings do not
// confuse the depth counter. The last returned slice is the outermost
// closing brace position; callers iterate and try to decode each.
func extractBraceBalancedJSON(text string) []string {
	var out []string
	start := -1
	depth := 0
	inString := false
	escape := false
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if inString {
			if escape {
				escape = false
			} else if ch == '\\' {
				escape = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 && start >= 0 {
				out = append(out, text[start:i+1])
				start = -1
			}
		}
	}
	return out
}

// ExtractVerdictFromJSONL scans a JSONL log one record at a time and returns
// the FIRST verdict found across all carrier fields. It is the Go mirror of
// review_verdict() in refinery-gate.sh: the live gate chains jq queries
// against the same carrier fields in priority order; we read the whole log
// in one pass and apply the same priority per record.
//
// Returns (VerdictExtraction{}, nil) when no record yields a verdict. A
// non-nil error is returned only for I/O failures; malformed JSONL lines
// are silently skipped (matching the live gate's jq behavior).
//
// Note on the findings[].raw_output recovery: this is the gastown-9lc fix.
// The OpenCode-family adapter's wrapper preserves the model's real text
// under `.part.review.findings[].raw_output` when it cannot parse a
// structured review. The previous live-gate query only looked at
// `.part.review.raw_output` (top-level), which the adapter does not write,
// so a model PASS verdict was downgraded to UNAVAILABLE.
func ExtractVerdictFromJSONL(r io.Reader) (VerdictExtraction, error) {
	scanner := bufio.NewScanner(r)
	// Allow large lines: opencode sessions can carry 100k+ token transcripts.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			// Skip malformed lines (live jq behavior); do not surface as error.
			continue
		}
		if ext := extractFromLogEntry(entry); ext.IsSet() {
			return ext, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return VerdictExtraction{}, err
	}
	return VerdictExtraction{}, nil
}

// extractFromLogEntry inspects a single JSONL record and returns the verdict
// extracted from the highest-priority carrier field that yields one.
//
// Carrier priority (matches the live gate's chained jq queries, but ALSO
// adds the gastown-9lc fix path):
//  1. part.text — direct text event (text-event channel).
//  2. item.text — alternate text-event channel used by some adapters.
//  3. .result — adapter-final structured review object.
//  4. part.review.raw_output — wrapper's top-level raw output, if any.
//  5. part.review.findings[].raw_output — OpenCode wrapper's per-finding
//     raw output. This is the gastown-9lc fix: previously the live gate
//     stopped at step 4 and never checked the per-finding array, so a
//     model PASS inside a synthetic-FAIL wrapper was recorded as
//     UNAVAILABLE.
func extractFromLogEntry(entry map[string]json.RawMessage) VerdictExtraction {
	if v := extractFromPartText(entry); v.IsSet() {
		return v
	}
	if v := extractFromItemText(entry); v.IsSet() {
		return v
	}
	if v := extractFromResultField(entry); v.IsSet() {
		return v
	}
	if v := extractFromPartReviewRawOutput(entry); v.IsSet() {
		return v
	}
	if v := extractFromPartReviewFindingsRawOutput(entry); v.IsSet() {
		return v
	}
	return VerdictExtraction{}
}

// carrierText extracts a string carrier from a nested map path, returning
// "" (and false) on any miss — missing key, wrong type, empty string.
// It does NOT try to parse the string; callers pipe the result through
// ExtractVerdictFromText.
func carrierText(entry map[string]json.RawMessage, path ...string) (string, bool) {
	cur := entry
	for i, key := range path {
		raw, ok := cur[key]
		if !ok {
			return "", false
		}
		if i == len(path)-1 {
			var s string
			if err := json.Unmarshal(raw, &s); err != nil || s == "" {
				return "", false
			}
			return s, true
		}
		var next map[string]json.RawMessage
		if err := json.Unmarshal(raw, &next); err != nil {
			return "", false
		}
		cur = next
	}
	return "", false
}

// findingsRawOutputs walks .part.review.findings[] and returns, in array
// order, the .raw_output string of each finding that has one. Elements
// without .raw_output, or with a non-string .raw_output, are skipped.
//
// The OpenCode-family adapter writes the model's raw text here when the
// wrapper could not parse it as structured review JSON — this is the
// gastown-9lc recovery path.
func findingsRawOutputs(entry map[string]json.RawMessage) ([]string, bool) {
	cur := entry
	// entry["part"]
	raw, ok := cur["part"]
	if !ok {
		return nil, false
	}
	var part map[string]json.RawMessage
	if err := json.Unmarshal(raw, &part); err != nil {
		return nil, false
	}
	// part["review"]
	raw, ok = part["review"]
	if !ok {
		return nil, false
	}
	var review map[string]json.RawMessage
	if err := json.Unmarshal(raw, &review); err != nil {
		return nil, false
	}
	// review["findings"]
	raw, ok = review["findings"]
	if !ok {
		return nil, false
	}
	var findings []json.RawMessage
	if err := json.Unmarshal(raw, &findings); err != nil {
		return nil, false
	}
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		var fm map[string]json.RawMessage
		if err := json.Unmarshal(f, &fm); err != nil {
			continue
		}
		rawOut, ok := fm["raw_output"]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(rawOut, &s); err != nil || s == "" {
			continue
		}
		out = append(out, s)
	}
	return out, true
}

func extractFromPartText(entry map[string]json.RawMessage) VerdictExtraction {
	text, ok := carrierText(entry, "part", "text")
	if !ok {
		return VerdictExtraction{}
	}
	return tagSource(ExtractVerdictFromText(text), VerdictSourceLogPartText)
}

func extractFromItemText(entry map[string]json.RawMessage) VerdictExtraction {
	text, ok := carrierText(entry, "item", "text")
	if !ok {
		return VerdictExtraction{}
	}
	return tagSource(ExtractVerdictFromText(text), VerdictSourceLogItemText)
}

func extractFromResultField(entry map[string]json.RawMessage) VerdictExtraction {
	text, ok := carrierText(entry, "result")
	if !ok {
		return VerdictExtraction{}
	}
	return tagSource(ExtractVerdictFromText(text), VerdictSourceLogResultField)
}

func extractFromPartReviewRawOutput(entry map[string]json.RawMessage) VerdictExtraction {
	text, ok := carrierText(entry, "part", "review", "raw_output")
	if !ok {
		return VerdictExtraction{}
	}
	return tagSource(ExtractVerdictFromText(text), VerdictSourceLogPartReviewRawOutput)
}

// extractFromPartReviewFindingsRawOutput is the gastown-9lc fix path. The
// OpenCode-family adapter's synthetic no-parse wrapper preserves the model's
// raw text inside .part.review.findings[].raw_output (each finding carries
// its raw_output). The live gate previously queried
// .part.review.raw_output (top-level) — a field the adapter does not write
// — and so missed the recoverable verdict entirely.
//
// Returns the first finding whose raw_output yields a parseable verdict, in
// array order. The synthetic wrapper emits exactly one finding carrying the
// model's full text, so this resolves unambiguously for the bug-report case.
func extractFromPartReviewFindingsRawOutput(entry map[string]json.RawMessage) VerdictExtraction {
	texts, ok := findingsRawOutputs(entry)
	if !ok {
		return VerdictExtraction{}
	}
	for _, text := range texts {
		if v := ExtractVerdictFromText(text); v.IsSet() {
			return tagSource(v, VerdictSourceLogPartReviewFindingsRawOutput)
		}
	}
	return VerdictExtraction{}
}

// tagSource overrides the Source of an extraction with a more specific carrier
// label. Used when a JSONL carrier field forwards to the text-level extractor.
func tagSource(v VerdictExtraction, src VerdictSource) VerdictExtraction {
	if !v.IsSet() {
		return v
	}
	v.Source = src
	return v
}
