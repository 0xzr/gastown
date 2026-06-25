#!/usr/bin/env bash
# gt-refinery-bypass-detector.sh — detect refinery-gate bypasses on protected branches.
#
# A bypass is a non-WIP commit on a protected branch (default origin/main) that
# landed WITHOUT either:
#   (a) a refinery telemetry event "gate_complete" with status=merge referencing
#       the commit or its tree-hash, OR
#   (b) an HMAC tree-token file under $GT_GATE_ATTEST_DIR/<tree>.
#
# When a bypass is detected, the script files an audit bead (in --fix mode) so
# the gap is surfaced immediately rather than discovered retroactively (the
# failure mode that produced gastown-a37 / gastown-66y).
#
# Default mode is report-only (--report). With --fix, audit beads are filed via
# `bd create` so the witness/auditor polecats pick them up. --dry-run suppresses
# ALL side effects (state file AND bead filing) so the detector can be re-run
# idempotently. Use --dry-run to probe what would be reported/filed without
# actually doing it.
#
# Operational: deploy from this repo to /home/ubuntu/gastown-spike/dropin/ (or
# equivalent operational dropin) and invoke from a supervisor / cron / post-
# merge-watchdog. The on-host rig repo is what `gt done` and `gt push` write
# to, so the local RIG_DIR is the source of truth for commit detection.
#
# Suggested deployment (per-rig, per-branch):
#   # Copy to dropin:
#   install -m 0755 scripts/gt-refinery-bypass-detector.sh \
#       /home/ubuntu/gastown-spike/dropin/gt-refinery-bypass-detector.sh
#
#   # Cron entry (every 5 min, --fix --auto-ack so beads are filed and state advances):
#   */5 * * * * GT_BYPASS_DETECT_RIG_DIR=/home/ubuntu/gt-town/gastown \
#             /home/ubuntu/gastown-spike/dropin/gt-refinery-bypass-detector.sh \
#             --fix --auto-ack --max-scan 50 \
#             >> /home/ubuntu/gt-town/.runtime/refinery-bypass-detector/cron.log 2>&1
#
# Initial run after deployment: --reset-state --dry-run to confirm what the
# detector would flag, then --fix --auto-ack to file beads for any current
# bypasses and start tracking. Subsequent runs only scan NEW commits landing
# after the recorded tip, so the cron load is bounded by merge frequency.
#
# Related: gastown-a37 (no-telemetry investigation), gastown-66y (35-commit
# rollup), gastown-cet.12.4..12.14 (retro-gate shards).

set -uo pipefail

RIG="${GT_BYPASS_DETECT_RIG:-gastown}"
BRANCH="${GT_BYPASS_DETECT_BRANCH:-main}"
REMOTE="${GT_BYPASS_DETECT_REMOTE:-origin}"
TOWN="${GT_TOWN:-/home/ubuntu/gt-town}"
RIG_DIR="${GT_BYPASS_DETECT_RIG_DIR:-$TOWN/$RIG}"

RUNTIME_DIR="${GT_BYPASS_DETECT_RUNTIME:-$TOWN/.runtime}"
REFINERY_TELEM_DIR="${GT_REFINERY_TELEMETRY_DIR:-$RUNTIME_DIR/refinery-telemetry}"
HOOK_TELEM_DIR="${GT_GATE_TELEMETRY_DIR:-$RUNTIME_DIR/gate-hook-telemetry}"
ATTEST_DIR="${GT_GATE_ATTEST_DIR:-/home/ubuntu/.gt-gate-attestations}"

STATE_DIR="${GT_BYPASS_DETECT_STATE_DIR:-$RUNTIME_DIR/refinery-bypass-detector}"
STATE_FILE="${STATE_DIR}/state.${RIG}.${BRANCH}"
LOG_FILE="${STATE_DIR}/log.${RIG}.${BRANCH}"

MAX_SCAN="${GT_BYPASS_DETECT_MAX_SCAN:-200}"
FIX=0
DRY_RUN=0
VERBOSE=0
AUTO_ACK=0

BD_BIN="${BD_BIN:-bd}"
BD_PREFIX="${BD_BIN//\//_}"

usage() {
  cat <<'USAGE'
Usage: gt-refinery-bypass-detector.sh [options]

Detect refinery-gate bypasses on a protected branch.

Options:
  --rig RIG            Rig name (default: gastown)
  --branch BRANCH      Branch to scan (default: main)
  --remote REMOTE      Remote name (default: origin)
  --max-scan N         Max commits to scan per run (default: 200)
  --since SHA          Resume scan from this tip SHA (overrides state file)
  --fix                File audit beads for bypasses (default: report only)
  --auto-ack           Update state file in --fix mode (default: in report mode)
  --dry-run            Suppress all side effects (state file AND bead filing)
  --reset-state        Forget the previous tip; rescan from --max-scan back
  -v, --verbose        Print extra diagnostics to stderr
  -h, --help           Show this help

Environment overrides (all optional):
  GT_BYPASS_DETECT_RIG / GT_BYPASS_DETECT_BRANCH / GT_BYPASS_DETECT_RIG_DIR
  GT_REFINERY_TELEMETRY_DIR / GT_GATE_TELEMETRY_DIR
  GT_GATE_ATTEST_DIR
  GT_BYPASS_DETECT_STATE_DIR / GT_BYPASS_DETECT_RUNTIME
  BD_BIN                Path to beads CLI (default: bd)

Exit codes:
  0  no bypasses detected
  1  bypasses detected (in --report mode: report printed; in --fix mode: beads filed)
  2  configuration error (missing rig dir, missing bd, etc.)
USAGE
}

log() {
  printf '[%s] %s\n' "$(date -Is)" "$*" >>"$LOG_FILE"
  [ "$VERBOSE" = 1 ] && printf '[%s] %s\n' "$(date -Is)" "$*" >&2
}

warn() { printf '[bypass-detector] WARN: %s\n' "$*" >&2; }
die()  { printf '[bypass-detector] ERROR: %s\n' "$*" >&2; exit 2; }

while [ "$#" -gt 0 ]; do
  case "$1" in
    --rig)         RIG="$2"; shift 2 ;;
    --branch)      BRANCH="$2"; shift 2 ;;
    --remote)      REMOTE="$2"; shift 2 ;;
    --max-scan)    MAX_SCAN="$2"; shift 2 ;;
    --since)       STATE_OVERRIDE_TIP="$2"; shift 2 ;;
    --fix)         FIX=1; shift ;;
    --auto-ack)    AUTO_ACK=1; shift ;;
    --dry-run)     DRY_RUN=1; shift ;;
    --reset-state) RESET_STATE=1; shift ;;
    -v|--verbose)  VERBOSE=1; shift ;;
    -h|--help)     usage; exit 0 ;;
    *)             die "unknown argument: $1" ;;
  esac
done

mkdir -p "$STATE_DIR" 2>/dev/null || die "cannot create state dir: $STATE_DIR"
: >"$LOG_FILE"

log "start rig=$RIG branch=$BRANCH remote=$REMOTE max_scan=$MAX_SCAN fix=$FIX dry_run=$DRY_RUN"

# Verify RIG_DIR resolves as a git repo: either has a .git (linked or directory),
# .repo.git (bare), or is itself a working tree. The bare-repo layout (rig uses
# /home/ubuntu/gt-town/gastown/.repo.git as its authoritative store) is also valid.
# Once we find the repo, set GIT_DIR_GLOBAL so every subsequent `git` invocation
# uses the resolved location — `git -C` does not work against a bare repo.
GIT_DIR_GLOBAL=""
if [ -d "$RIG_DIR/.repo.git" ]; then
  GIT_DIR_GLOBAL="$RIG_DIR/.repo.git"
elif GITDIR_FROM_RIG="$(git -C "$RIG_DIR" rev-parse --git-dir 2>/dev/null || true)"; then
  case "$GITDIR_FROM_RIG" in
    /*) GIT_DIR_GLOBAL="$GITDIR_FROM_RIG" ;;
    *)  GIT_DIR_GLOBAL="$RIG_DIR/$GITDIR_FROM_RIG" ;;
  esac
fi
if [ -z "$GIT_DIR_GLOBAL" ] || [ ! -d "$GIT_DIR_GLOBAL" ]; then
  die "missing rig git checkout: $RIG_DIR (no .git, .repo.git, or git working tree)"
fi
log "using git dir: $GIT_DIR_GLOBAL"

# Helper: run git against the resolved GIT_DIR_GLOBAL. With --git-dir set, git
# does not search parent dirs and works equally well for working trees and bare
# repos.
git_in_rig() { git --git-dir="$GIT_DIR_GLOBAL" "$@"; }

command -v "$BD_BIN" >/dev/null 2>&1 || die "beads CLI '$BD_BIN' not on PATH"
command -v jq >/dev/null 2>&1 || die "jq is required"

# Resolve the tip to scan from. Order:
#   1. --since SHA (explicit override)
#   2. state file (last successful run)
#   3. <remote>/<branch> (fresh scan from the current remote tip)
if [ -n "${STATE_OVERRIDE_TIP:-}" ]; then
  TIP="$STATE_OVERRIDE_TIP"
  log "tip from --since: $TIP"
elif [ "${RESET_STATE:-0}" = 1 ]; then
  TIP="$REMOTE/$BRANCH"
  log "tip from --reset-state: $TIP"
elif [ -f "$STATE_FILE" ]; then
  TIP="$(cat "$STATE_FILE" 2>/dev/null || true)"
  if [ -n "$TIP" ] && [ "$TIP" != "$(git_in_rig rev-parse "$REMOTE/$BRANCH" 2>/dev/null)" ]; then
    log "tip from state file: $TIP (current remote tip differs)"
  else
    log "tip from state file matches current remote tip — nothing new to scan"
    exit 0
  fi
else
  TIP="$REMOTE/$BRANCH"
  log "tip from remote (no state): $TIP"
fi

# Resolve range: NEW..TIP where NEW = TIP~MAX_SCAN, but cap at the state-file tip.
REMOTE_TIP="$(git_in_rig rev-parse "$REMOTE/$BRANCH" 2>/dev/null || true)"
[ -n "$REMOTE_TIP" ] || die "cannot resolve $REMOTE/$BRANCH in $RIG_DIR (git_dir=$GIT_DIR_GLOBAL)"

if [ "$TIP" = "$REMOTE_TIP" ]; then
  log "no new commits since last scan; exit"
  exit 0
fi

# Cap the scan window at MAX_SCAN commits. Tip of the range is REMOTE_TIP;
# base is TIP (the prior scan tip). If there are more than MAX_SCAN commits
# in TIP..REMOTE_TIP, we narrow to the most recent MAX_SCAN by walking
# MAX_SCAN parents back from REMOTE_TIP.
WINDOW_BASE="$(git_in_rig rev-list --max-count=1 --skip="$MAX_SCAN" "$REMOTE_TIP" 2>/dev/null || true)"
if [ -z "$WINDOW_BASE" ] || [ "$WINDOW_BASE" = "$REMOTE_TIP" ]; then
  # Either no MAX_SCAN-th ancestor, or the chain is shorter than MAX_SCAN.
  # In that case, scan from TIP to REMOTE_TIP.
  WINDOW_BASE="$TIP"
fi

COMMIT_LIST="$(git_in_rig rev-list --reverse "$WINDOW_BASE..$REMOTE_TIP" 2>/dev/null || true)"
if [ -z "$COMMIT_LIST" ]; then
  log "no commits in range $WINDOW_BASE..$REMOTE_TIP; updating state to $REMOTE_TIP"
  [ "$DRY_RUN" = 1 ] || printf '%s\n' "$REMOTE_TIP" >"$STATE_FILE"
  exit 0
fi
COMMIT_COUNT="$(printf '%s\n' "$COMMIT_LIST" | wc -l | tr -d ' ')"
log "scanning $COMMIT_COUNT commits in $WINDOW_BASE..$REMOTE_TIP"

# Build set of (commit, tree) pairs that have a refinery gate_complete status=merge
# event. This is the authoritative "this went through refinery" proof.
TELEM_FILES=("$REFINERY_TELEM_DIR"/refinery-gate-*.jsonl)
shopt -s nullglob
ATTESTED_COMMITS_TMP="$(mktemp)"
ATTESTED_TREES_TMP="$(mktemp)"
trap 'rm -f "$ATTESTED_COMMITS_TMP" "$ATTESTED_TREES_TMP" "${COMMITS_TMP:-}"' EXIT
if [ "${#TELEM_FILES[@]}" -gt 0 ]; then
  jq -rs '
    [ .[]
      | select(.event == "gate_complete")
      | select(.status == "merge")
      | { commit: (.commit // ""), tree: (.tree // ""), attested_tree: (.attested_tree // "") }
    ]
    | unique_by(.commit)
    | .[]
    | select(.commit != "" or .tree != "")
    | "\(.commit)\t\(.tree)\t\(.attested_tree)"
  ' "${TELEM_FILES[@]}" >"$ATTESTED_COMMITS_TMP" 2>/dev/null || true
fi

# Also allow HOOK_TELEM_DIR as a secondary signal: a successful
# pre-receive gate_complete (status=pass) for the commit is evidence the
# deterministic gate at least ran, even if refinery-gate.sh did not
# reach status=merge. We record those but DO NOT count them as full
# refinery attestation — they are reported separately.
HOOK_PASSES_TMP="$(mktemp)"
HOOK_FILES=("$HOOK_TELEM_DIR"/gate-hook-*.jsonl)
if [ "${#HOOK_FILES[@]}" -gt 0 ]; then
  jq -rs '
    [ .[]
      | select(.event == "gate_complete")
      | select(.status == "pass")
      | .new // ""
    ]
    | unique
    | .[]
    | select(. != "")
  ' "${HOOK_FILES[@]}" >"$HOOK_PASSES_TMP" 2>/dev/null || true
fi

# Function: test whether a given commit/tree is fully attested
is_refinery_attested() {
  local sha="$1" tree="$2"
  if [ -s "$ATTESTED_COMMITS_TMP" ]; then
    if grep -F -e "$sha"$'\t' "$ATTESTED_COMMITS_TMP" >/dev/null 2>&1; then
      return 0
    fi
    if [ -n "$tree" ] && grep -F -e "$tree"$'\t' "$ATTESTED_COMMITS_TMP" >/dev/null 2>&1; then
      return 0
    fi
  fi
  if [ -n "$tree" ] && [ -s "$ATTEST_DIR/$tree" ]; then
    return 0
  fi
  return 1
}

has_hook_pass() {
  local sha="$1"
  [ -s "$HOOK_PASSES_TMP" ] || return 1
  grep -Fx "$sha" "$HOOK_PASSES_TMP" >/dev/null 2>&1
}

# Classify and emit report rows
# Output columns (tab-separated):
#   KIND  SHA  SUBJECT  AUTHOR  TS  TREE  TELEMETRY  ATTEST  HOOK_PASS  CATEGORY
KIND="bypass"
ROWS=""
BYPASS_COUNT=0
WIP_COUNT=0
ATTESTED_COUNT=0
INCOMPLETE_COUNT=0
ALREADY_FILED=0

# Pre-load the set of existing bypass-detector-filed beads to avoid duplicates.
# We key on the SHA in the bead title prefix.
EXISTING_TITLES_TMP="$(mktemp)"
if [ "$FIX" = 1 ] || [ "$VERBOSE" = 1 ]; then
  "$BD_BIN" list --status=open --json 2>/dev/null \
    | jq -r '.[].title // empty' \
    | grep -E '^(Refinery bypass detected|BYPASS:|gate-bypass:)' \
    >"$EXISTING_TITLES_TMP" || true
fi

while IFS= read -r sha; do
  [ -n "$sha" ] || continue
  # Skip WIP commits — they are auto-checkpoints that intentionally bypass the
  # refinery gate (see internal/refinery/batch.go WIP rejection logic).
  SUBJECT="$(git_in_rig log -1 --format='%s' "$sha" 2>/dev/null || true)"
  case "$SUBJECT" in
    "WIP:"*|"wip:"*|"WIP "*|"wip "*)
      KIND="wip-skip"
      WIP_COUNT=$((WIP_COUNT + 1))
      log "skip WIP commit $sha: $SUBJECT"
      continue
      ;;
  esac

  TREE="$(git_in_rig rev-parse "$sha^{tree}" 2>/dev/null || true)"
  AUTHOR="$(git_in_rig log -1 --format='%an' "$sha" 2>/dev/null || true)"
  TS="$(git_in_rig log -1 --format='%aI' "$sha" 2>/dev/null || true)"

  if is_refinery_attested "$sha" "$TREE"; then
    KIND="attested"
    ATTESTED_COUNT=$((ATTESTED_COUNT + 1))
    ROW="${KIND}	${sha}	${SUBJECT}	${AUTHOR}	${TS}	${TREE}	full	yes	$(has_hook_pass "$sha" && echo yes || echo no)"
  else
    HOOK_PASS="no"
    has_hook_pass "$sha" && HOOK_PASS="yes"
    if [ -n "$TREE" ] && [ -s "$ATTEST_DIR/$TREE" ]; then
      # HMAC exists but no telemetry merge event: attest present, telemetry missing.
      KIND="attest-only"
      INCOMPLETE_COUNT=$((INCOMPLETE_COUNT + 1))
      ROW="${KIND}	${sha}	${SUBJECT}	${AUTHOR}	${TS}	${TREE}	missing	yes	${HOOK_PASS}"
    elif [ "$HOOK_PASS" = "yes" ]; then
      # Pre-receive hook ran (pass) but refinery gate_complete status=merge is missing.
      KIND="hook-only"
      INCOMPLETE_COUNT=$((INCOMPLETE_COUNT + 1))
      ROW="${KIND}	${sha}	${SUBJECT}	${AUTHOR}	${TS}	${TREE}	missing	no	yes"
    else
      # No telemetry merge event AND no HMAC token → full bypass
      KIND="bypass"
      BYPASS_COUNT=$((BYPASS_COUNT + 1))
      ROW="${KIND}	${sha}	${SUBJECT}	${AUTHOR}	${TS}	${TREE}	missing	no	no"
    fi
  fi

  ROWS="${ROWS}${ROW}"$'\n'
done <<<"$COMMIT_LIST"

# Print the report
printf 'bypass-detector report rig=%s branch=%s/%s commits=%d bypasses=%d wip_skipped=%d attested=%d incomplete=%d\n' \
  "$RIG" "$REMOTE" "$BRANCH" "$COMMIT_COUNT" "$BYPASS_COUNT" "$WIP_COUNT" "$ATTESTED_COUNT" "$INCOMPLETE_COUNT"
if [ -n "$ROWS" ]; then
  printf '%s\n' "$ROWS" | awk -F '\t' '
    BEGIN { printf "%-12s %-12s %-60s %-20s %-25s %-12s %-12s %-10s %-9s\n", "kind", "sha", "subject", "author", "ts", "tree", "telemetry", "hmac", "hook_pass" }
    {
      subj = $3; if (length(subj) > 58) subj = substr(subj,1,55) "..."
      printf "%-12s %-12s %-60s %-20s %-25s %-12s %-12s %-10s %-9s\n", $1, substr($2,1,12), subj, substr($4,1,20), $5, substr($6,1,12), $7, $8, $9
    }
  '
fi

log "summary bypasses=$BYPASS_COUNT wip=$WIP_COUNT attested=$ATTESTED_COUNT incomplete=$INCOMPLETE_COUNT"

# File audit beads in --fix mode (unless --dry-run, which suppresses ALL side effects).
if [ "$FIX" = 1 ] && [ "$BYPASS_COUNT" -gt 0 ] && [ "$DRY_RUN" != 1 ]; then
  FILED=0
  while IFS= read -r row; do
    [ -n "$row" ] || continue
    KIND="$(printf '%s' "$row" | awk -F '\t' '{print $1}')"
    [ "$KIND" = "bypass" ] || continue  # only file for full bypass; incomplete is reported separately
    SHA="$(printf '%s' "$row" | awk -F '\t' '{print $2}')"
    SUBJECT="$(printf '%s' "$row" | awk -F '\t' '{print $3}')"
    AUTHOR="$(printf '%s' "$row" | awk -F '\t' '{print $4}')"
    TS="$(printf '%s' "$row" | awk -F '\t' '{print $5}')"
    TREE="$(printf '%s' "$row" | awk -F '\t' '{print $6}')"

    SHORT="${sha:0:12}"
    TITLE="Refinery bypass detected: ${SHORT} ${SUBJECT}"
    # Avoid duplicate filing
    if grep -Fx -e "$TITLE" "$EXISTING_TITLES_TMP" >/dev/null 2>&1; then
      ALREADY_FILED=$((ALREADY_FILED + 1))
      log "skip duplicate bead for $SHORT (already filed)"
      continue
    fi

    BODY=$(cat <<EOF
Refinery-gate bypass detected by gt-refinery-bypass-detector.sh.

Evidence:
- Rig:               ${RIG}
- Branch:            ${REMOTE}/${BRANCH}
- Commit:            ${SHA}
- Tree:              ${TREE}
- Subject:           ${SUBJECT}
- Author:            ${AUTHOR}
- Authored at:       ${TS}
- Refinery telemetry gate_complete status=merge: missing
- HMAC token at ${ATTEST_DIR}/<tree>:            missing
- Pre-receive hook gate_complete status=pass:     no

This is a P1/P2 audit gap: the strict-core refinery path (refinery-gate.sh ->
run-all-gates -> 3-peer PASS -> Opus verify -> HMAC attestation -> merge) was
NOT followed for this commit. Per the mayor policy, any non-WIP commit landing
on ${REMOTE}/${BRANCH} without both telemetry merge evidence AND HMAC
attestation is a policy violation and must be retro-gated or rolled back.

Filed by: gt-refinery-bypass-detector.sh (auto)
Related:  gastown-a37 (17-commit no-telemetry investigation)
          gastown-66y (35-commit rollup)
          gastown-cet.12.4..12.14 (retro-gate shards)
Parent:   this is itself an audit-gap bead; for coordination, see gastown-a37.
EOF
)
    BODY_FILE="$(mktemp)"
    printf '%s\n' "$BODY" >"$BODY_FILE"

    if BEAD_ID="$("$BD_BIN" create \
          --title="$TITLE" \
          --type=bug \
          --priority=2 \
          --label=bypass-detector \
          --label=refinery-gate \
          --label=auto-filed \
          --body-file="$BODY_FILE" 2>&1)"; then
      FILED=$((FILED + 1))
      log "filed bead for $SHORT: $BEAD_ID"
    else
      warn "failed to file bead for $SHORT: $BEAD_ID"
    fi
    rm -f "$BODY_FILE"
  done <<<"$(printf '%s' "$ROWS" | grep -E '^bypass	' || true)"

  log "fix-mode filed $FILED new beads (skipped $ALREADY_FILED duplicates)"
fi

# Update state file (unless --dry-run or in --fix mode without --auto-ack)
if [ "$DRY_RUN" = 1 ]; then
  log "dry-run: not updating state file"
elif [ "$FIX" = 1 ] && [ "$AUTO_ACK" != 1 ]; then
  log "fix-mode without --auto-ack: not updating state file (operator must inspect)"
else
  printf '%s\n' "$REMOTE_TIP" >"$STATE_FILE"
  log "state updated to $REMOTE_TIP"
fi

# Exit code: 0 if no bypasses, 1 if bypasses detected (regardless of fix mode).
if [ "$BYPASS_COUNT" -gt 0 ]; then
  exit 1
fi
exit 0