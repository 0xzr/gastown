#!/usr/bin/env bash
# =============================================================================
# GATE-PARALLELISM LIBRARY
# =============================================================================
#
# Computes bounded Go test parallelism for the Gastown validation gate
# (scripts/run-all-gates.sh).
#
# The gate's `go test ./...` step at default parallelism can exhaust memory
# when multiple full gate/test processes run concurrently — Refinery bisect
# of a stacked MR batch, sibling rigs sharing the host, and ad-hoc polecat
# gate runs all pile up. go test was OOM-killed under exactly this condition
# (swap full, many packages already passed, no assertion failure) during
# durable gate validation on 2026-06-30. See gastown-b6my.
#
# This caps both knobs that drive go test's resource footprint:
#   - GOMAXPROCS  — in-process goroutine parallelism per test binary.
#   - go test -p  — number of test binaries compiled and run at once.
#
# Default for each: half the logical CPU count, minimum 2. Override either
# per-invocation by exporting a positive integer:
#   GT_GATE_GOMAXPROCS       -> overrides GOMAXPROCS
#   GT_GATE_TEST_PARALLEL    -> overrides `go test -p`
# An empty override falls back to the computed default.
#
# Public functions (side-effect-free):
#   gt_gate_compute_parallelism <cpu_count>
#       Prints "<gomaxprocs> <test_parallel>" for the given logical CPU
#       count, honoring GT_GATE_GOMAXPROCS / GT_GATE_TEST_PARALLEL.
#
# Sourced by scripts/run-all-gates.sh and scripts/run-all-gates_test.sh.
# Not executed directly; guarding against direct execution below.

gt_gate_compute_parallelism() {
  local cpu_count="${1:?cpu_count required}"
  local default=$(( cpu_count / 2 ))
  if [ "$default" -lt 2 ]; then
    default=2
  fi
  local gomaxprocs="${GT_GATE_GOMAXPROCS:-$default}"
  local test_parallel="${GT_GATE_TEST_PARALLEL:-$default}"
  printf '%s %s\n' "$gomaxprocs" "$test_parallel"
}

# Guard: this file is meant to be sourced, not run. If executed directly,
# print usage and exit non-zero so a mistaken `bash gate-parallelism.sh`
# fails loudly instead of silently defining functions and exiting 0.
if [ "${BASH_SOURCE[0]:-}" = "${0:-}" ]; then
  echo "gate-parallelism.sh: source this file from a gate script; do not execute it directly." >&2
  echo "  source scripts/lib/gate-parallelism.sh" >&2
  echo "  gt_gate_compute_parallelism <cpu_count>" >&2
  exit 1
fi
