#!/usr/bin/env bash
# Cut over the live ~/.local/bin/gt binary to the pinned Gas Town 1.2.0 runtime
# line while carrying the merged hardening fixes from origin/main.
#
# This script builds gt with Version=1.2.0, marks the binary as part of the
# operator-approved pinned runtime line, installs it safely, and verifies that
# the new binary contains the REWORK_DEFERRED throttle before finishing.
#
# Usage:
#   scripts/cutover-pinned-1.2.0.sh [--skip-forward-check] [--dry-run]
#
# Options:
#   --skip-forward-check  Bypass the descendant-of-installed-binary guard.
#   --dry-run             Perform the backup and record evidence but do not
#                         build or install a new binary. Useful for tests and
#                         for verifying backup topology before a real cutover.
#
# Environment:
#   CUTOVER_VERIFY_TIMEOUT (default: 30) — seconds the FINAL post-install
#       verification probes may run before they are treated as hangs and
#       routed to rollback. The verify is bounded or it does not run: a
#       candidate that hangs here would otherwise never reach rollback and
#       leave the bad binary live (gastown-cet.12.9). Requires GNU `timeout`;
#       if it is missing the verify refuses to run and rolls back.
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
DRY_RUN=""

for arg in "$@"; do
  case "$arg" in
    --skip-forward-check) SKIP_FORWARD_CHECK=1 ;;
    --dry-run) DRY_RUN=1 ;;
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

# Anchor the rollback backup to the directory of the live gt binary rather
# than the current working directory. This matches the documented rollback
# path (~/.local/bin/gt.before-pinned-1.2.0-cutover) and prevents the backup
# from being created wherever the operator happened to run the script.
GT_BIN_PATH="$(command -v gt 2>/dev/null || true)"
if [ -z "${GT_BIN_PATH}" ] || [ ! -e "${GT_BIN_PATH}" ]; then
  # If gt is not currently on PATH (e.g. an operator ran the script from a
  # shell without ~/.local/bin), fall back to the documented conventional path.
  GT_BIN_PATH="${INSTALL_DIR}/${BINARY}"
fi
if [ ! -e "${GT_BIN_PATH}" ]; then
  echo "ERROR: cannot resolve gt binary on PATH or at ${GT_BIN_PATH}" >&2
  exit 1
fi
GT_BIN_DIR="$(cd "$(dirname "${GT_BIN_PATH}")" && pwd)"
BACKUP_BINARY="${GT_BIN_DIR}/${BINARY}.before-pinned-1.2.0-cutover"

echo "=== GT pinned 1.2.0 runtime cutover ==="
echo "Repo:      ${REPO_ROOT}"
echo "Install:   ${WRAPPER_PATH} (wrapper) + ${REAL_BIN_PATH} (ELF)"
echo "Town root: ${TOWN_ROOT}"
echo "Record:    ${RECORD_FILE}"
echo ""

POST_VERSION=""
POST_VERBOSE=""

# Verify the current source tree is on a descendant of the installed binary.
# The forward-only check must read the version from the REAL ELF, not the
# wrapper, because the wrapper does not carry version metadata.
#
# When the installed binary does not advertise a commit token (no '@<commit>'
# in version --verbose) there is no anchor to validate against. Rather than
# silently skip with a warning — which would let a downgrade or wrong-version
# cutover proceed — we FAIL FAST. The operator must explicitly opt in with
# --skip-forward-check (gastown-cet.12.11).
if [ -z "${SKIP_FORWARD_CHECK:-}" ]; then
  PROBE="${REAL_BIN_PATH}"
  if [ ! -x "${PROBE}" ]; then
    PROBE="${WRAPPER_PATH}"
  fi
  INSTALLED_COMMIT="$(${PROBE} version --verbose 2>/dev/null | grep -o '@[a-f0-9]*' | head -1 | tr -d '@' || true)"
  if [ -z "${INSTALLED_COMMIT}" ]; then
    echo "ERROR: cannot determine installed binary commit (no '@<commit>' token in ${PROBE} version --verbose)" >&2
    echo "  Refusing to silently skip the forward-only check — an unanchored" >&2
    echo "  cutover to an older or diverged commit would re-trigger the crash" >&2
    echo "  loop this guard exists to prevent (gastown-cet.12.11)." >&2
    echo "  Build the new binary first so it carries a version commit, or" >&2
    echo "  override explicitly with --skip-forward-check (dangerous)." >&2
    exit 1
  fi
  HEAD_COMMIT="$(git -C "${REPO_ROOT}" rev-parse HEAD)"
  if ! git -C "${REPO_ROOT}" merge-base --is-ancestor "${INSTALLED_COMMIT}" "${HEAD_COMMIT}" 2>/dev/null; then
    echo "ERROR: HEAD ${HEAD_COMMIT} is not a descendant of installed binary ${INSTALLED_COMMIT}" >&2
    echo "Refusing to cut over to an older or diverged commit." >&2
    echo "Use --skip-forward-check to override (dangerous)." >&2
    exit 1
  fi
  echo "Forward-only check passed: ${INSTALLED_COMMIT} -> ${HEAD_COMMIT}"
else
  echo "Skipping forward-only check (--skip-forward-check)"
fi

# Record pre-cutover evidence.
PRE_VERSION="$(${REAL_BIN_PATH} version --verbose 2>/dev/null || ${WRAPPER_PATH} version --verbose 2>/dev/null || echo '<not installed>')"
echo "Pre-cutover binary version:"
echo "${PRE_VERSION}" | sed 's/^/  /'
echo ""

# Keep a rollback-capable backup of the currently installed binary so a bad
# cutover can be undone. Back up the real ELF (not the wrapper) since that is
# what safe-install will replace behind the wrapper.
if [ -f "${REAL_BIN_PATH}" ]; then
  cp "${REAL_BIN_PATH}" "${BACKUP_BINARY}"
  echo "Backed up current binary to: ${BACKUP_BINARY}"
  echo ""
fi

if [ -n "${DRY_RUN:-}" ]; then
  echo "=== DRY RUN: skipping build, install, and post-cutover verification ==="
  echo ""
else
  # Build the pinned 1.2.0 runtime binary from current source. The Makefile
  # safe-install target honors scripts/lib/wrapper-preserve.sh: if the public
  # path is the operational wrapper, the ELF is installed behind it as
  # gt-real-bin instead of clobbering the wrapper. The install is now atomic,
  # canary-gated, and flock-serialized (gastown-cet.12.9).
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

  # Auto-rollback on failed verification (gastown-cet.12.9). If ANY post-
  # cutover check below fails, restore the pre-cutover binary before exiting
  # so a bad pinned build never stays live — the failure mode that killed all
  # polecats at once. safe-install already snapshotted the previous real-bin
  # to gt-real-bin.bak.<ts>; BACKUP_BINARY is the same ELF captured here.
  #
  # Rollback is funnelled EXCLUSIVELY through the library's
  # gt_install_rollback, which is flock-serialized, canary-checked, itself
  # snapshotable (so the rollback is reversible), and atomic (rename). The
  # prior version fell back to a direct `cp` over the live real-bin if the
  # library rollback failed — that bypassed the lock, the rollback canary,
  # and the atomic rename, and could race an installer or restore bytes the
  # health gate had already rejected. There is NO direct-copy fallback now:
  # if the locked/canaried/atomic restore fails, we fail closed with a clear
  # error and preserve evidence rather than copy over the live slot blind
  # (gastown-cet.12.9 rework, codex finding #3).
  cutover_rollback() {
    local reason="$1"
    echo "ERROR: ${reason}" >&2
    if [ ! -f "${BACKUP_BINARY}" ]; then
      echo "ERROR: no pre-cutover backup at ${BACKUP_BINARY}; cannot auto-rollback" >&2
      echo "  the live binary at ${REAL_BIN_PATH} was NOT modified — investigate manually" >&2
      echo "  evidence: post-cutover binary left in place for inspection" >&2
      exit 1
    fi
    echo "Auto-rolling back to pre-cutover binary: ${BACKUP_BINARY}" >&2
    # Restore via the library so the rollback is flock-serialized, canary-
    # checked, snapshotable, and atomic — the same path as the install.
    if INSTALL_DIR="${INSTALL_DIR}" BINARY="${BINARY}" \
       GT_REAL_BIN="${REAL_BIN_PATH}" \
       bash -c 'source "$0"; gt_install_rollback "$1"' \
       "${SCRIPT_DIR}/lib/wrapper-preserve.sh" "${BACKUP_BINARY}" >&2; then
      echo "Rollback complete: ${REAL_BIN_PATH} restored from ${BACKUP_BINARY}" >&2
      exit 1
    fi
    # Fail closed: do NOT direct-copy over the live real-bin. The locked,
    # canaried, atomic restore refused — copying blind now could race an
    # installer or restore bytes the health gate rejected. Preserve evidence
    # and surface the failure for manual recovery (codex finding #3).
    echo "ERROR: locked/canaried rollback failed; refusing to direct-copy over live ${REAL_BIN_PATH}" >&2
    echo "  the bad post-cutover binary at ${REAL_BIN_PATH} was NOT overwritten" >&2
    echo "  manual recovery: inspect ${REAL_BIN_PATH} and ${BACKUP_BINARY}, then run" >&2
    echo "    source '${SCRIPT_DIR}/lib/wrapper-preserve.sh' && GT_REAL_BIN='${REAL_BIN_PATH}' gt_install_rollback '${BACKUP_BINARY}'" >&2
    exit 1
  }

  # Every post-install execution of the newly installed binary must be bounded
  # before it can run. If GNU timeout is missing, or the operator supplies a
  # non-positive/invalid timeout, fail closed into rollback instead of probing
  # the new binary unbounded.
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
    echo "ERROR: GNU timeout unavailable; cannot bound post-install verification" >&2
    echo "  refusing to run unbounded probes that could strand a bad binary live" >&2
    echo "  install coreutils before retrying" >&2
    cutover_rollback "cannot bound post-install verification (GNU timeout missing); refusing unbounded verify"
  fi

  # Post-cutover invariant: the public path is still a wrapper (text), and the
  # real binary slot has the freshly built ELF. This is what the doctor
  # wrapper-topology check would catch; we fail fast here too because the
  # wrapper-aware safe-install is what made it survive.
  echo "Verifying wrapper topology..."
  if ! gt_install_assert_wrapper_topology; then
    cutover_rollback "post-cutover wrapper topology is broken"
  fi
  if [ ! -x "${REAL_BIN_PATH}" ]; then
    cutover_rollback "${REAL_BIN_PATH} missing or not executable after cutover"
  fi
  echo "  Wrapper at ${WRAPPER_PATH}: preserved (text script)"
  echo "  ELF at ${REAL_BIN_PATH}: present and executable"
  echo ""

  # Verify the installed binary reports the approved version. Probe the real
  # ELF so we get accurate version metadata rather than the wrapper's pass-through.
  echo "Verifying installed binary version..."
  if ! POST_VERSION="$(timeout "${CUTOVER_VERIFY_TIMEOUT}" "${REAL_BIN_PATH}" version 2>&1)"; then
    printf '%s\n' "${POST_VERSION}" >&2
    cutover_rollback "installed binary version probe failed/timed out after ${CUTOVER_VERIFY_TIMEOUT}s"
  fi
  if ! printf '%s\n' "${POST_VERSION}" | grep -q "gt version 1.2.0"; then
    printf '%s\n' "${POST_VERSION}" >&2
    cutover_rollback "installed binary does not report version 1.2.0"
  fi
  if ! POST_VERBOSE="$(timeout "${CUTOVER_VERIFY_TIMEOUT}" "${REAL_BIN_PATH}" version --verbose 2>&1)"; then
    printf '%s\n' "${POST_VERBOSE}" >&2
    cutover_rollback "installed binary verbose version probe failed/timed out after ${CUTOVER_VERIFY_TIMEOUT}s"
  fi
  printf '%s\n' "${POST_VERBOSE}" | sed 's/^/  /'
  echo ""

  # Verify pinned runtime line is visible.
  echo "Verifying pinned runtime line..."
  if ! printf '%s\n' "${POST_VERBOSE}" | grep -q "Pinned runtime line: 1.2.0"; then
    cutover_rollback "installed binary does not report pinned runtime line 1.2.0"
  fi
  echo "  Pinned runtime line: 1.2.0 verified"
  echo ""

  # Verify the hardening fixes are advertised.
  echo "Verifying hardening fixes..."
  if ! printf '%s\n' "${POST_VERBOSE}" | grep -q "Hardening fixes:"; then
    cutover_rollback "installed binary does not advertise hardening fixes"
  fi
  printf '%s\n' "${POST_VERBOSE}" | grep "Hardening fixes:" | sed 's/^/  /'
  echo ""

  # Run the witness rework-deferred dry-run to prove the throttle is live.
  # Probe the public wrapper so we exercise the full PATH-mediated dispatch,
  # which catches cases where the wrapper has been broken even though the ELF
  # looks healthy. This is the FINAL post-install verification: if it fails
  # after `make safe-install` already installed the new binary, we MUST roll
  # back to the pre-cutover binary rather than `exit 1`, otherwise a bad
  # pinned build is left live — exactly the failure mode this script guards
  # against (gastown-cet.12.9 rework: codex finding #1).
  #
  # This verify MUST be bounded: a candidate can pass the pre-install canary
  # yet hang during this post-install dry-run. An UNBOUNDED verify would never
  # reach cutover_rollback and would leave the bad binary live. The real-binary
  # diagnostic probe below is bounded the same way for the same reason.
  echo "Running REWORK_DEFERRED throttle dry-run (timeout ${CUTOVER_VERIFY_TIMEOUT}s)..."
  if ! timeout "${CUTOVER_VERIFY_TIMEOUT}" "${WRAPPER_PATH}" witness rework-deferred dry-run; then
    echo "ERROR: REWORK_DEFERRED throttle dry-run failed or timed out (wrapper path)" >&2
    echo "  Real binary probe follows for diagnostics:" >&2
    timeout "${CUTOVER_VERIFY_TIMEOUT}" "${REAL_BIN_PATH}" witness rework-deferred dry-run >&2 || true
    cutover_rollback "REWORK_DEFERRED throttle dry-run failed/timed out after install; rolling back to pre-cutover binary"
  fi
  echo ""
fi

# Record durable cutover evidence. Probe the real ELF for commit/build_time
# metadata; record both paths so a post-incident reader can tell whether the
# wrapper topology was preserved.
mkdir -p "${RECORD_DIR}"
BUILD_COMMIT="$(printf '%s\n' "${POST_VERBOSE}" | grep -o '@[a-f0-9]*' | head -1 | tr -d '@' || true)"
BUILD_TIME="$(printf '%s\n' "${POST_VERBOSE}" | grep -oE 'Timestamp: .*' | sed 's/Timestamp: //' || true)"
WRAPPER_TOPOLOGY="wrapper"
if ! gt_install_is_wrapper "${WRAPPER_PATH}"; then
  WRAPPER_TOPOLOGY="plain"
fi
if [ -n "${DRY_RUN:-}" ]; then
  DRY_RUN_VALUE="true"
  BUILD_COMMIT=""
  BUILD_TIME=""
else
  DRY_RUN_VALUE="pass"
fi
cat > "${RECORD_FILE}" <<EOF
{
  "cutover_at": "$(date -u +"%Y-%m-%dT%H:%M:%SZ")",
  "runtime_line": "1.2.0",
  "source_repo": "${REPO_ROOT}",
  "installed_binary": "${REAL_BIN_PATH}",
  "public_path": "${WRAPPER_PATH}",
  "wrapper_topology": "${WRAPPER_TOPOLOGY}",
  "backup_binary": "${BACKUP_BINARY}",
  "build_commit": "${BUILD_COMMIT:-unknown}",
  "build_time": "${BUILD_TIME:-unknown}",
  "pre_version": $(printf '%s' "${PRE_VERSION}" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))' 2>/dev/null || echo '"<<unavailable>>"'),
  "post_features": "rework-deferred-throttle,hooked-polecats-working",
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
if [ -z "${DRY_RUN:-}" ]; then
  echo "Next steps:"
  echo "  1. Restart any running witness / daemon if you want the new binary"
  echo "     picked up immediately (this script uses safe-install, so the daemon"
  echo "     was NOT restarted). Run: ${WRAPPER_PATH} daemon restart"
  echo "     or for a single rig: ${WRAPPER_PATH} witness restart <rig>"
  echo "  2. Verify the live witness uses the new binary: ${WRAPPER_PATH} witness status <rig>"
  echo "  3. Inspect throttle records after the next patrol: ${WRAPPER_PATH} witness rework-deferred list"
  echo "  4. Run 'gt doctor' to confirm the wrapper-topology check is OK."
  echo ""
fi
echo "To roll back to the pre-cutover binary:"
echo "  cp '${BACKUP_BINARY}' '${REAL_BIN_PATH}'"
echo "  # or, the one-command rollback (restores the newest gt-real-bin.bak.*):"
echo "  source '${SCRIPT_DIR}/lib/wrapper-preserve.sh' && gt_install_rollback"
