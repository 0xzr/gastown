package refinery

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	gitpkg "github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/testutil"
)

// writeGoMod creates a minimal go.mod for surface-scope tests.
func writeGoMod(t *testing.T, dir, module string) {
	t.Helper()
	content := "module " + module + "\n\ngo 1.26.2\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// writeGoFile creates a .go file in dir/pkg with package declaration.
func writeGoFile(t *testing.T, dir, pkg, filename string) {
	t.Helper()
	pkgDir := filepath.Join(dir, pkg)
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "package " + pkg + "\n"
	if err := os.WriteFile(filepath.Join(pkgDir, filename), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// makeSurfaceRepo creates a git repo with a Go module and two packages on main,
// then returns the engineer and a feature branch that touches one of them.
func makeSurfaceRepo(t *testing.T, touchedPkg string) (workDir string, g *gitpkg.Git, e *Engineer, branch string) {
	t.Helper()
	workDir, g, _ = testGitRepo(t)
	writeGoMod(t, workDir, "example.com/test")
	writeGoFile(t, workDir, "touched", "touched.go")
	writeGoFile(t, workDir, "unrelated", "unrelated.go")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add packages")
	run(t, workDir, "git", "push", "origin", "main")

	touchedFile := filepath.Join(touchedPkg, touchedPkg+".go")
	branch = "feature/surface-" + touchedPkg
	run(t, workDir, "git", "checkout", "-b", branch, "main")
	writeGoFile(t, workDir, touchedPkg, "extra.go")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "change "+touchedFile)
	run(t, workDir, "git", "checkout", "main")

	e = newTestEngineer(t, workDir, g)
	e.output = io.Discard
	return workDir, g, e, branch
}

func TestRunGate_SurfaceScope_UnrelatedGoPackageFailureAccepted(t *testing.T) {
	if testing.Short() {
		t.Skip("surface scope tests use real git")
	}

	_, _, e, branch := makeSurfaceRepo(t, "touched")
	e.surface = &gateSurface{base: "main", head: branch}

	cmd := "echo '# example.com/test/unrelated'; echo 'FAIL example.com/test/unrelated'; exit 1"
	result := e.runGate(context.Background(), "test", &GateConfig{
		Cmd:          cmd,
		SurfaceScope: SurfaceScopeGoPackages,
	})

	if !result.Success {
		t.Errorf("expected unrelated failure to be accepted, got error: %s", result.Error)
	}
	if result.Error != "" {
		t.Errorf("expected empty error after surface acceptance, got: %q", result.Error)
	}
}

func TestRunGate_SurfaceScope_TouchedGoPackageFailureRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("surface scope tests use real git")
	}

	_, _, e, branch := makeSurfaceRepo(t, "touched")
	e.surface = &gateSurface{base: "main", head: branch}

	cmd := "echo '# example.com/test/touched'; echo 'FAIL example.com/test/touched'; exit 1"
	result := e.runGate(context.Background(), "test", &GateConfig{
		Cmd:          cmd,
		SurfaceScope: SurfaceScopeGoPackages,
	})

	if result.Success {
		t.Error("expected touched package failure to be rejected")
	}
}

func TestRunGate_SurfaceScope_DisabledIgnoresSurface(t *testing.T) {
	if testing.Short() {
		t.Skip("surface scope tests use real git")
	}

	_, _, e, branch := makeSurfaceRepo(t, "touched")
	e.surface = &gateSurface{base: "main", head: branch}

	cmd := "echo '# example.com/test/unrelated'; echo 'FAIL example.com/test/unrelated'; exit 1"
	result := e.runGate(context.Background(), "test", &GateConfig{
		Cmd:          cmd,
		SurfaceScope: SurfaceScopeDisabled,
	})

	if result.Success {
		t.Error("expected disabled surface scope to reject unrelated failure")
	}
}

func TestRunGate_SurfaceScope_NoGoMod_FallsBackToFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("surface scope tests use real git")
	}

	workDir, g, _ := testGitRepo(t)
	writeGoFile(t, workDir, "pkg", "pkg.go")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add pkg")
	run(t, workDir, "git", "push", "origin", "main")

	run(t, workDir, "git", "checkout", "-b", "feature/nogomod", "main")
	writeGoFile(t, workDir, "pkg", "extra.go")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "change pkg")
	run(t, workDir, "git", "checkout", "main")

	e := newTestEngineer(t, workDir, g)
	e.output = io.Discard
	e.surface = &gateSurface{base: "main", head: "feature/nogomod"}

	cmd := "echo '# example.com/test/pkg'; echo 'FAIL example.com/test/pkg'; exit 1"
	result := e.runGate(context.Background(), "test", &GateConfig{
		Cmd:          cmd,
		SurfaceScope: SurfaceScopeGoPackages,
	})

	if result.Success {
		t.Error("expected failure when go.mod is missing")
	}
}

func TestChangedGoPackages(t *testing.T) {
	if testing.Short() {
		t.Skip("surface scope tests use real git")
	}

	_, _, e, branch := makeSurfaceRepo(t, "touched")
	pkgs, err := e.changedGoPackages("main", branch)
	if err != nil {
		t.Fatalf("changedGoPackages: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("expected 1 changed package, got %d: %v", len(pkgs), pkgs)
	}
	if _, ok := pkgs["example.com/test/touched"]; !ok {
		t.Errorf("expected touched package in set, got: %v", pkgs)
	}
}

func TestParseGoFailingPackages(t *testing.T) {
	output := `
# example.com/test/unrelated
example.com/test/unrelated/file.go:10:5: undefined: x
FAIL	example.com/test/unrelated
FAIL
ok  	example.com/test/good
--- FAIL: TestSomething (0.00s)
`
	got := parseGoFailingPackages(output)
	want := map[string]struct{}{
		"example.com/test/unrelated": {},
	}
	if len(got) != len(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for pkg := range want {
		if _, ok := got[pkg]; !ok {
			t.Errorf("missing package %q in %v", pkg, got)
		}
	}
}

func TestSurfaceScope_InferenceForGoWorkspaceCommands(t *testing.T) {
	tests := []struct {
		cmd      string
		expected string
	}{
		{"go test ./...", SurfaceScopeGoPackages},
		{"go build ./...", SurfaceScopeGoPackages},
		{"CGO_ENABLED=0 go test ./...", SurfaceScopeGoPackages},
		{"go test ./internal/...", SurfaceScopeDisabled},
		{"go vet ./...", SurfaceScopeDisabled},
		{"golangci-lint run ./...", SurfaceScopeDisabled},
	}

	e := NewEngineer(&rig.Rig{Name: "test-rig", Path: t.TempDir()})
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := e.surfaceScope(&GateConfig{Cmd: tt.cmd})
			if got != tt.expected {
				t.Errorf("surfaceScope(%q) = %q, want %q", tt.cmd, got, tt.expected)
			}
		})
	}
}

// --- gastown-w5o: post-squash surface acceptance + audit trail ---

// TestRunGate_SurfaceScope_PostSquashGateRejectsUnrelatedFailure guards the
// headline fix for gastown-w5o: a post-squash gate must NEVER accept a failure
// via the surface-scope rule. Post-squash runs on the combined merged tree, so
// an "unrelated" failing package can be a real regression caused by the merged
// change. Accepting it would silently merge broken code.
func TestRunGate_SurfaceScope_PostSquashGateRejectsUnrelatedFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("surface scope tests use real git")
	}

	_, _, e, branch := makeSurfaceRepo(t, "touched")
	e.surface = &gateSurface{base: "main", head: branch}

	cmd := "echo '# example.com/test/unrelated'; echo 'FAIL example.com/test/unrelated'; exit 1"
	result := e.runGate(context.Background(), "post-build", &GateConfig{
		Cmd:          cmd,
		Phase:        GatePhasePostSquash,
		SurfaceScope: SurfaceScopeGoPackages,
	})

	if result.Success {
		t.Fatal("expected post-squash gate with an unrelated failure to be REJECTED (no surface acceptance)")
	}
	if result.SurfaceAccepted {
		t.Error("post-squash gate must not mark a suppressed failure as SurfaceAccepted")
	}
}

// TestRunGate_SurfaceScope_PreMergeUnrelatedFailureCapturesAuditInfo guards
// gastown-w5o: when a pre-merge gate accepts an unrelated failure, the
// suppressed failure must be captured on the GateResult so the merge path can
// record a durable audit obligation. A clean pass must NOT set SurfaceAccepted.
func TestRunGate_SurfaceScope_PreMergeUnrelatedFailureCapturesAuditInfo(t *testing.T) {
	if testing.Short() {
		t.Skip("surface scope tests use real git")
	}

	_, _, e, branch := makeSurfaceRepo(t, "touched")
	e.surface = &gateSurface{base: "main", head: branch}

	t.Run("unrelated failure is accepted and flagged", func(t *testing.T) {
		cmd := "echo '# example.com/test/unrelated'; echo 'FAIL example.com/test/unrelated'; exit 1"
		result := e.runGate(context.Background(), "test", &GateConfig{
			Cmd:          cmd,
			Phase:        GatePhasePreMerge,
			SurfaceScope: SurfaceScopeGoPackages,
		})
		if !result.Success {
			t.Fatalf("expected unrelated failure to be accepted, got error: %s", result.Error)
		}
		if !result.SurfaceAccepted {
			t.Error("expected SurfaceAccepted=true for a suppressed real failure")
		}
		if len(result.SurfaceAcceptedFailures) != 1 {
			t.Fatalf("expected 1 suppressed package, got %d: %v", len(result.SurfaceAcceptedFailures), result.SurfaceAcceptedFailures)
		}
		if got := result.SurfaceAcceptedFailures[0]; got != "example.com/test/unrelated" {
			t.Errorf("expected suppressed package example.com/test/unrelated, got %q", got)
		}
	})

	t.Run("clean pass is not flagged", func(t *testing.T) {
		result := e.runGate(context.Background(), "ok", &GateConfig{
			Cmd:          "exit 0",
			Phase:        GatePhasePreMerge,
			SurfaceScope: SurfaceScopeGoPackages,
		})
		if !result.Success {
			t.Fatalf("expected clean pass, got: %s", result.Error)
		}
		if result.SurfaceAccepted {
			t.Error("clean pass must not set SurfaceAccepted")
		}
		if len(result.SurfaceAcceptedFailures) != 0 {
			t.Errorf("clean pass must not capture suppressed failures, got: %v", result.SurfaceAcceptedFailures)
		}
	})
}

// TestSurfaceAcceptsFailure_PostSquashForbidden is a focused unit test on the
// decision function: even with a valid surface, go-packages scope, and a
// failing package fully outside the touched surface, a post-squash gate is
// refused. A pre-merge gate with the same inputs is accepted.
func TestSurfaceAcceptsFailure_PostSquashForbidden(t *testing.T) {
	if testing.Short() {
		t.Skip("surface scope tests use real git")
	}

	_, _, e, branch := makeSurfaceRepo(t, "touched")
	e.surface = &gateSurface{base: "main", head: branch}

	failedResult := GateResult{
		Name:    "test",
		Success: false,
		Output:  "# example.com/test/unrelated\nFAIL\texample.com/test/unrelated\n",
	}

	t.Run("post-squash is refused", func(t *testing.T) {
		accepted, suppressed := e.surfaceAcceptsFailure(&GateConfig{
			Cmd:          "go test ./...",
			Phase:        GatePhasePostSquash,
			SurfaceScope: SurfaceScopeGoPackages,
		}, failedResult)
		if accepted {
			t.Error("expected post-squash surface acceptance to be forbidden")
		}
		if len(suppressed) != 0 {
			t.Errorf("expected no suppressed packages on refusal, got: %v", suppressed)
		}
	})

	t.Run("pre-merge is accepted", func(t *testing.T) {
		accepted, suppressed := e.surfaceAcceptsFailure(&GateConfig{
			Cmd:          "go test ./...",
			Phase:        GatePhasePreMerge,
			SurfaceScope: SurfaceScopeGoPackages,
		}, failedResult)
		if !accepted {
			t.Error("expected pre-merge surface acceptance to be allowed")
		}
		if len(suppressed) != 1 || suppressed[0] != "example.com/test/unrelated" {
			t.Errorf("expected suppressed [example.com/test/unrelated], got: %v", suppressed)
		}
	})

	t.Run("default phase (empty) treated as pre-merge and accepted", func(t *testing.T) {
		accepted, _ := e.surfaceAcceptsFailure(&GateConfig{
			Cmd:          "go test ./...",
			SurfaceScope: SurfaceScopeGoPackages,
		}, failedResult)
		if !accepted {
			t.Error("expected empty phase (default pre-merge) to allow surface acceptance")
		}
	})
}

// TestMergeSurfaceSuppressed verifies the accumulator used to fold suppressed
// gates across pre-merge and post-squash phases into a single audit obligation.
func TestMergeSurfaceSuppressed(t *testing.T) {
	t.Run("nil dst", func(t *testing.T) {
		got := mergeSurfaceSuppressed(nil, map[string][]string{"g": {"a"}})
		if len(got) != 1 || len(got["g"]) != 1 || got["g"][0] != "a" {
			t.Fatalf("unexpected: %v", got)
		}
	})

	t.Run("empty src returns dst content unchanged", func(t *testing.T) {
		dst := map[string][]string{"g": {"a"}}
		got := mergeSurfaceSuppressed(dst, nil)
		if len(got) != 1 || got["g"][0] != "a" {
			t.Fatalf("unexpected: %v", got)
		}
	})

	t.Run("dedupes packages within a gate", func(t *testing.T) {
		dst := map[string][]string{"g": {"a", "b"}}
		src := map[string][]string{"g": {"b", "c"}}
		got := mergeSurfaceSuppressed(dst, src)
		if len(got["g"]) != 3 {
			t.Fatalf("expected 3 deduped packages, got: %v", got["g"])
		}
		// merged packages are sorted
		want := []string{"a", "b", "c"}
		for i, p := range want {
			if got["g"][i] != p {
				t.Errorf("index %d: got %q want %q (full: %v)", i, got["g"][i], p, got["g"])
			}
		}
	})

	t.Run("merges distinct gates", func(t *testing.T) {
		dst := map[string][]string{"build": {"x"}}
		src := map[string][]string{"test": {"y"}}
		got := mergeSurfaceSuppressed(dst, src)
		if len(got) != 2 {
			t.Fatalf("expected 2 gates, got: %v", got)
		}
		if len(got["build"]) != 1 || got["build"][0] != "x" {
			t.Errorf("build gate: %v", got["build"])
		}
		if len(got["test"]) != 1 || got["test"][0] != "y" {
			t.Errorf("test gate: %v", got["test"])
		}
	})
}

// TestSurfaceAcceptanceAuditDescription_BuildsDurableObligation verifies the
// audit-trail content recorded when a real gate failure is suppressed by the
// surface-scope rule: it must name the MR metadata and every suppressed package
// so the obligation is durable and actionable (gastown-w5o). Tested against the
// pure description builder so it does not require a Dolt server.
func TestSurfaceAcceptanceAuditDescription_BuildsDurableObligation(t *testing.T) {
	mr := &MRInfo{
		ID:          "gt-mr-1",
		Branch:      "polecat/opal/feat",
		SourceIssue: "gt-src-1",
		Target:      "main",
		Priority:    1,
	}

	suppressed := map[string][]string{
		"test":  {"example.com/test/unrelated"},
		"build": {"example.com/test/other"},
	}

	desc := surfaceAcceptanceAuditDescription(mr, suppressed)

	// The description must name the suppressed packages and the MR/branch/target.
	for _, pkg := range []string{"example.com/test/unrelated", "example.com/test/other"} {
		if !strings.Contains(desc, pkg) {
			t.Errorf("audit description must mention suppressed package %q", pkg)
		}
	}
	for _, want := range []string{mr.ID, mr.Branch, mr.SourceIssue, mr.Target} {
		if !strings.Contains(desc, want) {
			t.Errorf("audit description must mention %q", want)
		}
	}
	if !strings.Contains(strings.ToLower(desc), "suppress") {
		t.Error("audit description must explain the failures were suppressed")
	}
	// Both gate names must appear, with deterministic ordering (build before test).
	buildIdx := strings.Index(desc, "Gate \"build\"")
	testIdx := strings.Index(desc, "Gate \"test\"")
	if buildIdx < 0 || testIdx < 0 {
		t.Fatalf("audit description must list both gates; got:\n%s", desc)
	}
	if buildIdx > testIdx {
		t.Error("expected gates listed in sorted (build before test) order for determinism")
	}

	// The title must reference the MR id.
	if got := surfaceAcceptanceAuditTitle(mr); !strings.Contains(got, mr.ID) {
		t.Errorf("audit title %q must reference MR id %q", got, mr.ID)
	}
}

// TestRecordSurfaceAcceptanceAuditBead_NoSuppressionNoBead guards the
// no-op path: with nothing suppressed, no audit bead is created (the merge had
// no suppressed failures to audit). This path needs no Dolt server.
func TestRecordSurfaceAcceptanceAuditBead_NoSuppressionNoBead(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)

	mr := &MRInfo{ID: "gt-mr-2", Branch: "b", SourceIssue: "s", Target: "main"}

	beadID, err := e.recordSurfaceAcceptanceAuditBead(mr, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if beadID != "" {
		t.Errorf("expected no audit bead when nothing suppressed, got %q", beadID)
	}
}

// TestDoMerge_SurfaceScopeSuppressedFailure_RecordsAuditTrail is the keystone
// end-to-end guard for gastown-w5o: when a pre-merge gate surface-accepts a REAL
// failure (failing package outside the touched surface), a successful published
// merge must (1) record a durable audit bead, (2) surface
// SurfaceAcceptedFailure + AuditBead on the ProcessResult, and (3) write the
// audit trail into the source issue's close reason. Without this, a suppressed
// real failure merges silently with no durable trail.
func TestDoMerge_SurfaceScopeSuppressedFailure_RecordsAuditTrail(t *testing.T) {
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())

	// Repo with a go.mod and two packages; the branch touches only "touched".
	workDir, _, e, branch := makeSurfaceRepo(t, "touched")
	// makeSurfaceRepo sets e.output = io.Discard; replace with a capture buffer so
	// the test can assert on the audit-trail log lines.
	e.output = &bytes.Buffer{}
	e.config.RunTests = false
	e.config.AutoPush = true
	e.config.GatesParallel = false
	e.config.Gates = map[string]*GateConfig{
		"test": {
			// Fail on the UNRELATED package, which is outside the branch's touched
			// surface. The surface rule must accept this as a pass — but it is a real
			// failure, so the audit trail must be recorded.
			Cmd:          "echo '# example.com/test/unrelated'; echo 'FAIL example.com/test/unrelated'; exit 1",
			Phase:        GatePhasePreMerge,
			SurfaceScope: SurfaceScopeGoPackages,
		},
	}
	defer logEngineerOutputOnFail(t, e)

	e.beads = beads.NewIsolatedWithPort(workDir, port)
	if err := e.beads.Init("gt"); err != nil {
		t.Skipf("bd init unavailable in test environment: %v", err)
	}

	srcIssue, err := e.beads.Create(beads.CreateOptions{
		Title:  "gastown-w5o source feature",
		Labels: []string{"gt:task"},
	})
	if err != nil {
		t.Fatalf("create source issue: %v", err)
	}

	mrDesc := "branch: " + branch + "\nsource_issue: " + srcIssue.ID + "\ntarget: main\nworker: test"
	mrIssue, err := e.beads.Create(beads.CreateOptions{
		Title:       "MR for gastown-w5o",
		Labels:      []string{"gt:merge-request"},
		Description: mrDesc,
	})
	if err != nil {
		t.Fatalf("create MR issue: %v", err)
	}

	auditCalled := false
	e.recordSurfaceAcceptanceAuditBeadFunc = func(m *MRInfo, suppressed map[string][]string) (string, error) {
		auditCalled = true
		if len(suppressed) == 0 {
			t.Error("audit bead recorded with no suppressed gates")
		}
		if pkgs, ok := suppressed["test"]; !ok || len(pkgs) != 1 || pkgs[0] != "example.com/test/unrelated" {
			t.Errorf("expected suppressed test gate with [example.com/test/unrelated], got: %v", suppressed)
		}
		return "gt-surface-audit-fake", nil
	}

	result := e.doMerge(context.Background(), branch, "main", srcIssue.ID, nil)
	if !result.Success {
		t.Fatalf("expected merge to succeed (surface-accepted), got: %s", result.Error)
	}
	if result.PublishedCommit == "" {
		t.Fatal("expected PublishedCommit set after successful push")
	}
	if !result.SurfaceAcceptedFailure {
		t.Error("expected SurfaceAcceptedFailure=true on a surface-accepted real failure")
	}
	if result.SurfaceScopeAuditBead != "gt-surface-audit-fake" {
		t.Errorf("expected SurfaceScopeAuditBead=gt-surface-audit-fake, got %q", result.SurfaceScopeAuditBead)
	}
	if !auditCalled {
		t.Error("audit bead must be recorded after a published merge with a suppressed failure")
	}

	// HandleMRInfoSuccess must run the close path for a published merge.
	mr := &MRInfo{
		ID:          mrIssue.ID,
		Branch:      branch,
		Target:      "main",
		SourceIssue: srcIssue.ID,
	}
	e.HandleMRInfoSuccess(mr, result)

	output := e.output.(*bytes.Buffer).String()
	// The audit bead must have been recorded during doMerge (before close).
	if !strings.Contains(output, "Recorded surface-scope acceptance audit bead") {
		t.Errorf("expected audit bead log; output:\n%s", output)
	}
	// The surface-acceptance trail must be logged so it is visible to operators.
	if !strings.Contains(output, "surface-scope acceptance") {
		t.Errorf("expected surface-scope acceptance log; output:\n%s", output)
	}
	// The source issue must be closed for a published merge.
	if !strings.Contains(output, "Closed source issue: "+srcIssue.ID) {
		t.Errorf("expected source issue %s closed; output:\n%s", srcIssue.ID, output)
	}

	// Re-read the source issue and assert it was closed for the published merge.
	src, err := e.beads.Show(srcIssue.ID)
	if err != nil {
		t.Fatalf("beads.Show(%s): %v", srcIssue.ID, err)
	}
	if src.Status != "closed" {
		t.Errorf("expected source issue closed, got status %q", src.Status)
	}
}

// TestBuildMergedCloseReason_SurfaceScopeTrail guards gastown-w5o at the
// close-reason level: when a real failure was suppressed and the merge landed,
// the source close reason must carry the surface_scope_acceptance marker and the
// audit bead reference, so the suppressed failure is durable in merge history.
// A clean merge (no suppressed failure) must NOT carry the marker.
func TestBuildMergedCloseReason_SurfaceScopeTrail(t *testing.T) {
	mr := &MRInfo{ID: "gt-mr-3", Branch: "b", Target: "main", SourceIssue: "gt-src"}

	t.Run("suppressed failure surfaces the trail", func(t *testing.T) {
		result := ProcessResult{
			MergeCommit:            "abc123",
			SurfaceAcceptedFailure: true,
			SurfaceScopeAuditBead:  "gt-surface-audit-1",
		}
		reason := buildMergedCloseReason(mr, result)
		if !strings.Contains(reason, "Merged in "+mr.ID) {
			t.Errorf("close reason must reference MR id; got: %q", reason)
		}
		if !strings.Contains(reason, "commit_sha: abc123") {
			t.Errorf("close reason must reference merge commit; got: %q", reason)
		}
		if !strings.Contains(reason, "surface_scope_acceptance: suppressed_real_failure") {
			t.Errorf("close reason must mark surface_scope_acceptance; got: %q", reason)
		}
		if !strings.Contains(reason, "surface_scope_audit_bead: gt-surface-audit-1") {
			t.Errorf("close reason must reference the surface audit bead; got: %q", reason)
		}
	})

	t.Run("clean merge has no surface trail", func(t *testing.T) {
		result := ProcessResult{MergeCommit: "abc123"}
		reason := buildMergedCloseReason(mr, result)
		if strings.Contains(reason, "surface_scope") {
			t.Errorf("clean merge must not carry surface trail; got: %q", reason)
		}
	})

	t.Run("degraded quorum and surface acceptance both recorded without collision", func(t *testing.T) {
		// Both can co-occur on the PR path; both audit beads must appear and
		// neither must clobber the other (gastown-w5o).
		result := ProcessResult{
			MergeCommit:            "abc123",
			DegradedQuorum:         true,
			AuditBead:              "gt-reviewer-audit-1",
			SurfaceAcceptedFailure: true,
			SurfaceScopeAuditBead:  "gt-surface-audit-1",
		}
		reason := buildMergedCloseReason(mr, result)
		if !strings.Contains(reason, "review_state: degraded_quorum") {
			t.Errorf("close reason must record degraded quorum; got: %q", reason)
		}
		if !strings.Contains(reason, "audit_bead: gt-reviewer-audit-1") {
			t.Errorf("close reason must reference the reviewer audit bead; got: %q", reason)
		}
		if !strings.Contains(reason, "surface_scope_acceptance: suppressed_real_failure") {
			t.Errorf("close reason must mark surface_scope_acceptance; got: %q", reason)
		}
		if !strings.Contains(reason, "surface_scope_audit_bead: gt-surface-audit-1") {
			t.Errorf("close reason must reference the surface audit bead; got: %q", reason)
		}
	})
}
