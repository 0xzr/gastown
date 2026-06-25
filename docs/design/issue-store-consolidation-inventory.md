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
| `/home/ubuntu/gt-town/bdglobal/.beads` | server | `bdglobal` | — | — | 0 | **No** | **Document as legacy/empty**; no action unless future use is assigned |
| `/home/ubuntu/gt-town/beads_global/.beads` | server | `beads_global` | — | — | 0 | **No** | **Document as legacy/empty**; no action unless future use is assigned |

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

**Remaining unique issues (read-only list for audit):**

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

### 4.2 Orphan backup (`gastown-orphan-beadstore-20260624T162212Z/.beads`)

**Decision:** ARCHIVE as historical evidence.

**Contents:** one issue, `hq-27q` — Fix MQ lifecycle: do not close source beads
until Refinery terminal merge.

This issue is unique to the backup and does not exist in the live town store.
It is preserved as a snapshot of the state at `2026-06-24T12:22:00Z`. If it
becomes active, it should be re-created in the canonical town store and this
backup copy referenced as provenance.

### 4.3 `bdglobal` and `beads_global`

**Decision:** DOCUMENT as legacy/empty.

Both Dolt databases are currently empty (zero issues). They are listed by the
Dolt server (`gt dolt status`) but are not referenced by any rig route. They are
not deleted or removed; any future purpose must be explicitly registered in a
route or documented before use.

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

---

## 7. Audit artifacts

- This document: `docs/design/issue-store-consolidation-inventory.md`
- Canonical tracker: `gastown-73a`
- Closed duplicate: `hq-pwx` (mayor/gastown embedded store)
- Active HQ copy: `hq-try2` (town store, cross-linked)
- Live stale-export guardrail fix: `gastown-stale-export-clobber` (merged)
