#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

export PATH="/home/ubuntu/go-toolchain/go/bin:$PATH"
export GOFLAGS="${GOFLAGS:--buildvcs=false}"

echo "[gastown gates] git diff whitespace check"
git diff --check

echo "[gastown gates] conflict marker check"
# Use exact git conflict-marker tokens so decorative runs of '=' inside string
# literals (e.g. mail-body section dividers) do not trip this check.
if git grep -n -E '^(<{7} |={7}$|>{7} )' -- .; then
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

echo "[gastown gates] go test ./..."
go test ./...
