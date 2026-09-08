# internal/daemon — Background Daemon

Unix socket server, request handlers, task polling, agent lifecycle. Protocol is JSON + newline framing (10MB scanner buffer) with a `Message{Type, Payload}` envelope; `protocol.go` is the source of truth for message types and payload structs.

## Critical Invariants

- **Project context is lazy-loaded and cached** — use `getProjectContext()`, never re-load per-operation. The rationale for keeping this as a private method (not a separate `ProjectContextStore` module) is in the doc comment above `getProjectContext()` in `server.go`. Invalidation has TWO freshness signals: the `.sakusen.yml` mtime AND the tracks-workflows fingerprint (`tracksFingerprint()` over `.sakusen/tracks` + `~/.sakusen/tracks`) — either differing evicts and reloads, so runtime-created track workflow files are picked up.
- **Per-repo merge serialization is owned by `internal/merge`** — the daemon hands out `*merge.Lock` instances via `s.mergeLocks` (a `*merge.Locks` registry) to each Engine; handlers must never reach for raw mutexes.
- **Broadcasting happens outside locks** — agent state change callbacks fire after releasing mutexes to prevent deadlocks.
- **Task lifecycle transitions are serialized per task** — advance/continue-from-pause must go through `taskFlowLock()` and re-read the task under the lock; never trust a caller's status snapshot for a check-then-act transition.
- **Ad-hoc interactive tmux sessions go through `agent_spawn.go`** — `spawnInteractiveSession(pc, t, interactiveSpawnOpts)` is the single implementation behind terminal-task continue, tmux_direct setup, and daemon-restart restore (it replaced three hardcoded claude script builders); `interactiveAgent(cfg)` resolves the record to run (default agent if tmux-mode, else the conventional `"claude-tmux"` slug, else error). Restore resumes only when a chats row exists AND the agent has `resume_command`.
- **A pause is only genuine if the engine signalled it** — the engine's pause callback (wired in `getProjectContext` via `SetPauseCallback`) records into `Server.enginePaused`; `onAgentStateChange` must consume that signal before treating a pause-looking status as "awaiting approval", otherwise it finalizes. A status rollback on failed `StartAgent` must never restore a pause status when the failure is `agent.ErrTaskAlreadyTracked`.

## File Map

| File | Purpose |
|------|---------|
| `server.go` | Lifecycle (dirs → PID file → socket → agent callback → orphan recovery → `acceptLoop`/`taskPollerLoop`/`tmuxMonitorLoop`), connection handling, `getProjectContext`, `mergeLocks` registry |
| `store.go` | `taskStore` interface — the slice of `*db.DB` the server depends on; the fake-point for tests |
| `protocol.go` | Message types, request/response structs |
| `poller.go` | `taskPollerLoop` → `checkPendingTasks` → `startTaskAgent` (claim in DB, project context, work dir, runner wrapping `engine.RunTask`, spawn via manager) |
| `scheduler.go` | Periodic definitions: reconcile from each project's `.sakusen.yml`, fire due ones as ordinary tasks; startup catch-up runs separately |
| `broadcast.go` | `onAgentStateChange` → `MsgAgentUpdate` to subscribers; terminal states update the task, fire notifications, `checkProjectTasksDone` |
| `tmux_monitor.go` | Background tmux activity loop, broadcasts activity changes |
| `agent_spawn.go` | `spawnInteractiveSession` / `interactiveAgent` (see invariants) |
| `handlers_task.go` | Task CRUD & metadata: create (async title/slug via `summarizer:`), get, list, delete, retry, priority/field/dependency updates, revert, step contexts |
| `handlers_agent.go` | List/start/stop agents, output, subscribe/unsubscribe, logs |
| `handlers_continue.go` | Continue/advance/finalize, worktree/branch management, tmux setup, detach/attach branch |
| `handlers_waits_on.go` | `create_tasks_and_wait` / `wait_for_tasks`: child creation + `task_waits_on` edges; parent suspends to `StatusAwaitingChildren` |
| `handlers_cleanup.go` | Worktree/branch/log cleanup for completed/failed tasks (task ID 0 = all eligible) |
| `handlers_periodic.go` | List/get/pause/runs/fire-now for periodic definitions |
| `handlers_tracks.go` | Track create/get/list/set-context, `resolveTrackRef`, `trackToInfo` |
| `handlers_workflows.go` | `list_workflows` — flat listing across projects |

## Conventions

- Handlers receive `(conn, payload)` and respond via `sendMessage()` / `sendError()`.
- Adding a message type: constant in `protocol.go` → request/response structs → handler in the matching `handlers_*.go` → case in `handleMessage()`.
- Orphan recovery on startup: running/init → pending, finalizing → tmux, summarizing → pending.
- Non-worktree tasks skip branch resolution on create and the fast-track no-changes check on advance; `WorktreePath` is the project root.
