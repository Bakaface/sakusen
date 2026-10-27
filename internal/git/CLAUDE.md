# internal/git — Git Operations

Worktree management, merge/rebase, conflict resolution, branch operations. Every git call is `exec.Command("git", ...)` with `Dir` set; there is no library binding. Load `/git-operations` skill (`.claude/skills/git-operations/SKILL.md`) before making substantial changes.

## Critical Invariants

- **`MergeBranch()` is a bare `git merge --ff-only` in the repo root** — it never checks out a branch or resets; the caller ensures the root is clean and on the base branch, and git refuses rather than clobbering edits
- **`RevertCommits()` reverses in newest-first order** — reverse iteration ensures correct commit lineage
- **Non-worktree mode: no branch creation/deletion** — `on_complete` uses `Commit()`, not merge

## File Map

| File | Purpose |
|------|---------|
| `repo.go` | `Repo` — thin wrapper over a repo root. Main-tree operations (merge, branch management, worktree lifecycle) are methods with no path argument; operations scoped to one task worktree take that worktree's path explicitly. Cheap to construct per call site |
| `worktree.go` | Worktree create/checkout/remove/list/prune, detach/reattach HEAD, default-branch detection (`symbolic-ref` → main/master → HEAD). `EnsureWorktreeAt(path, branch, baseCommit)` adds a worktree at an explicit path from a base commit: an existing worktree root is reused untouched, a missing worktree on a surviving branch is re-added (after a prune) — unlike `CreateWorktree`, which removes an existing worktree first |
| `operations.go` | Commit, meaningful-change detection, fast-forward-only merge, rebase with auto-abort, diff stat, conflict helpers (`MergeInto` / `GetConflictedFiles` / `CompleteMerge` / `AbortMerge`), revert, commit-message utilities |
| `errors.go` | Sentinels classified from git stderr: `ErrWorktreeExists` (create falls back to checking out the existing branch), `ErrNotAWorktree` (remove treats as a no-op) |

## Conventions

- Worktree path: `<repoRoot>/.sakusen/worktrees/<branch with slashes as dashes>`.
- Isolated parallel branches (`isolation: worktree`) bypass the path convention: their worktree is `<task worktree>-<name>` on branch `<task branch>-<name>`, created via `EnsureWorktreeAt` (names derived in `internal/workflow/parallel.go`). After exit 0 a branch is committed via `Repo.Commit` (`sakusen: task #N branch <name>`; no-op on a clean tree, failed branches stay uncommitted) and later steps reach it via `{{branches.<name>.branch}}` / `{{branches.<name>.worktree}}`; its slot counts against `max_workers` (the first borrows the task's own), `require: all` is fail-fast while `require: N`/`any` never cancels on success and cancels the rest once N is unreachable, and `workflow.RemoveIsolatedBranchWorktrees` removes the worktrees with the task worktree, force-deleting the branches only when `CheckoutBranch == ""`. Details in the `/git-operations` skill.
- Conflict helpers leave the merge in progress on purpose; the workflow engine's conflict-resolution step runs an agent against the markers, then calls complete or abort.
- `HasMeaningfulChanges` takes an `excludeFiles` list; the caller decides what to ignore.
