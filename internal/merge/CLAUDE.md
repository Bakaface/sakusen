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
- **`merge-blocked` means "waiting, nothing touched on main"** — the wait sets it with the
  blocker (wrong branch, or the blocking paths) as the status detail (`error_message`), and on
  success restores the prior `t.Status` with an empty detail. `Config.CleanTargetTimeout` (0 =
  forever) fails the wait with `ErrTargetDirtyTimeout`.
- **Conflict resolver is engine-supplied** — `ConflictResolver` is wired by the workflow engine
  (typically spawns a Claude agent to fix conflicts and stage). A nil resolver means conflicts
  abort the merge — never silently succeed.
- **Non-worktree mode short-circuits** — when `task.Worktree == false`, merge is replaced by
  `commit()` (or no-op if `OnComplete` is `none`).

## File Map

| File | Purpose |
|------|---------|
| `coordinator.go` | `Coordinator`, `Config`, `Lock`, `Locks`, the `Finalize` pipeline, conflict-retry, `waitForReadyTarget`, commit-recorder hooks |
| `coordinator_test.go` | Unit tests for the pipeline + locking |

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
