package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/consult"
)

func TestTruncateForBeadTitle(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"short", "hello", "hello"},
		{"exact", strings.Repeat("a", 120), strings.Repeat("a", 120)},
		{"too long", strings.Repeat("a", 200), strings.Repeat("a", 117) + "..."},
		{"trims whitespace", "  hello  ", "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateForBeadTitle(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOneLine(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"no newlines", "hello world", "hello world"},
		{"strips newlines", "line1\nline2\nline3", "line1 line2 line3"},
		{"strips carriage returns", "a\rb\rc", "a b c"},
		{"trims outer whitespace", "  hello  ", "hello"},
		{"mixed", "  a\nb\rc\nd  ", "a b c d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := oneLine(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEmptyDash(t *testing.T) {
	if emptyDash("") != "-" {
		t.Errorf(`emptyDash("") = %q, want "-"`, emptyDash(""))
	}
	if emptyDash("   ") != "-" {
		t.Errorf(`emptyDash("   ") = %q, want "-"`, emptyDash("   "))
	}
	if emptyDash("x") != "x" {
		t.Errorf(`emptyDash("x") = %q, want "x"`, emptyDash("x"))
	}
}

func TestMirrorSummary(t *testing.T) {
	t.Run("nil response", func(t *testing.T) {
		if got := mirrorSummary(nil); got != "(no answer recorded)" {
			t.Errorf("got %q, want placeholder", got)
		}
	})
	t.Run("empty decided_by", func(t *testing.T) {
		if got := mirrorSummary(&consult.Response{}); got != "(no answer recorded)" {
			t.Errorf("got %q, want placeholder", got)
		}
	})
	t.Run("populated response", func(t *testing.T) {
		got := mirrorSummary(&consult.Response{
			DecidedBy: "opus",
			Decision:  "Allow with squash",
			Rationale: "Stacked tip-only MRs\nbreak publisher state.",
		})
		for _, want := range []string{"model=opus", "decision=Allow with squash", "rationale=Stacked tip-only MRs break publisher state."} {
			if !strings.Contains(got, want) {
				t.Errorf("got %q, want substring %q", got, want)
			}
		}
	})
}

func TestConsultActor_EnvOverride(t *testing.T) {
	t.Setenv("BD_ACTOR", "gastown/mayor")
	t.Setenv("GT_ROLE", "mayor")
	if got := consultActor(); got != "gastown/mayor" {
		t.Errorf("consultActor = %q, want gastown/mayor", got)
	}
}

func TestConsultActor_GTRoleFallback(t *testing.T) {
	t.Setenv("BD_ACTOR", "")
	t.Setenv("GT_ROLE", "mayor/role")
	if got := consultActor(); got != "mayor/role" {
		t.Errorf("consultActor = %q, want mayor/role", got)
	}
}

func TestConsultActor_DefaultIsMayor(t *testing.T) {
	t.Setenv("BD_ACTOR", "")
	t.Setenv("GT_ROLE", "")
	if got := consultActor(); got != "mayor" {
		t.Errorf("consultActor = %q, want mayor", got)
	}
}

func TestConsultTargetFor(t *testing.T) {
	if got := consultTargetFor(consult.ModelCodex); got != "codex" {
		t.Errorf("codex target = %q, want codex", got)
	}
	if got := consultTargetFor(consult.ModelOpus); got != "opus" {
		t.Errorf("opus target = %q, want opus", got)
	}
	// Unknown models fall back to the model name itself.
	if got := consultTargetFor(consult.RequestedModel("claude")); got != "claude" {
		t.Errorf("unknown target = %q, want claude", got)
	}
}

func TestFormatConsultMailBody_IncludesKeyFields(t *testing.T) {
	req := &consult.Request{
		BeadID:         "gastown-cet.6.4-xyz",
		Question:       "Allow stacked-branch tip-only MRs?",
		TriggerClass:   consult.TriggerMergePolicy,
		RequestedModel: consult.ModelOpus,
		AskedBy:        "mayor",
		AskedAt:        "2026-06-25T00:00:00Z",
		RelatedBead:    "gastown-cet.2.3",
		Options:        []string{"Allow", "Reject"},
		ContextRefs:    []string{"gastown-cet.2", "hq-try2"},
	}
	body := formatConsultMailBody(req)
	for _, want := range []string{
		"Consult packet: gastown-cet.6.4-xyz",
		"Trigger class:  merge_policy",
		"Requested model: opus",
		"Asked by:       mayor",
		"Related bead:   gastown-cet.2.3",
		"Allow stacked-branch tip-only MRs?",
		"1. Allow",
		"2. Reject",
		"gastown-cet.2",
		"hq-try2",
		"gt consult answer <id>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("mail body missing %q\n--full--\n%s", want, body)
		}
	}
}

func TestFormatConsultMailBody_NilSafe(t *testing.T) {
	if got := formatConsultMailBody(nil); got != "" {
		t.Errorf("formatConsultMailBody(nil) = %q, want empty", got)
	}
}

func TestFormatConsultMailBody_OmitsEmptyOptionalSections(t *testing.T) {
	req := &consult.Request{
		BeadID:         "gastown-xyz",
		Question:       "q?",
		TriggerClass:   consult.TriggerMergePolicy,
		RequestedModel: consult.ModelOpus,
		AskedBy:        "mayor",
		AskedAt:        "2026-06-25T00:00:00Z",
	}
	body := formatConsultMailBody(req)
	for _, absent := range []string{
		"Related bead:",
		"Options under consideration:",
		"Context refs",
		"current best guess",
	} {
		if strings.Contains(body, absent) {
			t.Errorf("body unexpectedly contains %q\n%s", absent, body)
		}
	}
}

func TestConsultCmd_Registers(t *testing.T) {
	// Walk the root command tree to confirm consult is registered. This
	// catches accidental removals during refactors.
	var found *cobra.Command
	for _, c := range rootCmd.Commands() {
		if c.Name() == "consult" {
			found = c
			break
		}
	}
	if found == nil {
		t.Fatalf("expected 'consult' subcommand to be registered on rootCmd")
	}
}

func TestConsultCmd_HasExpectedSubcommands(t *testing.T) {
	var consultCmdRef *cobra.Command
	for _, c := range rootCmd.Commands() {
		if c.Name() == "consult" {
			consultCmdRef = c
			break
		}
	}
	if consultCmdRef == nil {
		t.Fatalf("consult command not registered")
	}
	wantSubs := map[string]bool{"list": false, "show": false, "answer": false, "close": false}
	for _, sub := range consultCmdRef.Commands() {
		if _, ok := wantSubs[sub.Name()]; ok {
			wantSubs[sub.Name()] = true
		}
	}
	for name, seen := range wantSubs {
		if !seen {
			t.Errorf("expected subcommand %q on consult", name)
		}
	}
}
