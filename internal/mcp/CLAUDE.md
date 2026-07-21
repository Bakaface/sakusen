# internal/mcp — MCP Server

Model Context Protocol server that lets Claude Code (or any MCP client) talk to a running
sortie daemon over its Unix socket. Exposes task lifecycle management, but **no
irrecoverably destructive operations**.

## Critical Invariants

- **No irrecoverable operations**: the surface is `create_task`, `get_task`, `list_tasks`,
  `list_workflows`, `retry_task`, `advance_task`, `update_task_description`,
  `update_task_dependencies`, `create_tasks_and_wait`, `wait_for_tasks`,
  `update_step_context`, `create_track`, `update_track_context`, `list_tracks`. Do not add
  `delete_task`, `revert_task`, or `cleanup` here without an explicit design decision —
  every exposed mutation must be recoverable (re-editable DB state or a re-queued task;
  `retry_task` stops only the task's own agent and preserves worktree/branch).
  `update_step_context` is admitted because the daemon enforces that it can only write to
  the calling task's currently-active step (see `handleUpdateActiveStepContext`).
  `advance_task` is admitted because the daemon only accepts it for tasks in tmux state and
  a premature advance is recoverable via `retry_task`. `update_track_context` is admitted
  because the daemon enforces it can only write the **calling task's own track** (the task
  must have a track and an active step — see `handleUpdateTaskTrackContext`). Caveat: track
  context is a **persistent cross-task prompt-injection surface** — anything written flows
  verbatim into the prompts of every future task attached to that track or its children;
  the tool descriptions warn about this.
- **Project resolution is per-call**: `resolveProjectPath()` in `project.go` falls back to
  `git rev-parse --show-toplevel` against the caller's cwd when `project_path` is omitted,
  matching how the TUI resolves projects so tasks land on the same project row.
- **Daemon connection held for the MCP process lifetime**: `Serve()` connects once at start
  (fails fast if the daemon isn't running) and reuses that `*client.Client`.

## File Map

| File | Purpose |
|------|---------|
| `server.go` | `Serve(cfg)` entry point, tool registration, MCP-result error helper |
| `project.go` | `resolveProjectPath()` — explicit → cwd → git toplevel |
| `tool_create_task.go` | `create_task` tool definition + handler |
| `tool_create_tasks_and_wait.go` | `create_tasks_and_wait` + `wait_for_tasks` tools |
| `tool_get_task.go` | `get_task` tool definition + handler |
| `tool_list_tasks.go` | `list_tasks` tool — project-scoped or global compact summaries |
| `tool_list_workflows.go` | `list_workflows` tool definition + handler |
| `tool_retry_task.go` | `retry_task` tool — full or from-step retry |
| `tool_advance_task.go` | `advance_task` tool — mark a tmux step done (next step or finalize) |
| `tool_update_task_description.go` | `update_task_description` tool definition + handler |
| `tool_update_task_dependencies.go` | `update_task_dependencies` tool — add/remove blocked_by edges |
| `tool_update_step_context.go` | `update_step_context` tool definition + handler |
| `tool_create_tasks_and_wait.go` | `create_tasks_and_wait` + `wait_for_tasks` tool definitions + handlers |
| `tool_create_track.go` | `create_track` tool definition + handler |
| `tool_update_track_context.go` | `update_track_context` tool definition + handler (own-track-only) |
| `tool_list_tracks.go` | `list_tracks` tool definition + handler |

## Conventions

- Tool-level errors return `(mcp.NewToolResultError(...), nil)` — never propagate as transport
  errors. Use `resultErr()` from `server.go`.
- Keep each tool's argument schema co-located with its handler.
