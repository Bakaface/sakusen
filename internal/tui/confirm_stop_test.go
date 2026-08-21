package tui

import (
	"testing"

	"github.com/Bakaface/sakusen/internal/daemon"
	tea "github.com/charmbracelet/bubbletea"
)

func TestHandleConfirmKey_YConfirmsStop(t *testing.T) {
	m := Model{
		keys:          newKeyMap(),
		client:        &fakeTaskService{},
		list:          newListView(false, ""),
		detail:        newDetailView(),
		confirmAction: "stop",
		confirmTaskID: 42,
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	result, cmd := m.handleConfirmKey(msg)
	updated := result.(Model)

	if updated.confirmAction != "" {
		t.Errorf("expected confirmAction to be cleared, got %q", updated.confirmAction)
	}
	if updated.confirmTaskID != 0 {
		t.Errorf("expected confirmTaskID to be cleared, got %d", updated.confirmTaskID)
	}
	if cmd == nil {
		t.Error("expected command from confirmed stop, got nil")
	}
}

func TestHandleConfirmKey_NCancelsStop(t *testing.T) {
	m := Model{
		keys:          newKeyMap(),
		client:        &fakeTaskService{},
		list:          newListView(false, ""),
		detail:        newDetailView(),
		confirmAction: "stop",
		confirmTaskID: 42,
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}
	result, cmd := m.handleConfirmKey(msg)
	updated := result.(Model)

	if updated.confirmAction != "" {
		t.Errorf("expected confirmAction to be cleared, got %q", updated.confirmAction)
	}
	if updated.confirmTaskID != 0 {
		t.Errorf("expected confirmTaskID to be cleared, got %d", updated.confirmTaskID)
	}
	if cmd != nil {
		t.Error("expected no command from cancelled stop, got non-nil")
	}
}

func TestHandleConfirmKey_EscCancelsStop(t *testing.T) {
	m := Model{
		keys:          newKeyMap(),
		client:        &fakeTaskService{},
		list:          newListView(false, ""),
		detail:        newDetailView(),
		confirmAction: "stop",
		confirmTaskID: 42,
	}

	msg := tea.KeyMsg{Type: tea.KeyEsc}
	result, cmd := m.handleConfirmKey(msg)
	updated := result.(Model)

	if updated.confirmAction != "" {
		t.Errorf("expected confirmAction to be cleared, got %q", updated.confirmAction)
	}
	if cmd != nil {
		t.Error("expected no command from esc cancel, got non-nil")
	}
}

func TestHandleConfirmKey_OtherKeyIgnored(t *testing.T) {
	m := Model{
		keys:          newKeyMap(),
		client:        &fakeTaskService{},
		list:          newListView(false, ""),
		detail:        newDetailView(),
		confirmAction: "stop",
		confirmTaskID: 42,
	}

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}
	result, cmd := m.handleConfirmKey(msg)
	updated := result.(Model)

	if updated.confirmAction != "stop" {
		t.Errorf("expected confirmAction to remain 'stop', got %q", updated.confirmAction)
	}
	if updated.confirmTaskID != 42 {
		t.Errorf("expected confirmTaskID to remain 42, got %d", updated.confirmTaskID)
	}
	if cmd != nil {
		t.Error("expected no command from ignored key, got non-nil")
	}
}

func TestHandleListKey_STriggersStopConfirmation(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		client: &fakeTaskService{},
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 42, Title: "Running task", Status: "running"},
	})

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}}
	result, cmd := m.handleListKey(msg)
	updated := result.(Model)

	if updated.confirmAction != "stop" {
		t.Errorf("expected confirmAction to be 'stop', got %q", updated.confirmAction)
	}
	if updated.confirmTaskID != 42 {
		t.Errorf("expected confirmTaskID to be 42, got %d", updated.confirmTaskID)
	}
	if cmd != nil {
		t.Error("expected no command (confirmation pending), got non-nil")
	}
}

func TestHandleListKey_SNoOpWithNoClient(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		client: nil,
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 42, Title: "Running task", Status: "running"},
	})

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}}
	result, _ := m.handleListKey(msg)
	updated := result.(Model)

	if updated.confirmAction != "" {
		t.Errorf("expected no confirmAction without client, got %q", updated.confirmAction)
	}
}
