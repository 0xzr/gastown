package deacon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFeedStrandedStateFile(t *testing.T) {
	got := FeedStrandedStateFile("/tmp/town")
	want := filepath.Join("/tmp/town", "deacon", "feed-stranded-state.json")
	if got != want {
		t.Errorf("FeedStrandedStateFile = %q, want %q", got, want)
	}
}

func TestLoadFeedStrandedState_FileNotExist(t *testing.T) {
	state, err := LoadFeedStrandedState(t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Convoys == nil {
		t.Fatal("expected initialized Convoys map")
	}
	if len(state.Convoys) != 0 {
		t.Errorf("expected empty Convoys, got %d", len(state.Convoys))
	}
}

func TestSaveThenLoadFeedStrandedState(t *testing.T) {
	tmpDir := t.TempDir()
	// Ensure deacon dir exists
	os.MkdirAll(filepath.Join(tmpDir, "deacon"), 0755)

	state := &FeedStrandedState{
		Convoys: map[string]*ConvoyFeedState{
			"hq-cv-test1": {
				ConvoyID:     "hq-cv-test1",
				FeedCount:    2,
				LastFeedTime: time.Now().UTC().Add(-5 * time.Minute),
			},
		},
	}

	if err := SaveFeedStrandedState(tmpDir, state); err != nil {
		t.Fatalf("save error: %v", err)
	}

	loaded, err := LoadFeedStrandedState(tmpDir)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}

	if len(loaded.Convoys) != 1 {
		t.Fatalf("expected 1 convoy, got %d", len(loaded.Convoys))
	}

	cs := loaded.Convoys["hq-cv-test1"]
	if cs == nil {
		t.Fatal("missing hq-cv-test1")
	}
	if cs.FeedCount != 2 {
		t.Errorf("FeedCount = %d, want 2", cs.FeedCount)
	}
}

func TestConvoyFeedState_IsInCooldown(t *testing.T) {
	tests := []struct {
		name     string
		lastFeed time.Time
		cooldown time.Duration
		want     bool
	}{
		{
			name:     "zero time, not in cooldown",
			lastFeed: time.Time{},
			cooldown: 10 * time.Minute,
			want:     false,
		},
		{
			name:     "recent feed, in cooldown",
			lastFeed: time.Now().Add(-2 * time.Minute),
			cooldown: 10 * time.Minute,
			want:     true,
		},
		{
			name:     "old feed, not in cooldown",
			lastFeed: time.Now().Add(-20 * time.Minute),
			cooldown: 10 * time.Minute,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &ConvoyFeedState{LastFeedTime: tt.lastFeed}
			if got := s.IsInCooldown(tt.cooldown); got != tt.want {
				t.Errorf("IsInCooldown() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConvoyFeedState_CooldownRemaining(t *testing.T) {
	// Zero time = no cooldown
	s := &ConvoyFeedState{}
	if got := s.CooldownRemaining(10 * time.Minute); got != 0 {
		t.Errorf("expected 0 remaining for zero time, got %v", got)
	}

	// Expired cooldown
	s.LastFeedTime = time.Now().Add(-20 * time.Minute)
	if got := s.CooldownRemaining(10 * time.Minute); got != 0 {
		t.Errorf("expected 0 remaining for expired cooldown, got %v", got)
	}

	// Active cooldown
	s.LastFeedTime = time.Now().Add(-2 * time.Minute)
	remaining := s.CooldownRemaining(10 * time.Minute)
	if remaining <= 0 || remaining > 10*time.Minute {
		t.Errorf("expected remaining between 0 and 10m, got %v", remaining)
	}
}

func TestConvoyFeedState_RecordFeed(t *testing.T) {
	s := &ConvoyFeedState{ConvoyID: "hq-cv-test"}

	if s.FeedCount != 0 {
		t.Errorf("initial FeedCount = %d, want 0", s.FeedCount)
	}

	s.RecordFeed()
	if s.FeedCount != 1 {
		t.Errorf("after RecordFeed, FeedCount = %d, want 1", s.FeedCount)
	}
	if s.LastFeedTime.IsZero() {
		t.Error("LastFeedTime should be set after RecordFeed")
	}

	s.RecordFeed()
	if s.FeedCount != 2 {
		t.Errorf("after second RecordFeed, FeedCount = %d, want 2", s.FeedCount)
	}
}

func TestGetConvoyState_CreatesNew(t *testing.T) {
	state := &FeedStrandedState{
		Convoys: make(map[string]*ConvoyFeedState),
	}

	cs := state.GetConvoyState("hq-cv-new")
	if cs == nil {
		t.Fatal("expected non-nil ConvoyFeedState")
	}
	if cs.ConvoyID != "hq-cv-new" {
		t.Errorf("ConvoyID = %q, want %q", cs.ConvoyID, "hq-cv-new")
	}
	if cs.FeedCount != 0 {
		t.Errorf("FeedCount = %d, want 0", cs.FeedCount)
	}
}

func TestGetConvoyState_ReturnsExisting(t *testing.T) {
	state := &FeedStrandedState{
		Convoys: map[string]*ConvoyFeedState{
			"hq-cv-exist": {ConvoyID: "hq-cv-exist", FeedCount: 5},
		},
	}

	cs := state.GetConvoyState("hq-cv-exist")
	if cs.FeedCount != 5 {
		t.Errorf("FeedCount = %d, want 5", cs.FeedCount)
	}
}

func TestGetConvoyState_NilMap(t *testing.T) {
	state := &FeedStrandedState{}

	cs := state.GetConvoyState("hq-cv-test")
	if cs == nil {
		t.Fatal("expected non-nil ConvoyFeedState even with nil map")
	}
	if state.Convoys == nil {
		t.Fatal("Convoys map should be initialized")
	}
}

func TestLoadFeedStrandedState_CorruptedFile(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "deacon")
	os.MkdirAll(stateDir, 0755)

	// Write invalid JSON
	stateFile := filepath.Join(stateDir, "feed-stranded-state.json")
	os.WriteFile(stateFile, []byte("not json"), 0600)

	_, err := LoadFeedStrandedState(tmpDir)
	if err == nil {
		t.Fatal("expected error for corrupted file")
	}
}

func TestSaveFeedStrandedState_CreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	// Don't pre-create deacon dir — SaveFeedStrandedState should create it

	state := &FeedStrandedState{
		Convoys: make(map[string]*ConvoyFeedState),
	}

	if err := SaveFeedStrandedState(tmpDir, state); err != nil {
		t.Fatalf("save error: %v", err)
	}

	// Verify file was created
	stateFile := FeedStrandedStateFile(tmpDir)
	if _, err := os.Stat(stateFile); os.IsNotExist(err) {
		t.Fatal("state file not created")
	}
}

// --- hq-ozt3s: bounded failure + title-var tests ---

func TestFeedDogSlingArgs_IncludesTitleVar(t *testing.T) {
	args := feedDogSlingArgs("hq-cv-abc1")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--var convoy=hq-cv-abc1") {
		t.Errorf("missing convoy var, args=%v", args)
	}
	// The title var is the workaround for the beads cook.go root-title bug:
	// without a non-empty title, `bd mol wisp` fails with "title is required".
	if !strings.Contains(joined, "--var title=Convoy feed: hq-cv-abc1") {
		t.Errorf("missing non-empty title var, args=%v", args)
	}
}

func TestConvoyFeedState_RecordFailure(t *testing.T) {
	s := &ConvoyFeedState{ConvoyID: "hq-cv-test"}
	if s.FailureCount != 0 {
		t.Errorf("initial FailureCount = %d, want 0", s.FailureCount)
	}
	s.RecordFailure("boom")
	if s.FailureCount != 1 {
		t.Errorf("after RecordFailure, FailureCount = %d, want 1", s.FailureCount)
	}
	if s.LastFailureMsg != "boom" {
		t.Errorf("LastFailureMsg = %q, want %q", s.LastFailureMsg, "boom")
	}
	if s.LastFailureTime.IsZero() {
		t.Error("LastFailureTime should be set after RecordFailure")
	}
	s.RecordFailure("boom2")
	if s.FailureCount != 2 {
		t.Errorf("after second RecordFailure, FailureCount = %d, want 2", s.FailureCount)
	}
}

func TestConvoyFeedState_RecordFeedResetsFailures(t *testing.T) {
	s := &ConvoyFeedState{ConvoyID: "hq-cv-test"}
	s.RecordFailure("boom")
	s.RecordFailure("boom2")
	if s.FailureCount != 2 {
		t.Fatalf("precondition FailureCount = %d, want 2", s.FailureCount)
	}
	s.RecordFeed()
	if s.FailureCount != 0 {
		t.Errorf("after RecordFeed, FailureCount = %d, want 0", s.FailureCount)
	}
	if s.LastFailureMsg != "" {
		t.Errorf("after RecordFeed, LastFailureMsg = %q, want empty", s.LastFailureMsg)
	}
	if !s.LastFailureTime.IsZero() {
		t.Error("after RecordFeed, LastFailureTime should be zeroed")
	}
	if s.FeedCount != 1 {
		t.Errorf("after RecordFeed, FeedCount = %d, want 1", s.FeedCount)
	}
}

func TestConvoyFeedState_IsInFailureBackoff(t *testing.T) {
	s := &ConvoyFeedState{ConvoyID: "hq-cv-test"}
	// No failures → not in backoff
	if s.IsInFailureBackoff(10 * time.Minute) {
		t.Error("zero failures should not be in backoff")
	}
	// Recent failure → in backoff
	s.RecordFailure("boom")
	if !s.IsInFailureBackoff(10 * time.Minute) {
		t.Error("recent failure should be in backoff")
	}
	// Old failure → not in backoff
	s.LastFailureTime = time.Now().Add(-20 * time.Minute)
	if s.IsInFailureBackoff(10 * time.Minute) {
		t.Error("old failure should not be in backoff")
	}
}

// stubDispatcher captures dispatch calls and lets the test control success/failure.
type stubDispatcher struct {
	calls            []string
	fail             bool
	failCount        int // number of times to fail before succeeding
	failedAssignment *feedDogAssignment
}

func (s *stubDispatcher) dispatch(townRoot, convoyID string) (*feedDogAssignment, error) {
	s.calls = append(s.calls, convoyID)
	if s.fail || (s.failCount > 0 && len(s.calls) <= s.failCount) {
		return s.failedAssignment, fmt.Errorf("simulated wisp-creation failure")
	}
	return nil, nil
}

// withStubs swaps the package-level func vars for a test and restores them.
func withStubs(dispatch func(townRoot, convoyID string) (*feedDogAssignment, error), clear func(townRoot string, assignment *feedDogAssignment), stranded []StrandedConvoy, fn func()) {
	origDispatch, origClear, origFind := dispatchFeedDogFn, clearDogWorkingOnFeedFn, findStrandedConvoysFn
	defer func() {
		dispatchFeedDogFn = origDispatch
		clearDogWorkingOnFeedFn = origClear
		findStrandedConvoysFn = origFind
	}()
	if dispatch != nil {
		dispatchFeedDogFn = dispatch
	}
	if clear != nil {
		clearDogWorkingOnFeedFn = clear
	}
	if stranded != nil {
		findStrandedConvoysFn = func(string) ([]StrandedConvoy, error) { return stranded, nil }
	}
	fn()
}

func feedableConvoy(id string) []StrandedConvoy {
	return []StrandedConvoy{
		{ID: id, Title: "test convoy", TrackedCount: 1, ReadyCount: 1, ReadyIssues: []string{"gt-xyz"}},
	}
}

// TestFeedStranded_RecordsFailureNotFeed verifies that a dispatch failure
// records a failure (not a feed), increments FailureCount, and does not loop
// within a single invocation. Regression for hq-ozt3s.
func TestFeedStranded_RecordsFailureNotFeed(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "deacon"), 0755)

	disp := &stubDispatcher{
		fail:             true,
		failedAssignment: &feedDogAssignment{name: "feed-dog", workStartedAt: time.Now()},
	}
	clearCalls := 0
	clear := func(string, *feedDogAssignment) { clearCalls++ }

	withStubs(disp.dispatch, clear, feedableConvoy("hq-cv-fail1"), func() {
		result := FeedStranded(tmpDir, 3, time.Minute, 0, 0)
		if result.Fed != 0 {
			t.Errorf("Fed = %d, want 0 on dispatch failure", result.Fed)
		}
		if result.Errors != 1 {
			t.Errorf("Errors = %d, want 1", result.Errors)
		}
		if len(disp.calls) != 1 {
			t.Errorf("dispatch called %d times, want 1 (no within-cycle retry)", len(disp.calls))
		}
		if clearCalls != 1 {
			t.Errorf("defensive dog clear called %d times, want 1", clearCalls)
		}
	})

	// State should reflect the failure, not a feed.
	state, err := LoadFeedStrandedState(tmpDir)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	cs := state.Convoys["hq-cv-fail1"]
	if cs == nil {
		t.Fatal("expected convoy state recorded")
	}
	if cs.FailureCount != 1 {
		t.Errorf("FailureCount = %d, want 1", cs.FailureCount)
	}
	if cs.FeedCount != 0 {
		t.Errorf("FeedCount = %d, want 0 on failure", cs.FeedCount)
	}
	if cs.LastFailureMsg == "" {
		t.Error("LastFailureMsg should be recorded")
	}
}

// TestFeedStranded_FailedDispatchDoesNotClearUnrelatedFeedDog verifies that
// failed feed cleanup only targets the assignment reported by the failed sling.
func TestFeedStranded_FailedDispatchDoesNotClearUnrelatedFeedDog(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "deacon"), 0755)

	owned := &feedDogAssignment{name: "failed-feed-dog", workStartedAt: time.Now()}
	unrelated := &feedDogAssignment{name: "live-feed-dog", workStartedAt: owned.workStartedAt.Add(-time.Minute)}
	workingFeedDogs := map[string]*feedDogAssignment{
		owned.name:     owned,
		unrelated.name: unrelated,
	}

	disp := &stubDispatcher{fail: true, failedAssignment: owned}
	clear := func(_ string, assignment *feedDogAssignment) {
		if assignment == nil {
			t.Fatal("expected failed sling assignment")
		}
		delete(workingFeedDogs, assignment.name)
	}

	withStubs(disp.dispatch, clear, feedableConvoy("hq-cv-fail-owned"), func() {
		result := FeedStranded(tmpDir, 3, time.Minute, 0, 0)
		if result.Errors != 1 {
			t.Errorf("Errors = %d, want 1", result.Errors)
		}
	})

	if _, ok := workingFeedDogs[owned.name]; ok {
		t.Fatalf("owned failed feed dog %q was not cleared", owned.name)
	}
	if _, ok := workingFeedDogs[unrelated.name]; !ok {
		t.Fatalf("unrelated feed dog %q was cleared", unrelated.name)
	}
}

// TestFeedStranded_FailureBackoffSkipsRetry verifies that a convoy with a
// recent failure is skipped (not redispatched) until the backoff window
// expires. Regression for the infinite-loop half of hq-ozt3s.
func TestFeedStranded_FailureBackoffSkipsRetry(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "deacon"), 0755)

	// Pre-seed state: one recent failure.
	state := &FeedStrandedState{Convoys: map[string]*ConvoyFeedState{
		"hq-cv-fail2": {ConvoyID: "hq-cv-fail2", FailureCount: 1, LastFailureTime: time.Now()},
	}}
	if err := SaveFeedStrandedState(tmpDir, state); err != nil {
		t.Fatalf("save: %v", err)
	}

	disp := &stubDispatcher{fail: true}
	withStubs(disp.dispatch, func(string, *feedDogAssignment) {}, feedableConvoy("hq-cv-fail2"), func() {
		result := FeedStranded(tmpDir, 3, time.Minute, time.Hour, 5)
		if result.Fed != 0 || result.Errors != 0 {
			t.Errorf("Fed=%d Errors=%d, want 0/0 (backoff should skip dispatch)", result.Fed, result.Errors)
		}
		if result.Skipped != 1 {
			t.Errorf("Skipped = %d, want 1", result.Skipped)
		}
		if len(disp.calls) != 0 {
			t.Errorf("dispatch called %d times, want 0 during backoff", len(disp.calls))
		}
	})
}

// TestFeedStranded_ParksAfterMaxFailures verifies that a convoy at the
// maxFailures cap is surfaced as needs_attention and NOT redispatched.
func TestFeedStranded_ParksAfterMaxFailures(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "deacon"), 0755)

	state := &FeedStrandedState{Convoys: map[string]*ConvoyFeedState{
		"hq-cv-fail3": {ConvoyID: "hq-cv-fail3", FailureCount: 3, LastFailureTime: time.Now().Add(-time.Hour), LastFailureMsg: "prev boom"},
	}}
	if err := SaveFeedStrandedState(tmpDir, state); err != nil {
		t.Fatalf("save: %v", err)
	}

	disp := &stubDispatcher{}
	clearCalls := 0
	withStubs(disp.dispatch, func(string, *feedDogAssignment) { clearCalls++ }, feedableConvoy("hq-cv-fail3"), func() {
		result := FeedStranded(tmpDir, 3, time.Minute, time.Minute, 3)
		if result.Fed != 0 {
			t.Errorf("Fed = %d, want 0 (parked convoy not dispatched)", result.Fed)
		}
		if result.NeedsAttention != 1 {
			t.Errorf("NeedsAttention = %d, want 1 (parked convoy surfaced)", result.NeedsAttention)
		}
		if len(disp.calls) != 0 {
			t.Errorf("dispatch called %d times, want 0 for parked convoy", len(disp.calls))
		}
		if clearCalls != 0 {
			t.Errorf("defensive clear called %d times, want 0 for parked convoy", clearCalls)
		}
		found := false
		for _, d := range result.Details {
			if d.ConvoyID == "hq-cv-fail3" && d.Action == "needs_attention" && strings.Contains(d.Message, "prev boom") {
				found = true
			}
		}
		if !found {
			t.Errorf("expected needs_attention detail surfacing last failure msg; got %+v", result.Details)
		}
	})
}

// TestFeedStranded_SuccessResetsFailures verifies a successful feed after
// failures resets the failure counter so a transient burst doesn't park the
// convoy permanently.
func TestFeedStranded_SuccessResetsFailures(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "deacon"), 0755)

	state := &FeedStrandedState{Convoys: map[string]*ConvoyFeedState{
		"hq-cv-fail4": {ConvoyID: "hq-cv-fail4", FailureCount: 2, LastFailureTime: time.Now().Add(-time.Hour), LastFailureMsg: "old boom"},
	}}
	if err := SaveFeedStrandedState(tmpDir, state); err != nil {
		t.Fatalf("save: %v", err)
	}

	disp := &stubDispatcher{} // succeeds
	withStubs(disp.dispatch, func(string, *feedDogAssignment) {}, feedableConvoy("hq-cv-fail4"), func() {
		result := FeedStranded(tmpDir, 3, time.Minute, time.Minute, 3)
		if result.Fed != 1 {
			t.Errorf("Fed = %d, want 1", result.Fed)
		}
	})

	loaded, _ := LoadFeedStrandedState(tmpDir)
	cs := loaded.Convoys["hq-cv-fail4"]
	if cs.FailureCount != 0 {
		t.Errorf("FailureCount = %d after success, want 0 (reset)", cs.FailureCount)
	}
	if cs.LastFailureMsg != "" {
		t.Errorf("LastFailureMsg = %q after success, want empty", cs.LastFailureMsg)
	}
}
