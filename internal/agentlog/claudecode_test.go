package agentlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeProjectDirFor(t *testing.T) {
	// The project hash replaces '/' with '-', so the leading slash becomes '-'.
	// e.g., /some/work/dir → $HOME/.claude/projects/-some-work-dir
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("getting home dir: %v", err)
	}

	input := "/some/work/dir"
	wantSuffix := "-some-work-dir"
	wantDir := filepath.Join(home, claudeProjectsDir, wantSuffix)

	got, err := claudeProjectDirFor(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wantDir {
		t.Errorf("claudeProjectDirFor(%q) = %q, want %q", input, got, wantDir)
	}
}

func TestParseClaudeCodeLine_Text(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hello world"}]},"timestamp":"2026-02-23T10:00:00Z"}`
	events := parseClaudeCodeLine(line, "hq-mayor", "claudecode", "test-uuid")
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.EventType != "text" {
		t.Errorf("EventType = %q, want %q", ev.EventType, "text")
	}
	if ev.Role != "assistant" {
		t.Errorf("Role = %q, want %q", ev.Role, "assistant")
	}
	if ev.Content != "Hello world" {
		t.Errorf("Content = %q, want %q", ev.Content, "Hello world")
	}
	if ev.SessionID != "hq-mayor" {
		t.Errorf("SessionID = %q, want %q", ev.SessionID, "hq-mayor")
	}
	if ev.AgentType != "claudecode" {
		t.Errorf("AgentType = %q, want %q", ev.AgentType, "claudecode")
	}
}

func TestCheckClaudeMaxTokensDeadloopDetectsConsecutiveEmptyText(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := filepath.Join(t.TempDir(), "mayor")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}
	projectDirs, err := claudeProjectDirsFor(workDir)
	if err != nil {
		t.Fatalf("claudeProjectDirsFor: %v", err)
	}
	if err := os.MkdirAll(projectDirs[1], 0755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	path := filepath.Join(projectDirs[1], "session.jsonl")
	lines := []string{
		assistantTurn("2026-07-02T05:00:00Z", "max_tokens", ""),
		assistantTurn("2026-07-02T05:01:00Z", "max_tokens", "   \n\t"),
		assistantTurn("2026-07-02T05:02:00Z", "max_tokens", ""),
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	report, err := CheckClaudeMaxTokensDeadloop(workDir, 3, time.Date(2026, 7, 2, 4, 59, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("CheckClaudeMaxTokensDeadloop: %v", err)
	}
	if !report.Detected {
		t.Fatalf("Detected = false, want true; report=%+v", report)
	}
	if report.Consecutive != 3 {
		t.Fatalf("Consecutive = %d, want 3", report.Consecutive)
	}
	if report.LastPath != path {
		t.Fatalf("LastPath = %q, want %q", report.LastPath, path)
	}
}

func TestCheckClaudeMaxTokensDeadloopUsesConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := filepath.Join(t.TempDir(), "mayor")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}
	projectDirs, err := claudeProjectDirsForConfigDirs(workDir, filepath.Join(home, "account-a"))
	if err != nil {
		t.Fatalf("claudeProjectDirsForConfigDirs: %v", err)
	}
	path := filepath.Join(projectDirs[len(projectDirs)-1], "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	lines := []string{
		assistantTurn("2026-07-02T05:00:00Z", "max_tokens", ""),
		assistantTurn("2026-07-02T05:01:00Z", "max_tokens", ""),
		assistantTurn("2026-07-02T05:02:00Z", "max_tokens", ""),
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	report, err := CheckClaudeMaxTokensDeadloop(workDir, 3, time.Date(2026, 7, 2, 4, 59, 0, 0, time.UTC), filepath.Join(home, "account-a"))
	if err != nil {
		t.Fatalf("CheckClaudeMaxTokensDeadloop: %v", err)
	}
	if !report.Detected {
		t.Fatalf("Detected = false, want true; report=%+v", report)
	}
	if report.LastPath != path {
		t.Fatalf("LastPath = %q, want %q", report.LastPath, path)
	}
}

func TestCheckClaudeMaxTokensDeadloopScansNewestSessionOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := filepath.Join(t.TempDir(), "mayor")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}
	projectDirs, err := claudeProjectDirsFor(workDir)
	if err != nil {
		t.Fatalf("claudeProjectDirsFor: %v", err)
	}
	if err := os.MkdirAll(projectDirs[0], 0755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	oldBad := filepath.Join(projectDirs[0], "old-bad.jsonl")
	newHealthy := filepath.Join(projectDirs[0], "new-healthy.jsonl")
	oldLines := []string{
		assistantTurn("2026-07-02T05:00:00Z", "max_tokens", ""),
		assistantTurn("2026-07-02T05:01:00Z", "max_tokens", ""),
		assistantTurn("2026-07-02T05:02:00Z", "max_tokens", ""),
	}
	if err := os.WriteFile(oldBad, []byte(strings.Join(oldLines, "\n")+"\n"), 0644); err != nil {
		t.Fatalf("write old jsonl: %v", err)
	}
	if err := os.WriteFile(newHealthy, []byte(assistantTurn("2026-07-02T05:03:00Z", "end_turn", "I am healthy.")+"\n"), 0644); err != nil {
		t.Fatalf("write healthy jsonl: %v", err)
	}
	oldTime := time.Date(2026, 7, 2, 5, 2, 0, 0, time.UTC)
	newTime := time.Date(2026, 7, 2, 5, 3, 0, 0, time.UTC)
	if err := os.Chtimes(oldBad, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old: %v", err)
	}
	if err := os.Chtimes(newHealthy, newTime, newTime); err != nil {
		t.Fatalf("chtimes healthy: %v", err)
	}

	report, err := CheckClaudeMaxTokensDeadloop(workDir, 3, time.Date(2026, 7, 2, 4, 59, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("CheckClaudeMaxTokensDeadloop: %v", err)
	}
	if report.Detected {
		t.Fatalf("Detected = true, want false; report=%+v", report)
	}
	if report.LastPath != newHealthy {
		t.Fatalf("LastPath = %q, want newest %q", report.LastPath, newHealthy)
	}
	if report.ScannedTurns != 1 {
		t.Fatalf("ScannedTurns = %d, want 1", report.ScannedTurns)
	}
}

func TestCheckClaudeMaxTokensDeadloopResetsOnText(t *testing.T) {
	report := &MaxTokensDeadloopReport{Required: 3}
	input := strings.Join([]string{
		assistantTurn("2026-07-02T05:00:00Z", "max_tokens", ""),
		assistantTurn("2026-07-02T05:01:00Z", "max_tokens", "I am still working."),
		assistantTurn("2026-07-02T05:02:00Z", "max_tokens", ""),
	}, "\n")
	if err := scanClaudeMaxTokensDeadloop(strings.NewReader(input), "test.jsonl", 3, time.Time{}, report); err != nil {
		t.Fatalf("scanClaudeMaxTokensDeadloop: %v", err)
	}
	if report.Detected {
		t.Fatalf("Detected = true, want false; report=%+v", report)
	}
	if report.Consecutive != 1 {
		t.Fatalf("Consecutive = %d, want 1 after reset", report.Consecutive)
	}
}

func TestCheckClaudeMaxTokensDeadloopIgnoresOldEntries(t *testing.T) {
	report := &MaxTokensDeadloopReport{Required: 2}
	input := strings.Join([]string{
		assistantTurn("2026-07-02T04:00:00Z", "max_tokens", ""),
		assistantTurn("2026-07-02T04:01:00Z", "max_tokens", ""),
	}, "\n")
	since := time.Date(2026, 7, 2, 4, 30, 0, 0, time.UTC)
	if err := scanClaudeMaxTokensDeadloop(strings.NewReader(input), "test.jsonl", 2, since, report); err != nil {
		t.Fatalf("scanClaudeMaxTokensDeadloop: %v", err)
	}
	if report.ScannedTurns != 0 {
		t.Fatalf("ScannedTurns = %d, want 0", report.ScannedTurns)
	}
	if report.Detected {
		t.Fatalf("Detected = true, want false; report=%+v", report)
	}
}

func TestCheckClaudeMaxTokensDeadloopIgnoresUnparseableTimestampsWhenBounded(t *testing.T) {
	report := &MaxTokensDeadloopReport{Required: 2}
	input := strings.Join([]string{
		assistantTurn("not-a-time", "max_tokens", ""),
		assistantTurn("", "max_tokens", ""),
		assistantTurn("2026-07-02T05:00:00Z", "max_tokens", ""),
	}, "\n")
	since := time.Date(2026, 7, 2, 4, 30, 0, 0, time.UTC)
	if err := scanClaudeMaxTokensDeadloop(strings.NewReader(input), "test.jsonl", 2, since, report); err != nil {
		t.Fatalf("scanClaudeMaxTokensDeadloop: %v", err)
	}
	if report.ScannedTurns != 1 {
		t.Fatalf("ScannedTurns = %d, want 1", report.ScannedTurns)
	}
	if report.Consecutive != 1 {
		t.Fatalf("Consecutive = %d, want 1", report.Consecutive)
	}
	if report.Detected {
		t.Fatalf("Detected = true, want false; report=%+v", report)
	}
}

func assistantTurn(timestamp, stopReason, text string) string {
	payload, _ := json.Marshal(map[string]any{
		"type":      "assistant",
		"timestamp": timestamp,
		"message": map[string]any{
			"role":        "assistant",
			"stop_reason": stopReason,
			"content": []map[string]any{
				{"type": "text", "text": text},
			},
		},
	})
	return string(payload)
}

func TestParseClaudeCodeLine_ToolUse(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`
	events := parseClaudeCodeLine(line, "s1", "claudecode", "test-uuid")
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.EventType != "tool_use" {
		t.Errorf("EventType = %q, want %q", ev.EventType, "tool_use")
	}
	if ev.Content == "" {
		t.Error("Content should not be empty for tool_use")
	}
	// Content should contain the tool name
	if len(ev.Content) < 4 || ev.Content[:4] != "Bash" {
		t.Errorf("Content %q should start with tool name 'Bash'", ev.Content)
	}
}

func TestParseClaudeCodeLine_SkipsUnknownTypes(t *testing.T) {
	line := `{"type":"summary","content":"some summary"}`
	events := parseClaudeCodeLine(line, "s1", "claudecode", "test-uuid")
	if len(events) != 0 {
		t.Errorf("expected 0 events for summary type, got %d", len(events))
	}
}

func TestParseClaudeCodeLine_InvalidJSON(t *testing.T) {
	events := parseClaudeCodeLine("not json", "s1", "claudecode", "test-uuid")
	if len(events) != 0 {
		t.Errorf("expected 0 events for invalid JSON, got %d", len(events))
	}
}

func TestNewAdapter(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		wantNil   bool
		wantType  string
	}{
		{"claudecode", "claudecode", false, "claudecode"},
		{"empty defaults to claudecode", "", false, "claudecode"},
		{"opencode", "opencode", false, "opencode"},
		{"unknown", "kiro", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewAdapter(tt.agentType)
			if tt.wantNil {
				if a != nil {
					t.Errorf("expected nil adapter for %q", tt.agentType)
				}
				return
			}
			if a == nil {
				t.Fatalf("expected non-nil adapter for %q", tt.agentType)
			}
			if a.AgentType() != tt.wantType {
				t.Errorf("AgentType() = %q, want %q", a.AgentType(), tt.wantType)
			}
		})
	}
}
