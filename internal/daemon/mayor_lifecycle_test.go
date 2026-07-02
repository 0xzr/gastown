package daemon

import (
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/mayor"
	"github.com/steveyegge/gastown/internal/tmux"
)

// TestEvaluateMayorHealth covers the resume-after-daemon-downtime guard added
// in gastown-7yd: a heartbeat that predates the daemon's start time must not
// count as a Mayor zombie, because the staleness is an artifact of the daemon
// being down, not evidence of a stuck Mayor.
func TestEvaluateMayorHealth(t *testing.T) {
	const veryStaleThreshold = 20 * time.Minute
	daemonStartedAt := time.Now().UTC()

	tests := []struct {
		name            string
		sessionStatus   tmux.ZombieStatus
		hb              *mayor.Heartbeat
		daemonStartedAt time.Time
		want            mayorHealthAction
	}{
		{
			name:            "session dead → zombie",
			sessionStatus:   tmux.SessionDead,
			hb:              freshHeartbeat(1 * time.Minute),
			daemonStartedAt: daemonStartedAt,
			want:            mayorHealthActionZombie,
		},
		{
			name:            "agent dead → zombie",
			sessionStatus:   tmux.AgentDead,
			hb:              freshHeartbeat(1 * time.Minute),
			daemonStartedAt: daemonStartedAt,
			want:            mayorHealthActionZombie,
		},
		{
			name:            "agent hung → zombie",
			sessionStatus:   tmux.AgentHung,
			hb:              freshHeartbeat(1 * time.Minute),
			daemonStartedAt: daemonStartedAt,
			want:            mayorHealthActionZombie,
		},
		{
			name:            "missing heartbeat → refresh",
			sessionStatus:   tmux.SessionHealthy,
			hb:              nil,
			daemonStartedAt: daemonStartedAt,
			want:            mayorHealthActionRefresh,
		},
		{
			name:            "fresh heartbeat → idle",
			sessionStatus:   tmux.SessionHealthy,
			hb:              freshHeartbeat(1 * time.Minute),
			daemonStartedAt: daemonStartedAt,
			want:            mayorHealthActionIdle,
		},
		{
			name:            "stale-but-not-very-stale heartbeat → idle",
			sessionStatus:   tmux.SessionHealthy,
			hb:              freshHeartbeat(10 * time.Minute),
			daemonStartedAt: daemonStartedAt,
			want:            mayorHealthActionIdle,
		},
		{
			// gastown-7yd core fix: a healthy Mayor with a heartbeat from BEFORE
			// this daemon started must not be restarted. The staleness is an
			// artifact of the daemon being down.
			name:            "very stale heartbeat predating daemon start → refresh",
			sessionStatus:   tmux.SessionHealthy,
			hb:              staleHeartbeatBefore(daemonStartedAt, 1*time.Hour),
			daemonStartedAt: daemonStartedAt,
			want:            mayorHealthActionRefresh,
		},
		{
			// A very stale heartbeat written DURING this daemon's run means the
			// supervision path itself is stuck. That IS a real Mayor zombie.
			// Construct daemonStartedAt 1 hour ago so a 30-minute-old heartbeat
			// is unambiguously "after this daemon's start" while still very stale.
			name:            "very stale heartbeat after daemon start → zombie",
			sessionStatus:   tmux.SessionHealthy,
			hb:              freshHeartbeat(30 * time.Minute),
			daemonStartedAt: daemonStartedAt.Add(-1 * time.Hour),
			want:            mayorHealthActionZombie,
		},
		{
			// Zero daemonStartedAt disables the resume guard; a very stale
			// heartbeat is then treated as a zombie regardless of provenance.
			name:            "very stale heartbeat with zero daemonStartedAt → zombie",
			sessionStatus:   tmux.SessionHealthy,
			hb:              freshHeartbeat(1 * time.Hour),
			daemonStartedAt: time.Time{},
			want:            mayorHealthActionZombie,
		},
		{
			// Session dead AND heartbeat missing — still zombie (session health
			// dominates).
			name:            "session dead, heartbeat missing → zombie",
			sessionStatus:   tmux.SessionDead,
			hb:              nil,
			daemonStartedAt: daemonStartedAt,
			want:            mayorHealthActionZombie,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateMayorHealth(tc.sessionStatus, tc.hb, veryStaleThreshold, tc.daemonStartedAt)
			if got != tc.want {
				t.Errorf("evaluateMayorHealth() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEvaluateMayorHealth_IsHealthyZeroStaysHealthy verifies the post-2ea29bf9
// invariant that IsHealthy(0) (which skips tmux activity checking) is treated
// as healthy for a Mayor that is alive at the prompt. The bug was: a healthy
// Mayor with a stale heartbeat from before daemon start would be counted as a
// zombie after 3 patrol cycles, triggering an unnecessary supervised restart.
func TestEvaluateMayorHealth_IsHealthyZeroStaysHealthy(t *testing.T) {
	veryStaleThreshold := 20 * time.Minute
	daemonStartedAt := time.Now().UTC()

	// Simulate a Mayor that's been alive for hours (heartbeat timestamp from
	// well before the daemon started). This is exactly the gastown-7yd scenario:
	// daemon goes down, Mayor keeps running, daemon comes back up, sees a
	// very-stale heartbeat.
	hb := staleHeartbeatBefore(daemonStartedAt, 2*time.Hour)

	if got := evaluateMayorHealth(tmux.SessionHealthy, hb, veryStaleThreshold, daemonStartedAt); got != mayorHealthActionRefresh {
		t.Fatalf("evaluateMayorHealth(SessionHealthy, hb-before-start) = %v, want %v (refresh — must NOT count as zombie)",
			got, mayorHealthActionRefresh)
	}
}

// TestMayorUnhealthReason verifies the human-readable reason text used in logs
// and the restart reason. The text is what an operator sees when investigating
// a restart, so the right explanation must surface for each signal.
func TestMayorUnhealthReason(t *testing.T) {
	if got := mayorUnhealthReason(tmux.SessionDead, freshHeartbeat(1*time.Minute)); got != "session-dead" {
		t.Errorf("reason for SessionDead = %q, want session-dead", got)
	}
	hb := freshHeartbeat(1 * time.Hour)
	got := mayorUnhealthReason(tmux.SessionHealthy, hb)
	if want := "heartbeat very stale (age="; !startsWith(got, want) {
		t.Errorf("reason for stale heartbeat = %q, want prefix %q", got, want)
	}
	if got := mayorUnhealthReason(tmux.SessionHealthy, nil); got != "unknown" {
		t.Errorf("reason for nil inputs = %q, want unknown", got)
	}
}

func TestEvaluateMayorCriticalMailBacklog(t *testing.T) {
	now := time.Date(2026, 7, 2, 5, 0, 0, 0, time.UTC)
	since := now.Add(-31 * time.Minute)

	tests := []struct {
		name          string
		critical      int
		threshold     int
		since         time.Time
		restartAfter  time.Duration
		wantActive    bool
		wantSinceZero bool
		wantRestart   bool
	}{
		{
			name:          "below threshold resets",
			critical:      4,
			threshold:     5,
			since:         since,
			restartAfter:  30 * time.Minute,
			wantActive:    false,
			wantSinceZero: true,
			wantRestart:   false,
		},
		{
			name:          "first threshold breach starts timer",
			critical:      5,
			threshold:     5,
			since:         time.Time{},
			restartAfter:  30 * time.Minute,
			wantActive:    true,
			wantSinceZero: false,
			wantRestart:   false,
		},
		{
			name:          "threshold breach before grace does not restart",
			critical:      7,
			threshold:     5,
			since:         now.Add(-10 * time.Minute),
			restartAfter:  30 * time.Minute,
			wantActive:    true,
			wantSinceZero: false,
			wantRestart:   false,
		},
		{
			name:          "threshold breach after grace restarts",
			critical:      7,
			threshold:     5,
			since:         since,
			restartAfter:  30 * time.Minute,
			wantActive:    true,
			wantSinceZero: false,
			wantRestart:   true,
		},
		{
			name:          "zero restart grace only alerts",
			critical:      7,
			threshold:     5,
			since:         since,
			restartAfter:  0,
			wantActive:    true,
			wantSinceZero: false,
			wantRestart:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			active, nextSince, restart := evaluateMayorCriticalMailBacklog(tc.critical, tc.threshold, tc.since, now, tc.restartAfter)
			if active != tc.wantActive {
				t.Fatalf("active = %v, want %v", active, tc.wantActive)
			}
			if nextSince.IsZero() != tc.wantSinceZero {
				t.Fatalf("nextSince zero = %v, want %v (nextSince=%v)", nextSince.IsZero(), tc.wantSinceZero, nextSince)
			}
			if restart != tc.wantRestart {
				t.Fatalf("restart = %v, want %v", restart, tc.wantRestart)
			}
		})
	}
}

func TestMarkMayorStartedResetsCriticalMailGrace(t *testing.T) {
	started := time.Date(2026, 7, 2, 5, 0, 0, 0, time.UTC)
	oldBreach := started.Add(-31 * time.Minute)
	d := &Daemon{
		mayorLastStarted:              started.Add(-2 * time.Hour),
		mayorCriticalMailBacklogSince: oldBreach,
	}

	d.markMayorStarted(started)

	if !d.mayorLastStarted.Equal(started) {
		t.Fatalf("mayorLastStarted = %v, want %v", d.mayorLastStarted, started)
	}
	if !d.mayorCriticalMailBacklogSince.IsZero() {
		t.Fatalf("mayorCriticalMailBacklogSince = %v, want zero", d.mayorCriticalMailBacklogSince)
	}

	active, nextSince, restart := evaluateMayorCriticalMailBacklog(7, 5, d.mayorCriticalMailBacklogSince, started.Add(3*time.Minute), 30*time.Minute)
	if !active {
		t.Fatalf("active = false, want true")
	}
	if restart {
		t.Fatalf("restart = true, want false for a freshly started mayor")
	}
	if nextSince.IsZero() {
		t.Fatalf("nextSince is zero, want a fresh grace timer")
	}
	if nextSince.Sub(started) != 3*time.Minute {
		t.Fatalf("nextSince = %v, want first post-start check time", nextSince)
	}
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// freshHeartbeat returns a heartbeat with the given age relative to now.
func freshHeartbeat(age time.Duration) *mayor.Heartbeat {
	return &mayor.Heartbeat{
		Timestamp:     time.Now().Add(-age),
		Cycle:         1,
		LastAction:    "patrol-check",
		SessionStatus: "healthy",
	}
}

// staleHeartbeatBefore returns a heartbeat whose timestamp is `age` older than
// `start`. Used to simulate heartbeats written by a previous daemon incarnation.
func staleHeartbeatBefore(start time.Time, age time.Duration) *mayor.Heartbeat {
	return &mayor.Heartbeat{
		Timestamp:     start.Add(-age),
		Cycle:         1,
		LastAction:    "patrol-check",
		SessionStatus: "healthy",
	}
}
