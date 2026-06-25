package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a tiny helper that fails the test on error.
func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// writeELF writes a tiny file whose first byte is 0x7F (the ELF magic).
// We don't need a real ELF — the wrapper-topology check only inspects the
// first byte and Stat info.
func writeELF(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte{0x7F, 'E', 'L', 'F', 0, 0, 0, 0}, mode); err != nil {
		t.Fatalf("write ELF %s: %v", path, err)
	}
}

// wrapperHeader is the marker the wrapper check looks for in the first
// 30 lines of any text it considers a wrapper candidate.
const wrapperHeader = "#!/usr/bin/env bash\n# gt wrapper — guarantees the current validation model-mix on `gt sling`.\n"

// fakeWrapperOK is a script that always exits 0 — used when the test wants
// to simulate an operational wrapper that successfully handles `model-status`.
const fakeWrapperOK = "#!/usr/bin/env bash\n# gt wrapper — guarantees the current validation model-mix on `gt sling`.\nexit 0\n"

// fakeWrapperUnknownCommand is the historical fingerprint of the
// gastown-cet.16.1 incident: the wrapper is gone, the raw ELF has been
// dropped at the public path, and the operator runs `gt model-status` and
// sees "unknown command".
const fakeWrapperUnknownCommand = "#!/usr/bin/env bash\n# gt wrapper — guarantees the current validation model-mix on `gt sling`.\necho \"unknown command\" >&2\nexit 1\n"

func TestWrapperTopologyCheck_Name(t *testing.T) {
	c := NewWrapperTopologyCheck()
	if got, want := c.Name(), "wrapper-topology"; got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
	if got, want := c.Category(), CategoryInfrastructure; got != want {
		t.Fatalf("Category() = %q, want %q", got, want)
	}
	if c.CanFix() {
		t.Fatal("CanFix() = true, want false (wrapper cannot be auto-reconstructed)")
	}
}

func TestWrapperTopologyCheck_NoInstall(t *testing.T) {
	tmp := t.TempDir()
	c := NewWrapperTopologyCheck()
	c.InstallDirOverride = filepath.Join(tmp, ".local", "bin")

	res := c.Run(&CheckContext{})
	if res.Status != StatusOK {
		t.Fatalf("Status = %v, want OK (fresh host); message=%q", res.Status, res.Message)
	}
	if res.Message == "" {
		t.Fatal("Message is empty; expected a 'no gt installed yet' explanation")
	}
}

// Plain-ELF topology: ~./local/bin/gt is a raw ELF, no wrapper. Allowed.
func TestWrapperTopologyCheck_PlainELF_OK(t *testing.T) {
	tmp := t.TempDir()
	install := filepath.Join(tmp, ".local", "bin")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	gtPath := filepath.Join(install, "gt")
	writeELF(t, gtPath, 0o755)

	c := NewWrapperTopologyCheck()
	c.InstallDirOverride = install
	c.SkipModelStatusProbe = true

	res := c.Run(&CheckContext{})
	if res.Status != StatusWarning {
		t.Fatalf("Status = %v, want Warning (raw ELF on public path); message=%q", res.Status, res.Message)
	}
	// Warning rather than Error because plain-binary is a legitimate
	// topology; we just want the operator to confirm wrapper intent.
	if !strings.Contains(res.Message, "raw ELF") {
		t.Errorf("Message = %q, want it to mention 'raw ELF'", res.Message)
	}
}

// Wrapper topology intact: wrapper at public path, ELF at real-bin slot,
// and `gt model-status` returns 0.
func TestWrapperTopologyCheck_WrapperIntact_OK(t *testing.T) {
	tmp := t.TempDir()
	install := filepath.Join(tmp, ".local", "bin")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	gtPath := filepath.Join(install, "gt")
	realBin := filepath.Join(install, "gt-real-bin")
	writeFile(t, gtPath, fakeWrapperOK, 0o755)
	writeELF(t, realBin, 0o755)

	c := NewWrapperTopologyCheck()
	c.InstallDirOverride = install
	// Probe is enabled by default; fakeWrapperOK exits 0.

	res := c.Run(&CheckContext{})
	if res.Status != StatusOK {
		t.Fatalf("Status = %v, want OK; message=%q details=%v",
			res.Status, res.Message, res.Details)
	}
	if !strings.Contains(res.Message, "intact") {
		t.Errorf("Message = %q, want it to confirm topology is intact", res.Message)
	}
}

// Wrapper topology broken: wrapper preserved, real-bin missing.
// This is the post-incident state if a manual repair left the wrapper
// in place but no ELF behind it.
func TestWrapperTopologyCheck_WrapperPresentRealBinMissing_Error(t *testing.T) {
	tmp := t.TempDir()
	install := filepath.Join(tmp, ".local", "bin")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	gtPath := filepath.Join(install, "gt")
	writeFile(t, gtPath, fakeWrapperOK, 0o755)
	// Intentionally do NOT create gt-real-bin.

	c := NewWrapperTopologyCheck()
	c.InstallDirOverride = install

	res := c.Run(&CheckContext{})
	if res.Status != StatusError {
		t.Fatalf("Status = %v, want Error; message=%q", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "real-bin") || !strings.Contains(res.Message, "missing") {
		t.Errorf("Message = %q, want it to flag the missing real-bin ELF", res.Message)
	}
	if res.FixHint == "" {
		t.Error("FixHint is empty; expected `make safe-install` guidance")
	}
	if !strings.Contains(res.FixHint, "make safe-install") {
		t.Errorf("FixHint = %q, want it to mention `make safe-install`", res.FixHint)
	}
}

// Wrapper topology broken: real-bin ELF is present but not executable.
func TestWrapperTopologyCheck_RealBinNotExecutable_Error(t *testing.T) {
	tmp := t.TempDir()
	install := filepath.Join(tmp, ".local", "bin")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	gtPath := filepath.Join(install, "gt")
	realBin := filepath.Join(install, "gt-real-bin")
	writeFile(t, gtPath, fakeWrapperOK, 0o755)
	writeELF(t, realBin, 0o644) // not executable

	c := NewWrapperTopologyCheck()
	c.InstallDirOverride = install
	c.SkipModelStatusProbe = true // skip model-status so the executable check dominates

	res := c.Run(&CheckContext{})
	if res.Status != StatusError {
		t.Fatalf("Status = %v, want Error; message=%q", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "not executable") {
		t.Errorf("Message = %q, want it to mention 'not executable'", res.Message)
	}
}

// Real-bin slot exists but does not look like an ELF — corrupted install.
func TestWrapperTopologyCheck_RealBinNotELF_Error(t *testing.T) {
	tmp := t.TempDir()
	install := filepath.Join(tmp, ".local", "bin")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	gtPath := filepath.Join(install, "gt")
	realBin := filepath.Join(install, "gt-real-bin")
	writeFile(t, gtPath, fakeWrapperOK, 0o755)
	// Write some non-ELF content but mark it executable.
	if err := os.WriteFile(realBin, []byte("definitely not an elf\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	c := NewWrapperTopologyCheck()
	c.InstallDirOverride = install
	c.SkipModelStatusProbe = true

	res := c.Run(&CheckContext{})
	if res.Status != StatusError {
		t.Fatalf("Status = %v, want Error; message=%q", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "ELF") {
		t.Errorf("Message = %q, want it to flag non-ELF content", res.Message)
	}
}

// Wrapper marker absent: text file at public path but not the wrapper we
// recognize. Warning rather than Error — operator may have a deliberate
// hand-written script at that path.
func TestWrapperTopologyCheck_WrapperMarkerMissing_Warning(t *testing.T) {
	tmp := t.TempDir()
	install := filepath.Join(tmp, ".local", "bin")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	gtPath := filepath.Join(install, "gt")
	writeFile(t, gtPath, "#!/usr/bin/env bash\necho hello\n", 0o755)

	c := NewWrapperTopologyCheck()
	c.InstallDirOverride = install

	res := c.Run(&CheckContext{})
	if res.Status != StatusWarning {
		t.Fatalf("Status = %v, want Warning; message=%q", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "marker") {
		t.Errorf("Message = %q, want it to mention the missing wrapper marker", res.Message)
	}
}

// File at public path is neither ELF nor shebang — partial install.
func TestWrapperTopologyCheck_UnknownFormat_Error(t *testing.T) {
	tmp := t.TempDir()
	install := filepath.Join(tmp, ".local", "bin")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	gtPath := filepath.Join(install, "gt")
	writeFile(t, gtPath, "garbage data, no shebang or ELF magic", 0o755)

	c := NewWrapperTopologyCheck()
	c.InstallDirOverride = install

	res := c.Run(&CheckContext{})
	if res.Status != StatusError {
		t.Fatalf("Status = %v, want Error; message=%q", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "neither") {
		t.Errorf("Message = %q, want it to flag the unrecognized format", res.Message)
	}
}

// The exact fingerprint of gastown-cet.16.1: the wrapper script at the
// public path was preserved (operator restored it manually), but the
// pointed-at real-bin ELF is a stale binary that does not understand
// `model-status`. Result: `gt model-status` returns non-zero with
// "unknown command" — the same diagnostic the operator saw on 2026-06-25.
func TestWrapperTopologyCheck_ModelStatusUnknownCommand_Error(t *testing.T) {
	tmp := t.TempDir()
	install := filepath.Join(tmp, ".local", "bin")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	gtPath := filepath.Join(install, "gt")
	realBin := filepath.Join(install, "gt-real-bin")
	writeFile(t, gtPath, fakeWrapperUnknownCommand, 0o755)
	writeELF(t, realBin, 0o755)

	c := NewWrapperTopologyCheck()
	c.InstallDirOverride = install

	res := c.Run(&CheckContext{})
	if res.Status != StatusError {
		t.Fatalf("Status = %v, want Error; message=%q", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "model-status") || !strings.Contains(res.Message, "unknown command") {
		t.Errorf("Message = %q, want it to call out the 'unknown command' fingerprint", res.Message)
	}
	if !strings.Contains(res.FixHint, "make safe-install") {
		t.Errorf("FixHint = %q, want it to suggest `make safe-install`", res.FixHint)
	}
}

// Non-zero exit from model-status that is NOT the "unknown command"
// fingerprint should downgrade to Warning — we do not want a transient
// Dolt hiccup while reading the agent ledger to turn the entire doctor
// report red and gate fleet-fill.
func TestWrapperTopologyCheck_ModelStatusNonZeroNonUnknown_Warning(t *testing.T) {
	tmp := t.TempDir()
	install := filepath.Join(tmp, ".local", "bin")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	gtPath := filepath.Join(install, "gt")
	realBin := filepath.Join(install, "gt-real-bin")
	// exit 2 with a non-`unknown command` message — e.g., "no agents yet".
	script := "#!/usr/bin/env bash\n# gt wrapper — guarantees the current validation model-mix on `gt sling`.\necho \"no agents assigned yet\" >&2\nexit 2\n"
	writeFile(t, gtPath, script, 0o755)
	writeELF(t, realBin, 0o755)

	c := NewWrapperTopologyCheck()
	c.InstallDirOverride = install

	res := c.Run(&CheckContext{})
	if res.Status != StatusWarning {
		t.Fatalf("Status = %v, want Warning (transient model-status noise); message=%q",
			res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "model-status") {
		t.Errorf("Message = %q, want it to mention model-status", res.Message)
	}
}

// Defensive: ensure JSON-serializable result (doctor prints Details as
// a list of strings; this catches accidental fmt.Sprintf of map types
// that would later break report printing).
func TestWrapperTopologyCheck_ResultIsJSONSerializable(t *testing.T) {
	tmp := t.TempDir()
	install := filepath.Join(tmp, ".local", "bin")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	gtPath := filepath.Join(install, "gt")
	writeFile(t, gtPath, fakeWrapperOK, 0o755)
	realBin := filepath.Join(install, "gt-real-bin")
	writeELF(t, realBin, 0o755)

	c := NewWrapperTopologyCheck()
	c.InstallDirOverride = install

	res := c.Run(&CheckContext{})
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("json.Marshal(res) error = %v", err)
	}
	if len(b) == 0 {
		t.Fatal("json.Marshal(res) produced empty bytes")
	}
}

// Smoke test: the wrapperHeader constant is a recognizable prefix used by
// the install path's heuristic. If this drifts, scripts/lib/wrapper-preserve.sh
// and the doctor check will silently disagree about what counts as "the
// wrapper". Make the test fail loudly when the marker drifts.
func TestWrapperTopologyCheck_MarkerStable(t *testing.T) {
	want := "gt wrapper — guarantees the current validation model-mix"
	if WrapperMarker != want {
		t.Fatalf("WrapperMarker drift detected:\n  got:  %q\n  want: %q\n"+
			"Update scripts/lib/wrapper-preserve.sh to match before merging.",
			WrapperMarker, want)
	}
}
