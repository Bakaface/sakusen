package tui

import (
	"fmt"
	"strings"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/daemon"
	"github.com/Bakaface/sortie/internal/task"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

type taskInfoView struct {
	task     *daemon.TaskInfo
	workflow *config.WorkflowConfig
	viewport viewport.Model
	width    int
	height   int
	ready    bool
	showHelp bool // toggled by ctrl+h
}

func newTaskInfoView() taskInfoView {
	return taskInfoView{}
}

func (v *taskInfoView) SetTask(task *daemon.TaskInfo) {
	v.task = task
	v.updateContent()
}

func (v *taskInfoView) SetWorkflow(wf *config.WorkflowConfig) {
	v.workflow = wf
	v.updateContent()
}

func (v *taskInfoView) SetSize(width, height int) {
	v.width = width
	v.height = height
	v.recalcViewport()
}

func (v *taskInfoView) recalcViewport() {
	if v.width == 0 || v.height == 0 {
		return
	}

	// Header: title bar + blank line + gap before viewport = 3 lines
	// Footer: help bar + bottom margin = 3 lines
	headerHeight := 3
	footerHeight := 3
	vpHeight := v.height - headerHeight - footerHeight
	if vpHeight < 1 {
		vpHeight = 1
	}

	if !v.ready {
		v.viewport = viewport.New(v.width-4, vpHeight)
		v.viewport.HighPerformanceRendering = false
		v.ready = true
	} else {
		v.viewport.Width = v.width - 4
		v.viewport.Height = vpHeight
	}

	v.updateContent()
}

func (v *taskInfoView) updateContent() {
	if !v.ready || v.task == nil {
		return
	}

	content := v.renderMetadata()
	wrapped := lipgloss.NewStyle().Width(v.viewport.Width).Render(content)
	v.viewport.SetContent(wrapped)
}

func (v *taskInfoView) renderMetadata() string {
	t := v.task
	status := task.Status(t.Status)
	var b strings.Builder

	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(highlight)
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#E8E8E8"))

	// Task title
	taskTitle := t.Title
	if taskTitle == "" {
		taskTitle = t.Input
	}
	b.WriteString(subHeaderStyle.Render(
		fmt.Sprintf("#%d %s", t.ID, taskTitle)))
	b.WriteString("\n\n")

	// Status
	b.WriteString(labelStyle.Render("Status:    "))
	b.WriteString(stateStyle(status).Render(t.Status))
	b.WriteString("\n")

	// Priority
	if t.Priority != "" {
		b.WriteString(labelStyle.Render("Priority:  "))
		b.WriteString(priorityStyle(t.Priority).Render(t.Priority))
		b.WriteString("\n")
	}

	// Branch
	if t.Branch != "" {
		b.WriteString(labelStyle.Render("Branch:    "))
		b.WriteString(valueStyle.Render(t.Branch))
		if t.BranchName != "" && t.BranchName != t.Branch {
			b.WriteString(dimStyle.Render(fmt.Sprintf(" (template: %s)", t.BranchName)))
		}
		b.WriteString("\n")
	} else if t.BranchName != "" {
		b.WriteString(labelStyle.Render("Branch:    "))
		b.WriteString(dimStyle.Render(fmt.Sprintf("(template: %s)", t.BranchName)))
		b.WriteString("\n")
	}

	// Target branch
	if t.TargetBranch != "" {
		b.WriteString(labelStyle.Render("Target:    "))
		b.WriteString(valueStyle.Render(t.TargetBranch))
		b.WriteString("\n")
	}

	// Checkout branch
	if t.CheckoutBranch != "" {
		b.WriteString(labelStyle.Render("Checkout:  "))
		b.WriteString(valueStyle.Render(t.CheckoutBranch))
		b.WriteString("\n")
	}

	// Workflow
	if t.Workflow != "" {
		b.WriteString(labelStyle.Render("Workflow:  "))
		b.WriteString(valueStyle.Render(t.Workflow))
		b.WriteString("\n")
	}

	// Track (read-only; slug enriched by the daemon)
	if t.Track != "" {
		b.WriteString(labelStyle.Render("Track:     "))
		b.WriteString(valueStyle.Render(t.Track))
		b.WriteString("\n")
	}

	// Current step
	if t.CurrentStep != "" {
		stepText := fmt.Sprintf("%s (%d)", t.CurrentStep, t.StepIndex+1)
		if t.LoopIteration > 0 {
			stepText += fmt.Sprintf(" — loop iteration %d", t.LoopIteration)
		}
		b.WriteString(labelStyle.Render("Step:      "))
		b.WriteString(valueStyle.Render(stepText))
		b.WriteString("\n")
	}

	// Error message
	if t.ErrorMessage != "" {
		b.WriteString(labelStyle.Render("Error:     "))
		b.WriteString(stateStyle(task.StatusFailed).Render(t.ErrorMessage))
		b.WriteString("\n")
	}

	// Blocked by
	if len(t.BlockedBy) > 0 {
		ids := make([]string, len(t.BlockedBy))
		for i, id := range t.BlockedBy {
			ids[i] = fmt.Sprintf("#%d", id)
		}
		b.WriteString(labelStyle.Render("Blocked by: "))
		b.WriteString(valueStyle.Render(strings.Join(ids, ", ")))
		b.WriteString("\n")
	}

	// Timestamps
	b.WriteString(labelStyle.Render("Created:   "))
	b.WriteString(dimStyle.Render(t.CreatedAt.Format("2006-01-02 15:04:05")))
	b.WriteString("\n")
	if t.StartedAt != nil {
		b.WriteString(labelStyle.Render("Started:   "))
		b.WriteString(dimStyle.Render(t.StartedAt.Format("2006-01-02 15:04:05")))
		b.WriteString("\n")
	}
	if t.CompletedAt != nil {
		b.WriteString(labelStyle.Render("Completed: "))
		b.WriteString(dimStyle.Render(t.CompletedAt.Format("2006-01-02 15:04:05")))
		b.WriteString("\n")
	}

	// Worktree mode
	if !t.Worktree {
		b.WriteString(labelStyle.Render("Worktree:  "))
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#E88388")).Render("off"))
		b.WriteString(dimStyle.Render(" (runs in current directory)"))
		b.WriteString("\n")
	} else if t.WorktreePath != "" {
		b.WriteString(labelStyle.Render("Worktree:  "))
		b.WriteString(dimStyle.Render(t.WorktreePath))
		if t.WorktreeDetached {
			b.WriteString("  ")
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#E88388")).Render("[detached]"))
		}
		b.WriteString("\n")
	}

	// Workflow progress
	if v.workflow != nil && len(v.workflow.Steps) > 0 {
		b.WriteString("\n")
		b.WriteString(labelStyle.Render("Workflow Progress"))
		b.WriteString("\n")

		for i, step := range v.workflow.Steps {
			icon := "○" // pending
			style := dimStyle
			if i < t.StepIndex {
				icon = "✓" // completed
				style = stateStyle(task.StatusCompleted)
			} else if i == t.StepIndex && (status == task.StatusRunning || status == task.StatusAwaitingApproval || status == task.StatusSummarizingStep) {
				icon = "●" // active
				style = stateStyle(status)
			} else if status == task.StatusCompleted || status == task.StatusFinalizing || status == task.StatusSummarizing || status == task.StatusMergeBlocked || status == task.StatusResolvingConflicts || status == task.StatusMergeFailed {
				// merge-failed groups here rather than with StatusFailed below:
				// every workflow step ran, only finalization failed, so marking
				// a step ✗ would blame the wrong thing.
				icon = "✓"
				style = stateStyle(task.StatusCompleted)
			} else if status == task.StatusFailed && i == t.StepIndex {
				icon = "✗"
				style = stateStyle(task.StatusFailed)
			}

			suffix := ""
			if step.Human {
				suffix = " [human]"
			}
			// Explicit agent assignments only — the default cascade is
			// surfaced via the daemon's list_workflows, not re-resolved here.
			if step.Agent != "" {
				suffix += fmt.Sprintf(" [agent:%s]", step.Agent)
			}
			if step.Loop != nil {
				suffix += fmt.Sprintf(" [loop→%s ×%d]", step.Loop.Goto, step.Loop.MaxIterations)
			}

			b.WriteString(style.Render(fmt.Sprintf("  %s %d. %s%s", icon, i+1, step.Name, suffix)))
			b.WriteString("\n")
		}
	}

	// Input
	if t.Input != "" {
		b.WriteString("\n")
		b.WriteString(labelStyle.Render("Input"))
		b.WriteString("\n")
		b.WriteString(valueStyle.Render(t.Input))
		b.WriteString("\n")
	}

	// Context (AI summary)
	if t.Context != "" {
		b.WriteString("\n")
		b.WriteString(labelStyle.Render("Context"))
		b.WriteString("\n")
		b.WriteString(valueStyle.Render(t.Context))
		b.WriteString("\n")
	}

	return b.String()
}

func (v *taskInfoView) ScrollUp() {
	v.viewport.LineUp(1)
}

func (v *taskInfoView) ScrollDown() {
	v.viewport.LineDown(1)
}

func (v *taskInfoView) PageUp() {
	v.viewport.HalfViewUp()
}

func (v *taskInfoView) PageDown() {
	v.viewport.HalfViewDown()
}

func (v *taskInfoView) GotoTop() {
	v.viewport.GotoTop()
}

func (v *taskInfoView) GotoBottom() {
	v.viewport.GotoBottom()
}

func (v *taskInfoView) View() string {
	if v.task == nil {
		return "No task selected"
	}

	var b strings.Builder

	// App title
	b.WriteString(titleStyle.Render(" " + AppTitle + " "))
	b.WriteString("\n\n")

	// Scrollable content viewport
	if v.ready {
		vpContent := viewportStyle.Render(v.viewport.View())
		b.WriteString(vpContent)
	} else {
		b.WriteString("  Loading...")
	}

	b.WriteString("\n")
	b.WriteString(v.renderHelp())

	return b.String()
}

func (v *taskInfoView) renderHelp() string {
	var help strings.Builder
	help.WriteString(helpStyle.Render("  "))

	bindings := newTaskInfoKeyMap().ShortHelp()
	for i, binding := range bindings {
		if i > 0 {
			help.WriteString(helpStyle.Render(" | "))
		}
		help.WriteString(dimStyle.Render(binding.Help().Key))
		help.WriteString(helpStyle.Render(" "))
		help.WriteString(helpStyle.Render(binding.Help().Desc))
	}

	return help.String()
}
