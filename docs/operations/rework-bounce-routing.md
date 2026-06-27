# Refinery Rework-Bounce Routing

> **Owners**: Witness (routine), Mayor (ambiguous / escalated)
> **Status**: Required reading for `gastown-p3w`-era rigs.
> **Source**: gastown-p3w, hq-yyz, hq-6na

When the Refinery rejects a merge request (peer-review FAIL, deterministic
gate failure, or a manually-flagged rework), the rejection must produce a
bounded rework packet and deterministically re-dispatch the polecat. Mayor
intervention is not required for the routine case.

This document describes the routing pipeline that does that, who owns which
step, and the recovery classifier that handles stale `active_mr` references.

## End-to-end flow

```
Refinery.Engineer.handleReviewerRejection(mr, result)
  │
  ├─ Persist CloseReason=rejected + TerminalState=rejected-needs-rework
  │     on the MR bead (gastown-p3w, hq-yyz, hq-6na)
  │
  ├─ [gastown-p3w] routeRejectionToReworkBounce(mr, cause, errMsg)
  │     │
  │     └─ Shells out to `gt mq reject <rig> <mr> --notify --reason <…>`
  │        which is intercepted by the gt wrapper when
  │        GT_MQ_REWORK_ROUTER=shadow|enforce.
  │        │
  │        └─ gt-mq-reject-rework-router.py classifies the reason:
  │           - NEEDS_REWORK_PEER_REVIEW  → write bounded packet
  │             → invoke gt-scoped-rework-bounce-runner.sh
  │           - REVIEW_UNAVAILABLE_HOLD  → nudge worker, hold for next
  │             reviewer-available window
  │           - not_apply_conflict       → legacy: log + nudge only
  │
  ├─ Nudge the worker with a `REVIEWER_REJECTED` block so they can act
  │     before the routed packet lands.
  │
  └─ Nudge Mayor only if the route fails or the classification is
     ambiguous. Routine NEEDS_REWORK does not escalate.
```

## Classification rules (gastown-p3w)

The Refinery shapes the rejection reason so the dropin router's classifiers
match. Two distinct outcomes:

### `NEEDS_REWORK_PEER_REVIEW` — routine

Triggered when `cause` / `errMsg` indicate a peer-review content failure:
`codex returned FAIL`, `m3 failed`, `umans-kimi failed`, `umans-glm failed`,
`peer-fail`, `concrete blockers:`, `blockers:`, etc.

The reason text the engineer produces includes the cause, a `peer-fail
concrete blockers:` marker, and a "verdict=fail" phrase. The router's
`is_peer_review_content_failure` predicate then returns `True` and the
rework-bounce runner is invoked with `--max-fixes 1`.

### `REVIEW_UNAVAILABLE_HOLD` — tooling failure (NOT source rework)

Triggered when `cause` / `errMsg` indicate a cap-deferral or
reviewer-unavailable case: `reviewer unavailable`, `no-verdict`, `insufficient
quorum`, `capped`, `cap deferral`, `hook decision: defer`, `deferred`.

The router classifies this as `review_unavailable_deferred_no_code_failure`
and the worker is told *not* to resubmit the same commit. A retry should
only happen after reviewer availability changes.

This separation is the second acceptance criterion of gastown-p3w: review
tooling failures must not be misclassified as source-code rework, otherwise
we burn a bounded packet on a non-fixable condition.

## Stale `active_mr` recovery (gastown-p3w / Jasper case)

The Jasper-like failure mode is a slot whose `active_mr` references an MR
bead no longer present in beads (`gt mq status` returns "MR not found")
while the source issue is still open. Without classification, the slot
sits indefinitely as a generic recovery-held capacity leak.

The `AssessActiveMR` predicate handles this in `internal/polecat/active_mr.go`:

| `active_mr` bead state        | `source_issue` state | Verdict             |
|-------------------------------|----------------------|---------------------|
| exists, terminal, close_reason in {rejected, conflict, superseded} | any | Reconcilable (rework) |
| **missing (bead deleted)**    | **verified open**    | **Reconcilable (rework)** — gastown-p3w |
| missing                       | unknown / missing / terminal | Source-terminal gate (fail-closed) |
| exists, status=open/in_progress| any                  | Pending (live MR)   |
| nil                           | n/a                  | (no assessment)     |

The rework-reconcilable classification is gated on positive evidence that
the source issue is verified open (looked up, present, non-terminal). An
unknown or terminal source stays fail-closed so the slot escalates to Mayor
with a precise blocker reason (`source_status=missing`,
`source_issue=<missing>`, etc.) rather than being silently recycled.

## Ownership

| Step                                              | Owner    |
|---------------------------------------------------|----------|
| Engineer emits rejection reason + classifier-marker | Refinery (auto) |
| Dropin router classifies + writes packet          | Refinery dropin (auto) |
| Scoped rework-bounce runner executes              | Refinery dropin (auto) |
| Routine NEEDS_REWORK re-dispatch                  | Witness  |
| Review-tooling failures (REVIEW_UNAVAILABLE_HOLD) | Witness (waits for reviewer window) |
| Stale `active_mr` reconcile                      | Witness  |
| Ambiguous / escalated cases                       | Mayor    |

Mayor involvement is only required when the classification is ambiguous
(reviewer-cap + concrete blockers both match) or when the recovery
classifier fails closed (unknown / missing source issue). The
`route_reason` field in `.runtime/mq-rework-router.jsonl` records which
path was taken.

## Operator checklist

When investigating "slot stuck as NEEDS_RECOVERY":

1. Run `gt polecat check-recovery <rig>/<slot> --json`.
2. If `recovery_guard.verdict == "CLEAR"` but the slot is still recovery-held,
   the active_mr assessment is the gate. Look at
   `status.active_mr` + the in-source `active_mr_assessment.Reason`.
3. If the reason mentions `rework-reconcilable` or matches the Jasper-like
   shape (`status=missing source_status=open`), the slot is safe to recycle
   for rework via `gt polecat check-recovery <rig>/<slot> --reconcile-cleanup`.
4. If the reason mentions `source_status=missing` or `source_issue=<missing>`,
   the source is unverifiable — escalate to Mayor with the JSON.

When investigating "MR rejected but no rework packet appeared":

1. Check `.runtime/mq-rework-router.jsonl` for the latest entry with this MR.
2. If `routed=false` and `route_reason=not_apply_conflict`, the legacy
   nudge-only path was taken. Set `GT_MQ_REWORK_ROUTER=shadow` (or
   `enforce` to actually bounce) and re-trigger the rejection.
3. If `route_reason=routed`, the packet was written under
   `.runtime/refinery-rework-prompts/<rig>/<source>/<ts>-<mr>.md` (or
   `…-peer-review.md`) and the runner was invoked. Inspect that file
   for the rework instructions.

## Acceptance criteria mapping

- **Routing fix**: `engineer.routeRejectionToReworkBounce` +
  `gt-mq-reject-rework-router.py` produce a packet and invoke
  `gt-scoped-rework-bounce-runner.sh --fix --max-fixes 1` when
  `GT_MQ_REWORK_ROUTER=enforce`. Regression:
  `engineer_rework_bounce_test.go::TestRouteRejectionToReworkBounce_*`.
- **Jasper case**: `polecat/active_mr.go::assessStaleActiveMR` classifies
  a missing MR + verified-open source as Reconcilable. Regression:
  `active_mr_test.go::TestAssessActiveMRReconcilableMissingMRWithOpenSource`.
- **Review-tooling separation**: `engineer.reworkBounceReason` returns
  `REVIEW_UNAVAILABLE_HOLD` for cap-deferral cases. Regression:
  `engineer_rework_bounce_test.go::TestReworkBounceReason_ReviewerUnavailableIsSeparateClassification`.

## See also

- `internal/polecat/active_mr.go` — reconcilable rework close + Jasper case
- `internal/refinery/engineer.go` — `handleReviewerRejection`,
  `routeRejectionToReworkBounce`, `reworkBounceReason`
- `internal/refinery/engineer_rework_bounce_test.go` — routing regression
- `internal/polecat/active_mr_test.go` — recovery classifier regression
- `docs/dolt-health-guide.md` — Dolt RCA capture for router-related
  outages (refinery or mq hangs)
