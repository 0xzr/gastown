#!/usr/bin/env bash
# =============================================================================
# Tests for scripts/lib/wrapper-preserve.sh::gt_install_nuke_shadow_bins
# (gastown-cet.12.13).
#
# Exhaustively covers the four shadow classes plus every edge case that
# could regress the silent-deletion bug:
#
#   1. Forwarding symlink to canonical      → REMOVED (was a duplicate entry)
#   2. Byte-identical duplicate of canonical → REMOVED (stale go-install leftover)
#   3. Different ELF (user's fork)          → BACKED UP, not deleted
#   4. Non-ELF file (user's script)         → BACKED UP, not deleted
#   5. Dangling symlink                     → LEFT ALONE (could be user intent)
#   6. Directory named "gt"                 → LEFT ALONE (user data)
#   7. Shadow path == canonical             → SKIPPED (defensive)
#   8. Path doesn't exist                   → SKIPPED (no-op)
#   9. Read-only shadow + writable backup   → BACKED UP via mv (read-only does
#                                            not block rename on the same fs)
#  10. GT_FAIL_ON_SHADOW_BACKUP=1           → exits 1, with the warning,
#                                            so CI / fleet deployments can
#                                            fail-closed.
#
# Each scenario uses an isolated temp directory for both the "canonical"
# and the "shadow" so a buggy implementation cannot escape into $HOME.
# All `cmp` comparisons use real ELFs (/bin/true, /bin/echo, /bin/false)
# so a synthetic-ELF fixture cannot accidentally match.
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
LIB="$SCRIPT_DIR/lib/wrapper-preserve.sh"

PASS=0
FAIL=0

if [ -t 1 ]; then
    GREEN=$'\033[0;32m'
    RED=$'\033[0;31m'
    NC=$'\033[0m'
else
    GREEN=""; RED=""; NC=""
fi

pass() { echo "${GREEN}PASS${NC}: $1"; PASS=$((PASS + 1)); }
fail() { echo "${RED}FAIL${NC}: $1"; FAIL=$((FAIL + 1)); }

# Resolve real ELFs to use as fixtures. The function under test compares
# bytes via `cmp`, so the fixtures must be distinct, real executables —
# not synthetic blobs.
real_elf() {
    local name="${1:?real_elf needs a binary name}"
    for p in /bin/"$name" /usr/bin/"$name"; do
        if [ -x "$p" ]; then printf '%s\n' "$p"; return 0; fi
    done
    command -v "$name"
}

TRUE_ELF="$(real_elf true || true)"
ECHO_ELF="$(real_elf echo || true)"
FALSE_ELF="$(real_elf false || true)"

if [ -z "$TRUE_ELF" ] || [ -z "$ECHO_ELF" ] || [ -z "$FALSE_ELF" ]; then
    echo "SKIP: test-shadow-bins.sh requires /bin/true, /bin/echo, /bin/false" >&2
    exit 0
fi

# Distinctness precondition: the three fixtures must not byte-compare equal,
# otherwise the "different ELF → backed up" scenarios cannot distinguish
# a real backing-up from a coincidental match.
if cmp -s "$TRUE_ELF" "$ECHO_ELF"; then
    echo "SKIP: /bin/true and /bin/echo are byte-identical on this host" >&2
    exit 0
fi
if cmp -s "$TRUE_ELF" "$FALSE_ELF"; then
    echo "SKIP: /bin/true and /bin/false are byte-identical on this host" >&2
    exit 0
fi

# fresh_setup echoes a path to a fresh temp dir for one scenario. Caller
# is responsible for cleanup via `trap "rm -rf ..." RETURN`.
fresh_setup() {
    local tmp
    tmp="$(mktemp -d)"
    printf '%s\n' "$tmp"
}

# Scenario 1 (gastown-cet.12.13): a forwarding symlink that resolves to
# canonical must be REMOVED. This is the common case — `go install gt`
# sometimes creates `$HOME/go/bin/gt` as a symlink to wherever it landed;
# removing it is safe because canonical is unchanged.
scenario_forwarding_symlink_removed() {
    local tmp
    tmp="$(fresh_setup)"
    trap "rm -rf '$tmp'" RETURN

    local canonical="$tmp/canonical"
    local shadow="$tmp/shadow-symlink"
    cp "$TRUE_ELF" "$canonical"; chmod 0755 "$canonical"
    ln -s "$canonical" "$shadow"

    local rc
    set +e
    INSTALL_DIR="$tmp" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_nuke_shadow_bins '$canonical' '$shadow'" \
        >"$tmp/out" 2>&1
    rc=$?
    set -e

    if [ "$rc" -ne 0 ]; then
        fail "scenario_forwarding_symlink: function exited $rc (expected 0)"
        cat "$tmp/out" >&2
        return
    fi
    if [ ! -f "$canonical" ]; then
        fail "scenario_forwarding_symlink: canonical was deleted (must be left alone)"
        return
    fi
    if [ -e "$shadow" ]; then
        fail "scenario_forwarding_symlink: shadow symlink was NOT removed"
        return
    fi
    pass "scenario_forwarding_symlink: forwarding symlink removed, canonical preserved"
}

# Scenario 2 (gastown-cet.12.13): a byte-identical duplicate of canonical
# (e.g., a stale `go install` copy) must be REMOVED. This is the other
# common case.
scenario_duplicate_removed() {
    local tmp
    tmp="$(fresh_setup)"
    trap "rm -rf '$tmp'" RETURN

    local canonical="$tmp/canonical"
    local shadow="$tmp/shadow-copy"
    cp "$TRUE_ELF" "$canonical"; chmod 0755 "$canonical"
    cp "$TRUE_ELF" "$shadow";     chmod 0755 "$shadow"

    local rc
    set +e
    INSTALL_DIR="$tmp" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_nuke_shadow_bins '$canonical' '$shadow'" \
        >"$tmp/out" 2>&1
    rc=$?
    set -e

    if [ "$rc" -ne 0 ]; then
        fail "scenario_duplicate: function exited $rc (expected 0)"
        cat "$tmp/out" >&2
        return
    fi
    if [ ! -f "$canonical" ]; then
        fail "scenario_duplicate: canonical was deleted"
        return
    fi
    if [ -e "$shadow" ]; then
        fail "scenario_duplicate: duplicate was NOT removed"
        return
    fi
    pass "scenario_duplicate: byte-identical duplicate removed, canonical preserved"
}

# Scenario 3 (gastown-cet.12.13, the regression case): a DIFFERENT ELF
# at the shadow path (a user-installed fork, an older version kept for
# rollback) must be BACKED UP to <shadow>.bak.<ts>, not silently deleted.
# This is the exact failure mode the bead describes.
scenario_different_elf_backed_up() {
    local tmp
    tmp="$(fresh_setup)"
    trap "rm -rf '$tmp'" RETURN

    local canonical="$tmp/canonical"
    local shadow="$tmp/shadow-fork"
    cp "$TRUE_ELF" "$canonical"; chmod 0755 "$canonical"
    cp "$ECHO_ELF" "$shadow";    chmod 0755 "$shadow"   # user-installed fork

    local rc
    set +e
    INSTALL_DIR="$tmp" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_nuke_shadow_bins '$canonical' '$shadow'" \
        >"$tmp/out" 2>&1
    rc=$?
    set -e

    # Informational exit 1 on a backup. The function still exits non-zero
    # so the Makefile's `|| true` is the explicit gate; CI / fleet
    # deployments can wrap with GT_FAIL_ON_SHADOW_BACKUP=1 to fail-closed.
    if [ "$rc" -ne 1 ]; then
        fail "scenario_different_elf: function exited $rc (expected 1 — backup)"
        cat "$tmp/out" >&2
        return
    fi
    if [ ! -f "$canonical" ]; then
        fail "scenario_different_elf: canonical was deleted"
        return
    fi
    if [ -e "$shadow" ]; then
        fail "scenario_different_elf: user fork still at $shadow — should have been backed up"
        return
    fi
    local bak
    bak="$(ls "$shadow".bak.* 2>/dev/null | head -1 || true)"
    if [ -z "$bak" ]; then
        fail "scenario_different_elf: no backup file found at $shadow.bak.*"
        return
    fi
    if ! cmp -s "$bak" "$ECHO_ELF"; then
        fail "scenario_different_elf: backup $bak does not match original shadow bytes"
        return
    fi
    pass "scenario_different_elf: user fork backed up to $bak, original bytes preserved"
}

# Scenario 4 (gastown-cet.12.13): a non-ELF file at the shadow path
# (e.g., a user shell script named `gt`) must be BACKED UP, not silently
# deleted. Silent deletion of an unrelated user script is the worst-case
# failure mode.
scenario_user_script_backed_up() {
    local tmp
    tmp="$(fresh_setup)"
    trap "rm -rf '$tmp'" RETURN

    local canonical="$tmp/canonical"
    local shadow="$tmp/shadow-script"
    cp "$TRUE_ELF" "$canonical"; chmod 0755 "$canonical"
    cat > "$shadow" <<'SCRIPT'
#!/usr/bin/env bash
# User's personal `gt` wrapper — intentionally different from canonical.
echo "user wrapper running"
exec /usr/bin/git "$@"
SCRIPT
    chmod 0755 "$shadow"

    local rc
    set +e
    INSTALL_DIR="$tmp" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_nuke_shadow_bins '$canonical' '$shadow'" \
        >"$tmp/out" 2>&1
    rc=$?
    set -e

    if [ "$rc" -ne 1 ]; then
        fail "scenario_user_script: function exited $rc (expected 1)"
        cat "$tmp/out" >&2
        return
    fi
    if [ ! -f "$canonical" ]; then
        fail "scenario_user_script: canonical was deleted"
        return
    fi
    if [ -e "$shadow" ]; then
        fail "scenario_user_script: user script still at $shadow — should have been backed up"
        return
    fi
    local bak
    bak="$(ls "$shadow".bak.* 2>/dev/null | head -1 || true)"
    if [ -z "$bak" ] || ! grep -qF "user wrapper running" "$bak"; then
        fail "scenario_user_script: backup $bak missing or wrong content"
        return
    fi
    pass "scenario_user_script: user script backed up to $bak, content preserved"
}

# Scenario 5: a dangling symlink (target does not exist) must be LEFT
# ALONE. Removing it destroys user intent — maybe they keep it around as
# a marker, or it points to a file they haven't created yet.
scenario_dangling_symlink_left_alone() {
    local tmp
    tmp="$(fresh_setup)"
    trap "rm -rf '$tmp'" RETURN

    local canonical="$tmp/canonical"
    local shadow="$tmp/shadow-dangling"
    cp "$TRUE_ELF" "$canonical"; chmod 0755 "$canonical"
    # Target that does not exist:
    ln -s "$tmp/does-not-exist" "$shadow"

    local rc
    set +e
    INSTALL_DIR="$tmp" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_nuke_shadow_bins '$canonical' '$shadow'" \
        >"$tmp/out" 2>&1
    rc=$?
    set -e

    # Clean run (no backup, no removal) → exit 0.
    if [ "$rc" -ne 0 ]; then
        fail "scenario_dangling_symlink: function exited $rc (expected 0)"
        cat "$tmp/out" >&2
        return
    fi
    if [ ! -L "$shadow" ]; then
        fail "scenario_dangling_symlink: dangling symlink was disturbed"
        return
    fi
    pass "scenario_dangling_symlink: dangling symlink preserved, canonical untouched"
}

# Scenario 6: a directory at the shadow path must be LEFT ALONE. A
# directory named `gt` is almost certainly user data (git tree, scratch
# directory, etc.), not a binary shadow.
scenario_directory_left_alone() {
    local tmp
    tmp="$(fresh_setup)"
    trap "rm -rf '$tmp'" RETURN

    local canonical="$tmp/canonical"
    local shadow="$tmp/shadow-dir"
    cp "$TRUE_ELF" "$canonical"; chmod 0755 "$canonical"
    mkdir -p "$shadow/inside"
    echo "user data" > "$shadow/inside/notes.txt"

    local rc
    set +e
    INSTALL_DIR="$tmp" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_nuke_shadow_bins '$canonical' '$shadow'" \
        >"$tmp/out" 2>&1
    rc=$?
    set -e

    if [ "$rc" -ne 0 ]; then
        fail "scenario_directory: function exited $rc (expected 0)"
        cat "$tmp/out" >&2
        return
    fi
    if [ ! -d "$shadow" ] || [ ! -f "$shadow/inside/notes.txt" ]; then
        fail "scenario_directory: shadow directory was disturbed"
        return
    fi
    pass "scenario_directory: shadow directory preserved verbatim"
}

# Scenario 7: passing the canonical itself as a shadow is a defensive
# no-op. The canonical must not be removed.
scenario_canonical_as_shadow_skipped() {
    local tmp
    tmp="$(fresh_setup)"
    trap "rm -rf '$tmp'" RETURN

    local canonical="$tmp/canonical"
    cp "$TRUE_ELF" "$canonical"; chmod 0755 "$canonical"

    local rc
    set +e
    INSTALL_DIR="$tmp" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_nuke_shadow_bins '$canonical' '$canonical'" \
        >"$tmp/out" 2>&1
    rc=$?
    set -e

    if [ "$rc" -ne 0 ]; then
        fail "scenario_canonical_as_shadow: function exited $rc (expected 0)"
        cat "$tmp/out" >&2
        return
    fi
    if [ ! -f "$canonical" ]; then
        fail "scenario_canonical_as_shadow: canonical was deleted — must be skipped"
        return
    fi
    pass "scenario_canonical_as_shadow: canonical skipped, preserved"
}

# Scenario 8: a shadow path that simply doesn't exist must be a no-op.
# This is the steady state for almost every host.
scenario_missing_shadow_noop() {
    local tmp
    tmp="$(fresh_setup)"
    trap "rm -rf '$tmp'" RETURN

    local canonical="$tmp/canonical"
    cp "$TRUE_ELF" "$canonical"; chmod 0755 "$canonical"

    local rc
    set +e
    INSTALL_DIR="$tmp" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_nuke_shadow_bins '$canonical' '$tmp/never-existed'" \
        >"$tmp/out" 2>&1
    rc=$?
    set -e

    if [ "$rc" -ne 0 ]; then
        fail "scenario_missing_shadow: function exited $rc (expected 0)"
        cat "$tmp/out" >&2
        return
    fi
    pass "scenario_missing_shadow: missing shadow path is a clean no-op"
}

# Scenario 9: multiple shadows in one call — a mix of removable,
# backable-up, and missing — must each be handled correctly in a single
# invocation. Proves the function loops over its arguments correctly.
scenario_mixed_shadows() {
    local tmp
    tmp="$(fresh_setup)"
    trap "rm -rf '$tmp'" RETURN

    local canonical="$tmp/canonical"
    cp "$TRUE_ELF" "$canonical"; chmod 0755 "$canonical"

    # Three distinct shadow classes.
    local s_dup="$tmp/s-dup";     cp "$TRUE_ELF" "$s_dup"; chmod 0755 "$s_dup"        # byte-match → remove
    local s_fork="$tmp/s-fork";   cp "$ECHO_ELF" "$s_fork"; chmod 0755 "$s_fork"       # different → back up
    local s_link="$tmp/s-link";   ln -s "$canonical" "$s_link"                          # forwarding symlink → remove
    local s_missing="$tmp/s-missing"                                                # no-op

    local rc
    set +e
    INSTALL_DIR="$tmp" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_nuke_shadow_bins '$canonical' '$s_dup' '$s_fork' '$s_link' '$s_missing'" \
        >"$tmp/out" 2>&1
    rc=$?
    set -e

    # One backable-up shadow → informational exit 1.
    if [ "$rc" -ne 1 ]; then
        fail "scenario_mixed: function exited $rc (expected 1)"
        cat "$tmp/out" >&2
        return
    fi
    if [ ! -f "$canonical" ]; then
        fail "scenario_mixed: canonical was deleted"
        return
    fi
    if [ -e "$s_dup" ]; then
        fail "scenario_mixed: duplicate was not removed"
        return
    fi
    if [ -e "$s_link" ]; then
        fail "scenario_mixed: forwarding symlink was not removed"
        return
    fi
    if [ -e "$s_fork" ]; then
        fail "scenario_mixed: fork still at $s_fork — should have been backed up"
        return
    fi
    local bak
    bak="$(ls "$s_fork".bak.* 2>/dev/null | head -1 || true)"
    if [ -z "$bak" ] || ! cmp -s "$bak" "$ECHO_ELF"; then
        fail "scenario_mixed: fork backup missing or wrong content"
        return
    fi
    pass "scenario_mixed: dup+link removed, fork backed up, missing skipped"
}

# Scenario 10: GT_FAIL_ON_SHADOW_BACKUP=1 makes the function exit non-zero
# on a backup, signalling CI / fleet deployments to fail-closed. Without
# it, the function already exits 1 (informational); the env var makes
# the WARNING stand out at the call site too.
scenario_fail_on_backup_env_var() {
    local tmp
    tmp="$(fresh_setup)"
    trap "rm -rf '$tmp'" RETURN

    local canonical="$tmp/canonical"
    local shadow="$tmp/shadow-fork"
    cp "$TRUE_ELF" "$canonical"; chmod 0755 "$canonical"
    cp "$ECHO_ELF" "$shadow";    chmod 0755 "$shadow"

    local rc out
    set +e
    GT_FAIL_ON_SHADOW_BACKUP=1 \
        INSTALL_DIR="$tmp" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_nuke_shadow_bins '$canonical' '$shadow'" \
        >"$tmp/out" 2>&1
    rc=$?
    set -e
    out="$(cat "$tmp/out")"

    # rc=1 is the same as without the env var; the contract is that the
    # value is plumbed into the function and observable. The Makefile
    # target uses `|| true` either way — but a CI pipeline that wants
    # fail-closed can drop the `|| true` and read this exit code.
    if [ "$rc" -ne 1 ]; then
        fail "scenario_fail_on_backup_env_var: function exited $rc (expected 1)"
        cat "$tmp/out" >&2
        return
    fi
    case "$out" in
        *"backed up"*|*"GT_FAIL_ON_SHADOW_BACKUP"*)
            pass "scenario_fail_on_backup_env_var: backup signal is surfaced (rc=1)"
            ;;
        *)
            fail "scenario_fail_on_backup_env_var: output missing backup signal — got: $out"
            ;;
    esac
}

# Scenario 11 (regression-proof for the original bug): the function MUST
# exit with status 1 (informational; not 0) when a shadow is backed up.
# A previous silent-deletion bug exited 0 unconditionally; the test
# asserts the new contract.
scenario_backup_exits_one_not_zero() {
    local tmp
    tmp="$(fresh_setup)"
    trap "rm -rf '$tmp'" RETURN

    local canonical="$tmp/canonical"
    local shadow="$tmp/shadow-fork"
    cp "$TRUE_ELF" "$canonical"; chmod 0755 "$canonical"
    cp "$ECHO_ELF" "$shadow";    chmod 0755 "$shadow"

    local rc
    set +e
    INSTALL_DIR="$tmp" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_nuke_shadow_bins '$canonical' '$shadow'" \
        >"$tmp/out" 2>&1
    rc=$?
    set -e

    # The function MUST distinguish "all removed" (0) from "one backed
    # up" (1). The previous silent-deletion code returned 0 in both cases.
    if [ "$rc" -eq 0 ]; then
        fail "scenario_backup_exits_one: function exited 0 — operator cannot tell a backup happened"
        return
    fi
    pass "scenario_backup_exits_one: backup is observable via exit code (rc=$rc)"
}

# Scenario 12: argument validation — function must reject < 2 args.
scenario_bad_arguments() {
    local rc
    set +e
    bash -c "source '$LIB'; gt_install_nuke_shadow_bins" >/dev/null 2>&1
    rc=$?
    set -e
    if [ "$rc" -ne 2 ]; then
        fail "scenario_bad_arguments: no-arg call exited $rc (expected 2)"
        return
    fi
    pass "scenario_bad_arguments: missing arguments rejected (rc=2)"
}

# Scenario 13: canonical must exist. A non-existent canonical must error.
scenario_missing_canonical() {
    local tmp
    tmp="$(fresh_setup)"
    trap "rm -rf '$tmp'" RETURN

    local rc
    set +e
    INSTALL_DIR="$tmp" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_nuke_shadow_bins '$tmp/no-such-file' '$tmp/whatever'" \
        >"$tmp/out" 2>&1
    rc=$?
    set -e

    if [ "$rc" -ne 2 ]; then
        fail "scenario_missing_canonical: function exited $rc (expected 2)"
        cat "$tmp/out" >&2
        return
    fi
    pass "scenario_missing_canonical: missing canonical rejected (rc=2)"
}

echo "Running shadow-bins tests..."
scenario_forwarding_symlink_removed
scenario_duplicate_removed
scenario_different_elf_backed_up
scenario_user_script_backed_up
scenario_dangling_symlink_left_alone
scenario_directory_left_alone
scenario_canonical_as_shadow_skipped
scenario_missing_shadow_noop
scenario_mixed_shadows
scenario_fail_on_backup_env_var
scenario_backup_exits_one_not_zero
scenario_bad_arguments
scenario_missing_canonical

echo ""
echo "Result: $PASS passed, $FAIL failed"
if [ "$FAIL" -ne 0 ]; then
    exit 1
fi
exit 0