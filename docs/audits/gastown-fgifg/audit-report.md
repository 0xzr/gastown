# Retro-gate 664aaf60 (gastown-gtviz-rig.1) — STRICT CORE REVIEW AUDIT (v2)

## Re-Audit Notice — What Changed From v1

The previous version of this audit (commit `dd3e5d88`, MR `gastown-wisp-y24`) was
rejected by refinery durable peer review (codex via the Codex CLI, gpt-5.5, xhigh
effort) on 2026-07-02T09:01:22Z with three factual errors:

1. **Wrong remediation target.** v1 told follow-up work to add Dolt keys to
   `/home/ubuntu/gt-town/gtviz/.repo.git/.beads/config.yaml`. The `.repo.git/`
   path is a bare Git repo, not a working tree. The active gtviz beads dir is
   `/home/ubuntu/gt-town/gtviz/.beads/`, and that file already has the keys.
2. **Wrong port-coupling claim.** v1 said gtviz uses a different Dolt port than
   3307 and that hardcoding 3307 in gastown would route gtviz writes to the
   wrong server. Both rigs use port 3307 (verified via
   `/home/ubuntu/gt-town/gtviz/.beads/metadata.json` and `dolt-server.port`).
3. **Wrong "landed" timestamp.** v1 used the author date (2026-06-24T19:26:46Z)
   for telemetry cross-check. The committer date is 2026-06-24T19:39:50Z; the
   no-telemetry conclusion still holds but the audit used the wrong clock.

This v2 audit re-states scope, facts, and findings with the corrected evidence
and re-runs the four-model review on the corrected rubric.

The raw codex rejection is in
`/home/ubuntu/.claude/sessions/codex-review-20260702T090122-522254.jsonl`.

## Scope

Commit `664aaf609cba1dc444220074a45f19a548bf2a8e`
("chore: add explicit shared Dolt config keys for new gtviz rig
(gastown-gtviz-rig.1)" by jasper, committer date
2026-06-24T15:39:50-04:00 = 2026-06-24T19:39:50Z; author date
2026-06-24T15:26:46-04:00 = 2026-06-24T19:26:46Z; bead
`gastown-gtviz-rig.1`).

The commit lives in the gastown main repo
(`/home/ubuntu/gt-town/gastown/polecats/slit/gastown`). It modifies one file:
`.beads/config.yaml`. It reached `origin/main` with no gate panel on record
(see Telemetry below). **No new gtviz feature work — strictly a retro-review.**

## Commit Contents

Three lines added at the end of `.beads/config.yaml`:

```yaml
storage.backend: dolt
dolt.server: "127.0.0.1"
dolt.port: 3307
```

`git show 664aaf60 --stat` confirms one file changed, 4 lines added (3 new
config lines + 1 blank separator). No tests added. No other files touched.

## Corrected Facts (v2)

The v1 audit got three things wrong. v2 verifies them on the live system:

### F1: gtviz's actual beads dir is `/home/ubuntu/gt-town/gtviz/.beads/`

```
$ ls -la /home/ubuntu/gt-town/gtviz/ | head -10
drwxr-xr-x 12 ubuntu ubuntu 4096 Jul  1 02:25 .
drwxr-xr-x 41 ubuntu ubuntu 4096 Jul  2 09:58 ..
drwxr-xr-x  4 ubuntu ubuntu 4096 Jul  1 02:39 .beads            # ← working-tree beads dir
drwxr-xr-x  9 ubuntu ubuntu 4096 Jul  1 02:09 .repo.git        # ← bare git repo (no .beads)
```

```
$ cat /home/ubuntu/gt-town/gtviz/.beads/config.yaml
prefix: gtviz
issue-prefix: gtviz
dolt.idle-timeout: "0"
export.auto: "false"

storage.backend: dolt
dolt.server: "127.0.0.1"
dolt.port: 3307
```

The `.repo.git/` path v1 cited is a bare Git directory (created by
`git init --bare`) and has no `.beads/` subtree. The gtviz .beads/config.yaml
already has the three keys — the rig bootstrap (2026-06-30T19:58:43-04:00 per
`stat(1)`) wrote them in.

### F2: gtviz uses port 3307 — same as gastown root

```
$ cat /home/ubuntu/gt-town/gtviz/.beads/metadata.json
{
  "backend": "dolt",
  "database": "dolt",
  "dolt_database": "gtviz",
  "dolt_mode": "server",
  "dolt_server_host": "127.0.0.1",
  "dolt_server_port": 3307
}

$ cat /home/ubuntu/gt-town/gtviz/.beads/dolt-server.port
3307
```

```
$ cat /home/ubuntu/gt-town/gastown/.beads/metadata.json
{
  "backend": "dolt",
  "database": "dolt",
  "dolt_database": "gastown",
  "dolt_mode": "server",
  "dolt_server_host": "127.0.0.1",
  "dolt_server_port": 3307
}
```

Both rigs point at the same town-level shared Dolt server on port 3307. There
is **no** cross-rig port coupling: hardcoding 3307 in gastown root
`.beads/config.yaml` matches what gtviz already uses. The v1 "cross-rig port
coupling" finding is factually wrong.

### F3: Commit landed at 2026-06-24T19:39:50Z (committer date), not 19:26:46Z

```
$ git -C /home/ubuntu/gt-town/gastown/polecats/slit/gastown log -1 \
    --format='%H%n%aI%n%cI' 664aaf60
664aaf609cba1dc444220074a45f19a548bf2a8e
2026-06-24T15:26:46-04:00   # author date
2026-06-24T15:39:50-04:00   # committer date
```

The committer date is the canonical "landed" timestamp — the moment the commit
object was written to the repo. The 13-minute gap reflects `git commit` writing
the object on the local box; both fall inside the no-telemetry window
(see Telemetry below), so the no-telemetry conclusion is unchanged, but v2 uses
the right clock when reasoning about process findings.

## Telemetry Cross-Check (Bypass-merge, confirmed)

The commit landed at 2026-06-24T19:39:50Z. Gate logs and telemetry for
2026-06-24:

```
$ ls /home/ubuntu/gt-town/.runtime/refinery-gate-logs/ | grep "20260624T19"
20260624T191004Z-9390a039-validate.log
20260624T195912Z-0867b80d-validate.log
```

There is NO gate log between 19:10:04Z and 19:59:12Z on 2026-06-24. The commit
landed at 19:39:50Z — squarely in the gap. There is no telemetry event for
commit 664aaf60 in
`/home/ubuntu/gt-town/.runtime/refinery-telemetry/refinery-gate-20260624.jsonl`
(295 lines, grep for `664aaf60` returns no matches).

**Confirmed: bypass-merge with zero gate telemetry.** The commit reached
`origin/main` with no evidence that any reviewer — m3, codex, umans-kimi,
umans-glm, opus-verify, or any other peer — was invoked.

This is a worse-class bypass than the `gastown-cet.12.4` degraded-quorum case:
that commit at least had partial telemetry. 664aaf60 has none.

## Deterministic Gates

Re-ran standard local checks on the diff:

```
$ git show 664aaf60 --check                 # whitespace check
(no output → PASS)

$ git show 664aaf60 | grep -E "^(<<<<<<<|=======|>>>>>>>)"   # conflict marker check
(no matches → PASS)
```

**Result: PASS** (whitespace + conflict-marker only). The diff is a 3-line
additive config change, so the trivial gates cannot catch semantic issues. No
`go.mod` changes, so gofmt/govet gates are not applicable.

## Strict Core Model Review — Raw Verdicts (v2)

All four core reviewers were invoked with the same v2 rubric (corrected facts,
re-stated scope). m3 was self-attested; codex via the Codex CLI (gpt-5.5, xhigh
effort); umans-kimi and umans-glm via the umans proxy at
`http://127.0.0.1:8084`. Raw JSON verdicts are in:

- `m3-verdict.json`
- `codex-verdict.json`
- `umans-kimi-verdict.json`
- `umans-glm-verdict.json`

### m3 (MiniMax-M3, self-attest) — FAIL

```
verdict: FAIL
findings: 1 BLOCKING, 2 MAJOR, 2 MINOR
```

Highlights:
- **BLOCKING** — Bypass-merge: commit reached `origin/main` with zero gate
  telemetry. No `refinery-gate-20260624.jsonl` event; no gate log between
  19:10Z and 19:59Z on 2026-06-24. Worse than the `gastown-cet.12.4`
  degraded-quorum case (which at least had partial telemetry).
- **MAJOR** — Misleading commit message: subject says "for new gtviz rig" but
  the change lands in the **gastown** root `.beads/config.yaml`. The gtviz rig
  didn't exist at the time of the commit (gtviz `.beads/config.yaml` was
  created six days later, 2026-06-30T19:58:43-04:00). The change actually
  repairs the gastown root's own `dolt-config` doctor-check, not gtviz's.
- **MAJOR** — Redundant work: gtviz's later bootstrap independently wrote the
  same three keys. The commit was effectively a no-op for the gtviz rig — its
  only real effect is on the gastown root's doctor check, which the commit
  message doesn't mention.
- **MINOR** — No tests added (`internal/doctor/dolt_config_check_test.go` was
  not updated to assert the gastown-root key set is recognized).
- **MINOR** — Style inconsistency: `dolt.server: "127.0.0.1"` is quoted but
  `dolt.port: 3307` is bare. Pre-existing style in the file also mixes, so
  low-priority cleanup.

**Note on v1:** v1's two BLOCKING findings (wrong file, port coupling) are
factually wrong (see F1/F2 above) and have been dropped. The v1
author-timestamp WARNING is also fixed (see F3).

### codex (gpt-5.5, xhigh effort, CLI) — FAIL

```
verdict: FAIL
findings: 1 BLOCKING, 2 MAJOR, 1 MINOR
```

Highlights:
- **BLOCKING** — Process violation: commit 664aaf60 reached `origin/main` with
  no telemetry event and no gate log in the 19:10Z–19:59Z window. This is a
  bypass-merge, strictly worse than the `gastown-cet.12.4`
  degraded-quorum case.
- **MAJOR** — Commit message is misleading: "for new gtviz rig" but the change
  is to gastown root config, not gtviz. The gtviz rig didn't exist at commit
  time; the change actually fixed the gastown root's doctor-check, which the
  message does not say.
- **MAJOR** — No tests added. The doctor check (`dolt_config_check.go`) is
  the code that requires these keys; no test in `dolt_config_check_test.go`
  asserts the gastown-root key set is recognized.
- **MINOR** — Quote-style inconsistency (mixed quoted/bare scalars).

**Note on v1:** codex in the previous peer-review pass (rejection
`codex-review-20260702T090122-522254.jsonl`) correctly identified the v1 audit's
factual errors (wrong remediation path, wrong port-coupling claim, author vs
committer timestamp). v2 incorporates those corrections; the verdict here
reflects only the corrected facts.

### umans-kimi (umans-kimi-k2.7, model id `kimi`) — FAIL

```
verdict: FAIL
findings: 1 BLOCKING, 3 MAJOR, 1 MINOR
```

Highlights:
- **BLOCKING** — Bypass-merge: zero gate telemetry, no event in
  `refinery-gate-20260624.jsonl`, no gate log at the 19:39:50Z landing time.
- **MAJOR** — Misleading subject: "for new gtviz rig" but the change is in
  gastown root. The gtviz rig was bootstrapped 6 days later with the same
  three keys, so the change was effectively redundant for gtviz and only
  meaningful for gastown root.
- **MAJOR** — No tests added.
- **MAJOR** — Process violation: `gastown-cet.12.4` strict four-model rule not
  applied (this is the very rule that audit established for re-reviews).
- **MINOR** — Quote-style inconsistency.

### umans-glm (umans-glm-5.2, model id `glm`) — FAIL

```
verdict: FAIL
findings: 1 BLOCKING, 2 MAJOR, 2 MINOR
```

Highlights:
- **BLOCKING** — Bypass-merge: zero gate telemetry, no gate log in the
  19:10Z–19:59Z window. The commit was landed in a refinery pipeline gap.
- **MAJOR** — Misleading commit message: "for new gtviz rig" — the change is
  in the gastown root config. The gtviz rig's own `.beads/config.yaml` (created
  6 days later by rig bootstrap) already has the same three keys.
- **MAJOR** — No tests added; the change is untested.
- **MINOR** — Quote-style inconsistency.
- **MINOR** — Final newline handling: the file now ends with a blank line
  before the new block, which is consistent with the existing style, so this
  is a very low-priority note.

## Tally

| Reviewer     | Verdict                      | Quality                                                |
|--------------|------------------------------|--------------------------------------------------------|
| m3           | FAIL (1B, 2M, 2m)            | Self-attested, cited the F1/F2/F3 corrections         |
| codex        | FAIL (1B, 2M, 1m)            | Strong — caught v1 errors; v2 verdict on corrected facts |
| umans-kimi   | FAIL (1B, 3M, 1m)            | Adversarial, cited                                    |
| umans-glm    | FAIL (1B, 2M, 2m)            | Adversarial, cited                                    |

**4 FAIL, 0 PASS. Unanimous strict four-model verdict: FAIL.**

The verdict is FAIL even after v1's two wrong BLOCKING findings are removed.
The single remaining BLOCKING finding is the bypass-merge (zero gate
telemetry), which is independently verified.

## Conclusion — Retro-Gate Verdict

Commit 664aaf60 was intended to make the town-level shared-Dolt bd config
explicit. The change is not wrong in itself — the three lines
(`storage.backend: dolt`, `dolt.server: "127.0.0.1"`, `dolt.port: 3307`) are
the right keys for the gastown root and match what gtviz would later get from
its bootstrap. But:

1. The change is in the **gastown** root, not in gtviz. The commit subject
   "for new gtviz rig" is misleading. The actual effect is to fix the
   gastown root's own `dolt-config` doctor-check (which would otherwise flag
   the gastown root for missing explicit shared-Dolt keys).
2. The change is **redundant for the gtviz rig**, which was bootstrapped six
   days later (2026-06-30T19:58:43-04:00) and independently wrote the same
   three keys.
3. The change **reached `origin/main` with no gate telemetry** — a strict
   bypass-merge, worse than the `gastown-cet.12.4` degraded-quorum case.
   There is no evidence that any reviewer (m3, codex, umans-kimi, umans-glm,
   opus-verify, or otherwise) was invoked for this commit.
4. No tests were added. The doctor check (`dolt_config_check.go`) is the
   code that requires these keys; no test in `dolt_config_check_test.go`
   asserts the gastown-root key set is recognized.
5. Style inconsistency: `dolt.server` is quoted, `dolt.port` is bare. The
   pre-existing config also mixes, so this is a low-priority cleanup.

**Net strict four-model verdict: FAIL (4 of 4). The merge should not have
happened under strict four-model review.**

## Concrete Findings (Linked Follow-ups)

1. **Bypass-merge, no gate telemetry (BLOCKING)** — Commit 664aaf60 reached
   `origin/main` with no event in any `refinery-gate-YYYYMMDD.jsonl` and no
   gate log at `/home/ubuntu/gt-town/.runtime/refinery-gate-logs/` between
   19:10Z and 19:59Z on 2026-06-24. This is a worse-class bypass than the
   `gastown-cet.12.4` degraded-quorum case. Follow-up: file bead to require
   a CI failure when a commit reaches main without a gate record, and to
   investigate the refinery pipeline for the 2026-06-24T19:10Z–19:59Z gap.
   (Bead: `hq-0hxq0` — `[gastown-fgifg] Investigate bypass-merge: 664aaf60
   reached main with zero gate telemetry`)
2. **Misleading commit message (MAJOR)** — Subject says "for new gtviz rig"
   but the change is in the gastown root `.beads/config.yaml`. The gtviz rig
   didn't exist at commit time; the change actually fixed the gastown root's
   own doctor check. Follow-up: file bead to require commit-message scope
   review (and have the merge-queue reject commits whose subject says "for X
   rig" but whose diff is in a different rig's tree). (Bead: `hq-8v1xw` —
   `[gastown-fgifg] Require commit-message scope review`)
3. **No tests added (MAJOR)** — The change is to a key the doctor check
   requires; no test in `internal/doctor/dolt_config_check_test.go` was
   updated to assert the gastown-root key set is recognized. Follow-up: file
   bead to add a test. (Bead: `hq-tb38j` — `[gastown-fgifg] Add test
   asserting gastown-root dolt-config key set is recognized`)
4. **Redundant work for gtviz (MAJOR)** — The gtviz rig's bootstrap
   (2026-06-30T19:58:43-04:00) independently wrote the same three keys. The
   change was effectively a no-op for the gtviz rig. Follow-up: file bead to
   document the relationship (gastown root config was made explicit first;
   gtviz bootstrap inherited the same convention). (Bead: `hq-zz1h3` —
   `[gastown-fgifg] Document relationship: gastown-root config made explicit
   before gtviz bootstrap`)
5. **Style inconsistency (MINOR)** — `dolt.server: "127.0.0.1"` is quoted
   but `dolt.port: 3307` is bare. The pre-existing config style mixes too
   (some bare, some quoted), so this is a low-priority cleanup. Follow-up:
   include in any rework commit. (Bead: `hq-5aflu` — `[gastown-fgifg]
   Quote-style cleanup`)

## Why v1's BLOCKING findings were dropped

v1's "wrong file" finding said the keys should have been added to
`/home/ubuntu/gt-town/gtviz/.repo.git/.beads/config.yaml`. F1 shows the
`.repo.git/` path is a bare Git repo, not a working tree; the active gtviz
beads dir is `/home/ubuntu/gt-town/gtviz/.beads/`, and that file already has
the keys (gtviz bootstrap wrote them in on 2026-06-30T19:58:43-04:00). v1's
"port-coupling" finding said gtviz uses a different Dolt port. F2 shows both
gtviz and gastown use port 3307. v1 used the author date for the telemetry
cross-check; F3 shows the committer date is the canonical landing time, and
the no-telemetry conclusion holds for either.

## Raw Evidence Locations

- Commit object: `664aaf609cba1dc444220074a45f19a548bf2a8e` (gastown main)
- Author date: 2026-06-24T15:26:46-04:00 = 2026-06-24T19:26:46Z
- Committer date: 2026-06-24T15:39:50-04:00 = 2026-06-24T19:39:50Z
- Gate logs: `/home/ubuntu/gt-town/.runtime/refinery-gate-logs/` (no entry
  for 664aaf60; 19:10Z and 19:59Z are the surrounding log timestamps)
- Telemetry: `/home/ubuntu/gt-town/.runtime/refinery-telemetry/refinery-gate-20260624.jsonl`
  (no entry for 664aaf60)
- Working tree (gastown root):
  `/home/ubuntu/gt-town/gastown/.beads/config.yaml`
- gtviz working-tree beads dir: `/home/ubuntu/gt-town/gtviz/.beads/`
  (config.yaml + metadata.json + dolt-server.port = 3307)
- gtviz bare git dir: `/home/ubuntu/gt-town/gtviz/.repo.git/` (no `.beads/`)
- Doctor check: `internal/doctor/dolt_config_check.go`
- Prior audit (v1, rejected): `docs/audits/gastown-fgifg/audit-report.md`
  in commit `dd3e5d88`; raw review in
  `/home/ubuntu/.claude/sessions/codex-review-20260702T090122-522254.jsonl`
- Review outputs: `/tmp/m3-verdict-664aaf60-v2.json`,
  `/tmp/codex-response-664aaf60-v2.txt`,
  `/tmp/kimi-response-664aaf60-v2.json`,
  `/tmp/glm-response-664aaf60-v2.json`
- Prior audit: `docs/audits/gastown-cet.12.4/audit-report.md`

## Verdict Summary

- Deterministic gates: PASS (trivial — whitespace + conflict marker only)
- m3: FAIL (1B, 2M, 2m)
- codex: FAIL (1B, 2M, 1m)
- umans-kimi: FAIL (1B, 3M, 1m)
- umans-glm: FAIL (1B, 2M, 2m)
- **Net strict four-model verdict: FAIL (4 of 4)**
- **Original merge was a bypass-merge with no gate panel on record — worse
  than the gastown-cet.12.4 degraded-quorum case.**
- v2 supersedes v1. v1's two BLOCKING findings (wrong file, port coupling)
  and the author-timestamp WARNING were factually wrong and have been dropped.
  v2's single remaining BLOCKING is the bypass-merge finding, which is
  independently verified on the live system.
