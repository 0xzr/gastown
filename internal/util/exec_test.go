package util

import (
	"os"
	"os/exec"
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
