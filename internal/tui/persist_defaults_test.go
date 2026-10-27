package tui

import (
	"testing"

	"github.com/Bakaface/sakusen/internal/daemon"
)

// Only the New Task form saves the project's defaults; the branch-checkout
// action must not.
func TestCreateRequests_PersistDefaults(t *testing.T) {
	var captured daemon.CreateTaskRequest
	fake := &fakeTaskService{
		createTask: func(req daemon.CreateTaskRequest) (*daemon.TaskInfo, error) {
			captured = req
			return &daemon.TaskInfo{ID: 1}, nil
		},
	}
	m := Model{
		keys:        newKeyMap(),
		client:      fake,
		list:        newListView(false, ""),
		detail:      newDetailView(),
		view:        viewPrompt,
		projectPath: "/tmp/proj",
		prompt:      newPromptView(true, branchModeNew, "main", ""),
	}
	m.prompt.SetSize(80, 30)
	m.prompt.textarea.SetValue("do the thing")

	_, cmd := m.handlePromptSubmit()
	if cmd == nil {
		t.Fatal("expected a create command")
	}
	cmd()
	if !captured.PersistDefaults {
		t.Error("New Task form submit: PersistDefaults = false, want true")
	}

	captured = daemon.CreateTaskRequest{}
	m.createBranchTask("feature/x")()
	if captured.CheckoutBranch != "feature/x" {
		t.Fatalf("branch task not created: CheckoutBranch = %q", captured.CheckoutBranch)
	}
	if captured.PersistDefaults {
		t.Error("branch checkout: PersistDefaults = true, want false")
	}
}
