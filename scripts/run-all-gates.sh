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

mapfile -d '' go_files < <("$repo_root/scripts/lib/changed-go-files.sh")
if [ "${#go_files[@]}" -gt 0 ]; then
  echo "[gastown gates] gofmt check (changed Go files)"
  unformatted="$(gofmt -l "${go_files[@]}")"
  if [ -n "$unformatted" ]; then
    echo "$unformatted" >&2
    echo "[gastown gates] go files need gofmt" >&2
    exit 1
  fi
fi

echo "[gastown gates] go test ./..."
go test ./...
