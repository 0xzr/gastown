#!/usr/bin/env bash
# Regression tests for scripts/run-all-gates.sh gofmt scoping.
#
# Verifies that run-all-gates.sh (via scripts/lib/changed-go-files.sh) checks
# only changed Go files, not every tracked file. This prevents false rejections
# from pre-existing gofmt drift unrelated to the change being validated.
#
# Run: bash scripts/run-all-gates_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
HELPER="$REPO_ROOT/scripts/lib/changed-go-files.sh"
PASS=0
FAIL=0

setup_git() {
  TMPDIR="$(mktemp -d)"
  cd "$TMPDIR"
  git init -q
  git config user.email "test@example.com"
  git config user.name "Test User"
}

cleanup() {
  if [[ -n "${TMPDIR:-}" && -d "$TMPDIR" ]]; then
    rm -rf "$TMPDIR"
  fi
}
trap cleanup EXIT

# Return the null-delimited file list as newline-delimited text for assertions.
normalize() {
  tr '\0' '\n' | sed '/^$/d' | sort
}

assert_eq() {
  local test_name="$1" expected="$2" actual="$3"
  if [[ "$expected" == "$actual" ]]; then
    echo "  PASS: $test_name"
    PASS=$((PASS + 1))
  else
    echo "  FAIL: $test_name"
    echo "    expected:"
    echo "$expected" | sed 's/^/      /'
    echo "    got:"
    echo "$actual" | sed 's/^/      /'
    FAIL=$((FAIL + 1))
  fi
}

echo "=== run-all-gates gofmt scoping tests ==="
echo ""

# Test 1: HEAD~1 present — only changed files (last commit + working tree).
echo "Test: with HEAD~1, pre-existing files are excluded from changed list"
setup_git
# Commit 1 — pre-existing drift.
printf 'package old\n\nfunc BadlyFormatted(  ) {\n}\n' > old.go
git add old.go
git commit -q -m "initial"
# Commit 2 — a changed file in the commit range HEAD~1..HEAD.
printf 'package new\n\nfunc New(  ) {}\n' > new.go
git add new.go
git commit -q -m "add new"
# Working tree change.
printf 'package work\n\nfunc Work(  ) {}\n' > working.go
git add working.go

got="$("$HELPER" | normalize)"
cleanup
assert_eq "HEAD~1 present excludes pre-existing file" $'new.go\nworking.go' "$got"

# Test 2: HEAD~1 absent (single-commit repo / shallow clone) — do not scan whole repo.
echo "Test: without HEAD~1, whole-repo fallback is avoided"
setup_git
printf 'package old\n\nfunc BadlyFormatted(  ) {\n}\n' > old.go
printf 'package new\n\nfunc New(  ) {}\n' > new.go
git add old.go new.go
git commit -q -m "initial"
# Only working-tree change should appear; the committed files must NOT be listed
# because there is no baseline to diff against.
printf 'package work\n\nfunc Work(  ) {}\n' > working.go
git add working.go

got="$("$HELPER" | normalize)"
cleanup
assert_eq "HEAD~1 absent avoids whole-repo fallback" $'working.go' "$got"

# Test 3: HEAD~1 present — deleted file is not listed.
echo "Test: deleted files are excluded from changed list"
setup_git
printf 'package keep\n\nfunc Keep() {}\n' > keep.go
printf 'package drop\n\nfunc Drop() {}\n' > drop.go
git add keep.go drop.go
git commit -q -m "initial"
git rm -q drop.go
git commit -q -m "remove drop"
printf 'package keep\n\nfunc Keep2() {}\n' > keep2.go
git add keep2.go

got="$("$HELPER" | normalize)"
cleanup
assert_eq "deleted files excluded" $'keep2.go' "$got"

# Test 4: clean repo with HEAD~1 present and no Go changes in last commit.
echo "Test: clean repo with non-Go last commit produces empty changed list"
setup_git
printf 'package keep\n\nfunc Keep() {}\n' > keep.go
git add keep.go
git commit -q -m "initial"
printf 'no-op change\n' > README.md
git add README.md
git commit -q -m "docs only"

got="$("$HELPER" | normalize)"
cleanup
assert_eq "clean repo empty list" "" "$got"

# Test 5: HEAD~1 absent + clean working tree produces empty list.
echo "Test: single-commit clean repo produces empty changed list"
setup_git
printf 'package old\n\nfunc Old() {}\n' > old.go
git add old.go
git commit -q -m "initial"

got="$("$HELPER" | normalize)"
cleanup
assert_eq "single-commit clean repo empty list" "" "$got"

echo ""
echo "Results: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]] && exit 0 || exit 1
