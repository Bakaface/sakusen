# Daemon Protocol Reference

## Message Structure

```go
type Message struct {
    Type    MessageType     `json:"type"`
    Payload json.RawMessage `json:"payload,omitempty"`
}
```

## Client -> Server Commands

| Type | Request Struct | Purpose |
|------|---------------|---------|
| `list_agents` | — | List running agents |
| `list_tasks` | `ListTasksRequest{ProjectID, ProjectName}` | List tasks (optionally filtered) |
| `start_agent` | `StartAgentRequest{TaskID}` | Start agent for task |
| `stop_agent` | `StopAgentRequest{AgentID}` | Stop running agent |
| `create_task` | `CreateTaskRequest{Title, Description, Workflow, Priority, BranchName, TargetBranch, CheckoutBranch, ProjectPath, Worktree, BranchMode, TmuxDirect, Images, BlockedBy}` | Create new task. `Worktree` is `*bool` (nil = default true). `BranchMode` is `*int` (nil = default 0). |
| `get_task` | `GetTaskRequest{TaskID}` | Get single task |
| `delete_task` | `DeleteTaskRequest{TaskID}` | Delete task + cleanup |
| `retry_task` | `RetryTaskRequest{TaskID, StepName}` | Reset task. `StepName` (optional) restarts from a specific workflow step, preserving earlier completed step contexts. Empty `StepName` restarts from the beginning. |
| `continue_task` | `ContinueTaskRequest{TaskID, Workflow, Prompt}` | Resume/continue task |
| `advance_task` | `AdvanceTaskRequest{TaskID}` | Advance tmux-gated task (next step or finalize) |
| `get_output` | `GetOutputRequest{AgentID, FromLine}` | Agent output (paginated) |
| `get_logs` | `GetLogsRequest{TaskID, Tail, Offset}` | Task logs. `Tail` = last N lines (0 = no tail), `Offset` skips the first N (for incremental loading). |
| `send_input` | `SendInputRequest{AgentID, Input}` | Send input to agent |
| `update_priority` | `UpdatePriorityRequest{TaskID, Priority}` | Change priority |
| `update_field` | `UpdateFieldRequest{TaskID, Field, Value}` | Update title/description/context |
| `subscribe` | — | Subscribe to events |
| `unsubscribe` | — | Unsubscribe |
| `ping` | — | Health check |
| `revert_task` | `RevertTaskRequest{TaskID}` | Revert task changes |
| `detach_branch` | `DetachBranchRequest{TaskID}` | Detach worktree HEAD from branch |
| `attach_branch` | `AttachBranchRequest{TaskID}` | Reattach branch to worktree |
| `update_dependency` | `UpdateDependencyRequest{TaskID, BlockedBy, Action}` | Add/remove task dependency |
| `get_step_contexts` | `GetStepContextsRequest{TaskID}` | Get step context map (`step_name → context`) |
| `get_task_steps` | `GetTaskStepsRequest{TaskID}` | Get per-step state (ordered, includes pending placeholders for workflow steps without DB rows) |
| `update_step_context` | `UpdateStepContextRequest{TaskID, StepName, Context}` | Overwrite the captured context for a named step |
| `list_workflows` | `ListWorkflowsRequest{ProjectPath}` | List workflows for a project (flat list) |
| `create_track` | `CreateTrackRequest{Name, ParentTrack, Global, Workflow, Context, ProjectPath}` | Create a track (project-scoped unless `Global`) |
| `get_track` | `GetTrackRequest{ProjectPath, Track}` | Get a track (slug or numeric ID), its root-first ancestor chain, and the rendered `{{track.context}}` |
| `list_tracks` | `ListTracksRequest{ProjectPath}` | List tracks visible from a project (own + global) with context previews/sizes |
| `set_track_context` | `SetTrackContextRequest{ProjectPath, Track, Context, Mode}` | CLI-facing arbitrary-track context write (`Mode` = "replace" default or "append") |
| `update_task_track_context` | `UpdateTaskTrackContextRequest{TaskID, Context, Mode}` | MCP-facing own-track-only write — task must have a track and an active step |
| `shutdown` | — | Graceful shutdown |

## Server -> Client Events

| Type | Payload | Purpose |
|------|---------|---------|
| `agent_list` | `[]AgentInfo` | Agent listing response |
| `agent_update` | `AgentInfo` | Agent state change |
| `task_list` | `[]TaskInfo` | Task listing response |
| `task_update` | `TaskInfo` | Task state change |
| `output_chunk` | `OutputChunk` | Agent output lines |
| `ok` | — | Success acknowledgment |
| `error` | `ErrorResponse{Message}` | Error response |
| `tmux_activity` | `TmuxActivityResponse{TaskID, Activity}` | Tmux activity change |
| `pong` | — | Health check response |

## Key Types

```go
type AgentInfo struct {
    ID          string     `json:"id"`
    TaskID      int64      `json:"task_id"`
    Description string     `json:"description"`
    WorkDir     string     `json:"work_dir"`
    State       AgentState `json:"state"`   // pending|starting|running|waiting_for_input|completed|failed|stopped
    StartedAt   time.Time  `json:"started_at"`
    Error       string     `json:"error,omitempty"`
}

type TaskInfo struct {
    ID            int64      `json:"id"`
    ProjectID     int64      `json:"project_id"`
    ProjectName   string     `json:"project_name,omitempty"`
    ProjectPath   string     `json:"project_path,omitempty"`
    Title         string     `json:"title"`
    Description   string     `json:"description"`
    Slug          string     `json:"slug"`
    Workflow      string     `json:"workflow,omitempty"`
    TrackID       *int64     `json:"track_id,omitempty"`
    Track         string     `json:"track,omitempty"`   // track slug (daemon-side enrichment)
    Status        string     `json:"status"`
    Priority      string     `json:"priority"`
    StepIndex     int        `json:"step_index"`
    CurrentStep   string     `json:"current_step"`
    LoopIteration int        `json:"loop_iteration,omitempty"`
    BranchName       string     `json:"branch_name,omitempty"`
    Branch           string     `json:"branch"`
    TargetBranch     string     `json:"target_branch,omitempty"`
    CheckoutBranch   string     `json:"checkout_branch,omitempty"`
    Worktree         bool       `json:"worktree"`
    WorktreePath     string     `json:"worktree_path,omitempty"`
    WorktreeDetached bool       `json:"worktree_detached,omitempty"`
    ErrorMessage     string     `json:"error_message,omitempty"`
    Context          string     `json:"context,omitempty"`
    Images           []string   `json:"images,omitempty"`
    Commits          []string   `json:"commits,omitempty"`
    BlockedBy        []int64    `json:"blocked_by,omitempty"`
    CreatedAt        time.Time  `json:"created_at"`
    StartedAt        *time.Time `json:"started_at,omitempty"`
    CompletedAt      *time.Time `json:"completed_at,omitempty"`
    TmuxActivity     string     `json:"tmux_activity,omitempty"`
}
```

```go
// TrackInfo is the wire projection of a track. Context population varies by
// endpoint: create/get carry the full own context; list_tracks clears it and
// carries ContextPreview (first 200 bytes) instead. ContextLen is always set.
type TrackInfo struct {
    ID             int64      `json:"id"`
    ProjectID      *int64     `json:"project_id,omitempty"`
    Global         bool       `json:"global,omitempty"`      // project_id NULL
    ParentID       *int64     `json:"parent_id,omitempty"`
    ParentSlug     string     `json:"parent_slug,omitempty"`
    Name           string     `json:"name"`
    Slug           string     `json:"slug"`
    Workflow       string     `json:"workflow,omitempty"`
    Context        string     `json:"context,omitempty"`
    ContextPreview string     `json:"context_preview,omitempty"`
    ContextLen     int        `json:"context_len"`
    CreatedAt      time.Time  `json:"created_at"`
    UpdatedAt      time.Time  `json:"updated_at"`
}
```

Note: `AgentInfo` in the protocol is a simplified projection. Internal `agent.Agent` has additional fields (`PID`, `CurrentStep`, `StepIndex`, `Duration()`) not exposed over the wire.
