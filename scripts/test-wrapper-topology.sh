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

# Make a temp HOME with a fake ELF "source" binary that we can install.
# The ELF just needs to look like an ELF to the wrapper-preserve library.
make_elf_fixture() {
    local elf="$1"
    # First 4 bytes 0x7F 'E' 'L' 'F', followed by enough junk to be a real file.
    printf '\x7FELF%1024s' " " > "$elf"
    chmod 0755 "$elf"
}

# Scenario 1: fresh host, no wrapper at $HOME/.local/bin/gt.
scenario_plain_install() {
    local tmp
    tmp="$(mktemp -d)"
    trap "rm -rf '$tmp'" RETURN

    local install_dir="$tmp/home/.local/bin"
    mkdir -p "$install_dir"
    local elf="$tmp/source-gt"
    make_elf_fixture "$elf"

    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_preserve_wrapper '$elf'"

    if [ ! -f "$install_dir/gt" ]; then
        fail "scenario_plain_install: $install_dir/gt missing"
        return
    fi
    local first_byte
    first_byte="$(dd if="$install_dir/gt" bs=1 count=1 status=none | od -An -c | tr -d ' \n')"
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
    make_elf_fixture "$elf"

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
    real_first="$(dd if="$install_dir/gt-real-bin" bs=1 count=1 status=none | od -An -c | tr -d ' \n')"
    case "$real_first" in
        177|E) pass "scenario_wrapper_preserved: gt-real-bin is an ELF" ;;
        *)     fail "scenario_wrapper_preserved: gt-real-bin is not an ELF -- got: $real_first" ;;
    esac

    # Sanity: public gt must still be a script.
    local public_first
    public_first="$(dd if="$install_dir/gt" bs=1 count=1 status=none | od -An -c | tr -d ' \n')"
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
    local elf="$tmp/source-gt"
    local old_elf="$tmp/old-gt"
    make_elf_fixture "$elf"
    # Plant a different ELF at gt-real-bin that we can recognize post-install.
    # Use a recognizable magic prefix that differs from the new ELF's padding.
    printf '\x7FELFsource-rotated-1234567890' > "$old_elf"
    chmod 0755 "$old_elf"
    cp "$old_elf" "$install_dir/gt-real-bin"

    cat > "$install_dir/gt" <<'WRAPPER'
#!/usr/bin/env bash
# gt wrapper — guarantees the current validation model-mix on `gt sling`.
WRAPPER_INTACT=99
WRAPPER
    chmod 0755 "$install_dir/gt"

    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" \
        bash -c "source '$LIB'; gt_install_preserve_wrapper '$elf'"

    # New ELF at gt-real-bin (not the old one).
    local real_first
    real_first="$(dd if="$install_dir/gt-real-bin" bs=1 count=1 status=none | od -An -c | tr -d ' \n')"
    case "$real_first" in
        177|E)
            # Look for the new ELF's signature — the old had 'source-rotated' literal,
            # the new has just 'ELF' followed by spaces.
            if grep -q "source-rotated" "$install_dir/gt-real-bin"; then
                fail "scenario_realbin_rotated: gt-real-bin still holds the previous ELF"
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

    # Backup of previous ELF exists with timestamp suffix.
    local bak
    bak="$(ls "$install_dir"/gt-real-bin.bak.* 2>/dev/null | head -1 || true)"
    if [ -z "$bak" ]; then
        fail "scenario_realbin_rotated: expected a gt-real-bin.bak.<ts> snapshot"
    elif grep -q "source-rotated" "$bak"; then
        pass "scenario_realbin_rotated: previous ELF preserved at $bak"
    else
        fail "scenario_realbin_rotated: backup file $bak does not contain old ELF payload"
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
    make_elf_fixture "$elf"

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
    first="$(dd if="$install_dir/gt" bs=1 count=1 status=none | od -An -c | tr -d ' \n')"
    case "$first" in
        177|E) pass "scenario_non_wrapper_script_treated_as_plain: public path now ELF" ;;
        *)     fail "scenario_non_wrapper_script_treated_as_plain: public path is not ELF" ;;
    esac
}

echo "Running wrapper-topology smoke tests..."
scenario_plain_install
scenario_wrapper_preserved
scenario_realbin_rotated
scenario_topology_assertion_fails
scenario_non_wrapper_script_treated_as_plain

echo ""
echo "Result: $PASS passed, $FAIL failed"
if [ "$FAIL" -ne 0 ]; then
    exit 1
fi
exit 0
