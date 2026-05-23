package polecat

import "testing"

func TestResolveWorkstateDisposition(t *testing.T) {
	base := WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean}
	tests := []struct {
		name string
		in   WorkstateInput
		want Disposition
	}{
		{name: "available clean", in: base, want: DispositionAvailableClean},
		{name: "active work", in: WorkstateInput{State: StateWorking, CleanupStatus: CleanupClean}, want: DispositionActiveWork},
		{name: "active assigned hooked", in: WorkstateInput{State: StateWorking, HookBead: "gt-work", CleanupStatus: CleanupClean}, want: DispositionActiveWork},
		{name: "pinned only", in: WorkstateInput{State: StateIdle, HookBead: "gt-pinned", CleanupStatus: CleanupClean}, want: DispositionReconcileMetadata},
		{name: "stale pinned", in: WorkstateInput{State: StateIdle, HookBead: "gt-stale", CleanupStatus: CleanupClean}, want: DispositionReconcileMetadata},
		{name: "submitted preserved active mr", in: WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, ActiveMR: "mr-1", ActiveMRBlocks: true}, want: DispositionSubmittedPreserved},
		{name: "submit required", in: WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, HasSubmittableWork: true}, want: DispositionSubmitRequired},
		{name: "recover local cleanup", in: WorkstateInput{State: StateIdle, CleanupStatus: CleanupUnpushed}, want: DispositionRecoverLocal},
		{name: "recovery blocked dirty", in: WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, GitDirty: true}, want: DispositionRecoverLocal},
		{name: "reconcile metadata", in: WorkstateInput{State: StateIdle, HookBead: "gt-work", CleanupStatus: CleanupClean}, want: DispositionReconcileMetadata},
		{name: "blocked unknown", in: WorkstateInput{State: StateIdle, CleanupStatus: CleanupClean, GitCheckFailed: true}, want: DispositionBlockedUnknown},
		{name: "orphan session unknown", in: WorkstateInput{State: StateZombie, CleanupStatus: CleanupClean}, want: DispositionBlockedUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveWorkstateDisposition(tt.in)
			if got.Disposition != tt.want {
				t.Fatalf("Disposition = %q, want %q", got.Disposition, tt.want)
			}
		})
	}
}

func TestDispositionPredicates(t *testing.T) {
	tests := []struct {
		disposition    Disposition
		reusable       bool
		slotOpen       bool
		safeToNuke     bool
		needsMQSubmit  bool
		needsRecovery  bool
		countsCapacity bool
	}{
		{disposition: DispositionAvailableClean, reusable: true, slotOpen: true, safeToNuke: true},
		{disposition: DispositionSubmittedPreserved, reusable: true, slotOpen: true},
		{disposition: DispositionSubmitRequired, needsMQSubmit: true, countsCapacity: true},
		{disposition: DispositionRecoverLocal, needsRecovery: true, countsCapacity: true},
		{disposition: DispositionActiveWork, countsCapacity: true},
		{disposition: DispositionReconcileMetadata, countsCapacity: true},
		{disposition: DispositionBlockedUnknown, countsCapacity: true},
	}

	for _, tt := range tests {
		t.Run(string(tt.disposition), func(t *testing.T) {
			if got := tt.disposition.Reusable(); got != tt.reusable {
				t.Fatalf("Reusable = %v, want %v", got, tt.reusable)
			}
			if got := tt.disposition.SlotOpenEligible(); got != tt.slotOpen {
				t.Fatalf("SlotOpenEligible = %v, want %v", got, tt.slotOpen)
			}
			if got := tt.disposition.SafeToNuke(); got != tt.safeToNuke {
				t.Fatalf("SafeToNuke = %v, want %v", got, tt.safeToNuke)
			}
			if got := tt.disposition.NeedsMQSubmit(); got != tt.needsMQSubmit {
				t.Fatalf("NeedsMQSubmit = %v, want %v", got, tt.needsMQSubmit)
			}
			if got := tt.disposition.NeedsRecovery(); got != tt.needsRecovery {
				t.Fatalf("NeedsRecovery = %v, want %v", got, tt.needsRecovery)
			}
			if got := tt.disposition.CountsAgainstCapacity(); got != tt.countsCapacity {
				t.Fatalf("CountsAgainstCapacity = %v, want %v", got, tt.countsCapacity)
			}
		})
	}
}
