package daemon

import (
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
	"github.com/Bakaface/sakusen/internal/workflow"
)

// primeFanoutProject caches a project whose "planner" workflow is
// plan → review → units (fanout from: plan).
func primeFanoutProject(t *testing.T, s *Server, projID int64) {
	t.Helper()
	cfg := &config.Config{Workflows: []config.WorkflowConfig{
		{Name: "unit", Steps: []config.StepConfig{{Name: "build", Prompt: "build"}}},
		{Name: "planner", Steps: []config.StepConfig{
			{Name: "plan", Prompt: "plan"},
			{Name: "review", Prompt: "review"},
			{Name: "units", Fanout: &config.FanoutConfig{From: "plan", Workflow: "unit"}},
		}},
	}}
	repoRoot := "/tmp/sakusen-test"
	s.projectsMu.Lock()
	s.projects[projID] = &projectContext{
		cfg:                     cfg,
		engine:                  workflow.NewEngine(cfg, s.database, s.notifier, repoRoot),
		repoRoot:                repoRoot,
		tracksFingerprint:       tracksFingerprint(repoRoot),
		globalConfigFingerprint: globalConfigFingerprint(),
	}
	s.projectsMu.Unlock()
}

// newPlannerTask creates a running planner task with a task_steps row per
// entry of stepStatus ("running" or "completed").
func newPlannerTask(t *testing.T, s *Server, projID int64, stepStatus map[string]string) *task.Task {
	t.Helper()
	tk, err := s.database.CreateTask(projID, "planner task", "desc", "slug", "planner", "main", task.StatusRunning, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, status := range stepStatus {
		if err := s.database.CreateTaskStep(tk.ID, name); err != nil {
			t.Fatal(err)
		}
		if status == "completed" {
			if err := s.database.CompleteTaskStep(tk.ID, name, nil, 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	return tk
}

func publish(t *testing.T, s *Server, req PublishItemsRequest) *Message {
	t.Helper()
	clientConn, serverConn := pipeForHandler(t)
	go s.handlePublishItems(serverConn, req)
	return readOneMessage(t, clientConn)
}

func storedItems(t *testing.T, s *Server, taskID int64, step string) ([]task.PublishedItem, bool) {
	t.Helper()
	items, ok, err := s.database.(*db.DB).GetPublishedItems(taskID, step)
	if err != nil {
		t.Fatal(err)
	}
	return items, ok
}

func setupPublishItems(t *testing.T, stepStatus map[string]string) (*Server, *task.Task) {
	t.Helper()
	s, projID := setupServerWithProject(t)
	primeFanoutProject(t, s, projID)
	return s, newPlannerTask(t, s, projID, stepStatus)
}

func TestPublishItemsAcceptsRunningSource(t *testing.T) {
	s, tk := setupPublishItems(t, map[string]string{"plan": "running"})

	msg := publish(t, s, PublishItemsRequest{TaskID: tk.ID, StepName: "plan", Items: []task.PublishedItem{
		{Input: "first"},
		{Input: "second", Needs: []string{"item-1"}},
	}})
	if msg.Type != MsgPublishItems {
		t.Fatalf("expected MsgPublishItems, got %s: %s", msg.Type, string(msg.Payload))
	}
	var resp PublishItemsResponse
	if err := msg.DecodePayload(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 2 || resp.Items[0].Slug != "item-1" || resp.Items[1].Slug != "item-2" {
		t.Errorf("response = %+v, want slugs item-1, item-2", resp)
	}

	items, ok := storedItems(t, s, tk.ID, "plan")
	if !ok || len(items) != 2 || items[0].Slug != "item-1" || items[1].Slug != "item-2" || items[1].Needs[0] != "item-1" {
		t.Errorf("stored = %+v (ok=%v), want the normalized items", items, ok)
	}
}

func TestPublishItemsReplacesStoredList(t *testing.T) {
	s, tk := setupPublishItems(t, map[string]string{"plan": "running"})

	for _, items := range [][]task.PublishedItem{
		{{Slug: "a", Input: "a"}, {Slug: "b", Input: "b"}},
		{{Slug: "c", Input: "c"}},
	} {
		if msg := publish(t, s, PublishItemsRequest{TaskID: tk.ID, StepName: "plan", Items: items}); msg.Type != MsgPublishItems {
			t.Fatalf("expected MsgPublishItems, got %s: %s", msg.Type, string(msg.Payload))
		}
	}

	items, _ := storedItems(t, s, tk.ID, "plan")
	if len(items) != 1 || items[0].Slug != "c" {
		t.Errorf("stored = %+v, want only the second list", items)
	}
}

func TestPublishItemsRejections(t *testing.T) {
	ok := []task.PublishedItem{{Slug: "a", Input: "a"}}
	tests := []struct {
		name       string
		stepStatus map[string]string
		step       string
		items      []task.PublishedItem
		wantErr    string
	}{
		{"empty list", map[string]string{"plan": "running"}, "plan", nil, "items list is empty"},
		{"empty input", map[string]string{"plan": "running"}, "plan",
			[]task.PublishedItem{{Slug: "a", Input: "  "}}, "input is empty"},
		{"duplicate slug", map[string]string{"plan": "running"}, "plan",
			[]task.PublishedItem{{Slug: "a", Input: "x"}, {Slug: "a", Input: "y"}}, `duplicate slug "a"`},
		{"bad slug", map[string]string{"plan": "running"}, "plan",
			[]task.PublishedItem{{Slug: "-bad slug", Input: "x"}}, "invalid slug"},
		{"unknown need", map[string]string{"plan": "running"}, "plan",
			[]task.PublishedItem{{Slug: "a", Input: "x", Needs: []string{"ghost"}}}, "ghost"},
		{"self need", map[string]string{"plan": "running"}, "plan",
			[]task.PublishedItem{{Slug: "a", Input: "x", Needs: []string{"a"}}}, "itself"},
		{"cycle", map[string]string{"plan": "running"}, "plan",
			[]task.PublishedItem{
				{Slug: "a", Input: "x", Needs: []string{"b"}},
				{Slug: "b", Input: "y", Needs: []string{"a"}},
			}, "a -> b -> a"},
		{"fanout step itself", map[string]string{"units": "running"}, "units", ok, "not a fanout source"},
		{"non-source step", map[string]string{"review": "running"}, "review", ok, "not a fanout source"},
		{"completed source", map[string]string{"plan": "completed"}, "plan", ok, "status: completed"},
		{"source not started", nil, "plan", ok, "not started"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, tk := setupPublishItems(t, tt.stepStatus)
			msg := publish(t, s, PublishItemsRequest{TaskID: tk.ID, StepName: tt.step, Items: tt.items})
			if msg.Type != MsgError {
				t.Fatalf("expected MsgError, got %s: %s", msg.Type, string(msg.Payload))
			}
			var er ErrorResponse
			if err := msg.DecodePayload(&er); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(er.Message, tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", er.Message, tt.wantErr)
			}
			if tt.step == "units" && !strings.Contains(er.Message, `"planner"`) {
				t.Errorf("error = %q, want it to name the workflow", er.Message)
			}
			if _, stored := storedItems(t, s, tk.ID, tt.step); stored {
				t.Error("a rejected publish must store nothing")
			}
		})
	}
}

func TestPublishItemsUnknownTask(t *testing.T) {
	s, _ := setupPublishItems(t, nil)
	msg := publish(t, s, PublishItemsRequest{TaskID: 9999, StepName: "plan", Items: []task.PublishedItem{{Input: "x"}}})
	if msg.Type != MsgError {
		t.Fatalf("expected MsgError for an unknown task, got %s", msg.Type)
	}
}
