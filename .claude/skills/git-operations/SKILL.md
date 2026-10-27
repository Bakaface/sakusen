---
name: git-operations
description: >
  Sakusen's git layer (internal/git/): worktree create/ensure/checkout/remove,
  isolated parallel-branch worktrees, commit, fast-forward merge and rebase,
  conflict helpers, and branch operations. Use when editing files in
  internal/git/, working on worktree lifecycle or cleanup, branch creation or
  deletion, merge/rebase plumbing, or isolated parallel branch worktrees.
---

# Git Operations

Every git call is `exec.Command("git", ...)` with `Dir` set; there is no library binding.
`Repo` holds only the repo root, so callers build one per call site.

## File Map

| File | Purpose |
|------|---------|
| `repo.go` | `Repo`, `NewRepo()`, `Root()`, `worktreePath()` — the single definition of the worktree path convention |
| `worktree.go` | `CreateWorktree()`, `EnsureWorktreeAt()`, `CheckoutWorktree()`, `RemoveWorktree()`, `ListWorktrees()`, `CleanupWorktrees()`, `BranchExists()`, `FetchAndTrackBranch()`, `GetDefaultBranch()` (`symbolic-ref` → main/master → HEAD), detach/reattach HEAD, `CheckoutBranch()`, `IsGitRepo()`, `GetRepoRoot()` |
| `operations.go` | `Commit()`, `HasChanges()`, `ChangedPaths()`, `HasMeaningfulChanges()`, `MergeBranch()` (ff-only), `RebaseBranch()` (auto-abort), `RebaseInto()` / `GetConflictedFiles()` / `ContinueRebase()` / `AbortRebase()` (conflict flow), `DeleteBranch()` / `ForceDeleteBranch()`, `ListLocalBranches()`, `DiffStat()`, `GetLastCommitHash()`, `RevertCommits()`, commit-message helpers |
| `errors.go` | `ErrWorktreeExists`, `ErrNotAWorktree`, and `classifyGitErr()` — the one place git stderr is matched into a sentinel |

## Worktree Path Convention

`<repoRoot>/.sakusen/worktrees/<branch with slashes as dashes>`, resolved through
`Repo.worktreePath()` by both `CreateWorktree` and `CheckoutWorktree`. With no branch name,
`CreateWorktree` derives `sakusen-task-<id>` (`WorktreePrefix`).

## Creating Worktrees

| Function | Existing worktree at the path | Branch |
|---|---|---|
| `CreateWorktree(taskID, base, branch)` | Removed first | Created from `base` (default branch when empty); on `ErrWorktreeExists` falls back to checking out the existing branch |
| `EnsureWorktreeAt(path, branch, baseCommit)` | Reused untouched (`created == false`) | Surviving branch: prune, then re-add the worktree on it. Otherwise created from `baseCommit` |
| `CheckoutWorktree(taskID, branch)` | Removed first | Must exist locally or on origin (`FetchAndTrackBranch`) |

`EnsureWorktreeAt` takes an explicit path and never discards work, which is what lets an
isolated branch survive retries and loop-backs. Its "existing" test is `isWorktreeRoot` (the path
is the top level of a checkout), so a plain directory nested in another checkout does not count.

## Removing Worktrees and Branches

- `RemoveWorktree(path)` runs `git worktree remove --force`, tolerates `ErrNotAWorktree`, then
  `os.RemoveAll`s whatever is left.
- `CleanupWorktrees()` is `git worktree prune`. Call it before deleting a branch whose worktree
  was removed: git refuses to delete a branch it still thinks is checked out.
- `ForceDeleteBranch()` is `git branch -D`; `DeleteBranch()` is `-d` (fails if unmerged).
  Sakusen only deletes branches it created — callers gate on `t.CheckoutBranch == ""`.

## Isolated Parallel Branches (`isolation: worktree`)

An isolated parallel group (`internal/workflow/parallel.go`) gives each branch its own checkout,
outside the path convention:

- Worktree `<task worktree>-<name>` (`IsolatedBranchWorktreePath`) on git branch
  `<task branch>-<name>` (`IsolatedBranchGitBranch`), created with `EnsureWorktreeAt` from the
  task worktree's HEAD (`GetLastCommitHash`). Uncommitted task-worktree changes are not carried
  over.
- A successful branch is committed with `Repo.Commit(branchWorktree, "sakusen: task #N branch <name>")`.
  `Commit` stages everything and is a no-op on a clean tree; a failed branch is never committed.
- Cleanup goes through `workflow.RemoveIsolatedBranchWorktrees` at both task-cleanup sites:
  `cleanupMergedWorktree` (`internal/workflow/merge.go`, after a merge) and
  `cleanupWorktreeAndBranch` (`internal/daemon/handlers_continue.go`, on task delete, task cleanup and the no-change fast-track). It removes each
  branch worktree, prunes, then force-deletes the branch git branches only when the task has no
  `CheckoutBranch`. Paths come from the workflow config and the task, not DB rows, so renaming or
  removing an isolated branch in config orphans its old worktree.
- Template vars: `{{branches.<name>.branch}}` resolves to `<task branch>-<name>` and
  `{{branches.<name>.worktree}}` to the branch worktree path; both are `""` for unknown names and
  shared-group branches. This is how a later pick step finds a branch to `git merge`.
- Worker accounting: branches acquire slots from `agent.Manager.AcquireBranchSlot` via the
  engine's `SetBranchSlots` seam. A task's first held slot borrows its own `max_workers` slot, so
  `max_workers: 1` runs branches serially without deadlock; further slots count against the
  limit, and waiting branches are served before pending tasks.
- Join policy: `require: all` stays fail-fast (the first failure cancels siblings);
  `require: N`/`any` never cancels on success and cancels the rest once N becomes unreachable.

## Critical Invariants

- **`MergeBranch()` is a bare `git merge --ff-only` in the repo root** — it never checks out a
  branch or resets; the caller ensures the root is clean and on the base branch, and git refuses
  rather than clobbering edits
- **`RevertCommits()` reverses in newest-first order** — reverse iteration ensures correct commit
  lineage
- **Non-worktree mode: no branch creation/deletion** — `on_complete` uses `Commit()`, not merge
- **Conflict helpers leave the rebase in progress on purpose** — `RebaseInto` does not abort; the
  workflow engine's conflict-resolution step runs an agent against the markers, then calls
  `ContinueRebase` or `AbortRebase`
- **`HasMeaningfulChanges` takes an `excludeFiles` list** — the caller decides what to ignore
