package agentlog

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// claudeProjectsDir is the path under $HOME where Claude Code stores projects.
	claudeProjectsDir = ".claude/projects"

	// watchPollInterval is how often we poll for new JSONL content or files.
	watchPollInterval = 500 * time.Millisecond

	// watchFileTimeout is how long we wait for a JSONL file to appear after startup.
	watchFileTimeout = 30 * time.Second
)

// ClaudeCodeAdapter watches Claude Code JSONL conversation files.
//
// Claude Code writes conversation files at:
//
//	~/.claude/projects/<hash>/<session-uuid>.jsonl
//
// where <hash> is derived from the working directory by replacing "/" with "-"
// (e.g. /Users/pa/gt/mayor → -Users-pa-gt-mayor).
//
// The adapter finds the most recently modified JSONL file created after the
// Gas Town session start time (since), tails it, and automatically switches
// to a newer file when a new Claude session starts in the same project dir.
// This handles Claude instances that are frequently created and destroyed.
type ClaudeCodeAdapter struct{}

func (a *ClaudeCodeAdapter) AgentType() string { return "claudecode" }

// Watch starts tailing the Claude Code JSONL log for sessionID.
// workDir is the agent's CWD and is used to locate the project hash directory.
// since is the Gas Town session start time: only JSONL files modified at or
// after this time are considered, so unrelated Claude instances (user sessions,
// other Gas Town rigs) running in the same work dir are excluded.
// Pass zero since to watch any file regardless of age.
//
// When Claude exits and a new session starts (new JSONL file), Watch
// automatically switches to the new file within one poll interval (500ms).
func (a *ClaudeCodeAdapter) Watch(ctx context.Context, sessionID, workDir string, since time.Time) (<-chan AgentEvent, error) {
	projectDir, err := claudeProjectDirFor(workDir)
	if err != nil {
		return nil, fmt.Errorf("resolving project dir: %w", err)
	}

	ch := make(chan AgentEvent, 64)
	go func() {
		defer close(ch)

		// Loop indefinitely: find the active JSONL file, tail it, then switch when a newer
		// one appears (new Claude session). ctx cancellation is the only exit.
		// Timeouts from waitForNewestJSONL are retried so that agent restarts or late
		// session starts (JSONL file appearing after the 30s window) are picked up.
		var currentPath string
		for {
			if ctx.Err() != nil {
				return
			}
			jsonlPath, err := waitForNewestJSONL(ctx, projectDir, since)
			if err != nil {
				// ctx was canceled — clean exit.
				if ctx.Err() != nil {
					return
				}
				// Timeout: no JSONL appeared in 30s. Claude may not have started yet
				// or the agent restarted. Reset `since` so we pick up any new file.
				since = time.Now().Add(-watchPollInterval)
				continue
			}

			currentPath = jsonlPath

			// Tail the file; returns when a newer file appears or ctx is done.
			tailJSONL(ctx, currentPath, projectDir, since, sessionID, a.AgentType(), ch)

			if ctx.Err() != nil {
				return
			}
			// A newer file was detected — loop immediately to pick it up.
		}
	}()
	return ch, nil
}

// claudeProjectDirFor returns the Claude Code project directory for workDir.
// Formula: $HOME/.claude/projects/<hash> where hash = workDir with '/' → '-'.
// On Windows, backslashes are converted to forward slashes and the drive
// letter (e.g. "C:") is stripped before hashing, matching Claude Code's
// cross-platform behavior.
func claudeProjectDirFor(workDir string) (string, error) {
	dirs, err := claudeProjectDirsFor(workDir)
	if err != nil {
		return "", err
	}
	return dirs[0], nil
}

func claudeProjectDirsFor(workDir string) ([]string, error) {
	hash, err := claudeProjectHashFor(workDir)
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting home dir: %w", err)
	}
	return []string{
		filepath.Join(home, claudeProjectsDir, hash),
		filepath.Join(home, ".claude-anthropic", "projects", hash),
	}, nil
}

func claudeProjectDirsForConfigDirs(workDir string, configDirs ...string) ([]string, error) {
	dirs, err := claudeProjectDirsFor(workDir)
	if err != nil {
		return nil, err
	}
	hash, err := claudeProjectHashFor(workDir)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(dirs)+len(configDirs))
	unique := make([]string, 0, len(dirs)+len(configDirs))
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		unique = append(unique, dir)
	}
	for _, dir := range dirs {
		add(dir)
	}
	for _, configDir := range configDirs {
		configDir = strings.TrimSpace(configDir)
		if configDir == "" {
			continue
		}
		if strings.HasPrefix(configDir, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				configDir = filepath.Join(home, strings.TrimPrefix(configDir, "~/"))
			}
		}
		add(filepath.Join(configDir, "projects", hash))
	}
	return unique, nil
}

func claudeProjectHashFor(workDir string) (string, error) {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("resolving absolute path: %w", err)
	}
	// Normalize to forward slashes (no-op on Unix).
	normalized := filepath.ToSlash(abs)
	// Strip Windows drive letter prefix (e.g. "C:") so the hash matches
	// what Claude Code stores on Windows (hash starts with '-', not 'C:').
	if len(normalized) >= 2 && normalized[1] == ':' {
		normalized = normalized[2:]
	}
	return strings.ReplaceAll(normalized, "/", "-"), nil
}

// waitForNewestJSONL polls projectDir until a qualifying .jsonl file appears.
// "Qualifying" means mod time >= since (or any file if since is zero).
// Returns the path of the most recently modified qualifying file.
func waitForNewestJSONL(ctx context.Context, projectDir string, since time.Time) (string, error) {
	deadline := time.Now().Add(watchFileTimeout)
	for {
		if path, ok := newestJSONLIn(projectDir, since); ok {
			return path, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timeout: no JSONL file appeared in %s within %s", projectDir, watchFileTimeout)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(watchPollInterval):
		}
	}
}

// newestJSONLIn returns the most recently modified .jsonl file in dir whose
// modification time is >= since (skip if since is zero).
func newestJSONLIn(dir string, since time.Time) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	var bestPath string
	var bestTime time.Time
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// Skip files older than the Gas Town session start — they belong to
		// previous Claude sessions or unrelated Claude instances.
		if !since.IsZero() && info.ModTime().Before(since) {
			continue
		}
		if bestPath == "" || info.ModTime().After(bestTime) {
			bestPath = filepath.Join(dir, e.Name())
			bestTime = info.ModTime()
		}
	}
	return bestPath, bestPath != ""
}

// nativeSessionIDFromPath extracts the Claude Code session UUID from a JSONL file path.
// The filename is <uuid>.jsonl, so we strip the extension.
func nativeSessionIDFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, ".jsonl")
}

// tailJSONL reads all existing lines in path then polls for new ones, emitting
// AgentEvents on ch. It returns (without closing ch) when:
//   - a newer JSONL file appears in projectDir (new Claude session detected), or
//   - ctx is canceled.
//
// Callers loop back to waitForNewestJSONL after this returns to pick up the
// new session file. This handles Claude instances that are created and destroyed
// frequently: no events are lost because the file is tailed until we switch.
func tailJSONL(ctx context.Context, path, projectDir string, since time.Time, sessionID, agentType string, ch chan<- AgentEvent) {
	nativeID := nativeSessionIDFromPath(path)

	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 256*1024)
	var partial strings.Builder

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			partial.WriteString(line)
		}
		if err == nil || (err == io.EOF && strings.HasSuffix(partial.String(), "\n")) {
			fullLine := strings.TrimRight(partial.String(), "\r\n")
			partial.Reset()
			if fullLine != "" {
				for _, ev := range parseClaudeCodeLine(fullLine, sessionID, agentType, nativeID) {
					select {
					case ch <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
		}
		if err == io.EOF {
			// At EOF: check every poll whether a newer file has appeared.
			// This detects new Claude sessions within one poll interval (500ms).
			if newer, ok := newestJSONLIn(projectDir, since); ok && newer != path {
				return // newer Claude session detected — caller switches
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(watchPollInterval):
			}
		} else if err != nil {
			return // unexpected read error
		}
	}
}

// ── Claude Code JSONL structures ──────────────────────────────────────────────

// ccEntry is a top-level line in a Claude Code JSONL file.
type ccEntry struct {
	Type      string     `json:"type"`
	Message   *ccMessage `json:"message,omitempty"`
	Timestamp string     `json:"timestamp,omitempty"`
}

// ccMessage is the message field of a ccEntry.
type ccMessage struct {
	Role       string      `json:"role"`
	Content    []ccContent `json:"content"`
	Usage      *ccUsage    `json:"usage,omitempty"`
	StopReason string      `json:"stop_reason,omitempty"`
}

// ccUsage holds Claude API token usage counts for an assistant turn.
type ccUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// ccContent is one content block inside a ccMessage.
type ccContent struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// thinking (extended thinking models only)
	Thinking string `json:"thinking,omitempty"`

	// tool_use
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result (content is a string in the simple case)
	Content string `json:"content,omitempty"`
}

// parseClaudeCodeLine parses one JSONL line and returns 0 or more AgentEvents.
func parseClaudeCodeLine(line, sessionID, agentType, nativeSessionID string) []AgentEvent {
	var entry ccEntry
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return nil
	}
	// Only emit events for real conversation turns.
	if entry.Type != "assistant" && entry.Type != "user" {
		return nil
	}
	if entry.Message == nil {
		return nil
	}

	ts := time.Now()
	if entry.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil {
			ts = t
		}
	}

	var events []AgentEvent
	for _, c := range entry.Message.Content {
		var eventType, content string
		switch c.Type {
		case "text":
			eventType = "text"
			content = c.Text
		case "thinking":
			eventType = "thinking"
			content = c.Thinking
		case "tool_use":
			eventType = "tool_use"
			// Log tool name + full JSON input.
			content = c.Name + ": " + string(c.Input)
		case "tool_result":
			eventType = "tool_result"
			content = c.Content
		default:
			continue
		}
		if content == "" {
			continue
		}
		events = append(events, AgentEvent{
			AgentType:       agentType,
			SessionID:       sessionID,
			NativeSessionID: nativeSessionID,
			EventType:       eventType,
			Role:            entry.Message.Role,
			Content:         content,
			Timestamp:       ts,
		})
	}

	// Emit a dedicated "usage" event once per assistant turn so token counts
	// are not duplicated across content blocks of the same message.
	// Check all four token fields: a cache-only turn has InputTokens == 0 and
	// OutputTokens == 0 but non-zero CacheReadInputTokens, which must still be
	// recorded for accurate cost accounting.
	if entry.Type == "assistant" && entry.Message.Usage != nil {
		u := entry.Message.Usage
		if u.InputTokens > 0 || u.OutputTokens > 0 || u.CacheReadInputTokens > 0 || u.CacheCreationInputTokens > 0 {
			events = append(events, AgentEvent{
				AgentType:           agentType,
				SessionID:           sessionID,
				NativeSessionID:     nativeSessionID,
				EventType:           "usage",
				Role:                "assistant",
				Timestamp:           ts,
				InputTokens:         u.InputTokens,
				OutputTokens:        u.OutputTokens,
				CacheReadTokens:     u.CacheReadInputTokens,
				CacheCreationTokens: u.CacheCreationInputTokens,
			})
		}
	}

	return events
}

// MaxTokensDeadloopReport summarizes recent Claude assistant turns that ended
// with stop_reason=max_tokens while producing no non-empty text.
type MaxTokensDeadloopReport struct {
	Detected     bool
	Consecutive  int
	Required     int
	ScannedTurns int
	LastPath     string
	LastTime     time.Time
}

// CheckClaudeMaxTokensDeadloop scans the newest recent Claude Code JSONL
// transcript for consecutive max_tokens empty-text assistant turns in workDir.
func CheckClaudeMaxTokensDeadloop(workDir string, required int, since time.Time, configDirs ...string) (*MaxTokensDeadloopReport, error) {
	if required <= 0 {
		required = 1
	}
	report := &MaxTokensDeadloopReport{Required: required}
	projectDirs, err := claudeProjectDirsForConfigDirs(workDir, configDirs...)
	if err != nil {
		return report, err
	}

	type transcriptFile struct {
		path    string
		modTime time.Time
	}
	var newest *transcriptFile
	for _, dir := range projectDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return report, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			info, err := entry.Info()
			if err == nil && !since.IsZero() && info.ModTime().Before(since) {
				continue
			}
			if err != nil {
				continue
			}
			candidate := transcriptFile{path: path, modTime: info.ModTime()}
			if newest == nil || candidate.modTime.After(newest.modTime) || (candidate.modTime.Equal(newest.modTime) && candidate.path > newest.path) {
				newest = &candidate
			}
		}
	}
	if newest == nil {
		return report, nil
	}
	report.LastPath = newest.path
	f, err := os.Open(newest.path)
	if err != nil {
		return report, nil
	}
	err = scanClaudeMaxTokensDeadloop(f, newest.path, required, since, report)
	_ = f.Close()
	if err != nil {
		return report, err
	}
	report.Detected = report.Consecutive >= required
	return report, nil
}

func scanClaudeMaxTokensDeadloop(r io.Reader, path string, required int, since time.Time, report *MaxTokensDeadloopReport) error {
	reader := bufio.NewReaderSize(r, 256*1024)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			scanClaudeMaxTokensDeadloopLine(strings.TrimRight(line, "\r\n"), path, required, since, report)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func scanClaudeMaxTokensDeadloopLine(line, path string, required int, since time.Time, report *MaxTokensDeadloopReport) {
	if line == "" {
		return
	}
	var entry ccEntry
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return
	}
	if entry.Type != "assistant" || entry.Message == nil || entry.Message.Role != "assistant" {
		return
	}
	ts := time.Time{}
	if entry.Timestamp != "" {
		if parsed, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil {
			ts = parsed
		}
	}
	if !since.IsZero() {
		if ts.IsZero() || ts.Before(since) {
			return
		}
	}

	report.ScannedTurns++
	if entry.Message.StopReason == "max_tokens" && !claudeMessageHasNonEmptyText(entry.Message) {
		report.Consecutive++
		report.LastPath = path
		report.LastTime = ts
		if report.Consecutive >= required {
			report.Detected = true
		}
		return
	}
	report.Consecutive = 0
}

func claudeMessageHasNonEmptyText(msg *ccMessage) bool {
	for _, content := range msg.Content {
		if content.Type == "text" && strings.TrimSpace(content.Text) != "" {
			return true
		}
	}
	return false
}
