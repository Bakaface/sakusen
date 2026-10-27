package tui

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Bakaface/sakusen/internal/action"
	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/daemon"
	gitpkg "github.com/Bakaface/sakusen/internal/git"
	taskpkg "github.com/Bakaface/sakusen/internal/task"
	"github.com/Bakaface/sakusen/internal/tmux"
	"github.com/Bakaface/sakusen/internal/workflow"
	tea "github.com/charmbracelet/bubbletea"
)

type taskFieldUpdatedMsg struct {
	field string
}

type taskDeletedMsg int64

// taskUpdatedMsg carries the post-mutation TaskInfo. The list reducer
// replaces the matching row in place so the user sees the new state without
// waiting for the next refresh poll.
type taskUpdatedMsg daemon.TaskInfo

// actionCtx is the shared Ctx builder for TUI tea.Cmd factories.
func (m Model) actionCtx() action.Ctx {
	return action.Ctx{Cfg: m.cfg, Client: m.client, Out: io.Discard}
}

func (m Model) listTasks(c TaskService) ([]daemon.TaskInfo, error) {
	if m.projectID > 0 {
		return c.ListTasksFiltered(m.projectID)
	}
	if m.projectName != "" {
		return c.ListTasksByProjectName(m.projectName)
	}
	return c.ListTasksFiltered(0)
}

func (m Model) connectToDaemon() tea.Cmd {
	return func() tea.Msg {
		c := client.New(m.cfg)
		if err := c.Connect(); err != nil {
			return errorMsg(err)
		}

		if err := c.Subscribe(); err != nil {
			return errorMsg(err)
		}

		tasks, err := m.listTasks(c)
		if err != nil {
			return errorMsg(err)
		}

		// Drain subscription messages in background (picked up via tick refresh)
		go func() {
			for range c.Messages() {
			}
		}()

		return clientConnectedMsg{client: c, tasks: tasks}
	}
}

func (m Model) tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) refreshTasks() tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		tasks, err := m.listTasks(m.client)
		if err != nil {
			return errorMsg(err)
		}
		return tasksLoadedMsg(tasks)
	}
}

// loadFanoutChildren fetches the task's fanout children via get_task, since
// broadcasts and listings never carry them. Tasks whose workflow has no
// fanout step get no request.
func (m Model) loadFanoutChildren(t *daemon.TaskInfo) tea.Cmd {
	if m.client == nil || m.cfg == nil || t == nil || !hasFanoutStep(m.cfg.GetTaskWorkflow(t.Workflow)) {
		return nil
	}
	taskID := t.ID
	c := m.client
	return func() tea.Msg {
		info, err := c.GetTask(taskID)
		if err != nil {
			return taskFanoutLoadedMsg{taskID: taskID, err: err}
		}
		return taskFanoutLoadedMsg{taskID: taskID, children: info.FanoutChildren}
	}
}

func hasFanoutStep(wf *config.WorkflowConfig) bool {
	if wf == nil {
		return false
	}
	for i := range wf.Steps {
		if wf.Steps[i].IsFanout() {
			return true
		}
	}
	return false
}

func (m Model) loadOutput(taskID int64, offset int) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		lines, totalLines, err := m.client.GetLogs(taskID, 0, offset)
		if err != nil {
			return errorMsg(err)
		}
		return outputLoadedMsg{
			taskID:     taskID,
			lines:      lines,
			totalLines: totalLines,
			offset:     offset,
		}
	}
}

// runTaskAction is the shared cmd factory for the eight near-identical
// task-mutation verbs below (stop/retry/revert/detach/attach/continue/
// priority/dependsOn): guard the nil-client case, run the verb through the
// action package, and map the result to a tea.Msg exactly once. Each verb
// method supplies only its typed action.RunX call.
func (m Model) runTaskAction(fn func(action.Ctx) (action.Result, error)) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		res, err := fn(m.actionCtx())
		if err != nil {
			return errorMsg(err)
		}
		if res.Task != nil {
			return taskUpdatedMsg(*res.Task)
		}
		return nil
	}
}

func (m Model) stopTask(taskID int64) tea.Cmd {
	return m.runTaskAction(func(ctx action.Ctx) (action.Result, error) {
		return action.RunStop(ctx, action.StopArgs{ID: taskID})
	})
}

// retryTask resets a task and asks the daemon to re-run it. When stepName is
// empty, the workflow restarts from the first step. When non-empty, completed
// work for earlier steps is preserved and the engine resumes from that step.
func (m Model) retryTask(taskID int64, stepName string) tea.Cmd {
	return m.runTaskAction(func(ctx action.Ctx) (action.Result, error) {
		return action.RunRetry(ctx, action.RetryArgs{ID: taskID, FromStep: stepName})
	})
}

// confirmRetryMerge is the confirmAction value for retrying a merge-failed
// task's merge; it doubles as the "Retry merge task #N?" prompt text.
const confirmRetryMerge = "retry merge"

// retryMerge re-runs only finalization for a merge-failed task.
func (m Model) retryMerge(taskID int64) tea.Cmd {
	return m.runTaskAction(func(ctx action.Ctx) (action.Result, error) {
		return action.RunRetryMerge(ctx, action.RetryMergeArgs{ID: taskID})
	})
}

func (m Model) revertTask(taskID int64) tea.Cmd {
	return m.runTaskAction(func(ctx action.Ctx) (action.Result, error) {
		return action.RunRevert(ctx, action.RevertArgs{ID: taskID})
	})
}

func (m Model) detachBranch(taskID int64) tea.Cmd {
	return m.runTaskAction(func(ctx action.Ctx) (action.Result, error) {
		return action.RunDetach(ctx, action.DetachArgs{ID: taskID})
	})
}

func (m Model) attachBranch(taskID int64) tea.Cmd {
	return m.runTaskAction(func(ctx action.Ctx) (action.Result, error) {
		return action.RunAttachBranch(ctx, action.AttachBranchArgs{ID: taskID})
	})
}

func (m Model) continueTask(taskID int64, wf, prompt string) tea.Cmd {
	return m.runTaskAction(func(ctx action.Ctx) (action.Result, error) {
		return action.RunContinue(ctx, action.ContinueArgs{ID: taskID, Workflow: wf, Prompt: prompt})
	})
}

func (m Model) advanceTask(taskID int64) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		if _, err := m.client.AdvanceTask(taskID); err != nil {
			return errorMsg(err)
		}
		return nil
	}
}

func (m Model) deleteTask(taskID int64) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		if _, err := action.RunDelete(m.actionCtx(), action.DeleteArgs{ID: taskID}); err != nil {
			return errorMsg(err)
		}
		return taskDeletedMsg(taskID)
	}
}

func (m Model) updateTaskPriority(taskID int64, priority string) tea.Cmd {
	return m.runTaskAction(func(ctx action.Ctx) (action.Result, error) {
		return action.RunEdit(ctx, action.EditArgs{ID: taskID, Priority: &priority})
	})
}

func (m Model) addTaskDependency(taskID, blockedByID int64) tea.Cmd {
	return m.runTaskAction(func(ctx action.Ctx) (action.Result, error) {
		return action.RunDependsOn(ctx, action.DependsOnArgs{TaskID: taskID, BlockedByID: blockedByID, Direction: "add"})
	})
}

func (m Model) createTaskWithPrompt(title, slug, input, branchName string, worktree bool, images []string, targetBranch, checkoutBranch, workdir string) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}

		bm := int(m.prompt.branchMode)
		// The prompt always resolves an explicit worktree value (from the toggle
		// or the workflow pin), so pass it through as a concrete choice.
		worktreeChoice := worktree
		args := action.CreateArgs{
			Title:       title,
			Slug:        slug,
			Input:       input,
			Workflow:    m.selectedWorkflow,
			Branch:      branchName,
			Target:      targetBranch,
			Checkout:    checkoutBranch,
			Worktree:    &worktreeChoice,
			Workdir:     workdir,
			ProjectPath: m.projectPath,
			Images:      images,
			BranchMode:  &bm,
			// Only the New Task form saves the project's defaults.
			PersistDefaults: true,
		}
		if m.blockingTaskID != 0 {
			args.DependsOn = []int64{m.blockingTaskID}
		}
		res, err := action.RunCreate(m.actionCtx(), args)
		if err != nil {
			return errorMsg(fmt.Errorf("failed to create task: %w", err))
		}
		return taskCreatedMsg(*res.Task)
	}
}

func (m Model) handleEditorResult(path string) tea.Cmd {
	return func() tea.Msg {
		defer func() { _ = os.Remove(path) }()

		data, err := os.ReadFile(path)
		if err != nil {
			return errorMsg(fmt.Errorf("failed to read temp file: %w", err))
		}

		input := strings.TrimSpace(string(data))
		if input == "" {
			return nil // User cancelled
		}

		if m.client == nil {
			return nil
		}

		res, err := action.RunCreate(m.actionCtx(), action.CreateArgs{
			Input:       input,
			Workflow:    m.selectedWorkflow,
			ProjectPath: m.projectPath,
		})
		if err != nil {
			return errorMsg(fmt.Errorf("failed to create task: %w", err))
		}
		return taskCreatedMsg(*res.Task)
	}
}

func (m Model) handleFieldEditorResult(msg editorFieldFinishedMsg) tea.Cmd {
	return func() tea.Msg {
		defer func() { _ = os.Remove(msg.path) }()

		data, err := os.ReadFile(msg.path)
		if err != nil {
			return errorMsg(fmt.Errorf("failed to read temp file: %w", err))
		}

		value := strings.TrimSpace(string(data))
		if value == "" {
			return nil // User cleared the field or cancelled
		}

		if m.client == nil {
			return nil
		}

		editArgs := action.EditArgs{ID: msg.taskID}
		switch msg.field {
		case "title":
			editArgs.Title = &value
		case "input":
			editArgs.Input = &value
		case "context":
			editArgs.Context = &value
		default:
			return errorMsg(fmt.Errorf("unknown field: %s", msg.field))
		}
		if _, err := action.RunEdit(m.actionCtx(), editArgs); err != nil {
			return errorMsg(fmt.Errorf("failed to update %s: %w", msg.field, err))
		}

		return taskFieldUpdatedMsg{field: msg.field}
	}
}

func (m Model) openEditorForPrompt() tea.Cmd {
	f, err := os.CreateTemp("", "sakusen-prompt-*.md")
	if err != nil {
		return func() tea.Msg { return errorMsg(fmt.Errorf("failed to create temp file: %w", err)) }
	}

	// Pre-populate with current textarea content
	if content := m.prompt.Value(); content != "" {
		if _, err := f.WriteString(content); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			return func() tea.Msg { return errorMsg(fmt.Errorf("failed to write temp file: %w", err)) }
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return func() tea.Msg { return errorMsg(fmt.Errorf("failed to write temp file: %w", err)) }
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	path := f.Name()
	c := exec.Command(editor, path)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			_ = os.Remove(path)
			return errorMsg(fmt.Errorf("editor exited with error: %w", err))
		}
		return editorPromptFinishedMsg{path: path}
	})
}

// openEditorForStepContext launches $EDITOR with the current step context
// written to a temporary file. On editor exit the file contents are pushed
// back to the daemon via UpdateStepContext.
func (m Model) openEditorForStepContext(taskID int64, stepName, currentValue string) tea.Cmd {
	safeStep := strings.NewReplacer("/", "-", " ", "_").Replace(stepName)
	f, err := os.CreateTemp("", fmt.Sprintf("sakusen-step-%s-*.md", safeStep))
	if err != nil {
		return func() tea.Msg { return errorMsg(fmt.Errorf("failed to create temp file: %w", err)) }
	}

	if currentValue != "" {
		if _, err := f.WriteString(currentValue); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			return func() tea.Msg { return errorMsg(fmt.Errorf("failed to write temp file: %w", err)) }
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return func() tea.Msg { return errorMsg(fmt.Errorf("failed to write temp file: %w", err)) }
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	path := f.Name()
	c := exec.Command(editor, path)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			_ = os.Remove(path)
			return errorMsg(fmt.Errorf("editor exited with error: %w", err))
		}
		return editorStepContextFinishedMsg{taskID: taskID, stepName: stepName, path: path}
	})
}

func (m Model) handleStepContextEditorResult(msg editorStepContextFinishedMsg) tea.Cmd {
	return func() tea.Msg {
		defer func() { _ = os.Remove(msg.path) }()

		data, err := os.ReadFile(msg.path)
		if err != nil {
			return errorMsg(fmt.Errorf("failed to read temp file: %w", err))
		}

		value := string(data)
		if m.client == nil {
			return nil
		}

		if err := m.client.UpdateStepContext(msg.taskID, msg.stepName, value); err != nil {
			return errorMsg(fmt.Errorf("failed to update step context: %w", err))
		}

		return stepContextUpdatedMsg{taskID: msg.taskID, stepName: msg.stepName, context: value}
	}
}

func (m Model) openEditorForField(taskID int64, field, currentValue string) tea.Cmd {
	f, err := os.CreateTemp("", fmt.Sprintf("sakusen-%s-*.md", field))
	if err != nil {
		return func() tea.Msg { return errorMsg(fmt.Errorf("failed to create temp file: %w", err)) }
	}

	if currentValue != "" {
		if _, err := f.WriteString(currentValue); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			return func() tea.Msg { return errorMsg(fmt.Errorf("failed to write temp file: %w", err)) }
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return func() tea.Msg { return errorMsg(fmt.Errorf("failed to write temp file: %w", err)) }
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	path := f.Name()
	c := exec.Command(editor, path)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			_ = os.Remove(path)
			return errorMsg(fmt.Errorf("editor exited with error: %w", err))
		}
		return editorFieldFinishedMsg{taskID: taskID, field: field, path: path}
	})
}

func (m Model) openLogInEditor(task *daemon.TaskInfo) tea.Cmd {
	dataDir := filepath.Join(task.ProjectPath, ".sakusen")
	logFile := workflow.ProjectLogPath(dataDir, task.ID)

	if _, err := os.Stat(logFile); err != nil {
		return func() tea.Msg {
			return errorMsg(fmt.Errorf("no log file found for this task"))
		}
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	c := exec.Command(editor, logFile)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return errorMsg(fmt.Errorf("editor exited with error: %w", err))
		}
		return editorLogFinishedMsg{}
	})
}

// tmuxProjectNameFor returns the project name a task's tmux session was
// created under. The daemon names sessions from the TASK's project
// (`spawnInteractiveSession` uses `pc.cfg.Project.Name`), which is not
// necessarily the project the TUI was launched in: global mode lists every
// project, and a `.sakusen.yml` nested below another one makes cwd resolve to
// a different project row than the task's. Deriving the name from `m.cfg`
// instead sent attach looking for `<cwd project>-<id>`, which never exists.
// The cwd config remains the fallback for TaskInfo values that predate
// ProjectName enrichment.
func (m Model) tmuxProjectNameFor(t daemon.TaskInfo) string {
	if t.ProjectName != "" {
		return t.ProjectName
	}
	if m.cfg != nil {
		return m.cfg.Project.Name
	}
	return ""
}

// attachTmuxSession attaches to (or switches the client to) a task's tmux
// session, waking a hibernated — or otherwise missing — session first.
//
// Everything runs inside the returned tea.Cmd, including the existence check:
// waking is a daemon round trip that spawns a process, which must not block
// Update. The attach itself is still produced by tea.ExecProcess; calling it
// here and returning its message hands the runtime exactly what it would have
// received had Update returned the cmd directly.
func (m Model) attachTmuxSession(t daemon.TaskInfo) tea.Cmd {
	taskID := t.ID
	taskIDStr := fmt.Sprintf("%d", taskID)
	session := tmux.FindSession(m.tmuxProjectNameFor(t), taskIDStr, t.Slug, "")
	sessionName := session.Name

	behavior := ""
	if m.cfg != nil {
		behavior = m.cfg.TmuxNestedAttachBehavior
	}
	if behavior == "" {
		behavior = "switch"
	}

	return func() tea.Msg {
		if !session.Exists() {
			if taskpkg.Status(t.Status) != taskpkg.StatusTmux {
				return errorMsg(fmt.Errorf("no tmux session found for task #%d", taskID))
			}
			if err := m.client.WakeTask(taskID); err != nil {
				return errorMsg(fmt.Errorf("failed to wake tmux session for task #%d: %w", taskID, err))
			}
		}

		if tmux.IsInsideTmux() {
			if behavior == "nest" {
				// Nested attach: unset $TMUX and attach (creates nested tmux).
				// User detaches with prefix+d to return to the TUI.
				cmd := tmux.NestedAttachCommand(sessionName)
				return tea.ExecProcess(cmd, func(err error) tea.Msg {
					return tmuxDetachedMsg{taskID: taskID}
				})()
			}

			// Default "switch": switch the client to the target session.
			// The TUI keeps running in the background. User returns with prefix+L.
			cmd := tmux.SwitchClientCommand(sessionName)
			if err := cmd.Run(); err != nil {
				return errorMsg(fmt.Errorf("failed to switch to tmux session: %w", err))
			}
			return tmuxDetachedMsg{taskID: taskID}
		}

		// Not inside tmux: hand over the terminal to tmux attach.
		cmd := tmux.AttachCommand(sessionName)
		return tea.ExecProcess(cmd, func(err error) tea.Msg {
			return tmuxDetachedMsg{taskID: taskID}
		})()
	}
}

// tmuxProjectNames returns the distinct project names whose task sessions the
// list could contain, in order of first appearance. Sessions are named after
// the owning task's project, so the cwd's project name covers only a slice of
// the visible tasks in global mode — and none of them when cwd resolves to a
// different project row than the tasks' (see tmuxProjectNameFor). It falls back
// to the cwd's project before the first task load, so the indicators are
// populated on the first tick.
func (m Model) tmuxProjectNames() []string {
	seen := make(map[string]bool)
	var names []string
	for _, t := range m.list.allTasks {
		if name := m.tmuxProjectNameFor(t); name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if len(names) == 0 && m.cfg != nil && m.cfg.Project.Name != "" {
		names = append(names, m.cfg.Project.Name)
	}
	return names
}

func (m Model) checkTmuxSessions() tea.Cmd {
	projectNames := m.tmuxProjectNames()

	return func() tea.Msg {
		sessions, err := tmux.ListTaskSessionsForProjects(projectNames)
		if err != nil {
			return nil
		}
		result := make(map[int64]bool, len(sessions))
		for taskID := range sessions {
			result[taskID] = true
		}
		return tmuxSessionsMsg(result)
	}
}

type branchesLoadedMsg []string

func (m Model) loadLocalBranches() tea.Cmd {
	return func() tea.Msg {
		branches, err := gitpkg.NewRepo(m.projectPath).ListLocalBranches()
		if err != nil {
			return errorMsg(fmt.Errorf("failed to list branches: %w", err))
		}
		if len(branches) == 0 {
			return errorMsg(fmt.Errorf("no local branches found"))
		}
		return branchesLoadedMsg(branches)
	}
}

func (m Model) createBranchTask(branch string) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}

		// Checking out an existing branch needs an isolated worktree regardless of
		// the project default, so request one explicitly.
		worktreeOn := true
		res, err := action.RunCreate(m.actionCtx(), action.CreateArgs{
			Checkout:    branch,
			ProjectPath: m.projectPath,
			Worktree:    &worktreeOn,
			TmuxDirect:  true,
		})
		if err != nil {
			return errorMsg(fmt.Errorf("failed to create branch task: %w", err))
		}
		return taskCreatedMsg(*res.Task)
	}
}

// blockingTaskTitleFromList looks up a task title by ID from the current list.
func (m Model) blockingTaskTitleFromList(taskID int64) string {
	for _, t := range m.list.allTasks {
		if t.ID == taskID {
			return t.Title
		}
	}
	return ""
}

// fuzzyFilterBranches returns branches that match the query using fuzzy matching.
// Characters in the query must appear in order in the branch name, but not necessarily contiguously.
func fuzzyFilterBranches(branches []string, query string) []string {
	if query == "" {
		result := make([]string, len(branches))
		copy(result, branches)
		return result
	}

	query = strings.ToLower(query)
	var result []string
	for _, b := range branches {
		if fuzzyMatch(strings.ToLower(b), query) {
			result = append(result, b)
		}
	}
	return result
}

// fuzzyMatch returns true if all characters in pattern appear in str in order.
func fuzzyMatch(str, pattern string) bool {
	pi := 0
	for i := 0; i < len(str) && pi < len(pattern); i++ {
		if str[i] == pattern[pi] {
			pi++
		}
	}
	return pi == len(pattern)
}
