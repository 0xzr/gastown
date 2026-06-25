# Gastown Witness Scoped Restart Recovery Policy

Version: v1.0
Date: 2026-06-24
Rig: gastown
Owner: Mayor

## Scope

This policy delegates bounded, model-preserving restart recovery of stuck
polecats to the gastown Witness through the approved scoped restart runner.
The runner lives at:

```text
/home/ubuntu/gastown-spike/dropin/gt-scoped-restart-runner.py
/home/ubuntu/gastown-spike/dropin/gt-scoped-restart-runner.sh
```

Witness may not improvise shell mutation. It may use only this runner for
same-polecat restart recovery, with its dry-run proof, locks, telemetry,
`--max-fixes 1`, and post-state verification.

## Why this exists

Earlier recovery instructions told Witness to fall back to a manual
`gt session start <rig>/<polecat>` path when a polecat was stopped but still
owned recoverable work. That path is missing from the deployed system and does
not verify that the restarted session preserves the source bead's assigned
model. Stuck plan-approval lanes and clean dead lanes were escalating to Mayor
instead of self-healing. This policy replaces that missing path with the durable
scoped runner.

## Approved runner

Restart-only WIP recovery (same-polecat, same-model):

```bash
# Report-only: discover eligible candidates and rejection reasons
/home/ubuntu/gastown-spike/dropin/gt-scoped-restart-runner.sh --rig gastown

# Supervised fix: restart at most one eligible stopped same-owner polecat
/home/ubuntu/gastown-spike/dropin/gt-scoped-restart-runner.sh --rig gastown --target gastown/<polecat> --fix --max-fixes 1
```

`--fix` authority is active only after a dry-run proves a stable allowlisted
candidate. Witness must not set global repair environment variables, install
mutation cron, batch multiple fixes, or use any runner class that has not
passed its supervised canary.

This is not dispatch, merge submission, model rotation, direct cleanup,
worktree deletion, raw bead mutation, `gt done`, `gt mq submit`, or arbitrary
`bd`/`gt` passthrough.

## Required safe context

Witness may evaluate scoped recovery only from the safe town context:

- Working directory is `/home/ubuntu/gt-town`.
- GT binary resolves to `/home/ubuntu/.local/bin/gt`.
- GT version is the pinned Gas Town 1.2.0 line.
- Target worktree is not inside `/home/ubuntu/gt-town/gastown/mayor/rig`.
- Target worktree path belongs to the named polecat under
  `/home/ubuntu/gt-town/gastown/polecats/<polecat>/`.

Exact safe-context checks:

```bash
pwd -P
command -v gt
gt --version
```

Expected values are `/home/ubuntu/gt-town`,
`/home/ubuntu/.local/bin/gt`, and `gt version 1.2.0`.

## Eligibility

The executable runner owns the final eligibility decision. Witness must not
override a runner rejection by hand.

All sources must agree immediately before action:

- Session is stopped or absent. A running session is liveness, not restartable.
- Source bead is `hooked` or `in_progress`. Plain `open` is insufficient.
- Hook, `gt polecat check-recovery`, branch, and worktree identify the same rig,
  polecat, bead, and branch.
- `gt polecat check-recovery` says `NEEDS_RECOVERY` and `safe_to_nuke=false`.
- Allowed recovery reasons are only `branch_not_main`, `ahead_of_origin`,
  `ahead_of_*`, `dirty_worktree`, `no_mq`, `no_merge_queue_record`,
  `merge_queue_record_unavailable`, or a clean non-main work branch with no
  merge queue record.
- No open merge queue item exists for the same issue, branch, or commit.
- No live refinery gate, run-all-gates, `gt mq submit`, cargo, rustc, clippy,
  nextest, or test process is operating on the target worktree.
- No fresh live-touch stamp indicates active work for the target polecat, unless
  the runner proves a clean dead lane: no session, no target process, no open
  MQ match, no ahead commits, no dirty files, no tree diff, and the worktree
  matches `origin/main`. Clean dead lanes may restart immediately because the
  live-touch is only stale session residue, not evidence of active work.
- For committed dead lanes (ahead/tree-diff but clean worktree, no session, no
  process), the runner bypasses fresh live-touch the same way it does for clean
  dead lanes.

Exact live checks:

```bash
pgrep -af 'refinery-gate|run-all-gates|gt mq submit|cargo|rustc|clippy|nextest|test'
gt session list gastown --json
gt model-status
find /home/ubuntu/gt-town/.runtime -maxdepth 1 -name 'agent-live-touch.gastown-polecat-*.stamp' -mmin -10 -print
```

The process list must be matched to the target worktree, branch, bead, or
polecat before it blocks recovery. Non-target activity is recorded but does not
by itself block.

## Model and ownership preservation

Witness must preserve ownership. It may not rotate models.

Before restart, Witness must read the model assignment file when present:

```text
/home/ubuntu/gt-town/.runtime/model-assignments/<bead>.json
```

The expected agent from that file must not conflict with the target polecat's
configured model, current assignment, or current live inference if present. If
it conflicts, Witness must escalate rather than restart.

Routine model caps apply to new dispatch, not same-polecat recovery. The scoped
restart runner preserves the source bead's assigned model even if
`gt model-status` says that model is currently at its regular live cap. A
restart is recovery of an already-reserved lane, not a new routine assignment.

If the assigned model has failed three substantive attempts on the same source
bead, the model-preserving wrapper refuses the restart with exit code 75.
Witness must treat that as an escalation/rotation point, not as a transient
restart failure. Do not retry the same model again under this policy; escalate
to Mayor or use the normal model-attempt rotation path when explicitly
authorized.

After `gt session start`, the runner verifies within the configured window that
`gt model-status` shows the expected model live for the restarted polecat. If
the model is wrong or cannot be determined, the runner refuses with actionable
evidence. Witness must escalate that result; it must not perform a second
restart attempt.

## Stuck plan-approval and clean dead lanes

Two common gastown cases are covered by this policy:

1. **Stuck plan-approval lane**: a polecat session stopped while the bead is
   still `hooked` or `in_progress`, with no live session, no target process, no
   open MQ match, and no fresh dirty WIP. If `gt polecat check-recovery` reports
   `NEEDS_RECOVERY` and the runner finds an eligible candidate, Witness may run
   the scoped restart path after a fresh dry-run.

2. **Clean dead lane**: a stopped polecat with no session, no target process,
   no open MQ match, no ahead commits, no dirty files, no tree diff, and a
   worktree matching `origin/main`. The runner treats this as a clean dead
   lane and bypasses stale live-touch residue. The same-model restart is safe
   because no WIP is at risk.

In both cases, the runner records a git snapshot before restart and sends a
queued nudge that explicitly preserves WIP and warns against empty `gt done`.

## Dirty or ahead branches

Restart-only recovery never mutates files.

For dirty or ahead branches, the runner records before restart:

```bash
git -C <worktree> status --short --branch
git -C <worktree> rev-parse HEAD
git -C <worktree> rev-list --left-right --count origin/main...HEAD
git -C <worktree> diff --stat
```

The restart nudge explicitly says to preserve WIP, continue on the named
branch, use normal MR submission if appropriate, and never run empty `gt done`.

## Merge queue and terminal states

Open MR, `mq_record_found=true`, active gate, active submit, live build/test,
or live-touch means the work is owned. Witness monitors only.

Closed or rejected MR is not open ownership, but the runner reads and records
the close reason before restart. Restart is allowed only if the source bead is
still `hooked` or `in_progress` and the close reason does not say `merged`,
`superseded`, `terminal`, `no-action`, `duplicate`, or `wontfix`.

If the bead is terminal or `safe_to_nuke=true`, Witness must not use this
policy. That case belongs to Mayor escalation or a separately approved scoped
canary.

## Action

After locks and immediately before action, the scoped runner reruns the full
eligibility check. If anything changed, it aborts, releases locks with reason,
and reports a rejected candidate. Witness monitors or escalates; it does not
hand-execute the action.

Allowed restart runner action sequence:

```bash
gt session start gastown/<polecat>
gt nudge gastown/<polecat> --mode=queue --stdin
```

The runner runs `gt session start` first, then immediately verifies within the
configured window that the live model matches the model assignment file. Only
after that verification passes does it send the queued work nudge.

If `gt session start` fails, no session appears within 30 seconds, or post-start
model verification fails, the runner records the failed attempt, does not send
the work nudge, and reports the failure. Witness must escalate if the budget is
exhausted.

## Locks and attempt budget

The scoped restart runner persists correlation id, timestamp, session status,
check-recovery JSON, git status/ahead/dirty/diffstat, MQ status, live-process
checks, live-touch checks, model assignment, and attempt count under:

```text
/home/ubuntu/gt-town/.runtime/witness-recovery-attempts/gastown/<polecat>/<bead>.jsonl
```

Witness must not edit that ledger by hand. It may read it when explaining a
decision.

Locks are held through the restart and the 2-minute post-start model/session
verification. Locks are released before the 15-30 minute monitoring window,
with the monitoring state recorded in the attempt log.

Fresh lock TTL is 10 minutes. If a lock is older than 10 minutes but any matching
session, child process, live-touch stamp, gate, submit, build, or test activity
exists, Witness must treat the lock as active and wait or escalate. A stale
lock may be moved aside only after worktree-scoped and child-process checks
prove no matching activity; Witness must record the reason.

Budget: at most 2 restart+nudge attempts per rig+polecat+bead per rolling 30
minutes, with at least 15 minutes between restart attempts. Attempt 2 is allowed
only if attempt 1 produced validated progress before stopping. If attempt 1
produced no validated progress, escalate. Runner exit code 75 from
model-preserving `gt session start` is terminal for this policy: escalate to
Mayor for model rotation; Witness must not rotate models itself.

Validated progress means at least one of:

- new commit on the named branch
- dirty diff content changed
- MR opened
- explicit blocker or supersession persisted
- test or gate command completed with captured pass/fail
- terminal reusable state

Session start alone is not progress.

## Prohibitions

Witness must not:

- reset, clean, delete, nuke, close beads, recreate molecules, or remove
  dependency edges by hand
- skip verification, run `gt done`, `gt mq submit`, `gt land`, push,
  force-push, or use `--merge=direct`
- run refinery gates, run-all-gates, or cargo gates as recovery
- remove worktrees
- run the scoped restart runner with `--fix` unless a dry-run just proved a
  stable allowlisted candidate and the invocation uses `--max-fixes 1`
- run `gt sling`, dispatch new work, or rotate a bead to a different model
- upgrade GT past the pinned Gas Town 1.2.0 line
- edit Dolt or Beads state directly

## Dolt and escalation

If GT or Dolt hangs for more than 5 seconds, errors, or returns unexpected
empty data, Witness stops recovery, captures:

```bash
gt dolt dump
gt dolt status
```

and escalates with the diagnostics.

Escalations must include correlation id, attempt count, diagnostics, and the
specific reason. Use nudge for routine restart instructions and mail for durable
handoff or escalation.

## Rollout status

Initial gastown policy: the scoped restart runner has been generalized to
accept `--rig gastown` and to derive rig-specific patterns (wisp IDs, live-touch
stamps, process matchers, mayor-rig guard) from the `--rig` argument instead of
hardcoding the polybot rig. The runner remains report-only by default.

Witness may run report-only dry-runs immediately. `--fix --max-fixes 1` requires
a supervised canary against a real eligible stuck lane. Record canary outcomes
in the source bead notes.

## Town-wide ready dispatch is not Witness scope

Witness handles **polecat lifecycle** for its own rig: restart-recovery,
needs-recovery triage, merge-queue saturation detection. It does **not**
choose new ready beads across the town or reconcile the model mix fleet-wide.

Town-wide fleet fill lives one layer up:

- **Mayor** owns town-wide state, escalation, and durable cross-rig routing.
- **`gt scheduler run`** dispatches queued sling-context beads under
  `scheduler.max_polecats` capacity; queued work preserves explicit `--agent`
  assignments recorded in the sling-context fields.
- **External dropins** (e.g. `gt-model-mix-maintainer.py`,
  `gt-mayor-autonudge.sh`) wrap the town-wide fleet fill when a strict
  per-model cap is required; they reconcile live lane counts *town-wide*
  before picking the next model. See `internal/fleet/mix.go` for the
  canonical choose-agent algorithm and its regression tests.

Witness must not run `gt sling`, dispatch new work across rigs, or alter the
model mix. If a town-wide ready work + free capacity condition persists and
the autonudge has not surfaced it, Witness should escalate to Mayor with the
diagnostics (`gt scheduler status --json`, `gt polecat list --all --json`)
rather than attempt dispatch itself.
