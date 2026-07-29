package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// AnimationConfig controls the sortie (airplane) animation on task submission.
type AnimationConfig struct {
	Enabled  *bool `yaml:"enabled,omitempty"`
	Duration *int  `yaml:"duration,omitempty"` // milliseconds
}

// OptionsConfig holds TUI display options configurable via .sortie.yml
type OptionsConfig struct {
	Number     *bool            `yaml:"number,omitempty"`
	Branch     *bool            `yaml:"branch,omitempty"`
	Target     *bool            `yaml:"target,omitempty"`
	BranchView *bool            `yaml:"branchview,omitempty"`
	Animation  *AnimationConfig `yaml:"animation,omitempty"`
}

// WorktreeSyncPathsConfig specifies paths to sync into worktrees via copy or symlink.
// Supports both the new structured format (map with copy/link keys) and the legacy
// plain list format ([]string, treated as copy paths for backward compatibility).
type WorktreeSyncPathsConfig struct {
	Copy []string `yaml:"copy,omitempty"`
	Link []string `yaml:"link,omitempty"`
}

// UnmarshalYAML handles both the new structured format and the legacy list format.
// Legacy: worktree-sync-paths: [".claude", ".env"]  → treated as copy paths
// New:    worktree-sync-paths: { copy: [...], link: [...] }
func (w *WorktreeSyncPathsConfig) UnmarshalYAML(value *yaml.Node) error {
	// Try legacy list format first
	if value.Kind == yaml.SequenceNode {
		var paths []string
		if err := value.Decode(&paths); err != nil {
			return err
		}
		w.Copy = paths
		return nil
	}

	// New structured format
	type raw WorktreeSyncPathsConfig
	var r raw
	if err := value.Decode(&r); err != nil {
		return err
	}
	w.Copy = r.Copy
	w.Link = r.Link
	return nil
}

// IsEmpty returns true if no sync paths are configured.
func (w WorktreeSyncPathsConfig) IsEmpty() bool {
	return len(w.Copy) == 0 && len(w.Link) == 0
}

// AllPaths returns all configured paths (both copy and link) for backward-compatible checks.
func (w WorktreeSyncPathsConfig) AllPaths() []string {
	paths := make([]string, 0, len(w.Copy)+len(w.Link))
	paths = append(paths, w.Copy...)
	paths = append(paths, w.Link...)
	return paths
}

// ProjectConfig is loaded from .sortie.yml (both global ~/.sortie.yml and project-local)
type ProjectConfig struct {
	MaxWorkers      int                 `yaml:"max_workers"`
	DefaultPriority string              `yaml:"default_priority"`
	Yolo            *bool               `yaml:"yolo,omitempty"`
	PollInterval    string              `yaml:"poll_interval,omitempty"`
	Claude          *ClaudeConfig       `yaml:"claude,omitempty"`
	Verification    *VerificationConfig `yaml:"verification,omitempty"`
	Git             GitConfig           `yaml:"git"`
	// OnComplete is the project-level finalization action run after a task's
	// workflow finishes ("commit" | "merge" | "none"). Overridable per-workflow
	// via WorkflowConfig.OnComplete. Moved here from the former git.on_complete.
	OnComplete               string                  `yaml:"on_complete,omitempty"`
	Workflows                []WorkflowEntry         `yaml:"workflows"`
	Notifications            *NotificationsConfig    `yaml:"notifications,omitempty"`
	TmuxNestedAttachBehavior string                  `yaml:"tmux_nested_attach_behavior"`
	SystemPrompt             string                  `yaml:"system_prompt"`
	WorktreeSyncPaths        WorktreeSyncPathsConfig `yaml:"worktree-sync-paths"`
	WorktreeSetupCommand     string                  `yaml:"worktree-setup-command"`
	WorktreeSetupCommands    []string                `yaml:"worktree-setup-commands"`
	TmuxSetupCommand         string                  `yaml:"tmux-setup-command"`
	Options                  *OptionsConfig          `yaml:"options,omitempty"`
	// AllowedSummarizationModels restricts which Claude model aliases the
	// summarizer is allowed to pick from. The actual model is chosen
	// automatically per-call based on prompt size — the smallest allowed model
	// whose ceiling fits the prompt wins. Valid aliases: "haiku", "sonnet",
	// "opus". When empty, defaults to DefaultAllowedSummarizationModels (all
	// three). Per-step overrides live on StepConfig.AllowedSummarizationModels.
	AllowedSummarizationModels []string `yaml:"allowed_summarization_models,omitempty"`
}

// WorkflowEntry is a single item in the flat workflows: list. It is either
// a string referencing a workflow (a file under .sortie/workflows/ or a
// workflow defined in the global ~/.sortie.yml) or an inline WorkflowConfig
// definition. Exactly one of Ref / Inline is set.
type WorkflowEntry struct {
	// Ref, when non-empty, is the name of a referenced workflow, resolved
	// against the local file pool and then the global pool at load time
	// (unresolvable refs surface a hard error).
	Ref string

	// Inline, when non-nil, is an inline workflow definition.
	Inline *WorkflowConfig
}

// knownWorkflowFields lists the YAML keys of WorkflowConfig. Used to tell the
// standard inline form (`- name: X`, has a known key) apart from the named-body
// sugar (`- X: { ... }`, single unknown key) in WorkflowEntry.UnmarshalYAML.
var knownWorkflowFields = map[string]bool{
	"name":                    true,
	"description":             true,
	"input":                   true,
	"print":                   true,
	"on_complete":             true,
	"steps":                   true,
	"summarizer_prompt":       true,
	"worktree-sync-paths":     true,
	"worktree-setup-command":  true,
	"worktree-setup-commands": true,
	"tmux-setup-command":      true,
	"tmux":                    true, // deprecated — routed to the migration error
}

// UnmarshalYAML accepts three shapes:
//
//   - shared-impl              # scalar string ref
//   - name: the-work           # standard inline definition
//     steps: [...]
//   - the-work:                # named-body sugar (key is the workflow name)
//     steps: [...]
//
// The named-body sugar is recognised when the mapping has exactly one key that
// is not a known WorkflowConfig field. A sugar entry with an empty body
// (`- the-work:`) is treated as a bare ref, identical to `- the-work`.
func (e *WorkflowEntry) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		// String form: "- task-name"
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		e.Ref = s
		return nil
	case yaml.MappingNode:
		if name, body, ok := namedBodyEntry(value); ok {
			// Named-body sugar: the single unknown key is the workflow name.
			if body == nil || body.Tag == "!!null" {
				e.Ref = name
				return nil
			}
			var wf WorkflowConfig
			if err := body.Decode(&wf); err != nil {
				return err
			}
			wf.Name = name // the sugar key is authoritative
			e.Inline = &wf
			return nil
		}
		// Standard inline form: "- name: ...\n  steps: [...]"
		var wf WorkflowConfig
		if err := value.Decode(&wf); err != nil {
			return err
		}
		e.Inline = &wf
		return nil
	default:
		return fmt.Errorf("workflow entry must be a string (ref) or mapping (inline), got %v", value.Kind)
	}
}

// namedBodyEntry reports whether node is the named-body sugar form — a mapping
// with exactly one key that is not a known WorkflowConfig field — and if so
// returns the workflow name (the key) and its body node.
func namedBodyEntry(node *yaml.Node) (name string, body *yaml.Node, ok bool) {
	if node.Kind != yaml.MappingNode || len(node.Content) != 2 {
		return "", nil, false
	}
	key := node.Content[0]
	if key.Kind != yaml.ScalarNode || knownWorkflowFields[key.Value] {
		return "", nil, false
	}
	return key.Value, node.Content[1], true
}

type VerificationConfig struct {
	MaxRetries       int  `yaml:"max_retries"`
	VerifySummarizer bool `yaml:"verify_summarizer"`
}

type GitConfig struct {
	BaseBranch     string `yaml:"base_branch"`
	BranchTemplate string `yaml:"branch_template"`
}

// UnmarshalYAML decodes a GitConfig and rejects the removed `on_complete:` field
// with a migration error. on_complete moved from `git:` to the top level (and is
// now also overridable per-workflow).
func (g *GitConfig) UnmarshalYAML(value *yaml.Node) error {
	if err := checkRemovedGitOnComplete(value); err != nil {
		return err
	}
	type raw GitConfig
	var r raw
	if err := value.Decode(&r); err != nil {
		return err
	}
	*g = GitConfig(r)
	return nil
}

// checkRemovedGitOnComplete scans a `git:` mapping node for the removed
// `on_complete` key and returns a migration-friendly error pointing at the new
// top-level field.
func checkRemovedGitOnComplete(node *yaml.Node) error {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind == yaml.ScalarNode && key.Value == "on_complete" {
			return fmt.Errorf("git.on_complete was moved to the top level: replace `git:\\n  on_complete: X` with a top-level `on_complete: X` (it is now also overridable per-workflow)")
		}
	}
	return nil
}

type WorkflowConfig struct {
	Name string `yaml:"name"`

	// Description is a human-readable, one-line summary of the workflow. It is
	// PURE METADATA: surfaced via MCP (list_workflows) so agents can choose
	// workflows sensibly, and shown in the TUI workflow picker. It is NOT a pin
	// and never seeds the task input. (Historically this field doubled as the
	// input pin — see Input below — which silently hid the New Task input box;
	// that overload was removed.)
	Description string `yaml:"description,omitempty"`

	// The following five fields pre-pin New Task screen fields. When set, the
	// corresponding form field is hidden and its value supplied by the workflow.
	// See IsFullySpec() and the prompt-screen pin logic.
	//
	//   Input pins the task input (task.Input; seeds {{task.input}} and step
	//     prompts). When set, the New Task input box is hidden and this literal
	//     is submitted as the task input.
	//   Worktree pins the worktree toggle (task.Worktree).
	//   Branch pins a new-branch template (forces branch-mode "new").
	//   Checkout pins an existing branch to check out (forces branch-mode "existing").
	//   Target pins the target/base branch (task.TargetBranch).
	//
	// Branch and Checkout are mutually exclusive. Setting branch/checkout/target
	// while Worktree is pinned false is a config error (the git section is hidden).
	Input    string `yaml:"input,omitempty"`
	Worktree *bool  `yaml:"worktree,omitempty"`
	Branch   string `yaml:"branch,omitempty"`
	Checkout string `yaml:"checkout,omitempty"`
	Target   string `yaml:"target,omitempty"`

	// Print controls the workflow-level default execution mode for Claude steps.
	//   false (default): run each step in tmux (interactive Claude Code TUI).
	//   true:            run each step headless via `claude -p`.
	// Step-level Print overrides this default. Replaces the previous `tmux` field.
	Print bool `yaml:"print,omitempty"`
	// OnComplete, when non-empty, overrides the project-level on_complete action
	// for tasks running this workflow ("commit" | "merge" | "none"). Empty means
	// inherit the project-level setting. Precedence is locality-based (see
	// Config.EffectiveOnComplete): a project-scoped workflow's value beats the
	// project's top-level on_complete, but a GLOBAL workflow's value is only a
	// default — an explicit project-level on_complete beats it.
	OnComplete            string                  `yaml:"on_complete,omitempty"`
	Steps                 []StepConfig            `yaml:"steps"`
	SummarizerPrompt      string                  `yaml:"summarizer_prompt"`
	WorktreeSyncPaths     WorktreeSyncPathsConfig `yaml:"worktree-sync-paths,omitempty"`
	WorktreeSetupCommand  string                  `yaml:"worktree-setup-command,omitempty"`
	WorktreeSetupCommands []string                `yaml:"worktree-setup-commands,omitempty"`
	TmuxSetupCommand      string                  `yaml:"tmux-setup-command,omitempty"`

	// Hidden marks workflows loaded from .sortie/workflows/ that are NOT
	// referenced from .sortie.yml. Hidden workflows remain engine-reachable
	// (CLI flags, pickers, DB-persisted refs) but do not appear in TUI menus.
	// Not serialized to YAML — populated by the loader.
	Hidden bool `yaml:"-"`

	// Source records where this workflow definition originated:
	//   "inline"           — defined inline in .sortie.yml
	//   "<path>"           — file path under .sortie/workflows/
	// Not serialized to YAML — populated by the loader.
	Source string `yaml:"-"`

	// FromGlobal marks a workflow whose definition was adopted from the global
	// scope (~/.sortie.yml inline, ~/.sortie/workflows/, or ~/.sortie/tracks/)
	// rather than defined by the project. Global workflows carry their settings
	// along, but locality wins: an explicit project-level on_complete overrides
	// a FromGlobal workflow's OnComplete (see Config.EffectiveOnComplete).
	// Not serialized to YAML — populated by the loader.
	FromGlobal bool `yaml:"-"`
}

// UnmarshalYAML decodes a WorkflowConfig and rejects the legacy `tmux:` field
// with a migration error. The replacement is the inverted `print:` field.
func (wf *WorkflowConfig) UnmarshalYAML(value *yaml.Node) error {
	if err := checkDeprecatedTmuxField(value, "workflow"); err != nil {
		return err
	}
	type raw WorkflowConfig
	var r raw
	if err := value.Decode(&r); err != nil {
		return err
	}
	*wf = WorkflowConfig(r)
	return nil
}

// ValidatePins checks the pinnable New Task fields for internal consistency:
// branch and checkout are mutually exclusive, and branch/checkout/target are
// meaningless (and rejected) when the worktree toggle is pinned off.
func (wf *WorkflowConfig) ValidatePins() error {
	if wf.Branch != "" && wf.Checkout != "" {
		return fmt.Errorf("workflow %q: cannot set both branch and checkout", wf.Name)
	}
	if wf.Worktree != nil && !*wf.Worktree {
		if wf.Branch != "" || wf.Checkout != "" || wf.Target != "" {
			return fmt.Errorf("workflow %q: branch/checkout/target cannot be set when worktree: false", wf.Name)
		}
	}
	return nil
}

// IsFullySpec returns true when every New Task screen field is pinned by this
// workflow, so the screen can be skipped entirely. Title is auto-generated and
// never gates skip.
func (wf *WorkflowConfig) IsFullySpec() bool {
	if wf.Input == "" || wf.Worktree == nil {
		return false
	}
	if !*wf.Worktree {
		return true // git section N/A when worktree is pinned off
	}
	hasBranch := wf.Branch != "" || wf.Checkout != ""
	return hasBranch && wf.Target != ""
}

type StepConfig struct {
	Name string `yaml:"name"`
	// Description is a human-readable, one-line summary of what this step does.
	// Pure metadata, surfaced via MCP (list_workflows) so agents can reason
	// about a workflow's shape; it is never interpolated into prompts.
	Description string `yaml:"description,omitempty"`
	Prompt      string `yaml:"prompt"`
	Mode        string `yaml:"mode"`
	// Print, when non-nil, overrides the workflow-level Print default for this step.
	//   nil (default):    inherit workflow.Print
	//   *false:           tmux
	//   *true:            headless `claude -p`
	// Replaces the previous step-level `tmux` field.
	Print                 *bool       `yaml:"print,omitempty"`
	Timeout               string      `yaml:"timeout"`
	Human                 bool        `yaml:"human"`
	Loop                  *LoopConfig `yaml:"loop,omitempty"`
	SummarizationStrategy string      `yaml:"summarization_strategy,omitempty"`
	// SummarizationPrompt is the prompt template used when summarization_strategy is
	// "summarize_chat". Supports {{task.id}}, {{task.title}}, etc. template variables.
	// When empty, the default summarization prompt is used.
	SummarizationPrompt string `yaml:"summarization_prompt,omitempty"`
	// AllowedSummarizationModels narrows which models the summarizer may pick
	// for this step's `summarize_chat` pass. Same semantics as the project-level
	// field — auto-selection picks the cheapest allowed model that fits the
	// prompt. When empty, the project-level setting is used.
	AllowedSummarizationModels []string `yaml:"allowed_summarization_models,omitempty"`
	// RequireContext, when true, makes a failure to capture this step's
	// `summarize_chat` context BLOCK the task instead of advancing with an
	// empty context. Use it on steps whose output later steps template via
	// {{steps.<name>.context}} (e.g. a grilling step feeding implementing): if
	// the chat transcript can't be loaded or summarized, the task fails loudly
	// rather than silently running the next step with no plan. Only meaningful
	// for tmux steps with summarization_strategy "summarize_chat"; ignored
	// otherwise. Defaults to false (best-effort: warn and proceed).
	RequireContext bool `yaml:"require_context,omitempty"`

	// ref marks a step parsed from a bare-string list entry (e.g. `- planning`).
	// A reference step carries only its Name; resolveWorkflowSteps replaces it
	// with the same-named step from the base (global) workflow during config
	// resolution. Not serialized — populated by the loader and always false on a
	// fully-resolved step. Mirrors WorkflowEntry.Ref at the step level.
	ref bool
}

// UnmarshalYAML decodes a StepConfig. A bare scalar is a step *reference* — the
// named step is pulled from the same-named base (global) workflow during
// resolution (see resolveWorkflowSteps), mirroring how a scalar workflow entry
// references a workflow by name. A mapping is an inline step definition; the
// legacy `tmux:` field is rejected with a migration error.
func (s *StepConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var name string
		if err := value.Decode(&name); err != nil {
			return err
		}
		*s = StepConfig{Name: name, ref: true}
		return nil
	}
	if err := checkDeprecatedTmuxField(value, "step"); err != nil {
		return err
	}
	type raw StepConfig
	var r raw
	if err := value.Decode(&r); err != nil {
		return err
	}
	*s = StepConfig(r)
	return nil
}

// checkDeprecatedTmuxField scans a YAML mapping node for the removed `tmux:`
// field and returns a migration-friendly error pointing at the new `print:`
// field. scope is the noun reported in the error (e.g. "workflow" or "step").
func checkDeprecatedTmuxField(node *yaml.Node, scope string) error {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind != yaml.ScalarNode {
			continue
		}
		if key.Value == "tmux" {
			return fmt.Errorf("%s field `tmux` was removed in favour of `print` (inverted): replace `tmux: true` with `print: false` and `tmux: false` with `print: true`", scope)
		}
	}
	return nil
}

// LoopConfig defines a closed-loop jump back to an earlier step.
type LoopConfig struct {
	Goto          string             `yaml:"goto"`
	MaxIterations int                `yaml:"max_iterations"`
	ExitCondition *LoopExitCondition `yaml:"exit_condition,omitempty"`
}

// LoopExitCondition defines when a loop should exit early.
type LoopExitCondition struct {
	StepContextEmpty string `yaml:"step_context_empty"` // step name whose context to check
}

// ValidateLoops checks all loop configurations in a workflow for correctness.
func (wf *WorkflowConfig) ValidateLoops() error {
	stepIndex := make(map[string]int)
	for i, s := range wf.Steps {
		stepIndex[s.Name] = i
	}

	// Track loop ranges [goto_target, loop_step] to detect overlaps
	type loopRange struct {
		from, to int
		stepName string
	}
	var ranges []loopRange

	for i, step := range wf.Steps {
		if step.Loop == nil {
			continue
		}

		// goto must reference an existing step
		targetIdx, ok := stepIndex[step.Loop.Goto]
		if !ok {
			return fmt.Errorf("step %q: loop goto references unknown step %q", step.Name, step.Loop.Goto)
		}

		// goto must be an earlier step (no forward jumps, no self-reference)
		if targetIdx >= i {
			return fmt.Errorf("step %q: loop goto must reference an earlier step (got %q at index %d, current at %d)", step.Name, step.Loop.Goto, targetIdx, i)
		}

		// max_iterations is required and must be >= 1
		if step.Loop.MaxIterations < 1 {
			return fmt.Errorf("step %q: loop max_iterations must be >= 1", step.Name)
		}

		// A step with loop cannot have human or run inside tmux
		if step.Human {
			return fmt.Errorf("step %q: loop steps cannot have human: true", step.Name)
		}
		if !step.UsePrint(wf.Print) {
			return fmt.Errorf("step %q: loop steps cannot run in tmux (set print: true on the step or workflow)", step.Name)
		}

		// Validate exit condition
		if step.Loop.ExitCondition != nil {
			if step.Loop.ExitCondition.StepContextEmpty != "" {
				if _, ok := stepIndex[step.Loop.ExitCondition.StepContextEmpty]; !ok {
					return fmt.Errorf("step %q: exit_condition step_context_empty references unknown step %q", step.Name, step.Loop.ExitCondition.StepContextEmpty)
				}
			}
		}

		// Check for overlapping loops
		newRange := loopRange{from: targetIdx, to: i, stepName: step.Name}
		for _, existing := range ranges {
			// Overlapping if one range starts inside the other
			if (newRange.from > existing.from && newRange.from < existing.to) ||
				(newRange.to > existing.from && newRange.to < existing.to) ||
				(existing.from > newRange.from && existing.from < newRange.to) {
				return fmt.Errorf("step %q: loop range [%d..%d] overlaps with step %q range [%d..%d]",
					step.Name, newRange.from, newRange.to,
					existing.stepName, existing.from, existing.to)
			}
		}
		ranges = append(ranges, newRange)
	}

	return nil
}

// ValidateOnComplete checks the workflow-level on_complete override (if set) is
// a recognized action. An empty value means "inherit the project-level setting".
func (wf *WorkflowConfig) ValidateOnComplete() error {
	if !validOnCompleteValues[wf.OnComplete] {
		return fmt.Errorf("on_complete: invalid value %q (must be \"commit\", \"merge\", or \"none\")", wf.OnComplete)
	}
	return nil
}

// ValidateSteps checks step-level configurations for correctness.
func (wf *WorkflowConfig) ValidateSteps() error {
	for _, step := range wf.Steps {
		if !ValidSummarizationStrategies[step.SummarizationStrategy] {
			return fmt.Errorf("step %q: invalid summarization_strategy %q (must be %q, %q, or %q)",
				step.Name, step.SummarizationStrategy,
				SummarizationStrategyLastMessage, SummarizationStrategySummarizeChat, SummarizationStrategyNone)
		}
		for _, m := range step.AllowedSummarizationModels {
			if !ValidSummarizationModels[m] {
				return fmt.Errorf("step %q: invalid allowed_summarization_models entry %q (must be one of %q, %q, %q)",
					step.Name, m,
					SummarizationModelHaiku, SummarizationModelSonnet, SummarizationModelOpus)
			}
		}
	}
	return nil
}

// UsePrint returns whether this step should execute via `claude -p` (headless).
// Step-level setting overrides the workflow default. When false (the default), the
// step runs inside a tmux session housing the Claude Code TUI.
func (s *StepConfig) UsePrint(workflowDefault bool) bool {
	if s.Print != nil {
		return *s.Print
	}
	return workflowDefault
}

// UseTmux is the inverse of UsePrint: it returns whether this step runs inside a
// tmux session. Kept as a thin helper because most call sites still care about
// the tmux side of the decision.
func (s *StepConfig) UseTmux(workflowDefault bool) bool {
	return !s.UsePrint(workflowDefault)
}

// FirstStepIsTmux returns true when this workflow has at least one step and the
// first step runs in tmux (i.e. `print` is false at both workflow and step level
// for the first step). Tmux-first workflows may be started without a description
// since the user drives the session interactively.
func (wf *WorkflowConfig) FirstStepIsTmux() bool {
	if wf == nil || len(wf.Steps) == 0 {
		return false
	}
	return wf.Steps[0].UseTmux(wf.Print)
}

const (
	// DefaultStepTimeout is the fallback step timeout when none is configured.
	DefaultStepTimeout = 30 * time.Minute

	// DefaultOutputBufferLines is the default size of the per-agent output ring buffer.
	DefaultOutputBufferLines = 10000

	// SummarizationStrategyLastMessage uses the Claude result event text as step context.
	// Cheap (no extra Claude call) but often misleading — the final assistant message is
	// frequently a one-liner ("Done!") that loses decisions, file changes, and blockers.
	// Not usable for tmux steps because they have no NDJSON result event.
	SummarizationStrategyLastMessage = "last_message"

	// SummarizationStrategySummarizeChat runs a haiku pass over the full chat log and
	// stores the summary as step context. Default for new steps when unset.
	SummarizationStrategySummarizeChat = "summarize_chat"

	// SummarizationStrategyNone disables summarization for the step entirely:
	// no chat summarization pass is run and no step context is captured. Later
	// steps that reference `{{steps.<name>.context}}` will see an empty string.
	// Useful for steps whose output isn't meaningful to later steps (e.g.
	// fire-and-forget actions) and where the cost of a summarization pass is
	// unwanted.
	SummarizationStrategyNone = "none"

	// DefaultSummarizationStrategy is the strategy used when StepConfig.SummarizationStrategy
	// is left empty. See EffectiveSummarizationStrategy().
	DefaultSummarizationStrategy = SummarizationStrategySummarizeChat

	// SummarizationModelHaiku, SummarizationModelSonnet, SummarizationModelOpus
	// are the three Claude model aliases the summarizer auto-selects from.
	// Ordered cheapest → most capable.
	SummarizationModelHaiku  = "haiku"
	SummarizationModelSonnet = "sonnet"
	SummarizationModelOpus   = "opus"
)

// DefaultAllowedSummarizationModels is the allowlist used when neither the
// step nor the project specifies one. All three models are allowed by default;
// the summarizer picks the cheapest one whose prompt-size ceiling fits.
var DefaultAllowedSummarizationModels = []string{
	SummarizationModelHaiku,
	SummarizationModelSonnet,
	SummarizationModelOpus,
}

// ValidSummarizationModels enumerates accepted aliases for the
// allowed_summarization_models config field.
var ValidSummarizationModels = map[string]bool{
	SummarizationModelHaiku:  true,
	SummarizationModelSonnet: true,
	SummarizationModelOpus:   true,
}

// ValidSummarizationStrategies enumerates accepted values for StepConfig.SummarizationStrategy.
var ValidSummarizationStrategies = map[string]bool{
	"":                                 true, // empty means default (DefaultSummarizationStrategy)
	SummarizationStrategyLastMessage:   true,
	SummarizationStrategySummarizeChat: true,
	SummarizationStrategyNone:          true,
}

// EffectiveSummarizationStrategy returns the configured strategy, substituting the
// default when the field is empty. Use this instead of comparing SummarizationStrategy
// directly so the default can evolve without touching every callsite.
func (s *StepConfig) EffectiveSummarizationStrategy() string {
	if s.SummarizationStrategy == "" {
		return DefaultSummarizationStrategy
	}
	return s.SummarizationStrategy
}

// EffectiveAllowedSummarizationModels resolves the allowlist used to pick a
// summarization model for this step. Precedence: step-level >
// project-level > DefaultAllowedSummarizationModels. Returns a copy so callers
// can safely mutate.
func (s *StepConfig) EffectiveAllowedSummarizationModels(projectDefault []string) []string {
	src := s.AllowedSummarizationModels
	if len(src) == 0 {
		src = projectDefault
	}
	if len(src) == 0 {
		src = DefaultAllowedSummarizationModels
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// GlobalConfig from ~/.config/sortie/config.yaml
type GlobalConfig struct {
	MaxWorkers               int                 `yaml:"max_workers"`
	Yolo                     *bool               `yaml:"yolo,omitempty"`
	PollInterval             string              `yaml:"poll_interval,omitempty"`
	Claude                   *ClaudeConfig       `yaml:"claude,omitempty"`
	Verification             *VerificationConfig `yaml:"verification,omitempty"`
	Notifications            NotificationsConfig `yaml:"notifications"`
	TmuxNestedAttachBehavior string              `yaml:"tmux_nested_attach_behavior"`
	Options                  *OptionsConfig      `yaml:"options,omitempty"`
}

type NotificationsConfig struct {
	Enabled        bool `yaml:"enabled"`
	OnComplete     bool `yaml:"on_complete"`
	OnFailed       bool `yaml:"on_failed"`
	OnWaitingInput bool `yaml:"on_waiting_input"`
}

type ClaudeConfig struct {
	Command     string   `yaml:"command"`
	DefaultArgs []string `yaml:"default_args"`
	Yolo        bool     // whether to pass --dangerously-skip-permissions
}

// Args returns the effective argument list, including --dangerously-skip-permissions if Yolo is enabled.
func (c *ClaudeConfig) Args() []string {
	args := append([]string{}, c.DefaultArgs...)
	if c.Yolo {
		args = append(args, "--dangerously-skip-permissions")
	}
	return args
}

// CommandsConfig is used during init for project detection
type CommandsConfig struct {
	Test  string `yaml:"test"`
	Lint  string `yaml:"lint"`
	Build string `yaml:"build"`
}

// Config is the merged runtime config used by all components.
// It combines global config, project config (.sortie.yml), and computed paths.
type Config struct {
	// From .sortie.yml (project config)
	MaxWorkers      int
	DefaultPriority string
	Verification    VerificationConfig
	Git             GitConfig
	// OnComplete is the resolved project-level finalization action
	// ("commit" | "merge" | "none"). Per-workflow overrides live on
	// WorkflowConfig.OnComplete and are resolved at finalization time via
	// EffectiveOnComplete.
	OnComplete string

	// OnCompleteFromProject records that OnComplete was explicitly set by the
	// project-tier .sortie.yml (as opposed to inherited from ~/.sortie.yml or
	// the built-in default). When true, the project's OnComplete beats a
	// FromGlobal workflow's OnComplete in EffectiveOnComplete.
	// Populated by the loader.
	OnCompleteFromProject bool
	Workflows             []WorkflowConfig // flat resolved workflow list

	// System prompt preamble passed via --system-prompt to Claude agents
	SystemPrompt string

	// Paths to sync from project root into new worktrees
	WorktreeSyncPaths WorktreeSyncPathsConfig

	// Command to run after creating a worktree (e.g. dependency installation)
	WorktreeSetupCommand string

	// Commands to run sequentially after creating a worktree
	WorktreeSetupCommands []string

	// Command to run after creating a tmux session (e.g. layout setup)
	TmuxSetupCommand string

	// AllowedSummarizationModels is the project-level allowlist used to
	// auto-pick a summarization model. Empty means
	// DefaultAllowedSummarizationModels. Resolved per-step via
	// StepConfig.EffectiveAllowedSummarizationModels(cfg.AllowedSummarizationModels).
	AllowedSummarizationModels []string

	// From global config
	Notifications            NotificationsConfig
	TmuxNestedAttachBehavior string // "switch" (default) or "nest"

	// TUI display options (from .sortie.yml options section)
	Options OptionsConfig

	// Internal defaults (not in yaml)
	Claude ClaudeConfig

	// Whether a project config (.sortie.yml) was found
	ProjectConfigFound bool

	// Computed paths (project-local)
	ProjectDir   string
	DatabasePath string
	SocketPath   string
	PidFile      string
	PollInterval time.Duration

	// Project holds auto-detected and user-facing project metadata (name,
	// detected build/test/lint commands). Populated by ApplyDetectedProject
	// during Load/LoadForProject. Kept as an anonymous struct (not a named
	// compat type) since it stores real, non-duplicated data — see detect.go
	// and the many `cfg.Project.Name` readers across cmd/, internal/tui,
	// internal/daemon, and internal/workflow.
	Project struct {
		Name       string
		WorkDir    string
		Commands   CommandsConfig
		AutoDetect bool
	}

	// globalPool holds workflows resolved from the global ~/.sortie.yml (both
	// inline and file-based under ~/.sortie/workflows/). Project-level config
	// string refs that don't match a local .sortie/workflows/<name>.yml
	// fall back to this pool, letting projects reuse globally-defined
	// workflows by name. Populated during loadCommon, after the global
	// .sortie.yml is processed. Not serialized.
	globalPool *globalWorkflowPool
}
