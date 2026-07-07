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
// Conflict resolution (comprehensive rewrite, gastown-wisp-wdm rejection):
//
//	The parser collects every parseable verdict from every real carrier in the
//	log, then resolves conflicts fail-closed. A known synthetic OpenCode
//	wrapper verdict is the ONLY candidate that may be discarded. After dropping
//	those wrapper-only signals:
//	  - Any real FAIL candidate overrides every PASS. A parsed FAIL is never
//	    masked by a higher-priority PASS or by a peer being unavailable.
//	  - If no real FAIL exists, the highest-priority PASS wins, with last-wins
//	    within the same priority.
//	  - If no real candidate survives, the verdict is empty (NO_VERDICT /
//	    UNAVAILABLE territory).
//
// Carrier priority order (mirrors the live gate's chained jq queries):
//  1. part.text — direct text event. Carrier can be a string OR an object
//     with a nested .text field; both shapes are accepted.
//  2. item.text — alternate text-event channel. Same dual-shape support.
//  3. .result — adapter-final structured review object. Accepts string OR
//     a JSON object with verdict/text/raw_output fields.
//  4. part.review.raw_output — wrapper's top-level raw output, if any.
//  5. part.review.findings[].raw_output — per-finding raw output. This is the
//     gastown-9lc fix: the live gate previously stopped at step 4 and missed
//     the recoverable verdict inside the synthetic wrapper's findings array.
//  6. part.review.verdict — structured reviewer's own verdict. For synthetic
//     wrappers this is a fixed FAIL wrapper signal and is dropped.
//  7. loose fallback — .verdict // .review.verdict // .raw_output // .text
//     inside the record. The .review.verdict path is treated as synthetic when
//     the entry itself is a known synthetic wrapper.
//
// Verdict-precedence invariant (gastown-wisp-cj1): for any carrier that resolves
// to a structured object, the .verdict field is the AUTHORITATIVE structured
// signal and MUST be consulted BEFORE .text. A free-form .text carrier
// (e.g. .part.text, .item.text) typically contains only .text — for those, the
// absence of .verdict causes a natural fall through to .text, preserving prior
// behavior. But for `.result`, the structured review object carries both
// .verdict and .text, and if the caller consults .text first the result can be
// misclassified: a record shaped as `{"verdict":"FAIL","text":"PASS"}` would
// otherwise yield PASS, and a record shaped as
// `{"verdict":"FAIL","text":"no verdict"}` would otherwise yield empty. Both
// failure modes mask reviewer FAILs.
//
// Regex fallback invariant (gastown-wisp-wdm): the regex fallback scans ALL
// "verdict":"PASS|FAIL" mentions in a text blob and returns the LAST parseable
// verdict (last-wins), matching extract_review_verdict_text() in the live gate.
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

	// Adapter-failure sources (ClassifyReviewerAdapterFailure).
	VerdictSourceAdapterPlanModeTrap VerdictSource = "adapter_plan_mode_trap"
	VerdictSourceAdapterNoVerdict    VerdictSource = "adapter_no_verdict"
)

// carrierPriority orders carrier fields from highest (lowest number) to lowest
// priority. When no real FAIL is present, ExtractVerdictFromJSONL returns the
// LAST verdict found at the HIGHEST priority among real PASS candidates.
type carrierPriority int

const (
	prioPartText carrierPriority = iota + 1
	prioItemText
	prioResultField
	prioPartReviewRawOutput
	prioPartReviewFindingsRawOutput
	prioPartReviewVerdict
	prioLooseFallback
	prioMissing // sentinel for "no verdict found"
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

// extractedCandidate is an internal, index-aware verdict candidate used during
// JSONL conflict resolution. It carries a Synthetic flag so that known
// OpenCode wrapper-only signals can be dropped from the real evidence set.
type extractedCandidate struct {
	Verdict     ReviewerVerdict
	Source      VerdictSource
	RecordIndex int
	Priority    carrierPriority
	Synthetic   bool
}

// fenceRe matches a fenced code block of the form ```json ... ``` or ``` ... ```.
// DOTALL is implicit through [\s\S]. We accept optional "json" / "JSON" language
// tags to match the live python regex (`r"```(?:json)?\s*(.*?)```"`).
var fenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

// verdictRe matches the case-insensitive "verdict":"PASS" or "verdict":"FAIL"
// fallback. Used only as a last resort after structured parsing fails.
var verdictRe = regexp.MustCompile(`(?i)"verdict"\s*:\s*"(PASS|FAIL)"`)

// ReviewerAdapterFailure classifies reviewer-adapter output that failed to
// produce a parseable review verdict. The durable review gate still fails
// closed on these outputs; this type only names the failure mode for telemetry.
type ReviewerAdapterFailure struct {
	Kind   ReviewerAdapterFailureKind
	Source VerdictSource
	Detail string
}

// ReviewerAdapterFailureKind is the stable telemetry reason for a failed
// reviewer adapter.
type ReviewerAdapterFailureKind string

const (
	AdapterFailureNone         ReviewerAdapterFailureKind = ""
	AdapterFailurePlanModeTrap ReviewerAdapterFailureKind = "plan_mode_trap"
	AdapterFailureNoVerdict    ReviewerAdapterFailureKind = "no_verdict"
)

// planFileWriteRe detects the plan-mode trap observed in gastown-7xq2:
// reviewer output requesting a plan-file write instead of emitting the
// review-only JSON verdict. Keep this narrow; ordinary findings can mention a
// plan without being a plan-mode harness failure.
var planFileWriteRe = regexp.MustCompile(
	`(?is)\b(?:i(?:'ll| will| need to| should)|let me|we(?:'ll| will| need to))\s+(?:now\s+)?(?:write|update|create|edit)\s+(?:a\s+|the\s+|this\s+)?(?:review\s+)?plan(?:[-_ ]?file)?\b` +
		`|\b(?:write|update|create|edit)\s+(?:a\s+|the\s+|this\s+)?(?:review\s+)?plan[-_ ]?file\b` +
		`|(?:"name"\s*:\s*"(?:Write|Edit|MultiEdit)"[\s\S]{0,1000}"file_path"\s*:\s*"[^"]*\bplan[-_a-z0-9]*\.(?:md|txt)")` +
		`|(?:\b(?:Write|Edit|MultiEdit)\b[^\n]{0,400}\bplan[-_a-z0-9]*\.(?:md|txt)\b)`,
)

// toolCallPlanningRe detects tool-call planning output, not ordinary prose
// containing the word "task". TodoWrite is distinctive enough to match as a
// bare tool name; Task is only treated as planning when it appears as a JSON
// tool-call name or as "Task tool" prose.
var toolCallPlanningRe = regexp.MustCompile(
	`(?is)"name"\s*:\s*"(?:TodoWrite|Task|TaskCreate|TaskUpdate|TaskList)"` +
		`|(?:^|\W)(?:TodoWrite|TaskCreate|TaskUpdate|TaskList)\b` +
		`|(?:^|\W)Task\s+tool\b`,
)

// ClassifyReviewerAdapterFailure inspects raw reviewer-adapter output after the
// caller has failed to extract a verdict. It never reclassifies a parseable
// PASS/FAIL as an adapter failure; parse-first is the safety property.
func ClassifyReviewerAdapterFailure(text string) ReviewerAdapterFailure {
	if ExtractVerdictFromText(text).IsSet() {
		return ReviewerAdapterFailure{}
	}
	if strings.TrimSpace(text) == "" {
		return ReviewerAdapterFailure{}
	}
	if planFileWriteRe.MatchString(text) {
		return ReviewerAdapterFailure{
			Kind:   AdapterFailurePlanModeTrap,
			Source: VerdictSourceAdapterPlanModeTrap,
			Detail: "reviewer requested a plan-file write instead of a review-only JSON verdict",
		}
	}
	if toolCallPlanningRe.MatchString(text) {
		return ReviewerAdapterFailure{
			Kind:   AdapterFailurePlanModeTrap,
			Source: VerdictSourceAdapterPlanModeTrap,
			Detail: "reviewer emitted tool-call planning instead of a review-only JSON verdict",
		}
	}
	return ReviewerAdapterFailure{
		Kind:   AdapterFailureNoVerdict,
		Source: VerdictSourceAdapterNoVerdict,
		Detail: "reviewer adapter produced no parseable {verdict: PASS|FAIL} payload",
	}
}

// IsSet reports whether a known adapter-failure kind was classified.
func (f ReviewerAdapterFailure) IsSet() bool { return f.Kind != AdapterFailureNone }

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
	// single layer of quoting). This consumes the whole text, so no later-wins
	// conflict is possible.
	plain := strings.ToUpper(strings.Trim(strings.TrimSpace(text), "\"'`"))
	if plain == string(ReviewerVerdictPass) || plain == string(ReviewerVerdictFail) {
		return VerdictExtraction{Verdict: ReviewerVerdict(plain), Source: VerdictSourcePlainToken}
	}

	// Step 2: whole text is a single JSON object.
	if v, ok := decodeVerdictFromJSON(text); ok {
		return VerdictExtraction{Verdict: v, Source: VerdictSourceJSONObject}
	}

	// Steps 3-5: fenced code blocks, brace-matched JSON scans, and the regex
	// fallback may all produce parseable verdicts. The parser's recovery
	// contract is last-wins: the verdict that appears LATEST in the text wins.
	// (gastown-wisp-2ds regression: a fenced PASS followed by an unfenced
	// {"verdict":"FAIL"} must return FAIL.)
	var best VerdictExtraction
	var bestEnd int

	// Step 3: fenced ```json ... ``` blocks.
	if matches := fenceRe.FindAllStringSubmatchIndex(text, -1); matches != nil {
		for _, m := range matches {
			if len(m) < 4 {
				continue
			}
			body := strings.TrimSpace(text[m[2]:m[3]])
			if v, ok := decodeVerdictFromJSON(body); ok && m[1] > bestEnd {
				best = VerdictExtraction{Verdict: v, Source: VerdictSourceFencedCodeBlock}
				bestEnd = m[1]
			}
		}
	}

	// Step 4: brace-matched JSON scan. Walks the string tracking brace depth
	// and string-state, then tries to parse every balanced {...} slice.
	for _, sub := range extractBraceBalancedJSON(text) {
		if v, ok := decodeVerdictFromJSON(sub.body); ok && sub.end > bestEnd {
			best = VerdictExtraction{Verdict: v, Source: VerdictSourceBraceMatchedJSON}
			bestEnd = sub.end
		}
	}

	// Step 5: regex fallback for hand-written or partially-escaped verdicts.
	// Scan ALL matches; the last match is naturally late-wins because its end
	// position is compared against previous candidates.
	if matches := verdictRe.FindAllStringSubmatchIndex(text, -1); matches != nil {
		for _, m := range matches {
			if len(m) < 4 {
				continue
			}
			last := strings.ToUpper(text[m[2]:m[3]])
			switch last {
			case string(ReviewerVerdictPass), string(ReviewerVerdictFail):
				if m[1] > bestEnd {
					best = VerdictExtraction{Verdict: ReviewerVerdict(last), Source: VerdictSourceVerdictRegex}
					bestEnd = m[1]
				}
			}
		}
	}

	return best
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
// balancedJSON carries a brace-balanced substring along with the index of
// the closing brace so callers can compare source positions for last-wins.
type balancedJSON struct {
	body string
	end  int
}

func extractBraceBalancedJSON(text string) []balancedJSON {
	var out []balancedJSON
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
				out = append(out, balancedJSON{body: text[start : i+1], end: i + 1})
				start = -1
			}
		}
	}
	return out
}

// ExtractVerdictFromJSONL scans a JSONL log one record at a time and returns
// the resolved verdict after fail-closed conflict resolution across all real
// carriers. It is the Go mirror of review_verdict() in refinery-gate.sh.
//
// Semantics:
//   - All records are parsed (malformed lines are silently skipped, matching
//     the live gate's jq behavior). Non-I/O parse failures are not surfaced.
//   - Every well-known carrier is consulted on every record. Known synthetic
//     OpenCode wrapper verdicts are identified per-entry and dropped from the
//     real evidence set.
//   - After dropping synthetic-only signals:
//   - Any real FAIL candidate wins over any real PASS candidate.
//   - With no real FAIL, the highest-priority real PASS wins; within the
//     same priority, the latest record wins (last-wins).
//   - Returns (VerdictExtraction{}, nil) when no real candidate survives.
//     A non-nil error is returned only for I/O failures.
//
// Returns (VerdictExtraction{}, nil) when no record yields a verdict. A
// non-nil error is returned only for I/O failures.
func ExtractVerdictFromJSONL(r io.Reader) (VerdictExtraction, error) {
	scanner := bufio.NewScanner(r)
	// Allow large lines: opencode sessions can carry 100k+ token transcripts.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var entries []map[string]json.RawMessage
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
	}
	if err := scanner.Err(); err != nil {
		return VerdictExtraction{}, err
	}

	// Collect every candidate from every entry, tagging wrapper-only signals
	// as Synthetic so they can be dropped before conflict resolution.
	var all []extractedCandidate
	for i, entry := range entries {
		synthetic := isSyntheticReviewResult(entry)
		cands := carrierCandidatesForEntry(entry)
		for _, c := range cands {
			cand := extractedCandidate{
				Verdict:     c.extraction.Verdict,
				Source:      c.extraction.Source,
				RecordIndex: i,
				Priority:    priorityOfSource(c.extraction.Source),
			}
			// The only synthetic-only verdict signal we drop is the wrapper's
			// own hard-coded .part.review.verdict (and the loose-fallback
			// .review.verdict path that points at the same field).
			if synthetic {
				switch cand.Source {
				case VerdictSourceLogPartReviewVerdict:
					cand.Synthetic = true
				case VerdictSourceLogLooseFallback:
					cand.Synthetic = c.looseKind == looseReviewVerdict
				}
			}
			all = append(all, cand)
		}
	}

	if len(all) == 0 {
		return VerdictExtraction{}, nil
	}

	// Drop all known synthetic-wrapper-only signals.
	var real []extractedCandidate
	for _, c := range all {
		if !c.Synthetic {
			real = append(real, c)
		}
	}

	// Fail-closed: any real FAIL overrides every PASS, regardless of priority.
	var fails []extractedCandidate
	for _, c := range real {
		if c.Verdict == ReviewerVerdictFail {
			fails = append(fails, c)
		}
	}
	if len(fails) > 0 {
		best := bestCandidateByPriorityLastWins(fails)
		return VerdictExtraction{Verdict: ReviewerVerdictFail, Source: best.Source}, nil
	}

	// No real FAIL: choose the highest-priority PASS, last-wins within priority.
	var passes []extractedCandidate
	for _, c := range real {
		if c.Verdict == ReviewerVerdictPass {
			passes = append(passes, c)
		}
	}
	if len(passes) > 0 {
		best := bestCandidateByPriorityLastWins(passes)
		return VerdictExtraction{Verdict: ReviewerVerdictPass, Source: best.Source}, nil
	}

	return VerdictExtraction{}, nil
}

// bestCandidateByPriorityLastWins returns the highest-priority candidate,
// breaking ties by latest record index (last-wins). It assumes the input slice
// is non-empty.
func bestCandidateByPriorityLastWins(cands []extractedCandidate) extractedCandidate {
	best := cands[0]
	for _, c := range cands[1:] {
		if c.Priority < best.Priority ||
			(c.Priority == best.Priority && c.RecordIndex > best.RecordIndex) {
			best = c
		}
	}
	return best
}

// looseFallbackKind identifies which sub-path of extractFromLooseFallback
// produced a candidate. This is used only internally so the synthetic-wrapper
// detector can drop ONLY the .review.verdict shape (the wrapper's own verdict)
// and leave top-level .verdict / .raw_output / .text as real evidence.
type looseFallbackKind int

const (
	looseUnknown looseFallbackKind = iota
	looseTopLevelVerdict
	looseReviewVerdict
	looseRawOutput
	looseText
)

// candidateWithKind pairs a verdict extraction with the loose-fallback
// sub-path that produced it (if any). This lets synthetic-wrapper detection
// drop ONLY the wrapper's .review.verdict signal while keeping other loose
// fallback shapes real.
type candidateWithKind struct {
	extraction VerdictExtraction
	looseKind  looseFallbackKind
}

// carrierCandidatesForEntry returns every non-empty verdict that can be
// extracted from a single JSONL entry, in arbitrary order. Each candidate
// carries its source label so priorityOfSource can rank them, and the
// loose-fallback sub-path is preserved for precise synthetic detection. The
// synthetic status is assigned by the caller after inspecting the whole entry.
func carrierCandidatesForEntry(entry map[string]json.RawMessage) []candidateWithKind {
	var out []candidateWithKind
	if v := extractFromPartText(entry); v.IsSet() {
		out = append(out, candidateWithKind{extraction: v})
	}
	if v := extractFromItemText(entry); v.IsSet() {
		out = append(out, candidateWithKind{extraction: v})
	}
	if v := extractFromResultField(entry); v.IsSet() {
		out = append(out, candidateWithKind{extraction: v})
	}
	if v := extractFromPartReviewRawOutput(entry); v.IsSet() {
		out = append(out, candidateWithKind{extraction: v})
	}
	if v := extractFromPartReviewFindingsRawOutput(entry); v.IsSet() {
		out = append(out, candidateWithKind{extraction: v})
	}
	if v := extractFromPartReviewVerdict(entry); v.IsSet() {
		out = append(out, candidateWithKind{extraction: v})
	}
	if v, kind := extractFromLooseFallback(entry); v.IsSet() {
		out = append(out, candidateWithKind{extraction: v, looseKind: kind})
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
// A synthetic entry's part.review.verdict is a fixed FAIL wrapper signal
// (not the model's real verdict), so it is dropped from the real evidence set.
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
// helper for the synthetic detector and other path-only readers.
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
// both shapes).
//
// For object leaves, the structured verdict (.verdict) is consulted FIRST
// so a `{"verdict":"FAIL","text":"PASS"}` object cannot mask a FAIL verdict.
func extractFromPartText(entry map[string]json.RawMessage) VerdictExtraction {
	text, ok := carrierTextOrNested(entry, "part", "text")
	if !ok {
		return VerdictExtraction{}
	}
	return tagSource(ExtractVerdictFromText(text), VerdictSourceLogPartText)
}

// extractFromItemText reads .item.text — same dual-shape support as
// extractFromPartText.
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
// misclassified as PASS via its free-form .text carrier.
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

// extractFromPartReviewVerdict reads .part.review.verdict. For synthetic
// wrappers this is a hard-coded FAIL wrapper signal; ExtractVerdictFromJSONL
// marks it Synthetic and drops it. For non-synthetic entries it is the
// authoritative reviewer verdict (a literal "PASS" or "FAIL" string).
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

// extractFromLooseFallback handles adapters that emit their review as a
// structured object at the record's top level. It walks
// .verdict → .review.verdict → .raw_output → .text in priority order,
// mirroring the live gate's `.verdict // .review.verdict // .raw_output //
// .text` chain. Verdict precedence is correct: .verdict is consulted FIRST,
// so a structured FAIL is never masked by a misleading free-form .text carrier.
//
// The returned looseFallbackKind lets the caller distinguish which sub-path
// matched, so synthetic-wrapper detection can drop only the .review.verdict
// shape (the wrapper's own verdict) and keep top-level .verdict /.raw_output
// /.text as real evidence.
func extractFromLooseFallback(entry map[string]json.RawMessage) (VerdictExtraction, looseFallbackKind) {
	// 1. .verdict
	if v, ok := reviewString(entry, "verdict"); ok {
		if up := strings.ToUpper(strings.TrimSpace(v)); up == string(ReviewerVerdictPass) || up == string(ReviewerVerdictFail) {
			return VerdictExtraction{Verdict: ReviewerVerdict(up), Source: VerdictSourceLogLooseFallback}, looseTopLevelVerdict
		}
	}
	// 2. .review.verdict
	if review, ok := nestedMap(entry, "review"); ok {
		if v, ok := reviewString(review, "verdict"); ok {
			if up := strings.ToUpper(strings.TrimSpace(v)); up == string(ReviewerVerdictPass) || up == string(ReviewerVerdictFail) {
				return VerdictExtraction{Verdict: ReviewerVerdict(up), Source: VerdictSourceLogLooseFallback}, looseReviewVerdict
			}
		}
	}
	// 3. .raw_output
	if v, ok := reviewString(entry, "raw_output"); ok {
		return tagSource(ExtractVerdictFromText(v), VerdictSourceLogLooseFallback), looseRawOutput
	}
	// 4. .text
	if v, ok := reviewString(entry, "text"); ok {
		return tagSource(ExtractVerdictFromText(v), VerdictSourceLogLooseFallback), looseText
	}
	return VerdictExtraction{}, looseUnknown
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
// precedence, an entry shaped as `{"verdict":"FAIL","text":"PASS"}` would be
// misclassified as PASS (text wins, masks the FAIL), and an entry shaped as
// `{"verdict":"FAIL","text":"no verdict"}` would yield empty (text unparseable,
// .verdict never consulted, FAIL dropped).
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
