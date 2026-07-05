package deacon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/dog"
	"github.com/steveyegge/gastown/internal/util"
)

// Default parameters for feed-stranded rate limiting.
// Configurable via operational.deacon.max_feeds_per_cycle and
// operational.deacon.feed_cooldown in settings/config.json.
const (
	// DefaultMaxFeedsPerCycle is the maximum number of convoys to feed in one invocation.
	// Prevents spawning too many dogs at once.
	DefaultMaxFeedsPerCycle = 3

	// DefaultFeedCooldown is the minimum time between feeding the same convoy.
	// Prevents re-dispatching a dog before the previous one finishes.
	DefaultFeedCooldown = 10 * time.Minute

	// DefaultFeedFailureBackoff is the minimum time between retrying a convoy
	// whose last dispatch FAILED. Without this, a convoy that keeps failing
	// (e.g. mol-convoy-feed wisp-creation error) is retried every patrol cycle
	// indefinitely. See hq-ozt3s.
	DefaultFeedFailureBackoff = 30 * time.Minute

	// DefaultMaxFeedFailures caps the number of consecutive dispatch failures
	// for a single convoy before it is parked for agent review instead of
	// retried. Prevents unbounded retry loops on persistent failures.
	DefaultMaxFeedFailures = 3
)

// FeedStrandedState tracks feeding attempts per convoy.
// Persisted to deacon/feed-stranded-state.json.
type FeedStrandedState struct {
	// Convoys maps convoy ID to their feed tracking state.
	Convoys map[string]*ConvoyFeedState `json:"convoys"`

	// LastUpdated is when this state was last written.
	LastUpdated time.Time `json:"last_updated"`
}

// ConvoyFeedState tracks the feed history for a single convoy.
type ConvoyFeedState struct {
	// ConvoyID is the convoy identifier.
	ConvoyID string `json:"convoy_id"`

	// FeedCount is total number of feed dispatches for this convoy.
	FeedCount int `json:"feed_count"`

	// LastFeedTime is when the last feed was dispatched.
	LastFeedTime time.Time `json:"last_feed_time,omitempty"`

	// FailureCount is the number of consecutive failed dispatches for this
	// convoy. Reset to 0 by a successful feed (RecordFeed).
	FailureCount int `json:"failure_count"`

	// LastFailureTime is when the last dispatch failed.
	LastFailureTime time.Time `json:"last_failure_time,omitempty"`

	// LastFailureMsg is the error from the last failed dispatch, surfaced to
	// the deacon agent / Mayor so the root cause is visible when a convoy is
	// parked after DefaultMaxFeedFailures.
	LastFailureMsg string `json:"last_failure_msg,omitempty"`
}

// StrandedConvoy holds info about a stranded convoy from `gt convoy stranded --json`.
type StrandedConvoy struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	TrackedCount int      `json:"tracked_count"`
	ReadyCount   int      `json:"ready_count"`
	ReadyIssues  []string `json:"ready_issues"`
}

// FeedResult describes the outcome of a feed-stranded invocation.
type FeedResult struct {
	// Fed is the number of convoys dispatched to dogs for feeding.
	Fed int `json:"fed"`

	// Closed is the number of empty convoys auto-closed.
	Closed int `json:"closed"`

	// Skipped is the number of convoys skipped (cooldown).
	Skipped int `json:"skipped"`

	// NeedsAttention is the number of convoys with tracked issues but no ready
	// issues. These require agent judgment — Go surfaces the raw data but does
	// not classify or act on them.
	NeedsAttention int `json:"needs_attention"`

	// Errors is the number of convoys that failed to process.
	Errors int `json:"errors"`

	// Details has per-convoy results.
	Details []FeedConvoyResult `json:"details"`
}

// FeedConvoyResult describes the outcome for a single convoy.
type FeedConvoyResult struct {
	ConvoyID     string `json:"convoy_id"`
	Action       string `json:"action"` // "fed", "closed", "cooldown", "error", "limit", "needs_attention"
	Message      string `json:"message"`
	TrackedCount int    `json:"tracked_count,omitempty"` // Raw data for agent inspection
	ReadyCount   int    `json:"ready_count,omitempty"`   // Raw data for agent inspection
}

// FeedStrandedStateFile returns the path to the feed-stranded state file.
func FeedStrandedStateFile(townRoot string) string {
	return filepath.Join(townRoot, "deacon", "feed-stranded-state.json")
}

// LoadFeedStrandedState loads the feed-stranded state from disk.
// Returns empty state if file doesn't exist.
func LoadFeedStrandedState(townRoot string) (*FeedStrandedState, error) {
	stateFile := FeedStrandedStateFile(townRoot)

	data, err := os.ReadFile(stateFile) //nolint:gosec // G304: path is constructed from trusted townRoot
	if err != nil {
		if os.IsNotExist(err) {
			return &FeedStrandedState{
				Convoys: make(map[string]*ConvoyFeedState),
			}, nil
		}
		return nil, fmt.Errorf("reading feed-stranded state: %w", err)
	}

	var state FeedStrandedState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing feed-stranded state: %w", err)
	}

	if state.Convoys == nil {
		state.Convoys = make(map[string]*ConvoyFeedState)
	}

	return &state, nil
}

// SaveFeedStrandedState saves the feed-stranded state to disk.
func SaveFeedStrandedState(townRoot string, state *FeedStrandedState) error {
	stateFile := FeedStrandedStateFile(townRoot)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(stateFile), 0755); err != nil {
		return fmt.Errorf("creating deacon directory: %w", err)
	}

	state.LastUpdated = time.Now().UTC()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling feed-stranded state: %w", err)
	}

	return os.WriteFile(stateFile, data, 0600)
}

// GetConvoyState returns the feed state for a convoy, creating if needed.
func (s *FeedStrandedState) GetConvoyState(convoyID string) *ConvoyFeedState {
	if s.Convoys == nil {
		s.Convoys = make(map[string]*ConvoyFeedState)
	}

	state, ok := s.Convoys[convoyID]
	if !ok {
		state = &ConvoyFeedState{ConvoyID: convoyID}
		s.Convoys[convoyID] = state
	}
	return state
}

// IsInCooldown returns true if the convoy was recently fed.
func (s *ConvoyFeedState) IsInCooldown(cooldown time.Duration) bool {
	if s.LastFeedTime.IsZero() {
		return false
	}
	return time.Since(s.LastFeedTime) < cooldown
}

// CooldownRemaining returns how long until cooldown expires.
func (s *ConvoyFeedState) CooldownRemaining(cooldown time.Duration) time.Duration {
	if s.LastFeedTime.IsZero() {
		return 0
	}
	remaining := cooldown - time.Since(s.LastFeedTime)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// RecordFeed records a successful feed dispatch for the convoy and resets the
// consecutive-failure counter.
func (s *ConvoyFeedState) RecordFeed() {
	s.FeedCount++
	s.LastFeedTime = time.Now().UTC()
	s.FailureCount = 0
	s.LastFailureTime = time.Time{}
	s.LastFailureMsg = ""
}

// RecordFailure records a failed dispatch attempt for the convoy. The failure
// counter is what bounds the retry loop: once it reaches maxFailures the convoy
// is parked for agent review instead of retried. The message is preserved so
// the root cause is visible when the convoy is surfaced as needs_attention.
func (s *ConvoyFeedState) RecordFailure(msg string) {
	s.FailureCount++
	s.LastFailureTime = time.Now().UTC()
	s.LastFailureMsg = msg
}

// IsInFailureBackoff returns true if the convoy recently failed a dispatch and
// is still within the failure-backoff window. This prevents a failing convoy
// from being retried every patrol cycle. The backoff only applies between the
// first failure and the maxFailures cap; once parked the convoy is surfaced,
// not retried, so backoff no longer matters.
func (s *ConvoyFeedState) IsInFailureBackoff(backoff time.Duration) bool {
	if s.FailureCount == 0 || s.LastFailureTime.IsZero() {
		return false
	}
	return time.Since(s.LastFailureTime) < backoff
}

// FailureBackoffRemaining returns how long until the failure-backoff window
// expires.
func (s *ConvoyFeedState) FailureBackoffRemaining(backoff time.Duration) time.Duration {
	if s.FailureCount == 0 || s.LastFailureTime.IsZero() {
		return 0
	}
	remaining := backoff - time.Since(s.LastFailureTime)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// FindStrandedConvoys runs `gt convoy stranded --json` and parses the output.
func FindStrandedConvoys(townRoot string) ([]StrandedConvoy, error) {
	cmd := exec.Command("gt", "convoy", "stranded", "--json")
	cmd.Dir = townRoot
	cmd.Env = deaconReadOnlyRoutingEnv(townRoot)
	util.SetDetachedProcessGroup(cmd)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("running gt convoy stranded: %w", err)
	}

	var stranded []StrandedConvoy
	if err := json.Unmarshal(output, &stranded); err != nil {
		return nil, fmt.Errorf("parsing stranded convoys: %w", err)
	}

	return stranded, nil
}

// FeedStranded detects stranded convoys and takes mechanical actions where safe.
// Empty convoys (0 tracked) are auto-closed. Feedable convoys get a dog dispatched.
// Convoys with tracked-but-not-ready issues are surfaced as "needs_attention" with
// raw data (tracked_count, ready_count) for the deacon agent to inspect and decide.
// Rate limits by maxPerCycle and per-convoy cooldown.
//
// failureBackoff and maxFailures bound the retry loop on persistent dispatch
// failures (e.g. mol-convoy-feed wisp-creation errors). A failing convoy waits
// failureBackoff between attempts and is parked for agent review after
// maxFailures consecutive failures instead of retrying indefinitely. See hq-ozt3s.
// Zero values fall back to DefaultFeedFailureBackoff / DefaultMaxFeedFailures.
func FeedStranded(townRoot string, maxPerCycle int, cooldown, failureBackoff time.Duration, maxFailures int) *FeedResult {
	result := &FeedResult{}

	if maxPerCycle <= 0 {
		maxPerCycle = DefaultMaxFeedsPerCycle
	}
	if cooldown <= 0 {
		cooldown = DefaultFeedCooldown
	}
	if failureBackoff <= 0 {
		failureBackoff = DefaultFeedFailureBackoff
	}
	if maxFailures <= 0 {
		maxFailures = DefaultMaxFeedFailures
	}

	// Find stranded convoys
	stranded, err := findStrandedConvoysFn(townRoot)
	if err != nil {
		result.Errors++
		result.Details = append(result.Details, FeedConvoyResult{
			Action:  "error",
			Message: fmt.Sprintf("failed to find stranded convoys: %v", err),
		})
		return result
	}

	if len(stranded) == 0 {
		return result
	}

	// Load state for cooldown tracking
	state, err := LoadFeedStrandedState(townRoot)
	if err != nil {
		result.Errors++
		result.Details = append(result.Details, FeedConvoyResult{
			Action:  "error",
			Message: fmt.Sprintf("failed to load feed state: %v", err),
		})
		return result
	}

	fedCount := 0

	for _, convoy := range stranded {
		// Handle convoys with no ready issues.
		if convoy.ReadyCount == 0 {
			// Convoy has tracked issues but none are ready — surface raw data
			// for the deacon agent to inspect. Go does not classify WHY issues
			// aren't ready (dependency resolution, external block, etc.).
			if convoy.TrackedCount > 0 {
				result.NeedsAttention++
				result.Details = append(result.Details, FeedConvoyResult{
					ConvoyID:     convoy.ID,
					Action:       "needs_attention",
					Message:      fmt.Sprintf("%d tracked issues, 0 ready — requires agent review", convoy.TrackedCount),
					TrackedCount: convoy.TrackedCount,
					ReadyCount:   0,
				})
				continue
			}

			// Truly empty convoy (0 tracked issues) — auto-close
			if err := closeEmptyConvoy(townRoot, convoy.ID); err != nil {
				result.Errors++
				result.Details = append(result.Details, FeedConvoyResult{
					ConvoyID: convoy.ID,
					Action:   "error",
					Message:  fmt.Sprintf("failed to auto-close empty convoy: %v", err),
				})
			} else {
				result.Closed++
				result.Details = append(result.Details, FeedConvoyResult{
					ConvoyID: convoy.ID,
					Action:   "closed",
					Message:  "auto-closed empty convoy (0 tracked issues)",
				})
			}
			continue
		}

		// Rate limit: check per-cycle cap
		if fedCount >= maxPerCycle {
			result.Details = append(result.Details, FeedConvoyResult{
				ConvoyID: convoy.ID,
				Action:   "limit",
				Message:  fmt.Sprintf("skipped: per-cycle limit reached (%d/%d)", fedCount, maxPerCycle),
			})
			continue
		}

		// Rate limit: check per-convoy cooldown (successful feed throttle)
		convoyState := state.GetConvoyState(convoy.ID)
		if convoyState.IsInCooldown(cooldown) {
			remaining := convoyState.CooldownRemaining(cooldown)
			result.Skipped++
			result.Details = append(result.Details, FeedConvoyResult{
				ConvoyID: convoy.ID,
				Action:   "cooldown",
				Message:  fmt.Sprintf("in cooldown (remaining: %s)", remaining.Round(time.Second)),
			})
			continue
		}

		// Bounded failure: once a convoy has hit maxFailures consecutive dispatch
		// failures, park it for agent review instead of retrying. The last
		// failure message is surfaced so the root cause is visible. A successful
		// feed (RecordFeed) resets the counter, so a transient failure burst
		// does not permanently park a convoy.
		if convoyState.FailureCount >= maxFailures {
			result.NeedsAttention++
			result.Details = append(result.Details, FeedConvoyResult{
				ConvoyID: convoy.ID,
				Action:   "needs_attention",
				Message: fmt.Sprintf("parked after %d consecutive feed failures: %s",
					convoyState.FailureCount, convoyState.LastFailureMsg),
				ReadyCount: convoy.ReadyCount,
			})
			continue
		}

		// Bounded failure: rate-limit retries of a failing convoy so it is not
		// redispatched every patrol cycle. The backoff only applies between the
		// first failure and the maxFailures cap.
		if convoyState.IsInFailureBackoff(failureBackoff) {
			remaining := convoyState.FailureBackoffRemaining(failureBackoff)
			result.Skipped++
			result.Details = append(result.Details, FeedConvoyResult{
				ConvoyID: convoy.ID,
				Action:   "cooldown",
				Message: fmt.Sprintf("in failure backoff (%d consecutive failures, remaining: %s)",
					convoyState.FailureCount, remaining.Round(time.Second)),
			})
			continue
		}

		// Dispatch dog to feed the convoy
		if err := dispatchFeedDogFn(townRoot, convoy.ID); err != nil {
			msg := fmt.Sprintf("failed to dispatch feed dog: %v", err)
			convoyState.RecordFailure(msg)
			// Defensive cleanup: the sling subprocess clears its own dog on
			// failure, but an interruption mid-sling (or a future regression)
			// can leave a dog marked working on mol-convoy-feed with no live
			// wisp. Clear any such dog so the lane is not stranded. See hq-ozt3s.
			clearDogsWorkingOnFeedFn(townRoot, convoy.ID)
			result.Errors++
			action := "error"
			if convoyState.FailureCount >= maxFailures {
				// This failure pushed the convoy over the cap — surface it for
				// agent review rather than silently retrying next cycle.
				action = "needs_attention"
				result.NeedsAttention++
				result.Errors--
				msg = fmt.Sprintf("parked after %d consecutive feed failures: %s",
					convoyState.FailureCount, msg)
			}
			result.Details = append(result.Details, FeedConvoyResult{
				ConvoyID: convoy.ID,
				Action:   action,
				Message:  msg,
			})
			continue
		}

		convoyState.RecordFeed()
		fedCount++
		result.Fed++
		result.Details = append(result.Details, FeedConvoyResult{
			ConvoyID: convoy.ID,
			Action:   "fed",
			Message:  fmt.Sprintf("dispatched dog to feed (%d ready issues)", convoy.ReadyCount),
		})
	}

	// Save state
	if err := SaveFeedStrandedState(townRoot, state); err != nil {
		result.Details = append(result.Details, FeedConvoyResult{
			Action:  "error",
			Message: fmt.Sprintf("warning: failed to save feed state: %v", err),
		})
	}

	return result
}

// closeEmptyConvoy runs `gt convoy check <id>` to auto-close an empty convoy.
func closeEmptyConvoy(townRoot, convoyID string) error {
	cmd := exec.Command("gt", "convoy", "check", convoyID)
	cmd.Dir = townRoot
	cmd.Env = deaconMutationRoutingEnv(townRoot)
	util.SetDetachedProcessGroup(cmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// dispatchFeedDogFn is the dispatch function used by FeedStranded. It is a
// package-level variable so tests can stub it without spawning real gt/sling
// subprocesses. Mirrors the cleanupFailedDogFormulaWispFn pattern in
// sling_formula.go.
var dispatchFeedDogFn = dispatchFeedDog

// clearDogsWorkingOnFeedFn clears any dog left working on mol-convoy-feed after
// a failed dispatch. Stubbed in tests so they don't touch real dog state.
var clearDogsWorkingOnFeedFn = clearDogsWorkingOnFeed

// findStrandedConvoysFn is the stranded-convoy finder used by FeedStranded.
// Stubbed in tests so FeedStranded can be exercised without spawning gt.
var findStrandedConvoysFn = FindStrandedConvoys

// feedDogSlingArgs builds the gt-sling argv for dispatching a feed dog. Exposed
// for testing so the title-var workaround (see dispatchFeedDog) can be asserted
// without spawning a subprocess.
func feedDogSlingArgs(convoyID string) []string {
	return []string{
		"sling", constants.MolConvoyFeed, "deacon/dogs",
		"--var", fmt.Sprintf("convoy=%s", convoyID),
		"--var", fmt.Sprintf("title=Convoy feed: %s", convoyID),
	}
}

// dispatchFeedDog dispatches a dog to feed a stranded convoy via gt sling.
//
// A non-empty `title` var is passed because mol-convoy-feed declares a
// `[vars.title]` computed variable (default=""). beads' cookFormulaToSubgraph
// treats any formula with a `title` var as wanting `{{title}}` as the root
// issue title; with an empty default the root title substitutes to "" and
// `bd mol wisp` fails validation with "title is required", failing the sling at
// the "Creating wisp..." step. Passing a non-empty title sidesteps this without
// touching the formula (which would re-break GH#1133) or beads. See hq-ozt3s.
func dispatchFeedDog(townRoot, convoyID string) error {
	cmd := exec.Command("gt", feedDogSlingArgs(convoyID)...)
	cmd.Dir = townRoot
	cmd.Env = deaconMutationRoutingEnv(townRoot)
	util.SetDetachedProcessGroup(cmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// clearDogsWorkingOnFeed clears any dog whose current work assignment is
// mol-convoy-feed. The sling subprocess already clears its own dog on failure
// via cleanupDelayedDogFormulaFailure, but an interruption mid-sling (or a
// future regression) can leave a dog marked working with no live wisp. This is
// a defensive sweep so a failed feed does not strand a dog lane. It only acts
// on dogs whose Work exactly matches the formula name, so dogs doing other work
// are never touched.
func clearDogsWorkingOnFeed(townRoot, convoyID string) {
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		// Best-effort: if rigs config can't be loaded there's nothing safe to do.
		return
	}
	mgr := dog.NewManager(townRoot, rigsConfig)
	dogs, err := mgr.List()
	if err != nil {
		return
	}
	for _, d := range dogs {
		if d == nil || d.State != dog.StateWorking {
			continue
		}
		if d.Work == constants.MolConvoyFeed {
			_ = mgr.ClearWork(d.Name)
		}
	}
}

// PruneFeedStrandedState removes entries for convoys that are no longer open.
// Call periodically to prevent unbounded state growth.
func PruneFeedStrandedState(townRoot string) (int, error) {
	state, err := LoadFeedStrandedState(townRoot)
	if err != nil {
		return 0, err
	}

	pruned := 0
	for convoyID := range state.Convoys {
		status := getConvoyStatus(townRoot, convoyID)
		if status == "closed" || status == "" {
			delete(state.Convoys, convoyID)
			pruned++
		}
	}

	if pruned > 0 {
		if err := SaveFeedStrandedState(townRoot, state); err != nil {
			return pruned, err
		}
	}

	return pruned, nil
}

// getConvoyStatus returns the current status of a convoy bead.
func getConvoyStatus(townRoot, convoyID string) string {
	cmd := beads.Command(townRoot, townBeadsDir(townRoot), beads.ReadOnlyRouting, "show", convoyID, "--json")

	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	var issues []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(output, &issues); err != nil || len(issues) == 0 {
		return ""
	}
	return issues[0].Status
}
