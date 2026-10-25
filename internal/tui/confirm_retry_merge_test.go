package tui

import (
	"testing"

	"github.com/Bakaface/sakusen/internal/daemon"
	tea "github.com/charmbracelet/bubbletea"
)

func retryMergeTestModel(status string, svc *fakeTaskService) Model {
	m := Model{
		keys:   newKeyMap(),
		client: svc,
		list:   newListView(false, ""),
		detail: newDetailView(),
		view:   viewList,
	}
	m.list.SetTasks([]daemon.TaskInfo{{ID: 42, Title: "Task", Status: status}})
	return m
}

var keyRetryMerge = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'M'}}

// 'M' on a merge-failed task asks for confirmation, and 'y' re-runs only the
// merge through RetryMerge.
func TestRetryMerge_ConfirmThenRetriesMerge(t *testing.T) {
	var gotID int64
	svc := &fakeTaskService{retryMerge: func(id int64) (*daemon.TaskInfo, error) {
		gotID = id
		return &daemon.TaskInfo{ID: id, Status: "finalizing"}, nil
	}}
	m := retryMergeTestModel("merge-failed", svc)

	result, cmd := m.handleListKey(keyRetryMerge)
	updated := result.(Model)
	if cmd != nil {
		t.Fatal("expected no command before confirmation")
	}
	if updated.confirmAction != confirmRetryMerge || updated.confirmTaskID != 42 {
		t.Fatalf("confirm = %q for #%d, want %q for #42", updated.confirmAction, updated.confirmTaskID, confirmRetryMerge)
	}

	result, cmd = updated.handleConfirmKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if result.(Model).confirmAction != "" {
		t.Error("confirmation should be cleared after 'y'")
	}
	if cmd == nil {
		t.Fatal("expected a retry-merge command after confirming")
	}
	if msg, ok := cmd().(taskUpdatedMsg); !ok || msg.Status != "finalizing" {
		t.Errorf("expected taskUpdatedMsg with the daemon's task, got %#v", msg)
	}
	if gotID != 42 {
		t.Errorf("RetryMerge called for #%d, want #42", gotID)
	}
}

// Retry merge is offered only for merge-failed tasks.
func TestRetryMerge_IgnoredForOtherStatuses(t *testing.T) {
	for _, status := range []string{"failed", "completed", "running", "tmux", "pending"} {
		t.Run(status, func(t *testing.T) {
			m := retryMergeTestModel(status, &fakeTaskService{})
			result, cmd := m.handleListKey(keyRetryMerge)
			if result.(Model).confirmAction != "" || cmd != nil {
				t.Errorf("'M' on a %s task should do nothing", status)
			}
		})
	}
}
