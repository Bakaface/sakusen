---
name: client
description: >
  Sortie's IPC client: Unix socket connection to daemon, request-response RPC,
  subscription-based event streaming, and agent output consumption. Use when editing
  files in internal/client/, working on daemon communication, task/agent RPC methods,
  or event subscription handling.
---

# Client (TUI-to-Daemon Bridge)

`internal/client/` provides the sole IPC interface between the TUI (or any consumer) and the background daemon.

## Client Struct

```go
type Client struct {
    cfg       *config.Config
    conn      net.Conn
    mu        sync.Mutex
    respChan  chan *daemon.Message  // Request-response messages
    subChan   chan *daemon.Message  // Subscription broadcast messages
    errChan   chan error
    done      chan struct{}
    closeOnce sync.Once
}

New(cfg *config.Config) *Client
Connect() error
Close() error
```

## Connection Lifecycle

1. `New(cfg)` -- creates client with config (socket path from `cfg.Daemon.SocketPath`)
2. `Connect()` -- dials Unix socket, starts background reader goroutine that routes messages to `respChan` or `subChan`
3. Use RPC methods for request-response
4. `Subscribe()` / `Unsubscribe()` to receive broadcast events via `Messages()`
5. `Close()` -- shuts down connection and channels

## RPC Methods

### Agent Operations
```go
ListAgents() ([]daemon.AgentInfo, error)
StartAgent(taskID int64) error
StopAgent(agentID string) error
GetOutput(agentID string, fromLine int) ([]string, int, error)
SendInput(agentID, input string) error
```

### Task Operations
```go
ListTasks() ([]daemon.TaskInfo, error)
ListTasksFiltered(projectID int64) ([]daemon.TaskInfo, error)
ListTasksByProjectName(name string) ([]daemon.TaskInfo, error)
GetTask(id int64) (*daemon.TaskInfo, error)
CreateTask(description, workflow, branchName, projectPath string, worktree bool, images []string) (*daemon.TaskInfo, error)
CreateTaskWithOptions(req daemon.CreateTaskRequest) (*daemon.TaskInfo, error)
RetryTask(id int64, stepName string) error  // stepName="" restarts from the beginning; non-empty restarts at that step preserving earlier contexts
RevertTask(id int64) error
ContinueTask(id int64, workflow, prompt string) error
AdvanceTask(id int64) (string, error)  // advance tmux-gated task; returns daemon outcome message
UpdateTaskPriority(id int64, priority string) error
UpdateTaskField(id int64, field, value string) error
DeleteTask(id int64) error
StopTask(id int64) error
GetLogs(id int64, tail int, offset int) ([]string, int, error)  // tail = last N lines (0 = no tail), offset skips first N for incremental loading
```

### Step Operations
```go
GetStepContexts(taskID int64) (map[string]string, error)        // step_name -> context
GetTaskSteps(taskID int64) ([]daemon.TaskStepDetail, error)     // ordered, includes pending placeholders
UpdateStepContext(taskID int64, stepName, context string) error // overwrite captured context
```

### Track Operations
```go
CreateTrack(req daemon.CreateTrackRequest) (*daemon.TrackInfo, error)
GetTrack(projectPath, ref string) (*daemon.GetTrackResponse, error)      // ref = slug or numeric ID; includes chain + rendered {{track.context}}
ListTracks(projectPath string) ([]daemon.TrackInfo, error)               // project + global, previews/sizes only
SetTrackContext(projectPath, ref, context, mode string) error            // CLI surface: arbitrary track by ref
UpdateTaskTrackContext(taskID int64, context, mode string) error         // MCP surface: calling task's own track only
```

### Workflow Discovery
```go
ListWorkflows(projectPath string) (*daemon.ListWorkflowsResponse, error)  // flat list
```

### Dependency & Branch Operations
```go
AddTaskDependency(taskID, blockedByID int64) error
RemoveTaskDependency(taskID, blockedByID int64) error
DetachBranch(id int64) error
AttachBranch(id int64) error
```

### System
```go
Ping() error
Subscribe() error
Unsubscribe() error
```

## Event Streaming

```go
Messages() <-chan *daemon.Message   // Broadcast events (agent_update, task_update, tmux_activity)
Errors() <-chan error               // Connection errors
```

After `Subscribe()`, daemon pushes real-time events. Use `ParseAgentUpdate()` to decode agent state changes:

```go
// Package-level helper
ParseAgentUpdate(msg *daemon.Message) (*daemon.AgentInfo, error)
```

## Patterns

- **All RPC methods are synchronous** (send request, wait on `respChan`). `Subscribe` uses `sendAndWait`; `Unsubscribe` uses `requestOK` (which also waits) — neither is fire-and-forget.
- Subscription events arrive on `subChan`, consumed via `Messages()`
- Background reader goroutine routes messages by type (broadcast types -> `subChan`, others -> `respChan`)
- `Close()` uses `sync.Once` to prevent double-close panics
- Error channel signals connection failures to consumer
- Internal helpers: `request()` sends and checks for error response, `requestOK()` wraps `request()` for void RPCs, `send()` is fire-and-forget without waiting for a response
