#!/usr/bin/env bash
#
# Tests for done-empty-hook-guard.sh
#
# Run: bash scripts/guards/done-empty-hook-guard_test.sh
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GUARD="$SCRIPT_DIR/done-empty-hook-guard.sh"
PASS=0
FAIL=0

# Helpers ─────────────────────────────────────────────────────────────────────

assert_exit() {
  local test_name="$1" expected="$2" actual="$3"
  if [[ "$actual" == "$expected" ]]; then
    echo "  PASS: $test_name (exit $actual)"
    PASS=$((PASS + 1))
  else
    echo "  FAIL: $test_name (expected exit $expected, got $actual)"
    FAIL=$((FAIL + 1))
  fi
}

make_fake_bin() {
  local tmpdir="$1" name="$2"
  mkdir -p "$tmpdir/bin"
  cat > "$tmpdir/bin/$name"
  chmod +x "$tmpdir/bin/$name"
}

# Create a fake toolchain and working directory.
setup_fake_env() {
  local tmpdir
  tmpdir=$(mktemp -d)
  mkdir -p "$tmpdir/git" "$tmpdir/bin"

  cat > "$tmpdir/bin/gt" <<'EOF'
#!/usr/bin/env bash
if [ "${1:-}" = "hook" ] && [ "${2:-}" = "show" ]; then
  tgt="${3:-}"
  case "$tgt" in
    gastown/toast|gastown/polecats/toast)
      echo '{"agent":"gastown/polecats/toast","bead_id":"gastown-abc","status":"hooked"}'
      ;;
    gastown/shiny|gastown/polecats/shiny)
      echo '{"agent":"gastown/polecats/shiny","bead_id":"","status":"empty","source_bead":"gastown-recov"}'
      ;;
    gastown/envcat|gastown/polecats/envcat)
      echo '{"agent":"gastown/polecats/envcat","bead_id":"gastown-env","status":"hooked"}'
      ;;
    *)
      echo '{"agent":"'$tgt'","bead_id":"","status":"empty"}'
      ;;
  esac
  exit 0
fi
if [ "${1:-}" = "mq" ] && [ "${2:-}" = "list" ]; then
  echo '[{"id":"gastown-wisp-mr","source_issue":"gastown-mrsrc"}]'
  exit 0
fi
exit 0
EOF
  chmod +x "$tmpdir/bin/gt"

  cat > "$tmpdir/bin/git" <<'EOF'
#!/usr/bin/env bash
if [ "$1" = "rev-parse" ] && [ "$2" = "--abbrev-ref" ] && [ "$3" = "HEAD" ]; then
  echo "polecat/shiny/gastown-recov@mqx"
fi
exit 0
EOF
  chmod +x "$tmpdir/bin/git"

  cat > "$tmpdir/bin/jq" <<'EOF'
#!/usr/bin/env bash
exec /usr/bin/jq "$@"
EOF
  chmod +x "$tmpdir/bin/jq"

  echo "$tmpdir"
}

# Run guard in a subshell and print only the exit code.
guard_exit() {
  local tmpdir="$1"
  shift
  (
    set +e
    cd "$tmpdir/git"
    env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
        -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
        PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
        "$@" bash "$GUARD" --rig gastown --slot toast >/dev/null 2>&1
    echo $?
  )
}

# Run guard and print stdout/stderr with exit code appended as its own line.
guard_output() {
  local tmpdir="$1"
  shift
  local ec_file
  ec_file=$(mktemp)
  (
    cd "$tmpdir/git"
    env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
        -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
        PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
        "$@" bash "$GUARD" --rig gastown --slot toast 2>&1 || true
    echo $? > "$ec_file"
  )
  cat "$ec_file"
  rm -f "$ec_file"
}

# Extract last line (exit code) from guard_output.
last_line() {
  local input="$1"
  if [ -z "$input" ]; then
    echo ""
  else
    printf '%s\n' "$input" | tail -n 1
  fi
}

# ── Tests ───────────────────────────────────────────────────────────────────

echo "=== done-empty-hook-guard tests ==="
echo ""

# Test 1: guard passes when disabled (env unset)
echo "Test: disabled guard passes through"
tmpdir=$(setup_fake_env)
code=$(guard_exit "$tmpdir")
assert_exit "disabled guard exits 0" "0" "$code"
rm -rf "$tmpdir"

# Test 2: enforce + hooked bead → pass
echo "Test: hooked bead passes in enforce mode"
tmpdir=$(setup_fake_env)
code=$(guard_exit "$tmpdir" GT_DONE_EMPTY_HOOK_GUARD=enforce)
assert_exit "hooked bead in enforce mode exits 0" "0" "$code"
rm -rf "$tmpdir"

# Test 3: enforce + source_bead recovery → pass (gastown-dg1)
echo "Test: source_bead recovery passes in enforce mode"
tmpdir=$(setup_fake_env)
out=$(guard_output "$tmpdir" GT_DONE_EMPTY_HOOK_GUARD=enforce --slot shiny)
code=$(last_line "$out")
assert_exit "source_bead recovery exits 0" "0" "$code"
rm -rf "$tmpdir"

# Test 4: enforce + explicit source with MR evidence → pass
echo "Test: explicit source with MR evidence passes"
tmpdir=$(setup_fake_env)
out=$(
  cd "$tmpdir/git"
  env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
      -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
      PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
      GT_DONE_EMPTY_HOOK_GUARD=enforce \
      bash "$GUARD" --rig gastown --slot toast --source gastown-mrsrc 2>&1
  echo $?
)
code=$(last_line "$out")
assert_exit "explicit source with MR evidence exits 0" "0" "$code"
rm -rf "$tmpdir"

# Test 5: override bypasses reject
echo "Test: override bypass"
tmpdir=$(setup_fake_env)
out=$(
  cd "$tmpdir/git"
  env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
      -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
      PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
      GT_DONE_EMPTY_HOOK_GUARD=enforce GT_DONE_EMPTY_HOOK_OVERRIDE=1 \
      bash "$GUARD" --rig gastown --slot void 2>&1
  echo $?
)
code=$(last_line "$out")
assert_exit "override exits 0" "0" "$code"
rm -rf "$tmpdir"

# Test 6: empty hook + no evidence + enforce → reject with recovery message
echo "Test: enforce rejects empty hook with recovery message"
tmpdir=$(setup_fake_env)
out=$(
  set +e
  cd "$tmpdir/git"
  env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
      -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
      PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
      GT_DONE_EMPTY_HOOK_GUARD=enforce \
      bash "$GUARD" --rig gastown --slot void 2>&1
  echo $?
)
code=$(last_line "$out")
if [[ "$out" == *"done_guard_reject"* ]] && [[ "$out" == *"recovery="* ]]; then
  echo "  PASS: rejection includes recovery path"
  PASS=$((PASS + 1))
else
  echo "  FAIL: expected done_guard_reject with recovery, got: $out"
  FAIL=$((FAIL + 1))
fi
assert_exit "empty hook enforce exits 1" "1" "$code"
rm -rf "$tmpdir"

# Test 7: misresolved target via env: env canonical target has hooked work even
# though cwd-derived slot would be empty.
echo "Test: env canonical target overrides cwd misresolution"
tmpdir=$(setup_fake_env)
out=$(
  cd "$tmpdir/git"
  env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
      -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
      PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
      GT_DONE_EMPTY_HOOK_GUARD=enforce GT_RIG=gastown GT_POLECAT=envcat \
      bash "$GUARD" --rig gastown --slot wrongslot 2>&1
  echo $?
)
code=$(last_line "$out")
assert_exit "env canonical target recovers" "0" "$code"
rm -rf "$tmpdir"

# Test 8: shadow mode logs but does not block
echo "Test: shadow mode logs and exits 0"
tmpdir=$(setup_fake_env)
out=$(
  cd "$tmpdir/git"
  env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
      -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
      PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
      GT_DONE_EMPTY_HOOK_GUARD=shadow \
      bash "$GUARD" --rig gastown --slot void 2>&1 || true
  echo $?
)
code=$(last_line "$out")
if [[ "$out" == *"shadow:"* ]] && [[ "$out" == *"empty_hook_no_evidence"* ]]; then
  echo "  PASS: shadow mode logs rejection"
  PASS=$((PASS + 1))
else
  echo "  FAIL: expected shadow log, got: $out"
  FAIL=$((FAIL + 1))
fi
assert_exit "shadow mode exits 0" "0" "$code"
rm -rf "$tmpdir"

# ── Summary ──────────────────────────────────────────────────────────────────

echo ""
echo "Results: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]] && exit 0 || exit 1
