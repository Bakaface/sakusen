# Sortie

A daemon that orchestrates Claude Code agents to work through tasks in parallel.
Each task runs in an isolated git worktree through a configurable multi-step workflow.

## Architecture

```
cmd/sortie/          CLI entry points (daemon, tui, task CRUD)
internal/
  config/            .sortie.yml parsing, project type detection
  daemon/            Background daemon that schedules and runs tasks
  workflow/          Task execution engine, system prompt injection, template resolution
  claude/            Claude Code process spawning and output parsing
  tui/               BubbleTea terminal UI for monitoring and approval
  db/                SQLite persistence for tasks and projects
  git/               Git worktree operations
  merge/             Per-repo merge serialization (Coordinator/Locks/Lock)
  agent/             Agent state management
  task/              Task lifecycle and state transitions
  tmux/              Tmux session management for interactive tasks
  mcp/               MCP server exposing sortie tools (create_task, get_task, list_workflows,
                     create_tasks_and_wait, wait_for_tasks, update_step_context,
                     create_track, update_track_context, list_tracks)
  notify/            Notification support
  client/            Client for daemon communication
tests/e2e/           End-to-end tests (build tag `e2e`; see tests/e2e/README.md)
claude-code-plugin/  Claude Code plugin with sortie-configurer + worktree-parity-audit skills
```

## Key Concepts

- **Workflow steps** are defined in `.sortie.yml` with templated prompts (`{{task.id}}`, `{{task.title}}`, etc.)
- **`BuildSystemPrompt()`** in `internal/workflow/system-prompt.go` constructs the system prompt passed to spawned Claude agents via `--system-prompt`
- **Template resolution** (`internal/workflow/template.go`) interpolates task variables and prior step contexts into prompts
- **Git worktrees** isolate each task so parallel agents don't conflict
- **Tracks** (`tracks` table in the global DB) are named, hierarchical context containers; tasks attach at create time and step prompts opt in via `{{track.context}}` / `{{track.own_context}}`; track workflows live under `.sortie/tracks/<slug>/workflows/` (project) and `~/.sortie/tracks/<slug>/workflows/` (global) as hidden `<slug>:<name>` entries

## Development

All shortcuts are in `mise.toml` — prefer `mise run <task>` over raw `go` commands.

| Task | Command | Notes |
|------|---------|-------|
| Build everything (no install) | `go build ./...` | Compiles every package. |
| Build the `sortie` binary | `mise run build` (alias `b`) | Installs to `~/bin/sortie`. |
| Unit tests | `mise run test` → `go test ./...` | Excludes `integration`- and `e2e`-tagged files. |
| Integration tests | `mise run test:integration` (alias `ti`) → `go test -tags integration ./...` | Only `internal/claude/process_test.go` is tagged today. |
| End-to-end tests | `mise run test:e2e` → `go test -tags=e2e ./tests/e2e/...` | **Not picked up by `go test ./...`** — the build tag gates compilation. See [tests/e2e/README.md](tests/e2e/README.md). |
| Lint | `mise run lint` → `go vet ./...` + `gofmt -l .` check | No `golangci-lint` is configured. |

### Canonical "what to run after a change" rule

Before reporting work complete:

1. **Always** run `mise run test`.
2. If you touched `internal/claude/`, also run `mise run ti` (covers the `integration` build tag).
3. If you touched `internal/workflow/`, `internal/daemon/`, `internal/merge/`, `cmd/sortie/`, or anything that affects task execution end-to-end, also run `mise run test:e2e`.
4. Run `mise run lint` for any non-trivial code change.

If a test command fails or hangs, surface the failure — never silently skip a layer.

> **Do not invent commands.** There is no `make test`, no `Makefile`, no `justfile`, no `golangci-lint`. The full set of test/build/lint commands is the table above.

## Domain Context

Each package directory contains a `CLAUDE.md` with critical invariants and a pointer to the
corresponding domain skill. These auto-load when you read/edit files in that directory.

For substantial changes, load the full domain skill (referenced in each subdirectory's `CLAUDE.md`)
to get file maps, struct definitions, protocol details, and deeper conventions.

## Verifying Non-Interactive Output

The TUI cannot be run interactively from this harness. When modifying any `View()` method or layout code, you MUST verify rendering correctness programmatically — do not rely on reasoning about the math alone.

Write a standalone Go program (or test) that:
1. Constructs the component with realistic data
2. Calls `View()` or the relevant render function
3. Splits the output into lines and checks `lipgloss.Width()` on each line
4. Asserts structural properties (uniform width, border alignment, expected content)

Run this program via Bash to confirm correctness before telling the user it's fixed.

## Code Style

- Follow existing patterns in the codebase
- Keep changes minimal and focused
- For TUI code, follow the patterns documented in the `/tui` skill (BubbleTea/Lip Gloss)
