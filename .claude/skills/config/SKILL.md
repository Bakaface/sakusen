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
4. `./.sakusen.yml` (project-specific, wins)

```go
Load() (*Config, error)                    // From current directory
LoadForProject(projectDir string) (*Config, error)  // From specific path
```

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
    Summarizer               *SummarizerConfig       // Utility LLM command (summaries, titles)
    OnComplete               string                  // Top-level finalization action (moved out of git:)
    Workflows                []WorkflowEntry         `yaml:"workflows"` // flat list (string ref or inline)
    WorktreeSyncPaths        WorktreeSyncPathsConfig // Paths to copy/link into worktrees
    WorktreeSetupCommand     string                  // Single setup command (legacy)
    WorktreeSetupCommands    []string                // Ordered list of setup commands
    TmuxSetupCommand         string                  // Command to run after tmux session creation
    Notifications            *NotificationsConfig
    TmuxNestedAttachBehavior string                  // "switch" (default) or "nest"
    Options                  *OptionsConfig          // TUI display options
}
```

Removed keys (`claude:`, `yolo:`, `system_prompt:`, `allowed_summarization_models:`) are hard
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
    Hidden                bool                    // file-based workflow not referenced from .sakusen.yml
    Source                string                  // "inline" or path under .sakusen/workflows/
    FromGlobal            bool                    // definition adopted from the global scope
}
```

Methods: `IsFullySpec() bool` (true when input + worktree + branch/checkout + target are all pinned, so the New Task screen is skipped — note `description` is metadata and does NOT gate skip); `ValidatePins() error` (branch and checkout are mutually exclusive; branch/checkout/target are rejected when worktree is pinned false).

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
    Summarizer               *SummarizerConfig
}
```

The merged runtime `Config` (in `types.go`) flattens project + global settings and also holds
the merged `Agents` registry, `DefaultAgent`, `Summarizer`, and the resolved `WorktreeSyncPaths`.

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
    Command        string // prompt on stdin, response on stdout; SAKUSEN_PURPOSE tags the call site
    MaxPromptBytes int    // >0 → map-reduce chunking ceiling; 0 disables chunking
}
```

Resolution cascade (`Config.StepAgent(wf, &step)`): `step.agent` → `workflow.agent` →
top-level `default_agent:` → `"claude"` (`DefaultAgentSlug`). Helpers: `StepAgentSlug`,
`WorkflowAgentSlug`, `StepIsTmux`, `FirstStepIsTmux`, `ResolveAgent`,
`EffectiveDefaultAgentSlug`. The agent's mode decides headless vs tmux execution.

Merging: `mergeAgents` overlays project-tier records onto global-tier per slug (a redefined
slug wins wholesale). `validateAgents` runs in `Load()`/`LoadForProject()` AFTER all tiers
merge: record shape (kebab-case slug, required command, valid mode, tmux-only fields),
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

"Kind" is now an emergent property of how completely a workflow pins the New Task form — not a config category. The `n` key (and `:RunTask`) operates over the single flat list; fully-pinned workflows create a task immediately without showing the form.

### Track Workflows

Per-track workflow files live under `.sakusen/tracks/<slug>/workflows/*.yml` (project tier) and `~/.sakusen/tracks/<slug>/workflows/*.yml` (global tier). `appendTrackWorkflows` (called from `Load()`/`LoadForProject()` AFTER project-level resolution — never inside `loadProjectConfig`) registers them as `Hidden: true` workflows named `<slug>:<name>`; project shadows global on identical namespaced names. They follow the same file rules as `.sakusen/workflows/` (flat, kebab-case, no `name:` field) and go through the same pin/loop/step validation. See `internal/config/CLAUDE.md` for the full invariant.

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
}
```

**Summarization strategies**: `summarize_chat` (default when unset) runs the configured `summarizer:` command over the full chat log; `last_message` keeps the headless agent's result text — cheaper but often misleading and unusable for tmux steps (which have no result text). The default is resolved via `StepConfig.EffectiveSummarizationStrategy()` and lives in `DefaultSummarizationStrategy`. Validated at config load via `ValidateSteps()`.

**Summarizer command**: all summarization (step `summarize_chat` passes, the final task summarizer, AI titles, backfill-context) runs the single top-level `summarizer:` command via `runner.RunSync` — prompt on stdin, response on stdout, `SAKUSEN_PURPOSE` tagging the call site. `SummarizerConfig.MaxPromptBytes` (when > 0) gates map-reduce chunking of oversized chat logs; there is no model selection in sakusen — pick the model inside the command. `allowed_summarization_models` (top-level and step-level) is a removed key with a hard migration error.

**Loop validation**: goto must reference earlier step, max_iterations >= 1, no `human: true` on looped steps, no overlapping ranges; a loop step must not resolve to a tmux-mode agent (checked in `validateAgents` after tiers merge).

### LoopConfig

```go
type LoopConfig struct {
    Goto          string             // Target step name to jump back to
    MaxIterations int                // Required, must be >= 1
    ExitCondition *LoopExitCondition // Optional early exit condition
}

type LoopExitCondition struct {
    StepContextEmpty string // Step name whose context to check; exit if empty
}
```

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
| `accessors.go` | Workflow accessors, branch templates, save (`GetWorkflow()`, `ListWorkflowNames()`, `ResolveBranchTemplate()`, `Save()`) |
| `detect.go` | Project type detection (`DetectProject()`) |
| `validate.go` | Single-file validation for `sakusen validate` (`ValidateFile()`/`Diagnose()`) — enums, agent record shapes, loop/step rules, workflow file pool |

## Project Detection (detect.go)

`DetectProject()` probes for `package.json`, `go.mod`, `Gemfile`, Python markers, `Cargo.toml`. Returns `DetectedProject{Type, Commands}`. Detects `bun.lockb` and swaps npm -> bun. Project name always derives from `filepath.Base(dir)` in `ApplyDetectedProject` — manifest names are ignored to avoid scope/path characters that break tmux session lookup.

## Patterns

- Access workflows via `ListWorkflowNames()`, `GetWorkflow()`, `GetTaskWorkflow()`
- Resolve a step's agent via `Config.StepAgent()` (never read `step.Agent` directly — the cascade and registry lookup live in one place)
- Config validation at parse time; invalid configs return errors
- New fields: add to struct + YAML tag + merge logic + test fixtures
