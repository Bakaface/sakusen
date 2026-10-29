package config

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// AnimationConfig controls the sakusen (airplane) animation on task submission.
type AnimationConfig struct {
	Enabled  *bool `yaml:"enabled,omitempty"`
	Duration *int  `yaml:"duration,omitempty"` // milliseconds
}

// OptionsConfig holds TUI display options configurable via .sakusen.yml
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

// ProjectConfig is loaded from .sakusen.yml (both global ~/.sakusen.yml and project-local)
type ProjectConfig struct {
	MaxWorkers      int                 `yaml:"max_workers"`
	DefaultPriority string              `yaml:"default_priority"`
	PollInterval    string              `yaml:"poll_interval,omitempty"`
	Verification    *VerificationConfig `yaml:"verification,omitempty"`
	Git             GitConfig           `yaml:"git"`
	// Agents is the project-tier agent registry (slug → record). Merged over
	// the global tier per slug: a slug redefined here wins wholesale.
	Agents map[string]AgentConfig `yaml:"agents,omitempty"`
	// AgentAliases maps stable semantic agent names to target agent or variant
	// slugs (e.g. headless-implementer: claude:opus). Merged over the global
	// tier per key: an alias redefined here re-points the target.
	AgentAliases map[string]string `yaml:"agent_aliases,omitempty"`
	// DefaultAgent names the agent slug steps fall back to when neither the
	// step nor the workflow sets `agent:`. Empty → "claude".
	DefaultAgent string `yaml:"default_agent,omitempty"`
	// MergeConflicts configures the merge-conflict resolver role. See
	// MergeConflictsConfig.
	MergeConflicts *MergeConflictsConfig `yaml:"merge_conflicts,omitempty"`
	// Summarizer configures the utility LLM for summarization/title
	// generation. See SummarizerConfig.
	Summarizer *SummarizerConfig `yaml:"summarizer,omitempty"`
	// OnComplete is the project-level finalization action run after a task's
	// workflow finishes ("commit" | "merge" | "none"). Overridable per-workflow
	// via WorkflowConfig.OnComplete. Moved here from the former git.on_complete.
	OnComplete               string                  `yaml:"on_complete,omitempty"`
	Workflows                []WorkflowEntry         `yaml:"workflows"`
	Notifications            *NotificationsConfig    `yaml:"notifications,omitempty"`
	TmuxNestedAttachBehavior string                  `yaml:"tmux_nested_attach_behavior"`
	WorktreeSyncPaths        WorktreeSyncPathsConfig `yaml:"worktree-sync-paths"`
	WorktreeSetupCommand     string                  `yaml:"worktree-setup-command"`
	WorktreeSetupCommands    []string                `yaml:"worktree-setup-commands"`
	TmuxSetupCommand         string                  `yaml:"tmux-setup-command"`
	Options                  *OptionsConfig          `yaml:"options,omitempty"`
	// Routines are invocation bindings: each names a workflow defined under
	// workflows: and supplies pins plus an optional trigger (cadence or
	// after). Top-level (sibling to workflows:). See RoutineConfig.
	Routines []RoutineConfig `yaml:"routines,omitempty"`
	// Tmux configures tmux session hibernation. See TmuxConfig.
	Tmux *TmuxConfig `yaml:"tmux,omitempty"`
	// Merge configures the clean-target wait of the merge pipeline. See
	// MergeConfig.
	Merge *MergeConfig `yaml:"merge,omitempty"`
}

// MergeConfig is the top-level merge: block governing how a merge waits for
// the target repo's main checkout to be clean. Merged per field across tiers.
type MergeConfig struct {
	// CleanTargetTimeout fails the merge (merge-failed, worktree kept) when
	// the target checkout is still dirty this long after the task entered
	// merge-blocked. Parsed with time.ParseDuration. Empty inherits the lower
	// tier; "0" waits forever.
	CleanTargetTimeout string `yaml:"clean_target_timeout,omitempty"`
	// IgnorePaths are git glob pathspecs, relative to the repo root, whose
	// changes do not count as a dirty target checkout. A non-empty list
	// replaces the lower tier's.
	IgnorePaths []string `yaml:"ignore_paths,omitempty"`
}

// TmuxConfig is the top-level tmux: block governing hibernation of idle
// interactive task sessions. Accepted in every config tier, but read
// daemon-level (from the daemon's own merged Config) exactly like
// max_workers — another project's .sakusen.yml never changes what the
// running daemon does.
type TmuxConfig struct {
	// HibernateAfter is how long a tmux session may sit idle at its agent's
	// prompt before the daemon kills it (the task, worktree and conversation
	// survive; attach resumes it). Parsed with time.ParseDuration. Empty
	// inherits the lower tier; "0" disables idle hibernation.
	HibernateAfter string `yaml:"hibernate_after,omitempty"`
	// MaxLiveSessions is a soft cap on live sakusen tmux task sessions across
	// all projects. When exceeded, the daemon hibernates eligible sessions
	// oldest-idle-first until it is back at the cap. A pointer so an explicit
	// 0 (disable the cap) is distinguishable from "not set"; nil inherits the
	// lower tier.
	MaxLiveSessions *int `yaml:"max_live_sessions,omitempty"`
}

// RoutineConfig is a single entry under the top-level routines: key in
// .sakusen.yml. A routine BINDS an existing workflow to a way of invoking it:
// it never defines steps. A routine declares at most one trigger: Cadence (a
// cron schedule) or After (the name of another routine in the same project;
// this routine fires each time a run of that routine reaches completed).
// Without either it is on-demand only (TUI :RunRoutine, `sakusen routines
// run`, MCP run_routine). Every routine can also be run on demand. One
// workflow may back any number of routines with different pins.
type RoutineConfig struct {
	Name string `yaml:"name"`
	// Description is human-readable metadata surfaced in the routine palette
	// and over MCP. Never a pin, never interpolated into prompts.
	Description string `yaml:"description,omitempty"`

	// Workflow names the workflow this routine runs. Required; resolved by the
	// same exact-name lookup task creation uses, so hidden pool files and
	// "<slug>:<name>" track workflows are reachable.
	Workflow string `yaml:"workflow"`

	// Input, Worktree, Branch, Checkout, Target and Workdir are the New Task
	// pins (see the same-named WorkflowConfig fields). A pin set here
	// overrides the referenced workflow's own; branch and checkout override as
	// a PAIR, so setting either one ignores both of the workflow's. See
	// EffectivePins.
	Input    string `yaml:"input,omitempty"`
	Worktree *bool  `yaml:"worktree,omitempty"`
	Branch   string `yaml:"branch,omitempty"`
	Checkout string `yaml:"checkout,omitempty"`
	Target   string `yaml:"target,omitempty"`
	Workdir  string `yaml:"workdir,omitempty"`

	// Cadence is a standard 5-field cron expression, a descriptor (@daily,
	// @hourly, ...) or the @every shorthand. Mutually exclusive with After.
	// With neither set the routine is on-demand only: it is still registered
	// (and keeps its run history) but never fires by itself.
	Cadence string `yaml:"cadence,omitempty"`

	// After names another routine in the same project: this routine fires
	// each time a run of that routine (scheduled, triggered or on-demand)
	// reaches completed. Mutually exclusive with Cadence.
	After string `yaml:"after,omitempty"`

	// Priority for tasks this routine fires. Empty falls back to the project
	// default at fire time (resolved in createTaskFromRequest, not at
	// reconcile, so a changed default takes effect without a config touch).
	Priority string `yaml:"priority,omitempty"`

	// Paused, when true, keeps the routine registered but prevents automatic
	// (cadence or after) fires. Only meaningful with a trigger.
	Paused bool `yaml:"paused,omitempty"`

	// Notifications overrides the per-event notification switches for tasks
	// fired by this routine. It is the most specific tier (see
	// Config.EffectiveNotifications), above the bound workflow's own.
	Notifications *NotificationOverrides `yaml:"notifications,omitempty"`
}

// UnmarshalYAML decodes a RoutineConfig and rejects workflow-DEFINITION fields:
// a routine is a binding, so steps (and their agent / summarizer_prompt
// settings, and its exclusive_workdir lock) belong on the workflow it
// references. The removed `tmux:`/`print:`
// fields get the same migration errors as workflows.
func (r *RoutineConfig) UnmarshalYAML(value *yaml.Node) error {
	for _, key := range []string{"steps", "agent", "summarizer_prompt", "exclusive_workdir"} {
		if err := checkRemovedMappingKey(value, key, fmt.Sprintf(
			"routine field `%s` is not allowed: routines are bindings — define steps under `workflows:` and reference them with `workflow:`", key)); err != nil {
			return err
		}
	}
	if err := checkRemovedModeFields(value, "routine"); err != nil {
		return err
	}
	type raw RoutineConfig
	var rr raw
	if err := value.Decode(&rr); err != nil {
		return err
	}
	*r = RoutineConfig(rr)
	return nil
}

// IsScheduled reports whether the routine carries a cadence (and so fires on a
// schedule).
func (r *RoutineConfig) IsScheduled() bool { return r.Cadence != "" }

// IsTriggered reports whether the routine fires by itself — on a cadence or
// after another routine completes. Untriggered routines are on-demand only.
func (r *RoutineConfig) IsTriggered() bool { return r.Cadence != "" || r.After != "" }

// EffectivePins merges the routine's pins over the referenced workflow's,
// returning a WorkflowConfig-shaped value (named after the routine) that the
// fire path and the pin validators can consume directly.
//
// Input, Worktree, Target and Workdir merge per field. Branch and Checkout merge as a
// PAIR — setting either on the routine ignores both of the workflow's — which
// mirrors how an explicit request value displaces a workflow pin in
// createTaskFromRequest.
func (r *RoutineConfig) EffectivePins(wf *WorkflowConfig) WorkflowConfig {
	pins := WorkflowConfig{
		Name:     r.Name,
		Input:    r.Input,
		Worktree: r.Worktree,
		Branch:   r.Branch,
		Checkout: r.Checkout,
		Target:   r.Target,
		Workdir:  r.Workdir,
	}
	if wf == nil {
		return pins
	}
	if pins.Input == "" {
		pins.Input = wf.Input
	}
	if pins.Worktree == nil {
		pins.Worktree = wf.Worktree
	}
	if pins.Branch == "" && pins.Checkout == "" {
		pins.Branch = wf.Branch
		pins.Checkout = wf.Checkout
	}
	if pins.Target == "" {
		pins.Target = wf.Target
	}
	if pins.Workdir == "" {
		pins.Workdir = wf.Workdir
	}
	return pins
}

// ValidatePins checks the routine's effective pins for the same internal
// consistency WorkflowConfig.ValidatePins enforces, reporting against the
// routine's name.
func (r *RoutineConfig) ValidatePins(wf *WorkflowConfig) error {
	pins := r.EffectivePins(wf)
	if err := ValidateWorkdir(pins.Workdir); err != nil {
		return fmt.Errorf("routine %q: %w", r.Name, err)
	}
	return validatePinFields("routine", r.Name, pins.Worktree, pins.Branch, pins.Checkout, pins.Target)
}

// WorkflowEntry is a single item in the flat workflows: list. It is either
// a string referencing a workflow (a file under .sakusen/workflows/ or a
// workflow defined in the global ~/.sakusen.yml) or an inline WorkflowConfig
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
	"agent":                   true,
	"on_complete":             true,
	"steps":                   true,
	"summarizer_prompt":       true,
	"worktree-sync-paths":     true,
	"worktree-setup-command":  true,
	"worktree-setup-commands": true,
	"tmux-setup-command":      true,
	"workdir":                 true,
	"notifications":           true,
	"tmux":                    true, // removed — routed to the migration error
	"print":                   true, // removed — routed to the migration error
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
	// VerifyCommand runs via the shell in the task worktree root before an
	// on_complete: merge (and again after a rebase that moved the tip); a
	// failure leaves the task merge-failed. Empty disables verification.
	VerifyCommand string `yaml:"verify_command,omitempty"`
	// VerifyTimeout bounds one VerifyCommand run. Parsed with
	// time.ParseDuration; empty inherits the lower tier (default 15m), "0"
	// disables the limit. The resolved value is Config.GitVerifyTimeout.
	VerifyTimeout string `yaml:"verify_timeout,omitempty"`
	// VerifyParentMerges, when false, skips VerifyCommand for merges that
	// land on a parent task's branch (fanout `merge: parent` children and
	// create_tasks_and_wait `merge_into_parent` children); the parent's own
	// merge into the base branch is always verified. Nil inherits the lower
	// tier (default true). The resolved value is Config.GitVerifyParentMerges.
	VerifyParentMerges *bool `yaml:"verify_parent_merges,omitempty"`
	// CommitMessageCommand runs via bash in the task worktree when the daemon
	// commits it, with the staged diff on stdin; the first non-empty stdout
	// line becomes the commit message. Empty keeps the title-derived message.
	CommitMessageCommand string `yaml:"commit_message_command,omitempty"`
	// CommitMessageTimeout bounds one CommitMessageCommand run. Parsed with
	// time.ParseDuration; empty inherits the lower tier (default 2m) and the
	// value must be positive. The resolved value is
	// Config.GitCommitMessageTimeout.
	CommitMessageTimeout string `yaml:"commit_message_timeout,omitempty"`
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

	// The following six fields pre-pin New Task screen fields. When set, the
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
	//   Workdir pins the agent start directory (task.Workdir), relative to the
	//     task's checkout.
	//
	// Branch and Checkout are mutually exclusive. Setting branch/checkout/target
	// while Worktree is pinned false is a config error (the git section is hidden).
	Input    string `yaml:"input,omitempty"`
	Worktree *bool  `yaml:"worktree,omitempty"`
	Branch   string `yaml:"branch,omitempty"`
	Checkout string `yaml:"checkout,omitempty"`
	Target   string `yaml:"target,omitempty"`
	Workdir  string `yaml:"workdir,omitempty"`

	// ExclusiveWorkdir opts worktree:false tasks of this workflow into the
	// working-tree lock: such a task stays pending while any other active
	// worktree:false task of the same project occupies the project root. The
	// lock is one-sided (holders need not opt in) and a no-op for tasks that
	// run in their own worktree.
	ExclusiveWorkdir bool `yaml:"exclusive_workdir,omitempty"`

	// Agent, when non-empty, is the agent slug every step of this workflow
	// runs with unless the step sets its own `agent:`. Resolution cascade:
	// step.agent → workflow.agent → top-level default_agent → "claude" (see
	// Config.StepAgent). The agent's mode (headless vs tmux) decides how the
	// step executes — mode lives on the agent record, not the step.
	Agent string `yaml:"agent,omitempty"`
	// OnComplete, when non-empty, overrides the project-level on_complete action
	// for tasks running this workflow ("commit" | "merge" | "none"). Empty means
	// inherit the project-level setting. An explicit value always wins over the
	// top-level setting, whatever scope the workflow was defined in (see
	// Config.EffectiveOnComplete).
	OnComplete            string                  `yaml:"on_complete,omitempty"`
	Steps                 []StepConfig            `yaml:"steps"`
	SummarizerPrompt      string                  `yaml:"summarizer_prompt"`
	WorktreeSyncPaths     WorktreeSyncPathsConfig `yaml:"worktree-sync-paths,omitempty"`
	WorktreeSetupCommand  string                  `yaml:"worktree-setup-command,omitempty"`
	WorktreeSetupCommands []string                `yaml:"worktree-setup-commands,omitempty"`
	TmuxSetupCommand      string                  `yaml:"tmux-setup-command,omitempty"`

	// Notifications overrides the per-event notification switches for tasks
	// running this workflow (see Config.EffectiveNotifications).
	Notifications *NotificationOverrides `yaml:"notifications,omitempty"`

	// Hidden marks workflows loaded from .sakusen/workflows/ that are NOT
	// referenced from .sakusen.yml. Hidden workflows remain engine-reachable
	// (CLI flags, pickers, DB-persisted refs) but do not appear in TUI menus.
	// Not serialized to YAML — populated by the loader.
	Hidden bool `yaml:"-"`

	// Source records where this workflow definition originated:
	//   "inline"           — defined inline in .sakusen.yml
	//   "<path>"           — file path under .sakusen/workflows/
	// Not serialized to YAML — populated by the loader.
	Source string `yaml:"-"`

	// FromGlobal marks a workflow whose definition was adopted from the global
	// scope (~/.sakusen.yml inline, ~/.sakusen/workflows/, or ~/.sakusen/tracks/)
	// rather than defined by the project. Global workflows carry their settings
	// along unchanged; the flag records provenance for diagnostics.
	// Not serialized to YAML — populated by the loader.
	FromGlobal bool `yaml:"-"`
}

// UnmarshalYAML decodes a WorkflowConfig and rejects the removed `tmux:` and
// `print:` fields with migration errors. The replacement is the agent's mode
// (see the `agent:` field).
func (wf *WorkflowConfig) UnmarshalYAML(value *yaml.Node) error {
	if err := checkRemovedModeFields(value, "workflow"); err != nil {
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
// meaningless (and rejected) when the worktree toggle is pinned off, and
// workdir must be a local relative path.
func (wf *WorkflowConfig) ValidatePins() error {
	if err := ValidateWorkdir(wf.Workdir); err != nil {
		return fmt.Errorf("workflow %q: %w", wf.Name, err)
	}
	return validatePinFields("workflow", wf.Name, wf.Worktree, wf.Branch, wf.Checkout, wf.Target)
}

// validatePinFields holds the pin consistency rules shared by workflows and
// routines; kind/name only shape the error message.
func validatePinFields(kind, name string, worktree *bool, branch, checkout, target string) error {
	if branch != "" && checkout != "" {
		return fmt.Errorf("%s %q: cannot set both branch and checkout", kind, name)
	}
	if worktree != nil && !*worktree {
		if branch != "" || checkout != "" || target != "" {
			return fmt.Errorf("%s %q: branch/checkout/target cannot be set when worktree: false", kind, name)
		}
	}
	return nil
}

// CleanWorkdir normalizes a task workdir; "." and "" both mean the checkout root
// and return "".
func CleanWorkdir(workdir string) string {
	if workdir == "" {
		return ""
	}
	c := filepath.Clean(workdir)
	if c == "." {
		return ""
	}
	return c
}

// ValidateWorkdir rejects a workdir that is absolute or escapes the checkout.
func ValidateWorkdir(workdir string) error {
	c := CleanWorkdir(workdir)
	if c != "" && !filepath.IsLocal(c) {
		return fmt.Errorf("workdir %q must be a relative path inside the checkout", workdir)
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
	// Agent, when non-empty, selects the agent record this step runs with,
	// overriding the workflow-level `agent:` and the top-level default_agent.
	// The agent's mode (headless vs tmux) decides how the step executes.
	Agent   string `yaml:"agent,omitempty"`
	Timeout string `yaml:"timeout"`
	Human   bool   `yaml:"human"`
	// Advance controls how a tmux step advances. "auto" (the default when
	// empty) advances on the step-done sentinel; "explicit" ignores sentinels
	// for advancement (they still drive session capture and at-prompt
	// tracking), so the step advances only via advance_task or the user.
	// Only valid on tmux-mode steps; not a human gate.
	Advance               string      `yaml:"advance,omitempty"`
	Loop                  *LoopConfig `yaml:"loop,omitempty"`
	SummarizationStrategy string      `yaml:"summarization_strategy,omitempty"`
	// SummarizationPrompt is the prompt template used when summarization_strategy is
	// "summarize_chat". Supports {{task.id}}, {{task.title}}, etc. template variables.
	// When empty, the default summarization prompt is used.
	SummarizationPrompt string `yaml:"summarization_prompt,omitempty"`
	// RequireContext, when true, makes a failure to capture this step's
	// `summarize_chat` context BLOCK the task instead of advancing with an
	// empty context. Use it on steps whose output later steps template via
	// {{steps.<name>.context}} (e.g. a grilling step feeding implementing): if
	// the chat transcript can't be loaded or summarized, the task fails loudly
	// rather than silently running the next step with no plan. Only meaningful
	// for tmux steps with summarization_strategy "summarize_chat"; ignored
	// otherwise. Defaults to false (best-effort: warn and proceed).
	RequireContext bool `yaml:"require_context,omitempty"`

	// Parallel, when non-nil, makes this entry a PARALLEL GROUP rather than an
	// ordinary step: it carries no prompt of its own and instead fans out to
	// the branches under `parallel.branches`, which run concurrently as
	// headless agents on the same worktree. The group still occupies exactly
	// one cursor slot (t.StepIndex) and publishes one aggregate step context
	// (see workflow.FormatParallelAggregate) under its own name. Branch names
	// share the workflow's step namespace, so `{{steps.<branch>.context}}`
	// works too. See ValidateSteps for the mutually-exclusive field rules.
	Parallel *ParallelConfig `yaml:"parallel,omitempty"`

	// Fanout, when non-nil, makes this entry a FANOUT step: it runs no agent
	// and instead spawns one child task per item published by an earlier
	// step. See FanoutConfig and validateFanoutStep.
	Fanout *FanoutConfig `yaml:"fanout,omitempty"`

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
// removed `tmux:`, `print:`, and `allowed_summarization_models:` fields are
// rejected with migration errors.
func (s *StepConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var name string
		if err := value.Decode(&name); err != nil {
			return err
		}
		*s = StepConfig{Name: name, ref: true}
		return nil
	}
	if err := checkRemovedModeFields(value, "step"); err != nil {
		return err
	}
	if err := checkRemovedMappingKey(value, "allowed_summarization_models",
		"step field `allowed_summarization_models` was removed: summarization now runs the top-level `summarizer:` command; pick the model inside that command"); err != nil {
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

// checkRemovedModeFields scans a YAML mapping node for the removed `tmux:` and
// `print:` fields and returns a migration-friendly error pointing at the agent
// mechanism. scope is the noun reported in the error ("workflow" or "step").
func checkRemovedModeFields(node *yaml.Node, scope string) error {
	if err := checkRemovedMappingKey(node, "tmux",
		fmt.Sprintf("%s field `tmux` was removed: execution mode now comes from the agent record — set `agent: <slug>` where the agent's `mode` is \"tmux\" or \"headless\"", scope)); err != nil {
		return err
	}
	return checkRemovedMappingKey(node, "print",
		fmt.Sprintf("%s field `print` was removed: execution mode now comes from the agent record — set `agent: <slug>` where the agent's `mode` is \"headless\" (print) or \"tmux\"", scope))
}

// checkKnownMappingKeys rejects any top-level key of a mapping node that is
// not in known.
func checkKnownMappingKeys(node *yaml.Node, scope string, known ...string) error {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind == yaml.ScalarNode && !slices.Contains(known, key.Value) {
			return fmt.Errorf("%s: unknown field %q (valid: %s)", scope, key.Value, strings.Join(known, ", "))
		}
	}
	return nil
}

// checkRemovedMappingKey returns errMsg as an error when the given mapping
// node contains key at its top level.
func checkRemovedMappingKey(node *yaml.Node, keyName, errMsg string) error {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind == yaml.ScalarNode && key.Value == keyName {
			return fmt.Errorf("%s", errMsg)
		}
	}
	return nil
}

// LoopConfig defines a closed-loop jump back to an earlier step.
type LoopConfig struct {
	Goto          string `yaml:"goto"`
	MaxIterations int    `yaml:"max_iterations"`
	// OnMaxIterations decides what happens when the loop reaches
	// MaxIterations: LoopOnMaxIterationsContinue (default) falls through to
	// the next step; LoopOnMaxIterationsFail still evaluates ExitCondition on
	// that pass and fails the task when it is not met.
	OnMaxIterations string             `yaml:"on_max_iterations,omitempty"`
	ExitCondition   *LoopExitCondition `yaml:"exit_condition,omitempty"`
}

// Values of LoopConfig.OnMaxIterations.
const (
	LoopOnMaxIterationsContinue = "continue"
	LoopOnMaxIterationsFail     = "fail"
)

// LoopExitCondition defines when a loop should exit early. Any combination of
// the forms may be set; the loop exits as soon as one matches.
//
// step_context_empty exits on an ABSENCE, so it cannot distinguish "the step
// decided the work is done" from "the step crashed, timed out, or forgot to
// publish" — every one of those silently ends the loop and ships whatever is
// on the branch. Prefer an affirmative form: step_context_contains requires the
// step to make a positive statement to end the loop (a step that fails to run
// leaves the marker absent and the loop keeps going, bounded by
// max_iterations), and command lets a program make the decision.
//
// command runs via `bash -c` in the task's agent workdir with the step env
// contract plus SAKUSEN_LOOP_ITERATION, stdin fed from StdinFrom's context.
// Exit 0 ends the loop, exit 1 continues it, and any other status, a timeout
// or a failure to start fails the task — an uncertain decision must never
// silently end or continue the loop. The tail of the command's combined
// stdout+stderr is written to the task log on every outcome.
type LoopExitCondition struct {
	StepContextEmpty string `yaml:"step_context_empty"` // step name whose context must be empty

	// StepContextContains names the step whose context is searched for Marker.
	StepContextContains string `yaml:"step_context_contains"`
	// Marker is the literal substring that ends the loop. Required whenever
	// StepContextContains is set; matched case-sensitively, not as a regexp.
	Marker string `yaml:"marker"`

	// Command is a shell string whose exit status decides the loop.
	Command string `yaml:"command,omitempty"`
	// StdinFrom names the step whose context is piped to Command's stdin
	// (empty stdin when unset). Only valid with Command.
	StdinFrom string `yaml:"stdin_from,omitempty"`
	// Timeout bounds one Command run as a Go duration; defaults to
	// DefaultLoopExitCommandTimeout. Only valid with Command.
	Timeout string `yaml:"timeout,omitempty"`
}

// DefaultLoopExitCommandTimeout bounds an exit_condition command when its
// timeout is unset.
const DefaultLoopExitCommandTimeout = 2 * time.Minute

// EffectiveTimeout returns the command's timeout, or the default when unset.
// Parseability is validated at load.
func (ec *LoopExitCondition) EffectiveTimeout() time.Duration {
	if ec.Timeout == "" {
		return DefaultLoopExitCommandTimeout
	}
	d, err := time.ParseDuration(ec.Timeout)
	if err != nil || d <= 0 {
		return DefaultLoopExitCommandTimeout
	}
	return d
}

// ParallelConfig is the body of a `parallel:` group step: a join policy plus
// the branches that run concurrently under it. Branches reuse StepConfig so a
// branch supports the same subset of step fields an ordinary headless step
// does (name, description, agent, prompt, timeout, summarization_*); the rest
// are rejected by ValidateSteps.
//
// In a shared group, branches share the task's single worktree and run at the
// same time, so they are required (by documentation, not enforcement) to be
// read-only: two branches writing the same files would race.
type ParallelConfig struct {
	// Require is the join policy: "" / "all" (every branch must succeed;
	// the first failure cancels its siblings), "any" (one is enough), or a
	// positive integer in 1..len(branches).
	Require string `yaml:"require,omitempty"`
	// Isolation is "" / "shared" (branches run in the task's worktree) or
	// "worktree" (each branch gets its own throwaway worktree).
	Isolation string       `yaml:"isolation,omitempty"`
	Branches  []StepConfig `yaml:"branches"`
}

// RequireAll / RequireAny are the two symbolic `require:` values.
const (
	RequireAll = "all"
	RequireAny = "any"
)

// IsolationShared / IsolationWorktree are the valid `isolation:` values.
const (
	IsolationShared   = "shared"
	IsolationWorktree = "worktree"
)

// IsIsolated reports whether each branch runs in its own worktree.
func (p *ParallelConfig) IsIsolated() bool {
	return p != nil && p.Isolation == IsolationWorktree
}

// EffectiveRequire returns the configured join policy with the empty value
// resolved to its default ("all"). Use it for display; use RequiredCount for
// the actual arithmetic.
func (p *ParallelConfig) EffectiveRequire() string {
	if p == nil || p.Require == "" {
		return RequireAll
	}
	return p.Require
}

// RequiredCount returns how many branches must complete for the group to
// succeed. Validation guarantees Require is one of "", "all", "any", or an
// integer in range, so an unparseable value here can only mean the config
// bypassed validation — it degrades to "all".
func (p *ParallelConfig) RequiredCount() int {
	if p == nil {
		return 0
	}
	return RequiredCount(p.Require, len(p.Branches))
}

// RequiredCount maps a `require:` join policy over n members to how many must
// complete: "" / "all" → n, "any" → 1, an integer → itself. An unparseable
// value can only mean validation was bypassed, so it degrades to "all".
func RequiredCount(require string, n int) int {
	switch require {
	case "", RequireAll:
		return n
	case RequireAny:
		return 1
	}
	if v, err := strconv.Atoi(require); err == nil {
		return v
	}
	return n
}

// EffectiveBranch returns branch i with the group-level fallbacks applied: the
// agent and timeout fall back to the group's, and an unset summarization
// strategy resolves to last_message (NOT the global step default — a branch's
// result text is the artifact the aggregate is built from, and running every
// branch chat through the summarizer would rewrite it).
//
// This is the SINGLE normalization point for branch defaults: the engine, the
// validators, the daemon summaries and the TUI all go through it, and the
// parsed config is never rewritten in place.
func (p *ParallelConfig) EffectiveBranch(i int, group *StepConfig) StepConfig {
	if p == nil || i < 0 || i >= len(p.Branches) {
		return StepConfig{}
	}
	b := p.Branches[i]
	if group != nil {
		if b.Agent == "" {
			b.Agent = group.Agent
		}
		if b.Timeout == "" {
			b.Timeout = group.Timeout
		}
	}
	if b.SummarizationStrategy == "" {
		b.SummarizationStrategy = SummarizationStrategyLastMessage
	}
	return b
}

// EffectiveBranches returns every branch of the group with EffectiveBranch
// applied, in config order.
func (s *StepConfig) EffectiveBranches() []StepConfig {
	if !s.IsParallel() {
		return nil
	}
	out := make([]StepConfig, 0, len(s.Parallel.Branches))
	for i := range s.Parallel.Branches {
		out = append(out, s.Parallel.EffectiveBranch(i, s))
	}
	return out
}

// FanoutConfig is the body of a `fanout:` step: instead of running an agent,
// the step spawns child tasks, runs each through a workflow, and joins on them
// under the `require` policy. Exactly one item source is set: `from:` fans out
// the items an earlier step published at run time, while `nodes:` declares a
// static graph of children ordered by their `needs:`, for which the step-level
// `workflow` and `retries` are per-node defaults.
type FanoutConfig struct {
	// From names the earlier top-level step whose published items are fanned out.
	From string `yaml:"from,omitempty"`
	// Nodes is the declared child graph; mutually exclusive with From.
	Nodes []FanoutNodeConfig `yaml:"nodes,omitempty"`
	// Workflow is the workflow each child task runs; its steps must be headless.
	Workflow string `yaml:"workflow,omitempty"`
	// Input is the child task's input template; empty → DefaultFanoutInput.
	// Only valid with From: each node carries its own input.
	Input string `yaml:"input,omitempty"`
	// Merge is where a child's branch lands: "parent" (the fanning-out
	// task's branch) or "base" (the default).
	Merge string `yaml:"merge,omitempty"`
	// Require is the join policy: "" / "all", "any", or an integer >= 1.
	Require string `yaml:"require,omitempty"`
	// Retries is how many times a failed child is retried before it counts
	// as failed.
	Retries int `yaml:"retries,omitempty"`
}

// FanoutNodeConfig is one declared child of a `nodes:` fanout. Its name joins
// the workflow's step namespace, so {{steps.<name>.context}} addresses it.
type FanoutNodeConfig struct {
	Name string `yaml:"name"`
	// Workflow overrides the fanout step's workflow for this node.
	Workflow string `yaml:"workflow,omitempty"`
	// Input is the child task's input template.
	Input string `yaml:"input"`
	// Needs lists nodes of the same fanout that must complete first.
	Needs []string `yaml:"needs,omitempty"`
	// Retries overrides the fanout step's retries for this node.
	Retries *int `yaml:"retries,omitempty"`
}

// Fanout merge targets (FanoutConfig.Merge).
const (
	FanoutMergeParent = "parent"
	FanoutMergeBase   = "base"
)

// DefaultFanoutInput is the child input template when `input:` is unset.
const DefaultFanoutInput = "{{item.input}}"

// MaxFanoutDepth bounds nested fanouts. A task's depth is the number of
// fanout_parent_id hops above it.
const MaxFanoutDepth = 3

// EffectiveInput returns the child input template, defaulting to DefaultFanoutInput.
func (f *FanoutConfig) EffectiveInput() string {
	if f == nil || f.Input == "" {
		return DefaultFanoutInput
	}
	return f.Input
}

// EffectiveMerge returns the merge target, defaulting to FanoutMergeBase.
func (f *FanoutConfig) EffectiveMerge() string {
	if f == nil || f.Merge == "" {
		return FanoutMergeBase
	}
	return f.Merge
}

// EffectiveRequire returns the join policy, defaulting to RequireAll.
func (f *FanoutConfig) EffectiveRequire() string {
	if f == nil || f.Require == "" {
		return RequireAll
	}
	return f.Require
}

// IsNodes reports whether the fanout declares a static `nodes:` graph.
func (f *FanoutConfig) IsNodes() bool {
	return f != nil && len(f.Nodes) > 0
}

// EffectiveNodeWorkflow returns node i's workflow, falling back to the step's.
func (f *FanoutConfig) EffectiveNodeWorkflow(i int) string {
	if w := f.Nodes[i].Workflow; w != "" {
		return w
	}
	return f.Workflow
}

// EffectiveNodeRetries returns node i's retries, falling back to the step's.
func (f *FanoutConfig) EffectiveNodeRetries(i int) int {
	if r := f.Nodes[i].Retries; r != nil {
		return *r
	}
	return f.Retries
}

// NodeTopoOrder returns the indices of Nodes in dependency order, breaking
// ties by declaration order. Nodes on a cycle or behind an unknown need are
// omitted; validation rejects both.
func (f *FanoutConfig) NodeTopoOrder() []int {
	if f == nil {
		return nil
	}
	index := make(map[string]int, len(f.Nodes))
	for i, n := range f.Nodes {
		index[n.Name] = i
	}
	pending := make([]int, len(f.Nodes))
	dependents := make([][]int, len(f.Nodes))
	for i, n := range f.Nodes {
		for _, need := range n.Needs {
			j, ok := index[need]
			if !ok {
				pending[i] = -1
				break
			}
			pending[i]++
			dependents[j] = append(dependents[j], i)
		}
	}
	done := make([]bool, len(f.Nodes))
	out := make([]int, 0, len(f.Nodes))
	for {
		next := -1
		for i := range f.Nodes {
			if !done[i] && pending[i] == 0 {
				next = i
				break
			}
		}
		if next < 0 {
			return out
		}
		done[next] = true
		out = append(out, next)
		for _, d := range dependents[next] {
			if pending[d] > 0 {
				pending[d]--
			}
		}
	}
}

// RequiredCount returns how many of n children must complete.
func (f *FanoutConfig) RequiredCount(n int) int {
	if f == nil {
		return n
	}
	return RequiredCount(f.Require, n)
}

// Step advance modes (StepConfig.Advance).
const (
	AdvanceAuto     = "auto"
	AdvanceExplicit = "explicit"
)

// ExplicitAdvance reports whether the step advances only on an explicit
// advance_task / user action, never on its step-done sentinel.
func (s *StepConfig) ExplicitAdvance() bool {
	return s.Advance == AdvanceExplicit
}

// IsParallel reports whether this step entry is a parallel group.
func (s *StepConfig) IsParallel() bool {
	return s != nil && s.Parallel != nil
}

// IsFanout reports whether this step entry is a fanout step.
func (s *StepConfig) IsFanout() bool {
	return s != nil && s.Fanout != nil
}

// AllStepNames returns every name in the workflow's step namespace: each
// top-level step in order, with a group immediately followed by its branch
// names and a fanout by its node names. Used wherever "all names a {{steps.<name>.context}} ref could
// address" is needed (the engine's step-context snapshot, validation).
func (wf *WorkflowConfig) AllStepNames() []string {
	var out []string
	for i := range wf.Steps {
		step := &wf.Steps[i]
		out = append(out, step.Name)
		if step.IsFanout() {
			for _, n := range step.Fanout.Nodes {
				out = append(out, n.Name)
			}
		}
		if !step.IsParallel() {
			continue
		}
		for _, b := range step.Parallel.Branches {
			out = append(out, b.Name)
		}
	}
	return out
}

// BranchGroup locates the parallel group that owns the named branch. ok is
// false when name is not a branch of any group in this workflow (it may still
// be an ordinary step or a group name).
func (wf *WorkflowConfig) BranchGroup(name string) (group *StepConfig, groupIdx int, ok bool) {
	for i := range wf.Steps {
		step := &wf.Steps[i]
		if !step.IsParallel() {
			continue
		}
		for _, b := range step.Parallel.Branches {
			if b.Name == name {
				return step, i, true
			}
		}
	}
	return nil, -1, false
}

// ValidateLoops checks all loop configurations in a workflow for correctness.
func (wf *WorkflowConfig) ValidateLoops() error {
	stepIndex := make(map[string]int)
	for i, s := range wf.Steps {
		stepIndex[s.Name] = i
	}
	// Branches are addressable by exit conditions (they publish their own step
	// context) but not by goto: a group is one cursor slot, so a loop can only
	// jump to the group itself.
	branchOwner := make(map[string]string)
	for i := range wf.Steps {
		step := &wf.Steps[i]
		if !step.IsParallel() {
			continue
		}
		for _, b := range step.Parallel.Branches {
			branchOwner[b.Name] = step.Name
		}
	}
	knownContextStep := func(name string) bool {
		if _, ok := stepIndex[name]; ok {
			return true
		}
		_, ok := branchOwner[name]
		return ok
	}

	// Track loop ranges [goto_target, loop_step] to detect overlaps
	type loopRange struct {
		from, to int
		stepName string
	}
	var ranges []loopRange

	for i, step := range wf.Steps {
		// A loop on a fanout step is rejected by ValidateSteps.
		if step.Loop == nil || step.IsFanout() {
			continue
		}

		// goto must reference an existing step
		targetIdx, ok := stepIndex[step.Loop.Goto]
		if !ok {
			if group, isBranch := branchOwner[step.Loop.Goto]; isBranch {
				return fmt.Errorf("step %q: loop goto %q targets a branch of parallel group %q; target the group", step.Name, step.Loop.Goto, group)
			}
			return fmt.Errorf("step %q: loop goto references unknown step %q", step.Name, step.Loop.Goto)
		}

		// goto must be an earlier step or the step itself (no forward jumps)
		if targetIdx > i {
			return fmt.Errorf("step %q: loop goto must reference an earlier step or itself (got %q at index %d, current at %d)", step.Name, step.Loop.Goto, targetIdx, i)
		}

		// Re-running a fanout would spawn its children a second time.
		for k := targetIdx; k <= i; k++ {
			if wf.Steps[k].IsFanout() {
				return fmt.Errorf("step %q: loop range contains fanout step %q (a fanout cannot sit inside a loop)", step.Name, wf.Steps[k].Name)
			}
		}

		// max_iterations is required and must be >= 1
		if step.Loop.MaxIterations < 1 {
			return fmt.Errorf("step %q: loop max_iterations must be >= 1", step.Name)
		}

		switch step.Loop.OnMaxIterations {
		case "", LoopOnMaxIterationsContinue:
		case LoopOnMaxIterationsFail:
			if step.Loop.ExitCondition == nil {
				return fmt.Errorf("step %q: loop on_max_iterations %q requires an exit_condition", step.Name, LoopOnMaxIterationsFail)
			}
		default:
			return fmt.Errorf("step %q: loop on_max_iterations must be %q or %q (got %q)", step.Name, LoopOnMaxIterationsFail, LoopOnMaxIterationsContinue, step.Loop.OnMaxIterations)
		}

		// A step with loop cannot have human (the tmux-mode constraint is
		// enforced against the resolved agent in resolveAndValidateAgents,
		// which runs once all config tiers are merged).
		if step.Human {
			return fmt.Errorf("step %q: loop steps cannot have human: true", step.Name)
		}

		// Validate exit condition
		if ec := step.Loop.ExitCondition; ec != nil {
			if ec.StepContextEmpty != "" {
				if !knownContextStep(ec.StepContextEmpty) {
					return fmt.Errorf("step %q: exit_condition step_context_empty references unknown step %q", step.Name, ec.StepContextEmpty)
				}
			}
			if ec.StepContextContains != "" {
				if !knownContextStep(ec.StepContextContains) {
					return fmt.Errorf("step %q: exit_condition step_context_contains references unknown step %q", step.Name, ec.StepContextContains)
				}
				if strings.TrimSpace(ec.Marker) == "" {
					return fmt.Errorf("step %q: exit_condition step_context_contains requires a non-empty marker", step.Name)
				}
			}
			if ec.Marker != "" && ec.StepContextContains == "" {
				return fmt.Errorf("step %q: exit_condition marker requires step_context_contains", step.Name)
			}
			if ec.Command != "" && strings.TrimSpace(ec.Command) == "" {
				return fmt.Errorf("step %q: exit_condition command must not be blank", step.Name)
			}
			if ec.Command == "" {
				if ec.StdinFrom != "" {
					return fmt.Errorf("step %q: exit_condition stdin_from requires command", step.Name)
				}
				if ec.Timeout != "" {
					return fmt.Errorf("step %q: exit_condition timeout requires command", step.Name)
				}
			}
			if ec.StdinFrom != "" && !knownContextStep(ec.StdinFrom) {
				return fmt.Errorf("step %q: exit_condition stdin_from references unknown step %q", step.Name, ec.StdinFrom)
			}
			if ec.Timeout != "" {
				d, err := time.ParseDuration(ec.Timeout)
				if err != nil {
					return fmt.Errorf("step %q: exit_condition timeout %q: %w", step.Name, ec.Timeout, err)
				}
				if d <= 0 {
					return fmt.Errorf("step %q: exit_condition timeout %q must be > 0", step.Name, ec.Timeout)
				}
			}
			if ec.StepContextEmpty == "" && ec.StepContextContains == "" && ec.Command == "" {
				return fmt.Errorf("step %q: exit_condition must set step_context_empty, step_context_contains or command", step.Name)
			}
		}

		// Check for overlapping loops
		newRange := loopRange{from: targetIdx, to: i, stepName: step.Name}
		for _, existing := range ranges {
			// Overlapping if one range starts inside the other
			if (newRange.from > existing.from && newRange.from < existing.to) ||
				(newRange.to > existing.from && newRange.to < existing.to) ||
				(existing.from > newRange.from && existing.from < newRange.to) ||
				// A self-loop's step inside another loop's body nests two
				// loops on the one shared iteration counter.
				(existing.from == existing.to && existing.to >= newRange.from) {
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
	// Names live in ONE namespace shared by top-level steps and parallel
	// branches, because {{steps.<name>.context}} addresses both.
	seen := make(map[string]bool, len(wf.Steps))
	claim := func(name, what string) error {
		if name == "" {
			return fmt.Errorf("%s is missing a name", what)
		}
		if seen[name] {
			return fmt.Errorf("duplicate step name %q (steps and parallel branches share one namespace)", name)
		}
		seen[name] = true
		return nil
	}

	stepIndex := make(map[string]int, len(wf.Steps))
	branchNames := make(map[string]bool)
	for i := range wf.Steps {
		stepIndex[wf.Steps[i].Name] = i
		if wf.Steps[i].IsParallel() {
			for _, b := range wf.Steps[i].Parallel.Branches {
				branchNames[b.Name] = true
			}
		}
	}

	for i := range wf.Steps {
		step := &wf.Steps[i]
		if err := claim(step.Name, fmt.Sprintf("step %d", i)); err != nil {
			return err
		}
		if step.IsFanout() {
			if err := wf.validateFanoutStep(step, i, stepIndex, branchNames); err != nil {
				return err
			}
			for _, n := range step.Fanout.Nodes {
				_, isStep := stepIndex[n.Name]
				if isStep || branchNames[n.Name] || seen[n.Name] {
					return fmt.Errorf("step %q: fanout node %q duplicates another step name (steps, parallel branches and fanout nodes share one namespace)", step.Name, n.Name)
				}
				seen[n.Name] = true
			}
			continue
		}
		if step.IsParallel() {
			if err := validateParallelGroup(step); err != nil {
				return err
			}
			for j := range step.Parallel.Branches {
				b := &step.Parallel.Branches[j]
				if err := claim(b.Name, fmt.Sprintf("step %q: branch %d", step.Name, j)); err != nil {
					return err
				}
			}
			if step.Parallel.IsIsolated() && wf.Worktree != nil && !*wf.Worktree {
				return fmt.Errorf("parallel group %q sets isolation: worktree but the workflow pins worktree: false", step.Name)
			}
			continue
		}
		if !ValidSummarizationStrategies[step.SummarizationStrategy] {
			return fmt.Errorf("step %q: invalid summarization_strategy %q (must be %q, %q, or %q)",
				step.Name, step.SummarizationStrategy,
				SummarizationStrategyLastMessage, SummarizationStrategySummarizeChat, SummarizationStrategyNone)
		}
		switch step.Advance {
		case "", AdvanceAuto, AdvanceExplicit:
		default:
			return fmt.Errorf("step %q: invalid advance %q (must be %q or %q)",
				step.Name, step.Advance, AdvanceAuto, AdvanceExplicit)
		}
		if step.ExplicitAdvance() && step.Human {
			return fmt.Errorf("step %q: advance: explicit cannot be combined with human: true (a human step never auto-advances)", step.Name)
		}
	}
	return nil
}

// isolatedGroup returns the name of the workflow's first parallel group that
// sets isolation: worktree.
func (wf *WorkflowConfig) isolatedGroup() (string, bool) {
	for i := range wf.Steps {
		if wf.Steps[i].IsParallel() && wf.Steps[i].Parallel.IsIsolated() {
			return wf.Steps[i].Name, true
		}
	}
	return "", false
}

// validateParallelGroup checks a `parallel:` group entry and each of its
// branches. A group is a container, not a step: it carries no prompt and none
// of the per-step execution knobs (those belong on the branches). A branch is
// an ordinary headless step, so it may not itself be a group, loop, or gate.
func validateParallelGroup(step *StepConfig) error {
	p := step.Parallel
	// Group-level exclusions. Every field here is either meaningless on a
	// container (prompt, require_context) or would need semantics the group
	// deliberately doesn't have (loop/human on a fan-out, a summarization
	// pass over an aggregate that is already assembled from branch contexts).
	for _, banned := range []struct {
		set   bool
		field string
	}{
		{step.Prompt != "", "prompt"},
		{step.Loop != nil, "loop"},
		{step.Human, "human"},
		{step.Advance != "", "advance"},
		{step.SummarizationStrategy != "", "summarization_strategy"},
		{step.SummarizationPrompt != "", "summarization_prompt"},
		{step.RequireContext, "require_context"},
	} {
		if banned.set {
			return fmt.Errorf("step %q: a parallel group cannot set %s (it belongs on a branch)", step.Name, banned.field)
		}
	}
	if len(p.Branches) == 0 {
		return fmt.Errorf("step %q: parallel.branches must have at least one branch", step.Name)
	}
	switch p.Require {
	case "", RequireAll, RequireAny:
	default:
		n, err := strconv.Atoi(p.Require)
		if err != nil {
			return fmt.Errorf("step %q: invalid parallel.require %q (must be %q, %q, or an integer 1..%d)",
				step.Name, p.Require, RequireAll, RequireAny, len(p.Branches))
		}
		if n < 1 || n > len(p.Branches) {
			return fmt.Errorf("step %q: parallel.require %d is out of range (must be 1..%d, the branch count)",
				step.Name, n, len(p.Branches))
		}
	}
	switch p.Isolation {
	case "", IsolationShared, IsolationWorktree:
	default:
		return fmt.Errorf("step %q: invalid parallel.isolation %q (must be %q or %q)",
			step.Name, p.Isolation, IsolationShared, IsolationWorktree)
	}
	for j := range p.Branches {
		b := &p.Branches[j]
		// A bare-scalar list entry parses as a step *reference*, which only
		// resolveWorkflowSteps can expand — and it resolves against top-level
		// steps, not branches. Require an inline mapping.
		if b.ref {
			return fmt.Errorf("step %q: branch %q must be an inline mapping, not a step reference", step.Name, b.Name)
		}
		if b.IsParallel() {
			return fmt.Errorf("step %q: branch %q cannot itself be a parallel group (no nesting)", step.Name, b.Name)
		}
		if b.Loop != nil {
			return fmt.Errorf("step %q: branch %q cannot have a loop", step.Name, b.Name)
		}
		if b.Human {
			return fmt.Errorf("step %q: branch %q cannot have human: true", step.Name, b.Name)
		}
		if b.Advance != "" {
			return fmt.Errorf("step %q: branch %q cannot set advance", step.Name, b.Name)
		}
		if !ValidSummarizationStrategies[b.SummarizationStrategy] {
			return fmt.Errorf("step %q: branch %q: invalid summarization_strategy %q (must be %q, %q, or %q)",
				step.Name, b.Name, b.SummarizationStrategy,
				SummarizationStrategyLastMessage, SummarizationStrategySummarizeChat, SummarizationStrategyNone)
		}
	}
	return nil
}

// validateFanoutStep checks a `fanout:` step at index i. A fanout runs no agent,
// so every per-agent field is rejected; its item source must be an earlier
// ordinary step, which alone publishes items. Whether the child workflow exists
// is checked once every workflow is assembled (validateFanoutTargets).
func (wf *WorkflowConfig) validateFanoutStep(step *StepConfig, i int, stepIndex map[string]int, branchNames map[string]bool) error {
	f := step.Fanout
	for _, banned := range []struct {
		set   bool
		field string
	}{
		{step.Prompt != "", "prompt"},
		{step.Agent != "", "agent"},
		{step.Loop != nil, "loop"},
		{step.Human, "human"},
		{step.Advance != "", "advance"},
		{step.Parallel != nil, "parallel"},
		{step.SummarizationStrategy != "", "summarization_strategy"},
		{step.SummarizationPrompt != "", "summarization_prompt"},
		{step.RequireContext, "require_context"},
	} {
		if banned.set {
			return fmt.Errorf("step %q: fanout step cannot set %s (a fanout runs no agent)", step.Name, banned.field)
		}
	}

	switch {
	case f.From != "" && f.IsNodes():
		return fmt.Errorf("step %q: fanout sets both `from:` (%q) and `nodes:`; use one item source", step.Name, f.From)
	case f.IsNodes():
		return wf.validateFanoutNodes(step, i)
	case f.From == "":
		return fmt.Errorf("step %q: fanout needs `from:` (the earlier step whose items it fans out) or `nodes:` (a declared child graph)", step.Name)
	}
	if branchNames[f.From] {
		return fmt.Errorf("step %q: fanout from %q names a parallel branch; name an ordinary top-level step", step.Name, f.From)
	}
	fromIdx, ok := stepIndex[f.From]
	if !ok {
		return fmt.Errorf("step %q: fanout from references unknown step %q", step.Name, f.From)
	}
	if fromIdx >= i {
		return fmt.Errorf("step %q: fanout from %q must name an earlier step", step.Name, f.From)
	}
	from := &wf.Steps[fromIdx]
	if from.IsFanout() {
		return fmt.Errorf("step %q: fanout from %q names another fanout step", step.Name, f.From)
	}
	if from.IsParallel() {
		return fmt.Errorf("step %q: fanout from %q names a parallel group; name an ordinary step", step.Name, f.From)
	}

	if f.Workflow == "" {
		return fmt.Errorf("step %q: fanout needs `workflow:` (the workflow each child runs)", step.Name)
	}

	return validateFanoutCommon(step)
}

// validateFanoutCommon checks the join knobs shared by both item sources.
func validateFanoutCommon(step *StepConfig) error {
	f := step.Fanout
	switch f.Require {
	case "", RequireAll, RequireAny:
	default:
		if n, err := strconv.Atoi(f.Require); err != nil || n < 1 {
			return fmt.Errorf("step %q: fanout require %q is invalid (must be %q, %q, or an integer >= 1)",
				step.Name, f.Require, RequireAll, RequireAny)
		}
	}
	if f.Retries < 0 {
		return fmt.Errorf("step %q: fanout retries must be >= 0 (got %d)", step.Name, f.Retries)
	}
	switch f.Merge {
	case "", FanoutMergeParent, FanoutMergeBase:
	default:
		return fmt.Errorf("step %q: fanout merge %q is invalid (must be %q or %q)",
			step.Name, f.Merge, FanoutMergeParent, FanoutMergeBase)
	}
	return nil
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
)

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

// GlobalConfig from ~/.config/sakusen/config.yaml
type GlobalConfig struct {
	MaxWorkers               int                    `yaml:"max_workers"`
	PollInterval             string                 `yaml:"poll_interval,omitempty"`
	Verification             *VerificationConfig    `yaml:"verification,omitempty"`
	Notifications            NotificationsConfig    `yaml:"notifications"`
	TmuxNestedAttachBehavior string                 `yaml:"tmux_nested_attach_behavior"`
	Options                  *OptionsConfig         `yaml:"options,omitempty"`
	Agents                   map[string]AgentConfig `yaml:"agents,omitempty"`
	AgentAliases             map[string]string      `yaml:"agent_aliases,omitempty"`
	DefaultAgent             string                 `yaml:"default_agent,omitempty"`
	MergeConflicts           *MergeConflictsConfig  `yaml:"merge_conflicts,omitempty"`
	Summarizer               *SummarizerConfig      `yaml:"summarizer,omitempty"`
	Tmux                     *TmuxConfig            `yaml:"tmux,omitempty"`
	Merge                    *MergeConfig           `yaml:"merge,omitempty"`
}

type NotificationsConfig struct {
	Enabled        bool `yaml:"enabled"`
	OnComplete     bool `yaml:"on_complete"`
	OnFailed       bool `yaml:"on_failed"`
	OnWaitingInput bool `yaml:"on_waiting_input"`
	OnMergeBlocked bool `yaml:"on_merge_blocked"`
}

// NotificationOverrides is the `notifications:` block on a workflow or a
// routine. Each field overrides the global per-event switch only when
// set. There is deliberately no `enabled`: the global switch stays the master
// gate, so an override can silence events but never turn notifications on.
type NotificationOverrides struct {
	OnComplete     *bool `yaml:"on_complete,omitempty"`
	OnFailed       *bool `yaml:"on_failed,omitempty"`
	OnWaitingInput *bool `yaml:"on_waiting_input,omitempty"`
	// NotifyIf further gates completion notifications on a step's context.
	NotifyIf *NotifyCondition `yaml:"notify_if,omitempty"`
}

// UnmarshalYAML decodes strictly: the enclosing workflow and routine entries
// tolerate unknown keys, but a typo here would silently re-enable noise.
func (n *NotificationOverrides) UnmarshalYAML(value *yaml.Node) error {
	if err := checkKnownMappingKeys(value, "notifications", "on_complete", "on_failed", "on_waiting_input", "notify_if"); err != nil {
		return err
	}
	type raw NotificationOverrides
	var r raw
	if err := value.Decode(&r); err != nil {
		return err
	}
	*n = NotificationOverrides(r)
	return nil
}

// NotifyCondition limits completion notifications to runs where a step
// published something worth a look. Exactly one form is set:
// StepContextNonempty, or StepContextContains together with Marker.
type NotifyCondition struct {
	// StepContextNonempty names a step whose context must be non-blank.
	StepContextNonempty string `yaml:"step_context_nonempty,omitempty"`
	// StepContextContains names a step whose context must contain Marker
	// (literal, case-sensitive substring, as in LoopExitCondition).
	StepContextContains string `yaml:"step_context_contains,omitempty"`
	Marker              string `yaml:"marker,omitempty"`
}

// UnmarshalYAML decodes strictly, for the same reason as NotificationOverrides.
func (c *NotifyCondition) UnmarshalYAML(value *yaml.Node) error {
	if err := checkKnownMappingKeys(value, "notify_if", "step_context_nonempty", "step_context_contains", "marker"); err != nil {
		return err
	}
	type raw NotifyCondition
	var r raw
	if err := value.Decode(&r); err != nil {
		return err
	}
	*c = NotifyCondition(r)
	return nil
}

// Step returns the name of the step whose context the condition inspects.
func (c *NotifyCondition) Step() string {
	if c.StepContextContains != "" {
		return c.StepContextContains
	}
	return c.StepContextNonempty
}

// Matches reports whether the inspected step's context satisfies the condition.
func (c *NotifyCondition) Matches(stepContext string) bool {
	if c.StepContextContains != "" {
		return strings.Contains(stepContext, c.Marker)
	}
	return strings.TrimSpace(stepContext) != ""
}

// validate checks the block's shape and that notify_if names a step or
// parallel branch of wf.
func (n *NotificationOverrides) validate(wf *WorkflowConfig) error {
	c := n.NotifyIf
	if c == nil {
		return nil
	}
	switch {
	case c.StepContextNonempty != "" && c.StepContextContains != "":
		return fmt.Errorf("notifications.notify_if: set exactly one of `step_context_nonempty` or `step_context_contains`, not both")
	case c.StepContextNonempty == "" && c.StepContextContains == "":
		return fmt.Errorf("notifications.notify_if: set one of `step_context_nonempty` or `step_context_contains`")
	case c.StepContextContains != "" && strings.TrimSpace(c.Marker) == "":
		return fmt.Errorf("notifications.notify_if: `marker` is required with `step_context_contains`")
	case c.StepContextNonempty != "" && c.Marker != "":
		return fmt.Errorf("notifications.notify_if: `marker` is only valid with `step_context_contains`")
	}
	step := c.Step()
	for _, name := range wf.AllStepNames() {
		if name == step {
			return nil
		}
	}
	return fmt.Errorf("notifications.notify_if: step %q does not exist", step)
}

// ValidateNotifications checks the workflow's notifications override block.
func (wf *WorkflowConfig) ValidateNotifications() error {
	if wf.Notifications == nil {
		return nil
	}
	return wf.Notifications.validate(wf)
}

// CommandsConfig is used during init for project detection
type CommandsConfig struct {
	Test  string `yaml:"test"`
	Lint  string `yaml:"lint"`
	Build string `yaml:"build"`
}

// Config is the merged runtime config used by all components.
// It combines global config, project config (.sakusen.yml), and computed paths.
type Config struct {
	// From .sakusen.yml (project config)
	MaxWorkers      int
	DefaultPriority string
	Verification    VerificationConfig
	Git             GitConfig
	// OnComplete is the resolved project-level finalization action
	// ("commit" | "merge" | "none"). Per-workflow overrides live on
	// WorkflowConfig.OnComplete and are resolved at finalization time via
	// EffectiveOnComplete.
	OnComplete string

	Workflows []WorkflowConfig // flat resolved workflow list
	Routines  []RoutineConfig  // invocation bindings (top-level routines: section)

	// Agents is the merged agent registry (global tier overlaid by the
	// project tier, per slug). See AgentConfig and Config.StepAgent. After
	// load it also holds expanded `parent:variant` entries and resolved alias
	// entries — all ordinary records.
	Agents map[string]AgentConfig

	// AgentAliases is the merged `agent_aliases:` routing map (alias name →
	// target slug), overlaid per key across tiers. Resolved into ordinary
	// Agents entries at load time.
	AgentAliases map[string]string

	// DefaultAgent is the resolved top-level `default_agent:` slug (project
	// beats global). Empty means the implicit "claude" fallback.
	DefaultAgent string

	// MergeConflicts is the merged merge-conflict resolver configuration.
	MergeConflicts MergeConflictsConfig

	// Summarizer is the merged utility-LLM configuration.
	Summarizer SummarizerConfig

	// Paths to sync from project root into new worktrees
	WorktreeSyncPaths WorktreeSyncPathsConfig

	// Command to run after creating a worktree (e.g. dependency installation)
	WorktreeSetupCommand string

	// Commands to run sequentially after creating a worktree
	WorktreeSetupCommands []string

	// Command to run after creating a tmux session (e.g. layout setup)
	TmuxSetupCommand string

	// TmuxHibernateAfter is the resolved tmux.hibernate_after (0 = idle
	// hibernation disabled).
	TmuxHibernateAfter time.Duration

	// TmuxMaxLiveSessions is the resolved tmux.max_live_sessions soft cap
	// (0 = cap disabled).
	TmuxMaxLiveSessions int

	// MergeCleanTargetTimeout is the resolved merge.clean_target_timeout
	// (0 = wait forever).
	MergeCleanTargetTimeout time.Duration

	// MergeIgnorePaths is the resolved merge.ignore_paths.
	MergeIgnorePaths []string

	// GitVerifyTimeout is the resolved git.verify_timeout (0 = no limit).
	GitVerifyTimeout time.Duration

	// GitCommitMessageTimeout is the resolved git.commit_message_timeout.
	GitCommitMessageTimeout time.Duration

	// GitVerifyParentMerges is the resolved git.verify_parent_merges
	// (default true).
	GitVerifyParentMerges bool
	// From global config
	Notifications            NotificationsConfig
	TmuxNestedAttachBehavior string // "switch" (default) or "nest"

	// TUI display options (from .sakusen.yml options section)
	Options OptionsConfig

	// Whether a project config (.sakusen.yml) was found
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

	// globalPool holds workflows resolved from the global ~/.sakusen.yml (both
	// inline and file-based under ~/.sakusen/workflows/). Project-level config
	// string refs that don't match a local .sakusen/workflows/<name>.yml
	// fall back to this pool, letting projects reuse globally-defined
	// workflows by name. Populated during loadCommon, after the global
	// .sakusen.yml is processed. Not serialized.
	globalPool *globalWorkflowPool
}
