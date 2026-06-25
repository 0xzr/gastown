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
#                                          (MUTATING — performs install)
#   gt_install_assert_wrapper_topology  -> exits 1 if topology is broken
#   gt_install_check_forward_only       -> like Makefile's check-forward-only
#                                          but uses the real-bin path
#
# Environment:
#   INSTALL_DIR  (default: $HOME/.local/bin)
#   BINARY       (default: gt)
#   GT_REAL_BIN  (default: $INSTALL_DIR/gt-real-bin) — override per host
#   GT_FORCE_PLAIN_INSTALL=1 — bypass wrapper detection, treat as raw ELF
#
# Side effects of gt_install_preserve_wrapper:
#   - Snapshots any existing wrapper to $INSTALL_DIR/gt.wrapper.bak.<ts>
#   - Copies the freshly built ELF to $GT_REAL_BIN
#   - Writes the wrapper back to $INSTALL_DIR/gt if it had to be moved
#     out of the way during install
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
# Behavior:
#   - No wrapper at INSTALL_DIR/gt           → install ELF directly to it
#     (the legacy, plain-binary topology).
#   - Wrapper present, GT_REAL_BIN path free → install ELF to GT_REAL_BIN
#     and leave the wrapper untouched.
#   - Wrapper present, GT_REAL_BIN occupied  → snapshot the ELF at
#     GT_REAL_BIN to gt-real-bin.bak.<ts>, install new ELF, snapshot the
#     wrapper to gt.wrapper.bak.<ts>, restore the wrapper from snapshot
#     if it had to be moved out of the way.
gt_install_preserve_wrapper() {
    local src="${1:?gt_install_preserve_wrapper requires <source-elf>}"
    local install_dir="${INSTALL_DIR:-$HOME/.local/bin}"
    local binary="${BINARY:-gt}"
    local wrapper="$install_dir/$binary"
    local real_bin
    real_bin="$(gt_install_real_bin_path)"

    if [ ! -f "$src" ]; then
        echo "gt_install_preserve_wrapper: source ELF $src not found" >&2
        return 2
    fi

    mkdir -p "$install_dir" || return 3

    # Case A: no wrapper present — classic plain install.
    if ! gt_install_is_wrapper "$wrapper"; then
        # If something else is at the real-bin path from a previous wrapper
        # install, leave it alone (we are about to install the ELF to
        # $wrapper anyway and that real-bin path is only meaningful when
        # the wrapper is there).
        local tmp="$wrapper.new.$$"
        if ! cp "$src" "$tmp"; then
            echo "gt_install_preserve_wrapper: cp $src $tmp failed" >&2
            return 4
        fi
        chmod 0755 "$tmp" || true
        if ! mv "$tmp" "$wrapper"; then
            echo "gt_install_preserve_wrapper: mv $tmp $wrapper failed" >&2
            rm -f "$tmp" 2>/dev/null || true
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
        return 6
    fi
    if [ -f "$real_bin" ]; then
        local bak="$real_bin.bak.$ts"
        if ! cp "$real_bin" "$bak"; then
            echo "gt_install_preserve_wrapper: backup $real_bin → $bak failed" >&2
            return 7
        fi
        chmod 0644 "$bak" 2>/dev/null || true
        echo "gt_install_preserve_wrapper: backed up previous binary $real_bin → $bak"
    fi

    # Atomic-ish ELF install: write to .new, then rename over real-bin.
    local new_real="$real_bin.new.$$"
    if ! cp "$src" "$new_real"; then
        echo "gt_install_preserve_wrapper: cp $src $new_real failed" >&2
        return 8
    fi
    chmod 0755 "$new_real" || true
    if ! mv "$new_real" "$real_bin"; then
        echo "gt_install_preserve_wrapper: mv $new_real $real_bin failed" >&2
        rm -f "$new_real" 2>/dev/null || true
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
