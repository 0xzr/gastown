#!/usr/bin/env bash
# done-empty-hook-guard.sh — Empty-hook done guard (Issue 5 / gastown-dg1).
#
# Usage: done-empty-hook-guard.sh --rig RIG --slot SLOT [--source BEAD]
# Env:
#   GT_DONE_EMPTY_HOOK_GUARD=shadow|enforce        Guard mode (default: shadow)
#   GT_DONE_EMPTY_HOOK_OVERRIDE=1                  Bypass the guard
#   GT_RIG, GT_POLECAT                             Canonical polecat identity;
#                                                  used to resolve the canonical
#                                                  worktree when cwd has drifted.
#   GT_TOWN, GT_ROOT, GT_TOWN_ROOT                 Gas Town root directory
#                                                  (defaults to /home/ubuntu/gt-town).
#
# Rejects gt done from an empty hook with no durable evidence of work. When
# evidence exists (a hooked/in-progress bead, a source_bead recovered from the
# work branch, an open MR with an exact-matching source_issue, or
# override/shadow mode), the guard passes.
#
# This script is intended to replace the operational dropin at:
#   $HOME/gastown-spike/dropin/gt-done-empty-hook-guard.sh
#
# Differences from the original dropin:
#   - Uses GT_RIG/GT_POLECAT/GT_TOWN to locate the canonical polecat worktree,
#     fixing spurious empty_hook rejections after a polecat's cwd drifted to the
#     town or rig root. Branch evidence is read from the canonical worktree, not
#     from the process cwd.
#   - Honors the source_bead recovery field emitted by `gt hook show`, which
#     lets gt done recover when a molecule/wisp was deleted but the work
#     branch remains.
#   - MR evidence checks parse the merge-queue JSON and look up the source
#     bead by EXACT field match (or a literal substring match against the
#     description with the same needle semantics as beads.MatchesMRSourceIssue)
#     rather than a regex test(). This avoids spurious matches against unrelated
#     IDs that happen to embed the source ID (e.g. dots matching any character).
#   - Does NOT treat refinery commit-attestation files as standalone enforce-mode
#     evidence. Arbitrary non-empty files under $GT_GATE_ATTEST_DIR must not
#     satisfy the guard; durable HMAC attestation is the refinery's job.
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

# Resolve the authoritative git worktree. When GT_RIG/GT_POLECAT identify a
# polecat session, the canonical worktree is under the town root, even if the
# process cwd has drifted to the rig or town root. The canonical worktree is
# used for branch evidence (source_bead recovery) so a deleted molecule does not
# strand a polecat with no durable evidence.
canonical_worktree() {
  if [ -z "${GT_RIG:-}" ] || [ -z "${GT_POLECAT:-}" ]; then
    return 0
  fi
  local new_path old_path
  new_path="$TOWN/$GT_RIG/polecats/$GT_POLECAT/$GT_RIG"
  old_path="$TOWN/$GT_RIG/polecats/$GT_POLECAT"
  if [ -e "$new_path/.git" ]; then
    printf '%s' "$new_path"
  elif [ -e "$old_path/.git" ]; then
    printf '%s' "$old_path"
  fi
}

WORKTREE="$(canonical_worktree)"
if [ -z "$WORKTREE" ]; then
  WORKTREE="$(pwd -P 2>/dev/null || pwd)"
fi

# Probe hook show for the canonical target first, then the cwd-derived
# fallback. source_bead is the recovery signal added by gastown-dg1.
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
# evidence checks; otherwise fall back to the recovered source_bead from hook show.
SOURCE="${SOURCE:-${hook_source:-}}"

# Evidence from the merge queue: an OPEN MR whose source_issue field matches
# SOURCE EXACTLY. We use literal/field semantics matching
# beads.MatchesMRSourceIssue ("source_issue: <id>\n") so partial or
# regex-shaped IDs cannot spuriously pass (e.g. "gastown-cet.2.3" must not
# match "gastown-cetX2X3" through dot-as-any-character).
has_mr_evidence=0
if [ -n "$SOURCE" ] && command -v jq >/dev/null 2>&1; then
  mr_json="$(timeout 3 gt mq list "$RIG" --json --status=open 2>/dev/null || true)"
  if [ -n "$mr_json" ] && [ "$mr_json" != "[]" ] && [ "$mr_json" != "null" ]; then
    matched="$(jq -r --arg n "$SOURCE" \
      '[.[] | select(((.source_issue // "") == $n) or ((.description // "") | contains("source_issue: " + $n + "\n")))] | length' \
      <<< "$mr_json" 2>/dev/null || echo 0)"
    if [ "${matched:-0}" -ge 1 ]; then
      has_mr_evidence=1
    fi
  fi
fi

# Evidence from the worktree branch name. If the polecat is on a feature
# branch named after a bead, that is durable evidence even when gt hook show
# is empty — but we only count it when the bead is ASSIGNED to the expected
# agent (validated by source_bead from hook show). Without that validation
# we'd accept arbitrary branches.
branch=""
branch_issue=""
has_branch_evidence=0
# `git -C <dir>` works whether `.git` is a directory (real repo) or a file
# (worktree pointer); the command itself does the resolution. We only need
# to gate on `command -v git`, not on the filesystem shape of `.git`.
if command -v git >/dev/null 2>&1 && [ -e "$WORKTREE/.git" ]; then
  branch="$(git -C "$WORKTREE" rev-parse --abbrev-ref HEAD 2>/dev/null || true)"
fi
if [ -n "$branch" ] && [[ "$branch" == polecat/*/* ]]; then
  rest="${branch#polecat/*/}"
  branch_issue="${rest%%@*}"
  if [ -n "$branch_issue" ] && [ -n "$hook_source" ] && [ "$branch_issue" = "$hook_source" ]; then
    has_branch_evidence=1
  fi
fi

empty_hook=0
[ "$hook_state" = "empty" ] && [ -z "$hook_bead_id" ] && [ -z "$hook_source" ] && empty_hook=1

if [ "$MODE" = "override" ]; then
  echo "done_guard_ok target=$TARGET source=${SOURCE:-} hook_state=$hook_state mode=override action=override"
  exit 0
fi

if [ "$empty_hook" -eq 0 ] || \
   [ "$has_mr_evidence" -eq 1 ] || \
   [ "$has_branch_evidence" -eq 1 ]; then
  action="allow"
  if [ "$has_mr_evidence" -eq 1 ]; then
    action="mr_evidence"
  elif [ "$has_branch_evidence" -eq 1 ]; then
    action="branch_evidence"
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
