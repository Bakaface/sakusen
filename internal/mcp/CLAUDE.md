# internal/mcp — MCP Server

Model Context Protocol server that lets Claude Code (or any MCP client) talk to a running
sakusen daemon over its Unix socket. Exposes task lifecycle management, but **no
irrecoverably destructive operations**.

## Critical Invariants

- **No irrecoverable operations**: the 21-tool surface is `create_task`,
  `create_tasks_and_wait`, `wait_for_tasks`, `wait_for_task_status`, `get_task`,
  `list_tasks`, `list_workflows`, `list_routines`, `run_routine`, `retry_task`,
  `retry_merge`, `advance_task`, `stop_task`, `continue_task`, `update_task`,
  `update_step_context`, `publish_items`, `create_track`, `get_track`, `update_track`, `list_tracks`. Do not
  add `delete_task`, `revert_task`, or `cleanup` here without an explicit design decision — every exposed mutation must be recoverable (re-editable DB
  state or a re-queued task; `retry_task` stops only the task's own agent and preserves worktree/branch).
  `retry_merge` is admitted because the daemon accepts it only for `merge-failed` tasks and
  it runs the same finalization the task would have run anyway; a repeat failure keeps the
  worktree and branch. `update_step_context` is admitted because the daemon enforces that it
  can only write to the calling task's currently-active step (see `handleUpdateActiveStepContext`).
  `publish_items` is admitted on similar grounds: the daemon accepts it only for a RUNNING step
  that a later `fanout:` step names in `from:` (see `handlePublishItems`), and a re-publish
  replaces the stored list.
  `advance_task` is admitted because the daemon only accepts it for tasks in tmux state and
  a premature advance is recoverable via `retry_task`. `stop_task` is admitted on the same
  grounds as `retry_task`: it stops only the task's own agent and deletes nothing (the task
  keeps its pre-stop status — there is no "stopped" status — so recovery is `retry_task`,
  or the daemon's restart-time orphan recovery). `update_track` is admitted because the
  daemon enforces it can only write the **calling task's own track** (the task must have a
  track and an active step — see `handleUpdateTaskTrackContext` /
  `handleUpdateTaskTrackDescription`), and both fields are plain re-editable DB values.
  Caveat: track context is a **persistent cross-task prompt-injection surface** — anything
  written flows verbatim into the prompts of every future task attached to that track or
  its children; the tool descriptions warn about this.
- **Agents must never approve a human gate**: `handleContinueTask` doubles as the
  approval-resume path for awaiting-approval/tmux tasks, so `continue_task` always sets
  `terminal_only` on the daemon request and the daemon rejects any non-terminal task
  *before* that routing. Keep the check daemon-side — an MCP-side status pre-check would be
  raceable. `advance_task`'s tmux-state-only admission is the other half of this rule.
- **Env-var defaulting**: `create_tasks_and_wait`, `wait_for_tasks`, `update_step_context`,
  `publish_items` and `update_track` default their identity args from `$SAKUSEN_TASK_ID` (and
  `update_step_context`'s / `publish_items`' `step_name` from `$SAKUSEN_STEP`) via `env.go`. Explicit
  arguments always win. This is a convenience only — the daemon's own-task/own-step/
  own-track checks are what make the tools safe, not the caller's honesty.
- **Project resolution is per-call**: when `project_path` is omitted, `resolveProjectPath()`
  in `project.go` delegates to `config.FindProjectRoot()` — the nearest ancestor of the
  caller's cwd holding a `.sakusen.yml`, bounded by the git toplevel (which is itself
  checked, and is the fallback when no marker is found). The CLI (`config.Load`) and the TUI
  go through the same helper, so tasks land on the same project row. A `.sakusen.yml` in a
  subdirectory of a repo therefore owns its own project without an explicit `project_path`.
- **`create_tasks_and_wait` children merge into the project base branch by default**; a
  per-child `merge_into_parent: true` is forwarded as `CreateTaskRequest.MergeIntoParent` and
  resolved daemon-side to the parent's branch (`task.ParentMergeTarget`) — the MCP layer
  never computes a target branch itself.
- **Daemon connection held for the MCP process lifetime**: `Serve()` connects once at start
  (fails fast if the daemon isn't running) and reuses that `*client.Client`. The one
  exception is `wait_for_task_status`, which opens a dedicated connection per call: the
  waiter consumes every broadcast on its client, so it must not share the subscription
  `create_task`'s `wait_for_ready` relies on.

## File Map

| File | Purpose |
|------|---------|
| `server.go` | `Serve(cfg)` entry point, tool registration, MCP-result error helper |
| `project.go` | `resolveProjectPath()` — explicit → nearest ancestor `.sakusen.yml` → git toplevel (via `config.FindProjectRoot`) |
| `env.go` | `resolveParentTaskID` / `resolveTaskID` / `resolveStepName` — explicit arg → engine env var |
| `tool_create_task.go` | `create_task` tool definition + handler; `jsonResult` helper |
| `tool_create_tasks_and_wait.go` | `create_tasks_and_wait` + `wait_for_tasks` tool definitions + handlers |
| `tool_wait_for_task_status.go` | `wait_for_task_status` tool — read-only any-of status wait for non-task callers (no `$SAKUSEN_TASK_ID`), over `client.WaitForTaskStatus` |
| `tool_get_task.go` | `get_task` tool definition + handler; the TaskInfo carries the fanout fields `published_items` (step → items), `fanout_children` (step, slug, task_id, status, attempts) and `fanout_parent` (task_id, step, item) |
| `tool_list_tasks.go` | `list_tasks` tool — project-scoped or global compact summaries, status/track filters, newest-first limit |
| `tool_list_workflows.go` | `list_workflows` tool definition + handler |
| `tool_retry_task.go` | `retry_task` tool — full or from-step retry |
| `tool_retry_merge.go` | `retry_merge` tool — re-run only finalization of a merge-failed task |
| `tool_advance_task.go` | `advance_task` tool — mark a tmux step done (next step or finalize) |
| `tool_stop_task.go` | `stop_task` tool — stop the task's own agent, status unchanged |
| `tool_continue_task.go` | `continue_task` tool — re-run a terminal task under a required workflow (always `terminal_only`) |
| `tool_update_task.go` | `update_task` envelope — input/title/priority/track/not_before/blocked_by edits |
| `tool_update_step_context.go` | `update_step_context` tool definition + handler |
| `tool_publish_items.go` | `publish_items` tool — items (`input`, optional `slug`/`title`/`needs`) a later `fanout:` step spawns children from; validation is daemon-side (`task.NormalizeItems`) |
| `tool_create_track.go` | `create_track` tool definition + handler |
| `tool_get_track.go` | `get_track` tool — full own context, ancestor chain, rendered `{{track.context}}` |
| `tool_update_track.go` | `update_track` envelope — own-track context and/or description |
| `tool_list_tracks.go` | `list_tracks` tool definition + handler |

## Conventions

- Tool-level errors return `(mcp.NewToolResultError(...), nil)` — never propagate as transport
  errors. Use `resultErr()` from `server.go`.
- Keep each tool's argument schema co-located with its handler.
