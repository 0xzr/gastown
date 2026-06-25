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

# Source the wrapper-preservation library so that `make safe-install` (which
# this script invokes) installs the ELF behind any operational wrapper at
# ~/.local/bin/gt instead of clobbering it. See scripts/lib/wrapper-preserve.sh
# for the contract; see gastown-cet.16.1 for the incident that motivated it.
# shellcheck source=lib/wrapper-preserve.sh
source "${SCRIPT_DIR}/lib/wrapper-preserve.sh"

INSTALL_DIR="${HOME}/.local/bin"
BINARY="gt"
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

# Resolve paths once for the whole script. The cutover must respect the
# wrapper topology: the public path is the wrapper (a script) and the real
# ELF lives at gt-real-bin (or whatever GT_REAL_BIN points at).
WRAPPER_PATH="$(gt_install_wrapper_path)"
REAL_BIN_PATH="$(gt_install_real_bin_path)"

echo "=== GT pinned 1.2.0 runtime cutover ==="
echo "Repo:      ${REPO_ROOT}"
echo "Install:   ${WRAPPER_PATH} (wrapper) + ${REAL_BIN_PATH} (ELF)"
echo "Town root: ${TOWN_ROOT}"
echo "Record:    ${RECORD_FILE}"
echo ""

# Verify the current source tree is on a descendant of the installed binary.
# The forward-only check must read the version from the REAL ELF, not the
# wrapper, because the wrapper does not carry version metadata.
if [ -z "${SKIP_FORWARD_CHECK:-}" ]; then
  PROBE="${REAL_BIN_PATH}"
  if [ ! -x "${PROBE}" ]; then
    PROBE="${WRAPPER_PATH}"
  fi
  INSTALLED_COMMIT="$(${PROBE} version --verbose 2>/dev/null | grep -o '@[a-f0-9]*' | head -1 | tr -d '@' || true)"
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
PRE_VERSION="$(${REAL_BIN_PATH} version --verbose 2>/dev/null || ${WRAPPER_PATH} version --verbose 2>/dev/null || echo '<not installed>')"
echo "Pre-cutover binary version:"
echo "${PRE_VERSION}" | sed 's/^/  /'
echo ""

# Build the pinned 1.2.0 runtime binary from current source. The Makefile
# safe-install target honors scripts/lib/wrapper-preserve.sh: if the public
# path is the operational wrapper, the ELF is installed behind it as
# gt-real-bin instead of clobbering the wrapper.
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

# Post-cutover invariant: the public path is still a wrapper (text), and the
# real binary slot has the freshly built ELF. This is what the doctor
# wrapper-topology check would catch; we fail fast here too because the
# wrapper-aware safe-install is what made it survive.
echo "Verifying wrapper topology..."
if ! gt_install_assert_wrapper_topology; then
  echo "ERROR: post-cutover wrapper topology is broken" >&2
  exit 1
fi
if [ ! -x "${REAL_BIN_PATH}" ]; then
  echo "ERROR: ${REAL_BIN_PATH} missing or not executable after cutover" >&2
  exit 1
fi
echo "  Wrapper at ${WRAPPER_PATH}: preserved (text script)"
echo "  ELF at ${REAL_BIN_PATH}: present and executable"
echo ""

# Verify the installed binary reports the approved version. Probe the real
# ELF so we get accurate version metadata rather than the wrapper's pass-through.
echo "Verifying installed binary version..."
if ! ${REAL_BIN_PATH} version | grep -q "gt version 1.2.0"; then
  echo "ERROR: installed binary does not report version 1.2.0" >&2
  ${REAL_BIN_PATH} version >&2
  exit 1
fi
${REAL_BIN_PATH} version --verbose | sed 's/^/  /'
echo ""

# Verify pinned runtime line is visible.
echo "Verifying pinned runtime line..."
if ! ${REAL_BIN_PATH} version --verbose | grep -q "Pinned runtime line: 1.2.0"; then
  echo "ERROR: installed binary does not report pinned runtime line 1.2.0" >&2
  exit 1
fi
echo "  Pinned runtime line: 1.2.0 verified"
echo ""

# Verify the hardening fixes are advertised.
echo "Verifying hardening fixes..."
if ! ${REAL_BIN_PATH} version --verbose | grep -q "Hardening fixes:"; then
  echo "ERROR: installed binary does not advertise hardening fixes" >&2
  exit 1
fi
${REAL_BIN_PATH} version --verbose | grep "Hardening fixes:" | sed 's/^/  /'
echo ""

# Run the witness rework-deferred dry-run to prove the throttle is live.
# Probe the public wrapper so we exercise the full PATH-mediated dispatch,
# which catches cases where the wrapper has been broken even though the ELF
# looks healthy.
echo "Running REWORK_DEFERRED throttle dry-run..."
if ! ${WRAPPER_PATH} witness rework-deferred dry-run; then
  echo "ERROR: REWORK_DEFERRED throttle dry-run failed (wrapper path)" >&2
  echo "  Real binary probe follows for diagnostics:" >&2
  ${REAL_BIN_PATH} witness rework-deferred dry-run >&2 || true
  exit 1
fi
echo ""

# Record durable cutover evidence. Probe the real ELF for commit/build_time
# metadata; record both paths so a post-incident reader can tell whether the
# wrapper topology was preserved.
mkdir -p "${RECORD_DIR}"
BUILD_COMMIT="$(${REAL_BIN_PATH} version --verbose 2>/dev/null | grep -o '@[a-f0-9]*' | head -1 | tr -d '@')"
BUILD_TIME="$(${REAL_BIN_PATH} version --verbose 2>/dev/null | grep -oE 'Timestamp: .*' | sed 's/Timestamp: //' || true)"
WRAPPER_TOPOLOGY="wrapper"
if ! gt_install_is_wrapper "${WRAPPER_PATH}"; then
  WRAPPER_TOPOLOGY="plain"
fi
cat > "${RECORD_FILE}" <<EOF
{
  "cutover_at": "$(date -u +"%Y-%m-%dT%H:%M:%SZ")",
  "runtime_line": "1.2.0",
  "source_repo": "${REPO_ROOT}",
  "installed_binary": "${REAL_BIN_PATH}",
  "public_path": "${WRAPPER_PATH}",
  "wrapper_topology": "${WRAPPER_TOPOLOGY}",
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
echo "     was NOT restarted). Run: ${WRAPPER_PATH} daemon restart"
echo "     or for a single rig: ${WRAPPER_PATH} witness restart <rig>"
echo "  2. Verify the live witness uses the new binary: ${WRAPPER_PATH} witness status <rig>"
echo "  3. Inspect throttle records after the next patrol: ${WRAPPER_PATH} witness rework-deferred list"
echo "  4. Run 'gt doctor' to confirm the wrapper-topology check is OK."
