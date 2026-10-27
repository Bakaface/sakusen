package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/Bakaface/sakusen/internal/task"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

// maxFanoutSubRows caps the child sub-rows shown under one fanout step.
const maxFanoutSubRows = 20

type taskInfoView struct {
	task     *daemon.TaskInfo
	workflow *config.WorkflowConfig
	// fanoutChildren comes from get_task, not broadcasts, so it survives
	// SetTask for the same task ID.
	fanoutChildren []daemon.FanoutChildInfo
	viewport       viewport.Model
	width          int
	height         int
	ready          bool
	showHelp       bool // toggled by ctrl+h
}

func newTaskInfoView() taskInfoView {
	return taskInfoView{}
}

func (v *taskInfoView) SetTask(t *daemon.TaskInfo) {
	if v.task == nil || t == nil || v.task.ID != t.ID {
		v.fanoutChildren = nil
	}
	v.task = t
	v.updateContent()
}

func (v *taskInfoView) SetFanoutChildren(children []daemon.FanoutChildInfo) {
	v.fanoutChildren = children
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

	// Hibernated (tmux session killed while idle; attach wakes it)
	if t.HibernatedAt != nil {
		b.WriteString(labelStyle.Render("Hibernated: "))
		b.WriteString(dimStyle.Render(t.HibernatedAt.Format("2006-01-02 15:04:05")))
		b.WriteString("\n")
	}

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

	if t.WorkdirHeldBy != 0 {
		b.WriteString(labelStyle.Render("Workdir held by: "))
		b.WriteString(valueStyle.Render(fmt.Sprintf("#%d", t.WorkdirHeldBy)))
		b.WriteString("\n")
	}

	if t.NotBefore != nil {
		b.WriteString(labelStyle.Render("Not before: "))
		b.WriteString(valueStyle.Render(fmtRoutineTime(t.NotBefore)))
		b.WriteString("\n")
	}

	// Waiting on (child tasks this step is suspended for)
	if len(t.WaitsOn) > 0 {
		ids := make([]string, len(t.WaitsOn))
		for i, id := range t.WaitsOn {
			ids[i] = fmt.Sprintf("#%d", id)
		}
		b.WriteString(labelStyle.Render("Waiting on: "))
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
	if t.Workdir != "" {
		b.WriteString(labelStyle.Render("Workdir:   "))
		b.WriteString(dimStyle.Render(t.Workdir))
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
			if step.ExplicitAdvance() {
				suffix += " [advance:explicit]"
			}
			// Explicit agent assignments only — the default cascade is
			// surfaced via the daemon's list_workflows, not re-resolved here.
			if step.Agent != "" {
				suffix += fmt.Sprintf(" [agent:%s]", step.Agent)
			}
			if step.Loop != nil {
				suffix += fmt.Sprintf(" [loop→%s ×%d]", step.Loop.Goto, step.Loop.MaxIterations)
			}
			if step.IsParallel() {
				// K/N stays on the row after completion: how many branches
				// actually produced a review is the interesting fact, and it
				// is otherwise invisible once the group's icon turns ✓.
				isolation := ""
				if step.Parallel.IsIsolated() {
					isolation = ", isolation:worktree"
				}
				suffix += fmt.Sprintf(" [parallel %d/%d, require:%s%s]",
					completedBranches(&step, t.BranchStatus), len(step.Parallel.Branches),
					step.Parallel.EffectiveRequire(), isolation)
			}
			if step.IsFanout() {
				done, total := 0, 0
				for _, c := range v.fanoutChildren {
					if c.Step == step.Name {
						total++
						if c.Status == string(task.StatusCompleted) {
							done++
						}
					}
				}
				suffix += fmt.Sprintf(" [fanout %d/%d, require:%s →%s]",
					done, total, step.Fanout.EffectiveRequire(), step.Fanout.Workflow)
			}

			b.WriteString(style.Render(fmt.Sprintf("  %s %d. %s%s", icon, i+1, step.Name, suffix)))
			b.WriteString("\n")

			if step.IsFanout() {
				for _, row := range renderFanoutChildRows(step.Name, v.fanoutChildren) {
					b.WriteString(row)
					b.WriteString("\n")
				}
				continue
			}

			// Branch sub-rows. The current-step marker above lands on the
			// group row only; a branch's own state comes from BranchStatus,
			// which the daemon refreshes on every task update.
			if !step.IsParallel() {
				continue
			}
			for j := range step.Parallel.Branches {
				br := step.Parallel.EffectiveBranch(j, &step)
				status := t.BranchStatus[br.Name]
				// Explicit (branch or inherited-from-group) agents only, same
				// rule as the step rows above: this panel has no agent
				// registry, so it never re-resolves the default cascade.
				agentSuffix := ""
				if br.Agent != "" {
					agentSuffix = fmt.Sprintf(" [agent:%s]", br.Agent)
				}
				if wt, ok := t.BranchWorktrees[br.Name]; ok && step.Parallel.IsIsolated() {
					agentSuffix += fmt.Sprintf(" [wt:%s %s]", wt.State, filepath.Base(wt.Path))
				}
				b.WriteString(branchRowStyle(status).Render(
					fmt.Sprintf("      %s %s%s", branchIcon(status), br.Name, agentSuffix)))
				b.WriteString("\n")
			}
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

// completedBranches counts the group's branches whose BranchStatus is
// "completed" — the K of the K/N badge. A missing entry means the branch has
// no row yet (pending).
func completedBranches(group *config.StepConfig, statuses map[string]string) int {
	n := 0
	for _, br := range group.Parallel.Branches {
		if statuses[br.Name] == stepStatusCompleted {
			n++
		}
	}
	return n
}

// branchIcon maps a branch status to its sub-row glyph. An unknown/absent
// status is pending.
func branchIcon(status string) string {
	switch status {
	case stepStatusRunning:
		return "●"
	case stepStatusCompleted:
		return "✓"
	case stepStatusFailed:
		return "✗"
	default:
		return "○"
	}
}

// branchRowStyle colours a branch sub-row to match its glyph.
func branchRowStyle(status string) lipgloss.Style {
	switch status {
	case stepStatusRunning:
		return stateStyle(task.StatusRunning)
	case stepStatusCompleted:
		return stateStyle(task.StatusCompleted)
	case stepStatusFailed:
		return stateStyle(task.StatusFailed)
	default:
		return dimStyle
	}
}

func (v *taskInfoView) ScrollUp() {
	v.viewport.ScrollUp(1)
}

func (v *taskInfoView) ScrollDown() {
	v.viewport.ScrollDown(1)
}

func (v *taskInfoView) PageUp() {
	v.viewport.HalfPageUp()
}

func (v *taskInfoView) PageDown() {
	v.viewport.HalfPageDown()
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

// renderFanoutChildRows renders the sub-rows for one fanout step's children:
// failed first, then still-active, then completed, each group in its original
// order, capped at maxFanoutSubRows plus a "+N more" line.
func renderFanoutChildRows(step string, children []daemon.FanoutChildInfo) []string {
	var mine []daemon.FanoutChildInfo
	for _, c := range children {
		if c.Step == step {
			mine = append(mine, c)
		}
	}
	rank := func(status string) int {
		switch fanoutChildStepStatus(status) {
		case stepStatusFailed:
			return 0
		case stepStatusCompleted:
			return 2
		default:
			return 1
		}
	}
	sort.SliceStable(mine, func(i, j int) bool { return rank(mine[i].Status) < rank(mine[j].Status) })

	shown := mine
	if len(shown) > maxFanoutSubRows {
		shown = shown[:maxFanoutSubRows]
	}
	rows := make([]string, 0, len(shown)+1)
	for _, c := range shown {
		glyph := fanoutChildStepStatus(c.Status)
		rows = append(rows, branchRowStyle(glyph).Render(
			fmt.Sprintf("      %s %s #%d %s", branchIcon(glyph), c.Slug, c.TaskID, c.Status)))
	}
	if extra := len(mine) - len(shown); extra > 0 {
		rows = append(rows, dimStyle.Render(fmt.Sprintf("      +%d more", extra)))
	}
	return rows
}

// fanoutChildStepStatus maps a child task status onto the step-status
// vocabulary branchIcon and branchRowStyle understand.
func fanoutChildStepStatus(status string) string {
	switch task.Status(status) {
	case task.StatusCompleted:
		return stepStatusCompleted
	case task.StatusFailed, task.StatusMergeFailed:
		return stepStatusFailed
	case task.StatusPending, task.StatusInit:
		return ""
	default:
		return stepStatusRunning
	}
}
