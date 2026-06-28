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
VERIFY_BLOCK="$(sed -n '/^  cutover_rollback() {$/,/^# Record durable cutover evidence/p' "${CUTOVER_SCRIPT}" | sed '${/^# Record durable cutover evidence/d;}')"

# The whole non-dry-run cutover body (everything inside `else ... fi` after
# the dry-run branch) is used by the Finding #3 static assertion to scan for
# any direct `cp` over the live real-bin path outside the library restore.
CUTOVER_BODY="$(sed -n '/^  # Build the pinned 1.2.0 runtime binary/,/^# Record durable cutover evidence/p' "${CUTOVER_SCRIPT}")"

# Split the verify region into cutover_rollback's OWN body (everything from the
# `cutover_rollback() {` definition through its closing `}`) and the POST-
# rollback verify region (the `if ! ... ` checks that must call cutover_rollback
# on failure). A bare `exit 1` is legitimate ONLY inside cutover_rollback's body
# (the function's terminal exits — the success-path return, the missing-backup
# fail-closed, and the locked-rollback-failed fail-closed). Any `exit 1` in the
# POST-rollback verify region strands a bad binary: it skips the restore.
ROLLBACK_BODY="$(printf '%s\n' "${VERIFY_BLOCK}" | awk '/^  cutover_rollback\(\) \{/,/^  \}$/')"
POST_ROLLBACK_VERIFY="$(printf '%s\n' "${VERIFY_BLOCK}" | sed '1,/^  \}$/d')"
# Exclude comment lines (which may mention "exit 1" in prose) from the count.
POST_EXITS="$(printf '%s\n' "${POST_ROLLBACK_VERIFY}" | grep -v '^[[:space:]]*#' | grep -c 'exit 1' || true)"
if [ "${POST_EXITS}" -eq 0 ]; then
  pass "no bare exit 1 outside cutover_rollback in the verify region"
else
  fail "verify region has ${POST_EXITS} 'exit 1' outside cutover_rollback; a verify path strands a bad binary"
  printf '%s\n' "${POST_ROLLBACK_VERIFY}" | grep -v '^[[:space:]]*#' | grep 'exit 1' >&2
fi

# The throttle dry-run is the FINAL post-install verify, the one codex flagged:
# it runs after safe-install installed the new binary, so its failure MUST roll
# back rather than exit. Assert it routes failures to cutover_rollback.
THROTTLE_BLOCK="$(sed -n '/Running REWORK_DEFERRED throttle dry-run/,/^  echo ""$/p' "${CUTOVER_SCRIPT}" | head -25)"
if printf '%s\n' "${THROTTLE_BLOCK}" | grep -q 'cutover_rollback'; then
  pass "final throttle dry-run failure invokes cutover_rollback"
else
  fail "final throttle dry-run failure does NOT invoke cutover_rollback (bad build left live)"
  printf '%s\n' "${THROTTLE_BLOCK}" >&2
fi

# Every post-install verify step must route through cutover_rollback. Count the
# cutover_rollback invocations in the verify region (excluding the function
# definition line itself): topology, real-bin executable, version, pinned-line,
# hardening-fixes, and the throttle dry-run (missing-timeout fail-closed) = >=6.
ROLLBACK_CALLS="$(printf '%s\n' "${VERIFY_BLOCK}" | grep -c 'cutover_rollback "' || true)"
if [ "${ROLLBACK_CALLS}" -ge 6 ]; then
  pass "all post-cutover verify failures route through cutover_rollback (${ROLLBACK_CALLS} sites)"
else
  fail "only ${ROLLBACK_CALLS} cutover_rollback verify sites (expected >=6); a verify path strands a bad binary"
fi

# --- Regression (gastown-cet.12.9 rework, codex finding #1): bounded verify ---
# The final post-install verification (the throttle dry-run via the wrapper)
# MUST be bounded by a timeout, and the inability to enforce one (GNU timeout
# missing) must route to cutover_rollback rather than run unbounded. The prior
# code ran the wrapper dry-run with no bound: a candidate that hung there would
# never reach cutover_rollback and would leave the bad binary live. Assert the
# verify region wraps the wrapper probe in `timeout` and fail-closes on a
# missing timeout into rollback.
THROTTLE_VERIFY="$(printf '%s\n' "${THROTTLE_BLOCK}")"
if printf '%s\n' "${VERIFY_BLOCK}" | grep -qE 'POST_VERSION=.*timeout "\$\{CUTOVER_VERIFY_TIMEOUT\}" "\$\{REAL_BIN_PATH\}" version'; then
  pass "installed version probe is wrapped in a bounded timeout"
else
  fail "installed version probe is NOT bounded by a timeout (could hang before rollback)"
  printf '%s\n' "${VERIFY_BLOCK}" >&2
fi
if printf '%s\n' "${VERIFY_BLOCK}" | grep -qE 'POST_VERBOSE=.*timeout "\$\{CUTOVER_VERIFY_TIMEOUT\}" "\$\{REAL_BIN_PATH\}" version --verbose'; then
  pass "installed verbose version probe is wrapped in a bounded timeout"
else
  fail "installed verbose version probe is NOT bounded by a timeout (could hang before rollback)"
  printf '%s\n' "${VERIFY_BLOCK}" >&2
fi
if printf '%s\n' "${THROTTLE_VERIFY}" | grep -qE 'timeout "\$\{?CUTOVER_VERIFY_TIMEOUT\}?".*\$\{?WRAPPER_PATH\}?'; then
  pass "final throttle dry-run is wrapped in a bounded timeout"
else
  fail "final throttle dry-run is NOT bounded by a timeout (could hang and strand a bad binary)"
  printf '%s\n' "${THROTTLE_VERIFY}" >&2
fi
UNBOUNDED_REAL_PROBES="$(
  printf '%s\n' "${VERIFY_BLOCK}" |
    grep -v '^[[:space:]]*#' |
    grep -E '(^|[|;&(][[:space:]]*)"?\$\{REAL_BIN_PATH\}"?[[:space:]]+(version|witness)' |
    grep -v 'timeout' || true
)"
if [ -z "${UNBOUNDED_REAL_PROBES}" ]; then
  pass "post-install verify region has no unbounded real-binary executions"
else
  fail "post-install verify region still executes the real binary without timeout"
  printf '%s\n' "${UNBOUNDED_REAL_PROBES}" >&2
fi
# A missing GNU timeout must route to cutover_rollback, not run unbounded. The
# guard is `if ! command -v timeout ...; then <echo lines>; cutover_rollback`.
# Look for cutover_rollback within the few lines following the timeout check.
if printf '%s\n' "${VERIFY_BLOCK}" | grep -q 'command -v timeout'; then
  if printf '%s\n' "${VERIFY_BLOCK}" | grep -A5 'command -v timeout' | grep -q 'cutover_rollback'; then
    pass "missing-timeout in verify routes to cutover_rollback (fail-closed)"
  else
    fail "missing-timeout in verify does NOT route to cutover_rollback (could run unbounded)"
  fi
else
  fail "verify region has no missing-timeout fail-closed guard"
fi
if printf '%s\n' "${VERIFY_BLOCK}" | grep -q 'CUTOVER_VERIFY_TIMEOUT=0 disables the bound'; then
  pass "zero timeout is rejected instead of disabling the verify bound"
else
  fail "CUTOVER_VERIFY_TIMEOUT=0 is not rejected (GNU timeout 0 can run unbounded)"
fi

# --- Regression (gastown-cet.12.9 rework, codex finding #3): no direct cp ---
# cutover_rollback must funnel the restore EXCLUSIVELY through the library's
# gt_install_rollback (flock-serialized, canary-checked, atomic). The prior
# version fell back to a bare `cp <backup> <real-bin>` over the live slot when
# the library rollback failed — bypassing the lock, the rollback canary, and
# the atomic rename, racing an installer or restoring bytes the health gate
# had rejected. Assert the cutover script contains NO direct cp of the backup
# over REAL_BIN_PATH anywhere (the only restore path is gt_install_rollback).
#
# Pattern to forbid: a `cp` whose target is the live real-bin path. The
# legitimate `cp` calls in the script copy TO the backup or stage files, never
# cp-back-over-live. We scan the whole script (excluding comments) for any
# `cp ... <REAL_BIN_PATH>` or `cp ... "${REAL_BIN_PATH}"`.
DIRECT_CP="$(printf '%s\n' "${CUTOVER_BODY}" | grep -v '^[[:space:]]*#' | grep -E 'cp[[:space:]].*(\$\{?REAL_BIN_PATH\}?|gt-real-bin)' || true)"
if [ -z "${DIRECT_CP}" ]; then
  pass "cutover script performs no direct cp over the live real-bin"
else
  fail "cutover script still direct-copies over the live real-bin (codex finding #3 not fixed)"
  printf '%s\n' "${DIRECT_CP}" >&2
fi
# And assert the only restore path IS the library gt_install_rollback.
if printf '%s\n' "${CUTOVER_BODY}" | grep -q 'gt_install_rollback'; then
  pass "cutover rollback restores via the library gt_install_rollback"
else
  fail "cutover rollback does not use gt_install_rollback (no locked/canaried restore)"
fi

# --- Regression (gastown-cet.12.11): forward-only fail-fast on missing metadata ---
# The forward-only guard previously printed "Warning: cannot determine installed
# binary commit; skipping forward check" when `gt version --verbose` did not
# emit an '@<commit>' token, and exited 0. That let a downgrade or wrong-version
# cutover proceed with only a warning — the exact silent bypass this guard
# exists to prevent. The fix is to FAIL FAST unless --skip-forward-check is
# explicit. Lock in all three regimes with hermetic sub-tests.

# A separate fake ELF that does NOT emit an '@<commit>' token in version output.
# This is the binary that triggered the silent bypass in retro-gate shard D.
FAKE_HOME_NO_COMMIT="${TMPDIR}/home-nocommit"
FAKE_INSTALL_DIR_NO_COMMIT="${FAKE_HOME_NO_COMMIT}/.local/bin"
FAKE_TOWN_ROOT_NO_COMMIT="${TMPDIR}/town-nocommit"
mkdir -p "${FAKE_INSTALL_DIR_NO_COMMIT}" "${FAKE_TOWN_ROOT_NO_COMMIT}"

cat > "${FAKE_INSTALL_DIR_NO_COMMIT}/gt" <<'WRAPPER_NC'
#!/usr/bin/env bash
# Fake wrapper without version metadata (mirrors a binary that lost its
# ldflags -X Commit=... value — the failure mode the guard catches).
REAL_BIN="${0%/*}/gt-real-bin"
if [ "${1:-}" = "version" ]; then
  echo "gt version 1.1.9 (no commit metadata)"
  exit 0
fi
exec "${REAL_BIN}" "$@"
WRAPPER_NC
chmod 0755 "${FAKE_INSTALL_DIR_NO_COMMIT}/gt"

cat > "${FAKE_INSTALL_DIR_NO_COMMIT}/gt-real-bin" <<'ELF_NC'
#!/usr/bin/env bash
if [ "${1:-}" = "version" ]; then
  echo "gt version 1.1.9 (no commit metadata)"
  exit 0
fi
if [ "${1:-}" = "version" ] && [ "${2:-}" = "--verbose" ]; then
  echo "gt version 1.1.9 (no commit metadata)"
  exit 0
fi
echo "Unknown command: $1" >&2
exit 1
ELF_NC
chmod 0755 "${FAKE_INSTALL_DIR_NO_COMMIT}/gt-real-bin"

# --- Regime 1: NO skip flag, NO commit metadata → must FAIL FAST ---
set +e
HOME="${FAKE_HOME_NO_COMMIT}" \
  GT_TOWN_ROOT="${FAKE_TOWN_ROOT_NO_COMMIT}" \
  PATH="${FAKE_INSTALL_DIR_NO_COMMIT}:${PATH}" \
  bash "${CUTOVER_SCRIPT}" --dry-run \
  > "${TMPDIR}/nocommit-strict.out" 2> "${TMPDIR}/nocommit-strict.err"
NC_STRICT_RC=$?
set -e
if [ "${NC_STRICT_RC}" -ne 0 ]; then
  pass "cutover fails fast (exit ${NC_STRICT_RC}) when binary has no commit metadata and --skip-forward-check is NOT set"
else
  fail "cutover exited 0 with no commit metadata and no --skip-forward-check (silent bypass regression — gastown-cet.12.11)"
  echo "--- stdout ---"; cat "${TMPDIR}/nocommit-strict.out"
  echo "--- stderr ---"; cat "${TMPDIR}/nocommit-strict.err"
fi
if grep -q "ERROR: cannot determine installed binary commit" "${TMPDIR}/nocommit-strict.err"; then
  pass "fail-fast error message present on missing commit metadata (no silent warning)"
else
  fail "fail-fast error message missing on missing commit metadata"
  cat "${TMPDIR}/nocommit-strict.err" >&2
fi
# The 'Warning' line must NOT appear WITHOUT the 'ERROR' line — that would
# be the legacy silent-skip path returned. Use intermediate variables to
# avoid fragile && chains inside `if`.
WARN_HIT=0
ERR_HIT=0
grep -q "Warning: cannot determine installed binary commit" "${TMPDIR}/nocommit-strict.err" && WARN_HIT=1 || true
grep -q "ERROR: cannot determine installed binary commit" "${TMPDIR}/nocommit-strict.err" && ERR_HIT=1 || true
if [ "${WARN_HIT}" -eq 1 ] && [ "${ERR_HIT}" -eq 0 ]; then
  fail "regression: only the legacy 'Warning' line is emitted (silent skip path returned)"
fi
# Also assert no cutover evidence was recorded: an unanchored cutover must not
# proceed far enough to write its record file.
if [ -f "${FAKE_TOWN_ROOT_NO_COMMIT}/.runtime/pinned-1.2.0-cutover.json" ]; then
  fail "evidence record was written despite missing commit metadata (cutover did not fail fast)"
else
  pass "no evidence record written when forward-only check fails fast"
fi

# --- Regime 2: WITH --skip-forward-check, NO commit metadata → must SUCCEED ---
set +e
HOME="${FAKE_HOME_NO_COMMIT}" \
  GT_TOWN_ROOT="${FAKE_TOWN_ROOT_NO_COMMIT}" \
  PATH="${FAKE_INSTALL_DIR_NO_COMMIT}:${PATH}" \
  bash "${CUTOVER_SCRIPT}" --dry-run --skip-forward-check \
  > "${TMPDIR}/nocommit-skip.out" 2> "${TMPDIR}/nocommit-skip.err"
NC_SKIP_RC=$?
set -e
if [ "${NC_SKIP_RC}" -eq 0 ]; then
  pass "explicit --skip-forward-check overrides missing commit metadata (operator opt-in works)"
else
  fail "explicit --skip-forward-check did NOT override missing commit metadata (exit ${NC_SKIP_RC})"
  echo "--- stdout ---"; cat "${TMPDIR}/nocommit-skip.out"
  echo "--- stderr ---"; cat "${TMPDIR}/nocommit-skip.err"
fi
if grep -q "Skipping forward-only check (--skip-forward-check)" "${TMPDIR}/nocommit-skip.out"; then
  pass "--skip-forward-check banner emitted on explicit opt-in"
else
  fail "--skip-forward-check banner missing on explicit opt-in"
fi

# --- Regime 3: baseline scenario with --skip-forward-check (already passes) ---
# The original test scenario (lines 84-89) uses --skip-forward-check and
# exercises the full dry-run flow. Assert the skip banner is present in its
# output as a regression marker.
if grep -q "Skipping forward-only check (--skip-forward-check)" "${TMPDIR}/cutover.out"; then
  pass "baseline scenario emits --skip-forward-check banner (no regression)"
else
  fail "baseline scenario missing --skip-forward-check banner"
  cat "${TMPDIR}/cutover.out" >&2
fi

# --- Regime 4: cutover script must NOT contain the legacy warning-only branch ---
# Static check: the only place a "Warning: cannot determine installed binary
# commit" message is acceptable is the deprecated-and-skipped branch. After
# the fix the script should emit "ERROR: cannot determine installed binary
# commit" instead. This guards against a future edit that re-introduces the
# silent skip.
if grep -q 'Warning: cannot determine installed binary commit' "${CUTOVER_SCRIPT}"; then
  fail "cutover script still contains the legacy 'Warning' silent-skip message"
else
  pass "cutover script no longer contains the legacy 'Warning' silent-skip message"
fi
if grep -q 'ERROR: cannot determine installed binary commit' "${CUTOVER_SCRIPT}"; then
  pass "cutover script emits fail-fast 'ERROR' on missing commit metadata"
else
  fail "cutover script does NOT emit fail-fast 'ERROR' on missing commit metadata"
fi

# --- Regime 5: Makefile check-forward-only must also fail fast on missing metadata ---
# Static + dynamic: the Makefile target must contain the fail-fast branch
# (error, exit 1) rather than the legacy warning-only skip.
if grep -q 'WARNING.*cannot determine installed binary commit' Makefile; then
  fail "Makefile check-forward-only still contains legacy 'Warning' silent-skip (gastown-cet.12.11)"
fi
if ! grep -q 'ERROR.*cannot determine installed binary commit' Makefile; then
  fail "Makefile check-forward-only does NOT emit fail-fast 'ERROR' on missing commit metadata"
fi
# Dynamic check: invoke `make check-forward-only` against a fake install dir
# holding a binary with no '@<commit>' token. The Makefile must exit non-zero.
FAKE_HOME_MAKE="${TMPDIR}/home-make"
FAKE_INSTALL_DIR_MAKE="${FAKE_HOME_MAKE}/.local/bin"
mkdir -p "${FAKE_INSTALL_DIR_MAKE}"
cat > "${FAKE_INSTALL_DIR_MAKE}/gt" <<'NOCOMMIT_GT'
#!/usr/bin/env bash
# Fake gt without '@<commit>' version metadata. Matches both `version`
# and `version --verbose` since both are routed through $1 == "version".
if [ "${1:-}" = "version" ]; then
  echo "gt version 1.1.9 (no commit metadata)"
  exit 0
fi
exit 1
NOCOMMIT_GT
chmod 0755 "${FAKE_INSTALL_DIR_MAKE}/gt"
set +e
HOME="${FAKE_HOME_MAKE}" \
  make -C "${REPO_ROOT}" check-forward-only \
  > "${TMPDIR}/make-strict.out" 2> "${TMPDIR}/make-strict.err"
MAKE_STRICT_RC=$?
set -e
if [ "${MAKE_STRICT_RC}" -ne 0 ]; then
  pass "make check-forward-only fails fast (exit ${MAKE_STRICT_RC}) when binary has no commit metadata"
else
  fail "make check-forward-only exited 0 with no commit metadata (silent bypass regression — gastown-cet.12.11)"
  echo "--- stdout ---"; cat "${TMPDIR}/make-strict.out"
  echo "--- stderr ---"; cat "${TMPDIR}/make-strict.err"
fi
# Explicit override must succeed (SKIP_FORWARD_CHECK=1 turns the guard off).
set +e
HOME="${FAKE_HOME_MAKE}" \
  make -C "${REPO_ROOT}" check-forward-only SKIP_FORWARD_CHECK=1 \
  > "${TMPDIR}/make-skip.out" 2> "${TMPDIR}/make-skip.err"
MAKE_SKIP_RC=$?
set -e
if [ "${MAKE_SKIP_RC}" -eq 0 ]; then
  pass "make SKIP_FORWARD_CHECK=1 overrides missing commit metadata (explicit opt-in)"
else
  fail "make SKIP_FORWARD_CHECK=1 did NOT override missing commit metadata (exit ${MAKE_SKIP_RC})"
  cat "${TMPDIR}/make-skip.err" >&2
fi

echo ""
echo "Results: ${PASS} passed, ${FAIL} failed"
[ "${FAIL}" -eq 0 ]
