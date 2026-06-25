#!/usr/bin/env bash
# Roll back the live ~/.local/bin/gt binary to the pre-cutover binary recorded by
# scripts/cutover-pinned-1.2.0.sh.
#
# Usage:
#   scripts/rollback-pinned-1.2.0.sh [--evidence <path>]
#
# Post-conditions:
#   - The previously installed binary (the one active before the last
#     pinned-1.2.0 cutover) is restored.
#   - The evidence file is amended with a rollback_at record.
#   - gt version --verbose is probed and its output is recorded.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source the wrapper-preservation library so the rollback target matches what the
# cutover script would have replaced. The library provides gt_install_wrapper_path,
# gt_install_real_bin_path, and gt_install_is_wrapper.
# shellcheck source=lib/wrapper-preserve.sh
source "${SCRIPT_DIR}/lib/wrapper-preserve.sh"

TOWN_ROOT="${GT_TOWN_ROOT:-/home/ubuntu/gt-town}"
RECORD_DIR="${TOWN_ROOT}/.runtime"
RECORD_FILE="${RECORD_DIR}/pinned-1.2.0-cutover.json"

for arg in "$@"; do
  case "$arg" in
    --evidence) shift; RECORD_FILE="${1:-$RECORD_FILE}"; shift ;;
    --evidence=*) RECORD_FILE="${arg#*=}" ;;
    *) echo "Unknown argument: $arg" >&2; exit 1 ;;
  esac
done

WRAPPER_PATH="$(gt_install_wrapper_path)"
REAL_BIN_PATH="$(gt_install_real_bin_path)"

if [ ! -f "${RECORD_FILE}" ]; then
  echo "ERROR: cutover evidence file not found: ${RECORD_FILE}" >&2
  echo "       Cannot roll back without an evidence record from cutover-pinned-1.2.0.sh." >&2
  exit 1
fi

# Parse the evidence file. Prefer jq if available, but fall back to a simple
# Python one-liner so the script works on minimal hosts.
read_evidence() {
  local key="$1"
  if command -v jq >/dev/null 2>&1; then
    jq -r '."'"$key"'" // ""' "$RECORD_FILE"
  else
    python3 -c 'import json,sys; print(json.load(sys.stdin).get("'"$key"'", ""))' < "$RECORD_FILE"
  fi
}

BACKUP_BINARY="$(read_evidence backup_binary)"
INSTALLED_BINARY="$(read_evidence installed_binary)"
WRAPPER_TOPOLOGY="$(read_evidence wrapper_topology)"
PUBLIC_PATH="$(read_evidence public_path)"

# Fall back to library-resolved paths if the evidence file lacks them (older
# records or manual tests).
if [ -z "${INSTALLED_BINARY}" ]; then
  if gt_install_is_wrapper "${WRAPPER_PATH}"; then
    INSTALLED_BINARY="${REAL_BIN_PATH}"
    WRAPPER_TOPOLOGY="wrapper"
  else
    INSTALLED_BINARY="${WRAPPER_PATH}"
    WRAPPER_TOPOLOGY="plain"
  fi
fi
if [ -z "${BACKUP_BINARY}" ]; then
  BACKUP_BINARY="${INSTALLED_BINARY}.before-pinned-1.2.0-cutover"
fi

if [ ! -f "${BACKUP_BINARY}" ]; then
  echo "ERROR: rollback backup missing: ${BACKUP_BINARY}" >&2
  echo "       The cutover may not have created a backup, or it was removed." >&2
  exit 1
fi

echo "=== GT pinned 1.2.0 runtime rollback ==="
echo "Evidence:   ${RECORD_FILE}"
echo "Topology:   ${WRAPPER_TOPOLOGY}"
echo "Restore to: ${INSTALLED_BINARY}"
echo "Backup:     ${BACKUP_BINARY}"
echo ""

# Pre-rollback probe for the record.
PRE_VERSION="$(${INSTALLED_BINARY} version --verbose 2>/dev/null || ${WRAPPER_PATH} version --verbose 2>/dev/null || echo '<not installed>')"
echo "Current binary version:"
echo "${PRE_VERSION}" | sed 's/^/  /'
echo ""

# Safety check: the backup must look like an ELF so we do not restore garbage.
first_byte() {
  dd if="$1" bs=1 count=1 status=none 2>/dev/null | od -An -c | tr -d ' \n'
}
CASE=$(first_byte "${BACKUP_BINARY}")
case "$CASE" in
  177|E) ;;
  *)
    echo "ERROR: backup ${BACKUP_BINARY} does not appear to be an ELF binary" >&2
    echo "       Refusing to restore a non-binary file. Manual rollback: investigate ${BACKUP_BINARY}" >&2
    exit 1
    ;;
esac

# Confirm the backup is executable.
if [ ! -x "${BACKUP_BINARY}" ]; then
  echo "WARNING: backup ${BACKUP_BINARY} is not executable; chmod +x applied" >&2
  chmod +x "${BACKUP_BINARY}" || true
fi

# Atomic-ish restore: write to a temp sibling path, then rename over the target.
RESTORE_TMP="${INSTALLED_BINARY}.rollback-new.$$"
if ! cp "${BACKUP_BINARY}" "${RESTORE_TMP}"; then
  echo "ERROR: failed to stage rollback binary to ${RESTORE_TMP}" >&2
  exit 1
fi
chmod 0755 "${RESTORE_TMP}" || true
if ! mv "${RESTORE_TMP}" "${INSTALLED_BINARY}"; then
  echo "ERROR: failed to replace ${INSTALLED_BINARY} with rollback binary" >&2
  rm -f "${RESTORE_TMP}" 2>/dev/null || true
  exit 1
fi

# Post-rollback topology assertion.
if ! gt_install_assert_wrapper_topology; then
  echo "ERROR: post-rollback wrapper topology is broken" >&2
  exit 1
fi

# Post-rollback version probe. We deliberately do NOT require the version to
# equal 1.2.0 — the backup may legitimately be a different known-good build.
# The point is that it runs and reports a version.
POST_VERSION="$(${INSTALLED_BINARY} version --verbose 2>/dev/null || ${WRAPPER_PATH} version --verbose 2>/dev/null || echo '<probe failed>')"
echo "Restored binary version:"
echo "${POST_VERSION}" | sed 's/^/  /'
echo ""

# Amend the evidence file with rollback metadata. Preserve the original JSON and
# append a rollback object so incident reconstruction can read both directions.
ROLLBACK_AT="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
if command -v jq >/dev/null 2>&1; then
  jq --arg at "$ROLLBACK_AT" \
     --arg installed "$INSTALLED_BINARY" \
     --arg backup "$BACKUP_BINARY" \
     --arg topology "$WRAPPER_TOPOLOGY" \
     --arg pre_version "$PRE_VERSION" \
     --arg post_version "$POST_VERSION" \
     '.rollback = {
        rollback_at: $at,
        restored_binary: $installed,
        source_backup: $backup,
        wrapper_topology: $topology,
        pre_version: $pre_version,
        post_version: $post_version
      }' "$RECORD_FILE" > "${RECORD_FILE}.tmp" && mv "${RECORD_FILE}.tmp" "$RECORD_FILE"
else
  python3 - "$RECORD_FILE" "$ROLLBACK_AT" "$INSTALLED_BINARY" "$BACKUP_BINARY" "$WRAPPER_TOPOLOGY" "$PRE_VERSION" "$POST_VERSION" <<'PYEOF'
import json, sys
path, at, installed, backup, topology, pre, post = sys.argv[1:8]
with open(path, 'r') as f:
    data = json.load(f)
data['rollback'] = {
    'rollback_at': at,
    'restored_binary': installed,
    'source_backup': backup,
    'wrapper_topology': topology,
    'pre_version': pre,
    'post_version': post,
}
with open(path, 'w') as f:
    json.dump(data, f, indent=2)
    f.write('\n')
PYEOF
fi

echo "=== Rollback complete ==="
echo "Evidence updated: ${RECORD_FILE}"
echo ""
echo "Next steps:"
echo "  1. Restart any running witness / daemon to pick up the restored binary:"
echo "     ${WRAPPER_PATH} daemon restart"
echo "  2. Verify the live witness uses the restored binary: ${WRAPPER_PATH} witness status <rig>"
echo "  3. Run 'gt doctor' to confirm the wrapper-topology check is OK."
