package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func (m Model) handlePromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	pk := cachedPromptKeyMap

	// When help overlay is showing, only allow closing it
	if m.prompt.showHelp {
		if key.Matches(msg, pk.Help) || key.Matches(msg, pk.Cancel) {
			m.prompt.showHelp = false
		}
		return m, nil
	}

	// ── Shared keys (work identically in both task and workflow panes) ──
	switch {
	case key.Matches(msg, pk.Help):
		m.prompt.showHelp = true
		return m, nil

	case key.Matches(msg, pk.Cancel):
		m.continueTaskID = 0
		m.continueSelectedWorkflow = ""
		m.blockingTaskID = 0
		m.view = viewList
		return m, nil

	case key.Matches(msg, pk.SwitchField):
		m.prompt.SwitchFocus(true)
		return m, nil

	case key.Matches(msg, pk.SwitchFieldPrev):
		m.prompt.SwitchFocus(false)
		return m, nil

	case key.Matches(msg, pk.FocusTitle):
		m.prompt.FocusOn(promptFieldTitle)
		return m, nil

	case key.Matches(msg, pk.FocusInput):
		m.prompt.FocusOn(promptFieldInput)
		return m, nil

	case key.Matches(msg, pk.FocusGit):
		m.prompt.FocusGitSection()
		return m, nil

	case key.Matches(msg, pk.FocusWorkflow):
		m.prompt.FocusWorkflowPane()
		return m, nil

	case key.Matches(msg, pk.Worktree):
		m.prompt.ToggleWorktree()
		return m, nil

	case key.Matches(msg, pk.BranchMode):
		if m.prompt.worktree {
			m.prompt.ToggleBranchMode()
		}
		return m, nil

	case key.Matches(msg, pk.SwitchPane):
		m.prompt.CyclePane(true)
		return m, nil

	case key.Matches(msg, pk.SwitchPanePrev):
		m.prompt.CyclePane(false)
		return m, nil

	}

	// ── Pane-specific keys ──
	if m.prompt.activePane == paneWorkflow {
		return m.handleWorkflowPaneKey(msg)
	}
	return m.handleTaskPaneKey(msg)
}

// handleTaskPaneKey handles keys specific to the task pane (title, description, git fields).
func (m Model) handleTaskPaneKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	pk := cachedPromptKeyMap

	switch {
	case key.Matches(msg, pk.Submit):
		return m.handlePromptSubmit()

	case key.Matches(msg, pk.Editor):
		if m.prompt.focusField == promptFieldInput {
			return m, m.openEditorForPrompt()
		}
		cmd := m.prompt.Update(msg)
		return m, cmd

	case key.Matches(msg, pk.RemoveImage):
		m.prompt.RemoveLastImage()
		return m, nil

	default:
		m.prompt.validationError = ""
		cmd := m.prompt.Update(msg)
		return m, cmd
	}
}

// handlePromptSubmit handles the enter key to submit a new task or continue an existing one.
func (m Model) handlePromptSubmit() (tea.Model, tea.Cmd) {
	description := m.prompt.Value()

	// Continue mode
	if m.continueTaskID != 0 && m.continueSelectedWorkflow != "" {
		taskID := m.continueTaskID
		workflow := m.continueSelectedWorkflow
		m.continueTaskID = 0
		m.continueSelectedWorkflow = ""
		deferred := m.continueTask(taskID, workflow, description)
		if m.animationEnabled() {
			positions := m.planePositions(description)
			m.sortie = newSortieAnimation(positions, m.width, m.height, m.animationDuration())
			m.sortieCmd = deferred
			m.view = viewSortie
			return m, sortieTickCmd()
		}
		m.view = viewList
		return m, deferred
	}

	// New task mode
	checkoutBranch := m.prompt.CheckoutBranch()
	if description == "" && checkoutBranch == "" && !m.selectedWorkflowAllowsEmptyInput() {
		m.prompt.validationError = "description required"
		return m, nil
	}
	title := m.prompt.TitleValue()
	images := m.prompt.Images()
	branchName := m.prompt.BranchName()
	targetBranch := m.prompt.TargetBranch()
	worktree := m.prompt.Worktree()

	// Persist the current form selections as the new defaults for this session
	m.defaultWorktree = worktree
	m.defaultBranchMode = int(m.prompt.branchMode)
	m.defaultWorkflow = m.selectedWorkflow
	m.prompt.defaultWorkflow = m.defaultWorkflow

	deferred := m.createTaskWithPrompt(title, description, branchName, worktree, images, targetBranch, checkoutBranch)
	if m.animationEnabled() {
		positions := m.planePositions(description)
		m.sortie = newSortieAnimation(positions, m.width, m.height, m.animationDuration())
		m.sortieCmd = deferred
		m.view = viewSortie
		return m, sortieTickCmd()
	}
	m.view = viewList
	return m, deferred
}

// selectedWorkflowAllowsEmptyInput returns true when the workflow currently
// selected in the prompt pins an input (the pin supplies it) or has tmux as
// its first step (the user drives the session manually). Either case lets the
// user submit without typing an input.
func (m Model) selectedWorkflowAllowsEmptyInput() bool {
	if m.cfg == nil {
		return false
	}
	wf := m.cfg.GetWorkflow(m.selectedWorkflow)
	if wf == nil {
		return false
	}
	return wf.Input != "" || wf.FirstStepIsTmux()
}

// animationEnabled returns true if the sortie animation is configured on.
// Enabled by default; can be disabled via options.animation.enabled: false in config.
func (m Model) animationEnabled() bool {
	if m.width == 0 || m.height == 0 {
		return false
	}
	if m.cfg == nil || m.cfg.Options.Animation == nil || m.cfg.Options.Animation.Enabled == nil {
		return true
	}
	return *m.cfg.Options.Animation.Enabled
}

// animationDuration returns the configured animation duration in milliseconds,
// defaulting to 1500ms.
func (m Model) animationDuration() int {
	if m.cfg != nil && m.cfg.Options.Animation != nil && m.cfg.Options.Animation.Duration != nil {
		d := *m.cfg.Options.Animation.Duration
		if d > 0 {
			return d
		}
	}
	return 1500
}

// planePositions returns the screen coordinates of each ✈ prompt prefix
// in the prompt view, so the animation can keep planes in their exact spots.
// Layout: title(1) + blank(1) + "Title: " input(1) + blank(1) = row 4 is the first textarea line.
// Each textarea line's ✈ sits at column 4 (2 padding + prompt char position).
// Soft-wrapped continuation lines also get a plane.
func (m Model) planePositions(description string) [][2]int {
	const (
		textareaStartRow = 4 // rows above the textarea in prompt view
		planeCol         = 2 // left padding where ✈ renders
		maxPlanes        = 12
	)

	if description == "" {
		return [][2]int{{planeCol, textareaStartRow}}
	}

	// Calculate content width matching promptView.visualLineCount() logic
	promptWidth := lipgloss.Width(PromptPrefix)
	contentWidth := (m.width - 4) - promptWidth
	if contentWidth < 1 {
		contentWidth = 1
	}

	var positions [][2]int
	row := textareaStartRow
	for _, line := range strings.Split(description, "\n") {
		lineWidth := lipgloss.Width(line)
		if lineWidth == 0 {
			positions = append(positions, [2]int{planeCol, row})
			row++
		} else {
			visualLines := (lineWidth + contentWidth - 1) / contentWidth
			for v := 0; v < visualLines; v++ {
				positions = append(positions, [2]int{planeCol, row})
				row++
				if len(positions) >= maxPlanes {
					return positions
				}
			}
		}
	}

	if len(positions) == 0 {
		positions = append(positions, [2]int{planeCol, textareaStartRow})
	}
	return positions
}

// handleWorkflowPaneKey handles keys unique to the workflow pane (list navigation).
// Shared keys (focus, toggles, tab, cancel, help) are handled in handlePromptKey.
func (m Model) handleWorkflowPaneKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	pk := cachedPromptKeyMap

	// Enter from workflow pane: confirm selection and return to task pane
	if key.Matches(msg, pk.Submit) {
		m.prompt.activePane = paneTask
		m.prompt.Focus()
		return m, nil
	}

	keyStr := msg.String()
	idx := m.prompt.workflowCursor
	switch keyStr {
	case "j", "down":
		if m.prompt.workflowCursor < len(m.prompt.workflows)-1 {
			idx = m.prompt.workflowCursor + 1
		}
	case "k", "up":
		if m.prompt.workflowCursor > 0 {
			idx = m.prompt.workflowCursor - 1
		}
	case "G":
		idx = max(0, len(m.prompt.workflows)-1)
	default:
		// Number keys for quick selection (1-9)
		if len(keyStr) == 1 && keyStr[0] >= '1' && keyStr[0] <= '9' {
			n := int(keyStr[0] - '1')
			if n < len(m.prompt.workflows) {
				idx = n
			}
		}
	}

	if idx != m.prompt.workflowCursor && idx >= 0 && idx < len(m.prompt.workflows) {
		m = m.cycleToWorkflow(idx)
	}

	return m, nil
}

// cycleToWorkflow selects the workflow at idx in the prompt's cycler, repopulates
// any pinned fields via applyPins, and keeps focus in the workflow pane (never
// auto-creating, even if the new workflow is fully pinned — see the skip rule).
func (m Model) cycleToWorkflow(idx int) Model {
	m.prompt.workflowCursor = idx
	m.prompt.workflowName = m.prompt.workflows[idx]
	m.selectedWorkflow = m.prompt.workflowName
	// applyPins(nil) still restores any values displaced by the previous
	// workflow's pins before clearing the pin flags.
	m.prompt.applyPins(m.cfg.GetTaskWorkflow(m.selectedWorkflow))
	// applyPins moves focus into the task pane; restore the workflow pane so the
	// user can keep cycling, and resize because hidden fields change the layout.
	m.prompt.blurAll()
	m.prompt.activePane = paneWorkflow
	m.prompt.SetSize(m.width, m.height)
	return m
}
