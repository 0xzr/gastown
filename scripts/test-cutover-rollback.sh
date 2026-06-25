#!/usr/bin/env bash
# =============================================================================
# Regression tests for scripts/rollback-pinned-1.2.0.sh.
#
# Exercises two scenarios:
#
#   1. Wrapper topology: cutover installed ELF at ~/.local/bin/gt-real-bin and
#      backed it up. Rollback restores the ELF behind the wrapper and leaves
#      the wrapper untouched.
#
#   2. Plain topology: cutover replaced ~/.local/bin/gt directly. Rollback
#      restores the public binary path.
#
# The tests use isolated temp HOME directories and fake ELF files (first byte
# 0x7F 'E' 'L' 'F') so the real user install is never touched.
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROLLBACK_SCRIPT="${SCRIPT_DIR}/rollback-pinned-1.2.0.sh"

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

# Create a fake ELF file at the given path with an identifiable payload.
make_elf() {
    local path="$1"
    local payload="$2"
    shift 2
    printf '\x7FELF%s' "$payload" > "$path"
    chmod 0755 "$path"
}

# Verify `path` exists and starts with ELF.
assert_is_elf() {
    local path="$1"
    local label="$2"
    if [ ! -f "$path" ]; then
        fail "$label: $path missing"
        return 1
    fi
    local first
    first="$(dd if="$path" bs=1 count=1 status=none 2>/dev/null | od -An -c | tr -d ' \n')"
    case "$first" in
        177|E) ;;
        *) fail "$label: $path does not start with ELF"; return 1 ;;
    esac
    return 0
}

# Scenario 1: wrapper topology rollback.
scenario_wrapper_rollback() {
    local tmp
    tmp="$(mktemp -d)"
    trap "rm -rf '$tmp'" RETURN

    local install_dir="$tmp/home/.local/bin"
    mkdir -p "$install_dir"

    # Simulate the pre-cutover binary.
    local backup="$install_dir/gt-real-bin.before-pinned-1.2.0-cutover"
    make_elf "$backup" "pre-cutover-123"

    # Simulate the post-cutover binary.
    make_elf "$install_dir/gt-real-bin" "post-cutover-456"

    # Operational wrapper at public path.
    cat > "$install_dir/gt" <<'WRAPPER'
#!/usr/bin/env bash
# gt wrapper — guarantees the current validation model-mix on `gt sling`.
exec "$HOME/.local/bin/gt-real-bin" "$@"
WRAPPER
    chmod 0755 "$install_dir/gt"

    local record_dir="$tmp/town/.runtime"
    mkdir -p "$record_dir"
    local record="$record_dir/pinned-1.2.0-cutover.json"
    cat > "$record" <<EOF
{
  "cutover_at": "2026-06-25T11:47:00Z",
  "runtime_line": "1.2.0",
  "installed_binary": "$install_dir/gt-real-bin",
  "public_path": "$install_dir/gt",
  "wrapper_topology": "wrapper",
  "backup_binary": "$backup"
}
EOF

    local rc=0
    set +e
    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" GT_TOWN_ROOT="$tmp/town" \
        bash "$ROLLBACK_SCRIPT" --evidence="$record" >/dev/null 2>&1
    rc=$?
    set -e

    if [ "$rc" -ne 0 ]; then
        fail "scenario_wrapper_rollback: rollback script exited $rc"
        return
    fi

    if ! assert_is_elf "$install_dir/gt-real-bin" "scenario_wrapper_rollback restored ELF"; then
        return
    fi
    if grep -q "pre-cutover-123" "$install_dir/gt-real-bin"; then
        pass "scenario_wrapper_rollback: gt-real-bin restored from backup"
    else
        fail "scenario_wrapper_rollback: gt-real-bin does not contain backup payload"
    fi

    # Wrapper must still be the wrapper.
    local wrapper_first
    wrapper_first="$(dd if="$install_dir/gt" bs=1 count=1 status=none 2>/dev/null | od -An -c | tr -d ' \n')"
    if [ "$wrapper_first" = "#" ]; then
        pass "scenario_wrapper_rollback: public wrapper preserved"
    else
        fail "scenario_wrapper_rollback: public wrapper was clobbered"
    fi

    # Evidence file must contain rollback metadata.
    if [ -f "$record" ] && grep -q '"rollback_at"' "$record"; then
        pass "scenario_wrapper_rollback: evidence file amended"
    else
        fail "scenario_wrapper_rollback: evidence file not amended with rollback metadata"
    fi
}

# Scenario 2: plain topology rollback.
scenario_plain_rollback() {
    local tmp
    tmp="$(mktemp -d)"
    trap "rm -rf '$tmp'" RETURN

    local install_dir="$tmp/home/.local/bin"
    mkdir -p "$install_dir"

    # Pre-cutover and post-cutover binaries at the public path.
    local backup="$install_dir/gt.before-pinned-1.2.0-cutover"
    make_elf "$backup" "plain-pre-123"
    make_elf "$install_dir/gt" "plain-post-456"

    local record_dir="$tmp/town/.runtime"
    mkdir -p "$record_dir"
    local record="$record_dir/pinned-1.2.0-cutover.json"
    cat > "$record" <<EOF
{
  "cutover_at": "2026-06-25T11:47:00Z",
  "installed_binary": "$install_dir/gt",
  "public_path": "$install_dir/gt",
  "wrapper_topology": "plain",
  "backup_binary": "$backup"
}
EOF

    local rc=0
    set +e
    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" GT_TOWN_ROOT="$tmp/town" \
        bash "$ROLLBACK_SCRIPT" --evidence="$record" >/dev/null 2>&1
    rc=$?
    set -e

    if [ "$rc" -ne 0 ]; then
        fail "scenario_plain_rollback: rollback script exited $rc"
        return
    fi

    if ! assert_is_elf "$install_dir/gt" "scenario_plain_rollback restored ELF"; then
        return
    fi
    if grep -q "plain-pre-123" "$install_dir/gt"; then
        pass "scenario_plain_rollback: public binary restored from backup"
    else
        fail "scenario_plain_rollback: public binary does not contain backup payload"
    fi
}

# Scenario 3: rollback refuses when the backup is not an ELF.
scenario_refuses_garbage_backup() {
    local tmp
    tmp="$(mktemp -d)"
    trap "rm -rf '$tmp'" RETURN

    local install_dir="$tmp/home/.local/bin"
    mkdir -p "$install_dir"

    local backup="$install_dir/gt.before-pinned-1.2.0-cutover"
    echo "this is not a binary" > "$backup"
    make_elf "$install_dir/gt" "post-cutover-456"

    local record_dir="$tmp/town/.runtime"
    mkdir -p "$record_dir"
    local record="$record_dir/pinned-1.2.0-cutover.json"
    cat > "$record" <<EOF
{
  "installed_binary": "$install_dir/gt",
  "backup_binary": "$backup",
  "wrapper_topology": "plain"
}
EOF

    local rc=0
    set +e
    INSTALL_DIR="$install_dir" BINARY="gt" HOME="$tmp/home" GT_TOWN_ROOT="$tmp/town" \
        bash "$ROLLBACK_SCRIPT" --evidence="$record" >/dev/null 2>&1
    rc=$?
    set -e

    if [ "$rc" -eq 0 ]; then
        fail "scenario_refuses_garbage_backup: rollback succeeded with non-ELF backup"
    else
        pass "scenario_refuses_garbage_backup: rollback rejected non-ELF backup"
    fi
}

echo "Running cutover-rollback regression tests..."
scenario_wrapper_rollback
scenario_plain_rollback
scenario_refuses_garbage_backup

echo ""
echo "Result: $PASS passed, $FAIL failed"
if [ "$FAIL" -ne 0 ]; then
    exit 1
fi
exit 0
