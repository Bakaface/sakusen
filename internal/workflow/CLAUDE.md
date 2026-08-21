# internal/workflow — Task Execution Engine

Step execution, template resolution, step context capture, merge/conflict handling. Load `/workflow` skill before making substantial changes.

## Critical Invariants

- **Step context comes from the agent's result text, stored in DB after each step** — headless steps read it from `$SAKUSEN_RESULT_FILE` (stdout-tail fallback); tmux steps with `summarize_chat` get their chat via the agent record's `chat_log_command`. Synchronous capture; step index persisted
- **The tmux sentinel convention is generic** — the engine exports `SAKUSEN_DONE_DIR` (`StepDoneDir`) and `SAKUSEN_DONE_PREFIX` (`SentinelPrefix(step)`); whatever runs in the session (a hook, the agent, an idle-watcher) signals turn-end by writing `$SAKUSEN_DONE_DIR/$SAKUSEN_DONE_PREFIX-<ts>.json`, whose optional JSON payload may carry `session_id`/`transcript_path`. Core installs NO Stop hook — hookless tmux agents are manual-advance
- **The merge invariant lives in `internal/merge`** — serialization, conflict retry, target-clean wait, and cleanup-on-failure are owned by `*merge.Coordinator`; the engine only calls `e.coord.Finalize(ctx, t, baseBranch, logFn)`
- **Non-worktree mode skips branch creation, uses project root** — `on_complete` falls back to `Commit()` (or no-op) instead of merge; the Coordinator enforces this when `t.Worktree == false`
- **`{{track.*}}` vars are explicit opt-in and read LIVE per step** — `runStep` re-reads the task's track chain (`GetTrackChain`, root-first) at every step launch so mid-run track context updates reach later steps; retries may therefore render different prompts than the original run (accepted). Trackless tasks resolve all `{{track.*}}` vars to `""`; track context is never injected into a prompt automatically.
