# Retro-gate 664aaf60 (gastown-gtviz-rig.1) — STRICT CORE REVIEW AUDIT (v3)

## Re-Audit Notice — What Changed From v2

v2 (commit `72e9d12f`, MR `gastown-wisp-hdwh`) was replay-rejected by the
refinery durable gate: the gate judged the tree identical to the prior rejected
candidate `c789d6a7` and replayed the codex peer-review failure recorded in
`/home/ubuntu/.claude/sessions/codex-review-20260702T104235-1856498.jsonl`.

That codex review's single BLOCKING finding was that v2's bypass-merge
conclusion rested on an unsupported assumption: **the committer date
(2026-06-24T19:39:50Z) was treated as proof of when the commit reached
`origin/main`, then the bypass was "proven" by checking only the
19:10Z–19:59Z gate window.** A committer date proves when the commit object was
created, not when it was pushed/merged/landed.

v3 fixes this by **broadening the telemetry search to ALL days** instead of a
single window derived from the committer date. The bypass conclusion no longer
depends on the committer-date-as-landing-time assumption. See Telemetry below.

v3 also corrects two v2 findings that the codex review did not flag but which
are factually wrong:

1. **v2 finding #3 / `hq-tb38j` ("no tests added") is WRONG.** The doctor check
   that requires these keys (`internal/doctor/dolt_config_check.go`) already has
   tests in `dolt_config_check_test.go` exercising exactly this 3-key set — a
   positive case (`TestDoltConfigCheck_DetectsPolecatRedirectConfig`, line 46),
   a negative case (`TestDoltConfigCheck_DetectsMissingSharedKeys`, line 28),
   and a Fix case (`TestDoltConfigCheck_FixAddsMissingKeysOnly`, line 95). All
   three PASS. The key set is already regression-protected. `hq-tb38j` should
   be closed no-changes.
2. **v2 cited the bypass-detector log as if it corroborated 664aaf60's bypass.
   It does not.** The detector's 2026-06-25T17:21Z scan range was
   `7a5e67be..19594139`, both of which are dated 2026-06-25 — i.e. *after*
   664aaf60 (2026-06-24). 664aaf60 is an ancestor of the range *start*
   (`git merge-base --is-ancestor 664aaf60 7a5e67be` → YES), so it is NOT in
   the scanned range. The "3 bypasses" the detector found are unrelated
   commits. v3 does not claim detector corroboration.

This v3 audit supersedes v2. Raw codex rejection:
`/home/ubuntu/.claude/sessions/codex-review-20260702T104235-1856498.jsonl`.

## Scope

Commit `664aaf609cba1dc444220074a45f19a548bf2a8e`
("chore: add explicit shared Dolt config keys for new gtviz rig
(gastown-gtviz-rig.1)" by jasper; author date
2026-06-24T15:26:46-04:00 = 2026-06-24T19:26:46Z; committer date
2026-06-24T15:39:50-04:00 = 2026-06-24T19:39:50Z; bead `gastown-gtviz-rig.1`).

The commit lives in the gastown main repo. It modifies one file,
`.beads/config.yaml`. It reached `origin/main` with no gate panel on record.
**No new gtviz feature work — strictly a retro-review.**

## Commit Contents

Three lines added at the end of `.beads/config.yaml` (plus a blank separator):

```yaml
storage.backend: dolt
dolt.server: "127.0.0.1"
dolt.port: 3307
```

`git show 664aaf60 --stat` confirms one file changed, 4 lines added. No tests
added or needed (see Finding #3 correction). No other files touched.

## Telemetry Cross-Check (Bypass-merge, confirmed — window-independent)

The v2 blocker was that its no-telemetry proof depended on the committer date.
v3 instead searches the **entire** telemetry and gate-log corpus for the commit
hash, with no time window:

```
$ grep -rl "664aaf60" /home/ubuntu/gt-town/.runtime/refinery-gate-logs/
(no matches)

$ grep -rl "664aaf60" /home/ubuntu/gt-town/.runtime/refinery-telemetry/
(no matches)
```

`grep -rl` walks every file in both directories recursively and prints any file
containing the string `664aaf60`. Zero matches in either tree, across all days.
This is strictly stronger than v2's windowed check: even if the commit landed
at a completely different time than the committer date suggests, there is still
no gate record for it anywhere.

Independently confirmed the commit is on main (so a gate *should* exist):

```
$ git merge-base --is-ancestor 664aaf60 origin/main && echo YES
YES
```

For completeness, the 2026-06-24 gate-log directory (the date the commit object
was created) shows a gap around the committer date, consistent with — but not
required for — the conclusion:

```
$ ls /home/ubuntu/gt-town/.runtime/refinery-gate-logs/ | grep 20260624T19
20260624T191004Z-9390a039-validate.log
20260624T195912Z-0867b80d-validate.log
```

**Confirmed: bypass-merge with zero gate telemetry, proven independent of the
committer-date assumption.** No evidence that any reviewer (m3, codex,
umans-kimi, umans-glm, opus-verify, or otherwise) was invoked for this commit.

### What the bypass-detector log does NOT show

The prior audit leaned on the bypass-detector as corroboration. It is not:

```
$ cat /home/ubuntu/gt-town/.runtime/refinery-bypass-detector/log.gastown.main
[2026-06-25T17:21:51-04:00] start rig=gastown branch=main remote=origin max_scan=3 fix=0 dry_run=1
[2026-06-25T17:21:51-04:00] using git dir: /home/ubuntu/gt-town/gastown/.repo.git
[2026-06-25T17:21:51-04:00] tip from --reset-state: origin/main
[2026-06-25T17:21:52-04:00] scanning 3 commits in 7a5e67be..19594139
[2026-06-25T17:21:53-04:00] summary bypasses=3 wip=0 attested=0 incomplete=0
[2026-06-25T17:21:53-04:00] dry-run: not updating state file
```

The scan range bounds are both 2026-06-25:

```
$ git log -1 --format='%h %cI %s' 7a5e67be
7a5e67be 2026-06-25T15:34:49-04:00 fix(config): honor dog role agent overrides
$ git log -1 --format='%h %cI %s' 19594139
19594139 2026-06-25T17:07:01-04:00 fix(scheduler): drop 'gt patrol' substring ...
```

664aaf60 (2026-06-24) predates the range start and is not in it:

```
$ git merge-base --is-ancestor 664aaf60 7a5e67be && echo YES
YES   # 664aaf60 is an ancestor of the range START → outside the scanned range
```

The detector's "3 bypasses" are the three commits inside
`7a5e67be..19594139` (6fd1b635, 722af901, 19594139), none of which is 664aaf60.
**The bypass-detector log does not corroborate 664aaf60's bypass and is not
cited as evidence.** The bypass rests solely on the window-independent
telemetry search above.

## Deterministic Gates

```
$ git show 664aaf60 --check                 # whitespace check
(no output → PASS)

$ git show 664aaf60 | grep -E "^(<<<<<<<|=======|>>>>>>>)"   # conflict marker check
(no matches → PASS)

$ python3 -c "import yaml; yaml.safe_load(open('.beads/config.yaml'))" && echo VALID
VALID YAML
```

**Result: PASS** (whitespace + conflict-marker + YAML-parse). The diff is a
3-line additive config change, so trivial gates cannot catch semantic issues.
No `go.mod` changes, so gofmt/govet gates are not applicable.

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
follow-up (see Findings).

## Strict Core Model Review — Raw Verdicts (v3)

The four core reviewers were re-run on the v3 rubric (corrected facts,
window-independent telemetry proof, dropped the wrong "no-tests" and
detector-corroboration claims). Raw verdicts:

### m3 (MiniMax-M3, self-attest) — FAIL

- **BLOCKING** — Bypass-merge: 664aaf60 is on `origin/main` but appears in zero
  gate logs and zero telemetry files across all days (window-independent
  proof). No reviewer invocation on record.
- **MAJOR** — Misleading commit message: subject says "for new gtviz rig" but
  the change lands in the **gastown** root `.beads/config.yaml`. The gtviz rig
  did not exist at commit time (gtviz `.beads/config.yaml` mtime
  2026-06-30T19:58:43-04:00, six days later). The change actually repairs the
  gastown root's own `dolt-config` doctor-check, which the message does not say.
- **MAJOR** — Redundant for the gtviz rig: gtviz's later bootstrap independently
  wrote the same three keys, so this commit was a no-op for gtviz.
- **MINOR** — Style inconsistency: `dolt.server` quoted, `dolt.port` bare
  (pre-existing file style also mixes).

### codex (gpt-5.5, xhigh effort, CLI) — FAIL

- **BLOCKING** — Process violation: commit reached `origin/main` with zero gate
  telemetry (proven across all days, not just the committer-date window — the
  prior blocker is resolved).
- **MAJOR** — Commit message misleading: "for new gtviz rig" but the change is
  to gastown root config; the gtviz rig didn't exist at commit time.
- **MINOR** — Quote-style inconsistency (mixed quoted/bare scalars).
- *(codex's prior "no tests" MAJOR is dropped — the test suite already covers
  the 3-key set; see Finding #3 correction.)*

### umans-kimi (umans-kimi-k2.7, model id `kimi`) — FAIL

- **BLOCKING** — Bypass-merge: zero gate telemetry, window-independent.
- **MAJOR** — Misleading subject: "for new gtviz rig" but change is in gastown
  root; redundant for gtviz which bootstrapped the same keys six days later.
- **MAJOR** — Process violation: the `gastown-cet.12.4` strict four-model rule
  was not applied to this merge.
- **MINOR** — Quote-style inconsistency.

### umans-glm (umans-glm-5.2, model id `glm`) — FAIL

- **BLOCKING** — Bypass-merge: zero gate telemetry in any log file, any day.
- **MAJOR** — Misleading commit message: change is in gastown root config, not
  gtviz; gtviz bootstrap later wrote the same keys.
- **MINOR** — Quote-style inconsistency.

## Tally

| Reviewer     | Verdict                      | Notes                                                 |
|--------------|------------------------------|-------------------------------------------------------|
| m3           | FAIL (1B, 2M, 1m)            | Self-attested; cited v3 corrections                  |
| codex        | FAIL (1B, 1M, 1m)            | Prior blocker (committer-date proof) resolved        |
| umans-kimi   | FAIL (1B, 2M, 1m)            | Adversarial, cited                                    |
| umans-glm    | FAIL (1B, 1M, 1m)            | Adversarial, cited                                    |

**4 FAIL, 0 PASS. Unanimous strict four-model verdict: FAIL.**

The single remaining BLOCKING finding is the bypass-merge, now proven
independently of the committer-date assumption that blocked v2. v2's wrong
"no-tests" MAJOR is dropped; v2's detector-corroboration claim is retracted.

## Conclusion — Retro-Gate Verdict

Commit 664aaf60 made the town-level shared-Dolt `bd` config explicit. The three
lines (`storage.backend: dolt`, `dolt.server: "127.0.0.1"`, `dolt.port: 3307`)
are the right keys for the gastown root and match what gtviz would later get
from its bootstrap. The diff is functionally correct and is already
regression-protected by the existing doctor test suite. But:

1. The change is in the **gastown** root, not gtviz. The commit subject
   "for new gtviz rig" is misleading; the actual effect is to fix the gastown
   root's own `dolt-config` doctor-check.
2. The change is **redundant for the gtviz rig**, which bootstrapped six days
   later (2026-06-30T19:58:43-04:00) and independently wrote the same keys.
3. The change **reached `origin/main` with no gate telemetry** — a strict
   bypass-merge, proven by a full-corpus search across all gate logs and
   telemetry files (window-independent), worse-class than the
   `gastown-cet.12.4` degraded-quorum case.
4. `storage.backend` is a *dead key* at the `bd` runtime level (dolt is the only
   backend; `bd config get storage.backend` returns "not set"). This is a
   pre-existing design smell in the doctor check, not introduced by 664aaf60.
5. Style inconsistency: `dolt.server` quoted, `dolt.port` bare (pre-existing).

**Net strict four-model verdict: FAIL (4 of 4). The merge should not have
happened under strict four-model review.**

## Concrete Findings (Linked Follow-ups)

1. **Bypass-merge, no gate telemetry (BLOCKING)** — 664aaf60 reached
   `origin/main` with no entry in any `refinery-gate-*.log` or
   `refinery-gate-*.jsonl`, proven by full-corpus search (all days), independent
   of the committer date. Worse-class than the `gastown-cet.12.4`
   degraded-quorum case. Follow-up bead: **`hq-0hxq0`** (already OPEN,
   owner slit) — investigate the pipeline gap and add a CI check that fails
   when a commit reaches main without a gate record.
2. **Misleading commit message (MAJOR)** — Subject says "for new gtviz rig" but
   the diff is in gastown root `.beads/config.yaml`; the gtviz rig didn't exist
   at commit time. Follow-up bead: **`hq-8v1xw`** (already OPEN, owner slit) —
   require commit-message scope review.
3. ~~**No tests added (MAJOR)** — RETRACTED.~~ The 3-key set is already covered
   by `TestDoltConfigCheck_DetectsPolecatRedirectConfig` (positive),
   `TestDoltConfigCheck_DetectsMissingSharedKeys` (negative), and
   `TestDoltConfigCheck_FixAddsMissingKeysOnly` (fix), all PASS. Follow-up bead
   **`hq-tb38j`** should be **closed no-changes** — the requested test already
   exists.
4. **Redundant work for gtviz (MAJOR)** — gtviz bootstrap (2026-06-30) wrote
   the same three keys; this commit was a no-op for gtviz. Follow-up bead:
   **`hq-zz1h3`** (already OPEN, owner slit) — document the chronology.
5. **Style inconsistency (MINOR)** — `dolt.server` quoted, `dolt.port` bare.
   Follow-up bead: **`hq-5aflu`** (already OPEN, owner slit) — quote-style
   cleanup.
6. **`storage.backend` is a dead runtime key (INFORMATIONAL, new in v3)** — `bd`
   does not read `storage.backend` from config (dolt is the only backend); only
   the doctor check reads it. Pre-existing design smell, not introduced by
   664aaf60. No blocking action; noted for the doctor-check maintainer.

## Why v2 was replay-rejected (and how v3 resolves it)

The refinery durable gate replay-rejected v2 (`gastown-wisp-hdwh`,
branch `polecat/slit/gastown-fgifg@mr3l8hga` at `72e9d12`) because the tree was
identical to the prior rejected candidate `c789d6a7`, so it replayed the codex
peer-review failure. That codex review's BLOCKING finding was that v2's
bypass-merge proof depended on treating the committer date as the landing time
and checking only the 19:10Z–19:59Z window.

**v3 resolves it** by replacing the windowed check with a full-corpus search:
`grep -rl "664aaf60"` over the entire `refinery-gate-logs/` and
`refinery-telemetry/` trees returns zero matches. The bypass conclusion no
longer references the committer date at all. The codex blocker is satisfied.

v3 also drops v2's two other factual errors: the "no tests" finding (the tests
exist and pass) and the bypass-detector corroboration (the detector's scan
range postdates 664aaf60).

## Raw Evidence Locations

- Commit object: `664aaf609cba1dc444220074a45f19a548bf2a8e` (gastown main)
- Author date: 2026-06-24T15:26:46-04:00 = 2026-06-24T19:26:46Z
- Committer date: 2026-06-24T15:39:50-04:00 = 2026-06-24T19:39:50Z
- On origin/main: `git merge-base --is-ancestor 664aaf60 origin/main` → YES
- Gate logs: `/home/ubuntu/gt-town/.runtime/refinery-gate-logs/`
  (`grep -rl 664aaf60` → no matches, all days)
- Telemetry: `/home/ubuntu/gt-town/.runtime/refinery-telemetry/`
  (`grep -rl 664aaf60` → no matches, all days)
- Bypass-detector log: `/home/ubuntu/gt-town/.runtime/refinery-bypass-detector/log.gastown.main`
  (scan range `7a5e67be..19594139`, both 2026-06-25 — does NOT include
  664aaf60; not cited as evidence)
- Working tree (gastown root): `/home/ubuntu/gt-town/gastown/.beads/config.yaml`
- gtviz working-tree beads dir: `/home/ubuntu/gt-town/gtviz/.beads/`
  (config.yaml + metadata.json + dolt-server.port = 3307)
- gtviz bare git dir: `/home/ubuntu/gt-town/gtviz/.repo.git/` (no `.beads/`)
- Doctor check: `internal/doctor/dolt_config_check.go`
- Doctor tests: `internal/doctor/dolt_config_check_test.go` (3 tests, all PASS)
- `bd` runtime: `/home/ubuntu/.local/bin/bd-real`
  (strings: "Dolt is now the default (and only) storage backend for beads.")
- Prior audit v2 (rejected): `docs/audits/gastown-fgifg/audit-report.md` in
  commit `72e9d12f`; raw codex rejection in
  `/home/ubuntu/.claude/sessions/codex-review-20260702T104235-1856498.jsonl`
- Prior audit v1 (rejected): same path in commit `dd3e5d88`
- Precedent audit: `docs/audits/gastown-cet.12.4/audit-report.md`

## Verdict Summary

- Deterministic gates: PASS (whitespace + conflict-marker + YAML-parse)
- m3: FAIL (1B, 2M, 1m)
- codex: FAIL (1B, 1M, 1m) — prior committer-date blocker resolved
- umans-kimi: FAIL (1B, 2M, 1m)
- umans-glm: FAIL (1B, 1M, 1m)
- **Net strict four-model verdict: FAIL (4 of 4)**
- **Original merge was a bypass-merge with no gate panel on record, proven
  window-independent — worse than the gastown-cet.12.4 degraded-quorum case.**
- v3 supersedes v2. v2's wrong "no-tests" MAJOR is retracted; v2's
  bypass-detector corroboration is retracted. v2's committer-date blocker is
  resolved by the full-corpus telemetry search.
