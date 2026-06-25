# Pinned Gas Town 1.2.0 Runtime Cutover

This document describes how to deploy a `gt` binary that stays on the
operator-approved **1.2.0 runtime line** while carrying the merged hardening
fixes from `origin/main` (notably the `REWORK_DEFERRED` throttle from
`gastown-cet.11` and the hooked-polecats-should-be-`WORKING` fix from
`cfa97404`).

## Background

The live `~/.local/bin/gt` binary was built from an older 1.2.0-line commit
without these fixes. Upgrading it to the 1.2.1 line that `origin/main` reports
is forbidden by operator standing policy (dispatch regression #4220). This
cutover path rebuilds the current source tree, forces the reported version back
to `1.2.0`, and records durable evidence that the resulting binary contains the
required fixes.

## What the cutover changes

- Rebuilds `gt` from the current source with:
  - `Version=1.2.0` (so `gt version` reports the approved runtime)
  - `PINNED_RUNTIME_LINE=1.2.0` (surfaced by `gt version --verbose`)
  - `FEATURE_FLAGS=rework-deferred-throttle,hooked-polecats-working`
- Performs an atomic safe-install to `~/.local/bin/gt`.
- Backs up the previous binary to `~/.local/bin/gt.before-pinned-1.2.0-cutover`
  so rollback is a single `cp`.
- Verifies the installed binary with `gt version --verbose` and `gt witness
  rework-deferred dry-run`.
- Records cutover evidence (including backup path and pre-cutover version) in
  `$GT_TOWN_ROOT/.runtime/pinned-1.2.0-cutover.json`.

## What is NOT restarted automatically

The `safe-install` target does **not** restart the daemon or any running
witness sessions. They will continue running the previous binary image until
restarted. See the restart section below for the recommended order.

## Running the cutover

From the gastown source repo worktree:

```bash
scripts/cutover-pinned-1.2.0.sh
```

To override the forward-only safety check (only when you are certain the new
commit is safe):

```bash
scripts/cutover-pinned-1.2.0.sh --skip-forward-check
```

## Rolling back

The cutover script now copies the currently installed binary to
`~/.local/bin/gt.before-pinned-1.2.0-cutover` before installing the new one.
To roll back immediately:

```bash
cp ~/.local/bin/gt.before-pinned-1.2.0-cutover ~/.local/bin/gt
```

The cutover evidence record (see below) also stores the backup path and the
pre-cutover version string so operators can verify what was replaced.

## Post-cutover verification

1. **Version line:**
   ```bash
   gt version
   gt version --verbose
   ```
   Expected:
   - `gt version 1.2.0 (...)`
   - `Pinned runtime line: 1.2.0`
   - `Hardening fixes: rework-deferred-throttle, hooked-polecats-working`

2. **Throttle regression coverage:**
   ```bash
   gt witness rework-deferred dry-run
   ```
   Expected: exit 0 with `REWORK_DEFERRED throttle dry-run passed` and tuples
   for `gt-hold1`, `gt-park1`, and `gt-work999`.

3. **Evidence record:**
   ```bash
   cat "${GT_TOWN_ROOT:-/home/ubuntu/gt-town}/.runtime/pinned-1.2.0-cutover.json"
   ```
   Contains: cutover timestamp, source repo path, installed binary path, build
   commit, build time, and dry-run result.

## Restart requirements

Yes, a restart is required for running daemons/witnesses to pick up the new
binary:

```bash
# If a daemon is running:
gt daemon restart

# Or for a single rig's witness:
gt witness restart <rig>
```

After restart, confirm the processes are running the new binary by checking
`/proc/<pid>/exe` or by verifying the witness still reports correctly:

```bash
gt witness status <rig>
```

### Why the restart matters for the throttle

`safe-install` swaps the on-disk ELF but does **not** touch already-running
processes. A daemon or witness started before the cutover keeps the old binary
image in memory and will keep emitting un-throttled `REWORK_DEFERRED` notices —
including rollups that read "0 suppressed" — until it is restarted. This is the
operational root cause behind the gastown-3ip report: the fixed binary was
installed at 09:33, but the GT daemon (started 07:57) was still running the
pre-fix code, so `gt witness rework-deferred list` stayed empty and repeated
`gt-work999` notices kept reaching the Mayor. Restarting the daemon at 13:42
loaded the fixed binary and the symptom cleared.

## Post-restart live verification

The dry-run proves the throttle logic in a temp state directory; it does **not**
prove the *live* emitter is wired through the throttle. After restarting the
daemon/witness, verify the durable state actually populates from real patrol
traffic and that a repeated blocked tuple is suppressed and rolled up correctly:

1. **Trigger a real blocked-rework tuple** (or wait for the next patrol to
   re-detect one). A tuple is any (rig, bead, polecat, decision, source status)
   that an active DEFER/HOLD/PARK Mayor decision is blocking.

2. **Confirm durable state was written:**
   ```bash
   gt witness rework-deferred list
   ```
   Expected: a row for the blocked tuple with `suppressed >= 0`. An empty list
   after a known-blocked tuple means the live emitter is **not** routing through
   `notifyMayorOfReworkBlocked` → `EvaluateReworkDeferred` (a regression). All
   current emitters do route through it (handlers.go `HandleMergeFailed` at the
   merge-failed path and `resetAbandonedBead` at the abandoned-bead path); there
   is no parallel live emitter.

3. **Confirm a repeat is suppressed** (run a patrol or re-trigger the same
   tuple inside the 1-hour window): the stderr log line reads
   `REWORK_DEFERRED suppressed for <rig>/<polecat> bead=<bead> (suppressed_count=N ...)`
   and **no** new mail reaches the Mayor for that tuple.

4. **Confirm a rollup reports the real count** (advance past the window, e.g. by
   waiting or by re-triggering after the window elapses): the rollup subject and
   body read `N suppressed` / `rollup of N identical REWORK_DEFERRED notices`
   where `N` is the number actually suppressed — never `0 suppressed`. A `0
   suppressed` rollup is the gastown-3ip regression and must be reported.

The durable state file is `$GT_TOWN_ROOT/witness/rework-deferred-throttle.json`
and survives daemon/witness restarts, so suppression continues correctly across
cutover.

## Operational notes

- The throttle state file at `$TOWN_ROOT/witness/rework-deferred-throttle.json`
  is durable across witness/daemon restarts. Existing records survive the
  cutover, so already-throttled tuples continue to be suppressed correctly.
- First occurrences and tuple changes still emit immediately; the cutover does
  not change throttle semantics.
- Do not run this script from a feature or polecat branch unless you are
  intentionally deploying that branch. The Makefile's forward-only check
  prevents downgrade/crash-loop regressions.
