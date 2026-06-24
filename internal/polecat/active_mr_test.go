package polecat

import (
	"errors"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

type fakeActiveMRReader struct {
	issues map[string]*beads.Issue
	errs   map[string]error
}

func (f fakeActiveMRReader) Show(issueID string) (*beads.Issue, error) {
	if err := f.errs[issueID]; err != nil {
		return nil, err
	}
	issue, ok := f.issues[issueID]
	if !ok {
		return nil, beads.ErrNotFound
	}
	return issue, nil
}

func TestAssessActiveMR(t *testing.T) {
	reader := fakeActiveMRReader{issues: map[string]*beads.Issue{
		"mr-open":        &beads.Issue{ID: "mr-open", Status: "open"},
		"mr-closed":      &beads.Issue{ID: "mr-closed", Status: "closed"},
		"mr-with-source": &beads.Issue{ID: "mr-with-source", Status: "closed", Description: "source_issue: gt-closed\n"},
		"gt-closed":      &beads.Issue{ID: "gt-closed", Status: "closed"},
		"gt-open":        &beads.Issue{ID: "gt-open", Status: "open"},
		// hq-yyz / hq-6na: terminal MR closed for rework (rejected/conflict/
		// superseded) with an intentionally-open source. The MR is no longer
		// live in the merge queue, so the slot must be recyclable for rework.
		"mr-rejected":   &beads.Issue{ID: "mr-rejected", Status: "closed", Description: "source_issue: gt-open\nclose_reason: rejected\n"},
		"mr-conflict":   &beads.Issue{ID: "mr-conflict", Status: "closed", Description: "source_issue: gt-open\nclose_reason: conflict\n"},
		"mr-superseded": &beads.Issue{ID: "mr-superseded", Status: "closed", Description: "source_issue: gt-open\nclose_reason: superseded\n"},
		// A merged MR has close_reason=merged; PostMerge closes the source, so
		// source terminality remains the correct gate (not a rework reconcile).
		"mr-merged": &beads.Issue{ID: "mr-merged", Status: "closed", Description: "source_issue: gt-closed\nclose_reason: merged\n"},
	}}

	tests := []struct {
		name       string
		reader     IssueReader
		input      ActiveMRInput
		wantPend   bool
		wantSource string
	}{
		{name: "empty active MR is not pending", reader: reader, input: ActiveMRInput{}, wantPend: false},
		{name: "open MR is pending", reader: reader, input: ActiveMRInput{ActiveMR: "mr-open", SourceIssueHint: "gt-closed"}, wantPend: true},
		{name: "closed MR with terminal source is stale", reader: reader, input: ActiveMRInput{ActiveMR: "mr-closed", SourceIssueHint: "gt-closed"}, wantPend: false, wantSource: "gt-closed"},
		{name: "closed MR with unknown source is pending", reader: reader, input: ActiveMRInput{ActiveMR: "mr-closed"}, wantPend: true},
		{name: "closed MR with open source is pending", reader: reader, input: ActiveMRInput{ActiveMR: "mr-closed", SourceIssueHint: "gt-open"}, wantPend: true, wantSource: "gt-open"},
		{name: "missing MR with terminal source is stale", reader: reader, input: ActiveMRInput{ActiveMR: "mr-missing", SourceIssueHint: "gt-closed"}, wantPend: false, wantSource: "gt-closed"},
		{name: "missing MR with missing source is pending", reader: reader, input: ActiveMRInput{ActiveMR: "mr-missing", SourceIssueHint: "gt-missing"}, wantPend: true, wantSource: "gt-missing"},
		{name: "terminal MR source wins from description", reader: reader, input: ActiveMRInput{ActiveMR: "mr-with-source"}, wantPend: false, wantSource: "gt-closed"},
		{name: "nil reader fails closed", reader: nil, input: ActiveMRInput{ActiveMR: "mr-closed", SourceIssueHint: "gt-closed"}, wantPend: true},
		{name: "git unsafe fails closed when required", reader: reader, input: ActiveMRInput{ActiveMR: "mr-closed", SourceIssueHint: "gt-closed", RequireGitSafe: true}, wantPend: true, wantSource: "gt-closed"},
		{name: "git safe permits stale when required", reader: reader, input: ActiveMRInput{ActiveMR: "mr-closed", SourceIssueHint: "gt-closed", RequireGitSafe: true, GitSafe: true}, wantPend: false, wantSource: "gt-closed"},
		// hq-yyz / hq-6na: rejected MR with open source is stale (non-pending)
		// and reconcilable when git is safe — the dead MR must not block capacity.
		{name: "rejected MR with open source is stale when git safe", reader: reader, input: ActiveMRInput{ActiveMR: "mr-rejected", RequireGitSafe: true, GitSafe: true}, wantPend: false, wantSource: "gt-open"},
		{name: "conflict MR with open source is stale when git safe", reader: reader, input: ActiveMRInput{ActiveMR: "mr-conflict", RequireGitSafe: true, GitSafe: true}, wantPend: false, wantSource: "gt-open"},
		{name: "superseded MR with open source is stale when git safe", reader: reader, input: ActiveMRInput{ActiveMR: "mr-superseded", RequireGitSafe: true, GitSafe: true}, wantPend: false, wantSource: "gt-open"},
		// Rejected MR stays fail-closed when git is unsafe (live work at risk).
		{name: "rejected MR with open source blocks when git unsafe", reader: reader, input: ActiveMRInput{ActiveMR: "mr-rejected", RequireGitSafe: true, GitSafe: false}, wantPend: true, wantSource: "gt-open"},
		// Without RequireGitSafe, a rejected MR is stale regardless of git state.
		{name: "rejected MR is stale without git safety required", reader: reader, input: ActiveMRInput{ActiveMR: "mr-rejected"}, wantPend: false, wantSource: "gt-open"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssessActiveMR(tt.reader, tt.input)
			if got.Pending != tt.wantPend {
				t.Fatalf("Pending = %v, want %v (reason %q)", got.Pending, tt.wantPend, got.Reason)
			}
			if tt.wantSource != "" && got.SourceIssue != tt.wantSource {
				t.Fatalf("SourceIssue = %q, want %q", got.SourceIssue, tt.wantSource)
			}
		})
	}
}

func TestAssessActiveMRLookupErrorsFailClosed(t *testing.T) {
	reader := fakeActiveMRReader{
		issues: map[string]*beads.Issue{"gt-closed": &beads.Issue{ID: "gt-closed", Status: "closed"}},
		errs:   map[string]error{"mr-error": errors.New("bd exploded"), "gt-error": errors.New("bd exploded")},
	}

	if got := AssessActiveMR(reader, ActiveMRInput{ActiveMR: "mr-error", SourceIssueHint: "gt-closed"}); !got.Pending {
		t.Fatalf("MR lookup error Pending = false, want true")
	}
	reader.issues["mr-closed"] = &beads.Issue{ID: "mr-closed", Status: "closed"}
	if got := AssessActiveMR(reader, ActiveMRInput{ActiveMR: "mr-closed", SourceIssueHint: "gt-error"}); !got.Pending {
		t.Fatalf("source lookup error Pending = false, want true")
	}
}

// TestAssessActiveMRReconcilableReworkClose covers hq-yyz / hq-6na: a terminal
// MR closed for rework is reconcilable (safe to clear stale active_mr) and does
// not block capacity on an intentionally-open source. A merged MR is NOT
// reconcilable via this path — its source is closed by PostMerge, so source
// terminality is the correct gate.
func TestAssessActiveMRReconcilableReworkClose(t *testing.T) {
	reader := fakeActiveMRReader{issues: map[string]*beads.Issue{
		"mr-rejected": &beads.Issue{ID: "mr-rejected", Status: "closed", Description: "source_issue: gt-open\nclose_reason: rejected\n"},
		"mr-merged":   &beads.Issue{ID: "mr-merged", Status: "closed", Description: "source_issue: gt-open\nclose_reason: merged\n"},
		"gt-open":     &beads.Issue{ID: "gt-open", Status: "open"},
	}}

	rejected := AssessActiveMR(reader, ActiveMRInput{ActiveMR: "mr-rejected", RequireGitSafe: true, GitSafe: true})
	if rejected.Pending {
		t.Fatalf("rejected MR Pending = true, want false (reason %q)", rejected.Reason)
	}
	if !rejected.Stale {
		t.Fatalf("rejected MR Stale = false, want true")
	}
	if !rejected.Reconcilable {
		t.Fatalf("rejected MR Reconcilable = false, want true (hq-yyz/hq-6na)")
	}
	if rejected.CloseReason != "rejected" {
		t.Fatalf("CloseReason = %q, want rejected", rejected.CloseReason)
	}

	// Merged MR with an open source is NOT reconcilable via the rework path:
	// merged-but-source-open means the merge hasn't fully closed the source, so
	// the assessment stays pending (fail-closed) rather than silently clearing.
	merged := AssessActiveMR(reader, ActiveMRInput{ActiveMR: "mr-merged", RequireGitSafe: true, GitSafe: true})
	if merged.Reconcilable {
		t.Fatalf("merged MR Reconcilable = true, want false (merged uses source-terminal gate)")
	}
	if !merged.Pending {
		t.Fatalf("merged MR with open source Pending = false, want true (source not terminal)")
	}
}
