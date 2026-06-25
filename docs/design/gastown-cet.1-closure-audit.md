# Closure Audit — gastown-cet.1 (Meta-Rig Bootstrap & Issue-Store Consolidation)

**Audit date:** 2026-06-25
**Auditor:** gastown/polecats/quartz
**Tracking issue:** `gastown-cet.1.3` (Bootstrap closure audit and doctor evidence)
**Parent epic:** `gastown-cet.1` — Prerequisite: meta-rig bootstrap and issue-store consolidation (P0)
**Spec:** `/home/ubuntu/gt-town/mayor/implementation-reviews/gt-meta-hardening-spec-2026-06-24.md` (Workstream A)

---

## 1. Verdict

**gastown-cet.1 can be closed.** The bootstrap defect is fixed, regression-covered,
and merged; issue-store consolidation is complete with no Dolt data loss; the
duplicate class is canonicalized; and every remaining `gt doctor` warning is
either unrelated operational hygiene (linked to follow-up bead `hq-1ic1`) or
already tracked by an existing open epic (`gastown-cet.5`). No unresolved
bootstrap blocker is hidden.

Closure of `gastown-cet.1` is gated on the Refinery merging this audit's MR —
the Refinery closes the source bead after merge, per polecat protocol.

---

## 2. Dependency state

| Bead | Title | Status | Merged in |
|---|---|---|---|
| `gastown-cet.1.1` | Rig bootstrap prefix/identity regression coverage (P0) | ✅ CLOSED | `gastown-wisp-46k` |
| `gastown-cet.1.2` | Issue-store consolidation and duplicate canonicalization (P0) | ✅ CLOSED | `gastown-wisp-a7t` |

Both prerequisites are landed. `gastown-cet.1.3` (this audit) is the closure
gate for the parent epic `gastown-cet.1`.

---

## 3. Bootstrap acceptance verification

### 3.1 The original bootstrap defect (spec, Workstream A)

> `gt rig add <name> ... --prefix <prefix>` created the rig and route but failed
> to scaffold `gastown-witness` and `gastown-refinery` agent beads, because
> agent-bead creation targeted an `hq-` prefixed (town) database instead of the
> rig-prefixed database.

### 3.2 Fix and regression coverage (gastown-cet.1.1, merged `gastown-wisp-46k`)

- `internal/beads/beads.go`: `WithNoRoute()` helper keeps operations in the
  wrapper's resolved beads directory without prefix routing.
- `internal/cmd/rig.go`: `runRigAdd` / `runRigAdopt` use `WithNoRoute()` when
  creating witness/refinery agent beads → rig-prefixed beads land in the rig DB.
- `internal/rig/manager.go`: `Manager.initAgentBeads` uses `WithNoRoute()`.
- Regressions:
  - `internal/rig/manager_test.go`: `TestInitAgentBeads_RigScopedNotHQ`
  - `internal/cmd/rig_integration_test.go`: `TestRigAdd_SeedsRigScopedBeadsInRigDatabase`

### 3.3 Live verification (2026-06-25)

`gt doctor --rig gastown --verbose` bootstrap-critical checks — all ✓:

| Check | Result |
|---|---|
| `agent-beads-exist` | ✓ All 9 agent beads exist with `gt:agent` label |
| `rig-beads-exist` | ✓ All 3 rig identity beads exist |
| `rig-config-sync` | ✓ All registered rigs have valid configuration |
| `database-prefix` | ✓ All database prefixes match `routes.jsonl` |
| `prefix-conflict` / `prefix-mismatch` / `rig-name-mismatch` | ✓ none |
| `routing-mode` | ✓ Beads routing.mode is explicit |
| `dolt-config` | ✓ All shared Dolt configs are explicit |
| `routes-config` | ✓ Routes configured correctly (6 routes) |
| `witness-exists` / `refinery-exists` | ✓ both exist |
| `beads-redirect` / `beads-redirect-target` | ✓ valid |

Agent/identity beads resolve in the **canonical gastown store** (not HQ):

```
gastown-rig-gastown   · rig identity bead for gastown   (OPEN, P2)
gastown-witness       · gastown-witness                 (OPEN, P2)
gastown-refinery      · gastown-refinery                 (OPEN, P2)
```

gastown rig `.beads/config.yaml` is correct: `prefix: gastown`,
`routing.mode: explicit`, `dolt.idle-timeout: "0"`, `dolt.server: 127.0.0.1`,
`dolt.port: 3307`, `storage.backend: dolt`, `export.auto: "false"`.

**Acceptance met:** `gt doctor --rig gastown` no longer reports missing
`gastown-witness`, `gastown-refinery`, or `gastown-rig-gastown`; rig add
prefix bootstrap has regression coverage; GT 1.2.0 compatibility preserved.

---

## 4. Route / prefix checks

### 4.1 Town `routes.jsonl` (canonical)

```jsonl
{"prefix":"hq-","path":"."}
{"prefix":"hq-cv-","path":"."}
{"prefix":"polybot-","path":"polybot"}
{"prefix":"gt-","path":"."}
{"prefix":"gastown-","path":"gastown/mayor/rig"}
{"prefix":"gtviz-","path":"gtviz"}
```

- `gastown-` resolves to the gastown `mayor/rig` server database (canonical).
- `hq-` / `gt-` resolve to the town store.
- `polybot-` / `gtviz-` route to their own rigs (out of scope).

### 4.2 Rig-level `routes.jsonl`

A stale rig-level `routes.jsonl` existed at `/home/ubuntu/gt-town/polybot/.beads/`
(warning `rig-routes-jsonl`: "breaks routing"). This was auto-removed by
`gt doctor --fix` during the audit probe; post-fix the check reports "No
rig-level routes.jsonl files (6 rigs checked)". This is the standard operator
remediation used throughout `gastown-cet.1` and does not affect gastown routing.

---

## 5. Noncanonical store disposition (gastown-cet.1.2, merged `gastown-wisp-a7t`)

Full inventory: `docs/design/issue-store-consolidation-inventory.md`. Summary:

| Store | Backend | Canonical? | Disposition |
|---|---|---|---|
| `/home/ubuntu/gt-town/.beads` | server (`hq`) | **Yes** | Retain — authoritative `hq-*` tracker |
| `/home/ubuntu/gt-town/gastown/mayor/rig/.beads` | server (`gastown`) | **Yes** | Retain — authoritative `gastown-*` tracker |
| `/home/ubuntu/gt-town/gastown/.beads` | redirect | **Yes** (alias) | Retain redirect |
| `/home/ubuntu/gt-town/mayor/gastown/.beads` | embedded (`hq`) | No | **Quarantine** — historical evidence; 14 unique `hq-` issues preserved read-only |
| `/home/ubuntu/gt-town-backups/gastown-orphan-beadstore-20260624T162212Z/.beads` | embedded (`hq`) | No | **Archive** — 1 unique issue (`hq-27q`) preserved as snapshot |
| `/home/ubuntu/gt-town/bdglobal/.beads` | server (`bdglobal`) | No | **Document** — legacy/empty (0 issues) |
| `/home/ubuntu/gt-town/beads_global/.beads` | server (`beads_global`) | No | **Document** — legacy/empty (0 issues) |

**No raw Dolt data was deleted.** No `.dolt/`, `noms/`, `LOCK`, `manifest`, or
JSONL files were edited or removed. Stale JSONL/export-state was neutralized by
relocation (preserved under `/home/ubuntu/gt-town-backups/`), not deletion.
Rollback instructions are recorded in the inventory (§5).

The `bdglobal` / `beads_global` register-or-quarantine decision is explicitly
deferred to `gastown-cet.5` (alert dedup/hygiene epic, OPEN) — not a bootstrap
blocker.

---

## 6. Duplicate canonicalization: `hq-try2` / `hq-pwx`

| Bead | Store | Status | Resolution |
|---|---|---|---|
| `gastown-73a` | gastown (canonical) | ✅ CLOSED (merged `gastown-wisp-ryb`) | **Canonical tracker** — stacked-branch MR lifecycle bug, P0 |
| `hq-try2` | town (`hq`) | OPEN | Active town-store copy; cross-linked to `gastown-73a` (Workstream B tracker) |
| `hq-pwx` | mayor/gastown (embedded, noncanonical) | ✅ CLOSED | Closed as duplicate of `gastown-73a`; preserved as historical evidence |
| `hq-k6lt` | town (`hq`) | OPEN | Stale-export-clobber class; cross-linked to `gastown-stale-export-clobber` (merged `gastown-wisp-9aa`) |

**Acceptance met:** duplicate GT lifecycle issue class has one canonical
gastown tracker (`gastown-73a`) with links from both the stale/HQ copies, and
the duplicate (`hq-pwx`) is closed.

---

## 7. Remaining `gt doctor` warnings — classification

Final state after standard `--fix --no-start` remediation of two transient
failures (`priming`, `hook-attachment-valid` for `mol-witness-patrol`):
**100 passed, 6 warnings, 0 failed.**

> Note on the transient failures: both auto-resolved via `gt doctor --fix
> --no-start` (priming subsystem reconfigured; the invalid `mol-witness-patrol`
> hook attachment — `mol-` prefix has no route — was detached). These are
> runtime hygiene, not bootstrap defects; they reappear across rigs as patrol
> molecules cycle. Neither is a `gastown-cet.1` acceptance item.

| # | Warning | Bootstrap blocker? | Disposition |
|---|---|---|---|
| 1 | `stale-binary` — gt 139 commits behind | No — toolchain | `hq-1ic1` (fix: `gt install`) |
| 2 | `orphan-processes` — 2 procs outside tmux | No — runtime | `hq-1ic1` |
| 3 | `persistent-role-branches` — 1 role off main | No — operational | `hq-1ic1` |
| 4 | `clone-divergence` — 3 clones behind | No — operational sync | `hq-1ic1` |
| 5 | `unregistered-beads-dirs` — 3 dirs | No — consolidation residue | linked to `gastown-cet.5` (bdglobal/beads_global); neutralized stores documented in inventory |
| 6 | `polecat-clones-valid` — 1 warning (umans-glm session) | No — runtime | `hq-1ic1` |

**All 6 are unrelated to the bootstrap defect.** Each has a linked follow-up
(`hq-1ic1` for operational hygiene; `gastown-cet.5` for the unregistered-store
register/quarantine decision). No unresolved bootstrap blocker is hidden.

---

## 8. Evidence

- This document: `docs/design/gastown-cet.1-closure-audit.md`
- Consolidation inventory (gastown-cet.1.2): `docs/design/issue-store-consolidation-inventory.md`
- Canonical duplicate tracker: `gastown-73a` (CLOSED, merged `gastown-wisp-ryb`)
- Closed duplicate: `hq-pwx` (mayor/gastown embedded store)
- Active HQ copy: `hq-try2` (town store, cross-linked)
- Non-bootstrap warning follow-up: `hq-1ic1`
- Unregistered-store hygiene epic: `gastown-cet.5` (OPEN)
- Regression coverage: `gastown-cet.1.1` (merged `gastown-wisp-46k`)

## 9. Closure decision

✅ **gastown-cet.1 acceptance satisfied.** The dedicated `gastown` rig is
usable for source-controlled GT fixes: rig bootstrap creates prefix-correct
agent/identity beads with regression coverage; ad hoc stores are migrated or
cross-linked without data loss; the `hq-try2`/`hq-pwx` duplicate is
canonicalized; remaining warnings are tracked and unrelated to bootstrap.

The parent epic `gastown-cet.1` may be closed by the Refinery after this MR
merges. No bootstrap blocker remains unresolved.
