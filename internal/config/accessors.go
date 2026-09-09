package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// GetStepTimeout parses the step's timeout string or returns the default.
func (c *Config) GetStepTimeout(step StepConfig) time.Duration {
	if step.Timeout != "" {
		if d, err := time.ParseDuration(step.Timeout); err == nil {
			return d
		}
	}
	return DefaultStepTimeout
}

// GetWorkflow returns the workflow with the given name.
// If name is empty, returns the first non-hidden workflow (hidden workflows —
// unreferenced files and namespaced track workflows — are only reachable by
// exact name, never as an implicit default).
// If the workflow is not found, returns the default workflow.
func (c *Config) GetWorkflow(name string) *WorkflowConfig {
	for i := range c.Workflows {
		if c.Workflows[i].Name == name {
			return &c.Workflows[i]
		}
	}
	if name == "" {
		for i := range c.Workflows {
			if !c.Workflows[i].Hidden {
				return &c.Workflows[i]
			}
		}
	}
	// Return default
	def := DefaultWorkflow()
	return &def
}

// EffectiveOnComplete resolves the finalization action for a task running the
// named workflow. Precedence is a plain most-specific-wins cascade:
//
//  1. the workflow's own on_complete, wherever the workflow was defined
//  2. the project .sakusen.yml top-level on_complete
//  3. the inherited top-level on_complete (~/.sakusen.yml or built-in default)
//
// on_complete is a property of the workflow's shape — whether its last step is
// a human gate — so an explicit workflow value is authoritative regardless of
// scope. A project that needs a different value for a global workflow shadows
// it by name and sets on_complete there.
func (c *Config) EffectiveOnComplete(workflowName string) string {
	wf := c.GetWorkflow(workflowName)
	if wf != nil && wf.OnComplete != "" {
		return wf.OnComplete
	}
	return c.OnComplete
}

// ListWorkflowNames returns the names of active (non-hidden) workflows used for
// new task creation menus. If none configured, returns ["default"]. Use
// ListAllWorkflowNames for picker surfaces that should expose hidden workflows
// too.
func (c *Config) ListWorkflowNames() []string {
	return c.listNames(false)
}

// ListAllWorkflowNames returns the names of all workflows including hidden
// ones. Used by pickers and tab completion where every reachable workflow
// should be selectable.
func (c *Config) ListAllWorkflowNames() []string {
	return c.listNames(true)
}

func (c *Config) listNames(includeHidden bool) []string {
	if len(c.Workflows) == 0 {
		return []string{"default"}
	}
	names := make([]string, 0, len(c.Workflows))
	for _, w := range c.Workflows {
		if !includeHidden && w.Hidden {
			continue
		}
		names = append(names, w.Name)
	}
	if len(names) == 0 && !includeHidden {
		// All workflows hidden — fall back to default so menu surfaces still
		// have something to display.
		return []string{"default"}
	}
	return names
}

// GetTaskWorkflow returns the workflow config with the given name, or nil.
// Returns hidden workflows too. When name is empty, returns the first
// non-hidden workflow (or nil if none).
func (c *Config) GetTaskWorkflow(name string) *WorkflowConfig {
	if name == "" {
		for i := range c.Workflows {
			if !c.Workflows[i].Hidden {
				return &c.Workflows[i]
			}
		}
		return nil
	}
	for i := range c.Workflows {
		if c.Workflows[i].Name == name {
			return &c.Workflows[i]
		}
	}
	return nil
}

// GetRoutine returns the routine with the given name, or nil when absent.
func (c *Config) GetRoutine(name string) *RoutineConfig {
	for i := range c.Routines {
		if c.Routines[i].Name == name {
			return &c.Routines[i]
		}
	}
	return nil
}

// ListRoutineNames returns every routine name in config order.
func (c *Config) ListRoutineNames() []string {
	names := make([]string, 0, len(c.Routines))
	for i := range c.Routines {
		names = append(names, c.Routines[i].Name)
	}
	return names
}

// RoutineRequiresInput reports whether firing the routine needs an input
// argument: its effective input is empty and the workflow it binds references
// {{task.input}} somewhere. Such a routine is on-demand only (a scheduled one
// is a load error) and its run surfaces demand the argument. Returns false for
// a routine whose workflow no longer resolves — the fire fails on that instead.
func (c *Config) RoutineRequiresInput(r *RoutineConfig) bool {
	wf := c.GetTaskWorkflow(r.Workflow)
	if wf == nil {
		return false
	}
	if r.EffectivePins(wf).Input != "" {
		return false
	}
	_, ok := findTaskInputRef(wf)
	return ok
}

// GetWorktreeSyncPaths returns the sync paths for a workflow, falling back to the global config.
func (c *Config) GetWorktreeSyncPaths(wf *WorkflowConfig) WorktreeSyncPathsConfig {
	var wfVal WorktreeSyncPathsConfig
	if wf != nil {
		wfVal = wf.WorktreeSyncPaths
	}
	return firstNonEmptyValue(wfVal, c.WorktreeSyncPaths)
}

// GetWorktreeSetupCommand returns the setup command for a workflow, falling back to the global config.
func (c *Config) GetWorktreeSetupCommand(wf *WorkflowConfig) string {
	var wfVal string
	if wf != nil {
		wfVal = wf.WorktreeSetupCommand
	}
	return firstNonZero(wfVal, c.WorktreeSetupCommand)
}

// GetWorktreeSetupCommands returns the setup commands for a workflow, falling back to the global config.
func (c *Config) GetWorktreeSetupCommands(wf *WorkflowConfig) []string {
	var wfVal []string
	if wf != nil {
		wfVal = wf.WorktreeSetupCommands
	}
	return firstNonEmptySlice(wfVal, c.WorktreeSetupCommands)
}

// GetTmuxSetupCommand returns the tmux setup command for a workflow, falling back to the global config.
func (c *Config) GetTmuxSetupCommand(wf *WorkflowConfig) string {
	var wfVal string
	if wf != nil {
		wfVal = wf.TmuxSetupCommand
	}
	return firstNonZero(wfVal, c.TmuxSetupCommand)
}

// ResolveBranchForTask resolves the branch name for a task. If branchName is non-empty,
// it uses ResolveBranchTemplate; otherwise falls back to the config's branch template.
func (c *Config) ResolveBranchForTask(taskID int64, taskTitle, taskSlug, branchName string) string {
	if branchName != "" {
		return ResolveBranchTemplate(branchName, taskID, taskTitle, taskSlug)
	}
	return c.ResolveBranchName(taskID, taskSlug)
}

// ResolveBranchName applies the branch template for a task.
func (c *Config) ResolveBranchName(taskID int64, taskSlug string) string {
	tmpl := c.Git.BranchTemplate
	tmpl = strings.ReplaceAll(tmpl, "{{task_id}}", fmt.Sprintf("%d", taskID))
	tmpl = strings.ReplaceAll(tmpl, "{{task_slug}}", taskSlug)
	return tmpl
}

// ResolveBranchTemplate resolves a branch name template with task variables.
// Supports both config-style ({{task_id}}, {{task_slug}}) and workflow-style
// ({{task.title}}, {{task.id}}) placeholders.
func ResolveBranchTemplate(tmpl string, taskID int64, taskTitle, taskSlug string) string {
	// Config-style placeholders
	tmpl = strings.ReplaceAll(tmpl, "{{task_id}}", fmt.Sprintf("%d", taskID))
	tmpl = strings.ReplaceAll(tmpl, "{{task_slug}}", taskSlug)

	// Workflow-style placeholders
	tmpl = strings.ReplaceAll(tmpl, "{{task.id}}", fmt.Sprintf("%d", taskID))
	tmpl = strings.ReplaceAll(tmpl, "{{task.title}}", taskTitle)
	tmpl = strings.ReplaceAll(tmpl, "{{task.slug}}", taskSlug)

	return tmpl
}

// WriteProjectConfig writes a .sakusen.yml file.
func WriteProjectConfig(path string, proj *ProjectConfig) error {
	data, err := yaml.Marshal(proj)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (c *Config) Save() error {
	configPath := getGlobalConfigPath()
	if configPath == "" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return err
	}

	global := GlobalConfig{
		MaxWorkers:               c.MaxWorkers,
		Notifications:            c.Notifications,
		TmuxNestedAttachBehavior: c.TmuxNestedAttachBehavior,
	}

	data, err := yaml.Marshal(global)
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, data, 0644)
}
