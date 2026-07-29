---
name: config
description: >
  Sortie's configuration system: .sortie.yml parsing, loading hierarchy, workflow
  definitions, step/loop config, project type detection, and branch template resolution.
  Use when editing files in internal/config/, working on .sortie.yml parsing,
  workflow definitions, project detection, or configuration loading.
---

# Configuration System

## Loading Hierarchy (highest priority last)

1. Built-in defaults
2. `~/.config/sortie/config.yaml` (global daemon — `GlobalConfig`)
3. `~/.sortie.yml` (global workflow defaults)
4. `./.sortie.yml` (project-specific, wins)

```go
Load() (*Config, error)                    // From current directory
LoadForProject(projectDir string) (*Config, error)  // From specific path
```

## Key Types

### ProjectConfig (.sortie.yml)

```go
type ProjectConfig struct {
    MaxWorkers                 int
    DefaultPriority            string
    Yolo                       *bool                   // Skip Claude permissions (pointer for merge)
    PollInterval               string                  // Daemon task poll cadence (e.g. "2s")
    Claude                     *ClaudeConfig           // Per-project claude binary / default args
    Verification               *VerificationConfig
    Git                        GitConfig               // BaseBranch, BranchTemplate, OnComplete
    Workflows                  []WorkflowEntry         `yaml:"workflows"` // flat list (string ref or inline)
    SystemPrompt               string
    WorktreeSyncPaths          WorktreeSyncPathsConfig // Paths to copy/link into worktrees
    WorktreeSetupCommand       string                  // Single setup command (legacy)
    WorktreeSetupCommands      []string                // Ordered list of setup commands
    TmuxSetupCommand           string                  // Command to run after tmux session creation
    Notifications              *NotificationsConfig
    TmuxNestedAttachBehavior   string                  // "switch" (default) or "nest"
    Options                    *OptionsConfig          // TUI display options
    AllowedSummarizationModels []string                // Allowlist of haiku/sonnet/opus for the summarizer
}
```

`WorkflowEntry` is a single item in the flat `workflows:` list — exactly one of `Ref` (string) or `Inline` (`*WorkflowConfig`) is set. String entries resolve to `.sortie/workflows/<name>.yml` (local first, then global pool).

### OptionsConfig

```go
type OptionsConfig struct {
    Number    *bool            // show line numbers
    Branch    *bool            // show branch column
    Target    *bool            // show target branch column
    Animation *AnimationConfig // sortie animation on task submit
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
    Print                 bool                    // workflow-level default: true = headless `claude -p`, false = tmux. Step-level Print overrides.
    Steps                 []StepConfig
    SummarizerPrompt      string
    WorktreeSyncPaths     WorktreeSyncPathsConfig // Per-workflow sync paths (override project-level)
    WorktreeSetupCommand  string                  // Per-workflow setup command (override project-level)
    WorktreeSetupCommands []string                // Per-workflow ordered setup commands
    TmuxSetupCommand      string                  // Per-workflow tmux setup command (override project-level)

    // Populated by the loader, not from YAML:
    Hidden                bool                    // file-based workflow not referenced from .sortie.yml
    Source                string                  // "inline" or path under .sortie/workflows/
}
```

Methods: `IsFullySpec() bool` (true when input + worktree + branch/checkout + target are all pinned, so the New Task screen is skipped — note `description` is metadata and does NOT gate skip); `ValidatePins() error` (branch and checkout are mutually exclusive; branch/checkout/target are rejected when worktree is pinned false).

The legacy `tmux:` field is rejected at parse time with a migration error — the replacement is the inverted `Print` field.

### GlobalConfig (~/.config/sortie/config.yaml)

```go
type GlobalConfig struct {
    MaxWorkers               int
    Yolo                     *bool
    PollInterval             string
    Claude                   *ClaudeConfig
    Verification             *VerificationConfig
    Notifications            NotificationsConfig
    TmuxNestedAttachBehavior string
    Options                  *OptionsConfig
}
```

The merged runtime `Config` (in `config.go`) flattens project + global settings and also holds
`AllowedSummarizationModels`, `SystemPrompt`, and the resolved `WorktreeSyncPaths`.

### VerificationConfig

```go
type VerificationConfig struct {
    MaxRetries       int
    VerifySummarizer bool
}
```

## Workflow List

`workflows:` is a flat YAML sequence. Each item is either a string ref (resolves to `.sortie/workflows/<name>.yml`) or an inline mapping:

```yaml
workflows:
  - name: fast                   # no pins → always prompts New Task screen
    steps: [...]
  - update-changelog             # string ref → .sortie/workflows/update-changelog.yml
  - name: housekeeping           # all fields pinned → skips New Task screen
    description: "Run standard maintenance"
    worktree: true
    branch: sortie/housekeeping-{{task.id}}
    target: main
    steps: [...]
```

"Kind" is now an emergent property of how completely a workflow pins the New Task form — not a config category. The `n` key (and `:RunTask`) operates over the single flat list; fully-pinned workflows create a task immediately without showing the form.

### Track Workflows

Per-track workflow files live under `.sortie/tracks/<slug>/workflows/*.yml` (project tier) and `~/.sortie/tracks/<slug>/workflows/*.yml` (global tier). `appendTrackWorkflows` (called from `Load()`/`LoadForProject()` AFTER project-level resolution — never inside `loadProjectConfig`) registers them as `Hidden: true` workflows named `<slug>:<name>`; project shadows global on identical namespaced names. They follow the same file rules as `.sortie/workflows/` (flat, kebab-case, no `name:` field) and go through the same pin/loop/step validation. See `internal/config/CLAUDE.md` for the full invariant.

## StepConfig

```go
type StepConfig struct {
    Name, Prompt, Mode    string
    Description           string         // human-readable step metadata (surfaced via MCP); never interpolated
    Print                 *bool          // Override workflow-level Print; nil = inherit
    Timeout               string         // Parsed duration, default 30m (DefaultStepTimeout)
    Human                 bool           // Approval gate
    Loop                  *LoopConfig    // Optional retry loop
    SummarizationStrategy string         // Strategy for summarizing step output
}
```

**Summarization strategies**: `summarize_chat` (default when unset) runs a haiku pass over the full chat log; `last_message` uses the Claude result event text — cheaper but often misleading and unusable for tmux steps (which have no result event). The default is resolved via `StepConfig.EffectiveSummarizationStrategy()` and lives in `DefaultSummarizationStrategy`. Validated at config load via `ValidateSteps()`.

**Summarization model selection**: the summarizer auto-picks a model per-call based on prompt size. Users do not name a model — they pass an `allowed_summarization_models` allowlist instead. Valid aliases: `haiku`, `sonnet`, `opus` (validated at config load and in `ValidateSteps`). Resolved via `StepConfig.EffectiveAllowedSummarizationModels(projectDefault)` — step-level > project-level (`ProjectConfig.AllowedSummarizationModels` → merged into runtime `Config.AllowedSummarizationModels`) > `DefaultAllowedSummarizationModels` (all three). The selector (`chooseSummarizationModel` in `internal/workflow/summarizer.go`) picks the **cheapest** allowed model whose prompt-byte ceiling (`maxPromptBytesForModel`: haiku 380 KB, sonnet 700 KB, opus 1500 KB — empirically calibrated) fits the resolved prompt; if no allowed model fits, the summarizer falls back to map-reduce on the largest allowed model and re-selects on the reduced prompt. The same allowlist drives both step-level `summarize_chat` passes and the final-task summarizer.

**Loop validation**: goto must reference earlier step, max_iterations >= 1, no human/tmux on looped steps, no overlapping ranges.

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

Default: `"sortie/{{task_id}}-{{task_slug}}"`

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
GetGlobalDataDir() string                           // ~/.config/sortie/ (respects XDG_CONFIG_HOME)
SanitizeProjectName(name string) string             // Replaces dots with underscores
```

## File Map

| File | Purpose |
|------|---------|
| `types.go` | All struct/type definitions and their methods (`Config`, `ProjectConfig`, `WorkflowConfig`, `StepConfig`, etc.) |
| `config.go` | Loading, parsing, merging, defaults (`Load()`, `LoadForProject()`, `defaultConfig()`, `resolveWorkflows()`) |
| `accessors.go` | Workflow accessors, branch templates, save (`GetWorkflow()`, `ListWorkflowNames()`, `ResolveBranchTemplate()`, `Save()`) |
| `detect.go` | Project type detection (`DetectProject()`) |
| `validate.go` | Cross-field validation (loop targets, model allowlist, deprecated fields) — invoked from `Load()` |

## Project Detection (detect.go)

`DetectProject()` probes for `package.json`, `go.mod`, `Gemfile`, Python markers, `Cargo.toml`. Returns `DetectedProject{Type, Commands}`. Detects `bun.lockb` and swaps npm -> bun. Project name always derives from `filepath.Base(dir)` in `ApplyDetectedProject` — manifest names are ignored to avoid scope/path characters that break tmux session lookup.

## Patterns

- Access workflows via `ListWorkflowNames()`, `GetWorkflow()`, `GetTaskWorkflow()`
- `ClaudeConfig.Args()` adds `--dangerously-skip-permissions` if Yolo
- Config validation at parse time; invalid configs return errors
- New fields: add to struct + YAML tag + merge logic + test fixtures
