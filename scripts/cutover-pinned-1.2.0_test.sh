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

# --- Regression (gastown-cet.12.9 rework, codex finding #1) ---------------
# The final post-cutover verification — `gt witness rework-deferred dry-run`
# via the wrapper — runs AFTER `make safe-install` has already installed the
# new binary. If it fails, the script MUST invoke cutover_rollback (which
# restores the pre-cutover binary) rather than a bare `exit 1`, otherwise a
# bad pinned build is left live — the exact failure mode this cutover guards.
# The prior MR exited 1 directly here. Assert every verification failure
# path in the non-dry-run block routes through cutover_rollback instead.

# Extract the non-dry-run verify region: from the cutover_rollback() definition
# through the end of the throttle dry-run block (the "# Record durable cutover
# evidence" comment marks where verification ends and evidence recording begins).
# This is the region where a failed verify must not strand a bad binary.
# cutover_rollback itself ends with `exit 1`, so the only legitimate `exit 1`
# in this region is the one INSIDE cutover_rollback.
VERIFY_BLOCK="$(sed -n '/^  cutover_rollback() {$/,/^# Record durable cutover evidence/p' "${CUTOVER_SCRIPT}" | sed '${/^# Record durable cutover evidence/d;}')"

# There must be exactly ONE `exit 1` STATEMENT in the verify region — the one
# inside cutover_rollback's body. Any other bare `exit 1` is a verify path that
# strands a bad binary (the codex finding: the throttle dry-run used to exit 1).
# Exclude comment lines (which may mention "exit 1" in prose) from the count.
VERIFY_EXITS="$(printf '%s\n' "${VERIFY_BLOCK}" | grep -v '^[[:space:]]*#' | grep -c 'exit 1' || true)"
if [ "${VERIFY_EXITS}" -eq 1 ]; then
  pass "cutover verify region has a single exit 1 (inside cutover_rollback only)"
else
  fail "cutover verify region has ${VERIFY_EXITS} 'exit 1' (expected 1, inside cutover_rollback); a verify path strands a bad binary"
fi

# The throttle dry-run is the FINAL post-install verify, the one codex flagged:
# it runs after safe-install installed the new binary, so its failure MUST roll
# back rather than exit. Assert it routes failures to cutover_rollback.
THROTTLE_BLOCK="$(sed -n '/Running REWORK_DEFERRED throttle dry-run/,/^  echo ""$/p' "${CUTOVER_SCRIPT}" | head -20)"
if printf '%s\n' "${THROTTLE_BLOCK}" | grep -q 'cutover_rollback'; then
  pass "final throttle dry-run failure invokes cutover_rollback"
else
  fail "final throttle dry-run failure does NOT invoke cutover_rollback (bad build left live)"
  printf '%s\n' "${THROTTLE_BLOCK}" >&2
fi

# Every post-install verify step must route through cutover_rollback. Count the
# cutover_rollback invocations in the verify region (excluding the function
# definition line itself): topology, real-bin executable, version, pinned-line,
# hardening-fixes, and the throttle dry-run = 6 call sites.
ROLLBACK_CALLS="$(printf '%s\n' "${VERIFY_BLOCK}" | grep -c 'cutover_rollback "' || true)"
if [ "${ROLLBACK_CALLS}" -ge 6 ]; then
  pass "all post-cutover verify failures route through cutover_rollback (${ROLLBACK_CALLS} sites)"
else
  fail "only ${ROLLBACK_CALLS} cutover_rollback verify sites (expected >=6); a verify path strands a bad binary"
fi

echo ""
echo "Results: ${PASS} passed, ${FAIL} failed"
[ "${FAIL}" -eq 0 ]
