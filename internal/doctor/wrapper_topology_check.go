package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WrapperTopologyCheck verifies that the operational gt wrapper topology
// is intact at the user's install path. The wrapper is the bash script that
// injects --agent / --merge=mr / model-status into `gt sling` calls so the
// fleet-fill scheduler and the model-mix supervisor keep their reliable
// signal. If `~/.local/bin/gt` is a raw ELF binary, that signal is gone
// (see gastown-cet.16.1 for the originating incident: a pinned-binary
// cutover on 2026-06-25 clobbered the wrapper, the fleet silently drifted
// out of its caps, and ready work stopped being wrapper-assigned).
//
// Topology the check enforces:
//   - If gt is a wrapper script at <install>/gt: <install>/gt-real-bin
//     must exist, be executable, and look like an ELF.
//   - If gt is a plain ELF at <install>/gt: that is allowed (no wrapper).
//   - If gt is a wrapper, the wrapper must carry the well-known marker
//     header (so an arbitrary text file cannot masquerade as the wrapper).
//   - `gt model-status` (the wrapper-level command) must exit 0. If the
//     wrapper is present but its dispatch is broken, this is the cheapest
//     end-to-end smoke test we can run without state.
//
// Auto-fix is intentionally NOT supported: there is no way to reconstruct
// the wrapper or the real-bin binary from binary inspection alone. The
// check reports a clear remediation message instead.
type WrapperTopologyCheck struct {
	BaseCheck
	// HomeDir overrides $HOME for tests. Empty => use os.UserHomeDir.
	HomeDir string
	// InstallDirOverride overrides ~/.local/bin for tests. Empty => default.
	InstallDirOverride string
	// GTRealBinOverride overrides <install>/gt-real-bin. Empty => default.
	GTRealBinOverride string
	// SkipModelStatusProbe disables the gt model-status probe (used by tests
	// and when gt itself is broken enough that running it would mask the
	// underlying failure).
	SkipModelStatusProbe bool
}

// NewWrapperTopologyCheck creates a new wrapper-topology check.
func NewWrapperTopologyCheck() *WrapperTopologyCheck {
	return &WrapperTopologyCheck{
		BaseCheck: BaseCheck{
			CheckName:        "wrapper-topology",
			CheckDescription: "Verify ~/.local/bin/gt wrapper topology (operational model-mix wrapper preserved across installs)",
			CheckCategory:    CategoryInfrastructure,
		},
	}
}

// WrapperMarker is the human-edited comment that identifies the operational
// gt wrapper. Must match scripts/lib/wrapper-preserve.sh (kept in sync so
// the install path and the doctor check agree on what counts as "the
// wrapper").
const WrapperMarker = "gt wrapper — guarantees the current validation model-mix"

// WrapperPath returns the public install path the check probes.
func (c *WrapperTopologyCheck) wrapperPath() (string, error) {
	if c.InstallDirOverride != "" {
		return filepath.Join(c.InstallDirOverride, "gt"), nil
	}
	home := c.HomeDir
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate home dir: %w", err)
		}
	}
	return filepath.Join(home, ".local", "bin", "gt"), nil
}

// RealBinPath returns the real-bin slot the wrapper proxies to.
func (c *WrapperTopologyCheck) realBinPath() (string, error) {
	if c.GTRealBinOverride != "" {
		return c.GTRealBinOverride, nil
	}
	wp, err := c.wrapperPath()
	if err != nil {
		return "", err
	}
	return wp + "-real-bin", nil
}

// firstByte reads the first byte of path as a string token suitable for
// pattern matching. Returns "" if the file is missing or unreadable.
// Exported as-is so tests can construct synthetic topologies cheaply.
func firstByte(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	var buf [1]byte
	n, err := f.Read(buf[:])
	if err != nil || n != 1 {
		return ""
	}
	return string(buf[:])
}

// Run evaluates the topology. Returns an Error-severity result if the
// wrapper has been clobbered, a Warning if the wrapper is missing the
// well-known marker (we cannot tell whether it was the operational one),
// and OK otherwise.
func (c *WrapperTopologyCheck) Run(ctx *CheckContext) *CheckResult {
	wrapper, err := c.wrapperPath()
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Cannot determine wrapper install path",
			Details: []string{err.Error()},
		}
	}

	// No install yet → nothing to assert.
	if _, err := os.Stat(wrapper); err != nil {
		if os.IsNotExist(err) {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusOK,
				Message: "No gt installed yet (fresh host)",
			}
		}
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Cannot stat wrapper install path",
			Details: []string{err.Error()},
		}
	}

	switch firstByte(wrapper) {
	case "\x7f", "E":
		// 0x7F 'E' 'L' 'F' — public path is a raw ELF. That is allowed
		// (plain-binary topology) but it means model-status, --agent
		// injection, and merge=mr are NOT happening at dispatch. Flag a
		// Warning so operators can decide whether they meant to drop the
		// wrapper, and check whether a sibling wrapper-preserved slot
		// exists that we could be using instead.
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Public gt is a raw ELF binary (no wrapper)",
			Details: []string{
				fmt.Sprintf("public: %s", wrapper),
				"If an operational wrapper is intended (model-mix, --agent injection, model-status), reinstall via `make safe-install` from a source tree where scripts/lib/wrapper-preserve.sh is present.",
			},
		}

	case "#":
		// Shebang → text script. Either the operational wrapper or some
		// other script the operator dropped in. Distinguish by marker.
		if !c.hasWrapperMarker(wrapper) {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusWarning,
				Message: "Public gt is a script but lacks the wrapper marker header",
				Details: []string{
					fmt.Sprintf("public: %s", wrapper),
					fmt.Sprintf("expected header marker: %q", WrapperMarker),
					"This may be a stale or hand-edited wrapper. If it is the operational wrapper, re-emit it from scripts/lib/wrapper-preserve.sh.",
				},
			}
		}
		// Recognized wrapper. Verify the real-bin slot.
		realBin, err := c.realBinPath()
		if err != nil {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusError,
				Message: "Cannot resolve real-bin path while wrapper is present",
				Details: []string{err.Error()},
			}
		}
		return c.checkRealBin(wrapper, realBin)

	default:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "Public gt is neither a wrapper script nor an ELF binary",
			Details: []string{
				fmt.Sprintf("public: %s", wrapper),
				"Refusing to interpret a partial install. Re-run `make safe-install` from the source repo.",
			},
		}
	}
}

// hasWrapperMarker returns true when path starts with the operational
// wrapper marker (looked up in the first 30 lines, matching the install
// path's heuristic in scripts/lib/wrapper-preserve.sh).
func (c *WrapperTopologyCheck) hasWrapperMarker(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	// Read up to ~8 KiB; the marker lives in the comment header. Keep this
	// small so we don't slurp a 200-MiB ELF by accident (in case the path
	// somehow points at the binary — firstByte should already filter that
	// out, but defense in depth).
	buf := make([]byte, 8192)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return false
	}
	return strings.Contains(string(buf[:n]), WrapperMarker)
}

// checkRealBin enforces the wrapper topology invariants on the real-bin slot.
func (c *WrapperTopologyCheck) checkRealBin(wrapper, realBin string) *CheckResult {
	info, err := os.Stat(realBin)
	if err != nil {
		if os.IsNotExist(err) {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusError,
				Message: "Wrapper present but real-bin ELF is missing",
				Details: []string{
					fmt.Sprintf("wrapper: %s", wrapper),
					fmt.Sprintf("expected real-bin: %s", realBin),
					"Remediation: re-run `make safe-install` from the source repo. It will rebuild the ELF into the real-bin slot without touching the wrapper.",
				},
				FixHint: "make safe-install",
			}
		}
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "Cannot stat real-bin ELF",
			Details: []string{err.Error()},
		}
	}
	if !info.Mode().IsRegular() {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "Real-bin slot is not a regular file",
			Details: []string{fmt.Sprintf("%s: mode=%v", realBin, info.Mode())},
		}
	}
	// Executable bit. Don't be too strict — 0755 vs 0700 both pass sling
	// execution — but at minimum one of user/group/other execute must be set.
	if info.Mode().Perm()&0o111 == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "Real-bin ELF is not executable",
			Details: []string{
				fmt.Sprintf("%s: mode=%v", realBin, info.Mode()),
				"Remediation: chmod +x the real-bin ELF or re-run `make safe-install`.",
			},
			FixHint: fmt.Sprintf("chmod +x %s", realBin),
		}
	}
	switch firstByte(realBin) {
	case "\x7f", "E":
		// Looks like an ELF. Move on to the model-status probe.
	default:
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "Real-bin slot does not look like an ELF binary",
			Details: []string{
				fmt.Sprintf("%s: first byte is neither 0x7F nor 'E'", realBin),
				"Remediation: re-run `make safe-install` to overwrite it.",
			},
			FixHint: "make safe-install",
		}
	}

	if c.SkipModelStatusProbe {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Wrapper topology intact (model-status probe skipped)",
			Details: []string{
				fmt.Sprintf("wrapper: %s", wrapper),
				fmt.Sprintf("real-bin: %s", realBin),
			},
		}
	}

	// End-to-end smoke: invoke the public gt with `model-status`. This is
	// the cheapest way to catch a wrapper that has been preserved but is
	// now mis-dispatching (e.g., points at a stale real-bin from a prior
	// version that no longer supports model-status). We accept any
	// recognized exit code other than "unknown command" — that particular
	// failure is the historical fingerprint of the wrapper having been
	// clobbered by a raw ELF.
	cmd := exec.Command(wrapper, "model-status")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Wrapper topology intact",
			Details: []string{
				fmt.Sprintf("wrapper: %s", wrapper),
				fmt.Sprintf("real-bin: %s", realBin),
				"gt model-status: OK",
			},
		}
	}
	// Inspect exit error type. exitcode > 0 from a recognized subcommand
	// (e.g., "no agents assigned yet") is not a topology failure.
	exitErr, ok := err.(*exec.ExitError)
	if ok && strings.Contains(string(out), "unknown command") {
		details := []string{
			fmt.Sprintf("wrapper: %s", wrapper),
			fmt.Sprintf("real-bin: %s", realBin),
		}
		if len(out) > 0 {
			details = append(details, "model-status output: "+strings.TrimSpace(string(out)))
		}
		details = append(details,
			"Remediation: re-run `make safe-install` from the source repo so the wrapper is reinstated and the real-bin ELF is rebuilt.")
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: "Public gt reports `unknown command` for model-status (wrapper clobbered by raw ELF — see gastown-cet.16.1)",
			Details: details,
			FixHint: "make safe-install",
		}
	}
	// Non-zero exit but not the clobber fingerprint — record as a warning
	// rather than an error so a noisy model-status (e.g., a transient Dolt
	// hiccup while reading the agent ledger) does not turn the entire
	// doctor report red and block fleet-fill.
	msg := "Wrapper topology intact but `gt model-status` returned non-zero"
	details := []string{
		fmt.Sprintf("wrapper: %s", wrapper),
		fmt.Sprintf("real-bin: %s", realBin),
	}
	if exitErr != nil {
		details = append(details, fmt.Sprintf("exit=%d", exitErr.ExitCode()))
	} else {
		details = append(details, fmt.Sprintf("error: %v", err))
	}
	if len(out) > 0 {
		details = append(details, "model-status output: "+strings.TrimSpace(string(out)))
	}
	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: msg,
		Details: details,
		FixHint: "If this persists, run `gt doctor` and inspect fleet-supervisor state.",
	}
}
