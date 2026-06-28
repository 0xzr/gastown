package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/refinery/mrtelemetry"
)

// TestBuildMRReportOptions_Defaults verifies that no flags means
// "no time filter, no model filter".
func TestBuildMRReportOptions_Defaults(t *testing.T) {
	mrReportSinceDays = 0
	mrReportSinceRFC = ""
	mrReportModels = nil

	opts, err := buildMRReportOptions()
	if err != nil {
		t.Fatalf("buildMRReportOptions: %v", err)
	}
	if !opts.Since.IsZero() {
		t.Errorf("Since=%v, want zero (no filter)", opts.Since)
	}
	if len(opts.WriterModels) != 0 {
		t.Errorf("WriterModels=%v, want empty", opts.WriterModels)
	}
}

// TestBuildMRReportOptions_SinceDays verifies that --since-days produces
// a cutoff at time.Now() - N days.
func TestBuildMRReportOptions_SinceDays(t *testing.T) {
	mrReportSinceDays = 7
	mrReportSinceRFC = ""
	mrReportModels = nil
	defer func() { mrReportSinceDays = 0 }()

	before := time.Now().AddDate(0, 0, -7)
	opts, err := buildMRReportOptions()
	if err != nil {
		t.Fatalf("buildMRReportOptions: %v", err)
	}
	if opts.Since.Before(before.Add(-time.Second)) || opts.Since.After(before.Add(time.Second)) {
		t.Errorf("Since=%v, want approximately %v", opts.Since, before)
	}
}

// TestBuildMRReportOptions_SinceRFC verifies --since takes a valid RFC3339.
func TestBuildMRReportOptions_SinceRFC(t *testing.T) {
	mrReportSinceDays = 0
	mrReportSinceRFC = "2026-06-01T00:00:00Z"
	defer func() { mrReportSinceRFC = "" }()

	opts, err := buildMRReportOptions()
	if err != nil {
		t.Fatalf("buildMRReportOptions: %v", err)
	}
	want := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if !opts.Since.Equal(want) {
		t.Errorf("Since=%v, want %v", opts.Since, want)
	}
}

// TestBuildMRReportOptions_InvalidSince verifies that an invalid --since
// timestamp is rejected.
func TestBuildMRReportOptions_InvalidSince(t *testing.T) {
	mrReportSinceDays = 0
	mrReportSinceRFC = "not-a-timestamp"
	defer func() { mrReportSinceRFC = "" }()

	if _, err := buildMRReportOptions(); err == nil {
		t.Errorf("expected error for invalid --since, got nil")
	}
}

// TestBuildMRReportOptions_SinceRFCOverridesSinceDays verifies that
// --since takes precedence over --since-days.
func TestBuildMRReportOptions_SinceRFCOverridesSinceDays(t *testing.T) {
	mrReportSinceDays = 7
	mrReportSinceRFC = "2026-06-01T00:00:00Z"
	defer func() {
		mrReportSinceDays = 0
		mrReportSinceRFC = ""
	}()

	opts, err := buildMRReportOptions()
	if err != nil {
		t.Fatalf("buildMRReportOptions: %v", err)
	}
	if opts.Since.Year() != 2026 || opts.Since.Month() != 6 || opts.Since.Day() != 1 {
		t.Errorf("Since=%v, want 2026-06-01 (--since should override --since-days)",
			opts.Since)
	}
}

// TestBuildMRReportOptions_ModelFilter verifies that --model values are
// propagated to WriterModels.
func TestBuildMRReportOptions_ModelFilter(t *testing.T) {
	mrReportSinceDays = 0
	mrReportSinceRFC = ""
	mrReportModels = []string{"umans-kimi", "umans-glm"}
	defer func() { mrReportModels = nil }()

	opts, err := buildMRReportOptions()
	if err != nil {
		t.Fatalf("buildMRReportOptions: %v", err)
	}
	if len(opts.WriterModels) != 2 {
		t.Errorf("WriterModels=%v, want 2 entries", opts.WriterModels)
	}
}

// TestOpenMRTelemetryStore_NoRigPath verifies that an unknown rig path
// surfaces as an error rather than silently creating a file.
func TestOpenMRTelemetryStore_NoRigPath(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(townRoot, "marker"), []byte("x"), 0644); err != nil {
		t.Fatalf("marker: %v", err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(oldWD) }()

	_, err = openMRTelemetryStore("does-not-exist")
	if err == nil {
		t.Errorf("expected error for missing rig path, got nil")
	}
}

// TestWriteMRReportJSON_Shape verifies that JSON output contains the
// expected top-level keys (by_model, totals, generated_at).
func TestWriteMRReportJSON_Shape(t *testing.T) {
	report := &mrtelemetry.Report{
		ByModel: map[string]*mrtelemetry.ModelSummary{
			"umans-kimi": {TotalAttempts: 3, FirstPassCodexPassCount: 1},
		},
		Totals: &mrtelemetry.ModelSummary{TotalAttempts: 3},
	}
	var buf strings.Builder
	if err := writeMRReportJSON(&buf, report); err != nil {
		t.Fatalf("writeMRReportJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(buf.String()), &got); err != nil {
		t.Fatalf("Unmarshal JSON output: %v\nraw: %s", err, buf.String())
	}
	for _, key := range []string{"by_model", "totals", "generated_at"} {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON output missing key %q (got: %v)", key, got)
		}
	}
}
