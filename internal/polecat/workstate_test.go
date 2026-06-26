package polecat

import "testing"

func TestDecideWorkstateCanonicalFields(t *testing.T) {
	tests := []struct {
		name string
		in   WorkstateInput
		want WorkstateDisposition
	}{
		{
			name: "clean idle is reusable and safe",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "main"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, ReuseStatus: "idle-clean"},
		},
		{
			name: "dirty idle needs recovery and capacity",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupUnpushed},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsRecovery, Reason: "cleanup-has_unpushed", NeedsRecovery: true, CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed"},
		},
		{
			name: "unsubmitted branch needs mq submit",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", MQCheckRequired: true, HasSubmittableWork: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictNeedsMQSubmit, Reason: "mq-not-submitted", NeedsRecovery: true, NeedsMQSubmit: true, MQStatus: "not_submitted", CountsTowardCapacity: true, ReuseStatus: "idle-recovery-needed"},
		},
		{
			name: "terminal source makes mq submitted",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, Branch: "polecat/test", MQCheckRequired: true, HasSubmittableWork: true, AssignedBeadTerminal: true},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, MQStatus: "submitted", ReuseStatus: "idle-preserved"},
		},
		{
			name: "terminal active mr does not block when gatherer omits blocker",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, ActiveMR: "gt-mr-closed"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, ReuseStatus: "idle-clean"},
		},
		{
			name: "open active mr is preserved pending mr",
			in:   WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, ActiveMR: "gt-mr-open", ActiveMRBlocker: "active_mr=gt-mr-open status=open"},
			want: WorkstateDisposition{Verdict: WorkstateVerdictPendingMR, Reason: "active-mr-open", ReuseStatus: "idle-pr-open"},
		},
		{
			name: "working counts as working capacity",
			in:   WorkstateInput{State: StateWorking, CleanupStatus: CleanupClean},
			want: WorkstateDisposition{Verdict: WorkstateVerdictWorking, Reason: "working", NeedsRecovery: false, CountsTowardCapacity: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecideWorkstate(tt.in)
			if got.Verdict != tt.want.Verdict || got.Reason != tt.want.Reason || got.Reusable != tt.want.Reusable || got.SafeToNuke != tt.want.SafeToNuke || got.NeedsRecovery != tt.want.NeedsRecovery || got.NeedsMQSubmit != tt.want.NeedsMQSubmit || got.MQStatus != tt.want.MQStatus || got.CountsTowardCapacity != tt.want.CountsTowardCapacity || got.ReuseStatus != tt.want.ReuseStatus {
				t.Fatalf("DecideWorkstate() = %+v, want fields %+v", got, tt.want)
			}
		})
	}
}

func TestCanIgnoreStaleCleanupStatus(t *testing.T) {
	cases := []struct {
		name            string
		status          CleanupStatus
		workTerminal    bool
		hookSafe        bool
		activeMRSafe    bool
		gitClean        bool
		unpushedCommits int
		want            bool
	}{
		{
			name:         "all safe clean ignores uncommitted",
			status:       CleanupUncommitted,
			workTerminal: true, hookSafe: true, activeMRSafe: true, gitClean: true,
			want: true,
		},
		{
			name:         "unpushed preserved can be ignored when terminal",
			status:       CleanupUnpushed,
			workTerminal: true, hookSafe: true, activeMRSafe: true, gitClean: true, unpushedCommits: 0,
			want: true,
		},
		{
			name:         "unpushed with unpreserved commits cannot be ignored",
			status:       CleanupUnpushed,
			workTerminal: true, hookSafe: true, activeMRSafe: true, gitClean: true, unpushedCommits: 2,
			want: false,
		},
		{
			name:         "unpushed with dirty tree cannot be ignored",
			status:       CleanupUnpushed,
			workTerminal: true, hookSafe: true, activeMRSafe: true, gitClean: false, unpushedCommits: 0,
			want: false,
		},
		{
			name:         "non-terminal work cannot be ignored",
			status:       CleanupUnpushed,
			workTerminal: false, hookSafe: true, activeMRSafe: true, gitClean: true, unpushedCommits: 0,
			want: false,
		},
		{
			name:         "unsafe hook cannot be ignored",
			status:       CleanupUncommitted,
			workTerminal: true, hookSafe: false, activeMRSafe: true, gitClean: true,
			want: false,
		},
		{
			name:         "pending active MR cannot be ignored",
			status:       CleanupUncommitted,
			workTerminal: true, hookSafe: true, activeMRSafe: false, gitClean: true,
			want: false,
		},
		{
			name:         "clean status needs no ignore",
			status:       CleanupClean,
			workTerminal: true, hookSafe: true, activeMRSafe: true, gitClean: true,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CanIgnoreStaleCleanupStatus(tc.status, tc.workTerminal, tc.hookSafe, tc.activeMRSafe, tc.gitClean, tc.unpushedCommits)
			if got != tc.want {
				t.Errorf("CanIgnoreStaleCleanupStatus(%v, %v, %v, %v, %v, %d) = %v, want %v",
					tc.status, tc.workTerminal, tc.hookSafe, tc.activeMRSafe, tc.gitClean, tc.unpushedCommits, got, tc.want)
			}
		})
	}
}

func TestDecideWorkstateLiveSignals(t *testing.T) {
	tests := []struct {
		name                 string
		in                   WorkstateInput
		wantVerdict          string
		wantReason           string
		wantNeedsRecovery    bool
		wantConfidence       string
		wantSignalSubstrings []string
	}{
		{
			name:                 "gastown-cet.9 regression: stalled but live and hooked is WORKING",
			in:                   WorkstateInput{State: StateStalled, HookBead: "gastown-cet.9", SessionRunning: true, HeartbeatExists: true, HeartbeatFresh: true, ProcessAlive: true},
			wantVerdict:          WorkstateVerdictWorking,
			wantReason:           "live-hooked",
			wantNeedsRecovery:    false,
			wantConfidence:       WorkstateConfidenceHigh,
			wantSignalSubstrings: []string{"state=stalled", "session_running=true", "process_alive=true", "hook_active"},
		},
		{
			name:              "live hooked polecat in review-needed is WORKING",
			in:                WorkstateInput{State: StateReviewNeeded, HookBead: "gastown-cet.9", SessionRunning: true, HeartbeatExists: true, HeartbeatFresh: true, ProcessAlive: true},
			wantVerdict:       WorkstateVerdictWorking,
			wantReason:        "live-hooked",
			wantNeedsRecovery: false,
			wantConfidence:    WorkstateConfidenceHigh,
		},
		{
			name:              "live session without hook stays in recovery (scope-creep removed)",
			in:                WorkstateInput{State: StateReviewNeeded, SessionRunning: true, HeartbeatExists: true, HeartbeatFresh: true, ProcessAlive: true},
			wantVerdict:       WorkstateVerdictNeedsRecovery,
			wantReason:        "stale-session",
			wantNeedsRecovery: true,
			wantConfidence:    WorkstateConfidenceHigh,
		},
		{
			name:                 "dead session with hook needs recovery",
			in:                   WorkstateInput{State: StateStalled, HookBead: "gastown-cet.9", SessionRunning: false, HeartbeatExists: true, HeartbeatFresh: false, ProcessAlive: false},
			wantVerdict:          WorkstateVerdictNeedsRecovery,
			wantReason:           "stale-session",
			wantNeedsRecovery:    true,
			wantConfidence:       WorkstateConfidenceHigh,
			wantSignalSubstrings: []string{"state=stalled", "heartbeat_fresh=false", "process_alive=false"},
		},
		{
			name:              "no liveness data preserves conservative not-idle fallback",
			in:                WorkstateInput{State: StateStalled, HookBead: "gastown-cet.9"},
			wantVerdict:       WorkstateVerdictNeedsRecovery,
			wantReason:        "no-liveness-data",
			wantNeedsRecovery: true,
			wantConfidence:    WorkstateConfidenceLow,
		},
		{
			name:                 "working with live signals has high confidence",
			in:                   WorkstateInput{State: StateWorking, SessionRunning: true, HeartbeatExists: true, HeartbeatFresh: true, ProcessAlive: true},
			wantVerdict:          WorkstateVerdictWorking,
			wantReason:           "working",
			wantNeedsRecovery:    false,
			wantConfidence:       WorkstateConfidenceHigh,
			wantSignalSubstrings: []string{"state=working", "session_running=true"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecideWorkstate(tt.in)
			if got.Verdict != tt.wantVerdict {
				t.Errorf("Verdict = %q, want %q", got.Verdict, tt.wantVerdict)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
			}
			if got.NeedsRecovery != tt.wantNeedsRecovery {
				t.Errorf("NeedsRecovery = %v, want %v", got.NeedsRecovery, tt.wantNeedsRecovery)
			}
			if got.Confidence != tt.wantConfidence {
				t.Errorf("Confidence = %q, want %q", got.Confidence, tt.wantConfidence)
			}
			for _, substr := range tt.wantSignalSubstrings {
				found := false
				for _, s := range got.Signals {
					if s == substr {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Signals = %v, missing %q", got.Signals, substr)
				}
			}
		})
	}
}
