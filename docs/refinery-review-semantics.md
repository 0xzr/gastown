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
