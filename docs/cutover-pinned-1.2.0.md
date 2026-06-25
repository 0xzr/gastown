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
- Backs up the previous binary to a predictable sibling path
  (`~/.local/bin/gt-real-bin.before-pinned-1.2.0-cutover` when a wrapper is in
  use, or `~/.local/bin/gt.before-pinned-1.2.0-cutover` in the legacy plain
  topology) so rollback is a single `cp`.
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

The cutover script copies the currently installed binary to a predictable
sibling backup path before installing the new one. The exact path is recorded in
the cutover evidence file, because it depends on whether the host uses the
wrapper topology.

A dedicated rollback script performs the restore atomically, verifies the
restored binary, and appends rollback metadata to the evidence record:

```bash
scripts/rollback-pinned-1.2.0.sh
```

By default it reads `$GT_TOWN_ROOT/.runtime/pinned-1.2.0-cutover.json`. To use a
different evidence file:

```bash
scripts/rollback-pinned-1.2.0.sh --evidence /path/to/cutover.json
```

The script refuses to restore a backup that does not look like an ELF binary,
and it runs the same wrapper-topology assertion used by `gt doctor` after the
restore.

For manual rollback, copy the recorded backup to the recorded installed path:

- Wrapper topology (normal): backup is `~/.local/bin/gt-real-bin.before-pinned-1.2.0-cutover`
  and rollback restores the ELF behind the wrapper:
  ```bash
  cp ~/.local/bin/gt-real-bin.before-pinned-1.2.0-cutover ~/.local/bin/gt-real-bin
  ```
- Plain topology (legacy): backup is `~/.local/bin/gt.before-pinned-1.2.0-cutover`
  and rollback restores the public binary:
  ```bash
  cp ~/.local/bin/gt.before-pinned-1.2.0-cutover ~/.local/bin/gt
  ```

The cutover evidence record (see below) stores `backup_binary`,
`installed_binary`, and `public_path` so operators can verify exactly what was
replaced, and the appended `rollback` object records when the restore happened
and what version was active before/after.

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

## Operational notes

- The throttle state file at `$TOWN_ROOT/witness/rework-deferred-throttle.json`
  is durable across witness/daemon restarts. Existing records survive the
  cutover, so already-throttled tuples continue to be suppressed correctly.
- First occurrences and tuple changes still emit immediately; the cutover does
  not change throttle semantics.
- Do not run this script from a feature or polecat branch unless you are
  intentionally deploying that branch. The Makefile's forward-only check
  prevents downgrade/crash-loop regressions.
