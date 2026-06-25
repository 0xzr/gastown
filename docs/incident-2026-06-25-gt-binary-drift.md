# Incident: Runtime gt binary drift and silent polecat exits (2026-06-25)

## Summary

On 2026-06-25 around 11:47Z, four Gastown polecat sessions (jasper, obsidian,
opal, quartz) vanished in a tight ~14 second window immediately after topaz
dispatch. There was no corresponding nuke, remove, or kill log entry. The live
`~/.local/bin/gt` binary was built from commit `7a4fce93` (version 1.2.0), while
the Gastown fork runtime source in `/home/ubuntu/gt-town/gastown/mayor/rig` was
at commit `6af164b3`, 139 commits newer. The installed binary therefore lacked
several merged recovery and fleet-management fixes.

## Evidence timeline

| Time (UTC) | Event |
|------------|-------|
| ~11:47:00  | topaz dispatch completed. |
| ~11:47:02  | polecat sessions for jasper, obsidian, opal, and quartz disappeared. |
| ~11:48:00  | model-mix log live count dropped from 6 to 2. |
| post-event | No `gt log` kill, nuke, or remove entries recorded for the four sessions. |

## Technical findings

1. **Binary staleness**: `gt version --verbose` on the live binary reported
   commit `7a4fce93`; the source worktree was at `6af164b3`. The binary lagged
   the source by 139 commits.
2. **Missing session-exit telemetry**: Before the fix, pane/process death did not
   durably record exit code, signal, last transcript tail, spawning command, or
   model. This made it impossible to distinguish a clean exit from a crash or
   external kill.
3. **Recovery misclassification**: A hooked polecat whose tmux session was still
   running could be classified as `NEEDS_RECOVERY` solely because its persisted
   state was non-idle and heartbeats/process probes lagged. This could inflate
   `recovery_blocked` capacity and suppress new dispatches.
4. **Correlated disappearance**: Four sessions died almost simultaneously after
   a dispatch event, strongly suggesting a common trigger (stale binary behavior,
   signal, or supervisor action) rather than independent failures.

## Mitigations applied

- `internal/cmd/root.go`: automation commands (scheduler, witness, patrol,
  recovery, refinery) now refuse to run when the installed binary lags the
  source by more than the configured tested-release delta (`GT_STALE_BINARY_MAX_DELTA`).
- `internal/version/stale.go`: helpers `IsStaleBeyond` and `BlockingReason`
  provide the gating logic with commit-count tolerance.
- `internal/polecat/session_telemetry.go`, `session_manager.go`, `internal/cmd/log.go`,
  `internal/tmux/tmux.go`: polecat sessions now write durable start/exit telemetry
  including exit code, signal, supervisor source, spawning command, model, and
  last transcript/pane tails.
- `internal/polecat/workstate.go`: a running tmux session alone is now sufficient
  to treat a hooked polecat as `WORKING` instead of `NEEDS_RECOVERY`, preventing
  recovery-blocked capacity leaks during heartbeat/probe lag.
- `scripts/cutover-pinned-1.2.0.sh`, `docs/cutover-pinned-1.2.0.md`: the approved
  pinned-1.2.0 cutover path now backs up the previous binary and records
  attestation evidence so rollback is immediate.

## References

- Bead: `gastown-cet.16`
- Parent epic: `gastown-cet` (GT merge/refinery lifecycle correctness)
- Live binary commit: `7a4fce93`
- Runtime source commit at incident: `6af164b3`
