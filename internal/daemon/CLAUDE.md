# internal/daemon — Background Daemon

Unix socket server, request handlers, task polling, agent lifecycle. Load `/daemon` skill before making substantial changes.

## Critical Invariants

- **Project context is lazy-loaded and cached** — use `getProjectContext()`, never re-load per-operation. The rationale for keeping this as a private method (not a separate `ProjectContextStore` module) is in the doc comment above `getProjectContext()` in `server.go`. Invalidation has TWO freshness signals: the `.sortie.yml` mtime AND the tracks-workflows fingerprint (`tracksFingerprint()` over `.sortie/tracks` + `~/.sortie/tracks`) — either differing evicts and reloads, so runtime-created track workflow files are picked up.
- **Per-repo merge serialization is owned by `internal/merge`** — the daemon hands out `*merge.Lock` instances via `s.mergeLocks` (a `*merge.Locks` registry) to each Engine; handlers must never reach for raw mutexes.
- **Broadcasting happens outside locks** — agent state change callbacks fire after releasing mutexes to prevent deadlocks.
- **Task lifecycle transitions are serialized per task** — advance/continue-from-pause must go through `taskFlowLock()` and re-read the task under the lock; never trust a caller's status snapshot for a check-then-act transition.
- **A pause is only genuine if the engine signalled it** — the engine's pause callback (wired in `getProjectContext` via `SetPauseCallback`) records into `Server.enginePaused`; `onAgentStateChange` must consume that signal before treating a pause-looking status as "awaiting approval", otherwise it finalizes. A status rollback on failed `StartAgent` must never restore a pause status when the failure is `agent.ErrTaskAlreadyTracked`.
