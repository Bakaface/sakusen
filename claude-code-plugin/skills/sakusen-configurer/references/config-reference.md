# Sakusen Configuration Complete Reference

## Contents

- [Agents Section](#agents-section)
- [Summarizer Section](#summarizer-section)
- [Git Section](#git-section)
- [Finalization (`on_complete`)](#finalization-on_complete)
- [Verification Section](#verification-section)
- [Poll Interval](#poll-interval)
- [Options (TUI Display)](#options-tui-display)
- [Notifications Section](#notifications-section)
- [Worktree Sync Paths](#worktree-sync-paths)
- [Worktree Setup Commands](#worktree-setup-commands)
- [Tmux Setup Command](#tmux-setup-command)
- [Step Configuration Details](#step-configuration-details) — timeout, step context, summarization, require_context, human approval, execution mode, loops
- [Cross-Task References (`{{tasks.<id>.<field>}}`)](#cross-task-references-tasksidfield)
- [Child Task Orchestration (`{{children.*}}`)](#child-task-orchestration-children)
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

An agent may declare `variants:` (variant name → partial record): each variant inherits every parent field, overrides what it redefines (`env` merges per-key, variant wins), and becomes an ordinary agent named `<parent>:<variant>` usable anywhere a slug is accepted. Variants are one level deep (no nesting) and cannot unset a parent field. The top-level `agent_aliases:` map (alias → agent or variant slug) adds stable semantic names, merged per-key across tiers (more-local wins, so a project can re-point a global alias):

```yaml
agents:
  claude:
    command: 'claude -p "$(cat "$SAKUSEN_PROMPT_FILE")" --model "$SAKUSEN_MODEL" --output-format text > "$SAKUSEN_RESULT_FILE"'
    env:
      SAKUSEN_MODEL: default
    variants:
      opus:
        env:
          SAKUSEN_MODEL: claude-opus-4-1   # env-override pattern: parent command reads "$SAKUSEN_MODEL"

agent_aliases:
  headless-implementer: claude:opus       # swap the role's implementation in one place
```

See SKILL.md → Variants / Agent aliases for the full rules (inheritance semantics, validation errors, cross-tier behavior).

`sakusen init` scaffolds the two records above plus the user-owned scripts under `.sakusen/agents/` (they require `claude` and `jq` on PATH; steps reference them via `$SAKUSEN_PROJECT_PATH` so worktrees share the project-root copies). Swap the commands to use any other tool.

There is no system-prompt injection: the fully-resolved step prompt is delivered via `$SAKUSEN_PROMPT_FILE`, and attached images are appended to the step prompt itself. To customize system-level behavior, bake flags into the agent's `command` (e.g. `claude --append-system-prompt "..."`).

---

## Summarizer Section

```yaml
summarizer:
  command: claude -p --output-format text --model haiku --dangerously-skip-permissions
  max_prompt_bytes: 380000     # optional; >0 enables map-reduce chunking of huge chats
```

The utility LLM command used for `summarize_chat` step context, the final task summary, AI task titles, and `sakusen backfill-context`. The prompt is piped on **stdin**; the response must be printed on **stdout**. `SAKUSEN_PURPOSE` identifies the call site (`summarize`, `summarize_chat`, `summarize_chat_chunk`, `title`, `backfill_context`).

When the block is omitted, everything degrades gracefully: titles fall back to a truncated task input, summarize passes are skipped with a warning (or fail the task when a step sets `require_context: true`), and `backfill-context` errors out.

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

It can be overridden per-workflow via a workflow-level `on_complete:` key
(see the Workflows section). Resolution precedence: **workflow-level →
project-level → default (`commit`)**.

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

## Step Configuration Details

### Timeout Format

Go duration strings: `"30m"`, `"1h"`, `"1h30m"`, `"45m"`, `"2h"`. Default: `"30m"`.

### Step Context Flow

After each step completes, the agent's result text (headless: read from `$SAKUSEN_RESULT_FILE`, with a stdout-tail fallback) is captured as step context and stored in the `task_steps` database table. This context is available to subsequent steps via `{{steps.<step_name>.context}}` (or the backward-compat alias `{{artifacts.<step_name>}}`).

### Step Summarization

By default (`summarization_strategy` unset), the step's context is produced by **`summarize_chat`** — the configured `summarizer:` command summarizes the step's chat content. `last_message` and `none` are the alternatives:

```yaml
- name: grilling
  agent: claude-tmux           # interactive step → tmux-mode agent
  summarization_strategy: summarize_chat
  summarization_prompt: |
    Extract the durable design decisions reached in this Q&A.

    Format:
    - Numbered list, each item: question + paraphrased user answer.
    - Skip small-talk and detours.

    <chat>
    {{chat}}
    </chat>
  prompt: |
    Interview the user until shared understanding is reached...
```

| Strategy | What gets captured |
|---|---|
| (unset) | **Defaults to `summarize_chat`** |
| `summarize_chat` | The `summarizer:` command summarizes the step's chat content using `summarization_prompt` (the default) |
| `last_message` | The agent's final result text only (no summarizer call; unusable for tmux steps) |
| `none` | Nothing — no step context is captured; later `{{steps.<name>.context}}` references resolve to empty |

The chat content comes from the unified task log for headless steps, and from the agent record's `chat_log_command` for tmux steps — a tmux agent without `chat_log_command` captures no chat context. `summarize_chat` is essential for tmux steps where the meaningful output is the dialogue, not a final message. The summarizer step also unlocks the `step_context_empty` loop exit pattern: instruct the summarizer to emit empty output when "no issues found", and the loop will terminate. Requires a configured `summarizer:` — without one, the pass is skipped with a warning (or fails the task under `require_context: true`).

Inside `summarization_prompt`, the variable `{{chat}}` expands to the full chat content. All standard task variables (`{{task.id}}`, `{{steps.<name>.context}}`, etc.) are also available.

### Require Context

```yaml
- name: grilling
  require_context: true
  summarization_prompt: "..."
  prompt: "..."
```

By default, context capture is best-effort: if the chat transcript can't be loaded or summarized, Sakusen logs a warning and advances with an **empty** step context. `require_context: true` makes that failure **block the task** instead. Set it on steps whose output later steps template via `{{steps.<name>.context}}` (e.g. a grilling step feeding an implementing step) so the pipeline fails loudly rather than silently running the next step with no plan. Only meaningful for tmux steps with `summarize_chat`; ignored otherwise.

Example multi-step with step context:

```yaml
steps:
  - name: analyzing
    prompt: |
      Analyze the requirements:
      <task-input>
      {{task.input}}
      </task-input>
  - name: implementing
    prompt: |
      Implement based on the analysis:
      <step-context name="analyzing">
      {{steps.analyzing.context}}
      </step-context>
  - name: reviewing
    prompt: |
      Review the implementation:
      <step-context name="implementing">
      {{steps.implementing.context}}
      </step-context>
    human: true
```

### Human Approval Steps

When `human: true`, the task pauses at `awaiting-approval` status. The user reviews in the TUI and approves to continue. Use for review gates.

### Execution Mode: Headless vs. Tmux Agents

A step's execution mode comes from the agent record it resolves to (cascade: step `agent:` → workflow `agent:` → `default_agent:` → `"claude"`). When a step resolves to a **tmux-mode** agent:
- The agent command runs inside a detached interactive tmux session
- User can attach to watch/interact
- Task shows `tmux` status in TUI
- The daemon auto-advances on the agent's turn-end sentinel (hookless agents are manual-advance; press `c` to advance/finalize)

A **headless** agent is spawned as a subprocess, its stdout is streamed to the task log, and the step auto-advances on exit with the result text from `$SAKUSEN_RESULT_FILE`.

> The removed `tmux:` and `print:` fields are **hard load errors** — select an agent whose `mode` matches instead. See the mode section in SKILL.md for the full mode × `human` behavior table.

### Loop Configuration

Loops allow iterative refinement (e.g., implement → review → fix → review again).

```yaml
steps:
  - name: implementing
    prompt: |
      Implement the following:
      <task-input>
      {{task.input}}
      </task-input>
  - name: reviewing
    prompt: |
      Review the implementation:
      <step-context name="implementing">
      {{steps.implementing.context}}
      </step-context>
    human: true
  - name: fixing
    # inherits the (headless) default agent — loop steps must resolve to a headless agent
    prompt: |
      Fix the issues found during review:
      <step-context name="reviewing">
      {{steps.reviewing.context}}
      </step-context>
    loop:
      goto: reviewing
      max_iterations: 3
      exit_condition:
        step_context_empty: reviewing
```

**Validation rules:**
- `goto` must reference a step that appears BEFORE the loop step
- No self-reference
- `max_iterations` must be >= 1
- Loop steps cannot have `human: true`
- Loop steps cannot resolve to a tmux-mode agent — use a headless agent on the loop step (or its workflow)
- Loop ranges cannot overlap with other loops

---

## Cross-Task References (`{{tasks.<id>.<field>}}`)

Reference another task's fields anywhere templates resolve. Supported fields: `title`, `branch`, `input`, `context`.

Two places they work, with different semantics:

1. **In a task's input or context** (entered at create/edit time): the daemon validates each ref — missing task, cross-project ref, or ref to a `failed` task is a create/edit **error**. Refs to still-active tasks are **auto-added as `blocked_by` dependencies**, so the referencing task won't start until they finish. Refs are pre-resolved (single-pass, no recursive expansion) before the input/context is inlined into step prompts.
2. **In workflow step prompts**: resolved at render time with no validation or auto-blocking — a missing task resolves to empty string with a warning log.

`input` and `context` fields are multi-line — wrap them in semantic tags (see SKILL.md).

---

## Child Task Orchestration (`{{children.*}}`)

A step's agent can fan out child tasks via the sakusen MCP tools:

- **`create_tasks_and_wait`** — spawn one or more child tasks and suspend the calling step until ALL reach a terminal status (`completed` or `failed`). The parent task shows `awaiting-children`.
- **`wait_for_tasks`** — same suspension, but for pre-existing task IDs (already-terminal tasks are skipped).

Both default `parent_task_id` to the `SAKUSEN_TASK_ID` env var the engine injects into every step, so agents don't need to pass it. When all children finish, **the calling step re-runs from the same step index** with these variables populated:

| Variable | Description |
|---|---|
| `{{children.summary}}` | Formatted digest of every child (ID, status, title, context), sorted by ID — multi-line |
| `{{children.<id>.id}}` | Child task ID |
| `{{children.<id>.title}}` | Child task title |
| `{{children.<id>.status}}` | Terminal status: `completed` or `failed` |
| `{{children.<id>.context}}` | Child's final task context (its synthesized output) — multi-line |

**Children may live in a different project.** Pass a per-child `project_path` to `create_tasks_and_wait` (it defaults to the parent's project), or hand `wait_for_tasks` any task ID at all — the wait relation is purely ID-based. The child runs under its own project's `.sakusen.yml` and workflows and merges into its own repo; the parent suspends and resumes identically, and `{{children.<id>.status}}` is `completed`/`failed` regardless of where the child ran. One caveat: a cross-project child does **not** inherit the parent's track (tracks are project-scoped), so pass an explicit `track` if you want one — it resolves in the child's own project.

Unknown IDs and unsupported fields resolve to empty. On the first (pre-spawn) run of the step these are all empty — a typical orchestrator prompt branches: "If `<children-summary>` below is empty, break the task into subtasks and call `create_tasks_and_wait`. Otherwise, review the child results, check every `{{children.<id>.status}}` for failures, and integrate."

Since the meaningful state usually lives in the conversation, orchestrator steps pair well with `summarize_chat` (the default) and `require_context: true`.

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
| `allowed_summarization_models:` (top-level and step-level) | `summarizer:` command — pick the model inside it |
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
  claude-tmux:
    mode: tmux
    command: '"$SAKUSEN_PROJECT_PATH/.sakusen/agents/claude-tmux.sh"'
    resume_command: 'claude --dangerously-skip-permissions --resume "$SAKUSEN_SESSION_ID"'
    chat_log_command: '"$SAKUSEN_PROJECT_PATH/.sakusen/agents/claude-chat-log.sh"'

summarizer:
  command: claude -p --output-format text --model haiku --dangerously-skip-permissions
  max_prompt_bytes: 380000

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
