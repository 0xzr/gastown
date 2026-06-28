// Package refinery provides the merge queue processing agent.
package refinery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/crew"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/refinery/mrtelemetry"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/util"
)

// shortSHA returns at most 8 characters of a SHA for display.
func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// DefaultStaleClaimTimeout is the default duration after which a claimed MR
// is considered abandoned and eligible for re-claim. This is conservative
// to avoid re-claiming MRs that are legitimately processing long test suites.
// Can be overridden per-rig via MergeQueueConfig.StaleClaimTimeout.
const DefaultStaleClaimTimeout = 30 * time.Minute

// DefaultDurableReviewGateTimeout bounds the external review-gate command when
// config omits an explicit durable_review_gate timeout.
const DefaultDurableReviewGateTimeout = 45 * time.Minute

// isClaimStale checks if a claimed MR should be considered abandoned based on
// its UpdatedAt timestamp and configured timeout. Returns true if the claim
// is stale (eligible for re-claim), false if the claim is recent or the
// timestamp is invalid/missing.
func isClaimStale(updatedAt string, timeout time.Duration) (stale bool, parseErr error) {
	if updatedAt == "" {
		return false, nil // No timestamp - assume claim is valid
	}
	t, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return false, err // Caller should log the parse error
	}
	return time.Since(t) >= timeout, nil
}

// GateConfig defines a single quality gate command.
// GatePhase controls when a gate runs in the merge pipeline.
type GatePhase string

const (
	// GatePhasePreMerge runs the gate before the squash merge (default).
	// The gate validates the source branch on the target baseline.
	GatePhasePreMerge GatePhase = "pre-merge"

	// GatePhasePostSquash runs the gate after the squash merge but before push.
	// The gate validates the actual combined code, catching issues that only
	// manifest in the merged result (broken imports, boot failures, missing
	// templates). On failure, the merge is reset.
	GatePhasePostSquash GatePhase = "post-squash"
)

type GateConfig struct {
	// Cmd is the shell command to execute.
	Cmd string `json:"cmd"`

	// Timeout is the maximum time the gate command may run.
	// Zero means no timeout (inherits context deadline).
	Timeout time.Duration `json:"timeout"`

	// Phase controls when this gate runs: "pre-merge" (default) or "post-squash".
	// Pre-merge gates run before the squash merge on the source branch.
	// Post-squash gates run after the squash merge on the combined result,
	// before pushing. On post-squash failure, the merge is reset.
	Phase GatePhase `json:"phase"`

	// SurfaceScope controls whether failures outside the MR's touched surface
	// can be ignored. Values:
	//   - "disabled" (default): any failure fails the gate.
	//   - "go-packages": Go test/build failures are accepted when every
	//     failing package is outside the set of Go packages touched by the
	//     branch (or stack) being merged.
	// When empty, the scope is inferred from the command: "go test ./..."
	// and "go build ./..." style commands default to "go-packages".
	SurfaceScope string `json:"surface_scope,omitempty"`
}

// DurableReviewGateConfig configures the mandatory multi-model review gate.
// This gate runs after the normal quality gates and verifies that the merge
// candidate has passed the durable refinery review (e.g. refinery-gate.sh)
// and produced a verifiable HMAC attestation. It fails closed: a missing
// reviewer/HMAC attestation blocks the merge.
type DurableReviewGateConfig struct {
	// Required enables fail-closed enforcement. When true, the merge cannot
	// proceed unless the review gate passes and an attestation exists for the
	// merge-candidate tree. Defaults to true.
	Required bool `json:"required,omitempty"`

	// Cmd is the shell command that runs the durable review gate.
	// If empty while Required is true, the merge fails closed.
	Cmd string `json:"cmd,omitempty"`

	// Timeout is the maximum time the review gate command may run.
	// Zero uses DefaultDurableReviewGateTimeout.
	Timeout time.Duration `json:"timeout,omitempty"`

	// AttestDir is the directory where HMAC attestation files are written.
	// Each file is named after the merge-candidate tree hash. If empty, the
	// default is read from GT_GATE_ATTEST_DIR, falling back to
	// /home/ubuntu/.gt-gate-attestations.
	AttestDir string `json:"attest_dir,omitempty"`

	// HMACKeyPath is the server-local key used to verify durable review
	// attestations. If empty, the pinned production key path is used.
	HMACKeyPath string `json:"hmac_key_path,omitempty"`
}

// GateResult holds the outcome of a single gate execution.
type GateResult struct {
	Name    string
	Success bool
	Error   string
	Elapsed time.Duration
	// Output is the combined stdout/stderr of the gate command. It is kept
	// even on failure so scoped gates can inspect the failure surface.
	Output string
}

// gateSurface defines the git range whose changed packages are considered
// "touched" by the MR (or stack of MRs) under test.
type gateSurface struct {
	base string
	head string
}

// SurfaceScope values for GateConfig.SurfaceScope.
const (
	SurfaceScopeDisabled   = "disabled"
	SurfaceScopeGoPackages = "go-packages"
)

// MergeQueueConfig holds configuration for the merge queue processor.
//
// Note: Integration branch gating (polecat/refinery enabled flags) is handled at
// MR creation time via config.MergeQueueConfig and formula injection, not here.
// The Engineer's job is to merge whatever target the MR specifies — it doesn't
// need to know whether integration branches are enabled.
type MergeQueueConfig struct {
	// Enabled controls whether the merge queue is active.
	Enabled bool `json:"enabled"`

	// OnConflict is the strategy for handling conflicts: "assign_back" or "auto_rebase".
	OnConflict string `json:"on_conflict"`

	// RunTests controls whether to run tests before merging.
	RunTests bool `json:"run_tests"`

	// TestCommand is the command to run for testing.
	TestCommand string `json:"test_command"`

	// DeleteMergedBranches controls whether to delete branches after merge.
	DeleteMergedBranches bool `json:"delete_merged_branches"`

	// RetryFlakyTests is the number of times to retry flaky tests.
	RetryFlakyTests int `json:"retry_flaky_tests"`

	// PollInterval is how often to check for new MRs.
	PollInterval time.Duration `json:"poll_interval"`

	// MaxConcurrent is the maximum number of MRs to process concurrently.
	MaxConcurrent int `json:"max_concurrent"`

	// StaleClaimTimeout is how long a claimed MR can go without updates before
	// being considered abandoned and eligible for re-claim. This handles the
	// case where a refinery crashes mid-merge, leaving an MR permanently claimed.
	// Set conservatively to avoid re-claiming MRs with long-running test suites.
	// NOTE: Only one refinery instance runs per rig (enforced by ErrAlreadyRunning
	// in manager.go), so concurrent re-claim is not a concern in practice.
	StaleClaimTimeout time.Duration `json:"stale_claim_timeout"`

	// Gates defines named quality gate commands to run before merging.
	// When non-empty, gates replace the legacy RunTests/TestCommand path.
	// Each gate runs as a shell command with an optional per-gate timeout.
	Gates map[string]*GateConfig `json:"gates"`

	// GatesParallel controls whether gates run concurrently.
	// When true, all gates start simultaneously; any failure = overall failure.
	GatesParallel bool `json:"gates_parallel"`

	// StaleClaimWarningAfter is how long a claimed MR can sit without updates
	// before it triggers a "warning" severity anomaly.
	StaleClaimWarningAfter time.Duration `json:"stale_claim_warning_after"`

	// StaleClaimCriticalAfter is how long a claimed MR can sit without updates
	// before it triggers a "critical" severity anomaly.
	StaleClaimCriticalAfter time.Duration `json:"stale_claim_critical_after"`

	// MaxRetryCount is the maximum number of conflict resolution retries
	// before escalation to Mayor.
	MaxRetryCount int `json:"max_retry_count"`

	// AutoPush controls whether the refinery pushes to origin after merging.
	// When false, the refinery merges locally but does not push — the user
	// or a separate process handles pushing. Useful to avoid triggering
	// CI/CD builds (e.g. Vercel) on every merge.
	AutoPush bool `json:"auto_push"`

	// MergeStrategy controls how the refinery lands work: "direct" (default)
	// does local squash merge + git push; "pr" uses the VCS provider's merge API
	// which respects branch protection/restriction rules.
	MergeStrategy string `json:"merge_strategy,omitempty"`

	// VCSProvider selects the VCS platform for PR operations when
	// MergeStrategy="pr". Valid values: "github" (default), "bitbucket".
	VCSProvider string `json:"vcs_provider,omitempty"`

	// RequireReview controls whether the refinery requires at least one approving
	// review before merging a PR. Only meaningful when MergeStrategy="pr".
	// Nil defaults to false (no review required).
	RequireReview *bool `json:"require_review,omitempty"`

	// DegradedQuorumEnabled allows a merge to proceed when some reviewers are
	// unavailable or produce no verdict, provided enough independent PASS reviews
	// exist. When enabled, missing reviewers are recorded as audit obligations
	// rather than blocking the merge indefinitely. Nil defaults to false.
	DegradedQuorumEnabled *bool `json:"degraded_quorum_enabled,omitempty"`

	// ReviewQuorumMin is the minimum number of independent PASS reviews required
	// to satisfy degraded quorum. Only used when DegradedQuorumEnabled is true.
	// Zero defaults to 1.
	ReviewQuorumMin int `json:"review_quorum_min,omitempty"`

	// Batch holds configuration for the batch-then-bisect merge queue.
	// When nil or MaxBatchSize <= 1, batching is disabled and MRs process sequentially.
	Batch *BatchConfig `json:"batch,omitempty"`

	// DurableReviewGate configures the fail-closed multi-model review gate that
	// must pass before a merge is pushed. This is the source-controlled
	// enforcement counterpart to the live refinery-gate.sh runtime gate.
	DurableReviewGate *DurableReviewGateConfig `json:"durable_review_gate,omitempty"`
}

// DefaultMergeQueueConfig returns sensible defaults for merge queue configuration.
func DefaultMergeQueueConfig() *MergeQueueConfig {
	return &MergeQueueConfig{
		Enabled:                 true,
		OnConflict:              "assign_back",
		RunTests:                true,
		TestCommand:             "",
		DeleteMergedBranches:    true,
		GatesParallel:           true, // gt-8b2i: run gates concurrently (~2x speedup)
		RetryFlakyTests:         1,
		PollInterval:            30 * time.Second,
		MaxConcurrent:           1,
		StaleClaimTimeout:       DefaultStaleClaimTimeout,
		StaleClaimWarningAfter:  2 * time.Hour,
		StaleClaimCriticalAfter: 6 * time.Hour,
		MaxRetryCount:           5,
		AutoPush:                true,
		DurableReviewGate: &DurableReviewGateConfig{
			Required:    true,
			Cmd:         "",
			Timeout:     DefaultDurableReviewGateTimeout,
			AttestDir:   "",
			HMACKeyPath: "",
		},
	}
}

// MRInfo holds merge request information for display and processing.
// This replaces mrqueue.MR after the mrqueue package removal.
type MRInfo struct {
	ID              string     // Bead ID (e.g., "gt-abc123")
	Branch          string     // Source branch (e.g., "polecat/nux")
	Target          string     // Target branch (e.g., "main")
	SourceIssue     string     // The work item being merged
	Worker          string     // Who did the work
	Rig             string     // Which rig
	Title           string     // MR title
	Priority        int        // Priority (lower = higher priority)
	AgentBead       string     // Agent bead ID that created this MR
	RetryCount      int        // Conflict retry count
	ConflictTaskID  string     // Open conflict-resolution task for this MR (if any)
	ConvoyID        string     // Parent convoy ID if part of a convoy
	ConvoyCreatedAt *time.Time // Convoy creation time
	CreatedAt       time.Time  // MR creation time
	BlockedBy       string     // Task ID blocking this MR

	// Pre-verification fields (Phase 3: polecat-owned rebasing)
	// When set, the refinery can skip gates if VerifiedBase matches target HEAD.
	PreVerified     bool      // Polecat ran full gates after rebasing onto target
	PreVerifiedAt   time.Time // When verification completed
	PreVerifiedBase string    // Target branch SHA at verification time

	// Raw data for agent-side queue health analysis (ZFC: agent decides, Go transports)
	UpdatedAt          time.Time // When the MR was last updated
	Assignee           string    // Who claimed this MR (empty = unclaimed)
	BranchExistsLocal  bool      // Whether the MR branch exists locally
	BranchExistsRemote bool      // Whether the MR branch exists in remote tracking refs
}

// MRAnomaly represents an MR queue health problem that can stall processing.
type MRAnomaly struct {
	ID       string        `json:"id"`
	Branch   string        `json:"branch"`
	Type     string        `json:"type"` // stale-claim | orphaned-branch
	Assignee string        `json:"assignee,omitempty"`
	Age      time.Duration `json:"age,omitempty"`
	Detail   string        `json:"detail"`
}

// errMergeSlotTimeout is returned by acquireMainPushSlot when retries are
// exhausted due to slot contention. Infrastructure errors (beads down,
// permission errors) return a different error so callers can distinguish
// transient contention from real failures that need operator attention.
var errMergeSlotTimeout = errors.New("merge slot contention timeout")

// mergeSlotSeq is a package-level counter for unique merge slot holder IDs.
// Using time.Now().UnixNano() alone is insufficient on Windows where timer
// resolution can cause identical timestamps across concurrent goroutines.
var mergeSlotSeq uint64

// Engineer is the merge queue processor that polls for ready merge-requests
// and processes them according to the merge queue design.
type Engineer struct {
	rig                   *rig.Rig
	beads                 *beads.Beads
	git                   *git.Git
	config                *MergeQueueConfig
	prProvider            PRProvider // VCS-specific PR operations (nil when MergeStrategy != "pr")
	workDir               string
	output                io.Writer    // Output destination for user-facing messages
	router                *mail.Router // Mail router for sending protocol messages
	mergeSlotEnsureExists func() (string, error)
	mergeSlotAcquire      func(holder string, addWaiter bool) (*beads.MergeSlotStatus, error)
	mergeSlotRelease      func(holder string) error
	mergeSlotMaxRetries   int           // Max retries for slot acquisition (0 = no retry)
	mergeSlotRetryBackoff time.Duration // Initial backoff between retries

	// surface is the git range whose changed packages define the "touched
	// surface" for scoped gate acceptance. It is set before gates run and
	// cleared afterward.
	surface *gateSurface

	// routeRejectionExec is a test seam for the rework-bounce routing path.
	// When non-nil, routeRejectionToReworkBounce invokes this function
	// instead of shelling out to `gt mq reject`. Production code leaves this
	// nil and uses the real shell call.
	routeRejectionExec func(ctx context.Context, args ...string) error

	// reworkRouterTimeout overrides DefaultReworkRouterTimeout for tests.
	// Zero means use the default.
	reworkRouterTimeout time.Duration

	// reviewerRejectionWorkerNudge and reviewerRejectionMayorNudge are test
	// seams for the worker/mayor nudges in handleReviewerRejection. When
	// non-nil they replace the `gt nudge` exec calls so tests can assert
	// routing behavior without sending real nudges.
	reviewerRejectionWorkerNudge func(target, msg string) error
	reviewerRejectionMayorNudge  func(msg string) error

	// recordReviewerAuditBeadFunc is a test seam for the degraded-quorum audit
	// bead creation in doMergePR. When non-nil it replaces the real
	// recordReviewerAuditBead call so tests can assert that the audit bead is
	// recorded only after a successful merge (never on a failed merge — see
	// gastown-cet.12.6.2). Production code leaves this nil and uses the real
	// beads.Create path.
	recordReviewerAuditBeadFunc func(mr *MRInfo, ev *ReviewEvaluation) (string, error)
}

// DefaultReworkRouterTimeout bounds the external `gt mq reject` call that
// triggers the rework-bounce router. It must be long enough for a Dolt
// commit + router classification, but bounded so a hung gt/router cannot
// stall the refinery rejection path.
const DefaultReworkRouterTimeout = 2 * time.Minute

// reworkRouteClassifications lists the rework-bounce classifications the
// refinery emits from the rejection reason. The dropin router
// (gt-mq-reject-rework-router.py) accepts these as routing outcomes:
//
//   - NEEDS_REWORK_PEER_REVIEW: a peer reviewer (codex/m3/umans-kimi/umans-glm)
//     returned FAIL with concrete blockers. Routine case; router writes a
//     bounded rework packet and invokes gt-scoped-rework-bounce-runner.sh.
//   - REVIEW_UNAVAILABLE_HOLD: a tooling/cap-deferral failure (no-verdict,
//     reviewers-unavailable, cap-deferral, insufficient-quorum). Worker must
//     NOT be told to resubmit until reviewer availability changes.
//   - REWORK_ROUTE_AMBIGUOUS: both peer-review failure markers and cap markers
//     were observed, or neither matched. The router cannot classify safely;
//     escalate to Mayor for human judgment.
const (
	reworkRouteNeedsRework  = "NEEDS_REWORK_PEER_REVIEW"
	reworkRouteReviewerHold = "REVIEW_UNAVAILABLE_HOLD"
	reworkRouteAmbiguous    = "REWORK_ROUTE_AMBIGUOUS"
)

// NewEngineer creates a new Engineer for the given rig.
func NewEngineer(r *rig.Rig) *Engineer {
	cfg := DefaultMergeQueueConfig()

	// Determine the git working directory for refinery operations.
	// Prefer refinery/rig worktree, fall back to mayor/rig (legacy architecture).
	// Using rig.Path directly would find town's .git with rig-named remotes instead of "origin".
	gitDir := filepath.Join(r.Path, "refinery", "rig")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		gitDir = filepath.Join(r.Path, "mayor", "rig")
	}
	beadsClient := beads.New(r.Path)

	return &Engineer{
		rig:     r,
		beads:   beadsClient,
		git:     git.NewGit(gitDir),
		config:  cfg,
		workDir: gitDir,
		output:  os.Stdout,
		router:  mail.NewRouter(r.Path),
		mergeSlotEnsureExists: func() (string, error) {
			return beadsClient.MergeSlotEnsureExists()
		},
		mergeSlotAcquire: func(holder string, addWaiter bool) (*beads.MergeSlotStatus, error) {
			return beadsClient.MergeSlotAcquire(holder, addWaiter)
		},
		mergeSlotRelease: func(holder string) error {
			return beadsClient.MergeSlotRelease(holder)
		},
		mergeSlotMaxRetries:   10,
		mergeSlotRetryBackoff: 500 * time.Millisecond,
	}
}

// SetOutput sets the output writer for user-facing messages.
// This is useful for testing or redirecting output.
func (e *Engineer) SetOutput(w io.Writer) {
	e.output = w
}

// telemetryStore returns the per-rig durable MR-telemetry store. All
// refinery-path telemetry calls (refinery_started, validation, codex
// review, final outcome) flow through this single accessor so the file
// location is consistent with what gt mq submit writes at submission
// time. Telemetry failures are non-fatal — the caller is expected to
// wrap calls in best-effort guards so a broken telemetry store never
// stalls an MR.
func (e *Engineer) telemetryStore() *mrtelemetry.Store {
	if e.rig == nil || e.rig.Path == "" {
		return nil
	}
	return mrtelemetry.NewStore(mrtelemetry.DefaultStorePath(e.rig.Path))
}

// recordTelemetry is a best-effort wrapper for telemetry mutations. It
// logs (but never propagates) errors so a malformed or read-only
// telemetry file cannot stall MR processing.
func (e *Engineer) recordTelemetry(fn func(*mrtelemetry.Store) error) {
	store := e.telemetryStore()
	if store == nil {
		return
	}
	if err := fn(store); err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Note: MR telemetry update: %v\n", err)
	}
}

// recordRefineryStarted stamps the refinery_started_at timestamp on the
// MR's telemetry row (gastown-wjk). Called once at the start of
// ProcessMRInfo so the report's "refinery → verdict" duration is
// accurate even when validation is short-circuited by the pre-verified
// fast path.
func (e *Engineer) recordRefineryStarted(mrID string) {
	e.recordTelemetry(func(s *mrtelemetry.Store) error {
		return s.RecordRefineryStarted(context.Background(), mrID, time.Now().UTC())
	})
}

// recordValidation stamps validation_started_at, validation_finished_at,
// and validation_passed (gastown-wjk).
func (e *Engineer) recordValidation(mrID string, started, finished time.Time, passed bool) {
	e.recordTelemetry(func(s *mrtelemetry.Store) error {
		return s.RecordValidation(context.Background(), mrID, started, finished, passed)
	})
}

// recordCodexReview stamps codex_review timing, the verdict, and any
// per-reviewer results. The mrtelemetry store recomputes
// submit→verdict and refinery→verdict durations automatically.
func (e *Engineer) recordCodexReview(mrID string, started, finished time.Time, verdict string, reviewers []mrtelemetry.ReviewerResult) {
	e.recordTelemetry(func(s *mrtelemetry.Store) error {
		return s.RecordCodexReview(context.Background(), mrID, started, finished, verdict, reviewers)
	})
}

// recordFinalOutcome stamps the terminal decision (merged or rejected),
// failure class, and merge/published commits.
func (e *Engineer) recordFinalOutcome(mrID, finalGateDecision, failureClass string, mergedAt, rejectedAt time.Time, mergeCommit, publishedCommit string) {
	e.recordTelemetry(func(s *mrtelemetry.Store) error {
		return s.RecordFinalOutcome(context.Background(), mrID, finalGateDecision, failureClass, mergedAt, rejectedAt, mergeCommit, publishedCommit)
	})
}

// recordWriterOverwriteIfUnknown upgrades a writer_model="unknown" row
// to the durableReviewWriter value when the refinery can resolve a
// more authoritative attribution. This closes the gap when mq_submit
// couldn't find a model-assignment file at submit time but the rig's
// durable writer is now known. We never overwrite a non-"unknown"
// writer — submit-time attribution is authoritative when present.
func (e *Engineer) recordWriterOverwriteIfUnknown(mrID string) {
	if mrID == "" {
		return
	}
	store := e.telemetryStore()
	if store == nil {
		return
	}
	rec, err := store.GetByMRID(mrID)
	if err != nil || rec == nil {
		return
	}
	if rec.WriterModel != "" && rec.WriterModel != "unknown" {
		return
	}
	if writer := e.durableReviewWriterFromSource(rec.SourceBead); writer != "" {
		e.recordTelemetry(func(s *mrtelemetry.Store) error {
			return s.UpdateByMRID(mrID, func(a *mrtelemetry.MRAttempt) {
				if a.WriterModel == "" || a.WriterModel == "unknown" {
					a.WriterModel = writer
					a.WriterModelSource = "refinery_resolved"
				}
			})
		})
	}
}

// durableReviewWriterFromSource is a thin adapter around the existing
// durableReviewWriter family that accepts just the source-issue string
// (no MRInfo dependency) so it can be called from telemetry paths. It
// only consults the model-assignment file because we don't have an
// MRInfo (and hence no AgentBead) at the point telemetry is upgraded.
func (e *Engineer) durableReviewWriterFromSource(sourceIssue string) string {
	if sourceIssue == "" {
		return ""
	}
	return e.durableReviewWriterFromAssignment(sourceIssue)
}

// LoadConfig loads merge queue configuration from the rig's config.json.
func (e *Engineer) LoadConfig() error {
	configPath := filepath.Join(e.rig.Path, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Use defaults if no config file
			return nil
		}
		return fmt.Errorf("reading config: %w", err)
	}

	// Parse config file to extract merge_queue section
	var rawConfig struct {
		MergeQueue json.RawMessage `json:"merge_queue"`
	}
	if err := json.Unmarshal(data, &rawConfig); err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}

	if rawConfig.MergeQueue == nil {
		// No merge_queue section, use defaults
		return nil
	}

	// Parse merge_queue section into our config struct
	// We need special handling for poll_interval (string -> Duration)
	var mqRaw struct {
		Enabled               *bool                       `json:"enabled"`
		OnConflict            *string                     `json:"on_conflict"`
		RunTests              *bool                       `json:"run_tests"`
		TestCommand           *string                     `json:"test_command"`
		DeleteMergedBranches  *bool                       `json:"delete_merged_branches"`
		RetryFlakyTests       *int                        `json:"retry_flaky_tests"`
		PollInterval          *string                     `json:"poll_interval"`
		MaxConcurrent         *int                        `json:"max_concurrent"`
		StaleClaimTimeout     *string                     `json:"stale_claim_timeout"`
		Gates                 map[string]*gateConfigRaw   `json:"gates"`
		GatesParallel         *bool                       `json:"gates_parallel"`
		AutoPush              *bool                       `json:"auto_push"`
		MergeStrategy         *string                     `json:"merge_strategy"`
		VCSProvider           *string                     `json:"vcs_provider"`
		RequireReview         *bool                       `json:"require_review"`
		DegradedQuorumEnabled *bool                       `json:"degraded_quorum_enabled"`
		ReviewQuorumMin       *int                        `json:"review_quorum_min"`
		DurableReviewGate     *durableReviewGateConfigRaw `json:"durable_review_gate,omitempty"`
	}

	if err := json.Unmarshal(rawConfig.MergeQueue, &mqRaw); err != nil {
		return fmt.Errorf("parsing merge_queue config: %w", err)
	}

	// Apply non-nil values to config (preserving defaults for missing fields)
	if mqRaw.Enabled != nil {
		e.config.Enabled = *mqRaw.Enabled
	}
	if mqRaw.OnConflict != nil {
		e.config.OnConflict = *mqRaw.OnConflict
	}
	if mqRaw.RunTests != nil {
		e.config.RunTests = *mqRaw.RunTests
	}
	if mqRaw.TestCommand != nil {
		e.config.TestCommand = *mqRaw.TestCommand
	}
	if mqRaw.DeleteMergedBranches != nil {
		e.config.DeleteMergedBranches = *mqRaw.DeleteMergedBranches
	}
	if mqRaw.RetryFlakyTests != nil {
		e.config.RetryFlakyTests = *mqRaw.RetryFlakyTests
	}
	if mqRaw.MaxConcurrent != nil {
		e.config.MaxConcurrent = *mqRaw.MaxConcurrent
	}
	if mqRaw.PollInterval != nil {
		dur, err := time.ParseDuration(*mqRaw.PollInterval)
		if err != nil {
			return fmt.Errorf("invalid poll_interval %q: %w", *mqRaw.PollInterval, err)
		}
		e.config.PollInterval = dur
	}
	if mqRaw.StaleClaimTimeout != nil {
		dur, err := time.ParseDuration(*mqRaw.StaleClaimTimeout)
		if err != nil {
			return fmt.Errorf("invalid stale_claim_timeout %q: %w", *mqRaw.StaleClaimTimeout, err)
		}
		if dur <= 0 {
			return fmt.Errorf("stale_claim_timeout must be positive, got %v", dur)
		}
		e.config.StaleClaimTimeout = dur
	}

	// Parse gates configuration
	if mqRaw.Gates != nil {
		e.config.Gates = make(map[string]*GateConfig, len(mqRaw.Gates))
		for name, raw := range mqRaw.Gates {
			gc := &GateConfig{Cmd: raw.Cmd}
			if raw.Timeout != "" {
				dur, err := time.ParseDuration(raw.Timeout)
				if err != nil {
					return fmt.Errorf("invalid timeout for gate %q: %w", name, err)
				}
				if dur <= 0 {
					return fmt.Errorf("gate %q timeout must be positive, got %v", name, dur)
				}
				gc.Timeout = dur
			}
			switch raw.Phase {
			case "", "pre-merge":
				gc.Phase = GatePhasePreMerge
			case "post-squash":
				gc.Phase = GatePhasePostSquash
			default:
				return fmt.Errorf("gate %q has invalid phase %q: must be \"pre-merge\" or \"post-squash\"", name, raw.Phase)
			}
			gc.SurfaceScope = raw.SurfaceScope
			e.config.Gates[name] = gc
		}
	}
	if mqRaw.GatesParallel != nil {
		e.config.GatesParallel = *mqRaw.GatesParallel
	}
	if mqRaw.AutoPush != nil {
		e.config.AutoPush = *mqRaw.AutoPush
	}
	if mqRaw.MergeStrategy != nil {
		e.config.MergeStrategy = *mqRaw.MergeStrategy
	}
	if mqRaw.VCSProvider != nil {
		e.config.VCSProvider = *mqRaw.VCSProvider
	}
	if mqRaw.RequireReview != nil {
		e.config.RequireReview = mqRaw.RequireReview
	}
	if mqRaw.DegradedQuorumEnabled != nil {
		e.config.DegradedQuorumEnabled = mqRaw.DegradedQuorumEnabled
	}
	if mqRaw.ReviewQuorumMin != nil {
		e.config.ReviewQuorumMin = *mqRaw.ReviewQuorumMin
	}
	if mqRaw.DurableReviewGate != nil {
		if e.config.DurableReviewGate == nil {
			e.config.DurableReviewGate = &DurableReviewGateConfig{}
		}
		if mqRaw.DurableReviewGate.Required != nil {
			e.config.DurableReviewGate.Required = *mqRaw.DurableReviewGate.Required
		}
		if mqRaw.DurableReviewGate.Cmd != "" {
			e.config.DurableReviewGate.Cmd = mqRaw.DurableReviewGate.Cmd
		}
		if mqRaw.DurableReviewGate.AttestDir != "" {
			e.config.DurableReviewGate.AttestDir = mqRaw.DurableReviewGate.AttestDir
		}
		if mqRaw.DurableReviewGate.HMACKeyPath != "" {
			e.config.DurableReviewGate.HMACKeyPath = mqRaw.DurableReviewGate.HMACKeyPath
		}
		if mqRaw.DurableReviewGate.Timeout != "" {
			dur, err := time.ParseDuration(mqRaw.DurableReviewGate.Timeout)
			if err != nil {
				return fmt.Errorf("invalid durable_review_gate timeout %q: %w", mqRaw.DurableReviewGate.Timeout, err)
			}
			if dur <= 0 {
				return fmt.Errorf("durable_review_gate timeout must be positive, got %v", dur)
			}
			e.config.DurableReviewGate.Timeout = dur
		}
	}

	// Initialize the PR provider when merge_strategy=pr.
	if e.config.MergeStrategy == "pr" {
		if err := e.initPRProvider(); err != nil {
			return fmt.Errorf("initializing PR provider: %w", err)
		}
	}

	return nil
}

// initPRProvider creates the appropriate PRProvider based on vcs_provider config.
// Defaults to GitHub when vcs_provider is empty or "github".
func (e *Engineer) initPRProvider() error {
	switch e.config.VCSProvider {
	case "", "github":
		e.prProvider = newGitHubPRProvider(e.git)
	case "bitbucket":
		p, err := newBitbucketPRProvider(e.git)
		if err != nil {
			return err
		}
		e.prProvider = p
	default:
		return fmt.Errorf("unknown vcs_provider %q (supported: github, bitbucket)", e.config.VCSProvider)
	}
	return nil
}

// gateConfigRaw is the JSON-friendly representation of a gate config
// with timeout as a string duration.
type gateConfigRaw struct {
	Cmd          string `json:"cmd"`
	Timeout      string `json:"timeout"`
	Phase        string `json:"phase"`
	SurfaceScope string `json:"surface_scope,omitempty"`
}

// durableReviewGateConfigRaw is the JSON-friendly representation of a durable
// review gate config with timeout as a string duration.
type durableReviewGateConfigRaw struct {
	Required    *bool  `json:"required,omitempty"`
	Cmd         string `json:"cmd,omitempty"`
	Timeout     string `json:"timeout,omitempty"`
	AttestDir   string `json:"attest_dir,omitempty"`
	HMACKeyPath string `json:"hmac_key_path,omitempty"`
}

// Config returns the current merge queue configuration.
func (e *Engineer) Config() *MergeQueueConfig {
	return e.config
}

// ProcessResult contains the result of processing a merge request.
type ProcessResult struct {
	Success        bool
	MergeCommit    string
	Error          string
	Conflict       bool
	TestsFailed    bool
	SlotTimeout    bool // Merge slot contention timeout (distinct from build/test failure)
	BranchNotFound bool // Source branch no longer exists (e.g. cleaned up after cherry-pick)
	NoMerge        bool // Source issue has no_merge flag — intentionally blocked, not a failure
	NeedsApproval  bool // PR exists but lacks required approving review (merge_strategy=pr)

	// NeedsRework is true when a reviewer explicitly rejected the change with
	// concrete blockers. The MR is closed as rejected-needs-rework and the
	// polecat is asked to revise and resubmit.
	NeedsRework bool

	// ReviewerRejectionCause is a machine-readable key for the rejection when
	// NeedsRework is true. E.g. "race_condition", "missing_test".
	ReviewerRejectionCause string

	// DegradedQuorum is true when the merge proceeded under the explicit degraded
	// quorum rule: some reviewers were unavailable/no-verdict, but enough
	// independent PASS reviews existed and an audit obligation was recorded.
	DegradedQuorum bool

	// AuditBead is the ID of a follow-up audit task recorded for degraded-quorum
	// or reviewer-unavailability cases.
	AuditBead string

	// ConventionFailed is true when the branch's commit message violates the
	// repo convention (e.g., starts with "WIP:"). It is treated as a queue
	// conflict rather than a build/test failure so the MR is removed from the
	// ready queue without polluting the target branch.
	ConventionFailed bool

	// PublishedCommit is the SHA that has been verified to be reachable from the
	// configured upstream (e.g., origin/main). It is set only when the refinery
	// itself performed the push and verified it; for local-only merges
	// (auto_push=false) or file-remote merges where upstream did not advance,
	// this field remains empty. Consumers (HandleMRInfoSuccess) use it to decide
	// whether the source bead may be closed.
	PublishedCommit string
}

// isWIPCommitMessage reports whether a commit message is a work-in-progress
// checkpoint marker. Such commits must never be squash-merged to the default
// branch; they should live only on feature branches until the agent replaces
// them with a conventional commit.
func isWIPCommitMessage(msg string) bool {
	first := strings.TrimSpace(msg)
	if idx := strings.IndexAny(first, "\r\n"); idx >= 0 {
		first = first[:idx]
	}
	first = strings.TrimSpace(first)
	upper := strings.ToUpper(first)
	return strings.HasPrefix(upper, "WIP:") || strings.HasPrefix(upper, "WIP ")
}

// doMerge performs the actual git merge operation.
func (e *Engineer) doMerge(ctx context.Context, branch, target, sourceIssue string, mr *MRInfo, skipGates ...bool) ProcessResult {
	// GH#2778: Check no_merge flag on source issue before merging. The polecat
	// normally skips MR creation when no_merge is set, but if an MR is created
	// manually (e.g., gh pr create) the refinery would otherwise auto-merge it.
	if sourceIssue != "" {
		if si, err := e.beads.Show(sourceIssue); err == nil && si != nil {
			if af := beads.ParseAttachmentFields(si); af != nil && af.NoMerge {
				_, _ = fmt.Fprintf(e.output, "[Engineer] Source issue %s has no_merge=true — skipping merge\n", sourceIssue)
				return ProcessResult{NoMerge: true, Error: "no_merge flag set on source issue"}
			}
		}
	}

	// Step 1: Verify source branch exists locally (shared .repo.git with polecats)
	_, _ = fmt.Fprintf(e.output, "[Engineer] Checking local branch %s...\n", branch)
	exists, err := e.git.BranchExists(branch)
	if err != nil {
		return ProcessResult{
			Success: false,
			Error:   fmt.Sprintf("failed to check branch %s: %v", branch, err),
		}
	}
	if !exists {
		return ProcessResult{
			Success:        false,
			BranchNotFound: true,
			Error:          fmt.Sprintf("branch %s not found locally", branch),
		}
	}

	// Set the gate surface to the MR's changed range. This lets scoped gates
	// accept failures in packages that the branch does not touch. The surface
	// is restored at the end of doMerge.
	e.surface = &gateSurface{base: target, head: branch}
	defer func() { e.surface = nil }()

	// Step 2: Checkout the target branch
	_, _ = fmt.Fprintf(e.output, "[Engineer] Checking out target branch %s...\n", target)
	if err := e.git.Checkout(target); err != nil {
		return ProcessResult{
			Success: false,
			Error:   fmt.Sprintf("failed to checkout target %s: %v", target, err),
		}
	}

	// Make sure target is up to date with origin
	if err := e.git.Pull("origin", target); err != nil {
		// Pull might fail if nothing to pull, that's ok
		_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: pull from origin/%s: %v (continuing)\n", target, err)
	}

	reviewBase, err := e.git.Rev("HEAD")
	if err != nil {
		return ProcessResult{
			Success: false,
			Error:   fmt.Sprintf("failed to resolve pre-merge target %s: %v", target, err),
		}
	}
	e.surface = &gateSurface{base: reviewBase, head: branch}

	// Step 2.5: Revalidate the pre-verified skip-gates decision against the
	// *actual* refreshed target HEAD. ProcessMRInfo computed the initial
	// skipGates value by comparing mr.PreVerifiedBase to origin/<target> before
	// the pull above, so the remote-tracking ref or target may have advanced
	// between that probe and the real merge base. doMerge has the authoritative
	// refreshed reviewBase and must revalidate before using the fast path.
	// (gastown-6n7: TOCTOU between ProcessMRInfo skipGates and doMerge pull.)
	shouldSkipGates := len(skipGates) > 0 && skipGates[0]
	if shouldSkipGates {
		if mr == nil {
			// Caller explicitly requested the fast path without an MR struct;
			// production callers always provide a real MR. Trust the request and
			// let durableReviewRequiredForMR bind any attestation requirement
			// to skipGates below.
		} else if !mr.PreVerified || mr.PreVerifiedBase == "" {
			_, _ = fmt.Fprintln(e.output, "[Engineer] Pre-verification invalid — running gates normally")
			shouldSkipGates = false
		} else if mr.PreVerifiedBase != reviewBase {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Pre-verification stale — target refreshed from %s to %s, running gates normally\n",
				shortSHA(mr.PreVerifiedBase), shortSHA(reviewBase))
			shouldSkipGates = false
		}
	}

	// Step 3: Check for merge conflicts (using local branch)
	_, _ = fmt.Fprintf(e.output, "[Engineer] Checking for conflicts...\n")
	conflicts, err := e.git.CheckConflicts(branch, target)
	if err != nil {
		return ProcessResult{
			Success:  false,
			Conflict: true,
			Error:    fmt.Sprintf("conflict check failed: %v", err),
		}
	}
	if len(conflicts) > 0 {
		return ProcessResult{
			Success:  false,
			Conflict: true,
			Error:    fmt.Sprintf("merge conflicts in: %v", conflicts),
		}
	}

	// Step 3.5: Push submodule commits if the branch changes submodule pointers.
	// The refinery owns all remote pushes — submodule commits must land before the
	// parent pointer is merged, otherwise main gets dangling submodule references.
	subChanges, err := e.git.SubmoduleChanges(target, branch)
	if err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: could not check submodule changes: %v\n", err)
	}
	if len(subChanges) > 0 {
		// Ensure submodules are initialized in the refinery worktree
		// Use mayor/rig as reference to avoid re-fetching from remote
		mayorRig := filepath.Join(e.rig.Path, "mayor", "rig")
		if initErr := git.InitSubmodules(e.git.WorkDir(), mayorRig); initErr != nil {
			return ProcessResult{
				Success: false,
				Error:   fmt.Sprintf("failed to init submodules in refinery worktree: %v", initErr),
			}
		}
		for _, sc := range subChanges {
			if sc.NewSHA == "" {
				continue // Submodule removed, nothing to push
			}
			_, _ = fmt.Fprintf(e.output, "[Engineer] Pushing submodule %s (commit %s)...\n", sc.Path, shortSHA(sc.NewSHA))
			if pushErr := e.git.PushSubmoduleCommit(sc.Path, sc.NewSHA, "origin"); pushErr != nil {
				return ProcessResult{
					Success: false,
					Error:   fmt.Sprintf("failed to push submodule %s: %v", sc.Path, pushErr),
				}
			}
		}
		_, _ = fmt.Fprintf(e.output, "[Engineer] Pushed %d submodule(s)\n", len(subChanges))
	}

	// Step 4: Run quality gates (or legacy tests) if configured.
	// Phase 3 fast-path: if shouldSkipGates is true (pre-verified MR with a
	// verified, refreshed base), skip deterministic gate execution — the polecat
	// already ran gates after rebasing. The durable multi-model review/HMAC gate
	// is still enforced separately and cannot be bypassed by the fast path.
	validationStarted := time.Now()
	if shouldSkipGates {
		_, _ = fmt.Fprintln(e.output, "[Engineer] Skipping gates (pre-verified by polecat)")
		// gastown-wjk: even when gates are skipped, record validation
		// telemetry so the report knows whether this MR ran gates at all.
		// treat pre-verified as "passed" since the polecat already ran
		// gates on the rebased branch.
		if mr != nil && mr.ID != "" {
			e.recordValidation(mr.ID, validationStarted, time.Now(), true)
		}
	} else if len(e.config.Gates) > 0 {
		// New gates system: run configured quality gates
		gateResult := e.runGates(ctx)
		if mr != nil && mr.ID != "" {
			e.recordValidation(mr.ID, validationStarted, time.Now(), gateResult.Success)
		}
		if !gateResult.Success {
			return gateResult
		}
	} else if e.config.RunTests && e.config.TestCommand != "" {
		// Legacy test command path (backward compatible)
		_, _ = fmt.Fprintf(e.output, "[Engineer] Running tests: %s\n", e.config.TestCommand)
		result := e.runTests(ctx)
		if mr != nil && mr.ID != "" {
			e.recordValidation(mr.ID, validationStarted, time.Now(), result.Success)
		}
		if !result.Success {
			return ProcessResult{
				Success:     false,
				TestsFailed: true,
				Error:       result.Error,
			}
		}
		_, _ = fmt.Fprintln(e.output, "[Engineer] Tests passed")
	}

	// PR merge path: when merge_strategy=pr, use the VCS provider's merge API
	// instead of local squash merge + direct push. This respects branch
	// protection/restriction rules and preserves the PR audit trail.
	// The VCS provider (GitHub, Bitbucket) is selected via vcs_provider config.
	if e.config.MergeStrategy == "pr" {
		return e.doMergePR(ctx, branch, target, mr)
	}

	// Step 5: Perform the actual merge using squash merge
	// Get the original commit message from the polecat branch to preserve the
	// conventional commit format (feat:/fix:) instead of creating redundant merge commits
	originalMsg, err := e.git.GetBranchCommitMessage(branch)
	if err != nil {
		// Fallback to a descriptive message if we can't get the original
		originalMsg = fmt.Sprintf("Squash merge %s into %s", branch, target)
		if sourceIssue != "" {
			originalMsg = fmt.Sprintf("Squash merge %s into %s (%s)", branch, target, sourceIssue)
		}
		_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: could not get original commit message: %v\n", err)
	}

	// Convention check: WIP checkpoint commits are not allowed to land on the
	// default branch. The checkpoint_dog should keep them on feature branches,
	// but if one reaches the merge queue we reject it here rather than polluting
	// the target branch's history.
	if isWIPCommitMessage(originalMsg) {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Rejecting %s: commit message %q starts with WIP — WIP commits may not land on %s\n", branch, strings.TrimSpace(originalMsg), target)
		return ProcessResult{
			Success:          false,
			ConventionFailed: true,
			Error:            fmt.Sprintf("convention check failed: WIP commit message on %s may not be merged to %s", branch, target),
		}
	}

	_, _ = fmt.Fprintf(e.output, "[Engineer] Squash merging with message: %s\n", strings.TrimSpace(originalMsg))
	if err := e.git.MergeSquash(branch, originalMsg); err != nil {
		// ZFC: Use git's porcelain output to detect conflicts instead of parsing stderr.
		// GetConflictingFiles() uses `git diff --diff-filter=U` which is proper.
		conflicts, conflictErr := e.git.GetConflictingFiles()
		if conflictErr == nil && len(conflicts) > 0 {
			_ = e.git.AbortMerge()
			return ProcessResult{
				Success:  false,
				Conflict: true,
				Error:    "merge conflict during actual merge",
			}
		}
		// Non-conflict failure: still need to abort to clean up dirty merge state
		_ = e.git.AbortMerge()
		return ProcessResult{
			Success: false,
			Error:   fmt.Sprintf("merge failed: %v", err),
		}
	}

	// Step 5.5: Run post-squash gates on the merged result.
	// These validate the actual combined code before it goes anywhere.
	// On failure, reset the merge to undo the local squash commit.
	if !shouldSkipGates {
		postResult := e.runGatesForPhase(ctx, GatePhasePostSquash)
		if !postResult.Success {
			if resetErr := e.git.ResetHard("origin/" + target); resetErr != nil {
				_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to reset %s after post-squash gate failure: %v\n", target, resetErr)
			}
			return postResult
		}
	}

	// Step 5.6: Enforce the durable multi-model review gate on the merge
	// candidate before pushing. This gate runs fail-closed: a missing reviewer
	// or HMAC attestation blocks the merge. It is required for the default
	// branch in direct-merge mode; the PR merge path relies on VCS-level
	// review evaluation instead. A pre-verified MR that skips deterministic
	// gates must still pass durable review, so the gate decision includes the
	// actual skipGates outcome. (gastown-6n7)
	durableResult := e.runDurableReviewGate(ctx, branch, target, mr, shouldSkipGates, reviewBase)
	if !durableResult.Success {
		if resetErr := e.git.ResetHard("origin/" + target); resetErr != nil {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to reset %s after durable review gate failure: %v\n", target, resetErr)
		}
		return durableResult
	}

	// Step 6: Get the merge commit SHA
	mergeCommit, err := e.git.Rev("HEAD")
	if err != nil {
		return ProcessResult{
			Success: false,
			Error:   fmt.Sprintf("failed to get merge commit SHA: %v", err),
		}
	}

	// Step 7-8: Push to origin (when auto_push is enabled).
	if e.config.AutoPush {
		// Acquire merge slot before push to serialize writes to the default branch.
		// Only serialize pushes to the rig's default branch (typically main).
		// Integration-branch and feature-branch pushes don't need serialization.
		var pushHolder string
		if target == e.rig.DefaultBranch() {
			var slotErr error
			pushHolder, slotErr = e.acquireMainPushSlot(ctx)
			if slotErr != nil {
				// Reset the checked-out target branch to origin to undo the local squash commit.
				// ResetHard is required because target is the current branch (checked out in Step 2).
				if resetErr := e.git.ResetHard("origin/" + target); resetErr != nil {
					_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to reset %s after slot failure: %v\n", target, resetErr)
				}
				// Only classify as SlotTimeout for actual contention (retries exhausted).
				// Infrastructure errors (beads down, permission errors) should surface
				// through the normal failure/notification path for operator visibility.
				return ProcessResult{
					Success:     false,
					SlotTimeout: errors.Is(slotErr, errMergeSlotTimeout),
					Error:       fmt.Sprintf("failed to acquire merge slot before push: %v", slotErr),
				}
			}
			defer func() {
				// pushHolder is empty when the self-conflict bypass fires — conflict-resolution
				// owns the slot, so we must not release it here.
				if pushHolder != "" {
					if releaseErr := e.mergeSlotRelease(pushHolder); releaseErr != nil {
						_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to release merge slot for push (%s): %v\n", pushHolder, releaseErr)
					}
				}
			}()
		}

		_, _ = fmt.Fprintf(e.output, "[Engineer] Pushing to origin/%s...\n", target)
		if err := e.git.Push("origin", target, false); err != nil {
			// Reset the checked-out target branch to undo the local squash commit.
			// Without this, the next retry could see stale local state from the failed push.
			if resetErr := e.git.ResetHard("origin/" + target); resetErr != nil {
				_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to reset %s after push failure: %v\n", target, resetErr)
			}
			return ProcessResult{
				Success: false,
				Error:   fmt.Sprintf("failed to push to origin: %v", err),
			}
		}
		if err := e.git.VerifyPushedCommit("origin", target, mergeCommit); err != nil {
			if resetErr := e.git.ResetHard("origin/" + target); resetErr != nil {
				_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to reset %s after verified-push failure: %v\n", target, resetErr)
			}
			return ProcessResult{
				Success: false,
				Error:   err.Error(),
			}
		}
	} else {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Auto-push disabled, skipping push to origin/%s\n", target)
	}

	_, _ = fmt.Fprintf(e.output, "[Engineer] Successfully merged: %s\n", shortSHA(mergeCommit))

	result := ProcessResult{
		Success:     true,
		MergeCommit: mergeCommit,
	}
	// If auto_push was enabled and the push was verified, the merge commit is
	// durably published to the configured upstream. Record that so the source
	// bead close path can distinguish published merges from local-only merges.
	if e.config.AutoPush {
		result.PublishedCommit = mergeCommit
	}
	return result
}

// doMergePR handles merging via the VCS provider's PR merge API (merge_strategy=pr).
// This respects branch protection/restriction rules including required reviews.
// The VCS provider (GitHub, Bitbucket) is selected via vcs_provider config.
// Called from doMerge after quality gates have passed.
//
//nolint:unparam // ctx is reserved for future use when git methods accept context
func (e *Engineer) doMergePR(ctx context.Context, branch, target string, mr *MRInfo) ProcessResult {
	_ = ctx
	provider := e.config.VCSProvider
	if provider == "" {
		provider = "github"
	}
	_, _ = fmt.Fprintf(e.output, "[Engineer] Using PR merge strategy (vcs_provider=%s)\n", provider)

	if e.prProvider == nil {
		return ProcessResult{
			Success: false,
			Error:   fmt.Sprintf("no PR provider configured for vcs_provider=%s", provider),
		}
	}

	// Step PR.1: Find the PR for this branch
	prNumber, err := e.prProvider.FindPRNumber(branch)
	if err != nil {
		return ProcessResult{
			Success: false,
			Error:   fmt.Sprintf("failed to find PR for branch %s: %v", branch, err),
		}
	}
	if prNumber == 0 {
		return ProcessResult{
			Success: false,
			Error:   fmt.Sprintf("no open PR found for branch %s — merge_strategy=pr requires a PR", branch),
		}
	}
	_, _ = fmt.Fprintf(e.output, "[Engineer] Found PR #%d for branch %s\n", prNumber, branch)

	// Step PR.2: Evaluate reviewers if require_review is enabled.
	requireReview := e.config.RequireReview != nil && *e.config.RequireReview
	if requireReview {
		ev, err := e.prProvider.GetReviewEvaluation(prNumber)
		if err != nil {
			return ProcessResult{
				Success: false,
				Error:   fmt.Sprintf("failed to evaluate reviewers for PR #%d: %v", prNumber, err),
			}
		}

		rule := e.degradedQuorumRule()
		ev = EvaluateWithRule(ev, rule)

		switch ev.State {
		case ReviewStatePass:
			_, _ = fmt.Fprintf(e.output, "[Engineer] PR #%d has approving review\n", prNumber)
		case ReviewStateFail:
			_, _ = fmt.Fprintf(e.output, "[Engineer] PR #%d rejected by reviewer(s): %s\n", prNumber, ev.Error)
			return ProcessResult{
				Success:                false,
				NeedsRework:            true,
				ReviewerRejectionCause: ev.CauseKey,
				Error:                  fmt.Sprintf("PR #%d reviewer rejection: %s", prNumber, ev.Error),
			}
		case ReviewStateDegradedQuorum:
			_, _ = fmt.Fprintf(e.output, "[Engineer] PR #%d proceeding under degraded quorum: %s\n", prNumber, ev.Error)
			// The audit obligation only exists for a successful merge. Record the
			// audit bead AFTER finishPRMerge succeeds: if the provider merge or
			// push-verification fails, there is no merge to audit, and creating
			// the bead now would orphan it against a failed MR (gastown-cet.12.6.2).
			result := e.finishPRMerge(prNumber, branch, target, true)
			if !result.Success {
				return result
			}
			if mr != nil {
				auditFn := e.recordReviewerAuditBead
				if e.recordReviewerAuditBeadFunc != nil {
					auditFn = e.recordReviewerAuditBeadFunc
				}
				auditBead, auditErr := auditFn(mr, ev)
				if auditErr != nil {
					_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to record reviewer audit bead: %v\n", auditErr)
				} else {
					_, _ = fmt.Fprintf(e.output, "[Engineer] Recorded reviewer audit bead: %s\n", auditBead)
					result.AuditBead = auditBead
				}
			}
			return result
		default:
			// NO_VERDICT or UNAVAILABLE: keep MR in queue for retry rather than failing.
			_, _ = fmt.Fprintf(e.output, "[Engineer] PR #%d awaiting reviewer verdict (%s) — deferring merge\n", prNumber, ev.State)
			return ProcessResult{
				Success:       false,
				NeedsApproval: true,
				Error:         fmt.Sprintf("PR #%d reviewer state %s: %s", prNumber, ev.State, ev.Error),
			}
		}
	}

	return e.finishPRMerge(prNumber, branch, target, false)
}

// finishPRMerge performs the actual PR merge and syncs local state.
// It does not record the reviewer audit bead: the caller (doMergePR) records
// the audit bead only after this returns success, so a failed merge or
// push-verification cannot orphan an audit bead against a failed MR.
func (e *Engineer) finishPRMerge(prNumber int, branch, target string, degradedQuorum bool) ProcessResult {
	provider := e.config.VCSProvider
	if provider == "" {
		provider = "github"
	}

	_, _ = fmt.Fprintf(e.output, "[Engineer] Merging PR #%d via %s API (squash)...\n", prNumber, provider)
	mergeCommit, err := e.prProvider.MergePR(prNumber, "squash")
	if err != nil {
		return ProcessResult{
			Success: false,
			Error:   fmt.Sprintf("PR merge failed for PR #%d: %v", prNumber, err),
		}
	}

	// Step PR.4: Sync local target branch after remote merge
	if err := e.git.Checkout(target); err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to checkout %s after PR merge: %v\n", target, err)
	} else if err := e.git.Pull("origin", target); err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to pull %s after PR merge: %v\n", target, err)
	}

	if mergeCommit == "" {
		if sha, err := e.git.Rev("HEAD"); err == nil {
			mergeCommit = sha
		}
	}
	if err := e.git.VerifyPushedCommit("origin", target, mergeCommit); err != nil {
		return ProcessResult{
			Success: false,
			Error:   err.Error(),
		}
	}

	_, _ = fmt.Fprintf(e.output, "[Engineer] Successfully merged PR #%d: %s\n", prNumber, shortSHA(mergeCommit))
	return ProcessResult{
		Success:         true,
		MergeCommit:     mergeCommit,
		DegradedQuorum:  degradedQuorum,
		PublishedCommit: mergeCommit,
	}
}

// degradedQuorumRule builds the degraded-quorum rule from merge queue config.
func (e *Engineer) degradedQuorumRule() DegradedQuorumRule {
	rule := DegradedQuorumRule{
		Enabled: e.config.DegradedQuorumEnabled != nil && *e.config.DegradedQuorumEnabled,
	}
	if rule.Enabled {
		rule.MinPassReviews = e.config.ReviewQuorumMin
	}
	return rule
}

// EvaluateWithRule re-evaluates a provider-level review evaluation using the
// configured degraded-quorum rule. Providers return a raw classification;
// this applies the rig's explicit quorum settings.
func EvaluateWithRule(ev *ReviewEvaluation, rule DegradedQuorumRule) *ReviewEvaluation {
	if ev == nil {
		return nil
	}
	result := EvaluateReviews(ev.Results, rule)
	return &result
}

// recordReviewerAuditBead creates a follow-up audit task for reviewers that
// were unavailable or produced no verdict during a degraded-quorum merge.
func (e *Engineer) recordReviewerAuditBead(mr *MRInfo, ev *ReviewEvaluation) (string, error) {
	if mr == nil || ev == nil || len(ev.AuditReviewers) == 0 {
		return "", nil
	}

	description := fmt.Sprintf(`Reviewer audit obligation for degraded-quorum merge

## Metadata
- MR: %s
- Branch: %s
- Source issue: %s
- Review state: %s
- Audit reviewers: %s
- Pass reviews: %d

## Notes
These reviewers were unavailable or produced no verdict during the merge.
The merge proceeded under explicit degraded-quorum configuration.
Verify whether a retroactive review is needed and close this task when audited.`,
		mr.ID,
		mr.Branch,
		mr.SourceIssue,
		ev.State,
		strings.Join(ev.AuditReviewers, ", "),
		ev.PassCount,
	)

	task, err := e.beads.Create(beads.CreateOptions{
		Title:       fmt.Sprintf("Reviewer audit: %s", mr.ID),
		Labels:      []string{"gt:audit", "gt:task"},
		Priority:    mr.Priority,
		Description: description,
		Actor:       e.rig.Name + "/refinery",
		Rig:         e.rig.Name,
	})
	if err != nil {
		return "", fmt.Errorf("creating reviewer audit bead: %w", err)
	}

	return task.ID, nil
}

func (e *Engineer) acquireMainPushSlot(ctx context.Context) (string, error) {
	slotID, err := e.mergeSlotEnsureExists()
	if err != nil {
		return "", fmt.Errorf("ensure merge slot exists: %w", err)
	}

	seq := atomic.AddUint64(&mergeSlotSeq, 1)
	holder := fmt.Sprintf("%s/refinery/push/%d-%d", e.rig.Name, time.Now().UnixNano(), seq)

	// The conflict-resolution path holds the slot with holder "rigName/refinery".
	// Both push and conflict-resolution run in the same single-threaded refinery
	// agent, so if our own rig holds the slot for conflict resolution, we can
	// safely proceed without re-acquiring — no concurrent push is possible.
	selfConflictHolder := e.rig.Name + "/refinery"

	backoff := e.mergeSlotRetryBackoff
	if backoff == 0 {
		backoff = 500 * time.Millisecond
	}

	for attempt := 0; attempt <= e.mergeSlotMaxRetries; attempt++ {
		if attempt > 0 {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Merge slot held, retrying in %v (attempt %d/%d)...\n", backoff, attempt, e.mergeSlotMaxRetries)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return "", ctx.Err()
			}
			backoff = min(backoff*2, 10*time.Second)
		}

		status, err := e.mergeSlotAcquire(holder, false)
		if err != nil {
			return "", fmt.Errorf("acquire merge slot %s (%s): %w", slotID, holder, err)
		}
		if status == nil {
			return "", fmt.Errorf("acquire merge slot %s (%s): empty status", slotID, holder)
		}
		if status.Available || status.Holder == holder {
			return holder, nil
		}
		// Slot held by our own conflict-resolution path — safe to proceed.
		if status.Holder == selfConflictHolder {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Merge slot held by conflict-resolution path, proceeding\n")
			return "", nil // No holder to release — conflict-resolution owns the slot
		}
	}

	return "", fmt.Errorf("merge slot %s: %w after %d retries", slotID, errMergeSlotTimeout, e.mergeSlotMaxRetries)
}

// ValidateTestCommand validates that a test command is safe to execute.
// TestCommand comes from the rig's operator-controlled config.json, not from
// user input or PR branches. This validation provides defense-in-depth for the
// trusted infrastructure config path.
func ValidateTestCommand(cmd string) error {
	if strings.TrimSpace(cmd) == "" {
		return fmt.Errorf("test command must not be empty")
	}
	return nil
}

// runTests runs the configured test command and returns the result.
func (e *Engineer) runTests(ctx context.Context) ProcessResult {
	if err := ValidateTestCommand(e.config.TestCommand); err != nil {
		return ProcessResult{
			Success: false,
			Error:   fmt.Sprintf("invalid test command: %v", err),
		}
	}

	// Run the test command with retries for flaky tests
	maxRetries := e.config.RetryFlakyTests
	if maxRetries < 1 {
		maxRetries = 1
	}

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if attempt > 1 {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Retrying tests (attempt %d/%d)...\n", attempt, maxRetries)
		}

		// Trust boundary: TestCommand comes from rig's config.json (operator-controlled
		// infrastructure config), not from PR branches or user input. Shell execution
		// is intentional for flexibility (pipes, env vars, etc).
		_, _ = fmt.Fprintf(e.output, "[Engineer] Executing test command: %s\n", e.config.TestCommand)
		cmd := exec.CommandContext(ctx, "sh", "-c", e.config.TestCommand) //nolint:gosec // G204: TestCommand is from trusted rig config
		util.SetDetachedProcessGroup(cmd)
		cmd.Dir = e.workDir
		cmd.Env = util.GateCommandEnv() // ensure Go toolchain is on PATH for test subprocesses
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		if err == nil {
			return ProcessResult{Success: true}
		}
		lastErr = err

		// Check if context was canceled
		if ctx.Err() != nil {
			return ProcessResult{
				Success: false,
				Error:   "test run canceled",
			}
		}
	}

	return ProcessResult{
		Success:     false,
		TestsFailed: true,
		Error:       fmt.Sprintf("tests failed after %d attempts: %v", maxRetries, lastErr),
	}
}

// runGate executes a single quality gate command and returns the result.
func (e *Engineer) runGate(ctx context.Context, name string, gate *GateConfig) GateResult {
	start := time.Now()

	if strings.TrimSpace(gate.Cmd) == "" {
		return GateResult{
			Name:    name,
			Success: false,
			Error:   "gate command is empty",
			Elapsed: time.Since(start),
		}
	}

	// Apply per-gate timeout if configured
	gateCtx := ctx
	if gate.Timeout > 0 {
		var cancel context.CancelFunc
		gateCtx, cancel = context.WithTimeout(ctx, gate.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(gateCtx, "sh", "-c", gate.Cmd) //nolint:gosec // G204: Gate commands are from trusted rig config
	util.SetDetachedProcessGroup(cmd)
	cmd.Dir = e.workDir
	cmd.Env = util.GateCommandEnv() // ensure Go toolchain is on PATH for gate subprocesses
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	elapsed := time.Since(start)
	output := strings.TrimSpace(stdout.String())
	if stderrStr := strings.TrimSpace(stderr.String()); stderrStr != "" {
		if output != "" {
			output += "\n"
		}
		output += stderrStr
	}

	if err == nil {
		return GateResult{
			Name:    name,
			Success: true,
			Elapsed: elapsed,
			Output:  output,
		}
	}

	errMsg := fmt.Sprintf("%v", err)
	if gateCtx.Err() == context.DeadlineExceeded {
		errMsg = fmt.Sprintf("timed out after %v", gate.Timeout)
	}

	result := GateResult{
		Name:    name,
		Success: false,
		Error:   errMsg,
		Elapsed: elapsed,
		Output:  output,
	}

	// Surface-scoped acceptance: if all failures are outside the changed
	// surface, treat the gate as having passed.
	if e.surfaceAcceptsFailure(gate, result) {
		result.Success = true
		result.Error = ""

		_, _ = fmt.Fprintf(e.output, "[Engineer] Gate %q: failures limited to packages outside the changed surface; treating as passed\n", name)
	} else {
		if output != "" {
			// Cap error message to avoid huge failure strings
			cap := output
			if len(cap) > 500 {
				cap = cap[:500] + "..."
			}
			result.Error = fmt.Sprintf("%s: %s", result.Error, cap)
		}
	}

	return result
}

// surfaceScope returns the effective surface scope for a gate. Empty config
// defaults to "go-packages" for full-workspace Go test/build commands; all
// other commands default to "disabled".
func (e *Engineer) surfaceScope(gate *GateConfig) string {
	if gate.SurfaceScope != "" {
		return gate.SurfaceScope
	}
	cmd := strings.ToLower(gate.Cmd)
	if (strings.Contains(cmd, "go test") || strings.Contains(cmd, "go build")) && strings.Contains(cmd, "./...") {
		return SurfaceScopeGoPackages
	}
	return SurfaceScopeDisabled
}

// surfaceAcceptsFailure decides whether a failed gate can be accepted because
// its failures are limited to the packages outside the changed surface.
func (e *Engineer) surfaceAcceptsFailure(gate *GateConfig, result GateResult) bool {
	if result.Success {
		return false
	}
	if e.surface == nil {
		return false
	}
	scope := e.surfaceScope(gate)
	if scope != SurfaceScopeGoPackages {
		return false
	}

	changed, err := e.changedGoPackages(e.surface.base, e.surface.head)
	if err != nil || len(changed) == 0 {
		return false
	}

	failed := parseGoFailingPackages(result.Output)
	if len(failed) == 0 {
		// We couldn't identify the failing packages, so we can't safely
		// classify the failure as unrelated.
		return false
	}

	for pkg := range failed {
		if _, ok := changed[pkg]; ok {
			return false
		}
	}
	return true
}

// changedGoPackages returns the set of Go package import paths touched between
// two refs. It uses the module path declared in go.mod plus the directory of
// each changed .go file.
func (e *Engineer) changedGoPackages(base, head string) (map[string]struct{}, error) {
	files, err := e.git.DiffNameOnly(base, head)
	if err != nil {
		return nil, err
	}
	module := e.modulePath()
	if module == "" {
		return nil, fmt.Errorf("could not determine module path from go.mod")
	}
	pkgs := make(map[string]struct{})
	for _, f := range files {
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		dir := filepath.Dir(f)
		if dir == "." {
			dir = ""
		}
		pkgs[goPackagePath(module, dir)] = struct{}{}
	}
	return pkgs, nil
}

// modulePath reads the module directive from go.mod.
func (e *Engineer) modulePath() string {
	data, err := os.ReadFile(filepath.Join(e.workDir, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "//") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" {
			return fields[1]
		}
	}
	return ""
}

// goPackagePath joins a module path with a relative directory to form an
// import path.
func goPackagePath(module, relDir string) string {
	relDir = filepath.ToSlash(relDir)
	if relDir == "." || relDir == "" {
		return module
	}
	return module + "/" + relDir
}

var goFailingPackageRe = regexp.MustCompile(`(?m)^(?:FAIL|#)[ \t]+(\S+)`)

// parseGoFailingPackages extracts package import paths that Go reported as
// failing from test or build output. It recognizes lines like:
//
//	FAIL example.com/mod/pkg
//	# example.com/mod/pkg
//
// Lines that are just "FAIL" with no following package are ignored.
func parseGoFailingPackages(output string) map[string]struct{} {
	pkgs := make(map[string]struct{})
	for _, m := range goFailingPackageRe.FindAllStringSubmatch(output, -1) {
		pkg := m[1]
		// Strip a trailing colon that sometimes appears on build error lines.
		pkg = strings.TrimSuffix(pkg, ":")
		if pkg == "" {
			continue
		}
		pkgs[pkg] = struct{}{}
	}
	return pkgs
}

// runGates executes all pre-merge gates (backward-compatible entry point).
func (e *Engineer) runGates(ctx context.Context) ProcessResult {
	return e.runGatesForPhase(ctx, GatePhasePreMerge)
}

// runGatesForPhase executes gates matching the given phase.
// Gates run in parallel if GatesParallel is true; otherwise sequentially.
// Any single gate failure means overall failure.
func (e *Engineer) runGatesForPhase(ctx context.Context, phase GatePhase) ProcessResult {
	// Filter gates for this phase. Empty phase is treated as pre-merge (default).
	gates := make(map[string]*GateConfig)
	for name, gc := range e.config.Gates {
		gatePhase := gc.Phase
		if gatePhase == "" {
			gatePhase = GatePhasePreMerge
		}
		if gatePhase == phase {
			gates[name] = gc
		}
	}
	if len(gates) == 0 {
		return ProcessResult{Success: true}
	}

	// Sort gate names for deterministic ordering
	names := make([]string, 0, len(gates))
	for name := range gates {
		names = append(names, name)
	}
	sort.Strings(names)

	parallel := e.config.GatesParallel && phase == GatePhasePreMerge // post-squash always sequential
	_, _ = fmt.Fprintf(e.output, "[Engineer] Running %d %s gate(s) (parallel=%v)\n", len(names), phase, parallel)

	var results []GateResult

	if parallel {
		results = make([]GateResult, len(names))
		var wg sync.WaitGroup
		for i, name := range names {
			wg.Add(1)
			go func(idx int, gateName string) {
				defer wg.Done()
				_, _ = fmt.Fprintf(e.output, "[Engineer] Gate %q: starting (%s)\n", gateName, gates[gateName].Cmd)
				results[idx] = e.runGate(ctx, gateName, gates[gateName])
			}(i, name)
		}
		wg.Wait()
	} else {
		for _, name := range names {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Gate %q: starting (%s)\n", name, gates[name].Cmd)
			result := e.runGate(ctx, name, gates[name])
			results = append(results, result)
			if !result.Success {
				// Sequential mode: stop on first failure
				break
			}
		}
	}

	// Report results
	var failures []string
	for _, r := range results {
		if r.Success {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Gate %q: passed (%v)\n", r.Name, r.Elapsed.Truncate(time.Millisecond))
		} else {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Gate %q: FAILED (%v) - %s\n", r.Name, r.Elapsed.Truncate(time.Millisecond), r.Error)
			failures = append(failures, fmt.Sprintf("%s: %s", r.Name, r.Error))
		}
	}

	if len(failures) > 0 {
		return ProcessResult{
			Success:     false,
			TestsFailed: true,
			Error:       fmt.Sprintf("quality gates failed: %s", strings.Join(failures, "; ")),
		}
	}

	_, _ = fmt.Fprintln(e.output, "[Engineer] All quality gates passed")
	return ProcessResult{Success: true}
}

// effectiveDurableReviewTarget returns the branch the durable review gate
// protects. It only enforces on the rig's default branch (usually main),
// because that is where the audited merge history lives.
func (e *Engineer) effectiveDurableReviewTarget() string {
	return e.rig.DefaultBranch()
}

func canonicalMergeTarget(target string) string {
	target = strings.TrimSpace(target)
	target = strings.TrimPrefix(target, "refs/heads/")
	target = strings.TrimPrefix(target, "refs/remotes/")
	return strings.TrimPrefix(target, "origin/")
}

// durableReviewGateEnabled reports whether the durable review gate is
// configured as required for the given target branch.
func (e *Engineer) durableReviewGateEnabled(target string) bool {
	if e.config.DurableReviewGate == nil {
		return false
	}
	if !e.config.DurableReviewGate.Required {
		return false
	}
	return strings.EqualFold(canonicalMergeTarget(target), e.effectiveDurableReviewTarget())
}

// durableReviewRequiredForMR reports whether the durable review/HMAC gate must
// be enforced for this merge. The gate is required when configured for the
// target (Required=true). It is also required when the pre-verified skip-gates
// fast path is actually being used on the durable-review target, even when
// durable_review_gate.required is false. This prevents a stale or misconfigured
// Required toggle from letting gt done --pre-verified bypass durable
// multi-model review/HMAC attestation. The requirement is bound to the actual
// skipGates decision (not merely mr.PreVerified) so a stale PV MR that falls
// back to normal gates is not held to the extra requirement. (gastown-6n7)
func (e *Engineer) durableReviewRequiredForMR(mr *MRInfo, skipGates bool, target string) bool {
	_ = mr
	if e.durableReviewGateEnabled(target) {
		return true
	}
	if !skipGates {
		return false
	}
	if e.config.DurableReviewGate == nil {
		return false
	}
	return strings.EqualFold(canonicalMergeTarget(target), e.effectiveDurableReviewTarget())
}

// durableReviewAttestDir returns the configured attestation directory with
// environment fallbacks.
func (e *Engineer) durableReviewAttestDir() string {
	if e.config.DurableReviewGate != nil && e.config.DurableReviewGate.AttestDir != "" {
		return e.config.DurableReviewGate.AttestDir
	}
	if dir := os.Getenv("GT_GATE_ATTEST_DIR"); dir != "" {
		return dir
	}
	return "/home/ubuntu/.gt-gate-attestations"
}

// durableReviewHMACKeyPath returns the key path used by refinery-gate.sh when
// writing attestation tokens.
func (e *Engineer) durableReviewHMACKeyPath() string {
	if e.config.DurableReviewGate != nil && strings.TrimSpace(e.config.DurableReviewGate.HMACKeyPath) != "" {
		return strings.TrimSpace(e.config.DurableReviewGate.HMACKeyPath)
	}
	return "/home/ubuntu/.gt-gate-hmac-key"
}

const (
	minDurableReviewHMACKeyBytes = 32
	maxDurableReviewHMACKeyBytes = 4096
	maxDurableReviewTokenBytes   = 1024
	maxDurableReviewAssignBytes  = 16 * 1024
)

func readBoundedFile(file *os.File, path, label string, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s %s: %w", label, path, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s %s is too large", label, path)
	}
	return data, nil
}

func (e *Engineer) readDurableReviewHMACKey() ([]byte, error) {
	keyPath := e.durableReviewHMACKeyPath()
	file, info, err := openStableRegularFile(keyPath, "HMAC key")
	if err != nil {
		return nil, err
	}
	defer file.Close()

	if perm := info.Mode().Perm(); perm&0077 != 0 {
		return nil, fmt.Errorf("HMAC key %s has insecure permissions %03o; group/other permissions must be 000", keyPath, perm)
	}
	if info.Size() > maxDurableReviewHMACKeyBytes {
		return nil, fmt.Errorf("HMAC key %s is too large", keyPath)
	}
	key, err := readBoundedFile(file, keyPath, "HMAC key", maxDurableReviewHMACKeyBytes)
	if err != nil {
		return nil, err
	}
	key = bytes.TrimRight(key, "\r\n")
	trimmedKey := bytes.TrimSpace(key)
	if len(trimmedKey) == 0 {
		return nil, fmt.Errorf("HMAC key %s must contain non-whitespace bytes", keyPath)
	}
	if len(trimmedKey) < minDurableReviewHMACKeyBytes {
		return nil, fmt.Errorf("HMAC key %s must contain at least %d non-whitespace bytes", keyPath, minDurableReviewHMACKeyBytes)
	}
	return key, nil
}

func gateCommandEnvWith(extra ...string) []string {
	env := util.GateCommandEnv()
	if len(env) == 0 {
		// exec.Cmd.Env=nil inherits the parent environment. When adding gate
		// metadata we must materialize that same inherited environment first.
		env = os.Environ()
	}
	env = append([]string(nil), env...)
	for _, kv := range extra {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			env = append(env, kv)
			continue
		}
		prefix := key + "="
		replaced := false
		for i, existing := range env {
			if strings.HasPrefix(existing, prefix) {
				env[i] = kv
				replaced = true
				break
			}
		}
		if !replaced {
			env = append(env, kv)
		}
	}
	return env
}

var durableReviewAssignmentIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func durableReviewAssignmentID(sourceIssue string) (string, bool) {
	sourceIssue = strings.TrimSpace(sourceIssue)
	if !durableReviewAssignmentIDRE.MatchString(sourceIssue) {
		return "", false
	}
	if strings.ContainsAny(sourceIssue, `/\`) {
		return "", false
	}
	if strings.Contains(sourceIssue, "..") {
		return "", false
	}
	return sourceIssue, true
}

func (e *Engineer) durableReviewTownRoot() (string, bool) {
	if e.rig == nil || strings.TrimSpace(e.rig.Path) == "" {
		return "", false
	}
	start, err := filepath.EvalSymlinks(filepath.Clean(e.rig.Path))
	if err != nil {
		return "", false
	}
	for dir := start; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "mayor", "town.json")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return "", false
}

type durableReviewModelAssignment struct {
	Agent string `json:"agent"`
}

func (e *Engineer) durableReviewWriterFromAgentBead(agentBead string) string {
	if strings.TrimSpace(agentBead) == "" || e.beads == nil {
		return ""
	}
	agentBeads := e.beads.ForAgentBead()
	if agentBeads == nil {
		return ""
	}
	_, fields, err := agentBeads.GetAgentBead(agentBead)
	if err != nil || fields == nil {
		return ""
	}
	return strings.TrimSpace(fields.AssignedAgent)
}

func (e *Engineer) durableReviewWriterFromAssignment(sourceIssue string) string {
	assignmentID, ok := durableReviewAssignmentID(sourceIssue)
	if !ok || e.rig == nil || strings.TrimSpace(e.rig.Path) == "" {
		return ""
	}
	townRoot, ok := e.durableReviewTownRoot()
	if !ok {
		return ""
	}
	path := filepath.Join(townRoot, ".runtime", "model-assignments", assignmentID+".json")
	file, info, err := openStableRegularFile(path, "model assignment")
	if err != nil {
		return ""
	}
	defer file.Close()

	if perm := info.Mode().Perm(); perm&0022 != 0 {
		return ""
	}
	if info.Size() > maxDurableReviewAssignBytes {
		return ""
	}
	data, err := readBoundedFile(file, path, "model assignment", maxDurableReviewAssignBytes)
	if err != nil {
		return ""
	}
	var assignment durableReviewModelAssignment
	if err := json.Unmarshal(data, &assignment); err != nil {
		return ""
	}
	return strings.TrimSpace(assignment.Agent)
}

func (e *Engineer) durableReviewWriter(mr *MRInfo) string {
	if mr == nil {
		return "unknown"
	}
	if writer := e.durableReviewWriterFromAgentBead(mr.AgentBead); writer != "" {
		return writer
	}
	if writer := e.durableReviewWriterFromAssignment(mr.SourceIssue); writer != "" {
		return writer
	}
	return "unknown"
}

// durableReviewGateCmd returns the configured review gate command, or an empty
// string if none is configured.
//
// Legacy-config fallback: if durable_review_gate is required but the command is
// empty, reuse an existing post-squash gate whose command invokes
// refinery-gate.sh. Gastown's production config defines the durable review as
// a post-squash gate named "four-model-refinery-review"; this fallback lets
// that config enforce durable attestation without duplicating the command.
func (e *Engineer) durableReviewGateCmd() string {
	if e.config.DurableReviewGate == nil {
		return ""
	}
	cmd := strings.TrimSpace(e.config.DurableReviewGate.Cmd)
	if cmd != "" {
		return cmd
	}
	for _, gate := range e.config.Gates {
		if gate == nil || gate.Phase != GatePhasePostSquash {
			continue
		}
		candidate := strings.TrimSpace(gate.Cmd)
		if strings.Contains(candidate, "refinery-gate.sh") {
			return candidate
		}
	}
	return ""
}

func (e *Engineer) durableReviewGateTimeout() time.Duration {
	if e.config.DurableReviewGate != nil && e.config.DurableReviewGate.Timeout > 0 {
		return e.config.DurableReviewGate.Timeout
	}
	return DefaultDurableReviewGateTimeout
}

// durableReviewAttestationPath returns the path to the HMAC attestation file
// for a given tree hash, if an attestation directory is configured.
func (e *Engineer) durableReviewAttestationPath(tree string) string {
	if tree == "" {
		return ""
	}
	return filepath.Join(e.durableReviewAttestDir(), tree)
}

func durableReviewAttestationWriter(writer string) string {
	writer = normalizeWriter(writer)
	if writer == "" {
		return "unknown"
	}
	return writer
}

func durableReviewAttestationPayload(tree, writer string) []byte {
	return []byte(fmt.Sprintf("gastown-durable-review-v1\ntree=%s\nwriter=%s\n", tree, durableReviewAttestationWriter(writer)))
}

// expectedDurableReviewAttestation returns the HMAC token expected for a tree
// reviewed under the given writer identity. Binding the writer prevents an
// all-four unknown-writer attestation from being replayed as a known-writer
// three-peer attestation, or vice versa.
func expectedDurableReviewAttestationWithKey(key []byte, tree, writer string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(durableReviewAttestationPayload(tree, writer))
	return mac.Sum(nil)
}

// hasDurableReviewAttestation reports whether an HMAC attestation exists for
// the merge candidate at the current HEAD.
func (e *Engineer) hasDurableReviewAttestation(key []byte, writer string) (bool, string, error) {
	tree, err := e.git.Rev("HEAD^{tree}")
	if err != nil {
		return false, "", fmt.Errorf("resolving merge-candidate tree: %w", err)
	}
	path := e.durableReviewAttestationPath(tree)
	if path == "" {
		return false, tree, nil
	}
	file, info, err := openStableRegularFile(path, "attestation")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, tree, nil
		}
		return false, tree, err
	}
	defer file.Close()

	if info.Size() > maxDurableReviewTokenBytes {
		return false, tree, fmt.Errorf("checking attestation %s: token file too large", path)
	}
	rawToken, err := readBoundedFile(file, path, "attestation", maxDurableReviewTokenBytes)
	if err != nil {
		return false, tree, err
	}
	token := strings.TrimSpace(string(rawToken))
	actual, err := hex.DecodeString(token)
	if err != nil {
		return false, tree, fmt.Errorf("invalid HMAC attestation token at %s: %w", path, err)
	}
	expected := expectedDurableReviewAttestationWithKey(key, tree, writer)
	return hmac.Equal(actual, expected), tree, nil
}

// isEmptyReviewDiff reports whether the merge-candidate range (target...branch,
// triple-dot) contains no changes. This is the source-controlled counterpart
// of the m3 reviewer's "empty diff is blocking" rubric item: when the diff
// between branch and target is empty, the durable review gate refuses to grant
// an attestation regardless of what any reviewer (m3, codex, umans-kimi,
// umans-glm, opus) returned (gastown-cet.12.4). The gate must fail closed
// because a PASS on a zero-content diff is a degenerate verdict, not
// evidence of approval.
//
// A missing branch or target is treated as "unknown" and returns (false, nil):
// the gate does not refuse to run on a missing ref, only on a verified-empty
// diff. This avoids blocking legitimate first-commit reviews where the base
// branch is being created fresh.
func (e *Engineer) isEmptyReviewDiff(branch, target string) (bool, error) {
	if branch == "" || target == "" {
		return false, nil
	}
	return e.git.IsEmptyDiff(target, branch)
}

// runDurableReviewGate enforces the fail-closed multi-model review gate.
// It checks for an existing HMAC attestation for the merge-candidate tree and,
// if missing, runs the configured review gate command. The merge is rejected
// unless an attestation exists after enforcement.
//
// Empty-diff guard (gastown-cet.12.4): before invoking the reviewer, the gate
// checks whether the merge-candidate diff (branch...target) contains any
// changes. If the diff is empty, the gate fails closed regardless of what the
// reviewer returns. A reviewer that produces zero findings on a zero-content
// diff performed no actual review, so the gate must not write an attestation
// that would later be treated as evidence of approval. This defends against
// the m3-degenerate-PASS incident where m3 returned PASS on the empty gtviz
// initial commit and the gate treated that zero-content PASS as approval,
// enabling a degraded-quorum bypass.
func (e *Engineer) runDurableReviewGate(ctx context.Context, branch, target string, mr *MRInfo, skipGates bool, reviewBase string) ProcessResult {
	if !e.durableReviewRequiredForMR(mr, skipGates, target) {
		return ProcessResult{Success: true}
	}

	_, _ = fmt.Fprintf(e.output, "[Engineer] Durable review gate required for %s\n", target)

	// Fail closed immediately if no review gate command is configured.
	cmd := e.durableReviewGateCmd()
	if cmd == "" {
		_, _ = fmt.Fprintln(e.output, "[Engineer] Durable review gate FAILED - no command configured")
		return ProcessResult{
			Success:     false,
			TestsFailed: true,
			Error:       "durable review gate required but no command configured",
		}
	}
	key, err := e.readDurableReviewHMACKey()
	if err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Durable review gate FAILED - HMAC key check failed: %v\n", err)
		return ProcessResult{
			Success:     false,
			TestsFailed: true,
			Error:       fmt.Sprintf("durable review HMAC key check failed: %v", err),
		}
	}

	// Empty-diff guard: refuse to grant an attestation when there is nothing
	// to review. In the doMerge flow, target is already locally advanced to
	// the squashed candidate when this runs, so compare branch against the
	// pre-merge target commit captured before the squash. Direct unit tests
	// and callers that have not advanced target use the target branch itself.
	diffBase := target
	if base := strings.TrimSpace(reviewBase); base != "" {
		diffBase = base
	}
	if empty, err := e.isEmptyReviewDiff(branch, diffBase); err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Durable review gate FAILED - could not determine diff state: %v\n", err)
		return ProcessResult{
			Success:     false,
			TestsFailed: true,
			Error:       fmt.Sprintf("durable review gate could not determine diff state: %v", err),
		}
	} else if empty {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Durable review gate FAILED - empty diff (no changes between branch and review base %s); refusing to grant attestation on zero-content review\n", diffBase)
		return ProcessResult{
			Success:     false,
			TestsFailed: true,
			Error:       "durable review gate: empty diff (no changes between branch and review base); degenerate PASS on empty diff is not a valid attestation (gastown-cet.12.4)",
		}
	}

	writer := e.durableReviewWriter(mr)
	attestationWriter := durableReviewAttestationWriter(writer)

	// If an attestation already exists for this tree and writer identity, we can
	// skip running the expensive gate. An attestation for the same tree under a
	// different writer does not satisfy this check.
	attested, tree, err := e.hasDurableReviewAttestation(key, attestationWriter)
	if err != nil {
		return ProcessResult{
			Success:     false,
			TestsFailed: true,
			Error:       fmt.Sprintf("durable review attestation check failed: %v", err),
		}
	}
	if attested {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Durable review attestation present for tree %s (writer=%s); skipping gate\n", shortSHA(tree), attestationWriter)
		return ProcessResult{Success: true}
	}

	_, _ = fmt.Fprintf(e.output, "[Engineer] Running durable review gate for tree %s (writer=%s)...\n", shortSHA(tree), attestationWriter)

	gateTimeout := e.durableReviewGateTimeout()
	gateCtx, cancel := context.WithTimeout(ctx, gateTimeout)
	defer cancel()

	runCmd := exec.CommandContext(gateCtx, "sh", "-c", cmd) //nolint:gosec // G204: Review gate command is from trusted rig config
	util.SetDetachedProcessGroup(runCmd)
	runCmd.Dir = e.workDir
	// Expose metadata to the review gate command so it can locate the
	// worktree, the merge candidate, and the attestation directory.
	runCmd.Env = gateCommandEnvWith(
		"GT_REVIEW_GATE_WORKTREE="+e.workDir,
		"GT_REVIEW_GATE_BRANCH="+branch,
		"GT_REVIEW_GATE_TARGET="+target,
		"GT_REVIEW_GATE_WRITER="+attestationWriter,
		"GT_GATE_ATTEST_DIR="+e.durableReviewAttestDir(),
		"GT_GATE_HMAC_KEY="+e.durableReviewHMACKeyPath(),
	)
	var stdout, stderr bytes.Buffer
	runCmd.Stdout = &stdout
	runCmd.Stderr = &stderr

	start := time.Now()
	runErr := runCmd.Run()
	elapsed := time.Since(start)
	output := strings.TrimSpace(stdout.String())
	if stderrStr := strings.TrimSpace(stderr.String()); stderrStr != "" {
		if output != "" {
			output += "\n"
		}
		output += stderrStr
	}

	if runErr != nil {
		errMsg := fmt.Sprintf("durable review gate failed (%v)", runErr)
		if gateCtx.Err() == context.DeadlineExceeded {
			errMsg = fmt.Sprintf("durable review gate timed out after %v", gateTimeout)
		}
		if output != "" {
			cap := output
			if len(cap) > 500 {
				cap = cap[:500] + "..."
			}
			errMsg = fmt.Sprintf("%s: %s", errMsg, cap)
		}
		_, _ = fmt.Fprintf(e.output, "[Engineer] Durable review gate FAILED (%v) - %s\n", elapsed.Truncate(time.Millisecond), errMsg)
		return ProcessResult{
			Success:     false,
			TestsFailed: true,
			Error:       errMsg,
		}
	}

	_, _ = fmt.Fprintf(e.output, "[Engineer] Durable review gate passed (%v)\n", elapsed.Truncate(time.Millisecond))

	// Fail closed if the gate command exited 0 but did not write the
	// expected HMAC attestation. A missing attestation means there is no
	// durable proof that reviewers actually ran.
	attested, tree, err = e.hasDurableReviewAttestation(key, attestationWriter)
	if err != nil {
		return ProcessResult{
			Success:     false,
			TestsFailed: true,
			Error:       fmt.Sprintf("durable review gate passed but attestation check failed: %v", err),
		}
	}
	if !attested {
		return ProcessResult{
			Success:     false,
			TestsFailed: true,
			Error:       fmt.Sprintf("durable review gate passed but HMAC attestation missing for tree %s at %s", tree, e.durableReviewAttestationPath(tree)),
		}
	}

	_, _ = fmt.Fprintf(e.output, "[Engineer] Durable review attestation recorded for tree %s\n", shortSHA(tree))
	return ProcessResult{Success: true}
}

// syncCrewWorkspaces pulls latest changes to all crew workspaces.
// This ensures crew members have access to newly merged code without manual sync.
func (e *Engineer) syncCrewWorkspaces() {
	crewGit := git.NewGit(e.rig.Path)
	crewMgr := crew.NewManager(e.rig, crewGit)

	workers, err := crewMgr.List()
	if err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to list crew workspaces: %v\n", err)
		return
	}

	if len(workers) == 0 {
		return
	}

	_, _ = fmt.Fprintf(e.output, "[Engineer] Syncing %d crew workspace(s)...\n", len(workers))

	for _, worker := range workers {
		result, err := crewMgr.Pristine(worker.Name)
		if err != nil {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to sync crew/%s: %v\n", worker.Name, err)
			continue
		}
		if result.Pulled {
			_, _ = fmt.Fprintf(e.output, "[Engineer] ✓ Synced crew/%s\n", worker.Name)
		} else if result.PullError != "" {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: crew/%s pull failed: %s\n", worker.Name, result.PullError)
		}
	}
}

// ProcessMRInfo processes a merge request from MRInfo.
func (e *Engineer) ProcessMRInfo(ctx context.Context, mr *MRInfo) ProcessResult {
	target := canonicalMergeTarget(mr.Target)
	if target == "" {
		target = e.rig.DefaultBranch()
	}
	// MR fields are directly on the struct
	_, _ = fmt.Fprintln(e.output, "[Engineer] Processing MR:")
	_, _ = fmt.Fprintf(e.output, "  Branch: %s\n", mr.Branch)
	if target != mr.Target {
		_, _ = fmt.Fprintf(e.output, "  Target: %s (canonicalized from %s)\n", target, mr.Target)
	} else {
		_, _ = fmt.Fprintf(e.output, "  Target: %s\n", target)
	}
	_, _ = fmt.Fprintf(e.output, "  Worker: %s\n", mr.Worker)
	_, _ = fmt.Fprintf(e.output, "  Source: %s\n", mr.SourceIssue)

	// gastown-wjk: stamp refinery_started_at and (best-effort) upgrade
	// writer attribution when submit-time could not resolve a model.
	// Done before validation so the refinery→verdict duration is
	// accurate even when the pre-verified fast path skips gates.
	e.recordRefineryStarted(mr.ID)
	e.recordWriterOverwriteIfUnknown(mr.ID)

	// Phase 3: Check pre-verification fast-path.
	// If the polecat already rebased onto the target and ran gates, and the target
	// hasn't moved since, we can skip running gates entirely (~5s merge).
	skipGates := false
	if mr.PreVerified && mr.PreVerifiedBase != "" {
		_, _ = fmt.Fprintf(e.output, "  Pre-verified: yes (base=%s)\n", mr.PreVerifiedBase[:min(8, len(mr.PreVerifiedBase))])
		// Check if target HEAD still matches the verified base
		targetHead, err := e.git.Rev("origin/" + target)
		if err != nil {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: could not resolve origin/%s HEAD: %v (falling through to normal gates)\n", target, err)
		} else if targetHead == mr.PreVerifiedBase {
			_, _ = fmt.Fprintln(e.output, "[Engineer] Pre-verification valid — target unchanged, skipping gates (fast-path)")
			skipGates = true
		} else {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Pre-verification stale — target moved (%s → %s), running gates normally\n",
				mr.PreVerifiedBase[:min(8, len(mr.PreVerifiedBase))], targetHead[:min(8, len(targetHead))])
		}
	}

	// Use the shared merge logic
	return e.doMerge(ctx, mr.Branch, target, mr.SourceIssue, mr, skipGates)
}

// HandleMRInfoSuccess handles a successful merge from MRInfo.
func (e *Engineer) HandleMRInfoSuccess(mr *MRInfo, result ProcessResult) {
	// Release merge slot if this was a conflict resolution
	// The slot is held while conflict resolution is in progress
	holder := e.rig.Name + "/refinery"
	if err := e.mergeSlotRelease(holder); err != nil {
		// Best-effort: slot release failures are always non-fatal.
		// Slot may not have been held (optional acquisition) or may have expired.
		_, _ = fmt.Fprintf(e.output, "[Engineer] Note: merge slot release: %v\n", err)
	} else {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Released merge slot\n")
	}

	// Update and close the MR bead. Track whether the merged state is safe to
	// report as shipped, which determines whether the source bead may be closed
	// (hq-6sdu). A merge is published only when the refinery itself verified the
	// commit on the configured upstream.
	canCloseSource := false
	var updatedMRBead *beads.Issue
	if mr.ID != "" {
		// Fetch the MR bead to update its fields
		mrBead, err := e.beads.Show(mr.ID)
		if err != nil {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to fetch MR bead %s: %v\n", mr.ID, err)
		} else {
			// Update MR with merge_commit SHA, close_reason, and publication state.
			mrFields := beads.ParseMRFields(mrBead)
			if mrFields == nil {
				mrFields = &beads.MRFields{}
			}
			mrFields.MergeCommit = result.MergeCommit
			mrFields.CloseReason = "merged"
			if result.PublishedCommit != "" {
				mrFields.PublishedCommit = result.PublishedCommit
				mrFields.PublishedRemote = "origin"
				mrFields.PublishedAt = time.Now().UTC().Format(time.RFC3339)
				mrFields.TerminalState = beads.MRTerminalPublished
			} else {
				mrFields.TerminalState = beads.MRTerminalMergedLocalNotPublished
			}
			newDesc := beads.SetMRFields(mrBead, mrFields)
			if err := e.beads.Update(mr.ID, beads.UpdateOptions{Description: &newDesc}); err != nil {
				_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to update MR %s with merge commit: %v\n", mr.ID, err)
			} else {
				// Keep the in-memory representation in sync with what we wrote so
				// classification below is authoritative even if a subsequent Show
				// returns --allow-stale data.
				mrBead.Description = newDesc
				updatedMRBead = mrBead
			}
		}

		// Close MR bead with reason 'merged'
		if err := e.beads.CloseWithReason("merged", mr.ID); err != nil {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to close MR %s: %v\n", mr.ID, err)
		} else {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Closed MR bead: %s\n", mr.ID)
			// Classify using the in-memory updated bead to avoid races with
			// --allow-stale reads from Dolt immediately after a write.
			if updatedMRBead != nil {
				closedMR := *updatedMRBead
				closedMR.Status = "closed"
				canCloseSource = beads.CanCloseSourceBead(&closedMR)
			}
		}
	}

	// 1. Close source issue with reference to MR.
	// Use ForceCloseWithReason to bypass dependency checks — the source issue
	// may have an attached molecule (wisp) whose open steps would block a
	// normal close. This matches how gt done handles closures.
	if mr.SourceIssue != "" {
		if !canCloseSource {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Merge not yet published — leaving source issue open: %s\n", mr.SourceIssue)
		} else {
			closeReason := fmt.Sprintf("Merged in %s", mr.ID)
			if result.MergeCommit != "" {
				closeReason = fmt.Sprintf("%s\ntarget_branch: %s\ncommit_sha: %s", closeReason, mr.Target, result.MergeCommit)
			}
			if result.DegradedQuorum {
				closeReason = fmt.Sprintf("%s\nreview_state: degraded_quorum", closeReason)
				if result.AuditBead != "" {
					closeReason = fmt.Sprintf("%s\naudit_bead: %s", closeReason, result.AuditBead)
				}
			}
			if err := e.beads.ForceCloseWithReason(closeReason, mr.SourceIssue); err != nil {
				// Check if already closed (by polecat's gt done) — that's fine
				if issue, showErr := e.beads.Show(mr.SourceIssue); showErr == nil && beads.IssueStatus(issue.Status).IsTerminal() {
					_, _ = fmt.Fprintf(e.output, "[Engineer] Source issue already closed: %s\n", mr.SourceIssue)
				} else {
					_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to close source issue %s: %v\n", mr.SourceIssue, err)
				}
			} else {
				_, _ = fmt.Fprintf(e.output, "[Engineer] Closed source issue: %s\n", mr.SourceIssue)
			}
		}
	}

	// 1.2. Close conflict-resolution tasks that this land has made moot (hq-jnap).
	// Conflict beads otherwise outlive the successful re-land of their content
	// and rot as open issues (re-dlcs/re-4i3b/re-gcii pattern).
	e.closeSupersededConflictArtifacts(mr)

	// 1.5. Clear agent bead's active_mr reference (traceability cleanup)
	if mr.AgentBead != "" {
		if err := e.clearAgentActiveMR(mr.AgentBead); err != nil {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to clear agent bead %s active_mr: %v\n", mr.AgentBead, err)
		}
	}

	// 2. Delete source branch (local and remote).
	// Polecat branches (polecat/*) are always cleaned up — they are ephemeral
	// work branches that should never persist after merge. Other branches
	// respect the DeleteMergedBranches config.
	isPolecat := strings.HasPrefix(mr.Branch, "polecat/")
	if mr.Branch != "" && (e.config.DeleteMergedBranches || isPolecat) {
		if err := e.git.DeleteBranch(mr.Branch, true); err != nil {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to delete local branch %s: %v\n", mr.Branch, err)
		} else {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Deleted local branch: %s\n", mr.Branch)
		}
		// Remote delete — only polecat branches. Non-polecat branches may belong
		// to contributor forks with open upstream PRs; deleting them from origin
		// causes GitHub to auto-close those PRs via head_ref_delete. (GH#2669)
		// gas-fk4: Also skip deletion for polecat branches that have open PRs.
		// When merge_strategy=pr, polecat branches have GitHub PRs that should
		// be closed via gh pr merge (showing "merged"), not via branch deletion
		// (which shows "closed" and destroys the PR audit trail).
		if isPolecat {
			if e.git.HasOpenPR(mr.Branch) {
				_, _ = fmt.Fprintf(e.output, "[Engineer] Skipping remote branch delete for %s: open PR exists (gas-fk4)\n", mr.Branch)
			} else if err := e.git.DeleteRemoteBranch("origin", mr.Branch); err != nil {
				_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to delete remote branch %s: %v\n", mr.Branch, err)
			} else {
				_, _ = fmt.Fprintf(e.output, "[Engineer] Deleted remote branch: %s\n", mr.Branch)
			}
		}
	}

	// 3. Check and auto-close completed convoys
	// After closing a source issue, its parent convoy may now be complete.
	// Run convoy check to auto-close and notify subscribers.
	e.postMergeConvoyCheck(mr)

	// 4. Nudge mayor about successful merge so dispatcher can unblock
	// dependent work. Without this, mayor only discovers completion by polling.
	// Uses nudge (not mail) to avoid permanent Dolt commits for routine signals (GH#2434).
	nudgeMsg := fmt.Sprintf("MERGED: %s issue=%s branch=%s", mr.ID, mr.SourceIssue, mr.Branch)
	nudgeCmd := exec.Command("gt", "nudge", "mayor/", nudgeMsg)
	util.SetDetachedProcessGroup(nudgeCmd)
	nudgeCmd.Dir = e.workDir
	if err := nudgeCmd.Run(); err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to nudge mayor about merge: %v\n", err)
	}

	// 5. Log success
	_, _ = fmt.Fprintf(e.output, "[Engineer] ✓ Merged: %s (commit: %s)\n", mr.ID, result.MergeCommit)

	// gastown-wjk: stamp the terminal merge outcome so the report counts
	// this attempt as merged rather than leaving it as "in flight". Failure
	// to write the telemetry row is non-fatal — log and continue.
	e.recordFinalOutcome(mr.ID, "merged", "", time.Now().UTC(), time.Time{},
		result.MergeCommit, result.PublishedCommit)
}

func (e *Engineer) clearAgentActiveMR(agentBeadID string) error {
	return e.beads.ForAgentBead().UpdateAgentActiveMR(agentBeadID, "")
}

// handleReviewerRejection closes the MR as rejected-needs-rework and notifies
// the worker that the change requires revision. This is a terminal state, unlike
// NeedsApproval which keeps the MR in queue.
//
// gastown-p3w: routes the rejection through `gt mq reject` so the refinery's
// dropin rework router (GT_MQ_REWORK_ROUTER=shadow|enforce) can produce a
// bounded rework packet and invoke the scoped rework-bounce runner without
// Mayor intervention. The route call is shelled out BEFORE the MR is closed --
// `gt mq reject` refuses an already-closed MR with ErrClosedImmutable
// (manager.go), so closing first would silently drop the rework packet.
// The classification returned by the router also drives the worker nudge
// wording: routine NEEDS_REWORK keeps the revise-and-resubmit guidance,
// REVIEW_UNAVAILABLE_HOLD (tooling/cap-deferral) tells the worker NOT to
// resubmit until reviewer availability changes, and ambiguous cases escalate
// to Mayor for human judgment.
//
// gastown-p3w (wus rejection): the rework-bounce shell-out invokes `gt mq
// reject` WITHOUT --notify. Manager.notifyWorkerRejected sends a hardcoded
// "revise and resubmit with 'gt done'" nudge, which would contradict the
// classification-aware nudge this function emits moments later (especially
// for REVIEW_UNAVAILABLE_HOLD, where the worker must NOT be told to
// resubmit). The classification-aware nudge below is the single source of
// truth for what the worker should do next.
func (e *Engineer) handleReviewerRejection(mr *MRInfo, result ProcessResult) {
	cause := result.ReviewerRejectionCause
	if cause == "" {
		cause = "reviewer_rejection"
	}
	_, _ = fmt.Fprintf(e.output, "[Engineer] MR %s: reviewer rejection (cause=%s) — routing through rework-bounce then closing\n", mr.ID, cause)

	// Step 1: Persist the rejection cause on the MR bead. We do not close
	// yet -- the rework-bounce shell-out (step 2) needs the MR still open,
	// and the router inspects both pre-close and post-close state when
	// classifying.
	if mr.ID != "" && e.beads != nil {
		mrBead, err := e.beads.Show(mr.ID)
		if err != nil {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to fetch MR bead %s: %v\n", mr.ID, err)
		} else {
			mrFields := beads.ParseMRFields(mrBead)
			if mrFields == nil {
				mrFields = &beads.MRFields{}
			}
			mrFields.CloseReason = "rejected"
			mrFields.TerminalState = beads.MRTerminalRejectedNeedsRework
			mrFields.ReviewerRejectionCause = cause
			newDesc := beads.SetMRFields(mrBead, mrFields)
			if err := e.beads.Update(mr.ID, beads.UpdateOptions{Description: &newDesc}); err != nil {
				_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to record reviewer rejection on MR %s: %v\n", mr.ID, err)
			}
		}
	}

	// Step 2: gastown-p3w -- route the rejection through `gt mq reject`
	// (without --notify) so the dropin rework router can produce a bounded
	// rework packet and invoke the scoped rework-bounce runner without
	// Mayor intervention. This MUST happen before the MR is closed: `gt mq
	// reject` returns ErrClosedImmutable when the MR is already closed, so
	// a close-first ordering would silently drop the rework packet. The
	// notify flag is intentionally omitted because
	// Manager.notifyWorkerRejected sends a hardcoded "revise and resubmit"
	// nudge that would contradict the classification-aware nudge emitted in
	// step 4 (REVIEW_UNAVAILABLE_HOLD cases must not be told to resubmit
	// until reviewer availability changes).
	var routeClass string
	routed := false
	if mr.ID != "" && e.rig != nil && e.rig.Name != "" {
		routeClass, routed = e.routeRejectionToReworkBounce(mr, cause, result.Error, result.NeedsRework)
	}

	// Step 3: Close the MR when the router did not already do so. When
	// routed==true, step 2's `gt mq reject` -> Manager.RejectMR already
	// closed the MR bead with a rich "rejected: <classification reasonText>"
	// reason; closing again here would (a) emit a spurious "failed to close
	// MR" warning because the bead is already terminal, and (b) risk
	// overwriting that richer close reason with a bare "rejected". When
	// routed==false (no router configured, exec failure, or ambiguous
	// classification) the MR was not closed by the router, so we close it
	// here to preserve the prior terminal-rejection behavior.
	if !routed && mr.ID != "" && e.beads != nil {
		if err := e.beads.CloseWithReason("rejected", mr.ID); err != nil {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to close MR %s as rejected: %v\n", mr.ID, err)
		} else {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Closed MR %s as rejected-needs-rework\n", mr.ID)
		}
	}

	// Step 4: Nudge the worker with the classification-aware message. This
	// is the single source of truth for what the worker should do next --
	// the `gt mq reject` shell-out in step 2 deliberately omits --notify so
	// Manager.notifyWorkerRejected does not fire a contradictory generic
	// "resubmit" nudge ahead of this one.
	polecatName := strings.TrimPrefix(mr.Worker, "polecats/")
	nudgeTarget := fmt.Sprintf("%s/%s", e.rig.Name, polecatName)
	var nudgeMsg string
	switch routeClass {
	case reworkRouteReviewerHold:
		nudgeMsg = fmt.Sprintf("REVIEW_UNAVAILABLE_HOLD: branch=%s issue=%s cause=%s error=%s — reviewer tooling/cap deferral; do NOT resubmit until reviewer availability changes",
			mr.Branch, mr.SourceIssue, cause, result.Error)
	case reworkRouteAmbiguous:
		nudgeMsg = fmt.Sprintf("REWORK_ROUTE_AMBIGUOUS: branch=%s issue=%s cause=%s error=%s — both peer-review and cap markers observed (or neither); routing deferred to Mayor for human classification. Hold resubmit.",
			mr.Branch, mr.SourceIssue, cause, result.Error)
	default:
		// Routine NEEDS_REWORK (or unset when no router is configured):
		// revise-and-resubmit guidance.
		nudgeMsg = fmt.Sprintf("REVIEWER_REJECTED: branch=%s issue=%s cause=%s error=%s — revise and resubmit with 'gt done'",
			mr.Branch, mr.SourceIssue, cause, result.Error)
	}
	if e.reviewerRejectionWorkerNudge != nil {
		if err := e.reviewerRejectionWorkerNudge(nudgeTarget, nudgeMsg); err != nil {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to nudge %s about reviewer rejection: %v\n", polecatName, err)
		} else {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Nudged %s about reviewer rejection (routeClass=%s)\n", polecatName, routeClass)
		}
	} else {
		nudgeCmd := exec.Command("gt", "nudge", nudgeTarget, nudgeMsg)
		util.SetDetachedProcessGroup(nudgeCmd)
		nudgeCmd.Dir = e.workDir
		if err := nudgeCmd.Run(); err != nil {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to nudge %s about reviewer rejection: %v\n", polecatName, err)
		} else {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Nudged %s about reviewer rejection (routeClass=%s)\n", polecatName, routeClass)
		}
	}

	// Step 5: Only escalate to Mayor when the rework-bounce router could
	// not route the rejection (routed=false). Routed cases are owned by
	// the scoped rework-bounce runner and Witness from this point on;
	// routine NEEDS_REWORK does not need Mayor intervention. Unrouted
	// cases include: no router configured (legacy rigs), ambiguous
	// classification, and router exec failure.
	if !routed {
		mayorMsg := fmt.Sprintf("REVIEWER_REJECTED: %s issue=%s branch=%s cause=%s routeClass=%s routed=%t",
			mr.ID, mr.SourceIssue, mr.Branch, cause, routeClass, routed)
		if e.reviewerRejectionMayorNudge != nil {
			if err := e.reviewerRejectionMayorNudge(mayorMsg); err != nil {
				_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to nudge mayor about reviewer rejection: %v\n", err)
			}
		} else {
			mayorCmd := exec.Command("gt", "nudge", "mayor/", mayorMsg)
			util.SetDetachedProcessGroup(mayorCmd)
			mayorCmd.Dir = e.workDir
			if err := mayorCmd.Run(); err != nil {
				_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to nudge mayor about reviewer rejection: %v\n", err)
			}
		}
	}
}

// routeRejectionToReworkBounce invokes `gt mq reject` on the still-open MR
// so the dropin rework-bounce router (gt-mq-reject-rework-router.py) can
// classify the rejection, write a bounded rework packet, and schedule the
// scoped rework-bounce runner. The reason text is shaped so the router's
// peer-review content classifier matches (the historical "Codex-failed
// commit" rejections from gastown-wisp-ehs / gastown-wisp-wvl returned
// route_reason=not_apply_conflict because the rejection text had no
// classifier-matching keywords), and it preserves actionable reviewer
// details (concrete blockers and the affected branch) where practical.
//
// Review-tooling/cap-deferral cases are flagged with the
// REVIEW_UNAVAILABLE_HOLD prefix so the router classifies them separately
// as reviewer unavailable, not as source-code rework. Ambiguous cases
// (both peer-review failure markers AND cap markers observed, or neither
// matched) return REWORK_ROUTE_AMBIGUOUS so the caller can escalate to
// Mayor for human judgment.
//
// The shell call is bounded by DefaultReworkRouterTimeout and uses the
// cancellation process-group helper (util.SetProcessGroup) so a timed-out
// gt/router and any forked children are killed at the process-group level
// on deadline. SetDetachedProcessGroup only sets the Setpgid bit but does
// not install a Cancel function, so context timeout would leave orphaned
// gt/router subprocess trees.
//
// gastown-p3w (wus rejection): `gt mq reject` is invoked WITHOUT --notify.
// Manager.notifyWorkerRejected sends a hardcoded "revise and resubmit with
// 'gt done'" nudge that contradicts the classification-aware nudge emitted
// by handleReviewerRejection (especially for REVIEW_UNAVAILABLE_HOLD, where
// the worker must NOT be told to resubmit). handleReviewerRejection is the
// single source of truth for what the worker should do next.
func (e *Engineer) routeRejectionToReworkBounce(mr *MRInfo, cause, errMsg string, isPeerReviewRejection bool) (classification string, routed bool) {
	if mr == nil || mr.ID == "" {
		return "", false
	}
	routerMode := os.Getenv("GT_MQ_REWORK_ROUTER")
	if routerMode == "" {
		// No router mode configured -- skip silently so the path is a
		// no-op for rigs that have not opted in. This preserves the prior
		// behavior for environments that have not enabled the
		// rework-bounce pipeline.
		return "", false
	}

	classification, reasonText := reworkBounceReason(mr, cause, errMsg, isPeerReviewRejection)
	_, _ = fmt.Fprintf(e.output, "[Engineer] routing %s to rework-bounce: class=%s\n", mr.ID, classification)

	timeout := e.reworkRouterTimeout
	if timeout == 0 {
		timeout = DefaultReworkRouterTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := []string{"mq", "reject", e.rig.Name, mr.ID, "--reason", reasonText}
	runExec := func(ctx context.Context, args ...string) error {
		// Production: shell out to `gt mq reject` WITHOUT --notify. The
		// --notify flag would cause Manager.notifyWorkerRejected to fire a
		// hardcoded "resubmit" nudge ahead of the classification-aware
		// nudge emitted by handleReviewerRejection step 4. See the function
		// doc for the ordering contract.
		cmd := exec.CommandContext(ctx, "gt", args...)
		util.SetProcessGroup(cmd)
		cmd.Dir = e.workDir
		return cmd.Run()
	}
	if e.routeRejectionExec != nil {
		// Test seam: invoke the fake exec to assert argument shape without
		// shelling out.
		runExec = e.routeRejectionExec
	}
	if err := runExec(ctx, args...); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: rework-bounce router call for %s timed out after %s (process group killed)\n", mr.ID, timeout)
		} else {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: rework-bounce router call failed for %s: %v\n", mr.ID, err)
		}
		return classification, false
	}
	return classification, classification != reworkRouteAmbiguous
}

// reworkBounceReason shapes a router-friendly rejection reason from the
// engineer's cause/error pair and the caller's NeedsRework signal. The
// router's peer-review content classifier looks for tokens like
// "peer-fail", "codex/m3/umans-kimi failed", "blockers:", or
// "verdict:fail". Review-tooling/cap-deferral cases are flagged with
// "REVIEW_UNAVAILABLE_HOLD" so the router's reviewer-cap classifier
// (no-verdict, reviewers unavailable) can route them separately and avoid
// triggering a source-code rework packet for a tooling failure.
//
// Classification order is deliberate and fixes the gastown-p3w (wus
// rejection) Codex BLOCKING finding that cap markers short-circuited
// before peer-review markers were considered:
//  1. If BOTH a peer-review signal (peer-review markers in the haystack OR
//     the caller asserts isPeerReviewRejection) AND a cap marker are
//     observed, classify as REWORK_ROUTE_AMBIGUOUS -- the rejection is
//     unsafe to classify automatically and must escalate to Mayor. It must
//     NOT close the MR as a hold, mark it routed, or tell the worker not
//     to resubmit while concrete peer-review blockers also exist.
//  2. Else if a peer-review signal is observed (markers OR
//     isPeerReviewRejection), classify as NEEDS_REWORK_PEER_REVIEW.
//  3. Else if a cap marker is observed (and no peer-review signal),
//     classify as REVIEW_UNAVAILABLE_HOLD -- a pure tooling/cap failure,
//     not source code; the worker must not resubmit until reviewer
//     availability changes.
//  4. Else classify as REWORK_ROUTE_AMBIGUOUS so the caller can escalate
//     to Mayor for human judgment.
//
// gastown-p3w (Codex finding #1): the haystack normalizes underscores AND
// hyphens to spaces so documented hyphenated tooling-failure markers such
// as 'no-verdict', 'reviewer-unavailable', and 'cap-deferral' are
// recognized in addition to the underscore forms. capMarkers is kept in
// space-separated form because the haystack is normalized to spaces.
//
// gastown-p3w (wus rejection): isPeerReviewRejection is honored so concrete
// reviewer blockers with non-conforming CauseKey values (e.g.
// "race_condition", "missing_test", "api_break" from the historical
// review.go CauseKey assignments) default to NEEDS_REWORK_PEER_REVIEW
// instead of being misrouted. Per the NeedsRework field contract on
// ProcessResult, NeedsRework=true means a reviewer explicitly rejected with
// concrete blockers, so it is by definition a substantive peer-review
// rejection.
//
// The returned classification is for human/log visibility and to drive
// the worker nudge wording in handleReviewerRejection. The returned
// reasonText is the actual --reason argument the router will see; where
// practical it preserves the reviewer's concrete blocker text and the
// affected branch so the generated rework packet is actionable for the
// worker.
func reworkBounceReason(mr *MRInfo, cause, errMsg string, isPeerReviewRejection bool) (classification, reasonText string) {
	// Normalize both cause and errMsg into a single haystack. Underscores
	// AND hyphens are converted to spaces so hyphenated markers
	// (no-verdict, reviewer-unavailable, cap-deferral) match alongside the
	// underscore forms. Without hyphen normalization, these markers fall
	// through into NEEDS_REWORK_PEER_REVIEW and produce bogus source-code
	// rework packets for reviewer/cap issues.
	haystack := strings.ToLower(cause + " " + errMsg)
	haystack = strings.NewReplacer("_", " ", "-", " ").Replace(haystack)

	branch := ""
	if mr != nil {
		branch = mr.Branch
	}

	// Reviewer unavailable / cap deferral / no-verdict / quorum -- NOT
	// source code rework. The markers are stored in their normalized
	// (space-separated) form so the haystack's normalized form matches
	// both underscore and hyphen inputs from the upstream cause/errMsg.
	capMarkers := []string{
		"reviewer unavailable",
		"reviewers unavailable",
		"no verdict",
		"insufficient quorum",
		"capped",
		"cap deferral",
		"hook decision: defer",
		"deferred",
	}
	capMatch := matchAnyMarker(haystack, capMarkers)

	// Peer-review failure markers -- substantive reviewer verdicts with
	// actionable blockers. isPeerReviewRejection (NeedsRework=true by
	// field contract) is also a peer-review signal so that generic
	// CauseKey values like "race_condition" / "missing_test" route to
	// NEEDS_REWORK rather than AMBIGUOUS.
	peerMarkers := []string{
		"peer fail",
		"peer review fail",
		"verdict fail",
		"verdict:fail",
		"verdict: fail",
		"verdict=fail",
		"concrete blockers",
		"blockers:",
		"codex failed",
		"m3 failed",
		"umans kimi failed",
		"umans glm failed",
		"codex return fail",
		"return fail",
	}
	peerMatch := matchAnyMarker(haystack, peerMarkers) || isPeerReviewRejection

	// gastown-p3w (wus rejection Codex BLOCKING finding): mixed signals --
	// both a peer-review signal AND a cap marker -- are unsafe to classify
	// automatically. They must escalate to Mayor as AMBIGUOUS, NOT be
	// collapsed into a REVIEW_UNAVAILABLE_HOLD (which would close the MR,
	// mark it routed, suppress Mayor escalation, and tell the worker not to
	// resubmit while concrete peer-review blockers also exist). This check
	// comes BEFORE the pure peer-review and pure cap branches.
	if peerMatch && capMatch {
		return reworkRouteAmbiguous, fmt.Sprintf(
			"REWORK_ROUTE_AMBIGUOUS: mixed peer-review and cap/reviewer-unavailable markers observed "+
				"(cause=%s error=%q branch=%s isPeerReviewRejection=%t). "+
				"Cannot classify safely; escalating to Mayor for human judgment. Do not resubmit until classified.",
			cause, errMsg, branch, isPeerReviewRejection)
	}

	if peerMatch {
		reviewer := strings.NewReplacer("_", " ").Replace(cause)
		return reworkRouteNeedsRework, fmt.Sprintf(
			"NEEDS_REWORK_PEER_REVIEW: peer-fail concrete blockers: cause=%s. %s failed; "+
				"reviewer verdict=fail with actionable content blockers. error=%q branch=%s. "+
				"This is a routine NEEDS_REWORK, not a review-tooling failure. "+
				"Route to bounded rework packet and invoke gt-scoped-rework-bounce-runner.",
			cause, reviewer, errMsg, branch)
	}

	if capMatch {
		return reworkRouteReviewerHold, fmt.Sprintf(
			"REVIEW_UNAVAILABLE_HOLD: reviewers unavailable/no-verdict (cause=%s error=%q branch=%s). "+
				"This is a tooling/cap deferral, not a source-code rework. "+
				"Do not resubmit the same commit until reviewer availability changes.",
			cause, errMsg, branch)
	}

	// Neither matched and the caller did not assert isPeerReviewRejection.
	// We cannot tell whether this is a substantive peer-review failure
	// (in which case the cause/errMsg text is missing the documented
	// markers AND NeedsRework was not set, which is unexpected) or a
	// tooling failure. Escalate to Mayor for human classification.
	return reworkRouteAmbiguous, fmt.Sprintf(
		"REWORK_ROUTE_AMBIGUOUS: no recognized peer-review failure markers or cap markers in rejection "+
			"(cause=%s error=%q branch=%s isPeerReviewRejection=%t). "+
			"Cannot classify safely; escalating to Mayor for human judgment.",
		cause, errMsg, branch, isPeerReviewRejection)
}

// matchAnyMarker returns true if haystack contains any of the markers as a
// substring. The markers are already in their normalized (space-separated)
// form; haystack is the lowercased, underscore+hyphen-normalized rejection
// text from reworkBounceReason. Substring matching is sufficient because
// the markers are word-or-phrase forms that do not overlap each other.
func matchAnyMarker(haystack string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(haystack, marker) {
			return true
		}
	}
	return false
}

// HandleMRInfoFailure handles a failed merge from MRInfo.
// For conflicts, creates a resolution task and blocks the MR until resolved.
// For slot timeouts, the MR stays in queue for automatic retry without notifying polecats.
// This enables non-blocking delegation: the queue continues to the next MR.
func (e *Engineer) HandleMRInfoFailure(mr *MRInfo, result ProcessResult) {
	// Slot timeout is transient infrastructure contention — not a build/test/conflict failure.
	// The MR stays in queue and will be retried on the next poll cycle.
	// No polecat notification needed since there's nothing for a worker to fix.
	if result.SlotTimeout {
		_, _ = fmt.Fprintf(e.output, "[Engineer] ✗ Slot timeout: %s - %s\n", mr.ID, result.Error)
		_, _ = fmt.Fprintln(e.output, "[Engineer] MR remains in queue for automatic retry (slot contention)")
		return
	}

	// No-merge is intentional — the source issue has no_merge=true. Not a failure.
	// No polecat or mayor notification needed; the MR is simply dequeued.
	if result.NoMerge {
		_, _ = fmt.Fprintf(e.output, "[Engineer] MR %s: no_merge flag set on source issue, dequeued\n", mr.ID)
		return
	}

	// NeedsApproval: PR exists but lacks required approving review (merge_strategy=pr).
	// Not a failure — the MR stays in queue and will be retried on the next poll.
	// No polecat notification needed; the PR just needs a human review on GitHub.
	if result.NeedsApproval {
		_, _ = fmt.Fprintf(e.output, "[Engineer] MR %s: PR awaiting human approval, will retry next poll\n", mr.ID)
		return
	}

	// NeedsRework: a reviewer explicitly rejected the change with concrete
	// blockers. Close the MR as rejected-needs-rework and notify the worker.
	if result.NeedsRework {
		e.handleReviewerRejection(mr, result)
		return
	}

	// Branch-not-found: the remote branch doesn't exist. This can mean either
	// the branch was cleanly cherry-picked to target, OR the polecat's work was
	// lost (e.g., worktree in /tmp wiped by reboot before gt done pushed).
	// Escalate to mayor so lost work can be re-dispatched (gas-556).
	if result.BranchNotFound {
		_, _ = fmt.Fprintf(e.output, "[Engineer] MR %s: branch %s not found on remote — escalating to mayor (possible work loss)\n", mr.ID, mr.Branch)
		mayorMsg := fmt.Sprintf("BRANCH_MISSING: MR %s branch=%s issue=%s worker=%s — branch not on origin, work may be lost; re-dispatch if needed",
			mr.ID, mr.Branch, mr.SourceIssue, mr.Worker)
		mayorCmd := exec.Command("gt", "nudge", "mayor/", mayorMsg)
		mayorCmd.Dir = e.workDir
		if err := mayorCmd.Run(); err != nil {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to nudge mayor about missing branch: %v\n", err)
		}
		return
	}

	// Nudge polecat directly about the merge failure.
	// Previously sent MERGE_FAILED mail to witness (which relayed to polecat),
	// but that created permanent Dolt commits for routine protocol signals.
	// The witness discovers merge failures from MR bead status during patrol.
	failureType := "build"
	if result.Conflict {
		failureType = "conflict"
	} else if result.TestsFailed {
		failureType = "tests"
	}

	// gastown-wjk: stamp the terminal failure outcome so the report can
	// separate deterministic_validation failures from implementation_quality
	// from infra noise. Failure class is a coarse summary used for the
	// excluded_infra count and for distinguishing substantive FAIL counts
	// from reviewer_unavailable / timeout / infra_noise.
	telemetryFailureClass := "implementation_quality"
	switch {
	case result.Conflict:
		telemetryFailureClass = "merge_conflict"
	case result.TestsFailed:
		telemetryFailureClass = "deterministic_validation"
	case result.NeedsRework:
		telemetryFailureClass = "implementation_quality"
	case result.ConventionFailed:
		telemetryFailureClass = "convention_violation"
	}
	e.recordFinalOutcome(mr.ID, "rejected", telemetryFailureClass,
		time.Time{}, time.Now().UTC(), "", "")

	polecatName := strings.TrimPrefix(mr.Worker, "polecats/")
	nudgeTarget := fmt.Sprintf("%s/%s", e.rig.Name, polecatName)
	nudgeMsg := fmt.Sprintf("MERGE_FAILED: branch=%s issue=%s type=%s error=%s — fix and resubmit with 'gt done'",
		mr.Branch, mr.SourceIssue, failureType, result.Error)
	nudgeCmd := exec.Command("gt", "nudge", nudgeTarget, nudgeMsg)
	util.SetDetachedProcessGroup(nudgeCmd)
	nudgeCmd.Dir = e.workDir
	if err := nudgeCmd.Run(); err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to nudge %s about merge failure: %v\n", polecatName, err)
	} else {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Nudged %s about merge failure (%s)\n", polecatName, failureType)
	}

	// Nudge mayor about merge failure so dispatcher can unblock or reassign
	// dependent work immediately. Mirrors the success nudge in HandleMRInfoSuccess.
	mayorMsg := fmt.Sprintf("MERGE_FAILED: %s issue=%s branch=%s type=%s", mr.ID, mr.SourceIssue, mr.Branch, failureType)
	mayorCmd := exec.Command("gt", "nudge", "mayor/", mayorMsg)
	util.SetDetachedProcessGroup(mayorCmd)
	mayorCmd.Dir = e.workDir
	if err := mayorCmd.Run(); err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to nudge mayor about merge failure: %v\n", err)
	}

	// If this was a conflict, create a conflict-resolution task for dispatch
	// and block the MR until the task is resolved (non-blocking delegation)
	if result.Conflict {
		retryCount := mr.RetryCount + 1
		conflictSHA, revErr := e.git.Rev("origin/" + mr.Target)
		if revErr != nil {
			conflictSHA = "unknown-sha"
		}
		taskID, err := e.createConflictResolutionTaskForMR(mr, result)
		if err != nil {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to create conflict resolution task: %v\n", err)
		} else if taskID != "" {
			// Block the MR on the conflict resolution task using beads dependency
			// When the task closes, the MR unblocks and re-enters the ready queue
			if err := e.beads.AddDependency(mr.ID, taskID); err != nil {
				_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to block MR on task: %v\n", err)
			} else {
				if err := e.recordConflictTaskOnMR(mr, taskID, retryCount, conflictSHA); err != nil {
					_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to record conflict task on MR %s: %v\n", mr.ID, err)
				} else {
					mr.ConflictTaskID = taskID
					mr.RetryCount = retryCount
				}
				_, _ = fmt.Fprintf(e.output, "[Engineer] MR %s blocked on conflict task %s (non-blocking delegation)\n", mr.ID, taskID)
			}
		}
	}

	// Log the failure - MR stays in queue but may be blocked
	_, _ = fmt.Fprintf(e.output, "[Engineer] ✗ Failed: %s - %s\n", mr.ID, result.Error)
	if mr.BlockedBy != "" {
		_, _ = fmt.Fprintln(e.output, "[Engineer] MR blocked pending conflict resolution - queue continues to next MR")
	} else {
		_, _ = fmt.Fprintln(e.output, "[Engineer] MR remains in queue for retry")
	}
}

// createConflictResolutionTaskForMR creates a dispatchable task for resolving merge conflicts.
// This task will be picked up by bd ready and can be slung to a fresh polecat (spawned on demand).
// Returns the created task's ID for blocking the MR until resolution.
//
// Task format:
//
//	Title: Resolve merge conflicts: <original-issue-title>
//	Type: task
//	Priority: inherit from original (ZFC: agent decides boost strategy)
//	Parent: original MR bead
//	Description: metadata including branch, conflict SHA, etc.
//
// Merge Slot Integration:
// Before creating a conflict resolution task, we acquire the merge-slot for this rig.
// This serializes conflict resolution - only one polecat can resolve conflicts at a time.
// If the slot is already held, we skip creating the task and let the MR stay in queue.
// When the current resolution completes and merges, the slot is released.
func (e *Engineer) createConflictResolutionTaskForMR(mr *MRInfo, _ ProcessResult) (string, error) { // result unused but kept for future merge diagnostics
	// === MERGE SLOT GATE: Serialize conflict resolution ===
	// Ensure merge slot exists (idempotent)
	slotID, err := e.mergeSlotEnsureExists()
	slotHolder := "" // tracks acquired slot for cleanup on error
	if err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: could not ensure merge slot: %v\n", err)
		// Continue anyway - slot is optional for now
	} else {
		// Try to acquire the merge slot
		holder := e.rig.Name + "/refinery"
		status, err := e.mergeSlotAcquire(holder, false)
		if err != nil {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: could not acquire merge slot: %v\n", err)
			// Continue anyway - slot is optional
		} else if status == nil {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: merge slot returned nil status\n")
			// Continue anyway - slot is optional
		} else if !status.Available && status.Holder != "" && status.Holder != holder {
			// Slot is held by someone else - skip creating the task
			// The MR stays in queue and will retry when slot is released
			_, _ = fmt.Fprintf(e.output, "[Engineer] Merge slot held by %s - deferring conflict resolution\n", status.Holder)
			_, _ = fmt.Fprintf(e.output, "[Engineer] MR %s will retry after current resolution completes\n", mr.ID)
			return "", nil // Not an error - just deferred
		} else {
			slotHolder = holder
			_, _ = fmt.Fprintf(e.output, "[Engineer] Acquired merge slot: %s\n", slotID)
		}
	}
	// Release slot on error to prevent permanent blockage
	releaseSlotOnError := func() {
		if slotHolder != "" {
			_ = e.mergeSlotRelease(slotHolder)
		}
	}

	// Get the current main SHA for conflict tracking
	mainSHA, err := e.git.Rev("origin/" + mr.Target)
	if err != nil {
		mainSHA = "unknown-sha"
	}

	// Get the original issue title if we have a source issue
	originalTitle := mr.SourceIssue
	if mr.SourceIssue != "" {
		if sourceIssue, err := e.beads.Show(mr.SourceIssue); err == nil && sourceIssue != nil {
			originalTitle = sourceIssue.Title
		}
	}

	// ZFC: pass raw priority. Agent decides boost strategy.

	// Increment retry count for tracking
	retryCount := mr.RetryCount + 1

	// Build the task description with metadata
	description := fmt.Sprintf(`Resolve merge conflicts for branch %s

## Metadata
- Original MR: %s
- Branch: %s
- Conflict with: %s@%s
- Original issue: %s
- Retry count: %d

## Instructions
1. Check out the branch: git checkout %s
2. Rebase onto target: git rebase origin/%s
3. Resolve conflicts in your editor
4. Complete the rebase: git add . && git rebase --continue
5. Force-push the resolved branch: git push -f
6. Close this task: bd close <this-task-id>

The Refinery will automatically retry the merge after you force-push.`,
		mr.Branch,
		mr.ID,
		mr.Branch,
		mr.Target, shortSHA(mainSHA),
		mr.SourceIssue,
		retryCount,
		mr.Branch,
		mr.Target,
	)

	// Create the conflict resolution task
	taskTitle := fmt.Sprintf("Resolve merge conflicts: %s", originalTitle)
	task, err := e.beads.Create(beads.CreateOptions{
		Title:       taskTitle,
		Labels:      []string{"gt:task"},
		Priority:    mr.Priority,
		Description: description,
		Actor:       e.rig.Name + "/refinery",
		Rig:         e.rig.Name, // Ensure task lands in the rig's database (gt-7y7)
	})
	if err != nil {
		releaseSlotOnError()
		return "", fmt.Errorf("creating conflict resolution task: %w", err)
	}

	// gt-gpy: Validate task bead landed in the rig's database (warning only).
	townRoot := filepath.Dir(e.rig.Path)
	if prefixErr := beads.ValidateRigPrefix(townRoot, e.rig.Name, task.ID); prefixErr != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] WARNING: conflict task prefix mismatch: %v\n", prefixErr)
	}

	// The conflict task's ID is returned so the MR can be blocked on it.
	// When the task closes, the MR unblocks and re-enters the ready queue.

	_, _ = fmt.Fprintf(e.output, "[Engineer] Created conflict resolution task: %s (P%d)\n", task.ID, task.Priority)

	return task.ID, nil
}

func (e *Engineer) recordConflictTaskOnMR(mr *MRInfo, taskID string, retryCount int, conflictSHA string) error {
	mrBead, err := e.beads.Show(mr.ID)
	if err != nil {
		return err
	}
	mrFields := beads.ParseMRFields(mrBead)
	if mrFields == nil {
		mrFields = &beads.MRFields{}
	}
	mrFields.ConflictTaskID = taskID
	mrFields.RetryCount = retryCount
	mrFields.LastConflictSHA = conflictSHA
	newDesc := beads.SetMRFields(mrBead, mrFields)
	return e.beads.Update(mr.ID, beads.UpdateOptions{Description: &newDesc})
}

// closeSupersededConflictArtifacts closes conflict-resolution tasks made moot
// by a successful land of the source issue (hq-jnap). Two cases:
//  1. The merged MR's own conflict task is still open — the conflict was
//     resolved out-of-band (force-push) without `bd close`, so the task rots.
//  2. Another open MR carries the same source issue (a re-land) — its conflict
//     task is now pointless because the content is on the target branch.
//
// Superseded sibling MRs are closed only when their conflict task verifies it
// belongs to that MR/source issue; this avoids unblocking stale duplicate MRs.
// All operations are best-effort; failures are logged and don't affect the merge.
func (e *Engineer) closeSupersededConflictArtifacts(merged *MRInfo) {
	e.closeConflictTaskIfOpen(conflictTaskIDForMR(merged), merged.ID, merged.ID, merged.SourceIssue)

	if merged.SourceIssue == "" {
		return
	}
	all, err := e.ListAllOpenMRs()
	if err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: conflict-artifact sweep skipped (list MRs): %v\n", err)
		return
	}
	for _, other := range all {
		if other.ID == merged.ID || other.SourceIssue != merged.SourceIssue {
			continue
		}
		if !e.closeConflictTaskIfOpen(conflictTaskIDForMR(other), other.ID, merged.ID, merged.SourceIssue) {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Note: open MR %s shares source issue %s just merged via %s, but had no verified conflict task to close\n",
				other.ID, merged.SourceIssue, merged.ID)
			continue
		}
		reason := fmt.Sprintf("superseded by %s", merged.ID)
		if err := e.beads.CloseWithReason(reason, other.ID); err != nil {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to close superseded MR %s: %v\n", other.ID, err)
		} else {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Closed superseded MR %s: %s\n", other.ID, reason)
		}
	}
}

func conflictTaskIDForMR(mr *MRInfo) string {
	if mr == nil {
		return ""
	}
	if mr.ConflictTaskID != "" {
		return mr.ConflictTaskID
	}
	return mr.BlockedBy
}

// closeConflictTaskIfOpen closes a conflict-resolution task if it is still open.
func (e *Engineer) closeConflictTaskIfOpen(taskID, taskMRID, landedMRID, sourceIssue string) bool {
	if taskID == "" {
		return false
	}
	task, err := e.beads.Show(taskID)
	if err != nil || task == nil {
		return false
	}
	if !isConflictTaskForMR(task, taskMRID, sourceIssue) {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: refusing to close unverified conflict task %s for MR %s\n", taskID, taskMRID)
		return false
	}
	if task.Status == string(beads.StatusClosed) {
		return true
	}
	reason := fmt.Sprintf("conflict moot: %s landed (MR %s)", sourceIssue, landedMRID)
	if err := e.beads.CloseWithReason(reason, taskID); err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to close moot conflict task %s: %v\n", taskID, err)
		return false
	} else {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Closed moot conflict task: %s (%s)\n", taskID, reason)
	}
	return true
}

func isConflictTaskForMR(task *beads.Issue, mrID, sourceIssue string) bool {
	if task == nil || task.Description == "" || mrID == "" {
		return false
	}
	metadata := conflictTaskMetadata(task.Description)
	if metadata["Original MR"] != mrID {
		return false
	}
	return sourceIssue == "" || metadata["Original issue"] == sourceIssue
}

func conflictTaskMetadata(description string) map[string]string {
	metadata := make(map[string]string)
	for _, line := range strings.Split(description, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			metadata[key] = value
		}
	}
	return metadata
}

// IsBeadOpen checks if a bead is still open (not closed).
// This is used as a status checker to filter blocked MRs.
func (e *Engineer) IsBeadOpen(beadID string) (bool, error) {
	issue, err := e.beads.Show(beadID)
	if err != nil {
		// If we can't find the bead, treat as not open (fail open - allow MR to proceed)
		return false, nil
	}
	// "closed" status means the bead is done
	return issue.Status != "closed", nil
}

// issueToMRInfo converts a beads issue (with parsed MR fields) into an MRInfo.
// Shared by ListReadyMRs, ListBlockedMRs, and ListAllOpenMRs.
func issueToMRInfo(issue *beads.Issue, fields *beads.MRFields) *MRInfo {
	// Parse convoy created_at if present
	var convoyCreatedAt *time.Time
	if fields.ConvoyCreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, fields.ConvoyCreatedAt); err == nil {
			convoyCreatedAt = &t
		}
	}

	// Parse issue timestamps
	var createdAt, updatedAt time.Time
	if issue.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, issue.CreatedAt); err == nil {
			createdAt = t
		}
	}
	if issue.UpdatedAt != "" {
		if t, err := time.Parse(time.RFC3339, issue.UpdatedAt); err == nil {
			updatedAt = t
		}
	}

	// Parse pre-verification timestamp if present
	var preVerifiedAt time.Time
	if fields.PreVerifiedAt != "" {
		if t, err := time.Parse(time.RFC3339, fields.PreVerifiedAt); err == nil {
			preVerifiedAt = t
		}
	}

	return &MRInfo{
		ID:              issue.ID,
		Branch:          fields.Branch,
		Target:          fields.Target,
		SourceIssue:     fields.SourceIssue,
		Worker:          fields.Worker,
		Rig:             fields.Rig,
		Title:           issue.Title,
		Priority:        issue.Priority,
		AgentBead:       fields.AgentBead,
		RetryCount:      fields.RetryCount,
		ConflictTaskID:  fields.ConflictTaskID,
		ConvoyID:        fields.ConvoyID,
		ConvoyCreatedAt: convoyCreatedAt,
		PreVerified:     fields.PreVerified,
		PreVerifiedAt:   preVerifiedAt,
		PreVerifiedBase: fields.PreVerifiedBase,
		CreatedAt:       createdAt,
		UpdatedAt:       updatedAt,
		Assignee:        issue.Assignee,
	}
}

// firstOpenBlocker returns the ID of the first open blocker for an issue,
// or empty string if none are open.
func (e *Engineer) firstOpenBlocker(issue *beads.Issue) string {
	for _, blockerID := range issue.BlockedBy {
		isOpen, err := e.IsBeadOpen(blockerID)
		if err == nil && isOpen {
			return blockerID
		}
	}
	return ""
}

// ListReadyMRs returns MRs that are ready for processing:
// - Not claimed by another worker (checked via assignee field)
// - Not blocked by an open task (checked via firstOpenBlocker)
// Sorted by priority (highest first).
//
// Uses bd list instead of bd ready because MRs are ephemeral beads and
// bd ready filters out ephemeral issues (see gt-t5t6y). This matches the
// pattern used by ListBlockedMRs and ListAllOpenMRs.
func (e *Engineer) ListReadyMRs() ([]*MRInfo, error) {
	// Query beads for all open merge-request issues.
	// Cannot use ReadyWithType here because bd ready excludes ephemeral beads,
	// and MRs are ephemeral by design. Use List + manual blocker check instead.
	issues, err := e.beads.ListMergeRequests(beads.ListOptions{
		Status:   "open",
		Label:    "gt:merge-request",
		Priority: -1, // No priority filter
	})
	if err != nil {
		return nil, fmt.Errorf("querying beads for merge-requests: %w", err)
	}

	// Convert beads issues to MRInfo
	var mrs []*MRInfo
	for _, issue := range issues {
		// Skip closed MRs (workaround for bd list not respecting --status filter)
		if issue.Status != "open" {
			continue
		}

		// Skip blocked MRs (replaces bd ready's blocker filtering)
		if blockedBy := e.firstOpenBlocker(issue); blockedBy != "" {
			continue
		}

		// Belt-and-suspenders: skip MRs labeled gt:owned-direct.
		// These MRs shouldn't exist (gt done skips MR creation for owned+direct
		// convoys), but if one slips through, the refinery should not process it.
		if beads.HasLabel(issue, "gt:owned-direct") {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Skipping MR %s: owned+direct convoy (belt-and-suspenders)\n", issue.ID)
			continue
		}

		fields := beads.ParseMRFields(issue)
		if fields == nil {
			continue // Skip issues without MR fields
		}

		// Filter by rig — wisps are shared across all rigs (GH#2718).
		if fields.Rig != "" && !strings.EqualFold(fields.Rig, e.rig.Name) {
			continue
		}

		// Skip if already assigned, unless claim is stale (allows re-claim after crash).
		// NOTE: Only one refinery runs per rig (enforced by ErrAlreadyRunning in
		// manager.go), so concurrent re-claim race conditions are not a concern.
		if issue.Assignee != "" {
			stale, parseErr := isClaimStale(issue.UpdatedAt, e.config.StaleClaimTimeout)
			if parseErr != nil {
				_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: could not parse UpdatedAt for %s: %v (treating claim as valid)\n",
					issue.ID, parseErr)
			}
			if !stale {
				continue
			}
			_, _ = fmt.Fprintf(e.output, "[Engineer] Stale claim detected: %s (assignee: %s, updated: %s) — eligible for re-claim\n",
				issue.ID, issue.Assignee, issue.UpdatedAt)
		}

		mrs = append(mrs, issueToMRInfo(issue, fields))
	}

	return mrs, nil
}

// ListBlockedMRs returns MRs that are blocked by open tasks.
// Useful for monitoring/reporting.
//
// This queries beads for blocked merge-request issues.
func (e *Engineer) ListBlockedMRs() ([]*MRInfo, error) {
	// Query all merge-request issues (both ready and blocked)
	issues, err := e.beads.ListMergeRequests(beads.ListOptions{
		Status:   "open",
		Label:    "gt:merge-request",
		Priority: -1, // No priority filter
	})
	if err != nil {
		return nil, fmt.Errorf("querying beads for merge-requests: %w", err)
	}

	// Filter for blocked issues (those with open blockers)
	var mrs []*MRInfo
	for _, issue := range issues {
		// Skip if not blocked
		if len(issue.BlockedBy) == 0 {
			continue
		}

		// Check if any blocker is still open
		blockedBy := e.firstOpenBlocker(issue)
		if blockedBy == "" {
			continue // All blockers are closed, not blocked
		}

		fields := beads.ParseMRFields(issue)
		if fields == nil {
			continue
		}

		// Filter by rig — wisps are shared across all rigs (GH#2718).
		if fields.Rig != "" && !strings.EqualFold(fields.Rig, e.rig.Name) {
			continue
		}

		mr := issueToMRInfo(issue, fields)
		mr.BlockedBy = blockedBy
		mrs = append(mrs, mr)
	}

	return mrs, nil
}

// ListAllOpenMRs returns all open merge requests with full raw data.
// Unlike ListReadyMRs/ListBlockedMRs, this performs no filtering — it returns
// claimed, unclaimed, blocked, and unblocked MRs. It also checks branch existence
// so agents can detect orphaned MRs. Designed for agent-side queue health analysis
// (ZFC: Go transports data, agent decides what's interesting).
func (e *Engineer) ListAllOpenMRs() ([]*MRInfo, error) {
	issues, err := e.beads.ListMergeRequests(beads.ListOptions{
		Status:   "open",
		Label:    "gt:merge-request",
		Priority: -1,
	})
	if err != nil {
		return nil, fmt.Errorf("querying beads for merge-requests: %w", err)
	}

	var mrs []*MRInfo
	for _, issue := range issues {
		if issue.Status != "open" {
			continue
		}

		fields := beads.ParseMRFields(issue)
		if fields == nil {
			continue
		}

		// Filter by rig — wisps are shared across all rigs (GH#2718).
		if fields.Rig != "" && !strings.EqualFold(fields.Rig, e.rig.Name) {
			continue
		}

		mr := issueToMRInfo(issue, fields)

		// Check branch existence (local + remote tracking refs)
		mr.BranchExistsLocal, _ = e.git.BranchExists(fields.Branch)
		mr.BranchExistsRemote, _ = e.git.RemoteTrackingBranchExists("origin", fields.Branch)
		mr.BlockedBy = e.firstOpenBlocker(issue)

		mrs = append(mrs, mr)
	}

	return mrs, nil
}

// ListQueueAnomalies finds stale claims and orphaned branches in open MRs.
// This gives Witness/Refinery patrols deterministic signals for deadlock risk.
func (e *Engineer) ListQueueAnomalies(now time.Time) ([]*MRAnomaly, error) {
	issues, err := e.beads.ListMergeRequests(beads.ListOptions{
		Status:   "open",
		Label:    "gt:merge-request",
		Priority: -1,
	})
	if err != nil {
		return nil, fmt.Errorf("querying beads for merge-requests: %w", err)
	}

	// Filter by rig — wisps are shared across all rigs (GH#2718).
	filtered := make([]*beads.Issue, 0, len(issues))
	for _, issue := range issues {
		fields := beads.ParseMRFields(issue)
		if fields != nil && fields.Rig != "" && !strings.EqualFold(fields.Rig, e.rig.Name) {
			continue
		}
		filtered = append(filtered, issue)
	}

	return detectQueueAnomalies(filtered, now, e.config.StaleClaimWarningAfter, func(branch string) (bool, bool, error) {
		localExists, err := e.git.BranchExists(branch)
		if err != nil {
			return false, false, err
		}
		remoteTrackingExists, err := e.git.RemoteTrackingBranchExists("origin", branch)
		if err != nil {
			return false, false, err
		}
		return localExists, remoteTrackingExists, nil
	}), nil
}

func detectQueueAnomalies(
	issues []*beads.Issue,
	now time.Time,
	warningAfter time.Duration,
	branchExistsFn func(branch string) (localExists bool, remoteTrackingExists bool, err error),
) []*MRAnomaly {
	var anomalies []*MRAnomaly

	for _, issue := range issues {
		if issue == nil || issue.Status != "open" {
			continue
		}
		fields := beads.ParseMRFields(issue)
		if fields == nil || fields.Branch == "" {
			continue
		}

		// 1) Stale claim detection.
		if issue.Assignee != "" {
			updatedAt, err := time.Parse(time.RFC3339, issue.UpdatedAt)
			if err == nil {
				age := now.Sub(updatedAt)
				if age >= warningAfter {
					anomalies = append(anomalies, &MRAnomaly{
						ID:       issue.ID,
						Branch:   fields.Branch,
						Type:     "stale-claim",
						Assignee: issue.Assignee,
						Age:      age,
						Detail:   "MR is claimed but not progressing",
					})
				}
			}
		}

		// 2) Orphaned branch detection.
		// ZFC: report raw anomaly data. Agent decides severity.
		localExists, remoteTrackingExists, err := branchExistsFn(fields.Branch)
		if err == nil && !localExists && !remoteTrackingExists {
			anomalies = append(anomalies, &MRAnomaly{
				ID:     issue.ID,
				Branch: fields.Branch,
				Type:   "orphaned-branch",
				Detail: "MR branch is missing locally and in origin/* tracking refs",
			})
		}
	}

	return anomalies
}

// ClaimMR claims an MR for processing by setting the assignee field.
// This replaces mrqueue.Claim() for beads-based MRs.
// The workerID is typically the refinery's identifier (e.g., "gastown/refinery").
func (e *Engineer) ClaimMR(mrID, workerID string) error {
	return e.beads.Update(mrID, beads.UpdateOptions{
		Assignee: &workerID,
	})
}

// ReleaseMR releases a claimed MR back to the queue by clearing the assignee.
// This replaces mrqueue.Release() for beads-based MRs.
func (e *Engineer) ReleaseMR(mrID string) error {
	empty := ""
	return e.beads.Update(mrID, beads.UpdateOptions{
		Assignee: &empty,
	})
}

// postMergeConvoyCheck runs convoy completion checks after a successful merge.
//
// When a source issue is closed by a merge, any convoy tracking that issue may
// now be complete (all tracked issues closed). This method:
//  1. Runs `gt convoy check` to auto-close completed convoys and notify subscribers
//  2. For completed convoys with integration branches (swarms), triggers landing
//  3. Cleans up stale polecat branches from completed work
//
// All operations are best-effort: failures are logged but don't affect merge success.
func (e *Engineer) postMergeConvoyCheck(mr *MRInfo) {
	// Find town root from rig path (rig is at ~/gt/<rigname>, town is ~/gt)
	townRoot := filepath.Dir(e.rig.Path)
	townBeads := filepath.Join(townRoot, ".beads")

	// Quick check: does town-level beads exist?
	if _, err := os.Stat(townBeads); os.IsNotExist(err) {
		return
	}

	// Step 1: Run `gt convoy check` to auto-close completed convoys.
	// This handles cross-rig convoy completion: convoys in town beads (hq-*)
	// tracking issues in rig beads (gt-*) won't auto-close via bd close alone.
	closedConvoys := e.checkAndCloseCompletedConvoys(townRoot, townBeads)

	// Step 2: For each closed convoy, check if it has a swarm with an
	// integration branch that needs landing.
	for _, convoy := range closedConvoys {
		e.landConvoySwarm(townRoot, convoy)
	}

	// Step 3: Notify deacon of convoy-eligible merges for immediate feeding.
	// When the merged MR is part of a convoy, send a structured CONVOY_NEEDS_FEEDING
	// protocol message so the deacon can immediately feed the next ready issue
	// instead of waiting for the next patrol cycle (up to 10 minutes).
	e.notifyDeaconConvoyFeeding(mr)

	// Step 4: Clean up stale branches from completed work.
	// Prune remote tracking refs that no longer exist on origin.
	if e.config.DeleteMergedBranches {
		e.pruneStaleRemoteRefs()
	}
}

// notifyDeaconConvoyFeeding sends a CONVOY_NEEDS_FEEDING protocol message to
// the deacon when the merged MR is part of a convoy. This triggers immediate
// convoy feeding instead of waiting for the next deacon patrol cycle (up to
// 10 minutes). An event is also emitted to wake the deacon from await-signal.
func (e *Engineer) notifyDeaconConvoyFeeding(mr *MRInfo) {
	if mr.ConvoyID == "" {
		return
	}

	// Nudge deacon about convoy feeding instead of sending permanent mail.
	// The deacon discovers convoy state from beads on next patrol cycle;
	// this nudge just accelerates discovery.
	nudgeMsg := fmt.Sprintf("CONVOY_NEEDS_FEEDING: convoy=%s issue=%s", mr.ConvoyID, mr.SourceIssue)
	nudgeCmd := exec.Command("gt", "nudge", "deacon", nudgeMsg)
	util.SetDetachedProcessGroup(nudgeCmd)
	nudgeCmd.Dir = e.workDir
	if err := nudgeCmd.Run(); err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to nudge deacon about convoy feeding for %s: %v\n", mr.ConvoyID, err)
	} else {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Nudged deacon: CONVOY_NEEDS_FEEDING %s\n", mr.ConvoyID)
	}

	// Emit event to wake deacon from await-signal.
	_ = events.LogFeed(events.TypeMail, e.rig.Name+"/refinery", events.MailPayload("deacon/", "CONVOY_NEEDS_FEEDING "+mr.ConvoyID))
}

// convoyInfo holds minimal info about a closed convoy for post-merge processing.
type convoyInfo struct {
	ID          string
	Title       string
	Description string
}

func refineryHasLabel(labels []string, target string) bool {
	for _, label := range labels {
		if label == target {
			return true
		}
	}
	return false
}

// checkAndCloseCompletedConvoys finds and closes convoys where all tracked issues
// are complete. Returns the list of convoys that were closed.
func (e *Engineer) checkAndCloseCompletedConvoys(townRoot, townBeads string) []convoyInfo {
	townReadEnv := beads.BuildReadOnlyPinnedBDEnv(os.Environ(), townBeads)
	townMutationEnv := beads.BuildMutationPinnedBDEnv(os.Environ(), townBeads)
	routingReadEnv := beads.BuildReadOnlyRoutingBDEnv(os.Environ(), townBeads)

	// List all open issues and filter locally so legacy type=convoy beads remain visible.
	listArgs := beads.InjectFlatForListJSON([]string{"list", "--status=open", "--json", "--limit=0"})
	listArgs = beads.MaybePrependAllowStaleWithEnv(townReadEnv, listArgs)
	listCmd := beads.Command(townBeads, townBeads, beads.ReadOnlyPinned, listArgs...)
	var stdout bytes.Buffer
	listCmd.Stdout = &stdout

	if err := listCmd.Run(); err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to list convoys: %v\n", err)
		return nil
	}

	var convoys []struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Status      string   `json:"status"`
		Description string   `json:"description"`
		IssueType   string   `json:"issue_type"`
		Labels      []string `json:"labels"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &convoys); err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to parse convoy list: %v\n", err)
		return nil
	}

	var closed []convoyInfo

	for _, convoy := range convoys {
		if convoy.IssueType != "convoy" && !refineryHasLabel(convoy.Labels, "gt:convoy") {
			continue
		}
		// Get tracked issues for this convoy via bd dep list
		depArgs := beads.MaybePrependAllowStaleWithEnv(townReadEnv, []string{"dep", "list", convoy.ID, "--direction=down", "--type=tracks", "--json"})
		depCmd := beads.Command(townRoot, townBeads, beads.ReadOnlyPinned, depArgs...)
		var depOut bytes.Buffer
		depCmd.Stdout = &depOut

		if err := depCmd.Run(); err != nil {
			continue
		}

		var deps []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(depOut.Bytes(), &deps); err != nil {
			continue
		}

		// Refresh statuses from home rigs (cross-rig lookup)
		allClosed := true
		for _, dep := range deps {
			// Unwrap external:prefix:id format
			depID := dep.ID
			if strings.HasPrefix(depID, "external:") {
				parts := strings.SplitN(depID, ":", 3)
				if len(parts) == 3 {
					depID = parts[2]
				}
			}

			// Get fresh status from home rig via bd show with routing
			showArgs := beads.MaybePrependAllowStaleWithEnv(routingReadEnv, []string{"show", depID, "--json"})
			showCmd := beads.Command(townRoot, townBeads, beads.ReadOnlyRouting, showArgs...)
			var showOut bytes.Buffer
			showCmd.Stdout = &showOut

			if err := showCmd.Run(); err != nil || showOut.Len() == 0 {
				// Can't verify - treat as open to be safe
				allClosed = false
				break
			}

			var issues []struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(showOut.Bytes(), &issues); err != nil || len(issues) == 0 {
				allClosed = false
				break
			}

			if issues[0].Status != "closed" && issues[0].Status != "tombstone" {
				allClosed = false
				break
			}
		}

		if !allClosed {
			continue
		}

		// All tracked issues are complete - close the convoy
		reason := "All tracked issues completed"
		if len(deps) == 0 {
			reason = "Empty convoy — auto-closed as definitionally complete"
		}

		closeArgs := beads.MaybePrependAllowStaleWithEnv(townMutationEnv, []string{"close", convoy.ID, "-r", reason})
		closeCmd := beads.Command(townBeads, townBeads, beads.MutationPinned, closeArgs...)

		if err := closeCmd.Run(); err != nil {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to close convoy %s: %v\n", convoy.ID, err)
			continue
		}

		_, _ = fmt.Fprintf(e.output, "[Engineer] Auto-closed convoy %s: %s\n", convoy.ID, convoy.Title)
		closed = append(closed, convoyInfo{
			ID:          convoy.ID,
			Title:       convoy.Title,
			Description: convoy.Description,
		})

		// Send convoy completion notifications (owner + notify addresses)
		e.notifyConvoyCompletion(townRoot, convoy.ID, convoy.Title, convoy.Description)
	}

	return closed
}

// notifyConvoyCompletion sends notifications to convoy owner and notify addresses.
func (e *Engineer) notifyConvoyCompletion(townRoot, convoyID, title, description string) {
	fields, shouldNotify := e.claimConvoyCompletionNotification(townRoot, convoyID, description)
	if !shouldNotify {
		return
	}
	for _, addr := range fields.NotificationAddresses() {
		mailCmd := exec.Command("gt", "mail", "send", addr,
			"-s", fmt.Sprintf("🚚 Convoy landed: %s", title),
			"-m", fmt.Sprintf("Convoy %s has completed.\n\nAll tracked issues are now closed.\n\nClosed by: %s/refinery", convoyID, e.rig.Name))
		util.SetDetachedProcessGroup(mailCmd)
		mailCmd.Dir = townRoot
		if err := mailCmd.Run(); err != nil {
			_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: could not notify %s: %v\n", addr, err)
		}
	}
}

func (e *Engineer) claimConvoyCompletionNotification(townRoot, convoyID, fallbackDescription string) (*beads.ConvoyFields, bool) {
	townBeads := filepath.Join(townRoot, ".beads")
	description := fallbackDescription

	readEnv := beads.BuildReadOnlyPinnedBDEnv(os.Environ(), townBeads)
	showArgs := beads.MaybePrependAllowStaleWithEnv(readEnv, []string{"show", convoyID, "--json"})
	showCmd := beads.Command(townBeads, townBeads, beads.ReadOnlyPinned, showArgs...)
	var showOut bytes.Buffer
	showCmd.Stdout = &showOut
	if err := showCmd.Run(); err == nil && showOut.Len() > 0 {
		var convoys []struct {
			Description string `json:"description"`
		}
		if err := json.Unmarshal(showOut.Bytes(), &convoys); err == nil && len(convoys) > 0 {
			description = convoys[0].Description
		}
	}

	fields := beads.ParseConvoyFields(&beads.Issue{Description: description})
	if fields == nil {
		fields = &beads.ConvoyFields{}
	}
	if fields.CompletionNotifiedAt != "" {
		return fields, false
	}

	fields.CompletionNotifiedAt = time.Now().UTC().Format(time.RFC3339)
	newDesc := beads.SetConvoyFields(&beads.Issue{Description: description}, fields)
	mutationEnv := beads.BuildMutationPinnedBDEnv(os.Environ(), townBeads)
	updateArgs := beads.MaybePrependAllowStaleWithEnv(mutationEnv, []string{"update", convoyID, "--description=" + newDesc})
	updateCmd := beads.Command(townBeads, townBeads, beads.MutationPinned, updateArgs...)
	if err := updateCmd.Run(); err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: could not record convoy completion notification state for %s: %v\n", convoyID, err)
		return fields, false
	}

	return fields, true
}

// landConvoySwarm checks if a completed convoy has an associated swarm with an
// integration branch, and triggers landing if so.
func (e *Engineer) landConvoySwarm(townRoot string, convoy convoyInfo) {
	// ZFC: Use typed accessor instead of parsing description text
	fields := beads.ParseConvoyFields(&beads.Issue{Description: convoy.Description})
	var moleculeID string
	if fields != nil {
		moleculeID = fields.Molecule
	}

	if moleculeID == "" {
		return // No swarm/molecule associated with this convoy
	}

	// Check if the molecule has an integration branch (swarm/* pattern)
	integrationBranch := fmt.Sprintf("swarm/%s", moleculeID)
	branchExists, err := e.git.BranchExists(integrationBranch)
	if err != nil || !branchExists {
		// Also check remote
		remoteExists, _ := e.git.RemoteTrackingBranchExists("origin", integrationBranch)
		if !remoteExists {
			return // No integration branch to land
		}
	}

	_, _ = fmt.Fprintf(e.output, "[Engineer] Landing integration branch %s for convoy %s...\n", integrationBranch, convoy.ID)

	// Use gt swarm land to perform the landing
	landCmd := exec.Command("gt", "swarm", "land", moleculeID)
	util.SetDetachedProcessGroup(landCmd)
	landCmd.Dir = townRoot
	var landOut, landErr bytes.Buffer
	landCmd.Stdout = &landOut
	landCmd.Stderr = &landErr

	if err := landCmd.Run(); err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to land swarm %s: %v (%s)\n",
			moleculeID, err, strings.TrimSpace(landErr.String()))
		return
	}

	_, _ = fmt.Fprintf(e.output, "[Engineer] ✓ Landed integration branch for convoy %s\n", convoy.ID)
}

// pruneStaleRemoteRefs prunes remote tracking refs that no longer exist on origin.
// This cleans up refs from branches that were deleted on the remote after merge.
func (e *Engineer) pruneStaleRemoteRefs() {
	if err := e.git.FetchPrune("origin"); err != nil {
		_, _ = fmt.Fprintf(e.output, "[Engineer] Warning: failed to prune stale remote refs: %v\n", err)
	}
}
