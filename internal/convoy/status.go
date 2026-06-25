package convoy

import (
	"context"
	"strings"

	beadsdk "github.com/steveyegge/beads"
)

// This file is the single source of truth for "is this convoy currently an
// active scan target for the daemon's stranded scan loop?". Both
// `gt convoy stranded --json` (cmd/convoy.go) and the daemon's
// ConvoyManager.scan (daemon/convoy_manager.go) gate their actions on these
// helpers so that closed/deferred/staged convoys are never fed or
// completion-checked, even when upstream `bd list --status=open` returns a
// stale or unexpected row. See gastown-cet.14.

// IsActiveJSONStatus reports whether a JSON status string (typically read
// from `bd list --json` or a convoy field) represents an active convoy
// that the daemon should consider feeding or completion-checking.
//
// An empty string is treated as "unknown" and considered active so that
// older `gt convoy stranded --json` producers without the Status field keep
// working. Callers that want a hard guarantee should also check the
// returned status explicitly via IsActiveBeadsStatus when they have a
// beadsdk.Issue in hand.
func IsActiveJSONStatus(status string) bool {
	switch status {
	case "open":
		return true
	case "":
		// Unknown / legacy — preserve prior behavior. The daemon's
		// defensive gate on the receiving side catches the rare case
		// where status is missing or unrecognized.
		return true
	case "closed", "tombstone", "deferred",
		"staged_ready", "staged_warnings",
		"in_progress", "blocked", "pinned", "hooked":
		return false
	default:
		// Unrecognized future status — be conservative and skip. The
		// convoy manager must never act on a status it doesn't
		// recognize as active, because the wrong action could reopen
		// or close work.
		return false
	}
}

// IsActiveBeadsStatus reports whether a beads issue's status string
// indicates an active convoy. It is the SDK-side counterpart to
// IsActiveJSONStatus and exists so the daemon's defensive scan can
// query via store.GetIssue rather than re-parsing JSON.
//
// An empty status (for example, when the SDK call fails) is treated as
// active so a transient lookup failure doesn't suppress the regular scan
// loop. Operators who need stricter gating can extend this function.
func IsActiveBeadsStatus(status beadsdk.Status) bool {
	switch strings.TrimSpace(string(status)) {
	case "open":
		return true
	case "":
		return true
	default:
		return false
	}
}

// IsActiveConvoy queries the supplied store for the convoy's current
// status and reports whether the daemon should act on it. It returns true
// when the lookup fails (fail-open) so a transient store outage doesn't
// silently suppress the entire stranded scan — the operator can still see
// logs and triage. Status-driven false returns (closed/deferred/staged)
// are explicit so the caller can log the skip with the right context.
func IsActiveConvoy(ctx context.Context, store beadsdk.Storage, convoyID string) bool {
	if store == nil {
		return true
	}
	issue, err := store.GetIssue(ctx, convoyID)
	if err != nil || issue == nil {
		// Fail-open: log via caller if interested.
		return true
	}
	return IsActiveBeadsStatus(issue.Status)
}
