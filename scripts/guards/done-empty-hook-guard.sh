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
#   GT_GATE_ATTEST_DIR                             Refinery gate attestation
#                                                  directory (used for keyed
#                                                  commit evidence; defaults to
#                                                  /home/ubuntu/.gt-gate-attestations).
#
# Rejects gt done from an empty hook with no durable evidence of work. When
# evidence exists (a hooked/in-progress bead, a source_bead recovered from the
# work branch, an open MR with an exact-matching source_issue, a refinery HMAC
# attestation file for the current tree, or override/shadow mode), the guard
# passes.
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
#   - MR evidence checks parse the merge-queue JSON and look up the source
#     bead by EXACT field match (using the same needle semantics as
#     beads.MatchesMRSourceIssue) rather than raw substring grep over the
#     serialized JSON. This avoids spurious matches against unrelated MRs
#     that happen to embed the bead ID in commit messages, branches, or
#     other fields.
#   - Commit evidence checks for a keyed HMAC attestation file under
#     $GT_GATE_ATTEST_DIR/<tree-hash> instead of trusting unbounded
#     `git log --grep` substring matches. The keyed file must exist and be
#     non-empty for evidence to count.
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
ATTEST_DIR="${GT_GATE_ATTEST_DIR:-/home/ubuntu/.gt-gate-attestations}"

# Prefer canonical session identity over cwd-derived slot. The wrapper derives
# rig/slot from $PWD, but gt done itself reconstructs the worktree from
# GT_RIG/GT_POLECAT. Using the canonical identity avoids the "rig/rig" target
# misresolution that stranded polecats after deleted molecules.
if [ -n "${GT_RIG:-}" ] && [ -n "${GT_POLECAT:-}" ]; then
  TARGET="$GT_RIG/polecats/$GT_POLECAT"
else
  TARGET="$RIG/$SLOT"
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
# and refinery evidence checks; otherwise fall back to the recovered
# source_bead from hook show.
SOURCE="${SOURCE:-${hook_source:-}}"

# Evidence from the merge queue: an OPEN MR whose source_issue field matches
# SOURCE EXACTLY. We parse the JSON via jq and use the same needle semantics
# as beads.MatchesMRSourceIssue ("source_issue: <id>\n") so partial IDs
# (e.g. "gt-abc" matching "gt-abcdef") cannot spuriously pass.
has_mr_evidence=0
if [ -n "$SOURCE" ] && command -v jq >/dev/null 2>&1; then
  mr_json="$(timeout 3 gt mq list "$RIG" --json --status=open 2>/dev/null || true)"
  if [ -n "$mr_json" ] && [ "$mr_json" != "[]" ] && [ "$mr_json" != "null" ]; then
    needle="source_issue: ${SOURCE}\\n"
    matched="$(jq -r --arg n "$SOURCE" \
      '[.[] | select((.description // "") | test("source_issue: " + $n + "\n"))] | length' \
      <<< "$mr_json" 2>/dev/null || echo 0)"
    if [ "${matched:-0}" -ge 1 ]; then
      has_mr_evidence=1
    fi
    # Silence unused-variable warning while keeping the needle visible for
    # future readers.
    : "${needle:=$needle}"
  fi
fi

# Evidence from refinery HMAC attestations keyed by tree hash. The refinery
# writes one attestation file per reviewed tree under $GT_GATE_ATTEST_DIR;
# keyed-by-tree is strictly tighter than `git log --grep` substring matching
# over commit messages because:
#   - The tree hash commits to the exact reviewed content, not the message.
#   - There is no risk of a stale or unrelated commit message matching.
# We only require the file exists and is non-empty; HMAC verification is the
# refinery's job (gastown-6n7). This is best-effort evidence: a missing
# attestation simply means the durable review gate hasn't completed yet for
# this tree.
has_commit_evidence=0
if [ -n "$SOURCE" ] && [ -d "$ATTEST_DIR" ] && command -v git >/dev/null 2>&1; then
  cwd="$(pwd -P 2>/dev/null || pwd)"
  # See branch-evidence block above for why we use `-e` (not `-d`) on .git.
  if [ -e "$cwd/.git" ]; then
    tree="$(git -C "$cwd" rev-parse HEAD^{tree} 2>/dev/null || true)"
    if [ -n "$tree" ] && [ -s "$ATTEST_DIR/$tree" ]; then
      has_commit_evidence=1
    fi
  fi
fi

# Evidence from the worktree branch name. If the polecat is on a feature
# branch named after a bead, that is durable evidence even when gt hook show
# is empty — but we only count it when the bead is ASSIGNED to the expected
# agent (validated by source_bead from hook show). Without that validation
# we'd accept arbitrary branches.
cwd="$(pwd -P 2>/dev/null || pwd)"
branch=""
branch_issue=""
has_branch_evidence=0
# `git -C <dir>` works whether `.git` is a directory (real repo) or a file
# (worktree pointer); the command itself does the resolution. We only need
# to gate on `command -v git`, not on the filesystem shape of `.git`.
if command -v git >/dev/null 2>&1 && [ -e "$cwd/.git" ]; then
  branch="$(git -C "$cwd" rev-parse --abbrev-ref HEAD 2>/dev/null || true)"
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
   [ "$has_commit_evidence" -eq 1 ] || \
   [ "$has_branch_evidence" -eq 1 ]; then
  action="allow"
  if [ "$has_mr_evidence" -eq 1 ]; then
    action="mr_evidence"
  elif [ "$has_commit_evidence" -eq 1 ]; then
    action="commit_evidence"
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