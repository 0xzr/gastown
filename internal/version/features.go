// Package version provides build-time feature and runtime-line declarations that
// let a binary prove it belongs to an operator-approved pinned runtime line while
// still carrying backported hardening fixes.
package version

import "strings"

// PinnedRuntimeLine is the operator-approved runtime line this binary reports
// as (for example, "1.2.0"). It is normally empty for regular development builds;
// cutover builds set it via ldflags so the deployable binary stays on the
// approved pinned line while containing newer hardening fixes.
var PinnedRuntimeLine = ""

// FeatureFlags is a comma-separated list of hardening fixes/backports baked
// into this binary. Cutover builds list fixes explicitly so operators and
// automation can verify the binary contains required hardening before relying
// on the new behavior.
var FeatureFlags = ""

// FeatureList returns the parsed FeatureFlags list, or nil when unset.
func FeatureList() []string {
	if FeatureFlags == "" {
		return nil
	}
	parts := strings.Split(FeatureFlags, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// HasFeature reports whether a named flag is present in FeatureFlags.
func HasFeature(name string) bool {
	for _, f := range FeatureList() {
		if f == name {
			return true
		}
	}
	return false
}

// IsPinnedRuntime reports whether this binary was built for the named
// operator-approved runtime line.
func IsPinnedRuntime(line string) bool {
	return PinnedRuntimeLine != "" && PinnedRuntimeLine == line
}
