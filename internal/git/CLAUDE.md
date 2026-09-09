# internal/git — Git Operations

Worktree management, merge/rebase, conflict resolution, branch operations. Every git call is `exec.Command("git", ...)` with `Dir` set; there is no library binding.

## Critical Invariants

- **`MergeBranch()` includes deferred `CleanRepoState()` safety net** — aborts merge and hard-resets on failure to prevent corrupted state
- **`RevertCommits()` reverses in newest-first order** — reverse iteration ensures correct commit lineage
- **Non-worktree mode: no branch creation/deletion** — `on_complete` uses `Commit()`, not merge

## File Map

| File | Purpose |
|------|---------|
| `repo.go` | `Repo` — thin wrapper over a repo root. Main-tree operations (merge, branch management, worktree lifecycle) are methods with no path argument; operations scoped to one task worktree take that worktree's path explicitly. Cheap to construct per call site |
| `worktree.go` | Worktree create/checkout/remove/list/prune, detach/reattach HEAD, default-branch detection (`symbolic-ref` → main/master → HEAD) |
| `operations.go` | Commit, meaningful-change detection, merge (`--no-ff`), rebase with auto-abort, diff stat, conflict helpers (`MergeInto` / `GetConflictedFiles` / `CompleteMerge` / `AbortMerge`), revert, commit-message utilities |
| `errors.go` | Sentinels classified from git stderr: `ErrWorktreeExists` (create falls back to checking out the existing branch), `ErrNotAWorktree` (remove treats as a no-op) |

## Conventions

- Worktree path: `<repoRoot>/.sakusen/worktrees/<branch with slashes as dashes>`.
- Conflict helpers leave the merge in progress on purpose; the workflow engine's conflict-resolution step runs an agent against the markers, then calls complete or abort.
- `HasMeaningfulChanges` takes an `excludeFiles` list; the caller decides what to ignore.
