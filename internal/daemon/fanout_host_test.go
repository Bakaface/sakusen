package daemon

import (
	"testing"

	"github.com/Bakaface/sakusen/internal/task"
)

func TestFanoutHostRetryChildResetsFailedTask(t *testing.T) {
	s, projID := setupServerWithProject(t)
	tk, err := s.database.CreateTask(projID, "child", "input", "child", "default", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.database.CreateTaskStep(tk.ID, "build"); err != nil {
		t.Fatal(err)
	}
	if err := s.database.UpdateTaskStatusError(tk.ID, task.StatusFailed, "step \"build\" exited with code 1"); err != nil {
		t.Fatal(err)
	}

	if err := (fanoutHost{s: s}).RetryChild(tk.ID); err != nil {
		t.Fatalf("RetryChild: %v", err)
	}

	got, err := s.database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != task.StatusPending || got.ErrorMessage != "" {
		t.Errorf("task = %s %q, want pending with no error", got.Status, got.ErrorMessage)
	}
	rows, err := s.database.GetTaskStepRows(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("task_steps = %v, want cleared", rows)
	}
}
