# Sakusen Workflow Building Reference

Everything needed to author the `workflows:` list and its `routines:` bindings: entry shapes,
pins, step fields, prompts, template variables, loops, tracks, and MCP orchestration patterns.

## Contents

- [Workflow Entry Shapes](#workflow-entry-shapes)
- [Pinnable Fields](#pinnable-fields)
- [Routines — Invocation Bindings](#routines--invocation-bindings)
- [File-Based and Hidden Workflows](#file-based-and-hidden-workflows)
- [Global Workflows, Overrides, and Step References](#global-workflows-overrides-and-step-references)
- [Track Workflows](#track-workflows)
- [Workflow Fields](#workflow-fields)
- [Step Fields](#step-fields)
- [Execution Mode](#execution-mode)
- [Step Summarization](#step-summarization)
- [Loops](#loops)
- [Parallel Groups](#parallel-groups)
- [Fan-out Steps](#fan-out-steps)
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
`tmux-setup-command`, `notifications`. Indent the body one level deeper than the name key — a sibling
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
| `workdir` | string | Pins the agent start directory: a path relative to the task's worktree (or the main checkout when `worktree: false`) where every agent and tmux session starts. Must stay inside the checkout and exist when the agent starts (the step fails otherwise) |

Validation: `branch` and `checkout` are mutually exclusive; `branch`/`checkout`/`target` are
rejected when `worktree: false`; `workdir` must be a relative path inside the checkout (no
absolute paths, no `..` escapes). `workdir` is optional and never gates skipping the New Task
screen. An explicit `workdir` on `create_task` / `sakusen create --workdir` / the New Task form
overrides the pin.

**Skipping the New Task screen.** A workflow that pins *every* field (`input` + `worktree` +
`branch`/`checkout` + `target`) creates a task immediately without showing the form. Two
shortcuts: when `worktree: false` is pinned, `input` alone suffices (the git fields are N/A);
workflows whose **first step resolves to a tmux-mode agent** may be created without an input
at all — the user drives the session interactively.

**Working-tree lock.** `exclusive_workdir: true` (not a pin) makes a `worktree: false` task of
this workflow stay `pending` while any other active `worktree: false` task of the same project
(running, paused for approval, a live or hibernated tmux session, ...) is using the project
root. It starts on the next poll after the holder finishes; the TUI shows
`pending (workdir busy #N)`. The lock is one-sided — holders need not set the flag — and it is
a no-op for tasks running in their own worktree. It belongs to the workflow: a routine bound to
it inherits the setting and may not set its own.

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

## Routines — Invocation Bindings

`workflows:` defines what runs. The sibling top-level `routines:` key defines *ways of running
it*. A routine names a workflow, supplies pins, and optionally a trigger (`cadence` or
`after`) — it never defines steps, so one workflow can back any number of differently pinned
routines.

```yaml
routines:
  - name: compose-wiki                 # required, kebab-case, unique
    description: Rebuild the wiki      # metadata (routine palette, MCP); NOT a pin
    workflow: wiki-compose             # required — the workflow this binds
    input: "Recompose every wiki page" # pins, overriding the workflow's own
    worktree: true
    branch: sakusen/wiki-{{task.id}}
    target: main
    workdir: docs                      # agent start dir inside the checkout
    cadence: "0 3 * * *"               # optional trigger — omit for on-demand only
    # after: other-routine             # ...or fire when that routine's run completes
    priority: low                      # optional — else the project default at fire time
    paused: false                      # optional — only meaningful with a trigger
    notifications:                     # optional — per-event overrides (see config-reference.md)
      on_complete: false
```

"Kind" is emergent, never declared:

| Shape | How it starts |
|---|---|
| Workflow, no routine | New Task screen (`n` / `:RunTask`), or immediately if fully pinned |
| Workflow + routine | On demand: `:RunRoutine`, `sakusen routines run <name>`, MCP `run_routine` |
| Workflow + routine + `cadence` | The above, plus the daemon fires it on schedule |
| Workflow + routine + `after` | The above, plus the daemon fires it each time the named routine's run completes |

**Cadence.** Standard 5-field cron (`0 3 * * *`), a descriptor (`@daily`, `@hourly`, ...) or
`@every <duration>`. The scheduler ticks every 30s, so sub-minute `@every` values are observed
at tick resolution (a validate warning says so). No cadence = on-demand only; the routine
still gets run history and a last-task link, it simply never becomes due.

**After.** `after: <routine>` names another routine in the **same** `.sakusen.yml`; this
routine fires each time that routine's latest run reaches `completed` — whether the upstream
ran on a cadence, on its own `after:`, or on demand. A run that ends `failed`/`merge-failed`
is skipped (the next completed run fires). Setting both `cadence:` and `after:` is a load
error, as are unknown names, self-references and cycles. Detection happens on the 30s
scheduler tick; a new `after:` routine does not fire for an upstream run that finished before
it existed, and completions that happen while it is paused are not replayed on resume.
Skip-overlap applies: if the routine's own previous run is still active, that trigger is
skipped.

**Pin override.** A pin on the routine beats the workflow's, field by field for `input`,
`worktree`, `target` and `workdir`. `branch` and `checkout` override as a **pair**: setting either on the
routine ignores both of the workflow's, because they are two answers to one question. The
merged result goes through the same pin validation as a workflow, reported against the
routine's name.

**Arguments.** A run takes one optional free-text argument that becomes `{{task.input}}` and
overrides the routine's own `input:`. If the bound workflow references `{{task.input}}` in any
step or parallel-branch prompt and neither the routine nor the workflow pins an input, the
routine **requires** that argument: the TUI opens an input-only prompt, and the CLI and MCP
reject an empty one. A **triggered** routine (`cadence` or `after`) in that state is a load
error — an automatic fire has nobody to ask. Fix it by pinning `input:` on the routine or
dropping the trigger.

```yaml
workflows:
  - name: digest
    steps:
      - name: writing
        prompt: "Write a digest of {{task.input}}"

routines:
  - name: digest-now      # legal: on-demand, asks for its argument
    workflow: digest
  - name: digest-nightly  # legal: the pin answers {{task.input}}
    workflow: digest
    cadence: "@daily"
    input: "yesterday's merged PRs"
```

Routines are read-only at runtime — `.sakusen.yml` is the source of truth. `sakusen routines
list/show/run/runs/pause/resume <name>` surfaces and drives them; pausing an on-demand routine
is rejected (there is no trigger to stop).

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
- **Startable by name** when bound from `routines:` — which is the idiomatic pairing: a pool
  file referenced only from `routines:` stays hidden AND produces no validate warning, because
  the routine is what makes it reachable

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
  workdir: em/rails-foundation # pin: agents and tmux sessions start in this subdirectory
  agent: claude              # agent slug every step inherits unless it sets its own
  on_complete: commit        # per-workflow override: "commit" | "merge" | "none"
  summarizer_prompt: "..."   # custom prompt for the post-completion task summarizer
  worktree-sync-paths: {...} # per-workflow override of the project-level value
  worktree-setup-command: "..."
  worktree-setup-commands: ["...", "..."]
  tmux-setup-command: "..."
  notifications: {...}       # per-event notification overrides + notify_if (see config reference)
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
    advance: auto          # tmux steps only: auto (default) | explicit — see below
    summarization_strategy: summarize_chat   # how this step's context is captured
    summarization_prompt: "..."              # prompt fed to the summarizer for THIS step
    require_context: false # true = fail the task if summarize_chat context can't be captured
    loop: {...}            # jump back to an earlier step or repeat this one (see Loops)
    parallel: {...}        # fan out to concurrent branches instead of running a prompt (see Parallel Groups)
    fanout: {...}          # spawn one child task per published item or declared node, then join (see Fan-out Steps)
```

A step sets EITHER `prompt:`, `parallel:` OR `fanout:` — a `parallel:` step is a container and a
`fanout:` step spawns child tasks; neither runs an agent itself. See
[Parallel Groups](#parallel-groups) and [Fan-out Steps](#fan-out-steps).

- **`description:`** is pure metadata. It is surfaced through the MCP `list_workflows` tool
  (each step entry carries its own `name` and `description`) so an orchestrating agent can
  reason about a workflow's shape before creating a task. It is **never** interpolated into
  prompts. Distinct from the workflow-level `description:`.
- **`timeout:`** Go duration strings: `"30m"`, `"1h"`, `"1h30m"`, `"45m"`, `"2h"`.
- **`human: true`** pauses the task at `awaiting-approval`; the user reviews in the TUI and
  approves to continue. Use for review gates.
- **`advance: explicit`** stops a tmux step from auto-advancing on its turn-end sentinel. The
  step advances only when the agent calls the MCP `advance_task` tool or the user advances it.
  Sentinels still record the session id and mark the agent idle at its prompt. Unlike `human:
  true`, it is **not** an approval gate: the task reads as running, not `awaiting-approval`. Use
  it for steps that must sit through several turns before the agent decides the work is done,
  such as waiting through a long call before capturing it. The default is `advance: auto`. It
  requires a tmux-mode agent, cannot be combined with `human: true`, and is not allowed on a
  parallel group or branch. `list_workflows` reports it as `advance: "explicit"`.
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
| `tmux` + `advance: explicit` | `false` | tmux, advances only via `advance_task` or the user (not an approval gate) |
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
  goto: "step-name"      # an EARLIER step, or this step itself to repeat it
  max_iterations: 3      # >= 1
  on_max_iterations: fail  # optional: fail | continue (default); fail requires exit_condition
  exit_condition:                       # set any combination (exit as soon as one matches)
    step_context_contains: "step-name"    # affirmative: exit when that step's context contains marker
    marker: "LOOP-EXIT"                   # required with step_context_contains; literal, case-sensitive
    command: 'my-gate --check'            # affirmative: exit 0 ends the loop, 1 continues, else task fails
    stdin_from: "step-name"               # optional, command only: pipe that step's context to stdin
    timeout: 2m                           # optional, command only: Go duration > 0, default 2m
    step_context_empty: "step-name"       # exit when that step's context is empty
```

**Prefer `step_context_contains` over `step_context_empty`.** Exiting on an absence cannot
distinguish "the step decided the work is done" from "the step crashed, timed out, or forgot
to publish" — every one of those silently ends the loop and ships whatever is on the branch. A
marker makes the exit an explicit statement the step has to produce; a step that fails to run
leaves it absent and the loop keeps going, bounded by `max_iterations`. Use
`step_context_empty` only when an empty context is genuinely unambiguous.

**`command` lets a program make the decision** instead of a substring match on free text —
e.g. a sweep loop that ends once `grep -rl old_api src` finds nothing, or a classifier CLI
that reads a decider step's verdict on stdin. It is evaluated after the loop step's context
is captured (only while the loop would otherwise continue, or on the last pass under
`on_max_iterations: fail`), via `bash -c` in the task's
workdir (worktree or main checkout, honouring `workdir`), with the step env contract
(`SAKUSEN_TASK_ID`, `SAKUSEN_STEP`, `SAKUSEN_WORKTREE`, `SAKUSEN_PROJECT_PATH`,
`SAKUSEN_TRACK_ID` when tracked) plus `SAKUSEN_LOOP_ITERATION` (same value as
`{{loop.iteration}}`) and `SAKUSEN_PURPOSE=loop_exit`. The exit status is a three-way
contract:

- `0` — exit the loop.
- `1` — continue looping (bounded by `max_iterations`).
- anything else, a timeout, or a failure to start (a missing binary exits `127`) — **fail the
  task**. The reason and the command's combined output (truncated) go to the task log and the
  failure reason; an uncertain decision never silently ends or continues the loop.

Each evaluation is logged to the task log (`Loop exit: command `…` exited 0, exiting loop`),
followed by the tail of the command's combined output when it printed anything.

```yaml
loop:
  goto: planning
  max_iterations: 15
  exit_condition:
    command: 'test "$(grep -rl old_api src | wc -l)" -eq 0'
```

**`on_max_iterations` decides what the cap means.** With `continue` (the default) a loop that
reaches `max_iterations` simply ends and the workflow moves on to the next step, even though
its exit condition was never met. With `fail` the exit conditions are still evaluated on that
last pass; if one matches the loop exits normally, otherwise the task fails with
`loop on step "<step>" reached max_iterations (N) without meeting its exit condition`.
**Recommend `fail` for autonomous loops in workflows that auto-merge** (`on_complete: merge`)
— otherwise a half-done sweep is reviewed and merged as if finished. No shell gate step
comparing `{{loop.iteration}}` with `{{loop.max_iterations}}` is needed.

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

- `goto` must reference a step that appears BEFORE the loop step, or the loop step itself (a
  self-loop repeats that one step until its exit condition holds); no forward jumps
- `max_iterations` must be >= 1
- `on_max_iterations`, when present, must be `fail` or `continue`; `fail` requires an `exit_condition`
- `exit_condition`, when present, must set `step_context_empty`, `step_context_contains` or `command`
- `marker` is required with `step_context_contains`, and is rejected without it
- `step_context_empty`, `step_context_contains` and `stdin_from` must name steps that exist
- `command` must not be blank; `stdin_from` and `timeout` are only valid with `command`
- `timeout` must parse as a Go duration and be > 0
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

**In a shared group (the default), branches must be read-only.** They share the task's single
worktree and run at the same time, so two branches writing the same files would race. Nothing
enforces this — say so in the branch prompts. For branches that write, use
[`isolation: worktree`](#isolation--per-branch-worktrees).

### `require:` — the join policy

| Value | Meaning |
|---|---|
| omitted / `all` | Every branch must succeed. The FIRST failure cancels its still-running siblings and fails the task. |
| `any` | One success is enough. Losing branches are allowed to finish; the task advances. |
| `<N>` | At least N branches must succeed (1..number of branches). No fail-fast. |

When too few branches succeed, the task fails with an error naming each failed branch and its
reason (`exit 3`, `timed out after 20m`, `cancelled`) plus a tail of the first failure's log.

### `isolation:` — per-branch worktrees

| Value | Meaning |
|---|---|
| omitted / `shared` | Every branch runs in the task's worktree (read-only branches). |
| `worktree` | Every branch gets its own throwaway worktree and git branch, so branches may write. |

```yaml
  - name: attempts
    parallel:
      isolation: worktree
      require: 1
      branches:
        - name: attempt-opus
          agent: claude:opus
          prompt: "Implement: <task-input>{{task.input}}</task-input>"
        - name: attempt-codex
          agent: codex
          prompt: "Implement: <task-input>{{task.input}}</task-input>"

  - name: pick
    prompt: |
      Compare `{{branches.attempt-opus.branch}}` (worktree `{{branches.attempt-opus.worktree}}`) with `{{branches.attempt-codex.branch}}` (worktree `{{branches.attempt-codex.worktree}}`), pick the better attempt and `git merge` its branch into the current worktree.
      <attempt-summaries>
      {{steps.attempts.context}}
      </attempt-summaries>
```

- **Naming:** branch `<name>` runs in worktree `<task worktree>-<name>` on git branch
  `<task branch>-<name>`.
- **Starting point:** branches are created from the task branch's HEAD. **Uncommitted changes in
  the task worktree are not carried over** — commit them in an earlier step.
- **Hand-back:** a successful branch's changes are committed on its branch
  (`sakusen: task #N branch <name>`; nothing is committed when the tree is clean). A failed
  branch is left uncommitted. Nothing is merged into the task branch for you — a later step does
  that, locating each branch through `{{branches.<name>.branch}}` and
  `{{branches.<name>.worktree}}` (both `""` for unknown names and for shared-group branches).
- **Join policy:** `require: all` still cancels siblings on the first failure. Under `any` / `<N>`
  a success never cancels the others (their work is kept for the picking step); the group fails
  as soon as N successes become unreachable.
- **Retries and loops** reuse an existing branch worktree, so a re-run branch continues from its
  previous state.
- **Cleanup:** branch worktrees are removed with the task worktree (after merge, on delete or
  cleanup); their git branches are deleted too unless the task checked out a user-provided
  branch.
- **Workers:** isolated branches count against `max_workers`. A task's first running branch
  reuses the task's own slot; each further branch takes another slot, waiting if none is free.
- **Requires a worktree:** `isolation: worktree` is a load error under a workflow or routine
  that pins `worktree: false`, and fails the group at runtime for a task created without one.

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

- A group sets none of `prompt`, `loop`, `human`, `advance`, `summarization_strategy`,
  `summarization_prompt`, `require_context` — those belong on a branch.
- A branch may not contain a nested `parallel:`, a `loop:`, `human: true` or `advance:`, and must be written
  as an inline mapping (a bare string is a step *reference*, which only resolves against
  top-level steps).
- A loop's `goto:` may target a **group** but not a branch; an `exit_condition` may name either.
  A loop back over a group re-runs all of its branches.
- Retry-from-step takes the **group** name; a branch name is rejected. Retrying a group re-runs
  only the branches that did not complete — completed branches keep their captured context.
  Retrying from an earlier step re-runs the whole group.
- Branch agent output goes to a per-branch log (`.sakusen/logs/<task>/branch-<name>.log`), not
  the unified task log, which records only the `=== parallel <group>: ... ===` markers.

### Parallel group vs. fanout vs. child tasks

A shared parallel group is for **one task doing several things at once on one branch**: several
readers of the same diff, whose outputs are combined into one prompt. It costs no extra worktrees,
branches, or task rows, and the branches cannot write. An `isolation: worktree` group lets the
branches write (best-of-N, competing attempts) while staying one task with one merge.

A [fanout step](#fan-out-steps) is for **a declared fan-out that produces changes**: an earlier
step publishes a list of items with `publish_items`, and the engine spawns one child task per
item, each in its own worktree running its own workflow. Retries and the `require` policy are
config, not prose. The NEXT step reads every child's result through `{{steps.<fanout>.context}}`.
When the units are known up front, declare them as [`nodes:`](#declared-nodes) with `needs:`
instead — a static graph whose nodes start as soon as their needs have merged.

Rule of thumb: a shared `parallel:` group is for read-only branches with inline prompts on the
task's own worktree; `fanout:` is for isolated child tasks that write.

Use `create_tasks_and_wait` (see [MCP Orchestration Patterns](#mcp-orchestration-patterns))
for **ad-hoc spawning** that an agent decides on at run time. The calling step suspends and is
re-run when the children finish, so its prompt must handle both the plan pass and the check pass.
The children's results come back to that same re-run step as `{{children.summary}}`.

---

## Fan-out Steps

A `fanout:` step runs no agent. It spawns one child task per item that an earlier step published, waits for all of them, then advances to the next step. That step reads every child's result through the fanout's aggregate context.

```yaml
- name: inventory
  agent: planner            # publishes items via the publish_items MCP tool
- name: units
  fanout:
    from: inventory         # earlier step whose published items drive this fan-out
    workflow: fast-and-furious
    input: "{{steps.inventory.context}}\n\nYour unit:\n{{item.input}}"
    merge: parent           # children base on and merge into this task's branch
    require: all            # all | any | N
    retries: 1              # automatic re-runs of a failed child before the join judges it
- name: finishing
  prompt: |
    The units are done:
    {{steps.units.context}}
```

| Key | Default | Notes |
|---|---|---|
| `from` | required unless `nodes` is set | An earlier, ordinary top-level step (not a fanout, parallel group or parallel branch). It publishes the items. |
| `workflow` | required | The workflow each child runs. It must exist and have no `human:` step or step that resolves to a tmux-mode agent, because children run unattended. |
| `input` | `{{item.input}}` | Child task input template. Sees the normal variables plus `{{item.*}}`. |
| `merge` | `base` | `parent`: children base on and merge into the fanning-out task's branch. `base`: children target the usual base branch. |
| `require` | `all` | Join policy: `all`, `any`, or an integer `N >= 1` (at least N children must complete). |
| `retries` | `0` | How many times a failed child is re-run automatically before the join judges it. |
| `allow_empty` | `false` | Only valid with `from`. When `true`, a `from:` step that publishes nothing does not fail the task: the fanout spawns nothing, skips `require` and completes at once with context `fanout <name>: 0 items published (allow_empty)`. Rejected with `nodes:`. |

A fanout step cannot set `prompt`, `agent`, `loop`, `human`, `advance`, `parallel`, `summarization_strategy`, `summarization_prompt` or `require_context`. No loop's `goto` range may contain a fanout step, because re-running it would spawn its children again. Config loading and `sakusen validate` reject every one of these.

**Publishing items.** The `from:` step's agent calls the `publish_items` MCP tool, or runs `sakusen publish-items`. The task and step default to `$SAKUSEN_TASK_ID` / `$SAKUSEN_STEP`. Each item is `{input, slug?, title?, needs?}`:

- `input` is required and non-empty.
- `slug` must match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$` and be unique. A missing slug becomes `item-<n>` (1-based position).
- `needs` lists slugs of other items whose children must complete first. It becomes the child's `blocked_by`. Unknown slugs, self-references and cycles are rejected.

The call is validated at once and errors go back to the model. The step must be running, and a later fanout must name it in `from:`. Publishing again replaces the list. Every fresh run of the `from:` step clears its earlier list. A headless `from:` step that completes without publishing fails the task. A tmux one that publishes nothing fails at the fanout instead.

**Spawn.** On entry the fanout creates one child per item in the parent's project, in dependency order:

- each child gets the rendered `input`, the parent's track and priority, and a title (the item's `title`, or its slug if it has none);
- `needs` become `blocked_by` edges between the children;
- the parent then sits in `awaiting-children`.

A `require: N` larger than the number of items fails the step before anything spawns. Each child records its parent, fanout step and item. A unique index on those three keeps spawning idempotent across a daemon restart.

**Join.** The parent resumes once every child is terminal:

- A failed child is re-run while it has retries left. Dependents that were cascade-failed by a retried blocker never ran, so they are reset alongside it without spending their own attempts.
- `require` is then applied. If it is met, the aggregate becomes `{{steps.<fanout>.context}}` and the task advances to the next step; the fanout step itself is not re-run. If not, the task fails with an error listing the failed items as `<slug> #<child id>`.
- Retrying the parent reuses completed children and re-runs failed ones, with their attempt counters reset.

**Depth cap.** A child workflow may itself fan out, down to `MaxFanoutDepth` = 3 levels. A task already at that depth fails its fanout step at spawn time.

**Aggregate context.** One header line, then one line per item in published order:

```
fanout units: 2/3 completed (require all)
- api #12 completed — Added the endpoint and its tests
- ui #13 completed — (no context)
- docs #14 failed — (failed: step "write" exited with code 1)
```

Each summary is the first non-empty line of the child's context with whitespace collapsed, truncated to 120 runes (ending in `…`). Children with no context show `(no context)`. A failed child shows `(failed: <first line of its error>)`. This keeps the aggregate compact at hundreds of children. Full per-child results stay reachable via `get_task` on the listed IDs, or via `{{tasks.<id>.context}}`.

### Declared nodes

When the units are known when you write the workflow, declare them under `nodes:` instead of publishing them at run time. `needs:` orders them as a graph:

```yaml
- name: planning
  prompt: Plan the change for {{task.input}}.
- name: build
  fanout:
    workflow: fast-and-furious   # default for every node
    merge: parent
    retries: 1                   # default for every node
    nodes:
      - name: schema
        input: "Design the schema.\n\n{{steps.planning.context}}"
      - name: api
        input: "Build the API on this schema:\n{{steps.schema.context}}"
        needs: [schema]
      - name: ui
        input: "Build the UI on this schema:\n{{steps.schema.context}}"
        needs: [schema]
        retries: 2
      - name: e2e
        workflow: test-only
        input: "Write end-to-end tests.\n{{steps.api.context}}\n{{steps.ui.context}}"
        needs: [api, ui]
- name: finishing
  prompt: |
    {{steps.build.context}}
```

| Key | Default | Notes |
|---|---|---|
| `name` | required | Node name. It must match the item slug shape (`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`). |
| `workflow` | the step's `workflow` | The workflow this node's child runs. |
| `input` | required | Child task input template. |
| `needs` | none | Names of sibling nodes that must complete first. |
| `retries` | the step's `retries` | Automatic re-runs of this node's failed child. |

Validation rules (config load and `sakusen validate`):

- `from:` and `nodes:` are mutually exclusive, and the step-level `input:` is rejected with `nodes:` (each node carries its own).
- Node names join the workflow's step namespace (no collisions with steps, parallel branches or other nodes); a node's context is `{{steps.<node>.context}}`.
- `needs` may only name nodes of the same fanout; cycles are rejected.
- A node's `input` may reference `{{steps.<name>.context}}` only for its transitive needs or for names declared before the fanout.
- A node that another node needs must run a child workflow with `on_complete: merge`.
- A `require: N` larger than the node count is rejected.
- In a node's input, `{{item.slug}}`/`{{item.title}}` are the node name, `{{item.index}}` its 1-based position in `nodes:`, and `{{item.input}}` is empty.

Run time:

- A node's child spawns only once all its needs have completed (merged), with its input rendered at that moment; under `merge: parent` its worktree already holds its needs' work.
- Each node retries on its own `retries`.
- A node whose need failed for good is never spawned, counts as failed at the join, and shows as `- <node> (skipped: need <need> failed)` in the aggregate.
- Each completed node gets its own step row, readable as `{{steps.<node>.context}}`.
- The parent wakes whenever any child becomes terminal, so ready nodes start promptly.
- The TUI lists node rows in dependency order.

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
| `{{item.input}}`, `{{item.slug}}`, `{{item.title}}`, `{{item.index}}` | The published item a fanout child is spawned for (`index` is 1-based). For a declared node, `slug`/`title` are the node name, `index` its 1-based position in `nodes:`, and `input` is empty. **Only in a fanout step's or node's `input:`.** See [Fan-out Steps](#fan-out-steps). |

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
| `create_task` | Queue a new task. `workflow` is required except for `tmux_direct` tasks. Optional `workdir` sets the agent start subdirectory (overrides the workflow's pin). |
| `create_tasks_and_wait` | Spawn child tasks and suspend the calling step until all reach a terminal status. Each child spec accepts `workdir` like `create_task`, plus `merge_into_parent` to land the child on the parent's branch instead of the base branch. |
| `wait_for_tasks` | Same suspension, for pre-existing task IDs (any project). |
| `get_task` | Task detail; `include_*` flags opt into per-step state, captured step contexts, and recent agent output. |
| `list_tasks` | Compact task summaries, project-scoped or `all_projects=true`. |
| `update_step_context` | Publish the calling task's active-step context verbatim (see below). |
| `publish_items` | Publish the items a later `fanout:` step spawns children from. Call from the running `from:` step; each call replaces the list. See [Fan-out Steps](#fan-out-steps). |
| `advance_task` | Mark a paused tmux step done — resume at the next step or finalize. |
| `update_task_input` | Replace a task's input (re-validates `{{tasks.<id>.<field>}}` refs, auto-adds blockers). |
| `update_task_dependencies` | Add/remove `blocked_by` edges. Removals apply before additions; cycles rejected. |
| `retry_task` | Re-run a task from the start or from a named step, preserving earlier step contexts. |
| `retry_merge` | Re-run only finalization (verify, merge, summarize, cleanup) of a `merge-failed` task; no agent step re-runs. |
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

**Children merge into the project base branch by default** — each child finalizes under its own
workflow's `on_complete`, so with `merge` its work reaches main before the parent's later
steps (audit, review, human approval) have run. To gate it behind the parent, set
`merge_into_parent: true` on the child spec:

- The child's worktree branches from the parent branch's current tip, and its merge
  fast-forwards the parent's branch **in the parent's worktree** (same per-repo lock, verify
  command, rebase and conflict resolution as a merge into main), so siblings merging one after
  another rebase onto each other's results. Main is untouched until the parent itself merges.
- The merge waits in `merge-blocked` while the parent is still running a step (it lands once the
  parent is suspended at `awaiting-children`) and while the parent's worktree has uncommitted
  changes outside `merge.ignore_paths` — so have the parent commit its own edits before
  spawning, and keep `.sakusen-output.log` gitignored (`sakusen init` does). Past
  `merge.clean_target_timeout` the child goes `merge-failed`.
- On resume the parent's worktree contains every merged child's commits.
- The parent must be a worktree task in the same project; an explicit `target_branch` must equal
  the parent's branch (passing the parent's branch as `target_branch` behaves the same).

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

The step needs `advance: explicit`. Without it, a hooked tmux agent advances on its first turn
end, because the Stop hook writes a turn-end sentinel after every turn.

```yaml
- name: shared-implement
  steps:
    - name: researching
      agent: claude-tmux
      advance: explicit
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
- An invalid `summarization_strategy`, `advance` or `on_complete` value
- `advance: explicit` combined with `human: true`, or on a step resolving to a headless agent
- The removed `print:` / `tmux:` fields on a workflow or step; step-level
  `allowed_summarization_models:`
- An explicit `agent:` / `default_agent:` naming a slug that does not exist
- A routine missing `workflow:`, or naming one that does not exist
- A routine setting `steps:` / `agent:` / `summarizer_prompt:` / `exclusive_workdir:`
  (routines are bindings)
- A routine with a duplicate or non-kebab-case name, an unparseable `cadence`, an invalid
  `priority`, or effective pins that fail pin validation
- A routine setting both `cadence:` and `after:`, or an `after:` naming an unknown routine,
  itself, or forming a cycle
- A **triggered** routine (`cadence` or `after`) whose workflow references `{{task.input}}`
  with no effective input
- The removed top-level `periodic:` key

Warnings (non-fatal, surfaced by `sakusen validate`):

- A file under `.sakusen/workflows/` that is neither referenced in `.sakusen.yml` nor bound by
  a routine (it is hidden and unreachable by any menu)
- A routine `cadence:` of `@every <under a minute>` (observed at the scheduler's ~30s tick)
