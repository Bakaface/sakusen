# internal/workflow — Task Execution Engine

Step execution, template resolution, system prompt injection, merge/conflict handling. Load `/workflow` skill before making substantial changes.

## Critical Invariants

- **Step context captured from Claude's NDJSON result event, stored in DB after each step** — synchronous capture; step index persisted
- **The merge invariant lives in `internal/merge`** — serialization, conflict retry, target-clean wait, and cleanup-on-failure are owned by `*merge.Coordinator`; the engine only calls `e.coord.Finalize(ctx, t, baseBranch, logFn)`
- **Non-worktree mode skips branch creation, uses project root** — `on_complete` falls back to `Commit()` (or no-op) instead of merge; the Coordinator enforces this when `t.Worktree == false`
- **`{{track.*}}` vars are explicit opt-in and read LIVE per step** — `runStep` re-reads the task's track chain (`GetTrackChain`, root-first) at every step launch so mid-run track context updates reach later steps; retries may therefore render different prompts than the original run (accepted). Trackless tasks resolve all `{{track.*}}` vars to `""`. `BuildSystemPrompt` never injects track context automatically.
