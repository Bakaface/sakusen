package tui

import (
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/daemon"
	tea "github.com/charmbracelet/bubbletea"
)

// TestRetryFlow_OpensStepPickerForFailedTask verifies that pressing 'r' on a
// failed task dispatches a command (the step loader) rather than entering a
// y/n confirmation. The picker itself is materialised by taskStepsLoadedMsg
// in a separate model Update tick.
func TestRetryFlow_OpensStepPickerForFailedTask(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		client: &fakeTaskService{},
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 42, Title: "Failed task", Status: "failed"},
	})

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}}
	result, cmd := m.handleListKey(msg)
	updated := result.(Model)

	if updated.confirmAction != "" {
		t.Errorf("expected no confirmation prompt for retry, got %q", updated.confirmAction)
	}
	if cmd == nil {
		t.Error("expected a step-loading command, got nil")
	}
}

// TestRetryFlow_TaskStepsLoaded_SingleStepSkipsPicker verifies that when a
// workflow has only one step, the retry flow fires immediately without
// presenting a picker.
func TestRetryFlow_TaskStepsLoaded_SingleStepSkipsPicker(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		client: &fakeTaskService{},
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}

	msg := taskStepsLoadedMsg{
		taskID: 1,
		steps: []daemon.TaskStepDetail{
			{Name: "only", Status: stepStatusCompleted},
		},
		action: "retry",
	}
	result, cmd := m.Update(msg)
	updated := result.(Model)

	if updated.selector.kind == selectorRetryStep {
		t.Error("expected no picker for single-step workflow")
	}
	if cmd == nil {
		t.Error("expected retry command to fire immediately for single-step workflow")
	}
}

// TestRetryFlow_TaskStepsLoaded_MultiStepShowsPicker verifies that a
// multi-step workflow opens a selectorRetryStep picker pre-cursored on the
// step that was last running/failed.
func TestRetryFlow_TaskStepsLoaded_MultiStepShowsPicker(t *testing.T) {
	completedAt := time.Now()
	m := Model{
		keys:   newKeyMap(),
		client: &fakeTaskService{},
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Failed task", Status: "failed", CurrentStep: "review"},
	})

	msg := taskStepsLoadedMsg{
		taskID: 1,
		steps: []daemon.TaskStepDetail{
			{Name: "plan", Status: stepStatusCompleted, CompletedAt: &completedAt},
			{Name: "implement", Status: stepStatusCompleted, CompletedAt: &completedAt},
			{Name: "review", Status: stepStatusRunning},
			{Name: "test", Status: stepStatusPending},
		},
		action: "retry",
	}
	result, cmd := m.Update(msg)
	updated := result.(Model)

	if updated.selector.kind != selectorRetryStep {
		t.Fatalf("expected selectorRetryStep, got %d", updated.selector.kind)
	}
	if cmd != nil {
		t.Error("expected no command (waiting on user choice), got non-nil")
	}
	if len(updated.selector.items) != 4 {
		t.Errorf("expected 4 step rows, got %d", len(updated.selector.items))
	}
	if updated.selector.cursor != 2 {
		t.Errorf("expected cursor on 'review' (index 2), got %d", updated.selector.cursor)
	}
	// All steps must be selectable for retry (no disabled rows).
	for i, d := range updated.selector.disabled {
		if d {
			t.Errorf("expected row %d to be selectable, was disabled", i)
		}
	}
}

// TestRetryFlow_SelectorChoiceFiresRetry verifies that picking a step in the
// retry-step selector dispatches the retry command with the chosen step name.
func TestRetryFlow_SelectorChoiceFiresRetry(t *testing.T) {
	m := Model{
		keys:   newKeyMap(),
		client: &fakeTaskService{},
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
		selector: selector{
			kind:   selectorRetryStep,
			items:  []string{"✓ plan", "✓ implement", "⟳ review (interrupted)"},
			cursor: 1, // implement
			taskID: 1,
			action: "retry",
		},
		taskSteps: []daemon.TaskStepDetail{
			{Name: "plan", Status: stepStatusCompleted},
			{Name: "implement", Status: stepStatusCompleted},
			{Name: "review", Status: stepStatusRunning},
		},
	}

	enter := tea.KeyMsg{Type: tea.KeyEnter}
	result, cmd := m.handleSelectorKey(enter)
	updated := result.(Model)

	if updated.selector.kind == selectorRetryStep {
		t.Error("expected selector to reset after choice")
	}
	if cmd == nil {
		t.Error("expected retry command after choice, got nil")
	}
}
