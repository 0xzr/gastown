#!/usr/bin/env bash
# run-all-gates.sh — Gastown deterministic validation gate.
#
# Used by the Refinery merge queue as the phase-1 validation gate.  When this
# script exists in the repo worktree, the refinery runs it directly instead of
# falling back to the shared rig script.
#
# Checks:
#   1. No whitespace errors in the working diff.
#   2. No Git conflict markers (with Go raw-string-literal awareness).
#   3. Changed Go files are gofmt-clean.
#   4. The full Go test suite passes.
#
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

export PATH="/home/ubuntu/go-toolchain/go/bin:$PATH"
export GOFLAGS="${GOFLAGS:--buildvcs=false}"

echo "[gastown gates] git diff whitespace check"
git diff --check

echo "[gastown gates] conflict marker check"
if ! python3 scripts/check_conflict_markers.py; then
  echo "[gastown gates] conflict markers found" >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "[gastown gates] go toolchain not found; expected /home/ubuntu/go-toolchain/go/bin/go" >&2
  exit 1
fi

gofmt_input="$(mktemp)"
trap 'rm -f "$gofmt_input"' EXIT
if git rev-parse --verify -q HEAD~1 >/dev/null 2>&1; then
  git diff -z --name-only --diff-filter=ACMR HEAD~1 HEAD -- '*.go' >>"$gofmt_input"
else
  git ls-files -z '*.go' >>"$gofmt_input"
fi
git diff -z --name-only --diff-filter=ACMR HEAD -- '*.go' >>"$gofmt_input"
mapfile -d '' go_files < <(sort -zu "$gofmt_input" | while IFS= read -r -d '' file; do
  [ -f "$file" ] && printf '%s\0' "$file"
done)
if [ "${#go_files[@]}" -gt 0 ]; then
  echo "[gastown gates] gofmt check (changed Go files)"
  unformatted="$(gofmt -l "${go_files[@]}")"
  if [ -n "$unformatted" ]; then
    echo "$unformatted" >&2
    echo "[gastown gates] go files need gofmt" >&2
    exit 1
  fi
fi

# Cap Go test parallelism so concurrent gate runs (Refinery bisect of stacked
# MR batches, sibling rigs sharing the host, ad-hoc polecat runs) keep memory
# headroom. `go test ./...` was OOM-killed at default parallelism under
# concurrent load — swap exhausted, many packages already passed, no assertion
# failure. See gastown-b6my.
#
# Default: half the logical CPUs (min 2) for both GOMAXPROCS and `go test -p`.
# Override per-run with positive integers: GT_GATE_GOMAXPROCS, GT_GATE_TEST_PARALLEL.
# shellcheck source=lib/gate-parallelism.sh
source "${repo_root}/scripts/lib/gate-parallelism.sh"
cpu_count="$(nproc 2>/dev/null || echo 4)"
read -r gate_gomaxprocs gate_test_parallel < <(gt_gate_compute_parallelism "$cpu_count")
export GOMAXPROCS="$gate_gomaxprocs"

echo "[gastown gates] go test ./... (GOMAXPROCS=${gate_gomaxprocs}, -p=${gate_test_parallel})"
go test -p "$gate_test_parallel" ./...
