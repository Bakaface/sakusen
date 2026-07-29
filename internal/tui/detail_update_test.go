package tui

import (
	"testing"

	"github.com/Bakaface/sortie/internal/client"
	"github.com/Bakaface/sortie/internal/daemon"
	tea "github.com/charmbracelet/bubbletea"
	"strings"
)

func TestHandleDetailKey_QReturnsToList(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewDetail,
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	result, cmd := m.handleDetailKey(msg)
	updated := result.(Model)

	if updated.view != viewList {
		t.Errorf("expected view to be viewList (%d), got %d", viewList, updated.view)
	}
	if updated.quitting {
		t.Error("expected quitting to be false")
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
}

func TestHandleDetailKey_EscReturnsToList(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewDetail,
	}
	// Default follow mode is true; first Esc exits follow mode
	m.detail.SetFollowMode(false)

	msg := tea.KeyMsg{Type: tea.KeyEscape}
	result, cmd := m.handleDetailKey(msg)
	updated := result.(Model)

	if updated.view != viewList {
		t.Errorf("expected view to be viewList (%d), got %d", viewList, updated.view)
	}
	if cmd != nil {
		t.Error("expected no command, got non-nil")
	}
}

func TestHandleDetailKey_EscInFollowModeExitsFollow(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewDetail,
	}
	m.detail.SetFollowMode(true)

	msg := tea.KeyMsg{Type: tea.KeyEscape}
	result, _ := m.handleDetailKey(msg)
	updated := result.(Model)

	if updated.view != viewDetail {
		t.Errorf("expected view to remain viewDetail (%d), got %d", viewDetail, updated.view)
	}
	if updated.detail.IsFollowMode() {
		t.Error("expected follow mode to be false after esc")
	}
}

func TestHandleDetailKey_QInFollowModeReturnsToList(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewDetail,
	}
	m.detail.SetFollowMode(true)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	result, _ := m.handleDetailKey(msg)
	updated := result.(Model)

	if updated.view != viewList {
		t.Errorf("expected view to be viewList (%d), got %d", viewList, updated.view)
	}
}

func TestDetailView_ShowsOnlyLogs(t *testing.T) {
	d := newDetailView()
	d.SetTask(&daemon.TaskInfo{
		ID:           14,
		Title:        "Test task",
		Input:        "Some description",
		Status:       "running",
		Branch:       "sortie/14-test",
		CurrentStep:  "implement",
		StepIndex:    1,
		Context:      "some context info",
		ErrorMessage: "",
	})
	d.SetSize(80, 24)
	d.SetOutput([]string{"log line 1", "log line 2"})

	output := d.View()

	// Should contain the compact title with task ID
	if !strings.Contains(output, "#14") {
		t.Error("expected output to contain task ID '#14'")
	}

	// Should contain log content
	if !strings.Contains(output, "log line 1") {
		t.Error("expected output to contain log lines")
	}

	// Should NOT contain verbose metadata sections
	if strings.Contains(output, "Status:") {
		t.Error("expected output to not contain 'Status:' metadata line")
	}
	if strings.Contains(output, "Branch:") {
		t.Error("expected output to not contain 'Branch:' metadata line")
	}
	if strings.Contains(output, "Step 1:") {
		t.Error("expected output to not contain workflow step line")
	}
	if strings.Contains(output, "Context:") {
		t.Error("expected output to not contain 'Context:' section")
	}
	if strings.Contains(output, "Some description") {
		t.Error("expected output to not contain description")
	}
}

func TestHandleDetailKey_TReturnsCommandWhenTaskSet(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewDetail,
	}
	task := daemon.TaskInfo{ID: 42, Title: "Test", Status: "awaiting-approval"}
	m.detail.SetTask(&task)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}}
	_, cmd := m.handleDetailKey(msg)

	if cmd == nil {
		t.Error("expected command from 't' key in detail view with task, got nil")
	}
}

func TestHandleDetailKey_TNoOpWithNoTask(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewDetail,
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}}
	_, cmd := m.handleDetailKey(msg)

	if cmd != nil {
		t.Error("expected no command from 't' key in detail view without task, got non-nil")
	}
}

func TestHandleDetailKey_EOpensLogEditor(t *testing.T) {
	task := &daemon.TaskInfo{
		ID:          1,
		ProjectPath: "/some/project",
		CurrentStep: "implement",
	}
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewDetail,
	}
	m.detail.SetTask(task)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}}
	_, cmd := m.handleDetailKey(msg)

	// The "e" key should produce a command (tea.ExecProcess or error)
	if cmd == nil {
		t.Error("expected a command from 'e' key, got nil")
	}
}

func TestHandleDetailKey_EWithNoTask(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewDetail,
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}}
	_, cmd := m.handleDetailKey(msg)

	if cmd != nil {
		t.Error("expected nil command when no task is selected")
	}
}

func TestHandleDetailKey_CtrlCTriggersStopConfirmation(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		client: &client.Client{},
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewDetail,
	}
	task := daemon.TaskInfo{ID: 55, Title: "Running task", Status: "running"}
	m.detail.SetTask(&task)

	msg := tea.KeyMsg{Type: tea.KeyCtrlC}
	result, cmd := m.handleDetailKey(msg)
	updated := result.(Model)

	if updated.confirmAction != "stop" {
		t.Errorf("expected confirmAction to be 'stop', got %q", updated.confirmAction)
	}
	if updated.confirmTaskID != 55 {
		t.Errorf("expected confirmTaskID to be 55, got %d", updated.confirmTaskID)
	}
	if cmd != nil {
		t.Error("expected no command (confirmation pending), got non-nil")
	}
}

func TestHandleDetailKey_CtrlCNoOpWithNoTask(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		client: &client.Client{},
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewDetail,
	}

	msg := tea.KeyMsg{Type: tea.KeyCtrlC}
	result, _ := m.handleDetailKey(msg)
	updated := result.(Model)

	if updated.confirmAction != "" {
		t.Errorf("expected no confirmAction without task, got %q", updated.confirmAction)
	}
}

func TestHandleDetailKey_CtrlCNoOpWithNoClient(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		client: nil,
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewDetail,
	}
	task := daemon.TaskInfo{ID: 55, Title: "Running task", Status: "running"}
	m.detail.SetTask(&task)

	msg := tea.KeyMsg{Type: tea.KeyCtrlC}
	result, _ := m.handleDetailKey(msg)
	updated := result.(Model)

	if updated.confirmAction != "" {
		t.Errorf("expected no confirmAction without client, got %q", updated.confirmAction)
	}
}

func TestHandleDetailKey_ConfirmationBlocksOtherKeys(t *testing.T) {
	m := Model{
		keys:          newKeyMap(),
		client:        &client.Client{},
		list:          newListView(false, ""),
		detail:        newDetailView(),
		view:          viewDetail,
		confirmAction: "stop",
		confirmTaskID: 42,
	}
	task := daemon.TaskInfo{ID: 42, Title: "Running task", Status: "running"}
	m.detail.SetTask(&task)

	// Pressing 'q' while confirmation is active should not navigate away
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	result, _ := m.handleDetailKey(msg)
	updated := result.(Model)

	if updated.view != viewDetail {
		t.Error("expected to stay in detail view while confirmation is active")
	}
	if updated.confirmAction != "stop" {
		t.Errorf("expected confirmAction to remain 'stop', got %q", updated.confirmAction)
	}
}
