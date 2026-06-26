# Refinery Review Semantics

This document describes how the refinery classifies PR reviews and when a merge
can proceed under degraded quorum because some reviewers are unavailable or
produced no verdict.

## Background

When `merge_queue.merge_strategy` is `"pr"` and `require_review` is true, the
refinery must decide whether a PR is safe to merge based on GitHub or Bitbucket
review state. The historical bug treated any missing or empty review as a
content failure, blocking merges indefinitely and creating false rejections.

## Reviewer Verdicts

The refinery classifies each review probe into exactly one of these verdicts:

| Verdict | Meaning | Typical cause |
|---------|---------|---------------|
| `PASS` | Explicit approving review. | Reviewer clicked "Approve". |
| `FAIL` | Explicit rejection with concrete blockers. | Reviewer requested changes or listed blocking issues. |
| `NO_VERDICT` | No actionable review output. | No reviews, only comments, or an unrecognized state. |
| `UNAVAILABLE` | The review source could not be reached. | Provider error, rate limit, reviewer account capped/disabled. |

`NO_VERDICT` and `UNAVAILABLE` are **not** treated as content `FAIL`. They are
non-terminal states that may be handled by degraded quorum.

**Empty-review guard (gastown-cet.12.4):** a `PASS` verdict on a known-empty
diff (zero changes between base and head) is reclassified as a `FAIL` with
cause key `empty_diff_degenerate_pass`. A reviewer that produces zero
findings on a zero-content diff performed no actual review, so the verdict
must not authoritatively approve the merge. The durable review gate also
refuses to run the reviewer command at all when the merge-candidate diff is
empty, so a degenerate `PASS` cannot lead to an HMAC attestation that would
later be treated as evidence of approval.

## Decision Rules

The refinery applies these rules in order:

1. **Any `FAIL` with concrete blockers rejects the merge.** This preserves the
   existing behavior where requested changes with evidence block the MR.
2. **All reviews `PASS` with no missing reviewers:** merge normally.
3. **Degraded quorum is enabled, the minimum number of independent `PASS`
   reviews is met, and no required reviewer has `NO_VERDICT`:** the merge may
   proceed. Missing or unavailable reviewers are recorded as an audit obligation.
4. **Otherwise:** the MR stays in the queue for retry (no false failure
   notification).

## Degraded Quorum Configuration

Add these keys to `merge_queue` in the rig's `config.json` (or
`.gastown/settings.json`):

```json
{
  "merge_queue": {
    "merge_strategy": "pr",
    "vcs_provider": "github",
    "require_review": true,
    "degraded_quorum_enabled": true,
    "review_quorum_min": 1
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `degraded_quorum_enabled` | `*bool` | `false` | Allow merges when some reviewers are unavailable/no-verdict. |
| `review_quorum_min` | `int` | `1` | Minimum independent approving reviews required to satisfy degraded quorum. Required reviewers may only be skipped when they are `UNAVAILABLE`. |

## Audit Obligations

When a merge proceeds under degraded quorum, the refinery creates a follow-up
audit bead labeled `gt:audit` and `gt:task`. The audit records:

* the MR and source issue;
* which reviewers were unavailable or had no verdict;
* how many PASS reviews justified the merge.

The source issue close reason also includes `review_state: degraded_quorum`
and `audit_bead: <id>` for traceability.

## Backward Compatibility

* Without `degraded_quorum_enabled`, behavior is unchanged: missing or
  unavailable reviewers block the merge queue entry but do not produce a false
  `FAIL` notification.
* `require_review` still defaults to `false`.
* Existing `require_review` configs continue to work.

## Core Multi-Model Quorum (source-controlled runtime gate)

The live multi-model runtime gate (`gastown-spike/dropin/refinery-gate.sh`)
enforces a strict core reviewer quorum. That rule is **source-controlled** in
`internal/refinery/review.go` via `EvaluateCoreReviewerQuorum` +
`CoreReviewerQuorum` so it is durable and tested, not just a property of the
dropin script. The two implementations agree.

**Core reviewers:** `m3`, `codex`, `umans-kimi`, `umans-glm` (the
`CoreMultiModelReviewers` set).

**Rules:**

1. **Writer exclusion.** If the writer is a known core reviewer, it is the
   *only* reviewer excluded; the merge requires **all remaining** core reviewers
   to return `PASS` (`peer-review:3/3`). If the writer is unknown, the merge
   requires **all four** core reviewers to return `PASS` (`peer-review:4/4`).
   Implementer-style writer ids (`codex-impl`) normalize to the canonical id
   (`codex`) so a writer never reviews its own diff.
2. **Any parsed `FAIL` rejects.** A `FAIL` from any consulted reviewer (core or
   Opus) takes precedence over unavailability — a real rejection is never masked
   by a peer being down (`mixed FAIL + UNAVAILABLE -> REJECT`).
3. **Unavailable core defers.** Any selected core reviewer that is
   `UNAVAILABLE` or `NO_VERDICT` **defers** the merge (non-zero, no attestation)
   and is recorded in `AuditReviewers` so a re-audit bead can be filed. It
   never merges under incomplete core coverage.
4. **Opus verify.** Opus runs after the core panel: an Opus `FAIL` **rejects**;
   Opus `UNAVAILABLE`/`NO_VERDICT` files an audit bead but does **not** block a
   merge once the core panel has fully `PASS`ed (merge under
   `ReviewStateDegradedQuorum`).

**Telemetry.** `EvaluateCoreReviewerQuorum` returns a `ReviewEvaluation`
recording the fields the runtime gate emits: `PassCount` (peer_passes),
`UnavailableCount`, `NoVerdictCount`, plus `ExpectedPeerCount()` and the
`PeerReviewPhase()`/`OpusStatus()` tokens (`peer-review:4/4`, `peer-review:3/3`).

The acceptance tests in `review_test.go` (`TestCoreQuorum_*`) cover: unknown
writer requires all four; known `m3`/`codex` writers require the other three;
`codex-impl` normalization; one unavailable core defers with no attestation; one
core `NO_VERDICT` defers; one core `FAIL` rejects; mixed `FAIL`+`UNAVAILABLE`
rejects (FAIL precedence); Opus unavailable after core PASS merges with audit;
Opus `FAIL` rejects; explicit Opus PASS merges; and telemetry shape.
