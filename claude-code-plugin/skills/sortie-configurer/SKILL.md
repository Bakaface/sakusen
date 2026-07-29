---
name: sortie-configurer
description: >
  Generate and edit .sortie.yml project configuration files for the Sortie daemon.
  Sortie orchestrates Claude Code agents working on tasks in parallel using isolated
  git worktrees. Use when (1) creating a new .sortie.yml config, (2) adding or modifying
  workflows, (3) configuring git, tmux, notifications, or verification settings,
  (4) user mentions "sortie config", ".sortie.yml", or asks about sortie workflow/task
  configuration, (5) troubleshooting sortie config issues.
---

# Sortie Configuration Skill

Generate correct `.sortie.yml` project configuration files for the Sortie daemon.

## What is Sortie?

Sortie is a daemon that orchestrates multiple Claude Code agents working on tasks in parallel. Each task runs in an isolated git worktree. Configuration lives in `.sortie.yml` at the project root.

## Config Loading Order (later overrides earlier)

1. Built-in defaults (hardcoded)
2. Global daemon config: `~/.config/sortie/config.yaml` (subset of fields, no workflows)
3. Global sortie config: `$XDG_CONFIG_HOME/sortie/config.yml` if present, else `~/.sortie.yml`
4. **Project config: `.sortie.yml`** (this is what you generate)

## Quick Start

Minimal working config:

```yaml
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

## Top-Level Fields

| Field | Type | Default | Description |
|---|---|---|---|
| `max_workers` | int | `3` | Max concurrent Claude agents |
| `default_priority` | string | `"medium"` | `low`, `medium`, `high`, `urgent` |
| `yolo` | bool | `false` | Pass `--dangerously-skip-permissions` to Claude |
| `poll_interval` | string | `"5s"` | Daemon task-polling cadence (Go duration string). Rarely overridden per-project. |
| `system_prompt` | string | minimal default | Preamble of the system prompt passed to every spawned agent via `--system-prompt`. Sortie always appends a fixed verification footer ("run the project's own test/lint commands") and the resolved step prompt — you only control the preamble. |
| `verification` | object | — | Summarizer verification settings (`max_retries`, `verify_summarizer`) |
| `git` | object | — | Branch naming, base branch, completion action |
| `workflows` | list | — | **Primary config block** — flat list of workflow pipelines |
| `notifications` | object | — | Desktop notification toggles |
| `claude` | object | — | Override the Claude binary: `command` (path/name) and `default_args` (list of strings). |
| `allowed_summarization_models` | list[string] | `[haiku, sonnet, opus]` | Restrict which models the summarizer auto-selects from (subset of `haiku`, `sonnet`, `opus`; cheapest fitting model wins). Per-step override available. |
| `options` | object | — | TUI display toggles: `number`, `branch`, `target`, `branchview` (bools), `animation` (`enabled` bool, `duration` ms). |
| `tmux_nested_attach_behavior` | string | `"switch"` | `"switch"` or `"nest"` for tmux-in-tmux |
| `worktree-sync-paths` | object | — | Hard-link or copy paths from main checkout into each worktree (e.g., `.docs`, `.env`). Also settable per-workflow. See [Sharing files into worktrees](#sharing-files-into-worktrees). |
| `worktree-setup-command` | string | — | Single shell command run after worktree creation (`{{worktree_path}}` available). Also settable per-workflow. |
| `worktree-setup-commands` | list[string] | — | Multiple setup commands run in order; preferred over the singular form when more than one step is needed. Also settable per-workflow. |
| `tmux-setup-command` | string | — | Shell command run when launching a tmux step. Variables: `{{session_name}}`, `{{worktree_path}}`, `{{run_agent}}`, `{{claude_command}}`. Also settable per-workflow. |

> **Per-workflow overrides:** `worktree-sync-paths`, `worktree-setup-command`, `worktree-setup-commands`, and `tmux-setup-command` may be set on an individual workflow (an entry in the flat `workflows:` list). A non-empty workflow-level value fully overrides the project-level one for tasks running that workflow.

### Field-name convention

Top-level field names mix two casing styles — **don't guess, copy exactly**:

- **kebab-case:** `worktree-sync-paths`, `worktree-setup-command`, `worktree-setup-commands`, `tmux-setup-command`
- **snake_case:** `max_workers`, `default_priority`, `system_prompt`, `poll_interval`, `allowed_summarization_models`, `tmux_nested_attach_behavior`, `base_branch`, `branch_template`, `on_complete`

If you author an unrecognized variant (`worktree_sync_paths`, `tmux_setup_command`, etc.), Sortie will silently ignore it.

### Sharing files into worktrees

`worktree-sync-paths` shape:

```yaml
worktree-sync-paths:
  link:                 # hard-linked (NOT symlinked — see caveat below)
    - .docs
    - .env.local
  copy:                 # copied (independent per worktree)
    - some/template.tpl
```

**Important:** `link:` performs **hard-links**, not symbolic links. Sortie's binary calls `hardLinkDir` under the hood. Implications:

- For source/text trees (markdown, configs), hard-links behave like the symlinks users typically expect — files appear in the worktree, edits sync via shared inodes.
- Hard-links cannot cross filesystems. If `.sortie/worktrees/` lives on a different filesystem from the main checkout, `link:` will fail; use `copy:` instead.
- For files you want **isolated per worktree** (build output, generated code, per-task `.env` overrides), use `copy:` not `link:`.
- Symbolic links are **not supported** as a `worktree-sync-paths` mode. If you genuinely need symlinks, create them in `worktree-setup-command` (e.g., `ln -s ...`).

## Workflow List

`workflows:` is a flat YAML sequence — there are no `tasks:`, `one-off:`, or `init:` sub-categories. Each item is either a string ref or an inline mapping:

```yaml
workflows:
  - implement            # → .sortie/workflows/implement.yml (file-based)
  - name: quick-fix      # inline, no pins → always shows New Task screen
    steps:
      - name: do
        prompt: "fix it"
  - name: housekeeping   # all fields pinned → skips New Task screen immediately
    description: "Run standard maintenance"   # metadata (workflow picker / MCP); NOT a pin
    input: "Audit and clean the codebase."    # pins the task input
    worktree: true
    branch: sortie/housekeeping-{{task.id}}
    target: main
    steps:
      - name: cleaning
        prompt: "Audit and clean the codebase."
        print: true
```

"Kind" is an emergent property of pinning: the `n` key (and `:RunTask`) operates over the single flat list. Workflows that have all fields pinned (`input` + `worktree` + `branch`/`checkout` + `target`) create a task immediately without showing the New Task form. Two shortcuts: when `worktree: false` is pinned, `input` alone suffices (the git fields are N/A); workflows whose **first step runs in tmux** may be created without an input at all — the user drives the session interactively. (`description` is separate human-readable metadata, never a pin.)

### Pinnable fields

A workflow may pin any subset of New Task screen fields:

| Field | Type | Effect |
|---|---|---|
| `input` | string | Pins the task input; hides the input box from the form |
| `worktree` | bool | Pins the worktree on/off toggle |
| `branch` | string | Pins a new-branch template; forces branch-mode "new" |
| `checkout` | string | Pins an existing branch to check out; forces branch-mode "existing" |
| `target` | string | Pins the target/base branch |

Validation: `branch` and `checkout` are mutually exclusive; `branch`/`checkout`/`target` are rejected when `worktree: false`.

### Inline vs. File-Based Workflows

- **String refs** → resolved against `.sortie/workflows/<name>.yml` first, then the **global pool** — every workflow resolved from the global `~/.sortie.yml` (inline or file-based under `~/.sortie/workflows/`, referenced or hidden alike)
- **Inline maps** → full workflow definition embedded directly in `.sortie.yml`

A project definition (inline or local file) with the same name as a global workflow legally **overrides** it — the inline-vs-file collision error applies only within a single config scope.

A workflow file at `.sortie/workflows/<name>.yml` contains the same fields as an inline workflow body — minus the `name:` field, which is always the filename. Use kebab-case filenames starting with a letter or digit (`[a-z0-9][a-z0-9-]*`, extension `.yml` or `.yaml`). Subdirectories are not supported.

**Files not referenced from `.sortie.yml` are loaded as hidden.** Hidden workflows are:

- **Not** shown in TUI menus (the `n` shortcut)
- **Reachable** via `:RunTask <name>` (and tab completion)
- **Reachable** via CLI: `sortie create -w <name>` accepts hidden workflows
- **Returned** by the MCP `list_workflows` tool with `"hidden": true`

### When to split a workflow into a file

Default to inline. Split when any of the following holds:

- The resulting `.sortie.yml` would exceed ~200 lines
- A single workflow body exceeds ~40 lines
- There are more than five workflows

Splitting trades single-file readability for per-workflow editability. For tiny projects, inline beats file-sprawl.

### Hard errors at config load

- String ref points to a missing file (`.sortie/workflows/<name>.yml`) and is not in the global pool.
- Same name is both inlined in `.sortie.yml` and present as a file.
- A file-based workflow sets a `name:` field (filename is authoritative).
- A workflow file uses a non-kebab-case filename or lives in a subdirectory of `.sortie/workflows/`.

### Warnings (non-fatal — surfaced by `sortie validate`)

- File present under `.sortie/workflows/` but not referenced in `.sortie.yml` (it's hidden).

## Workflow Structure

```yaml
- name: my-workflow          # unique name (required)
  description: "..."         # human-readable metadata (workflow picker / MCP); NOT a pin
  input: "..."               # optional: pins the task input (hides the New Task input box)
  print: false               # workflow-level default: false = tmux (default), true = headless claude -p
  summarizer_prompt: "..."   # custom prompt for post-completion summarizer
  worktree-sync-paths: {...} # optional per-workflow override of the project-level value
  steps:                     # ordered list of steps (required)
    - name: step-name        # unique step identifier (required)
      prompt: "..."          # template string sent to Claude (required)
      print: true            # per-step override (omit to inherit workflow default)
      timeout: "30m"         # Go duration string
      human: false           # pause for human approval
      summarization_strategy: summarize_chat   # how this step's context is captured (see below)
      summarization_prompt: "..."              # prompt fed to the summarizer for THIS step's context
      allowed_summarization_models: [haiku]    # optional per-step summarizer model allowlist
      require_context: false   # true = fail the task if this step's summarize_chat context can't be captured
      loop:                  # optional: jump back to earlier step
        goto: "step-name"    # must reference an earlier step
        max_iterations: 3    # >= 1
        exit_condition:
          step_context_empty: "step-name"  # exit early if this step's context is empty
```

### `print`: tmux vs. headless execution

**Tmux is the default execution mode.** Each step runs inside an interactive tmux session hosting the Claude Code TUI, and the daemon auto-advances on turn-end. Set `print: true` to run a step headless via `claude -p` instead.

- **Workflow-level `print`** (bool, default `false`) sets the default for all steps.
- **Step-level `print`** (bool) overrides the workflow default for that step. Omit it to inherit.

| `print` | `human` | Behavior |
|---|---|---|
| `false` (default) | `false` | tmux + auto-advance on turn-end |
| `false` | `true` | tmux + manual approval |
| `true` | `false` | headless `claude -p` + auto-advance on exit |
| `true` | `true` | headless `claude -p`, then pause at `awaiting-approval` |

> **⚠️ The legacy `tmux:` field was removed (pre-Sortie-54) and the daemon now refuses to load any config containing it.** Never emit `tmux:` on a workflow or step. Migration: `tmux: true` → `print: false` (or omit — tmux is the default); `tmux: false` → `print: true`.

The `mode:` field (e.g. `mode: "automatic"`) is vestigial — it is parsed but does not affect execution. Do not rely on it; omit it from new configs.

### Step summarization

**The default strategy is `summarize_chat`** (when `summarization_strategy` is unset). It summarizes the entire transcript via a second Claude call using `summarization_prompt`. Inside `summarization_prompt`, the variable `{{chat}}` expands to the full transcript. This is essential for tmux/grilling steps where the meaningful output is the conversation, not a final message; it is also the default for ordinary steps.

Set `summarization_strategy: last_message` to instead capture only the agent's final output message as context (cheap — no extra Claude call — but often a one-liner that loses decisions; not usable for tmux steps, which have no NDJSON result event).

Set `summarization_strategy: none` to skip context capture entirely for the step — no last-message text is stored and no summarization pass is run. Useful for steps whose output is not meaningful to later steps (`{{steps.<name>.context}}` will resolve to empty).

The summarizer auto-selects the cheapest model (`haiku` < `sonnet` < `opus`) whose prompt-size ceiling fits the transcript. Restrict the candidate set with `allowed_summarization_models` at project level or per-step (step-level overrides project-level).

**`require_context: true`** makes a failure to capture a step's `summarize_chat` context **fail the task** instead of silently advancing with an empty context (the default is best-effort: warn and proceed). Set it on steps whose output later steps template via `{{steps.<name>.context}}` — e.g. a grilling/interview step feeding an implementing step. Only meaningful for tmux steps with `summarize_chat`; ignored otherwise.

### Prompt formatting

Prompt fields (`prompt`, `summarization_prompt`, `summarizer_prompt`, `system_prompt`) are LLM input, not human reading. Do not hard-wrap prose at ~80 columns — block scalars (`|`) preserve every newline as a token. Keep only the structural newlines: blank lines between paragraphs, one line per list item (continuation text stays on the item line), code fences verbatim. Reflow on contact when editing existing prompts.

### Wrapping multi-line interpolations

Several template variables expand to **multi-line** content at render time (a step's full output, a transcript, a task input). When inlined raw, the boundary between fixed prompt text and interpolated content vanishes — paragraphs of step context blend into the next instruction, and the receiving agent cannot tell where one ends and the other begins.

**Rule: wrap every multi-line interpolation in a semantic XML-style tag named after the variable.** Place the opening tag, the variable, and the closing tag each on their own line so the captured content sits between two clean boundaries:

```yaml
prompt: |
  Implement the following:
  <task-input>
  {{task.input}}
  </task-input>

  Earlier review feedback:
  <step-context name="reviewing">
  {{steps.reviewing.context}}
  </step-context>
```

Canonical tag for each multi-line variable:

| Variable | Wrapping tag |
|---|---|
| `{{task.input}}` | `<task-input>...</task-input>` |
| `{{task.context}}` | `<task-context>...</task-context>` |
| `{{task.images}}` | `<task-images>...</task-images>` |
| `{{steps.<name>.context}}` | `<step-context name="<name>">...</step-context>` |
| `{{artifacts.<name>}}` | `<step-context name="<name>">...</step-context>` (alias of the above) |
| `{{tasks.<id>.input}}` | `<task-input id="<id>">...</task-input>` |
| `{{tasks.<id>.context}}` | `<task-context id="<id>">...</task-context>` |
| `{{children.summary}}` | `<children-summary>...</children-summary>` |
| `{{children.<id>.context}}` | `<child-context id="<id>">...</child-context>` |
| `{{chat}}` | `<chat>...</chat>` |

Single-line variables (`{{task.id}}`, `{{task.title}}`, `{{task.slug}}`, `{{task.branch}}`, `{{git.base_branch}}`, `{{git.target_branch}}`, `{{git.repo_root}}`, `{{loop.iteration}}`, `{{loop.max_iterations}}`, `{{tasks.<id>.title}}`, `{{tasks.<id>.branch}}`, `{{children.<id>.status}}`, `{{children.<id>.title}}`) are inlined into surrounding prose **without** wrapping — they fit on one line and a tag would only add noise.

Do **not** use triple-backtick fences for this. Interpolated content (especially `{{chat}}` and summarized step contexts) routinely contains its own code fences, which would break the outer fence. XML-style tags survive arbitrary nested content.

## Template Variables

**Step prompts** (`prompt:`) and **summarizer prompts** (`summarizer_prompt:`):

Variables marked **multi-line** must be wrapped in a semantic tag — see [Wrapping multi-line interpolations](#wrapping-multi-line-interpolations).

| Variable | Description |
|---|---|
| `{{task.id}}` | Numeric task ID |
| `{{task.title}}` | Task title |
| `{{task.input}}` | Full task input **(multi-line — wrap in `<task-input>`)** |
| `{{task.context}}` | Task's accumulated context summary (from a prior run / continuation) **(multi-line — wrap in `<task-context>`)** |
| `{{task.slug}}` | URL-safe slug from title |
| `{{task.branch}}` | Resolved branch name |
| `{{task.images}}` | Newline-joined attached image paths **(multi-line — wrap in `<task-images>`)** |
| `{{git.base_branch}}` | Configured base branch |
| `{{git.target_branch}}` | Task's target/merge branch |
| `{{git.repo_root}}` | Repository root path |
| `{{loop.iteration}}` | Current loop iteration (in loops) |
| `{{loop.max_iterations}}` | Max loop iterations (in loops) |
| `{{steps.<step_name>.context}}` | Context captured from a prior step's result **(multi-line — wrap in `<step-context name="<step_name>">`)** |
| `{{artifacts.<step_name>}}` | Backward compat alias for `{{steps.<step_name>.context}}` **(multi-line — same wrapping)** |
| `{{tasks.<id>.<field>}}` | Field of **another task** by numeric ID. Fields: `title`, `branch`, `input`, `context`. Missing task / lookup error resolves to empty. See reference: Cross-Task References. |
| `{{children.summary}}` | Digest of all child tasks after a `create_tasks_and_wait` resume **(multi-line — wrap in `<children-summary>`)** |
| `{{children.<id>.<field>}}` | Field of a specific child task. Fields: `id`, `title`, `status` (`completed`/`failed`), `context`. See reference: Child Task Orchestration. |

**Step `summarization_prompt:`** — same variables as above, plus:

| Variable | Description |
|---|---|
| `{{chat}}` | Full transcript of the step being summarized **(multi-line — wrap in `<chat>`)**. Only valid inside `summarization_prompt`. |

**`worktree-setup-command` / `worktree-setup-commands:`** — only `{{worktree_path}}` is available. Commands run with the **project root** (not the worktree) as cwd; a non-zero exit **fails the task**.

**`tmux-setup-command:`**:

| Variable | Description |
|---|---|
| `{{session_name}}` | Tmux session name created for the task |
| `{{worktree_path}}` | Absolute path to the task's worktree |
| `{{run_agent}}` | Pre-built command string that launches the Claude agent inside the worktree |
| `{{claude_command}}` | Raw claude CLI invocation (prefer `{{run_agent}}`) |

If the command contains `{{run_agent}}` or `{{claude_command}}`, **you control where the agent runs** — Sortie will not auto-start it in window 0. Omit both and Sortie launches the agent itself after your layout command runs.

**Environment variables** — every step's Claude process (and anything it spawns) gets: `SORTIE_TASK_ID`, `SORTIE_STEP`, `SORTIE_WORKTREE`, `SORTIE_PROJECT_PATH` (repo root, not the worktree), `SORTIE_PURPOSE=step`. Useful in prompts that shell out or call sortie MCP tools.

## Decision Tree

When the user describes what they want, follow this:

1. **"Just implement tasks"** → Single workflow with an `implementing` step (no pins)
2. **"Review before completing"** → Add a step with `human: true`
3. **"Interactive tmux session"** → This is the default — omit `print` (or set `print: false`). Set `print: true` only to opt a step into headless `claude -p`.
4. **"Multi-step pipeline"** → Multiple steps with step context passing results between steps
5. **"Iterative review loop"** → Use `loop` config on a fix step pointing back to review
6. **"Predefined maintenance job (no user prompt)"** → Pin all fields (`input`, `worktree`, `branch`, `target`) so the New Task screen is skipped
7. **"Bootstrap from PRD (run immediately)"** → Same as above — pin all fields so the task is created immediately
8. **"Share files/dirs across worktrees"** ("symlink X into worktrees", ".env should be available", "docs/configs visible to agents") → Use `worktree-sync-paths` (`link:` for shared/synced files, `copy:` for per-worktree isolated copies). Note this is hard-link, not symlink.
9. **"Run something after worktree creation"** (install deps, generate files, create symlinks) → Use `worktree-setup-command` (single) or `worktree-setup-commands` (multiple)
10. **"Summarize a tmux/conversational step"** → Set `summarization_strategy: summarize_chat` and provide a `summarization_prompt` using `{{chat}}`
11. **"Fan out subtasks / orchestrate child tasks from a step"** → Prompt the step's agent to call the sortie MCP tool `create_tasks_and_wait` (or `wait_for_tasks` for pre-existing tasks). The step suspends at `awaiting-children` and re-runs with `{{children.summary}}` / `{{children.<id>.<field>}}` populated — see reference: Child Task Orchestration
12. **"Later steps depend on this step's output"** (grilling/planning feeding implementation) → Set `require_context: true` on the producing step so a failed context capture fails the task loudly
13. **"Reference another task's output"** ("build on task 42", "after #17 merges") → Use `{{tasks.<id>.<field>}}` in the task input — active refs auto-block until the referenced task completes

For complete field reference with validation rules and examples, read `references/config-reference.md`.

## Discovering undocumented fields

If you encounter a field used in an existing config that this skill doesn't document, or if the user asks about a feature not covered here, the binary itself is the authoritative source. Run:

```bash
strings $(which sortie) | grep 'yaml:"' | sort -u
```

This lists every YAML field name the binary will accept. Cross-reference unknown fields against the function names exposed in the binary (`strings $(which sortie) | grep 'aface/sortie/internal'`) to infer behavior. Update this skill when you confirm new fields work.

## Important Rules

- Step `name` values must be unique within a workflow
- Loop `goto` must reference an earlier step (no forward jumps, no self-reference)
- Loop steps cannot have `human: true`, and cannot run in tmux — set `print: true` on the loop step (or its workflow)
- Loop ranges cannot overlap
- `on_complete` (top-level, or per-workflow override) values: `"commit"`, `"merge"`, `"none"` — moved out of `git:`; `git.on_complete` is now an error
- Never emit the removed `tmux:` field — use `print:` (inverted). The daemon hard-rejects `tmux:` at load.
- `git.branch_template` supports: `{{task_id}}`, `{{task_slug}}`, `{{task.id}}`, `{{task.title}}`, `{{task.slug}}`
- The file goes at the project root as `.sortie.yml`
- The `input` pin supplies the task input when the New Task screen is skipped; `description` is separate metadata
- If both `worktree-setup-command` and `worktree-setup-commands` are set, **both run** (singular first, then the list, in order); any non-zero exit fails the task. `worktree-sync-paths` failures, by contrast, only log a warning.

## Validating a config

After **every** write or edit to a `.sortie.yml`, validate it with the built-in CLI:

```bash
sortie validate           # validates ./.sortie.yml
sortie validate path/to/.sortie.yml   # validates an explicit file
```

`sortie validate` runs the same checks the daemon performs at load time, plus a few that the runtime silently tolerates:

- YAML syntax errors
- **Unknown top-level fields** (catches typos like `worktree_sync_paths` for `worktree-sync-paths`)
- **Removed `tmux:` field** on a workflow or step (hard error pointing at `print:`)
- Workflow loop validity (forward `goto`, self-reference, missing target step, `max_iterations < 1`, overlapping ranges, `human: true` or tmux mode on a loop step)
- Invalid `summarization_strategy` values
- Invalid `allowed_summarization_models` entries (must be `haiku`, `sonnet`, or `opus`)
- Invalid `on_complete` — top-level or per-workflow (must be `commit`, `merge`, or `none`); the removed `git.on_complete` location produces a migration error
- Invalid `default_priority` (must be `low`, `medium`, `high`, or `urgent`)
- Invalid `tmux_nested_attach_behavior` (must be `switch` or `nest`)
- Duplicate workflow names within the flat list and duplicate step names within a workflow
- File-based workflow errors: missing string ref, inline+file collision, invalid filename, `name:` field in file
- File-based workflow warnings: unreferenced files (hidden)

Exit code is `0` on success and non-zero on the first error. Run it before reporting completion — never declare a config "done" until `sortie validate` exits cleanly.

## Output Instructions

When generating a `.sortie.yml`:
1. Ask what kind of workflows the user needs (or infer from context)
2. Generate a complete, valid YAML file
3. Write it to `.sortie.yml` in the project root
4. **Run `sortie validate`** and fix any reported errors before finishing
5. Explain the key choices made
