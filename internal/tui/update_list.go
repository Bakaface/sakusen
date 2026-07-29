package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/daemon"
	// Aliased: this file uses "task" as the local variable name for
	// *daemon.TaskInfo throughout (e.g. "if task := m.list.Selected(); ...");
	// the alias avoids that shadowing the package.
	taskpkg "github.com/Bakaface/sortie/internal/task"
	"github.com/Bakaface/sortie/internal/workflow"
)

// tmuxContinueAction returns "advance" when the tmux step has more workflow
// steps after it, "finalize" otherwise. Both dispatch to the same AdvanceTask
// RPC — the daemon does the smart routing. The label affects only the
// confirmation prompt the user sees.
func tmuxContinueAction(cfg *config.Config, task *daemon.TaskInfo) string {
	if cfg == nil || task == nil {
		return "finalize"
	}
	wf := cfg.GetWorkflow(task.Workflow)
	// Delegate to workflow.HasMoreSteps — the sanctioned StepIndex
	// arithmetic (see internal/workflow/cursor.go) — instead of re-deriving
	// "does this task have steps left" here. daemon.TaskInfo only carries
	// the wire-level StepIndex, so wrap it in a minimal task.Task; the
	// function only reads that one field.
	if workflow.HasMoreSteps(&taskpkg.Task{StepIndex: task.StepIndex}, wf) {
		return "advance"
	}
	return "finalize"
}

// deleteWordBackward removes the last word from s, mimicking ctrl+backspace behavior.
// It first strips trailing whitespace, then removes non-whitespace characters.
func deleteWordBackward(s string) string {
	// Trim trailing spaces
	trimmed := strings.TrimRight(s, " ")
	if trimmed == "" {
		return ""
	}
	// Find the last space in the trimmed string
	lastSpace := strings.LastIndex(trimmed, " ")
	if lastSpace == -1 {
		return ""
	}
	return trimmed[:lastSpace+1]
}

func (m Model) handleListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Handle search mode input
	if m.searchMode {
		return m.handleSearchKey(msg)
	}

	// Handle command mode input
	if m.commandMode {
		return m.handleCommandKey(msg)
	}

	// Handle generic selection dialog if active
	if m.selector.IsActive() {
		return m.handleSelectorKey(msg)
	}

	// Handle branch selection if active
	if m.selectingBranch {
		return m.handleBranchSelectKey(msg)
	}

	// Handle confirmation prompt if active
	if m.confirmAction != "" {
		return m.handleConfirmKey(msg)
	}

	keyStr := msg.String()

	// Handle help overlay — consume all keys except ctrl+h and esc which dismiss it
	if m.list.showHelp {
		if key.Matches(msg, m.keys.Help) || key.Matches(msg, m.keys.Back) {
			m.list.showHelp = false
			return m, nil
		}
		return m, nil
	}

	// Handle chord sequences (dd, gg, oa, ea, ed, et, ec)
	if ret, cmd, handled := m.tryChord(keyStr); handled {
		return ret, cmd
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		m.quitting = true
		if m.client != nil {
			m.client.Close()
		}
		return m, tea.Quit

	case key.Matches(msg, m.keys.Up):
		m.list.MoveUp()
		return m, nil

	case key.Matches(msg, m.keys.Down):
		m.list.MoveDown()
		return m, nil

	case key.Matches(msg, m.keys.GotoBottom):
		m.list.GotoBottom()
		return m, nil

	case key.Matches(msg, m.keys.PageDown):
		m.list.PageDown()
		return m, nil

	case key.Matches(msg, m.keys.PageUp):
		m.list.PageUp()
		return m, nil

	case key.Matches(msg, m.keys.Enter):
		if task := m.list.Selected(); task != nil {
			m.view = viewTaskInfo
			m.taskInfo.SetTask(task)
			m.taskInfo.SetWorkflow(m.cfg.GetWorkflow(task.Workflow))
			return m, nil
		}
		return m, nil

	case key.Matches(msg, m.keys.Logs):
		if task := m.list.Selected(); task != nil {
			m.view = viewDetail
			m.detail.SetTask(task)
			m.detail.SetFollowMode(true)
			m.logStream.reset(task.ID)
			taskID, offset := m.logStream.nextRequest()
			return m, m.loadOutput(taskID, offset)
		}
		return m, nil

	case key.Matches(msg, m.keys.Retry):
		// Retry if selected task is failed, merge-failed, completed, or stale
		// tmux. Instead of asking yes/no, load the workflow steps and let the
		// user pick which step to restart from. Single-step workflows skip the
		// picker entirely (handled in the taskStepsLoadedMsg handler).
		if task := m.list.Selected(); task != nil && m.client != nil {
			status := taskpkg.Status(task.Status)
			if status == taskpkg.StatusFailed || status == taskpkg.StatusMergeFailed || status == taskpkg.StatusCompleted || status == taskpkg.StatusTmux {
				return m.openRetryStepSelection(task)
			}
		}

	case key.Matches(msg, m.keys.Revert):
		if task := m.list.Selected(); task != nil && m.client != nil {
			if taskpkg.Status(task.Status) == taskpkg.StatusCompleted || taskpkg.Status(task.Status) == taskpkg.StatusFailed {
				m.confirmAction = "revert"
				m.confirmTaskID = task.ID
				return m, nil
			}
		}
		return m, nil

	case key.Matches(msg, m.keys.Stop):
		if task := m.list.Selected(); task != nil && m.client != nil {
			m.confirmAction = "stop"
			m.confirmTaskID = task.ID
			return m, nil
		}
		return m, nil

	case key.Matches(msg, m.keys.Attach):
		if task := m.list.Selected(); task != nil {
			return m, m.attachTmuxSession(task.ID)
		}
		return m, nil

	case key.Matches(msg, m.keys.Continue):
		if task := m.list.Selected(); task != nil && m.client != nil {
			status := taskpkg.Status(task.Status)
			if status == taskpkg.StatusAwaitingApproval {
				m.confirmAction = "continue"
				m.confirmTaskID = task.ID
				return m, nil
			}
			if status.IsTerminal() {
				workflows := m.cfg.ListWorkflowNames()
				m.continueTaskID = task.ID
				m.continueSelectedWorkflow = ""
				if len(workflows) > 0 {
					m.continueSelectedWorkflow = workflows[0]
				}
				m.view = viewPrompt
				m.prompt.Reset()
				m.prompt.workflowName = m.continueSelectedWorkflow
				m.prompt.workflows = workflows
				m.prompt.workflowCursor = 0
				m.prompt.SetSize(m.width, m.height)
				m.prompt.Focus()
				return m, nil
			}
			if status == taskpkg.StatusTmux {
				m.confirmAction = tmuxContinueAction(m.cfg, task)
				m.confirmTaskID = task.ID
				return m, nil
			}
		}
		return m, nil

	case key.Matches(msg, m.keys.ChangePriority):
		if task := m.list.Selected(); task != nil && m.client != nil {
			m.selector = selector{
				kind:      selectorPriority,
				title:     "Select Priority",
				items:     []string{"low", "medium", "high", "urgent"},
				itemStyle: func(name string) lipgloss.Style { return priorityStyle(name) },
				hint:      "j/k: navigate | enter: select | 1-4: quick select | esc: cancel",
				taskID:    task.ID,
			}
			return m, nil
		}
		return m, nil

	case key.Matches(msg, m.keys.NewTask):
		// If search is active, "n" navigates to next match
		if len(m.list.matchedIndices) > 0 {
			m.list.nextMatch(m.searchDirection)
			return m, nil
		}
		// Otherwise, "n" is new task
		if m.client == nil || m.projectPath == "" {
			return m, nil
		}
		workflows := m.cfg.ListWorkflowNames()
		m.selectedWorkflow = m.resolveDefaultWorkflow(workflows)
		wf := m.cfg.GetTaskWorkflow(m.selectedWorkflow)
		// Fully-pinned workflow → skip the screen and create immediately.
		if wf != nil && wf.IsFullySpec() {
			return m, m.createTaskWithPrompt("", wf.Description, wf.Branch, *wf.Worktree, nil, wf.Target, wf.Checkout)
		}
		m.view = viewPrompt
		m.prompt.defaultWorkflow = m.defaultWorkflow
		m.prompt.Reset()
		m.prompt.workflowName = m.selectedWorkflow
		m.prompt.workflows = workflows
		m.prompt.workflowCursor = m.prompt.defaultWorkflowCursor()
		if wf != nil {
			m.prompt.applyPins(wf)
		}
		m.prompt.SetSize(m.width, m.height) // recalc widths for split layout
		m.prompt.Focus()
		return m, nil

	case key.Matches(msg, m.keys.NewBlockingTask):
		// Previous match (opposite direction) if search is active
		if len(m.list.matchedIndices) > 0 {
			m.list.previousMatch(m.searchDirection)
			return m, nil
		}
		// Otherwise, create a new task that blocks the selected task
		if m.client == nil || m.projectPath == "" {
			return m, nil
		}
		task := m.list.Selected()
		if task == nil {
			return m, nil
		}
		m.blockingTaskID = task.ID
		workflows := m.cfg.ListWorkflowNames()
		m.selectedWorkflow = m.resolveDefaultWorkflow(workflows)
		wf := m.cfg.GetTaskWorkflow(m.selectedWorkflow)
		// Fully-pinned workflow → create the blocking task immediately
		// (m.blockingTaskID is threaded through createTaskWithPrompt).
		if wf != nil && wf.IsFullySpec() {
			return m, m.createTaskWithPrompt("", wf.Description, wf.Branch, *wf.Worktree, nil, wf.Target, wf.Checkout)
		}
		m.view = viewPrompt
		m.prompt.defaultWorkflow = m.defaultWorkflow
		m.prompt.Reset()
		m.prompt.workflowName = m.selectedWorkflow
		m.prompt.workflows = workflows
		m.prompt.workflowCursor = m.prompt.defaultWorkflowCursor()
		if wf != nil {
			m.prompt.applyPins(wf)
		}
		m.prompt.blockingTaskID = m.blockingTaskID
		m.prompt.blockingTaskTitle = m.blockingTaskTitleFromList(m.blockingTaskID)
		m.prompt.SetSize(m.width, m.height)
		m.prompt.Focus()
		return m, nil

	case key.Matches(msg, m.keys.DetachBranch):
		if task := m.list.Selected(); task != nil && m.client != nil {
			if task.Worktree && task.WorktreePath != "" && !task.WorktreeDetached {
				return m, m.detachBranch(task.ID)
			}
		}
		return m, nil

	case key.Matches(msg, m.keys.AttachBranch):
		if task := m.list.Selected(); task != nil && m.client != nil {
			if task.Worktree && task.WorktreePath != "" && task.WorktreeDetached {
				return m, m.attachBranch(task.ID)
			}
		}
		return m, nil

	case key.Matches(msg, m.keys.ToggleBranchView):
		m.list.branchView = !m.list.branchView
		if m.list.branchView {
			m.list.showBranch = true
		}
		m.list.applyFilter()
		return m, nil

	case key.Matches(msg, m.keys.BranchTask):
		if m.client != nil && m.projectPath != "" {
			return m, m.loadLocalBranches()
		}
		return m, nil

	case key.Matches(msg, m.keys.Help):
		m.list.showHelp = !m.list.showHelp
		return m, nil

	case key.Matches(msg, m.keys.SearchForward):
		m.searchMode = true
		m.searchQuery = ""
		m.searchDirection = 1 // forward search
		return m, nil

	case key.Matches(msg, m.keys.SearchBackward):
		m.searchMode = true
		m.searchQuery = ""
		m.searchDirection = -1 // backward search
		return m, nil

	case key.Matches(msg, m.keys.GotoTask):
		m.commandMode = true
		m.commandInput = ""
		return m, nil

	case key.Matches(msg, m.keys.Back):
		// Clear search highlights (like :noh in vim)
		m.list.matchedIndices = nil
		m.list.currentMatchIdx = 0
		m.searchQuery = ""
		return m, nil
	}

	return m, nil
}

func (m Model) handleCommandKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	keyStr := msg.String()

	switch keyStr {
	case "esc":
		m.commandMode = false
		m.commandInput = ""
		m.commandHistory.Reset()
		return m, nil

	case "enter":
		input := m.commandInput
		m.commandMode = false
		m.commandInput = ""
		m.commandHistory.Push(input)
		return executeCommand(m, input)

	case "tab":
		if completed, ok := completeRunTask(m, m.commandInput); ok {
			m.commandInput = completed
		}
		return m, nil

	case "up", "ctrl+p":
		if entry, ok := m.commandHistory.Up(m.commandInput); ok {
			m.commandInput = entry
		}
		return m, nil

	case "down", "ctrl+n":
		if entry, ok := m.commandHistory.Down(m.commandInput); ok {
			m.commandInput = entry
		}
		return m, nil

	case "ctrl+w":
		m.commandInput = deleteWordBackward(m.commandInput)
		if m.commandInput == "" {
			m.commandMode = false
		}
		return m, nil

	case "backspace":
		if len(m.commandInput) > 0 {
			m.commandInput = m.commandInput[:len(m.commandInput)-1]
		}
		if m.commandInput == "" {
			m.commandMode = false
		}
		return m, nil

	default:
		// Only accept printable characters
		if len(keyStr) == 1 && keyStr[0] >= ' ' && keyStr[0] <= '~' {
			m.commandInput += keyStr
		}
		return m, nil
	}
}

func (m Model) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	keyStr := msg.String()

	switch keyStr {
	case "esc":
		m.searchMode = false
		m.searchQuery = ""
		m.searchHistory.Reset()
		return m, nil

	case "enter":
		query := m.searchQuery
		direction := m.searchDirection
		m.searchMode = false
		m.searchHistory.Push(query)
		if query != "" {
			m.list.performSearchAndJump(query, m.list.table.Cursor(), direction)
		}
		return m, nil

	case "up", "ctrl+p":
		if entry, ok := m.searchHistory.Up(m.searchQuery); ok {
			m.searchQuery = entry
		}
		return m, nil

	case "down", "ctrl+n":
		if entry, ok := m.searchHistory.Down(m.searchQuery); ok {
			m.searchQuery = entry
		}
		return m, nil

	case "ctrl+w":
		m.searchQuery = deleteWordBackward(m.searchQuery)
		if m.searchQuery == "" {
			m.searchMode = false
		}
		return m, nil

	case "backspace":
		if len(m.searchQuery) > 0 {
			m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
		}
		if m.searchQuery == "" {
			m.searchMode = false
		}
		return m, nil

	default:
		// Only accept printable characters
		if len(keyStr) == 1 && keyStr[0] >= ' ' && keyStr[0] <= '~' {
			m.searchQuery += keyStr
		}
		return m, nil
	}
}

// handleSelectorKey dispatches key events to the generic selector and handles the result.
func (m Model) handleSelectorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	result := m.selector.HandleKey(msg.String())
	switch result {
	case selChosen:
		return m.handleSelectorChoice()
	case selCancelled:
		return m.handleSelectorCancel()
	}
	return m, nil
}

// handleSelectorChoice dispatches the selected item based on selector kind.
func (m Model) handleSelectorChoice() (tea.Model, tea.Cmd) {
	item := m.selector.Selected()
	kind := m.selector.kind
	taskID := m.selector.taskID
	action := m.selector.action
	cursor := m.selector.cursor

	// For step-list selectors (artifact view and retry-from-step) the items
	// carry decoration (glyphs/state); look up the bare step name from the
	// parallel taskSteps slice before resetting.
	if (kind == selectorArtifact || kind == selectorRetryStep) && cursor >= 0 && cursor < len(m.taskSteps) {
		item = m.taskSteps[cursor].Name
	}
	m.selector.Reset()

	switch kind {
	case selectorPriority:
		return m, m.updateTaskPriority(taskID, item)

	case selectorArtifact:
		return m.performArtifactAction(taskID, item, action)

	case selectorRetryStep:
		return m, m.retryTask(taskID, item)

	case selectorWorkflow:
		return m.launchWorkflow(item)
	}

	return m, nil
}

// launchWorkflow starts a task for the named workflow. When the workflow pins
// every New Task field it is created immediately; otherwise the New Task prompt
// opens with the workflow preselected (cycler locked to this one name) and any
// pinned fields populated and hidden.
func (m Model) launchWorkflow(wfName string) (tea.Model, tea.Cmd) {
	if m.client == nil || m.projectPath == "" {
		return m, nil
	}
	wf := m.cfg.GetTaskWorkflow(wfName)
	if wf == nil {
		m.err = fmt.Errorf("unknown workflow: %s", wfName)
		return m, nil
	}
	m.selectedWorkflow = wfName

	if wf.IsFullySpec() {
		return m, m.createTaskWithPrompt("", wf.Description, wf.Branch, *wf.Worktree, nil, wf.Target, wf.Checkout)
	}

	m.view = viewPrompt
	m.prompt.defaultWorkflow = m.defaultWorkflow
	m.prompt.Reset()
	m.prompt.preselectedWorkflow = wfName
	m.prompt.workflowName = wfName
	m.prompt.workflows = []string{wfName}
	m.prompt.workflowCursor = 0
	m.prompt.applyPins(wf)
	m.prompt.SetSize(m.width, m.height)
	m.prompt.Focus()
	return m, nil
}

// handleSelectorCancel handles cleanup when a selection is cancelled.
func (m Model) handleSelectorCancel() (tea.Model, tea.Cmd) {
	m.selector.Reset()
	return m, nil
}

func (m Model) handleBranchSelectKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	keyStr := msg.String()

	// Handle "gg" sequence for go-to-top
	if keyStr == "g" {
		if m.branchPendingG {
			m.branchPendingG = false
			m.branchCursor = 0
			return m, nil
		}
		m.branchPendingG = true
		return m, nil
	}
	m.branchPendingG = false

	switch keyStr {
	case "up", "ctrl+k", "ctrl+p":
		if m.branchCursor > 0 {
			m.branchCursor--
		}
		return m, nil
	case "down", "ctrl+j", "ctrl+n":
		if m.branchCursor < len(m.branchFiltered)-1 {
			m.branchCursor++
		}
		return m, nil
	case "G":
		m.branchCursor = max(0, len(m.branchFiltered)-1)
		return m, nil
	case "ctrl+d", "pgdown":
		half := max(1, len(m.branchFiltered)/2)
		m.branchCursor = min(m.branchCursor+half, len(m.branchFiltered)-1)
		return m, nil
	case "ctrl+u", "pgup":
		half := max(1, len(m.branchFiltered)/2)
		m.branchCursor = max(m.branchCursor-half, 0)
		return m, nil
	case "enter":
		if len(m.branchFiltered) > 0 {
			branch := m.branchFiltered[m.branchCursor]
			m.selectingBranch = false
			m.branchFilter = ""
			return m, m.createBranchTask(branch)
		}
		return m, nil
	case "esc":
		m.selectingBranch = false
		m.branchFilter = ""
		return m, nil
	case "backspace":
		if len(m.branchFilter) > 0 {
			m.branchFilter = m.branchFilter[:len(m.branchFilter)-1]
			m.branchFiltered = fuzzyFilterBranches(m.branchList, m.branchFilter)
			m.branchCursor = 0
		}
		return m, nil
	default:
		// Accept printable characters for fuzzy search
		if len(keyStr) == 1 && keyStr[0] >= ' ' && keyStr[0] <= '~' {
			m.branchFilter += keyStr
			m.branchFiltered = fuzzyFilterBranches(m.branchList, m.branchFilter)
			m.branchCursor = 0
			return m, nil
		}
	}

	return m, nil
}

// handleConfirmKey handles y/n/esc keys for the shared confirmation dialog.
func (m Model) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y":
		action := m.confirmAction
		taskID := m.confirmTaskID
		m.confirmAction = ""
		m.confirmTaskID = 0
		switch action {
		case "continue":
			return m, m.continueTask(taskID, "", "")
		case "advance", "finalize":
			return m, m.advanceTask(taskID)
		case "delete":
			return m, m.deleteTask(taskID)
		case "revert":
			return m, m.revertTask(taskID)
		case "stop":
			return m, m.stopTask(taskID)
		default:
			return m, nil
		}
	case "n", "esc":
		m.confirmAction = ""
		m.confirmTaskID = 0
		return m, nil
	default:
		return m, nil
	}
}

// resolveDefaultWorkflow returns the saved default workflow if it exists in the
// provided list, otherwise falls back to the first available workflow.
func (m Model) resolveDefaultWorkflow(workflows []string) string {
	if m.defaultWorkflow != "" {
		for _, name := range workflows {
			if name == m.defaultWorkflow {
				return name
			}
		}
	}
	if len(workflows) > 0 {
		return workflows[0]
	}
	return ""
}
