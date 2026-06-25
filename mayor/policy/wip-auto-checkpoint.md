# WIP Auto-Checkpoint Policy

> **Scope:** This policy governs how the `checkpoint_dog` patrol and the
> Refinery merge queue handle automatic work-in-progress (WIP) checkpoints.

## 1. What a WIP checkpoint is

A WIP checkpoint is an automatic git commit created by the `checkpoint_dog`
to preserve in-flight work against session crashes, context-window limits, or
accidental shutdowns. Its commit message has the form:

```text
WIP: checkpoint (auto)
```

The content of a WIP checkpoint is real work — source code, tests, or docs —
but the **message is not a conventional commit**. WIP checkpoints are recovery
artifacts, not mergeable units of history.

## 2. WIP checkpoints must never land on `main`

WIP checkpoints are forbidden on the repository's default branch (usually
`main`). They would:

- Pollute the permanent commit history with misleading messages.
- Bypass the branch → MR/review → merge-queue flow.
- Undermine the four-model refinery gate, which treats conventional commit
  messages as part of the merge contract.

If a checkpoint has to be created while a polecat is on `main`, the
`checkpoint_dog` must first create or reset a dedicated feature branch
`wip/<polecat>` and switch the worktree to it before committing.

## 3. Auto-checkpoint plumbing

The `checkpoint_dog` (`internal/daemon/checkpoint_dog.go`) enforces this rule:

1. Before staging any changes, resolve the current branch.
2. If the current branch is the repository default branch, run
   `git checkout -B wip/<polecat> <default-branch>`.
3. Commit the checkpoint on `wip/<polecat>`.

This keeps the default branch tip unchanged and leaves real landing commits for
`gt done`, which produces a conventional commit and submits the result through
the Refinery merge queue.

## 4. Refinery convention gate

As a defense-in-depth measure, the Refinery rejects any MR whose HEAD commit
message starts with `WIP:` or `WIP ` at squash-merge time. The rejection
happens in two places:

- `internal/refinery/engineer.go` (`doMerge`) for single-MR processing.
- `internal/refinery/batch.go` (`BuildRebaseStack`) for batched merges.

The affected MR is removed from the current batch and recorded as a conflict
(queued for retry), and the target branch is not polluted.

## 5. Adherence

Polecats should not manually push or merge branches whose HEAD commit is a WIP
checkpoint. If a WIP branch reaches the merge queue, the Refinery will reject
it and the polecat must run `gt done` or otherwise rewrite the branch tip to a
conventional commit before the branch can land.
