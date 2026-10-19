package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/task"
)

func TestHandleUpdateNotBefore(t *testing.T) {
	s, projID := setupServerWithProject(t)
	tk, err := s.database.CreateTask(projID, "Task", "input", "task", "", "", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)

	c1, s1 := pipeForHandler(t)
	go s.handleUpdateNotBefore(s1, UpdateNotBeforeRequest{TaskID: tk.ID, NotBefore: &at})
	msg := readOneMessage(t, c1)
	mustNotError(t, msg)
	if resp := decodePayload[UpdateNotBeforeResponse](t, msg); resp.Task.NotBefore == nil || !resp.Task.NotBefore.Equal(at) {
		t.Errorf("after set NotBefore = %v, want %v", resp.Task.NotBefore, at)
	}

	c2, s2 := pipeForHandler(t)
	go s.handleUpdateNotBefore(s2, UpdateNotBeforeRequest{TaskID: tk.ID})
	msg = readOneMessage(t, c2)
	mustNotError(t, msg)
	if resp := decodePayload[UpdateNotBeforeResponse](t, msg); resp.Task.NotBefore != nil {
		t.Errorf("after clear NotBefore = %v, want nil", resp.Task.NotBefore)
	}

	for _, st := range []task.Status{task.StatusRunning, task.StatusCompleted} {
		if err := s.database.UpdateTaskStatus(tk.ID, st); err != nil {
			t.Fatal(err)
		}
		c, sc := pipeForHandler(t)
		go s.handleUpdateNotBefore(sc, UpdateNotBeforeRequest{TaskID: tk.ID, NotBefore: &at})
		msg = readOneMessage(t, c)
		if msg.Type != MsgError {
			t.Fatalf("%s: expected error, got %s", st, msg.Type)
		}
		if e := decodePayload[ErrorResponse](t, msg); !strings.Contains(e.Message, "pending task") {
			t.Errorf("%s: error = %q", st, e.Message)
		}
		if got, _ := s.database.GetTask(tk.ID); got.NotBefore != nil {
			t.Errorf("%s: not_before changed on non-pending task", st)
		}
	}
}

func TestCreateTaskFromRequest_PersistsNotBefore(t *testing.T) {
	s, database, proj := newSchedulerTestServer(t)
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, time.FixedZone("", 2*3600))
	tk, _, err := s.createTaskFromRequest(CreateTaskRequest{
		Title: "t", Input: "x", Workflow: "default", ProjectPath: proj.Path, NotBefore: &at,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.NotBefore == nil || !got.NotBefore.Equal(at) {
		t.Fatalf("NotBefore = %v, want %v", got.NotBefore, at)
	}
	if info := s.taskToInfo(got); info.NotBefore == nil || !info.NotBefore.Equal(at) {
		t.Errorf("TaskInfo.NotBefore = %v", info.NotBefore)
	}
}
