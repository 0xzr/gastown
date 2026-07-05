package cmd

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExecuteSling_OpenMRBlocksDispatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
if [ "${1:-}" = "--allow-stale" ]; then
  shift
fi
case "$1" in
  show)
    printf '%s\n' '[{"id":"gt-work","title":"Work","status":"open","assignee":"","description":""}]'
    ;;
  list)
    printf '%s\n' '[{"id":"gt-wisp-mr1","title":"MR","status":"open","description":"branch: polecat/rust/gt-work@abc\ntarget: main\nsource_issue: gt-work\nrig: gastown\n","labels":["gt:merge-request"]}]'
    ;;
  sql)
    printf '%s\n' '[]'
    ;;
  *)
    echo "unexpected bd args: $*" >&2
    exit 1
    ;;
esac
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result, err := executeSling(SlingParams{
		BeadID:   "gt-work",
		RigName:  "testrig",
		TownRoot: townRoot,
	})
	if err == nil {
		t.Fatal("expected open-MR dispatch skip, got nil")
	}
	if !errors.Is(err, errSlingSkipped) {
		t.Fatalf("error = %v, want errSlingSkipped", err)
	}
	want := "bead gt-work has open MR gt-wisp-mr1 — dispatch blocked; process the MR instead"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
	if result == nil || result.ErrMsg != want {
		t.Fatalf("result.ErrMsg = %q, want %q", result.ErrMsg, want)
	}
}

func TestOpenMRDispatchGuard_ReadErrorAllowsDispatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	bdScript := `#!/bin/sh
if [ "${1:-}" = "--allow-stale" ]; then
  shift
fi
case "$1" in
  list)
    echo "merge queue unavailable" >&2
    exit 2
    ;;
  *)
    echo '[]'
    ;;
esac
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stderr strings.Builder
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = oldStderr }()

	guardErr := checkOpenMRDispatchGuard(townRoot, filepath.Join(townRoot, ".beads"), "gt-work")

	_ = w.Close()
	os.Stderr = oldStderr
	out, _ := io.ReadAll(r)
	stderr.Write(out)

	if guardErr != nil {
		t.Fatalf("guard error = %v, want nil", guardErr)
	}
	if !strings.Contains(stderr.String(), "could not read merge queue for bead gt-work") {
		t.Fatalf("stderr missing merge queue warning: %q", stderr.String())
	}
}
