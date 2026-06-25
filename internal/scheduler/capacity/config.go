// Package capacity provides types and pure functions for the capacity-controlled
// dispatch scheduler. The impure orchestration (dispatch loop, enqueue, epic/convoy
// resolution) stays in cmd but uses types and pure functions from this package.
package capacity

import "time"

// SchedulerConfig configures the capacity scheduler for polecat dispatch.
// This is a town-wide setting (not per-rig) because capacity control is host-wide:
// API rate limits, memory, and CPU are shared resources across all rigs.
//
// Behavior is driven entirely by MaxPolecats:
//
//	-1 (default): direct dispatch — gt sling works as before, near-zero overhead
//	 0:           direct dispatch (same as -1)
//	 N > 0:       deferred dispatch — labels/metadata applied, daemon dispatches
type SchedulerConfig struct {
	// MaxPolecats is the max concurrent polecats across ALL rigs.
	// Includes both scheduler-dispatched and directly-slung polecats.
	// nil/absent = default (-1, direct dispatch). 0 = direct dispatch (same as -1).
	// N > 0 = deferred dispatch with capacity control.
	MaxPolecats *int `json:"max_polecats,omitempty"`

	// BatchSize is the number of beads to dispatch per heartbeat tick.
	// Limits spawn rate per 3-minute cycle.
	// nil/absent = default (1). Explicit 0 is rejected by config setter.
	BatchSize *int `json:"batch_size,omitempty"`

	// SpawnDelay is the delay between spawns to prevent Dolt lock contention.
	// Default: "0s".
	SpawnDelay string `json:"spawn_delay,omitempty"`

	// ModelMixCaps is the per-model concurrent-lane cap, expressed as a map
	// from model name to the maximum number of concurrent polecats allowed
	// town-wide on that model. When non-empty, the scheduler treats model-mix
	// caps as active: queued beads without an explicit allowed model
	// assignment on their sling context are refused from native dispatch
	// (gastown-cet.16.2). The map is a fleet.MixCaps in shape but defined here
	// as map[string]int to avoid an import cycle (capacity is a leaf package).
	ModelMixCaps map[string]int `json:"model_mix_caps,omitempty"`

	// RequireExplicitAgent forces the scheduler to refuse dispatching any
	// queued bead whose sling context does not carry an explicit Agent value.
	// This is the model-mix-caps enforcement lever: with caps active, only
	// beads drained through gt sling --agent <model> (or the model-mix
	// maintainer with --agent and --merge=mr) carry a durable model
	// assignment; everything else must be blocked rather than silently
	// defaulting to Claude Code's default Kimi path. (gastown-cet.16.2)
	RequireExplicitAgent bool `json:"require_explicit_agent,omitempty"`
}

// DefaultSchedulerConfig returns a SchedulerConfig with sensible defaults.
// MaxPolecats=-1 means direct dispatch (no scheduler overhead).
func DefaultSchedulerConfig() *SchedulerConfig {
	defaultMax := -1
	defaultBatch := 1
	return &SchedulerConfig{
		MaxPolecats: &defaultMax,
		BatchSize:   &defaultBatch,
		SpawnDelay:  "0s",
	}
}

// GetMaxPolecats returns MaxPolecats or the default (-1, direct dispatch) if unset.
func (c *SchedulerConfig) GetMaxPolecats() int {
	if c == nil || c.MaxPolecats == nil {
		return -1
	}
	return *c.MaxPolecats
}

// GetBatchSize returns BatchSize or the default (1) if unset.
func (c *SchedulerConfig) GetBatchSize() int {
	if c == nil || c.BatchSize == nil {
		return 1
	}
	return *c.BatchSize
}

// GetSpawnDelay returns SpawnDelay as a duration, defaulting to 0s.
func (c *SchedulerConfig) GetSpawnDelay() time.Duration {
	if c == nil || c.SpawnDelay == "" {
		return 0
	}
	return ParseDurationOrDefault(c.SpawnDelay, 0)
}

// IsDeferred returns true when the scheduler is configured for deferred dispatch
// (max_polecats > 0). Returns false for direct dispatch (-1) and disabled (0).
func (c *SchedulerConfig) IsDeferred() bool {
	return c.GetMaxPolecats() > 0
}

// IsModelMixCapped reports whether model-mix caps are configured (i.e., the
// ModelMixCaps map is non-empty). When caps are active, the dispatch path
// must refuse queued beads without an explicit allowed model assignment —
// otherwise Claude Code's default model path can claim the lane and silently
// drift the town away from the configured 2/2/2 (or similar) fleet mix
// (gastown-cet.16.2).
func (c *SchedulerConfig) IsModelMixCapped() bool {
	return c != nil && len(c.ModelMixCaps) > 0
}

// GetModelMixCaps returns the configured ModelMixCaps as a map[string]int,
// or an empty map when caps are not set. Callers should treat the returned
// map as read-only.
func (c *SchedulerConfig) GetModelMixCaps() map[string]int {
	if c == nil {
		return map[string]int{}
	}
	if c.ModelMixCaps == nil {
		return map[string]int{}
	}
	return c.ModelMixCaps
}

// GetRequireExplicitAgent returns true when caps are active and the scheduler
// must refuse to dispatch beads whose sling context does not name an allowed
// model. Returns false when caps are inactive or the flag is unset.
func (c *SchedulerConfig) GetRequireExplicitAgent() bool {
	if c == nil {
		return false
	}
	return c.RequireExplicitAgent
}

// ParseDurationOrDefault parses a Go duration string, returning fallback on error or empty input.
func ParseDurationOrDefault(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}
