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
FALSE_ELF="$(real_elf false)"
BASH_ELF="$(real_elf bash)"
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

# --- Regression (gastown-cet.12.9 rework, codex finding #3): fail-closed ---
# When the locked/canaried rollback REFUSES the candidate (its canary rejects
# the backup bytes — a backup that is itself a bad ELF), the restore must NOT
# fall back to a direct `cp` over the live real-bin. The prior cutover code
# had a direct-cp fallback after a failed gt_install_rollback; this test
# proves the library path itself fail-closes (refuses, leaves the live slot
# untouched, preserves evidence) rather than copying blind. We plant a BAD
# backup (false, exits 1 → canary rejects) and a GOOD live binary, then ask
# gt_install_rollback to restore the bad backup: it must refuse and leave the
# live binary exactly as it was.
INSTALL_DIR2="${TMPDIR}/home2/.local/bin"
mkdir -p "${INSTALL_DIR2}"

# GOOD live binary behind a wrapper (true). The live slot must survive.
cp "$TRUE_ELF" "${INSTALL_DIR2}/gt-real-bin"
chmod 0755 "${INSTALL_DIR2}/gt-real-bin"
LIVE_INO2="$(stat -c '%i' "${INSTALL_DIR2}/gt-real-bin" 2>/dev/null || stat -f '%i' "${INSTALL_DIR2}/gt-real-bin")"
cat > "${INSTALL_DIR2}/gt" <<'WR'
#!/usr/bin/env bash
# gt wrapper — guarantees the current validation model-mix on `gt sling`.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"; exec "${SCRIPT_DIR}/gt-real-bin" "$@"
WR
chmod 0755 "${INSTALL_DIR2}/gt"

# A BAD backup: false (exits 1 → canary rejects). This is the bytes a naive
# direct-cp rollback would drop over the live slot.
[ -n "$FALSE_ELF" ] || { echo "SKIP finding#3 fail-closed: no 'false' ELF" >&2; }
if [ -n "$FALSE_ELF" ]; then
    BAD_BACKUP="${INSTALL_DIR2}/gt-real-bin.bak.20260627T120000Z"
    cp "$FALSE_ELF" "$BAD_BACKUP"; chmod 0644 "$BAD_BACKUP"

    set +e
    INSTALL_DIR="$INSTALL_DIR2" BINARY="gt" HOME="${TMPDIR}/home2" \
        bash -c "source '$LIB'; gt_install_rollback '$BAD_BACKUP'" \
        >"${TMPDIR}/failclosed.out" 2>&1
    RC2=$?
    set -e

    if [ "$RC2" -eq 0 ]; then
        fail "gt_install_rollback accepted a bad canary-failing backup (rc=$RC2)"
    else
        pass "gt_install_rollback refused a canary-failing backup (rc=$RC2)"
    fi
    # The live binary must be UNTOUCHED — no direct cp over it.
    NOW_INO2="$(stat -c '%i' "${INSTALL_DIR2}/gt-real-bin" 2>/dev/null || stat -f '%i' "${INSTALL_DIR2}/gt-real-bin")"
    if [ "$NOW_INO2" = "$LIVE_INO2" ]; then
        pass "fail-closed: live real-bin left untouched when rollback canary rejects backup"
    else
        fail "fail-closed: live real-bin was overwritten despite canary rejection (direct-cp bug)"
    fi
    # And the bad backup must still exist (evidence preserved for manual review).
    if [ -f "$BAD_BACKUP" ]; then
        pass "fail-closed: bad backup preserved as evidence"
    else
        fail "fail-closed: bad backup was consumed/deleted (evidence lost)"
    fi
fi

# --- Regression (gastown-cet.12.9 current rework): rollback restore TOCTOU. ---
# gt_install_rollback used to canary one copy of the restore snapshot, then
# copy the original restore path again into the live staging file. A mutable or
# symlinked restore path could change after canary passed and before the final
# copy, bypassing the health gate. The fixed path copies restore ONCE to a
# private staged file, canaries that staged file, then renames that same file
# into place.
if [ -n "$BASH_ELF" ] && [ -n "$FALSE_ELF" ]; then
    INSTALL_DIR3="${TMPDIR}/home3/.local/bin"
    MUTATE_CWD="${TMPDIR}/mutate-cwd"
    mkdir -p "${INSTALL_DIR3}" "${MUTATE_CWD}"

    cp "$TRUE_ELF" "${INSTALL_DIR3}/gt-real-bin"
    chmod 0755 "${INSTALL_DIR3}/gt-real-bin"
    cat > "${INSTALL_DIR3}/gt" <<'WR'
#!/usr/bin/env bash
# gt wrapper — guarantees the current validation model-mix on `gt sling`.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"; exec "${SCRIPT_DIR}/gt-real-bin" "$@"
WR
    chmod 0755 "${INSTALL_DIR3}/gt"

    # The healthy restore target is bash. The canary runs `<candidate> version`;
    # with this cwd-local script, bash sleeps briefly and exits 0. That gives the
    # background flipper a deterministic window while the canary is in progress.
    cat > "${MUTATE_CWD}/version" <<'VER'
sleep 1
exit 0
VER
    GOOD_RESTORE="${TMPDIR}/good-restore"
    BAD_RESTORE="${TMPDIR}/bad-restore"
    RESTORE_LINK="${TMPDIR}/restore-link"
    cp "$BASH_ELF" "$GOOD_RESTORE"; chmod 0755 "$GOOD_RESTORE"
    cp "$FALSE_ELF" "$BAD_RESTORE"; chmod 0755 "$BAD_RESTORE"
    ln -s "$GOOD_RESTORE" "$RESTORE_LINK"

    (
        # Flip as soon as rollback has created either the old canary probe
        # (buggy implementation) or the new rollback staging file (fixed
        # implementation), i.e. after the initial good bytes have been copied
        # and while the slow canary is running.
        i=0
        while [ "$i" -lt 80 ]; do
            if ls "${INSTALL_DIR3}"/gt-real-bin.canary.* "${INSTALL_DIR3}"/gt-real-bin.rollback.* >/dev/null 2>&1; then
                ln -sfn "$BAD_RESTORE" "$RESTORE_LINK"
                exit 0
            fi
            i=$((i + 1))
            sleep 0.05
        done
        ln -sfn "$BAD_RESTORE" "$RESTORE_LINK"
    ) &
    FLIP_PID=$!

    set +e
    (
        cd "${MUTATE_CWD}"
        INSTALL_DIR="$INSTALL_DIR3" BINARY="gt" HOME="${TMPDIR}/home3" GT_INSTALL_CANARY_TIMEOUT=5 \
            bash -c "source '$LIB'; gt_install_rollback '$RESTORE_LINK'"
    ) >"${TMPDIR}/restore-mutate.out" 2>&1
    RC3=$?
    set -e
    wait "$FLIP_PID" 2>/dev/null || true

    if [ "$RC3" -eq 0 ]; then
        pass "rollback accepted the initially healthy restore artifact"
    else
        fail "rollback failed for initially healthy restore artifact (rc=$RC3)"
        cat "${TMPDIR}/restore-mutate.out" >&2
    fi
    if cmp -s "${INSTALL_DIR3}/gt-real-bin" "$GOOD_RESTORE"; then
        pass "rollback installed the exact canary-approved staged artifact"
    else
        fail "rollback re-read the mutated restore path instead of the canary-approved artifact"
        cat "${TMPDIR}/restore-mutate.out" >&2
    fi
    if [ "$(readlink "$RESTORE_LINK" 2>/dev/null || true)" = "$BAD_RESTORE" ]; then
        pass "restore path mutated during rollback test (test had teeth)"
    else
        fail "restore path did not mutate during rollback test (precondition missing)"
    fi
else
    echo "SKIP restore mutation regression: need bash+false ELFs" >&2
fi

echo ""
echo "Results: ${PASS} passed, ${FAIL} failed"
[ "${FAIL}" -eq 0 ]
