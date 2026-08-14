---
name: cli
description: >
  Sortie's CLI entry point and command registration: subcommands, flags, project
  config enforcement, and command routing. Use when editing files in cmd/sortie/,
  adding new CLI commands, modifying flags, or changing pre-run validation logic.
---

# CLI Entry Point

Cobra-based CLI split across multiple files in `cmd/sortie/`.

| File | Contents |
|------|----------|
| `main.go` | Root command, `PersistentPreRunE`, `noProjectRequired` map, `init()` registration |
| `daemon.go` | `daemon start`, `daemon stop`, `daemon status` subcommands |
| `task_crud.go` | `create`, `edit`, `delete` commands |
| `tasks.go` | `tasks`, `start`, `stop`, `list`/`agents`, `retry`, `revert`, `continue`, `logs`, `cleanup`, `attach`, `detach`, `attach-branch` commands |
| `tui.go` | `tui` command, `resolveProjectMode()` helper |
| `init.go` | `init` command (scaffolds `.sortie.yml` + user-owned agent scripts under `.sortie/agents/` from the embedded `scaffold/` FS; never overwrites existing files) |
| `validate.go` | `validate [path]` command — runs config validation, surfaces errors directly (PersistentPreRunE suppresses generic load errors for this command) |
| `mcp.go` | `mcp` command — starts the MCP server over stdio (see `internal/mcp/`) |
| `backfill_context.go` | `backfill-context` — backfills missing step contexts for older tasks |
| `version.go` | `version` — prints build version |
| `depends_on.go` | `depends-on add/rm/list <task_id> [<blocked_by_id>]` — manage task dependencies |
| `tracks.go` | `tracks create/list/show/set-context` — manage tracks (named, hierarchical context containers) |
| `helpers.go` | `taskTableRow`, `printTaskTable()`, `truncateStr()`, `completeTaskIDs()` shell completion |

## Command Registration

All subcommands registered in `init()`:

| Command | Flags | Purpose |
|---------|-------|---------|
| `daemon start` | `--foreground/-f` | Start background daemon |
| `daemon stop` | — | Stop daemon |
| `daemon status` | — | Check daemon status |
| `tui` | `--global/-g` | Launch terminal UI |
| `init` | — | Initialize `.sortie.yml` |
| `validate [path]` | — | Validate `.sortie.yml` (path optional, defaults to cwd) |
| `mcp` | — | Run MCP server over stdio for Claude Code to consume |
| `version` | — | Print build version |
| `backfill-context` | — | Backfill missing step contexts for older tasks |
| `tasks [id]` | `--json/-j` | List all tasks or show task detail |
| `start` | — | Start agent for task |
| `agents` / `list` | `--json/-j` | List running agents |
| `stop` | — | Stop running task |
| `retry` | `--from-step <name>` | Retry failed task. With `--from-step`, restart at a specific workflow step while preserving completed work from earlier steps. |
| `revert` | — | Revert all commits made by a task |
| `continue` | — | Continue task (awaiting-approval, completed, or failed) |
| `logs` | `--tail/-n` | View step logs |
| `cleanup` | — | Remove worktrees for completed/failed tasks |
| `attach` | — | Attach to tmux session |
| `detach` | — | Detach worktree branch so it can be checked out elsewhere |
| `attach-branch` | — | Reattach branch to worktree after detach |
| `create` | `--priority/-p`, `--branch/-b`, `--workflow/-w`, `--title`, `--no-worktree`, `--target`, `--checkout`, `--track` | Create task |
| `edit` | `--title/-t`, `--description/-d`, `--context/-c`, `--priority/-p` | Edit task fields |
| `delete` | `--yes/-y` | Delete task |
| `depends-on add <task_id> <blocked_by_id>` | — | Add dependency edge |
| `depends-on rm <task_id> <blocked_by_id>` | — | Remove dependency edge |
| `depends-on list <task_id>` | — | List task dependencies |
| `tracks create <name>` | `--parent`, `--global`, `--workflow/-w`, `--context/-c` | Create a track |
| `tracks list` | `--json/-j` | List tracks visible from this project (project + global) |
| `tracks show <slug-or-id>` | `--json/-j` | Show track metadata and rendered `{{track.context}}` |
| `tracks set-context <slug-or-id> [context]` | `--append` | Replace (or append to) a track's own context; reads stdin when no argument |

## Project Config Enforcement

### noProjectRequired Map

```go
var noProjectRequired = map[string]bool{
    "init": true, "help": true, "completion": true,
    "__complete": true, "__completeNoDesc": true,
    "start": true, "stop": true, "status": true,
    "validate": true, "mcp": true, "backfill-context": true, "version": true,
}
```

### PersistentPreRunE

Runs before every command:
1. Loads config via `config.Load()` into package-level `var cfg *config.Config`. The `validate` command suppresses load errors here so it can surface them itself.
2. Skips project check for daemon subcommands (`start`, `stop`, `status`) and `tui`.
3. For all other non-exempted commands, requires `.sortie.yml` to exist (returns error if missing).

## Patterns

- Most commands use `client.Client` for daemon communication
- `cleanup` and `tui` access the database directly via `db.Open()`, bypassing the daemon
- `tasks` falls back to direct DB access (`listTasksFromDB()`) when the daemon is not running
- `cleanup` modifies task state (clears worktree paths) without requiring the daemon
- Task ID arguments parsed as `int64` from positional args
- `--no-worktree` flag on `create` sets `Worktree: false` (default true)
- `--target` overrides `git.base_branch` for the task's target branch
- `--checkout` checks out an existing branch instead of creating a new one (mutually exclusive with `--branch`)
