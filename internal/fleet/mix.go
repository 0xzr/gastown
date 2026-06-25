// Package fleet provides town-wide fleet fill primitives: model-mix
// reconciliation across all active rigs and live lane counting.
//
// Background
//
// gastown-1l8 surfaced a fleet-fill gap: a configured six-polecat pool with
// caps (umans-glm=2, umans-kimi=2, m3=2) drifted to 2 GLM / 3 Kimi / 1 M3 and
// stayed underfilled because the sling-time round-robin did not reconcile
// against the live model counts already occupying lanes. External dropin
// scripts (gt-model-mix-maintainer.py, gt-mayor-autonudge.sh) wrap this
// behavior, but the canonical reconciliation algorithm lives here so any
// Go-native dispatcher (or a future port of the dropin) uses the same logic.
//
// Scope: this package is purely town-wide fleet fill. Witness polecat
// lifecycle (recovery, restart, kill) is out of scope; see
// WITNESS_SCOPED_RESTART_RECOVERY_POLICY.md.
package fleet

import (
	"errors"
	"sort"
)

// ErrNoEligibleAgent is returned when every config agent is either at cap
// or marked unhealthy. Callers should treat this as a no-op, not a fault.
var ErrNoEligibleAgent = errors.New("fleet: no eligible model under caps")

// MixCaps maps a model name to its per-town concurrent-lane cap. The map is
// expected to enumerate every config agent — a missing entry is treated as
// cap=0 (never picked).
type MixCaps map[string]int

// LiveCounts maps a model name to its current occupied-lane count, summed
// across every active rig (NOT just one rig — the bug fixed here was a
// single-rig query that undercounted live lanes).
type LiveCounts map[string]int

// HealthyAgents maps a model name to whether it is currently eligible for
// dispatch. A false entry suppresses that model even if it has headroom.
type HealthyAgents map[string]bool

// RotationOrder is the deterministic tiebreaker when multiple models are
// eligible. It is the rotation list used to advance the index — typically
// each entry repeated up to its cap. See NewRotationFromCaps.
type RotationOrder []string

// NewRotationFromCaps returns the rotation sequence for a MixCaps map. The
// order of iteration is sorted alphabetically by model name so the rotation
// is stable across runs and rigs (Go map iteration order is randomized).
func NewRotationFromCaps(caps MixCaps) RotationOrder {
	names := make([]string, 0, len(caps))
	for name := range caps {
		names = append(names, name)
	}
	sort.Strings(names)

	var rot RotationOrder
	for _, name := range names {
		c := caps[name]
		if c < 0 {
			c = 0
		}
		for i := 0; i < c; i++ {
			rot = append(rot, name)
		}
	}
	return rot
}

// PickResult is what ChooseAgent returns: the next model to sling, and the
// rotation index the caller should persist for the *next* call. Persist
// NextIndex after a successful sling; on a skipped / failed dispatch, the
// caller may or may not advance — see the doc comment on ChooseAgent.
type PickResult struct {
	Agent     string // empty when ErrNoEligibleAgent is returned
	NextIndex int    // wrap-relative index; pass back as PickIndex next time
}

// ChooseAgent picks the next model to dispatch given live counts and the
// current rotation index. The algorithm:
//
//  1. Build the eligible set: every cap agent with counts < cap AND healthy.
//  2. Walk the rotation starting from pickIndex, return the first eligible
//     agent.
//  3. If none of the rotation entries are eligible, return ErrNoEligibleAgent
//     (all agents at cap or unhealthy).
//
// This reconciles against live lanes — including legacy lanes on other rigs
// (gastown-1l8 acceptance: "already-running legacy lanes count toward mix").
//
// ChooseAgent is pure: it does not read or write any state. Callers that
// load or persist the rotation index own that I/O.
func ChooseAgent(caps MixCaps, counts LiveCounts, healthy HealthyAgents, pickIndex int) (PickResult, error) {
	if len(caps) == 0 {
		return PickResult{}, ErrNoEligibleAgent
	}
	eligible := make(map[string]bool, len(caps))
	for agent, cap := range caps {
		if cap <= 0 {
			continue
		}
		if counts[agent] >= cap {
			continue
		}
		if !healthy[agent] {
			continue // default to "healthy" when unset
		}
		eligible[agent] = true
	}
	if len(eligible) == 0 {
		return PickResult{}, ErrNoEligibleAgent
	}

	rot := NewRotationFromCaps(caps)
	if len(rot) == 0 {
		// Shouldn't happen — caps were non-empty — but fall back to the
		// alphabetically smallest eligible agent deterministically.
		names := make([]string, 0, len(eligible))
		for name := range eligible {
			names = append(names, name)
		}
		sort.Strings(names)
		return PickResult{Agent: names[0], NextIndex: 0}, nil
	}

	if pickIndex < 0 {
		pickIndex = 0
	}
	for offset := 0; offset < len(rot); offset++ {
		idx := (pickIndex + offset) % len(rot)
		agent := rot[idx]
		if eligible[agent] {
			return PickResult{Agent: agent, NextIndex: (idx + 1) % len(rot)}, nil
		}
	}
	return PickResult{}, ErrNoEligibleAgent
}

// LaneCounts is a per-model occupied-lane count snapshot. It exists so
// callers can normalize counts from a town-wide session enumeration.
type LaneCounts struct {
	Counts LiveCounts
	Total  int // total occupied lanes across all models
}