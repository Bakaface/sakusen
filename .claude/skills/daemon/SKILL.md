---
name: daemon
description: >
  Sakusen's background daemon: Unix socket server, request handlers, client-server protocol,
  task polling, agent lifecycle, and event broadcasting. Use when editing files in
  internal/daemon/, working on server startup, message handling, task scheduling,
  agent state changes, or pub/sub broadcasting.
---

# Daemon Architecture

## Server Structure

```go
type Server struct {
    cfg      *config.Config
    listener net.Listener        // Unix socket
    manager  *agent.Manager      // Concurrent agent execution
    database *db.DB
    notifier *notify.Notifier    // Desktop/sound notifications

    projectsMu sync.RWMutex
    projects   map[int64]*projectContext  // Lazy-loaded per-project config+engine

    // Per-repo merge serialization is delegated to internal/merge. The daemon
    // hands out per-repo *merge.Lock instances to engines via this registry,
    // so locks survive engine reconstruction on config reloads.
    mergeLocks *merge.Locks

    mu           sync.RWMutex
    clients      map[net.Conn]bool    // All connected clients
    subscribers  map[net.Conn]bool    // Pub/sub subscribers
    tmuxActivity map[int64]string     // Latest tmux activity per task ID

    // Per-task tmux auto-advance bookkeeping (idle-since timestamp + advancing flag).
    tmuxAutoState map[int64]*tmuxAutoEntry

    ctx    context.Context
    cancel context.CancelFunc
    wg     sync.WaitGroup

    shutdownOnce sync.Once
}
```

**Startup**: ensure dirs -> PID file -> Unix socket -> register agent callback -> recover orphans -> spawn `acceptLoop()` + `taskPollerLoop()` + `tmuxMonitorLoop()`

## File Map

| File | Purpose |
|------|---------|
| `server.go` | Lifecycle, connection handling, project context caching (`getProjectContext`), `mergeLocks` registry |
| `handlers_task.go` | Task CRUD & metadata: create, get, list, delete, retry, update priority/field/dependency, revert, step contexts, title generation |
| `handlers_agent.go` | Agent ops, subscriptions, logs: list/start/stop agents, get output, subscribe/unsubscribe, get logs |
| `handlers_continue.go` | Continuation flow: continue/finalize tasks, worktree/branch management, tmux setup, detach/attach branch |
| `agent_spawn.go` | `spawnInteractiveSession(pc, t, interactiveSpawnOpts)` — shared ad-hoc tmux session spawn (continue fallback, tmux_direct, restart restore); `interactiveAgent(cfg)` resolves the tmux-mode agent record (default agent if tmux, else the `"claude-tmux"` slug, else error) |
| `handlers_workflows.go` | `list_workflows` handler — projects → flat workflow listing |
| `handlers_tracks.go` | Track ops: create/get/list tracks, set track context (CLI), own-track-only context write (MCP), `resolveTrackRef`, `trackToInfo` |
| `tmux_monitor.go` | Background tmux activity monitoring loop, broadcasts activity changes to subscribers |
| `broadcast.go` | Event broadcasting, agent state change handling |
| `protocol.go` | Message types, request/response structs |
| `poller.go` | Background polling for pending tasks |

## Protocol

See [references/protocol.md](references/protocol.md) for all message types and payload structs.

JSON + newline framing, 10MB scanner buffer. `Message{Type, Payload}` structure.

Key message types include `MsgShutdown` for graceful daemon shutdown.

## Protocol Types

### AgentInfo (protocol-facing, simplified)
```go
type AgentInfo struct {
    ID          string
    TaskID      int64
    Description string
    WorkDir     string
    State       AgentState  // pending|starting|running|waiting_for_input|completed|failed|stopped
    StartedAt   time.Time
    Error       string
}
```

Note: `AgentInfo` in protocol does NOT have `PID`, `CurrentStep`, `StepIndex`, or `Duration`. Those fields exist only on `agent.Agent` internally.

### TaskInfo
```go
type TaskInfo struct {
    ID, ProjectID                           int64
    ProjectName, ProjectPath                string    // Populated from project lookup
    Title, Description, Slug, Workflow      string
    TrackID                                 *int64    // Attached track (nil = trackless)
    Track                                   string    // Track slug (daemon-side enrichment; empty in DB-only fallback)
    Status, Priority                        string
    StepIndex, LoopIteration                int
    CurrentStep, BranchName, Branch         string
    TargetBranch, CheckoutBranch            string
    Worktree                                bool
    WorktreePath                            string
    WorktreeDetached                        bool
    ErrorMessage, Context                   string
    BlockedBy                               []int64
    Images                                  []string
    Commits                                 []string
    CreatedAt                               time.Time
    StartedAt, CompletedAt                  *time.Time
    TmuxActivity                            string
}
```

## Handler Patterns

- Handlers receive `(conn, payload)`, respond via `sendMessage()` or `sendError()`
- `handleCreateTask`: creates task + async AI title refinement via the configured `summarizer:` command (`runner.RunSync`, `SAKUSEN_PURPOSE=title`, 30s timeout); with no summarizer configured it falls back to a sanitized truncated input as title (no error). For non-worktree tasks, branch resolution is skipped.
- `handleContinueTask`: complex — resumes approval/tmux, or creates tmux for terminal tasks. Non-worktree tasks use project root as `WorktreePath`.
- `handleAdvanceTask`: resumes the engine when more workflow steps remain; on the last step fast-tracks if no changes (worktree only), otherwise runs async summarizer (StatusSummarizing) + on_complete. Non-worktree tasks skip the fast-track check.

### Adding New Message Types

1. Add constant to `protocol.go`
2. Create request/response structs
3. Add handler in the appropriate `handlers_*.go` file
4. Wire in `handleMessage()` switch

## Task Polling

`taskPollerLoop()` at configurable interval -> `checkPendingTasks()` -> `startTaskAgent()`:
1. Claim task atomically in DB
2. Get project context (lazy-loaded)
3. Determine work dir (worktree path or repo root)
4. Create runner wrapping `engine.RunTask()`
5. Spawn agent via manager

## Broadcasting

`onAgentStateChange()` fires on every state transition:
- Broadcasts `MsgAgentUpdate` to subscribers
- On terminal states: updates task, fires notifications
- `checkProjectTasksDone()`: fires `AllTasksCompleted` when all project tasks terminal

## Recovery

`recoverOrphanedTasks()`: running/init -> pending, finalizing -> tmux, summarizing -> pending

## Patterns

- Use `getProjectContext()` for project-specific config (lazy-loads and caches)
- Broadcasting happens outside locks to avoid deadlocks
- Agent state change callback fires outside manager mutex
- Database ops should be atomic where possible (e.g., `ClaimTask()`)
