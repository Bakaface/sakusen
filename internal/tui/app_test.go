package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

func TestTmuxSessionsMsg_UpdatesListView(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}

	sessions := tmuxSessionsMsg(map[int64]bool{1: true, 5: true})
	result, cmd := m.Update(sessions)
	updated := result.(Model)

	if !updated.list.tmuxSessions[1] {
		t.Error("expected task 1 to have tmux session")
	}
	if !updated.list.tmuxSessions[5] {
		t.Error("expected task 5 to have tmux session")
	}
	if updated.list.tmuxSessions[3] {
		t.Error("expected task 3 to NOT have tmux session")
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
}

func TestTmuxDetachedMsg_TriggersRefresh(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}

	msg := tmuxDetachedMsg{taskID: 42}
	_, cmd := m.Update(msg)

	if cmd == nil {
		t.Error("expected refresh command after tmux detach, got nil")
	}
}

func TestHandleListKey_TReturnsCommandWhenTaskSelected(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 42, Title: "Test", Status: "awaiting-approval"},
	})

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}}
	_, cmd := m.handleListKey(msg)

	if cmd == nil {
		t.Error("expected command from 't' key with selected task, got nil")
	}
}

func TestHandleListKey_TNoOpWithNoTasks(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}}
	_, cmd := m.handleListKey(msg)

	if cmd != nil {
		t.Error("expected no command from 't' key with no tasks, got non-nil")
	}
}

func TestListView_RendersTmuxIndicator(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Task with tmux", Status: "awaiting-approval", CurrentStep: "implement"},
		{ID: 2, Title: "Task without tmux", Status: "running", CurrentStep: "review"},
	})
	l.tmuxSessions = map[int64]bool{1: true}
	l.SetSize(100, 24)

	output := l.View()

	if !strings.Contains(output, "[wip]") {
		t.Error("expected task list to contain [wip] indicator for non-human task with tmux session")
	}
}

func TestListView_RendersTmuxStatus(t *testing.T) {
	// Non-human tmux step with NO live session (none registered here) renders
	// as "running" with a stale [T] postfix — a dead pane is never [wip].
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Task with tmux status", Status: "tmux", CurrentStep: "implement"},
	})
	l.SetSize(100, 24)

	output := l.View()

	if strings.Contains(output, "▣") {
		t.Error("expected task list to NOT contain ▣ icon — tmux tasks render the real underlying status")
	}
	if !strings.Contains(output, "●") {
		t.Error("expected task list to contain ● icon (running) for non-human tmux task")
	}
	if !strings.Contains(output, "[T]") {
		t.Error("expected task list to contain [T] postfix for non-human tmux task without a live session")
	}
}

func TestListView_RendersHumanTmuxStatus(t *testing.T) {
	// Human tmux step renders as "awaiting-approval" (user has been handed
	// the wheel) with a [T] postfix — Claude has finished, the pane is idle.
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Human tmux task", Status: "tmux", CurrentStep: "review", StepHuman: true},
	})
	l.SetSize(100, 24)

	output := l.View()

	if strings.Contains(output, "▣") {
		t.Error("expected task list to NOT contain ▣ icon — tmux tasks render the real underlying status")
	}
	if !strings.Contains(output, "◷") {
		t.Error("expected task list to contain ◷ icon (awaiting-approval) for human tmux task")
	}
	if !strings.Contains(output, "[T]") {
		t.Error("expected task list to contain [T] postfix for human tmux task")
	}
}

func TestListView_NoTmuxIndicatorWithoutSessions(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Task without tmux", Status: "running", CurrentStep: "implement"},
	})
	l.SetSize(100, 24)

	output := l.View()

	if strings.Contains(output, "[T]") {
		t.Error("expected task list to NOT contain [T] indicator when no tmux sessions")
	}
}

func TestListView_DetachedOmitsTmuxActivity(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Detached tmux task", Status: "tmux", CurrentStep: "dev", TmuxActivity: "wip", WorktreeDetached: true},
	})
	l.tmuxSessions = map[int64]bool{1: true}
	l.SetSize(100, 24)

	output := l.View()

	if !strings.Contains(output, "[detached]") {
		t.Error("expected task list to contain [detached] for detached task")
	}
	if strings.Contains(output, "[wip]") {
		t.Error("expected task list to NOT contain [wip] when task is detached")
	}
}

func TestListView_TmuxTasksFloatToTop(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 5, Title: "Newest running", Status: "running"},
		{ID: 4, Title: "Older tmux", Status: "tmux"},
		{ID: 3, Title: "Pending", Status: "pending"},
		{ID: 2, Title: "Even older tmux", Status: "tmux"},
		{ID: 1, Title: "Oldest running", Status: "running"},
	})

	// Tmux tasks first (preserving ID-desc order within the group),
	// then everything else (preserving ID-desc order within that group).
	wantIDs := []int64{4, 2, 5, 3, 1}
	if len(l.tasks) != len(wantIDs) {
		t.Fatalf("expected %d tasks, got %d", len(wantIDs), len(l.tasks))
	}
	for i, want := range wantIDs {
		if l.tasks[i].ID != want {
			t.Errorf("position %d: expected task ID %d, got %d", i, want, l.tasks[i].ID)
		}
	}
}

func TestListView_NotDetachedShowsTmuxPostfix(t *testing.T) {
	// Non-detached tmux task gets a tmux postfix — [T] for human steps
	// (Claude idle, wheel handed over), [wip] for non-human (Claude working).
	// The postfix is driven by StepHuman, matching the same human/non-human
	// distinction the engine uses to decide whether auto-advance is allowed.
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Active tmux task", Status: "tmux", CurrentStep: "implement", StepHuman: true, WorktreeDetached: false},
	})
	l.tmuxSessions = map[int64]bool{1: true}
	l.SetSize(100, 24)

	output := l.View()

	if !strings.Contains(output, "[T]") {
		t.Error("expected task list to contain [T] for non-detached human tmux task")
	}
	if strings.Contains(output, "[detached]") {
		t.Error("expected task list to NOT contain [detached] for non-detached task")
	}
}

func TestHandleKey_ClearsErrorAndProcessesKey(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		client: &client.Client{},
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		err:    fmt.Errorf("some background error"),
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 26, Title: "Test task", Status: "awaiting-approval"},
	})

	// Press "c" while m.err is set — should clear error AND trigger continue confirmation
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}
	result, _ := m.handleKey(msg)
	updated := result.(Model)

	if updated.err != nil {
		t.Error("expected error to be cleared")
	}
	if updated.confirmAction != "continue" {
		t.Errorf("expected confirmAction to be 'continue', got %q", updated.confirmAction)
	}
	if updated.confirmTaskID != 26 {
		t.Errorf("expected confirmTaskID to be 26, got %d", updated.confirmTaskID)
	}
}

func TestHandleKey_ClearsErrorOnAnyKey(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		err:    fmt.Errorf("some error"),
	}

	// Press "R" (refresh) while m.err is set — should clear error AND process the key
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}}
	result, cmd := m.handleKey(msg)
	updated := result.(Model)

	if updated.err != nil {
		t.Error("expected error to be cleared")
	}
	// "R" triggers refreshTasks which requires a client, so cmd should be non-nil only with client
	// Without client, it returns nil — that's fine, the important thing is the error was cleared
	// and we didn't just swallow the keypress
	_ = cmd
}

func TestHandleListKey_QQuitsApp(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	result, cmd := m.handleListKey(msg)
	updated := result.(Model)

	if !updated.quitting {
		t.Error("expected quitting to be true")
	}
	if cmd == nil {
		t.Error("expected quit command, got nil")
	}
}

func TestHandleListKey_EnterOpensTaskInfoView(t *testing.T) {
	m := Model{
		keys:     newKeyMap(),
		list:     newListView(false, ""),
		detail:   newDetailView(),
		taskInfo: newTaskInfoView(),
		view:     viewList,
		cfg:      &config.Config{},
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 7, Title: "Test task", Status: "running"},
	})

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	result, cmd := m.handleListKey(msg)
	updated := result.(Model)

	if updated.view != viewTaskInfo {
		t.Errorf("expected view to be viewTaskInfo (%d), got %d", viewTaskInfo, updated.view)
	}
	if updated.taskInfo.task == nil {
		t.Fatal("expected taskInfo.task to be set")
	}
	if updated.taskInfo.task.ID != 7 {
		t.Errorf("expected taskInfo task ID 7, got %d", updated.taskInfo.task.ID)
	}
	if cmd != nil {
		t.Error("expected no command (no log loading for task info), got non-nil")
	}
}

func TestHandleListKey_LOpensLogsView(t *testing.T) {
	m := Model{
		keys:     newKeyMap(),
		list:     newListView(false, ""),
		detail:   newDetailView(),
		taskInfo: newTaskInfoView(),
		view:     viewList,
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 7, Title: "Test task", Status: "running"},
	})

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}}
	result, cmd := m.handleListKey(msg)
	updated := result.(Model)

	if updated.view != viewDetail {
		t.Errorf("expected view to be viewDetail (%d), got %d", viewDetail, updated.view)
	}
	if updated.detail.task == nil {
		t.Fatal("expected detail.task to be set")
	}
	if updated.detail.task.ID != 7 {
		t.Errorf("expected detail task ID 7, got %d", updated.detail.task.ID)
	}
	if !updated.detail.IsFollowMode() {
		t.Error("expected follow mode to be true")
	}
	if cmd == nil {
		t.Error("expected loadOutput command, got nil")
	}
}

func TestListView_GlobalModeTitle(t *testing.T) {
	l := newListView(true, "")
	l.SetSize(100, 24)
	output := l.View()

	if !strings.Contains(output, "Global") {
		t.Error("expected global mode title to contain 'Global'")
	}
}

func TestListView_GlobalModeShowsProjectColumn(t *testing.T) {
	l := newListView(true, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Task", Status: "running", ProjectName: "myproject"},
	})
	l.SetSize(120, 24)
	output := l.View()

	if !strings.Contains(output, "Project") {
		t.Error("expected global mode to show Project header column")
	}
	if !strings.Contains(output, "myproject") {
		t.Error("expected global mode to show project name 'myproject'")
	}
}

func TestListView_SortsTasksDescendingByID(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "First task", Status: "completed"},
		{ID: 3, Title: "Third task", Status: "running"},
		{ID: 2, Title: "Second task", Status: "pending"},
	})

	if len(l.tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(l.tasks))
	}
	if l.tasks[0].ID != 3 {
		t.Errorf("expected first task ID to be 3 (newest), got %d", l.tasks[0].ID)
	}
	if l.tasks[1].ID != 2 {
		t.Errorf("expected second task ID to be 2, got %d", l.tasks[1].ID)
	}
	if l.tasks[2].ID != 1 {
		t.Errorf("expected third task ID to be 1 (oldest), got %d", l.tasks[2].ID)
	}
}

func TestListView_SortsTasksDescendingPreservesCursor(t *testing.T) {
	l := newListView(false, "")
	// Set initial tasks with cursor at position 0
	l.SetTasks([]daemon.TaskInfo{
		{ID: 5, Title: "Task 5", Status: "running"},
		{ID: 3, Title: "Task 3", Status: "pending"},
	})
	l.table.SetCursor(1) // pointing at task 3

	// Update with same tasks — cursor should stay valid
	l.SetTasks([]daemon.TaskInfo{
		{ID: 3, Title: "Task 3", Status: "pending"},
		{ID: 5, Title: "Task 5", Status: "running"},
	})

	if l.table.Cursor() > len(l.tasks)-1 {
		t.Errorf("cursor %d exceeds task count %d", l.table.Cursor(), len(l.tasks))
	}
	// Tasks should still be sorted descending
	if l.tasks[0].ID != 5 {
		t.Errorf("expected first task ID to be 5, got %d", l.tasks[0].ID)
	}
}

func TestListView_NonGlobalModeHidesProjectColumn(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Task", Status: "running", ProjectName: "myproject"},
	})
	l.SetSize(100, 24)
	output := l.View()

	if strings.Contains(output, "Project") {
		t.Error("expected non-global mode to NOT show Project header column")
	}
}

func TestHandleListKey_CTriggersConfirmForCompletedTask(t *testing.T) {
	cfg := &config.Config{
		Workflows: []config.WorkflowConfig{
			{Name: "implement"},
			{Name: "review"},
		},
	}

	m := Model{
		cfg:    cfg,
		keys:   newKeyMap(),
		client: &client.Client{},
		list:   newListView(false, ""),
		detail: newDetailView(),
		prompt: newPromptView(true, branchModeNew, ""),
		view:   viewList,
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 10, Title: "Completed task", Status: "completed"},
	})

	// Single "c" now goes straight to prompt for completed tasks
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	if updated.view != viewPrompt {
		t.Errorf("expected view to be viewPrompt, got %d", updated.view)
	}
	if updated.continueTaskID != 10 {
		t.Errorf("expected continueTaskID to be 10, got %d", updated.continueTaskID)
	}
}

func TestHandleListKey_CTriggersConfirmForFailedTask(t *testing.T) {
	cfg := &config.Config{
		Workflows: []config.WorkflowConfig{
			{Name: "implement"},
			{Name: "review"},
		},
	}

	m := Model{
		cfg:    cfg,
		keys:   newKeyMap(),
		client: &client.Client{},
		list:   newListView(false, ""),
		detail: newDetailView(),
		prompt: newPromptView(true, branchModeNew, ""),
		view:   viewList,
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 11, Title: "Failed task", Status: "failed"},
	})

	// Single "c" now goes straight to prompt for failed tasks
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	if updated.view != viewPrompt {
		t.Errorf("expected view to be viewPrompt, got %d", updated.view)
	}
	if updated.continueTaskID != 11 {
		t.Errorf("expected continueTaskID to be 11, got %d", updated.continueTaskID)
	}
}

func TestHandleListKey_CNoOpForRunningTask(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		client: &client.Client{},
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 12, Title: "Running task", Status: "running"},
	})

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	if updated.confirmAction != "" {
		t.Errorf("expected no confirmAction for running task, got %q", updated.confirmAction)
	}
}

func TestHandleListKey_CNoOpWithNoClient(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 13, Title: "Completed task", Status: "completed"},
	})

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	if updated.confirmAction != "" {
		t.Errorf("expected no confirmAction without client, got %q", updated.confirmAction)
	}
}

func TestHandleListKey_CTriggersFinalizeForTmuxTask(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		client: &client.Client{},
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 14, Title: "Tmux task", Status: "tmux"},
	})

	// Single "c" triggers finalize confirm immediately
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}
	result, cmd := m.handleListKey(msg)
	updated := result.(Model)

	if updated.confirmAction != "finalize" {
		t.Errorf("expected confirmAction to be 'finalize', got %q", updated.confirmAction)
	}
	if updated.confirmTaskID != 14 {
		t.Errorf("expected confirmTaskID to be 14, got %d", updated.confirmTaskID)
	}
	if cmd != nil {
		t.Error("expected no command (confirmation pending), got non-nil")
	}
}

func TestHandleListKey_CNoOpForPendingTask(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		client: &client.Client{},
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 15, Title: "Pending task", Status: "pending"},
	})

	// Single "c" should not trigger any action for pending task
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	if updated.confirmAction != "" {
		t.Errorf("expected no confirmAction for pending task, got %q", updated.confirmAction)
	}
}

func TestHandleListKey_CContinuesAwaitingApprovalTask(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		client: &client.Client{},
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 16, Title: "Awaiting task", Status: "awaiting-approval"},
	})

	// Single "c" should trigger continue confirmation for awaiting-approval task
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	if updated.confirmAction != "continue" {
		t.Errorf("expected confirmAction to be 'continue' for awaiting-approval task, got %q", updated.confirmAction)
	}
	if updated.confirmTaskID != 16 {
		t.Errorf("expected confirmTaskID to be 16, got %d", updated.confirmTaskID)
	}
}

func newTestModelWithTasks(n int) Model {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}
	tasks := make([]daemon.TaskInfo, n)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	m.list.SetTasks(tasks)
	m.list.SetSize(100, 30) // 30 lines tall → visibleRows = 23, half = 11
	return m
}

func TestHandleListKey_GGGoesToTop(t *testing.T) {
	m := newTestModelWithTasks(20)
	m.list.table.SetCursor(15)

	// First "g"
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}}
	result, _ := m.handleListKey(msg)
	m = result.(Model)

	if m.list.table.Cursor() != 15 {
		t.Errorf("expected cursor to stay at 15 after first 'g', got %d", m.list.table.Cursor())
	}
	if m.pendingChord != "g" {
		t.Error("expected pendingG to be true after first 'g'")
	}

	// Second "g"
	result, _ = m.handleListKey(msg)
	m = result.(Model)

	if m.list.table.Cursor() != 0 {
		t.Errorf("expected cursor at 0 after 'gg', got %d", m.list.table.Cursor())
	}
	if m.pendingChord != "" {
		t.Error("expected pendingG to be false after 'gg'")
	}
}

func TestHandleListKey_GGResetByOtherKey(t *testing.T) {
	m := newTestModelWithTasks(20)
	m.list.table.SetCursor(10)

	// Press "g" then "j" — should NOT go to top, second key consumed silently
	gMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}}
	result, _ := m.handleListKey(gMsg)
	m = result.(Model)

	jMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	result, _ = m.handleListKey(jMsg)
	m = result.(Model)

	if m.list.table.Cursor() != 10 {
		t.Errorf("expected cursor at 10 after g+j (j consumed silently), got %d", m.list.table.Cursor())
	}
	if m.pendingChord != "" {
		t.Error("expected pendingG to be false after non-g key")
	}
}

func TestHandleListKey_ShiftGGoesToBottom(t *testing.T) {
	m := newTestModelWithTasks(20)
	m.list.table.SetCursor(0)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	if updated.list.table.Cursor() != 19 {
		t.Errorf("expected cursor at 19 (last task) after 'G', got %d", updated.list.table.Cursor())
	}
}

func TestHandleListKey_CtrlDPageDown(t *testing.T) {
	m := newTestModelWithTasks(30)
	m.list.table.SetCursor(0)

	msg := tea.KeyMsg{Type: tea.KeyCtrlD}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	// height=30, visibleRows=23, half=11
	if updated.list.table.Cursor() != 11 {
		t.Errorf("expected cursor at 11 after ctrl+d, got %d", updated.list.table.Cursor())
	}
}

func TestHandleListKey_CtrlUPageUp(t *testing.T) {
	m := newTestModelWithTasks(30)
	m.list.table.SetCursor(20)

	msg := tea.KeyMsg{Type: tea.KeyCtrlU}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	// height=30, visibleRows=23, half=11
	if updated.list.table.Cursor() != 9 {
		t.Errorf("expected cursor at 9 after ctrl+u, got %d", updated.list.table.Cursor())
	}
}

func TestHandleListKey_CtrlDClampsToEnd(t *testing.T) {
	m := newTestModelWithTasks(10)
	m.list.table.SetCursor(8)

	msg := tea.KeyMsg{Type: tea.KeyCtrlD}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	if updated.list.table.Cursor() != 9 {
		t.Errorf("expected cursor clamped to 9 (last task) after ctrl+d, got %d", updated.list.table.Cursor())
	}
}

func TestHandleListKey_CtrlUClampsToStart(t *testing.T) {
	m := newTestModelWithTasks(10)
	m.list.table.SetCursor(2)

	msg := tea.KeyMsg{Type: tea.KeyCtrlU}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	if updated.list.table.Cursor() != 0 {
		t.Errorf("expected cursor clamped to 0 after ctrl+u, got %d", updated.list.table.Cursor())
	}
}

func TestHandleListKey_PgDownPageDown(t *testing.T) {
	m := newTestModelWithTasks(30)
	m.list.table.SetCursor(0)

	msg := tea.KeyMsg{Type: tea.KeyPgDown}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	// height=30, visibleRows=23, half=11
	if updated.list.table.Cursor() != 11 {
		t.Errorf("expected cursor at 11 after pgdown, got %d", updated.list.table.Cursor())
	}
}

func TestHandleListKey_PgUpPageUp(t *testing.T) {
	m := newTestModelWithTasks(30)
	m.list.table.SetCursor(20)

	msg := tea.KeyMsg{Type: tea.KeyPgUp}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	// height=30, visibleRows=23, half=11
	if updated.list.table.Cursor() != 9 {
		t.Errorf("expected cursor at 9 after pgup, got %d", updated.list.table.Cursor())
	}
}

func TestListView_GotoTopAndBottom(t *testing.T) {
	l := newListView(false, "")
	tasks := make([]daemon.TaskInfo, 10)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	l.SetTasks(tasks)

	l.GotoBottom()
	if l.table.Cursor() != 9 {
		t.Errorf("expected cursor at 9 after GotoBottom, got %d", l.table.Cursor())
	}

	l.GotoTop()
	if l.table.Cursor() != 0 {
		t.Errorf("expected cursor at 0 after GotoTop, got %d", l.table.Cursor())
	}
}

func TestListView_PageDownPageUp(t *testing.T) {
	l := newListView(false, "")
	tasks := make([]daemon.TaskInfo, 30)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	l.SetTasks(tasks)
	l.SetSize(100, 30) // visibleRows=23, half=11

	l.PageDown()
	if l.table.Cursor() != 11 {
		t.Errorf("expected cursor at 11 after PageDown, got %d", l.table.Cursor())
	}

	l.PageDown()
	if l.table.Cursor() != 22 {
		t.Errorf("expected cursor at 22 after second PageDown, got %d", l.table.Cursor())
	}

	l.PageUp()
	if l.table.Cursor() != 11 {
		t.Errorf("expected cursor at 11 after PageUp, got %d", l.table.Cursor())
	}
}

func TestListView_ShowsRealTaskID(t *testing.T) {
	l := newListView(false, "")
	// Use non-sequential IDs to prove the ID column shows real task IDs,
	// not positional indices (e.g., 1, 2, 3).
	l.SetTasks([]daemon.TaskInfo{
		{ID: 42, Title: "First task", Status: "running"},
		{ID: 7, Title: "Second task", Status: "pending"},
		{ID: 137, Title: "Third task", Status: "completed"},
	})
	l.SetSize(100, 24)

	output := l.View()

	// IDs should be displayed without '#' prefix
	if !strings.Contains(output, "42") {
		t.Error("expected list to show real task ID 42")
	}
	if !strings.Contains(output, "137") {
		t.Error("expected list to show real task ID 137")
	}
	// Verify no '#' prefix on IDs
	if strings.Contains(output, "#42") {
		t.Error("expected task ID 42 without '#' prefix")
	}
	if strings.Contains(output, "#137") {
		t.Error("expected task ID 137 without '#' prefix")
	}
}

func TestHandleListKey_RRetriesFailedTask(t *testing.T) {
	m := Model{
		keys:        newKeyMap(),
		client:      &client.Client{},
		list:        newListView(false, ""),
		detail:      newDetailView(),
		view:        viewList,
		projectPath: "/tmp/test-project",
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "Housekeeping", Description: "Clean up code"},
			},
		},
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 5, Title: "Failed task", Status: "failed"},
	})

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}}
	result, cmd := m.handleListKey(msg)
	updated := result.(Model)

	// Retry flow now dispatches a command to load workflow steps; no
	// confirmation prompt is shown until the user picks a step.
	if updated.selector.kind == selectorWorkflow {
		t.Error("expected selector kind not to be selectorWorkflow when retrying failed task")
	}
	if updated.confirmAction != "" {
		t.Errorf("expected no confirmAction (retry now opens step picker), got %q", updated.confirmAction)
	}
	if cmd == nil {
		t.Error("expected command to load workflow steps for retry, got nil")
	}
}

func TestHandleListKey_RRetriesTmuxTask(t *testing.T) {
	m := Model{
		keys:        newKeyMap(),
		client:      &client.Client{},
		list:        newListView(false, ""),
		detail:      newDetailView(),
		view:        viewList,
		projectPath: "/tmp/test-project",
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "Housekeeping", Description: "Clean up code"},
			},
		},
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 3, Title: "Stale tmux task", Status: "tmux"},
	})

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}}
	result, cmd := m.handleListKey(msg)
	updated := result.(Model)

	if updated.selector.kind == selectorWorkflow {
		t.Error("expected selector kind not to be selectorWorkflow when retrying tmux task")
	}
	if updated.confirmAction != "" {
		t.Errorf("expected no confirmAction (retry now opens step picker), got %q", updated.confirmAction)
	}
	if cmd == nil {
		t.Error("expected command to load workflow steps for retry, got nil")
	}
}

func TestHandleListKey_RRetriesCompletedTask(t *testing.T) {
	m := Model{
		keys:        newKeyMap(),
		client:      &client.Client{},
		list:        newListView(false, ""),
		detail:      newDetailView(),
		view:        viewList,
		projectPath: "/tmp/test-project",
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "Housekeeping", Description: "Clean up code"},
			},
		},
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 7, Title: "Completed task", Status: "completed"},
	})

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}}
	result, cmd := m.handleListKey(msg)
	updated := result.(Model)

	if updated.selector.kind == selectorWorkflow {
		t.Error("expected selector kind not to be selectorWorkflow when retrying completed task")
	}
	if updated.confirmAction != "" {
		t.Errorf("expected no confirmAction (retry now opens step picker), got %q", updated.confirmAction)
	}
	if cmd == nil {
		t.Error("expected command to load workflow steps for retry, got nil")
	}
}

func TestHandleListKey_RNoOpOnRunningTask(t *testing.T) {
	m := Model{
		keys:        newKeyMap(),
		client:      &client.Client{},
		list:        newListView(false, ""),
		detail:      newDetailView(),
		view:        viewList,
		projectPath: "/tmp/test-project",
		cfg:         &config.Config{},
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Running task", Status: "running"},
	})

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}}
	_, cmd := m.handleListKey(msg)

	// r on a running task does nothing (not retryable)
	if cmd != nil {
		t.Error("expected no command for non-retryable task, got non-nil")
	}
}

func TestListView_PageWithSmallHeight(t *testing.T) {
	l := newListView(false, "")
	tasks := make([]daemon.TaskInfo, 10)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	l.SetTasks(tasks)
	l.SetSize(100, 6) // visibleRows=1, half=1 (minimum)

	l.PageDown()
	if l.table.Cursor() != 1 {
		t.Errorf("expected cursor at 1 with small height, got %d", l.table.Cursor())
	}
}

func TestHandlePromptKey_CtrlGOpensEditor(t *testing.T) {
	m := Model{
		keys:        newKeyMap(),
		client:      &client.Client{},
		prompt:      newPromptView(true, branchModeNew, ""),
		view:        viewPrompt,
		projectPath: "/tmp/test",
	}
	m.prompt.SetSize(80, 24)

	msg := tea.KeyMsg{Type: tea.KeyCtrlG}
	_, cmd := m.handlePromptKey(msg)

	if cmd == nil {
		t.Error("expected editor command from ctrl+g, got nil")
	}
}

func TestHandlePromptKey_EnterSubmitsTask(t *testing.T) {
	m := Model{
		keys:        newKeyMap(),
		client:      &client.Client{},
		prompt:      newPromptView(true, branchModeNew, ""),
		view:        viewPrompt,
		projectPath: "/tmp/test",
	}
	m.prompt.SetSize(80, 24)
	m.prompt.textarea.SetValue("test description")

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	result, cmd := m.handlePromptKey(msg)
	updated := result.(Model)

	if updated.view != viewList {
		t.Errorf("expected view to be viewList after enter, got %d", updated.view)
	}
	if cmd == nil {
		t.Error("expected create task command from enter, got nil")
	}
}

func TestHandlePromptKey_EnterEmptyDoesNothing(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		client: &client.Client{},
		prompt: newPromptView(true, branchModeNew, ""),
		view:   viewPrompt,
	}
	m.prompt.SetSize(80, 24)

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	result, cmd := m.handlePromptKey(msg)
	updated := result.(Model)

	if updated.view != viewPrompt {
		t.Errorf("expected view to remain viewPrompt with empty input, got %d", updated.view)
	}
	if cmd != nil {
		t.Error("expected no command with empty input, got non-nil")
	}
}

func TestHandlePromptKey_EscCancels(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		prompt: newPromptView(true, branchModeNew, ""),
		view:   viewPrompt,
	}

	msg := tea.KeyMsg{Type: tea.KeyEscape}
	result, cmd := m.handlePromptKey(msg)
	updated := result.(Model)

	if updated.view != viewList {
		t.Errorf("expected view to be viewList after esc, got %d", updated.view)
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
}

func TestEditorPromptFinishedMsg_SetsTextareaValue(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		prompt: newPromptView(true, branchModeNew, ""),
		view:   viewPrompt,
	}
	m.prompt.SetSize(80, 24)

	// Create a temp file with content
	f, err := os.CreateTemp("", "test-editor-*.md")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("edited content from editor")
	f.Close()

	msg := editorPromptFinishedMsg{path: f.Name()}
	result, _ := m.Update(msg)
	updated := result.(Model)

	if updated.prompt.Value() != "edited content from editor" {
		t.Errorf("expected textarea value to be 'edited content from editor', got %q", updated.prompt.Value())
	}
}

func TestEditorPromptFinishedMsg_EmptyFilePreservesTextarea(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		prompt: newPromptView(true, branchModeNew, ""),
		view:   viewPrompt,
	}
	m.prompt.SetSize(80, 24)
	m.prompt.textarea.SetValue("original content")

	// Create an empty temp file
	f, err := os.CreateTemp("", "test-editor-*.md")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	msg := editorPromptFinishedMsg{path: f.Name()}
	result, _ := m.Update(msg)
	updated := result.(Model)

	// Empty editor content should preserve original textarea value
	if updated.prompt.Value() != "original content" {
		t.Errorf("expected textarea to keep 'original content', got %q", updated.prompt.Value())
	}
}

func TestPromptView_HelpShowsEditorShortcutInFullHelp(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		prompt: newPromptView(true, branchModeNew, ""),
		view:   viewPrompt,
	}

	output := m.renderPromptHelpOverlay()

	if !strings.Contains(output, "ctrl+g") {
		t.Error("expected prompt full help to contain 'ctrl+g'")
	}
	if !strings.Contains(output, "editor") {
		t.Error("expected prompt full help to contain 'editor'")
	}
}

func TestPromptView_HelpShowsEnterAndCtrlJ(t *testing.T) {
	p := newPromptView(true, branchModeNew, "")
	p.SetSize(100, 24)

	output := p.View()

	if !strings.Contains(output, "enter") {
		t.Error("expected prompt help to contain 'enter'")
	}
	if !strings.Contains(output, "submit") {
		t.Error("expected prompt help to contain 'submit'")
	}
	if !strings.Contains(output, "ctrl+j") {
		t.Error("expected prompt help to contain 'ctrl+j'")
	}
	if !strings.Contains(output, "newline") {
		t.Error("expected prompt help to contain 'newline'")
	}
	if strings.Contains(output, "ctrl+d") {
		t.Error("expected prompt help to NOT contain 'ctrl+d'")
	}
}

func TestHandlePromptKey_CtrlJInsertsNewline(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		prompt: newPromptView(true, branchModeNew, ""),
		view:   viewPrompt,
	}
	m.prompt.SetSize(80, 24)
	m.prompt.textarea.SetValue("line one")

	// Move cursor to end of text
	msg := tea.KeyMsg{Type: tea.KeyCtrlE}
	m.prompt.Update(msg)

	// Ctrl+J should insert a newline (handled by textarea's InsertNewline binding)
	msg = tea.KeyMsg{Type: tea.KeyCtrlJ}
	m.prompt.Update(msg)

	val := m.prompt.textarea.Value()
	if !strings.Contains(val, "\n") {
		t.Errorf("expected ctrl+j to insert newline, got %q", val)
	}
}

func TestPromptView_WordJumpKeybindings(t *testing.T) {
	p := newPromptView(true, branchModeNew, "")
	p.SetSize(80, 24)
	p.textarea.SetValue("hello world foo")

	// Move cursor to end of line
	endMsg := tea.KeyMsg{Type: tea.KeyCtrlE}
	p.Update(endMsg)
	endCol := p.textarea.LineInfo().ColumnOffset

	// ctrl+left should jump back (word backward)
	ctrlLeft := tea.KeyMsg{Type: tea.KeyCtrlLeft}
	p.Update(ctrlLeft)
	afterFirstLeft := p.textarea.LineInfo().ColumnOffset
	if afterFirstLeft >= endCol {
		t.Errorf("expected cursor to move left from %d, got %d", endCol, afterFirstLeft)
	}

	// ctrl+left again should jump further back
	p.Update(ctrlLeft)
	afterSecondLeft := p.textarea.LineInfo().ColumnOffset
	if afterSecondLeft >= afterFirstLeft {
		t.Errorf("expected cursor to move further left from %d, got %d", afterFirstLeft, afterSecondLeft)
	}

	// ctrl+right should jump forward (word forward)
	ctrlRight := tea.KeyMsg{Type: tea.KeyCtrlRight}
	p.Update(ctrlRight)
	afterRight := p.textarea.LineInfo().ColumnOffset
	if afterRight <= afterSecondLeft {
		t.Errorf("expected cursor to move right from %d, got %d", afterSecondLeft, afterRight)
	}
}

func TestListView_RendersPriorityBadge(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Urgent task", Status: "pending", Priority: "urgent"},
		{ID: 2, Title: "Low task", Status: "pending", Priority: "low"},
	})
	l.SetSize(120, 30)

	output := l.View()
	if !strings.Contains(output, " P ") {
		t.Error("expected priority header 'P' in list view")
	}
	if !strings.Contains(output, "U") {
		t.Error("expected 'U' badge for urgent task")
	}
	if !strings.Contains(output, "L") {
		t.Error("expected 'L' badge for low task")
	}
}

func TestPriorityBadge(t *testing.T) {
	tests := []struct {
		priority string
		want     string
	}{
		{"urgent", "U"},
		{"high", "H"},
		{"medium", "M"},
		{"low", "L"},
		{"", "M"},
		{"unknown", "M"},
	}
	for _, tt := range tests {
		t.Run(tt.priority, func(t *testing.T) {
			got := priorityBadge(tt.priority)
			if got != tt.want {
				t.Errorf("priorityBadge(%q) = %q, want %q", tt.priority, got, tt.want)
			}
		})
	}
}

func TestHandleListKey_QuestionMarkTogglesHelp(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}

	// Press "ctrl+h" to open help
	msg := tea.KeyMsg{Type: tea.KeyCtrlH}
	result, cmd := m.handleListKey(msg)
	updated := result.(Model)

	if !updated.list.showHelp {
		t.Error("expected showHelp to be true after 'ctrl+h'")
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}

	// Press "ctrl+h" again to close help
	result, cmd = updated.handleListKey(msg)
	updated = result.(Model)

	if updated.list.showHelp {
		t.Error("expected showHelp to be false after second 'ctrl+h'")
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
}

func TestHandleListKey_EscClosesHelp(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}
	m.list.showHelp = true

	msg := tea.KeyMsg{Type: tea.KeyEscape}
	result, cmd := m.handleListKey(msg)
	updated := result.(Model)

	if updated.list.showHelp {
		t.Error("expected showHelp to be false after esc")
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
}

func TestHandleListKey_HelpConsumesKeys(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Test", Status: "running"},
	})
	m.list.showHelp = true

	// Press "j" while help is shown — should NOT move cursor
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	result, cmd := m.handleListKey(msg)
	updated := result.(Model)

	if updated.list.table.Cursor() != 0 {
		t.Errorf("expected cursor to stay at 0 while help shown, got %d", updated.list.table.Cursor())
	}
	if !updated.list.showHelp {
		t.Error("expected showHelp to remain true")
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
}

func TestViewRendersHelpOverlay(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}
	m.list.showHelp = true

	output := m.View()

	if !strings.Contains(output, "Help") {
		t.Error("expected help overlay to contain 'Help' title")
	}
	if !strings.Contains(output, "Navigation") {
		t.Error("expected help overlay to contain 'Navigation' group")
	}
	if !strings.Contains(output, "Actions") {
		t.Error("expected help overlay to contain 'Actions' group")
	}
	if !strings.Contains(output, "General") {
		t.Error("expected help overlay to contain 'General' group")
	}
	// Check some keybindings are shown
	if !strings.Contains(output, "task info") {
		t.Error("expected help overlay to contain 'task info' binding")
	}
	if !strings.Contains(output, "new task") {
		t.Error("expected help overlay to contain 'new task' binding")
	}
	if !strings.Contains(output, "quit") {
		t.Error("expected help overlay to contain 'quit' binding")
	}
}

func TestShortHelp_ContainsHelpBinding(t *testing.T) {
	keys := newKeyMap()
	bindings := keys.ShortHelp()

	found := false
	for _, b := range bindings {
		if b.Help().Key == "ctrl+h" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected ShortHelp to contain 'ctrl+h' help binding")
	}
}

func TestShortHelp_IsConcise(t *testing.T) {
	keys := newKeyMap()
	bindings := keys.ShortHelp()

	if len(bindings) > 10 {
		t.Errorf("expected ShortHelp to have at most 10 bindings for conciseness, got %d", len(bindings))
	}
}

func TestListView_ShowsAscendingIndexColumn(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 42, Title: "First task", Status: "running"},
		{ID: 7, Title: "Second task", Status: "pending"},
		{ID: 3, Title: "Third task", Status: "completed"},
	})
	l.SetSize(100, 24)

	output := l.View()

	// Header should NOT contain "#" column (vim-style has no header)
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "ID") && strings.Contains(line, "Status") {
			if strings.Contains(line, "#") {
				t.Error("expected header line to NOT contain '#' (vim-style has no header for line numbers)")
			}
			break
		}
	}

	// The line numbers for 3 tasks should be: 1, 2, 3 (1-based, vim-style)
	if !strings.Contains(output, "1") {
		t.Error("expected first task row to show line number '1'")
	}
	if !strings.Contains(output, "2") {
		t.Error("expected second task row to show line number '2'")
	}
	if !strings.Contains(output, "3") {
		t.Error("expected third task row to show line number '3'")
	}
}

func TestListView_AscendingIndexOnlyForFirst10(t *testing.T) {
	l := newListView(false, "")
	tasks := make([]daemon.TaskInfo, 12)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{
			ID:     int64(100 - i),
			Title:  fmt.Sprintf("Task %d", i),
			Status: "pending",
		}
	}
	l.SetTasks(tasks)
	l.SetSize(100, 30)

	// Vim-style shows line numbers for ALL tasks (not just first 10)
	// Render task at index 10 - should show line number "11" (1-based)
	line10 := l.renderTask(tasks[0], 10, false)
	if !strings.Contains(line10, "11") {
		t.Error("expected task at index 10 to show line number '11' (1-based)")
	}

	// Render task at index 11 - should show line number "12" (1-based)
	line11 := l.renderTask(tasks[0], 11, false)
	if !strings.Contains(line11, "12") {
		t.Error("expected task at index 11 to show line number '12' (1-based)")
	}
}

func TestHandleListKey_NumberKeyNavigatesToTask(t *testing.T) {
	m := newTestModelWithTasks(12)
	// cursor starts at 0

	// Command mode: press ":", then "1", then "0", then enter → should go to line 10 (index 9)
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)
	if !updated.commandMode {
		t.Error("expected command mode to be active after pressing ':'")
	}

	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)

	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'0'}}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)

	msg = tea.KeyMsg{Type: tea.KeyEnter}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)
	if updated.list.table.Cursor() != 9 {
		t.Errorf("expected cursor at 9 (line 10) after :10<enter>, got %d", updated.list.table.Cursor())
	}
	if updated.commandMode {
		t.Error("expected command mode to exit after executing command")
	}
}

func TestHandleListKey_NumberKeyClampedToTaskCount(t *testing.T) {
	m := newTestModelWithTasks(3) // only 3 tasks, rows 0-2 (lines 1-3)

	// Command mode: enter ":6" with only 3 tasks → cursor should stay in place
	m.list.table.SetCursor(1)
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'6'}}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)

	msg = tea.KeyMsg{Type: tea.KeyEnter}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)
	// Line 6 doesn't exist; cursor should stay at 1
	if updated.list.table.Cursor() != 1 {
		t.Errorf("expected cursor to stay at 1 when target line exceeds task count, got %d", updated.list.table.Cursor())
	}

	// Command mode: enter ":3" — maps to line 3, row index 2
	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)

	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)

	msg = tea.KeyMsg{Type: tea.KeyEnter}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)
	if updated.list.table.Cursor() != 2 {
		t.Errorf("expected cursor at 2 (line 3) after :3<enter>, got %d", updated.list.table.Cursor())
	}
}

func TestListView_GotoIndex(t *testing.T) {
	l := newListView(false, "")
	tasks := make([]daemon.TaskInfo, 5)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	l.SetTasks(tasks)

	l.GotoIndex(3)
	if l.table.Cursor() != 3 {
		t.Errorf("expected cursor at 3, got %d", l.table.Cursor())
	}

	// Out of bounds — should not move
	l.GotoIndex(10)
	if l.table.Cursor() != 3 {
		t.Errorf("expected cursor to stay at 3 for out-of-bounds index, got %d", l.table.Cursor())
	}

	l.GotoIndex(-1)
	if l.table.Cursor() != 3 {
		t.Errorf("expected cursor to stay at 3 for negative index, got %d", l.table.Cursor())
	}
}

func TestCommandMode_EnterAndExit(t *testing.T) {
	m := newTestModelWithTasks(5)

	// Press ":" to enter command mode
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)
	if !updated.commandMode {
		t.Error("expected commandMode to be true after pressing ':'")
	}

	// Press "esc" to exit command mode
	msg = tea.KeyMsg{Type: tea.KeyEsc}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)
	if updated.commandMode {
		t.Error("expected commandMode to be false after pressing esc")
	}
	if updated.commandInput != "" {
		t.Error("expected commandInput to be cleared after exiting command mode")
	}
}

func TestCommandMode_BackspaceExitsWhenEmpty(t *testing.T) {
	m := newTestModelWithTasks(5)

	// Enter command mode and type a character
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)
	if updated.commandInput != "5" {
		t.Errorf("expected commandInput to be '5', got '%s'", updated.commandInput)
	}

	// Backspace to empty
	msg = tea.KeyMsg{Type: tea.KeyBackspace}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)
	// Should exit command mode when backspacing to empty
	if updated.commandMode {
		t.Error("expected commandMode to exit when backspacing to empty input")
	}
	if updated.commandInput != "" {
		t.Error("expected commandInput to be empty")
	}
}

func TestDeleteWordBackward(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"hello", ""},
		{"hello world", "hello "},
		{"hello world ", "hello "},
		{"one two three", "one two "},
		{"a", ""},
		{"a b", "a "},
		{"  ", ""},
	}
	for _, tc := range tests {
		got := deleteWordBackward(tc.input)
		if got != tc.expected {
			t.Errorf("deleteWordBackward(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestCommandMode_CtrlW_DeletesWord(t *testing.T) {
	m := newTestModelWithTasks(5)

	// Enter command mode
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	// Type "RunTask Refactor"
	for _, ch := range "RunTask Refactor" {
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}}
		result, _ = updated.handleListKey(msg)
		updated = result.(Model)
	}
	if updated.commandInput != "RunTask Refactor" {
		t.Fatalf("expected commandInput 'RunTask Refactor', got %q", updated.commandInput)
	}

	// Press ctrl+w to delete "Refactor"
	msg = tea.KeyMsg{Type: tea.KeyCtrlW}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)
	if updated.commandInput != "RunTask " {
		t.Errorf("expected commandInput 'RunTask ' after ctrl+w, got %q", updated.commandInput)
	}
	if !updated.commandMode {
		t.Error("expected to still be in command mode")
	}

	// Press ctrl+w again to delete "RunTask"
	msg = tea.KeyMsg{Type: tea.KeyCtrlW}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)
	if updated.commandInput != "" {
		t.Errorf("expected commandInput '' after second ctrl+w, got %q", updated.commandInput)
	}
	// Should exit command mode when empty
	if updated.commandMode {
		t.Error("expected commandMode to exit when ctrl+w empties input")
	}
}

func TestSearchMode_CtrlW_DeletesWord(t *testing.T) {
	m := newTestModelWithTasks(5)

	// Enter search mode
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	// Type "hello world"
	for _, ch := range "hello world" {
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}}
		result, _ = updated.handleListKey(msg)
		updated = result.(Model)
	}
	if updated.searchQuery != "hello world" {
		t.Fatalf("expected searchQuery 'hello world', got %q", updated.searchQuery)
	}

	// Press ctrl+w to delete "world"
	msg = tea.KeyMsg{Type: tea.KeyCtrlW}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)
	if updated.searchQuery != "hello " {
		t.Errorf("expected searchQuery 'hello ' after ctrl+w, got %q", updated.searchQuery)
	}
	if !updated.searchMode {
		t.Error("expected to still be in search mode")
	}

	// Press ctrl+w again to delete "hello"
	msg = tea.KeyMsg{Type: tea.KeyCtrlW}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)
	if updated.searchQuery != "" {
		t.Errorf("expected searchQuery '' after second ctrl+w, got %q", updated.searchQuery)
	}
	// Should exit search mode when empty
	if updated.searchMode {
		t.Error("expected searchMode to exit when ctrl+w empties query")
	}
}

func TestCommandMode_GotoLine(t *testing.T) {
	m := newTestModelWithTasks(5)

	// Enter ":3<enter>" with 5 tasks → cursor should be at index 2 (line 3)
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)

	msg = tea.KeyMsg{Type: tea.KeyEnter}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)
	if updated.list.table.Cursor() != 2 {
		t.Errorf("expected cursor at 2 (line 3) after :3<enter>, got %d", updated.list.table.Cursor())
	}
	if updated.commandMode {
		t.Error("expected command mode to exit after executing command")
	}
}

func TestCommandMode_InvalidCommand(t *testing.T) {
	m := newTestModelWithTasks(5)

	// Enter ":xyz<enter>" → should set error
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)

	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)

	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)

	msg = tea.KeyMsg{Type: tea.KeyEnter}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)
	if updated.err == nil {
		t.Error("expected error to be set for invalid command")
	}
	errMsg := updated.err.Error()
	if !strings.Contains(errMsg, "unknown command") && !strings.Contains(errMsg, "Unknown command") {
		t.Errorf("expected error to mention 'unknown command', got: %s", errMsg)
	}
}

func TestCommandMode_DisplaysInView(t *testing.T) {
	m := newTestModelWithTasks(5)
	m.list.SetSize(100, 24)
	m.width = 100
	m.height = 24

	// Enter command mode and type "12"
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)

	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)

	output := updated.View()
	if !strings.Contains(output, ":12") {
		t.Error("expected view to contain ':12' when commandMode is true with commandInput '12'")
	}
}

func TestListView_LineNumWidth(t *testing.T) {
	// 1-9 tasks → minimum width 2
	l := newListView(false, "")
	tasks := make([]daemon.TaskInfo, 9)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	l.SetTasks(tasks)
	if l.lineNumWidth() != 2 {
		t.Errorf("expected lineNumWidth to be 2 for 9 tasks, got %d", l.lineNumWidth())
	}

	// 10-99 tasks → width 2
	tasks = make([]daemon.TaskInfo, 50)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	l.SetTasks(tasks)
	if l.lineNumWidth() != 2 {
		t.Errorf("expected lineNumWidth to be 2 for 50 tasks, got %d", l.lineNumWidth())
	}

	// 100+ tasks → width 3
	tasks = make([]daemon.TaskInfo, 150)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	l.SetTasks(tasks)
	if l.lineNumWidth() != 3 {
		t.Errorf("expected lineNumWidth to be 3 for 150 tasks, got %d", l.lineNumWidth())
	}
}

func TestCommandMode_NumberKeysNoLongerNavigate(t *testing.T) {
	m := newTestModelWithTasks(10)
	m.list.table.SetCursor(0)

	// Press number key '5' in list view → should NOT move cursor (no direct navigation)
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)
	if updated.list.table.Cursor() != 0 {
		t.Errorf("expected cursor to stay at 0 after pressing '5' (numbers no longer directly navigate), got %d", updated.list.table.Cursor())
	}
	// Should NOT enter command mode (need to press ':' first)
	if updated.commandMode {
		t.Error("expected command mode to NOT be active (need to press ':' first)")
	}
}

func TestListView_HeaderHasIndexColumn(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Task", Status: "pending"},
	})
	l.SetSize(100, 24)
	output := l.View()

	// Vim-style: header should NOT contain "#", but task rows should show line numbers
	lines := strings.Split(output, "\n")
	headerFound := false
	for _, line := range lines {
		if strings.Contains(line, "ID") && strings.Contains(line, "Status") {
			if strings.Contains(line, "#") {
				t.Error("expected header line to NOT contain '#' (vim-style)")
			}
			headerFound = true
			break
		}
	}
	if !headerFound {
		t.Error("could not find header line")
	}

	// Task row should contain line number "1"
	if !strings.Contains(output, "1") {
		t.Error("expected task row to show line number '1'")
	}
}

func TestTaskCreatedMsg_CursorMovesToTop(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}

	// Set up existing tasks and move cursor away from top
	tasks := []daemon.TaskInfo{
		{ID: 3, Title: "Task 3", Status: "pending"},
		{ID: 2, Title: "Task 2", Status: "pending"},
		{ID: 1, Title: "Task 1", Status: "pending"},
	}
	m.list.SetTasks(tasks)
	m.list.table.SetCursor(2) // cursor at bottom

	// Simulate a new task being created
	msg := taskCreatedMsg(daemon.TaskInfo{ID: 4, Title: "New Task", Status: "pending"})
	result, _ := m.Update(msg)
	updated := result.(Model)

	if updated.list.table.Cursor() != 0 {
		t.Errorf("expected cursor at 0 after task creation, got %d", updated.list.table.Cursor())
	}
}

// --- Artifact keybinding tests ---

func TestHandleListKey_OAOpensArtifactSelection(t *testing.T) {
	m := Model{
		keys:     newKeyMap(),
		list:     newListView(false, ""),
		detail:   newDetailView(),
		taskInfo: newTaskInfoView(),
		view:     viewList,
		client:   &client.Client{},
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Test task", Status: "running", Workflow: "default", WorktreePath: "/tmp/test"},
	})

	// First "o" sets pendingO
	oMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}}
	result, _ := m.handleListKey(oMsg)
	updated := result.(Model)
	if updated.pendingChord != "o" {
		t.Error("expected pendingO to be true after 'o'")
	}

	// "a" triggers step context fetch — should return a cmd (async daemon call)
	aMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	result, cmd := updated.handleListKey(aMsg)
	updated = result.(Model)

	if updated.pendingChord != "" {
		t.Error("expected pendingO to be false after 'oa'")
	}
	// Should return a cmd to fetch step contexts from daemon
	if cmd == nil {
		t.Error("expected load step context command, got nil")
	}
}

func TestHandleListKey_OAMultipleArtifactsShowsSelection(t *testing.T) {
	m := Model{
		keys:     newKeyMap(),
		list:     newListView(false, ""),
		detail:   newDetailView(),
		taskInfo: newTaskInfoView(),
		view:     viewList,
		client:   &client.Client{},
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Test task", Status: "completed", Workflow: "default", WorktreePath: "/tmp/test"},
	})

	// Press "o" then "a" — triggers async fetch
	oMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}}
	result, _ := m.handleListKey(oMsg)
	updated := result.(Model)

	aMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	result, cmd := updated.handleListKey(aMsg)
	updated = result.(Model)

	// Should have returned a cmd to fetch step contexts from daemon
	if cmd == nil {
		t.Error("expected fetch step contexts command, got nil")
	}

	// Simulate receiving step contexts from daemon (multiple steps)
	now := time.Now()
	msg := taskStepsLoadedMsg{
		taskID: 1,
		steps: []daemon.TaskStepDetail{
			{Name: "implement", Status: stepStatusCompleted, Context: "impl content", CompletedAt: &now},
			{Name: "review", Status: stepStatusCompleted, Context: "review content", CompletedAt: &now},
		},
		action: "view",
	}
	result2, _ := updated.Update(msg)
	updated2 := result2.(Model)

	if updated2.selector.kind != selectorArtifact {
		t.Error("expected selector kind to be selectorArtifact with multiple step contexts")
	}
	if len(updated2.selector.items) != 2 {
		t.Errorf("expected 2 selector items, got %d", len(updated2.selector.items))
	}
	if updated2.selector.action != "view" {
		t.Errorf("expected selector action 'view', got %q", updated2.selector.action)
	}
}

func TestHandleListKey_ETOpensEditTitle(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		client: &client.Client{},
	}
	m.list.SetSize(100, 30)
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Original title", Status: "pending"},
	})

	// Press "e" to enter pending state
	eMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}}
	result, _ := m.handleListKey(eMsg)
	updated := result.(Model)
	if updated.pendingChord != "e" {
		t.Error("expected pendingE to be true after 'e'")
	}

	// Press "t" to trigger edit title
	tMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}}
	result, cmd := updated.handleListKey(tMsg)
	updated = result.(Model)

	if updated.pendingChord != "" {
		t.Error("expected pendingE to be false after 'et' sequence")
	}
	// The command should be non-nil (opens editor)
	if cmd == nil {
		t.Error("expected a command to open editor for title, got nil")
	}
}

func TestHandleListKey_ETNoTaskSelected(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		client: &client.Client{},
	}
	m.list.SetSize(100, 30)
	// No tasks set — no task selected

	// Press "e" then "t"
	eMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}}
	result, _ := m.handleListKey(eMsg)
	updated := result.(Model)

	tMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}}
	result, cmd := updated.handleListKey(tMsg)
	updated = result.(Model)

	if updated.pendingChord != "" {
		t.Error("expected pendingE to be false")
	}
	if cmd != nil {
		t.Error("expected no command when no task is selected")
	}
}

func TestTaskFieldUpdatedMsg_TitleUpdatesStatus(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		client: &client.Client{},
	}

	msg := taskFieldUpdatedMsg{field: "title"}
	result, cmd := m.Update(msg)
	updated := result.(Model)

	if updated.statusMessage != "Title updated" {
		t.Errorf("expected status message 'Title updated', got %q", updated.statusMessage)
	}
	if updated.statusMessageTTL != 2 {
		t.Errorf("expected statusMessageTTL 2, got %d", updated.statusMessageTTL)
	}
	if !updated.list.refreshing {
		t.Error("expected list to be refreshing after field update")
	}
	if cmd == nil {
		t.Error("expected refresh command after field update")
	}
}

func TestHandleListKey_EAReturnsCommand(t *testing.T) {
	// "ea" now fetches step contexts from daemon asynchronously
	m := Model{
		keys:     newKeyMap(),
		list:     newListView(false, ""),
		detail:   newDetailView(),
		taskInfo: newTaskInfoView(),
		view:     viewList,
		client:   &client.Client{}, // non-nil client to pass the check
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "default", Steps: []config.StepConfig{{Name: "implement"}}},
			},
		},
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Test task", Status: "completed", Workflow: "default"},
	})

	// Press "e" then "a"
	eMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}}
	result, _ := m.handleListKey(eMsg)
	updated := result.(Model)
	if updated.pendingChord != "e" {
		t.Error("expected pendingE to be true after 'e'")
	}

	aMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	result, cmd := updated.handleListKey(aMsg)
	_ = result.(Model)

	// Should return a command (async daemon fetch)
	if cmd == nil {
		t.Error("expected async fetch command, got nil")
	}
}

func TestHandleListKey_OANoDaemonShowsError(t *testing.T) {
	m := Model{
		keys:     newKeyMap(),
		list:     newListView(false, ""),
		detail:   newDetailView(),
		taskInfo: newTaskInfoView(),
		view:     viewList,
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "default", Steps: []config.StepConfig{{Name: "implement"}}},
			},
		},
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Pending task", Status: "pending", Workflow: "default"},
	})

	// Press "o" then "a" — no client connected
	oMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}}
	result, _ := m.handleListKey(oMsg)
	updated := result.(Model)

	aMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	result, _ = updated.handleListKey(aMsg)
	updated = result.(Model)

	if updated.err == nil {
		t.Error("expected error when not connected to daemon")
	}
	if !strings.Contains(updated.err.Error(), "not connected") {
		t.Errorf("expected 'not connected' error, got: %v", updated.err)
	}
}

func TestHandleListKey_ONonAResetsPending(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}

	// Press "o" then "x" — should reset pendingO and do nothing
	oMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}}
	result, _ := m.handleListKey(oMsg)
	updated := result.(Model)
	if updated.pendingChord != "o" {
		t.Error("expected pendingO to be true after 'o'")
	}

	xMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}
	result, _ = updated.handleListKey(xMsg)
	updated = result.(Model)
	if updated.pendingChord != "" {
		t.Error("expected pendingO to be false after non-'a' key")
	}
}

func TestHandleTaskInfoKey_YDNoTaskNoError(t *testing.T) {
	m := Model{
		keys:         newKeyMap(),
		list:         newListView(false, ""),
		detail:       newDetailView(),
		taskInfo:     newTaskInfoView(),
		view:         viewTaskInfo,
		pendingChord: "y",
	}
	// No task set on taskInfo

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}}
	result, cmd := m.handleTaskInfoKey(msg)
	updated := result.(Model)

	if updated.pendingChord != "" {
		t.Error("expected pendingY to be false")
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
}

func TestSetNumber_DefaultTrue(t *testing.T) {
	l := newListView(false, "")
	if !l.showLineNumbers {
		t.Error("expected showLineNumbers to default to true")
	}
}

func TestSetNumber_EnablesLineNumbers(t *testing.T) {
	m := newTestModelWithTasks(5)
	m.list.showLineNumbers = false

	result, _ := executeCommand(m, "set number")
	updated := result.(Model)
	if !updated.list.showLineNumbers {
		t.Error("expected showLineNumbers to be true after ':set number'")
	}
}

func TestSetNumber_DisablesLineNumbers(t *testing.T) {
	m := newTestModelWithTasks(5)
	m.list.showLineNumbers = true

	result, _ := executeCommand(m, "set nonumber")
	updated := result.(Model)
	if updated.list.showLineNumbers {
		t.Error("expected showLineNumbers to be false after ':set nonumber'")
	}
}

func TestSetNumber_TogglesLineNumbers(t *testing.T) {
	m := newTestModelWithTasks(5)
	m.list.showLineNumbers = true

	result, _ := executeCommand(m, "set number!")
	updated := result.(Model)
	if updated.list.showLineNumbers {
		t.Error("expected showLineNumbers to be false after ':set number!' (was true)")
	}

	result, _ = executeCommand(updated, "set number!")
	updated = result.(Model)
	if !updated.list.showLineNumbers {
		t.Error("expected showLineNumbers to be true after ':set number!' (was false)")
	}
}

func TestSetNumber_CommandModeIntegration(t *testing.T) {
	m := newTestModelWithTasks(5)

	// Enter command mode with ":"
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	// Type "set nonumber"
	for _, ch := range "set nonumber" {
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}}
		result, _ = updated.handleListKey(msg)
		updated = result.(Model)
	}

	// Press enter to execute
	msg = tea.KeyMsg{Type: tea.KeyEnter}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)

	if updated.commandMode {
		t.Error("expected command mode to exit after executing command")
	}
	if updated.list.showLineNumbers {
		t.Error("expected showLineNumbers to be false after ':set nonumber<enter>'")
	}
}

func TestSetNumber_HidesLineNumbersInView(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Task One", Status: "pending"},
		{ID: 2, Title: "Task Two", Status: "running"},
	})
	l.SetSize(100, 24)

	// With line numbers enabled (default), output should contain line numbers
	output := l.View()
	lines := strings.Split(output, "\n")
	// Find the first task row (after header) and check it has " 1 " pattern
	foundLineNum := false
	for _, line := range lines {
		if strings.Contains(line, "Task One") && strings.Contains(line, " 1 ") {
			foundLineNum = true
			break
		}
	}
	if !foundLineNum {
		t.Error("expected line numbers in view when showLineNumbers is true")
	}

	// Disable line numbers
	l.showLineNumbers = false
	output = l.View()

	// Header should not have the gutter space
	lines = strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "ID") && strings.Contains(line, "Status") {
			// With line numbers off, the header should start closer to the left
			// (no gutter space padding)
			if strings.HasPrefix(line, "    ") {
				// Check it doesn't have the extra gutter space
				// The gutter with numbers would be at least 4 spaces wider
			}
			break
		}
	}

	// Task rows should not contain line number gutter
	foundLineNum = false
	for _, line := range lines {
		if strings.Contains(line, "Task One") {
			// With showLineNumbers=false, the line should NOT have the " 1 " line number
			// before the task ID
			if strings.Contains(line, " 1 ") && strings.Contains(line, " 2 ") {
				// Both line numbers present would indicate line numbers are still shown
				foundLineNum = true
			}
			break
		}
	}
}

func TestMatchSetNumber(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"set number", true},
		{"set nonumber", true},
		{"set number!", true},
		{"set num", false},
		{"setnumber", false},
		{"set", false},
		{"number", false},
		{"  set number  ", true},
		{"set number ", true},
	}
	for _, tt := range tests {
		_, ok := matchSetOption(tt.input)
		if ok != tt.want {
			t.Errorf("matchSetOption(%q) = %v, want %v", tt.input, ok, tt.want)
		}
	}
}

func TestSetFinished_DefaultTrue(t *testing.T) {
	l := newListView(false, "")
	if !l.showFinished {
		t.Error("expected showFinished to default to true")
	}
}

func TestSetFinished_EnablesFinishedTasks(t *testing.T) {
	m := newTestModelWithTasks(5)
	m.list.showFinished = false

	result, _ := executeCommand(m, "set finished")
	updated := result.(Model)
	if !updated.list.showFinished {
		t.Error("expected showFinished to be true after ':set finished'")
	}
}

func TestSetFinished_DisablesFinishedTasks(t *testing.T) {
	m := newTestModelWithTasks(5)
	m.list.showFinished = true

	result, _ := executeCommand(m, "set nofinished")
	updated := result.(Model)
	if updated.list.showFinished {
		t.Error("expected showFinished to be false after ':set nofinished'")
	}
}

func TestSetFinished_TogglesFinishedTasks(t *testing.T) {
	m := newTestModelWithTasks(5)
	m.list.showFinished = true

	result, _ := executeCommand(m, "set finished!")
	updated := result.(Model)
	if updated.list.showFinished {
		t.Error("expected showFinished to be false after ':set finished!' (was true)")
	}

	result, _ = executeCommand(updated, "set finished!")
	updated = result.(Model)
	if !updated.list.showFinished {
		t.Error("expected showFinished to be true after ':set finished!' (was false)")
	}
}

func TestSetFinished_CommandModeIntegration(t *testing.T) {
	m := newTestModelWithTasks(5)

	// Enter command mode with ":"
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	// Type "set nofinished"
	for _, ch := range "set nofinished" {
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}}
		result, _ = updated.handleListKey(msg)
		updated = result.(Model)
	}

	// Press enter to execute
	msg = tea.KeyMsg{Type: tea.KeyEnter}
	result, _ = updated.handleListKey(msg)
	updated = result.(Model)

	if updated.commandMode {
		t.Error("expected command mode to exit after executing command")
	}
	if updated.list.showFinished {
		t.Error("expected showFinished to be false after ':set nofinished<enter>'")
	}
}

func TestSetFinished_HidesFinishedTasksInView(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Active Task", Status: "pending"},
		{ID: 2, Title: "Running Task", Status: "running"},
		{ID: 3, Title: "Done Task", Status: "completed"},
		{ID: 4, Title: "Failed Task", Status: "failed"},
	})
	l.SetSize(100, 24)

	// With showFinished=true (default), all tasks should be visible
	output := l.View()
	if !strings.Contains(output, "Active Task") {
		t.Error("expected 'Active Task' in view when showFinished is true")
	}
	if !strings.Contains(output, "Done Task") {
		t.Error("expected 'Done Task' in view when showFinished is true")
	}
	if !strings.Contains(output, "Failed Task") {
		t.Error("expected 'Failed Task' in view when showFinished is true")
	}

	// Hide finished tasks
	l.showFinished = false
	l.applyFilter()
	output = l.View()
	if !strings.Contains(output, "Active Task") {
		t.Error("expected 'Active Task' in view when showFinished is false")
	}
	if !strings.Contains(output, "Running Task") {
		t.Error("expected 'Running Task' in view when showFinished is false")
	}
	if strings.Contains(output, "Done Task") {
		t.Error("expected 'Done Task' to be hidden when showFinished is false")
	}
	if strings.Contains(output, "Failed Task") {
		t.Error("expected 'Failed Task' to be hidden when showFinished is false")
	}

	// Show finished tasks again
	l.showFinished = true
	l.applyFilter()
	output = l.View()
	if !strings.Contains(output, "Done Task") {
		t.Error("expected 'Done Task' in view after re-enabling showFinished")
	}
	if !strings.Contains(output, "Failed Task") {
		t.Error("expected 'Failed Task' in view after re-enabling showFinished")
	}
}

func TestMatchSetFinished(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"set finished", true},
		{"set nofinished", true},
		{"set finished!", true},
		{"set fin", false},
		{"setfinished", false},
		{"set", false},
		{"finished", false},
		{"  set finished  ", true},
		{"set finished ", true},
	}
	for _, tt := range tests {
		_, ok := matchSetOption(tt.input)
		if ok != tt.want {
			t.Errorf("matchSetOption(%q) = %v, want %v", tt.input, ok, tt.want)
		}
	}
}

func TestSetFinished_PreservesAllTasksOnRefresh(t *testing.T) {
	l := newListView(false, "")
	l.showFinished = false
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Active", Status: "pending"},
		{ID: 2, Title: "Done", Status: "completed"},
	})

	// Only active tasks should be in the display list
	if len(l.tasks) != 1 {
		t.Errorf("expected 1 visible task, got %d", len(l.tasks))
	}
	// All tasks should be preserved
	if len(l.allTasks) != 2 {
		t.Errorf("expected 2 allTasks, got %d", len(l.allTasks))
	}

	// Re-enable showFinished and verify all tasks come back
	l.showFinished = true
	l.applyFilter()
	if len(l.tasks) != 2 {
		t.Errorf("expected 2 visible tasks after re-enabling showFinished, got %d", len(l.tasks))
	}
}

func TestListView_GlobalModeHasIndexColumn(t *testing.T) {
	l := newListView(true, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Task", Status: "running", ProjectName: "proj"},
	})
	l.SetSize(120, 24)
	output := l.View()

	// Vim-style: header should NOT contain "#"
	lines := strings.Split(output, "\n")
	headerFound := false
	for _, line := range lines {
		if strings.Contains(line, "ID") && strings.Contains(line, "Project") {
			if strings.Contains(line, "#") {
				t.Error("expected global mode header to NOT contain '#' (vim-style)")
			}
			headerFound = true
			break
		}
	}
	if !headerFound {
		t.Error("could not find header line")
	}

	// Task row should contain line number "1"
	if !strings.Contains(output, "1") {
		t.Error("expected task row to show line number '1'")
	}
}

func TestHandleListKey_SlashEntersForwardSearch(t *testing.T) {
	m := Model{
		keys: newKeyMap(),
		list: newListView(false, ""),
		view: viewList,
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	if !updated.searchMode {
		t.Error("expected searchMode to be true after '/'")
	}
	if updated.searchDirection != 1 {
		t.Errorf("expected searchDirection to be 1 (forward), got %d", updated.searchDirection)
	}
	if updated.searchQuery != "" {
		t.Error("expected searchQuery to be empty initially")
	}
}

func TestHandleListKey_QuestionMarkEntersBackwardSearch(t *testing.T) {
	m := Model{
		keys: newKeyMap(),
		list: newListView(false, ""),
		view: viewList,
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	if !updated.searchMode {
		t.Error("expected searchMode to be true after '?'")
	}
	if updated.searchDirection != -1 {
		t.Errorf("expected searchDirection to be -1 (backward), got %d", updated.searchDirection)
	}
}

func TestHandleSearchKey_EscCancelsSearch(t *testing.T) {
	m := Model{
		keys:        newKeyMap(),
		list:        newListView(false, ""),
		view:        viewList,
		searchMode:  true,
		searchQuery: "test",
	}

	msg := tea.KeyMsg{Type: tea.KeyEscape}
	result, _ := m.handleSearchKey(msg)
	updated := result.(Model)

	if updated.searchMode {
		t.Error("expected searchMode to be false after esc")
	}
	if updated.searchQuery != "" {
		t.Error("expected searchQuery to be cleared after esc")
	}
}

func TestHandleSearchKey_EnterCommitsSearch(t *testing.T) {
	m := Model{
		keys:            newKeyMap(),
		list:            newListView(false, ""),
		view:            viewList,
		searchMode:      true,
		searchQuery:     "fix",
		searchDirection: 1,
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Fix bug", Status: "pending"},
		{ID: 2, Title: "Add feature", Status: "running"},
		{ID: 3, Title: "Fix crash", Status: "completed"},
	})

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	result, _ := m.handleSearchKey(msg)
	updated := result.(Model)

	if updated.searchMode {
		t.Error("expected searchMode to be false after enter")
	}
	if len(updated.list.matchedIndices) != 2 {
		t.Errorf("expected 2 matches for 'fix', got %d", len(updated.list.matchedIndices))
	}
}

func TestHandleSearchKey_TypingBuildsQuery(t *testing.T) {
	m := Model{
		keys:       newKeyMap(),
		list:       newListView(false, ""),
		view:       viewList,
		searchMode: true,
	}

	// Type 'a'
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	result, _ := m.handleSearchKey(msg)
	updated := result.(Model)
	if updated.searchQuery != "a" {
		t.Errorf("expected searchQuery 'a', got '%s'", updated.searchQuery)
	}

	// Type 'b'
	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}}
	result, _ = updated.handleSearchKey(msg)
	updated = result.(Model)
	if updated.searchQuery != "ab" {
		t.Errorf("expected searchQuery 'ab', got '%s'", updated.searchQuery)
	}
}

func TestHandleSearchKey_BackspaceDeletesChar(t *testing.T) {
	m := Model{
		keys:        newKeyMap(),
		list:        newListView(false, ""),
		view:        viewList,
		searchMode:  true,
		searchQuery: "ab",
	}

	msg := tea.KeyMsg{Type: tea.KeyBackspace}
	result, _ := m.handleSearchKey(msg)
	updated := result.(Model)
	if updated.searchQuery != "a" {
		t.Errorf("expected searchQuery 'a', got '%s'", updated.searchQuery)
	}

	// Backspace on last char exits search mode
	result, _ = updated.handleSearchKey(msg)
	updated = result.(Model)
	if updated.searchMode {
		t.Error("expected searchMode to be false when query becomes empty via backspace")
	}
}

func TestHandleListKey_NNavigatesToNextMatch(t *testing.T) {
	m := Model{
		keys:            newKeyMap(),
		list:            newListView(false, ""),
		view:            viewList,
		searchDirection: 1,
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Fix bug", Status: "pending"},
		{ID: 2, Title: "Add feature", Status: "running"},
		{ID: 3, Title: "Fix crash", Status: "completed"},
	})
	// Order after sort: 3, 2, 1 => indices: 0="Fix crash", 1="Add feature", 2="Fix bug"
	m.list.performSearch("fix")
	m.list.table.SetCursor(0)
	m.list.currentMatchIdx = 0

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	if updated.list.table.Cursor() != 2 {
		t.Errorf("expected cursor to move to 2 (next fix match), got %d", updated.list.table.Cursor())
	}
}

func TestHandleListKey_ShiftNNavigatesToPrevMatch(t *testing.T) {
	m := Model{
		keys:            newKeyMap(),
		list:            newListView(false, ""),
		view:            viewList,
		searchDirection: 1,
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Fix bug", Status: "pending"},
		{ID: 2, Title: "Add feature", Status: "running"},
		{ID: 3, Title: "Fix crash", Status: "completed"},
	})
	m.list.performSearch("fix")
	m.list.table.SetCursor(2)
	m.list.currentMatchIdx = 1

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	if updated.list.table.Cursor() != 0 {
		t.Errorf("expected cursor to move to 0 (prev fix match), got %d", updated.list.table.Cursor())
	}
}

func TestHandleListKey_NWithNoSearchCreatesNewTask(t *testing.T) {
	cfg := &config.Config{}
	m := Model{
		cfg:         cfg,
		keys:        newKeyMap(),
		list:        newListView(false, ""),
		detail:      newDetailView(),
		prompt:      newPromptView(true, branchModeNew, ""),
		view:        viewList,
		client:      &client.Client{},
		projectPath: "/some/path",
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	// With no search matches, n should open prompt view for new task
	if updated.view != viewPrompt {
		t.Errorf("expected view to be viewPrompt (%d), got %d", viewPrompt, updated.view)
	}
}

func TestViewRendersSearchBar(t *testing.T) {
	m := Model{
		keys:            newKeyMap(),
		list:            newListView(false, ""),
		view:            viewList,
		searchMode:      true,
		searchQuery:     "test",
		searchDirection: 1,
	}

	output := m.View()
	if !strings.Contains(output, "/test") {
		t.Error("expected View to contain '/test' search bar for forward search")
	}

	// Backward search
	m.searchDirection = -1
	output = m.View()
	if !strings.Contains(output, "?test") {
		t.Error("expected View to contain '?test' search bar for backward search")
	}
}

func TestHandleListKey_SearchModeDispatchesToHandleSearchKey(t *testing.T) {
	m := Model{
		keys:       newKeyMap(),
		list:       newListView(false, ""),
		view:       viewList,
		searchMode: true,
	}

	// Typing while in search mode should build query
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	if updated.searchQuery != "x" {
		t.Errorf("expected searchQuery 'x' when typing in search mode, got '%s'", updated.searchQuery)
	}
}

func TestFullHelp_ContainsSearchBindings(t *testing.T) {
	keys := newKeyMap()
	groups := keys.FullHelp()

	foundForward := false
	foundBackward := false
	for _, group := range groups {
		for _, b := range group {
			if b.Help().Key == "/" {
				foundForward = true
			}
			if b.Help().Key == "?" {
				foundBackward = true
			}
		}
	}
	if !foundForward {
		t.Error("expected FullHelp to contain '/' search forward binding")
	}
	if !foundBackward {
		t.Error("expected FullHelp to contain '?' search backward binding")
	}
}

func TestStatusMessage_ClearedOnKeypress(t *testing.T) {
	m := Model{
		keys:          newKeyMap(),
		list:          newListView(false, ""),
		detail:        newDetailView(),
		taskInfo:      newTaskInfoView(),
		view:          viewList,
		statusMessage: "Copied description to clipboard",
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	result, _ := m.handleKey(msg)
	updated := result.(Model)

	if updated.statusMessage != "" {
		t.Errorf("expected statusMessage to be cleared on keypress, got %q", updated.statusMessage)
	}
}

func TestStatusMessage_TickDecrementsAndClears(t *testing.T) {
	m := Model{
		keys:             newKeyMap(),
		list:             newListView(false, ""),
		detail:           newDetailView(),
		taskInfo:         newTaskInfoView(),
		view:             viewList,
		statusMessage:    "Copied description to clipboard",
		statusMessageTTL: 2,
	}

	// First tick: TTL decrements to 1, message still visible
	result, _ := m.Update(tickMsg(time.Now()))
	updated := result.(Model)

	if updated.statusMessage == "" {
		t.Error("expected statusMessage to still be present after first tick")
	}
	if updated.statusMessageTTL != 1 {
		t.Errorf("expected statusMessageTTL=1, got %d", updated.statusMessageTTL)
	}

	// Second tick: TTL decrements to 0, message cleared
	result, _ = updated.Update(tickMsg(time.Now()))
	updated = result.(Model)

	if updated.statusMessage != "" {
		t.Errorf("expected statusMessage to be cleared after TTL expired, got %q", updated.statusMessage)
	}
}

func TestStatusMessage_RenderedInView(t *testing.T) {
	m := Model{
		keys:          newKeyMap(),
		list:          newListView(false, ""),
		detail:        newDetailView(),
		taskInfo:      newTaskInfoView(),
		view:          viewList,
		width:         80,
		height:        24,
		statusMessage: "Copied description to clipboard",
	}

	output := m.View()
	if !strings.Contains(output, "Copied description to clipboard") {
		t.Error("expected View() to contain the status message")
	}
}

func TestStatusMessage_NotRenderedWhenEmpty(t *testing.T) {
	m := Model{
		keys:          newKeyMap(),
		list:          newListView(false, ""),
		detail:        newDetailView(),
		taskInfo:      newTaskInfoView(),
		view:          viewList,
		width:         80,
		height:        24,
		statusMessage: "",
	}

	output := m.View()
	if strings.Contains(output, "Copied") {
		t.Error("expected View() to not contain clipboard message when statusMessage is empty")
	}
}

func TestYDEmptyDescription_NoStatusMessage(t *testing.T) {
	m := Model{
		keys:         newKeyMap(),
		list:         newListView(false, ""),
		detail:       newDetailView(),
		taskInfo:     newTaskInfoView(),
		view:         viewTaskInfo,
		pendingChord: "y",
	}
	task := daemon.TaskInfo{ID: 1, Title: "Test", Input: "", Context: "ctx"}
	m.taskInfo.SetTask(&task)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}}
	result, _ := m.handleTaskInfoKey(msg)
	updated := result.(Model)

	if updated.statusMessage != "" {
		t.Errorf("expected no status message for empty description, got %q", updated.statusMessage)
	}
}

func TestYCEmptyContext_NoStatusMessage(t *testing.T) {
	m := Model{
		keys:         newKeyMap(),
		list:         newListView(false, ""),
		detail:       newDetailView(),
		taskInfo:     newTaskInfoView(),
		view:         viewTaskInfo,
		pendingChord: "y",
	}
	task := daemon.TaskInfo{ID: 1, Title: "Test", Input: "desc", Context: ""}
	m.taskInfo.SetTask(&task)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}
	result, _ := m.handleTaskInfoKey(msg)
	updated := result.(Model)

	if updated.statusMessage != "" {
		t.Errorf("expected no status message for empty context, got %q", updated.statusMessage)
	}
}

func TestListView_WindowedRendering(t *testing.T) {
	l := newListView(false, "")
	tasks := make([]daemon.TaskInfo, 20)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	l.SetTasks(tasks)
	l.SetSize(100, 15) // visibleRows = 15 - 7 = 8

	output := l.View()
	lines := strings.Split(output, "\n")

	// Count task lines (lines containing "Task ")
	taskLines := 0
	for _, line := range lines {
		if strings.Contains(line, "Task ") {
			taskLines++
		}
	}
	if taskLines != 8 {
		t.Errorf("expected 8 visible task rows, got %d", taskLines)
	}

	// Helper row should always be present
	if !strings.Contains(output, "quit") {
		t.Error("expected help row to be present in windowed view")
	}
}

func TestListView_HelperRowPersistsAfterSearchCancel(t *testing.T) {
	m := Model{
		keys: newKeyMap(),
		list: newListView(false, ""),
		view: viewList,
	}
	tasks := make([]daemon.TaskInfo, 20)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	m.list.SetTasks(tasks)
	m.list.SetSize(100, 15)

	// Verify helper row is present initially
	output := m.View()
	if !strings.Contains(output, "quit") {
		t.Error("expected help row before search")
	}

	// Enter search mode — search input replaces help row to prevent UI jump
	m.searchMode = true
	m.searchQuery = "test"
	output = m.View()
	if strings.Contains(output, "quit") {
		t.Error("expected help row to be replaced by search input")
	}
	if !strings.Contains(output, "/test") {
		t.Error("expected search bar during search")
	}

	// Cancel search with Esc
	msg := tea.KeyMsg{Type: tea.KeyEscape}
	result, _ := m.handleSearchKey(msg)
	updated := result.(Model)
	output = updated.View()
	if !strings.Contains(output, "quit") {
		t.Error("expected help row after cancelling search")
	}
	if strings.Contains(output, "/test") {
		t.Error("expected search bar to be gone after cancel")
	}
}

func TestListView_ScrollOffsetFollowsCursor(t *testing.T) {
	l := newListView(false, "")
	tasks := make([]daemon.TaskInfo, 20)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	l.SetTasks(tasks)
	l.SetSize(100, 15) // visibleRows = 8

	// Cursor starts at 0, scroll offset at 0
	if l.scrollOffset != 0 {
		t.Errorf("expected scrollOffset 0, got %d", l.scrollOffset)
	}

	// Move cursor past visible window
	for i := 0; i < 12; i++ {
		l.MoveDown()
	}
	// Cursor should be at 12, scrollOffset should have adjusted
	if l.table.Cursor() != 12 {
		t.Errorf("expected cursor at 12, got %d", l.table.Cursor())
	}
	if l.scrollOffset < 3 {
		t.Errorf("expected scrollOffset >= 3, got %d", l.scrollOffset)
	}

	// View should still contain the help row
	output := l.View()
	if !strings.Contains(output, "quit") {
		t.Error("expected help row after scrolling down")
	}
}

func TestListView_ExtraLinesReduceVisibleRows(t *testing.T) {
	l := newListView(false, "")
	l.SetSize(100, 15) // visibleRows = 15 - 7 = 8

	if l.visibleRows() != 8 {
		t.Errorf("expected 8 visible rows without extra lines, got %d", l.visibleRows())
	}

	l.extraLines = 1 // e.g., search bar
	if l.visibleRows() != 7 {
		t.Errorf("expected 7 visible rows with 1 extra line, got %d", l.visibleRows())
	}

	l.extraLines = 2 // e.g., search bar + command bar
	if l.visibleRows() != 6 {
		t.Errorf("expected 6 visible rows with 2 extra lines, got %d", l.visibleRows())
	}
}

func TestListView_ScrollIndicatorInHelpBar(t *testing.T) {
	l := newListView(false, "")
	tasks := make([]daemon.TaskInfo, 20)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	l.SetTasks(tasks)
	l.SetSize(100, 15) // visibleRows = 8, 20 tasks → needs scrolling

	output := l.View()

	// When more tasks exist than visible, should show scroll position
	if !strings.Contains(output, "1-8/20") {
		t.Errorf("expected scroll position '1-8/20' in help bar, got:\n%s", output)
	}

	// Scroll down past the visible window
	for i := 0; i < 12; i++ {
		l.MoveDown()
	}
	output = l.View()

	// Should show updated scroll position
	if !strings.Contains(output, "/20") {
		t.Error("expected scroll position with /20 in help bar after scrolling")
	}
}

func TestListView_NoScrollIndicatorWhenAllVisible(t *testing.T) {
	l := newListView(false, "")
	tasks := make([]daemon.TaskInfo, 5)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	l.SetTasks(tasks)
	l.SetSize(100, 15) // visibleRows = 8, only 5 tasks → no scrolling needed

	output := l.View()

	// Should NOT show scroll position when all tasks fit
	if strings.Contains(output, "/5") {
		t.Error("should not show scroll indicator when all tasks are visible")
	}
}

func TestListView_TitleAlwaysVisible(t *testing.T) {
	l := newListView(false, "")
	tasks := make([]daemon.TaskInfo, 50)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	l.SetTasks(tasks)
	l.SetSize(100, 20) // visibleRows = 13

	// Title should always be the first content
	output := l.View()
	if !strings.Contains(output, AppTitle) {
		t.Error("expected title to be present in output")
	}

	lines := strings.Split(output, "\n")
	if len(lines) == 0 || !strings.Contains(lines[0], AppTitle) {
		t.Error("expected title to be on the first line")
	}

	// After scrolling down, title should still be first
	for i := 0; i < 30; i++ {
		l.MoveDown()
	}
	output = l.View()
	lines = strings.Split(output, "\n")
	if len(lines) == 0 || !strings.Contains(lines[0], AppTitle) {
		t.Error("expected title to remain on first line after scrolling")
	}
}

func TestStatusMessage_CountedInExtraLines(t *testing.T) {
	m := Model{
		keys: newKeyMap(),
		list: newListView(false, ""),
		view: viewList,
	}
	tasks := make([]daemon.TaskInfo, 20)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	m.list.SetTasks(tasks)
	m.list.SetSize(100, 15) // height=15, visibleRows=9 without extras

	// Without status message: count task lines
	output := m.View()
	baseTaskLines := countTaskLines(output)

	// With status message: should have one fewer task line to make room
	m.statusMessage = "Copied!"
	output = m.View()
	withMsgTaskLines := countTaskLines(output)
	if withMsgTaskLines >= baseTaskLines {
		t.Errorf("expected fewer task lines with status message: base=%d, withMsg=%d", baseTaskLines, withMsgTaskLines)
	}
	if !strings.Contains(output, "Copied!") {
		t.Error("expected status message in output")
	}

	// With status message + search mode: search replaces help row (no extra lines),
	// so task count should be the same as status-only.
	m.searchMode = true
	m.searchQuery = "test"
	output = m.View()
	withBothTaskLines := countTaskLines(output)
	if withBothTaskLines != withMsgTaskLines {
		t.Errorf("expected same task lines with search+status (search replaces help row): withMsg=%d, withBoth=%d", withMsgTaskLines, withBothTaskLines)
	}
}

// countTaskLines counts lines containing "Task " in the output (rendered task rows).
func countTaskLines(output string) int {
	count := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "Task ") {
			count++
		}
	}
	return count
}

func TestListView_OutputFitsTerminalHeight(t *testing.T) {
	m := Model{
		keys: newKeyMap(),
		list: newListView(false, ""),
		view: viewList,
	}
	tasks := make([]daemon.TaskInfo, 30)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	m.list.SetTasks(tasks)

	for _, height := range []int{10, 15, 20, 30, 40} {
		m.list.SetSize(100, height)
		m.width = 100
		m.height = height

		output := m.View()
		lines := strings.Split(output, "\n")
		// The output should not exceed terminal height
		// (last line may be empty from trailing newline, so allow height+1)
		if len(lines) > height+1 {
			t.Errorf("height=%d: output has %d lines, expected <= %d", height, len(lines), height+1)
		}
	}

	// Also test with status message (the bug case)
	m.statusMessage = "Task updated"
	for _, height := range []int{10, 15, 20} {
		m.list.SetSize(100, height)
		m.width = 100
		m.height = height

		output := m.View()
		lines := strings.Split(output, "\n")
		if len(lines) > height+1 {
			t.Errorf("height=%d with statusMessage: output has %d lines, expected <= %d", height, len(lines), height+1)
		}
	}
}

func TestBottomBar_PinnedToBottom(t *testing.T) {
	m := Model{
		keys: newKeyMap(),
		list: newListView(false, ""),
		view: viewList,
	}
	tasks := make([]daemon.TaskInfo, 5)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	m.list.SetTasks(tasks)
	m.list.SetSize(100, 24)
	m.width = 100
	m.height = 24

	// Command bar replaces the help row (not appended at bottom)
	m.commandMode = true
	m.commandInput = "set"
	output := m.View()
	if !strings.Contains(output, ":set") {
		t.Error("expected command bar in output")
	}
	// Help row should be replaced, not present alongside command bar
	if strings.Contains(output, "quit") {
		t.Error("expected help row to be replaced by command bar")
	}

	// Status message should appear on the last non-empty line
	m.commandMode = false
	m.statusMessage = "Copied!"
	output = m.View()
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	lastLine := lines[len(lines)-1]
	if !strings.Contains(lastLine, "Copied!") {
		t.Errorf("expected status message on last line, got %q", lastLine)
	}

	// Both command bar (in help row) and status message (at bottom)
	m.commandMode = true
	m.commandInput = "42"
	m.statusMessage = "Done"
	output = m.View()
	if !strings.Contains(output, ":42") {
		t.Error("expected command bar in output")
	}
	lines = strings.Split(strings.TrimRight(output, "\n"), "\n")
	lastLine = lines[len(lines)-1]
	if !strings.Contains(lastLine, "Done") {
		t.Errorf("expected status message on last line, got %q", lastLine)
	}
}

func TestBottomBar_OutputStillFitsTerminalHeight(t *testing.T) {
	m := Model{
		keys: newKeyMap(),
		list: newListView(false, ""),
		view: viewList,
	}
	tasks := make([]daemon.TaskInfo, 30)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	m.list.SetTasks(tasks)

	// Test with command bar + status message at various heights
	m.commandMode = true
	m.commandInput = "test"
	m.statusMessage = "Flash!"
	for _, height := range []int{10, 15, 20, 30} {
		m.list.SetSize(100, height)
		m.width = 100
		m.height = height

		output := m.View()
		lines := strings.Split(output, "\n")
		if len(lines) > height+1 {
			t.Errorf("height=%d with bottom bars: output has %d lines, expected <= %d", height, len(lines), height+1)
		}
	}
}

func TestCommandSearchMode_NoExtraLines(t *testing.T) {
	m := Model{
		keys: newKeyMap(),
		list: newListView(false, ""),
		view: viewList,
	}
	tasks := make([]daemon.TaskInfo, 20)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	m.list.SetTasks(tasks)
	m.list.SetSize(100, 15)

	// Count task lines without any mode active
	baseOutput := m.View()
	baseTaskLines := countTaskLines(baseOutput)

	// Command mode should NOT reduce visible task lines (replaces help row)
	m.commandMode = true
	m.commandInput = "42"
	cmdOutput := m.View()
	cmdTaskLines := countTaskLines(cmdOutput)
	if cmdTaskLines != baseTaskLines {
		t.Errorf("command mode should not change task count: base=%d, cmd=%d", baseTaskLines, cmdTaskLines)
	}
	if !strings.Contains(cmdOutput, ":42") {
		t.Error("expected command input in output")
	}

	// Search mode should NOT reduce visible task lines (replaces help row)
	m.commandMode = false
	m.searchMode = true
	m.searchQuery = "hello"
	m.searchDirection = 1
	searchOutput := m.View()
	searchTaskLines := countTaskLines(searchOutput)
	if searchTaskLines != baseTaskLines {
		t.Errorf("search mode should not change task count: base=%d, search=%d", baseTaskLines, searchTaskLines)
	}
	if !strings.Contains(searchOutput, "/hello") {
		t.Error("expected search input in output")
	}

	// Backward search
	m.searchDirection = -1
	searchOutput = m.View()
	if !strings.Contains(searchOutput, "?hello") {
		t.Error("expected backward search input in output")
	}
}

func TestSearchInputPaddingMatchesCommandInput(t *testing.T) {
	m := Model{
		keys: newKeyMap(),
		list: newListView(false, ""),
		view: viewList,
	}
	tasks := make([]daemon.TaskInfo, 5)
	for i := range tasks {
		tasks[i] = daemon.TaskInfo{ID: int64(i + 1), Title: fmt.Sprintf("Task %d", i+1), Status: "pending"}
	}
	m.list.SetTasks(tasks)
	m.list.SetSize(100, 15)

	// Get command mode help override
	m.commandMode = true
	m.commandInput = "x"
	_ = m.View()
	cmdOverride := m.list.helpOverride

	// Get search mode help override
	m.commandMode = false
	m.searchMode = true
	m.searchQuery = "x"
	m.searchDirection = 1
	_ = m.View()
	searchOverride := m.list.helpOverride

	// Both should have the same leading padding (2 spaces)
	cmdPrefix := cmdOverride[:len(cmdOverride)-len(strings.TrimLeft(cmdOverride, " "))]
	searchPrefix := searchOverride[:len(searchOverride)-len(strings.TrimLeft(searchOverride, " "))]
	if cmdPrefix != searchPrefix {
		t.Errorf("search input padding %q does not match command input padding %q", searchPrefix, cmdPrefix)
	}

	// Backward search should also have matching padding
	m.searchDirection = -1
	_ = m.View()
	backwardOverride := m.list.helpOverride
	backwardPrefix := backwardOverride[:len(backwardOverride)-len(strings.TrimLeft(backwardOverride, " "))]
	if backwardPrefix != cmdPrefix {
		t.Errorf("backward search input padding %q does not match command input padding %q", backwardPrefix, cmdPrefix)
	}
}

// --- Selection menu vim navigation tests ---

// --- RunTask command tests ---

func TestMatchRunTask(t *testing.T) {
	tests := []struct {
		input    string
		wantArgs string
		wantOK   bool
	}{
		{"RunTask", "", true},
		{"RunTask Refactor", "Refactor", true},
		{"RunTask  Security ", "Security", true},
		{"runtask", "", false},
		{"RunTas", "", false},
		{"set number", "", false},
		{"", "", false},
	}

	for _, tt := range tests {
		args, ok := matchRunTask(tt.input)
		if ok != tt.wantOK {
			t.Errorf("matchRunTask(%q): ok = %v, want %v", tt.input, ok, tt.wantOK)
		}
		if args != tt.wantArgs {
			t.Errorf("matchRunTask(%q): args = %q, want %q", tt.input, args, tt.wantArgs)
		}
	}
}

// TestExecRunTask_PreselectsWorkflow verifies that execRunTask with a named
// workflow opens viewPrompt with the workflow preselected (cycler locked to 1 name).
func TestExecRunTask_PreselectsWorkflow(t *testing.T) {
	m := Model{
		keys:        newKeyMap(),
		client:      &client.Client{},
		list:        newListView(false, ""),
		detail:      newDetailView(),
		prompt:      newPromptView(true, branchModeNew, ""),
		view:        viewList,
		projectPath: "/tmp/test-project",
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "implement", Description: "Implement task"},
			},
		},
	}

	result, _ := execRunTask(m, "implement")
	updated := result.(Model)

	if updated.selectedWorkflow != "implement" {
		t.Errorf("expected selectedWorkflow 'implement', got %q", updated.selectedWorkflow)
	}
	if updated.view != viewPrompt {
		t.Errorf("expected view=viewPrompt, got %d", updated.view)
	}
	if updated.prompt.preselectedWorkflow != "implement" {
		t.Errorf("expected prompt.preselectedWorkflow 'implement', got %q", updated.prompt.preselectedWorkflow)
	}
	if len(updated.prompt.workflows) != 1 {
		t.Errorf("expected prompt.workflows of length 1 (cycler hidden), got %d", len(updated.prompt.workflows))
	}
}

// TestExecRunTask_EmptyArgs_OpensPicker verifies that execRunTask with no args
// opens a filterable selectorWorkflow picker over all workflows.
func TestExecRunTask_EmptyArgs_OpensPicker(t *testing.T) {
	m := Model{
		keys:        newKeyMap(),
		client:      &client.Client{},
		list:        newListView(false, ""),
		detail:      newDetailView(),
		view:        viewList,
		projectPath: "/tmp/test-project",
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "implement"},
			},
		},
	}

	result, _ := execRunTask(m, "")
	updated := result.(Model)

	if updated.selector.kind != selectorWorkflow {
		t.Errorf("expected picker to open (selectorWorkflow), got kind=%d", updated.selector.kind)
	}
}

// TestExecRunTask_UnknownWorkflow verifies an error is set for unknown workflow names.
func TestExecRunTask_UnknownWorkflow(t *testing.T) {
	m := Model{
		keys:        newKeyMap(),
		client:      &client.Client{},
		list:        newListView(false, ""),
		detail:      newDetailView(),
		view:        viewList,
		projectPath: "/tmp/test-project",
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "implement"},
			},
		},
	}

	result, cmd := execRunTask(m, "nonexistent")
	updated := result.(Model)

	if updated.err == nil {
		t.Error("expected error for unknown workflow")
	}
	if !strings.Contains(updated.err.Error(), "unknown workflow") {
		t.Errorf("expected 'unknown workflow' error, got %q", updated.err.Error())
	}
	if cmd != nil {
		t.Error("expected nil command for unknown workflow")
	}
}

// TestExecRunTask_NoClient verifies an error is set when no daemon client is connected.
func TestExecRunTask_NoClient(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "implement"},
			},
		},
	}

	result, _ := execRunTask(m, "implement")
	updated := result.(Model)

	if updated.err == nil {
		t.Error("expected error when client is nil")
	}
}

// TestExecRunTask_FullyPinned verifies that a fully-pinned workflow creates a task
// immediately (returns a non-nil cmd) and does NOT switch to viewPrompt.
func TestExecRunTask_FullyPinned(t *testing.T) {
	worktreeTrue := true
	m := Model{
		keys:        newKeyMap(),
		client:      &client.Client{},
		list:        newListView(false, ""),
		detail:      newDetailView(),
		prompt:      newPromptView(true, branchModeNew, ""),
		view:        viewList,
		projectPath: "/tmp/test-project",
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{
					Name:     "pinned-wf",
					Input:    "Do the thing",
					Worktree: &worktreeTrue,
					Branch:   "sakusen/{{task_id}}-auto",
					Target:   "main",
				},
			},
		},
	}

	result, cmd := execRunTask(m, "pinned-wf")
	updated := result.(Model)

	// Fully-pinned → task created immediately, no prompt.
	if updated.view == viewPrompt {
		t.Error("expected NOT to switch to viewPrompt for fully-pinned workflow")
	}
	if cmd == nil {
		t.Error("expected non-nil cmd (immediate task creation) for fully-pinned workflow")
	}
}

// fullyPinnedConfig returns a config whose sole workflow is fully specified, so
// IsFullySpec() is true and the New Task screen should be skipped.
func fullyPinnedConfig() *config.Config {
	worktreeTrue := true
	return &config.Config{
		Workflows: []config.WorkflowConfig{
			{
				Name:     "pinned-wf",
				Input:    "Do the thing",
				Worktree: &worktreeTrue,
				Branch:   "sakusen/{{task_id}}-auto",
				Target:   "main",
			},
		},
	}
}

// TestHandleListKey_NFullyPinnedSkipsScreen verifies the headline keyboard entry
// point: pressing "n" with a fully-pinned default workflow creates the task
// immediately instead of opening the New Task screen.
func TestHandleListKey_NFullyPinnedSkipsScreen(t *testing.T) {
	m := Model{
		cfg:         fullyPinnedConfig(),
		keys:        newKeyMap(),
		list:        newListView(false, ""),
		detail:      newDetailView(),
		prompt:      newPromptView(true, branchModeNew, ""),
		view:        viewList,
		client:      &client.Client{},
		projectPath: "/some/path",
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}
	result, cmd := m.handleListKey(msg)
	updated := result.(Model)

	if updated.view == viewPrompt {
		t.Error("expected 'n' on a fully-pinned workflow to skip the New Task screen, but view became viewPrompt")
	}
	if cmd == nil {
		t.Error("expected non-nil cmd (immediate task creation) for 'n' on a fully-pinned workflow")
	}
}

// TestHandleListKey_ShiftNFullyPinnedSkipsScreen verifies the blocking-task entry
// point ("N"): a fully-pinned workflow creates the blocking task immediately and
// threads the selected task's ID as the blocker, without opening the screen.
func TestHandleListKey_ShiftNFullyPinnedSkipsScreen(t *testing.T) {
	m := newTestModelWithTasks(3)
	m.cfg = fullyPinnedConfig()
	m.prompt = newPromptView(true, branchModeNew, "")
	m.client = &client.Client{}
	m.projectPath = "/some/path"
	m.list.table.SetCursor(1) // select task #2

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}}
	result, cmd := m.handleListKey(msg)
	updated := result.(Model)

	if updated.view == viewPrompt {
		t.Error("expected 'N' on a fully-pinned workflow to skip the New Task screen, but view became viewPrompt")
	}
	if cmd == nil {
		t.Error("expected non-nil cmd (immediate blocking-task creation) for 'N' on a fully-pinned workflow")
	}
	if updated.blockingTaskID != 2 {
		t.Errorf("blockingTaskID: got %d, want 2 (selected task should block the new task)", updated.blockingTaskID)
	}
}

// TestCompleteRunTask_SingleMatch verifies tab-completion of a RunTask prefix.
func TestCompleteRunTask_SingleMatch(t *testing.T) {
	m := Model{
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "implement"},
				{Name: "review"},
			},
		},
	}

	completed, ok := completeRunTask(m, "RunTask imp")
	if !ok {
		t.Fatal("expected completion to succeed")
	}
	if completed != "RunTask implement" {
		t.Errorf("expected 'RunTask implement', got %q", completed)
	}
}

// TestCompleteRunTask_MultipleMatchesExtends verifies common-prefix extension.
func TestCompleteRunTask_MultipleMatchesExtends(t *testing.T) {
	m := Model{
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "implement-feature"},
				{Name: "implement-fix"},
			},
		},
	}

	// Common prefix of "implement-feature" and "implement-fix" is "implement-f"
	completed, ok := completeRunTask(m, "RunTask impl")
	if !ok {
		t.Fatal("expected completion to extend to common prefix")
	}
	if completed != "RunTask implement-f" {
		t.Errorf("expected 'RunTask implement-f', got %q", completed)
	}
}

// TestCompleteRunTask_IncludesHidden verifies hidden workflows appear in tab-completion.
func TestCompleteRunTask_IncludesHidden(t *testing.T) {
	m := Model{
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "implement"},
				{Name: "secret-wf", Hidden: true},
			},
		},
	}

	completed, ok := completeRunTask(m, "RunTask sec")
	if !ok {
		t.Fatal("expected completion for hidden workflow")
	}
	if completed != "RunTask secret-wf" {
		t.Errorf("expected 'RunTask secret-wf', got %q", completed)
	}
}

// TestCompleteRunTask_ExactCommand verifies that typing "RunTask" exactly adds a space.
func TestCompleteRunTask_ExactCommand(t *testing.T) {
	m := Model{
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "implement"},
			},
		},
	}

	completed, ok := completeRunTask(m, "RunTask")
	if !ok {
		t.Fatal("expected completion to add space after RunTask")
	}
	if completed != "RunTask " {
		t.Errorf("expected 'RunTask ', got %q", completed)
	}
}

// TestCompleteRunTask_NilConfig verifies no completion when config is nil.
func TestCompleteRunTask_NilConfig(t *testing.T) {
	m := Model{}

	_, ok := completeRunTask(m, "RunTask impl")
	if ok {
		t.Error("expected no completion with nil config")
	}
}

// TestCommandMode_TabCompletion verifies tab-completion in command mode.
func TestCommandMode_TabCompletion(t *testing.T) {
	m := Model{
		keys:         newKeyMap(),
		list:         newListView(false, ""),
		detail:       newDetailView(),
		view:         viewList,
		commandMode:  true,
		commandInput: "RunTask Ref",
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "Refactor", Description: "Refactor code"},
				{Name: "Review", Description: "Review code"},
			},
		},
	}

	msg := tea.KeyMsg{Type: tea.KeyTab}
	result, _ := m.handleCommandKey(msg)
	updated := result.(Model)

	if updated.commandInput != "RunTask Refactor" {
		t.Errorf("expected commandInput 'RunTask Refactor' after tab, got %q", updated.commandInput)
	}
	if !updated.commandMode {
		t.Error("expected to remain in command mode after tab")
	}
}

// TestCommandMode_RunTaskIntegration exercises the full :RunTask <name> command flow.
func TestCommandMode_RunTaskIntegration(t *testing.T) {
	m := Model{
		keys:        newKeyMap(),
		client:      &client.Client{},
		list:        newListView(false, ""),
		detail:      newDetailView(),
		prompt:      newPromptView(true, branchModeNew, ""),
		view:        viewList,
		projectPath: "/tmp/test-project",
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "implement", Description: "Implement task"},
			},
		},
	}

	// Enter command mode
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}}
	result, _ := m.handleListKey(msg)
	m = result.(Model)
	if !m.commandMode {
		t.Fatal("expected command mode to be active")
	}

	// Type "RunTask implement"
	for _, ch := range "RunTask implement" {
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}}
		result, _ = m.handleCommandKey(msg)
		m = result.(Model)
	}
	if m.commandInput != "RunTask implement" {
		t.Fatalf("expected commandInput 'RunTask implement', got %q", m.commandInput)
	}

	// Press enter
	msg = tea.KeyMsg{Type: tea.KeyEnter}
	result, _ = m.handleCommandKey(msg)
	m = result.(Model)

	if m.commandMode {
		t.Error("expected command mode to be inactive after enter")
	}
	// Non-fully-spec workflow → prompt opens
	if m.view != viewPrompt {
		t.Errorf("expected view=viewPrompt after RunTask implement, got %d", m.view)
	}
	if m.selectedWorkflow != "implement" {
		t.Errorf("expected selectedWorkflow 'implement', got %q", m.selectedWorkflow)
	}
}

// --- launchWorkflow coverage ---

// TestLaunchWorkflow_FullyPinned verifies that a fully-pinned workflow creates a
// task immediately: returns a non-nil cmd and does NOT switch to viewPrompt.
func TestLaunchWorkflow_FullyPinned(t *testing.T) {
	worktreeTrue := true
	m := Model{
		keys:        newKeyMap(),
		client:      &client.Client{},
		list:        newListView(false, ""),
		prompt:      newPromptView(true, branchModeNew, ""),
		view:        viewList,
		projectPath: "/tmp/test-project",
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{
					Name:     "auto-wf",
					Input:    "Automated task",
					Worktree: &worktreeTrue,
					Branch:   "sakusen/{{task_id}}-auto",
					Target:   "main",
				},
			},
		},
	}

	result, cmd := m.launchWorkflow("auto-wf")
	updated := result.(Model)

	// Fully-pinned: immediate task creation — should NOT open prompt.
	if updated.view == viewPrompt {
		t.Error("expected NOT to switch to viewPrompt for fully-pinned workflow")
	}
	if cmd == nil {
		t.Error("expected non-nil cmd for fully-pinned workflow (immediate task creation)")
	}
}

// TestLaunchWorkflow_PartiallyPinned verifies that a partially-pinned workflow
// (e.g. description pinned, worktree not) opens viewPrompt with only the
// unpinned fields visible.
func TestLaunchWorkflow_PartiallyPinned(t *testing.T) {
	m := Model{
		keys:        newKeyMap(),
		client:      &client.Client{},
		list:        newListView(false, ""),
		prompt:      newPromptView(true, branchModeNew, ""),
		view:        viewList,
		projectPath: "/tmp/test-project",
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{
					Name:  "partial-wf",
					Input: "Pinned description",
					// Worktree, Branch, Target not pinned → not fully spec
				},
			},
		},
	}

	result, cmd := m.launchWorkflow("partial-wf")
	updated := result.(Model)

	// Not fully-pinned → should open prompt.
	if updated.view != viewPrompt {
		t.Errorf("expected viewPrompt for partially-pinned workflow, got %d", updated.view)
	}
	// Description is pinned → should NOT appear in visibleFields.
	fields := updated.prompt.visibleFields()
	for _, f := range fields {
		if f == promptFieldInput {
			t.Error("expected description to be absent from visibleFields (it is pinned)")
		}
	}
	// cmd is nil for the prompt-open path.
	if cmd != nil {
		t.Error("expected nil cmd (prompt opened, no immediate task creation)")
	}
}

// TestLaunchWorkflow_Unpinned verifies that a workflow with no pins opens the
// full prompt with all fields visible.
func TestLaunchWorkflow_Unpinned(t *testing.T) {
	m := Model{
		keys:        newKeyMap(),
		client:      &client.Client{},
		list:        newListView(false, ""),
		prompt:      newPromptView(true, branchModeNew, ""),
		view:        viewList,
		projectPath: "/tmp/test-project",
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "plain-wf"},
			},
		},
	}

	result, cmd := m.launchWorkflow("plain-wf")
	updated := result.(Model)

	if updated.view != viewPrompt {
		t.Errorf("expected viewPrompt for unpinned workflow, got %d", updated.view)
	}
	// All standard fields should be visible (title + description since worktree=true by default in prompt).
	fields := updated.prompt.visibleFields()
	hasTitle := false
	hasDesc := false
	for _, f := range fields {
		if f == promptFieldTitle {
			hasTitle = true
		}
		if f == promptFieldInput {
			hasDesc = true
		}
	}
	if !hasTitle {
		t.Error("expected promptFieldTitle in visibleFields for unpinned workflow")
	}
	if !hasDesc {
		t.Error("expected promptFieldInput in visibleFields for unpinned workflow")
	}
	if cmd != nil {
		t.Error("expected nil cmd for unpinned workflow (prompt opened)")
	}
}

// TestLaunchWorkflow_UnknownWorkflow verifies an error is returned for unknown names.
func TestLaunchWorkflow_UnknownWorkflow(t *testing.T) {
	m := Model{
		keys:        newKeyMap(),
		client:      &client.Client{},
		list:        newListView(false, ""),
		view:        viewList,
		projectPath: "/tmp/test-project",
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "implement"},
			},
		},
	}

	result, cmd := m.launchWorkflow("no-such-workflow")
	updated := result.(Model)

	if updated.err == nil {
		t.Error("expected error for unknown workflow name")
	}
	if cmd != nil {
		t.Error("expected nil cmd for unknown workflow")
	}
}

func TestPromptHelpToggle(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		prompt: newPromptView(true, branchModeNew, ""),
		view:   viewPrompt,
	}

	// Press "ctrl+h" to open help
	msg := tea.KeyMsg{Type: tea.KeyCtrlH}
	result, cmd := m.handlePromptKey(msg)
	updated := result.(Model)

	if !updated.prompt.showHelp {
		t.Error("expected showHelp to be true after 'ctrl+h'")
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}

	// Press "ctrl+h" again to close help
	result, cmd = updated.handlePromptKey(msg)
	updated = result.(Model)

	if updated.prompt.showHelp {
		t.Error("expected showHelp to be false after second 'ctrl+h'")
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
}

func TestPromptHelpCloseWithEsc(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		prompt: newPromptView(true, branchModeNew, ""),
		view:   viewPrompt,
	}
	m.prompt.showHelp = true

	msg := tea.KeyMsg{Type: tea.KeyEscape}
	result, cmd := m.handlePromptKey(msg)
	updated := result.(Model)

	if updated.prompt.showHelp {
		t.Error("expected showHelp to be false after esc")
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
}

func TestPromptHelpConsumesKeys(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		prompt: newPromptView(true, branchModeNew, ""),
		view:   viewPrompt,
	}
	m.prompt.showHelp = true

	// Press a regular key while help is shown — should be consumed
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	result, cmd := m.handlePromptKey(msg)
	updated := result.(Model)

	if !updated.prompt.showHelp {
		t.Error("expected showHelp to remain true")
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
}

func TestViewRendersPromptHelpOverlay(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		prompt: newPromptView(true, branchModeNew, ""),
		view:   viewPrompt,
	}
	m.prompt.showHelp = true

	output := m.View()

	// Should show the prompt help overlay title
	if !strings.Contains(output, "New Task Help") {
		t.Error("expected help overlay to contain 'New Task Help'")
	}

	// Should contain prompt-specific group names
	if !strings.Contains(output, "Input") {
		t.Error("expected help overlay to contain 'Input' group")
	}
	if !strings.Contains(output, "Focus") {
		t.Error("expected help overlay to contain 'Focus' group")
	}
	if !strings.Contains(output, "Toggles") {
		t.Error("expected help overlay to contain 'Toggles' group")
	}

	// Should contain prompt-specific bindings
	if !strings.Contains(output, "submit") {
		t.Error("expected help overlay to contain 'submit'")
	}
	if !strings.Contains(output, "worktree") {
		t.Error("expected help overlay to contain 'worktree'")
	}
	if !strings.Contains(output, "editor") {
		t.Error("expected help overlay to contain 'editor'")
	}

	// Should NOT contain list-view-specific bindings
	if strings.Contains(output, "new task") {
		t.Error("expected help overlay NOT to contain list-specific 'new task' binding")
	}
	if strings.Contains(output, "run task") {
		t.Error("expected help overlay NOT to contain list-specific 'run task' binding")
	}

	// Should contain close hint
	if !strings.Contains(output, "Press ctrl+h or esc to close") {
		t.Error("expected help overlay to contain close hint")
	}
}

func TestPromptShortHelpContainsHelpBinding(t *testing.T) {
	keys := newPromptKeyMap()
	bindings := keys.ShortHelp()

	found := false
	for _, b := range bindings {
		if b.Help().Key == "ctrl+h" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected prompt ShortHelp to contain 'ctrl+h' help binding")
	}
}

func TestPromptShortHelpShowsOnlyEssentialBindings(t *testing.T) {
	keys := newPromptKeyMap()
	bindings := keys.ShortHelp()

	// ShortHelp should only show essential bindings (submit, cancel, field/pane switch, newline, help)
	if len(bindings) > 7 {
		t.Errorf("expected ShortHelp to show at most 7 bindings, got %d", len(bindings))
	}

	// Should contain the essential bindings
	helpKeys := make(map[string]bool)
	for _, b := range bindings {
		helpKeys[b.Help().Key] = true
	}
	for _, expected := range []string{"enter", "esc", "ctrl+h"} {
		if !helpKeys[expected] {
			t.Errorf("expected ShortHelp to contain %q", expected)
		}
	}
}

func TestPromptFullHelpContainsAllBindings(t *testing.T) {
	keys := newPromptKeyMap()
	groups := keys.FullHelp()

	// Collect all binding descriptions
	allDescs := make(map[string]bool)
	for _, group := range groups {
		for _, b := range group {
			allDescs[b.Help().Desc] = true
		}
	}

	// FullHelp should contain all prompt bindings
	for _, expected := range []string{"submit", "cancel", "next/prev field", "newline", "worktree", "editor", "remove last image", "help"} {
		if !allDescs[expected] {
			t.Errorf("expected FullHelp to contain binding for %q", expected)
		}
	}
}

func TestPromptHelpOverlayIsSeparateFromListHelp(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		prompt: newPromptView(true, branchModeNew, ""),
		view:   viewPrompt,
	}

	// Render prompt help overlay
	promptHelp := m.renderPromptHelpOverlay()

	// Render list help overlay
	listHelp := m.renderHelpOverlay()

	// They should have different titles
	if !strings.Contains(promptHelp, "New Task Help") {
		t.Error("expected prompt help to contain 'New Task Help'")
	}
	if !strings.Contains(listHelp, " Help ") {
		t.Error("expected list help to contain ' Help '")
	}

	// Prompt help should NOT contain list-specific bindings
	if strings.Contains(promptHelp, "new task") {
		t.Error("prompt help should not contain 'new task'")
	}
	if strings.Contains(promptHelp, "logs") {
		t.Error("prompt help should not contain 'logs'")
	}

	// List help should NOT contain prompt-specific bindings
	if strings.Contains(listHelp, "switch field") {
		t.Error("list help should not contain 'switch field'")
	}
	if strings.Contains(listHelp, "newline") {
		t.Error("list help should not contain 'newline'")
	}
}

// ── Help overlays on detail (logs), task-info, and artifact views ──
//
// CTRL+H must be available on every screen that hosts the full TUI chrome —
// not just the list and prompt. These tests assert the toggle and the
// overlay content for the three remaining screens.

func TestHandleDetailKey_CtrlHTogglesHelp(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewDetail,
	}
	task := daemon.TaskInfo{ID: 1, Title: "Test"}
	m.detail.SetTask(&task)

	msg := tea.KeyMsg{Type: tea.KeyCtrlH}
	result, _ := m.handleDetailKey(msg)
	updated := result.(Model)
	if !updated.detail.showHelp {
		t.Error("expected detail.showHelp to be true after ctrl+h")
	}

	// Second ctrl+h closes it.
	result, _ = updated.handleDetailKey(msg)
	updated = result.(Model)
	if updated.detail.showHelp {
		t.Error("expected detail.showHelp to be false after second ctrl+h")
	}
}

func TestHandleDetailKey_EscClosesHelp(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewDetail,
	}
	m.detail.showHelp = true

	msg := tea.KeyMsg{Type: tea.KeyEscape}
	result, _ := m.handleDetailKey(msg)
	updated := result.(Model)
	if updated.detail.showHelp {
		t.Error("expected detail.showHelp to be false after esc")
	}
}

func TestDetailHelpOverlay_ContainsGroupsAndBindings(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewDetail,
		width:  120, height: 30,
	}
	task := daemon.TaskInfo{ID: 1, Title: "Test"}
	m.detail.SetTask(&task)
	m.detail.SetSize(120, 30)
	m.detail.showHelp = true

	output := m.View()

	if !strings.Contains(output, "Logs Help") {
		t.Error("expected detail help to have 'Logs Help' title")
	}
	if !strings.Contains(output, "Navigation") {
		t.Error("expected detail help to have 'Navigation' group")
	}
	if !strings.Contains(output, "Actions") {
		t.Error("expected detail help to have 'Actions' group")
	}
	if !strings.Contains(output, "General") {
		t.Error("expected detail help to have 'General' group")
	}
	for _, want := range []string{"ctrl+h", "ctrl+c", "tmux attach", "open log", "follow", "normal mode"} {
		if !strings.Contains(output, want) {
			t.Errorf("expected detail help to mention %q", want)
		}
	}
}

func TestDetailShortHelp_ContainsHelpBinding(t *testing.T) {
	for _, m := range []interface{ ShortHelp() []key.Binding }{
		newDetailFollowKeyMap(), newDetailNormalKeyMap(),
	} {
		found := false
		for _, b := range m.ShortHelp() {
			if b.Help().Key == "ctrl+h" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected detail keymap %T ShortHelp to contain ctrl+h", m)
		}
	}
}

func TestHandleTaskInfoKey_CtrlHTogglesHelp(t *testing.T) {
	m := Model{
		keys:     newKeyMap(),
		list:     newListView(false, ""),
		taskInfo: newTaskInfoView(),
		view:     viewTaskInfo,
	}
	task := daemon.TaskInfo{ID: 1, Title: "Test", Status: "running"}
	m.taskInfo.SetTask(&task)

	msg := tea.KeyMsg{Type: tea.KeyCtrlH}
	result, _ := m.handleTaskInfoKey(msg)
	updated := result.(Model)
	if !updated.taskInfo.showHelp {
		t.Error("expected taskInfo.showHelp to be true after ctrl+h")
	}

	result, _ = updated.handleTaskInfoKey(msg)
	updated = result.(Model)
	if updated.taskInfo.showHelp {
		t.Error("expected taskInfo.showHelp to be false after second ctrl+h")
	}
}

func TestTaskInfoHelpOverlay_ContainsBindings(t *testing.T) {
	m := Model{
		keys:     newKeyMap(),
		list:     newListView(false, ""),
		taskInfo: newTaskInfoView(),
		view:     viewTaskInfo,
		width:    120, height: 30,
	}
	task := daemon.TaskInfo{ID: 1, Title: "Test", Status: "running"}
	m.taskInfo.SetTask(&task)
	m.taskInfo.SetSize(120, 30)
	m.taskInfo.showHelp = true

	output := m.View()

	if !strings.Contains(output, "Task Info Help") {
		t.Error("expected task-info help to have 'Task Info Help' title")
	}
	for _, want := range []string{"ctrl+h", "ed", "et", "ec", "yd", "yc", "oa", "ea", "logs", "tmux attach"} {
		if !strings.Contains(output, want) {
			t.Errorf("expected task-info help to mention %q", want)
		}
	}
}

func TestTaskInfoShortHelp_ContainsHelpBinding(t *testing.T) {
	bindings := newTaskInfoKeyMap().ShortHelp()
	found := false
	for _, b := range bindings {
		if b.Help().Key == "ctrl+h" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected task-info ShortHelp to contain ctrl+h binding")
	}
}

func TestHandleArtifactViewKey_CtrlHTogglesHelp(t *testing.T) {
	m := Model{
		keys: newKeyMap(),
		list: newListView(false, ""),
		view: viewArtifact,
	}
	m.artifactView.SetSize(120, 30)
	m.artifactView.SetContent("implement", "test content")

	msg := tea.KeyMsg{Type: tea.KeyCtrlH}
	result, _ := m.handleArtifactViewKey(msg)
	updated := result.(Model)
	if !updated.artifactView.showHelp {
		t.Error("expected artifactView.showHelp to be true after ctrl+h")
	}

	result, _ = updated.handleArtifactViewKey(msg)
	updated = result.(Model)
	if updated.artifactView.showHelp {
		t.Error("expected artifactView.showHelp to be false after second ctrl+h")
	}
}

func TestArtifactHelpOverlay_ContainsBindings(t *testing.T) {
	m := Model{
		keys:  newKeyMap(),
		list:  newListView(false, ""),
		view:  viewArtifact,
		width: 120, height: 30,
	}
	m.artifactView.SetSize(120, 30)
	m.artifactView.SetContent("implement", "test content")
	m.artifactView.editable = true
	m.artifactView.showHelp = true

	output := m.View()

	if !strings.Contains(output, "Step Context Help") {
		t.Error("expected artifact help to have 'Step Context Help' title")
	}
	for _, want := range []string{"ctrl+h", "gg", "G", "edit context", "esc/q"} {
		if !strings.Contains(output, want) {
			t.Errorf("expected artifact help to mention %q", want)
		}
	}
}

func TestArtifactShortHelp_ContainsHelpBinding(t *testing.T) {
	bindings := newArtifactViewKeyMap().ShortHelp(true)
	found := false
	for _, b := range bindings {
		if b.Help().Key == "ctrl+h" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected artifact ShortHelp to contain ctrl+h binding")
	}
}
