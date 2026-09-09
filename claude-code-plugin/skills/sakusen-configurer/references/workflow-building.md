# Sakusen Workflow Building Reference

Everything needed to author the `workflows:` list: entry shapes, pins, step fields, prompts,
template variables, loops, tracks, and MCP orchestration patterns.

## Contents

- [Workflow Entry Shapes](#workflow-entry-shapes)
- [Pinnable Fields](#pinnable-fields)
- [File-Based and Hidden Workflows](#file-based-and-hidden-workflows)
- [Global Workflows, Overrides, and Step References](#global-workflows-overrides-and-step-references)
- [Track Workflows](#track-workflows)
- [Workflow Fields](#workflow-fields)
- [Step Fields](#step-fields)
- [Execution Mode](#execution-mode)
- [Step Summarization](#step-summarization)
- [Loops](#loops)
- [Parallel Groups](#parallel-groups)
- [Prompt Formatting](#prompt-formatting)
- [Wrapping Multi-Line Interpolations](#wrapping-multi-line-interpolations)
- [Template Variables](#template-variables)
- [Tracks in Workflows](#tracks-in-workflows)
- [Cross-Task References](#cross-task-references)
- [MCP Orchestration Patterns](#mcp-orchestration-patterns)
- [Load Errors and Warnings](#load-errors-and-warnings)

---

## Workflow Entry Shapes

`workflows:` is a flat YAML sequence — there are no `tasks:`, `one-off:`, or `init:`
sub-categories. Each entry takes one of three shapes:

```yaml
workflows:
  # 1. String ref — resolved against .sakusen/workflows/<name>.yml, then the global pool
  - implement

  # 2. Standard inline definition — a mapping with a `name:` key
  - name: quick-fix
    steps:
      - name: do
        prompt: "fix it"

  # 3. Named-body sugar — a mapping whose single key is NOT a known workflow field;
  #    that key becomes the workflow name. The body must be nested under it.
  - housekeeping:
      description: "Run standard maintenance"
      steps:
        - name: cleaning
          prompt: "Audit and clean the codebase."

  # 3b. Named-body sugar with an empty body == the bare ref `- deploy`
  - deploy:
```

Sugar detection is structural: a mapping with **exactly one key** that is not one of
`name`, `description`, `input`, `agent`, `on_complete`, `steps`, `summarizer_prompt`,
`worktree-sync-paths`, `worktree-setup-command`, `worktree-setup-commands`,
`tmux-setup-command`. Indent the body one level deeper than the name key — a sibling
`steps:` makes it a two-key mapping and the sugar is not recognized.

Prefer the standard inline form for new configs. The sugar is most useful when overriding a
global workflow by name (see [Step References](#step-references)).

---

## Pinnable Fields

"Kind" is an emergent property of pinning: the `n` key (and `:RunTask`) operates over the
single flat list. A workflow may pin any subset of New Task screen fields:

| Field | Type | Effect |
|---|---|---|
| `input` | string | Pins the task input; hides the input box from the form |
| `worktree` | bool | Pins the worktree on/off toggle |
| `branch` | string | Pins a new-branch template; forces branch-mode "new" |
| `checkout` | string | Pins an existing branch to check out; forces branch-mode "existing" |
| `target` | string | Pins the target/base branch |

Validation: `branch` and `checkout` are mutually exclusive; `branch`/`checkout`/`target` are
rejected when `worktree: false`.

**Skipping the New Task screen.** A workflow that pins *every* field (`input` + `worktree` +
`branch`/`checkout` + `target`) creates a task immediately without showing the form. Two
shortcuts: when `worktree: false` is pinned, `input` alone suffices (the git fields are N/A);
workflows whose **first step resolves to a tmux-mode agent** may be created without an input
at all — the user drives the session interactively.

`description` is separate human-readable metadata (workflow picker, MCP `list_workflows`) and
is **never** a pin.

```yaml
- name: housekeeping           # all fields pinned → skips New Task screen immediately
  description: "Run standard maintenance"   # metadata; NOT a pin
  input: "Audit and clean the codebase."    # pins the task input
  worktree: true
  branch: sakusen/housekeeping-{{task.id}}
  target: main
  steps:
    - name: cleaning
      prompt: "Audit and clean the codebase."
```

---

## File-Based and Hidden Workflows

A workflow file at `.sakusen/workflows/<name>.yml` contains the same fields as an inline
workflow body — minus the `name:` field, which is always the filename. Use kebab-case
filenames starting with a letter or digit (`[a-z0-9][a-z0-9-]*`, extension `.yml` or
`.yaml`). Subdirectories are not supported.

**Files not referenced from `.sakusen.yml` are loaded as hidden.** Hidden workflows are:

- **Not** shown in TUI menus (the `n` shortcut) and never chosen as the implicit default
- **Reachable** via `:RunTask <name>` (and tab completion)
- **Reachable** via CLI: `sakusen create -w <name>` accepts hidden workflows
- **Returned** by the MCP `list_workflows` tool with `"hidden": true`

### When to split a workflow into a file

Default to inline. Split when any of the following holds:

- The resulting `.sakusen.yml` would exceed ~200 lines
- A single workflow body exceeds ~40 lines
- There are more than five workflows

Splitting trades single-file readability for per-workflow editability. For tiny projects,
inline beats file-sprawl.

---

## Global Workflows, Overrides, and Step References

Workflows can be authored once in the global tier and reused by every project.

**Authoring tiers:**

| Location | Scope |
|---|---|
| `.sakusen.yml` inline / `.sakusen/workflows/*.yml` | Project |
| `~/.sakusen.yml` inline / `~/.sakusen/workflows/*.yml` | Global |

Every workflow resolved from the global config — inline or file-based, referenced or hidden
alike — lands in the **global pool**. A project-level string ref resolves against
`.sakusen/workflows/<name>.yml` first, then falls back to the global pool:

```yaml
# project .sakusen.yml
workflows:
  - shared-impl      # defined in ~/.sakusen.yml or ~/.sakusen/workflows/shared-impl.yml
```

**Per-project override.** A project definition (inline, named-body sugar, or a local
`.sakusen/workflows/<name>.yml`) with the same name as a global workflow legally **overrides**
it. The inline-vs-file collision error applies only *within* a single config scope.

### Step References

Inside a project workflow that overrides a same-named global workflow, a **bare-string entry
in `steps:` is a step reference** resolved against the base (global) workflow. This is how to
customize a shared global workflow by swapping one step instead of re-declaring all of them:

```yaml
# ~/.sakusen.yml — the shared base
workflows:
  - name: the-work
    steps:
      - name: planning
        prompt: "Plan the work..."
      - name: implementing
        prompt: "Implement the plan..."
      - name: reviewing
        prompt: "Review the change..."
```

```yaml
# project .sakusen.yml — reuse two steps, replace one, add one
workflows:
  - name: the-work
    steps:
      - planning                 # ref → base "planning" step, verbatim
      - name: implementing       # same name as a base step → REPLACES it wholesale
        prompt: "Implement the plan, then run `mise run test`."
      - name: verifying          # brand-new local step
        prompt: "Run the full test suite and report failures."
```

Rules:

- A bare-string step carries only its name; it is replaced by the same-named step from the
  base workflow during load.
- An inline step whose name matches a base step **fully replaces** it — replace-not-merge,
  the same semantics as workflow-level overrides. There is no field-level merging.
- Dropping a step = omitting it. Adding a step = an inline entry with a new name. Reordering
  = listing the refs in a different order.
- Referencing a step that is **absent from the base** — or referencing anything when there is
  **no same-named global workflow** — is a hard load error.
- Duplicate step names after resolution are a hard load error.

### `on_complete` precedence

`on_complete` resolution is **most-specific-wins**:

1. The workflow's own `on_complete`, wherever the workflow was defined (project **or** global)
2. The project `.sakusen.yml` top-level `on_complete`
3. The global `~/.sakusen.yml` top-level `on_complete`, or the built-in default `commit`

`on_complete` is a property of the workflow's shape — whether its last step is a human gate — so
an explicit workflow value is authoritative in every project that adopts it. A project that needs
a different value for a global workflow shadows it by name (inline in `.sakusen.yml` or a
`.sakusen/workflows/<name>.yml` file) and sets `on_complete` there.

---

## Track Workflows

A track's own workflows live under a `workflows/` directory inside the track's slug directory:

| Location | Tier |
|---|---|
| `.sakusen/tracks/<slug>/workflows/*.yml` | Project |
| `~/.sakusen/tracks/<slug>/workflows/*.yml` | Global |

They follow the exact same file rules as `.sakusen/workflows/` (flat directory, `.yml`/`.yaml`
only, kebab-case base names, no `name:` field), and go through the same pin/step/loop
validation as every other workflow.

Two things make them different:

- **Namespaced.** Each loads as `<slug>:<name>` — `.sakusen/tracks/auth/workflows/harden.yml`
  becomes the workflow `auth:harden`. The track slug directory name must be kebab-case.
- **Always hidden.** They never appear in TUI menus and are never the implicit default; they
  are reachable **only** by their exact namespaced name (`:RunTask auth:harden`,
  `sakusen create -w auth:harden`, MCP `create_task`).

Project-tier track workflows shadow global-tier ones with the same namespaced name. There is
no implicit shadowing of ordinary workflows — `auth:harden` and `harden` are unrelated names.

---

## Workflow Fields

```yaml
- name: my-workflow          # unique name (required; omit in file-based workflows)
  description: "..."         # human-readable metadata (workflow picker / MCP); NOT a pin
  input: "..."               # pins the task input (hides the New Task input box)
  worktree: true             # pin
  branch: "..."              # pin (mutually exclusive with checkout)
  checkout: "..."            # pin
  target: main               # pin
  agent: claude              # agent slug every step inherits unless it sets its own
  on_complete: commit        # per-workflow override: "commit" | "merge" | "none"
  summarizer_prompt: "..."   # custom prompt for the post-completion task summarizer
  worktree-sync-paths: {...} # per-workflow override of the project-level value
  worktree-setup-command: "..."
  worktree-setup-commands: ["...", "..."]
  tmux-setup-command: "..."
  steps: [...]               # ordered list of steps (required)
```

A non-empty workflow-level `worktree-sync-paths` / `worktree-setup-command` /
`worktree-setup-commands` / `tmux-setup-command` fully overrides the project-level value for
tasks running that workflow.

---

## Step Fields

```yaml
steps:
  - name: step-name        # unique step identifier within the workflow (required)
    description: "..."     # human-readable metadata surfaced via MCP list_workflows; NOT a prompt
    prompt: "..."          # template string sent to the agent (required)
    agent: claude-tmux     # per-step agent override (omit to inherit workflow/default_agent)
    timeout: "30m"         # Go duration string; default "30m"
    human: false           # pause at awaiting-approval after the step
    summarization_strategy: summarize_chat   # how this step's context is captured
    summarization_prompt: "..."              # prompt fed to the summarizer for THIS step
    require_context: false # true = fail the task if summarize_chat context can't be captured
    loop: {...}            # jump back to an earlier step (see Loops)
    parallel: {...}        # fan out to concurrent branches instead of running a prompt (see Parallel Groups)
```

A step sets EITHER `prompt:` OR `parallel:`, never both — a `parallel:` step is a container, not
a step that runs an agent. See [Parallel Groups](#parallel-groups).

- **`description:`** is pure metadata. It is surfaced through the MCP `list_workflows` tool
  (each step entry carries its own `name` and `description`) so an orchestrating agent can
  reason about a workflow's shape before creating a task. It is **never** interpolated into
  prompts. Distinct from the workflow-level `description:`.
- **`timeout:`** Go duration strings: `"30m"`, `"1h"`, `"1h30m"`, `"45m"`, `"2h"`.
- **`human: true`** pauses the task at `awaiting-approval`; the user reviews in the TUI and
  approves to continue. Use for review gates.
- **Agent cascade:** step `agent:` → workflow `agent:` → top-level `default_agent:` → the
  `"claude"` slug. Explicit references to unknown slugs are load errors.
- **`require_context: true`** makes a failure to capture this step's `summarize_chat` context
  **fail the task** instead of silently advancing with an empty context (the default is
  best-effort: warn and proceed). Set it on steps whose output later steps template via
  `{{steps.<name>.context}}` — e.g. a grilling/interview step feeding an implementing step.
  Only meaningful for tmux steps with `summarize_chat`; ignored otherwise.
- The step-level **`mode:`** field (e.g. `mode: "automatic"`) is vestigial — parsed but
  without effect. Omit it from new configs. The meaningful `mode` lives on the agent record.

### Step context flow

After each step completes, its captured output is stored in the `task_steps` table and is
available to later steps via `{{steps.<step_name>.context}}` (or the backward-compat alias
`{{artifacts.<step_name>}}`).

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
    human: true
    prompt: |
      Review the implementation:
      <step-context name="implementing">
      {{steps.implementing.context}}
      </step-context>
```

---

## Execution Mode

A step's execution mode comes from the **agent record it resolves to**, never from the step:

- **headless** — Sakusen spawns the agent command, streams its stdout, auto-advances on exit;
  the result text comes from `$SAKUSEN_RESULT_FILE` (stdout-tail fallback).
- **tmux** — Sakusen runs the command inside a detached tmux session. The user can attach to
  watch/interact; the task shows `tmux` status and the workflow pauses until a turn-end
  sentinel lands (auto-advance) or the user advances manually (`c`).

| resolved mode | `human` | Behavior |
|---|---|---|
| `headless` | `false` | headless spawn + auto-advance on exit |
| `headless` | `true` | headless spawn, then pause at `awaiting-approval` |
| `tmux` | `false` | tmux + auto-advance on turn-end sentinel (manual-advance for hookless agents) |
| `tmux` | `true` | tmux + manual approval |

> **⚠️ The `print:` and `tmux:` fields were removed and the daemon refuses to load any config
> containing them.** Never emit either on a workflow or step. Migration: `print: true` → an
> agent with `mode: headless`; `print: false` / `tmux: true` → an agent with `mode: tmux`.

---

## Step Summarization

**The default strategy is `summarize_chat`** (when `summarization_strategy` is unset). It
summarizes the step's chat via the configured `summarizer:` command using
`summarization_prompt`. Inside `summarization_prompt`, `{{chat}}` expands to the full chat
content. This is essential for tmux/grilling steps where the meaningful output is the
conversation, not a final message.

**Capture precedence.** A context published manually through the `update_step_context` MCP
tool always wins — automatic capture is skipped entirely (see
[Publishing a verbatim artifact](#publishing-a-verbatim-artifact)). Otherwise the step's
`summarization_strategy` decides:

| Strategy | What gets captured |
|---|---|
| (unset) | **Defaults to `summarize_chat`** |
| `summarize_chat` | The `summarizer:` command summarizes the step's chat using `summarization_prompt` |
| `last_message` | The agent's final result text only. Cheap — no summarizer call — but often a one-liner that loses decisions. Not usable for tmux steps, which have no result text. |
| `none` | Nothing — no result text stored, no summarization pass; `{{steps.<name>.context}}` resolves to empty. For steps whose output isn't meaningful to later steps. |

Where the chat comes from: for headless steps it is the agent's **streamed stdout** for that
step (the echoed prompt is stripped); for tmux steps it is produced by the agent record's
`chat_log_command` — a tmux agent without one captures no chat context.

**`summarize_chat` is a no-op for most headless agents.** The standard headless command
redirects stdout into `$SAKUSEN_RESULT_FILE` (that is the env contract), so the agent streams
nothing and there is no transcript to summarize — Sakusen detects this, skips the summarizer,
and keeps the result text. Set `summarization_strategy: last_message` on such steps to make
that explicit and skip the dead `summarization_prompt`. Only headless agents that *also* print
their reasoning on stdout get a real `summarize_chat` pass.

```yaml
- name: grilling
  agent: claude-tmux           # interactive step → tmux-mode agent
  require_context: true
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

All summarization runs the single top-level `summarizer:` command — there is no model
selection in Sakusen; pick the model inside that command. Oversized chats are map-reduced when
`summarizer.max_prompt_bytes` is set. With no `summarizer:` configured, summarize passes are
skipped with a warning (or fail the task under `require_context: true`).

---

## Loops

Loops allow iterative refinement (e.g. implement → review → fix → review again).

```yaml
loop:
  goto: "step-name"      # must reference an EARLIER step
  max_iterations: 3      # >= 1
  exit_condition:                       # set either form, or both (exit when either matches)
    step_context_contains: "step-name"    # preferred: exit when that step's context contains marker
    marker: "LOOP-EXIT"                   # required with step_context_contains; literal, case-sensitive
    step_context_empty: "step-name"       # exit when that step's context is empty
```

**Prefer `step_context_contains` over `step_context_empty`.** Exiting on an absence cannot
distinguish "the step decided the work is done" from "the step crashed, timed out, or forgot
to publish" — every one of those silently ends the loop and ships whatever is on the branch. A
marker makes the exit an explicit statement the step has to produce; a step that fails to run
leaves it absent and the loop keeps going, bounded by `max_iterations`. Use
`step_context_empty` only when an empty context is genuinely unambiguous.

The marker has to actually reach the referenced step's *context*, so instruct the producing
step to emit it:

```yaml
steps:
  - name: implementing
    prompt: |
      Implement the following:
      <task-input>
      {{task.input}}
      </task-input>
  - name: reviewing
    summarization_strategy: last_message
    prompt: |
      Review the implementation:
      <step-context name="implementing">
      {{steps.implementing.context}}
      </step-context>

      List every issue you find. If there are no issues at all, reply with exactly
      the single line REVIEW-CLEAN and nothing else.
  - name: fixing
    # inherits the headless default agent — loop steps must resolve headless
    prompt: |
      Fix the issues found during review (pass {{loop.iteration}} of {{loop.max_iterations}}):
      <step-context name="reviewing">
      {{steps.reviewing.context}}
      </step-context>
    loop:
      goto: reviewing
      max_iterations: 3
      exit_condition:
        step_context_contains: reviewing
        marker: "REVIEW-CLEAN"
```

**Validation rules:**

- `goto` must reference a step that appears BEFORE the loop step; no self-reference
- `max_iterations` must be >= 1
- `exit_condition`, when present, must set `step_context_empty` or `step_context_contains`
- `marker` is required with `step_context_contains`, and is rejected without it
- Both `step_context_empty` and `step_context_contains` must name steps that exist
- Loop steps cannot have `human: true`
- Loop steps cannot resolve to a tmux-mode agent (a tmux step pauses the engine, so a loop
  over it could never iterate) — use a headless agent on the loop step or its workflow
- Loop ranges cannot overlap with other loops

`{{loop.iteration}}` and `{{loop.max_iterations}}` are available inside the loop range. Both
reset once the loop exits, so they do not leak into post-loop steps.

---

## Parallel Groups

A step carrying `parallel:` instead of `prompt:` is a **group**: it fans out to several branches
that run **concurrently as headless agents on the same worktree**, joins them, and publishes one
aggregated step context. Use it for redundant work whose value comes from disagreement —
independent code reviews by different models is the canonical case.

```yaml
steps:
  - name: implementing
    prompt: "Implement: {{task.input}}"

  - name: review
    parallel:
      require: any          # "" | all (default) | any | <positive int, 1..len(branches)>
      branches:
        - name: review-opus
          agent: claude:opus
          prompt: "{{prompt.review}}"
        - name: review-codex
          agent: codex
          prompt: "{{prompt.review}}"
          timeout: 20m

  - name: synthesize
    prompt: |
      Two independent reviews of the same change follow. Dedupe overlapping findings, rank by
      severity, and drop anything only one reviewer could verify.
      <parallel-reviews>
      {{steps.review.context}}
      </parallel-reviews>
```

**Branches must be read-only.** They share the task's single worktree and run at the same time,
so two branches writing the same files would race. Nothing enforces this — say so in the branch
prompts. (Isolated per-branch worktrees are a separate, later feature.)

### `require:` — the join policy

| Value | Meaning |
|---|---|
| omitted / `all` | Every branch must succeed. The FIRST failure cancels its still-running siblings and fails the task. |
| `any` | One success is enough. Losing branches are allowed to finish; the task advances. |
| `<N>` | At least N branches must succeed (1..number of branches). No fail-fast. |

When too few branches succeed, the task fails with an error naming each failed branch and its
reason (`exit 3`, `timed out after 20m`, `cancelled`) plus a tail of the first failure's log.

### Branch fields

A branch takes `name`, `description`, `agent`, `prompt`, `timeout`, `summarization_strategy` and
`summarization_prompt` — the ordinary step fields minus the ones that make no sense in a fan-out.

- **Agent cascade:** branch `agent:` → group `agent:` → workflow `agent:` → `default_agent:` →
  `"claude"`. Every branch must resolve to a **headless** agent; a tmux agent is a load error
  (the join is synchronous, so a paused branch could never complete).
- **`timeout:`** falls back to the group's `timeout:` when the branch omits it.
- **`summarization_strategy:`** defaults to `last_message` for branches (NOT the global
  `summarize_chat` default). A branch's result text IS the artifact the aggregate is built from;
  running it through the summarizer would rewrite the review you asked for. Set
  `summarization_strategy: summarize_chat` explicitly on a branch whose agent streams its
  reasoning rather than writing a result file.
- Branch prompts additionally see `{{branch.name}}` and `{{branch.agent}}` — useful for asking a
  reviewer to sign its findings. Both resolve to `""` outside a branch.

### The aggregate — `{{steps.<group>.context}}`

The group name owns a step context assembling every branch, in config order:

```
## review-opus (claude:opus)

<review-opus's findings>

## review-codex (codex) (failed: timed out after 20m)

## review-quiet (claude) (no context)
```

A failed branch is a header with `(failed: <reason>)` and no body; a branch that captured nothing
is marked `(no context)`. Both are deliberately loud — a synthesis prompt must never mistake a
missing review for agreement.

**The format is fixed.** If you want a different layout, address the branches individually:
each branch also publishes `{{steps.<branch>.context}}`, so a prompt can interleave them however
it likes. Branch names live in the same namespace as step names and must be unique across the
whole workflow.

### Rules

- A group sets none of `prompt`, `loop`, `human`, `summarization_strategy`, `summarization_prompt`,
  `require_context` — those belong on a branch.
- A branch may not contain a nested `parallel:`, a `loop:`, or `human: true`, and must be written
  as an inline mapping (a bare string is a step *reference*, which only resolves against
  top-level steps).
- A loop's `goto:` may target a **group** but not a branch; an `exit_condition` may name either.
  A loop back over a group re-runs all of its branches.
- Retry-from-step takes the **group** name; a branch name is rejected. Retrying a group re-runs
  only the branches that did not complete — completed branches keep their captured context.
  Retrying from an earlier step re-runs the whole group.
- Branch agent output goes to a per-branch log (`.sakusen/logs/<task>/branch-<name>.log`), not
  the unified task log, which records only the `=== parallel <group>: ... ===` markers.

### Parallel group vs. child tasks

A parallel group is for **one task doing several things at once on one branch**: several readers
of the same diff, whose outputs are combined into one prompt. It costs no extra worktrees,
branches, or task rows, and the branches cannot write.

Use `create_tasks_and_wait` (see [MCP Orchestration Patterns](#mcp-orchestration-patterns))
instead when the concurrent work needs to **produce changes** — separate worktrees, separate
branches, separate merges — or when each unit needs its own multi-step workflow. Their results
come back as `{{children.summary}}`, the sibling shape of the group aggregate.

---

## Prompt Formatting

Prompt fields (`prompt`, `summarization_prompt`, `summarizer_prompt`) are LLM input, not human
reading. Do not hard-wrap prose at ~80 columns — block scalars (`|`) preserve every newline as
a token. Keep only the structural newlines: blank lines between paragraphs, one line per list
item (continuation text stays on the item line), code fences verbatim. Reflow on contact when
editing existing prompts.

---

## Wrapping Multi-Line Interpolations

Several template variables expand to **multi-line** content at render time (a step's full
output, a transcript, a task input, a track's accumulated context). When inlined raw, the
boundary between fixed prompt text and interpolated content vanishes — paragraphs of step
context blend into the next instruction, and the receiving agent cannot tell where one ends
and the other begins.

**Rule: wrap every multi-line interpolation in a semantic XML-style tag named after the
variable.** Place the opening tag, the variable, and the closing tag each on their own line so
the captured content sits between two clean boundaries:

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
| `{{track.context}}` | `<track-context>...</track-context>` |
| `{{track.own_context}}` | `<track-context>...</track-context>` (own tier only) |
| `{{chat}}` | `<chat>...</chat>` |

Single-line variables (`{{task.id}}`, `{{task.title}}`, `{{task.slug}}`, `{{task.branch}}`,
`{{git.base_branch}}`, `{{git.target_branch}}`, `{{git.repo_root}}`, `{{loop.iteration}}`,
`{{loop.max_iterations}}`, `{{track.id}}`, `{{track.name}}`, `{{tasks.<id>.title}}`,
`{{tasks.<id>.branch}}`, `{{children.<id>.status}}`, `{{children.<id>.title}}`) are inlined
into surrounding prose **without** wrapping — they fit on one line and a tag would only add
noise.

Do **not** use triple-backtick fences for this. Interpolated content (especially `{{chat}}`
and summarized step contexts) routinely contains its own code fences, which would break the
outer fence. XML-style tags survive arbitrary nested content.

---

## Template Variables

Available in **step prompts** (`prompt:`) and **workflow summarizer prompts**
(`summarizer_prompt:`). Variables marked **multi-line** must be wrapped in a semantic tag —
see [Wrapping Multi-Line Interpolations](#wrapping-multi-line-interpolations).

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
| `{{steps.<step_name>.context}}` | Context captured from a prior step **(multi-line — wrap in `<step-context name="<step_name>">`)** |
| `{{artifacts.<step_name>}}` | Backward-compat alias for `{{steps.<step_name>.context}}` **(multi-line — same wrapping)** |
| `{{track.id}}` | Task's track ID; empty for a trackless task |
| `{{track.name}}` | Task's track name; empty for a trackless task |
| `{{track.context}}` | Full ancestor track chain, root-first **(multi-line — wrap in `<track-context>`)** |
| `{{track.own_context}}` | The leaf track's own context only **(multi-line — wrap in `<track-context>`)** |
| `{{tasks.<id>.<field>}}` | Field of **another task** by numeric ID. Fields: `title`, `branch`, `input`, `context`. See [Cross-Task References](#cross-task-references). |
| `{{children.summary}}` | Digest of all child tasks after a `create_tasks_and_wait` resume **(multi-line — wrap in `<children-summary>`)** |
| `{{children.<id>.<field>}}` | Field of a specific child task. Fields: `id`, `title`, `status` (`completed`/`failed`), `context`. See [MCP Orchestration Patterns](#mcp-orchestration-patterns). |
| `{{prompt.<name>}}` | Contents of a shared prompt file. See [Prompt Includes](#prompt-includes). |

Unknown placeholders are left in the rendered prompt verbatim, so a typo shows up as literal
`{{taks.id}}` text rather than an error. `{{prompt.<name>}}` is the one exception — it must
resolve or the config fails to load.

**Step `summarization_prompt:`** — same variables as above, plus:

| Variable | Description |
|---|---|
| `{{chat}}` | Full transcript of the step being summarized **(multi-line — wrap in `<chat>`)**. Only valid inside `summarization_prompt`. |

**`worktree-setup-command` / `worktree-setup-commands:`** — only `{{worktree_path}}` is
available. Commands run with the **project root** (not the worktree) as cwd; a non-zero exit
**fails the task**.

**`tmux-setup-command:`**:

| Variable | Description |
|---|---|
| `{{session_name}}` | Tmux session name created for the task |
| `{{worktree_path}}` | Absolute path to the task's worktree |
| `{{run_agent}}` | Path to the wrapper script that launches the agent with the `SAKUSEN_*` env exported |
| `{{agent_command}}` | Raw agent shell command from the agent record (prefer `{{run_agent}}` — this one lacks the env exports). The old `{{claude_command}}` name is a load error. |

If the command contains `{{run_agent}}` or `{{agent_command}}`, **you control where the agent
runs** — Sakusen will not auto-start it in window 0. Omit both and Sakusen launches the agent
itself after your layout command runs.

## Prompt Includes

When several workflows repeat the same passage of prompt text — implementer craft guidance, a
"take NO action on your own" review rules block, commit/attribution hygiene — factor it into a
markdown file and reference it with `{{prompt.<name>}}`:

```yaml
# ~/.sakusen/prompts/implementer-core.md holds the shared craft guidance
steps:
  - name: implementing
    prompt: |
      Apply the plan for task #{{task.id}}.

      <plan>
      {{steps.planning.context}}
      </plan>

      {{prompt.implementer-core}}
```

`<name>` is a file basename — letters, digits, dashes and underscores; kebab-case by convention —
resolved to `<name>.md` and searched project-first, first hit wins:

1. `<project>/.sakusen/prompts/<name>.md`
2. `~/.sakusen/prompts/<name>.md`

So a project can override a globally-shared passage by dropping a same-named file in its own tree.

Rules:

- Works in step `prompt`, step `summarization_prompt`, workflow `summarizer_prompt`, and
  `merge_conflicts.prompt` — anything that interpolates template variables.
- The file's contents are substituted first and the whole combined text is resolved in the same
  pass, so an included passage may itself use `{{task.id}}`, `{{steps.<name>.context}}`, etc.
- **Depth is 1** — an included file may not contain `{{prompt.*}}`.
- A single trailing newline is stripped, so inline placement (`Foo {{prompt.x}} bar`) adds no
  blank line.
- A missing file, a malformed name, or a nested include is a **hard load / `sakusen validate`
  error** naming the workflow, step, field and the paths searched — a half-resolved prompt never
  reaches an agent.
- There is no `prompts:` YAML map and no agent-level prompt: an agent answers "what runs this
  step", a prompt file answers "what to say at this position in this workflow". Keep the
  step-context wiring (`{{steps.<name>.context}}` blocks) and scope-policy sentences inline —
  those deliberately differ per workflow. Factor out only the passages that are genuinely
  identical.

**Environment variables** — every step's agent process (and anything it spawns) gets the
`SAKUSEN_*` contract described in SKILL.md → Agents → Environment contract:
`SAKUSEN_TASK_ID`, `SAKUSEN_STEP`, `SAKUSEN_WORKTREE`, `SAKUSEN_PROJECT_PATH` (repo root, not
the worktree), `SAKUSEN_PURPOSE=step`, `SAKUSEN_AGENT`, `SAKUSEN_TRACK_ID` (only on tracked
tasks), `SAKUSEN_PROMPT_FILE`, plus `SAKUSEN_RESULT_FILE` (headless) or
`SAKUSEN_DONE_DIR`/`SAKUSEN_DONE_PREFIX` (tmux). Useful in prompts that shell out or call
sakusen MCP tools.

---

## Tracks in Workflows

A **track** is a named, mutable context container that tasks attach to at create time. Tracks
are hierarchical (a track may have a parent), and each carries a free-form `context` blob that
accumulates across a stream of related tasks — a sprint, a refactor, a feature area. Tasks
opt into a track when they are created; the track itself is managed outside the workflow
config (TUI, CLI, and the `create_track` MCP tool).

For workflow authoring, four things matter:

**1. Track context is explicit opt-in.** It is never auto-injected into a prompt. A step sees
track context only if its `prompt:` interpolates `{{track.context}}` or
`{{track.own_context}}`.

**2. It is read live at every step launch.** The engine re-reads the task's track chain when
each step starts, so a track update made mid-run (by a human or by an earlier step) reaches
later steps of the same task. A retried step may therefore render a different prompt than its
original run.

**3. Trackless tasks resolve every `{{track.*}}` variable to the empty string.** A workflow
that references track vars still runs fine off-track — the tag just wraps nothing. Write the
prompt so an empty block is harmless.

**4. `{{track.context}}` renders the whole ancestor chain, root-first.** Each track with a
non-empty context contributes a `## Track: <name>` header followed by its context, joined by
blank lines; ancestors used purely for grouping (empty context) are skipped entirely.
`{{track.own_context}}` is the leaf track's own context only, with no headers.

```yaml
- name: implementing
  prompt: |
    Implement task #{{task.id}}: {{task.title}}

    <task-input>
    {{task.input}}
    </task-input>

    Standing context for this track (may be empty):
    <track-context>
    {{track.context}}
    </track-context>
```

### Writing back to the track

A step's agent can publish to its **own** track via the `update_track_context` MCP tool,
passing its own task ID from `$SAKUSEN_TASK_ID`. The daemon rejects calls from tasks with no
track or no active step, so the tool can only ever write the calling task's track. Default
`mode` is `append` (blank-line separator — the normal way to accumulate); `replace` overwrites
the track's entire own context and can destroy context shared with other tasks.

```yaml
- name: recording-decisions
  prompt: |
    Summarize the durable decisions from this task in at most 10 lines, then call the
    sakusen MCP tool `update_track_context` with task_id={{task.id}} and mode="append"
    to publish them to the track.

    Write only decisions that stay true for future tasks. Do not write task-specific
    detail, file paths, or anything you are not confident about.
```

> **⚠️ Prompt-injection surface.** Whatever a step writes to a track flows **verbatim** into
> the prompts of every future task attached to that track or its children, persistently.
> Treat a write-back step as publishing to a shared, long-lived prompt: constrain what it may
> write, keep it short, and never have it copy untrusted content (issue bodies, web pages,
> third-party output) into the track.

The companion `update_track_description` tool sets the track's stable one-liner purpose
statement (replace-only). The description is metadata for routing agents choosing a track via
`list_tracks`; it is never injected into prompts.

---

## Cross-Task References

Reference another task's fields anywhere templates resolve, via `{{tasks.<id>.<field>}}`.
Supported fields: `title`, `branch`, `input`, `context`.

Two places they work, with different semantics:

1. **In a task's input or context** (entered at create/edit time): the daemon validates each
   ref — missing task, cross-project ref, or ref to a `failed` task is a create/edit
   **error**. Refs to still-active tasks are **auto-added as `blocked_by` dependencies**, so
   the referencing task won't start until they finish. Refs are pre-resolved (single-pass, no
   recursive expansion) before the input/context is inlined into step prompts.
2. **In workflow step prompts**: resolved at render time with no validation and no
   auto-blocking — a missing task resolves to an empty string with a warning log.

`input` and `context` are multi-line — wrap them in semantic tags.

---

## MCP Orchestration Patterns

Sakusen ships an MCP server exposing its own daemon to agents. A step prompt can therefore
instruct its agent to call sakusen tools — that is how workflows fan out, publish artifacts,
and self-advance.

### Step-relevant tool cheatsheet

| Tool | Purpose |
|---|---|
| `list_workflows` | List a project's workflows (name, description, pins, `fully_spec`, `first_step_is_tmux`, per-step name + description). Call before `create_task`. |
| `create_task` | Queue a new task. `workflow` is required except for `tmux_direct` tasks. |
| `create_tasks_and_wait` | Spawn child tasks and suspend the calling step until all reach a terminal status. |
| `wait_for_tasks` | Same suspension, for pre-existing task IDs (any project). |
| `get_task` | Task detail; `include_*` flags opt into per-step state, captured step contexts, and recent agent output. |
| `list_tasks` | Compact task summaries, project-scoped or `all_projects=true`. |
| `update_step_context` | Publish the calling task's active-step context verbatim (see below). |
| `advance_task` | Mark a paused tmux step done — resume at the next step or finalize. |
| `update_task_input` | Replace a task's input (re-validates `{{tasks.<id>.<field>}}` refs, auto-adds blockers). |
| `update_task_dependencies` | Add/remove `blocked_by` edges. Removals apply before additions; cycles rejected. |
| `retry_task` | Re-run a task from the start or from a named step, preserving earlier step contexts. |
| `create_track` | Create a track. |
| `update_track_context` | Append/replace the calling task's own track context. |
| `update_track_description` | Set the calling task's own track description (replace-only). |
| `list_tracks` | Tracks visible from a project (own project-scoped + all global). |

The server exposes no irrecoverable operations — there is no task deletion or worktree cleanup
tool to call from a prompt.

### Child task orchestration

A step's agent can fan out child tasks:

- **`create_tasks_and_wait`** — spawn one or more child tasks and suspend the calling step
  until ALL reach a terminal status (`completed` or `failed`). The parent shows
  `awaiting-children`.
- **`wait_for_tasks`** — same suspension, but for pre-existing task IDs (already-terminal
  tasks are skipped).

Both default `parent_task_id` to the `SAKUSEN_TASK_ID` env var the engine injects into every
step, so agents don't need to pass it. When all children finish, **the calling step re-runs
from the same step index** with these variables populated:

| Variable | Description |
|---|---|
| `{{children.summary}}` | Formatted digest of every child (ID, status, title, context), sorted by ID — multi-line |
| `{{children.<id>.id}}` | Child task ID |
| `{{children.<id>.title}}` | Child task title |
| `{{children.<id>.status}}` | Terminal status: `completed` or `failed` |
| `{{children.<id>.context}}` | Child's final task context (its synthesized output) — multi-line |

**Children may live in a different project.** Pass a per-child `project_path` to
`create_tasks_and_wait` (it defaults to the parent's project), or hand `wait_for_tasks` any
task ID at all — the wait relation is purely ID-based. The child runs under its own project's
`.sakusen.yml` and workflows and merges into its own repo; the parent suspends and resumes
identically, and `{{children.<id>.status}}` is `completed`/`failed` regardless of where the
child ran. One caveat: a cross-project child does **not** inherit the parent's track (tracks
are project-scoped), so pass an explicit `track` if you want one — it resolves in the child's
own project.

Unknown IDs and unsupported fields resolve to empty. On the first (pre-spawn) run of the step
these are all empty — a typical orchestrator prompt branches on that:

```yaml
- name: orchestrating
  require_context: true
  prompt: |
    <children-summary>
    {{children.summary}}
    </children-summary>

    If the block above is empty, break this task into independent subtasks and call the
    sakusen MCP tool `create_tasks_and_wait` (call `list_workflows` first to pick each
    child's workflow). Otherwise, review the child results, check every child's status for
    failures, and integrate the work.

    <task-input>
    {{task.input}}
    </task-input>
```

Since the meaningful state usually lives in the conversation, orchestrator steps pair well
with `summarize_chat` (the default) and `require_context: true`.

### Publishing a verbatim artifact

`update_step_context` writes the canonical context value for a task's **currently-active**
step, bypassing the post-session chat summarizer. Use it when a step produces a long-form
artifact — a refined PRD, a decision record, a migration plan — that later steps must receive
word-for-word rather than compressed.

- `task_id` is required (from `$SAKUSEN_TASK_ID`) and `step_name` must match the task's active
  step; the daemon rejects writes to any other step with a clear error.
- `mode: "replace"` (default) overwrites — the typical end-of-step write. `mode: "append"`
  concatenates with a newline separator, useful for incremental progress within a step.
- It also works for a **tmux step paused at its approval gate**: the daemon resolves the
  active step as the one owning the live session and writes there.
- A manual write **wins over automatic capture** — the `summarize_chat` pass is skipped rather
  than clobbering the artifact, and the write satisfies `require_context: true`. Keeping
  `require_context: true` on such a step is still worthwhile: it fails the task loudly if the
  agent never published and no chat could be captured either.

```yaml
- name: planning
  agent: claude-tmux
  prompt: |
    Produce an implementation plan for task #{{task.id}}.

    When the plan is final, publish it verbatim by calling the sakusen MCP tool
    `update_step_context` with task_id={{task.id}}, step_name="planning", and the full
    plan as `context`. Do not summarize it — later steps consume it word-for-word.
```

### The human-gated step

Combining the two tools gives a workflow whose first step is a real conversation and whose
remaining steps run unattended off that conversation's output:

1. An interactive (tmux-mode) step interviews the user until the requirements are settled.
2. The agent publishes a decision record via `update_step_context` — a canonical, verbatim
   artifact rather than a summarizer's lossy compression of the chat.
3. The agent calls `advance_task` as its **last action**. The daemon kills the tmux session
   and resumes at the next step (or finalizes, if the tmux step was the last one). Because the
   call ends the session immediately, anything the agent intended to do afterwards will not
   happen.
4. Every later step templates `{{steps.<gate-step>.context}}` and runs headless.

```yaml
- name: shared-implement
  steps:
    - name: researching
      agent: claude-tmux
      description: "Human-gated interview that produces the decision record"
      require_context: true
      prompt: |
        Research task #{{task.id}}: {{task.title}}

        <task-input>
        {{task.input}}
        </task-input>

        Interview the user until scope and approach are settled. Then:
        1. Write a decision record (request, root cause, locked scope, file-by-file plan).
        2. Publish it verbatim with the sakusen MCP tool `update_step_context`
           (task_id={{task.id}}, step_name="researching", mode="replace").
        3. As your final action, call `advance_task` with task_id={{task.id}}. This ends
           this session immediately, so do nothing after it.
    - name: implementing
      prompt: |
        Implement task #{{task.id}} following the locked plan below. It is authoritative.

        <step-context name="researching">
        {{steps.researching.context}}
        </step-context>
```

A premature `advance_task` is recoverable — `retry_task` with a `step_name` re-runs from that
step, preserving earlier step contexts.

---

## Load Errors and Warnings

Hard errors at config load:

- A string ref points to a missing file (`.sakusen/workflows/<name>.yml`) and is not in the
  global pool
- The same name is both inlined in `.sakusen.yml` and present as a file (within one scope)
- A file-based workflow sets a `name:` field (the filename is authoritative)
- A workflow file uses a non-kebab-case filename or lives in a subdirectory of
  `.sakusen/workflows/`
- Duplicate workflow names in the flat list; duplicate step names within a workflow
- A step reference names a step absent from the base workflow, or there is no base workflow
- Any loop validation failure (see [Loops](#loops))
- An invalid `summarization_strategy` or `on_complete` value
- The removed `print:` / `tmux:` fields on a workflow or step; step-level
  `allowed_summarization_models:`
- An explicit `agent:` / `default_agent:` naming a slug that does not exist

Warnings (non-fatal, surfaced by `sakusen validate`):

- A file under `.sakusen/workflows/` that is not referenced in `.sakusen.yml` (it is hidden)
