#!/usr/bin/env bash
# Regression test for the cutover auto-rollback path (gastown-cet.12.9).
#
# The cutover script must restore the pre-cutover binary when post-install
# verification fails, rather than leaving a bad pinned build live. Exercising
# the real cutover script end-to-end requires `make safe-install` (a full
# Go build), which is too heavy for a unit test. Instead this test sources the
# install library directly and proves the rollback primitive the cutover
# delegates to (gt_install_rollback) restores the snapshot a failed install
# would have left behind. The cutover wiring itself is verified by the
# grep-level checks in cutover-pinned-1.2.0_test.sh (rollback guidance and
# the cutover_rollback helper are emitted).
#
# This mirrors the operational topology: a wrapper at the public path, the
# real ELF behind it at gt-real-bin, and a .bak snapshot from a prior install.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
LIB="${SCRIPT_DIR}/lib/wrapper-preserve.sh"

PASS=0
FAIL=0
pass() { echo "PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "FAIL: $1"; FAIL=$((FAIL + 1)); }

TMPDIR=""
cleanup() { [ -n "${TMPDIR}" ] && [ -d "${TMPDIR}" ] && rm -rf "${TMPDIR}"; }
trap cleanup EXIT
TMPDIR="$(mktemp -d)"

# Resolve two distinct real ELFs that both exit 0 on `version` so the canary
# accepts them. true (new build) and echo (incumbent).
real_elf() {
    for p in /bin/"$1" /usr/bin/"$1"; do [ -x "$p" ] && { printf '%s\n' "$p"; return 0; }; done
    command -v "$1"
}
TRUE_ELF="$(real_elf true)"
ECHO_ELF="$(real_elf echo)"
[ -n "$TRUE_ELF" ] && [ -n "$ECHO_ELF" ] || { echo "SKIP: need true+echo ELFs" >&2; exit 0; }

INSTALL_DIR="${TMPDIR}/home/.local/bin"
mkdir -p "${INSTALL_DIR}"

# --- Operational topology: incumbent echo ELF behind a wrapper. ----------
cp "$ECHO_ELF" "${INSTALL_DIR}/gt-real-bin"
chmod 0755 "${INSTALL_DIR}/gt-real-bin"
cat > "${INSTALL_DIR}/gt" <<'WR'
#!/usr/bin/env bash
# gt wrapper — guarantees the current validation model-mix on `gt sling`.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"; exec "${SCRIPT_DIR}/gt-real-bin" "$@"
WR
chmod 0755 "${INSTALL_DIR}/gt"

# --- Simulate a cutover: install a NEW binary (true) behind the wrapper. --
# This snapshots the incumbent to gt-real-bin.bak.<ts>, exactly as the real
# cutover's safe-install would.
NEW_ELF="${TMPDIR}/new-gt"
cp "$TRUE_ELF" "$NEW_ELF"; chmod 0755 "$NEW_ELF"
INSTALL_DIR="$INSTALL_DIR" BINARY="gt" HOME="${TMPDIR}/home" \
    bash -c "source '$LIB'; gt_install_preserve_wrapper '$NEW_ELF'" \
    >"${TMPDIR}/install.out" 2>&1 || { fail "install step failed"; cat "${TMPDIR}/install.out"; }

# The live binary is now the NEW one (true), distinct from the incumbent (echo).
if cmp -s "${INSTALL_DIR}/gt-real-bin" "$ECHO_ELF"; then
    fail "new build did not replace incumbent (precondition)"
else
    pass "cutover installed new build behind wrapper"
fi

# --- Simulate post-install verification FAILURE and auto-rollback. -------
# A failed verification (version mismatch, topology break, etc.) must restore
# the incumbent from the .bak snapshot rather than leaving the new build live.
set +e
INSTALL_DIR="$INSTALL_DIR" BINARY="gt" HOME="${TMPDIR}/home" \
    bash -c "source '$LIB'; gt_install_rollback" \
    >"${TMPDIR}/rollback.out" 2>&1
RC=$?
set -e

if [ "$RC" -ne 0 ]; then
    fail "gt_install_rollback failed (rc=$RC)"
    cat "${TMPDIR}/rollback.out" >&2
else
    pass "gt_install_rollback exited 0 on failed-cutover recovery"
fi

# The live binary must once again be the incumbent (echo).
if cmp -s "${INSTALL_DIR}/gt-real-bin" "$ECHO_ELF"; then
    pass "live binary restored to the pre-cutover (incumbent) ELF"
else
    fail "live binary was NOT restored to the incumbent — bad build left live"
fi

# The rollback snapshotted the new (bad) build first, so it is reversible.
if ls "${INSTALL_DIR}"/gt-real-bin.bak.pre-rollback.* >/dev/null 2>&1; then
    pass "rollback snapshotted the bad build (reversible recovery)"
else
    fail "no pre-rollback snapshot — recovery is not reversible"
fi

echo ""
echo "Results: ${PASS} passed, ${FAIL} failed"
[ "${FAIL}" -eq 0 ]
