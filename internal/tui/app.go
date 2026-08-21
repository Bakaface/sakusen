package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type view int

const (
	viewList view = iota
	viewDetail
	viewTaskInfo
	viewPrompt
	viewArtifact
	viewSakusen
)

type Model struct {
	cfg               *config.Config
	client            TaskService
	logStream         logStream
	keys              keyMap
	list              listView
	detail            detailView
	taskInfo          taskInfoView
	prompt            promptView
	view              view
	width             int
	height            int
	err               error
	quitting          bool
	projectID         int64  // 0 = global mode (show all projects)
	projectPath       string // project directory path, empty in global mode
	projectName       string // repo basename for filtering in global mode
	globalMode        bool
	defaultWorktree   bool   // per-project worktree preference
	defaultBranchMode int    // per-project branch mode preference (0=new, 1=existing)
	defaultWorkflow   string // per-project default workflow name

	// Blocking task creation state
	blockingTaskID int64 // when non-zero, the newly created task will block this task

	// Confirmation state
	confirmAction string // "continue", "advance", "finalize", "delete", "revert", or "stop"; empty if no confirmation pending
	confirmTaskID int64

	// Chord sequence state — first key of a pending two-key chord (e.g. "d" for dd, "o" for oa).
	// Replaces the old pendingDelete/pendingO/pendingE/pendingY booleans.
	pendingChord string

	// Workflow selection state (kept after selector closes)
	selectedWorkflow string

	// Continue workflow selection state (kept after selector closes)
	continueTaskID           int64
	continueSelectedWorkflow string // workflow selected for continue, held while user enters prompt

	// Generic selection dialog state
	selector selector

	// Branch selection state (for "b" keybind — fuzzy-searchable branch picker)
	selectingBranch bool
	branchCursor    int
	branchPendingG  bool
	branchList      []string // all local branches
	branchFilter    string   // fuzzy search input
	branchFiltered  []string // branches matching the filter

	// Step context state (kept after selector closes)
	taskSteps []daemon.TaskStepDetail // loaded step details for current selection

	// Artifact viewer state
	artifactView artifactViewState

	// Sakusen animation state
	sakusen    sakusenAnimation
	sakusenCmd tea.Cmd // deferred command to run when animation completes

	// Status message (flash message, auto-clears after a few ticks)
	statusMessage    string
	statusMessageTTL int // ticks remaining before auto-clear

	// Command mode state (vim-style : commands)
	commandMode    bool
	commandInput   string
	commandHistory inputHistory

	// Search mode state (vim-style / and ? search)
	searchMode      bool
	searchQuery     string
	searchDirection int // 1 for forward (/), -1 for backward (?)
	searchHistory   inputHistory
}

type clientConnectedMsg struct {
	client *client.Client
	tasks  []daemon.TaskInfo
}
type taskUpdateMsg daemon.TaskInfo
type taskCreatedMsg daemon.TaskInfo
type editorFinishedMsg struct{ path string }
type editorPromptFinishedMsg struct{ path string }
type tasksLoadedMsg []daemon.TaskInfo
type outputLoadedMsg struct {
	taskID     int64    // which task this output belongs to (prevents stale data)
	lines      []string // new lines since last fetch (or all lines if offset=0)
	totalLines int      // total line count on server (used as next offset)
	offset     int      // the offset used for this request (0 = full load)
}
type errorMsg error
type tickMsg time.Time
type tmuxDetachedMsg struct{ taskID int64 }
type tmuxSessionsMsg map[int64]bool
type editorLogFinishedMsg struct{}
type editorFieldFinishedMsg struct {
	taskID int64
	field  string
	path   string
}
type editorStepContextFinishedMsg struct {
	taskID   int64
	stepName string
	path     string
}
type stepContextUpdatedMsg struct {
	taskID   int64
	stepName string
	context  string
}

func NewModel(cfg *config.Config, projectID int64, projectPath, projectName string, globalMode bool, defaultWorktree bool, defaultBranchMode int, defaultWorkflow string) Model {
	list := newListView(globalMode, projectName)

	// Apply config-driven display options
	if cfg != nil {
		if cfg.Options.Number != nil {
			list.showLineNumbers = *cfg.Options.Number
		}
		if cfg.Options.Branch != nil {
			list.showBranch = *cfg.Options.Branch
		}
		if cfg.Options.Target != nil {
			list.showTarget = *cfg.Options.Target
		}
		if cfg.Options.BranchView != nil {
			list.branchView = *cfg.Options.BranchView
			if *cfg.Options.BranchView {
				list.showBranch = true
			}
		}
	}

	return Model{
		cfg:               cfg,
		keys:              newKeyMap(),
		list:              list,
		detail:            newDetailView(),
		taskInfo:          newTaskInfoView(),
		prompt:            newPromptView(defaultWorktree, branchMode(defaultBranchMode), cfgBaseBranch(cfg)),
		view:              viewList,
		projectID:         projectID,
		projectPath:       projectPath,
		projectName:       projectName,
		globalMode:        globalMode,
		defaultWorktree:   defaultWorktree,
		defaultBranchMode: defaultBranchMode,
		defaultWorkflow:   defaultWorkflow,
		commandHistory:    newInputHistory(50),
		searchHistory:     newInputHistory(50),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.connectToDaemon(),
		m.tickCmd(),
		m.list.spinner.Tick,
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.list.SetSize(msg.Width, msg.Height)
		m.detail.SetSize(msg.Width, msg.Height)
		m.taskInfo.SetSize(msg.Width, msg.Height)
		m.prompt.SetSize(msg.Width, msg.Height)
		m.artifactView.SetSize(msg.Width, msg.Height)
		m.sakusen.width = msg.Width
		m.sakusen.height = msg.Height
		return m, nil

	case sakusenTickMsg:
		m.sakusen = m.sakusen.Update()
		if m.sakusen.done {
			m.view = viewList
			deferred := m.sakusenCmd
			m.sakusenCmd = nil
			return m, deferred
		}
		return m, sakusenTickCmd()

	case clientConnectedMsg:
		m.client = msg.client
		m.list.SetTasks(msg.tasks)
		return m, nil

	case tasksLoadedMsg:
		m.list.refreshing = false
		m.list.SetTasks(msg)
		return m, nil

	case taskUpdateMsg:
		m.list.UpdateTask(daemon.TaskInfo(msg))
		if m.view == viewDetail && m.detail.task != nil && m.detail.task.ID == msg.ID {
			task := daemon.TaskInfo(msg)
			m.detail.SetTask(&task)
		}
		if m.view == viewTaskInfo && m.taskInfo.task != nil && m.taskInfo.task.ID == msg.ID {
			task := daemon.TaskInfo(msg)
			m.taskInfo.SetTask(&task)
		}
		return m, nil

	case editorFinishedMsg:
		return m, m.handleEditorResult(msg.path)

	case editorPromptFinishedMsg:
		data, err := os.ReadFile(msg.path)
		os.Remove(msg.path)
		if err != nil {
			m.err = fmt.Errorf("failed to read temp file: %w", err)
			return m, nil
		}
		text := strings.TrimSpace(string(data))
		if text != "" {
			m.prompt.textarea.SetValue(text)
		}
		m.prompt.Focus()
		return m, nil

	case editorLogFinishedMsg:
		return m, nil

	case editorFieldFinishedMsg:
		return m, m.handleFieldEditorResult(msg)

	case editorStepContextFinishedMsg:
		return m, m.handleStepContextEditorResult(msg)

	case stepContextUpdatedMsg:
		// Refresh cached step list and viewer if currently viewing this step.
		for i := range m.taskSteps {
			if m.taskSteps[i].Name == msg.stepName {
				m.taskSteps[i].Context = msg.context
				m.taskSteps[i].Status = stepStatusCompleted
				if m.view == viewArtifact && m.artifactView.name == msg.stepName {
					m.artifactView.SetContent(msg.stepName, msg.context)
					m.artifactView.editable = msg.context != ""
				}
				break
			}
		}
		m.statusMessage = fmt.Sprintf("step %q context saved · already-run steps won't see this", msg.stepName)
		m.statusMessageTTL = 4
		return m, nil

	case taskFieldUpdatedMsg:
		m.statusMessage = fmt.Sprintf("%s updated", capitalize(msg.field))
		m.statusMessageTTL = 2
		m.list.refreshing = true
		return m, m.refreshTasks()

	case taskStepsLoadedMsg:
		m.taskSteps = msg.steps

		// Retry path: every step in the workflow is a valid restart target,
		// not just non-pending ones. Single-step workflows skip the picker
		// (and the now-removed confirmation) entirely.
		if msg.action == "retry" {
			if len(msg.steps) == 1 {
				return m, m.retryTask(msg.taskID, msg.steps[0].Name)
			}

			names := make([]string, len(msg.steps))
			descriptions := make([]string, len(msg.steps))
			for i, s := range msg.steps {
				names[i] = retryStepSelectorLabel(s)
				descriptions[i] = retryStepSelectorDescription(s)
			}

			// Find the task so we can default the cursor to its last step.
			var taskRef *daemon.TaskInfo
			for i := range m.list.allTasks {
				if m.list.allTasks[i].ID == msg.taskID {
					t := m.list.allTasks[i]
					taskRef = &t
					break
				}
			}

			m.selector = selector{
				kind:         selectorRetryStep,
				title:        "Retry From Step",
				items:        names,
				cursor:       retryStepCursor(taskRef, msg.steps),
				descriptions: descriptions,
				itemStyle:    stepSelectorItemStyle,
				hint:         "j/k: navigate  enter: retry from step  esc: cancel",
				taskID:       msg.taskID,
				action:       msg.action,
			}
			return m, nil
		}

		// Build parallel slices for the generic selector.
		names := make([]string, len(msg.steps))
		descriptions := make([]string, len(msg.steps))
		disabled := make([]bool, len(msg.steps))
		for i, s := range msg.steps {
			names[i] = stepSelectorLabel(s)
			descriptions[i] = stepSelectorDescription(s)
			disabled[i] = !stepIsActionable(s)
		}

		// Single completed step with content — skip the picker entirely.
		actionable := actionableSteps(msg.steps)
		if len(actionable) == 1 && msg.action == "view" {
			step := actionable[0]
			m.selector = selector{kind: selectorArtifact, taskID: msg.taskID, action: msg.action}
			m.artifactView.SetContent(step.Name, renderStepBody(step))
			m.artifactView.editable = stepIsEditable(step)
			m.artifactView.taskID = msg.taskID
			m.view = viewArtifact
			return m, nil
		}
		if len(actionable) == 1 && msg.action == "edit" && stepIsEditable(actionable[0]) {
			step := actionable[0]
			m.selector = selector{kind: selectorArtifact, taskID: msg.taskID, action: msg.action}
			return m, m.openEditorForStepContext(msg.taskID, step.Name, step.Context)
		}

		// Place cursor on the first actionable row.
		cursor := 0
		for i, s := range msg.steps {
			if stepIsActionable(s) {
				cursor = i
				break
			}
		}

		m.selector = selector{
			kind:         selectorArtifact,
			title:        "Step Context",
			items:        names,
			cursor:       cursor,
			descriptions: descriptions,
			disabled:     disabled,
			itemStyle:    stepSelectorItemStyle,
			hint:         "j/k: navigate  enter: view  e: edit  esc: cancel",
			taskID:       msg.taskID,
			action:       msg.action,
		}
		return m, nil

	case branchesLoadedMsg:
		m.branchList = msg
		m.branchFiltered = make([]string, len(msg))
		copy(m.branchFiltered, msg)
		m.branchFilter = ""
		m.branchCursor = 0
		m.selectingBranch = true
		return m, nil

	case tmuxDetachedMsg:
		m.list.refreshing = true
		return m, m.refreshTasks()

	case tmuxSessionsMsg:
		m.list.tmuxSessions = msg
		m.list.recomputeColumns()
		return m, nil

	case taskDeletedMsg:
		m.list.RemoveTask(int64(msg))
		return m, nil

	case taskUpdatedMsg:
		m.list.UpdateTask(daemon.TaskInfo(msg))
		return m, nil

	case paletteActionMsg:
		if msg.result.Task != nil {
			m.list.UpdateTask(*msg.result.Task)
		}
		for _, t := range msg.result.Tasks {
			m.list.RemoveTask(t.ID)
		}
		if msg.result.Message != "" {
			m.statusMessage = msg.result.Message
			m.statusMessageTTL = 4
		}
		return m, nil

	case taskCreatedMsg:
		m.list.UpdateTask(daemon.TaskInfo(msg))
		m.list.GotoTop()
		if m.blockingTaskID != 0 {
			blockedTaskID := m.blockingTaskID
			newTaskID := msg.ID
			m.blockingTaskID = 0
			return m, m.addTaskDependency(blockedTaskID, newTaskID)
		}
		return m, nil

	case outputLoadedMsg:
		if m.detail.task == nil {
			return m, nil
		}
		// logStream ignores stale results from a previously viewed task and
		// picks AppendNewLines vs SetOutput based on the offset used for
		// the request that produced this message.
		m.logStream.apply(msg, &m.detail)
		return m, nil

	case tickMsg:
		var cmds []tea.Cmd

		// Auto-clear status message after TTL expires
		if m.statusMessage != "" {
			m.statusMessageTTL--
			if m.statusMessageTTL <= 0 {
				m.statusMessage = ""
			}
		}

		if m.view == viewDetail && m.detail.task != nil && m.client != nil {
			taskID, offset := m.logStream.nextRequest()
			cmds = append(cmds, m.loadOutput(taskID, offset))
		}

		if m.client != nil && !m.list.refreshing {
			m.list.refreshing = true
			cmds = append(cmds, m.refreshTasks())
		}

		cmds = append(cmds, m.checkTmuxSessions())
		cmds = append(cmds, m.tickCmd())
		return m, tea.Batch(cmds...)

	case errorMsg:
		m.err = msg
		return m, nil

	case spinner.TickMsg:
		if m.list.loading {
			cmd := m.list.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Clear error and status message on any keypress, but still process the key
	m.err = nil
	m.statusMessage = ""

	switch m.view {
	case viewList:
		return m.handleListKey(msg)
	case viewDetail:
		return m.handleDetailKey(msg)
	case viewTaskInfo:
		return m.handleTaskInfoKey(msg)
	case viewPrompt:
		return m.handlePromptKey(msg)
	case viewArtifact:
		return m.handleArtifactViewKey(msg)
	case viewSakusen:
		// Any keypress skips the animation
		m.view = viewList
		deferred := m.sakusenCmd
		m.sakusenCmd = nil
		return m, deferred
	}
	return m, nil
}

func (m Model) View() string {
	if m.quitting {
		return "Goodbye!\n"
	}

	if m.err != nil {
		return fmt.Sprintf("  Error: %v\n\n  Press any key to continue.", m.err)
	}

	// Show help overlay
	if m.list.showHelp && m.view == viewList {
		return m.renderHelpOverlay()
	}

	// Show prompt help overlay
	if m.prompt.showHelp && m.view == viewPrompt {
		return m.renderPromptHelpOverlay()
	}

	// Show detail (logs) help overlay
	if m.detail.showHelp && m.view == viewDetail {
		return m.renderDetailHelpOverlay()
	}

	// Show task-info help overlay
	if m.taskInfo.showHelp && m.view == viewTaskInfo {
		return m.renderTaskInfoHelpOverlay()
	}

	// Show artifact-view help overlay
	if m.artifactView.showHelp && m.view == viewArtifact {
		return m.renderArtifactHelpOverlay()
	}

	// Show selection dialog
	if m.selector.IsActive() {
		return m.selector.View()
	}

	// Show branch selection as its own screen (fuzzy-searchable)
	if m.selectingBranch {
		var b strings.Builder
		b.WriteString(titleStyle.Render(" Select Branch ") + "\n\n")

		if m.branchFilter != "" {
			b.WriteString("  " + dimStyle.Render("filter: ") + m.branchFilter + "█\n\n")
		}

		// Calculate visible window for scrolling
		maxVisible := m.height - 7 // account for header, filter, footer
		if maxVisible < 1 {
			maxVisible = 10
		}
		branches := m.branchFiltered
		startIdx := 0
		if m.branchCursor >= maxVisible {
			startIdx = m.branchCursor - maxVisible + 1
		}
		endIdx := startIdx + maxVisible
		if endIdx > len(branches) {
			endIdx = len(branches)
		}

		if len(branches) == 0 {
			b.WriteString("  " + dimStyle.Render("no matching branches") + "\n")
		} else {
			for i := startIdx; i < endIdx; i++ {
				label := "  " + branches[i]
				if i == m.branchCursor {
					b.WriteString(selectedStyle.Render("> "+label) + "\n")
				} else {
					b.WriteString("    " + label + "\n")
				}
			}
		}
		b.WriteString("\n" + dimStyle.Render("  type to filter | enter: select | esc: cancel"))
		return b.String()
	}

	// Build bottom bar lines (confirmation, status message).
	// These are rendered at the very bottom of the terminal.
	// Command and search inputs replace the help bar to prevent UI jumps.
	var bottomLines []string
	if m.confirmAction != "" {
		bottomLines = append(bottomLines, fmt.Sprintf("  %s task #%d? (y/n)", capitalize(m.confirmAction), m.confirmTaskID))
	}
	// Command/search inputs replace the keybinds help row instead of adding extra lines,
	// so the interface stays put when entering command or search mode.
	m.list.helpOverride = ""
	if m.commandMode {
		m.list.helpOverride = fmt.Sprintf("  :%s█", m.commandInput)
	} else if m.searchMode {
		searchChar := "/"
		if m.searchDirection < 0 {
			searchChar = "?"
		}
		matchInfo := ""
		if len(m.list.matchedIndices) > 0 {
			matchInfo = fmt.Sprintf(" [%d/%d]", m.list.currentMatchIdx+1, len(m.list.matchedIndices))
		}
		m.list.helpOverride = fmt.Sprintf("  %s%s█%s", searchChar, m.searchQuery, matchInfo)
	}
	if m.statusMessage != "" {
		bottomLines = append(bottomLines, fmt.Sprintf("  %s", m.statusMessage))
	}

	// Count extra lines so views can reserve space for the bottom bar.
	extra := len(bottomLines)
	m.list.extraLines = extra

	var content string
	switch m.view {
	case viewDetail:
		content = m.detail.View()
	case viewTaskInfo:
		content = m.taskInfo.View()
	case viewPrompt:
		content = m.prompt.View()
	case viewArtifact:
		content = m.artifactView.View()
	case viewSakusen:
		return m.sakusen.View()
	default:
		content = m.list.View()
	}

	// Append bottom bar lines, padded to the very bottom of the terminal.
	if len(bottomLines) > 0 {
		if m.height > 0 {
			contentLines := strings.Count(content, "\n")
			// Account for the trailing newline we add at the end
			totalUsed := contentLines + 1 + len(bottomLines)
			padding := m.height - totalUsed
			if padding > 0 {
				content += strings.Repeat("\n", padding)
			}
		}
		content += "\n" + strings.Join(bottomLines, "\n")
	}

	return content + "\n"
}

func (m Model) renderHelpOverlay() string {
	keys := newKeyMap()
	groups := keys.FullHelp()

	groupNames := []string{"Navigation", "Actions", "General"}

	headingStyle := lipgloss.NewStyle().Bold(true).Foreground(highlight)

	var b strings.Builder
	b.WriteString(titleStyle.Render(" Help ") + "\n\n")

	for i, group := range groups {
		if i < len(groupNames) {
			b.WriteString("  " + headingStyle.Render(groupNames[i]) + "\n")
		}
		for _, binding := range group {
			fmt.Fprintf(&b, "    %-12s %s\n", dimStyle.Render(binding.Help().Key), helpStyle.Render(binding.Help().Desc))
		}
		b.WriteString("\n")
	}

	b.WriteString(dimStyle.Render("  Press ctrl+h or esc to close"))
	return b.String()
}

func (m Model) renderPromptHelpOverlay() string {
	keys := newPromptKeyMap()
	groups := keys.FullHelp()

	groupNames := []string{"Input", "Focus", "Toggles"}

	headingStyle := lipgloss.NewStyle().Bold(true).Foreground(highlight)

	var b strings.Builder
	b.WriteString(titleStyle.Render(" New Task Help ") + "\n\n")

	for i, group := range groups {
		if i < len(groupNames) {
			b.WriteString("  " + headingStyle.Render(groupNames[i]) + "\n")
		}
		for _, binding := range group {
			fmt.Fprintf(&b, "    %-12s %s\n", dimStyle.Render(binding.Help().Key), helpStyle.Render(binding.Help().Desc))
		}
		b.WriteString("\n")
	}

	b.WriteString(dimStyle.Render("  Press ctrl+h or esc to close"))
	return b.String()
}

// renderDetailHelpOverlay renders the help overlay for the logs (detail) view.
func (m Model) renderDetailHelpOverlay() string {
	keys := cachedDetailNormalKeyMap
	groups := keys.FullHelp()
	groupNames := []string{"Navigation", "Actions", "General"}

	headingStyle := lipgloss.NewStyle().Bold(true).Foreground(highlight)

	var b strings.Builder
	b.WriteString(titleStyle.Render(" Logs Help ") + "\n\n")

	for i, group := range groups {
		if i < len(groupNames) {
			b.WriteString("  " + headingStyle.Render(groupNames[i]) + "\n")
		}
		for _, binding := range group {
			fmt.Fprintf(&b, "    %-12s %s\n", dimStyle.Render(binding.Help().Key), helpStyle.Render(binding.Help().Desc))
		}
		b.WriteString("\n")
	}

	b.WriteString(dimStyle.Render("  Press ctrl+h or esc to close"))
	return b.String()
}

// renderTaskInfoHelpOverlay renders the help overlay for the task-info view.
func (m Model) renderTaskInfoHelpOverlay() string {
	keys := cachedTaskInfoKeyMap
	groups := keys.FullHelp()
	groupNames := []string{"Navigation", "Actions", "General"}

	headingStyle := lipgloss.NewStyle().Bold(true).Foreground(highlight)

	var b strings.Builder
	b.WriteString(titleStyle.Render(" Task Info Help ") + "\n\n")

	for i, group := range groups {
		if i < len(groupNames) {
			b.WriteString("  " + headingStyle.Render(groupNames[i]) + "\n")
		}
		for _, binding := range group {
			fmt.Fprintf(&b, "    %-12s %s\n", dimStyle.Render(binding.Help().Key), helpStyle.Render(binding.Help().Desc))
		}
		b.WriteString("\n")
	}

	b.WriteString(dimStyle.Render("  Press ctrl+h or esc to close"))
	return b.String()
}

// renderArtifactHelpOverlay renders the help overlay for the artifact (step context) view.
func (m Model) renderArtifactHelpOverlay() string {
	keys := cachedArtifactViewKeyMap
	groups := keys.FullHelp(m.artifactView.editable)
	groupNames := []string{"Navigation", "Actions", "General"}

	headingStyle := lipgloss.NewStyle().Bold(true).Foreground(highlight)

	var b strings.Builder
	b.WriteString(titleStyle.Render(" Step Context Help ") + "\n\n")

	for i, group := range groups {
		if len(group) == 0 {
			continue
		}
		if i < len(groupNames) {
			b.WriteString("  " + headingStyle.Render(groupNames[i]) + "\n")
		}
		for _, binding := range group {
			fmt.Fprintf(&b, "    %-12s %s\n", dimStyle.Render(binding.Help().Key), helpStyle.Render(binding.Help().Desc))
		}
		b.WriteString("\n")
	}

	b.WriteString(dimStyle.Render("  Press ctrl+h or esc to close"))
	return b.String()
}

func Run(cfg *config.Config, projectID int64, projectPath, projectName string, globalMode bool, defaultWorktree bool, defaultBranchMode int, defaultWorkflow string) error {
	p := tea.NewProgram(
		NewModel(cfg, projectID, projectPath, projectName, globalMode, defaultWorktree, defaultBranchMode, defaultWorkflow),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	_, err := p.Run()
	return err
}

func cfgBaseBranch(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.Git.BaseBranch
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
