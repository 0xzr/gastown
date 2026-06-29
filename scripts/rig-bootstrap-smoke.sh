#!/usr/bin/env bash
# smoke-rig-bootstrap.sh — smoke test for a Gas Town rig bootstrap state.
#
# Validates the bootstrap invariants every Gas Town rig MUST satisfy:
#   1. config.json exists at the rig root.
#   2. config.json parses as valid JSON.
#   3. config.json has type=rig.
#   4. config.json has a positive version.
#   5. config.json has a non-empty name.
#   6. config.json has beads.prefix.
#   7. config.json beads.prefix matches config.json name.
#   8. CLAUDE.md exists at the rig root.
#
# Exit codes:
#   0  — all invariants pass.
#   1  — at least one invariant failed (error printed).
#   2  — usage / environment error (script invoked incorrectly).
#
# Usage:
#   smoke-rig-bootstrap.sh [RIG_ROOT]
#
# RIG_ROOT defaults to the rig whose mayor/rig/scripts/ this file lives in
# (i.e. three levels above this script: scripts/ → rig/ → mayor/ → <rig>).
# Pass an explicit RIG_ROOT to validate an arbitrary rig directory.
#
# This script is intentionally pure POSIX-ish bash with no external
# dependencies beyond `jq` (or python3 as a JSON parser fallback) so it can
# run anywhere `bash` is available — including the CI runner that the
# Refinery merge queue invokes.

set -euo pipefail

# Resolve script directory in a portable way (no readlink -f, no realpath).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Default rig root: parent of mayor/rig/scripts/, i.e. <rig>/mayor/rig/scripts/
# The rig root is three levels above the script (scripts/ → rig/ → mayor/ → <rig>).
DEFAULT_RIG_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

RIG_ROOT="${1:-$DEFAULT_RIG_ROOT}"

if [ ! -d "$RIG_ROOT" ]; then
  echo "[smoke-rig-bootstrap] rig root not found: $RIG_ROOT" >&2
  exit 2
fi

fail_count=0

# Helper: emit a failure line and bump the failure counter.
fail() {
  echo "[smoke-rig-bootstrap] FAIL: $1" >&2
  fail_count=$((fail_count + 1))
}

pass() {
  echo "[smoke-rig-bootstrap] PASS: $1"
}

# --- Invariant 1: config.json exists ---
config_json="$RIG_ROOT/config.json"
if [ ! -f "$config_json" ]; then
  fail "config.json missing at $config_json"
  echo "[smoke-rig-bootstrap] ABORT: cannot continue without config.json" >&2
  exit 1
fi
pass "config.json exists at $config_json"

# --- Invariant 2: config.json parses as JSON ---
# Prefer jq; fall back to python3 if jq is unavailable.
#
# NOTE: never interpolate the file path or jq expression into Python source
# via shell expansion — both are attacker-controlled in adversarial settings
# (rig root passed via argv, expression derived from a stringified jq path).
# Pass them as argv to python instead; the heredoc below is QUOTED so the
# shell does not expand $file or $expr inside the Python source.
json_get() {
  # json_get <file> <jq-expression-on-.|>
  local file="$1"
  local expr="$2"
  if command -v jq >/dev/null 2>&1; then
    jq -r "$expr" "$file"
  elif command -v python3 >/dev/null 2>&1; then
    python3 - "$file" "$expr" <<'PYEOF'
import json
import sys

file_path = sys.argv[1]
expr = sys.argv[2]

with open(file_path) as f:
    d = json.load(f)

# Translate jq dotted-path expression ('.foo.bar') into a key list.
# Only simple dotted identifiers are supported — we do NOT eval the
# expression, so anything more elaborate (filters, indexes, etc.) is out
# of scope. This keeps the surface tiny and avoids accidental Python
# execution from a tainted expression.
keys = expr.lstrip('.').split('.')
keys = [k for k in keys if k]

v = d
for k in keys:
    if isinstance(v, dict) and k in v:
        v = v[k]
    else:
        v = None
        break

if isinstance(v, (dict, list)):
    print(json.dumps(v))
elif v is None:
    print('null')
else:
    print(v)
PYEOF
  else
    echo "[smoke-rig-bootstrap] neither jq nor python3 available for JSON parsing" >&2
    exit 2
  fi
}

if ! json_get "$config_json" "." >/dev/null 2>&1; then
  fail "config.json is not valid JSON"
  exit 1
fi
pass "config.json is valid JSON"

# --- Invariant 3: type == "rig" ---
type_val="$(json_get "$config_json" '.type')"
if [ "$type_val" != "rig" ]; then
  fail "config.json type is '$type_val', expected 'rig'"
else
  pass "config.json type is 'rig'"
fi

# --- Invariant 4: version is a positive integer ---
version_val="$(json_get "$config_json" '.version')"
if ! [[ "$version_val" =~ ^[0-9]+$ ]] || [ "$version_val" -lt 1 ]; then
  fail "config.json version is '$version_val', expected a positive integer"
else
  pass "config.json version is $version_val"
fi

# --- Invariant 5: name is non-empty ---
name_val="$(json_get "$config_json" '.name')"
if [ -z "$name_val" ] || [ "$name_val" = "null" ]; then
  fail "config.json name is missing or empty"
else
  pass "config.json name is '$name_val'"
fi

# --- Invariant 6: beads.prefix is present ---
prefix_val="$(json_get "$config_json" '.beads.prefix')"
if [ -z "$prefix_val" ] || [ "$prefix_val" = "null" ]; then
  fail "config.json beads.prefix is missing"
else
  pass "config.json beads.prefix is '$prefix_val'"
fi

# --- Invariant 7: beads.prefix matches rig name ---
if [ -n "$name_val" ] && [ "$name_val" != "null" ] && [ -n "$prefix_val" ] && [ "$prefix_val" != "null" ]; then
  if [ "$name_val" != "$prefix_val" ]; then
    fail "config.json name ('$name_val') does not match beads.prefix ('$prefix_val')"
  else
    pass "config.json name matches beads.prefix ('$name_val')"
  fi
fi

# --- Invariant 8: CLAUDE.md exists ---
claude_md="$RIG_ROOT/CLAUDE.md"
if [ ! -f "$claude_md" ]; then
  fail "CLAUDE.md missing at $claude_md"
else
  pass "CLAUDE.md exists at $claude_md"
fi

# --- Summary ---
if [ "$fail_count" -gt 0 ]; then
  echo "[smoke-rig-bootstrap] $fail_count invariant(s) failed for rig root: $RIG_ROOT" >&2
  exit 1
fi

echo "[smoke-rig-bootstrap] all invariants passed for rig root: $RIG_ROOT"
exit 0