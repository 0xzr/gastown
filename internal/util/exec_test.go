package util

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExecWithOutput(t *testing.T) {
	// Test successful command
	var output string
	var err error
	if runtime.GOOS == "windows" {
		output, err = ExecWithOutput(".", "cmd", "/c", "echo hello")
	} else {
		output, err = ExecWithOutput(".", "echo", "hello")
	}
	if err != nil {
		t.Fatalf("ExecWithOutput failed: %v", err)
	}
	if output != "hello" {
		t.Errorf("expected 'hello', got %q", output)
	}

	// Test command that fails
	if runtime.GOOS == "windows" {
		_, err = ExecWithOutput(".", "cmd", "/c", "exit /b 1")
	} else {
		_, err = ExecWithOutput(".", "false")
	}
	if err == nil {
		t.Error("expected error for failing command")
	}
}

func TestExecRun(t *testing.T) {
	// Test successful command
	var err error
	if runtime.GOOS == "windows" {
		err = ExecRun(".", "cmd", "/c", "exit /b 0")
	} else {
		err = ExecRun(".", "true")
	}
	if err != nil {
		t.Fatalf("ExecRun failed: %v", err)
	}

	// Test command that fails
	if runtime.GOOS == "windows" {
		err = ExecRun(".", "cmd", "/c", "exit /b 1")
	} else {
		err = ExecRun(".", "false")
	}
	if err == nil {
		t.Error("expected error for failing command")
	}
}

func TestExecWithOutput_WorkDir(t *testing.T) {
	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "exec-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Test that workDir is respected
	var output string
	if runtime.GOOS == "windows" {
		output, err = ExecWithOutput(tmpDir, "cmd", "/c", "cd")
	} else {
		output, err = ExecWithOutput(tmpDir, "pwd")
	}
	if err != nil {
		t.Fatalf("ExecWithOutput failed: %v", err)
	}
	if !strings.Contains(output, tmpDir) && !strings.Contains(tmpDir, output) {
		t.Errorf("expected output to contain %q, got %q", tmpDir, output)
	}
}

func TestFirstLine(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{"hello\nworld", "hello"},
		{"\n\nhello\nworld", "hello"},
		{"  hello  \nworld", "hello"},
		{"", ""},
		{"\n\n\n", ""},
		{"Error: something went wrong\nUsage:\n  gt convoy [flags]\n", "Error: something went wrong"},
	}
	for _, tc := range tests {
		got := FirstLine(tc.input)
		if got != tc.want {
			t.Errorf("FirstLine(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestExecWithOutput_StderrInError(t *testing.T) {
	// Test that stderr is captured in error
	var err error
	if runtime.GOOS == "windows" {
		_, err = ExecWithOutput(".", "cmd", "/c", "echo error message 1>&2 & exit /b 1")
	} else {
		_, err = ExecWithOutput(".", "sh", "-c", "echo 'error message' >&2; exit 1")
	}
	if err == nil {
		t.Error("expected error")
	}
	if !strings.Contains(err.Error(), "error message") {
		t.Errorf("expected error to contain stderr, got %q", err.Error())
	}
}

// envPath returns the value of PATH= from a slice of "K=V" env entries.
func envPath(env []string) string {
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			return strings.TrimPrefix(kv, "PATH=")
		}
	}
	return ""
}

// TestGateCommandEnv_GoOnPath verifies that when `go` is resolvable on PATH the
// environment is returned unchanged (nil → os/exec inherits the parent env).
func TestGateCommandEnv_GoOnPath(t *testing.T) {
	// Place a fake `go` binary on PATH so LookPath succeeds.
	dir := t.TempDir()
	fakeGo := dir + string(os.PathSeparator) + "go"
	if runtime.GOOS == "windows" {
		fakeGo += ".exe"
	}
	if err := os.WriteFile(fakeGo, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldPath := os.Getenv("PATH")
	defer os.Setenv("PATH", oldPath)
	os.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
	defer os.Unsetenv("GOROOT")

	if _, err := exec.LookPath("go"); err != nil {
		t.Fatalf("prerequisite: `go` should resolve on PATH, got %v", err)
	}

	if env := GateCommandEnv(); env != nil {
		t.Errorf("expected nil env when go is on PATH (inherit unchanged), got %v", env)
	}
}

// TestGateCommandEnv_GoMissing verifies that when `go` is NOT on PATH but a
// toolchain exists in a discovery location, its bin dir is prepended to PATH.
func TestGateCommandEnv_GoMissing(t *testing.T) {
	// Build a toolchain tree at $HOME/go-toolchain/go/bin containing a fake `go`.
	home := t.TempDir()
	binDir := home + string(os.PathSeparator) + "go-toolchain" + string(os.PathSeparator) + "go" + string(os.PathSeparator) + "bin"
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeGo := binDir + string(os.PathSeparator) + "go"
	if err := os.WriteFile(fakeGo, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldHome, oldPath, oldRoot := os.Getenv("HOME"), os.Getenv("PATH"), os.Getenv("GOROOT")
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("PATH", oldPath)
		os.Setenv("GOROOT", oldRoot)
	}()
	os.Setenv("HOME", home)
	os.Setenv("GOROOT", "")
	// A PATH that provably has no `go` anywhere.
	os.Setenv("PATH", "/usr/bin:/bin")

	if _, err := exec.LookPath("go"); err == nil {
		t.Skip("go is resolvable on the restricted PATH; cannot test the missing case")
	}

	env := GateCommandEnv()
	if env == nil {
		t.Fatal("expected non-nil env with prepended toolchain PATH, got nil")
	}
	path := envPath(env)
	if !strings.HasPrefix(path, binDir) {
		t.Errorf("expected PATH to start with toolchain bin %q, got %q", binDir, path)
	}
	// Original PATH must still be present (prepended, not replaced).
	if !strings.Contains(path, "/usr/bin") {
		t.Errorf("expected original PATH entries preserved, got %q", path)
	}
}

// withEnv restores env vars to their prior values when the test ends.
// Use as: defer withEnv(t).Restore()
type envSnapshot struct {
	home, goroot, path string
}

func snapshotEnv() envSnapshot {
	return envSnapshot{
		home:   os.Getenv("HOME"),
		goroot: os.Getenv("GOROOT"),
		path:   os.Getenv("PATH"),
	}
}

func (s envSnapshot) restore() {
	os.Setenv("HOME", s.home)
	os.Setenv("GOROOT", s.goroot)
	os.Setenv("PATH", s.path)
}

// hasUsrLocalGoBin reports whether /usr/local/go/bin/go exists on this host.
// The implementation hardcodes that path, so we can only assert fallback
// behavior on hosts where the file is present.
func hasUsrLocalGoBin() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	_, err := os.Stat("/usr/local/go/bin/go")
	return err == nil
}

// writeFakeGo creates a non-functional `go` binary at dir/go with the given mode.
// The fake script always exits 0 — enough to satisfy os.Stat presence checks
// during discovery. Test only — never run.
func writeFakeGo(t *testing.T, dir string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", dir, err)
	}
	goBin := filepath.Join(dir, "go")
	if runtime.GOOS == "windows" {
		goBin += ".exe"
	}
	if err := os.WriteFile(goBin, []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
		t.Fatalf("WriteFile(%q): %v", goBin, err)
	}
	return goBin
}

// TestGateCommandEnv_NoToolchainFound verifies that GateCommandEnv returns
// nil when none of the documented candidate locations contain a `go` binary.
// This is the "let the gate fail with its own diagnostic" branch — better
// than faking a toolchain via a bogus PATH entry.
func TestGateCommandEnv_NoToolchainFound(t *testing.T) {
	if hasUsrLocalGoBin() {
		t.Skip("/usr/local/go/bin/go exists on this host; cannot exercise the no-toolchain branch")
	}

	snap := snapshotEnv()
	defer snap.restore()

	// HOME points to a directory that has no go-toolchain subtree, GOROOT is
	// unset, and PATH is restricted so `go` is not resolvable on the parent
	// PATH either.
	os.Setenv("HOME", t.TempDir())
	os.Setenv("GOROOT", "")
	os.Setenv("PATH", "/usr/bin:/bin")

	if _, err := exec.LookPath("go"); err == nil {
		t.Skip("go is resolvable on the restricted PATH; cannot test the missing case")
	}

	if env := GateCommandEnv(); env != nil {
		t.Errorf("expected nil env when no toolchain found, got %v", env)
	}
}

// TestGateCommandEnv_GoToolchainPrecedence verifies that when both GOROOT/bin
// and $HOME/go-toolchain/go/bin contain a `go` binary, GOROOT/bin is prepended
// first. The order is load-bearing: GOROOT is the operator-set override, so a
// pinned GOROOT toolchain must win over the host default.
func TestGateCommandEnv_GoToolchainPrecedence(t *testing.T) {
	if hasUsrLocalGoBin() {
		t.Skip("/usr/local/go/bin/go exists; the third slot would also be present")
	}

	snap := snapshotEnv()
	defer snap.restore()

	root := t.TempDir()
	gorootBin := filepath.Join(root, "goroot", "bin")
	writeFakeGo(t, gorootBin, 0o755)

	home := t.TempDir()
	homeBin := filepath.Join(home, "go-toolchain", "go", "bin")
	writeFakeGo(t, homeBin, 0o755)

	os.Setenv("HOME", home)
	os.Setenv("GOROOT", filepath.Join(root, "goroot"))
	os.Setenv("PATH", "/usr/bin:/bin")

	if _, err := exec.LookPath("go"); err == nil {
		t.Skip("go is resolvable on the restricted PATH; cannot test the missing case")
	}

	env := GateCommandEnv()
	if env == nil {
		t.Fatal("expected non-nil env with prepended toolchain entries, got nil")
	}
	path := envPath(env)
	gorootIdx := strings.Index(path, gorootBin)
	homeIdx := strings.Index(path, homeBin)
	if gorootIdx < 0 {
		t.Fatalf("GOROOT/bin %q not in PATH: %q", gorootBin, path)
	}
	if homeIdx < 0 {
		t.Fatalf("HOME/go-toolchain/bin %q not in PATH: %q", homeBin, path)
	}
	if gorootIdx >= homeIdx {
		t.Errorf("expected GOROOT/bin before HOME/go-toolchain in PATH, got %q", path)
	}
}

// TestGateCommandEnv_UsrLocalGoBinFallback verifies that when neither GOROOT
// nor $HOME/go-toolchain contains a `go` binary, the function falls back to
// /usr/local/go/bin (the standard tarball install location). The fallback path
// is hardcoded in the implementation, so this test runs only on hosts where
// /usr/local/go/bin/go is actually present.
func TestGateCommandEnv_UsrLocalGoBinFallback(t *testing.T) {
	if !hasUsrLocalGoBin() {
		t.Skip("/usr/local/go/bin/go not present on this host; cannot exercise fallback")
	}

	snap := snapshotEnv()
	defer snap.restore()

	// HOME has no go-toolchain subtree; GOROOT unset.
	os.Setenv("HOME", t.TempDir())
	os.Setenv("GOROOT", "")
	os.Setenv("PATH", "/usr/bin:/bin")

	if _, err := exec.LookPath("go"); err == nil {
		t.Skip("go is resolvable on the restricted PATH; cannot test the missing case")
	}

	env := GateCommandEnv()
	if env == nil {
		t.Fatal("expected non-nil env with /usr/local/go/bin prepended, got nil")
	}
	path := envPath(env)
	if !strings.Contains(path, "/usr/local/go/bin") {
		t.Errorf("expected PATH to contain /usr/local/go/bin fallback, got %q", path)
	}
}

// TestGateCommandEnv_HomeUnset_FallsBackToUserHomeDir verifies that when $HOME
// is not set, GateCommandEnv still discovers the toolchain by looking up the
// current user's home directory via os/user. Background refinery and daemon
// sessions are often launched with a minimal environment that omits HOME, which
// previously caused every Go gate to fail with "go: not found" even though the
// toolchain was installed at the user's home.
func TestGateCommandEnv_HomeUnset_FallsBackToUserHomeDir(t *testing.T) {
	if hasUsrLocalGoBin() {
		t.Skip("/usr/local/go/bin/go exists; cannot isolate the HOME-unset fallback branch")
	}

	snap := snapshotEnv()
	defer snap.restore()

	// Create a fake toolchain in a temp directory and tell the production lookup
	// to treat that temp directory as the user's home. HOME itself is left empty
	// so os.UserHomeDir would otherwise fail and the os/user fallback would be
	// exercised.
	home := t.TempDir()
	binDir := filepath.Dir(writeFakeGo(t, filepath.Join(home, "go-toolchain", "go", "bin"), 0o755))

	os.Setenv("HOME", "")
	os.Setenv("GOROOT", "")
	os.Setenv("PATH", "/usr/bin:/bin")

	if _, err := exec.LookPath("go"); err == nil {
		t.Skip("go is resolvable on the restricted PATH; cannot test the missing case")
	}

	old := userHomeDirForTest
	userHomeDirForTest = func() string { return home }
	defer func() { userHomeDirForTest = old }()

	env := GateCommandEnv()
	if env == nil {
		t.Fatal("expected non-nil env with fallback home toolchain PATH, got nil")
	}
	path := envPath(env)
	if !strings.HasPrefix(path, binDir) {
		t.Errorf("expected PATH to start with toolchain bin %q, got %q", binDir, path)
	}
	if !strings.Contains(path, "/usr/bin") {
		t.Errorf("expected original PATH entries preserved, got %q", path)
	}
}

// TestGateCommandEnv_NonExecutableGoIsDetected pins the presence-only check
// contract: the helper treats any file named `go` (regardless of executable
// bits) as a toolchain candidate. This is a deliberate trade-off — see the
// comment on goToolchainPathEntries — and a future change should fail this
// test so the discoverer of the change has to reason about whether the new
// behavior is intentional.
func TestGateCommandEnv_NonExecutableGoIsDetected(t *testing.T) {
	if hasUsrLocalGoBin() {
		t.Skip("/usr/local/go/bin/go exists; HOME-only candidate is not the only entry")
	}

	snap := snapshotEnv()
	defer snap.restore()

	home := t.TempDir()
	nonExecBin := writeFakeGo(t, filepath.Join(home, "go-toolchain", "go", "bin"), 0o644)

	os.Setenv("HOME", home)
	os.Setenv("GOROOT", "")
	os.Setenv("PATH", "/usr/bin:/bin")

	if _, err := exec.LookPath("go"); err == nil {
		t.Skip("go is resolvable on the restricted PATH; cannot test the missing case")
	}

	env := GateCommandEnv()
	if env == nil {
		t.Fatalf("expected non-nil env (presence-only check should still find %q), got nil", nonExecBin)
	}
	path := envPath(env)
	if !strings.Contains(path, filepath.Dir(nonExecBin)) {
		t.Errorf("expected PATH to contain %q despite non-executable mode, got %q",
			filepath.Dir(nonExecBin), path)
	}
}
