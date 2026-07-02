# Retro-gate 664aaf60 (gastown-gtviz-rig.1) — STRICT CORE REVIEW AUDIT

## Scope

Audit commit `664aaf609cba1dc444220074a45f19a548bf2a8e` ("chore: add explicit shared Dolt config keys for new gtviz rig" by jasper, 2026-06-24T15:26:46-04:00, bead `gastown-gtviz-rig.1`). The commit lives in the gastown main repo and modifies `.beads/config.yaml`. It reached `origin/main` with no gate panel on record (see Telemetry below). **No new GTViz feature work — strictly a retro-review.**

## Commit Contents

The commit modifies one file (`.beads/config.yaml`) in the gastown main repo (`/home/ubuntu/gt-town/gastown`). Three lines added at the end of the file:

```yaml
storage.backend: dolt
dolt.server: "127.0.0.1"
dolt.port: 3307
```

`git show --stat` confirms exactly one file changed, 4 lines added (3 new config lines + 1 blank separator). No tests added. No other files touched.

## Prior gtviz context

The gtviz rig is a separate bare repo at `/home/ubuntu/gt-town/gtviz/.repo.git` (with backup at `/home/ubuntu/gt-town-backups/gtviz-origin.git`). It has its own `.beads/` directory created by the rig bootstrap process. Per `/home/ubuntu/gt-town/gtviz/config.json` (the rig config), gtviz has its own beads prefix and dolt setup. The previous gtviz retro-audit (`gastown-cet.12.4`, see `docs/audits/gastown-cet.12.4/audit-report.md`) found that the gtviz rig bootstrap files (config.json, CLAUDE.md, AGENTS.md) live in the working tree and were eventually committed in follow-up beads (`gastown-nnl`, `gastown-c9m`, `gastown-7n3`).

## Deterministic Gates

Re-ran standard local checks on the diff:

```
$ git show 664aaf60 --check            # whitespace check
(no output → PASS)

$ git show 664aaf60 | grep -E "^(<<<<<<<|=======|>>>>>>>)"   # conflict marker check
(no matches → PASS)
```

**Result: PASS** (whitespace + conflict-marker only). The diff is a 3-line additive config change, so the trivial gates cannot catch semantic issues. No `go.mod` changes, so gofmt/govet gates are not applicable.

## Strict Core Model Review — Raw Verdicts

All four core reviewers were invoked with the same rubric. m3 was self-attested; codex via the Codex CLI (gpt-5.5, xhigh effort); umans-kimi and umans-glm via the umans proxy at `http://127.0.0.1:8084`. Raw JSON verdicts are in:

- `m3-verdict.json`
- `codex-verdict.json`
- `umans-kimi-verdict.json`
- `umans-glm-verdict.json`

### m3 (MiniMax-M3, self-attest) — FAIL

```
verdict: FAIL
findings: 2 BLOCKING, 3 MAJOR, 1 MINOR
```

Highlights:
- **BLOCKING** — Scope mismatch: commit message says "for new gtviz rig" but the change is in the GASTOWN root `.beads/config.yaml`, not the gtviz rig's own `.beads/config.yaml`. The doctor check validates every `.beads/` independently, so the gtviz rig will still fail the check.
- **BLOCKING** — Implicit port coupling: hardcoded `dolt.port: 3307` in the gastown root creates cross-rig port coupling. The gtviz rig's dolt server is at a different port (per `gtviz/config.json`).
- **MAJOR** — Side effect: every `bd` invocation in the gastown root now defaults to Dolt at port 3307.
- **MAJOR** — No tests added.
- **MAJOR** — Process violation: no gate panel on record for this commit.
- **MINOR** — Inconsistent quoting (dolt.server quoted, dolt.port bare).

### codex (gpt-5.5, xhigh effort, CLI) — FAIL

```
verdict: FAIL
findings: 2 BLOCKING, 2 MAJOR, 1 MINOR
```

Highlights:
- **BLOCKING** — Commit message says "for gtviz rig" but the edit is in gastown's root config. gtviz has its own separate bare repo and `.beads/config.yaml`.
- **BLOCKING** — Repository-wide side effect: every bd invocation from gastown root now defaults to Dolt on 127.0.0.1:3307. Can route gastown issue operations to the wrong or unavailable Dolt service.
- **MAJOR** — Doctor validation is per-`.beads/`; adding Dolt keys to gastown cannot satisfy gtviz's independent config check.
- **MAJOR** — No tests or gate-panel telemetry on record.
- **MINOR** — Mixed quoted/unquoted scalar style.

**Strong review** — codex this time did NOT pass the change at face value; it caught the scope mismatch and side effect that its first gtviz audit (gastown-cet.12.4) missed.

### umans-kimi (umans-kimi-k2.7, model id `kimi`) — FAIL

```
verdict: FAIL
findings: 2 BLOCKING, 3 MAJOR, 1 MINOR
```

Highlights:
- **BLOCKING** — Scope mismatch: keys added to gastown root config instead of `/home/ubuntu/gt-town/gtviz/.repo.git/.beads/config.yaml`. Doctor check validates every `.beads/` dir independently.
- **BLOCKING** — Invasive side effect on gastown: storage.backend='dolt' + dolt.port=3307 in root config changes default for every bd invocation in gastown worktree.
- **MAJOR** — No tests added.
- **MAJOR** — No telemetry entry, no gate panel, violating gastown-cet.12.4 strict four-model rule.
- **MAJOR** — Cross-rig port coupling risk.
- **MINOR** — Style inconsistency (mixed quoting).

### umans-glm (umans-glm-5.2, model id `glm`) — FAIL

```
verdict: FAIL
findings: 2 BLOCKING, 2 MAJOR, 2 MINOR
```

Highlights:
- **BLOCKING** — Scope mismatch: keys added to wrong file. gtviz is a separate bare repo at `/home/ubuntu/gt-town/gtviz/.repo.git` with its own `.beads/`. Doctor check validates each `.beads/` dir independently.
- **BLOCKING** — Unintended side effect: setting `storage.backend='dolt'` in gastown root flips the default storage backend for all gastown-root bd invocations.
- **MAJOR** — No gate panel on record; four-model rule from gastown-cet.12.4 not applied.
- **MAJOR** — Cross-rig port coupling risk; port 3307 is town-level default; gtviz may need distinct port.
- **MINOR** — No tests added.
- **MINOR** — Style inconsistency.

## Tally

| Reviewer | Verdict | Quality |
|---|---|---|
| m3 | FAIL (2 BLOCKING, 3 MAJOR, 1 MINOR) | Adversarial, cited |
| codex | FAIL (2 BLOCKING, 2 MAJOR, 1 MINOR) | Strong — caught scope mismatch + side effect |
| umans-kimi | FAIL (2 BLOCKING, 3 MAJOR, 1 MINOR) | Adversarial, cited |
| umans-glm | FAIL (2 BLOCKING, 2 MAJOR, 2 MINOR) | Adversarial, cited |

**4 FAIL, 0 PASS. Unanimous strict four-model verdict: FAIL.**

This is a significant improvement over the prior gtviz audit (`gastown-cet.12.4`), where codex returned a weak PASS. The strict four-model rule is now actually applied — all four reviewers caught the scope mismatch (commit message says "for gtviz rig" but lands in gastown root) and the side-effect concern (default backend changes for all gastown-root bd invocations).

## Telemetry Cross-Check (degraded-quorum merge)

The commit's author time is 2026-06-24T15:26:46-04:00 = 2026-06-24T19:26:46Z. Gate logs and telemetry for 2026-06-24:

```
$ ls /home/ubuntu/gt-town/.runtime/refinery-gate-logs/ | grep "20260624T19"
20260624T191004Z-9390a039-validate.log
20260624T195912Z-0867b80d-validate.log
```

There is NO gate log between 19:10:04Z and 19:59:12Z on 2026-06-24. The commit landed at 19:26:46Z — squarely in the gap. There is no telemetry event for commit 664aaf60 in any `refinery-gate-YYYYMMDD.jsonl` file. The commit reached `origin/main` without a recorded gate panel.

**This is the same class of degraded/bypass merge flagged in `gastown-cet.12.4`** — but with a critical difference: the gtviz initial commit (2abdc645) at least had *some* gate telemetry events (which the previous audit was retroactively re-reviewing). Commit 664aaf60 has **zero** gate telemetry. There is no evidence that any reviewer — m3, codex, umans-kimi, umans-glm, opus-verify, or any other peer — was even invoked for this commit.

**Stronger finding than gastown-cet.12.4**: not a degraded-quorum merge (where some reviewers failed/weren't available), but a **bypass merge** (no review at all).

## Conclusion — Retro-Gate Verdict

The commit was intended to fix the gtviz rig's doctor-check failure (the gtviz rig's own `.beads/config.yaml` was missing the `storage.backend` / `dolt.server` / `dolt.port` keys that `internal/doctor/dolt_config_check.go` requires for shared-Dolt beads dirs). Instead, the change landed in the **wrong repo and the wrong file** — the gastown root config. The net effect:

1. The gtviz rig's doctor check still fails (its own `.beads/config.yaml` was not updated).
2. Every `bd` invocation in the gastown root now silently defaults to the town-level Dolt at port 3307.
3. The change is permanently in main with no gate record and no test coverage.

**Net strict four-model verdict: FAIL (4 of 4). The merge should not have happened under strict four-model review.**

## Concrete Findings (Linked Follow-ups)

1. **Wrong file (BLOCKING)** — Keys should be in `/home/ubuntu/gt-town/gtviz/.repo.git/.beads/config.yaml`, not in the gastown root `.beads/config.yaml`. Follow-up: file bead to revert this commit and apply the equivalent change to the gtviz rig's own `.beads/config.yaml`. (Bead: `gastown-fgifg.gtviz-config-relocate`)
2. **Cross-rig port coupling (BLOCKING)** — Hardcoded `dolt.port: 3307` in the gastown root config creates implicit port coupling. gtviz uses a different port; the change as written would route gtviz writes to the wrong server if gtviz ever inherited these keys. Follow-up: file bead to ensure gtviz's config uses its own port, not 3307. (Bead: `gastown-fgifg.cross-rig-port-coupling`)
3. **Side effect on gastown root (MAJOR)** — Every `bd` invocation in the gastown root now resolves `storage.backend='dolt'` + port 3307 by default. This is a broad behavioral change for what was intended as a single-rig fix. Follow-up: file bead to verify that no existing `bd` workflows in the gastown root depend on a non-Dolt default, and add a test that asserts the config is honored. (Bead: `gastown-fgifg.gastown-root-side-effect`)
4. **No tests added (MAJOR)** — The new behavior (town-root beads now resolves these keys) is untested. Follow-up: file bead to add a test in `internal/doctor/dolt_config_check_test.go` that asserts the gastown-root `.beads/config.yaml` is recognized by the doctor check. (Bead: `gastown-fgifg.no-tests-added`)
5. **Process violation: no gate panel on record (MAJOR)** — Commit 664aaf60 reached `origin/main` with no telemetry event in any `refinery-gate-YYYYMMDD.jsonl` and no gate log at `/home/ubuntu/gt-town/.runtime/refinery-gate-logs/`. This is a worse-class bypass than the `gastown-cet.12.4` audit (which was at least a degraded-quorum case with partial telemetry). Follow-up: file bead to require a CI failure when a commit reaches main without a gate record, and to investigate the refinery pipeline for 2026-06-24T19:26Z gap. (Bead: `gastown-fgifg.bypass-merge-no-gate`)
6. **Style inconsistency (MINOR)** — `dolt.server: "127.0.0.1"` is quoted but `dolt.port: 3307` is bare. The existing config style mixes too (some bare, some quoted), so this is a low-priority cleanup. Follow-up: include in any rework commit. (Bead: `gastown-fgifg.quote-style-inconsistency`)

## Raw Evidence Locations

- Commit object: gastown main repo → `664aaf609cba1dc444220074a45f19a548bf2a8e`
- Gate logs: `/home/ubuntu/gt-town/.runtime/refinery-gate-logs/` (no entry for 664aaf60)
- Telemetry: `/home/ubuntu/gt-town/.runtime/refinery-telemetry/refinery-gate-20260624.jsonl` (no entry for 664aaf60)
- Working tree: `/home/ubuntu/gt-town/gastown/.beads/config.yaml`
- gtviz rig: `/home/ubuntu/gt-town/gtviz/.repo.git/.beads/config.yaml` (the file that should have been edited)
- Doctor check: `internal/doctor/dolt_config_check.go`
- Review outputs: `/tmp/m3-verdict-664aaf60.json`, `/tmp/codex-response-664aaf60.txt`, `/tmp/kimi-response-664aaf60.json`, `/tmp/glm-response-664aaf60.json`
- Prior audit: `docs/audits/gastown-cet.12.4/audit-report.md`

## Verdict Summary

- Deterministic gates: PASS (trivial — whitespace + conflict marker only)
- m3: FAIL
- codex: FAIL (strong — caught scope mismatch and side effect)
- umans-kimi: FAIL
- umans-glm: FAIL
- **Net strict four-model verdict: FAIL (4 of 4)**
- **Original merge was a bypass-merge with no gate panel on record — worse than the gastown-cet.12.4 degraded-quorum case.**
