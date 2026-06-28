#!/usr/bin/env bash
# done-empty-hook-guard.sh — Empty-hook done guard (Issue 5 / gastown-dg1).
#
# Usage: done-empty-hook-guard.sh --rig RIG --slot SLOT [--source BEAD]
# Env:
#   GT_DONE_EMPTY_HOOK_GUARD=shadow|enforce        Guard mode (default: shadow)
#   GT_DONE_EMPTY_HOOK_OVERRIDE=1                  Bypass the guard
#   GT_RIG, GT_POLECAT                             Canonical polecat identity;
#                                                  used when cwd-based resolution
#                                                  misresolves the target.
#
# Rejects gt done from an empty hook with no durable evidence of work. When
# evidence exists (a hooked/in-progress bead, a source_bead recovered from the
# work branch, an existing MR, or refinery attestation), the guard passes.
#
# This script is intended to replace the operational dropin at:
#   $HOME/gastown-spike/dropin/gt-done-empty-hook-guard.sh
#
# Differences from the original dropin:
#   - Uses GT_RIG/GT_POLECAT to build the canonical target when available,
#     fixing the "rig/rig" misresolution that caused spurious empty_hook
#     rejections after a polecat's cwd drifted to the town/rig root.
#   - Honors the source_bead recovery field emitted by `gt hook show`, which
#     lets gt done recover when a molecule/wisp was deleted but the work
#     branch remains.
#   - Emits a specific actionable recovery path on rejection instead of only
#     printing the reason.
#
# Exit codes:
#   0 — Allow (evidence present, override set, shadow mode, or disabled)
#   1 — Block (enforce mode with no evidence)
#   2 — Usage error

set -euo pipefail

RIG=""
SLOT=""
SOURCE=""

while [ $# -gt 0 ]; do
  case "$1" in
    --rig) RIG="$2"; shift 2 ;;
    --slot) SLOT="$2"; shift 2 ;;
    --source) SOURCE="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

[ -n "$RIG" ] && [ -n "$SLOT" ] || {
  echo "usage: --rig RIG --slot SLOT [--source BEAD]" >&2
  exit 2
}

MODE="${GT_DONE_EMPTY_HOOK_GUARD:-shadow}"
[ "${GT_DONE_EMPTY_HOOK_OVERRIDE:-0}" = "1" ] && MODE="override"

TOWN="${GT_TOWN:-${GT_ROOT:-${GT_TOWN_ROOT:-/home/ubuntu/gt-town}}}"

# Prefer canonical session identity over cwd-derived slot. The wrapper derives
# rig/slot from $PWD, but gt done itself reconstructs the worktree from
# GT_RIG/GT_POLECAT. Using the canonical identity avoids the "rig/rig" target
# misresolution that stranded polecats after deleted molecules.
if [ -n "${GT_RIG:-}" ] && [ -n "${GT_POLECAT:-}" ]; then
  TARGET="$GT_RIG/polecats/$GT_POLECAT"
else
  TARGET="$RIG/$SLOT"
fi

# Probe hook show for both the cwd-derived target and the canonical session
# target. source_bead is the recovery signal added by gastown-dg1.
probe_hook_show() {
  local tgt="$1"
  timeout 2 gt hook show "$tgt" --json 2>/dev/null || true
}

hook_json="$(probe_hook_show "$TARGET")"
if [ -z "$hook_json" ] && [ -n "${GT_RIG:-}" ] && [ -n "${GT_POLECAT:-}" ]; then
  hook_json="$(probe_hook_show "$RIG/$SLOT")" || true
fi

hook_bead_id=""
hook_source=""
hook_state="empty"
if [ -n "$hook_json" ] && command -v jq >/dev/null 2>&1; then
  hook_bead_id="$(jq -r '.bead_id // ""' <<< "$hook_json")"
  hook_source="$(jq -r '.source_bead // ""' <<< "$hook_json")"
  hook_state="$(jq -r '(.status // "empty")' <<< "$hook_json")"
fi

# If a source was explicitly supplied (e.g. gt done --issue), trust it for MR
# and refinery evidence checks.
SOURCE="${SOURCE:-${hook_source:-}}"

# Evidence from the merge queue: an open MR for the source bead.
mr_list="$(timeout 3 gt mq list "$RIG" --json 2>/dev/null || true)"
has_mr_evidence=0
if [ -n "$SOURCE" ] && [ -n "$mr_list" ] && grep -qF "$SOURCE" <<< "$mr_list"; then
  has_mr_evidence=1
fi

# Evidence from refinery attestation directory keyed by source (best effort).
has_commit_evidence=0
if [ -n "$SOURCE" ]; then
  refinery_dir="$TOWN/$RIG/refinery/rig"
  if [ -d "$refinery_dir" ]; then
    commit="$(git -C "$refinery_dir" log --format=%H -n 1 --grep="$SOURCE" origin/main 2>/dev/null || true)"
    [ -n "$commit" ] && has_commit_evidence=1
  fi
fi

# Evidence from the worktree branch name. If the polecat is on a feature branch
# named after a bead, that is durable evidence even when gt hook show is empty.
cwd="$(pwd -P 2>/dev/null || pwd)"
branch=""
branch_issue=""
has_branch_evidence=0
if command -v git >/dev/null 2>&1 && [ -d "$cwd/.git" ]; then
  branch="$(git -C "$cwd" rev-parse --abbrev-ref HEAD 2>/dev/null || true)"
fi
if [ -n "$branch" ] && [[ "$branch" == polecat/*/* ]]; then
  rest="${branch#polecat/*/}"
  branch_issue="${rest%%@*}"
  if [ -n "$branch_issue" ]; then
    if [ -n "$SOURCE" ] && [ "$branch_issue" = "$SOURCE" ]; then
      has_branch_evidence=1
    elif [ -z "$SOURCE" ]; then
      # No explicit source and no hook; a named polecat branch is still
      # evidence, but we need to validate that the bead exists and is assigned.
      # source_bead from hook show already performed that validation; if it was
      # empty we conservatively do not treat the branch alone as sufficient.
      :
    fi
  fi
fi

empty_hook=0
[ "$hook_state" = "empty" ] && [ -z "$hook_bead_id" ] && [ -z "$hook_source" ] && empty_hook=1

if [ "$empty_hook" -eq 0 ] || \
   [ "$has_mr_evidence" -eq 1 ] || \
   [ "$has_commit_evidence" -eq 1 ] || \
   [ "$MODE" = "override" ]; then
  action="allow"
  if [ "$MODE" = "override" ]; then
    action="override"
  elif [ "$has_mr_evidence" -eq 1 ]; then
    action="mr_evidence"
  elif [ "$has_commit_evidence" -eq 1 ]; then
    action="commit_evidence"
  elif [ -n "$hook_source" ]; then
    action="source_bead"
  fi
  echo "done_guard_ok target=$TARGET source=${SOURCE:-} hook_state=$hook_state mode=$MODE action=$action"
  exit 0
fi

# Rejection: build an actionable recovery message. If we can infer a bead from
# the branch, tell the operator exactly how to re-hook it. Otherwise keep the
# message generic.
recovery=""
if [ -n "$branch_issue" ]; then
  recovery="gt hook $branch_issue $TARGET"
else
  recovery="gt hook <bead> $TARGET"
fi

MSG="done_guard_reject target=$TARGET source=${SOURCE:-} reason=empty_hook_no_evidence mode=$MODE recovery=\"$recovery\""
if [ "$MODE" = "enforce" ]; then
  echo "$MSG" >&2
  exit 1
fi
echo "shadow: $MSG"
exit 0
