// gastown-cet.2.3 regression tests for the beads package terminal-state helpers.
//
// The source-bead guard lives in this package because the refinery (and any
// other production closure path) beads issue state directly. These tests pin
// the classification that drives CanCloseSourceBead.
package beads

import "testing"

func TestBeadsClassifyMRTerminalState_OpenIsPendingRefinery(t *testing.T) {
	mr := &Issue{Status: "open"}
	if got := ClassifyMRTerminalState(mr); got != MRTerminalPendingRefinery {
		t.Errorf("open MR classified as %q, want %q", got, MRTerminalPendingRefinery)
	}
	if CanCloseSourceBead(mr) {
		t.Error("CanCloseSourceBead=true for open MR — source would close prematurely")
	}
}

func TestBeadsClassifyMRTerminalState_InProgressIsPendingRefinery(t *testing.T) {
	mr := &Issue{Status: "in_progress"}
	if got := ClassifyMRTerminalState(mr); got != MRTerminalPendingRefinery {
		t.Errorf("in_progress MR classified as %q, want %q", got, MRTerminalPendingRefinery)
	}
	if CanCloseSourceBead(mr) {
		t.Error("CanCloseSourceBead=true for in_progress MR — source would close prematurely")
	}
}

func TestBeadsClassifyMRTerminalState_ClosedMergedNoPublished(t *testing.T) {
	mr := &Issue{
		Status: "closed",
		Description: `branch: polecat/quartz/gt-abc
	target: main
	source_issue: gt-abc
	commit_sha: b5a6a81600000000000000000000000000000000
	merge_commit: f752592e00000000000000000000000000000000
	close_reason: merged`,
	}
	got := ClassifyMRTerminalState(mr)
	if got != MRTerminalMergedLocalNotPublished {
		t.Fatalf("closed+merged MR with no PublishedCommit classified as %q, want %q", got, MRTerminalMergedLocalNotPublished)
	}
	if CanCloseSourceBead(mr) {
		t.Error("CanCloseSourceBead=true for merged-local-not-published MR — hq-6sdu regression")
	}
}

func TestBeadsClassifyMRTerminalState_ClosedMergedPublished(t *testing.T) {
	mr := &Issue{
		Status: "closed",
		Description: `branch: polecat/quartz/gt-abc
	target: main
	source_issue: gt-abc
	commit_sha: b5a6a81600000000000000000000000000000000
	merge_commit: f752592e00000000000000000000000000000000
	close_reason: merged
	published_commit: 7b076fc1000000000000000000000000000000000
	published_remote: origin
	published_at: 2026-06-24T17:30:00Z`,
	}
	got := ClassifyMRTerminalState(mr)
	if got != MRTerminalPublished {
		t.Fatalf("classified as %q, want %q", got, MRTerminalPublished)
	}
	if !CanCloseSourceBead(mr) {
		t.Error("CanCloseSourceBead=false for published MR — source would stay open forever")
	}
	if !IsMRTerminalPublished(mr) {
		t.Error("IsMRTerminalPublished=false for published MR")
	}
}

func TestBeadsClassifyMRTerminalState_ClosedRejected(t *testing.T) {
	mr := &Issue{
		Status: "closed",
		Description: `branch: polecat/quartz/gt-abc
	target: main
	close_reason: rejected`,
	}
	got := ClassifyMRTerminalState(mr)
	if got != MRTerminalRejectedNeedsRework {
		t.Errorf("classified as %q, want %q", got, MRTerminalRejectedNeedsRework)
	}
	if CanCloseSourceBead(mr) {
		t.Error("CanCloseSourceBead=true for rejected MR — source would close on rejection")
	}
}

func TestBeadsClassifyMRTerminalState_ClosedConflict(t *testing.T) {
	mr := &Issue{
		Status:      "closed",
		Description: "close_reason: conflict\n",
	}
	got := ClassifyMRTerminalState(mr)
	if got != MRTerminalRejectedNeedsRework {
		t.Errorf("classified as %q, want %q", got, MRTerminalRejectedNeedsRework)
	}
}

func TestBeadsClassifyMRTerminalState_ClosedSuperseded(t *testing.T) {
	mr := &Issue{
		Status:      "closed",
		Description: "close_reason: superseded\n",
	}
	got := ClassifyMRTerminalState(mr)
	if got != MRTerminalRejectedNeedsRework {
		t.Errorf("classified as %q, want %q", got, MRTerminalRejectedNeedsRework)
	}
}

func TestBeadsClassifyMRTerminalState_ClosedUnknownReason(t *testing.T) {
	mr := &Issue{
		Status:      "closed",
		Description: "close_reason: weather-is-nice\n",
	}
	got := ClassifyMRTerminalState(mr)
	if got != MRTerminalRejectedNeedsRework {
		t.Errorf("classified as %q, want %q (default safe state)", got, MRTerminalRejectedNeedsRework)
	}
}

func TestBeadsClassifyMRTerminalState_NilMR(t *testing.T) {
	if got := ClassifyMRTerminalState(nil); got != "" {
		t.Errorf("nil MR classified as %q, want empty", got)
	}
	if CanCloseSourceBead(nil) {
		t.Error("CanCloseSourceBead(nil)=true — must be conservative on nil")
	}
	if IsMRTerminalPublished(nil) {
		t.Error("IsMRTerminalPublished(nil)=true — must be conservative on nil")
	}
}

func TestBeadsClassifyMRTerminalState_LegacyCloseReasonInProse(t *testing.T) {
	mr := &Issue{
		Status: "closed",
		Description: `Some prose at the top.

close_reason: merged`,
	}
	got := ClassifyMRTerminalState(mr)
	if got != MRTerminalMergedLocalNotPublished {
		t.Errorf("legacy close_reason classified as %q, want %q", got, MRTerminalMergedLocalNotPublished)
	}
}

func TestBeadsCanCloseSourceBead_PublishedOnly(t *testing.T) {
	// Umbrella: only the published terminal state permits source-bead closure.
	published := &Issue{
		Status: "closed",
		Description: `close_reason: merged
published_commit: 7b076fc1000000000000000000000000000000000`,
	}
	if !CanCloseSourceBead(published) {
		t.Error("CanCloseSourceBead=false for published MR")
	}

	unpublished := &Issue{
		Status:      "closed",
		Description: "close_reason: merged\n",
	}
	if CanCloseSourceBead(unpublished) {
		t.Error("CanCloseSourceBead=true for merged-local-not-published MR")
	}
}
