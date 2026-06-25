# Investigation: 17 restart-window commits merged with no refinery gate telemetry

**Bead:** `gastown-a37`
**Investigator:** polecat jasper
**Date:** 2026-06-25
**Parent / context:** `gastown-jkm` retro-attest proof-table follow-up

---

## 1. Scope and hypothesis

The mayor policy defines a merge as:

> `refinery-gate.sh` → run-all-gates → 3 independent peers PASS → Opus PASS → HMAC attestation → merge.

`gastown-jkm` compiled a proof table over the 24h restart window
(2026-06-24T20:48:17-04:00 through 2026-06-25T13:46:00-04:00) and found:

- 35 non-WIP commits on `origin/main`.
- 18 had refinery telemetry with `status=reject` (Phase-1 deterministic gate failed).
- **17 had zero refinery telemetry at all**, which means the strict-core refinery gate never ran on those merge trees.

This investigation determines **why the gate telemetry is missing** and whether the
merge path was `gt done`/Refinery or a direct push that bypassed the gate.

---

## 2. Methodology

Evidence sources inspected:

1. `/home/ubuntu/gt-town/.runtime/refinery-telemetry/refinery-gate-*.jsonl` — every `gate_start`, `phase_complete`, `review_complete`, and `gate_complete` event.
2. `/home/ubuntu/gt-town/.runtime/gate-hook-telemetry/gate-hook-*.jsonl` — the deterministic pre-receive hook telemetry.
3. `/home/ubuntu/.gt-gate-attestations/<tree_hash>` — HMAC tokens written only on refinery `gate_complete` with `status=merge`.
4. Local bare repo reflog: `/home/ubuntu/gt-town/gastown/.repo.git/logs/refs/heads/main`.
5. Remote branch topology: `git name-rev --refs='refs/remotes/origin/polecat/*'`.
6. GitHub API: branch protection status, PR list, commit author/committer metadata.
7. Existing retro-gate shard beads: `gastown-cet.12.4`–`.12.8`, `gastown-cet.12.14`, `gastown-wpf`.

---

## 3. Primary finding

All 17 affected commits have **zero events** in the refinery telemetry stream.
The earliest refinery telemetry event is at 2026-06-24T22:01:04-04:00, and the
telemetry system was continuously recording events after that point for other
commits; therefore a simple "telemetry not yet enabled" explanation does not
account for the later no-gate commits.

Because `refinery-gate.sh` emits `gate_start` at the very beginning of every
candidate evaluation, the absence of any event for a commit means the gate
script was **never invoked** for that tree. These merges happened **outside the
Refinery strict-core gate path**.

---

## 4. Affected commits (detailed evidence)

| SHA | Tree | Committer (local) | Source bead | Nearest surviving polecat branch | Retro-gate coverage | Refinery events |
|-----|------|---------------------|-------------|----------------------------------|---------------------|-----------------|
| `6faf2e93` | `661256907378` | 2026-06-25 12:11:22 -0400 | gastown-dec | `origin/polecat/obsidian/idle-clean-20260625T1818~3` | gastown-cet.12.14 shard E (in progress) | 0 |
| `62c64b64` | `2abdff3ba2ae` | 2026-06-25 11:00:29 -0400 | gastown-cet.12.4 | `origin/polecat/obsidian/idle-clean-20260625T1818~6` | gastown-cet.12.4 (audit deliverable) | 0 |
| `8efcb5b1` | `a28ee3d4adb2` | 2026-06-25 10:14:01 -0400 | gastown-cet.16.2 | `origin/polecat/quartz/gastown-cet.17@mqtln3s1~1` | **NOT covered by any retro-gate shard** | 0 |
| `09880751` | `e4470fd3a337` | 2026-06-25 10:06:02 -0400 | gastown-l76 | `origin/polecat/opal/gastown-l76@mqthcyjn` | gastown-cet.12.14 shard E (in progress) | 0 |
| `a57c457d` | `b80fe1090d55` | 2026-06-25 09:55:33 -0400 | gastown-cet.5.1 | `origin/polecat/opal/gastown-l76@mqthcyjn~1` | gastown-cet.12.14 shard E (in progress) | 0 |
| `6ae7a4c5` | `4771fa5dcb71` | 2026-06-25 09:22:32 -0400 | gastown-c76 | `origin/polecat/jasper/gastown-c76@mqtfcgw1` | gastown-cet.12.14 shard E (in progress) | 0 |
| `141f7144` | `9f27ea82c767` | 2026-06-25 09:06:22 -0400 | gastown-cet.5.3 | `origin/polecat/quartz/gastown-cet.5.3@mqthaah6` | gastown-cet.12.8 shard D | 0 |
| `a534610f` | `177d969ab84b` | 2026-06-25 08:01:35 -0400 | gastown-vi6 | `origin/polecat/opal/gastown-vi6@mqtff4e4` | gastown-cet.12.8 shard D | 0 |
| `f998325f` | `564bb220fc0c` | 2026-06-25 07:57:47 -0400 | gastown-1l8 | `origin/polecat/opal/gastown-vi6@mqtff4e4~1` | gastown-cet.12.8 shard D | 0 |
| `17a1f47b` | `ffe860350a4a` | 2026-06-25 07:53:23 -0400 | gastown-cet.1.3 | `origin/polecat/quartz/gastown-cet.1.3@mqtfbpdy` | gastown-cet.12.8 shard D | 0 |
| `0c43863a` | `0b71749da120` | 2026-06-25 01:23:30 -0400 | gastown-cet.6.2 | `origin/polecat/onyx/gastown-cet.13@mqt2hum6~2` | gastown-cet.12.7 shard C | 0 |
| `a7547605` | `f74f046c75f2` | 2026-06-25 01:20:00 -0400 | gastown-cet.6.4 | `origin/polecat/onyx/gastown-cet.13@mqt2hum6~3` | gastown-cet.12.7 shard C | 0 |
| `7feabe39` | `2d3ac7604aee` | 2026-06-25 01:15:09 -0400 | gastown-hkd | `origin/polecat/quartz/gastown-hkd@mqszzw7l` | gastown-cet.12.7 shard C | 0 |
| `f5966a10` | `3297ade42c41` | 2026-06-24 23:34:16 -0400 | gastown-cet.11 | `origin/polecat/opal/gastown-cet.11@mqsxwup4` | gastown-cet.12.7 shard C | 0 |
| `f9bd330a` | `76b94d9678f3` | 2026-06-24 22:12:19 -0400 | gastown-cet.10 | `origin/polecat/obsidian/gastown-cet.1.2@mqswbee8~2` | gastown-cet.12.7 shard C | 0 |
| `ff7f3a36` | `a5f8c6829e70` | 2026-06-24 22:10:27 -0400 | gastown-cet.7 | `origin/polecat/obsidian/gastown-cet.1.2@mqswbee8~3` | gastown-cet.12.7 shard C + gastown-cet.12.14 shard E | 0 |
| `cfa97404` | `4b5b36952b75` | 2026-06-24 21:57:15 -0400 | gastown-cet.9 | `origin/polecat/obsidian/gastown-cet.1.2@mqswbee8~6` | gastown-cet.12.7 shard C | 0 |

### What the source branches show

Every affected commit can be traced to a polecat branch in `refs/remotes/origin/polecat/*`.
Some originals have been folded into cleanup/stacked branches, but none appear to
be stray commits authored directly on `main`. This means the commits were produced
through the normal polecat workflow, but the **merge to main bypassed the
Refinery gate** rather than going through `gt done` → Refinery → strict-core review.

### Retro-gate coverage status

- **Covered by existing or in-progress retro-gate shards:** 16 / 17
  - `gastown-cet.12.4`: `62c64b64` (audit deliverable)
  - `gastown-cet.12.7` shard C: 11 commits
  - `gastown-cet.12.8` shard D: 4 commits
  - `gastown-cet.12.14` shard E: in progress, covers `6faf2e93`, `09880751`, `a57c457d`, `6ae7a4c5`, `ff7f3a36`
- **No retro-gate coverage:** 1 / 17 — `8efcb5b1` (`gastown-cet.16.2`)

---

## 5. Push-path / enforcement assessment

### 5.1 GitHub branch protection

```bash
gh api repos/0xzr/gastown/branches/main --jq '.protection'
```

Result:

```json
{"enabled": false, "required_status_checks": {"checks": [], "contexts": [], "enforcement_level": "off"}}
```

**Conclusion:** `origin/main` on GitHub has **no branch protection**. An account
with push access can fast-forward `main` directly without any gate, PR, or status check.

### 5.2 GitHub pull requests

```bash
gh pr list --repo 0xzr/gastown --state all --limit 100
```

Result: **zero PRs**. The repository is not using the GitHub PR merge path.

### 5.3 Local authoritative bare repo hook state

The local mirror used by the Refinery is at `/home/ubuntu/gt-town/gastown/.repo.git`.

```bash
git -C /home/ubuntu/gt-town/gastown/.repo.git config core.hookspath
# → .githooks

ls -la /home/ubuntu/gt-town/gastown/.repo.git/.githooks
# → No such file or directory
```

The configured hook path does not exist, and the only `pre-receive` file present
is a sample. **There is no active pre-receive hook enforcing the deterministic
gate on the local authoritative repo either.**

### 5.4 Gate-hook telemetry

`/home/ubuntu/gt-town/.runtime/gate-hook-telemetry/gate-hook-20260625.jsonl` shows
the deterministic pre-receive hook running only until 2026-06-25T00:12:55-04:00,
and the events reference SHAs that are **not** in the current `origin/main`
history. After that point the hook telemetry is silent, while commits continued
landing on `origin/main`. This is consistent with the deterministic hook also
being bypassed by direct pushes.

### 5.5 HMAC attestation tokens

`/home/ubuntu/.gt-gate-attestations/` contains no tree-hash token for any of the
17 commits (and none for any restart-window commit). This is expected: tokens are
written only by `refinery-gate.sh` on a successful strict-core review. No gate
run → no token.

### 5.6 Reflog

The local `origin/main` reflog entries for the window all read `update by push`;
they carry no actor or merge-message detail. The local bare repo reflog has only
seven entries and cannot reliably distinguish Refinery merges from direct pushes.
The GitHub side (no branch protection, no PRs) is the stronger signal.

---

## 6. Root-cause conclusion

The 17 commits were produced on polecat branches but were **merged to main
through a path that did not invoke `refinery-gate.sh`**. The most likely
mechanism is a direct `git push origin main` (or equivalent fast-forward update)
while GitHub branch protection was disabled. There is no evidence that the
Refinery strict-core gate ran on these trees.

Current enforcement status:

| Control | Status |
|---------|--------|
| GitHub branch protection on `main` | **Disabled** |
| GitHub PRs used for merge | **None** |
| Local bare repo active pre-receive hook | **Missing** (`.githooks` absent) |
| Refinery telemetry for these commits | **None** |
| HMAC attestation tokens for these commits | **None** |

**Gate enforcement is therefore not active on all merge paths.** A direct push
to `origin/main` bypasses both the deterministic pre-receive gate and the
strict-core refinery gate.

---

## 7. Required actions and follow-ups

1. **Patch the gate hook / enforcement**
   - Enable GitHub branch protection for `main`, or
   - Install and activate the local pre-receive gate hook so that every push to
the authoritative repo is gated, and
   - Ensure the pre-receive hook checks the refinery HMAC attestation so that a
merge cannot land without the strict-core 4-model review being recorded.

   This work is tracked in `gastown-66y` and `gastown-wpf` already; the root
cause identified here (branch protection disabled + no active local hook) should
be added to those beads.

2. **Add automated bypass detection**
   - A periodic patrol or post-push hook should compare new `origin/main`
commits against `/home/ubuntu/gt-town/.runtime/refinery-telemetry/` and
`/home/ubuntu/.gt-gate-attestations/`.
   - If a non-WIP commit lands without a corresponding `gate_complete`
`status=merge` event and HMAC tree token, file an audit bead automatically.
   - This is the concrete detection requested by `gastown-a37`. Implementation
is outside the current code repo (it touches the runtime gate/hook system); a
new follow-up bead should be filed for the implementation work.

3. **Complete retro-gate coverage**
   - 16 of the 17 no-telemetry commits are already covered by shards C/D/E or by
`gastown-cet.12.4`.
   - `8efcb5b1` (`gastown-cet.16.2`) is **not** in any shard and should be added
to `gastown-cet.12.14` shard E or a new follow-up shard.
   - The audit deliverables (`62c64b64`, `17a1f47b`, `0c43863a`, `a7547605`)
have companion design notes / shard reports and can be confirmed via external
evidence once the deterministic-gate false positive (`gastown-m2h`) is resolved.

---

## 8. Artifact paths

- Telemetry: `/home/ubuntu/gt-town/.runtime/refinery-telemetry/refinery-gate-202606{23,24,25}.jsonl`
- Gate logs: `/home/ubuntu/gt-town/.runtime/refinery-gate-logs/`
- Hook telemetry: `/home/ubuntu/gt-town/.runtime/gate-hook-telemetry/gate-hook-202606{23,24,25}.jsonl`
- Attestations: `/home/ubuntu/.gt-gate-attestations/`
- Local bare repo: `/home/ubuntu/gt-town/gastown/.repo.git`
- This report: `docs/audits/gastown-a37/investigation.md`
- Parent proof table: `gastown-jkm` DESIGN field
- Related retro-gate beads: `gastown-cet.12.4`, `gastown-cet.12.7`, `gastown-cet.12.8`, `gastown-cet.12.14`, `gastown-wpf`, `gastown-66y`
