# internal/daemon — Background Daemon

Unix socket server, request handlers, task polling, agent lifecycle. Load `/daemon` skill before making substantial changes.

## Critical Invariants

- **Project context is lazy-loaded and cached** — use `getProjectContext()`, never re-load per-operation. The rationale for keeping this as a private method (not a separate `ProjectContextStore` module) is in the doc comment above `getProjectContext()` in `server.go`. Invalidation has TWO freshness signals: the `.sakusen.yml` mtime AND the tracks-workflows fingerprint (`tracksFingerprint()` over `.sakusen/tracks` + `~/.sakusen/tracks`) — either differing evicts and reloads, so runtime-created track workflow files are picked up.
- **Per-repo merge serialization is owned by `internal/merge`** — the daemon hands out `*merge.Lock` instances via `s.mergeLocks` (a `*merge.Locks` registry) to each Engine; handlers must never reach for raw mutexes.
- **Broadcasting happens outside locks** — agent state change callbacks fire after releasing mutexes to prevent deadlocks.
- **Task lifecycle transitions are serialized per task** — advance/continue-from-pause must go through `taskFlowLock()` and re-read the task under the lock; never trust a caller's status snapshot for a check-then-act transition.
- **Ad-hoc interactive tmux sessions go through `agent_spawn.go`** — `spawnInteractiveSession(pc, t, interactiveSpawnOpts)` is the single implementation behind terminal-task continue, tmux_direct setup, and daemon-restart restore (it replaced three hardcoded claude script builders); `interactiveAgent(cfg)` resolves the record to run (default agent if tmux-mode, else the conventional `"claude-tmux"` slug, else error). Restore resumes only when a chats row exists AND the agent has `resume_command`.
- **A pause is only genuine if the engine signalled it** — the engine's pause callback (wired in `getProjectContext` via `SetPauseCallback`) records into `Server.enginePaused`; `onAgentStateChange` must consume that signal before treating a pause-looking status as "awaiting approval", otherwise it finalizes. A status rollback on failed `StartAgent` must never restore a pause status when the failure is `agent.ErrTaskAlreadyTracked`.
