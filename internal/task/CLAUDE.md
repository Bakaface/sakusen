# internal/task — Task Lifecycle

Task model, status state machine, priority system. Load `/task-lifecycle` skill before making substantial changes.

## Critical Invariants

- **Status transitions via `db.UpdateTaskStatus()`, not direct field assignment** — ensures persistence and broadcast
- **Allowed status transitions live in one table (`transitions.go`)** — `db.UpdateTaskStatus`/`UpdateTaskError`/`UpdateTaskStatusError` check it atomically against the DB row and return `*TransitionError` on an illegal move; `db.TransitionTaskStatus(id, from, to)` is the compare-and-set form for check-then-act paths; only the `db.ResetTaskFor*` paths (retry, continue, startup recovery) bypass it. A new status write must either fit the table or extend it (and `transitions_test.go`)
- **Title refinement is async during `init`** — if AI title generation fails, fallback to sanitized input
- **Worktree defaults to `true`; when `false`, task runs in project root** — affects all git/merge operations downstream
