# Retro-gate shard E re-audit — commits 5-11 (gastown-cet.12.14)

## Scope

Re-audit the seven shard-E commits originally blocked by unavailable `umans-kimi` and `umans-glm` reviewers:

- `1791f805` feat(witness,daemon): self-recovery for context-saturated or stalled patrol sessions (gastown-o9d)
- `ee837575` fix(witness): REWORK_DEFERRED rollup reports real suppressed count (gastown-3ip)
- `09880751` test(cmd): regression tests and error surfacing for convoy-stage JSON output (gastown-l76)
- `a57c457d` feat(alerts): canonical root-cause keys and alert occurrence aggregation (gastown-cet.5.1)
- `6ae7a4c5` fix(scheduler): refuse agent-role mismatch dispatch of Deacon beads under mol-polecat-work (gastown-c76)
- `0471d3f8` docs(design): issue-store consolidation inventory and duplicate canonicalization (gastown-cet.1.2)
- `ff7f3a36` test: Mayor decision precedence guards + gt mayor decision CLI (gastown-cet.7)

## Reviewer set

- m3 (MiniMax-M3, self-attest)
- codex (gpt-5.5, xhigh)
- umans-kimi (umans/umans-kimi-k2.7 via opencode)
- umans-glm (umans/umans-glm-5.2 via opencode)

## Re-audit resolution

The original shard-E audit (`gastown-cet.12.14`) recorded `umans-kimi` and `umans-glm` as UNAVAILABLE because the proxy at `127.0.0.1:8084` rejected the adapter token with 401 "invalid proxy api key".

After investigation, the local proxy is healthy and the token in `~/.umans/config.json` (`umans-proxy`) validates correctly when passed as the `Authorization: Bearer` header. The opencode-based adapter also required the `opencode` binary to be on `PATH` (`/home/ubuntu/.npm-global/bin`), which was missing from the shell environment used by the orchestrator scripts. Exporting that path restored adapter execution.

Retrospective note: the adapter health check (`auth_valid=true`) only verified that the proxy token was accepted at the health endpoint; it did not verify that the `opencode` CLI could actually launch and reach the proxy. The missing binary caused the early re-audit runs to return UNAVAILABLE envelopes even after the proxy token was valid.

## Per-commit verdicts

| # | SHA | Subject | m3 | codex | umans-kimi | umans-glm | Consensus |
|---|-----|---------|----|-------|------------|-----------|-----------|
| 5 | `1791f805` | feat(witness,daemon): self-recovery for context-saturated or stalled patrol sessions (gastown-o9d) | FAIL | FAIL | FAIL | UNAVAILABLE | **FAIL** |
| 6 | `ee837575` | fix(witness): REWORK_DEFERRED rollup reports real suppressed count (gastown-3ip) | FAIL | FAIL | PASS | PASS | **FAIL** |
| 7 | `09880751` | test(cmd): regression tests and error surfacing for convoy-stage JSON output (gastown-l76) | PASS | PASS | PASS | PASS | **PASS** |
| 8 | `a57c457d` | feat(alerts): canonical root-cause keys and alert occurrence aggregation (gastown-cet.5.1) | FAIL | FAIL | PASS | UNAVAILABLE | **FAIL** |
| 9 | `6ae7a4c5` | fix(scheduler): refuse agent-role mismatch dispatch of Deacon beads under mol-polecat-work (gastown-c76) | FAIL | FAIL | FAIL | UNAVAILABLE | **FAIL** |
| 10 | `0471d3f8` | docs(design): issue-store consolidation inventory and duplicate canonicalization (gastown-cet.1.2) | PASS | PASS | PASS | PASS | **PASS** |
| 11 | `ff7f3a36` | test: Mayor decision precedence guards + gt mayor decision CLI (gastown-cet.7) | PASS | PASS | PASS | PASS | **PASS** |

*`UNAVAILABLE*` means the available reviewers all PASS but at least one reviewer did not return a verdict.

## Blocking findings summary (FAIL commits)

### `1791f805` — feat(witness,daemon): self-recovery for context-saturated or stalled patrol sessions (gastown-o9d)

BLOCKING findings:
- **internal/cmd/witness_liveness.go:413** — `gt witness replay --apply` executes `sh -c` on a command string built from handoff JSON fields, creating a command-injection vector via polecat or assigned_agent values.
- **internal/witness/liveness.go:1063** — The supervised restart path synthesizes a handoff with only heartbeat metadata and `StoppedLanes: nil`, so it does not capture stopped lanes, dirty/ahead state, queued scheduler beads, or in-flight cleanup as required.
- **internal/witness/liveness.go:999** — `ShouldRestart` suppresses restart when no heartbeat exists, but the new witness heartbeat writer has no production caller, so real stalled witnesses will not produce the file the daemon polls.
- **internal/witness/liveness.go:1009** — The daemon supervisor ignores `CommandDurationMs` and `MaxCommandDuration`, so a saturated witness wedged in a long-running command with a fresh heartbeat will not be restarted despite the stalled-session requirement.

_codex summary:_ The commit fails because the self-recovery path is not safely executable and does not actually collect or poll the production liveness and handoff state required by the specification.

### `ee837575` — fix(witness): REWORK_DEFERRED rollup reports real suppressed count (gastown-3ip)

BLOCKING findings:
- **scripts/cutover-pinned-1.2.0.sh:58, scripts/cutover-pinned-1.2.0.sh:108** — The rollback backup path is relative (`gt.before-pinned-1.2.0-cutover`), so the script does not create the documented `~/.local/bin/...` backup and the printed/docs rollback command can fail outside the original working directory.

_codex summary:_ The throttle rollup fix is covered by logic-level tests, but the cutover rollback change breaks the documented recovery path.

### `a57c457d` — feat(alerts): canonical root-cause keys and alert occurrence aggregation (gastown-cet.5.1)

BLOCKING findings:
- **internal/alerts/aggregator.go:120, internal/alerts/aggregator.go:146** — Record renders multi-line alert descriptions and passes them through Beads Create/Update, whose real CLI path uses --description values that bd rejects for embedded newlines, so production aggregation fails.
- **internal/cmd/patrol_scan_test.go:229** — The changed patrol-scan aggregation path is tested only with an in-memory fake BeadsClient, so it misses the real beads CLI/database behavior required by the production path.

_codex summary:_ The implementation adds the intended aggregation shape, but the real production beads path is broken and untested.

### `6ae7a4c5` — fix(scheduler): refuse agent-role mismatch dispatch of Deacon beads under mol-polecat-work (gastown-c76)

BLOCKING findings:
- **internal/cmd/sling_content_guard.go:21** — The broad `gt patrol` substring rejects any mol-polecat-work bead that mentions the patrol CLI, so legitimate polecat code tasks such as fixing `gt patrol scan/report` would be refused or closed as Deacon work.

_codex summary:_ The guard adds the intended scheduler protection, but its matching is too broad and can block valid production work.

## Consensus impact of missing umans-glm verdicts

`umans-glm` timed out/rate-limited on three commits: `1791f805`, `a57c457d`, and `6ae7a4c5`. All three already have FAIL consensus from `m3` + `codex`; the missing `umans-glm` verdicts therefore cannot upgrade any commit from FAIL to PASS. The shard-E consensus tally remains unchanged.

## Tally

- **PASS**: 3 commits (3 of the 7)
- **FAIL**: 4 commits (4 of the 7)

These match the original m3+codex-only tally from `gastown-cet.12.14`.

## HMAC attestation

Per the acceptance criteria, the HMAC attestation is written only after all five reviewers (four peers + Opus verify) PASS for every commit. Four reviewers did not all PASS, and `umans-glm` is still partially unavailable. **HMAC NOT written.**

## Deterministic gate status

`mayor/rig/scripts/run-all-gates.sh` still fails on the pre-existing conflict-marker false positive in `scripts/check-conflict-markers_test.py` (regex patterns that include `=======`), tracked separately. This audit commit adds only `docs/audits/gastown-cet.12.14/*` markdown files and does not introduce new gate failures.

## Raw verdict files

- `m3-verdicts.jsonl`
- `codex-verdicts.jsonl`
- `umans-kimi-verdicts.jsonl`
- `umans-glm-verdicts.jsonl`

## Next steps

1. The FAIL commits already have follow-up beads filed (gastown-c4r, gastown-s08, gastown-yti, gastown-wat).
2. Close the re-audit beads `gastown-8sg` and `gastown-kv9`; the missing umans verdicts have been obtained to the extent the current proxy capacity allows, and the prior m3+codex consensus is confirmed.
