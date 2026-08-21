---
name: git-operations
description: >
  Sakusen's git operations: worktree management, merge/rebase strategies, conflict
  resolution, branch operations, and commit message generation. Use when editing
  files in internal/git/, working on worktree creation/removal, merge logic,
  conflict handling, or conventional commit utilities.
---

# Git Operations

Two files: `operations.go` (core git commands) and `worktree.go` (worktree lifecycle).

## Worktree Management

```go
const WorktreePrefix = "sakusen-task-"

type Worktree struct {
    Path     string
    Branch   string
    RepoRoot string
}

CreateWorktree(repoRoot string, taskID int64, baseBranch, branchName string) (*Worktree, error)
CheckoutWorktree(repoRoot string, taskID int64, branchName string) (*Worktree, error)
RemoveWorktree(repoRoot, worktreePath string) error
ListWorktrees(repoRoot string) ([]string, error)   // Filters WorktreePrefix ("sakusen-task-") and "sakusen-" paths
CleanupWorktrees(repoRoot string) error             // git worktree prune
IsGitRepo(path string) bool
GetRepoRoot(path string) (string, error)
GetDefaultBranch(repoRoot string) string            // symbolic-ref -> main/master -> HEAD
BranchExists(repoRoot, branchName string) bool
FetchAndTrackBranch(repoRoot, branchName string) error
DetachWorktreeHead(worktreePath string) error
ReattachWorktreeBranch(worktreePath, branch string) error
CheckoutBranch(repoPath, branch string) error
IsWorktreeDetached(worktreePath string) bool
```

Worktree path convention: `<repoRoot>/.sakusen/worktrees/<branchName-with-slashes-as-dashes>`

`CreateWorktree` handles "already exists" by falling back to plain checkout.

`CheckoutWorktree` checks out an existing branch into a worktree, fetching from remote if not found locally.

## Core Operations

| Function | What it does |
|----------|-------------|
| `Commit(dir, msg)` | `add -A` -> status check -> `commit -m` (no-op if clean) |
| `HasMeaningfulChanges(dir, excludeFiles)` | Uncommitted + committed changes excluding caller-specified files |
| `MergeBranch(repoRoot, branch, baseBranch, commitMsg)` | Checkout baseBranch -> `merge --no-ff -m commitMsg` branch (preserves task branch commit history under the merge commit). Includes deferred `CleanRepoState()` safety net on failure. |
| `RebaseBranch(dir, baseBranch)` | Rebase with auto-abort on failure |
| `DiffStat(dir, baseBranch)` | merge-base -> `diff --stat` |
| `GetCurrentBranch(dir)` | Current branch name |
| `HasChanges(dir)` | Whether working tree has uncommitted changes |
| `CleanRepoState(repoRoot)` | Abort in-progress merge + hard reset to HEAD, verify clean |
| `ListLocalBranches(repoRoot)` | Sorted local branches excluding current branch |
| `GetLastCommitHash(dir)` | SHA of most recent commit on current branch |
| `RevertCommits(dir, commits)` | Revert each commit hash in reverse order (newest first) |

## Conflict Resolution

```go
MergeInto(dir, baseBranch)       // Leaves conflicts in progress
GetConflictedFiles(dir)          // git diff --diff-filter=U
CompleteMerge(dir)               // add -A -> commit --no-edit
AbortMerge(dir)                  // git merge --abort
```

Used by workflow engine's `resolveConflicts()` which spawns Claude to fix conflict markers.

## Branch Operations

```go
DeleteBranch(repoRoot, branch string) error       // Normal delete
ForceDeleteBranch(repoRoot, branch string) error   // Force delete
GetLastCommitMessage(workDir string) (string, error)
```

## Commit Message Utilities

- `ConventionalCommitFromTitle(title)` — freeform text -> conventional commit format
- `GetLastCommitMessage(workDir)` — most recent commit subject on the current branch

## Non-Worktree Mode

When `task.Worktree == false`, git worktree/branch operations are skipped entirely:
- No `CreateWorktree` / `RemoveWorktree` calls
- No branch resolution or deletion
- `on_complete: merge` falls back to `Commit()` in project root
- `DiffStat` against base branch may return empty if changes are already committed

## Patterns

- All git commands via `exec.Command("git", ...)` with `Dir` set
- `HasMeaningfulChanges()` takes `excludeFiles []string` — the caller decides what to exclude
- `GetDefaultBranch()`: tries `symbolic-ref`, falls back to main/master/HEAD
