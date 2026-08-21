package tui

import (
	"testing"

	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/Bakaface/sakusen/internal/task"
)

// TestExecutedCmd_RetryTask_UpdatesList proves the TaskService seam: the
// fake stands in for the daemon connection, retryTask's returned tea.Cmd is
// actually executed (not just checked for non-nil), and the resulting
// taskUpdatedMsg is reduced through Update so the list reflects the new
// status.
func TestExecutedCmd_RetryTask_UpdatesList(t *testing.T) {
	fake := &fakeTaskService{
		retryTask: func(id int64, stepName string) (*daemon.TaskInfo, error) {
			if id != 7 {
				t.Fatalf("expected retry for task 7, got %d", id)
			}
			return &daemon.TaskInfo{ID: 7, Title: "Retried task", Status: string(task.StatusRunning)}, nil
		},
	}
	m := Model{
		client: fake,
		list:   newListView(false, ""),
		detail: newDetailView(),
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 7, Title: "Failed task", Status: string(task.StatusFailed)},
	})

	cmd := m.retryTask(7, "implement")
	if cmd == nil {
		t.Fatal("expected a non-nil cmd")
	}

	msg := cmd()
	updated, cmd2 := m.Update(msg)
	if cmd2 != nil {
		t.Errorf("expected no follow-up cmd from taskUpdatedMsg, got non-nil")
	}
	result := updated.(Model)

	got := result.list.allTasks[0]
	if got.ID != 7 || got.Status != string(task.StatusRunning) || got.Title != "Retried task" {
		t.Errorf("expected list to reflect the retried task, got %+v", got)
	}
}

// TestExecutedCmd_StopTask_UpdatesList mirrors the retry case for stopTask,
// covering a second of the eight collapsed runTaskAction verbs.
func TestExecutedCmd_StopTask_UpdatesList(t *testing.T) {
	fake := &fakeTaskService{
		stopTask: func(id int64) (*daemon.TaskInfo, error) {
			return &daemon.TaskInfo{ID: id, Title: "Stopped task", Status: string(task.StatusFailed)}, nil
		},
	}
	m := Model{
		client: fake,
		list:   newListView(false, ""),
		detail: newDetailView(),
	}
	m.list.SetTasks([]daemon.TaskInfo{
		{ID: 3, Title: "Running task", Status: string(task.StatusRunning)},
	})

	cmd := m.stopTask(3)
	if cmd == nil {
		t.Fatal("expected a non-nil cmd")
	}

	msg := cmd()
	updated, _ := m.Update(msg)
	result := updated.(Model)

	got := result.list.allTasks[0]
	if got.Status != string(task.StatusFailed) {
		t.Errorf("expected list task status to be updated to failed, got %q", got.Status)
	}
}

// TestExecutedCmd_RefreshTasks_PopulatesList proves refreshTasks' cmd, when
// executed against the fake, produces a tasksLoadedMsg that lands in the
// list on reduction.
func TestExecutedCmd_RefreshTasks_PopulatesList(t *testing.T) {
	fake := &fakeTaskService{
		listTasksFiltered: func(projectID int64) ([]daemon.TaskInfo, error) {
			return []daemon.TaskInfo{
				{ID: 1, Title: "One", Status: string(task.StatusPending)},
				{ID: 2, Title: "Two", Status: string(task.StatusRunning)},
			}, nil
		},
	}
	m := Model{
		client: fake,
		list:   newListView(false, ""),
		detail: newDetailView(),
	}

	cmd := m.refreshTasks()
	if cmd == nil {
		t.Fatal("expected a non-nil cmd")
	}

	msg := cmd()
	updated, _ := m.Update(msg)
	result := updated.(Model)

	if len(result.list.allTasks) != 2 {
		t.Fatalf("expected 2 tasks loaded into the list, got %d", len(result.list.allTasks))
	}
}

// TestExecutedCmd_LoadOutput_SecondFetchAppends drives the logStream seam
// end to end: two loadOutput round trips through the fake, reduced through
// Update, must produce a full replace on the first fetch (offset 0) and an
// incremental append using the offset logStream advanced to (the server's
// reported total line count) on the second — never re-requesting from 0 and
// never re-replacing already-displayed content.
func TestExecutedCmd_LoadOutput_SecondFetchAppends(t *testing.T) {
	var gotOffsets []int
	fake := &fakeTaskService{
		getLogs: func(id int64, tail, offset int) ([]string, int, error) {
			gotOffsets = append(gotOffsets, offset)
			if offset == 0 {
				return []string{"line1", "line2"}, 2, nil
			}
			return []string{"line3"}, 3, nil
		},
	}
	m := Model{
		client: fake,
		list:   newListView(false, ""),
		detail: newDetailView(),
	}
	taskRef := &daemon.TaskInfo{ID: 9, Title: "Streaming task", Status: string(task.StatusRunning)}
	m.detail.SetTask(taskRef)
	m.logStream.reset(taskRef.ID)

	// First fetch: offset 0, full load.
	taskID, offset := m.logStream.nextRequest()
	if taskID != 9 || offset != 0 {
		t.Fatalf("expected first request (9, 0), got (%d, %d)", taskID, offset)
	}
	cmd1 := m.loadOutput(taskID, offset)
	msg1 := cmd1()
	updated, _ := m.Update(msg1)
	m = updated.(Model)

	if got := m.detail.output; len(got) != 2 || got[0] != "line1" || got[1] != "line2" {
		t.Fatalf("expected full load of [line1 line2], got %v", got)
	}

	// Second fetch: offset must have advanced to the server-reported total
	// (2), not stayed at 0 and not been derived by the caller.
	taskID, offset = m.logStream.nextRequest()
	if taskID != 9 || offset != 2 {
		t.Fatalf("expected second request (9, 2), got (%d, %d)", taskID, offset)
	}
	cmd2 := m.loadOutput(taskID, offset)
	msg2 := cmd2()
	updated, _ = m.Update(msg2)
	m = updated.(Model)

	if got := m.detail.output; len(got) != 3 || got[2] != "line3" {
		t.Fatalf("expected append to produce [line1 line2 line3], got %v", got)
	}
	if len(gotOffsets) != 2 || gotOffsets[0] != 0 || gotOffsets[1] != 2 {
		t.Fatalf("expected server to see offsets [0 2], got %v", gotOffsets)
	}
}
