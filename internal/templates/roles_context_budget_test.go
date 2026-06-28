package templates

import (
	"os"
	"strings"
	"testing"
)

// This file is the test/dry-run harness for gastown-cet.6.3:
// "Small-prompt/context budget rules for GLM Mayor".
//
// Goal: prove the GLM-safe context budget rules are encoded in the Mayor and
// Witness role templates, that the rule packets remain bounded (a rendered
// packet does not balloon past the small-prompt budget the rules themselves
// prescribe), and that the rules survive template rendering with a non-default
// TownRoot (the same anti-hardcoding invariant the rest of the template tests
// enforce).
//
// The rules were learned from the GLM Witness rollout and the report-audit
// review rounds: umans-glm-5.2 has a smaller effective context window than
// Claude/Codex defaults and degrades before the hard context limit, so long
// prompts, unbounded tool output, and rework appended to fat sessions caused
// mid-task stalls and repeated bad resubmissions. These tests pin the encoded
// remediation so it cannot silently regress.

// glmContextBudgetRules is the canonical set of GLM-safe context budget rules
// that the Mayor template must encode. Each entry is a short, distinct
// marker phrase lifted from the rule wording; a marker present in the rendered
// output means the rule is reachable to a GLM-routed Mayor.
//
// Order is not load-bearing — it mirrors the rule numbering in the template
// so a failure points at the missing rule.
var glmContextBudgetRules = []string{
	"Small prompts first",                 // rule 1: small prompts, bounded excerpts
	"Bound tool-output ingestion",         // rule 2: drop full tool output
	"Bounded status summaries",            // rule 3: bounded high-volume reports
	"Durable summaries before compaction", // rule 4: durable handoff/notes before compaction
	"Fresh-context packets for rework",    // rule 5: rework = fresh session, not append
	"Handoff hygiene",                     // rule 6: bounded handoff packets
}

// mayorBudgetMustHaves are the load-bearing tokens that make the GLM budget
// section concrete and actionable (not just a heading). Each maps to a
// concrete behavior a reviewer can check the template actually tells the
// Mayor to do.
var mayorBudgetMustHaves = []string{
	"umans-glm-5.2",       // names the model the rules apply to
	"GT_MQ_REWORK_BOUNCE", // names the rework-fresh-session pattern
	"smaller",             // smaller effective context window
	"bd update --notes",   // durable notes before compaction
	"4000 tokens",         // concrete prompt-budget ceiling
}

// witnessBudgetMustHaves are the load-bearing tokens for the Witness GLM
// budget section. The Witness section specializes the same rules for the
// patrol lifecycle.
var witnessBudgetMustHaves = []string{
	"umans-glm-5.2",
	"handoff --cycle --reason compaction --yes", // the PreCompact fix that motivated the rules
	"GT_MQ_REWORK_BOUNCE",
	"bd update --notes",
}

// glmBudgetRenderData returns RoleData for the mayor template using a
// non-default TownRoot, so the budget test also exercises the anti-hardcoding
// invariant (rules must render under a custom instance root).
func glmBudgetRenderData() RoleData {
	return RoleData{
		Role:          "mayor",
		TownRoot:      "/custom/glm-instance",
		TownName:      "glm-instance",
		WorkDir:       "/custom/glm-instance",
		DefaultBranch: "main",
		MayorSession:  "gt-glm-instance-mayor",
		DeaconSession: "gt-glm-instance-deacon",
	}
}

// TestRenderRole_Mayor_GLMSafeContextBudgetRules asserts the Mayor template
// encodes every GLM-safe context budget rule and the concrete tokens that make
// each rule actionable. A missing marker means a rule was dropped or weakened
// during editing.
func TestRenderRole_Mayor_GLMSafeContextBudgetRules(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	output, err := tmpl.RenderRole("mayor", glmBudgetRenderData())
	if err != nil {
		t.Fatalf("RenderRole(mayor) error = %v", err)
	}

	if !strings.Contains(output, "GLM-Safe Context Budget") {
		t.Fatalf("mayor template missing 'GLM-Safe Context Budget' section heading")
	}

	for _, marker := range glmContextBudgetRules {
		if !strings.Contains(output, marker) {
			t.Errorf("mayor template missing GLM budget rule marker %q", marker)
		}
	}
	for _, tok := range mayorBudgetMustHaves {
		if !strings.Contains(output, tok) {
			t.Errorf("mayor template missing GLM budget token %q", tok)
		}
	}
}

// TestRenderRole_Mayor_GLMContextBudgetPacketBounded proves the rendered
// GLM-safe budget packet stays bounded: it must not exceed a modest byte
// ceiling, and it must be materially smaller than the full Mayor context.
// This is the "tests or dry-run exercises prove prompt packets remain bounded"
// acceptance criterion: the rule packet itself obeys the small-prompt budget
// the rules prescribe, so injecting it into a GLM session does not itself
// cause the context bloat the rules exist to prevent.
func TestRenderRole_Mayor_GLMContextBudgetPacketBounded(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	output, err := tmpl.RenderRole("mayor", glmBudgetRenderData())
	if err != nil {
		t.Fatalf("RenderRole(mayor) error = %v", err)
	}

	// Extract the GLM budget section so the bound is measured on the packet a
	// GLM session would actually ingest, not the whole role context.
	packet := extractGLMBudgetSection(output)
	if packet == "" {
		t.Fatalf("could not locate GLM-Safe Context Budget section in mayor output")
	}

	const packetByteCeiling = 6000 // ~1.5k tokens; well under the 4k prompt ceiling the rules cite
	if n := len(packet); n > packetByteCeiling {
		t.Errorf("GLM context budget packet is %d bytes, exceeds %d-byte ceiling — the rule packet itself bloats context",
			n, packetByteCeiling)
	}

	// The packet must be a small fraction of the full Mayor context: if the
	// budget section ever grew to dominate the role doc, it would defeat its
	// own purpose. 25% is a generous ceiling that still catches runaway growth.
	if len(packet) > len(output)/4 {
		t.Errorf("GLM budget packet (%d bytes) is more than 25%% of full mayor context (%d bytes) — trim it",
			len(packet), len(output))
	}
}

// TestRenderRole_Witness_GLMSafeContextBudgetRules asserts the Witness
// template — the role whose GLM rollout motivated these rules — also encodes
// the GLM-safe budget section with its load-bearing tokens.
func TestRenderRole_Witness_GLMSafeContextBudgetRules(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	data := RoleData{
		Role:          "witness",
		RigName:       "myrig",
		TownRoot:      "/custom/glm-instance",
		TownName:      "glm-instance",
		WorkDir:       "/custom/glm-instance/myrig/witness",
		DefaultBranch: "main",
		Polecats:      []string{"Cat1", "Cat2"},
		MayorSession:  "gt-glm-instance-mayor",
		DeaconSession: "gt-glm-instance-deacon",
	}
	output, err := tmpl.RenderRole("witness", data)
	if err != nil {
		t.Fatalf("RenderRole(witness) error = %v", err)
	}

	if !strings.Contains(output, "GLM-Safe Context Budget") {
		t.Fatalf("witness template missing 'GLM-Safe Context Budget' section heading")
	}
	for _, tok := range witnessBudgetMustHaves {
		if !strings.Contains(output, tok) {
			t.Errorf("witness template missing GLM budget token %q", tok)
		}
	}

	packet := extractGLMBudgetSection(output)
	if packet == "" {
		t.Fatalf("could not locate GLM-Safe Context Budget section in witness output")
	}
	// Witness section is narrower (no mayor status/handoff breadth), so it must
	// be even smaller than the mayor packet.
	if len(packet) > 6000 {
		t.Errorf("witness GLM budget packet is %d bytes, exceeds 6000-byte ceiling", len(packet))
	}
}

// TestRenderRole_GLMContextBudget_TemplateUsesCmdFunc asserts the GLM budget
// rules reference the CLI via the {{ cmd }} template function, not a hardcoded
// "gt" literal, so a GT_COMMAND override renders the correct command. This is
// the anti-hardcoding invariant the other template tests enforce, applied to
// the budget section.
//
// It checks the template source rather than a GT_COMMAND-overridden render
// because CmdName() caches its first value in a package-level sync.Once: a
// render-time override is order-dependent and would poison the cache for the
// rest of the suite. The source check is both stronger (it directly verifies
// the template uses the function) and order-independent.
func TestRenderRole_GLMContextBudget_TemplateUsesCmdFunc(t *testing.T) {
	for _, file := range []string{"roles/mayor.md.tmpl", "roles/witness.md.tmpl"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("os.ReadFile(%q): %v", file, err)
		}
		section := extractGLMBudgetSection(string(src))
		if section == "" {
			t.Fatalf("%s: could not locate GLM-Safe Context Budget section", file)
		}
		// The budget section must drive CLI references through {{ cmd }}.
		if !strings.Contains(section, "{{ cmd }}") {
			t.Errorf("%s: GLM budget section does not use {{ cmd }} — CLI references are hardcoded", file)
		}
		// It must NOT contain hardcoded `` `gt <subcommand>` `` command literals
		// (backtick-quoted gt commands) that would ignore GT_COMMAND. The list
		// covers every gt subcommand used in the role templates so a regression
		// that re-hardcodes any of them fails this test instead of silently
		// breaking GT_COMMAND overrides in production.
		for _, lit := range []string{"`gt handoff", "`gt nudge", "`gt convoy", "`gt status", "`gt prime", "`gt peek", "`gt mail send", "`gt sling", "`gt patrol", "`gt tap"} {
			if strings.Contains(section, lit) {
				t.Errorf("%s: GLM budget section contains hardcoded %q instead of {{ cmd }}", file, lit)
			}
		}
	}
}

// extractGLMBudgetSection returns the rendered text of the "GLM-Safe Context
// Budget" section: from its heading up to (but not including) the next
// "### " or "---" boundary. Returns "" if the section is absent. This isolates
// the packet a GLM session would ingest so the bound is measured on the rule
// packet, not the surrounding role doc.
func extractGLMBudgetSection(output string) string {
	const heading = "GLM-Safe Context Budget"
	start := strings.Index(output, heading)
	if start < 0 {
		return ""
	}
	// Walk forward to the end of the line containing the heading, then find the
	// next top-level section boundary.
	rest := output[start:]
	// Find the next "---" separator or next "### " heading after the section.
	// The section uses "### GLM-Safe..." (h3) so the next "### " starts a peer.
	cut := len(rest)
	if i := strings.Index(rest[1:], "\n### "); i >= 0 {
		cut = i + 1
	}
	if j := strings.Index(rest[1:], "\n---"); j >= 0 && j+1 < cut {
		cut = j + 1
	}
	return strings.TrimSpace(rest[:cut])
}
