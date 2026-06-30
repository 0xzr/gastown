package beads

import (
	"strings"
	"testing"
)

func TestParseMergeSlotData(t *testing.T) {
	tests := []struct {
		name    string
		desc    string
		want    mergeSlotData
		wantErr bool
	}{
		{
			name: "empty description returns zero value",
			desc: "",
			want: mergeSlotData{},
		},
		{
			name: "valid json parses holder and waiters",
			desc: `{"holder":"warboy","waiters":["capable","furiosa"]}`,
			want: mergeSlotData{
				Holder:  "warboy",
				Waiters: []string{"capable", "furiosa"},
			},
		},
		{
			name:    "invalid json returns error",
			desc:    `{"holder":"warboy"`,
			wantErr: true,
		},
		{
			name:    "non-json description returns error",
			desc:    "this is not json",
			wantErr: true,
		},
		{
			name: "json with wrong types returns error",
			desc: `{"holder":123}`,
			wantErr: true,
		},
		{
			name: "valid empty json object returns zero holder",
			desc: `{}`,
			want: mergeSlotData{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issue := &Issue{ID: "gt-slot", Description: tc.desc}
			got, err := parseMergeSlotData(issue)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseMergeSlotData() error = nil, wantErr true")
				}
				if !strings.Contains(err.Error(), "parsing merge slot data") {
					t.Errorf("parseMergeSlotData() error = %q, want it to contain 'parsing merge slot data'", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMergeSlotData() unexpected error: %v", err)
			}
			if got.Holder != tc.want.Holder {
				t.Errorf("Holder = %q, want %q", got.Holder, tc.want.Holder)
			}
			if len(got.Waiters) != len(tc.want.Waiters) {
				t.Errorf("len(Waiters) = %d, want %d", len(got.Waiters), len(tc.want.Waiters))
			} else {
				for i := range got.Waiters {
					if got.Waiters[i] != tc.want.Waiters[i] {
						t.Errorf("Waiters[%d] = %q, want %q", i, got.Waiters[i], tc.want.Waiters[i])
					}
				}
			}
		})
	}
}

func TestMergeSlotStatusFromIssue(t *testing.T) {
	t.Run("valid description", func(t *testing.T) {
		issue := &Issue{ID: "gt-slot", Description: `{"holder":"warboy","waiters":["capable"]}`}
		status := mergeSlotStatusFromIssue(issue)
		if status.ID != "gt-slot" {
			t.Errorf("ID = %q, want %q", status.ID, "gt-slot")
		}
		if status.Available {
			t.Error("Available = true, want false")
		}
		if status.Holder != "warboy" {
			t.Errorf("Holder = %q, want %q", status.Holder, "warboy")
		}
		if status.Error != "" {
			t.Errorf("Error = %q, want empty", status.Error)
		}
	})

	t.Run("empty description reports available", func(t *testing.T) {
		issue := &Issue{ID: "gt-slot", Description: ""}
		status := mergeSlotStatusFromIssue(issue)
		if !status.Available {
			t.Error("Available = false, want true")
		}
		if status.Error != "" {
			t.Errorf("Error = %q, want empty", status.Error)
		}
	})

	t.Run("corrupt description exposes parse error", func(t *testing.T) {
		issue := &Issue{ID: "gt-slot", Description: `{"holder":"warboy"`}
		status := mergeSlotStatusFromIssue(issue)
		if status.Error == "" {
			t.Fatal("Error = empty, want parse error")
		}
		if !strings.Contains(status.Error, "parsing merge slot data") {
			t.Errorf("Error = %q, want it to contain 'parsing merge slot data'", status.Error)
		}
		if status.Available {
			t.Error("Available = true on corrupt data, want false")
		}
	})
}
