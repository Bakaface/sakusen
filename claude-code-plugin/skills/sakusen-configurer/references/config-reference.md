# Sakusen Configuration Complete Reference

## Contents

- [Agents Section](#agents-section)
- [Summarizer Section](#summarizer-section)
- [Merge Conflicts Section](#merge-conflicts-section)
- [Git Section](#git-section)
- [Finalization (`on_complete`)](#finalization-on_complete)
- [Verification Section](#verification-section)
- [Poll Interval](#poll-interval)
- [Options (TUI Display)](#options-tui-display)
- [Notifications Section](#notifications-section)
- [Worktree Sync Paths](#worktree-sync-paths)
- [Worktree Setup Commands](#worktree-setup-commands)
- [Tmux Setup Command](#tmux-setup-command)
- [Prompt Includes (`.sakusen/prompts/`)](#prompt-includes-sakusenprompts)
- [Step Configuration, Loops, Cross-Task and Child References](#step-configuration-loops-cross-task-and-child-references) — moved to [workflow-building.md](workflow-building.md)
- [Task States](#task-states)
- [Task Priorities](#task-priorities)
- [Continue Workflow](#continue-workflow)
- [Legacy Config Formats (Removed)](#legacy-config-formats-removed)
- [Complete Example](#complete-example)

## Agents Section

```yaml
default_agent: claude          # slug steps fall back to; defaults to "claude" when unset

agents:
  claude:
    mode: headless             # "headless" (default) or "tmux"
    command: '"$SAKUSEN_PROJECT_PATH/.sakusen/agents/claude-headless.sh"'
  claude-tmux:
    mode: tmux
    command: '"$SAKUSEN_PROJECT_PATH/.sakusen/agents/claude-tmux.sh"'
    resume_command: 'claude --dangerously-skip-permissions --resume "$SAKUSEN_SESSION_ID"'
    chat_log_command: '"$SAKUSEN_PROJECT_PATH/.sakusen/agents/claude-chat-log.sh"'
    env:
      MY_VAR: value            # extra env for every spawn of this agent
```

Every workflow step runs one of these shell commands (via `sh -c`, in the task workdir). See SKILL.md → Agents for the full field table, the selection cascade (step `agent:` → workflow `agent:` → `default_agent:` → `"claude"`), the `SAKUSEN_*` environment contract, the tmux turn-end sentinel convention, and v1 limitations.

`agents:` and `default_agent:` may also be set in the global `~/.sakusen.yml`; project-tier records override global ones **per slug, wholesale** (records are not field-merged, and a redefinition replaces the record's `variants:` too).

An agent may declare `variants:` (variant name → partial record): each variant inherits every parent field, overrides what it redefines (`env` merges per-key, variant wins), and becomes an ordinary agent named `<parent>:<variant>` usable anywhere a slug is accepted. A ref may stack several of a parent's variants as modifiers in any order (`claude-tmux:fable:with-plugins` ≡ `claude-tmux:with-plugins:fable`); two modifiers setting the same env key or field to different values is a load error. Variants cannot nest and cannot unset a parent field. The top-level `agent_aliases:` map (alias → agent, variant, or composed ref) adds stable semantic names, merged per-key across tiers (more-local wins, so a project can re-point a global alias):

```yaml
agents:
  claude:
    command: >-
      claude -p "$(cat "$SAKUSEN_PROMPT_FILE")" --model "$SAKUSEN_MODEL"
      $([ "$SAKUSEN_PLUGINS" = true ] && echo --plugin-dir=./plugins)
      --output-format text > "$SAKUSEN_RESULT_FILE"
    env:
      SAKUSEN_MODEL: default
      SAKUSEN_PLUGINS: "false"
    variants:
      opus:
        env:
          SAKUSEN_MODEL: claude-opus-4-1   # env-override pattern: parent command reads "$SAKUSEN_MODEL"
      with-plugins:
        env:
          SAKUSEN_PLUGINS: "true"

agent_aliases:
  headless-implementer: claude:opus                 # swap the role's implementation in one place
  smart-conversationist: claude:opus:with-plugins   # composed ref: one modifier per dimension
```

See SKILL.md → Variants / Agent aliases for the full rules (inheritance semantics, composition and the conflict rule, validation errors, cross-tier behavior).

`sakusen init` scaffolds the two records above plus the user-owned scripts under `.sakusen/agents/` (they require `claude` and `jq` on PATH; steps reference them via `$SAKUSEN_PROJECT_PATH` so worktrees share the project-root copies). Swap the commands to use any other tool.

There is no system-prompt injection: the fully-resolved step prompt is delivered via `$SAKUSEN_PROMPT_FILE`, and attached images are appended to the step prompt itself. To customize system-level behavior, bake flags into the agent's `command` (e.g. `claude --append-system-prompt "..."`).

---

## Summarizer Section

```yaml
summarizer:
  agent: claude:haiku          # headless slug from `agents:` (variants/aliases work)
  max_prompt_bytes: 380000     # optional; >0 enables map-reduce chunking of huge chats
  title_prompt: "..."          # optional; overrides the built-in AI title prompt
  slug_prompt: "..."           # optional; overrides the built-in AI slug prompt
  slug_agent: "..."            # optional; runs the slug call on its own agent
  # command: "..."             # bare-command alternative to `agent:` (mutually exclusive)
  # slug_command: "..."        # bare-command alternative to `slug_agent:` (mutually exclusive)
```

The utility LLM used for `summarize_chat` step context, the final task summary, AI task titles and slugs, and `sakusen backfill-context`. `SAKUSEN_PURPOSE` identifies the call site (`summarize`, `summarize_chat`, `summarize_chat_chunk`, `title`, `slug`, `backfill_context`). Title and slug calls run with the task's project root as working directory (summarize calls use the task's worktree), so context-loading commands like `claude -p` pick up that project's instructions rather than the daemon's cwd.

Two runner shapes, mutually exclusive (setting both is a load error):

- `agent: <slug>` — a **headless** agent from the `agents:` registry, invoked through the file contract: the prompt is written to a scratch `$SAKUSEN_PROMPT_FILE` (never the task worktree — titles run before one exists), the answer is read from `$SAKUSEN_RESULT_FILE` (falling back to the tail of stdout), and `$SAKUSEN_PROJECT_PATH` plus the agent's `env:` are exported. Unknown or tmux-mode slugs are load errors.
- `command: "..."` — a bare shell command with the prompt piped on **stdin** and the response printed on **stdout**.

`title_prompt` / `slug_prompt` replace the built-in prompts for the title and slug calls; `{{input}}` is substituted with the task input (a prompt without the placeholder gets the input appended). `slug_agent` / `slug_command` run the slug call on a different agent or command (model or tool) than the rest of the summarizer work; each falls back to the shared setting when unset, and setting one alone enables AI slugs without enabling AI titles or summaries.

The slug answer is used **verbatim** (surrounding whitespace aside) — it is never shortened, re-cased or re-shaped, so its full wording reaches the branch name and worktree path. An answer that isn't a single token of letters, digits, dashes, underscores or dots is rejected and the slug falls back to the slugified title.

When the block is omitted, everything degrades gracefully: titles fall back to a truncated task input, slugs to the slugified title, summarize passes are skipped with a warning (or fail the task when a step sets `require_context: true`), and `backfill-context` errors out.

`summarizer:` merges **wholesale** across config tiers: a project block replaces the global one entirely rather than merging field by field.

---

## Merge Conflicts Section

```yaml
merge_conflicts:
  agent: codex                 # optional; omit → workflow agent → default_agent → "claude"
  timeout: 10m                 # optional; 10m is the default
  prompt: |                    # optional; replaces the built-in prompt body entirely
    Resolve the conflict markers left by merging `{{git.base_branch}}` into `{{task.branch}}`:

    {{conflict.files}}

    Stage each resolved file with `git add`. Do not commit.
```

Configures the agent-driven merge-conflict resolver (`SAKUSEN_PURPOSE=merge_conflict`). Like the summarizer, the role block holds the role's knobs while `agent:` picks a **headless** runner from the registry (variants and aliases are valid targets). An explicit `agent:` must resolve and be headless — both are load errors otherwise; with no explicit agent the cascade is workflow `agent:` → `default_agent:` → `"claude"`, and a tmux-mode result falls back to a headless `"claude"` record. `timeout` is a Go duration string, validated at load.

`prompt` replaces the built-in body wholesale (it is not a preamble). `{{conflict.files}}` renders the conflicted files as one `` - `path` `` line each; task and git variables resolve as usual, while step, loop, children and track variables resolve empty because no step ran. The block merges wholesale across tiers.

---

## Git Section

```yaml
git:
  base_branch: main                              # Base branch for worktrees (default: system default)
  branch_template: "sakusen/{{task_id}}-{{task_slug}}"  # Branch naming template
```

> **Note:** `on_complete` is a **top-level** key (see "Finalization" below), not part of the `git:` section. The legacy `git.on_complete` location was removed — configs that still use it produce a migration error.

### Branch Template Variables

| Variable | Description |
|---|---|
| `{{task_id}}` | Numeric task ID |
| `{{task_slug}}` | URL-safe slug from title |
| `{{task.id}}` | Same as `{{task_id}}` |
| `{{task.title}}` | Raw task title |
| `{{task.slug}}` | Same as `{{task_slug}}` |

---

## Finalization (`on_complete`)

```yaml
on_complete: commit    # top-level: "commit", "merge", or "none" (default: "commit")
```

Controls what Sakusen does after a task's workflow finishes:

- `"commit"` — Commits changes in the worktree (default)
- `"merge"` — Merges the task branch into base branch
- `"none"` — Leaves changes in the worktree branch without action

It can be overridden per-workflow via a workflow-level `on_complete:` key.
Resolution is **locality-based** — the more locally-defined setting wins:

1. A **project-scoped** workflow's `on_complete` (inline in `.sakusen.yml` or a
   `.sakusen/workflows/` file)
2. The project `.sakusen.yml` top-level `on_complete`, when explicitly set
3. A **global** workflow's `on_complete` (`~/.sakusen.yml` inline or
   `~/.sakusen/workflows/`)
4. The inherited top-level `on_complete` (`~/.sakusen.yml` or the built-in
   default `commit`)

A global workflow's `on_complete` is a cross-project *default*, not an override:
adopting it must not silently defeat a project's explicit choice.

> Moved here from the former `git.on_complete`. The old location now errors.

---

## Verification Section

```yaml
verification:
  max_retries: 2             # int
  verify_summarizer: true    # bool
```

Both fields are accepted by the schema (and validated) but are **not currently read by any execution path** — the block is inert at runtime today. Keep it minimal; do not rely on it to change behavior.

---

## Poll Interval

```yaml
poll_interval: 5s            # Daemon task-polling cadence (Go duration; default 5s)
```

Invalid duration strings are a hard load error. Rarely set per-project.

---

## Options (TUI Display)

```yaml
options:
  number: true               # Show task numbers in the list
  branch: true               # Show branch column
  target: true               # Show target/base branch column
  branchview: false          # Group the list by branch
  animation:
    enabled: true            # Sakusen (airplane) animation on task submission
    duration: 800            # Animation duration in milliseconds (default: 1500)
```

Cosmetic-only; does not affect task execution.

---

## Notifications Section

```yaml
notifications:
  enabled: true              # Master toggle (default: true)
  on_complete: true          # Notify when task completes
  on_failed: true            # Notify when task fails
  on_waiting_input: true     # Notify when task awaits human input
```

---

## Worktree Sync Paths

```yaml
worktree-sync-paths:
  link:                      # Hard-linked into each worktree
    - .docs
    - .env.local
    - config/secrets.yml
  copy:                      # Copied into each worktree (independent files)
    - templates/starter.tpl
```

Files and directories listed here are populated into each new worktree before any setup commands run. Paths are relative to the project root.

A legacy plain-list form is also accepted and treated as `copy:` paths: `worktree-sync-paths: [".claude", ".env"]`. Prefer the structured form in new configs.

### `link:` vs `copy:`

| Mode | Mechanism | When edits sync | Cross-filesystem | Best for |
|---|---|---|---|---|
| `link` | hard-link (`hardLinkDir`) | Yes — shared inodes | Fails — both must be on same FS | Shared docs, configs, lockfiles agents read but rarely modify |
| `copy` | file copy | No — independent | Works | Per-task `.env` overrides, scratch templates, files agents will mutate |

### Symlinks are not supported

`link:` performs **hard-links**, not symbolic links. The Sakusen binary's code path is `linkPath` → `hardLinkDir`. (One nuance: when the *source* path is itself a symlink, it is replicated as a symlink in the worktree — macOS `link(2)` refuses to hard-link symlinks.) If you need a true symlink (e.g., to a path outside the project root, or across filesystems), create it from a setup command:

```yaml
worktree-setup-commands:
  - ln -s /shared/build-cache {{worktree_path}}/.cache
```

### Entries are plain path strings

Each `copy:` / `link:` entry is a **string path relative to the project root** — the destination inside the worktree always mirrors the source path. There is no per-entry `target`/rename form (the schema is `copy: []string` and `link: []string`). To place a synced file at a different path, use a `worktree-setup-command` to move or symlink it after sync.

A missing source path is skipped silently. A `link:` failure (e.g. cross-filesystem) is collected and reported but does not abort the other entries — and sync failures overall only log a warning; the task proceeds. If a synced file is a hard requirement, verify it in a `worktree-setup-command` instead (those DO fail the task on non-zero exit).

### Per-workflow override

`worktree-sync-paths` (and `worktree-setup-command`, `worktree-setup-commands`, `tmux-setup-command`) may also be set on an individual workflow. A non-empty workflow-level value fully replaces the project-level one for tasks running that workflow:

```yaml
workflows:
  - name: heavy
    worktree-sync-paths:
      link: [.docs, vendor/cache]
    steps:
      - name: implementing
        prompt: "..."
```

---

## Worktree Setup Commands

Run shell commands after worktree creation (and after `worktree-sync-paths` is applied). Use for: installing dependencies, generating files, creating real symlinks, copying secrets from a vault, etc.

Two forms:

```yaml
# Single command
worktree-setup-command: |
  pnpm install --frozen-lockfile --dir {{worktree_path}}

# Multiple commands (run in order; preferred when more than one step is needed)
worktree-setup-commands:
  - pnpm install --frozen-lockfile --dir {{worktree_path}}
  - cp ~/.config/myproject/.env.local {{worktree_path}}/.env.local
  - mkdir -p {{worktree_path}}/.cache
```

If both are set, **both run** — the singular command first, then the list in order.

Each command runs via `sh -c` with the **project root** (not the worktree) as `cwd` — always use `{{worktree_path}}` to address the worktree. `{{worktree_path}}` is the **only** template variable available here (`{{session_name}}` / `{{run_agent}}` exist only in `tmux-setup-command`).

A non-zero exit from any command **fails the task** ("worktree setup failed") and stops the remaining commands. This is the opposite of `worktree-sync-paths`, whose per-path failures only log a warning.

---

## Tmux Setup Command

Run when a step uses tmux mode. Customizes the tmux session layout (windows, panes, initial commands) before the agent starts.

```yaml
tmux-setup-command: |-
  tmux rename-window -t {{session_name}}:0 vim
  tmux new-window -t {{session_name}}:1 -n agent -c {{worktree_path}}
  tmux send-keys -t {{session_name}}:1 '{{run_agent}}' C-m
  tmux new-window -t {{session_name}}:9 -n bash -c {{worktree_path}}
  tmux select-window -t {{session_name}}:1
```

Variables:

| Variable | Description |
|---|---|
| `{{session_name}}` | Tmux session created for the task |
| `{{worktree_path}}` | Absolute path to the task's worktree |
| `{{run_agent}}` | Path to the wrapper script that launches the agent with the `SAKUSEN_*` env exported (prefer this) |
| `{{agent_command}}` | Raw agent shell command from the agent record (lacks the env exports; the old `{{claude_command}}` name is a load error) |

The command runs via `sh -c` with the **worktree** as `cwd`; a non-zero exit fails the session launch.

**Agent-launch control:** when the command contains `{{run_agent}}` or `{{agent_command}}`, Sakusen assumes *you* place the agent (as in the example above) and does not auto-start it. When neither appears, Sakusen starts the agent itself in window 0 after your command runs. If you do not set `tmux-setup-command` at all, Sakusen uses a minimal default that just starts the agent.

---

## Prompt Includes (`.sakusen/prompts/`)

Not a YAML section — a directory. Any templated prompt string can inline a shared passage of
text with `{{prompt.<name>}}`, resolved to `<name>.md` and searched project-first, first hit
wins:

1. `<project>/.sakusen/prompts/<name>.md`
2. `~/.sakusen/prompts/<name>.md`

```yaml
merge_conflicts:
  prompt: "{{prompt.conflict-rules}}"        # .sakusen/prompts/conflict-rules.md
workflows:
  - name: sensible
    summarizer_prompt: "{{prompt.wrap-up}}"
    steps:
      - name: implementing
        prompt: |
          Apply the plan for task #{{task.id}}.
          {{prompt.implementer-core}}
        summarization_prompt: "{{prompt.summary-rules}}\n<chat>{{chat}}</chat>"
```

| Rule | Behavior |
|---|---|
| Valid names | `[A-Za-z0-9_-]+` (kebab-case by convention); anything else is a load error |
| Where it works | step `prompt`, step `summarization_prompt`, workflow `summarizer_prompt`, `merge_conflicts.prompt` — **not** `git.branch_template`, `worktree-setup-command`, or `tmux-setup-command` |
| Resolution order | The include is substituted first, then all other placeholders resolve over the combined text — so an included file may itself use `{{task.id}}`, `{{steps.<name>.context}}`, … |
| Nesting | Depth 1 — an included file containing `{{prompt.*}}` is a load error |
| Whitespace | One trailing newline is stripped; other leading/trailing whitespace is preserved |
| Missing file | Hard error at config load and from `sakusen validate`, naming the workflow, step, field and both searched paths |

There is no top-level `prompts:` map and no agent-level prompt — files only. See
[workflow-building.md → Prompt Includes](workflow-building.md#prompt-includes).

---

## Step Configuration, Loops, Cross-Task and Child References

Moved to [workflow-building.md](workflow-building.md) — the single workflow-authoring
reference. It covers step fields (`description`, `timeout`, `human`, `require_context`,
summarization strategies), step context flow, execution mode, loops and their validation
rules, [cross-task references](workflow-building.md#cross-task-references)
(`{{tasks.<id>.<field>}}`), and
[MCP orchestration patterns](workflow-building.md#mcp-orchestration-patterns) including child
task orchestration (`{{children.*}}`).

---

## Task States

| Status | Description |
|---|---|
| `pending` | Waiting to be picked up by a worker |
| `init` | Initializing worktree and environment |
| `running` | Agent is executing |
| `awaiting-approval` | Paused at a `human: true` step |
| `awaiting-children` | Step suspended on child tasks (`create_tasks_and_wait` / `wait_for_tasks`) |
| `tmux` | Running in interactive tmux session |
| `finalizing` | Running post-completion steps |
| `summarizing` | Generating task context summary (also used for the step summary of single-step workflows) |
| `summarizing_step` | Summarizing a completed step's transcript (multi-step workflows) |
| `merge-blocked` | Merge conflicts or merge failure |
| `resolving-conflicts` | Agent resolving merge conflicts |
| `completed` | Successfully finished |
| `failed` | Execution failed |

---

## Task Priorities

| Priority | Sort Value |
|---|---|
| `low` | 1 |
| `medium` | 2 (default) |
| `high` | 3 |
| `urgent` | 4 |

---

## Continue Workflow

Pressing `c` on a completed/failed task triggers continuation:
1. TUI shows workflow selection (task workflows)
2. User enters a prompt (enhanced with continuation context)
3. Task resets to `pending` with the selected workflow
4. Daemon picks up and re-executes

For terminal tasks, continuation creates/reuses a worktree and starts an interactive (tmux-mode) agent whose initial prompt includes the task input and any previously captured context.

### Fast-Track Finalization

When finalizing a tmux task with no meaningful changes (ignoring `.sakusen-output.log` and `CLAUDE.md`), the task skips summarizer/on_complete and goes directly to `completed`.

---

## Legacy Config Formats (Removed)

All of the following are **hard load errors** with migration messages:

| Removed | Replacement |
|---|---|
| `claude:` (binary override block) | `agents:` records — the whole invocation lives in the agent's `command` |
| `yolo:` | Permission flags directly in the agent's `command` |
| `system_prompt:` | System-prompt flags in the agent's `command`, or fold the text into step prompts |
| `allowed_summarization_models:` (top-level and step-level) | `summarizer:` block — pick the model inside its agent or command |
| `merge_conflict_agent:` | `agent:` inside the top-level `merge_conflicts:` block |
| `print:` / `tmux:` (workflow and step level) | The resolved agent record's `mode` |
| `{{claude_command}}` in `tmux-setup-command` | `{{agent_command}}` or `{{run_agent}}` |
| `git.on_complete` | Top-level `on_complete:` |
| Singular `workflow:` key; `workflows: {tasks:, one-off:, init:}` map | The current flat `workflows:` list |

```yaml
workflows:
  - name: default
    steps:
      - name: implementing
        prompt: "Implement the task"
```

---

## Complete Example

```yaml
max_workers: 3
default_priority: medium
tmux_nested_attach_behavior: switch

default_agent: claude
agents:
  claude:
    mode: headless
    command: '"$SAKUSEN_PROJECT_PATH/.sakusen/agents/claude-headless.sh"'
    variants:
      haiku:
        env:
          ANTHROPIC_MODEL: haiku
  claude-tmux:
    mode: tmux
    command: '"$SAKUSEN_PROJECT_PATH/.sakusen/agents/claude-tmux.sh"'
    resume_command: 'claude --dangerously-skip-permissions --resume "$SAKUSEN_SESSION_ID"'
    chat_log_command: '"$SAKUSEN_PROJECT_PATH/.sakusen/agents/claude-chat-log.sh"'

summarizer:
  agent: claude:haiku
  max_prompt_bytes: 380000

merge_conflicts:
  timeout: 10m

verification:
  verify_summarizer: true

git:
  base_branch: main
  branch_template: "sakusen/{{task_id}}-{{task_slug}}"

on_complete: merge

notifications:
  enabled: true
  on_complete: true
  on_failed: true
  on_waiting_input: true

workflows:
  - name: sensible
    summarizer_prompt: "Summarize what was implemented and any decisions made"
    on_complete: commit    # optional per-workflow override of the top-level on_complete
    steps:
      - name: implementing
        prompt: |
          Implement task #{{task.id}}: {{task.title}}

          <task-input>
          {{task.input}}
          </task-input>
        timeout: 45m
      - name: reviewing
        agent: claude-tmux   # interactive review in a tmux session
        prompt: |
          Review the implementation for task #{{task.id}}.

          Implementation summary:
          <step-context name="implementing">
          {{steps.implementing.context}}
          </step-context>
        human: true
        timeout: 20m
      - name: fixing
        # inherits the headless default agent — loop steps must resolve headless
        prompt: |
          Fix the issues found during review:
          <step-context name="reviewing">
          {{steps.reviewing.context}}
          </step-context>
        timeout: 30m
        loop:
          goto: reviewing
          max_iterations: 3
          exit_condition:
            step_context_empty: reviewing

  - name: quick
    agent: claude-tmux   # every step of this workflow runs interactively
    steps:
      - name: implementing
        prompt: |
          Implement task #{{task.id}}: {{task.title}}

          <task-input>
          {{task.input}}
          </task-input>

  # Fully-pinned: New Task screen is skipped; task created immediately
  - name: housekeeping
    description: "Run standard codebase maintenance: linting, dead code removal, dependency updates"  # metadata only
    input: "Run standard codebase maintenance: linting, dead code removal, dependency updates"        # pins the task input
    worktree: true
    branch: sakusen/housekeeping-{{task.id}}
    target: main
    steps:
      - name: auditing
        prompt: "Audit the codebase for code smells, unused dependencies, and dead code"
        timeout: 20m
      - name: cleaning
        prompt: |
          Apply the following cleanups:
          <step-context name="auditing">
          {{steps.auditing.context}}
          </step-context>
        timeout: 30m

  - name: from-prd
    description: "Analyze a PRD and create implementation tasks"
    worktree: true
    branch: sakusen/from-prd-{{task.id}}
    target: main
    steps:
      - name: analyzing
        prompt: |
          Analyze the PRD and break it into implementable tasks.
          Create sakusen tasks for each piece of work.
        timeout: 30m
```
