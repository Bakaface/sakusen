# internal/task — Task Lifecycle

Task model, status state machine, priority system. Load `/task-lifecycle` skill before making substantial changes.

## Critical Invariants

- **Status transitions via `db.UpdateTaskStatus()`, not direct field assignment** — ensures persistence and broadcast
- **Title refinement is async during `init`** — if AI title generation fails, fallback to sanitized input
- **Worktree defaults to `true`; when `false`, task runs in project root** — affects all git/merge operations downstream
