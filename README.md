# ✈ Sortie

Sortie is a daemon that orchestrates [Claude Code](https://docs.anthropic.com/en/docs/claude-code) agents through long-lived, multi-step workflows. Each task runs in its own git worktree on its own branch, advances through whatever steps you define in config — anything from a single "implement" step to a full plan/implement/review/approve/merge chain with loops and human gates — and reports back to a terminal UI where you stay in the driver's seat.

You decide what runs, how many run at once, where the human gates go, and how finished work lands on your base branch. Sortie just keeps the agents on the rails.

> ⚠️ **Early days — breaking changes expected.** Sortie is under active development. Config formats, CLI flags, and the database schema may change without notice between releases. Pin to a tagged version if you need stability.

```
┌─────────────┐    ┌────────────────┐    ┌─────────────────┐
│  sortie tui │ ←→ │ sortie daemon  │ ←→ │ Claude Code     │
│  (control)  │    │ (orchestrator) │    │ agents in       │
└─────────────┘    └────────────────┘    │ git worktrees   │
                          │              └─────────────────┘
                          ↓
                    ┌──────────┐
                    │ SQLite   │
                    │ tasks.db │
                    └──────────┘
```

## Why

- **You stay in control.** Human-approval gates pause any step until you sign off. Tmux steps drop you straight into the agent's session for back-and-forth.
- **Parallelism without conflicts.** Every task gets a dedicated git worktree and branch, so N agents can work concurrently on the same repo without stepping on each other.
- **Workflows, not one-shots.** Chain planning, implementation, review, and final-approval steps. Loop the review/implement cycle until it converges. Pass artifacts between steps.
- **It survives a reboot.** Tasks live in SQLite. Logs are persisted per step. Stop the daemon, restart it, pick up where you left off.
- **Local first.** No cloud, no telemetry. A Go binary, a Unix socket, a SQLite file under `~/.config/sortie/`.

## Install

```bash
go install github.com/Bakaface/sortie/cmd/sortie@latest
```

Requires Go 1.25+. The binary lands in `$(go env GOBIN)` (defaults to `$(go env GOPATH)/bin`) — make sure that's on your `PATH`.

To pin to a specific release, replace `@latest` with a tag (e.g. `@v0.1.0`). To verify what you installed:

```bash
sortie version
```

Building from a checkout works too:

```bash
git clone https://github.com/Bakaface/sortie && cd sortie
go build -o sortie ./cmd/sortie
```

## Quick Start

```bash
# Inside any git repo:
sortie init               # writes .sortie.yml + .sortie/ data dir
sortie daemon start       # starts the background daemon (Unix socket)
sortie tui                # opens the TUI
```

In the TUI, press `n` to create a new task, pick a workflow, and watch it run. Press `enter` on a task to follow its live logs.

To run from the command line instead:

```bash
sortie create "Add a /healthz endpoint"          # creates a pending task
sortie start <id>                                # kicks off its workflow
sortie logs <id>                                 # tails its logs
sortie tasks                                     # list, or `sortie tasks <id>` for detail
```

## How a task runs

1. **You create a task** (TUI or `sortie create`) and pick a workflow.
2. **The daemon picks it up** when a worker slot is free (`max_workers` controls concurrency).
3. **A worktree is provisioned** at `.worktrees/<branch>` on a new branch derived from `git.branch_template`. `worktree-sync-paths` and `worktree-setup-commands` run here (e.g. copy `.env`, run `bun install`).
4. **Each workflow step spawns a Claude Code agent** in that worktree with the rendered prompt and a Sortie-built system prompt. Output is parsed live (NDJSON), persisted to per-step log files, and broadcast to the TUI.
5. **Step context is captured** at the end of each step (the agent's last message by default, or a Haiku summary when `summarization_strategy: summarize_chat`) and made available to later steps via `{{steps.<name>.context}}`.
6. **Human gates pause** the workflow on `human: true` steps; **tmux gates** suspend until you detach from the agent's tmux session. **Loops** jump back to an earlier step until an exit condition is met or `max_iterations` is reached.
7. **On completion**, depending on `on_complete` (project-level, or the running workflow's override), Sortie either leaves the work as a `commit` on the branch or `merge`s it into the base branch.

## Workflow configuration

Workflows live in `.sortie.yml` at the repo root as a flat `workflows:` list. Each entry is either a string ref (resolved to `.sortie/workflows/<name>.yml`) or an inline workflow mapping.

A workflow may **pin** any subset of New Task screen fields (`input`, `worktree`, `branch`, `checkout`, `target`). Pinned fields are pre-filled and hidden from the form. If all fields are pinned the New Task screen is skipped entirely and the task is created immediately. (A separate top-level `description:` is human-readable metadata — a one-line summary surfaced in the workflow picker and via MCP `list_workflows`; it is **not** a pin and never becomes the task input.)

Minimal `.sortie.yml`:

```yaml
max_workers: 3
yolo: false                       # pass --dangerously-skip-permissions to claude

git:
  base_branch: main
  branch_template: sortie/{{task_id}}-{{task_slug}}

on_complete: merge                # commit | merge | none (per-workflow overridable)

workflows:
  - name: sensible workflow
    on_complete: commit           # optional: override the project-level on_complete
    steps:
      - name: implementing
        prompt: |
          Implement task #{{task.id}}: {{task.title}}
          {{task.input}}
        timeout: 30m

      - name: reviewing
        prompt: |
          Review the implementation.
          ## Implementation summary
          {{steps.implementing.context}}
        timeout: 20m
```

Workflows with no pins always prompt the New Task screen. Workflows that pin all fields (input + worktree + branch/checkout + target) skip the screen and create the task immediately — useful for predefined maintenance jobs or project bootstrapping pipelines:

```yaml
workflows:
  - name: housekeeping
    description: "Run standard maintenance: linting, dead code removal"   # metadata (picker/MCP)
    input: "Audit and clean the codebase: lint, remove dead code."        # pins the task input
    worktree: true
    branch: sortie/housekeeping-{{task.id}}
    target: main
    steps:
      - name: cleaning
        prompt: "Audit and clean the codebase."
        print: true
```

File pool is flat: `.sortie/workflows/<name>.yml` (no `tasks/`, `one-off/`, or `init/` subdirectories). Global pool: `~/.sortie/workflows/<name>.yml`.

### Step options

| Key | Type | Notes |
|---|---|---|
| `name` | string | Step ID, used in `{{steps.<name>.context}}` and loop targets. |
| `prompt` | string | Templated prompt sent to the agent. |
| `timeout` | duration | e.g. `30m`. Default: 30 minutes. |
| `human` | bool | Pause and wait for explicit approval in the TUI. |
| `print` | bool | `true` = headless `claude -p`; `false` (default) = tmux. Step-level overrides workflow-level. |
| `summarization_strategy` | enum | `last_message`, `summarize_chat` (default, Haiku-summarized chat log), or `none` (no context captured). |
| `loop` | object | Jump back to an earlier step. See below. |

### Loops

```yaml
- name: reviewing
  prompt: |
    Review iteration {{loop.iteration}} of {{loop.max_iterations}}.
    {{steps.implementing.context}}
    If everything passes, output nothing.
  loop:
    goto: implementing
    max_iterations: 3
    exit_condition:
      step_context_empty: reviewing   # exit early when this step's output is empty
```

Loops must point to an earlier step, can't be `human:` or run in tmux (set `print: true`), and can't overlap with other loops.

### Template variables

Available in any step `prompt`:

- `{{task.id}}`, `{{task.title}}`, `{{task.input}}`, `{{task.context}}`, `{{task.slug}}`, `{{task.branch}}`
  — `task.input` is the user-supplied task input; `task.context` is the summary written by the workflow's summarizer after the task completes; empty until then.
- `{{tasks.<id>.<field>}}` — reference another task's field by ID. Supported fields: `title`, `branch`, `input`, `context`.
  References inside the task's own `input`/`context` are pre-expanded before being inlined into a step prompt
  (single-pass; nested refs in the looked-up task's fields remain verbatim).
  At create or edit time, references are validated:
  - missing task, cross-project, failed dependency, or unsupported field → request is rejected;
  - active dependency → added automatically to `blocked_by`;
  - completed dependency → no edge added (its fields are already resolvable);
  - self-reference → resolved at runtime, but never added as a `blocked_by` edge.
  `{{tasks.<id>.context}}` is only populated after the referenced task has been summarized.
- `{{git.base_branch}}`
- `{{steps.<step_name>.context}}` — captured output of a prior step
- `{{loop.iteration}}`, `{{loop.max_iterations}}` (inside a loop body)

### Worktree provisioning

```yaml
worktree-sync-paths:
  copy: [".env", ".env.local"]      # copied into each worktree
  link: [".claude", "node_modules"] # symlinked

worktree-setup-commands:            # run sequentially after sync
  - bun install
  - bun run db:migrate

tmux-setup-command: |               # run once after tmux session creation
  tmux split-window -h "tail -f .sortie/logs/<id>/<step>.log"
```

## Project layout reset


```
cmd/sortie/         CLI entry points (daemon, tui, task CRUD)
internal/
  config/           .sortie.yml parsing, project type auto-detection
  daemon/           Background daemon: Unix socket server, scheduling, pub/sub
  workflow/         Step engine, prompt templating, summarizer, merge logic
  claude/           Claude Code process spawning, NDJSON stream parsing
  agent/            Agent state machine, concurrent worker manager
  task/             Task model, status state machine, priority, dependencies
  tui/              BubbleTea terminal UI (list, detail, prompt, animation)
  db/               SQLite persistence and migrations
  git/              Worktree, branch, merge, conflict-resolution operations
  tmux/             Tmux session lifecycle, capture, monitoring
  client/           IPC client (RPC + event subscription) for tui/cli
  notify/           Desktop notifications
claude-code-plugin/ Companion Claude Code plugin (sortie-configurer skill)
```

The daemon listens on a Unix socket at `~/.config/sortie/daemon.sock` (or `$XDG_CONFIG_HOME/sortie/`) and persists state to `tasks.db` next to it. Project-level data (logs, the `.worktrees/` directory) lives under `.sortie/` inside the repo.

## TUI

Launch with `sortie tui`. Add `-g` / `--global` to see tasks across every project Sortie has tracked.

Common keys (full help with `ctrl+h`):

| Key | Action |
|---|---|
| `j` / `k` / `↑↓` | Move selection |
| `enter` | Open task detail / follow live logs |
| `n` / `N` | New task / new blocking task |
| `c` | Continue an awaiting-approval / completed / failed task |
| `s` | Stop the running step |
| `r` / `R` | Retry / revert |
| `t` | Attach to the task's tmux session |
| `b` / `alt+b` | Branch a new task off this one / toggle branch tree view |
| `D` / `A` | Detach branch from worktree / reattach |
| `o` / `e` | Open / edit step context (artifact) |
| `dd` | Delete task (worktree + branch + logs) |
| `/`, `?`, `n`, `N` | Vim-style search and next/prev match |
| `gg`, `G`, `:N` | Jump to top, bottom, or line N |

In the detail view, `j/k/G/gg/ctrl+u/ctrl+d` scroll logs; `esc` toggles between follow and normal mode; `e` opens the log file in `$EDITOR`.

## Tmux integration

Tmux is the **default** execution mode: every step runs inside a named tmux session (`sortie/<project>/<task_id>/<step>`) hosting an interactive Claude Code TUI. The daemon installs a project-scoped Claude Code `Stop` hook so it can detect turn-end events and either auto-advance to the next workflow step or finalize the task — no human "approve" keystroke needed unless the step has `human: true`. If the Stop hook never fires (e.g. managed-settings policy disabled hooks), the daemon falls back to a tmux pane hash-stability detector with a 30 s idle threshold.

To opt into headless execution via `claude -p` (e.g. for short, deterministic steps where you don't need to watch), set `print: true`:

```yaml
workflows:
  - name: ship-it
    print: true         # workflow-level default: headless
    steps:
      - name: implement
        prompt: "..."
      - name: review
        print: false    # this step still uses tmux
        prompt: "..."
```

| `print` | `human` | Behavior |
|---|---|---|
| false (default) | false | tmux + auto-advance via Stop hook (with hash fallback) |
| false | true | tmux + manual approval (drop into the session, then press `a`/`c`) |
| true | false | headless `claude -p` + auto-advance on exit (legacy non-tmux flow) |
| true | true | headless `claude -p`, then pause at `awaiting_approval` |

Press `t` in the TUI to attach to a tmux session. Sortie detects nested-tmux situations (you're already inside tmux) and either switches client or nests a session, controlled by `tmux_nested_attach_behavior` (`switch` / `nest`).

`sortie attach <task_id>` does the same from the shell.

### Migrating from `tmux:` to `print:`

The pre-Sortie-54 `tmux:` field was removed. Inversion mapping:

- `tmux: true`  → `print: false` (or drop the line entirely — tmux is now the default)
- `tmux: false` → `print: true`

The daemon refuses to load a config containing the old `tmux:` field, with an error pointing at the new field.

## CLI reference

**Daemon**

```bash
sortie daemon start           # start (foreground; background it with '&' or your service manager)
sortie daemon stop            # graceful shutdown
sortie daemon status          # is it running, what PID
```

**Tasks**

```bash
sortie create <input> [--workflow w] [--priority high] [--title T]
              [--branch tmpl] [--target main] [--checkout existing-branch]
              [--no-worktree]
sortie tasks [<id>] [--json]  # list, or detail for one
sortie edit <id> [--title T] [--input I] [--context C] [--priority P]
sortie delete <id> [-y]
sortie start <id>             # manually kick off a pending task
sortie stop <id>              # stop a running task
sortie retry <id> [--from-step name]  # restart workflow (default) or jump to a specific step
sortie revert <id>            # revert all commits made by a completed task
sortie continue <id>          # resume an awaiting-approval / completed / failed task
sortie logs <id> [step] [-n N]
sortie cleanup [<id>]         # remove worktree + branch + logs for completed/failed
sortie agents [--json]        # list running agents
sortie depends-on add <id> <blocked-by-id>     # mark <id> as blocked by another task
sortie depends-on rm  <id> <blocked-by-id>     # remove a dependency
sortie depends-on list <id>                    # list tasks blocking <id>
```

**Worktree branch management**

```bash
sortie detach <id>            # detach branch so you can check it out elsewhere
sortie attach-branch <id>     # reattach after detach
sortie attach <id>            # attach to the task's tmux session
```

**TUI**

```bash
sortie tui [-g]               # -g for cross-project view
```

## Requirements

- Go 1.25+
- git (worktree support, ≥ 2.5)
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) CLI on `PATH` as `claude`
- tmux (only required if you use tmux steps or `sortie attach`)
