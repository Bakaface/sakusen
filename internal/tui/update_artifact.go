package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/Bakaface/sakusen/internal/task"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type taskStepsLoadedMsg struct {
	taskID int64
	steps  []daemon.TaskStepDetail
	action string
}

const (
	stepStatusPending   = "pending"
	stepStatusRunning   = "running"
	stepStatusCompleted = "completed"
)

func (m Model) openArtifactSelection(task *daemon.TaskInfo, action string) (tea.Model, tea.Cmd) {
	if m.client == nil {
		m.err = fmt.Errorf("not connected to daemon")
		return m, nil
	}

	taskID := task.ID
	return m, func() tea.Msg {
		steps, err := m.client.GetTaskSteps(taskID)
		if err != nil {
			return errorMsg(fmt.Errorf("failed to get task steps: %w", err))
		}
		if len(steps) == 0 {
			return errorMsg(fmt.Errorf("task #%d has no workflow steps", taskID))
		}
		return taskStepsLoadedMsg{
			taskID: taskID,
			steps:  steps,
			action: action,
		}
	}
}

// openRetryStepSelection loads the task's workflow steps and arranges for the
// caller to be shown a retry-step picker. The picker is bypassed when the
// workflow only has a single step (handled in the taskStepsLoadedMsg handler).
func (m Model) openRetryStepSelection(task *daemon.TaskInfo) (tea.Model, tea.Cmd) {
	return m.openArtifactSelection(task, "retry")
}

// stepIsActionable returns true when a step row can be opened (viewed or edited).
// Pending rows are non-actionable; completed-but-empty rows are viewable
// (showing a placeholder) but not editable — actionability covers viewing only.
func stepIsActionable(step daemon.TaskStepDetail) bool {
	return step.Status != stepStatusPending
}

// stepIsEditable returns true when a step's context may be edited.
func stepIsEditable(step daemon.TaskStepDetail) bool {
	return step.Status == stepStatusCompleted && step.Context != ""
}

func (m Model) performArtifactAction(taskID int64, stepName, action string) (tea.Model, tea.Cmd) {
	step, ok := m.lookupStep(stepName)
	if !ok {
		m.err = fmt.Errorf("step %q not found", stepName)
		return m, nil
	}

	if action == "edit" {
		if !stepIsEditable(step) {
			m.statusMessage = fmt.Sprintf("step %q is not editable yet", stepName)
			m.statusMessageTTL = 3
			return m, nil
		}
		return m, m.openEditorForStepContext(taskID, stepName, step.Context)
	}

	// View action
	m.artifactView.SetContent(stepName, renderStepBody(step))
	m.artifactView.editable = stepIsEditable(step)
	m.artifactView.taskID = taskID
	m.view = viewArtifact
	return m, nil
}

// lookupStep returns the cached step detail by name (post-load, post-selector).
func (m Model) lookupStep(name string) (daemon.TaskStepDetail, bool) {
	for _, s := range m.taskSteps {
		if s.Name == name {
			return s, true
		}
	}
	return daemon.TaskStepDetail{}, false
}

// renderStepBody returns the viewer body for a step, including placeholders
// for non-completed states so the viewer can still surface the row.
func renderStepBody(step daemon.TaskStepDetail) string {
	switch step.Status {
	case stepStatusCompleted:
		if step.Context == "" {
			return "(no context captured for this step)"
		}
		return step.Context
	case stepStatusRunning:
		return "⟳ Step in progress — context will be captured when it completes."
	default:
		return "· Step has not started yet."
	}
}

// stepSelectorLabel returns the displayed item text for a step row in the
// generic selector. Format: "<glyph> <name>[ (state)]".
func stepSelectorLabel(step daemon.TaskStepDetail) string {
	switch step.Status {
	case stepStatusCompleted:
		if step.Context == "" {
			return "✗ " + step.Name + " (empty)"
		}
		return "✓ " + step.Name
	case stepStatusRunning:
		return "⟳ " + step.Name + " (running)"
	default:
		return "· " + step.Name + " (pending)"
	}
}

// stepSelectorDescription returns the dim sub-line shown beneath the
// currently highlighted row in the selector.
func stepSelectorDescription(step daemon.TaskStepDetail) string {
	switch step.Status {
	case stepStatusCompleted:
		if step.Context == "" {
			return "no context captured"
		}
		size := formatByteSize(len(step.Context))
		if step.CompletedAt != nil {
			return fmt.Sprintf("completed %s · %s", formatRelativeTime(*step.CompletedAt), size)
		}
		return size
	case stepStatusRunning:
		return "in progress"
	default:
		return ""
	}
}

// stepSelectorItemStyle returns the per-row color for non-highlighted rows.
// The selector applies this style after the "N. " number prefix is added.
func stepSelectorItemStyle(label string) lipgloss.Style {
	trimmed := strings.TrimSpace(label)
	// The selector prepends "  N. " — skip past it to find the glyph.
	if idx := strings.Index(trimmed, ". "); idx >= 0 {
		trimmed = trimmed[idx+2:]
	}
	if strings.HasPrefix(trimmed, "·") || strings.HasPrefix(trimmed, "✗") {
		return dimStyle
	}
	if strings.HasPrefix(trimmed, "⟳") {
		return stateStyles[task.StatusRunning]
	}
	return normalStyle
}

// actionableSteps returns only steps that can be opened in the viewer
// (anything that's not "pending").
func actionableSteps(steps []daemon.TaskStepDetail) []daemon.TaskStepDetail {
	out := make([]daemon.TaskStepDetail, 0, len(steps))
	for _, s := range steps {
		if stepIsActionable(s) {
			out = append(out, s)
		}
	}
	return out
}

// retryStepSelectorLabel returns the displayed item text for a step row in
// the retry-step picker. Same glyph vocabulary as stepSelectorLabel but
// without the artifact-specific "(empty)" annotation: every step is a valid
// retry target regardless of whether it has captured context.
func retryStepSelectorLabel(step daemon.TaskStepDetail) string {
	switch step.Status {
	case stepStatusCompleted:
		return "✓ " + step.Name
	case stepStatusRunning:
		return "⟳ " + step.Name + " (interrupted)"
	default:
		return "· " + step.Name + " (not yet run)"
	}
}

// retryStepSelectorDescription returns the dim sub-line shown beneath the
// currently highlighted row in the retry-step picker.
func retryStepSelectorDescription(step daemon.TaskStepDetail) string {
	switch step.Status {
	case stepStatusCompleted:
		if step.CompletedAt != nil {
			return fmt.Sprintf("completed %s · earlier steps preserved", formatRelativeTime(*step.CompletedAt))
		}
		return "completed · earlier steps preserved"
	case stepStatusRunning:
		return "was running when task stopped"
	default:
		return "skips ahead; earlier steps will not run"
	}
}

// retryStepCursor picks the default selection for the retry-step picker.
// Heuristic: prefer the step the task was last on (current_step or the
// running/failed step from task_steps); otherwise the last completed step;
// otherwise the first row.
func retryStepCursor(t *daemon.TaskInfo, steps []daemon.TaskStepDetail) int {
	if t != nil && t.CurrentStep != "" {
		for i, s := range steps {
			if s.Name == t.CurrentStep {
				return i
			}
		}
	}
	for i, s := range steps {
		if s.Status == stepStatusRunning {
			return i
		}
	}
	lastCompleted := -1
	for i, s := range steps {
		if s.Status == stepStatusCompleted {
			lastCompleted = i
		}
	}
	if lastCompleted >= 0 {
		return lastCompleted
	}
	return 0
}

func formatByteSize(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func formatRelativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func (m Model) handleArtifactViewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	ak := cachedArtifactViewKeyMap

	// Help overlay — ctrl+h toggles, ctrl+h or esc/q dismisses.
	if m.artifactView.showHelp {
		if key.Matches(msg, ak.Help) || key.Matches(msg, ak.Back) {
			m.artifactView.showHelp = false
		}
		return m, nil
	}
	if key.Matches(msg, ak.Help) {
		m.artifactView.showHelp = true
		return m, nil
	}

	switch {
	case key.Matches(msg, ak.Back): // "q", "esc"
		m.view = viewList
		return m, nil
	case key.Matches(msg, ak.Edit): // "e"
		if !m.artifactView.editable {
			m.statusMessage = "step context not editable"
			m.statusMessageTTL = 2
			return m, nil
		}
		step, ok := m.lookupStep(m.artifactView.name)
		if !ok {
			m.err = fmt.Errorf("step %q not found", m.artifactView.name)
			return m, nil
		}
		return m, m.openEditorForStepContext(m.artifactView.taskID, m.artifactView.name, step.Context)
	}

	// Handle "gg" sequence
	if key.Matches(msg, ak.GotoTop) {
		if m.artifactView.pendingG {
			m.artifactView.pendingG = false
			m.artifactView.GotoTop()
			return m, nil
		}
		m.artifactView.pendingG = true
		return m, nil
	}
	m.artifactView.pendingG = false

	switch {
	case key.Matches(msg, ak.GotoBottom): // "G"
		m.artifactView.GotoBottom()
		return m, nil
	case key.Matches(msg, ak.Down): // "j", "down"
		m.artifactView.ScrollDown()
		return m, nil
	case key.Matches(msg, ak.Up): // "k", "up"
		m.artifactView.ScrollUp()
		return m, nil
	case key.Matches(msg, ak.HalfDown): // "ctrl+d", "pgdown"
		m.artifactView.PageDown()
		return m, nil
	case key.Matches(msg, ak.HalfUp): // "ctrl+u", "pgup"
		m.artifactView.PageUp()
		return m, nil
	}

	return m, nil
}
