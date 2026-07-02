package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSetGetDefaultSocket(t *testing.T) {
	// Save and restore
	orig := defaultSocket
	defer func() { defaultSocket = orig }()

	// Initially empty
	SetDefaultSocket("")
	if got := GetDefaultSocket(); got != "" {
		t.Errorf("expected empty, got %q", got)
	}

	SetDefaultSocket("mytown")
	if got := GetDefaultSocket(); got != "mytown" {
		t.Errorf("expected %q, got %q", "mytown", got)
	}
}

func TestNewTmuxInheritsSocket(t *testing.T) {
	orig := defaultSocket
	defer func() { defaultSocket = orig }()

	SetDefaultSocket("testtown")
	tmx := NewTmux()
	if tmx.socketName != "testtown" {
		t.Errorf("NewTmux() socketName = %q, want %q", tmx.socketName, "testtown")
	}
}

func TestNewTmuxWithSocket(t *testing.T) {
	tmx := NewTmuxWithSocket("custom")
	if tmx.socketName != "custom" {
		t.Errorf("NewTmuxWithSocket() socketName = %q, want %q", tmx.socketName, "custom")
	}
}

func TestBuildCommandNoSocket(t *testing.T) {
	orig := defaultSocket
	defer func() { defaultSocket = orig }()

	SetDefaultSocket("")
	cmd := BuildCommand("list-sessions")
	args := cmd.Args
	// Should be: tmux -u list-sessions
	expected := []string{"tmux", "-u", "list-sessions"}
	if len(args) != len(expected) {
		t.Fatalf("args = %v, want %v", args, expected)
	}
	for i, a := range args {
		if a != expected[i] {
			t.Errorf("args[%d] = %q, want %q", i, a, expected[i])
		}
	}
}

func TestBuildCommandWithSocket(t *testing.T) {
	orig := defaultSocket
	defer func() { defaultSocket = orig }()

	SetDefaultSocket("mytown")
	cmd := BuildCommand("has-session", "-t", "hq-mayor")
	args := cmd.Args
	// Should be: tmux -u -L mytown has-session -t hq-mayor
	expected := []string{"tmux", "-u", "-L", "mytown", "has-session", "-t", "hq-mayor"}
	if len(args) != len(expected) {
		t.Fatalf("args = %v, want %v", args, expected)
	}
	for i, a := range args {
		if a != expected[i] {
			t.Errorf("args[%d] = %q, want %q", i, a, expected[i])
		}
	}
}

func TestKillServerAndRemoveSocketRemovesSocketFile(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	socket := fmt.Sprintf("gt-test-socket-cleanup-%d", os.Getpid())
	tm := NewTmuxWithSocket(socket)
	t.Cleanup(func() {
		_ = tm.KillServer()
		_ = RemoveSocketFile(socket)
	})

	if err := tm.NewSession("gt-test-bootstrap", ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := os.Stat(SocketPath(socket)); err != nil {
		t.Fatalf("socket path was not created: %v", err)
	}
	if err := tm.KillServerAndRemoveSocket(); err != nil {
		t.Fatalf("KillServerAndRemoveSocket: %v", err)
	}
	if _, err := os.Stat(SocketPath(socket)); !os.IsNotExist(err) {
		t.Fatalf("socket path still exists after cleanup: %v", err)
	}
}

func TestEnsureTownServerBootstrapsOwnedEmptyServer(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	socket := fmt.Sprintf("gt-test-owned-%d", os.Getpid())
	townRoot := t.TempDir()
	tm := NewTmuxWithSocket(socket)
	t.Cleanup(func() {
		_ = tm.KillServer()
		_ = RemoveSocketFile(socket)
	})

	if err := EnsureTownServer(townRoot, socket); err != nil {
		t.Fatalf("EnsureTownServer: %v", err)
	}
	info, err := tm.ServerInfo()
	if err != nil {
		t.Fatalf("ServerInfo: %v", err)
	}
	if info.Owner != "gt" {
		t.Fatalf("owner = %q, want gt", info.Owner)
	}
	if info.TownRoot != townRoot {
		t.Fatalf("town root = %q, want %q", info.TownRoot, townRoot)
	}
	if info.RecordedSocket != socket {
		t.Fatalf("recorded socket = %q, want %q", info.RecordedSocket, socket)
	}
	if info.Origin != "gt-daemon-bootstrap" {
		t.Fatalf("origin = %q, want gt-daemon-bootstrap", info.Origin)
	}
	for _, session := range info.Sessions {
		if session == townServerBootstrapAnchor {
			t.Fatalf("bootstrap anchor session should have been removed; sessions=%v", info.Sessions)
		}
	}
	if info.Argv != "" && !containsAll(info.Argv, "gt-tmux-anchor") {
		t.Fatalf("server argv = %q, want bootstrap anchor evidence", info.Argv)
	}
}

func containsAll(s string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(s, needle) {
			return false
		}
	}
	return true
}
