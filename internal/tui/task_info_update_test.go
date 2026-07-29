package tui

import (
	"strings"
	"testing"

	"github.com/Bakaface/sortie/internal/client"
	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/daemon"
	tea "github.com/charmbracelet/bubbletea"
)

func TestHandleTaskInfoKey_QReturnsToList(t *testing.T) {
	m := Model{
		keys:     newKeyMap(),
		list:     newListView(false, ""),
		detail:   newDetailView(),
		taskInfo: newTaskInfoView(),
		view:     viewTaskInfo,
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	result, cmd := m.handleTaskInfoKey(msg)
	updated := result.(Model)

	if updated.view != viewList {
		t.Errorf("expected view to be viewList (%d), got %d", viewList, updated.view)
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
}

func TestHandleTaskInfoKey_EscReturnsToList(t *testing.T) {
	m := Model{
		keys:     newKeyMap(),
		list:     newListView(false, ""),
		detail:   newDetailView(),
		taskInfo: newTaskInfoView(),
		view:     viewTaskInfo,
	}

	msg := tea.KeyMsg{Type: tea.KeyEscape}
	result, cmd := m.handleTaskInfoKey(msg)
	updated := result.(Model)

	if updated.view != viewList {
		t.Errorf("expected view to be viewList (%d), got %d", viewList, updated.view)
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
}

func TestHandleTaskInfoKey_LOpensLogsView(t *testing.T) {
	m := Model{
		keys:     newKeyMap(),
		list:     newListView(false, ""),
		detail:   newDetailView(),
		taskInfo: newTaskInfoView(),
		view:     viewTaskInfo,
	}
	task := daemon.TaskInfo{ID: 42, Title: "Test", Status: "running"}
	m.taskInfo.SetTask(&task)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}}
	result, cmd := m.handleTaskInfoKey(msg)
	updated := result.(Model)

	if updated.view != viewDetail {
		t.Errorf("expected view to be viewDetail (%d), got %d", viewDetail, updated.view)
	}
	if updated.detail.task == nil {
		t.Fatal("expected detail.task to be set")
	}
	if updated.detail.task.ID != 42 {
		t.Errorf("expected detail task ID 42, got %d", updated.detail.task.ID)
	}
	if !updated.detail.IsFollowMode() {
		t.Error("expected follow mode to be true")
	}
	if cmd == nil {
		t.Error("expected loadOutput command, got nil")
	}
}

func TestTaskInfoView_ShowsMetadata(t *testing.T) {
	v := newTaskInfoView()
	v.SetTask(&daemon.TaskInfo{
		ID:          14,
		Title:       "Test task",
		Input:       "Some description",
		Status:      "running",
		Branch:      "sortie/14-test",
		CurrentStep: "implement",
		StepIndex:   0,
		Context:     "some context info",
	})
	v.SetWorkflow(&config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{
			{Name: "implement"},
			{Name: "review", Human: true},
			{Name: "test"},
		},
	})
	v.SetSize(80, 40)

	output := v.View()

	// Should contain metadata
	if !strings.Contains(output, "#14") {
		t.Error("expected output to contain task ID '#14'")
	}
	if !strings.Contains(output, "Status:") {
		t.Error("expected output to contain 'Status:'")
	}
	if !strings.Contains(output, "running") {
		t.Error("expected output to contain status 'running'")
	}
	if !strings.Contains(output, "Branch:") {
		t.Error("expected output to contain 'Branch:'")
	}
	if !strings.Contains(output, "sortie/14-test") {
		t.Error("expected output to contain branch name")
	}
	if !strings.Contains(output, "Some description") {
		t.Error("expected output to contain description")
	}
	if !strings.Contains(output, "some context info") {
		t.Error("expected output to contain context")
	}

	// Should show workflow progress
	if !strings.Contains(output, "implement") {
		t.Error("expected output to contain step name 'implement'")
	}
	if !strings.Contains(output, "review") {
		t.Error("expected output to contain step name 'review'")
	}
	if !strings.Contains(output, "[human]") {
		t.Error("expected output to contain '[human]' indicator")
	}
}

func TestTaskInfoView_NoLogs(t *testing.T) {
	v := newTaskInfoView()
	v.SetTask(&daemon.TaskInfo{
		ID:     14,
		Title:  "Test task",
		Status: "running",
	})
	v.SetSize(80, 24)

	output := v.View()

	// Should NOT contain log-style content or FOLLOW/NORMAL mode
	if strings.Contains(output, "FOLLOW") {
		t.Error("expected task info view to not contain FOLLOW mode indicator")
	}
	if strings.Contains(output, "NORMAL") {
		t.Error("expected task info view to not contain NORMAL mode indicator")
	}
}

func TestHandleTaskInfoKey_YSetsPendingY(t *testing.T) {
	m := Model{
		keys:     newKeyMap(),
		list:     newListView(false, ""),
		detail:   newDetailView(),
		taskInfo: newTaskInfoView(),
		view:     viewTaskInfo,
	}
	task := daemon.TaskInfo{ID: 1, Title: "Test", Input: "desc", Context: "ctx"}
	m.taskInfo.SetTask(&task)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	result, cmd := m.handleTaskInfoKey(msg)
	updated := result.(Model)

	if updated.pendingChord != "y" {
		t.Error("expected pendingY to be true after 'y'")
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
}

func TestHandleTaskInfoKey_YDCopiesDescription(t *testing.T) {
	m := Model{
		keys:         newKeyMap(),
		list:         newListView(false, ""),
		detail:       newDetailView(),
		taskInfo:     newTaskInfoView(),
		view:         viewTaskInfo,
		pendingChord: "y",
	}
	task := daemon.TaskInfo{ID: 1, Title: "Test", Input: "task description text", Context: "ctx"}
	m.taskInfo.SetTask(&task)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}}
	result, cmd := m.handleTaskInfoKey(msg)
	updated := result.(Model)

	if updated.pendingChord != "" {
		t.Error("expected pendingY to be false after 'yd'")
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
	// Clipboard may fail in CI/headless environments — verify either
	// status message (success) or error (clipboard unavailable) is set.
	if updated.err == nil && updated.statusMessage != "Copied input to clipboard" {
		t.Errorf("expected status message 'Copied input to clipboard', got %q", updated.statusMessage)
	}
}

func TestHandleTaskInfoKey_YCCopiesContext(t *testing.T) {
	m := Model{
		keys:         newKeyMap(),
		list:         newListView(false, ""),
		detail:       newDetailView(),
		taskInfo:     newTaskInfoView(),
		view:         viewTaskInfo,
		pendingChord: "y",
	}
	task := daemon.TaskInfo{ID: 1, Title: "Test", Input: "desc", Context: "task context text"}
	m.taskInfo.SetTask(&task)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}
	result, cmd := m.handleTaskInfoKey(msg)
	updated := result.(Model)

	if updated.pendingChord != "" {
		t.Error("expected pendingY to be false after 'yc'")
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
	// Clipboard may fail in CI/headless environments — verify either
	// status message (success) or error (clipboard unavailable) is set.
	if updated.err == nil && updated.statusMessage != "Copied context to clipboard" {
		t.Errorf("expected status message 'Copied context to clipboard', got %q", updated.statusMessage)
	}
}

func TestHandleTaskInfoKey_YResetByOtherKey(t *testing.T) {
	m := Model{
		keys:     newKeyMap(),
		list:     newListView(false, ""),
		detail:   newDetailView(),
		taskInfo: newTaskInfoView(),
		view:     viewTaskInfo,
	}
	task := daemon.TaskInfo{ID: 1, Title: "Test", Input: "desc"}
	m.taskInfo.SetTask(&task)

	// Press "y" then "j" — should reset pendingY and scroll down
	yMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	result, _ := m.handleTaskInfoKey(yMsg)
	m = result.(Model)

	if m.pendingChord != "y" {
		t.Error("expected pendingY to be true after 'y'")
	}

	jMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	result, _ = m.handleTaskInfoKey(jMsg)
	m = result.(Model)

	if m.pendingChord != "" {
		t.Error("expected pendingY to be false after non-yank key")
	}
}

func TestHandleTaskInfoKey_OAReturnsCommand(t *testing.T) {
	m := Model{
		keys:     newKeyMap(),
		list:     newListView(false, ""),
		detail:   newDetailView(),
		taskInfo: newTaskInfoView(),
		view:     viewTaskInfo,
		client:   &client.Client{}, // non-nil client to pass the check
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "default", Steps: []config.StepConfig{{Name: "implement"}}},
			},
		},
	}
	task := daemon.TaskInfo{ID: 1, Title: "Test", Status: "completed", Workflow: "default"}
	m.taskInfo.SetTask(&task)

	// Press "o" then "a"
	oMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}}
	result, _ := m.handleTaskInfoKey(oMsg)
	updated := result.(Model)
	if updated.pendingChord != "o" {
		t.Error("expected pendingO to be true after 'o'")
	}

	aMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	result, cmd := updated.handleTaskInfoKey(aMsg)
	_ = result.(Model)

	// Should return a command (async daemon fetch)
	if cmd == nil {
		t.Error("expected async fetch command, got nil")
	}
}

func TestHandleTaskInfoKey_EAReturnsCommand(t *testing.T) {
	m := Model{
		keys:     newKeyMap(),
		list:     newListView(false, ""),
		detail:   newDetailView(),
		taskInfo: newTaskInfoView(),
		view:     viewTaskInfo,
		client:   &client.Client{}, // non-nil client to pass the check
		cfg: &config.Config{
			Workflows: []config.WorkflowConfig{
				{Name: "default", Steps: []config.StepConfig{{Name: "implement"}}},
			},
		},
	}
	task := daemon.TaskInfo{ID: 1, Title: "Test", Status: "completed", Workflow: "default"}
	m.taskInfo.SetTask(&task)

	// Press "e" then "a" — should return async fetch command
	eMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}}
	result, _ := m.handleTaskInfoKey(eMsg)
	updated := result.(Model)

	aMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	result, cmd := updated.handleTaskInfoKey(aMsg)
	_ = result.(Model)

	// Should return a command (async daemon fetch)
	if cmd == nil {
		t.Error("expected async fetch command, got nil")
	}
}

func TestHandleTaskInfoKey_YDEmptyDescriptionNoOp(t *testing.T) {
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
	result, cmd := m.handleTaskInfoKey(msg)
	updated := result.(Model)

	if updated.pendingChord != "" {
		t.Error("expected pendingY to be false")
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
}

func TestHandleTaskInfoKey_YCEmptyContextNoOp(t *testing.T) {
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
	result, cmd := m.handleTaskInfoKey(msg)
	updated := result.(Model)

	if updated.pendingChord != "" {
		t.Error("expected pendingY to be false")
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
}

func TestTaskInfoView_ShowsPriority(t *testing.T) {
	v := newTaskInfoView()
	v.SetSize(100, 50)
	task := &daemon.TaskInfo{
		ID:       1,
		Title:    "Test task",
		Status:   "pending",
		Priority: "high",
	}
	v.SetTask(task)

	output := v.renderMetadata()
	if !strings.Contains(output, "Priority:") {
		t.Error("expected task info to show priority label")
	}
	if !strings.Contains(output, "high") {
		t.Error("expected task info to show priority value 'high'")
	}
}

func TestTaskInfoView_HelpShowsYankBindings(t *testing.T) {
	v := newTaskInfoView()
	v.SetTask(&daemon.TaskInfo{ID: 1, Title: "Test", Status: "running"})
	v.SetSize(120, 24)

	output := v.View()

	if !strings.Contains(output, "yd") {
		t.Error("expected task info help to contain 'yd' binding")
	}
	if !strings.Contains(output, "yc") {
		t.Error("expected task info help to contain 'yc' binding")
	}
}

func TestHandleTaskInfoKey_ETOpensEditTitle(t *testing.T) {
	m := Model{
		keys:     newKeyMap(),
		list:     newListView(false, ""),
		detail:   newDetailView(),
		taskInfo: newTaskInfoView(),
		view:     viewTaskInfo,
		client:   &client.Client{},
	}
	task := daemon.TaskInfo{ID: 1, Title: "Original title", Status: "pending"}
	m.taskInfo.SetTask(&task)

	// Press "e" to enter pending state
	eMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}}
	result, _ := m.handleTaskInfoKey(eMsg)
	updated := result.(Model)
	if updated.pendingChord != "e" {
		t.Error("expected pendingE to be true after 'e'")
	}

	// Press "t" to trigger edit title
	tMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}}
	result, cmd := updated.handleTaskInfoKey(tMsg)
	updated = result.(Model)

	if updated.pendingChord != "" {
		t.Error("expected pendingE to be false after 'et' sequence")
	}
	if cmd == nil {
		t.Error("expected a command to open editor for title, got nil")
	}
}

func TestHandleTaskInfoKey_CtrlCTriggersStopConfirmation(t *testing.T) {
	m := Model{
		keys:     newKeyMap(),
		client:   &client.Client{},
		list:     newListView(false, ""),
		detail:   newDetailView(),
		taskInfo: newTaskInfoView(),
		view:     viewTaskInfo,
	}
	task := daemon.TaskInfo{ID: 77, Title: "Running task", Status: "running"}
	m.taskInfo.SetTask(&task)

	msg := tea.KeyMsg{Type: tea.KeyCtrlC}
	result, cmd := m.handleTaskInfoKey(msg)
	updated := result.(Model)

	if updated.confirmAction != "stop" {
		t.Errorf("expected confirmAction to be 'stop', got %q", updated.confirmAction)
	}
	if updated.confirmTaskID != 77 {
		t.Errorf("expected confirmTaskID to be 77, got %d", updated.confirmTaskID)
	}
	if cmd != nil {
		t.Error("expected no command (confirmation pending), got non-nil")
	}
}

func TestHandleTaskInfoKey_CtrlCNoOpWithNoTask(t *testing.T) {
	m := Model{
		keys:     newKeyMap(),
		client:   &client.Client{},
		list:     newListView(false, ""),
		detail:   newDetailView(),
		taskInfo: newTaskInfoView(),
		view:     viewTaskInfo,
	}

	msg := tea.KeyMsg{Type: tea.KeyCtrlC}
	result, _ := m.handleTaskInfoKey(msg)
	updated := result.(Model)

	if updated.confirmAction != "" {
		t.Errorf("expected no confirmAction without task, got %q", updated.confirmAction)
	}
}

func TestHandleTaskInfoKey_ConfirmationBlocksOtherKeys(t *testing.T) {
	m := Model{
		keys:          newKeyMap(),
		client:        &client.Client{},
		list:          newListView(false, ""),
		detail:        newDetailView(),
		taskInfo:      newTaskInfoView(),
		view:          viewTaskInfo,
		confirmAction: "stop",
		confirmTaskID: 77,
	}
	task := daemon.TaskInfo{ID: 77, Title: "Running task", Status: "running"}
	m.taskInfo.SetTask(&task)

	// Pressing 'q' while confirmation is active should not navigate away
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	result, _ := m.handleTaskInfoKey(msg)
	updated := result.(Model)

	if updated.view != viewTaskInfo {
		t.Error("expected to stay in task info view while confirmation is active")
	}
	if updated.confirmAction != "stop" {
		t.Errorf("expected confirmAction to remain 'stop', got %q", updated.confirmAction)
	}
}

func TestHandleTaskInfoKey_EscDismissesArtifactSelection(t *testing.T) {
	m := Model{
		keys:     newKeyMap(),
		list:     newListView(false, ""),
		detail:   newDetailView(),
		taskInfo: newTaskInfoView(),
		view:     viewTaskInfo,
		selector: selector{
			kind:   selectorArtifact,
			cursor: 0,
			items:  []string{"implement", "review"},
			action: "view",
		},
	}
	task := daemon.TaskInfo{ID: 1, Title: "Test", Status: "completed"}
	m.taskInfo.SetTask(&task)

	msg := tea.KeyMsg{Type: tea.KeyEscape}
	result, _ := m.handleTaskInfoKey(msg)
	updated := result.(Model)

	if updated.selector.kind == selectorArtifact {
		t.Error("expected selector kind not to be selectorArtifact after esc")
	}
	// Should stay in task info view, not return to list
	if updated.view != viewTaskInfo {
		t.Errorf("expected view to remain viewTaskInfo (%d), got %d", viewTaskInfo, updated.view)
	}
}
