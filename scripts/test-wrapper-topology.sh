#!/usr/bin/env bash
# =============================================================================
# Smoke test for scripts/lib/wrapper-preserve.sh.
#
# Exercises the four scenarios that motivated gastown-cet.16.1:
#
#   1. Fresh host with no wrapper       → ELF installed to ~/.local/bin/gt
#   2. Host with operational wrapper    → ELF installed to ~/.local/bin/gt-real-bin,
#                                         wrapper preserved
#   3. Host with wrapper + existing real-bin ELF → previous ELF snapshotted,
#                                                 new ELF installed, wrapper intact
#   4. Topology assertion fails fast    → missing real-bin ELF with wrapper present
#                                         returns non-zero with remediation message
#
# Each scenario runs in an isolated temp HOME so the real user install is
# never touched. The test exits non-zero on the first failure and prints a
# banner describing what passed/failed.
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
LIB="$SCRIPT_DIR/lib/wrapper-preserve.sh"

PASS=0
FAIL=0

# Colored output if stdout is a TTY.
if [ -t 1 ]; then
    GREEN=$'\033[0;32m'
    RED=$'\033[0;31m'
    NC=$'\033[0m'
else
    GREEN=""; RED=""; NC=""
fi

pass() { echo "${GREEN}PASS${NC}: $1"; PASS=$((PASS + 1)); }
fail() { echo "${RED}FAIL${NC}: $1"; FAIL=$((FAIL + 1)); }

# Resolve a real ELF binary to use as an install fixture. The pre-install
# canary actually RUNS the new binary's `version`, so fixtures must be
# genuine executables: `true` exits 0 (canary passes), `false` exits 1
# (canary rejects). Prefer /bin then /usr/bin, then PATH.
real_elf() {
    local name="${1:?real_elf needs a binary name}"
    for p in /bin/"$name" /usr/bin/"$name"; do
        if [ -x "$p" ]; then printf '%s\n' "$p"; return 0; fi
    done
    command -v "$name"
}
TRUE_ELF="$(real_elf true)"
FALSE_ELF="$(real_elf false)"
ECHO_ELF="$(real_elf echo)"

# A healthy ELF fixture: a copy of a real ELF that exits 0, so the pre-install
# canary's `version` probe passes. (Was a synthetic \x7FELF blob; that can no
# longer run under the canary gate added in gastown-cet.12.9.)
make_elf_fixture() {
    local elf="$1"
    if [ -z "$TRUE_ELF" ]; then
        echo "SKIP: no real 'true' ELF found on this host" >&2
        return 1
    fi
    cp "$TRUE_ELF" "$elf"
    chmod 0755 "$elf"
}

# A BAD ELF fixture: a real ELF that exits non-zero. The canary MUST reject it.
make_bad_elf_fixture() {
    local elf="$1"
    if [ -z "$FALSE_ELF" ]; then
        echo "SKIP: no real 'false' ELF found on this host" >&2
        return 1
    fi
    cp "$FALSE_ELF" "$elf"
    chmod 0755 "$elf"
}

# first_byte <path> echoes the od token of the file's first byte (for the
# ELF/shebang switch used throughout these scenarios).
first_byte() {
    dd if="$1" bs=1 count=1 status=none | od -An -c | tr -d ' \n'
}

# Scenario 1: fresh host, no wrapper at $HOME/.local/bin/gt.
scenario_plain_install() {
    local tmp
    tmp="$(mktemp -d)"
    trap "rm -rf '$tmp'" RETURN

    local install_dir="$tmp/home/.local/bin"
    mkdir -p "$install_dir"
    local elf="$tmp/source-gt"
    make_elf_fixture "$elf" || return 0

    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_preserve_wrapper '$elf'"

    if [ ! -f "$install_dir/gt" ]; then
        fail "scenario_plain_install: $install_dir/gt missing"
        return
    fi
    local first_byte
    first_byte="$(first_byte "$install_dir/gt")"
    case "$first_byte" in
        177|E) pass "scenario_plain_install: ELF installed to public path" ;;
        *)     fail "scenario_plain_install: expected ELF at public path, got byte: $first_byte" ;;
    esac
    if [ -e "$install_dir/gt-real-bin" ]; then
        fail "scenario_plain_install: gt-real-bin should NOT exist on plain install"
    else
        pass "scenario_plain_install: no stray gt-real-bin on plain install"
    fi
}

# Scenario 2: host with operational wrapper. ELF must install to gt-real-bin,
# wrapper must survive verbatim.
scenario_wrapper_preserved() {
    local tmp
    tmp="$(mktemp -d)"
    trap "rm -rf '$tmp'" RETURN

    local install_dir="$tmp/home/.local/bin"
    mkdir -p "$install_dir"
    local elf="$tmp/source-gt"
    make_elf_fixture "$elf" || return 0

    # Plant a wrapper that looks like the operational one.
    cat > "$install_dir/gt" <<'WRAPPER'
#!/usr/bin/env bash
# gt wrapper — guarantees the current validation model-mix on `gt sling`. If a
# sling has no explicit --agent, inject the next agent from the rotation
# (2 umans-glm, 2 umans-kimi, 2 m3) and default --merge=mr.
WRAPPER_SENTINEL="WRAPPER_INTACT_42"
WRAPPER
    chmod 0755 "$install_dir/gt"

    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_preserve_wrapper '$elf'"

    if [ ! -f "$install_dir/gt-real-bin" ]; then
        fail "scenario_wrapper_preserved: $install_dir/gt-real-bin missing"
        return
    fi
    pass "scenario_wrapper_preserved: ELF installed at gt-real-bin"

    if ! grep -qF "WRAPPER_SENTINEL=\"WRAPPER_INTACT_42\"" "$install_dir/gt"; then
        fail "scenario_wrapper_preserved: wrapper sentinel lost"
    else
        pass "scenario_wrapper_preserved: wrapper sentinel preserved verbatim"
    fi

    # Sanity: gt-real-bin must be the ELF (not a stale wrapper snapshot).
    local real_first
    real_first="$(first_byte "$install_dir/gt-real-bin")"
    case "$real_first" in
        177|E) pass "scenario_wrapper_preserved: gt-real-bin is an ELF" ;;
        *)     fail "scenario_wrapper_preserved: gt-real-bin is not an ELF -- got: $real_first" ;;
    esac

    # Sanity: public gt must still be a script.
    local public_first
    public_first="$(first_byte "$install_dir/gt")"
    case "$public_first" in
        \#) pass "scenario_wrapper_preserved: public gt remains a shebang script" ;;
        *)  fail "scenario_wrapper_preserved: public gt was clobbered -- got: $public_first" ;;
    esac
}

# Scenario 3: wrapper present + previous ELF at gt-real-bin. The previous ELF
# must be backed up with a timestamp suffix and the new ELF must replace it.
scenario_realbin_rotated() {
    local tmp
    tmp="$(mktemp -d)"
    trap "rm -rf '$tmp'" RETURN

    local install_dir="$tmp/home/.local/bin"
    mkdir -p "$install_dir"
    # New ELF: true (exits 0 → canary passes). Incumbent: a DISTINCT healthy
    # ELF (echo) so the rotation is provable by content.
    local elf="$tmp/source-gt"
    make_elf_fixture "$elf" || return 0
    [ -n "$ECHO_ELF" ] || { echo "SKIP scenario_realbin_rotated: no 'echo' ELF" >&2; return 0; }
    cp "$ECHO_ELF" "$install_dir/gt-real-bin"
    chmod 0755 "$install_dir/gt-real-bin"
    local old_ino
    old_ino="$(stat -c '%i' "$install_dir/gt-real-bin" 2>/dev/null || stat -f '%i' "$install_dir/gt-real-bin")"

    cat > "$install_dir/gt" <<'WRAPPER'
#!/usr/bin/env bash
# gt wrapper — guarantees the current validation model-mix on `gt sling`.
WRAPPER_INTACT=99
WRAPPER
    chmod 0755 "$install_dir/gt"

    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_preserve_wrapper '$elf'"

    # New ELF at gt-real-bin — a different inode than the old one proves it was
    # replaced (rename installs a fresh file rather than rewriting in place).
    local new_ino
    new_ino="$(stat -c '%i' "$install_dir/gt-real-bin" 2>/dev/null || stat -f '%i' "$install_dir/gt-real-bin")"
    case "$(first_byte "$install_dir/gt-real-bin")" in
        177|E)
            if [ "$new_ino" = "$old_ino" ]; then
                fail "scenario_realbin_rotated: gt-real-bin inode unchanged (not replaced)"
            else
                pass "scenario_realbin_rotated: gt-real-bin replaced with new ELF"
            fi
            ;;
        *) fail "scenario_realbin_rotated: gt-real-bin is not an ELF" ;;
    esac

    # Wrapper sentinel still intact.
    if grep -qF "WRAPPER_INTACT=99" "$install_dir/gt"; then
        pass "scenario_realbin_rotated: wrapper preserved across real-bin rotation"
    else
        fail "scenario_realbin_rotated: wrapper sentinel lost"
    fi

    # Backup of previous ELF exists with timestamp suffix and holds the OLD
    # binary (compare bytes; .bak is a copy so its inode differs).
    local bak
    bak="$(ls "$install_dir"/gt-real-bin.bak.* 2>/dev/null | grep -v 'pre-rollback' | head -1 || true)"
    if [ -z "$bak" ]; then
        fail "scenario_realbin_rotated: expected a gt-real-bin.bak.<ts> snapshot"
    elif cmp -s "$bak" "$ECHO_ELF"; then
        pass "scenario_realbin_rotated: previous ELF preserved at $bak"
    else
        fail "scenario_realbin_rotated: backup $bak does not match previous ELF"
    fi
}

# Scenario 4: topology assertion fails fast when wrapper is present but
# gt-real-bin is missing or non-executable.
scenario_topology_assertion_fails() {
    local tmp
    tmp="$(mktemp -d)"
    trap "rm -rf '$tmp'" RETURN

    local install_dir="$tmp/home/.local/bin"
    mkdir -p "$install_dir"

    cat > "$install_dir/gt" <<'WRAPPER'
#!/usr/bin/env bash
# gt wrapper — guarantees the current validation model-mix on `gt sling`.
WRAPPER
    chmod 0755 "$install_dir/gt"
    # Intentionally do NOT create gt-real-bin.

    local output rc
    local out_file="$tmp/assertion.out"
    # Disable errexit locally: this scenario deliberately exercises a failing
    # invocation and needs to read the rc + captured output without the
    # surrounding `set -e` bailing on the expected non-zero exit.
    set +e
    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" \
        bash -c 'source "$0"; gt_install_assert_wrapper_topology' "$LIB" >"$out_file" 2>&1
    rc=$?
    set -e
    output="$(cat "$out_file")"

    if [ "$rc" -eq 0 ]; then
        fail "scenario_topology_assertion_fails: assertion passed but should have failed"
    else
        pass "scenario_topology_assertion_fails: assertion failed with rc=$rc as expected"
    fi
    case "$output" in
        *"remediation"*) pass "scenario_topology_assertion_fails: output mentions remediation" ;;
        *)               fail "scenario_topology_assertion_fails: output missing remediation hint -- got: $output" ;;
    esac
}

# Scenario 5: a non-wrapper script (no marker) is treated as plain — install
# does not try to preserve arbitrary text at the public path.
scenario_non_wrapper_script_treated_as_plain() {
    local tmp
    tmp="$(mktemp -d)"
    trap "rm -rf '$tmp'" RETURN

    local install_dir="$tmp/home/.local/bin"
    mkdir -p "$install_dir"
    local elf="$tmp/source-gt"
    make_elf_fixture "$elf" || return 0

    # Plain text file at the public path that is NOT the wrapper.
    printf '#!/usr/bin/env bash\necho hi\n' > "$install_dir/gt"
    chmod 0755 "$install_dir/gt"

    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_preserve_wrapper '$elf'"

    if [ -e "$install_dir/gt-real-bin" ]; then
        fail "scenario_non_wrapper_script_treated_as_plain: should not have created gt-real-bin"
    else
        pass "scenario_non_wrapper_script_treated_as_plain: no gt-real-bin created"
    fi

    local first
    first="$(first_byte "$install_dir/gt")"
    case "$first" in
        177|E) pass "scenario_non_wrapper_script_treated_as_plain: public path now ELF" ;;
        *)     fail "scenario_non_wrapper_script_treated_as_plain: public path is not ELF" ;;
    esac
}

# Scenario 6 (gastown-cet.12.9): a BAD ELF — one whose `version` exits
# non-zero — must be rejected by the pre-install canary BEFORE the live
# binary is touched. The live slot must be left untouched.
scenario_canary_rejects_bad_elf() {
    local tmp
    tmp="$(mktemp -d)"
    trap "rm -rf '$tmp'" RETURN

    local install_dir="$tmp/home/.local/bin"
    mkdir -p "$install_dir"
    local bad_elf="$tmp/bad-gt"
    make_bad_elf_fixture "$bad_elf" || return 0

    # Plant a live ELF at gt-real-bin behind a wrapper so we can prove the
    # canary left it alone. Use a healthy ELF (sleep) as the incumbent.
    local sleep_elf
    sleep_elf="$(real_elf sleep)"
    [ -n "$sleep_elf" ] || { echo "SKIP scenario_canary_rejects_bad_elf: no 'sleep' ELF" >&2; return 0; }
    cp "$sleep_elf" "$install_dir/gt-real-bin"
    chmod 0755 "$install_dir/gt-real-bin"
    local live_ino
    live_ino="$(stat -c '%i' "$install_dir/gt-real-bin" 2>/dev/null || stat -f '%i' "$install_dir/gt-real-bin")"

    cat > "$install_dir/gt" <<'WRAPPER'
#!/usr/bin/env bash
# gt wrapper — guarantees the current validation model-mix on `gt sling`.
WRAPPER
    chmod 0755 "$install_dir/gt"

    local out rc
    set +e
    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_preserve_wrapper '$bad_elf'" \
        >"$tmp/canary.out" 2>&1
    rc=$?
    set -e
    out="$(cat "$tmp/canary.out")"

    if [ "$rc" -eq 0 ]; then
        fail "scenario_canary_rejects_bad_elf: install succeeded for a bad ELF (rc=$rc)"
    else
        pass "scenario_canary_rejects_bad_elf: bad ELF rejected with rc=$rc"
    fi
    case "$out" in
        *"canary"*) pass "scenario_canary_rejects_bad_elf: output mentions canary gate" ;;
        *)          fail "scenario_canary_rejects_bad_elf: output missing canary mention -- got: $out" ;;
    esac

    # The live ELF must be unchanged (same inode — nothing was renamed over it).
    local now_ino
    now_ino="$(stat -c '%i' "$install_dir/gt-real-bin" 2>/dev/null || stat -f '%i' "$install_dir/gt-real-bin")"
    if [ "$now_ino" = "$live_ino" ]; then
        pass "scenario_canary_rejects_bad_elf: live binary left untouched"
    else
        fail "scenario_canary_rejects_bad_elf: live binary was modified despite canary"
    fi
}

# Scenario 7 (gastown-cet.12.9): GT_INSTALL_CANARY_TIMEOUT=0 skips the version
# probe, so a synthetic (non-runnable) ELF can again be installed. This is the
# escape hatch for cross-compiled builds that can't run on the build host.
scenario_canary_skip_installs_synthetic_elf() {
    local tmp
    tmp="$(mktemp -d)"
    trap "rm -rf '$tmp'" RETURN

    local install_dir="$tmp/home/.local/bin"
    mkdir -p "$install_dir"
    # A synthetic ELF that is NOT runnable (the old fixture shape).
    local synth="$tmp/synth-gt"
    printf '\x7FELF%1024s' " " > "$synth"
    chmod 0755 "$synth"

    cat > "$install_dir/gt" <<'WRAPPER'
#!/usr/bin/env bash
# gt wrapper — guarantees the current validation model-mix on `gt sling`.
WRAPPER
    chmod 0755 "$install_dir/gt"

    local rc
    set +e
    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" GT_INSTALL_CANARY_TIMEOUT=0 \
        bash -c "source '$LIB'; gt_install_preserve_wrapper '$synth'" \
        >"$tmp/skip.out" 2>&1
    rc=$?
    set -e

    if [ "$rc" -ne 0 ]; then
        fail "scenario_canary_skip_installs_synthetic_elf: install failed under canary-skip (rc=$rc)"
        cat "$tmp/skip.out" >&2
        return
    fi
    pass "scenario_canary_skip_installs_synthetic_elf: synthetic ELF installed under canary-skip"
    if [ ! -f "$install_dir/gt-real-bin" ]; then
        fail "scenario_canary_skip_installs_synthetic_elf: gt-real-bin missing"
    else
        pass "scenario_canary_skip_installs_synthetic_elf: ELF at gt-real-bin"
    fi
}

# Scenario 8 (gastown-cet.12.9): concurrent installs are serialized by the
# flock. A second installer started while the first holds the lock must block
# (not interleave) when given a wait budget, and fail fast when GT_INSTALL_LOCK_TIMEOUT=0.
scenario_concurrent_installs_serialized() {
    local tmp
    tmp="$(mktemp -d)"
    trap "rm -rf '$tmp'" RETURN

    local install_dir="$tmp/home/.local/bin"
    mkdir -p "$install_dir"
    local elf="$tmp/source-gt"
    make_elf_fixture "$elf" || return 0

    # Hold the lock in a background subshell that sleeps, then try to install
    # with a non-blocking attempt. The non-blocking install must report contention.
    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_with_lock sleep 2" &
    local holder=$!
    # Give the holder a moment to acquire.
    sleep 0.4

    local rc
    set +e
    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" GT_INSTALL_LOCK_TIMEOUT=0 \
        bash -c "source '$LIB'; gt_install_preserve_wrapper '$elf'" >"$tmp/contend.out" 2>&1
    rc=$?
    set -e
    wait "$holder" 2>/dev/null || true

    if [ "$rc" -eq 0 ]; then
        fail "scenario_concurrent_installs_serialized: install ran despite held lock"
    else
        pass "scenario_concurrent_installs_serialized: install blocked by lock (rc=$rc)"
    fi
    case "$(cat "$tmp/contend.out")" in
        *"in progress"*) pass "scenario_concurrent_installs_serialized: output reports contention" ;;
        *"timed out"*)  pass "scenario_concurrent_installs_serialized: output reports timeout" ;;
        *)              fail "scenario_concurrent_installs_serialized: output missing contention msg -- got: $(cat "$tmp/contend.out")" ;;
    esac
}

# Scenario 9 (gastown-cet.12.9): one-command rollback. After a wrapper-preserving
# install that snapshots the previous ELF, gt_install_rollback restores the
# previous ELF from the newest .bak.<ts> snapshot.
scenario_rollback_restores_previous_binary() {
    local tmp
    tmp="$(mktemp -d)"
    trap "rm -rf '$tmp'" RETURN

    local install_dir="$tmp/home/.local/bin"
    mkdir -p "$install_dir"

    # Incumbent: a distinct real ELF (echo, exits 0 → canary passes) behind a
    # wrapper. New build: true (also exits 0). Distinct content proves rotation.
    [ -n "$ECHO_ELF" ] || { echo "SKIP scenario_rollback: no 'echo' ELF" >&2; return 0; }
    cp "$ECHO_ELF" "$install_dir/gt-real-bin"
    chmod 0755 "$install_dir/gt-real-bin"
    local incumbent_ino
    incumbent_ino="$(stat -c '%i' "$install_dir/gt-real-bin" 2>/dev/null || stat -f '%i' "$install_dir/gt-real-bin")"

    cat > "$install_dir/gt" <<'WRAPPER'
#!/usr/bin/env bash
# gt wrapper — guarantees the current validation model-mix on `gt sling`.
WRAPPER
    chmod 0755 "$install_dir/gt"

    # New build: a DIFFERENT real ELF (true). Install it behind the wrapper.
    local new_elf="$tmp/new-gt"
    make_elf_fixture "$new_elf" || return 0
    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_preserve_wrapper '$new_elf'" >"$tmp/install.out" 2>&1
    local installed_ino
    installed_ino="$(stat -c '%i' "$install_dir/gt-real-bin" 2>/dev/null || stat -f '%i' "$install_dir/gt-real-bin")"
    if [ "$installed_ino" = "$incumbent_ino" ]; then
        fail "scenario_rollback: new install did not replace incumbent (precondition)"
        return
    fi
    pass "scenario_rollback: new ELF installed (snapshot of incumbent taken)"

    # Roll back to the previous snapshot.
    local rc
    set +e
    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_rollback" >"$tmp/rollback.out" 2>&1
    rc=$?
    set -e
    if [ "$rc" -ne 0 ]; then
        fail "scenario_rollback: gt_install_rollback failed (rc=$rc)"
        cat "$tmp/rollback.out" >&2
        return
    fi
    pass "scenario_rollback: gt_install_rollback exited 0"

    # The live slot must now hold the incumbent ELF again (the rollback
    # restored from a .bak that was a copy of the incumbent). Compare bytes
    # because the restore is cp+mv (fresh inode).
    if cmp -s "$install_dir/gt-real-bin" "$ECHO_ELF"; then
        pass "scenario_rollback: live binary matches the incumbent ELF"
    else
        fail "scenario_rollback: live binary does not match incumbent"
    fi

    # The rollback itself snapshotted the (new) binary first, so it is reversible.
    if ls "$install_dir"/gt-real-bin.bak.pre-rollback.* >/dev/null 2>&1; then
        pass "scenario_rollback: rollback was reversible (pre-rollback snapshot exists)"
    else
        fail "scenario_rollback: no pre-rollback snapshot found"
    fi
}

# Scenario 10 (gastown-cet.12.9): rollback with no snapshot available must
# fail cleanly rather than clobbering the live binary.
scenario_rollback_no_snapshot_fails() {
    local tmp
    tmp="$(mktemp -d)"
    trap "rm -rf '$tmp'" RETURN

    local install_dir="$tmp/home/.local/bin"
    mkdir -p "$install_dir"
    # A healthy incumbent ELF (true) behind a wrapper, but NO .bak snapshots.
    make_elf_fixture "$install_dir/gt-real-bin" 2>/dev/null || cp "$TRUE_ELF" "$install_dir/gt-real-bin"
    chmod 0755 "$install_dir/gt-real-bin"
    local live_ino
    live_ino="$(stat -c '%i' "$install_dir/gt-real-bin" 2>/dev/null || stat -f '%i' "$install_dir/gt-real-bin")"

    cat > "$install_dir/gt" <<'WRAPPER'
#!/usr/bin/env bash
# gt wrapper — guarantees the current validation model-mix on `gt sling`.
WRAPPER
    chmod 0755 "$install_dir/gt"

    # No .bak snapshots exist.
    local rc
    set +e
    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_rollback" >"$tmp/norb.out" 2>&1
    rc=$?
    set -e

    if [ "$rc" -eq 0 ]; then
        fail "scenario_rollback_no_snapshot_fails: rollback succeeded with no snapshot"
    else
        pass "scenario_rollback_no_snapshot_fails: rollback failed cleanly (rc=$rc)"
    fi
    case "$(cat "$tmp/norb.out")" in
        *"no rollback snapshot"*) pass "scenario_rollback_no_snapshot_fails: output explains missing snapshot" ;;
        *)                        fail "scenario_rollback_no_snapshot_fails: output missing explanation -- got: $(cat "$tmp/norb.out")" ;;
    esac
    # Live binary untouched.
    local now_ino
    now_ino="$(stat -c '%i' "$install_dir/gt-real-bin" 2>/dev/null || stat -f '%i' "$install_dir/gt-real-bin")"
    if [ "$now_ino" = "$live_ino" ]; then
        pass "scenario_rollback_no_snapshot_fails: live binary left untouched"
    else
        fail "scenario_rollback_no_snapshot_fails: live binary was modified"
    fi
}

# Scenario 11 (gastown-cet.12.9 rework, codex finding #2): a no-argument
# rollback must select the newest NORMAL install backup, NOT a pre-rollback
# snapshot. The pre-rollback snapshot captures whatever binary was live just
# before a rollback — i.e. the bad build being recovered from. Under the old
# glob `bak.*` + `sort -r`, "pre-rollback" sorted after digit timestamps
# ('p' > '2'), so a second rollback would restore the bad-current binary.
# This test forces that ordering (newer timestamped install backup AND a
# pre-rollback snapshot both present) and asserts the install backup wins.
scenario_rollback_skips_pre_rollback_snapshots() {
    local tmp
    tmp="$(mktemp -d)"
    trap "rm -rf '$tmp'" RETURN

    local install_dir="$tmp/home/.local/bin"
    mkdir -p "$install_dir"

    # Known-good incumbent: echo (exits 0 → canary passes). This is the binary
    # we want rollback to restore.
    [ -n "$ECHO_ELF" ] || { echo "SKIP scenario_rollback_pre_rollback: no 'echo' ELF" >&2; return 0; }
    cp "$ECHO_ELF" "$install_dir/gt-real-bin"
    chmod 0755 "$install_dir/gt-real-bin"

    # A known-bad ELF: false (exits 1 → canary rejects). This is the bad build
    # that was live before the (simulated) first rollback and got snapshotted
    # as a pre-rollback artifact. Rollback must NEVER select it.
    [ -n "$FALSE_ELF" ] || { echo "SKIP scenario_rollback_pre_rollback: no 'false' ELF" >&2; return 0; }

    cat > "$install_dir/gt" <<'WRAPPER'
#!/usr/bin/env bash
# gt wrapper — guarantees the current validation model-mix on `gt sling`.
WRAPPER
    chmod 0755 "$install_dir/gt"

    # Plant a NORMAL install backup of the known-good incumbent. Use a
    # deliberately LATER timestamp than the pre-rollback snapshot below so the
    # two are clearly ordered — the bug was that pre-rollback won regardless of
    # timestamp because of the lexical 'p' > digit sort.
    local good_bak="$install_dir/gt-real-bin.bak.20260627T235959Z"
    cp "$ECHO_ELF" "$good_bak"; chmod 0644 "$good_bak"

    # Plant a pre-rollback snapshot of the BAD build, with an EARLIER timestamp
    # than the install backup. The old glob (bak.*) + sort -r would still pick
    # THIS one first because "pre-rollback" > "20260627..." lexically.
    local bad_pre="$install_dir/gt-real-bin.bak.pre-rollback.20260627T000000Z"
    cp "$FALSE_ELF" "$bad_pre"; chmod 0644 "$bad_pre"

    # Make the LIVE binary the bad one too, so a naive rollback that picks the
    # pre-rollback snapshot would (after its own canary rejects the bad
    # candidate) fail — but more importantly, we assert WHICH snapshot was
    # selected by checking the restore source reported in the output.
    cp "$FALSE_ELF" "$install_dir/gt-real-bin"
    chmod 0755 "$install_dir/gt-real-bin"

    local rc out
    set +e
    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_rollback" >"$tmp/rb.out" 2>&1
    rc=$?
    set -e
    out="$(cat "$tmp/rb.out")"

    if [ "$rc" -ne 0 ]; then
        fail "scenario_rollback_skips_pre_rollback: rollback failed (rc=$rc)"
        echo "$out" >&2
        return
    fi

    # The rollback MUST have restored from the known-good install backup, not
    # the pre-rollback bad-build snapshot. Assert by the reported source path.
    case "$out" in
        *"restored $good_bak"*)
            pass "scenario_rollback_skips_pre_rollback: selected normal install backup, not pre-rollback snapshot"
            ;;
        *"restored $bad_pre"*)
            fail "scenario_rollback_skips_pre_rollback: selected the pre-rollback (bad-build) snapshot"
            ;;
        *)
            fail "scenario_rollback_skips_pre_rollback: unexpected restore source -- got: $out"
            ;;
    esac

    # And the live binary must now be the known-good incumbent.
    if cmp -s "$install_dir/gt-real-bin" "$ECHO_ELF"; then
        pass "scenario_rollback_skips_pre_rollback: live binary is the known-good install backup"
    else
        fail "scenario_rollback_skips_pre_rollback: live binary is not the known-good backup (bad build left live?)"
    fi
}

# Scenario 12 (gastown-cet.12.9 rework, codex finding #3): the pre-install
# canary must run on the EXACT staged bytes that will be installed, inside the
# lock — not on the source before the lock with a later copy of the source.
# Deterministic proof of the TOCTOU the old code had: the old implementation
# canaried $src BEFORE acquiring the lock, then copied $src again inside the
# lock. We make the installer canary a GOOD symlinked source, then flip the
# symlink to BAD while a blocker holds the lock — so by the time the installer
# acquires the lock and copies, $src points at the bad binary.
#   - OLD code: canary (pre-lock) blessed the good bytes; the locked copy then
#     reads the now-bad symlink → BAD ELF installed, live binary changed.
#   - FIXED code: the freeze (cp $src→staged) + canary both run AFTER acquiring
#     the lock, so they see the already-flipped BAD source and REJECT before the
#     live slot is touched.
# The blocker acquires the lock first and flips AFTER a short delay (so the
# installer's canary has run on the good source and is blocked waiting for the
# lock), matching the suite's existing sleep-based coordination.
scenario_canary_freezes_staged_bytes_under_lock() {
    local tmp
    tmp="$(mktemp -d)"
    trap "rm -rf '$tmp'" RETURN

    local install_dir="$tmp/home/.local/bin"
    mkdir -p "$install_dir"

    [ -n "$TRUE_ELF" ] || { echo "SKIP scenario_canary_freeze: no 'true' ELF" >&2; return 0; }
    [ -n "$FALSE_ELF" ] || { echo "SKIP scenario_canary_freeze: no 'false' ELF" >&2; return 0; }

    # The wrapper so the install takes the Case-B real-bin path.
    cat > "$install_dir/gt" <<'WRAPPER'
#!/usr/bin/env bash
# gt wrapper — guarantees the current validation model-mix on `gt sling`.
WRAPPER
    chmod 0755 "$install_dir/gt"

    # Plant a healthy incumbent so we can prove a rejected install left it alone.
    cp "$TRUE_ELF" "$install_dir/gt-real-bin"
    chmod 0755 "$install_dir/gt-real-bin"
    local live_ino
    live_ino="$(stat -c '%i' "$install_dir/gt-real-bin" 2>/dev/null || stat -f '%i' "$install_dir/gt-real-bin")"

    # Symlinked source that starts GOOD. The blocker will flip it to BAD while
    # holding the install lock, after the installer has already canaried it.
    local good_target="$tmp/good-gt"; cp "$TRUE_ELF"  "$good_target"; chmod 0755 "$good_target"
    local bad_target="$tmp/bad-gt";  cp "$FALSE_ELF" "$bad_target";  chmod 0755 "$bad_target"
    local symlinked_src="$tmp/src-gt"
    ln -s "$good_target" "$symlinked_src"

    # Blocker: acquire the install lock immediately, hold it, flip the symlink
    # good→bad after a short delay (so the installer's pre-lock canary runs on
    # the good source first and then blocks on the lock), hold briefly, release.
    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" bash -c '
        source "$0"
        exec 9>"$(gt_install_lock_path)"
        flock -n 9 || exit 0
        sleep 0.6
        ln -sfn "$1" "$2"
        sleep 0.5
    ' "$LIB" "$bad_target" "$symlinked_src" &
    local blocker=$!
    # Let the blocker acquire the lock first.
    sleep 0.2

    local rc out
    set +e
    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" GT_INSTALL_LOCK_TIMEOUT=10 \
        bash -c "source '$LIB'; gt_install_preserve_wrapper '$symlinked_src'" \
        >"$tmp/freeze.out" 2>&1
    rc=$?
    set -e
    wait "$blocker" 2>/dev/null || true
    out="$(cat "$tmp/freeze.out")"

    # The install MUST have been rejected (canary saw the bad staged bytes).
    if [ "$rc" -eq 0 ]; then
        fail "scenario_canary_freezes_staged_bytes: install accepted a symlinked source flipped to bad under the lock (rc=$rc)"
    else
        pass "scenario_canary_freezes_staged_bytes: install rejected staged bad bytes under lock (rc=$rc)"
    fi
    case "$out" in
        *"canary"*) pass "scenario_canary_freezes_staged_bytes: output mentions canary gate" ;;
        *)          fail "scenario_canary_freezes_staged_bytes: output missing canary mention -- got: $out" ;;
    esac

    # The live binary must be untouched (the bad candidate never reached it).
    local now_ino
    now_ino="$(stat -c '%i' "$install_dir/gt-real-bin" 2>/dev/null || stat -f '%i' "$install_dir/gt-real-bin")"
    if [ "$now_ino" = "$live_ino" ]; then
        pass "scenario_canary_freezes_staged_bytes: live binary left untouched"
    else
        fail "scenario_canary_freezes_staged_bytes: live binary was modified despite canary"
    fi

    # No stale staging file left behind.
    if ls "$install_dir"/.gt-install.stage.* >/dev/null 2>&1; then
        fail "scenario_canary_freezes_staged_bytes: staging file leaked"
    else
        pass "scenario_canary_freezes_staged_bytes: staging file cleaned up"
    fi
}

echo "Running wrapper-topology smoke tests..."
scenario_plain_install
scenario_wrapper_preserved
scenario_realbin_rotated
scenario_topology_assertion_fails
scenario_non_wrapper_script_treated_as_plain
scenario_canary_rejects_bad_elf
scenario_canary_skip_installs_synthetic_elf
scenario_concurrent_installs_serialized
scenario_rollback_restores_previous_binary
scenario_rollback_no_snapshot_fails
scenario_rollback_skips_pre_rollback_snapshots
scenario_canary_freezes_staged_bytes_under_lock

echo ""
echo "Result: $PASS passed, $FAIL failed"
if [ "$FAIL" -ne 0 ]; then
    exit 1
fi
exit 0
