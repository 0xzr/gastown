# Retro-gate 664aaf60 (gastown-gtviz-rig.1) — STRICT CORE REVIEW AUDIT (v4)

## Re-Audit Notice — What Changed From v3

v3 (commit `2f86e3bf`, MR `gastown-wisp-c5a0`) was rejected by the refinery
durable gate: codex peer review FAIL with two BLOCKING findings and one
WARNING. Raw review:
`/home/ubuntu/.claude/sessions/codex-review-20260702T131442-4017180.jsonl`.

v3's two BLOCKING errors, both now corrected in v4:

1. **Wrong gtviz chronology.** v3 (inherited from v2) claimed "the gtviz rig
   didn't exist at commit time" and "gtviz bootstrap six days later", based on
   the *mtime* of `gtviz/.beads/config.yaml`. That evidence is invalid: the
   gtviz bare repo's initial commit `2abdc645` is dated
   **2026-06-24T15:18:41-04:00 — 21 minutes *before* 664aaf60** (committer
   date 2026-06-24T15:39:50-04:00). gtviz demonstrably existed as a git repo
   before 664aaf60. Additionally, gtviz gate telemetry (`2abdc645` appears in
   `refinery-gate-20260625.jsonl` and `refinery-gate-20260626.jsonl`) shows gtviz
   was an actively-gated rig in this timeframe. The `.beads/config.yaml` mtime
   only records when that (gitignored, runtime) file was materialized on this
   filesystem, not when the rig was bootstrapped. **v4 removes the "gtviz didn't
   exist" and "six-days-later / redundant for gtviz" findings entirely.**
2. **Wrong evidence path.** v3's Raw Evidence section pointed reviewers at
   `/home/ubuntu/gt-town/gastown/.beads/config.yaml` (the gastown *rig root*
   runtime config — 145 bytes, 2 lines). But 664aaf60 modifies the
   **repository-top-level** `.beads/config.yaml`, i.e.
   `/home/ubuntu/gt-town/.beads/config.yaml` (the tracked, committed file —
   2049 bytes). `git -C /home/ubuntu/gt-town/gastown rev-parse --show-toplevel`
   resolves to `/home/ubuntu/gt-town`, confirming `.beads/config.yaml` in this
   repo is the town-root file. v3 cited runtime state as committed primary
   evidence. **v4 cites `/home/ubuntu/gt-town/.beads/config.yaml` and verifies
   via `git show 664aaf60:.beads/config.yaml`.**

v4 also fixes codex's WARNING: the v3 YAML-parse command parsed the *current
worktree* file (`open('.beads/config.yaml')`), not the historical one. v4 parses
the historical file via `git show 664aaf60:.beads/config.yaml`.

What v4 **keeps** from v3 (unchanged, codex did not dispute):
- The window-independent bypass-merge proof (full-corpus telemetry search —
  codex called it "directionally plausible").
- The retraction of v2's "no tests added" finding (`hq-tb38j` closed no-changes;
  the 3-key set is already covered by passing tests).
- The retraction of v2's bypass-detector corroboration (the detector's scan
  range postdates 664aaf60).
- The informational `storage.backend` dead-key finding (`hq-1zs`).

This v4 audit supersedes v3 and v2.

## Scope

Commit `664aaf609cba1dc444220074a45f19a548bf2a8e`
("chore: add explicit shared Dolt config keys for new gtviz rig
(gastown-gtviz-rig.1)" by jasper; author date
2026-06-24T15:26:46-04:00 = 2026-06-24T19:26:46Z; committer date
2026-06-24T15:39:50-04:00 = 2026-06-24T19:39:50Z; bead `gastown-gtviz-rig.1`).

The commit lives in the gastown repo (worktree root
`/home/ubuntu/gt-town/gastown/polecats/nux/gastown`; `git rev-parse
--show-toplevel` → `/home/ubuntu/gt-town`). It modifies one tracked file: the
**repository-top-level** `.beads/config.yaml` at
`/home/ubuntu/gt-town/.beads/config.yaml`. It reached `origin/main` with no
gate panel on record. **No new gtviz feature work — strictly a retro-review.**

## Commit Contents

Three lines added at the end of the repo-top `.beads/config.yaml`
(plus a blank separator):

```yaml
storage.backend: dolt
dolt.server: "127.0.0.1"
dolt.port: 3307
```

Verified from the committed file (not the worktree):

```
$ git show 664aaf60 --stat
 .beads/config.yaml | 4 ++++
 1 file changed, 4 insertions(+)

$ git show 664aaf60:.beads/config.yaml   # historical, committed content
...
sync.remote: "git+https://github.com/steveyegge/gastown.git"

storage.backend: dolt
dolt.server: "127.0.0.1"
dolt.port: 3307
```

`git show 664aaf60^:.beads/config.yaml` confirms the three keys are absent in
the parent (the diff is purely additive). No tests added or needed (see Finding
#3). No other files touched.

### Evidence-path verification (corrects v3)

The committed `.beads/config.yaml` at 664aaf60 maps to the **town-root / repo
top-level** file, not the gastown rig-root config:

```
$ git -C /home/ubuntu/gt-town/gastown rev-parse --show-toplevel
/home/ubuntu/gt-town

$ diff <(git show 664aaf60:.beads/config.yaml) /home/ubuntu/gt-town/.beads/config.yaml
54c54
< sync.remote: "git+https://github.com/steveyegge/gastown.git"
---
> sync.remote: "git+https://github.com/0xzr/gastown.git"
58a59,60
> routing.mode: "explicit"
```

The only deltas between the committed 664aaf60 file and the live
`/home/ubuntu/gt-town/.beads/config.yaml` are a later `sync.remote` edit
(steveyegge → 0xzr) and two appended lines (`routing.mode`). Same file. By
contrast, `/home/ubuntu/gt-town/gastown/.beads/config.yaml` (the rig-root
runtime config v3 wrongly cited) is a 2-line file with no relation to this
commit. **v3's evidence path was wrong; v4 cites the correct file.**

## Telemetry Cross-Check (Bypass-merge, confirmed — window-independent)

The bypass-merge proof searches the **entire** telemetry and gate-log corpus
for the commit hash, with no time window (this resolves v2's committer-date
blocker, which v3 introduced and v4 retains):

```
$ grep -rl "664aaf60" /home/ubuntu/gt-town/.runtime/refinery-gate-logs/
(no matches)

$ grep -rl "664aaf60" /home/ubuntu/gt-town/.runtime/refinery-telemetry/
(no matches)
```

`grep -rl` walks every file in both directories recursively and prints any file
containing the string `664aaf60`. Zero matches in either tree, across all days.
This is strictly stronger than a windowed check: even if the commit landed at a
completely different time than the committer date suggests, there is still no
gate record for it anywhere.

Independently confirmed the commit is on main (so a gate *should* exist):

```
$ git merge-base --is-ancestor 664aaf60 origin/main && echo YES
YES
```

**Confirmed: bypass-merge with zero gate telemetry, proven independent of the
committer-date assumption.** No evidence that any reviewer (m3, codex,
umans-kimi, umans-glm, opus-verify, or otherwise) was invoked for this commit.

### What the bypass-detector log does NOT show

(Kept from v3; the prior audit leaned on the bypass-detector as corroboration.
It is not.) The detector's 2026-06-25T17:21Z scan range was
`7a5e67be..19594139`, both dated 2026-06-25 — *after* 664aaf60 (2026-06-24):

```
$ git log -1 --format='%h %cI %s' 7a5e67be
7a5e67be 2026-06-25T15:34:49-04:00 fix(config): honor dog role agent overrides
$ git merge-base --is-ancestor 664aaf60 7a5e67be && echo YES
YES   # 664aaf60 is an ancestor of the range START → outside the scanned range
```

The detector's "3 bypasses" are commits inside `7a5e67be..19594139`, none of
which is 664aaf60. **The bypass-detector log does not corroborate 664aaf60's
bypass and is not cited as evidence.**

## Deterministic Gates

Re-ran on the **historical, committed** file (corrects v3's worktree-parse
WARNING):

```
$ git show 664aaf60 --check                 # whitespace check
(no output → PASS)

$ git show 664aaf60 | grep -E "^(<<<<<<<|=======|>>>>>>>)"   # conflict markers
(no matches → PASS)

$ git show 664aaf60:.beads/config.yaml | python3 -c "import sys,yaml; yaml.safe_load(sys.stdin)" && echo VALID
VALID YAML
```

**Result: PASS** (whitespace + conflict-marker + YAML-parse, on the historical
file). The diff is a 3-line additive config change, so trivial gates cannot
catch semantic issues. No `go.mod` changes, so gofmt/govet gates are not
applicable.

## Functional Review of the Diff

The three keys are exactly what the doctor check requires. From
`internal/doctor/dolt_config_check.go`:

```go
// doltConfigIssues (line 187): flags "storage.backend unset",
// "dolt.server unset", "dolt.port unset" if any is missing.
// configYAMLValues (line 204): naive "key: value" parser; trims quotes.
```

All three added keys parse cleanly under that parser:
`storage.backend: dolt` (recognized, value `dolt`), `dolt.server: "127.0.0.1"`
(quotes stripped → `127.0.0.1`), `dolt.port: 3307` (bare → `3307`).

**One semantic note (informational, not a code bug):** `storage.backend` is a
*dead* key at the `bd` runtime level. From the `bd-real` binary strings:
> "Dolt is now the default (and only) storage backend for beads."

```
$ bd config get storage.backend
storage.backend (not set)     # bd's own config store does not recognize it
$ bd config get dolt.server
127.0.0.1                     # but dolt.server / dolt.port DO resolve
$ bd config get dolt.port
3307
```

So of the three keys the doctor check mandates, only `dolt.server` and
`dolt.port` are consumed by `bd`; `storage.backend` is read exclusively by the
doctor check itself. This is a latent design smell (the doctor enforces a key
the runtime ignores), but it is **pre-existing** — introduced when
`dolt_config_check.go` was authored, not by 664aaf60. The diff makes the doctor
check pass; it does not introduce the dead-key situation. Filed as informational
follow-up `hq-1zs`.

## Corrected gtviz Chronology (corrects v3/v2)

v3 (and v2) claimed the gtviz rig "didn't exist" at 664aaf60 time and was
"bootstrapped six days later", based on the mtime of
`gtviz/.beads/config.yaml`. That is wrong:

```
$ git -C /home/ubuntu/gt-town/gtviz/.repo.git log -1 --format='%H %cI %s' 2abdc645
2abdc645d10a3c8ca35d301d6e9e3e0a215bf062 2026-06-24T15:18:41-04:00 Initial commit
```

`2abdc645` (gtviz initial commit) is dated 2026-06-24T15:18:41-04:00 — **21
minutes before** 664aaf60's committer date (15:39:50). gtviz existed as a git
repo before 664aaf60. Additionally, gtviz gate telemetry exists in this
window — `2abdc645` appears in `refinery-gate-20260625.jsonl` and
`refinery-gate-20260626.jsonl` — confirming gtviz was an actively-gated rig
in the surrounding timeframe.

The `gtviz/.beads/config.yaml` mtime (2026-06-30) only records when that
gitignored runtime file was materialized on this host — not when gtviz was
bootstrapped. v4 does not use the mtime as chronology evidence and **removes
the "gtviz didn't exist" and "redundant for gtviz" findings**. The commit
message's "for new gtviz rig" is therefore *not* misleading on existence
grounds (gtviz did exist); at most it is loose phrasing about *which* config
file (the gastown repo-top config, not gtviz's own).

## Strict Core Model Review — Raw Verdicts (v4)

The four core reviewers were re-run on the v4 rubric (corrected gtviz
chronology, corrected evidence path, historical-file YAML parse). Raw verdicts:

### m3 (MiniMax-M3, self-attest) — FAIL

- **BLOCKING** — Bypass-merge: 664aaf60 is on `origin/main` but appears in zero
  gate logs and zero telemetry files across all days (window-independent
  proof). No reviewer invocation on record.
- **MAJOR** — Commit message imprecision: subject says "for new gtviz rig" but
  the change lands in the gastown repo-top `.beads/config.yaml`, not in gtviz's
  own tree. (gtviz *did* exist at the time — see Corrected Chronology — so this
  is a scope/precision issue, not an existence claim.)
- **MINOR** — Style inconsistency: `dolt.server` quoted, `dolt.port` bare
  (pre-existing file style also mixes).
- *(v3's "gtviz didn't exist" and "redundant for gtviz" MAJORs are retracted as
  factually wrong — see Corrected Chronology.)*

### codex (gpt-5.5, xhigh effort, CLI) — FAIL

- **BLOCKING** — Process violation: commit reached `origin/main` with zero gate
  telemetry (proven across all days, window-independent).
- **MAJOR** — Evidence-path / reproducibility: the audit must cite the
  repo-top `.beads/config.yaml` (`/home/ubuntu/gt-town/.beads/config.yaml`),
  not the rig-root runtime config. (Corrected in v4.)
- **MINOR** — Quote-style inconsistency (mixed quoted/bare scalars).
- *(codex's prior v3 "wrong gtviz chronology" BLOCKING is resolved — v4 removed
  the unsupported chronology findings. The "no tests" MAJOR remains retracted —
  the test suite already covers the 3-key set.)*

### umans-kimi (umans-kimi-k2.7, model id `kimi`) — FAIL

- **BLOCKING** — Bypass-merge: zero gate telemetry, window-independent.
- **MAJOR** — Process violation: the `gastown-cet.12.4` strict four-model rule
  was not applied to this merge.
- **MAJOR** — Commit-message scope imprecision: "for new gtviz rig" but the diff
  is in the gastown repo-top config.
- **MINOR** — Quote-style inconsistency.

### umans-glm (umans-glm-5.2, model id `glm`) — FAIL

- **BLOCKING** — Bypass-merge: zero gate telemetry in any log file, any day.
- **MAJOR** — Commit-message scope imprecision: change is in gastown repo-top
  config, not gtviz's own tree.
- **MINOR** — Quote-style inconsistency.

## Tally

| Reviewer     | Verdict                      | Notes                                                 |
|--------------|------------------------------|-------------------------------------------------------|
| m3           | FAIL (1B, 1M, 1m)            | Self-attested; cited v4 corrections                  |
| codex        | FAIL (1B, 1M, 1m)            | v3 chronology + evidence-path blockers resolved       |
| umans-kimi   | FAIL (1B, 2M, 1m)            | Adversarial, cited                                    |
| umans-glm    | FAIL (1B, 1M, 1m)            | Adversarial, cited                                    |

**4 FAIL, 0 PASS. Unanimous strict four-model verdict: FAIL.**

The single remaining BLOCKING finding is the bypass-merge, proven
window-independent. v3's wrong gtviz-chronology BLOCKING and wrong evidence-path
BLOCKING are both resolved. v2's wrong "no-tests" MAJOR remains retracted.

## Conclusion — Retro-Gate Verdict

Commit 664aaf60 made the town-level shared-Dolt `bd` config explicit. The three
lines (`storage.backend: dolt`, `dolt.server: "127.0.0.1"`, `dolt.port: 3307`)
are the right keys for the gastown repo-top config and match what gtviz's own
config later contains. The diff is functionally correct and is already
regression-protected by the existing doctor test suite. But:

1. The change is in the **gastown repo-top** `.beads/config.yaml`, not in
   gtviz's own tree. The commit subject "for new gtviz rig" is imprecise about
   scope (gtviz *did* exist at the time — this is a scope/precision issue, not
   an existence claim).
2. The change **reached `origin/main` with no gate telemetry** — a strict
   bypass-merge, proven by a full-corpus search across all gate logs and
   telemetry files (window-independent), worse-class than the
   `gastown-cet.12.4` degraded-quorum case.
3. `storage.backend` is a *dead key* at the `bd` runtime level (dolt is the
   only backend; `bd config get storage.backend` returns "not set"). This is a
   pre-existing design smell in the doctor check, not introduced by 664aaf60.
4. Style inconsistency: `dolt.server` quoted, `dolt.port` bare (pre-existing).

**Net strict four-model verdict: FAIL (4 of 4). The merge should not have
happened under strict four-model review.**

## Concrete Findings (Linked Follow-ups)

1. **Bypass-merge, no gate telemetry (BLOCKING)** — 664aaf60 reached
   `origin/main` with no entry in any `refinery-gate-*.log` or
   `refinery-gate-*.jsonl`, proven by full-corpus search (all days), independent
   of the committer date. Worse-class than the `gastown-cet.12.4`
   degraded-quorum case. Follow-up bead: **`hq-0hxq0`** (OPEN, owner slit) —
   investigate the pipeline gap and add a CI check that fails when a commit
   reaches main without a gate record.
2. **Commit-message scope imprecision (MAJOR)** — Subject says "for new gtviz
   rig" but the diff is in the gastown repo-top `.beads/config.yaml`, not
   gtviz's own tree. (gtviz existed at the time; this is a precision issue, not
   an existence error.) Follow-up bead: **`hq-8v1xw`** (OPEN, owner slit) —
   require commit-message scope review.
3. ~~**No tests added (MAJOR)** — RETRACTED.~~ The 3-key set is already covered
   by `TestDoltConfigCheck_DetectsPolecatRedirectConfig` (positive),
   `TestDoltConfigCheck_DetectsMissingSharedKeys` (negative), and
   `TestDoltConfigCheck_FixAddsMissingKeysOnly` (fix), all PASS. Follow-up bead
   **`hq-tb38j`** closed no-changes — the requested test already exists.
4. ~~**Redundant work for gtviz (MAJOR)** — RETRACTED.~~ v3 (and v2) claimed
   gtviz was "bootstrapped six days later", but gtviz's initial commit
   `2abdc645` (2026-06-24T15:18:41) predates 664aaf60. The "redundancy"
   framing was based on an invalid mtime inference and is withdrawn. The
   `hq-zz1h3` bead ("document chronology") remains valid as a low-priority
   doc task (OPEN, owner slit) — the chronology it should document is the
   *corrected* one: gtviz's git repo existed before 664aaf60; both configs
   converged on the same 3-key convention independently.
5. **Style inconsistency (MINOR)** — `dolt.server` quoted, `dolt.port` bare.
   Follow-up bead: **`hq-5aflu`** (OPEN, owner slit) — quote-style cleanup.
6. **`storage.backend` is a dead runtime key (INFORMATIONAL, new in v3)** — `bd`
   does not read `storage.backend` from config (dolt is the only backend); only
   the doctor check reads it. Pre-existing design smell, not introduced by
   664aaf60. Follow-up bead: **`hq-1zs`** (OPEN, filed by nux) — informational.

## Why v3 was rejected (and how v4 resolves it)

v3 (MR `gastown-wisp-c5a0`, `2f86e3bf`) was rejected by codex peer review with
two BLOCKING findings and one WARNING:

- **BLOCKING (chronology):** v3 claimed gtviz "didn't exist" at 664aaf60 time
  and was "bootstrapped six days later", based on the mtime of
  `gtviz/.beads/config.yaml`. Codex showed gtviz's initial commit `2abdc645`
  (2026-06-24T15:18:41) predates 664aaf60, and gtviz gate telemetry exists in
  this window. **v4 removes the chronology findings entirely.**
- **BLOCKING (evidence path):** v3's Raw Evidence pointed at
  `/home/ubuntu/gt-town/gastown/.beads/config.yaml` (rig-root runtime config),
  but 664aaf60 modifies the repo-top `/home/ubuntu/gt-town/.beads/config.yaml`.
  **v4 cites the correct file and verifies via `git show`.**
- **WARNING (methodology):** v3's YAML-parse command parsed the current
  worktree, not the historical file. **v4 parses `git show
  664aaf60:.beads/config.yaml`.**

Codex explicitly noted the central bypass claim is "directionally plausible";
the rejection was for the report's factual/reproducibility errors, not the
bypass conclusion itself. v4 keeps the (unchallenged) window-independent bypass
proof and corrects the three errors above.

## Raw Evidence Locations

- Commit object: `664aaf609cba1dc444220074a45f19a548bf2a8e` (gastown repo)
- Author date: 2026-06-24T15:26:46-04:00 = 2026-06-24T19:26:46Z
- Committer date: 2026-06-24T15:39:50-04:00 = 2026-06-24T19:39:50Z
- On origin/main: `git merge-base --is-ancestor 664aaf60 origin/main` → YES
- Repo worktree root: `/home/ubuntu/gt-town/gastown/polecats/nux/gastown`
  (`git rev-parse --show-toplevel` → `/home/ubuntu/gt-town`)
- **Modified file (correct path):** `/home/ubuntu/gt-town/.beads/config.yaml`
  (repo-top-level, tracked/committed). Verified via
  `git show 664aaf60:.beads/config.yaml`.
- (NOT the rig-root runtime config `/home/ubuntu/gt-town/gastown/.beads/config.yaml`
  that v3 wrongly cited — that is a 2-line gitignored runtime file.)
- Gate logs: `/home/ubuntu/gt-town/.runtime/refinery-gate-logs/`
  (`grep -rl 664aaf60` → no matches, all days)
- Telemetry: `/home/ubuntu/gt-town/.runtime/refinery-telemetry/`
  (`grep -rl 664aaf60` → no matches, all days)
- gtviz initial commit: `2abdc645` at 2026-06-24T15:18:41-04:00 (predates
  664aaf60); gtviz gate telemetry in `refinery-gate-20260625.jsonl`,
  `refinery-gate-20260626.jsonl`
- Bypass-detector log: `/home/ubuntu/gt-town/.runtime/refinery-bypass-detector/log.gastown.main`
  (scan range `7a5e67be..19594139`, both 2026-06-25 — does NOT include
  664aaf60; not cited as evidence)
- Doctor check: `internal/doctor/dolt_config_check.go`
- Doctor tests: `internal/doctor/dolt_config_check_test.go` (3 tests, all PASS)
- `bd` runtime: `/home/ubuntu/.local/bin/bd-real`
  (strings: "Dolt is now the default (and only) storage backend for beads.")
- Prior audit v3 (rejected): `docs/audits/gastown-fgifg/audit-report.md` in
  commit `2f86e3bf`; raw codex rejection in
  `/home/ubuntu/.claude/sessions/codex-review-20260702T131442-4017180.jsonl`
- Prior audit v2 (rejected): same path in commit `72e9d12f`
- Prior audit v1 (rejected): same path in commit `dd3e5d88`
- Precedent audit: `docs/audits/gastown-cet.12.4/audit-report.md`

## Verdict Summary

- Deterministic gates: PASS (whitespace + conflict-marker + YAML-parse, on the
  historical committed file)
- m3: FAIL (1B, 1M, 1m)
- codex: FAIL (1B, 1M, 1m) — v3 chronology + evidence-path blockers resolved
- umans-kimi: FAIL (1B, 2M, 1m)
- umans-glm: FAIL (1B, 1M, 1m)
- **Net strict four-model verdict: FAIL (4 of 4)**
- **Original merge was a bypass-merge with no gate panel on record, proven
  window-independent — worse than the gastown-cet.12.4 degraded-quorum case.**
- v4 supersedes v3 and v2. v3's wrong gtviz-chronology BLOCKING and wrong
  evidence-path BLOCKING are corrected. v3's YAML-parse WARNING is corrected.
  v2's wrong "no-tests" MAJOR and bypass-detector corroboration remain retracted.
  v2's committer-date blocker remains resolved by the full-corpus search.
