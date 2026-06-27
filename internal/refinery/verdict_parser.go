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
// Live-gate priority parity: ExtractVerdictFromJSONL scans the WHOLE log and
// returns the LAST verdict found at the HIGHEST carrier priority. This mirrors
// `extract_review_verdict_text()` reading stdin end-to-end and returning the
// last parsed verdict, combined with the live gate's `jq ... | tail -1` (or
// equivalent last-wins concatenation) for each priority chain. The carrier
// priority order matches the live gate's chained `jq` queries:
//  1. part.text — direct text event. Carrier can be a string OR an object
//     with a nested .text field; both shapes are accepted.
//  2. item.text — alternate text-event channel. Same dual-shape support.
//  3. .result — adapter-final structured review object. Accepts string OR
//     a JSON object with verdict/text/raw_output fields.
//  4. part.review.raw_output — wrapper's top-level raw output, if any.
//  5. (synthetic review_result only) part.review.findings[].raw_output —
//     OpenCode wrapper's per-finding raw output. This is the gastown-9lc
//     fix: previously the live gate stopped at step 4 and never checked the
//     per-finding array, so a model PASS inside a synthetic-FAIL wrapper
//     was recorded as UNAVAILABLE.
//  6. (non-synthetic review_result only) part.review.verdict — structured
//     reviewer's own verdict. Skipped for synthetic wrappers because their
//     verdict is a fixed FAIL wrapper signal that masks the real model text.
//  7. (non-synthetic review_result only) loose fallback — looks for
//     .verdict / .review.verdict / .raw_output / .text inside the record.
//
// Verdict-precedence invariant (gastown-wisp-cj1 finding): for any carrier
// that resolves to a structured object, the .verdict field is the
// AUTHORITATIVE structured signal and MUST be consulted BEFORE .text. A
// free-form .text carrier (e.g. .part.text, .item.text) typically contains
// only .text — for those, the absence of .verdict causes a natural fall
// through to .text, preserving prior behavior. But for `.result`, the
// structured review object carries both .verdict and .text, and if the
// caller consults .text first the result can be misclassified: a record
// shaped as `{"verdict":"FAIL","text":"PASS"}` would otherwise yield PASS,
// and a record shaped as `{"verdict":"FAIL","text":"no verdict"}` would
// otherwise yield empty. Both failure modes mask reviewer FAILs.
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
	VerdictSourceLogPartReviewVerdict           VerdictSource = "log_part_review_verdict"
	VerdictSourceLogLooseFallback               VerdictSource = "log_loose_fallback"
)

// carrierPriority orders carrier fields from highest (lowest number) to lowest
// priority. ExtractVerdictFromJSONL scans the WHOLE log and returns the LAST
// verdict found at the HIGHEST priority — i.e., higher priority wins, and
// within the same priority the LAST record wins. This matches the live
// gate's behavior: jq pipelines emit all matching values across all records,
// piped through extract_review_verdict_text() which keeps the last parsed
// verdict; the outer bash `if v empty then try next priority` chain picks
// the first non-empty priority.
type carrierPriority int

const (
	prioPartText carrierPriority = iota + 1
	prioItemText
	prioResultField
	prioPartReviewRawOutput
	prioPartReviewFindingsRawOutput // only meaningful when any record is synthetic
	prioPartReviewVerdict           // only meaningful when NO record is synthetic
	prioLooseFallback               // only meaningful when NO record is synthetic
	prioMissing                     // sentinel for "no verdict found" — higher than any real priority
)

func priorityOfSource(s VerdictSource) carrierPriority {
	switch s {
	case VerdictSourceLogPartText:
		return prioPartText
	case VerdictSourceLogItemText:
		return prioItemText
	case VerdictSourceLogResultField:
		return prioResultField
	case VerdictSourceLogPartReviewRawOutput:
		return prioPartReviewRawOutput
	case VerdictSourceLogPartReviewFindingsRawOutput:
		return prioPartReviewFindingsRawOutput
	case VerdictSourceLogPartReviewVerdict:
		return prioPartReviewVerdict
	case VerdictSourceLogLooseFallback:
		return prioLooseFallback
	}
	return prioMissing
}

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
// the LAST verdict found at the HIGHEST carrier priority across the whole log.
// It is the Go mirror of review_verdict() in refinery-gate.sh.
//
// Semantics:
//   - All records are parsed (malformed lines are silently skipped, matching
//     the live gate's jq behavior). Non-I/O parse failures are not surfaced.
//   - The log is scanned in two logical passes: first to compute
//     syntheticReviewAny (any record is a synthetic OpenCode-style wrapper),
//     then to find the highest-priority verdict with last-wins within priority.
//   - The highest carrier priority that yields any verdict wins. Within a
//     single priority, the LAST record carrying a verdict wins. This mirrors
//     `extract_review_verdict_text()` reading stdin end-to-end and returning
//     the last parsed verdict, combined with the live gate's per-priority
//     `jq ... | tail -1` last-wins chaining.
//
// Returns (VerdictExtraction{}, nil) when no record yields a verdict. A
// non-nil error is returned only for I/O failures.
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

	var entries []map[string]json.RawMessage
	syntheticAny := false
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
		entries = append(entries, entry)
		if isSyntheticReviewResult(entry) {
			syntheticAny = true
		}
	}
	if err := scanner.Err(); err != nil {
		return VerdictExtraction{}, err
	}

	// Second pass: find the highest-priority verdict with last-wins within
	// priority. We track (priority, recordIndex, verdict) so that a higher
	// priority always overrides a lower one, and within the same priority a
	// later record overrides an earlier one.
	var best VerdictExtraction
	bestPrio := prioMissing
	bestIdx := -1
	for i, entry := range entries {
		cands := carrierCandidatesForEntry(entry, syntheticAny)
		for _, c := range cands {
			p := priorityOfSource(c.Source)
			if p == prioMissing {
				continue
			}
			// Lower priority value = higher priority. Strictly-better
			// priority always wins; equal priority takes the later record.
			if p < bestPrio || (p == bestPrio && i > bestIdx) {
				bestPrio = p
				bestIdx = i
				best = c
			}
		}
	}
	return best, nil
}

// carrierCandidatesForEntry returns every non-empty verdict that can be
// extracted from a single JSONL entry, in arbitrary order. Each candidate
// carries its source label so priorityOfSource can rank them.
//
// The syntheticAny flag mirrors the live gate's `synthetic_no_json` one-shot
// decision: if any record in the log is synthetic, only the
// findings[].raw_output path is consulted (the live gate skips
// part.review.verdict for synthetic logs); if NO record is synthetic, the
// findings[] path is skipped and part.review.verdict + the loose fallback
// are consulted instead.
func carrierCandidatesForEntry(entry map[string]json.RawMessage, syntheticAny bool) []VerdictExtraction {
	var out []VerdictExtraction
	if v := extractFromPartText(entry); v.IsSet() {
		out = append(out, v)
	}
	if v := extractFromItemText(entry); v.IsSet() {
		out = append(out, v)
	}
	if v := extractFromResultField(entry); v.IsSet() {
		out = append(out, v)
	}
	if v := extractFromPartReviewRawOutput(entry); v.IsSet() {
		out = append(out, v)
	}
	if syntheticAny {
		if v := extractFromPartReviewFindingsRawOutput(entry); v.IsSet() {
			out = append(out, v)
		}
	} else {
		if v := extractFromPartReviewVerdict(entry); v.IsSet() {
			out = append(out, v)
		}
		if v := extractFromLooseFallback(entry); v.IsSet() {
			out = append(out, v)
		}
	}
	return out
}

// isSyntheticReviewResult reports whether entry carries an OpenCode-family
// synthetic no-parse wrapper. The live gate detects this by ANY of:
//   - part.review.adapter_error == "no_parseable_review_json"
//   - part.review.summary matches the canonical OpenCode failure summary
//   - any of part.review.findings[].explanation matches the canonical
//     OpenCode failure explanation
//
// A synthetic record's part.review.verdict is a fixed FAIL wrapper signal
// (not the model's real verdict), so the part.review.verdict / loose-fallback
// paths are skipped for synthetic logs to avoid masking the real model text
// that lives inside findings[].raw_output.
func isSyntheticReviewResult(entry map[string]json.RawMessage) bool {
	const (
		syntheticAdapterError = "no_parseable_review_json"
		syntheticSummary      = "OpenCode review output was normalized to FAIL because it did not contain parseable {verdict, findings, summary} JSON."
		syntheticExplanation  = "OpenCode did not return parseable review JSON."
	)

	review, ok := nestedMap(entry, "part", "review")
	if !ok {
		return false
	}
	if errStr, ok := reviewString(review, "adapter_error"); ok && errStr == syntheticAdapterError {
		return true
	}
	if sumStr, ok := reviewString(review, "summary"); ok && sumStr == syntheticSummary {
		return true
	}
	if findingsRaw, ok := review["findings"]; ok {
		var findings []json.RawMessage
		if err := json.Unmarshal(findingsRaw, &findings); err == nil {
			for _, f := range findings {
				var fm map[string]json.RawMessage
				if err := json.Unmarshal(f, &fm); err != nil {
					continue
				}
				if explStr, ok := reviewString(fm, "explanation"); ok && explStr == syntheticExplanation {
					return true
				}
			}
		}
	}
	return false
}

// nestedMap returns the map at the given nested key path. It is a small
// helper for the synthetic detector and other path-only readers; it does NOT
// surface leaf strings (callers should use carrierTextOrNested for that,
// which also handles string-vs-object carriers with verdict-precedence).
func nestedMap(entry map[string]json.RawMessage, path ...string) (map[string]json.RawMessage, bool) {
	cur := entry
	for _, key := range path {
		raw, ok := cur[key]
		if !ok {
			return nil, false
		}
		var next map[string]json.RawMessage
		if err := json.Unmarshal(raw, &next); err != nil {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// reviewString returns a string-typed field from a map. It is a thin shim
// over json.Unmarshal that treats missing keys, non-string values, and
// empty strings uniformly as "not present".
func reviewString(m map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := m[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || s == "" {
		return "", false
	}
	return s, true
}

// extractFromPartText reads .part.text — accepts BOTH a plain string and an
// object whose .text field carries the actual text (the live gate supports
// both shapes: `((.part.text? | type)=="string") then .part.text elif
// ((.part.text? | type)=="object") then (.part.text.text? // empty)`).
//
// For object leaves, the structured verdict (.verdict) is consulted FIRST
// so a `{"verdict":"FAIL","text":"PASS"}` object cannot mask a FAIL verdict
// by being misclassified as PASS via its free-form text carrier.
func extractFromPartText(entry map[string]json.RawMessage) VerdictExtraction {
	text, ok := carrierTextOrNested(entry, "part", "text")
	if !ok {
		return VerdictExtraction{}
	}
	return tagSource(ExtractVerdictFromText(text), VerdictSourceLogPartText)
}

// extractFromItemText reads .item.text — same dual-shape support as
// extractFromPartText (string OR object with nested .text). Verdict
// precedence on object leaves is enforced by carrierTextOrNested.
func extractFromItemText(entry map[string]json.RawMessage) VerdictExtraction {
	text, ok := carrierTextOrNested(entry, "item", "text")
	if !ok {
		return VerdictExtraction{}
	}
	return tagSource(ExtractVerdictFromText(text), VerdictSourceLogItemText)
}

// extractFromResultField reads .result — accepts a plain string OR a
// structured JSON object. The structured object's .verdict is consulted
// FIRST so an authoritative `{"verdict":"FAIL","text":"PASS"}` cannot be
// misclassified as PASS via its free-form .text carrier; the .text field
// is consulted only if .verdict is absent or unparseable.
func extractFromResultField(entry map[string]json.RawMessage) VerdictExtraction {
	text, ok := carrierTextOrNested(entry, "result")
	if !ok {
		return VerdictExtraction{}
	}
	return tagSource(ExtractVerdictFromText(text), VerdictSourceLogResultField)
}

func extractFromPartReviewRawOutput(entry map[string]json.RawMessage) VerdictExtraction {
	text, ok := carrierTextOrNested(entry, "part", "review", "raw_output")
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
// Returns the LAST finding whose raw_output yields a parseable verdict, in
// array order. The synthetic wrapper emits exactly one finding carrying the
// model's full text, so this resolves unambiguously for the bug-report case;
// the LAST-wins rule mirrors extract_review_verdict_text() reading the
// concatenated jq output end-to-end and returning the last parsed verdict.
func extractFromPartReviewFindingsRawOutput(entry map[string]json.RawMessage) VerdictExtraction {
	texts, ok := findingsRawOutputs(entry)
	if !ok {
		return VerdictExtraction{}
	}
	var last VerdictExtraction
	for _, text := range texts {
		if v := ExtractVerdictFromText(text); v.IsSet() {
			last = tagSource(v, VerdictSourceLogPartReviewFindingsRawOutput)
		}
	}
	return last
}

// extractFromPartReviewVerdict reads .part.review.verdict for NON-synthetic
// review_result entries. The live gate intentionally skips this path when
// any synthetic record is present: synthetic wrappers hard-code verdict=FAIL
// as a wrapper signal, and the real model verdict is preserved inside
// findings[].raw_output (see extractFromPartReviewFindingsRawOutput).
//
// For non-synthetic entries, .part.review.verdict is the authoritative
// reviewer verdict (a literal "PASS" or "FAIL" string), so we honor it
// directly without re-parsing. Any non-PASS/FAIL value yields empty —
// fail-closed.
func extractFromPartReviewVerdict(entry map[string]json.RawMessage) VerdictExtraction {
	verdict, ok := carrierTextOrNested(entry, "part", "review", "verdict")
	if !ok {
		return VerdictExtraction{}
	}
	up := strings.ToUpper(strings.TrimSpace(verdict))
	switch up {
	case string(ReviewerVerdictPass), string(ReviewerVerdictFail):
		return VerdictExtraction{Verdict: ReviewerVerdict(up), Source: VerdictSourceLogPartReviewVerdict}
	}
	return VerdictExtraction{}
}

// extractFromLooseFallback handles non-synthetic adapters that emit their
// review as a structured object at the record's top level. It walks
// .verdict → .review.verdict → .raw_output → .text in priority order,
// mirroring the live gate's `.verdict // .review.verdict // .raw_output //
// .text` chain. The live gate applies this to the adapter OUTPUT, not the
// raw_log_path JSONL; the Go mirror applies it per-record on the JSONL
// because some adapters that emit `.raw_log_path` ALSO emit a structured
// review object inline in earlier records.
//
// Verdict precedence is correct here: .verdict is consulted FIRST, so a
// structured FAIL is never masked by a misleading free-form .text carrier.
func extractFromLooseFallback(entry map[string]json.RawMessage) VerdictExtraction {
	// 1. .verdict
	if v, ok := reviewString(entry, "verdict"); ok {
		if up := strings.ToUpper(strings.TrimSpace(v)); up == string(ReviewerVerdictPass) || up == string(ReviewerVerdictFail) {
			return VerdictExtraction{Verdict: ReviewerVerdict(up), Source: VerdictSourceLogLooseFallback}
		}
	}
	// 2. .review.verdict
	if review, ok := nestedMap(entry, "review"); ok {
		if v, ok := reviewString(review, "verdict"); ok {
			if up := strings.ToUpper(strings.TrimSpace(v)); up == string(ReviewerVerdictPass) || up == string(ReviewerVerdictFail) {
				return VerdictExtraction{Verdict: ReviewerVerdict(up), Source: VerdictSourceLogLooseFallback}
			}
		}
	}
	// 3. .raw_output
	if v, ok := reviewString(entry, "raw_output"); ok {
		return tagSource(ExtractVerdictFromText(v), VerdictSourceLogLooseFallback)
	}
	// 4. .text
	if v, ok := reviewString(entry, "text"); ok {
		return tagSource(ExtractVerdictFromText(v), VerdictSourceLogLooseFallback)
	}
	return VerdictExtraction{}
}

// carrierTextOrNested extracts a string carrier from a nested path. The leaf
// can be EITHER:
//   - a JSON string (taken as-is); or
//   - a JSON object, in which case the carrier walks .verdict → .text →
//     .raw_output in that order (mirroring the live gate's `.verdict //
//     .text // .raw_output` fallback chain applied to object leaves).
//
// VERDICT PRECEDENCE INVARIANT (gastown-wisp-cj1): for object leaves,
// .verdict is consulted FIRST. This is the authoritative structured signal —
// if it parses to PASS or FAIL, that verdict is honored even if the
// free-form .text carrier disagrees or is unparseable. Without this
// precedence, an entry shaped as `{"verdict":"FAIL","text":"PASS"}` would
// be misclassified as PASS (text wins, masks the FAIL), and an entry shaped
// as `{"verdict":"FAIL","text":"no verdict"}` would yield empty (text
// unparseable, .verdict never consulted, FAIL dropped).
//
// Returns ("", false) on any miss — missing key, wrong type, empty string,
// no usable field inside an object carrier. Callers pipe the result
// through ExtractVerdictFromText.
func carrierTextOrNested(entry map[string]json.RawMessage, path ...string) (string, bool) {
	leaf, ok := nestedRaw(entry, path...)
	if !ok {
		return "", false
	}
	// String leaf — return as-is.
	var s string
	if err := json.Unmarshal(leaf, &s); err == nil {
		if s == "" {
			return "", false
		}
		return s, true
	}
	// Object leaf — walk .verdict / .text / .raw_output with VERDICT FIRST.
	// (gastown-wisp-cj1 fix: previously this walked .text first, which let
	// a free-form .text carrier mask an authoritative .verdict.)
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(leaf, &obj); err != nil {
		return "", false
	}
	if v, ok := reviewString(obj, "verdict"); ok {
		return v, true
	}
	if v, ok := reviewString(obj, "text"); ok {
		return v, true
	}
	if v, ok := reviewString(obj, "raw_output"); ok {
		return v, true
	}
	return "", false
}

// nestedRaw returns the json.RawMessage at the given nested key path. It
// returns (nil, false) on any missing key or non-object intermediate node.
// This is the path-walking primitive for both carrierTextOrNested (string
// or object leaves) and the strict-string carrierText helper kept for
// backwards-compatibility with the original review.go-style callers.
func nestedRaw(entry map[string]json.RawMessage, path ...string) (json.RawMessage, bool) {
	cur := entry
	for i, key := range path {
		raw, ok := cur[key]
		if !ok {
			return nil, false
		}
		if i == len(path)-1 {
			return raw, true
		}
		var next map[string]json.RawMessage
		if err := json.Unmarshal(raw, &next); err != nil {
			return nil, false
		}
		cur = next
	}
	return nil, false
}

// findingsRawOutputs returns every .raw_output string from
// .part.review.findings[]. The list is in array order; the LAST parseable
// verdict wins when extractFromPartReviewFindingsRawOutput iterates it.
func findingsRawOutputs(entry map[string]json.RawMessage) ([]string, bool) {
	review, ok := nestedMap(entry, "part", "review")
	if !ok {
		return nil, false
	}
	raw, ok := review["findings"]
	if !ok {
		return nil, false
	}
	var findings []json.RawMessage
	if err := json.Unmarshal(raw, &findings); err != nil {
		return nil, false
	}
	var out []string
	for _, f := range findings {
		var fm map[string]json.RawMessage
		if err := json.Unmarshal(f, &fm); err != nil {
			continue
		}
		v, ok := reviewString(fm, "raw_output")
		if !ok {
			continue
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// tagSource wraps a VerdictExtraction's source label.
func tagSource(v VerdictExtraction, s VerdictSource) VerdictExtraction {
	v.Source = s
	return v
}
