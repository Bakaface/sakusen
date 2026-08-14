package workflow

import (
	"time"

	"github.com/Bakaface/sortie/internal/config"
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
	Summarizer       config.SummarizerConfig
	ProjectName      string
	TmuxSetupCommand string

	// full is retained so the delegate methods below can reuse
	// config.Config's own workflow-lookup / branch-template / agent
	// resolution instead of duplicating it.
	full *config.Config
}

// newEngineConfig snapshots the fields/methods Engine needs from cfg.
func newEngineConfig(cfg *config.Config) *engineConfig {
	return &engineConfig{
		BaseBranch:       cfg.Git.BaseBranch,
		Summarizer:       cfg.Summarizer,
		ProjectName:      cfg.Project.Name,
		TmuxSetupCommand: cfg.TmuxSetupCommand,
		full:             cfg,
	}
}

func (e *engineConfig) GetWorkflow(name string) *config.WorkflowConfig {
	return e.full.GetWorkflow(name)
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

// WorkflowAgent resolves the workflow-level agent record (workflow.agent →
// default_agent → "claude"). Used by paths that need an agent without a
// concrete step (merge conflict resolution).
func (e *engineConfig) WorkflowAgent(wf *config.WorkflowConfig) (string, config.AgentConfig, error) {
	return e.full.StepAgent(wf, nil)
}

// StepIsTmux reports whether a step resolves to a tmux-mode agent.
func (e *engineConfig) StepIsTmux(wf *config.WorkflowConfig, step *config.StepConfig) bool {
	return e.full.StepIsTmux(wf, step)
}

// ResolveAgent looks up an agent record by slug.
func (e *engineConfig) ResolveAgent(slug string) (config.AgentConfig, bool) {
	return e.full.ResolveAgent(slug)
}
