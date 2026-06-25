package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

// ErrNoExplicitAgent indicates that a queued bead was rejected from native
// scheduler dispatch because model-mix caps are active and the bead's sling
// context does not carry an explicit allowed model assignment. Without an
// explicit agent, native dispatch would fall back to Claude Code's default
// model path, silently drifting the fleet mix (gastown-cet.16.2).
//
// The fix path is for callers to drain queued work through
// `gt sling --agent <model> --merge=mr` or via the model-mix maintainer
// (gt-model-mix-maintainer.py) so each lane records a durable assignment.
var ErrNoExplicitAgent = fmt.Errorf("bead has no explicit model assignment under model-mix caps")

// ErrAgentNotInMixCaps indicates that the explicit Agent value on a queued
// bead is not present in the configured model-mix caps map. The configured
// set is the only allowed set under caps, so unknown values must be rejected.
var ErrAgentNotInMixCaps = fmt.Errorf("bead agent is not in configured model-mix caps")

// ValidateExplicitAgentForMixCaps enforces the model-mix guard described in
// gastown-cet.16.2. When caps are inactive, no check is performed and the
// function returns nil. When caps are active:
//
//  1. If the agent is empty, ErrNoExplicitAgent is returned. The bead was
//     scheduled without --agent, so dispatching it would fall back to the
//     default Claude Code model and silently drift the fleet mix.
//  2. If the agent is set but is not in the configured caps map,
//     ErrAgentNotInMixCaps is returned. The configured set is the only
//     allowed set under caps.
//
// The "caps are active" predicate is (cfg.GetModelMixCaps() non-empty AND
// cfg.GetRequireExplicitAgent() true). Both conditions must hold — caps
// without the enforcement flag are merely informational.
func ValidateExplicitAgentForMixCaps(agent string, cfg *capacity.SchedulerConfig) error {
	if cfg == nil {
		return nil
	}
	if !cfg.IsModelMixCapped() {
		return nil
	}
	if !cfg.GetRequireExplicitAgent() {
		return nil
	}

	agent = strings.TrimSpace(agent)
	if agent == "" {
		return fmt.Errorf("%w: schedule beads with `gt sling --agent <model> --merge=mr` (or the model-mix maintainer) so each lane records a durable model assignment",
			ErrNoExplicitAgent)
	}

	caps := cfg.GetModelMixCaps()
	if _, ok := caps[agent]; !ok {
		// Sort keys for stable error output (Go map iteration is randomized).
		allowed := make([]string, 0, len(caps))
		for k := range caps {
			allowed = append(allowed, k)
		}
		sort.Strings(allowed)
		return fmt.Errorf("%w: agent=%q allowed=%v — either fix the schedule's --agent or extend scheduler.model_mix_caps to include this model",
			ErrAgentNotInMixCaps, agent, allowed)
	}

	return nil
}
