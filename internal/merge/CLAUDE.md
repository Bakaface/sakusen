# internal/merge — Per-Repo Merge Serialization

The deep module that owns the "all merges into the same repo must serialize, and Sakusen never
discards changes or switches branches in the main checkout" invariant. Consolidates logic that
previously lived across the daemon, the workflow engine, and `internal/git`.

## Critical Invariants

- **All merges go through `Coordinator.Finalize()`** — the public surface is intentionally tiny.
  Workflow engine calls `e.coord.Finalize(ctx, t, baseBranch, action, logFn)` and nothing else.
- **Per-repo serialization via a shared `*Lock`** — the daemon owns a `*Locks` registry
  (`Locks.For(repoRoot)` returns a stable per-repo Lock). Multiple Coordinators (e.g., after
  config reload) that point at the same repo share the same Lock so serialization survives.
- **Never touch user state in the main checkout** — Sakusen never discards changes or switches
  branches in the main checkout; if main is dirty or on another branch the merge waits in
  merge-blocked. Readiness (clean, including untracked files, outside `Config.IgnorePaths`, and on
  `baseBranch`) is checked before the merge and re-checked under the lock; a failed re-check
  re-enters the wait without consuming a merge attempt. The merge is `--ff-only` and conflicts are
  resolved in the task worktree, so nothing is ever left half-applied in main.
- **The merge target may be a task worktree** — when the main checkout is not on `baseBranch`,
  `targetCheckout` asks the engine-wired `TargetWorktreeLister` (`SetTargetWorktrees`; the
  engine lists the project's worktree tasks whose branch is `baseBranch`) for the task worktree
  that has it checked out, and fast-forwards THERE (a child merging into its parent's branch).
  Same lock, same invariants: never discard or switch, readiness = clean outside
  `Config.IgnorePaths` and on `baseBranch`, re-checked under the lock; verify, rebase and
  conflict resolution run in the merging task's worktree exactly as for main. The owning task
  must be idle (`acceptsMerges`: awaiting-children, awaiting-approval or terminal) — a running,
  tmux, finalizing (etc.) owner is a merge-blocked blocker, never touched. With no lister, or no
  task worktree on the branch, only the main checkout can take the merge (today's blocker).
- **`merge-blocked` means "waiting, nothing touched on main"** — the wait sets it with the
  blocker (wrong branch, or the blocking paths) as the status detail (`error_message`), and on
  success restores the prior `t.Status` with an empty detail. `Config.CleanTargetTimeout` (0 =
  forever) fails the wait with `ErrTargetDirtyTimeout`.
- **Conflict resolver is engine-supplied** — `ConflictResolver` is wired by the workflow engine
  (typically spawns a Claude agent to fix conflicts and stage). A nil resolver means conflicts
  abort the merge — never silently succeed.
- **Only a verified tip is fast-forwarded** — when `Config.VerifyCommand` is set, `merge()` runs it
  (`sh -c` in the worktree root, own process group, `Config.VerifyTimeout`) after the worktree commit
  and again after every `refreshFromBase` that moved the task tip. Failure or timeout returns a wrapped
  `ErrVerifyFailed` before any merge attempt; the daemon records it as `merge-failed`, worktree kept.
- **The commit message command never blocks a commit** — both worktree commits (`commit()` and the
  one at the top of `merge()`) take their message from `commitMessage`. With
  `Config.CommitMessageCommand` set it stages, then runs the command via `bash -c` in the worktree
  root (own process group, `Config.CommitMessageTimeout`, default 2m) with the staged diff on stdin
  (capped at `maxCommitDiffBytes`) and `SAKUSEN_TASK_ID`/`SAKUSEN_TASK_TITLE`/`SAKUSEN_WORKTREE`/
  `SAKUSEN_PROJECT_PATH`/`SAKUSEN_PURPOSE=commit_message` (+ `SAKUSEN_TRACK_ID`); the first
  non-empty stdout line is the message. Any failure, timeout or empty output logs the reason and
  falls back to `gitpkg.ConventionalCommitFromTitle`; nothing staged skips the command.
- **Non-worktree mode short-circuits** — when `task.Worktree == false`, merge is replaced by
  `commit()` (or no-op if `OnComplete` is `none`).

## File Map

| File | Purpose |
|------|---------|
| `coordinator.go` | `Coordinator`, `Config`, `Lock`, `Locks`, the `Finalize` pipeline, conflict-retry, `waitForReadyTarget`, `targetCheckout` / `targetWorktree` (target resolution), commit-recorder hooks |
| `verify.go` | Pre-merge verify command: `verify`, `runVerifyCommand`, `ErrVerifyFailed`, output tail capping |
| `commitmsg.go` | Commit message command: `commitMessage` (with fallback), `runCommitMessageCommand`, diff capping |
| `commitmsg_test.go` | Unset/used/fallback (failure, timeout, empty)/stdin/env, merge path, diff capping |
| `coordinator_test.go` | Unit tests for the pipeline + locking |
| `verify_test.go` | Verify pass/fail/timeout and re-verify after rebase |
| `task_worktree_target_test.go` | Merges into a branch checked out in a task worktree: ff, dirty/busy blocking, sibling sequence |

## Patterns

- `Finalize(ctx, t, baseBranch, action, logFn)` is the only entrypoint callers should use. The
  `action` ("commit"/"merge"/"none") is resolved per-task by the caller (workflow-level override
  falling back to the project-level `on_complete`), not stored on the Coordinator.
- `NewCoordinator(repoRoot, lock, cfg, resolveConflicts, setStatus, recordCommit,
  setStatusDetail, onBlocked)` zero-fills sensible defaults (`MaxAttempts = 3`,
  `BlockedPollInterval = 10s`, empty Lock if nil). Every callback is nil-safe.
  `setStatusDetail` (wired to `UpdateTaskStatusError`) writes the merge-blocked detail;
  `onBlocked` fires once per entry into merge-blocked (the engine posts a desktop notification).
- `Config.CleanTargetTimeout` (0 = wait forever) turns a long merge-blocked wait into a wrapped
  `ErrTargetDirtyTimeout`, which the daemon records as `merge-failed` with the worktree kept.
  `Config.IgnorePaths` are git glob pathspecs excluded from the dirtiness check
  (`git.Repo.ChangedPaths`).
- Status writes happen **outside** the per-repo lock via the `StatusSetter` callback so they
  can take DB mutexes without inversion.
