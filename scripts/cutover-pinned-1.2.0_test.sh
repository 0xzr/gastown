#!/usr/bin/env bash
# Regression test for the cutover-pinned-1.2.0.sh backup path.
#
# Mirrors the operational wrapper topology (public gt is a wrapper script,
# real ELF is gt-real-bin) and runs the cutover script with --dry-run from a
# directory that is NOT the install directory. Verifies that the rollback
# backup is created at the documented absolute path
# ($HOME/.local/bin/gt.before-pinned-1.2.0-cutover) rather than in the current
# working directory.
#
# This test is safe to run on a developer machine: it uses an isolated temp
# HOME and temp town root, and --dry-run prevents any real install.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CUTOVER_SCRIPT="${SCRIPT_DIR}/cutover-pinned-1.2.0.sh"

PASS=0
FAIL=0

pass() { echo "PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "FAIL: $1"; FAIL=$((FAIL + 1)); }

TMPDIR=""

cleanup() {
  if [ -n "${TMPDIR}" ] && [ -d "${TMPDIR}" ]; then
    rm -rf "${TMPDIR}"
  fi
}
trap cleanup EXIT

TMPDIR="$(mktemp -d)"
FAKE_HOME="${TMPDIR}/home"
FAKE_TOWN_ROOT="${TMPDIR}/town"
FAKE_INSTALL_DIR="${FAKE_HOME}/.local/bin"
FAKE_CWD="${TMPDIR}/some/other/workdir"
mkdir -p "${FAKE_INSTALL_DIR}" "${FAKE_CWD}" "${FAKE_TOWN_ROOT}"

# Build a fake operational wrapper. It must carry the marker recognized by
# scripts/lib/wrapper-preserve.sh so the cutover script treats the public
# path as the wrapper and backs up the real ELF at gt-real-bin.
cat > "${FAKE_INSTALL_DIR}/gt" <<'WRAPPER'
#!/usr/bin/env bash
# gt wrapper — guarantees the current validation model-mix on `gt sling`.
# This fake wrapper dispatches version/witness to the adjacent gt-real-bin.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REAL_BIN="${SCRIPT_DIR}/gt-real-bin"
if [ "${1:-}" = "version" ] && [ "${2:-}" = "--verbose" ]; then
  exec "${REAL_BIN}" version --verbose
fi
if [ "${1:-}" = "version" ]; then
  exec "${REAL_BIN}" version
fi
exec "${REAL_BIN}" "$@"
WRAPPER
chmod 0755 "${FAKE_INSTALL_DIR}/gt"

# Build the fake real ELF adjacent to the wrapper. It does not need to be a
# real ELF for this dry-run test; it only needs to respond to version.
cat > "${FAKE_INSTALL_DIR}/gt-real-bin" <<'ELF'
#!/usr/bin/env bash
if [ "${1:-}" = "version" ] && [ "${2:-}" = "--verbose" ]; then
  echo "gt version 1.1.9 (pre-cutover) @deadbeef"
  echo "Timestamp: 2026-06-25T00:00:00Z"
  exit 0
fi
if [ "${1:-}" = "version" ]; then
  echo "gt version 1.1.9"
  exit 0
fi
echo "Unknown command: $1" >&2
exit 1
ELF
chmod 0755 "${FAKE_INSTALL_DIR}/gt-real-bin"

EXPECTED_BACKUP="${FAKE_INSTALL_DIR}/gt.before-pinned-1.2.0-cutover"
EXPECTED_RECORD="${FAKE_TOWN_ROOT}/.runtime/pinned-1.2.0-cutover.json"

# Run the cutover from a directory that is not the install dir. A relative
# backup would land here; the fix must land next to the resolved gt binary.
set +e
HOME="${FAKE_HOME}" \
  GT_TOWN_ROOT="${FAKE_TOWN_ROOT}" \
  PATH="${FAKE_INSTALL_DIR}:${PATH}" \
  bash "${CUTOVER_SCRIPT}" --dry-run --skip-forward-check \
  > "${TMPDIR}/cutover.out" 2> "${TMPDIR}/cutover.err"
RC=$?
set -e

if [ "$RC" -eq 0 ]; then
  pass "cutover script exits 0 in dry-run"
else
  fail "cutover script exited $RC"
  echo "--- stdout ---"
  cat "${TMPDIR}/cutover.out"
  echo "--- stderr ---"
  cat "${TMPDIR}/cutover.err"
fi

if [ -f "${EXPECTED_BACKUP}" ]; then
  pass "backup created at documented absolute path: ${EXPECTED_BACKUP}"
else
  fail "backup missing at documented absolute path: ${EXPECTED_BACKUP}"
fi

if [ -f "${FAKE_CWD}/gt.before-pinned-1.2.0-cutover" ]; then
  fail "relative backup leaked into cwd: ${FAKE_CWD}/gt.before-pinned-1.2.0-cutover"
else
  pass "no relative backup leaked into cwd"
fi

if grep -qF "${EXPECTED_BACKUP}" "${EXPECTED_RECORD}"; then
  pass "evidence record contains absolute backup path"
else
  fail "evidence record missing absolute backup path"
  cat "${EXPECTED_RECORD}" || true
fi

if grep -q '"dry_run": "true"' "${EXPECTED_RECORD}"; then
  pass "evidence record marks dry_run as true"
else
  fail "evidence record does not mark dry_run as true"
  cat "${EXPECTED_RECORD}" || true
fi

echo ""
echo "Results: ${PASS} passed, ${FAIL} failed"
[ "${FAIL}" -eq 0 ]
