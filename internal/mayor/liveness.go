package mayor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/nudge"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

// Default heartbeat age thresholds. Override via settings/config.json under
// operational.mayor.
const (
	defaultHeartbeatStaleThreshold     = 5 * time.Minute
	defaultHeartbeatVeryStaleThreshold = 20 * time.Minute
)

// Heartbeat represents the Mayor's heartbeat file contents.
// Mirrors deacon.Heartbeat so the daemon can reuse staleness logic.
type Heartbeat struct {
	// Timestamp is when the heartbeat was written.
	Timestamp time.Time `json:"timestamp"`

	// Cycle is the current wake cycle number.
	Cycle int64 `json:"cycle"`

	// LastAction describes what produced the heartbeat (e.g. "patrol-check").
	LastAction string `json:"last_action,omitempty"`

	// SessionStatus reports the tmux health status at the time of the heartbeat.
	SessionStatus string `json:"session_status,omitempty"`
}

// HeartbeatFile returns the path to the Mayor heartbeat file.
func HeartbeatFile(townRoot string) string {
	return filepath.Join(townRoot, constants.DirMayor, "heartbeat.json")
}

// WriteHeartbeat writes a new heartbeat to disk.
func WriteHeartbeat(townRoot string, hb *Heartbeat) error {
	hbFile := HeartbeatFile(townRoot)
	if err := os.MkdirAll(filepath.Dir(hbFile), 0755); err != nil {
		return err
	}
	if hb.Timestamp.IsZero() {
		hb.Timestamp = time.Now().UTC()
	}
	data, err := json.MarshalIndent(hb, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(hbFile, data, 0600)
}

// ReadHeartbeat reads the Mayor heartbeat from disk.
// Returns nil if the file doesn't exist or can't be parsed.
func ReadHeartbeat(townRoot string) *Heartbeat {
	hbFile := HeartbeatFile(townRoot)
	data, err := os.ReadFile(hbFile) //nolint:gosec // G304: path constructed from trusted townRoot
	if err != nil {
		return nil
	}
	var hb Heartbeat
	if err := json.Unmarshal(data, &hb); err != nil {
		return nil
	}
	return &hb
}

// Age returns how old the heartbeat is. A nil heartbeat is treated as very old.
func (hb *Heartbeat) Age() time.Duration {
	if hb == nil {
		return 24 * time.Hour * 365
	}
	return time.Since(hb.Timestamp)
}

// IsFresh returns true if the heartbeat is younger than the stale threshold.
func (hb *Heartbeat) IsFresh(staleThreshold time.Duration) bool {
	if hb == nil || staleThreshold <= 0 {
		return false
	}
	return hb.Age() < staleThreshold
}

// IsStale returns true if the heartbeat is older than stale but not very stale.
func (hb *Heartbeat) IsStale(staleThreshold, veryStaleThreshold time.Duration) bool {
	if hb == nil {
		return false
	}
	age := hb.Age()
	return age >= staleThreshold && age < veryStaleThreshold
}

// IsVeryStale returns true if the heartbeat is older than the very-stale threshold.
func (hb *Heartbeat) IsVeryStale(veryStaleThreshold time.Duration) bool {
	if hb == nil {
		return true
	}
	return hb.Age() >= veryStaleThreshold
}

// Touch writes a minimal heartbeat, incrementing the cycle and recording action.
func Touch(townRoot, action, status string) error {
	existing := ReadHeartbeat(townRoot)
	cycle := int64(1)
	if existing != nil {
		cycle = existing.Cycle + 1
	}
	return WriteHeartbeat(townRoot, &Heartbeat{
		Timestamp:     time.Now().UTC(),
		Cycle:         cycle,
		LastAction:    action,
		SessionStatus: status,
	})
}

// IsHealthy checks the Mayor tmux session using the same three-level check that
// Witness uses: session existence, agent liveness, and activity staleness.
func (m *Manager) IsHealthy(maxInactivity time.Duration) tmux.ZombieStatus {
	t := tmux.NewTmux()
	return t.CheckSessionHealth(m.SessionName(), maxInactivity)
}

// IsPollerAlive reports whether the nudge-queue poller for the Mayor session is running.
func (m *Manager) IsPollerAlive() (int, bool) {
	return nudge.IsPollerAlive(m.townRoot, m.SessionName())
}

// ContextSnapshot captures the Mayor's current hook and mail state so it can be
// preserved across a self-restart. This mirrors the git snapshot that the scoped
// restart runner records before a polecat recovery.
type ContextSnapshot struct {
	// HookBead is the ID of the bead currently on the Mayor's hook, if any.
	HookBead string `json:"hook_bead,omitempty"`

	// HookedCount is the number of beads assigned to the Mayor.
	HookedCount int `json:"hooked_count"`

	// UnreadMailCount is the total unread mail for the Mayor.
	UnreadMailCount int `json:"unread_mail_count"`

	// CriticalMailCount is the number of unread high/urgent messages.
	CriticalMailCount int `json:"critical_mail_count"`

	// Timestamp is when the snapshot was captured.
	Timestamp time.Time `json:"timestamp"`
}

// CaptureContextSnapshot records the Mayor's hook and mail context.
// gtPath is the path to the gt binary used to query hook and inbox.
func CaptureContextSnapshot(townRoot, gtPath string) (*ContextSnapshot, error) {
	snap := &ContextSnapshot{Timestamp: time.Now().UTC()}

	if gtPath == "" {
		gtPath = "gt"
	}

	hookBead, hookedCount, err := queryMayorHook(townRoot, gtPath)
	if err == nil {
		snap.HookBead = hookBead
		snap.HookedCount = hookedCount
	}

	unread, critical, err := queryMayorCriticalMail(townRoot, gtPath)
	if err == nil {
		snap.UnreadMailCount = unread
		snap.CriticalMailCount = critical
	}

	return snap, nil
}

// queryMayorHook runs `gt hook --json` and returns the hooked bead ID and count.
func queryMayorHook(townRoot, gtPath string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, gtPath, "hook", "--json")
	cmd.Dir = townRoot
	output, err := cmd.Output()
	if err != nil {
		return "", 0, fmt.Errorf("gt hook: %w", err)
	}

	var payload struct {
		BeadID string `json:"bead_id"`
		ID     string `json:"id"`
	}
	if err := json.Unmarshal(output, &payload); err != nil {
		return "", 0, err
	}
	beadID := payload.BeadID
	if beadID == "" {
		beadID = payload.ID
	}
	count := 0
	if beadID != "" {
		count = 1
	}
	return beadID, count, nil
}

// queryMayorCriticalMail runs `gt mail inbox --identity mayor/ --unread --json`
// and returns total unread plus high/urgent unread counts.
func queryMayorCriticalMail(townRoot, gtPath string) (int, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, gtPath, "mail", "inbox", "--identity", "mayor/", "--unread", "--json")
	cmd.Dir = townRoot
	output, err := cmd.Output()
	if err != nil {
		return 0, 0, fmt.Errorf("gt mail inbox: %w", err)
	}

	var messages []struct {
		Priority string `json:"priority"`
	}
	if err := json.Unmarshal(output, &messages); err != nil {
		return 0, 0, err
	}

	total := len(messages)
	critical := 0
	for _, m := range messages {
		switch strings.ToLower(m.Priority) {
		case "high", "urgent":
			critical++
		}
	}
	return total, critical, nil
}

// RecoveryAttempt records a single mayor self-restart supervision event.
type RecoveryAttempt struct {
	// Timestamp is when the attempt was recorded.
	Timestamp time.Time `json:"timestamp"`

	// Reason is the supervision signal that triggered the attempt (e.g. "agent-hung").
	Reason string `json:"reason"`

	// ContextSnapshot is the hook/mail state captured before restart.
	ContextSnapshot *ContextSnapshot `json:"context_snapshot,omitempty"`

	// GitSnapshot records git state before restart.
	GitSnapshot *GitSnapshot `json:"git_snapshot,omitempty"`

	// Verification is the post-restart identity/model verification result.
	Verification *ModelVerification `json:"verification,omitempty"`

	// Error is recorded if the attempt failed.
	Error string `json:"error,omitempty"`
}

// GitSnapshot captures git state from the mayor working directory before a restart.
type GitSnapshot struct {
	WorkingDir string `json:"working_dir"`
	HEAD       string `json:"head"`
	Branch     string `json:"branch"`
	Status     string `json:"status"`
}

// CaptureGitSnapshot records git state for the mayor directory.
func CaptureGitSnapshot(townRoot string) *GitSnapshot {
	mayorDir := filepath.Join(townRoot, constants.DirMayor)
	snap := &GitSnapshot{WorkingDir: mayorDir}

	if head, err := gitOutput(mayorDir, "rev-parse", "HEAD"); err == nil {
		snap.HEAD = head
	}
	if branch, err := gitOutput(mayorDir, "branch", "--show-current"); err == nil {
		snap.Branch = branch
	}
	if status, err := gitOutput(mayorDir, "status", "--short", "--branch"); err == nil {
		snap.Status = status
	}
	return snap
}

func gitOutput(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ModelVerification captures the result of verifying the Mayor's resolved model after restart.
type ModelVerification struct {
	// ExpectedModel is the model name resolved from settings for the mayor role.
	ExpectedModel string `json:"expected_model"`

	// ActualModel is the model reported by gt model-status.
	ActualModel string `json:"actual_model"`

	// Verified is true if expected and actual match.
	Verified bool `json:"verified"`

	// Error is non-empty if verification could not be performed.
	Error string `json:"error,omitempty"`
}

// VerifyMayorModel checks that the running Mayor session is using the expected
// agent/model. It compares the configured role agent with the output of
// `gt model-status`, when available.
func VerifyMayorModel(townRoot, gtPath string) *ModelVerification {
	result := &ModelVerification{}

	mayorDir := filepath.Join(townRoot, constants.DirMayor)
	rc := config.ResolveRoleAgentConfig("mayor", townRoot, mayorDir)
	if rc != nil && rc.ResolvedAgent != "" {
		result.ExpectedModel = rc.ResolvedAgent
	}

	if gtPath == "" {
		gtPath = "gt"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, gtPath, "model-status", "--json")
	cmd.Dir = townRoot
	output, err := cmd.Output()
	if err != nil {
		result.Error = fmt.Sprintf("model-status failed: %v", err)
		return result
	}

	actual := parseModelStatusOutput(output)
	result.ActualModel = actual
	if result.ExpectedModel != "" && actual != "" {
		result.Verified = strings.EqualFold(actual, result.ExpectedModel)
	}
	return result
}

// parseModelStatusOutput handles both JSON and plain-text model-status output.
func parseModelStatusOutput(output []byte) string {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return ""
	}

	if trimmed[0] == '{' {
		var payload struct {
			Model string `json:"model"`
			Name  string `json:"name"`
		}
		if err := json.Unmarshal(output, &payload); err == nil {
			if payload.Model != "" {
				return payload.Model
			}
			return payload.Name
		}
	}

	// Plain text: use first non-empty line.
	lines := strings.Split(trimmed, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// RecoveryAttemptsDir returns the directory where mayor recovery attempts are logged.
func RecoveryAttemptsDir(townRoot string) string {
	return filepath.Join(townRoot, constants.DirRuntime, "mayor-recovery-attempts")
}

// RecordRecoveryAttempt appends a recovery attempt record to the durable ledger.
func RecordRecoveryAttempt(townRoot string, attempt *RecoveryAttempt) error {
	if attempt.Timestamp.IsZero() {
		attempt.Timestamp = time.Now().UTC()
	}

	dir := RecoveryAttemptsDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating recovery ledger dir: %w", err)
	}

	data, err := json.Marshal(attempt)
	if err != nil {
		return fmt.Errorf("marshaling recovery attempt: %w", err)
	}

	filename := fmt.Sprintf("%s.jsonl", attempt.Timestamp.Format("20060102T150405Z"))
	path := filepath.Join(dir, filename)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening recovery ledger: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("writing recovery ledger: %w", err)
	}
	return nil
}

// Supervisor provides a bundled interface for daemon-driven Mayor supervision checks.
type Supervisor struct {
	townRoot string
	gtPath   string
}

// NewSupervisor creates a Supervisor for the given town root.
func NewSupervisor(townRoot, gtPath string) *Supervisor {
	return &Supervisor{
		townRoot: townRoot,
		gtPath:   gtPath,
	}
}

// RecordSuccess writes a heartbeat and resets the recovery ledger error state.
func (s *Supervisor) RecordSuccess(status string) error {
	return Touch(s.townRoot, "patrol-check", status)
}

// SnapshotContext captures the current hook/mail context.
func (s *Supervisor) SnapshotContext() (*ContextSnapshot, error) {
	return CaptureContextSnapshot(s.townRoot, s.gtPath)
}

// SnapshotGit captures git state from the mayor directory.
func (s *Supervisor) SnapshotGit() *GitSnapshot {
	return CaptureGitSnapshot(s.townRoot)
}

// VerifyModel runs post-restart model verification.
func (s *Supervisor) VerifyModel() *ModelVerification {
	return VerifyMayorModel(s.townRoot, s.gtPath)
}

// RecordRecovery records a recovery attempt to the durable ledger.
func (s *Supervisor) RecordRecovery(attempt *RecoveryAttempt) error {
	return RecordRecoveryAttempt(s.townRoot, attempt)
}

// CriticalMailBacklog returns the total unread and critical unread counts for the Mayor.
func CriticalMailBacklog(townRoot, gtPath string) (int, int, error) {
	return queryMayorCriticalMail(townRoot, gtPath)
}

// MayorSessionName returns the canonical tmux session name for the Mayor.
// This is a convenience wrapper for callers outside the package.
func MayorSessionName() string {
	return session.MayorSessionName()
}
