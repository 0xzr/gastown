package witness

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// fakeBeadSource is an in-memory PolecatBeadSource for FleetState tests.
// It mirrors the minimal surface DetectFleetState reads so we can exercise
// the gastown-72v invariants (ephemeral MR detection, hooked-bead based
// active-implementation classification, recovery-held classification)
// without spinning up Dolt.
type fakeBeadSource struct {
	// hooked returns the issues returned by bd.List(opts) with Status=hooked,
	// filtered by Assignee when set. Other List() filters are ignored: the
	// FleetState code only ever asks for hooked beads in production.
	hooked map[string][]*beads.Issue
	// byAssignee returns the issues returned by bd.ListByAssignee(assignee).
	byAssignee map[string][]*beads.Issue
	// mrs is the slice returned by bd.ListMergeRequests, regardless of opts.
	// It can include ephemeral wisps to exercise the gastown-72v wisp gate.
	mrs []*beads.Issue
	// issuesByID satisfies bd.Show: key is issue ID, value is the issue.
	// The FleetState fallback uses Show(hookBead) only.
	issuesByID map[string]*beads.Issue
	// showErr forces Show to error (used to test error path).
	showErr error
	// listErr forces bd.List to error.
	listErr error
	// listMRsErr forces bd.ListMergeRequests to error.
	listMRsErr error
}

func (f *fakeBeadSource) List(opts beads.ListOptions) ([]*beads.Issue, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if opts.Status != beads.StatusHooked {
		return nil, nil
	}
	if f.hooked == nil {
		return nil, nil
	}
	if opts.Assignee == "" {
		// No assignee filter requested — flatten everything we have.
		var out []*beads.Issue
		for _, issues := range f.hooked {
			out = append(out, issues...)
		}
		return out, nil
	}
	return f.hooked[opts.Assignee], nil
}

func (f *fakeBeadSource) ListByAssignee(assignee string) ([]*beads.Issue, error) {
	if f.byAssignee == nil {
		return nil, nil
	}
	return f.byAssignee[assignee], nil
}

func (f *fakeBeadSource) ListMergeRequests(opts beads.ListOptions) ([]*beads.Issue, error) {
	if f.listMRsErr != nil {
		return nil, f.listMRsErr
	}
	return f.mrs, nil
}

func (f *fakeBeadSource) Show(id string) (*beads.Issue, error) {
	if f.showErr != nil {
		return nil, f.showErr
	}
	if issue, ok := f.issuesByID[id]; ok {
		return issue, nil
	}
	return nil, errors.New("not found")
}

// makeHookedIssue builds a hooked work bead with the given ID and assignee.
func makeHookedIssue(id, assignee string) *beads.Issue {
	return &beads.Issue{
		ID:       id,
		Status:   beads.StatusHooked,
		Assignee: assignee,
	}
}

// makeMRIssue builds a merge-request-shaped issue; set ephemeral=true for wisps.
func makeMRIssue(id, assignee string, ephemeral bool) *beads.Issue {
	issue := &beads.Issue{
		ID:       id,
		Status:   "open",
		Assignee: assignee,
		Labels:   []string{"gt:merge-request"},
	}
	if ephemeral {
		issue.Ephemeral = true
	}
	return issue
}

// TestLoadMRGateMap_DetectsWisps is the canonical gastown-72v regression test:
// the MR gate map MUST see MR beads regardless of whether they live in the
// issues table (durable) or the wisps table (ephemeral). Prior versions used
// bd.List and missed ephemeral wisps created by gt mq submit; this test pins
// the wisp-aware contract via the ListMergeRequests seam.
func TestLoadMRGateMap_DetectsWisps(t *testing.T) {
	durable := makeMRIssue("gastown-wisp-biw", "gastown/polecats/quartz", false)
	wisp := makeMRIssue("gastown-wisp-e3a", "gastown/polecats/p3w", true)
	src := &fakeBeadSource{mrs: []*beads.Issue{durable, wisp}}

	got, err := loadMRGateMap(src)
	if err != nil {
		t.Fatalf("loadMRGateMap: %v", err)
	}

	if got["gastown/polecats/quartz"] != 1 {
		t.Errorf("gates[quartz] = %d, want 1", got["gastown/polecats/quartz"])
	}
	if got["gastown/polecats/p3w"] != 1 {
		t.Errorf("gates[p3w] = %d, want 1 (ephemeral wisp MR MUST be visible)", got["gastown/polecats/p3w"])
	}
}

// TestLoadMRGateMap_NilSource guards the nil-safety precondition so a misuse
// from production wiring does not panic.
func TestLoadMRGateMap_NilSource(t *testing.T) {
	if _, err := loadMRGateMap(nil); err == nil {
		t.Fatal("loadMRGateMap(nil) returned nil error; want non-nil")
	}
}

// TestLoadMRGateMap_PropagatesStoreError ensures the bead-store error path
// is not silently swallowed: callers depend on err to decide whether to
// classify based on session+work alone.
func TestLoadMRGateMap_PropagatesStoreError(t *testing.T) {
	want := errors.New("dolt down")
	src := &fakeBeadSource{listMRsErr: want}
	_, err := loadMRGateMap(src)
	if err == nil || !strings.Contains(err.Error(), "dolt down") {
		t.Fatalf("loadMRGateMap: got %v, want wraps %v", err, want)
	}
}

// TestCollectGatedPolecats_StableOutput verifies deterministic, sorted output
// from the assignee->name mapping; post-submit gate reporting must be stable
// for witness/mayor dashboards.
func TestCollectGatedPolecats_StableOutput(t *testing.T) {
	gates := map[string]int{
		"gastown/polecats/quartz": 2,
		"gastown/polecats/alpha":  1,
		"gastown/polecats/p3w":    1,
		"random/other/route":      3, // non-conforming assignee → ignored
	}
	got := collectGatedPolecats(gates)
	want := []string{"alpha", "p3w", "quartz"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collectGatedPolecats = %v, want %v", got, want)
	}
}

// TestCollectGatedPolecats_EmptyInputs covers the nil-input and no-gates paths.
func TestCollectGatedPolecats_EmptyInputs(t *testing.T) {
	if got := collectGatedPolecats(nil); got != nil {
		t.Errorf("collectGatedPolecats(nil) = %v, want nil", got)
	}
	if got := collectGatedPolecats(map[string]int{}); got != nil {
		t.Errorf("collectGatedPolecats(empty) = %v, want nil", got)
	}
}

// TestIsActiveImplementation_HookedBeadAuthoritative is the gastown-72v
// hook_bead regression: when the work bead itself is hooked and assigned to
// the polecat, we treat the polecat as active regardless of the legacy
// AgentFields.HookBead value (which can be empty for freshly minted agents).
func TestIsActiveImplementation_HookedBeadAuthoritative(t *testing.T) {
	assignee := "gastown/polecats/quartz"
	src := &fakeBeadSource{
		hooked: map[string][]*beads.Issue{
			assignee: {makeHookedIssue("gastown-72v", assignee)},
		},
	}
	if !isActiveImplementation(src, assignee, "", false, "") {
		t.Fatal("isActiveImplementation = false; want true (hooked work bead assigned to polecat)")
	}
}

// TestIsActiveImplementation_LegacyHookBeadFallback verifies the legacy
// HookBead path is still honored when it resolves to a currently hooked
// bead for the same assignee.
func TestIsActiveImplementation_LegacyHookBeadFallback(t *testing.T) {
	assignee := "gastown/polecasts/quartz"
	bead := makeHookedIssue("gastown-zzz", assignee)
	src := &fakeBeadSource{issuesByID: map[string]*beads.Issue{bead.ID: bead}}
	if !isActiveImplementation(src, assignee, bead.ID, false, "") {
		t.Fatal("isActiveImplementation = false; want true (legacy HookBead resolves to hooked bead)")
	}
}

// TestIsActiveImplementation_AssignedWorkInFlight covers the empty-HookBead
// case the bead description flags: live session with non-empty assigned work
// in any active status must be reported as active implementation.
func TestIsActiveImplementation_AssignedWorkInFlight(t *testing.T) {
	assignee := "gastown/polecats/p3w"
	bead := &beads.Issue{
		ID:       "gastown-xyz",
		Status:   "in_progress",
		Assignee: assignee,
	}
	src := &fakeBeadSource{
		byAssignee: map[string][]*beads.Issue{assignee: {bead}},
	}
	if !isActiveImplementation(src, assignee, "", true, "working") {
		t.Fatal("isActiveImplementation = false; want true (live session with assigned in_progress work)")
	}
}

// TestIsActiveImplementation_NoActiveSignal covers the negative path: no
// hooked bead, no legacy HookBead, no live session → not active.
func TestIsActiveImplementation_NoActiveSignal(t *testing.T) {
	src := &fakeBeadSource{}
	if isActiveImplementation(src, "gastown/polecats/zeta", "", false, "") {
		t.Fatal("isActiveImplementation = true; want false (no active signal)")
	}
}

// TestIsRecoveryHeld covers the stuck/escalated/awaiting-gate/paused bucket.
func TestIsRecoveryHeld(t *testing.T) {
	cases := []struct {
		state string
		want  bool
	}{
		{"stuck", true},
		{"escalated", true},
		{"awaiting-gate", true},
		{"paused", true},
		{"working", false},
		{"idle", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isRecoveryHeld(tc.state, false); got != tc.want {
			t.Errorf("isRecoveryHeld(%q) = %v, want %v", tc.state, got, tc.want)
		}
	}
}

// TestSortStrings covers the local sort helper used by collectGatedPolecats.
func TestSortStrings(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"three-element scramble", []string{"b", "a", "c"}, []string{"a", "b", "c"}},
		{"alphabetical", []string{"quartz", "alpha", "p3w"}, []string{"alpha", "p3w", "quartz"}},
		{"nil-input", nil, nil},
		{"single", []string{"only"}, []string{"only"}},
	}
	for _, tc := range cases {
		got := append([]string(nil), tc.in...)
		sortStrings(got)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: sortStrings(%v) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}

// setupPolecatsDir creates a townRoot with a "<rig>/polecats/<name>" structure
// for every polecat in `names`. Always ensures the "<rig>/polecats" directory
// itself exists so DetectFleetState does not error out on a missing polecats
// dir even when `names` is empty.
//
// Returns the townRoot for use with DetectFleetState. The fixture uses
// empty subdirs — DetectFleetState walks them but its per-polecat agent-bead
// lookup shells out (returns nil on error), and tmux.HasSession returns false
// when no tmux server is running, which is the expected CI/test posture.
func setupPolecatsDir(t *testing.T, rig string, names []string) string {
	t.Helper()
	townRoot := t.TempDir()
	polecatsDir := filepath.Join(townRoot, rig, "polecats")
	if err := os.MkdirAll(polecatsDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", polecatsDir, err)
	}
	for _, name := range names {
		dir := filepath.Join(polecatsDir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return townRoot
}

// TestDetectFleetState_LiveMRGatePreventsEmpty_Deduped covers the dedup path
// in addition to the empty-fleet invariant: when the per-polecat loop and
// the seed-by-MR-gate path both discover the same polecat, PostSubmitGate
// must contain it exactly once (gastown-72v invariant).
func TestDetectFleetState_LiveMRGatePreventsEmpty_Deduped(t *testing.T) {
	townRoot := setupPolecatsDir(t, "gastown", []string{"quartz", "zeta"})
	src := &fakeBeadSource{
		mrs: []*beads.Issue{
			makeMRIssue("gastown-wisp-e3a", "gastown/polecats/quartz", true),
		},
	}

	got, err := DetectFleetState(src, townRoot, "gastown")
	if err != nil {
		t.Fatalf("DetectFleetState: %v", err)
	}
	if got.IsEmpty {
		t.Fatal("FleetState.IsEmpty = true; want false (open MR gate is non-empty)")
	}
	// quartz is in both the gate map and the polecats dir: must appear once.
	count := 0
	for _, name := range got.PostSubmitGate {
		if name == "quartz" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("PostSubmitGate contains %d copies of quartz; want 1. got=%v", count, got.PostSubmitGate)
	}
	// zeta has no gate, no work → must end up in Idle, not PostSubmitGate.
	for _, name := range got.PostSubmitGate {
		if name == "zeta" {
			t.Errorf("zeta in PostSubmitGate with no gate; got=%v", got.PostSubmitGate)
		}
	}
	if len(got.Idle) != 1 || got.Idle[0] != "zeta" {
		t.Errorf("Idle = %v, want [zeta]", got.Idle)
	}
}

// TestDedupSorted covers the dedup helper invoked at the end of DetectFleetState.
func TestDedupSorted(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"single", []string{"a"}, []string{"a"}},
		{"dedup preserves order via sort", []string{"b", "a", "b", "c", "a"}, []string{"a", "b", "c"}},
		{"already-unique", []string{"zeta", "alpha"}, []string{"alpha", "zeta"}},
	}
	for _, tc := range cases {
		got := dedupSorted(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: dedupSorted(%v) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}

// invariant test: a lane with no live session but an open MR gate (typical
// post-submit state) MUST NOT be classified as fleet-empty. This prevents
// witness/mayor summaries from claiming "no active polecats" while the
// refinery is actively draining that lane's MR.
func TestDetectFleetState_LiveMRGatePreventsEmpty(t *testing.T) {
	townRoot := setupPolecatsDir(t, "gastown", []string{"quartz"})
	src := &fakeBeadSource{
		mrs: []*beads.Issue{
			makeMRIssue("gastown-wisp-e3a", "gastown/polecats/quartz", true),
		},
	}

	got, err := DetectFleetState(src, townRoot, "gastown")
	if err != nil {
		t.Fatalf("DetectFleetState: %v", err)
	}
	if got.IsEmpty {
		t.Fatal("FleetState.IsEmpty = true; want false (open MR gate is non-empty)")
	}
	if !reflect.DeepEqual(got.PostSubmitGate, []string{"quartz"}) {
		t.Errorf("PostSubmitGate = %v, want [quartz]", got.PostSubmitGate)
	}
	if got.HasActiveWork() != true {
		t.Error("HasActiveWork = false; want true")
	}
}

// TestDetectFleetState_ActiveImplementationHasHookedBead proves the
// hook_bead-empty-but-assigned-work case is classified as ActiveImplementation
// (not PostSubmitGate), so witness/mayor know the lane is in-flight, not
// gated.
func TestDetectFleetState_ActiveImplementationHasHookedBead(t *testing.T) {
	townRoot := setupPolecatsDir(t, "gastown", []string{"p3w"})
	assignee := "gastown/polecats/p3w"
	src := &fakeBeadSource{
		hooked: map[string][]*beads.Issue{
			assignee: {makeHookedIssue("gastown-72v", assignee)},
		},
	}

	got, err := DetectFleetState(src, townRoot, "gastown")
	if err != nil {
		t.Fatalf("DetectFleetState: %v", err)
	}
	if got.IsEmpty {
		t.Fatal("IsEmpty = true; want false (hooked work on p3w)")
	}
	if !reflect.DeepEqual(got.ActiveImplementation, []string{"p3w"}) {
		t.Errorf("ActiveImplementation = %v, want [p3w]", got.ActiveImplementation)
	}
}

// TestDetectFleetState_TrulyEmptyReturnsEmpty covers the negative case: a
// polecats dir with no entries AND no MR gates is the only way to get
// IsEmpty=true in this fixture. This makes the regression mode ("empty
// while e3a is draining") impossible.
func TestDetectFleetState_TrulyEmptyReturnsEmpty(t *testing.T) {
	townRoot := setupPolecatsDir(t, "gastown", nil)
	src := &fakeBeadSource{}

	got, err := DetectFleetState(src, townRoot, "gastown")
	if err != nil {
		t.Fatalf("DetectFleetState: %v", err)
	}
	if !got.IsEmpty {
		t.Errorf("IsEmpty = false; want true (no polecats, no MR gates)")
	}
	if got.HasActiveWork() {
		t.Error("HasActiveWork = true; want false")
	}
}

// TestDetectFleetState_MissingPolecatsDirSurfacesError checks the failure mode
// where the rig dir exists but has no polecats subdir: callers must not get
// a silent "fleet is empty" answer for a misconfigured rig.
func TestDetectFleetState_MissingPolecatsDirSurfacesError(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0o755); err != nil {
		t.Fatalf("mkdir rig dir: %v", err)
	}
	src := &fakeBeadSource{}
	if _, err := DetectFleetState(src, townRoot, "gastown"); err == nil {
		t.Fatal("DetectFleetState with missing polecats dir returned nil error; want non-nil")
	}
}

// TestFleetState_HasActiveWorkNilSafe covers the nil-receiver guard used by
// callers that may not have a populated FleetState yet.
func TestFleetState_HasActiveWorkNilSafe(t *testing.T) {
	var f *FleetState
	if f.HasActiveWork() {
		t.Error("(*FleetState)(nil).HasActiveWork = true; want false")
	}
}

// TestFleetState_JSONShape verifies the JSON contract is stable for downstream
// tooling (witness dashboards, mayor summaries). We don't want a future
// refactor to silently rename a field and break observability.
func TestFleetState_JSONShape(t *testing.T) {
	f := &FleetState{
		Rig:                  "gastown",
		ActiveImplementation: []string{"alpha"},
		PostSubmitGate:       []string{"beta"},
		RecoveryHeld:         []string{"gamma"},
		Idle:                 []string{"delta"},
		IsEmpty:              false,
	}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		`"Rig":"gastown"`,
		`"ActiveImplementation":["alpha"]`,
		`"PostSubmitGate":["beta"]`,
		`"RecoveryHeld":["gamma"]`,
		`"Idle":["delta"]`,
		`"IsEmpty":false`,
	} {
		if !strings.Contains(string(data), key) {
			t.Errorf("FleetState JSON missing %s; got %s", key, data)
		}
	}
}

// TestDetectFleetStateFromCwd_FallsBackToWorkDir checks the convenience
// wrapper handles the empty-town-root case by using the work directory as
// the town root.
func TestDetectFleetStateFromCwd_FallsBackToWorkDir(t *testing.T) {
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, "gastown", "polecats"), 0o755); err != nil {
		t.Fatalf("mkdir polecats: %v", err)
	}
	// workspace.Find will return empty/error — verify the fallback kicks in.
	src := &fakeBeadSource{}
	got, err := DetectFleetStateFromCwd(src, workDir, "gastown")
	if err != nil {
		t.Fatalf("DetectFleetStateFromCwd: %v", err)
	}
	if !got.IsEmpty {
		t.Errorf("IsEmpty = false; want true (no polecats, no MR gates)")
	}
}
