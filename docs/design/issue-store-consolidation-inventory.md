# Issue-Store Consolidation and Duplicate Canonicalization Inventory

**Audit date:** 2026-06-25  
**Auditor:** gastown/polecats/obsidian  
**Tracking issue:** `gastown-cet.1.2`  
**Spec:** `/home/ubuntu/gt-town/mayor/implementation-reviews/gt-meta-hardening-spec-2026-06-24.md`

## Scope

Finish the bootstrap follow-up for historical/ad hoc GT issue stores. Inventory
`/home/ubuntu/gt-town/mayor/gastown`, the gastown orphan beadstore backup,
`gt-`/HQ duplicates, `bdglobal`, and `beads_global`; decide
migrate/cross-link/quarantine for each **without deleting Dolt data**. Canonicalize
the `hq-try2` / `hq-pwx` duplicate class.

## Constraint: no raw Dolt edits

All state changes are performed through the `bd` CLI or documented as read-only
evidence. No `.dolt/`, `noms/`, `LOCK`, `manifest`, or JSONL files were edited or
removed.

---

## 1. Known ad hoc issue stores

| Store path | Backend | Dolt DB | Project ID | Prefix(es) seen | Open issues | Canonical? | Resolution |
|---|---|---|---|---|---|---|---|
| `/home/ubuntu/gt-town/.beads` | server | `hq` | `80bff360-bff2-4939-ad8e-39a6154c3007` | `hq-`, `hq-cv-`, `gt-` | ~287 `hq-` open | **Yes** — canonical town store | Retain as authoritative `hq-*` tracker |
| `/home/ubuntu/gt-town/gastown/mayor/rig/.beads` | server | `gastown` | (server-mode) | `gastown-` | 7 `gastown-` open | **Yes** — canonical gastown rig store | Retain as authoritative `gastown-*` tracker |
| `/home/ubuntu/gt-town/gastown/.beads` | redirect | → `gastown/mayor/rig/.beads` | — | `gastown-` | (via redirect) | **Yes** (alias) | Retain redirect |
| `/home/ubuntu/gt-town/mayor/gastown/.beads` | embedded | `hq` | `417de1f3-d3ad-4651-81bf-c00a16b5b210` | `hq-` | 14 open | **No** | **Quarantine as historical evidence**; duplicates closed, unique issues preserved read-only |
| `/home/ubuntu/gt-town-backups/gastown-orphan-beadstore-20260624T162212Z/.beads` | embedded | `hq` | `417de1f3-d3ad-4651-81bf-c00a16b5b210`* | `hq-` | 1 open (`hq-27q`) | **No** | **Archive as historical evidence**; unique issue `hq-27q` documented |
| `/home/ubuntu/gt-town/bdglobal/.beads` | server | `bdglobal` | — | — | 0 | **No** (legacy) | **Register as protected legacy/empty**; labeled in `gt dolt status`, ignored by `gt doctor` |
| `/home/ubuntu/gt-town/beads_global/.beads` | server | `beads_global` | — | — | 0 | **No** (legacy) | **Register as protected legacy/empty**; labeled in `gt dolt status`, ignored by `gt doctor` |

\* Orphan backup `metadata.json` reuses the same `project_id` as
`mayor/gastown/.beads`; this is consistent with it being a backup of that
project, but its Dolt commit hash differs (`ob3iqp...` vs the live embedded
store), confirming it is a point-in-time snapshot, not a live duplicate.

### 1.1 Town `routes.jsonl` registry

```text
{"prefix":"hq-","path":"."}
{"prefix":"hq-cv-","path":"."}
{"prefix":"polybot-","path":"polybot"}
{"prefix":"gt-","path":"."}
{"prefix":"gastown-","path":"gastown/mayor/rig"}
{"prefix":"gtviz-","path":"gtviz"}
```

- `hq-` and `gt-` resolve to the town store (`/home/ubuntu/gt-town/.beads`).
- `gastown-` resolves to the gastown mayor/rig server database.
- `polybot-` and `gtviz-` route to their own rigs and are out of scope for this
  GT consolidation.

### 1.2 Key counts (live query, 2026-06-25)

- Town store total open: ~310 lines emitted by `bd list`; 287 lines match an
  `hq-` prefix marker.
- Noncanonical `mayor/gastown` embedded store: 15 open `hq-` issues before
  canonicalization, 14 after closing `hq-pwx` as duplicate.
- Orphan backup store: 1 open `hq-` issue (`hq-27q`).
- `bdglobal` and `beads_global`: 0 issues each.
- Gastown canonical store: 7 open `gastown-` issues before canonicalization,
  8 after creating the canonical `gastown-73a` tracker.

---

## 2. Duplicate class canonicalization: `hq-try2` / `hq-pwx`

### 2.1 Evidence

Both issues describe the same polybot-h85.10.18 stacked-branch MR lifecycle defect:

| Field | `hq-try2` (town store) | `hq-pwx` (mayor/gastown embedded) |
|---|---|---|
| Title | GT MR submission must not cherry-pick only tip commit or close source before merge | identical |
| Priority | P0 bug | P0 bug |
| Created | 2026-06-23T16:55:28Z | 2026-06-23T16:54:55Z |
| Store | canonical town | noncanonical `mayor/gastown` |

`hq-pwx` was created 33 seconds earlier but landed in the noncanonical embedded
store. `hq-try2` was filed slightly later in the canonical town store and is the
active tracker referenced by Workstream B of the meta-hardening spec.

### 2.2 Canonical resolution

- **Canonical tracker created:** `gastown-73a` — GT MR submission must not cherry-pick only tip commit or close source before merge (P0 bug).
- **Cross-link added to `hq-try2`:** notes reference `gastown-73a` as the canonical gastown tracker; the HQ/town copy remains the active Workstream B tracker.
- **`hq-pwx` closed as duplicate** of `gastown-73a` with reason: "canonical tracker is gastown-73a; this noncanonical-store copy is preserved as historical evidence."

This satisfies the acceptance criterion: *duplicate GT lifecycle issues have one
canonical gastown tracker with links from stale/HQ copies.*

---

## 3. Stale JSONL / export clobber case: `hq-k6lt`

`hq-k6lt` (town store, P1) documents a polybot-wcy bead-state race where an
issue reverted to CLOSED in Dolt shortly after reopen, blocking the scoped
rework-bounce runner. The root failure class — stale JSONL/export state
clobbering live Dolt rows — is the same class already fixed in
`gastown-stale-export-clobber` (closed, merged in `gastown-wisp-9aa`).

### 3.1 Canonical resolution

- `gastown-stale-export-clobber` is the canonical, implemented, and merged
  tracker for the stale-export-clobber class.
- `hq-k6lt` is preserved in the town store as a live operational escalation
  (`hq-wisp-53uy`) until the polybot-wcy recovery is complete, but its failure
  class is already addressed by the gastown hardening work.
- The implemented guardrails (`internal/doctor/stale_jsonl_export_check.go`,
  `internal/beads/beads_types.go`, `internal/rig/manager.go`) ensure that a
  stale export cannot silently re-close or revert a reopened canonical Dolt
  bead; any future stale export import is fenced by doctor checks and the
  `BEADS_NO_AUTO_IMPORT` / `BD_EXPORT_AUTO=false` environment locks.

---

## 4. Noncanonical store decisions and neutralization

### 4.1 `mayor/gastown/.beads` (embedded)

**Decision:** QUARANTINE as historical evidence.

**Rationale:**

- It is an embedded Dolt store inside `/home/ubuntu/gt-town/mayor/gastown`,
  not routed by `routes.jsonl`.
- It contains 14 remaining `hq-` issues (after `hq-pwx` closed) that are not
  visible to normal `bd` routing from the gastown worktree.
- Left as-is it is a shadow store that can re-introduce duplicate `hq-` IDs if
  an unanchored `bd create` resolves to it.

**Neutralization steps taken:**

1. Documented in this inventory.
2. Closed the duplicate `hq-pwx` and linked it to the canonical gastown tracker.
3. The remaining 14 unique issues are preserved but are not routed by the town
   registry; any future work on them should be re-created in the canonical town
   store if they become active.

**Original 14 unique issues at audit time (2026-06-25):**

- `hq-3jh` P0 Make gt patrol scan support read-only Witness-safe mode
- `hq-4y8` P0 GT must not close source issue before MR actually merges
- `hq-6af` P0 Refinery gate must treat no-verdict reviewers as unavailable, not FAIL
- `hq-juu` P0 gt session start must preserve polecat model assignment
- `hq-kdj` P0 Bead 3: diagnose and fix witness tmux socket split-brain recovery
- `hq-rzf` P0 Durable GT merge/reconcile fixes after Opus no-verdict rejection
- `hq-3mh` P1 Scheduler-spawned polecats lose hook attachment and report false POLECAT_DONE
- `hq-4do` P1 post-merge cleanup leaves merged polecat blocked when remote branch is missing
- `hq-6na` P1 Polecat recovery should clear terminal active_mr after MQ rejection
- `hq-9lv` P1 gt session restart should preserve polecat model assignment
- `hq-faz` P1 gt done guard ignores restored hook/--issue and resolves target as polybot/polybot
- `hq-k2i` P1 gt handoff --cycle must safely target remote polecat sessions or fail closed
- `hq-t7t` P1 gt session restart must preserve polecat model assignment
- `hq-yyz` P1 check-recovery active_mr stale after MR resubmission

Many of these overlap with Workstreams B and C of the meta-hardening spec; as
those workstreams are implemented, the canonical trackers will be created in the
town or gastown stores and the embedded copies should be closed as duplicates.
No mass migration was performed to avoid disturbing Dolt state.

#### 4.1.1 Remediation status (gastown-cet.1.2.1, 2026-06-28)

The audit deferred remediation pending canonical tracker creation. The
follow-up bead `gastown-cet.1.2.1` (assigned polecat onyx) executed the
disposition work on 2026-06-28. At remediation time, the embedded store
contained 28 open `hq-*` issues (14 original + 14 new since audit). All were
closed via `bd` CLI with cross-link notes pointing to canonical trackers;
no raw `.dolt/`, `noms/`, `LOCK`, `manifest`, or JSONL files were modified.

Per-issue disposition:

| Embedded | Title | Disposition | Canonical |
|---|---|---|---|
| `hq-3lt` | Retro-attest all restart-window completed work | closed-duplicate | `gastown-jkm` (CLOSED, Merged) |
| `hq-4y8` | GT must not close source issue before MR actually merges | closed-duplicate | `gastown-73a` (CLOSED, Merged) |
| `hq-5mf` | Enforce strict multi-model attestation | closed-duplicate | `gastown-7g4` (CLOSED) |
| `hq-6af` | Refinery gate must treat no-verdict reviewers as unavailable | closed-duplicate | `gastown-cet.2.2` (CLOSED) |
| `hq-6nb` | Fix gofmt failure on current main | closed-duplicate | `gastown-bqm` (CLOSED, Merged) |
| `hq-730` | pre-verified refinery fast-path bypasses multi-model/HMAC gate | closed-duplicate | `gastown-6n7` (CLOSED, Merged) |
| `hq-9nh` | Dog sessions falsely fail liveness | closed-duplicate | `gastown-dogfix25` (CLOSED, Merged) |
| `hq-9uz` | live pinned runtime false-dead health | closed-duplicate | `gastown-6ta` (CLOSED) |
| `hq-dxw` | Cut over Mayor liveness fix onto pinned 1.2.0 | closed-duplicate | `gastown-cet.15` (CLOSED) |
| `hq-rzf` | Durable GT merge/reconcile after Opus no-verdict | closed-duplicate | `gastown-rkb` (CLOSED, Merged) |
| `hq-vr6` | Automate refinery peer-review rework bounce | closed-duplicate | `gastown-p3w` (CLOSED, Merged) |
| `hq-xqf` | Refinery gate M3 adapter drops fenced PASS | closed-duplicate | `gastown-cet.2.2` (CLOSED; M3 fenced-PASS fix may overlap) |
| `hq-ju2` | Audit gt done empty_hook_no_evidence after deleted molecules | closed-duplicate | `gastown-dg1` (CLOSED, Merged) |
| `hq-03j` | Startup protocol empty-hook polecats run gt done again | closed-duplicate | `gastown-t7l` (CLOSED, Merged) |
| `hq-9lv` | gt session restart preserve polecat model assignment | closed-duplicate | `gastown-hkd` (CLOSED, Merged) |
| `hq-t7t` | gt session restart preserve polecat model assignment | closed-duplicate | `gastown-hkd` (CLOSED, Merged) |
| `hq-yyz` | check-recovery active_mr stale after MR resubmission | closed-duplicate | `gastown-1gd` (OPEN) |
| `hq-6na` | Polecat recovery clear terminal active_mr | closed-duplicate | `gastown-1gd` (OPEN) |
| `hq-juu` | gt session start preserve polecat model assignment | closed-duplicate | `gastown-34y` (OPEN) |
| `hq-3jh` | gt patrol scan read-only Witness-safe | closed-preserved | none in gastown; re-create if active |
| `hq-kdj` | witness tmux socket split-brain recovery | closed-preserved | none in gastown; re-create if active |
| `hq-8bc` | REWORK_DEFERRED live notices bypass durable throttle | closed-preserved | `gastown-cet.7` (CLOSED) |
| `hq-4do` | post-merge cleanup merged polecat blocked | closed-preserved | `gastown-1gd` (OPEN) |
| `hq-k2i` | gt handoff --cycle safe-target | closed-preserved | `gastown-cet.3.1` (CLOSED) |
| `hq-faz` | gt done guard ignores restored hook/--issue | closed-preserved | `gastown-dg1`/`gastown-t7l` (CLOSED) |
| `hq-3mh` | Scheduler-spawned polecats lose hook attachment | closed-preserved | `gastown-cet.16.2` (CLOSED) |
| `hq-fgp` | Block Claude plan mode for polecat sessions | closed-preserved | feature landed 9ab35de9; `gastown-ex0` (OPEN, doc review) |
| `hq-u7e` | Fix witness patrol loop: rig actor cannot resolve HQ patrol | closed-preserved | `gastown-72v` (OPEN); appeared during remediation |

#### 4.1.2 Structural note: ongoing write traffic

During remediation (28 closed), a new issue `hq-u7e` was created in the
embedded store mid-session, confirming the audit's concern that this
noncanonical store continues to receive writes. The structural fix is to
make `bd` route away from this embedded store: the `routes.jsonl` registry
already does not route `hq-*` here, so the writes must originate from a
cwd that resolves to this directory. Recommended follow-up: add a
`gt doctor` check (`internal/doctor/noncanonical_stores_check.go`) that
flags `bd create` activity in this embedded store and emits an escalation
hook, mirroring the existing `protectedSharedServerDatabases()` mechanism.
Filed as `gastown-cet.1.2.1.1` (gt doctor escalation hook).

Acceptance criteria for `gastown-cet.1.2.1`:
- [x] Every open `hq-*` issue in the embedded store has a documented disposition
      (28/28 closed as duplicate/preserved with cross-link notes).
- [x] This §4.1 inventory updated with remediation status per issue.
- [x] No raw `.dolt/`, `noms/`, `LOCK`, `manifest`, or JSONL files modified.
      Verified via `git status` of all nearby worktrees clean and Dolt commit
      log for the embedded store shows only `bd`/`bd close` operations.

### 4.2 Orphan backup (`gastown-orphan-beadstore-20260624T162212Z/.beads`)

**Decision:** ARCHIVE as historical evidence.

**Contents:** one issue, `hq-27q` — Fix MQ lifecycle: do not close source beads
until Refinery terminal merge.

This issue is unique to the backup and does not exist in the live town store.
It is preserved as a snapshot of the state at `2026-06-24T12:22:00Z`. If it
becomes active, it should be re-created in the canonical town store and this
backup copy referenced as provenance.

### 4.3 `bdglobal` and `beads_global`

**Decision:** REGISTER as protected legacy empty stores.

Both Dolt databases are currently empty (zero issues). They are retained on disk
so existing metadata references remain stable. Their disposition is implemented
in the codebase:

- `internal/doltserver/doltserver.go:protectedSharedServerDatabases()` labels
  `bdglobal` and `beads_global` as protected legacy empty databases. This makes
  `gt dolt status` / `gt dolt list` report their purpose instead of an
  unexplained empty store, and prevents `gt dolt cleanup` from removing them.
- `internal/doctor/unregistered_beads_check.go` treats the top-level
  `bdglobal/` and `beads_global/` directories as known legacy stores (via the
  canonical list exposed by `doltserver.ProtectedSharedServerDatabaseNames()`),
  so `gt doctor` no longer reports them as unregistered beads directories.

No Dolt data is deleted. To reverse the registration, remove the entries from
`protectedSharedServerDatabases()` (which automatically updates
`knownLegacyStoreDirs`) and re-run `gt doctor` to confirm expected warnings
return.

#### 4.3.1 Verification status (`gastown-cet.1.2.4`, 2026-06-28)

This verification bead (`gastown-cet.1.2.4`, polecat onyx, 2026-06-28) is the
live status tracker for the `beads_global` registration. The implementation
that precedes it is already in place; the bead's purpose is to confirm the four
target state checks and keep the audit record current.

**Target state 1 — `gt dolt status` labels `beads_global` as protected**

```text
● Dolt server is running (PID 4059825)
  Port: 3307
  Data dir: /home/ubuntu/gt-town/.dolt-data
  Databases:
    ...
    - beads_global         (legacy empty beads_global database (protected))
    ...
```

Result: **PASS**. `beads_global` appears with the protected-legacy purpose label,
not as an unexplained empty store.

**Target state 2 — `gt dolt list` does not flag `beads_global` as orphaned**

```text
Rig databases in /home/ubuntu/gt-town/.dolt-data:
  ...
  beads_global (legacy empty beads_global database (protected))
    /home/ubuntu/gt-town/.dolt-data/beads_global
  ...
```

Result: **PASS**. `beads_global` is listed as a protected legacy database, not
as an orphan.

**Target state 3 — `gt doctor` does not report `beads_global/` as unregistered**

The `unregistered-beads-dirs` doctor check scans top-level directories with
`.beads/metadata.json` and flags those that are neither registered rigs nor known
system/legacy directories. After the implementation of
`knownLegacyStoreDirs()` (backed by
`doltserver.ProtectedSharedServerDatabaseNames()`), `beads_global/` is excluded
from the check.

On this run the check emitted:

```text
⚠  unregistered-beads-dirs 2 unregistered directory(ies) with beads metadata
     └─ beads/ has .beads/metadata.json pointing to database "beads" (not a registered rig)
     └─ gt-town/ has .beads/metadata.json pointing to database "gt-town" (not a registered rig)
```

Result: **PASS**. The two flagged directories are `beads/` and `gt-town/`;
`beads_global/` is absent, so `gt doctor` does not report it as unregistered.

**Target state 4 — `gt dolt cleanup --dry-run` does not propose removing
`beads_global/`**

```text
✓ No orphaned databases found in .dolt-data/
```

Result: **PASS**. Cleanup dry-run proposes no removal.

**Verification summary**

| Check | Command | Result |
|---|---|---|
| Protected purpose label visible | `gt dolt status` | PASS |
| Not flagged as orphaned | `gt dolt list` | PASS |
| Not reported as unregistered | `gt doctor` | PASS |
| Cleanup dry-run ignores it | `gt dolt cleanup --dry-run` | PASS |

All target states from the §4.3 decision are satisfied. Future regressions in
`beads_global` protection should be filed as new bug beads and cross-linked to
`gastown-cet.1.2.4`.

---

## 5. Rollback instructions

If any of the canonicalization decisions need to be undone:

1. **`hq-pwx` reopen:**
   ```bash
   cd /home/ubuntu/gt-town/mayor/gastown
   bd update hq-pwx --status=open --notes "Reopened per rollback of gastown-cet.1.2 canonicalization."
   ```
2. **`gastown-73a` status:**
   - Leave open if it remains the active consolidation tracker.
   - If the canonicalization is fully reverted, close `gastown-73a` with
     `bd close gastown-73a --reason="rolled back: hq-pwx reopened, hq-try2 remains canonical town copy"`.
3. **`hq-try2` note cleanup:**
   ```bash
   cd /home/ubuntu/gt-town
   bd update hq-try2 --notes "Rollback: removed gastown-73a cross-link."
   ```
4. **Document rollback:** append a dated rollback note to this file and update
   `gastown-cet.1.2` if the bead is still open.
5. **Remediation rollback (`gastown-cet.1.2.1`, 28 issues):** for each closed
   embedded `hq-*` issue, run from `cd /home/ubuntu/gt-town/mayor/gastown`:
   ```bash
   bd update <hq-id> --status=open --notes "Reopened per rollback of gastown-cet.1.2.1 remediation; canonical tracker: <canonical-id>."
   ```
   Then revert this document's §4.1.1 disposition table.

No Dolt files were modified outside `bd`, so rollback is limited to `bd` state
changes and this document.

---

## 6. Verification checklist

- [x] Dry-run inventory lists counts, prefixes, and canonical resolution for every known ad hoc store.
- [x] Duplicate GT lifecycle issue class `hq-try2` / `hq-pwx` has one canonical gastown tracker (`gastown-73a`) with links from both stale/HQ copies.
- [x] Noncanonical stores are documented as historical evidence; `hq-pwx` duplicate closed, remaining embedded-store issues preserved read-only.
- [x] Stale export clobber case (`hq-k6lt`) explicitly tied to canonical `gastown-stale-export-clobber` fix.
- [x] Rollback instructions recorded.
- [x] No raw `.dolt/`, `noms/`, `LOCK`, `manifest`, or JSONL files edited or removed.
- [x] `bdglobal` and `beads_global` registered as protected legacy empty stores in `doltserver.go` and ignored by `unregistered-beads-dirs`; `beads_global` specifically verified in §4.3.1 via `gastown-cet.1.2.4`.
- [x] **Remediation (`gastown-cet.1.2.1`, 2026-06-28):** All 28 open `hq-*` issues in the embedded `mayor/gastown/.beads` store closed via `bd` CLI as duplicates or preserved-for-provenance with cross-link notes pointing to canonical gastown/town trackers; `bd list --status=open` from `/home/ubuntu/gt-town/mayor/gastown` now returns zero issues. Disposition table recorded in §4.1.1. No raw Dolt files modified.

---

## 7. Audit artifacts

- This document: `docs/design/issue-store-consolidation-inventory.md`
- Canonical tracker: `gastown-73a`
- Closed duplicate: `hq-pwx` (mayor/gastown embedded store)
- Active HQ copy: `hq-try2` (town store, cross-linked)
- Live stale-export guardrail fix: `gastown-stale-export-clobber` (merged)
- Remediation bead (`gastown-cet.1.2.1`, 2026-06-28, polecat onyx): disposition
  table in §4.1.1; 28 embedded-store `hq-*` issues closed via `bd` as
  duplicate/preserved with cross-link notes to canonical gastown/town trackers.
