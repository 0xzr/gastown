#!/usr/bin/env bash
# =============================================================================
# WRAPPER-PRESERVE LIBRARY
# =============================================================================
#
# Detects whether ~/.local/bin/gt is the operational gt wrapper (a bash
# script that injects --agent, --merge=mr, model-status, etc. into the
# pinned gt binary) versus a raw ELF binary. When a wrapper is present,
# install tooling MUST install the ELF behind the wrapper as
# ~/.local/bin/gt-real-bin so that:
#
#   - The wrapper topology stays intact (PATH resolves gt to the script).
#   - `gt model-status`, the sling rotation, the town lifecycle guard, and
#     the model-assignments ledger all keep working.
#   - Fleet-fill and the model-mix scheduler keep their reliable signal.
#
# Source this file from Makefile install/safe-install targets, the cutover
# script, and any installer that drops an ELF at $INSTALL_DIR/gt. Set
# INSTALL_DIR and BINARY before sourcing if your caller does not.
#
# Public functions (all side-effect-free except where noted):
#   gt_install_is_wrapper [path]        -> returns 0 if path is the wrapper
#   gt_install_real_bin_path            -> echoes the path to install the ELF
#   gt_install_wrapper_path             -> echoes the wrapper path (INSTALL_DIR/gt)
#   gt_install_preserve_wrapper         -> atomically installs ELF behind wrapper
#                                          (MUTATING — performs install). Acquires
#                                          a flock, freezes the candidate ELF to a
#                                          staged copy, runs a pre-install canary on
#                                          the EXACT staged bytes that will be
#                                          installed, and snapshots the previous
#                                          binary so gt_install_rollback can
#                                          restore it (see gastown-cet.12.9).
#   gt_install_assert_wrapper_topology  -> exits 1 if topology is broken
#   gt_install_check_forward_only       -> like Makefile's check-forward-only
#                                          but uses the real-bin path
#   gt_install_rollback                 -> one-command restore to the newest
#                                          gt-real-bin.bak.<ts> snapshot
#                                          (MUTATING — replaces the live ELF).
#                                          (see gastown-cet.12.9)
#
# Environment:
#   INSTALL_DIR  (default: $HOME/.local/bin)
#   BINARY       (default: gt)
#   GT_REAL_BIN  (default: $INSTALL_DIR/gt-real-bin) — override per host
#   GT_FORCE_PLAIN_INSTALL=1 — bypass wrapper detection, treat as raw ELF
#   GT_INSTALL_LOCK_TIMEOUT (default: 60) — seconds to wait for the install
#       flock before giving up. Set to 0 for a non-blocking attempt.
#   GT_INSTALL_CANARY_TIMEOUT (default: 10) — seconds the pre-install health
#       gate allows the new ELF's `version` probe to run before treating it
#       as a hang. Set to 0 to skip the canary entirely (NOT recommended).
#       The probe is fail-closed on the bound: if GNU `timeout` is missing
#       the candidate is refused rather than run unbounded (codex finding #2).
#
# Side effects of gt_install_preserve_wrapper:
#   - Snapshots any existing wrapper to $INSTALL_DIR/gt.wrapper.bak.<ts>
#   - Copies the freshly built ELF to $GT_REAL_BIN
#   - Writes the wrapper back to $INSTALL_DIR/gt if it had to be moved
#     out of the way during install
#   - Acquires an exclusive flock on $INSTALL_DIR/.gt-install.lock for the
#     duration of the install so concurrent installers cannot race.
#   - Freezes the candidate ELF to a staged copy inside the lock, then runs
#     the pre-install canary on the EXACT staged bytes that will be installed.
#     A non-zero or hung probe aborts BEFORE the live binary is touched. The
#     freeze eliminates the canary-vs-copy TOCTOU for a mutable or symlinked
#     source (gastown-cet.12.9 rework).
# =============================================================================

# Guard against double-sourcing.
if [ -n "${GT_WRAPPER_PRESERVE_LOADED:-}" ]; then
    return 0 2>/dev/null || true
fi
GT_WRAPPER_PRESERVE_LOADED=1

# Wrapper marker — must appear in the FIRST 30 lines of any wrapper script
# we recognize as the operational gt wrapper. The header comment is the
# stable, human-edited invariant; relying on file mode or shebang alone is
# too permissive (any text file passes) and relying on the file size is too
# brittle (the wrapper is intentionally append-only).
#
# Keep this in sync with the header in ~/.local/bin/gt on the operational
# host. If the wrapper is ever rewritten, update both sides.
GT_WRAPPER_MARKER='gt wrapper — guarantees the current validation model-mix'

# --- install lock (gastown-cet.12.9) -------------------------------------
# Concurrent installs of the pinned gt binary are the failure mode that can
# kill every polecat at once: two installers writing to gt-real-bin (or the
# plain public path) interleave cp/mv and leave a half-written ELF on PATH.
# We serialize installs with an exclusive flock on a sidecar lockfile in the
# install directory. The lock is held for the duration of preserve_wrapper
# only; it is never held across a build.
#
# GT_INSTALL_LOCK_TIMEOUT (default 60s) bounds how long a contending
# installer waits. 0 means "do not block — fail fast if contended".

# gt_install_lock_path echoes the sidecar lockfile path. Lives next to the
# binary so it tracks INSTALL_DIR overrides (tests use a temp HOME).
gt_install_lock_path() {
    local install_dir="${INSTALL_DIR:-$HOME/.local/bin}"
    printf '%s/.gt-install.lock\n' "$install_dir"
}

# gt_install_canary <elf> returns 0 if <elf> looks healthy enough to install:
#   - it is a regular file
#   - its first byte is an ELF magic byte (0x7F or 'E')
#   - it is executable
#   - `<elf> version` exits 0 within GT_INSTALL_CANARY_TIMEOUT seconds
# A non-zero return means the new build is bad or hung and MUST NOT replace
# the live binary. This is the "pre-install canary/health gate" from
# gastown-cet.12.9: it catches a corrupted pinned ELF before it is installed
# behind the preserved wrapper, which is the failure mode that killed all
# polecats at once.
#
# Fail-closed semantics: the version probe is BOUNDED or it does not run. If
# GNU `timeout` is unavailable we cannot enforce the bound, so we refuse to
# run the candidate at all (it would hold the install/rollback flock while a
# hung binary ran unbounded) rather than run it unbounded
# (gastown-cet.12.9 rework, codex finding #2). GT_INSTALL_CANARY_TIMEOUT=0
# is the documented opt-out: skip the probe and accept the ELF/executable
# magic checks alone.
gt_install_canary() {
    local elf="${1:?gt_install_canary requires <elf>}"
    local timeout_s="${GT_INSTALL_CANARY_TIMEOUT:-10}"

    if [ ! -f "$elf" ]; then
        echo "gt_install_canary: $elf is not a regular file" >&2
        return 1
    fi
    local first_byte
    first_byte="$(dd if="$elf" bs=1 count=1 status=none 2>/dev/null | od -An -c | tr -d ' \n')"
    case "$first_byte" in
        177|E) : ;;           # ELF magic — proceed
        *)
            echo "gt_install_canary: $elf is not an ELF (first byte: $first_byte)" >&2
            return 1
            ;;
    esac
    if [ ! -x "$elf" ]; then
        echo "gt_install_canary: $elf is not executable" >&2
        return 1
    fi

    # Skip the version probe when explicitly disabled. This is an escape
    # hatch for hosts where the new binary cannot run in the build sandbox
    # (e.g. cross-compiled); default behavior is to probe.
    if [ "$timeout_s" = "0" ]; then
        return 0
    fi

    # Run the new binary's `version` with a hard timeout. A hung or
    # non-zero exit means the build is broken: abort before touching the
    # live slot.
    #
    # If GNU `timeout` is UNAVAILABLE, we cannot bound the probe. Running the
    # candidate unbounded is unsafe: this runs while the install/rollback flock
    # is held, so a hung `version` would hold the lock indefinitely and a bad
    # binary that hangs (the exact failure mode a canary exists to catch) would
    # never be detected. Fail closed instead — refuse to install a candidate
    # we cannot bound (gastown-cet.12.9 rework, codex finding #2). An operator
    # who cannot install coreutils may set GT_INSTALL_CANARY_TIMEOUT=0 to accept
    # the magic/elf/executable checks alone (the documented escape hatch).
    if ! command -v timeout >/dev/null 2>&1; then
        echo "gt_install_canary: GNU timeout unavailable; cannot bound the version probe for $elf" >&2
        echo "  refusing to run an unbounded candidate while the install flock is held" >&2
        echo "  (set GT_INSTALL_CANARY_TIMEOUT=0 to skip the probe, or install coreutils)" >&2
        return 1
    fi
    if ! timeout "$timeout_s" "$elf" version >/dev/null 2>&1; then
        echo "gt_install_canary: $elf version probe failed or timed out (${timeout_s}s)" >&2
        return 1
    fi
    return 0
}

# gt_install_with_lock <command...> runs the given command inside a subshell
# that holds the exclusive install flock for its duration. The command's exit
# status propagates as the function's return. This is the single serialization
# point for installs and rollbacks (gastown-cet.12.9): the body is plain code
# and need not know about locking.
#
# Why a subshell-and-fd rather than acquire/release helpers: a file descriptor
# holding a flock cannot be handed back across a function boundary in POSIX
# sh / bash (the fd dies with the subshell that opened it). Wrapping the body
# in `( flock N; "$@" ) N>"$lock"` keeps the lock's lifetime tied to exactly
# the install body, which is the only safe lifetime for it.
gt_install_with_lock() {
    if [ "$#" -eq 0 ]; then
        echo "gt_install_with_lock: requires a command" >&2
        return 2
    fi
    local lock_file
    lock_file="$(gt_install_lock_path)"
    local timeout_s="${GT_INSTALL_LOCK_TIMEOUT:-60}"
    mkdir -p "$(dirname "$lock_file")" 2>/dev/null || true

    (
        if [ "$timeout_s" = "0" ]; then
            if ! flock -n 9; then
                echo "gt_install_with_lock: install in progress ($lock_file); try again" >&2
                exit 2
            fi
        else
            if ! flock -w "$timeout_s" 9; then
                echo "gt_install_with_lock: timed out after ${timeout_s}s waiting for $lock_file" >&2
                exit 2
            fi
        fi
        "$@"
    ) 9>"$lock_file"
}

# gt_install_rollback restores the live ELF from the newest
# gt-real-bin.bak.<ts> snapshot. One-command rollback (gastown-cet.12.9).
# Honors the same flock so a rollback cannot race an in-flight install.
#
# Arguments:
#   $1 (optional) — explicit snapshot path to restore. Default: newest .bak.
# Environment:
#   INSTALL_DIR, BINARY, GT_REAL_BIN — as above
# Behavior:
#   - Locates the newest gt-real-bin.bak.<ts> NORMAL install snapshot (or
#     uses $1). Pre-rollback snapshots (.bak.pre-rollback.*) are deliberately
#     excluded from the default selection because they capture the bad binary
#     a rollback was recovering from, not a known-good install (see below).
#   - Snapshots the CURRENT live ELF to a .bak.pre-rollback.<ts> first (so the
#     rollback itself is reversible), then atomically restores the chosen
#     snapshot.
#   - Re-runs the canary on the restored binary; refuses to install a
#     snapshot that fails the health gate (you'd just trade one bad binary
#     for another).
#   - Re-asserts wrapper topology afterwards.
gt_install_rollback() {
    gt_install_with_lock _gt_install_rollback_body "${@}"
}

# _gt_install_rollback_body is the lock-guarded implementation of rollback.
# Callers should invoke gt_install_rollback, not this function directly.
_gt_install_rollback_body() {
    local chosen="${1:-}"
    local install_dir="${INSTALL_DIR:-$HOME/.local/bin}"
    local binary="${BINARY:-gt}"
    local wrapper="$install_dir/$binary"
    local real_bin
    real_bin="$(gt_install_real_bin_path)"

    local ts
    ts="$(date -u +%Y%m%dT%H%M%SZ 2>/dev/null || date +%s)"

    # Resolve the snapshot to restore.
    local restore="$chosen"
    if [ -z "$restore" ]; then
        # Newest NORMAL install backup by lexical timestamp sort (the UTC
        # YYYYmmddTHHMMSSZ format sorts chronologically). We deliberately
        # EXCLUDE pre-rollback snapshots (gt-real-bin.bak.pre-rollback.*):
        # those capture the binary that was live immediately before a
        # rollback — which is by definition the bad build we were recovering
        # FROM. Under `sort -r`, "pre-rollback" sorts AFTER digit timestamps
        # ('p' > '2'), so a bare `sort -r | head -1` over every .bak.* would
        # return a pre-rollback snapshot whenever one exists, and a later
        # no-argument rollback would restore the bad-current binary instead
        # of the newest known-good install backup (gastown-cet.12.9 rework).
        # The .bak.<ts> install backups always start with a digit; match only
        # those so the default rollback picks a normal install snapshot.
        restore="$(ls -1 "$real_bin".bak.[0-9]* 2>/dev/null | sort -r | head -1 || true)"
    fi
    if [ -z "$restore" ] || [ ! -f "$restore" ]; then
        echo "gt_install_rollback: no rollback snapshot found at $real_bin.bak.*" >&2
        echo "  pass an explicit snapshot path as \$1 if one exists elsewhere" >&2
        return 1
    fi

    # Canary the candidate BEFORE touching the live slot. A bad snapshot
    # is not a recovery. The .bak snapshots are stored mode 0644 (non-
    # executable) so they are never accidentally invoked from PATH; probe a
    # temporary executable copy instead — the rollback itself chmods the
    # restored file 0755 before renaming it into the live slot.
    local cand_probe="$real_bin.canary.$$"
    if ! cp "$restore" "$cand_probe"; then
        echo "gt_install_rollback: cp $restore $cand_probe failed" >&2
        return 1
    fi
    chmod 0755 "$cand_probe" 2>/dev/null || true
    if ! gt_install_canary "$cand_probe"; then
        echo "gt_install_rollback: candidate $restore failed the canary; refusing to restore" >&2
        rm -f "$cand_probe" 2>/dev/null || true
        return 1
    fi
    rm -f "$cand_probe" 2>/dev/null || true

    # Snapshot the current live ELF so this rollback is itself reversible.
    if [ -f "$real_bin" ]; then
        local pre="$real_bin.bak.pre-rollback.$ts"
        if ! cp "$real_bin" "$pre"; then
            echo "gt_install_rollback: could not snapshot current $real_bin → $pre" >&2
            return 1
        fi
        chmod 0644 "$pre" 2>/dev/null || true
        echo "gt_install_rollback: snapshotted current binary $real_bin → $pre"
    fi

    # Atomic restore: copy snapshot to a staging file, then rename over the
    # live slot. We already canaried the source, so the staged copy is good.
    local staged="$real_bin.rollback.$$"
    if ! cp "$restore" "$staged"; then
        echo "gt_install_rollback: cp $restore $staged failed" >&2
        rm -f "$staged" 2>/dev/null || true
        return 1
    fi
    chmod 0755 "$staged" || true
    if ! mv "$staged" "$real_bin"; then
        echo "gt_install_rollback: mv $staged $real_bin failed" >&2
        rm -f "$staged" 2>/dev/null || true
        return 1
    fi

    echo "gt_install_rollback: restored $restore → $real_bin"
    # If the public path is a wrapper, re-assert topology so the operator
    # gets a clear pass/fail alongside the restore. A plain install has no
    # wrapper to assert; that is fine.
    if gt_install_is_wrapper "$wrapper"; then
        if ! gt_install_assert_wrapper_topology; then
            echo "gt_install_rollback: WARNING topology broken after rollback" >&2
            return 1
        fi
    fi
    return 0
}

# gt_install_wrapper_path echoes the canonical public path (the slot that
# PATH resolves first). Override BINARY for non-gt binaries.
gt_install_wrapper_path() {
    local install_dir="${INSTALL_DIR:-$HOME/.local/bin}"
    local binary="${BINARY:-gt}"
    printf '%s/%s\n' "$install_dir" "$binary"
}

# gt_install_real_bin_path echoes the path the ELF should land at. Honors
# GT_REAL_BIN so a host that has standardized on a different name (e.g.,
# gt-pinned, gt-real) does not have to fork this library.
gt_install_real_bin_path() {
    if [ -n "${GT_REAL_BIN:-}" ]; then
        printf '%s\n' "$GT_REAL_BIN"
        return 0
    fi
    local install_dir="${INSTALL_DIR:-$HOME/.local/bin}"
    local binary="${BINARY:-gt}"
    printf '%s/%s-real-bin\n' "$install_dir" "$binary"
}

# gt_install_is_wrapper [path]
# Returns 0 (true) if `path` is the operational wrapper, 1 (false) otherwise.
# Skips the heuristic if GT_FORCE_PLAIN_INSTALL=1 is set.
gt_install_is_wrapper() {
    local target="${1:-$(gt_install_wrapper_path)}"

    if [ "${GT_FORCE_PLAIN_INSTALL:-}" = "1" ]; then
        return 1
    fi

    if [ ! -e "$target" ] || [ ! -f "$target" ]; then
        return 1
    fi
    if [ ! -r "$target" ]; then
        return 1
    fi

    # An ELF binary starts with 0x7F 'E' 'L' 'F'. A wrapper script starts
    # with a shebang (#!). The first-byte check is the cheap, decisive test.
    local first_byte
    first_byte="$(dd if="$target" bs=1 count=1 status=none | od -An -c | tr -d ' \n')"
    case "$first_byte" in
        177|E) return 1 ;;   # 0x7F 'E' → ELF
        \#)    : ;;          # shebang → candidate wrapper
        *)     return 1 ;;   # unknown format; treat as not-the-wrapper
    esac

    # The shebang passes; now require the human-edited marker in the header.
    # Using head -n 30 keeps the check fast and resilient to trailing appends.
    if head -n 30 "$target" 2>/dev/null | grep -qF "$GT_WRAPPER_MARKER"; then
        return 0
    fi
    return 1
}

# gt_install_preserve_wrapper <source-elf>
# Atomically replace the installed gt binary while preserving the wrapper.
# Arguments:
#   $1 — path to the freshly built ELF (e.g., ./gt)
# Environment:
#   INSTALL_DIR, BINARY, GT_REAL_BIN — as described above
#   GT_INSTALL_LOCK_TIMEOUT, GT_INSTALL_CANARY_TIMEOUT — as described above
# Behavior:
#   - Acquires the exclusive install flock for the whole install so
#     concurrent installers cannot interleave writes (gastown-cet.12.9).
#   - Runs the pre-install canary on the source ELF; a bad or hung binary
#     aborts BEFORE the live slot is touched (gastown-cet.12.9).
#   - No wrapper at INSTALL_DIR/gt           → install ELF directly to it
#     (the legacy, plain-binary topology), atomically (write-new-then-rename).
#   - Wrapper present, GT_REAL_BIN path free → install ELF to GT_REAL_BIN
#     and leave the wrapper untouched.
#   - Wrapper present, GT_REAL_BIN occupied  → snapshot the ELF at
#     GT_REAL_BIN to gt-real-bin.bak.<ts>, install new ELF (atomic rename),
#     snapshot the wrapper to gt.wrapper.bak.<ts>, restore the wrapper from
#     snapshot if it had to be moved out of the way. The .bak.<ts> snapshot
#     is what gt_install_rollback restores.
gt_install_preserve_wrapper() {
    local src="${1:?gt_install_preserve_wrapper requires <source-elf>}"

    # Validate the source up front, before acquiring the lock, so a bad
    # argument fails fast rather than after waiting on a contended lock.
    if [ ! -f "$src" ]; then
        echo "gt_install_preserve_wrapper: source ELF $src not found" >&2
        return 2
    fi

    # The whole freeze-canary-install cycle runs under the lock. The previous
    # implementation canaried $src BEFORE the lock and then copied $src again
    # inside the critical section: a mutable or symlinked source could change
    # between the canary and the copy (TOCTOU), letting a bad ELF reach the
    # live slot while the canary had blessed different bytes. We now freeze
    # the source to a staged copy under the lock and canary the EXACT staged
    # bytes that will be installed, so the validated bytes are the installed
    # bytes (gastown-cet.12.9 rework).
    gt_install_with_lock _gt_install_preserve_wrapper_body "$src"
}

# _gt_install_preserve_wrapper_body is the lock-guarded implementation.
# Callers should invoke gt_install_preserve_wrapper, not this function.
_gt_install_preserve_wrapper_body() {
    local src="${1:?}"
    local install_dir="${INSTALL_DIR:-$HOME/.local/bin}"
    local binary="${BINARY:-gt}"
    local wrapper="$install_dir/$binary"
    local real_bin
    real_bin="$(gt_install_real_bin_path)"

    mkdir -p "$install_dir" || return 3

    # FREEZE the candidate: copy $src to a staging file ONCE, under the lock.
    # Everything that follows — the canary and the install — operates on this
    # frozen copy, so the bytes the canary validates are the bytes that reach
    # the live slot. A symlinked or concurrently-mutated $src cannot change
    # them between canary and install (gastown-cet.12.9 rework).
    local staged="$install_dir/.gt-install.stage.$$"
    if ! cp "$src" "$staged"; then
        echo "gt_install_preserve_wrapper: cp $src $staged (freeze) failed" >&2
        rm -f "$staged" 2>/dev/null || true
        return 4
    fi
    chmod 0755 "$staged" || true

    # Pre-install canary on the EXACT staged bytes that will be installed. A
    # bad or hung build aborts BEFORE the live binary is touched. The canary
    # is fail-closed on both the exit code AND the timer: if the bound cannot
    # be enforced (no `timeout`), the candidate is refused outright rather
    # than run unbounded under the lock (gastown-cet.12.9 rework, codex #2).
    if ! gt_install_canary "$staged"; then
        echo "gt_install_preserve_wrapper: staged ELF $staged failed the pre-install canary; refusing to install" >&2
        rm -f "$staged" 2>/dev/null || true
        return 11
    fi

    # Case A: no wrapper present — classic plain install. Atomic: rename the
    # staged (canary-passed) copy over the public path. Renaming is atomic on
    # POSIX filesystems, so no reader ever sees a half-written ELF at the
    # public path (gastown-cet.12.9).
    if ! gt_install_is_wrapper "$wrapper"; then
        # If something else is at the real-bin path from a previous wrapper
        # install, leave it alone (we are about to install the ELF to
        # $wrapper anyway and that real-bin path is only meaningful when
        # the wrapper is there).
        if ! mv "$staged" "$wrapper"; then
            echo "gt_install_preserve_wrapper: mv $staged $wrapper failed" >&2
            rm -f "$staged" 2>/dev/null || true
            return 5
        fi
        echo "gt_install_preserve_wrapper: plain install → $wrapper"
        return 0
    fi

    # Case B: wrapper present — install the ELF behind it as gt-real-bin.
    local ts
    ts="$(date -u +%Y%m%dT%H%M%SZ 2>/dev/null || date +%s)"

    # Snapshot the existing ELF at real-bin (if any) so we can roll back if
    # the new build is bad.
    if [ -e "$real_bin" ] && [ ! -f "$real_bin" ]; then
        echo "gt_install_preserve_wrapper: $real_bin exists and is not a regular file; refusing" >&2
        rm -f "$staged" 2>/dev/null || true
        return 6
    fi
    if [ -f "$real_bin" ]; then
        local bak="$real_bin.bak.$ts"
        if ! cp "$real_bin" "$bak"; then
            echo "gt_install_preserve_wrapper: backup $real_bin → $bak failed" >&2
            rm -f "$staged" 2>/dev/null || true
            return 7
        fi
        chmod 0644 "$bak" 2>/dev/null || true
        echo "gt_install_preserve_wrapper: backed up previous binary $real_bin → $bak"
    fi

    # Atomic ELF install: rename the staged (canary-passed) copy over real-bin.
    # Rename is atomic on POSIX filesystems, so the wrapper never dispatches
    # to a half-written ELF (gastown-cet.12.9). The staged copy is the
    # validated bytes, so no second copy of $src is taken here.
    if ! mv "$staged" "$real_bin"; then
        echo "gt_install_preserve_wrapper: mv $staged $real_bin failed" >&2
        rm -f "$staged" 2>/dev/null || true
        return 9
    fi

    # Belt-and-suspenders: the wrapper was never moved out of the way, but if
    # any earlier iteration of this installer accidentally clobbered it,
    # re-detect now and surface the problem loudly rather than silently
    # shipping a broken topology.
    if ! gt_install_is_wrapper "$wrapper"; then
        echo "gt_install_preserve_wrapper: WARNING wrapper at $wrapper is no longer recognized" >&2
        echo "  expected marker: $GT_WRAPPER_MARKER" >&2
        echo "  refusing to auto-restore; manual intervention required" >&2
        return 10
    fi

    echo "gt_install_preserve_wrapper: installed ELF → $real_bin"
    echo "  wrapper preserved at $wrapper"
    echo "  rollback: gt_install_rollback (restores $real_bin.bak.<ts>)"
    return 0
}

# gt_install_assert_wrapper_topology
# Exits 1 with a remediation message if the topology is broken. Returns 0
# when nothing is installed yet (fresh host) or when the wrapper is intact
# and the real-bin ELF is present.
gt_install_assert_wrapper_topology() {
    local wrapper
    wrapper="$(gt_install_wrapper_path)"
    local real_bin
    real_bin="$(gt_install_real_bin_path)"

    # Nothing installed yet → nothing to assert.
    if [ ! -e "$wrapper" ] && [ ! -e "$real_bin" ]; then
        return 0
    fi

    # Wrapper-topology case: wrapper at $wrapper, ELF at $real_bin.
    if gt_install_is_wrapper "$wrapper"; then
        if [ ! -f "$real_bin" ]; then
            echo "wrapper-topology: wrapper at $wrapper but ELF missing at $real_bin" >&2
            echo "  remediation: re-run 'make safe-install' from the source repo" >&2
            echo "                (it will rebuild the ELF into $real_bin without touching the wrapper)" >&2
            return 1
        fi
        if [ ! -x "$real_bin" ]; then
            echo "wrapper-topology: $real_bin is not executable" >&2
            echo "  remediation: chmod +x $real_bin" >&2
            return 1
        fi
        # Confirm the ELF is actually an ELF, not a stale text file.
        local first_byte
        first_byte="$(dd if="$real_bin" bs=1 count=1 status=none 2>/dev/null | od -An -c | tr -d ' \n')"
        case "$first_byte" in
            177|E)
                # Looks like an ELF; good.
                ;;
            *)
                echo "wrapper-topology: $real_bin does not appear to be an ELF binary" >&2
                echo "  remediation: re-run 'make safe-install' to overwrite it" >&2
                return 1
                ;;
        esac
        return 0
    fi

    # Plain-binary case: $wrapper is itself the ELF. That is allowed; only
    # fail if it does not look like an ELF (would indicate a half-written
    # install).
    if [ -e "$wrapper" ]; then
        local first_byte
        first_byte="$(dd if="$wrapper" bs=1 count=1 status=none 2>/dev/null | od -An -c | tr -d ' \n')"
        case "$first_byte" in
            177|E) return 0 ;;
            *)
                echo "wrapper-topology: $wrapper exists but is neither wrapper nor ELF" >&2
                echo "  remediation: manual inspection required" >&2
                return 1
                ;;
        esac
    fi

    return 0
}

# gt_install_check_forward_only
# Forward-only check that reads the commit from the real-bin ELF rather than
# from the wrapper path. Mirrors Makefile's check-forward-only so the cutover
# script can rely on it.
gt_install_check_forward_only() {
    local repo_dir="${1:-$REPO_ROOT}"
    local real_bin
    real_bin="$(gt_install_real_bin_path)"
    local wrapper
    wrapper="$(gt_install_wrapper_path)"

    # Pick the path that actually carries the binary's version string.
    # Preference order: real-bin (wrapper topology) > wrapper (plain).
    local probe="$wrapper"
    if [ -f "$real_bin" ]; then
        probe="$real_bin"
    fi
    if [ ! -x "$probe" ]; then
        echo "gt_install_check_forward_only: $probe not executable; skipping" >&2
        return 0
    fi

    local installed_commit
    installed_commit="$("$probe" version --verbose 2>/dev/null | grep -o '@[a-f0-9]*' | head -1 | tr -d '@' || true)"
    if [ -z "$installed_commit" ]; then
        echo "gt_install_check_forward_only: cannot read commit from $probe; skipping"
        return 0
    fi

    local head_commit
    head_commit="$(git -C "$repo_dir" rev-parse HEAD 2>/dev/null || true)"
    if [ -z "$head_commit" ]; then
        echo "gt_install_check_forward_only: cannot read HEAD from $repo_dir; skipping"
        return 0
    fi

    if git -C "$repo_dir" merge-base --is-ancestor "$installed_commit" "$head_commit" 2>/dev/null; then
        echo "gt_install_check_forward_only: OK ($installed_commit is ancestor of $head_commit)"
        return 0
    fi

    echo "ERROR: HEAD $head_commit is not a descendant of installed binary $installed_commit" >&2
    echo "Refusing to install an older or diverged build." >&2
    return 1
}
