package refinery

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	gitpkg "github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
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
