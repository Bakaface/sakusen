---
name: sakusen-configurer
description: >
  Generate and edit .sakusen.yml project configuration files for the Sakusen daemon.
  Sakusen orchestrates user-configured coding agents (Claude Code, opencode, aider, any
  CLI) working on tasks in parallel using isolated git worktrees. Use when (1) creating
  a new .sakusen.yml config, (2) building, adding, or modifying workflows, workflow steps,
  step prompts, loops, or agents, (3) configuring git, tmux, summarizer, notifications,
  tracks, or verification settings, (4) user mentions "sakusen config", ".sakusen.yml",
  or asks about sakusen workflow/task/agent/track configuration, (5) troubleshooting
  sakusen config issues.
---

# Sakusen Configuration Skill

Generate correct `.sakusen.yml` project configuration files for the Sakusen daemon.

## What is Sakusen?

Sakusen is a daemon that orchestrates multiple user-configured coding agents working on tasks in parallel. Each task runs in an isolated git worktree. An **agent** is just a shell command declared under the top-level `agents:` map — Sakusen is agent-agnostic and talks to it through environment variables. Configuration lives in `.sakusen.yml` at the project root.

## Reference Files

| File | Load when |
|---|---|
| `references/workflow-building.md` | **Creating or editing any workflow, routine, step, prompt, loop, parallel group, or template variable.** Complete authoring reference: entry shapes, pins, routines (invocation bindings), file-based/hidden/global/track workflows, step references, every workflow and step field, summarization, loops, parallel groups, prompt wrapping, the full template-variable catalog, tracks in prompts, cross-task refs, and MCP orchestration patterns. |
| `references/config-reference.md` | Working on non-workflow config: agents, summarizer, git, worktree sync/setup, tmux setup, notifications, options, task states/priorities, legacy formats, and a complete example config. |

## Config Loading Order (later overrides earlier)

1. Built-in defaults (hardcoded)
2. Global daemon config: `~/.config/sakusen/config.yaml` (subset of fields, no workflows)
3. Global sakusen config: `$XDG_CONFIG_HOME/sakusen/config.yml` if present, else `~/.sakusen.yml`
4. **Project config: `.sakusen.yml`** (this is what you generate)

## Quick Start

The easiest start is `sakusen init`, which scaffolds a `.sakusen.yml` with working `claude` / `claude-tmux` agent records plus user-owned agent scripts under `.sakusen/agents/` (those scripts require `claude` and `jq` on PATH — edit or replace them freely; sakusen never overwrites them).

Minimal hand-written config:

```yaml
default_agent: claude
agents:
  claude:
    mode: headless
    command: 'claude --dangerously-skip-permissions -p "$(cat "$SAKUSEN_PROMPT_FILE")" | tee "$SAKUSEN_RESULT_FILE"'

workflows:
  - name: default
    steps:
      - name: implementing
        prompt: |
          Implement task #{{task.id}}: {{task.title}}

          <task-input>
          {{task.input}}
          </task-input>
```

Steps run whichever agent the cascade resolves (step `agent:` → workflow `agent:` → `default_agent:` → the `"claude"` slug). A config with no `agents:` map still loads — but running a step fails with an instructive error.

## Top-Level Fields

| Field | Type | Default | Description |
|---|---|---|---|
| `max_workers` | int | `3` | Max concurrent agents |
| `default_priority` | string | `"medium"` | `low`, `medium`, `high`, `urgent` |
| `poll_interval` | string | `"5s"` | Daemon task-polling cadence (Go duration string). Rarely overridden per-project. |
| `agents` | map | — | **Agent registry** — slug → agent record (`mode`, `command`, `resume_command`, `chat_log_command`, `env`). See [Agents](#agents). |
| `default_agent` | string | `"claude"` | Agent slug steps fall back to when neither the step nor the workflow sets `agent:`. |
| `summarizer` | object | — | Utility LLM for summaries and AI titles/slugs (`agent` or `command`, `slug_agent`/`slug_command`, `max_prompt_bytes`, `title_prompt`, `slug_prompt`). See [Summarizer](#summarizer). |
| `merge_conflicts` | object | — | Merge-conflict resolver role (`agent`, `timeout`, `prompt`). See [Merge conflicts](#merge-conflicts). |
| `verification` | object | — | Summarizer verification settings (`max_retries`, `verify_summarizer`) |
| `git` | object | — | Branch naming, base branch |
| `on_complete` | string | `"commit"` | Finalization action (`commit`/`merge`/`none`); per-workflow overridable |
| `workflows` | list | — | **Primary config block** — flat list of workflow pipelines |
| `notifications` | object | — | Desktop notification toggles |
| `options` | object | — | TUI display toggles: `number`, `branch`, `target`, `branchview` (bools), `animation` (`enabled` bool, `duration` ms). |
| `tmux_nested_attach_behavior` | string | `"switch"` | `"switch"` or `"nest"` for tmux-in-tmux |
| `worktree-sync-paths` | object | — | Hard-link or copy paths from main checkout into each worktree (e.g., `.docs`, `.env`). Also settable per-workflow. See [Sharing files into worktrees](#sharing-files-into-worktrees). |
| `worktree-setup-command` | string | — | Single shell command run after worktree creation (`{{worktree_path}}` available). Also settable per-workflow. |
| `worktree-setup-commands` | list[string] | — | Multiple setup commands run in order; preferred over the singular form when more than one step is needed. Also settable per-workflow. |
| `tmux-setup-command` | string | — | Shell command run when launching a tmux step. Variables: `{{session_name}}`, `{{worktree_path}}`, `{{run_agent}}`, `{{agent_command}}`. Also settable per-workflow. |

### Removed keys (hard load errors — never emit these)

| Removed key | Replacement |
|---|---|
| `claude:` | Define agents under the `agents:` map (`sakusen init` scaffolds claude records) |
| `yolo:` | Put permission flags (e.g. `--dangerously-skip-permissions`) directly in the agent's `command` |
| `system_prompt:` | Bake system-prompt flags into the agent's `command` (e.g. `claude --append-system-prompt "..."`) or fold the text into step prompts |
| `allowed_summarization_models:` (top-level and step-level) | The `summarizer:` block; pick the model inside its agent or command |
| `merge_conflict_agent:` | `agent:` inside the top-level `merge_conflicts:` block |
| `print:` (workflow and step level) | `agent: <slug>` where the agent's `mode` is `headless` (was `print: true`) or `tmux` (was `print: false`) |
| `tmux:` (workflow and step level) | Same — the agent record's `mode` |
| `{{claude_command}}` in `tmux-setup-command` | `{{agent_command}}` (or `{{run_agent}}`) |
| `git.on_complete` | Top-level `on_complete:` |

> **Per-workflow overrides:** `worktree-sync-paths`, `worktree-setup-command`, `worktree-setup-commands`, and `tmux-setup-command` may be set on an individual workflow (an entry in the flat `workflows:` list). A non-empty workflow-level value fully overrides the project-level one for tasks running that workflow.

### Field-name convention

Top-level field names mix two casing styles — **don't guess, copy exactly**:

- **kebab-case:** `worktree-sync-paths`, `worktree-setup-command`, `worktree-setup-commands`, `tmux-setup-command`
- **snake_case:** `max_workers`, `default_priority`, `default_agent`, `poll_interval`, `tmux_nested_attach_behavior`, `base_branch`, `branch_template`, `on_complete`, `max_prompt_bytes`, `title_prompt`, `slug_prompt`, `slug_command`, `resume_command`, `chat_log_command`

If you author an unrecognized variant (`worktree_sync_paths`, `tmux_setup_command`, etc.), Sakusen will silently ignore it.

## Agents

Every workflow step runs one of the shell commands declared under `agents:`. Sakusen is agent-agnostic — commands run via `sh -c` in the task workdir with an env-var contract; Claude Code, opencode, aider, or a raw model CLI all work.

```yaml
default_agent: claude

agents:
  claude:                       # slug: kebab-case ([a-z0-9][a-z0-9-]*)
    mode: headless              # "headless" (default when omitted) or "tmux"
    command: '"$SAKUSEN_PROJECT_PATH/.sakusen/agents/claude-headless.sh"'
  claude-tmux:
    mode: tmux
    command: '"$SAKUSEN_PROJECT_PATH/.sakusen/agents/claude-tmux.sh"'
    resume_command: 'claude --dangerously-skip-permissions --resume "$SAKUSEN_SESSION_ID"'
    chat_log_command: '"$SAKUSEN_PROJECT_PATH/.sakusen/agents/claude-chat-log.sh"'
    env:                        # extra env exported to every spawn of this agent
      MY_VAR: value
```

Agent record fields:

| Field | Modes | Description |
|---|---|---|
| `mode` | — | `headless` (spawned subprocess, synchronous result) or `tmux` (detached interactive session that pauses the workflow). Default: `headless`. |
| `command` | both | **Required.** The shell command that runs the agent. |
| `resume_command` | tmux only | When set, restored sessions after a daemon restart run this with `SAKUSEN_SESSION_ID` set to the recorded session id instead of starting fresh. |
| `chat_log_command` | tmux only | When set, run to obtain the step's conversation log (printed on stdout) for the `summarize_chat` strategy. Env: `SAKUSEN_SESSION_ID`, plus `SAKUSEN_SENTINEL_FILE` / `SAKUSEN_TRANSCRIPT_PATH` from the latest turn-end sentinel. |
| `env` | both | Extra environment variables for every spawn (command, resume, chat-log alike). Cannot override `SAKUSEN_*` contract vars. |
| `variants` | both | Named child configs (variant name → partial record) inheriting every parent field. See Variants below. |

**Selection cascade:** step `agent:` → workflow `agent:` → top-level `default_agent:` → the `"claude"` slug. Explicit references to unknown slugs are load errors; the implicit `"claude"` fallback may be missing at load time (steps then fail at run time with a pointer to `sakusen init`).

### Variants

A variant inherits every field from its parent agent and overrides only what it redefines (`env` merges per-key — variant wins, parent-only keys survive; all other fields override wholesale). Each variant becomes an ordinary agent named `<parent>:<variant>`, usable anywhere a slug is accepted (`agent:`, `default_agent:`, alias targets). The parent stays usable as-is.

The canonical pattern is **env-override**: the parent command reads a shell variable, variants set it via `env:` (commands run via `sh -c` with the agent's env exported, so `"$VAR"` expands). Keep each variant to one dimension — a ref can stack several of them:

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
      opus:         { env: { SAKUSEN_MODEL: claude-opus-4-1 } }
      fable:        { env: { SAKUSEN_MODEL: claude-fable-5-1 } }
      with-plugins: { env: { SAKUSEN_PLUGINS: "true" } }
```

#### Composing variants

A ref is a parent slug followed by any number of that parent's variants, used as **modifiers**:

```yaml
    agent: claude:opus:with-plugins
```

- **Any order.** `claude:with-plugins:opus` and `claude:opus:with-plugins` are the same agent: modifiers are sorted alphabetically into a canonical form, and that canonical form is the record's registry key.
- **Only referenced combinations exist.** A combination becomes a registry entry when some `agent:`, `default_agent:`, or alias target mentions it — the parent's cross product is never materialized.
- **Conflict rule.** Two modifiers in one ref that set the same env key, or the same whole field, to *different* values is a load error naming both modifiers. Identical values are fine. This gives mutually-exclusive dimensions for free: `opus` and `fable` both write `SAKUSEN_MODEL`, so `claude:opus:fable` fails.
- Every field a single variant may set is allowed on a modifier (`command`, `mode`, `resume_command`, `chat_log_command`, `env`); the composed record is shape-checked like any other.
- A modifier must be a variant declared by *that* parent — `claude:with-plugins` fails when `claude` doesn't declare `with-plugins`, listing the declared names. Repeating a modifier (`claude:opus:opus`) is also an error.

Rules and limitations:

- Variant names are kebab-case; the `<parent>:<variant>` slug is created by expansion only — authoring a literal colon key under `agents:` is a load error.
- **No nesting** — a variant declaring its own `variants:` is a load error. Cross-product dimensions are expressed by stacking modifiers in a ref, not by pre-multiplying variant names.
- A variant **cannot unset** a parent field (empty = inherit). E.g. a `mode: headless` variant of a tmux parent with `resume_command` fails validation (tmux-only field on the resolved record) — use a separate agent record instead.
- Cross-tier: a slug redefined in a more-local tier replaces the record wholesale, variants included — a project cannot add a variant to a global agent without redefining the whole record.

#### Sharing variants across agents

Variants are declared per-parent — there is no shared modifier namespace in Sakusen. When two agents need the same dimension, share the block with a YAML anchor and the `<<:` merge key:

```yaml
x-models: &models
  opus:  { env: { SAKUSEN_MODEL: claude-opus-4-1 } }
  fable: { env: { SAKUSEN_MODEL: claude-fable-5-1 } }

agents:
  claude:
    command: ...
    variants: *models
  claude-tmux:
    mode: tmux
    command: ...
    variants:
      <<: *models
      with-plugins: { env: { SAKUSEN_PLUGINS: "true" } }
```

Unknown top-level keys are ignored by the loader, so an `x-`-prefixed anchor block is a safe place to park the shared definition.

### Agent aliases

The top-level `agent_aliases:` map (alias → target) gives roles stable semantic names so workflows reference the role while the underlying agent is swapped in one place:

```yaml
agent_aliases:
  headless-implementer: claude:opus                    # target may be an agent or a variant
  conversationist: claude-tmux:fable:with-plugins      # ...or a composed ref
  reviewer: claude
```

An alias resolves into an ordinary registry entry, usable anywhere a slug is. Alias names are plain kebab-case (no colons); collisions with existing agent/variant slugs, unknown targets, and alias→alias chains are load errors. Aliases merge per-key across tiers (more-local wins), so a project can re-point a globally-defined alias.

### Environment contract

Every agent spawn gets:

| Variable | Description |
|---|---|
| `SAKUSEN_TASK_ID` | Task id |
| `SAKUSEN_STEP` | Step name |
| `SAKUSEN_WORKTREE` | Absolute path of the task workdir |
| `SAKUSEN_PROJECT_PATH` | Absolute path of the project repo root |
| `SAKUSEN_PURPOSE` | `step` (or `merge_conflict` for the conflict resolver) |
| `SAKUSEN_AGENT` | Resolved agent slug |
| `SAKUSEN_TRACK_ID` | Task's track id (only when the task is on a track). A **track** is a named, hierarchical context container tasks attach to at create time — see `references/workflow-building.md` → Tracks in Workflows. |
| `SAKUSEN_PROMPT_FILE` | File containing the fully-resolved step prompt |

Headless mode additionally:

| Variable | Description |
|---|---|
| `SAKUSEN_RESULT_FILE` | File the command pipeline should write the agent's final result text to. When absent after exit, Sakusen falls back to the tail of captured stdout (crude — write the file). |

Tmux mode additionally (inside the session wrapper script):

| Variable | Description |
|---|---|
| `SAKUSEN_DONE_DIR` | Directory turn-end sentinel files must be written to |
| `SAKUSEN_DONE_PREFIX` | Filename prefix a sentinel for this step must use |

**Turn-end signalling (tmux):** when the agent finishes a turn, something (a hook, the agent itself, an idle-watcher) writes `"$SAKUSEN_DONE_DIR/$SAKUSEN_DONE_PREFIX-$(date +%s%N).json"`. The file MAY contain a JSON object with `session_id` (recorded for `resume_command` and `chat_log_command`) and `transcript_path`. The scaffolded claude-tmux agent does this via a Claude Code Stop hook (`.sakusen/agents/claude-settings.json`).

### v1 limitations

- **Hookless tmux agents are manual-advance** — no sentinel means the task pauses at `tmux` status until the user advances it.
- **Agents without `chat_log_command`** cannot capture `summarize_chat` context for tmux steps (context degrades to empty, or the task fails when the step sets `require_context: true`).
- **No `summarizer:` configured** → no AI titles (falls back to truncated input) and no chat/step/task summaries (skipped with a warning).
- **Loop steps cannot use tmux-mode agents** (a tmux step pauses the engine, so a loop over it could never iterate).
- The merge-conflict resolver needs a **headless** agent: `merge_conflicts.agent` when set, else the workflow's agent, with the `"claude"` slug as fallback when the workflow agent is tmux-mode.

## System roles

The summarizer and the merge-conflict resolver are not workflow steps, but each still needs something to run. Each has its own top-level block: the block holds the role's own knobs, and picks its runner from the `agents:` registry via `agent: <slug>` (the registry only says HOW something runs). Role agents must be **headless** — both roles are synchronous passes — and must honor the file contract (`$SAKUSEN_PROMPT_FILE` in, `$SAKUSEN_RESULT_FILE` out, stdout tail as fallback). Variants (`parent:variant`) and `agent_aliases:` names are valid targets. Both blocks merge **wholesale** across tiers: a project block replaces the global one entirely.

## Summarizer

```yaml
summarizer:
  agent: claude:haiku       # headless slug from `agents:`
  max_prompt_bytes: 380000
  title_prompt: "..."       # optional; overrides the built-in AI title prompt
  slug_prompt: "..."        # optional; overrides the built-in AI slug prompt
  slug_agent: "..."         # optional; runs the slug call on its own agent
  # command: "..."          # alternative to `agent:` — mutually exclusive with it
  # slug_command: "..."     # alternative to `slug_agent:` — mutually exclusive with it
```

The utility LLM Sakusen shells out to for text-in/text-out work: chat/step summaries (`summarize_chat`), the final task summary, AI task titles and slugs, and `sakusen backfill-context`. `SAKUSEN_PURPOSE` identifies the call site (`summarize`, `summarize_chat`, `summarize_chat_chunk`, `title`, `slug`, `backfill_context`).

Two runner shapes, mutually exclusive: `agent:` runs a registry agent through the file contract, while `command:` runs a bare shell command with the prompt on **stdin** and the response on **stdout**. Same rule for the slug pair (`slug_agent:` vs `slug_command:`); either falls back to the main setting when unset, and setting one alone enables AI slugs without AI titles or summaries.

`title_prompt` / `slug_prompt` (optional) replace the built-in prompts for those two calls; `{{input}}` is substituted with the task input (a prompt without the placeholder gets the input appended). The slug answer is used verbatim — never shortened or re-shaped — so it reaches the branch name in full.

`max_prompt_bytes` (optional, > 0) bounds a single invocation: larger chat logs are summarized map-reduce style (chunked on line boundaries, each chunk summarized, then reduced). `0`/omitted disables chunking. Omit the whole block to disable summarization (everything degrades gracefully — see v1 limitations).

## Merge conflicts

```yaml
merge_conflicts:
  agent: codex              # optional; omit → workflow agent → default_agent → "claude"
  timeout: 10m              # optional; 10m is the default
  prompt: |                 # optional; replaces the built-in prompt body entirely
    Resolve the conflict markers left by merging `{{git.base_branch}}` into `{{task.branch}}`:

    {{conflict.files}}

    Stage each resolved file with `git add`. Do not commit.
```

Configures the agent-driven conflict resolver used when a task branch is merged. An explicit `agent:` must resolve and be headless (load error otherwise); without one, the cascade falls back to the workflow's agent and then to the `"claude"` slug when that agent is tmux-mode. `timeout` is a Go duration string, validated at load.

`prompt` replaces the built-in body — it is not a preamble or wrapper. `{{conflict.files}}` renders the conflicted files as one `` - `path` `` line each; task and git variables resolve as usual, while step, loop, children and track variables resolve empty (no step ran). The resolver spawn exports the usual step contract with `SAKUSEN_PURPOSE=merge_conflict`.

## Sharing files into worktrees

`worktree-sync-paths` shape:

```yaml
worktree-sync-paths:
  link:                 # hard-linked (NOT symlinked — see caveat below)
    - .docs
    - .env.local
  copy:                 # copied (independent per worktree)
    - some/template.tpl
```

**Important:** `link:` performs **hard-links**, not symbolic links. Sakusen's binary calls `hardLinkDir` under the hood. Implications:

- For source/text trees (markdown, configs), hard-links behave like the symlinks users typically expect — files appear in the worktree, edits sync via shared inodes.
- Hard-links cannot cross filesystems. If `.sakusen/worktrees/` lives on a different filesystem from the main checkout, `link:` will fail; use `copy:` instead.
- For files you want **isolated per worktree** (build output, generated code, per-task `.env` overrides), use `copy:` not `link:`.
- Symbolic links are **not supported** as a `worktree-sync-paths` mode. If you genuinely need symlinks, create them in `worktree-setup-command` (e.g., `ln -s ...`).

## Workflows

> **Read `references/workflow-building.md` before creating or editing any workflow, step, prompt, loop, or template variable.** What follows is only the shape of the block; every field, rule, and pattern lives in that reference.

`workflows:` is a flat YAML sequence — there are no `tasks:`, `one-off:`, or `init:` sub-categories. Each entry is a string ref, an inline `- name: X` mapping, or the named-body sugar `- X:` with a nested body:

```yaml
workflows:
  - implement            # → .sakusen/workflows/implement.yml, else the global pool
  - name: quick-fix      # inline, no pins → always shows the New Task screen
    steps:
      - name: do
        prompt: "fix it"
  - name: housekeeping   # all fields pinned → skips the New Task screen immediately
    description: "Run standard maintenance"   # metadata (picker / MCP); NOT a pin
    input: "Audit and clean the codebase."    # pins the task input
    worktree: true
    branch: sakusen/housekeeping-{{task.id}}
    target: main
    steps:
      - name: cleaning
        prompt: "Audit and clean the codebase."
```

"Kind" is an emergent property of pinning and of binding: a workflow that pins every New Task field (`input` + `worktree` + `branch`/`checkout` + `target`) creates its task immediately instead of showing the form, and a workflow named by a `routines:` entry additionally becomes runnable by that routine's name.

The sibling top-level `routines:` key holds INVOCATION BINDINGS — a name, the workflow it runs, pins that override the workflow's own, and an optional `cadence`. A routine never defines steps; with a cadence it is scheduled, without one it is on-demand (`:RunRoutine`, `sakusen routines run <name>`, MCP `run_routine`):

```yaml
routines:
  - name: compose-wiki
    description: Rebuild the wiki
    workflow: wiki-compose               # required; may name a hidden pool file
    branch: sakusen/wiki-{{task.id}}     # pins beat the workflow's, branch/checkout as a pair
  - name: nightly-sweep                  # same workflow, scheduled binding
    workflow: wiki-compose
    cadence: "0 3 * * *"
```

A workflow body holds `name`, `description`, the pins (`input`, `worktree`, `branch`, `checkout`, `target`), `agent`, `on_complete`, `summarizer_prompt`, the per-workflow worktree/tmux overrides, and `steps:`. A step holds `name`, `description`, `prompt`, `agent`, `timeout`, `human`, `summarization_strategy`, `summarization_prompt`, `require_context`, and `loop` — or, instead of `prompt`, a `parallel:` block that fans out to concurrent headless branches on the same worktree (see the reference's Parallel Groups section). **Execution mode comes from the resolved agent record, never from the step.**

Prompts interpolate `{{task.*}}`, `{{git.*}}`, `{{steps.<name>.context}}`, `{{loop.*}}`, `{{track.*}}`, `{{tasks.<id>.<field>}}`, `{{children.*}}`, and `{{prompt.<name>}}` (inlines `<project>/.sakusen/prompts/<name>.md`, falling back to `~/.sakusen/prompts/<name>.md`, for passages shared across workflows). **Every multi-line variable must be wrapped in a semantic XML-style tag** (`<task-input>`, `<step-context name="...">`, `<track-context>`, `<chat>`, …) so the receiving agent can tell fixed prompt text from interpolated content. Do not hard-wrap prompt prose — see the reference for the canonical tag table, the full variable catalog, and prompt-formatting rules.

## Decision Tree

When the user describes what they want, follow this:

1. **"Just implement tasks"** → Single workflow with an `implementing` step (no pins)
2. **"Review before completing"** → Add a step with `human: true`
3. **"Interactive tmux session"** → Point the step (or workflow) at a tmux-mode agent, e.g. `agent: claude-tmux`. Headless steps just use a headless agent (the scaffolded default).
4. **"Multi-step pipeline"** → Multiple steps with step context passing results between steps
5. **"Iterative review loop"** → Use `loop` config on a fix step pointing back to review; prefer a `step_context_contains` + `marker` exit
6. **"Predefined maintenance job (no user prompt)"** → Pin all fields (`input`, `worktree`, `branch`, `target`) so the New Task screen is skipped
7. **"Bootstrap from PRD (run immediately)"** → Same as above — pin all fields so the task is created immediately
8. **"Share files/dirs across worktrees"** ("symlink X into worktrees", ".env should be available", "docs/configs visible to agents") → Use `worktree-sync-paths` (`link:` for shared/synced files, `copy:` for per-worktree isolated copies). Note this is hard-link, not symlink.
9. **"Run something after worktree creation"** (install deps, generate files, create symlinks) → Use `worktree-setup-command` (single) or `worktree-setup-commands` (multiple)
10. **"Summarize a tmux/conversational step"** → Set `summarization_strategy: summarize_chat` and provide a `summarization_prompt` using `{{chat}}`
11. **"Fan out subtasks / orchestrate child tasks from a step"** → Prompt the step's agent to call the sakusen MCP tool `create_tasks_and_wait` (or `wait_for_tasks` for pre-existing tasks). The step suspends at `awaiting-children` and re-runs with `{{children.summary}}` / `{{children.<id>.<field>}}` populated — see `references/workflow-building.md` → MCP Orchestration Patterns
12. **"Later steps depend on this step's output"** (grilling/planning feeding implementation) → Set `require_context: true` on the producing step so a failed context capture fails the task loudly
13. **"Reference another task's output"** ("build on task 42", "after #17 merges") → Use `{{tasks.<id>.<field>}}` in the task input — active refs auto-block until the referenced task completes
14. **"Talk to me first, then run unattended"** (interview / plan approval / human gate) → A tmux step that publishes a decision record via `update_step_context` and self-advances via `advance_task`, followed by headless steps templating that step's context — see `references/workflow-building.md` → The human-gated step
15. **"Carry standing context across a stream of related tasks"** (sprint/epic/feature-area context) → Interpolate `{{track.context}}` in step prompts (explicit opt-in, empty for trackless tasks) and optionally write back with `update_track_context` — see `references/workflow-building.md` → Tracks in Workflows
16. **"Several workflows repeat the same block of prompt text"** (craft guidance, review rules, commit hygiene) → Put the passage in `.sakusen/prompts/<name>.md` (or `~/.sakusen/prompts/` to share across projects) and reference it as `{{prompt.<name>}}`. Keep step-context wiring and scope-policy sentences inline — see `references/workflow-building.md` → Prompt Includes
17. **"Run the same review with two different models"** / "redundant checks in parallel" / "several agents look at the same diff at once" → A `parallel:` group step: branches run concurrently on the one worktree, `require:` sets the join policy, and the group publishes one aggregated `{{steps.<group>.context}}` for a following synthesis step. Branches must be read-only — for concurrent work that WRITES, use child tasks (`create_tasks_and_wait`) instead. See `references/workflow-building.md` → Parallel Groups
18. **"Run this workflow on a schedule"** / **"give this predefined job a name I can invoke"** ("compose the wiki", "nightly digest", "weekly dependency bump") → A `routines:` entry naming the workflow, with pins and an optional `cadence`. One workflow can back several routines with different pins — see `references/workflow-building.md` → Routines — Invocation Bindings
19. **"Reuse one workflow across projects / customize a shared workflow"** → Author it in `~/.sakusen.yml` or `~/.sakusen/workflows/`, reference it by name from the project, and override individual steps with bare-string step refs — see `references/workflow-building.md` → Global Workflows, Overrides, and Step References

## Discovering undocumented fields

If you encounter a field used in an existing config that this skill doesn't document, or if the user asks about a feature not covered here, the binary itself is the authoritative source. Run:

```bash
strings $(which sakusen) | grep 'yaml:"' | sort -u
```

This lists every YAML field name the binary will accept. Cross-reference unknown fields against the function names exposed in the binary (`strings $(which sakusen) | grep 'aface/sakusen/internal'`) to infer behavior. Update this skill when you confirm new fields work.

## Important Rules

- Step `name` values must be unique within a workflow
- Agent slugs must be kebab-case; `command` is required; `resume_command`/`chat_log_command` are only valid on tmux-mode agents
- Explicit `agent:` / `default_agent:` references must name a slug that exists under `agents:`
- Loop `goto` must reference an earlier step (no forward jumps, no self-reference)
- Loop steps cannot have `human: true`, and cannot resolve to a tmux-mode agent — use a headless agent on the loop step (or its workflow)
- Loop ranges cannot overlap
- A `parallel:` group step sets no `prompt`/`loop`/`human`/`summarization_strategy`/`summarization_prompt`/`require_context`; its branches set no `parallel`/`loop`/`human` and must be inline mappings. `require:` is `all` (default), `any`, or an integer in `1..len(branches)`
- Parallel branch names share the step namespace — unique across the whole workflow — and every branch must resolve to a headless agent. A loop `goto` may target a group but not a branch; retry-from-step takes the group name, not a branch name
- `on_complete` (top-level, or per-workflow override) values: `"commit"`, `"merge"`, `"none"` — moved out of `git:`; `git.on_complete` is now an error
- Never emit the removed keys (`claude:`, `yolo:`, `system_prompt:`, `allowed_summarization_models:`, `print:`, `tmux:`) — all are hard load errors. See [Removed keys](#removed-keys-hard-load-errors--never-emit-these).
- `git.branch_template` supports: `{{task_id}}`, `{{task_slug}}`, `{{task.id}}`, `{{task.title}}`, `{{task.slug}}` — `{{prompt.<name>}}` does NOT work here (prompt fields only)
- Every `{{prompt.<name>}}` must have a matching `.md` file under `.sakusen/prompts/` or `~/.sakusen/prompts/`; a missing file, a bad name (`[A-Za-z0-9_-]+` only), or an included file that itself contains `{{prompt.*}}` is a hard load error
- The file goes at the project root as `.sakusen.yml`
- The `input` pin supplies the task input when the New Task screen is skipped; `description` is separate metadata
- If both `worktree-setup-command` and `worktree-setup-commands` are set, **both run** (singular first, then the list, in order); any non-zero exit fails the task. `worktree-sync-paths` failures, by contrast, only log a warning.

## Validating a config

After **every** write or edit to a `.sakusen.yml`, validate it with the built-in CLI:

```bash
sakusen validate           # validates ./.sakusen.yml
sakusen validate path/to/.sakusen.yml   # validates an explicit file
```

`sakusen validate` runs the same checks the daemon performs at load time, plus a few that the runtime silently tolerates:

- YAML syntax errors
- **Unknown top-level fields** (catches typos like `worktree_sync_paths` for `worktree-sync-paths`)
- **Removed keys** (`claude:`, `yolo:`, `system_prompt:`, `allowed_summarization_models:`, and `tmux:`/`print:` on a workflow or step) — migration errors pointing at the `agents:`/`summarizer:` replacements
- **Agent record shapes** (invalid slug, missing `command`, invalid `mode`, `resume_command`/`chat_log_command` on a headless agent). Note: `agent:`/`default_agent:` *references* are only checked by the full daemon load, not single-file validation — they may resolve against the global tier.
- Workflow loop validity (forward `goto`, self-reference, missing target step, `max_iterations < 1`, overlapping ranges, `human: true` on a loop step)
- Invalid `summarization_strategy` values
- Invalid `on_complete` — top-level or per-workflow (must be `commit`, `merge`, or `none`); the removed `git.on_complete` location produces a migration error
- Invalid `default_priority` (must be `low`, `medium`, `high`, or `urgent`)
- Invalid `tmux_nested_attach_behavior` (must be `switch` or `nest`)
- Duplicate workflow names within the flat list and duplicate step names within a workflow
- File-based workflow errors: missing string ref, inline+file collision, invalid filename, `name:` field in file
- File-based workflow warnings: unreferenced files (hidden)

Exit code is `0` on success and non-zero on the first error. Run it before reporting completion — never declare a config "done" until `sakusen validate` exits cleanly.

## Output Instructions

When generating a `.sakusen.yml`:
1. Ask what kind of workflows the user needs (or infer from context)
2. Load `references/workflow-building.md` before authoring the `workflows:` block
3. Generate a complete, valid YAML file
4. Write it to `.sakusen.yml` in the project root
5. **Run `sakusen validate`** and fix any reported errors before finishing
6. Explain the key choices made
</content>
