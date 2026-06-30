package beads

import (
	"strings"
	"testing"
)

// TestParseMergeSlotData_MalformedDescription is the retro-bug P0 regression
// for gastown-p61un: parseMergeSlotData must surface json.Unmarshal errors
// instead of discarding them. The merge slot stores serialized conflict-
// resolution state as JSON in the bead's Description; a malformed/truncated
// Description previously decoded to a zero-value mergeSlotData{Holder:""},
// which made MergeSlotAcquire treat the slot as available and let a second
// actor acquire concurrently — breaking merge-queue serialization.
func TestParseMergeSlotData_MalformedDescription(t *testing.T) {
	cases := []struct {
		name string
		desc string
	}{
		{"not json", "not json at all"},
		// cut mid-value (truncated object)
		{"truncated object", `{"holder":"refinery/push`},
		{"truncated array in waiters", `{"holder":"a","waiters":["b"}`},
		{"lone brace", "{"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := parseMergeSlotData(&Issue{Description: tc.desc})
			if err == nil {
				t.Fatalf("expected error for malformed Description %q, got nil (data=%+v)", tc.desc, data)
			}
			// A non-empty Holder despite a parse error means partial decode
			// leaked through; the bug is that Holder=="" is the dangerous case,
			// so at minimum the caller must see the error to decide.
			_ = data
		})
	}
}

// TestParseMergeSlotData_EmptyDescription asserts the empty-but-present case
// is still treated as an available slot (no state) without error.
func TestParseMergeSlotData_EmptyDescription(t *testing.T) {
	data, err := parseMergeSlotData(&Issue{Description: ""})
	if err != nil {
		t.Fatalf("unexpected error for empty Description: %v", err)
	}
	if data.Holder != "" {
		t.Errorf("expected empty Holder for empty Description, got %q", data.Holder)
	}
}

// TestParseMergeSlotData_Valid decodes a well-formed slot state.
func TestParseMergeSlotData_Valid(t *testing.T) {
	data, err := parseMergeSlotData(&Issue{Description: `{"holder":"refinery/push/x","waiters":["refinery/push/y"]}`})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data.Holder != "refinery/push/x" {
		t.Errorf("Holder = %q, want refinery/push/x", data.Holder)
	}
	if len(data.Waiters) != 1 || data.Waiters[0] != "refinery/push/y" {
		t.Errorf("Waiters = %v, want [refinery/push/y]", data.Waiters)
	}
}

// TestMergeSlotStatusFromIssue_CorruptIsUnavailable is the core P0 guard:
// a corrupt slot state must report Available=false with an Error so callers
// (MergeSlotAcquire) do not treat it as freely acquirable.
func TestMergeSlotStatusFromIssue_CorruptIsUnavailable(t *testing.T) {
	status := mergeSlotStatusFromIssue(&Issue{ID: "merge-slot", Description: `{"holder":"refinery/push/x`})
	if status.Available {
		t.Error("corrupt slot must NOT be reported as available")
	}
	if status.Error == "" {
		t.Error("corrupt slot must surface a non-empty Error")
	}
	if !strings.Contains(status.Error, "parsing merge slot state") {
		t.Errorf("Error should mention parse failure, got: %q", status.Error)
	}
}

// TestMergeSlotStatusFromIssue_HeldIsUnavailable confirms the happy held path
// still reports unavailable once the parse is honored.
func TestMergeSlotStatusFromIssue_HeldIsUnavailable(t *testing.T) {
	status := mergeSlotStatusFromIssue(&Issue{ID: "merge-slot", Description: `{"holder":"refinery/push/x"}`})
	if status.Available {
		t.Error("held slot must not be available")
	}
	if status.Holder != "refinery/push/x" {
		t.Errorf("Holder = %q, want refinery/push/x", status.Holder)
	}
	if status.Error != "" {
		t.Errorf("expected no error for valid state, got: %q", status.Error)
	}
}

// TestMergeSlotStatusFromIssue_EmptyIsAvailable confirms the empty-state path
// remains available (the legitimate "no holder" case).
func TestMergeSlotStatusFromIssue_EmptyIsAvailable(t *testing.T) {
	status := mergeSlotStatusFromIssue(&Issue{ID: "merge-slot", Description: `{"holder":""}`})
	if !status.Available {
		t.Error("empty-holder slot must be available")
	}
	if status.Error != "" {
		t.Errorf("expected no error for empty state, got: %q", status.Error)
	}
}
