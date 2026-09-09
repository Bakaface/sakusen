---
name: config
description: >
  Sakusen's configuration system: .sakusen.yml parsing, loading hierarchy, workflow
  definitions, step/loop config, project type detection, and branch template resolution.
  Use when editing files in internal/config/, working on .sakusen.yml parsing,
  workflow definitions, project detection, or configuration loading.
---

# Configuration System

## Loading Hierarchy (highest priority last)

1. Built-in defaults
2. `~/.config/sakusen/config.yaml` (global daemon — `GlobalConfig`)
3. `~/.sakusen.yml` (global workflow defaults)
4. `<project root>/.sakusen.yml` (project-specific, wins)

```go
Load() (*Config, error)                    // From current directory
LoadForProject(projectDir string) (*Config, error)  // From specific path
```

`Load()` finds the project root with `FindProjectRoot()` (`projectroot.go`): the nearest ancestor of cwd containing a `.sakusen.yml`, bounded by the git toplevel (which is itself checked, and is the fallback when no marker is found), so a `.sakusen.yml` in a subdirectory of a repo owns its own project. The TUI (`resolveProjectMode`) and the MCP server (`resolveProjectPath`) share the same helper.

## Key Types

### ProjectConfig (.sakusen.yml)

```go
type ProjectConfig struct {
    MaxWorkers               int
    DefaultPriority          string
    PollInterval             string                  // Daemon task poll cadence (e.g. "2s")
    Verification             *VerificationConfig
    Git                      GitConfig               // BaseBranch, BranchTemplate
    Agents                   map[string]AgentConfig  // Project-tier agent registry (slug → record)
    DefaultAgent             string                  // Fallback agent slug; empty → "claude"
    Summarizer               *SummarizerConfig       // Utility LLM role block (summaries, titles)
    MergeConflicts           *MergeConflictsConfig   // Merge-conflict resolver role block
    OnComplete               string                  // Top-level finalization action (moved out of git:)
    Workflows                []WorkflowEntry         `yaml:"workflows"` // flat list (string ref or inline)
    Routines                 []RoutineConfig         `yaml:"routines,omitempty"` // invocation bindings
    WorktreeSyncPaths        WorktreeSyncPathsConfig // Paths to copy/link into worktrees
    WorktreeSetupCommand     string                  // Single setup command (legacy)
    WorktreeSetupCommands    []string                // Ordered list of setup commands
    TmuxSetupCommand         string                  // Command to run after tmux session creation
    Notifications            *NotificationsConfig
    TmuxNestedAttachBehavior string                  // "switch" (default) or "nest"
    Options                  *OptionsConfig          // TUI display options
}
```

Removed keys (`claude:`, `yolo:`, `system_prompt:`, `allowed_summarization_models:`,
`merge_conflict_agent:`, `periodic:`) are hard
load-time migration errors surfaced by `checkRemovedProjectKeys` (agents.go) on the raw YAML.

`WorkflowEntry` is a single item in the flat `workflows:` list — exactly one of `Ref` (string) or `Inline` (`*WorkflowConfig`) is set. String entries resolve to `.sakusen/workflows/<name>.yml` (local first, then global pool).

### OptionsConfig

```go
type OptionsConfig struct {
    Number    *bool            // show line numbers
    Branch    *bool            // show branch column
    Target    *bool            // show target branch column
    Animation *AnimationConfig // sakusen animation on task submit
}

type AnimationConfig struct {
    Enabled  *bool // disabled by default
    Duration *int  // milliseconds (default 1000)
}
```

Options are also settable at runtime via vim-style `:set` commands.
Boolean options: `:set X`, `:set noX`, `:set X!` (toggle).
Value options: `:set X=N`. See `command.go` `boolOptions`/`intOptions` registries.

### WorkflowConfig

```go
type WorkflowConfig struct {
    Name                  string
    Description           string                  // human-readable metadata (picker/MCP); NOT a pin
    // Pinnable New Task screen fields — when set, the corresponding field is
    // hidden and pre-filled. IsFullySpec() returns true when all are pinned,
    // allowing the New Task screen to be skipped entirely.
    Input                 string                  // pins the task input (seeds {{task.input}})
    Worktree              *bool                   // pins the worktree toggle
    Branch                string                  // pins a new-branch template (forces branch-mode "new")
    Checkout              string                  // pins an existing branch to check out (forces branch-mode "existing")
    Target                string                  // pins the target/base branch
    Agent                 string                  // workflow-level agent slug; steps inherit unless they set their own
    OnComplete            string                  // per-workflow override of the finalization action
    Steps                 []StepConfig
    SummarizerPrompt      string
    WorktreeSyncPaths     WorktreeSyncPathsConfig // Per-workflow sync paths (override project-level)
    WorktreeSetupCommand  string                  // Per-workflow setup command (override project-level)
    WorktreeSetupCommands []string                // Per-workflow ordered setup commands
    TmuxSetupCommand      string                  // Per-workflow tmux setup command (override project-level)

    // Populated by the loader, not from YAML:
    Hidden                bool                    // not offered in the `n` picker and never the implicit default:
                                                  //   a pool file not listed in workflows:, or a <slug>:<name> track workflow
    Source                string                  // "inline" or path under .sakusen/workflows/
    FromGlobal            bool                    // definition adopted from the global scope
}
```

Methods: `IsFullySpec() bool` (true when input + worktree + branch/checkout + target are all pinned, so the New Task screen is skipped — note `description` is metadata and does NOT gate skip); `ValidatePins() error` (branch and checkout are mutually exclusive; branch/checkout/target are rejected when worktree is pinned false — the rules live in `validatePinFields`, shared with `RoutineConfig`).

The removed `tmux:` and `print:` fields are rejected at parse time (workflow and step level) with migration errors — execution mode now comes from the resolved agent record's `mode` (see the Agents section below).

### GlobalConfig (~/.config/sakusen/config.yaml)

```go
type GlobalConfig struct {
    MaxWorkers               int
    PollInterval             string
    Verification             *VerificationConfig
    Notifications            NotificationsConfig
    TmuxNestedAttachBehavior string
    Options                  *OptionsConfig
    Agents                   map[string]AgentConfig
    DefaultAgent             string
    MergeConflicts           *MergeConflictsConfig
    Summarizer               *SummarizerConfig
}
```

The merged runtime `Config` (in `types.go`) flattens project + global settings and also holds
the merged `Agents` registry, `DefaultAgent`, `Summarizer`, `MergeConflicts`, and the resolved
`WorktreeSyncPaths`.

### Agents (agents.go)

```go
type AgentConfig struct {
    Mode           string            // "headless" (default when empty) or "tmux"
    Command        string            // required; run via `sh -c` in the task workdir
    ResumeCommand  string            // tmux-only: resume with SAKUSEN_SESSION_ID after daemon restart
    ChatLogCommand string            // tmux-only: prints the chat log on stdout for summarize_chat
    Env            map[string]string // extra env for every spawn of this agent
}

type SummarizerConfig struct {
    Agent          string // `agent:` — headless registry slug; runs via the prompt/result file contract
    Command        string // prompt on stdin, response on stdout; mutually exclusive with Agent
    SlugAgent      string // `slug_agent:` — runs the slug call on its own agent (falls back to Agent)
    SlugCommand    string // `slug_command:` — runs the slug call on its own command (falls back to Command)
    MaxPromptBytes int    // >0 → map-reduce chunking ceiling; 0 disables chunking
    TitlePrompt    string // `title_prompt:` — overrides the built-in AI title prompt ({{input}})
    SlugPrompt     string // `slug_prompt:`  — overrides the built-in AI slug prompt ({{input}})
}

type MergeConflictsConfig struct {
    Agent   string // headless registry slug; empty → workflow agent → default_agent → "claude"
    Timeout string // duration string bounding one resolver pass; empty → DefaultMergeConflictTimeout ("10m")
    Prompt  string // replaces the built-in resolver body entirely ({{conflict.files}} + task/git vars)
}
```

Resolution cascade (`Config.StepAgent(wf, &step)`): `step.agent` → `workflow.agent` →
top-level `default_agent:` → `"claude"` (`DefaultAgentSlug`). Helpers: `StepAgentSlug`,
`WorkflowAgentSlug`, `StepIsTmux`, `FirstStepIsTmux`, `ResolveAgent`,
`EffectiveDefaultAgentSlug`. The agent's mode decides headless vs tmux execution.

Variants and composed refs: an agent may declare `variants:` (variant name → partial record)
that inherit every parent field and override only what they redefine (`env` merges per-key,
variant wins; other fields override wholesale). `expandAgentVariants` inserts each as an
ordinary `<parent>:<variant>` registry entry and clears `Variants` on stored records. A ref is
`parent[:modifier]*`: it may stack any number of that parent's variants as modifiers in any
order, `canonicalAgentRef` sorts them alphabetically into the registry key, and `ResolveAgent`
canonicalizes before lookup (authored refs are never rewritten). `expandComposedAgentRefs`
expands only the composed refs the config actually mentions (`default_agent`, workflow/step
`agent:`, alias targets) — never the cross product. Two modifiers setting the same field or env
key to different values is a load error, as are duplicate modifiers, modifiers the parent
doesn't declare, and nested `variants:`.

Merging: `mergeAgents` overlays project-tier records onto global-tier per slug (a redefined
slug wins wholesale). `resolveAndValidateAgents` runs in `Load()`/`LoadForProject()` AFTER all
tiers merge: record shape (kebab-case slug, required command, valid mode, tmux-only fields),
explicit `agent:`/`default_agent:` refs must resolve (the implicit `"claude"` fallback is
exempt — it fails at step-run time with an instructive error), loop steps must not resolve to
tmux agents, and `{{claude_command}}` in any tmux-setup-command is rejected (use
`{{agent_command}}` / `{{run_agent}}`).

### VerificationConfig

```go
type VerificationConfig struct {
    MaxRetries       int
    VerifySummarizer bool
}
```

## Workflow List

`workflows:` is a flat YAML sequence. Each item is either a string ref (resolves to `.sakusen/workflows/<name>.yml`) or an inline mapping:

```yaml
workflows:
  - name: fast                   # no pins → always prompts New Task screen
    steps: [...]
  - update-changelog             # string ref → .sakusen/workflows/update-changelog.yml
  - name: housekeeping           # all fields pinned → skips New Task screen
    description: "Run standard maintenance"
    worktree: true
    branch: sakusen/housekeeping-{{task.id}}
    target: main
    steps: [...]
```

"Kind" is now an emergent property of how completely a workflow pins the New Task form and of whether a routine binds it — not a config category. The `n` key (and `:RunTask`) operates over the single flat list; fully-pinned workflows create a task immediately without showing the form.

### Routines (invocation bindings)

`routines:` is the orthogonal axis to `workflows:`: definitions live under `workflows:`, ways of invoking them live here. A routine names a workflow, supplies pins and an optional cadence, and NEVER defines steps.

```go
type RoutineConfig struct {
    Name        string   // required, kebab-case, unique within routines:
    Description string   // metadata for the palette and MCP; never a pin
    Workflow    string   // required; resolved by exact name (hidden pool files and <slug>:<name> track workflows allowed)
    Input       string   // pins
    Worktree    *bool
    Branch      string
    Checkout    string
    Target      string
    Cadence     string   // cron / @every / descriptor; empty ⇒ on-demand only
    Priority    string   // empty ⇒ project default at fire time
    Paused      bool     // only meaningful with a cadence
}
```

```yaml
routines:
  - name: compose-wiki           # on-demand: :RunRoutine, `sakusen routines run`, MCP run_routine
    description: Rebuild the wiki
    workflow: wiki-compose
    branch: sakusen/wiki-{{task.id}}
  - name: nightly-sweep          # + cadence ⇒ also scheduled
    workflow: wiki-compose
    cadence: "0 3 * * *"
```

- `steps`, `agent`, `summarizer_prompt`, `tmux`, `print` on a routine are load errors ("routines are bindings").
- `EffectivePins(wf)` merges the routine over the workflow: `input`, `worktree` and `target` per field; `branch`/`checkout` as a PAIR (setting either on the routine ignores both of the workflow's). `ValidatePins(wf)` runs the shared pin rules against the merged result, naming the routine.
- `IsScheduled()` is `Cadence != ""`. Both kinds get a `periodic_definitions` row; cadence-less rows are never due.
- `Config.RoutineRequiresInput(r)` is true when the effective input is empty and the workflow references `{{task.input}}` in any step or parallel-branch prompt. That is a LOAD ERROR for a scheduled routine and legal for an on-demand one, whose run surfaces then demand the argument (the daemon re-checks it).
- A workflow referenced only from `routines:` stays hidden — the routine is what makes it startable — and `Diagnose` suppresses the "is hidden" warning for it.

### Track Workflows

Per-track workflow files live under `.sakusen/tracks/<slug>/workflows/*.yml` (project tier) and `~/.sakusen/tracks/<slug>/workflows/*.yml` (global tier). `appendTrackWorkflows` (called from `Load()`/`LoadForProject()` AFTER project-level resolution — never inside `loadProjectConfig`) registers them as `Hidden: true` workflows named `<slug>:<name>`; project shadows global on identical namespaced names. They follow the same file rules as `.sakusen/workflows/` (flat, kebab-case, no `name:` field) and go through the same pin/loop/step validation. See `internal/config/CLAUDE.md` for the full invariant.

### Prompt Includes

`{{prompt.<name>}}` in any templated prompt string — step `prompt`, step `summarization_prompt`, workflow `summarizer_prompt`, `merge_conflicts.prompt` — inlines the contents of `<name>.md`, so several workflows can share one passage of prompt text. There is NO `prompts:` YAML map and no agent-level prompt: the unit of reuse is a passage of text, not a step or an agent.

Lookup mirrors the workflow tiers, first hit wins (`PromptDirs(projectDir)`):

1. `<project>/.sakusen/prompts/<name>.md`
2. `~/.sakusen/prompts/<name>.md`

`<name>` allows `[A-Za-z0-9_-]+` (kebab-case by convention). `ExpandPromptIncludes` strips a single trailing newline, and includes are one level deep — an included file containing `{{prompt.*}}` is an error. `validatePromptIncludes` runs in `Load()`/`LoadForProject()` (after track workflows and agent resolution) and in `validateProject`, so a missing file / bad name / nested include is a hard load and `sakusen validate` error naming the workflow, step, field and searched paths. Validation does not rewrite the config — `internal/workflow` re-reads the file at every step launch, so editing a shared passage needs no reload. See `internal/config/CLAUDE.md`.

## StepConfig

```go
type StepConfig struct {
    Name, Prompt, Mode    string
    Description           string         // human-readable step metadata (surfaced via MCP); never interpolated
    Agent                 string         // agent slug override; empty = inherit workflow/default
    Timeout               string         // Parsed duration, default 30m (DefaultStepTimeout)
    Human                 bool           // Approval gate
    Loop                  *LoopConfig    // Optional retry loop
    SummarizationStrategy string         // Strategy for summarizing step output
    SummarizationPrompt   string         // Custom summarize_chat prompt ({{chat}} placeholder)
    RequireContext        bool           // Fail the task when summarize_chat context capture fails
    Parallel              *ParallelConfig // Fan-out group; mutually exclusive with Prompt/Loop/Human (see below)
}
```

**Summarization strategies**: `summarize_chat` (default when unset) runs the configured `summarizer:` command over the full chat log; `last_message` keeps the headless agent's result text — cheaper but often misleading and unusable for tmux steps (which have no result text). The default is resolved via `StepConfig.EffectiveSummarizationStrategy()` and lives in `DefaultSummarizationStrategy`. Validated at config load via `ValidateSteps()`.

**System roles**: the summarizer and the merge-conflict resolver each get a top-level block holding the role's knobs while picking a runner from the `agents:` registry. Role agents must be headless (both are synchronous passes) and are validated at load: `validateAgentRefs` requires every role slug to resolve and be non-tmux, `validateRoleBlocks` (shared with `validate.go`) enforces `agent`/`command` exclusivity and `merge_conflicts.timeout` parseability. Both blocks merge WHOLESALE across tiers.

**Summarizer**: all summarization (step `summarize_chat` passes, the final task summarizer, AI titles and slugs, backfill-context) runs the single top-level `summarizer:` block, resolved by `Config.SummarizerInvocation()` / `SummarizerSlugInvocation()` into a `SummarizerInvocation` and executed by `workflow.RunSummarizer` — `agent:` goes through `runner.RunAgentSync` (prompt in `$SAKUSEN_PROMPT_FILE` in a scratch dir, answer from `$SAKUSEN_RESULT_FILE` with a stdout-tail fallback, `SAKUSEN_PROJECT_PATH` + the agent's `env` exported), `command:` through `runner.RunSync` (prompt on stdin, response on stdout); `SAKUSEN_PURPOSE` tags the call site either way. `MaxPromptBytes` (when > 0) gates map-reduce chunking of oversized chat logs; `TitlePrompt`/`SlugPrompt` override the built-in title/slug prompts (`{{input}}` = task input); the slug side resolves via `EffectiveSlugAgent()`/`EffectiveSlugCommand()` and is gated by `SlugConfigured()` (so a slug-only setting enables AI slugs without AI titles); there is no model selection in sakusen — pick the model inside the agent or command. `allowed_summarization_models` (top-level and step-level) is a removed key with a hard migration error.

**Merge conflicts**: `merge_conflicts:` configures the resolver — `agent:` (cascade `merge_conflicts.agent` → workflow `agent:` → `default_agent:` → `"claude"`, resolved by `Config.MergeConflictAgentFor`; only the lower tiers fall back from a tmux agent to a headless `"claude"`), `timeout:` (default `10m`), and `prompt:` (replaces the built-in body; `{{conflict.files}}` plus task/git vars). The old top-level `merge_conflict_agent:` is a removed key with a migration error.

### ParallelConfig (fan-out / fan-in groups)

```go
type ParallelConfig struct {
    Require  string        // "" | "all" (default) | "any" | "<N>" (1..len(Branches))
    Branches []StepConfig  // Branches reuse StepConfig; each is an ordinary headless step
}

func (p *ParallelConfig) EffectiveRequire() string                            // "" → "all"
func (p *ParallelConfig) RequiredCount() int                                  // how many must complete
func (p *ParallelConfig) EffectiveBranch(i int, group *StepConfig) StepConfig // THE normalization point
func (s *StepConfig) IsParallel() bool
func (s *StepConfig) EffectiveBranches() []StepConfig
func (wf *WorkflowConfig) AllStepNames() []string                             // steps + branches, in order
func (wf *WorkflowConfig) BranchGroup(name string) (*StepConfig, int, bool)
```

A step entry carrying `parallel:` is a GROUP: no prompt of its own, one cursor slot, N branches that run concurrently on the same worktree. `EffectiveBranch` is the ONLY place branch defaults are applied — `agent` and `timeout` fall back to the group's, and an unset `summarization_strategy` becomes `last_message` (deliberately NOT `DefaultSummarizationStrategy`: a branch's result text is the artifact the group's aggregate is built from, and a summarizer pass would rewrite it). It returns a copy; the parsed config is never rewritten, so every consumer must call it rather than reading `Branches[i]` directly.

**Group/branch validation** (`ValidateSteps` → `validateParallelGroup`, reached by every load path and `sakusen validate`):

- A group may not set `prompt`, `loop`, `human`, `summarization_strategy`, `summarization_prompt`, or `require_context` — all belong on a branch.
- `branches` needs ≥ 1 entry; `require` must be `""`, `all`, `any`, or an integer in `1..len(branches)`.
- A branch may not be a group (no nesting), may not have `loop` or `human`, and must be an inline mapping (a bare-scalar entry parses as a step *reference*, which only resolves against top-level steps).
- Step names and branch names share ONE namespace and must be unique across the whole workflow — `{{steps.<name>.context}}` addresses both. Also enforced in `validateUniqueNames` and `resolveWorkflowSteps`.
- `ValidateLoops`: a `goto` naming a branch is an error (target the group); exit conditions may name a branch or a group.
- `validateAgentRefs` (production load only): every branch's EFFECTIVE agent must be headless — the join is synchronous, so a tmux branch could never complete. `collectAgentRefs`, `validatePromptIncludes` and the routine input guard all walk branches too.

**Loop validation**: goto must reference earlier step, max_iterations >= 1, no `human: true` on looped steps, no overlapping ranges; a loop step must not resolve to a tmux-mode agent (checked in `validateAgents` after tiers merge).

### LoopConfig

```go
type LoopConfig struct {
    Goto          string             // Target step name to jump back to
    MaxIterations int                // Required, must be >= 1
    ExitCondition *LoopExitCondition // Optional early exit condition
}

type LoopExitCondition struct {
    StepContextEmpty    string // Step name whose context to check; exit if empty
    StepContextContains string // Step name whose context to search for Marker
    Marker              string // Literal substring that ends the loop (required with StepContextContains)
}
```

Both forms may be set; the loop exits as soon as either matches.

**Prefer `StepContextContains`.** `StepContextEmpty` exits on an *absence*, so it cannot tell "the step decided the work is done" from "the step crashed, timed out, or forgot to publish" — each of those silently ends the loop and ships whatever is on the branch. A marker requires the step to make a positive statement; a step that fails to run leaves it absent and the loop keeps going, bounded by `max_iterations`.

Validation: `StepContextContains` requires a non-blank `Marker`, `Marker` requires `StepContextContains`, both step references must name real steps, and an `exit_condition` block setting neither form is a load-time error.

## Worktree Sync Paths

```go
type WorktreeSyncPathsConfig struct {
    Copy []string   // Paths to copy into worktrees
    Link []string   // Paths to symlink into worktrees
}

GetWorktreeSyncPaths(wf *WorkflowConfig) WorktreeSyncPathsConfig
```

Supports two modes: `copy` (full recursive copy) and `link` (symlink to source).
Legacy plain-list format (`[]string`) is treated as copy paths for backward compatibility.
Returns workflow-level paths if set, otherwise project-level `WorktreeSyncPaths`.

## Branch Templates

Default: `"sakusen/{{task_id}}-{{task_slug}}"`

Variables: `{{task_id}}`, `{{task_slug}}`, `{{task.title}}`, `{{task.id}}`, `{{task.slug}}`

## Config Accessors

```go
GetWorkflow(name string) *WorkflowConfig            // By name; returns first if name=""; returns DefaultWorkflow() if not found
GetTaskWorkflow(name string) *WorkflowConfig        // By name (includes hidden); returns first non-hidden if name=""; nil if not found
DefaultWorkflow() WorkflowConfig                    // Built-in single-step default workflow
ListWorkflowNames() []string                        // Active (non-hidden) workflow names; ["default"] if none configured
ListAllWorkflowNames() []string                     // All workflow names including hidden (for pickers/tab-completion)
GetRoutine(name string) *RoutineConfig              // By name; nil if absent
ListRoutineNames() []string                         // All routine names, config order
RoutineRequiresInput(r *RoutineConfig) bool         // Effective input empty AND the workflow references {{task.input}}
GetStepTimeout(step StepConfig) time.Duration       // Parses Timeout string, falls back to 30m
GetWorktreeSetupCommand(wf *WorkflowConfig) string  // Workflow-level override, then project-level
GetTmuxSetupCommand(wf *WorkflowConfig) string      // Workflow-level override, then project-level
ResolveBranchForTask(taskID int64, taskTitle, taskSlug, branchName string) string
WriteProjectConfig(path string, proj *ProjectConfig) error  // Package-level function
```

## Exported Utilities

```go
GetGlobalDataDir() string                           // ~/.config/sakusen/ (respects XDG_CONFIG_HOME)
SanitizeProjectName(name string) string             // Replaces dots with underscores
```

## File Map

| File | Purpose |
|------|---------|
| `types.go` | All struct/type definitions and their methods (`Config`, `ProjectConfig`, `WorkflowConfig`, `StepConfig`, etc.) |
| `config.go` | Loading, parsing, merging, defaults (`Load()`, `LoadForProject()`, `defaultConfig()`, `resolveWorkflows()`) |
| `agents.go` | `AgentConfig`/`SummarizerConfig`, the step→workflow→default_agent→"claude" cascade (`StepAgent` etc.), `validateAgents`, `mergeAgents`, `checkRemovedProjectKeys` (removed-key migration errors) |
| `accessors.go` | Workflow and routine accessors, branch templates, save (`GetWorkflow()`, `ListWorkflowNames()`, `GetRoutine()`, `RoutineRequiresInput()`, `ResolveBranchTemplate()`, `Save()`) |
| `cadence.go` | Routine cadence parsing (`ParseRoutineCadence()`, `NextRoutineFire()`) — 5-field cron + descriptors + `@every` |
| `prompts.go` | `{{prompt.<name>}}` includes — `PromptDirs()`, `ExpandPromptIncludes()`, `validatePromptIncludes()` |
| `projectroot.go` | `FindProjectRoot()` — nearest ancestor `.sakusen.yml`, bounded by the git toplevel; shared by `Load()`, the TUI and the MCP server |
| `detect.go` | Project type detection (`DetectProject()`) |
| `validate.go` | Single-file validation for `sakusen validate` (`ValidateFile()`/`Diagnose()`) — enums, agent record shapes, loop/step rules, workflow file pool |

## Project Detection (detect.go)

`DetectProject()` probes for `package.json`, `go.mod`, `Gemfile`, Python markers, `Cargo.toml`. Returns `DetectedProject{Type, Commands}`. Detects `bun.lockb` and swaps npm -> bun. Project name always derives from `filepath.Base(dir)` in `ApplyDetectedProject` — manifest names are ignored to avoid scope/path characters that break tmux session lookup.

## Patterns

- Access workflows via `ListWorkflowNames()`, `GetWorkflow()`, `GetTaskWorkflow()`
- Resolve a step's agent via `Config.StepAgent()` (never read `step.Agent` directly — the cascade and registry lookup live in one place)
- Config validation at parse time; invalid configs return errors
- New fields: add to struct + YAML tag + merge logic + test fixtures
