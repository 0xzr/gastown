#!/usr/bin/env bash
# Cut over the live ~/.local/bin/gt binary to the latest main branch commit.
#
# This is the safe install path when the current source worktree is on a
# non-build branch (e.g., a polecat worktree) and `gt stale` refuses an
# automated rebuild. It builds from the main branch ref in the bare repo,
# installs the ELF behind the operational wrapper, and restarts the daemon
# so the running witness picks up the fixed binary.
#
# Usage:
#   scripts/cutover-main.sh [--dry-run] [--skip-forward-check] [--target-ref <ref>]
#
# Options:
#   --dry-run             Record evidence and run the forward-only check but
#                         do not build, install, or restart the daemon.
#   --skip-forward-check  Bypass the descendant-of-installed-binary guard.
#   --target-ref <ref>    Build from this ref instead of refs/heads/main.
#                         Useful when local main has the fix but origin/main
#                         is still behind (the default resolves main via the
#                         bare repo so local main is preferred).
#
# Environment:
#   CUTOVER_VERIFY_TIMEOUT (default: 30) — seconds the FINAL post-install
#       verification probes may run before they are treated as hangs and
#       routed to rollback. Requires GNU `timeout`.
#   GT_TOWN_ROOT (default: /home/ubuntu/gt-town) — town root for evidence.
#   GT_STALE_BINARY_MAX_DELTA — forwarded to the build if set.
#
# Post-conditions:
#   - ~/.local/bin/gt-real-bin reports the main branch commit.
#   - gt witness rework-deferred dry-run passes.
#   - gt witness rework-deferred live-dry-run passes.
#   - The daemon is restarted and running the new binary.
#   - Evidence is recorded in $GT_TOWN_ROOT/.runtime/main-cutover.json.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Source the wrapper-preservation library so the install is atomic and rollback-capable.
# shellcheck disable=SC1091
# shellcheck source=lib/wrapper-preserve.sh
source "${SCRIPT_DIR}/lib/wrapper-preserve.sh"

INSTALL_DIR="${HOME}/.local/bin"
BINARY="gt"
SKIP_FORWARD_CHECK=""
DRY_RUN=""
TARGET_REF="refs/heads/main"

while [ $# -gt 0 ]; do
  case "$1" in
    --skip-forward-check) SKIP_FORWARD_CHECK=1; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    --target-ref)
      if [ $# -lt 2 ]; then
        echo "ERROR: --target-ref requires a value" >&2
        exit 1
      fi
      TARGET_REF="$2"
      shift 2
      ;;
    --target-ref=*)
      TARGET_REF="${1#--target-ref=}"
      shift
      ;;
    *) echo "Unknown argument: $1" >&2; exit 1 ;;
  esac
done

TOWN_ROOT="${GT_TOWN_ROOT:-/home/ubuntu/gt-town}"
if [ ! -d "${TOWN_ROOT}" ]; then
  echo "ERROR: town root ${TOWN_ROOT} does not exist" >&2
  exit 1
fi

RECORD_DIR="${TOWN_ROOT}/.runtime"
RECORD_FILE="${RECORD_DIR}/main-cutover.json"

WRAPPER_PATH="$(gt_install_wrapper_path)"
REAL_BIN_PATH="$(gt_install_real_bin_path)"

GT_BIN_PATH="$(command -v gt 2>/dev/null || true)"
if [ -z "${GT_BIN_PATH}" ] || [ ! -e "${GT_BIN_PATH}" ]; then
  GT_BIN_PATH="${INSTALL_DIR}/${BINARY}"
fi
if [ ! -e "${GT_BIN_PATH}" ]; then
  echo "ERROR: cannot resolve gt binary on PATH or at ${GT_BIN_PATH}" >&2
  exit 1
fi
GT_BIN_DIR="$(cd "$(dirname "${GT_BIN_PATH}")" && pwd)"
BACKUP_BINARY="${GT_BIN_DIR}/${BINARY}.before-main-cutover"

# Resolve the bare repo from the current worktree so we can add a temp worktree
# from any branch without disturbing the current checked-out branch.
BARE_REPO="$(git -C "${REPO_ROOT}" rev-parse --git-common-dir 2>/dev/null || true)"
if [ -z "${BARE_REPO}" ]; then
  BARE_REPO="$(git -C "${REPO_ROOT}" rev-parse --git-dir 2>/dev/null || true)"
fi
if [ -z "${BARE_REPO}" ]; then
  echo "ERROR: cannot resolve git directory for ${REPO_ROOT}" >&2
  exit 1
fi

# Target ref must resolve in the bare repo.
TARGET_COMMIT="$(git -C "${BARE_REPO}" rev-parse --verify "${TARGET_REF}^{commit}" 2>/dev/null || true)"
if [ -z "${TARGET_COMMIT}" ]; then
  echo "ERROR: cannot resolve target ref ${TARGET_REF} in ${BARE_REPO}" >&2
  exit 1
fi

echo "=== GT main branch cutover ==="
echo "Repo:       ${REPO_ROOT}"
echo "Bare repo:  ${BARE_REPO}"
echo "Target:     ${TARGET_REF} (${TARGET_COMMIT})"
echo "Install:    ${WRAPPER_PATH} (wrapper) + ${REAL_BIN_PATH} (ELF)"
echo "Town root:  ${TOWN_ROOT}"
echo "Record:     ${RECORD_FILE}"
echo ""

POST_VERSION=""
POST_VERBOSE=""

# Forward-only guard: the target commit must be a descendant of the installed
# binary so we never install an older or diverged build.
if [ -z "${SKIP_FORWARD_CHECK:-}" ]; then
  PROBE="${REAL_BIN_PATH}"
  if [ ! -x "${PROBE}" ]; then
    PROBE="${WRAPPER_PATH}"
  fi
  INSTALLED_COMMIT="$(${PROBE} version --verbose 2>/dev/null | grep -o '@[a-f0-9]\{7,\}' | head -1 | tr -d '@' || true)"
  if [ -z "${INSTALLED_COMMIT}" ]; then
    echo "ERROR: cannot determine installed binary commit (no '@<commit>' token in ${PROBE} version --verbose)" >&2
    echo "  Override explicitly with --skip-forward-check (dangerous)." >&2
    exit 1
  fi
  if ! git -C "${BARE_REPO}" merge-base --is-ancestor "${INSTALLED_COMMIT}" "${TARGET_COMMIT}" 2>/dev/null; then
    echo "ERROR: target ${TARGET_COMMIT} is not a descendant of installed binary ${INSTALLED_COMMIT}" >&2
    echo "Refusing to install an older or diverged build." >&2
    echo "Use --skip-forward-check to override (dangerous)." >&2
    exit 1
  fi
  echo "Forward-only check passed: ${INSTALLED_COMMIT} -> ${TARGET_COMMIT}"
else
  echo "Skipping forward-only check (--skip-forward-check)"
fi

PRE_VERSION="$(${REAL_BIN_PATH} version --verbose 2>/dev/null || ${WRAPPER_PATH} version --verbose 2>/dev/null || echo '<not installed>')"
echo "Pre-cutover binary version:"
# shellcheck disable=SC2001
printf '%s\n' "${PRE_VERSION}" | sed 's/^/  /'
echo ""

if [ -f "${REAL_BIN_PATH}" ]; then
  cp "${REAL_BIN_PATH}" "${BACKUP_BINARY}"
  echo "Backed up current binary to: ${BACKUP_BINARY}"
  echo ""
fi

if [ -n "${DRY_RUN:-}" ]; then
  echo "=== DRY RUN: skipping build, install, and restart ==="
  echo ""
else
  # Build from a temporary worktree rooted at the target commit. This avoids
  # switching the current worktree's branch and lets the cutover run from a
  # polecat or feature worktree safely.
  TMP_WORKTREE=""
  cleanup_worktree() {
    if [ -n "${TMP_WORKTREE}" ] && [ -d "${TMP_WORKTREE}" ]; then
      git -C "${BARE_REPO}" worktree remove --force "${TMP_WORKTREE}" 2>/dev/null || true
      rm -rf "${TMP_WORKTREE}" 2>/dev/null || true
    fi
  }
  trap cleanup_worktree EXIT

  TMP_WORKTREE="$(mktemp -d -t gt-main-cutover-XXXXXX)"
  echo "Creating temporary worktree at ${TMP_WORKTREE} ..."
  git -C "${BARE_REPO}" worktree add --detach "${TMP_WORKTREE}" "${TARGET_COMMIT}" >/dev/null 2>&1
  echo "Built from ${TARGET_REF} (${TARGET_COMMIT}) in temporary worktree"
  echo ""

  cutover_rollback() {
    local reason="$1"
    echo "ERROR: ${reason}" >&2
    if [ ! -f "${BACKUP_BINARY}" ]; then
      echo "ERROR: no pre-cutover backup at ${BACKUP_BINARY}; cannot auto-rollback" >&2
      exit 1
    fi
    echo "Auto-rolling back to pre-cutover binary: ${BACKUP_BINARY}" >&2
    if INSTALL_DIR="${INSTALL_DIR}" BINARY="${BINARY}" \
       GT_REAL_BIN="${REAL_BIN_PATH}" \
       bash -c 'source "$0"; gt_install_rollback "$1"' \
       "${SCRIPT_DIR}/lib/wrapper-preserve.sh" "${BACKUP_BINARY}" >&2; then
      echo "Rollback complete: ${REAL_BIN_PATH} restored from ${BACKUP_BINARY}" >&2
      exit 1
    fi
    echo "ERROR: locked/canaried rollback failed; manual recovery required" >&2
    exit 1
  }

  CUTOVER_VERIFY_TIMEOUT="${CUTOVER_VERIFY_TIMEOUT:-30}"
  case "${CUTOVER_VERIFY_TIMEOUT}" in
    ''|*[!0-9]*)
      cutover_rollback "invalid CUTOVER_VERIFY_TIMEOUT='${CUTOVER_VERIFY_TIMEOUT}'; refusing unbounded post-install verification"
      ;;
    0)
      cutover_rollback "CUTOVER_VERIFY_TIMEOUT=0 disables the bound; refusing unbounded post-install verification"
      ;;
  esac
  if ! command -v timeout >/dev/null 2>&1; then
    cutover_rollback "cannot bound post-install verification (GNU timeout missing); refusing unbounded verify"
  fi

  echo "Building gt from temporary worktree ..."
  (
    cd "${TMP_WORKTREE}"
    # A detached temp worktree cannot supply VCS info to Go, so disable
    # build stamping rather than letting `go build` fail with VCS status errors.
    GOFLAGS="${GOFLAGS:+$GOFLAGS }-buildvcs=false" make build
  ) || cutover_rollback "build failed in temporary worktree"
  echo ""

  # Verify the source we built contains the gastown-okmd0 source fixes. A
  # cutover-only MR that does not change the live emitter/canonicalization/
  # persistence path is insufficient (Mayor rejection of gastown-wisp-cqu).
  echo "Verifying source contains REWORK_DEFERRED live-path guards ..."
  if ! grep -q "IsReworkDeferredTestTuple" "${TMP_WORKTREE}/internal/witness/rework_deferred_throttle.go"; then
    cutover_rollback "target source missing REWORK_DEFERRED test-tuple guard (gastown-okmd0 source work not present)"
  fi
  if ! grep -q "beadStatusPreventsReworkBlockedNotice" "${TMP_WORKTREE}/internal/witness/handlers.go"; then
    cutover_rollback "target source missing stale closed-bead guard (gastown-okmd0 source work not present)"
  fi
  echo "  Source fixes present."
  echo ""

  echo "Installing new ELF behind wrapper ..."
  if ! gt_install_preserve_wrapper "${TMP_WORKTREE}/gt"; then
    cutover_rollback "install failed (wrapper-preserve refused the candidate)"
  fi
  if ! gt_install_assert_wrapper_topology; then
    cutover_rollback "post-install wrapper topology is broken"
  fi
  echo ""

  echo "Verifying installed binary version ..."
  if ! POST_VERSION="$(timeout "${CUTOVER_VERIFY_TIMEOUT}" "${REAL_BIN_PATH}" version 2>&1)"; then
    printf '%s\n' "${POST_VERSION}" >&2
    cutover_rollback "installed binary version probe failed/timed out after ${CUTOVER_VERIFY_TIMEOUT}s"
  fi
  printf '%s\n' "${POST_VERSION}" | sed 's/^/  /'
  EXPECTED_SHORT="$(git -C "${BARE_REPO}" rev-parse --short "${TARGET_COMMIT}")"
  if ! printf '%s\n' "${POST_VERSION}" | grep -qE "@${TARGET_COMMIT}|@${EXPECTED_SHORT}"; then
    cutover_rollback "installed binary does not report target commit ${TARGET_COMMIT}"
  fi
  echo ""

  echo "Restarting daemon to pick up new binary ..."
  if "${WRAPPER_PATH}" daemon status >/dev/null 2>&1; then
    "${WRAPPER_PATH}" daemon stop >/dev/null 2>&1 || true
    sleep 1
    if ! "${WRAPPER_PATH}" daemon start >/dev/null 2>&1; then
      cutover_rollback "daemon restart failed after install"
    fi
    echo "Daemon restarted."
  else
    echo "No daemon currently running; new binary will be used on next start."
  fi
  echo ""

  echo "Running REWORK_DEFERRED throttle dry-run (timeout ${CUTOVER_VERIFY_TIMEOUT}s) ..."
  if ! timeout "${CUTOVER_VERIFY_TIMEOUT}" "${WRAPPER_PATH}" witness rework-deferred dry-run; then
    cutover_rollback "REWORK_DEFERRED throttle dry-run failed/timed out after install"
  fi
  echo ""

  echo "Running REWORK_DEFERRED throttle live-dry-run (timeout ${CUTOVER_VERIFY_TIMEOUT}s) ..."
  if ! timeout "${CUTOVER_VERIFY_TIMEOUT}" "${WRAPPER_PATH}" witness rework-deferred live-dry-run; then
    cutover_rollback "REWORK_DEFERRED throttle live-dry-run failed/timed out after install"
  fi
  echo ""

  echo "Checking daemon status after restart ..."
  if ! timeout "${CUTOVER_VERIFY_TIMEOUT}" "${WRAPPER_PATH}" daemon status; then
    cutover_rollback "daemon status probe failed/timed out after restart"
  fi
  echo ""
fi

# Record durable cutover evidence.
mkdir -p "${RECORD_DIR}"
BUILD_TIME="$(printf '%s\n' "${POST_VERBOSE:-}" | grep -oE 'Timestamp: .*' | sed 's/Timestamp: //' || true)"
WRAPPER_TOPOLOGY="wrapper"
if ! gt_install_is_wrapper "${WRAPPER_PATH}"; then
  WRAPPER_TOPOLOGY="plain"
fi
if [ -n "${DRY_RUN:-}" ]; then
  DRY_RUN_VALUE="true"
  BUILD_TIME=""
else
  DRY_RUN_VALUE="false"
fi
cat > "${RECORD_FILE}" <<EOF
{
  "cutover_at": "$(date -u +"%Y-%m-%dT%H:%M:%SZ")",
  "runtime_line": "main",
  "source_repo": "${REPO_ROOT}",
  "bare_repo": "${BARE_REPO}",
  "target_ref": "${TARGET_REF}",
  "target_commit": "${TARGET_COMMIT}",
  "installed_binary": "${REAL_BIN_PATH}",
  "public_path": "${WRAPPER_PATH}",
  "wrapper_topology": "${WRAPPER_TOPOLOGY}",
  "backup_binary": "${BACKUP_BINARY}",
  "build_time": "${BUILD_TIME:-unknown}",
  "pre_version": $(printf '%s' "${PRE_VERSION}" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))' 2>/dev/null || echo '"<<unavailable>>"'),
  "dry_run": "${DRY_RUN_VALUE}"
}
EOF

if [ -n "${DRY_RUN:-}" ]; then
  echo "=== Cutover dry-run complete ==="
else
  echo "=== Cutover complete ==="
fi
echo "Evidence recorded: ${RECORD_FILE}"
echo "Rollback binary:    ${BACKUP_BINARY}"
if [ -n "${DRY_RUN:-}" ]; then
  echo ""
  echo "This was a dry run: no new binary was built or installed."
fi
echo ""
echo "To roll back to the pre-cutover binary:"
echo "  cp '${BACKUP_BINARY}' '${REAL_BIN_PATH}'"
echo "  # or use the wrapper-preservation rollback:"
echo "  source '${SCRIPT_DIR}/lib/wrapper-preserve.sh' && GT_REAL_BIN='${REAL_BIN_PATH}' gt_install_rollback '${BACKUP_BINARY}'"
