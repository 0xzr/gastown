# Retro-gate shard gap resolution (gastown-wpf)

## Scope

This document resolves the gap reported in `gastown-wpf`: eleven commits in the
Gastown restart window `0171489a6..573af6bb7` were not covered by retro-gate
shards A–D.

## Gap summary

The following commits were identified as uncovered in `gastown-wpf`:

1. `2b532d09` feat(witness): live-dry-run exercises exact throttle path against production state file
2. `2ea29bf9` fix(mayor,daemon): use heartbeat for liveness, add restart grace, capture Mayor hook context (gastown-xzf)
3. `1a080b7b` refactor(cmd,beads): remove dead cmd terminal-state helpers, move tests to beads package (gastown-a3k)
4. `6faf2e93` fix(consult): durable non-ephemeral bead, append-notes mirror, state guards (gastown-dec)
5. `1791f805` feat(witness,daemon): self-recovery for context-saturated or stalled patrol sessions (gastown-o9d)
6. `ee837575` fix(witness): REWORK_DEFERRED rollup reports real suppressed count (gastown-3ip)
7. `09880751` test(cmd): regression tests and error surfacing for convoy-stage JSON output (gastown-l76)
8. `a57c457d` feat(alerts): canonical root-cause keys and alert occurrence aggregation (gastown-cet.5.1)
9. `6ae7a4c5` fix(scheduler): refuse agent-role mismatch dispatch of Deacon beads under mol-polecat-work (gastown-c76)
10. `0471d3f8` docs(design): issue-store consolidation inventory and duplicate canonicalization (gastown-cet.1.2)
11. `ff7f3a36` test: Mayor decision precedence guards + gt mayor decision CLI (gastown-cet.7)

## Resolution

All eleven commits are now covered by **shard E**:

- **Bead:** `gastown-cet.12.14` — *Retro-gate Gastown shipped range shard E: late-arrival and missed restart-window commits*
- **Parent epic:** `gastown-cet.12` — *Retro-audit gastown fork hardening merged before four-model gate*
- **Owner/assignee:** `gastown/polecats/opal` is executing the strict-core four-model review and HMAC attestation.

Shard E explicitly enumerates the same eleven commits and tracks the required
strict-core review (`m3`, `codex`, `umans-kimi`, `umans-glm`) plus Opus verify.
The HMAC attestation will be written to `/home/ubuntu/.gt-gate-attestations/<tree_hash>`
only after all five reviewers PASS, per the parent epic policy.

## Why no additional shard was created

A new shard was suggested in `gastown-wpf`, but the Mayor already instantiated the
required shard (`gastown-cet.12.14`) before this polecat session resumed. The gap
bead therefore requires no further implementation; its purpose is to ensure the
commits are *tracked* by a retro-gate shard, which is now satisfied.

## Evidence

- `bd show gastown-cet.12.14` lists all eleven commits in its description and
  acceptance criteria.
- `bd show gastown-cet.12` shows `gastown-cet.12.14` as a child of the retro-gate
  epic.
- This report is committed to `docs/audits/gastown-wpf/shard-gap-resolution.md`
  as durable, version-controlled evidence of the resolution.

## Verdict

The `gastown-wpf` gap is **resolved by reference** to `gastown-cet.12.14`. No
additional code or audit work is required under this bead.
