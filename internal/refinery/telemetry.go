package refinery

import (
	"context"
	"time"

	"github.com/steveyegge/gastown/internal/refinery/mrtelemetry"
)

// TelemetryRecorder returns the per-attempt telemetry recorder, or nil if
// telemetry is disabled (e.g. town root could not be resolved). Callers (the
// CLI) use this to read historical records and compute summaries. Nil-safe.
func (e *Engineer) TelemetryRecorder() *mrtelemetry.Recorder {
	if e == nil {
		return nil
	}
	return e.telemetry
}

// This file holds the per-attempt telemetry instrumentation for gastown-wjk.
// All entry points are nil-safe (a nil e.telemetry or e.currentAttempt makes
// the call a no-op) and never return errors into the merge path. Telemetry is
// strictly best-effort: it must not break merges.

// beginTelemetryAttempt starts a telemetry record for an MR processing attempt.
// Writer attribution is captured HERE, at claim/processing start, so a model
// reassignment mid-flight does not retrobably change the recorded writer of an
// already-submitted attempt (the attribution invariant). The attempt number is
// 1 + the count of prior terminal telemetry attempts for the same source bead.
//
// The handle is stored on e.currentAttempt for accumulation by the gate/review
// instrumentation and finalization by the terminal handlers. Nil-safe.
func (e *Engineer) beginTelemetryAttempt(mr *MRInfo, submittedAt time.Time) {
	if e == nil || e.telemetry == nil || mr == nil {
		return
	}
	writer := e.durableReviewWriter(mr)
	attempt := e.telemetryAttemptNumber(mr.SourceIssue)
	e.currentAttempt = e.telemetry.BeginAttempt(mrtelemetry.AttemptStart{
		Rig:         e.rigName(),
		SourceBead:  mr.SourceIssue,
		MRID:        mr.ID,
		Attempt:     attempt,
		Polecat:     mr.Worker,
		WriterModel: writer,
		Branch:      mr.Branch,
		CommitSHA:   "",
		TreeSHA:     "",
		SubmittedAt: submittedAt,
	})
}

// telemetryAttemptNumber returns 1 + the count of prior terminal telemetry
// records for the same source bead. When no prior records exist (or telemetry
// is disabled, or the source bead is empty), returns 1. This makes attempt#
// durable across re-dispatches of the same bead even if the in-memory state is
// lost, because the JSONL history is the source of truth.
func (e *Engineer) telemetryAttemptNumber(sourceBead string) int {
	if e == nil || e.telemetry == nil || sourceBead == "" {
		return 1
	}
	files, err := e.telemetry.Files(time.Time{})
	if err != nil || len(files) == 0 {
		return 1
	}
	count := 0
	for _, rec := range readTelemetryRecords(files) {
		if rec.SourceBead == sourceBead && rec.FinalGateDecision != "" {
			count++
		}
	}
	return count + 1
}

// rigName returns the rig name or "" if unset.
func (e *Engineer) rigName() string {
	if e == nil || e.rig == nil {
		return ""
	}
	return e.rig.Name
}

// recordTelemetryValidation records the deterministic-gate (validation) phase
// start/finish and verdict for the current attempt. Nil-safe.
func (e *Engineer) recordTelemetryValidation(started, finished time.Time, verdict string) {
	if e == nil || e.currentAttempt == nil {
		return
	}
	e.currentAttempt.SetValidation(started, finished, verdict)
}

// recordTelemetryCodexReview records the codex/durable-review phase start/finish
// and verdict for the current attempt. Nil-safe.
func (e *Engineer) recordTelemetryCodexReview(started, finished time.Time, verdict string) {
	if e == nil || e.currentAttempt == nil {
		return
	}
	e.currentAttempt.SetCodexReview(started, finished, verdict)
}

// recordTelemetryCommit records the commit/tree SHAs for the current attempt
// once known (after the merge commit is created). Nil-safe.
func (e *Engineer) recordTelemetryCommit(commitSHA, treeSHA string) {
	if e == nil || e.currentAttempt == nil {
		return
	}
	e.currentAttempt.SetCommit(commitSHA, treeSHA)
}

// finalizeTelemetryMerged finalizes the current attempt as a successful merge.
// Nil-safe and idempotent (a second finalization is a no-op).
func (e *Engineer) finalizeTelemetryMerged(mergeCommit string) {
	if e == nil || e.currentAttempt == nil {
		return
	}
	if mergeCommit != "" {
		e.currentAttempt.SetCommit(mergeCommit, "")
	}
	e.currentAttempt.Finalize(mrtelemetry.DecisionMerged, mrtelemetry.FailureClassNone, time.Now())
	e.currentAttempt = nil
}

// finalizeTelemetryFailure finalizes the current attempt as a failure/rejection,
// deriving the failure class from the ProcessResult flags. Nil-safe and
// idempotent.
func (e *Engineer) finalizeTelemetryFailure(result ProcessResult) {
	if e == nil || e.currentAttempt == nil {
		return
	}
	decision, failureClass := classifyTerminalFailure(result)
	codexVerdict := codexVerdictFromResult(result)
	if codexVerdict != "" {
		e.currentAttempt.SetCodexVerdict(codexVerdict)
	}
	e.currentAttempt.Finalize(decision, failureClass, time.Now())
	e.currentAttempt = nil
}

// classifyTerminalFailure maps a ProcessResult to a (finalGateDecision,
// failureClass) pair. The decision is the authoritative terminal state the
// refinery acted on; the failureClass is the bounded taxonomy used to separate
// substantive implementation failures from deterministic validation,
// reviewer unavailability, timeouts, infra, and convention issues.
func classifyTerminalFailure(result ProcessResult) (decision, failureClass string) {
	switch {
	case result.NeedsRework:
		// A reviewer explicitly rejected with concrete blockers. The codex
		// verdict is FAIL — this is the substantive signal the Mayor wants to
		// see separated from infra noise.
		return mrtelemetry.DecisionRejected, mrtelemetry.FailureClassSubstantiveImpl
	case result.SlotTimeout:
		return mrtelemetry.DecisionSlotTimeout, mrtelemetry.FailureClassTimeout
	case result.ConventionFailed:
		return mrtelemetry.DecisionFailed, mrtelemetry.FailureClassConvention
	case result.Conflict:
		return mrtelemetry.DecisionFailed, mrtelemetry.FailureClassConflict
	case result.TestsFailed:
		// Deterministic validation gate (build/test/lint/typecheck) failed
		// before the codex review gate. Distinct from a substantive codex FAIL.
		return mrtelemetry.DecisionFailed, mrtelemetry.FailureClassDeterministicValidation
	case result.BranchNotFound:
		return mrtelemetry.DecisionFailed, mrtelemetry.FailureClassInfra
	case result.NoMerge:
		return mrtelemetry.DecisionNoMerge, mrtelemetry.FailureClassNone
	case result.NeedsApproval:
		return mrtelemetry.DecisionNeedsApproval, mrtelemetry.FailureClassNone
	default:
		// Unspecified failure — treat as infra noise so it does not pollute
		// the substantive codex-FAIL signal.
		return mrtelemetry.DecisionFailed, mrtelemetry.FailureClassInfra
	}
}

// codexVerdictFromResult derives the codex verdict from the ProcessResult. A
// NeedsRework rejection is a codex FAIL (the reviewer returned concrete
// blockers). Otherwise the verdict is left unset (the codex review
// instrumentation records the actual verdict directly when the gate ran).
func codexVerdictFromResult(result ProcessResult) string {
	if result.NeedsRework {
		return mrtelemetry.CodexVerdictFail
	}
	return ""
}

// codexVerdictFromDurableGate derives the codex verdict from the durable review
// gate's ProcessResult and gate context. The mapping:
//   - Success                      -> PASS (attestation recorded)
//   - NeedsRework (reject-replay)   -> FAIL (unchanged tree rejected)
//   - Timeout (gateCtx deadline)   -> UNAVAILABLE (reviewer did not return)
//   - Other failure                 -> NO_VERDICT (gate ran but no verdict)
//
// This is used by the deferred recorder in runDurableReviewGate so every exit
// path records a finish timestamp + verdict without touching each return.
func codexVerdictFromDurableGate(result ProcessResult, gateCtx context.Context) string {
	if result.Success {
		return mrtelemetry.CodexVerdictPass
	}
	if result.NeedsRework {
		return mrtelemetry.CodexVerdictFail
	}
	if gateCtx != nil && gateCtx.Err() == context.DeadlineExceeded {
		return mrtelemetry.CodexVerdictUnavailable
	}
	return mrtelemetry.CodexVerdictNoVerdict
}

// readTelemetryRecords is a thin wrapper used by telemetryAttemptNumber to
// load all records from a set of files. Best-effort: unreadable files skipped.
func readTelemetryRecords(files []string) []mrtelemetry.AttemptRecord {
	var out []mrtelemetry.AttemptRecord
	for _, f := range files {
		recs, err := mrtelemetry.ReadFile(f)
		if err != nil {
			continue
		}
		out = append(out, recs...)
	}
	return out
}
