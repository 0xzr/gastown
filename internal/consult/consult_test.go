package consult

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRequest_Validate(t *testing.T) {
	tests := []struct {
		name    string
		req     *Request
		wantErr string
	}{
		{
			name:    "nil request",
			req:     nil,
			wantErr: "nil request",
		},
		{
			name: "valid request",
			req: &Request{
				Question:       "Should we allow stacked-branch tip-only MRs?",
				TriggerClass:   TriggerMergePolicy,
				RequestedModel: ModelOpus,
				AskedBy:        "mayor",
				AskedAt:        "2026-06-25T00:00:00Z",
			},
		},
		{
			name: "empty question",
			req: &Request{
				TriggerClass:   TriggerMergePolicy,
				RequestedModel: ModelOpus,
				AskedBy:        "mayor",
			},
			wantErr: "question is required",
		},
		{
			name: "whitespace-only question",
			req: &Request{
				Question:       "   \t  ",
				TriggerClass:   TriggerMergePolicy,
				RequestedModel: ModelOpus,
				AskedBy:        "mayor",
			},
			wantErr: "question is required",
		},
		{
			name: "invalid trigger",
			req: &Request{
				Question:       "x",
				TriggerClass:   "bogus",
				RequestedModel: ModelOpus,
				AskedBy:        "mayor",
			},
			wantErr: "invalid trigger class",
		},
		{
			name: "invalid model",
			req: &Request{
				Question:       "x",
				TriggerClass:   TriggerMergePolicy,
				RequestedModel: "gpt-99",
				AskedBy:        "mayor",
			},
			wantErr: "invalid requested model",
		},
		{
			name: "empty asked_by",
			req: &Request{
				Question:       "x",
				TriggerClass:   TriggerMergePolicy,
				RequestedModel: ModelOpus,
			},
			wantErr: "asked_by is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestResponse_Validate(t *testing.T) {
	tests := []struct {
		name    string
		resp    *Response
		wantErr string
	}{
		{
			name:    "nil response",
			resp:    nil,
			wantErr: "nil response",
		},
		{
			name: "valid response",
			resp: &Response{
				DecidedBy:  "opus",
				DecidedAt:  "2026-06-25T00:00:00Z",
				Decision:   "Allow with squash",
				Rationale:  "Stacking tip-only MRs breaks publisher state.",
				Confidence: "high",
			},
		},
		{
			name: "missing decided_by",
			resp: &Response{
				DecidedAt: "2026-06-25T00:00:00Z",
				Rationale: "x",
			},
			wantErr: "decided_by is required",
		},
		{
			name: "missing decided_at",
			resp: &Response{
				DecidedBy: "opus",
				Rationale: "x",
			},
			wantErr: "decided_at is required",
		},
		{
			name: "missing rationale",
			resp: &Response{
				DecidedBy: "opus",
				DecidedAt: "2026-06-25T00:00:00Z",
			},
			wantErr: "rationale is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.resp.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestFingerprint_StableAcrossWhitespace(t *testing.T) {
	a := Fingerprint("Should we allow stacked MRs? ", TriggerMergePolicy)
	b := Fingerprint("  Should we allow stacked MRs?", TriggerMergePolicy)
	if a != b {
		t.Fatalf("fingerprint not stable across whitespace: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "consult-fp:merge_policy:") {
		t.Fatalf("fingerprint missing prefix: %q", a)
	}
}

func TestFingerprint_DifferentTriggerClassesDiffer(t *testing.T) {
	a := Fingerprint("same question", TriggerMergePolicy)
	b := Fingerprint("same question", TriggerRecoveryLoop)
	if a == b {
		t.Fatalf("expected fingerprints to differ across trigger classes, both = %q", a)
	}
}

func TestFormatRequestDescription_ContainsRequiredFields(t *testing.T) {
	req := &Request{
		Question:        "Allow stacked-branch tip-only MRs?",
		TriggerClass:    TriggerMergePolicy,
		RequestedModel:  ModelOpus,
		ContextRefs:     []string{"gastown-cet.2", "hq-try2"},
		Options:         []string{"Allow", "Reject", "Allow with squash"},
		CurrentDecision: "Allow with squash",
		AskedBy:         "mayor",
		AskedAt:         "2026-06-25T00:00:00Z",
		RelatedBead:     "gastown-cet.6.4",
		Fingerprint:     "consult-fp:merge_policy:abc123",
	}
	desc := FormatRequestDescription("Allow stacked-branch tip-only MRs?", req)
	wantLines := []string{
		"trigger_class: merge_policy",
		"requested_model: opus",
		"question: Allow stacked-branch tip-only MRs?",
		"context_refs: gastown-cet.2,hq-try2",
		"asked_by: mayor",
		"asked_at: 2026-06-25T00:00:00Z",
		"related_bead: gastown-cet.6.4",
		"fingerprint: consult-fp:merge_policy:abc123",
		"state: open",
		"answered_by: null",
		"decision: null",
		"rationale: null",
	}
	for _, want := range wantLines {
		if !strings.Contains(desc, want) {
			t.Errorf("description missing line %q\n--full--\n%s", want, desc)
		}
	}
	// Options are indented as a numbered list.
	for _, opt := range []string{"1. Allow", "2. Reject", "3. Allow with squash"} {
		if !strings.Contains(desc, opt) {
			t.Errorf("description missing option %q", opt)
		}
	}
}

func TestParseAnswerSection_RoundTrip(t *testing.T) {
	resp := &Response{
		DecidedBy:  "opus",
		DecidedAt:  "2026-06-25T00:00:00Z",
		Decision:   "Allow with squash",
		Rationale:  "Stacked tip-only MRs break publisher state.",
		Confidence: "high",
	}
	section := FormatAnswerSection(resp, "mayor", "acted on consultation")
	// Embed the section under a fake description to exercise the parser.
	desc := "Title\nstate: open\n" + section
	gotResp, closer, reason := ParseAnswerSection(desc)
	if gotResp.DecidedBy != resp.DecidedBy {
		t.Errorf("DecidedBy = %q, want %q", gotResp.DecidedBy, resp.DecidedBy)
	}
	if gotResp.DecidedAt != resp.DecidedAt {
		t.Errorf("DecidedAt = %q, want %q", gotResp.DecidedAt, resp.DecidedAt)
	}
	if gotResp.Decision != resp.Decision {
		t.Errorf("Decision = %q, want %q", gotResp.Decision, resp.Decision)
	}
	if gotResp.Rationale != resp.Rationale {
		t.Errorf("Rationale = %q, want %q", gotResp.Rationale, resp.Rationale)
	}
	if gotResp.Confidence != resp.Confidence {
		t.Errorf("Confidence = %q, want %q", gotResp.Confidence, resp.Confidence)
	}
	if closer != "mayor" {
		t.Errorf("closer = %q, want mayor", closer)
	}
	if reason != "acted on consultation" {
		t.Errorf("reason = %q, want acted on consultation", reason)
	}
}

func TestIsValidTriggerClass(t *testing.T) {
	for _, c := range []TriggerClass{
		TriggerMergePolicy,
		TriggerWitnessRefineryOverride,
		TriggerRecoveryLoop,
		TriggerAmbiguousDirective,
		TriggerLowConfidenceOutput,
	} {
		if !IsValidTriggerClass(c) {
			t.Errorf("IsValidTriggerClass(%q) = false, want true", c)
		}
	}
	for _, c := range []TriggerClass{"", "bogus", "MergePolicy"} {
		if IsValidTriggerClass(c) {
			t.Errorf("IsValidTriggerClass(%q) = true, want false", c)
		}
	}
}

func TestIsValidRequestedModel(t *testing.T) {
	for _, m := range []RequestedModel{ModelCodex, ModelOpus} {
		if !IsValidRequestedModel(m) {
			t.Errorf("IsValidRequestedModel(%q) = false, want true", m)
		}
	}
	for _, m := range []RequestedModel{"", "claude", "opus-4"} {
		if IsValidRequestedModel(m) {
			t.Errorf("IsValidRequestedModel(%q) = true, want false", m)
		}
	}
}

func TestDefaultLoopPolicy(t *testing.T) {
	p := DefaultLoopPolicy()
	if p.Threshold != 3 {
		t.Errorf("Threshold = %d, want 3", p.Threshold)
	}
	if p.Window != 30*time.Minute {
		t.Errorf("Window = %s, want 30m", p.Window)
	}
	if p.EscalateAt != 6 {
		t.Errorf("EscalateAt = %d, want 6", p.EscalateAt)
	}
}

func TestLoopDetector_FiresOnlyAboveThreshold(t *testing.T) {
	townRoot := t.TempDir()
	d := NewLoopDetector(townRoot, LoopPolicy{
		Threshold:  3,
		Window:     30 * time.Minute,
		EscalateAt: 6,
	})
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	fp := "recovery:push-failed"

	// 1st and 2nd occurrences: no action.
	for i := 0; i < 2; i++ {
		dec, err := d.RecordAndCheck(fp, "witness", "gt-1", now.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatalf("RecordAndCheck err: %v", err)
		}
		if dec.Action != LoopActionNone {
			t.Errorf("[%d] Action = %s, want none", i+1, dec.Action)
		}
	}

	// 3rd occurrence: action should be LoopActionConsult (crosses Threshold).
	dec, err := d.RecordAndCheck(fp, "witness", "gt-2", now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("RecordAndCheck err: %v", err)
	}
	if dec.Action != LoopActionConsult {
		t.Errorf("at threshold Action = %s, want consult", dec.Action)
	}
	if dec.Count != 3 {
		t.Errorf("at threshold Count = %d, want 3", dec.Count)
	}

	// 4th and 5th occurrences: still LoopActionConsult (above Threshold, below EscalateAt).
	for i := 0; i < 2; i++ {
		dec, err := d.RecordAndCheck(fp, "witness", "gt-extra", now.Add(time.Duration(4+i)*time.Minute))
		if err != nil {
			t.Fatalf("RecordAndCheck err: %v", err)
		}
		if dec.Action != LoopActionConsult {
			t.Errorf("[%d] Action = %s, want consult (between threshold and escalate_at)",
				4+i, dec.Action)
		}
	}

	// 6th occurrence: action should escalate.
	dec, err = d.RecordAndCheck(fp, "witness", "gt-3", now.Add(6*time.Minute))
	if err != nil {
		t.Fatalf("RecordAndCheck err: %v", err)
	}
	if dec.Action != LoopActionEscalate {
		t.Errorf("at escalate_at Action = %s, want escalate", dec.Action)
	}
	if dec.Count != 6 {
		t.Errorf("at escalate_at Count = %d, want 6", dec.Count)
	}
}

func TestLoopDetector_IgnoresEventsOutsideWindow(t *testing.T) {
	townRoot := t.TempDir()
	d := NewLoopDetector(townRoot, LoopPolicy{
		Threshold:  3,
		Window:     10 * time.Minute,
		EscalateAt: 6,
	})
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	fp := "recovery:wakeup-race"

	// 5 events at t-15m (all outside the window).
	for i := 0; i < 5; i++ {
		if err := RecordLoopEvent(townRoot, fp, "witness", "gt-x", now.Add(-15*time.Minute)); err != nil {
			t.Fatalf("RecordLoopEvent err: %v", err)
		}
	}
	dec, err := d.Check(fp, now)
	if err != nil {
		t.Fatalf("Check err: %v", err)
	}
	if dec.Action != LoopActionNone {
		t.Errorf("Action = %s, want none (events outside window)", dec.Action)
	}
	if dec.Count != 0 {
		t.Errorf("Count = %d, want 0 (events outside window)", dec.Count)
	}
}

func TestLoopDetector_OnlyMatchesOwnFingerprint(t *testing.T) {
	townRoot := t.TempDir()
	d := NewLoopDetector(townRoot, LoopPolicy{
		Threshold:  3,
		Window:     30 * time.Minute,
		EscalateAt: 6,
	})
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := RecordLoopEvent(townRoot, "recovery:other", "witness", "gt-x", now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("RecordLoopEvent err: %v", err)
		}
	}
	dec, err := d.Check("recovery:mine", now)
	if err != nil {
		t.Fatalf("Check err: %v", err)
	}
	if dec.Count != 0 {
		t.Errorf("Count = %d, want 0 (different fingerprint)", dec.Count)
	}
	if dec.Action != LoopActionNone {
		t.Errorf("Action = %s, want none (different fingerprint)", dec.Action)
	}
}

func TestLoopDetector_EmptyFingerprintReturnsNone(t *testing.T) {
	townRoot := t.TempDir()
	d := NewLoopDetector(townRoot, DefaultLoopPolicy())
	dec, err := d.Check("", time.Now())
	if err != nil {
		t.Fatalf("Check err: %v", err)
	}
	if dec.Action != LoopActionNone {
		t.Errorf("Action = %s, want none (empty fingerprint)", dec.Action)
	}
}

func TestLoopDetector_ZeroPolicyFallsBackToDefaults(t *testing.T) {
	townRoot := t.TempDir()
	d := NewLoopDetector(townRoot, LoopPolicy{}) // zero policy
	if d.Policy.Threshold != 3 {
		t.Errorf("Threshold = %d, want default 3", d.Policy.Threshold)
	}
	if d.Policy.Window != 30*time.Minute {
		t.Errorf("Window = %s, want default 30m", d.Policy.Window)
	}
	if d.Policy.EscalateAt != 6 {
		t.Errorf("EscalateAt = %d, want default 6", d.Policy.EscalateAt)
	}
}

func TestLoopState_RoundTrip(t *testing.T) {
	townRoot := t.TempDir()
	if err := RecordLoopEvent(townRoot, "fp-a", "witness", "gt-1", time.Now()); err != nil {
		t.Fatalf("RecordLoopEvent err: %v", err)
	}
	if err := RecordLoopEvent(townRoot, "fp-b", "deacon", "gt-2", time.Now()); err != nil {
		t.Fatalf("RecordLoopEvent err: %v", err)
	}
	// Confirm the file landed where LoopFile expects.
	state, err := LoadLoopState(townRoot)
	if err != nil {
		t.Fatalf("LoadLoopState err: %v", err)
	}
	if len(state.Events) != 2 {
		t.Fatalf("len(Events) = %d, want 2", len(state.Events))
	}
	want := map[string]bool{"fp-a": false, "fp-b": false}
	for _, ev := range state.Events {
		if _, ok := want[ev.Fingerprint]; ok {
			want[ev.Fingerprint] = true
		}
	}
	for fp, seen := range want {
		if !seen {
			t.Errorf("event for fingerprint %q not persisted", fp)
		}
	}
}

func TestLoopFile_PathStable(t *testing.T) {
	got := LoopFile("/tmp/town")
	want := filepath.Join("/tmp/town", "mayor", "consult_loops.json")
	if got != want {
		t.Errorf("LoopFile = %q, want %q", got, want)
	}
}

func TestRecordLoopEvent_EmptyFingerprintRejected(t *testing.T) {
	townRoot := t.TempDir()
	if err := RecordLoopEvent(townRoot, "", "witness", "gt-1", time.Now()); err == nil {
		t.Fatalf("expected error for empty fingerprint, got nil")
	}
}

func TestSaveLoopState_NilRejected(t *testing.T) {
	if err := SaveLoopState("/tmp", nil); err == nil {
		t.Fatalf("expected error for nil state, got nil")
	}
}
