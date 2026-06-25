#!/usr/bin/env bash
# Cut over the live ~/.local/bin/gt binary to the pinned Gas Town 1.2.0 runtime
# line while carrying the merged hardening fixes from origin/main.
#
# This script builds gt with Version=1.2.0, marks the binary as part of the
# operator-approved pinned runtime line, installs it safely, and verifies that
# the new binary contains the REWORK_DEFERRED throttle before finishing.
#
# Usage:
#   scripts/cutover-pinned-1.2.0.sh [--skip-forward-check]
#
# Requirements:
#   - Run from inside the gastown source repo worktree.
#   - Current HEAD must be a descendant of the installed binary's commit
#     (Makefile check-forward-only). Pass --skip-forward-check to override.
#   - The live binary path is $HOME/.local/bin/gt.
#
# Post-conditions:
#   - ~/.local/bin/gt reports "gt version 1.2.0".
#   - gt version --verbose shows "Pinned runtime line: 1.2.0" and the hardening
#     fixes list.
#   - gt witness rework-deferred dry-run passes.
#   - Evidence is recorded in $GT_TOWN_ROOT/.runtime/pinned-1.2.0-cutover.json.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

INSTALL_DIR="${HOME}/.local/bin"
BINARY="${INSTALL_DIR}/gt"
SKIP_FORWARD_CHECK=""

for arg in "$@"; do
  case "$arg" in
    --skip-forward-check) SKIP_FORWARD_CHECK=1 ;;
    *) echo "Unknown argument: $arg" >&2; exit 1 ;;
  esac
done

# Determine town root. GT_TOWN_ROOT is respected if set; otherwise fall back to
# the conventional location used by gastown deployments.
TOWN_ROOT="${GT_TOWN_ROOT:-/home/ubuntu/gt-town}"
if [ ! -d "${TOWN_ROOT}" ]; then
  echo "ERROR: town root ${TOWN_ROOT} does not exist" >&2
  exit 1
fi

RECORD_DIR="${TOWN_ROOT}/.runtime"
RECORD_FILE="${RECORD_DIR}/pinned-1.2.0-cutover.json"

echo "=== GT pinned 1.2.0 runtime cutover ==="
echo "Repo:      ${REPO_ROOT}"
echo "Install:   ${BINARY}"
echo "Town root: ${TOWN_ROOT}"
echo "Record:    ${RECORD_FILE}"
echo ""

# Verify the current source tree is on a descendant of the installed binary.
if [ -z "${SKIP_FORWARD_CHECK:-}" ]; then
  INSTALLED_COMMIT="$(${BINARY} version --verbose 2>/dev/null | grep -o '@[a-f0-9]*' | head -1 | tr -d '@' || true)"
  if [ -n "${INSTALLED_COMMIT}" ]; then
    HEAD_COMMIT="$(git -C "${REPO_ROOT}" rev-parse HEAD)"
    if ! git -C "${REPO_ROOT}" merge-base --is-ancestor "${INSTALLED_COMMIT}" "${HEAD_COMMIT}" 2>/dev/null; then
      echo "ERROR: HEAD ${HEAD_COMMIT} is not a descendant of installed binary ${INSTALLED_COMMIT}" >&2
      echo "Refusing to cut over to an older or diverged commit." >&2
      echo "Use --skip-forward-check to override (dangerous)." >&2
      exit 1
    fi
    echo "Forward-only check passed: ${INSTALLED_COMMIT} -> ${HEAD_COMMIT}"
  else
    echo "Warning: cannot determine installed binary commit; skipping forward check"
  fi
else
  echo "Skipping forward-only check (--skip-forward-check)"
fi

# Record pre-cutover evidence.
PRE_VERSION="$(${BINARY} version --verbose 2>/dev/null || echo '<not installed>')"
echo "Pre-cutover binary version:"
echo "${PRE_VERSION}" | sed 's/^/  /'
echo ""

# Build the pinned 1.2.0 runtime binary from current source.
echo "Building pinned 1.2.0 runtime binary..."
(
  cd "${REPO_ROOT}"
  make safe-install \
    VERSION=1.2.0 \
    BUILD=pinned \
    PINNED_RUNTIME_LINE=1.2.0 \
    FEATURE_FLAGS=rework-deferred-throttle,hooked-polecats-working
)
echo ""

# Verify the installed binary reports the approved version.
echo "Verifying installed binary version..."
if ! ${BINARY} version | grep -q "gt version 1.2.0"; then
  echo "ERROR: installed binary does not report version 1.2.0" >&2
  ${BINARY} version >&2
  exit 1
fi
${BINARY} version --verbose | sed 's/^/  /'
echo ""

# Verify pinned runtime line is visible.
echo "Verifying pinned runtime line..."
if ! ${BINARY} version --verbose | grep -q "Pinned runtime line: 1.2.0"; then
  echo "ERROR: installed binary does not report pinned runtime line 1.2.0" >&2
  exit 1
fi
echo "  Pinned runtime line: 1.2.0 verified"
echo ""

# Verify the hardening fixes are advertised.
echo "Verifying hardening fixes..."
if ! ${BINARY} version --verbose | grep -q "Hardening fixes:"; then
  echo "ERROR: installed binary does not advertise hardening fixes" >&2
  exit 1
fi
${BINARY} version --verbose | grep "Hardening fixes:" | sed 's/^/  /'
echo ""

# Run the witness rework-deferred dry-run to prove the throttle is live.
echo "Running REWORK_DEFERRED throttle dry-run..."
if ! ${BINARY} witness rework-deferred dry-run; then
  echo "ERROR: REWORK_DEFERRED throttle dry-run failed" >&2
  exit 1
fi
echo ""

# Record durable cutover evidence.
mkdir -p "${RECORD_DIR}"
BUILD_COMMIT="$(${BINARY} version --verbose 2>/dev/null | grep -o '@[a-f0-9]*' | head -1 | tr -d '@')"
BUILD_TIME="$(${BINARY} version --verbose 2>/dev/null | grep -oE 'Timestamp: .*' | sed 's/Timestamp: //' || true)"
cat > "${RECORD_FILE}" <<EOF
{
  "cutover_at": "$(date -u +"%Y-%m-%dT%H:%M:%SZ")",
  "runtime_line": "1.2.0",
  "source_repo": "${REPO_ROOT}",
  "installed_binary": "${BINARY}",
  "build_commit": "${BUILD_COMMIT:-unknown}",
  "build_time": "${BUILD_TIME:-unknown}",
  "pre_version": $(printf '%s' "${PRE_VERSION}" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))' 2>/dev/null || echo '"<<unavailable>>"'),
  "post_features": "rework-deferred-throttle,hooked-polecats-working",
  "dry_run": "pass"
}
EOF

echo "=== Cutover complete ==="
echo "Evidence recorded: ${RECORD_FILE}"
echo ""
echo "Next steps:"
echo "  1. Restart any running witness / daemon if you want the new binary"
echo "     picked up immediately (this script uses safe-install, so the daemon"
echo "     was NOT restarted). Run: ${BINARY} daemon restart"
echo "     or for a single rig: ${BINARY} witness restart <rig>"
echo "  2. Verify the live witness uses the new binary: ${BINARY} witness status <rig>"
echo "  3. Inspect throttle records after the next patrol: ${BINARY} witness rework-deferred list"
