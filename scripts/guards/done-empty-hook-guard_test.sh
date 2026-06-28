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

assert_contains() {
  local test_name="$1" needle="$2" haystack="$3"
  if [[ "$haystack" == *"$needle"* ]]; then
    echo "  PASS: $test_name (contains '$needle')"
    PASS=$((PASS + 1))
  else
    echo "  FAIL: $test_name (missing '$needle'); got: $haystack"
    FAIL=$((FAIL + 1))
  fi
}

assert_not_contains() {
  local test_name="$1" needle="$2" haystack="$3"
  if [[ "$haystack" != *"$needle"* ]]; then
    echo "  PASS: $test_name (does not contain '$needle')"
    PASS=$((PASS + 1))
  else
    echo "  FAIL: $test_name (unexpectedly contains '$needle')"
    FAIL=$((FAIL + 1))
  fi
}

# Run the guard with explicit env and args, then print stdout/stderr followed
# by a __EXIT__N trailer on its own line. The exit code is captured BEFORE
# any other command runs so `|| true`-style masking cannot clobber it. This
# is the regression for codex finding #3: the prior helper used
# `... || true; echo $?`, which silently reported success even when the
# guard rejected.
run_guard() {
  local tmpdir="$1"; shift
  (
    set +e
    cd "$tmpdir/git"
    env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
        -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
        -u GT_MQ_JSON_MODE -u GT_GATE_ATTEST_DIR \
        PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
        "$@" bash "$GUARD" "$@" 2>&1
    ec=$?
    printf '__EXIT__%s\n' "$ec"
  )
}

# run_guard variants where the guard's own args differ from the env vars.
# Useful when the test wants to pass GT_MQ_JSON_MODE or override
# FAKE_BRANCH without also passing them as positional guard args.
run_guard_with_env() {
  local tmpdir="$1"; shift
  (
    set +e
    cd "$tmpdir/git"
    env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
        -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
        -u GT_MQ_JSON_MODE -u GT_GATE_ATTEST_DIR \
        PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
        "$@" bash "$GUARD" --rig gastown --slot void 2>&1
    ec=$?
    printf '__EXIT__%s\n' "$ec"
  )
}

run_guard_slot_with_env() {
  local tmpdir="$1"; local slot="$2"; shift 2
  (
    set +e
    cd "$tmpdir/git"
    env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
        -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
        -u GT_MQ_JSON_MODE -u GT_GATE_ATTEST_DIR \
        PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
        "$@" bash "$GUARD" --rig gastown --slot "$slot" 2>&1
    ec=$?
    printf '__EXIT__%s\n' "$ec"
  )
}

# Extract the exit code from a run_guard output.
guard_code() {
  printf '%s\n' "$1" | sed -n 's/^__EXIT__//p' | tail -n 1
}

# Strip the __EXIT__N trailer.
guard_body() {
  printf '%s\n' "$1" | sed '/^__EXIT__[0-9]*$/d'
}

# Set up a fake toolchain (gt, git, jq) and a fake worktree under $tmpdir.
setup_fake_env() {
  local tmpdir
  tmpdir=$(mktemp -d)
  mkdir -p "$tmpdir/git" "$tmpdir/bin"

  # Fake `gt`:
  #   - hook show emits a JSON object keyed by target.
  #     * toast  → hooked (gastown-abc)
  #     * shiny  → empty + source_bead (gastown-recov) for recovery
  #     * envcat → hooked (gastown-env) for env-canonical-target test
  #     * jasper → empty + source_bead (gastown-dg1) for cwd-drift test
  #     * branchmatch → empty + source_bead (gastown-bm), paired with FAKE_BRANCH
  #     * void   → empty, no source_bead, used as the default for rejection tests
  #   - mq list --json emits a list of MRs. The default list contains both a
  #     precise MR (gastown-mrsrc with source_issue on its own line) and a
  #     substring-confusable MR (gastown-mrsrc-extended whose description
  #     embeds "gastown-mrsrc" mid-line). The guard MUST only match the
  #     precise MR.
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
    gastown/jasper|gastown/polecats/jasper)
      echo '{"agent":"gastown/polecats/jasper","bead_id":"","status":"empty","source_bead":"gastown-dg1"}'
      ;;
    gastown/branchmatch|gastown/polecats/branchmatch)
      echo '{"agent":"gastown/polecats/branchmatch","bead_id":"","status":"empty","source_bead":"gastown-bm"}'
      ;;
    gastown/void|gastown/polecats/void)
      echo '{"agent":"gastown/polecats/void","bead_id":"","status":"empty"}'
      ;;
    *)
      echo '{"agent":"'$tgt'","bead_id":"","status":"empty"}'
      ;;
  esac
  exit 0
fi
if [ "${1:-}" = "mq" ] && [ "${2:-}" = "list" ]; then
  case "${GT_MQ_JSON_MODE:-default}" in
    precise-only)
      cat <<JSON
[
  {"id":"gastown-wisp-mr-only","description":"branch: polecat/jasper/gastown-mrsrc@x\nsource_issue: gastown-mrsrc\n"}
]
JSON
      ;;
    substring-only)
      cat <<JSON
[
  {"id":"gastown-wisp-mr-confusable","description":"branch: polecat/jasper/gastown-mrsrc-extended@x\nnote: contains gastown-mrsrc substring\n"}
]
JSON
      ;;
    dotted-confusable)
      cat <<JSON
[
  {"id":"gastown-wisp-dot","description":"branch: polecat/jasper/gastown-cetX12X6X2@x\nsource_issue: gastown-cetX12X6X2\n"}
]
JSON
      ;;
    dotted-exact)
      cat <<JSON
[
  {"id":"gastown-wisp-dot-exact","description":"branch: polecat/jasper/gastown-cet.12.6.2@x\nsource_issue: gastown-cet.12.6.2\n"}
]
JSON
      ;;
    empty)
      echo '[]'
      ;;
    *)
      cat <<JSON
[
  {"id":"gastown-wisp-mr","description":"branch: polecat/jasper/gastown-mrsrc@x\nsource_issue: gastown-mrsrc\n"},
  {"id":"gastown-wisp-mr-confusable","description":"branch: polecat/jasper/gastown-mrsrc-extended@x\nnote: contains gastown-mrsrc substring\n"}
]
JSON
      ;;
  esac
  exit 0
fi
exit 0
EOF
  chmod +x "$tmpdir/bin/gt"

  # Fake `git` — answers rev-parse --abbrev-ref HEAD and rev-parse HEAD^{tree}.
  # Strips global git options (`-C <dir>` consumes two args; `--git-dir=<p>`,
  # `--bare`, etc. consume one) so positional subcommand args land in the
  # right slots. FAKE_BRANCH is the value returned for --abbrev-ref HEAD; if
  # empty, returns "main" so no branch_evidence fires. FAKE_TREE is the tree
  # hash used by the keyed attestation check.
  cat > "$tmpdir/bin/git" <<'EOF'
#!/usr/bin/env bash
# Strip global git options so positional subcommand args land in the right
# slots. -C takes a value; --git-dir= takes a value via =.
args=()
while [ $# -gt 0 ]; do
  case "$1" in
    -C) shift 2 ;;
    --git-dir=*) shift ;;
    --bare) shift ;;
    *) args+=("$1"); shift ;;
  esac
done
set -- "${args[@]}"
if [ "$1" = "rev-parse" ] && [ "$2" = "--abbrev-ref" ] && [ "$3" = "HEAD" ]; then
  if [ -n "${FAKE_BRANCH:-}" ]; then
    echo "${FAKE_BRANCH}"
  else
    echo "main"
  fi
  exit 0
fi
if [ "$1" = "rev-parse" ] && [ "$2" = "HEAD^{tree}" ]; then
  echo "${FAKE_TREE:-1234567890abcdef1234567890abcdef12345678}"
  exit 0
fi
exit 0
EOF
  chmod +x "$tmpdir/bin/git"

  # Fake `jq` — defer to system jq so the guard's jq queries work unchanged.
  cat > "$tmpdir/bin/jq" <<'EOF'
#!/usr/bin/env bash
exec /usr/bin/jq "$@"
EOF
  chmod +x "$tmpdir/bin/jq"

  # .git marker so the guard's `if [ -e "$WORKTREE/.git" ]` branch evaluates true.
  mkdir -p "$tmpdir/git/.git"

  echo "$tmpdir"
}

# ── Tests ───────────────────────────────────────────────────────────────────

echo "=== done-empty-hook-guard tests ==="
echo ""

# Test 1: guard passes when disabled (env unset, shadow default).
echo "Test: disabled guard passes through"
tmpdir=$(setup_fake_env)
out=$(run_guard_slot_with_env "$tmpdir" toast)
code=$(guard_code "$out")
assert_exit "disabled guard exits 0" "0" "$code"
rm -rf "$tmpdir"

# Test 2: enforce + hooked bead → pass.
echo "Test: hooked bead passes in enforce mode"
tmpdir=$(setup_fake_env)
out=$(run_guard_slot_with_env "$tmpdir" toast GT_DONE_EMPTY_HOOK_GUARD=enforce)
code=$(guard_code "$out")
assert_exit "hooked bead in enforce mode exits 0" "0" "$code"
rm -rf "$tmpdir"

# Test 3: enforce + source_bead recovery → pass (gastown-dg1).
# Uses slot=shiny whose hook show emits source_bead=gastown-recov. Default
# FAKE_BRANCH is "main", so this passes via source_bead (not branch_evidence).
echo "Test: source_bead recovery passes in enforce mode"
tmpdir=$(setup_fake_env)
out=$(run_guard_slot_with_env "$tmpdir" shiny GT_DONE_EMPTY_HOOK_GUARD=enforce)
code=$(guard_code "$out")
body=$(guard_body "$out")
assert_exit "source_bead recovery exits 0" "0" "$code"
assert_contains "action reports source_bead" "action=source_bead" "$body"
rm -rf "$tmpdir"

# Test 4: enforce + explicit source with PRECISE MR evidence → pass.
# Uses void slot (empty hook) so the guard has to look at MR evidence.
echo "Test: explicit source with precise MR evidence passes"
tmpdir=$(setup_fake_env)
out=$(run_guard_with_env "$tmpdir" GT_DONE_EMPTY_HOOK_GUARD=enforce GT_MQ_JSON_MODE=precise-only)
# Inject --source by re-running with a small wrapper.
out=$(env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
        -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
        PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
        GT_DONE_EMPTY_HOOK_GUARD=enforce GT_MQ_JSON_MODE=precise-only \
        bash "$GUARD" --rig gastown --slot void --source gastown-mrsrc 2>&1
      ec=$?
      printf '__EXIT__%s\n' "$ec")
code=$(guard_code "$out")
body=$(guard_body "$out")
assert_exit "precise MR evidence exits 0" "0" "$code"
assert_contains "action reports mr_evidence" "action=mr_evidence" "$body"
rm -rf "$tmpdir"

# Test 5: enforce + substring-only MR → REJECT.
# Regression for codex finding #1: substring matches must NOT pass.
# The fake MR embeds "gastown-mrsrc" in a non-source_issue line; the guard
# must reject because no MR has the exact "source_issue: gastown-mrsrc\n" needle.
echo "Test: substring-only MR evidence rejected (codex finding #1)"
tmpdir=$(setup_fake_env)
out=$(env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
        -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
        PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
        GT_DONE_EMPTY_HOOK_GUARD=enforce GT_MQ_JSON_MODE=substring-only \
        bash "$GUARD" --rig gastown --slot void --source gastown-mrsrc 2>&1
      ec=$?
      printf '__EXIT__%s\n' "$ec")
code=$(guard_code "$out")
body=$(guard_body "$out")
assert_exit "substring-only MR exits 1" "1" "$code"
assert_contains "rejection mentions empty_hook_no_evidence" "empty_hook_no_evidence" "$body"
assert_not_contains "rejection does NOT claim mr_evidence" "action=mr_evidence" "$body"
rm -rf "$tmpdir"

# Test 6: enforce + empty MR list → reject.
echo "Test: empty MR list rejects in enforce mode"
tmpdir=$(setup_fake_env)
out=$(env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
        -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
        PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
        GT_DONE_EMPTY_HOOK_GUARD=enforce GT_MQ_JSON_MODE=empty \
        bash "$GUARD" --rig gastown --slot void --source gastown-mrsrc 2>&1
      ec=$?
      printf '__EXIT__%s\n' "$ec")
code=$(guard_code "$out")
assert_exit "empty MR list exits 1" "1" "$code"
rm -rf "$tmpdir"

# Test 7: enforce + branch-derived source_bead → PASS via branch_evidence.
# Slot=branchmatch returns hook_source=gastown-bm. Set FAKE_BRANCH so the
# embedded issue matches the source_bead. Asserts action=branch_evidence
# (priority over source_bead).
echo "Test: branch-derived source_bead triggers branch_evidence"
tmpdir=$(setup_fake_env)
out=$(env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
        -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
        PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
        GT_DONE_EMPTY_HOOK_GUARD=enforce FAKE_BRANCH="polecat/jasper/gastown-bm@mqx" \
        bash "$GUARD" --rig gastown --slot branchmatch 2>&1
      ec=$?
      printf '__EXIT__%s\n' "$ec")
code=$(guard_code "$out")
body=$(guard_body "$out")
assert_exit "branch_evidence exits 0" "0" "$code"
assert_contains "action reports branch_evidence" "action=branch_evidence" "$body"
rm -rf "$tmpdir"

# Test 8: override bypasses reject.
echo "Test: override bypasses reject"
tmpdir=$(setup_fake_env)
out=$(env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
        -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
        PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
        GT_DONE_EMPTY_HOOK_GUARD=enforce GT_DONE_EMPTY_HOOK_OVERRIDE=1 \
        bash "$GUARD" --rig gastown --slot void 2>&1
      ec=$?
      printf '__EXIT__%s\n' "$ec")
code=$(guard_code "$out")
body=$(guard_body "$out")
assert_exit "override exits 0" "0" "$code"
assert_contains "action reports override" "action=override" "$body"
rm -rf "$tmpdir"

# Test 9: enforce rejects empty hook with recovery message.
echo "Test: enforce rejects empty hook with recovery path"
tmpdir=$(setup_fake_env)
out=$(env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
        -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
        PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
        GT_DONE_EMPTY_HOOK_GUARD=enforce \
        bash "$GUARD" --rig gastown --slot void 2>&1
      ec=$?
      printf '__EXIT__%s\n' "$ec")
code=$(guard_code "$out")
body=$(guard_body "$out")
assert_exit "empty hook enforce exits 1" "1" "$code"
assert_contains "rejection includes done_guard_reject" "done_guard_reject" "$body"
assert_contains "rejection includes recovery" "recovery=" "$body"
rm -rf "$tmpdir"

# Test 10: env canonical target recovers.
echo "Test: env canonical target overrides cwd misresolution"
tmpdir=$(setup_fake_env)
out=$(env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
        -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
        PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
        GT_DONE_EMPTY_HOOK_GUARD=enforce GT_RIG=gastown GT_POLECAT=envcat \
        bash "$GUARD" --rig gastown --slot wrongslot 2>&1
      ec=$?
      printf '__EXIT__%s\n' "$ec")
code=$(guard_code "$out")
assert_exit "env canonical target recovers" "0" "$code"
rm -rf "$tmpdir"

# Test 11: shadow mode logs but does not block.
echo "Test: shadow mode logs and exits 0"
tmpdir=$(setup_fake_env)
out=$(env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
        -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
        PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
        GT_DONE_EMPTY_HOOK_GUARD=shadow \
        bash "$GUARD" --rig gastown --slot void 2>&1
      ec=$?
      printf '__EXIT__%s\n' "$ec")
code=$(guard_code "$out")
body=$(guard_body "$out")
assert_exit "shadow mode exits 0" "0" "$code"
assert_contains "shadow mode logs shadow:" "shadow:" "$body"
assert_contains "shadow mode logs empty_hook_no_evidence" "empty_hook_no_evidence" "$body"
rm -rf "$tmpdir"

# Test 12: dotted bead ID uses literal, not regex, matching.
# The confusable MR's source_issue differs in characters where dots would
# match any character under a regex. The guard MUST reject because there is
# no exact source_issue match.
echo "Test: dotted bead ID rejects regex-style false match"
tmpdir=$(setup_fake_env)
out=$(env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
        -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
        PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
        GT_DONE_EMPTY_HOOK_GUARD=enforce GT_MQ_JSON_MODE=dotted-confusable \
        bash "$GUARD" --rig gastown --slot void --source gastown-cet.12.6.2 2>&1
      ec=$?
      printf '__EXIT__%s\n' "$ec")
code=$(guard_code "$out")
body=$(guard_body "$out")
assert_exit "dotted confusable MR exits 1" "1" "$code"
assert_contains "rejection mentions empty_hook_no_evidence" "empty_hook_no_evidence" "$body"
assert_not_contains "rejection does NOT claim mr_evidence" "action=mr_evidence" "$body"
rm -rf "$tmpdir"

# Test 13: exact dotted bead ID matches when present.
echo "Test: exact dotted bead ID matches literally"
tmpdir=$(setup_fake_env)
out=$(env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
        -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
        PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
        GT_DONE_EMPTY_HOOK_GUARD=enforce GT_MQ_JSON_MODE=dotted-exact \
        bash "$GUARD" --rig gastown --slot void --source gastown-cet.12.6.2 2>&1
      ec=$?
      printf '__EXIT__%s\n' "$ec")
code=$(guard_code "$out")
body=$(guard_body "$out")
assert_exit "exact dotted MR exits 0" "0" "$code"
assert_contains "action reports mr_evidence" "action=mr_evidence" "$body"
rm -rf "$tmpdir"

# Test 14: forged commit attestation does not grant evidence.
# Regression for codex finding #3: a non-empty file named after the tree hash
# under $GT_GATE_ATTEST_DIR must not satisfy the guard. Durable HMAC
# attestation is enforced by the refinery merge path, not by this script.
echo "Test: forged commit attestation is rejected"
tmpdir=$(setup_fake_env)
attest_dir="$tmpdir/attest"
mkdir -p "$attest_dir"
echo -n "gastown-durable-review-v1" > "$attest_dir/1234567890abcdef1234567890abcdef12345678"
out=$(env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
        -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
        PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
        GT_DONE_EMPTY_HOOK_GUARD=enforce GT_MQ_JSON_MODE=empty \
        GT_GATE_ATTEST_DIR="$attest_dir" FAKE_TREE=1234567890abcdef1234567890abcdef12345678 \
        bash "$GUARD" --rig gastown --slot void --source gastown-mrsrc 2>&1
      ec=$?
      printf '__EXIT__%s\n' "$ec")
code=$(guard_code "$out")
body=$(guard_body "$out")
assert_exit "forged attestation exits 1" "1" "$code"
assert_contains "rejection mentions empty_hook_no_evidence" "empty_hook_no_evidence" "$body"
assert_not_contains "rejection does NOT claim commit_evidence" "action=commit_evidence" "$body"
rm -rf "$tmpdir"

# Test 15: canonical worktree overrides cwd drift for branch evidence.
# Regression for cwd-drift false reject: the process cwd is a non-git
# directory, but GT_RIG/GT_POLECAT/GT_TOWN locate the real polecat worktree.
# The guard reads the branch from the canonical worktree and recovers via
# branch_evidence.
echo "Test: canonical worktree recovers from cwd drift"
tmpdir=$(setup_fake_env)
mkdir -p "$tmpdir/town/gastown/polecats/jasper/gastown"
touch "$tmpdir/town/gastown/polecats/jasper/gastown/.git"
mkdir -p "$tmpdir/drift"
out=$(
  set +e
  cd "$tmpdir/drift"
  env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
      -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT -u GT_MQ_JSON_MODE -u GT_GATE_ATTEST_DIR \
      PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
      GT_DONE_EMPTY_HOOK_GUARD=enforce \
      GT_RIG=gastown GT_POLECAT=jasper GT_TOWN="$tmpdir/town" \
      FAKE_BRANCH="polecat/jasper/gastown-dg1@mqx" \
      bash "$GUARD" --rig gastown --slot jasper 2>&1
  ec=$?
  printf '__EXIT__%s\n' "$ec"
)
code=$(guard_code "$out")
body=$(guard_body "$out")
assert_exit "cwd drift canonical worktree exits 0" "0" "$code"
assert_contains "action reports branch_evidence" "action=branch_evidence" "$body"
rm -rf "$tmpdir"

# Test 16: helper records real exit code.
# Regression for prior helper bug: the helper MUST NOT mask failures.
# If a future helper introduces `|| true` before recording $?, this test
# would falsely report exit=0 here, so it asserts exit=1 explicitly.
echo "Test: helper records real exit code"
tmpdir=$(setup_fake_env)
out=$(env -u GT_DONE_EMPTY_HOOK_GUARD -u GT_DONE_EMPTY_HOOK_OVERRIDE \
        -u GT_RIG -u GT_POLECAT -u GT_TOWN -u GT_TOWN_ROOT -u GT_ROOT \
        PATH="$tmpdir/bin:${PATH:-/usr/bin:/bin}" \
        GT_DONE_EMPTY_HOOK_GUARD=enforce \
        bash "$GUARD" --rig gastown --slot void 2>&1
      ec=$?
      printf '__EXIT__%s\n' "$ec")
code=$(guard_code "$out")
assert_exit "real exit code is 1 (not masked to 0)" "1" "$code"
rm -rf "$tmpdir"

# ── Summary ──────────────────────────────────────────────────────────────────

echo ""
echo "Results: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]] && exit 0 || exit 1
