package refinery

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/testutil"
)

// logEngineerOutputOnFail dumps the captured engineer output when a test fails.
// HandleMRInfoSuccess logs warnings as output, so seeing them is essential for
// diagnosing bead/update failures in test.
func logEngineerOutputOnFail(t *testing.T, e *Engineer) {
	t.Helper()
	if !t.Failed() {
		return
	}
	if buf, ok := e.output.(*bytes.Buffer); ok && buf.Len() > 0 {
		t.Logf("engineer output:\n%s", buf.String())
	}
}

// TestHandleMRInfoSuccess_LocalMergeDoesNotCloseSource is the hq-6sdu guard
// test for the refinery's production merge path. When the refinery performs a
// local-only merge (auto_push=false) the merge commit is never published to the
// configured upstream, so the source bead must remain open. The MR bead is
// still closed, but its terminal_state must record merged-local-not-published
// and it must not advertise a published_commit.
func TestHandleMRInfoSuccess_LocalMergeDoesNotCloseSource(t *testing.T) {
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())

	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = false
	e.config.Gates = nil
	defer logEngineerOutputOnFail(t, e)

	e.beads = beads.NewIsolatedWithPort(workDir, port)
	if err := e.beads.Init("gt"); err != nil {
		t.Skipf("bd init unavailable in test environment: %v", err)
	}

	srcIssue, err := e.beads.Create(beads.CreateOptions{
		Title:  "hq-6sdu source feature",
		Labels: []string{"gt:task"},
	})
	if err != nil {
		t.Fatalf("create source issue: %v", err)
	}

	branch := "feature/hq-6sdu-unpublished"
	createFeatureBranch(t, workDir, branch, "feature.txt", "feature content")

	mrDesc := fmt.Sprintf("branch: %s\nsource_issue: %s\ntarget: main\nworker: test", branch, srcIssue.ID)
	mrIssue, err := e.beads.Create(beads.CreateOptions{
		Title:       "MR for hq-6sdu unpublished merge",
		Labels:      []string{"gt:merge-request"},
		Description: mrDesc,
	})
	if err != nil {
		t.Fatalf("create MR issue: %v", err)
	}

	result := e.doMerge(context.Background(), branch, "main", srcIssue.ID, nil)
	if !result.Success {
		t.Fatalf("expected local merge to succeed, got: %s", result.Error)
	}
	if result.MergeCommit == "" {
		t.Fatal("expected a merge commit SHA")
	}
	if result.PublishedCommit != "" {
		t.Fatalf("local-only merge must not set PublishedCommit, got %s", result.PublishedCommit)
	}

	mr := &MRInfo{
		ID:          mrIssue.ID,
		Branch:      branch,
		Target:      "main",
		SourceIssue: srcIssue.ID,
	}
	e.HandleMRInfoSuccess(mr, result)

	output := e.output.(*bytes.Buffer).String()
	if !strings.Contains(output, "Closed MR bead: "+mrIssue.ID) {
		t.Errorf("expected MR %s to be closed; output:\n%s", mrIssue.ID, output)
	}
	if !strings.Contains(output, "Merge not yet published — leaving source issue open: "+srcIssue.ID) {
		t.Errorf("expected source issue %s to remain open for unpublished merge; output:\n%s", srcIssue.ID, output)
	}
	if strings.Contains(output, "Closed source issue: "+srcIssue.ID) {
		t.Errorf("source issue %s must NOT be closed for an unpublished merge", srcIssue.ID)
	}
}

// TestHandleMRInfoSuccess_PublishedMergeClosesSource verifies the happy path:
// when auto_push is enabled and the refinery successfully pushes to and verifies
// the commit on origin, the MR reaches terminal_state=published and the source
// bead is closed.
func TestHandleMRInfoSuccess_PublishedMergeClosesSource(t *testing.T) {
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())

	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.RunTests = false
	e.config.AutoPush = true
	e.config.Gates = nil
	defer logEngineerOutputOnFail(t, e)

	e.beads = beads.NewIsolatedWithPort(workDir, port)
	if err := e.beads.Init("gt"); err != nil {
		t.Skipf("bd init unavailable in test environment: %v", err)
	}

	srcIssue, err := e.beads.Create(beads.CreateOptions{
		Title:  "hq-6sdu published source feature",
		Labels: []string{"gt:task"},
	})
	if err != nil {
		t.Fatalf("create source issue: %v", err)
	}

	branch := "feature/hq-6sdu-published"
	createFeatureBranch(t, workDir, branch, "feature.txt", "published feature content")

	mrDesc := fmt.Sprintf("branch: %s\nsource_issue: %s\ntarget: main\nworker: test", branch, srcIssue.ID)
	mrIssue, err := e.beads.Create(beads.CreateOptions{
		Title:       "MR for hq-6sdu published merge",
		Labels:      []string{"gt:merge-request"},
		Description: mrDesc,
	})
	if err != nil {
		t.Fatalf("create MR issue: %v", err)
	}

	result := e.doMerge(context.Background(), branch, "main", srcIssue.ID, nil)
	if !result.Success {
		t.Fatalf("expected published merge to succeed, got: %s", result.Error)
	}
	if result.PublishedCommit == "" {
		t.Fatal("expected PublishedCommit to be set after successful push")
	}
	if result.PublishedCommit != result.MergeCommit {
		t.Errorf("PublishedCommit = %s, want %s", result.PublishedCommit, result.MergeCommit)
	}

	mr := &MRInfo{
		ID:          mrIssue.ID,
		Branch:      branch,
		Target:      "main",
		SourceIssue: srcIssue.ID,
	}
	e.HandleMRInfoSuccess(mr, result)

	output := e.output.(*bytes.Buffer).String()
	if !strings.Contains(output, "Closed MR bead: "+mrIssue.ID) {
		t.Errorf("expected MR %s to be closed; output:\n%s", mrIssue.ID, output)
	}
	if !strings.Contains(output, "Closed source issue: "+srcIssue.ID) {
		t.Errorf("expected source issue %s to be closed for published merge; output:\n%s", srcIssue.ID, output)
	}
	if strings.Contains(output, "Merge not yet published") {
		t.Errorf("published merge should not log 'Merge not yet published'")
	}
}
