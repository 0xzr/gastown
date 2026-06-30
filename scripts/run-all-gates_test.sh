#!/usr/bin/env bash
# Tests for the gate parallelism computation in scripts/lib/gate-parallelism.sh.
#
# scripts/run-all-gates.sh sources that library and uses it to cap GOMAXPROCS
# and `go test -p` so concurrent gate runs (Refinery bisect, sibling rigs,
# ad-hoc polecat runs) don't OOM-kill `go test ./...`. See gastown-b6my.
#
# Run: bash scripts/run-all-gates_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/gate-parallelism.sh
source "${SCRIPT_DIR}/lib/gate-parallelism.sh"

PASS=0
FAIL=0

pass() { echo "PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "FAIL: $1"; FAIL=$((FAIL + 1)); }

assert_eq() {
  local name="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then
    pass "$name (=$actual)"
  else
    fail "$name: expected '$expected', got '$actual'"
  fi
}

# Run the computation in a clean subshell with explicit overrides.
#   compute <cpu_count> [gomaxprocs] [test_parallel]
# Empty override strings fall back to the computed default (:- semantics).
compute() {
  local cpu="$1" gomax="${2:-}" testp="${3:-}"
  (
    unset GT_GATE_GOMAXPROCS GT_GATE_TEST_PARALLEL
    [ -n "$gomax" ] && export GT_GATE_GOMAXPROCS="$gomax"
    [ -n "$testp" ] && export GT_GATE_TEST_PARALLEL="$testp"
    gt_gate_compute_parallelism "$cpu"
  )
}

# --- Default: half the CPUs, minimum 2 --------------------------------------
assert_eq "1 cpu default"   "2 2" "$(compute 1)"
assert_eq "2 cpu default"   "2 2" "$(compute 2)"
assert_eq "3 cpu default"   "2 2" "$(compute 3)"
assert_eq "4 cpu default"   "2 2" "$(compute 4)"
assert_eq "6 cpu default"   "3 3" "$(compute 6)"
assert_eq "8 cpu default"   "4 4" "$(compute 8)"
assert_eq "16 cpu default"  "8 8" "$(compute 16)"
assert_eq "32 cpu default"  "16 16" "$(compute 32)"

# --- Override one knob, leave the other at default --------------------------
assert_eq "8 cpu, GOMAXPROCS=1"        "1 4" "$(compute 8 1)"
assert_eq "8 cpu, TEST_PARALLEL=2"     "4 2" "$(compute 8 "" 2)"

# --- Override both knobs ----------------------------------------------------
assert_eq "8 cpu, both override"       "3 5" "$(compute 8 3 5)"
assert_eq "4 cpu, both override"       "1 8" "$(compute 4 1 8)"

# --- Empty-string override falls back to default ----------------------------
assert_eq "8 cpu, empty overrides"     "4 4" "$(compute 8 "" "")"

# --- Missing cpu_count argument fails loudly --------------------------------
# `:?` aborts the calling shell, so run in a subshell to contain the abort
# and observe its non-zero exit without killing this test harness.
if ( gt_gate_compute_parallelism ) 2>/dev/null; then
  fail "missing cpu_count should error (got success)"
else
  pass "missing cpu_count errors"
fi

echo
if [ "$FAIL" -eq 0 ]; then
  echo "All $PASS tests passed"
  exit 0
else
  echo "$FAIL test(s) failed, $PASS passed"
  exit 1
fi
