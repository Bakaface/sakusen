# cmd/sakusen — CLI Entry Points

Cobra-based CLI. Every subcommand is registered in an `init()` in its own file; `sakusen --help` is the authoritative list of commands and flags.

## Critical Invariants

- **Config required for non-exempt commands** — `PersistentPreRunE` enforces `.sakusen.yml` exists; only daemon subcommands (`start`, `stop`, `status`), `tui`, and completion are exempt via `noProjectRequired` map
- **Task IDs are `int64`** — parsed from positional args, not strings
- **`--no-worktree` defaults to false** — tasks get isolated worktrees by default

## File Map

| File | Contents |
|------|----------|
| `main.go` | Root command, `PersistentPreRunE`, `noProjectRequired` map |
| `action_runner.go` | `runAction`: opens a client, builds an `action.Ctx`, invokes the verb from `internal/action`, prints the result. Daemon-free verbs (e.g. `validate`) call `action.Run<Verb>` directly |
| `daemon.go` | `daemon start/stop/status` |
| `tui.go` | `tui`, `resolveProjectMode()` |
| `init.go` | `init` — scaffolds `.sakusen.yml` and agent scripts under `.sakusen/agents/` from the embedded `scaffold/` FS; never overwrites |
| `validate.go` | `validate [path]` — surfaces config errors itself (pre-run suppresses the generic load error for this command) |
| `mcp.go` | `mcp` — MCP server over stdio (`internal/mcp`) |
| `task_crud.go` | `create`, `edit`, `delete` |
| `tasks.go` | `tasks`, `start`, `stop`, `agents`/`list`, `retry`, `revert`, `continue`, `logs`, `cleanup`, `attach`, `detach`, `attach-branch` |
| `tasks_step_context.go` | `tasks step-context` — raw stored text with `--step` (pipe-safe, byte-exact), JSON map with `--all` |
| `wait_for_tasks.go` | `wait-for-tasks` — CLI/test-only surface for the `MsgWaitForTasks` RPC; agents use the MCP tools instead |
| `depends_on.go` | `depends-on add/rm/list` |
| `tracks.go` | `tracks create/list/show/set-context` |
| `routines.go` | `routines list/show/pause/resume/runs/run` — all by routine name |
| `backfill_context.go` | `backfill-context` for older tasks |
| `helpers.go` | Task table printing, truncation, shell completion for task IDs |
| `version.go` | `version` |

## Conventions

- `PersistentPreRunE` loads config into the package-level `cfg`, then requires `.sakusen.yml` unless the command is in `noProjectRequired`.
- Most commands talk to the daemon through `client.Client`; `cleanup` and `tui` open the DB directly, and `tasks` falls back to the DB when the daemon is down.
- `create`: `--target` overrides `git.base_branch`; `--checkout` reuses an existing branch and is mutually exclusive with `--branch`.
