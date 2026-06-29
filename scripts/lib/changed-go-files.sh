#!/usr/bin/env bash
# changed-go-files.sh — list Go files changed in the current git repository.
#
# Outputs null-delimited filenames to stdout. Only files that still exist in
# the working tree are emitted. Designed to be sourced/run by run-all-gates.sh.
#
# The output includes:
#   - files changed between HEAD~1 and HEAD (when HEAD~1 is available)
#   - files changed in the working tree relative to HEAD
#
# When HEAD~1 is absent (single-commit repository or shallow clone), the script
# intentionally does NOT fall back to a full-repo scan, because that would
# false-reject on pre-existing gofmt drift.
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

gofmt_input="$(mktemp)"
trap 'rm -f "$gofmt_input"' EXIT

if git rev-parse --verify -q HEAD~1 >/dev/null 2>&1; then
  git diff -z --name-only --diff-filter=ACMR HEAD~1 HEAD -- '*.go' >>"$gofmt_input"
else
  # Single-commit or shallow clone: no meaningful baseline is available, so we
  # deliberately do not list the entire repository. Rely on the working-tree diff
  # below to catch uncommitted changes. A whole-repo list here would reintroduce
  # the false rejections described in gastown-cet.12.6.8.
  :
fi
git diff -z --name-only --diff-filter=ACMR HEAD -- '*.go' >>"$gofmt_input"

sort -zu "$gofmt_input" | while IFS= read -r -d '' file; do
  [ -f "$file" ] && printf '%s\0' "$file"
done
