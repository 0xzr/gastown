package beads

import (
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
