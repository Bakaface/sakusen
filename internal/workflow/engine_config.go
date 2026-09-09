package workflow

import (
	"time"

	"github.com/Bakaface/sakusen/internal/config"
)

// engineConfig is the narrow slice of *config.Config that Engine actually
// depends on. Before this type existed, Engine held the full *config.Config
// and reached into whatever field or method it needed from any file in this
// package — the true dependency surface was invisible, scattered across
// engine.go/step.go/summarizer.go/merge.go. This type writes that surface
// down in one place.
//
// Direct fields mirror config.Config values Engine reads verbatim (a plain
// snapshot taken once at construction). The delegate methods mirror
// config.Config accessors whose behavior depends on state that's impractical
// to flatten out here (the full resolved Workflows list, the branch template,
// the agent registry) — duplicating that logic would be worse than keeping a
// pointer back to the source Config for those few calls. See NewEngine: the
// daemon already reconstructs the Engine wholesale on every config reload (it
// does not mutate an existing Engine's config in place), so a value
// snapshotted at construction time is exactly as fresh as holding the full
// *config.Config would be.
type engineConfig struct {
	BaseBranch       string
	MergeConflicts   config.MergeConflictsConfig
	Summarizer       config.SummarizerConfig
	ProjectName      string
	TmuxSetupCommand string

	// PromptDirs are the ordered {{prompt.<name>}} include search roots
	// (project then global). Snapshotted like every other value here; the
	// FILES themselves are re-read at every step launch, so editing a shared
	// passage takes effect without a config reload.
	PromptDirs []string

	// full is retained so the delegate methods below can reuse
	// config.Config's own workflow-lookup / branch-template / agent
	// resolution instead of duplicating it.
	full *config.Config
}

// newEngineConfig snapshots the fields/methods Engine needs from cfg.
// repoRoot anchors the project tier of the prompt-include search when the
// config carries no ProjectDir (standalone/test construction).
func newEngineConfig(cfg *config.Config, repoRoot string) *engineConfig {
	projectDir := cfg.ProjectDir
	if projectDir == "" {
		projectDir = repoRoot
	}
	return &engineConfig{
		BaseBranch:       cfg.Git.BaseBranch,
		MergeConflicts:   cfg.MergeConflicts,
		Summarizer:       cfg.Summarizer,
		ProjectName:      cfg.Project.Name,
		TmuxSetupCommand: cfg.TmuxSetupCommand,
		PromptDirs:       config.PromptDirs(projectDir),
		full:             cfg,
	}
}

func (e *engineConfig) GetWorkflow(name string) *config.WorkflowConfig {
	return e.full.GetWorkflow(name)
}

// GetTaskWorkflow is the strict lookup: nil when name does not resolve,
// where GetWorkflow silently substitutes the built-in single-step default.
// Anything driving a task whose workflow name comes from the DB must use
// this — a name that stops resolving (the project's `workflows:` list was
// edited mid-flight) would otherwise silently re-shape a running task into
// the default workflow.
func (e *engineConfig) GetTaskWorkflow(name string) *config.WorkflowConfig {
	return e.full.GetTaskWorkflow(name)
}

func (e *engineConfig) EffectiveOnComplete(workflowName string) string {
	return e.full.EffectiveOnComplete(workflowName)
}

func (e *engineConfig) GetStepTimeout(step config.StepConfig) time.Duration {
	return e.full.GetStepTimeout(step)
}

func (e *engineConfig) GetWorktreeSyncPaths(wf *config.WorkflowConfig) config.WorktreeSyncPathsConfig {
	return e.full.GetWorktreeSyncPaths(wf)
}

func (e *engineConfig) GetWorktreeSetupCommand(wf *config.WorkflowConfig) string {
	return e.full.GetWorktreeSetupCommand(wf)
}

func (e *engineConfig) GetWorktreeSetupCommands(wf *config.WorkflowConfig) []string {
	return e.full.GetWorktreeSetupCommands(wf)
}

func (e *engineConfig) ResolveBranchForTask(taskID int64, taskTitle, taskSlug, branchName string) string {
	return e.full.ResolveBranchForTask(taskID, taskTitle, taskSlug, branchName)
}

// StepAgent resolves the agent record for a step (step.agent → workflow.agent
// → default_agent → "claude"). See config.Config.StepAgent.
func (e *engineConfig) StepAgent(wf *config.WorkflowConfig, step *config.StepConfig) (string, config.AgentConfig, error) {
	return e.full.StepAgent(wf, step)
}

// StepAgentSlug resolves only the agent SLUG for a step, without requiring the
// record to exist. See config.Config.StepAgentSlug.
func (e *engineConfig) StepAgentSlug(wf *config.WorkflowConfig, step *config.StepConfig) string {
	return e.full.StepAgentSlug(wf, step)
}

// MergeConflictAgent resolves the headless agent that fixes merge conflicts
// (merge_conflicts.agent → workflow.agent → default_agent → "claude").
// See config.Config.MergeConflictAgentFor.
func (e *engineConfig) MergeConflictAgent(wf *config.WorkflowConfig) (string, config.AgentConfig, error) {
	return e.full.MergeConflictAgentFor(wf)
}

// SummarizerInvocation resolves how summarizer calls run (registry agent or
// bare command) from the snapshotted summarizer block, against the live agent
// registry. See config.Config.SummarizerInvocationFor.
func (e *engineConfig) SummarizerInvocation() (config.SummarizerInvocation, bool) {
	return e.full.SummarizerInvocationFor(&e.Summarizer)
}

// StepIsTmux reports whether a step resolves to a tmux-mode agent.
func (e *engineConfig) StepIsTmux(wf *config.WorkflowConfig, step *config.StepConfig) bool {
	return e.full.StepIsTmux(wf, step)
}
