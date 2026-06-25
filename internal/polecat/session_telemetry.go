package polecat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SessionTelemetry records the start and exit evidence for a polecat session.
// It is written when the session starts and enriched when it exits.
type SessionTelemetry struct {
	SessionID        string    `json:"session_id"`
	PolecatName      string    `json:"polecat_name"`
	RigName          string    `json:"rig_name"`
	HookBead         string    `json:"hook_bead,omitempty"`
	Command          string    `json:"command"`
	Model            string    `json:"model,omitempty"`
	SupervisorSource string    `json:"supervisor_source,omitempty"`
	StartTime        time.Time `json:"start_time"`
	TranscriptDir    string    `json:"transcript_dir,omitempty"`
	// Exit evidence (populated on stop/crash).
	ExitCode           int       `json:"exit_code,omitempty"`
	Signal             string    `json:"signal,omitempty"`
	ExitReason         string    `json:"exit_reason,omitempty"`
	ExitTime           time.Time `json:"exit_time,omitempty"`
	LastTranscriptPath string    `json:"last_transcript_path,omitempty"`
	LastTranscriptTail string    `json:"last_transcript_tail,omitempty"`
	LastPaneTail       string    `json:"last_pane_tail,omitempty"`
}

// SessionTelemetryDir returns the directory where session telemetry files live.
func SessionTelemetryDir(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "session-telemetry")
}

// SessionTelemetryPath returns the path for a given session ID.
func SessionTelemetryPath(townRoot, sessionID string) string {
	return filepath.Join(SessionTelemetryDir(townRoot), sessionID+".json")
}

// WriteSessionStartTelemetry persists initial session evidence.
func WriteSessionStartTelemetry(townRoot string, tel *SessionTelemetry) error {
	if townRoot == "" || tel.SessionID == "" {
		return nil // best-effort: nothing to record
	}
	tel.StartTime = time.Now().UTC()

	dir := SessionTelemetryDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating telemetry dir: %w", err)
	}
	path := SessionTelemetryPath(townRoot, tel.SessionID)
	data, err := json.MarshalIndent(tel, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling session telemetry: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil { //nolint:gosec // G302: non-sensitive operational data
		return fmt.Errorf("writing session telemetry: %w", err)
	}
	return nil
}

// LoadSessionTelemetry reads telemetry for a session.
func LoadSessionTelemetry(townRoot, sessionID string) (*SessionTelemetry, error) {
	path := SessionTelemetryPath(townRoot, sessionID)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tel SessionTelemetry
	if err := json.Unmarshal(data, &tel); err != nil {
		return nil, err
	}
	return &tel, nil
}

// RecordSessionExit updates the telemetry file with exit evidence and
// attempts to capture the tail of the session transcript.
func RecordSessionExit(townRoot, sessionID, supervisorSource string, exitCode int, sig, reason string) error {
	if townRoot == "" || sessionID == "" {
		return nil
	}
	tel, err := LoadSessionTelemetry(townRoot, sessionID)
	if err != nil {
		// No start telemetry — create a minimal record so exit evidence is not lost.
		tel = &SessionTelemetry{SessionID: sessionID}
	}
	tel.ExitTime = time.Now().UTC()
	tel.SupervisorSource = supervisorSource
	tel.ExitCode = exitCode
	tel.Signal = sig
	tel.ExitReason = reason

	if tel.TranscriptDir != "" {
		path, tail := latestTranscriptTail(tel.TranscriptDir, tel.StartTime, 50)
		if path != "" {
			tel.LastTranscriptPath = path
			tel.LastTranscriptTail = tail
		}
	}

	return WriteSessionStartTelemetry(townRoot, tel)
}

// latestTranscriptTail finds the newest JSONL file in transcriptDir that was
// modified after sessionStart and returns its path and last maxLines lines.
func latestTranscriptTail(transcriptDir string, sessionStart time.Time, maxLines int) (string, string) {
	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		return "", ""
	}
	type cand struct {
		path    string
		modTime time.Time
	}
	var candidates []cand
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// Only consider files modified at or after session start. This avoids
		// tails from prior sessions in the same project directory.
		if !info.ModTime().IsZero() && info.ModTime().Before(sessionStart) {
			continue
		}
		candidates = append(candidates, cand{
			path:    filepath.Join(transcriptDir, e.Name()),
			modTime: info.ModTime(),
		})
	}
	if len(candidates) == 0 {
		return "", ""
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})
	path := candidates[0].path
	tail := tailFileLines(path, maxLines)
	return path, tail
}

// UpdateSessionTelemetryLastPaneTail records the final captured pane output for
// a session. Best-effort: failures are silently ignored.
func UpdateSessionTelemetryLastPaneTail(townRoot, sessionID, tail string) error {
	if townRoot == "" || sessionID == "" {
		return nil
	}
	tel, err := LoadSessionTelemetry(townRoot, sessionID)
	if err != nil {
		return err
	}
	tel.LastPaneTail = tail
	return WriteSessionStartTelemetry(townRoot, tel)
}

// tailFileLines returns the last n lines of a file as a single string.
func tailFileLines(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	// Drop an empty trailing line so the tail is compact.
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}
