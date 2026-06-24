# Closure Provenance Source-of-Truth Design

**Bead:** gastown-cet.4.1
**Workstream:** D (Closure Provenance and Data-Plane Alignment)
**Parent spec:** `/home/ubuntu/gt-town/mayor/implementation-reviews/gt-meta-hardening-spec-2026-06-24.md`
**Status:** Design (not yet implemented)

## Problem

Gas Town closes beads through at least nine writer paths (refinery, convoy,
reaper, gt done, sling, handoff, witness, user, rig-bootstrap). Today the
canonical `issues` table carries only:

| Column          | Type         | What it actually means                |
|-----------------|--------------|----------------------------------------|
| `status`        | VARCHAR(32)  | open/in_progress/blocked/deferred/closed |
| `closed_at`     | DATETIME     | wall-clock at close                    |
| `closed_by_session` | VARCHAR(255) | Claude Code session ID (NOT agent identity) |
| `close_reason`  | TEXT         | free-text, sometimes `"Merged in <id>\ntarget_branch: ...\ncommit_sha: ..."` |

Provenance is split across:

- `events.actor` (Dolt, separate table)
- `.beads/interactions.jsonl` (file-based audit log)
- description-text key/value pairs (escalation beads, MR beads)
- free-text `close_reason` conventions parsed at read time

This makes audit, dependency-readiness checks, and root-cause analysis
unreliable. The accepted invariant is: **a closed bead should carry enough
structured metadata to answer who closed it, why, and what artifact (if any)
proves the work landed — without free-text parsing.**

## Source of truth

**Dolt `issues` table is the canonical store.** All structured closure
provenance lives there. The JSON wire format (`bd show --json`) reflects SQL
state. The audit log (`.beads/interactions.jsonl`) is a durable backup, never
authoritative.

Lifting fields out of description-text into first-class columns is the
priority. Description-text provenance is parsing-fragile and silent on loss.

## Canonical schema additions

Six new columns on the `issues` table. Existing columns (`status`,
`closed_at`, `closed_by_session`, `close_reason`) stay — `close_reason`
becomes a human-readable summary, not the structured store.

### 1. `closed_by`

- **Type:** VARCHAR(255), NOT NULL, DEFAULT `'unknown'`
- **Semantics:** Identity of the actor that issued the close.
- **Format:**
  - Agents: `<rig>/<role>/<name>` — e.g. `gastown/refinery`, `gastown/polecats/quartz`, `gastown/witness`, `gastown/deacon`
  - Users: `user:<handle>` — e.g. `user:mayor`, `user:andrew`
  - Systems: `system:<component>` — e.g. `system:reaper`, `system:dolt-migration`
- **Writer ownership:** every close path. Populated from process context
  (e.g. `GT_ROLE`, `BD_SESSION_ID`, `USER`, or the calling function's
  `actor` parameter).

### 2. `closure_source`

- **Type:** VARCHAR(32), NOT NULL, DEFAULT `'unknown'`
- **Semantics:** Which subsystem initiated the close.
- **Allowed values (enum):**
  `refinery`, `convoy`, `reaper`, `polecat-done`, `sling`, `handoff`,
  `witness`, `user`, `escalation`, `rig-bootstrap`, `migration`,
  `unknown`.
- **Writer ownership:** every close path. The Go API surface gains an enum
  type `beads.ClosureSource` with `IsValid()` so non-conforming writes
  fail at compile/test time.

### 3. `closure_trigger`

- **Type:** VARCHAR(64), NOT NULL, DEFAULT `'unknown'`
- **Semantics:** Specific trigger event within the source.
- **Allowed values (enum, by source):**

  | Source         | Triggers                                                                |
  |----------------|-------------------------------------------------------------------------|
  | refinery       | `merged`, `rejected`, `superseded`, `conflict-moot`, `stacked-merged`   |
  | convoy         | `all-tracked-completed`, `empty-convoy`, `manual-force`                 |
  | reaper         | `stale-auto-close`, `stale-plugin-receipt`, `stale-plugin-dispatch`, `stale-orphan` |
  | polecat-done   | `mr-submitted`, `no-changes`, `no-merge`, `direct-merge`, `direct-merge-late` |
  | sling          | `force-resling`, `polecat-nuked`, `dog-session-failed`, `stale-dog-replaced`, `orphan-wisp-cleanup` |
  | handoff        | `superseded-by-session`, `molecule-handoff`, `orphan-molecule-recovery` |
  | witness        | `orphan-molecule`, `orphan-wisp`                                        |
  | user           | `manual-close`, `manual-cascade`, `convoy-force-close`                  |
  | escalation     | `escalation-resolved`                                                   |
  | rig-bootstrap  | `agent-removed`, `crew-removed`, `identity-removed`                     |
  | migration      | `backfill`, `schema-v2-migration`                                       |
  | unknown        | (catch-all, never trusted for analysis)                                 |

- **Writer ownership:** every close path.

### 4. `linked_mr`

- **Type:** VARCHAR(64), NULL
- **Semantics:** Bead ID of the merge request that caused this close, when
  the close is downstream of an MR (source issue closed by refinery, or MR
  superseded/rejected).
- **Writer ownership:** refinery merge path (source issue), refinery reject
  path (rejected MR and any superseded MRs), refinery supersede path,
  `gt done` default-MR path.

### 5. `merged_commit`

- **Type:** VARCHAR(64), NULL
- **Semantics:** SHA of the merge commit on the target branch.
- **Writer ownership:** refinery `PostMerge` (source issue), `gt done`
  direct-merge paths. Reuses `mr.MergeCommit` already captured by refinery.

### 6. `published_commit`

- **Type:** VARCHAR(64), NULL
- **Semantics:** SHA of the commit reachable on the upstream/published
  remote (e.g. GitHub `origin`) when the refinery verifies external
  publication. NULL when no upstream, or when publication verification
  failed (in which case the `close_reason` and `events` row carry the
  failure evidence).
- **Writer ownership:** refinery `PostMerge` after
  `VerifyPushedCommit(...)` succeeds.

### Out of scope (kept on existing surfaces)

- `status`, `closed_at`, `closed_by_session`, `close_reason`: unchanged.
  `close_reason` continues to carry human-readable prose (`"Merged in
  mr-xyz\ntarget_branch: main\ncommit_sha: abc123"`) for one release
  cycle after the migration lands, then parsing of the `"Merged in "`
  prefix is removed from dependency-readiness checks (the canonical
  check moves to `linked_mr`).
- `MRFields.TerminalState`, `MRFields.PublishedCommit`,
  `MRFields.PublishedRemote`, `MRFields.PublishedAt` (currently encoded
  in MR-bead description text via `internal/beads/fields.go:618-659`):
  these stay on MR beads as a denormalized cache, but the new
  `issues.published_commit` column becomes the primary source for source
  beads and other close paths. The MR-bead description continues to
  carry them for backward compat; new writers set both.

## Writer ownership matrix

Every close writer must populate all six canonical fields. `closed_by`,
`closure_source`, `closure_trigger` are mandatory; `linked_mr`,
`merged_commit`, `published_commit` are NULL by default and set when
applicable.

| Writer (file:line)                          | closed_by                | closure_source  | closure_trigger                  | linked_mr | merged_commit | published_commit |
|---------------------------------------------|--------------------------|-----------------|----------------------------------|-----------|---------------|------------------|
| refinery `Manager.PostMerge` (manager.go:599-653) | `gastown/refinery`       | `refinery`      | `merged`                         | yes (the MR) | yes (`mr.MergeCommit`) | yes if `VerifyPushedCommit` succeeds |
| refinery `Manager.RejectMR` (manager.go:555-588)   | `gastown/refinery` or `user:<name>` (whichever invoked) | `refinery` | `rejected` | yes (the MR) | NULL | NULL |
| refinery supersede path (engineer.go:1895-1919)   | `gastown/refinery`       | `refinery`      | `superseded`                     | yes (new MR) | NULL | NULL |
| refinery conflict-moot (engineer.go:1931-1955)    | `gastown/refinery`       | `refinery`      | `conflict-moot`                  | yes (landed MR) | NULL | NULL |
| refinery stacked-merged (engineer.go)             | `gastown/refinery`       | `refinery`      | `stacked-merged`                 | yes | yes | yes if verified |
| refinery convoy auto-close (engineer.go:2418-2542) | `gastown/refinery`       | `convoy`        | `all-tracked-completed`          | NULL | NULL | NULL |
| convoy manual close (cmd/convoy.go:1019-1153)     | `user:<name>` (operator) | `user`          | `convoy-force-close`             | NULL | NULL | NULL |
| convoy check / auto-close (cmd/convoy.go:890)     | `gastown/refinery` or `gastown/deacon` (depending on caller) | `convoy` | `all-tracked-completed` or `empty-convoy` | NULL | NULL | NULL |
| deacon empty-convoy (deacon/feed_stranded.go:260-279) | `gastown/deacon`         | `convoy`        | `empty-convoy`                   | NULL | NULL | NULL |
| reaper `AutoClose` (reaper.go:712-822)            | `system:reaper`          | `reaper`        | `stale-auto-close`               | NULL | NULL | NULL |
| reaper `ClosePluginReceipts` (reaper.go:922-988) | `system:reaper`          | `reaper`        | `stale-plugin-receipt`           | NULL | NULL | NULL |
| reaper `ClosePluginDispatches` (reaper.go:1011-1080) | `system:reaper`       | `reaper`        | `stale-plugin-dispatch`          | NULL | NULL | NULL |
| reaper `closeWispsInBatches` (reaper.go:445-547) | `system:reaper`          | `reaper`        | `stale-orphan`                   | NULL | NULL | NULL |
| `gt done` default-MR (cmd/done.go:1383-1401)      | `gastown/polecats/<name>` | `polecat-done` | `mr-submitted`                   | yes (new MR) | NULL | NULL |
| `gt done` no-changes (cmd/done.go:656-714)        | `gastown/polecats/<name>` | `polecat-done` | `no-changes`                     | NULL | NULL | NULL |
| `gt done` direct-merge (cmd/done.go:797-847)      | `gastown/polecats/<name>` | `polecat-done` | `direct-merge`                   | NULL | yes (pushed SHA) | yes if verified |
| `gt done` direct-merge late (cmd/done.go:1110-1157) | `gastown/polecats/<name>` | `polecat-done` | `direct-merge-late`           | NULL | yes | yes if verified |
| `gt done` no-merge (cmd/done.go:1063-1094)        | `gastown/polecats/<name>` | `polecat-done` | `no-merge`                       | optional | NULL | NULL |
| sling force-resling (cmd/sling_helpers.go:246-289) | `gastown/<caller>` or `user:<name>` | `sling` | `force-resling`                  | NULL | NULL | NULL |
| sling polecat-nuked (cmd/polecat.go:2028-2064)    | `gastown/<caller>`       | `sling`         | `polecat-nuked`                  | NULL | NULL | NULL |
| sling dog-session-failed (cmd/sling_formula.go:64-76) | `gastown/<caller>`    | `sling`         | `dog-session-failed`             | NULL | NULL | NULL |
| handoff molecule (cmd/handoff.go:1662-1670)       | `gastown/<caller>`       | `handoff`       | `superseded-by-session`          | NULL | NULL | NULL |
| witness orphan recovery (witness/handlers.go:3160-3178) | `gastown/witness`     | `witness`       | `orphan-molecule`                | NULL | NULL | NULL |
| `gt close` manual (cmd/close.go:84-108)           | `user:<name>`            | `user`          | `manual-close`                   | NULL | NULL | NULL |
| `gt close --cascade` (cmd/close.go:137-203)       | `user:<name>`            | `user`          | `manual-cascade`                 | NULL | NULL | NULL |
| `gt polecat identity rename` (cmd/polecat_identity.go:600-625) | `user:<name>` | `rig-bootstrap` | `identity-removed`               | NULL | NULL | NULL |
| `gt polecat identity remove` (cmd/polecat_identity.go:701-705) | `user:<name>` | `rig-bootstrap` | `identity-removed`               | NULL | NULL | NULL |
| `gt crew remove` (cmd/crew_lifecycle.go:168-184)  | `user:<name>`            | `rig-bootstrap` | `agent-removed`                  | NULL | NULL | NULL |
| `gt patrol report` (cmd/patrol_report.go:93-123)  | `gastown/<patrol-agent>` | varies by patrol | varies (e.g. `manual-close`)    | NULL | NULL | NULL |
| escalation `CloseEscalation` (beads/beads_escalation.go:249-280) | the escalator (from `EscalationFields.ClosedBy`) | `escalation` | `escalation-resolved`            | NULL | NULL | NULL |

`closed_by` for agents that originate work on behalf of a user (e.g. polecat
running `gt close`) is the polecat's rig/role/name, **not** the human. The
human's identity is captured in the `events` table (`actor` column on the
close event) for forensics, but the canonical column records the executing
agent.

## Backward compatibility

### Existing data

Three pre-existing populations must be handled:

1. **Beads closed before the schema migration:** all six canonical fields
   default to NULL / `'unknown'`. We do **not** invent data. The presence of
   `closure_source='unknown'` is the audit signal that this closure was
   pre-migration.
2. **Description-text provenance (escalation beads, MR beads):** parsed at
   read time by existing helpers (`ParseEscalationFields`, `ParseMRFields`)
   during backfill and on every read for one release cycle.
3. **Free-text `close_reason` conventions:**
   - `"Merged in <mr-id>"` prefix → `closure_source='refinery'`,
     `closure_trigger='merged'`, `linked_mr=<id>`.
   - `target_branch: <branch>` line → recorded in `events.old_value` /
     `events.new_value` (existing pattern), not in a separate column.
   - `commit_sha: <sha>` line → `merged_commit=<sha>`.
   - `pr_url: <url>` line → recorded in `events.new_value`.
   - `audit_bead: <id>` line → recorded in `events.new_value`.

### Read path

`bd show --json` MUST expose all six new fields on every issue regardless
of when it was closed. For pre-migration closures the fields are present
and equal to `'unknown'` / NULL.

`bd show` (text) prints the new fields when non-default:

```
status: closed
closed_at: 2026-06-24T19:36:14Z
closed_by: gastown/refinery
closure_source: refinery
closure_trigger: merged
linked_mr: gastown-mr-42
merged_commit: abc123...
published_commit: abc123...
close_reason: Merged in gastown-mr-42
                target_branch: main
                commit_sha: abc123...
```

### Dependency-readiness parsing

`internal/convoy/operations.go:188-281` (`IsIssueBlocked`) currently parses
`CloseReason` for the `"Merged in "` prefix to validate merge-blocks
blockers. After migration:

- New code reads `linked_mr` directly.
- Old code (still in production during the cutover window) keeps parsing
  the prefix.
- After one release, the prefix parse is removed and the column is the
  only source.

The cutover is gated on a smoke test: every merge-blocks blocker closed in
the prior 30 days parses to the same `linked_mr` via both paths.

### `content_hash` change

`internal/types/types.go:118-171` (in the upstream `beads` SDK) excludes
`closed_at`, `close_reason`, `closed_by_session` from `content_hash`. The
six new fields are added to `content_hash` so two beads with the same
content but different provenance hash distinctly. This is a behavior
change that requires a migration of the `content_hash` cache for all
existing rows.

## Backfill rules

Backfill is **opportunistic and evidence-bound**. We never invent
provenance. A row is backfilled only when at least one of the following is
true:

1. **An `events` row of type `closed` exists** for this issue with a
   non-empty `actor`. Use that `actor` as `closed_by`.
2. **`close_reason` parses to a known pattern.** Apply the mappings in the
   table above (`"Merged in "`, `commit_sha:`, etc.).
3. **Description-text parses.** For escalation beads, parse
   `closed_by:` / `closed_reason:` from the description. For MR beads,
   parse `merge_commit:`, `terminal_state:`, `published_commit:` from
   `MRFields`.
4. **`.beads/interactions.jsonl` has a matching entry.** Cross-reference
   on `(issue_id, field_change_to_closed)`. If the entry's `actor` is a
   known agent path, use it.

If none of (1)-(4) hold, leave `closure_source='unknown'` and
`closure_trigger='unknown'`. We mark the row with a `provenance:backfilled`
or `provenance:unverifiable` label so analysts can distinguish backfilled
rows from genuine fresh closures.

The backfill is runnable as a one-shot migration command:

```
bd migrate closure-provenance --dry-run    # report counts by source
bd migrate closure-provenance --execute   # apply
```

with `--dry-run` mandatory before `--execute`. Output includes:

- Total beads scanned.
- Counts per evidence source (events, close_reason, description, audit log).
- Count left as `unknown`.
- Sample rows for each evidence source.
- Verification: every `linked_mr` set by backfill must reference an
  existing MR bead (FK-style check via post-backfill `bd show`).

## Unknown / unverifiable rules

A row with `closure_source='unknown'` MUST NOT be silently upgraded to a
specific source during analysis. Audit reports and root-cause queries
that depend on `closure_source` or `closure_trigger` must either:

- Filter out `'unknown'`, or
- Surface the count of `'unknown'` rows prominently so the reader knows
  the analysis is partial.

`closed_by='unknown'` is similarly treated. Beads closed pre-migration
that survive an upgrade without backfill evidence remain `'unknown'`
permanently — we do not retroactively fabricate actor identity.

## Writer implementation guidance

The Go API surface gains:

```go
// In internal/beads/beads.go
type ClosureSource string
type ClosureTrigger string

const (
    ClosureSourceRefinery      ClosureSource = "refinery"
    ClosureSourceConvoy        ClosureSource = "convoy"
    ClosureSourceReaper        ClosureSource = "reaper"
    ClosureSourcePolecatDone   ClosureSource = "polecat-done"
    ClosureSourceSling         ClosureSource = "sling"
    ClosureSourceHandoff       ClosureSource = "handoff"
    ClosureSourceWitness       ClosureSource = "witness"
    ClosureSourceUser          ClosureSource = "user"
    ClosureSourceEscalation    ClosureSource = "escalation"
    ClosureSourceRigBootstrap  ClosureSource = "rig-bootstrap"
    ClosureSourceMigration     ClosureSource = "migration"
    ClosureSourceUnknown       ClosureSource = "unknown"
)

// Provenance carries all six canonical fields.
type Provenance struct {
    ClosedBy        string
    ClosureSource   ClosureSource
    ClosureTrigger  ClosureTrigger
    LinkedMR        string // empty when not applicable
    MergedCommit    string // empty when not applicable
    PublishedCommit string // empty when not applicable
}

func (b *Beads) CloseWithProvenance(p Provenance, ids ...string) error
func (b *Beads) CloseWithReasonAndProvenance(reason string, p Provenance, ids ...string) error
func (b *Beads) ForceCloseWithProvenance(p Provenance, ids ...string) error
func (b *Beads) ForceCloseWithReasonAndProvenance(reason string, p Provenance, ids ...string) error
```

The new methods are the canonical writers. Existing `Close`,
`CloseWithReason`, `ForceCloseWithReason` are kept as thin wrappers that
default `Provenance` to `closed_by=<BD_SESSION_ID>` and
`closure_source='unknown'`, `closure_trigger='unknown'` — they will emit a
warning log on every call so callers can be migrated to the new surface.

The raw Dolt SQL writers (reaper `AutoClose`, `ClosePluginReceipts`,
`ClosePluginDispatches`, `closeWispsInBatches`) are migrated to use the
new SQL UPDATE that includes all six columns. The beads SDK's
`CloseIssueInTx` (`internal/storage/issueops/close.go:29-86`) is extended
to write all six columns.

## Test cases

### Unit tests

1. **Each writer populates the right fields.**
   For every writer in the matrix above, a unit test calls the writer
   (in dry-run / mock mode) and asserts the resulting `Provenance`
   matches the expected row.

2. **Provenance enum validation.**
   `ClosureSource("nope").IsValid() == false`. Unknown triggers are
   rejected at the API layer (warning + downgraded to `'unknown'`).

3. **`content_hash` includes new fields.**
   Two beads with identical content but different `closed_by` hash
   differently.

4. **`bd show --json` exposes all fields.**
   - Fresh close: all six fields populated per the writer.
   - Pre-migration close: fields present with defaults (`'unknown'` /
     NULL).

5. **Backward-compat text output.**
   `bd show` renders the new fields, omits them when defaults, and keeps
   printing `close_reason` for human readability.

6. **Dependency-readiness parsing cutover.**
   The smoke test from "Backward compatibility" above: 30 days of
   merge-blocks blockers, prefix-parse and column-parse produce identical
   `linked_mr` for every row.

7. **Backfill produces no invented data.**
   For 100 closed beads with no `events` row, no parseable
   `close_reason`, and no audit-log entry, the backfill leaves
   `closure_source='unknown'` and `closure_trigger='unknown'`.

8. **Backfill evidence sources are independent.**
   A bead with both an `events` row and a parseable `close_reason`
   agrees on `closure_source` between them; disagreement is reported as
   an error.

9. **`close_reason` parsing correctness.**
   For each known pattern (`"Merged in "`, `target_branch:`,
   `commit_sha:`, `pr_url:`, `audit_bead:`), the parser extracts the
   expected value.

### Integration tests

10. **End-to-end refinery merge path.**
    Create an MR bead, merge via refinery, assert source issue has
    `closure_source=refinery`, `closure_trigger=merged`, `linked_mr=<id>`,
    `merged_commit=<sha>`, `published_commit=<sha>` (when verified).

11. **End-to-end convoy auto-close.**
    Create convoy with N tracked issues, close all N, run refinery
    convoy check, assert convoy has `closure_source=convoy`,
    `closure_trigger=all-tracked-completed`, no `linked_mr`.

12. **End-to-end reaper stale close.**
    Insert stale bead, run reaper, assert `closure_source=reaper`,
    `closure_trigger=stale-auto-close`.

13. **End-to-end `gt done` paths.**
    No-changes, default-MR, direct-merge, no-merge variants each
    produce the expected `Provenance`.

14. **End-to-end sling burn.**
    Force-resling a polecat produces `closure_source=sling`,
    `closure_trigger=force-resling` on the molecule wisp.

15. **Cross-rig evidence (`beads_global`, `bdglobal`).**
    Cross-rig closures propagate `Provenance` correctly when one rig
    closes a bead in another rig's namespace.

### Characterization / regression tests (from the parent spec)

16. **`hq-try2` — stacked branch tip-only MR.**
    Closure provenance on a stacked-branch MR records the rejection
    event (`closure_source=refinery`, `closure_trigger=stacked-merged`
    or pre-creation reject) and the source bead stays pending.

17. **`hq-6sdu` — local-file merge without publication guard.**
    After publication verification lands (gastown-cet.4.2), a local-only
    merge that lacks `published_commit` is recorded distinctly from a
    fully published merge.

18. **`hq-6af` — no-verdict reviewer.**
    `closure_trigger='merged'` only fires after degraded-quorum rules
    resolve; rejected merges due to no-verdict use
    `closure_trigger='rejected'` with the reviewer evidence recorded in
    `events.new_value`.

## Files affected

### Schema (upstream `beads` SDK — gastown-cet.4.2 implementation work)

- `internal/storage/schema/migrations/00NN_add_closure_provenance.up.sql`
  (new migration)
- `internal/storage/issueops/close.go` — extend UPDATE column list.
- `internal/types/types.go` — add `Provenance` struct + `content_hash`
  coverage.
- `internal/storage/issueops/scan.go` — add columns to
  `IssueSelectColumns`.
- `internal/storage/issueops/update.go` — extend `IsAllowedUpdateField`
  and `ManageClosedAt` logic.

### Go API surface (this repo)

- `internal/beads/beads.go` — new `*WithProvenance` methods and
  `ClosureSource` / `ClosureTrigger` enums.
- `internal/beads/store.go` — pass `Provenance` through to SDK.

### Writer updates (this repo)

Every writer in the matrix above gains provenance parameters. Major files:

- `internal/refinery/manager.go` — `RejectMR`, `PostMerge`.
- `internal/refinery/engineer.go` — supersede, conflict-moot, stacked,
  convoy auto-close.
- `internal/refinery/types.go` — `MergeRequest.Close` keeps in-memory
  state machine; surface maps to `Provenance`.
- `internal/cmd/convoy.go` — manual close, convoy check auto-close.
- `internal/deacon/feed_stranded.go` — empty-convoy close.
- `internal/reaper/reaper.go` — `AutoClose`, `ClosePluginReceipts`,
  `ClosePluginDispatches`, `closeWispsInBatches`.
- `internal/cmd/done.go` — no-changes, default-MR, direct-merge,
  no-merge variants.
- `internal/cmd/sling_helpers.go` — `burnMolecules`.
- `internal/cmd/sling_formula.go` — `closeFormulaWisp`.
- `internal/cmd/polecat.go` — nuke path.
- `internal/cmd/handoff.go` — handoff path.
- `internal/witness/handlers.go` — orphan molecule recovery.
- `internal/cmd/close.go` — manual close, cascade.
- `internal/cmd/polecat_identity.go` — rename, remove.
- `internal/cmd/crew_lifecycle.go` — crew remove.
- `internal/cmd/patrol_report.go` — patrol report close.
- `internal/beads/beads_escalation.go` — `CloseEscalation`.

### Migration tooling

- `cmd/bd/migrate_closure_provenance.go` — new migration command with
  dry-run + execute modes.

### Documentation

- `docs/design/closure-provenance.md` — this file.
- `docs/dolt-health-guide.md` — note the new columns on Dolt GC flatten.
- `docs/concepts/` — update lifecycle docs.

## Open questions (blockers for gastown-cet.4.2 implementation)

1. **Should `closed_by` accept arbitrary free-form strings or be FK-style
   validated against the agent-bead registry?** Recommendation: free-form
   for now; the agent-bead registry is not exhaustive (system actors
   don't always have agent beads).
2. **Should `published_commit` also carry the remote name?** Currently
   `MRFields.PublishedRemote` exists for MR beads; should the canonical
   column store the remote inline (`<remote>:<sha>`) or stay SHA-only
   with a separate column? Recommendation: SHA-only; remote can be
   derived from rig config (`git remote get-url origin`).
3. **Should the migration touch wisps (the `dolt_ignored` mirror) at all?**
   Recommendation: yes, but as a no-op schema addition so future wisps
   closures carry the same fields. Existing closed wisps backfilled the
   same way.
4. **Should the new columns be added in one migration or two (add columns
   nullable, then backfill, then enforce NOT NULL)?** Recommendation: two
   phases. Phase 1: ADD COLUMN nullable. Phase 2 (after backfill runs
   successfully): ALTER COLUMN ... SET NOT NULL.
