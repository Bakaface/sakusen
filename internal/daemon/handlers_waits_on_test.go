package daemon

import (
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/task"
)

// projBPath is the "other project" in these tests (setupServerWithProject
// creates the first project at /tmp/sakusen-test).
const projBPath = "/tmp/sakusen-test-other"

// mustOtherProject resolves (creating if needed) the second project row used
// as the cross-project destination.
func mustOtherProject(t *testing.T, s *Server) int64 {
	t.Helper()
	proj, err := s.database.GetOrCreateProject(projBPath)
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}
	return proj.ID
}

// waitTaskLeavesInit drains the background refineTaskTitle goroutine kicked off
// by kickOffPostCreate. Without this the goroutine can still be writing to the
// DB when t.Cleanup closes it.
func waitTaskLeavesInit(t *testing.T, s *Server, id int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := s.database.GetTask(id)
		if err == nil && tk != nil && tk.Status != task.StatusInit {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task #%d never left init status", id)
}

// newParent creates a running parent task in project A, optionally on a track.
func newParent(t *testing.T, s *Server, projA int64, trackID *int64) *task.Task {
	t.Helper()
	parent, err := s.database.CreateTaskWithPriority(
		projA, "parent", "parent work", "parent", "", "", "branch-parent", "", "",
		task.StatusRunning, task.PriorityMedium, false, nil, trackID,
	)
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	return parent
}

func waitsOnIDs(t *testing.T, s *Server, parentID int64) []int64 {
	t.Helper()
	ids, err := s.database.GetTaskWaitsOn(parentID)
	if err != nil {
		t.Fatalf("GetTaskWaitsOn: %v", err)
	}
	return ids
}

func TestHandleCreateTasksAndWait_CrossProject(t *testing.T) {
	t.Run("cross-project child is created and the wait edge recorded", func(t *testing.T) {
		s, projA := setupServerWithProject(t)
		projB := mustOtherProject(t, s)
		parent := newParent(t, s, projA, nil)

		clientConn, serverConn := pipeForHandler(t)
		go s.handleCreateTasksAndWait(serverConn, CreateTasksAndWaitRequest{
			ParentTaskID: parent.ID,
			Tasks: []CreateTaskRequest{
				{Title: "cross child", Input: "do it elsewhere", ProjectPath: projBPath},
			},
		})
		msg := readOneMessage(t, clientConn)
		mustNotError(t, msg)
		resp := decodePayload[CreateTasksAndWaitResponse](t, msg)

		if len(resp.Children) != 1 {
			t.Fatalf("got %d children, want 1", len(resp.Children))
		}
		if resp.Children[0].ProjectPath != projBPath {
			t.Errorf("child ProjectPath=%q want %q", resp.Children[0].ProjectPath, projBPath)
		}
		childID := resp.Children[0].ID

		child, err := s.database.GetTask(childID)
		if err != nil {
			t.Fatalf("get child: %v", err)
		}
		if child.ProjectID == parent.ProjectID {
			t.Fatalf("child landed in the parent's project (%d) — expected project B (%d)", child.ProjectID, projB)
		}

		ids := waitsOnIDs(t, s, parent.ID)
		if len(ids) != 1 || ids[0] != childID {
			t.Errorf("waits-on edges = %v, want [%d]", ids, childID)
		}
		waitTaskLeavesInit(t, s, childID)
	})

	t.Run("cross-project child does NOT inherit the parent's track", func(t *testing.T) {
		s, projA := setupServerWithProject(t)
		mustOtherProject(t, s)

		tr, err := s.database.CreateTrack(&projA, "Payments", "payments", "", "", "", nil)
		if err != nil {
			t.Fatalf("create track: %v", err)
		}
		parent := newParent(t, s, projA, &tr.ID)

		clientConn, serverConn := pipeForHandler(t)
		go s.handleCreateTasksAndWait(serverConn, CreateTasksAndWaitRequest{
			ParentTaskID: parent.ID,
			Tasks: []CreateTaskRequest{
				{Title: "far child", Input: "elsewhere", ProjectPath: projBPath},
				{Title: "near child", Input: "same project"},
			},
		})
		msg := readOneMessage(t, clientConn)
		// Inheriting the parent's track here would fail the create outright:
		// resolveTrackRef rejects a numeric track ID owned by another project.
		mustNotError(t, msg)
		resp := decodePayload[CreateTasksAndWaitResponse](t, msg)
		if len(resp.Children) != 2 {
			t.Fatalf("got %d children, want 2", len(resp.Children))
		}

		far, err := s.database.GetTask(resp.Children[0].ID)
		if err != nil {
			t.Fatalf("get far child: %v", err)
		}
		if far.TrackID != nil {
			t.Errorf("cross-project child inherited track #%d, want trackless", *far.TrackID)
		}

		near, err := s.database.GetTask(resp.Children[1].ID)
		if err != nil {
			t.Fatalf("get near child: %v", err)
		}
		if near.TrackID == nil || *near.TrackID != tr.ID {
			t.Errorf("same-project child TrackID=%v, want %d", near.TrackID, tr.ID)
		}

		waitTaskLeavesInit(t, s, far.ID)
		waitTaskLeavesInit(t, s, near.ID)
	})

	t.Run("global parent track is also not inherited cross-project", func(t *testing.T) {
		s, projA := setupServerWithProject(t)
		mustOtherProject(t, s)

		// A global track (project_id NULL) would be accepted by resolveTrackRef
		// from any project, but the detach rule is deliberately uniform.
		tr, err := s.database.CreateTrack(nil, "Company Wide", "company-wide", "", "", "", nil)
		if err != nil {
			t.Fatalf("create global track: %v", err)
		}
		parent := newParent(t, s, projA, &tr.ID)

		clientConn, serverConn := pipeForHandler(t)
		go s.handleCreateTasksAndWait(serverConn, CreateTasksAndWaitRequest{
			ParentTaskID: parent.ID,
			Tasks:        []CreateTaskRequest{{Title: "far child", Input: "elsewhere", ProjectPath: projBPath}},
		})
		msg := readOneMessage(t, clientConn)
		mustNotError(t, msg)
		resp := decodePayload[CreateTasksAndWaitResponse](t, msg)

		child, err := s.database.GetTask(resp.Children[0].ID)
		if err != nil {
			t.Fatalf("get child: %v", err)
		}
		if child.TrackID != nil {
			t.Errorf("cross-project child inherited global track #%d, want trackless", *child.TrackID)
		}
		waitTaskLeavesInit(t, s, child.ID)
	})

	t.Run("explicit track on a cross-project child resolves in the child's own project", func(t *testing.T) {
		s, projA := setupServerWithProject(t)
		projB := mustOtherProject(t, s)

		trB, err := s.database.CreateTrack(&projB, "Payments", "payments", "", "", "", nil)
		if err != nil {
			t.Fatalf("create project-B track: %v", err)
		}
		parent := newParent(t, s, projA, nil)

		clientConn, serverConn := pipeForHandler(t)
		go s.handleCreateTasksAndWait(serverConn, CreateTasksAndWaitRequest{
			ParentTaskID: parent.ID,
			Tasks:        []CreateTaskRequest{{Title: "far child", Input: "elsewhere", ProjectPath: projBPath, Track: "payments"}},
		})
		msg := readOneMessage(t, clientConn)
		mustNotError(t, msg)
		resp := decodePayload[CreateTasksAndWaitResponse](t, msg)

		child, err := s.database.GetTask(resp.Children[0].ID)
		if err != nil {
			t.Fatalf("get child: %v", err)
		}
		if child.TrackID == nil || *child.TrackID != trB.ID {
			t.Errorf("child TrackID=%v, want project-B track %d", child.TrackID, trB.ID)
		}
		waitTaskLeavesInit(t, s, child.ID)
	})

	t.Run("explicit 'none' on a cross-project child stays trackless", func(t *testing.T) {
		s, projA := setupServerWithProject(t)
		mustOtherProject(t, s)

		tr, err := s.database.CreateTrack(&projA, "Payments", "payments", "", "", "", nil)
		if err != nil {
			t.Fatalf("create track: %v", err)
		}
		parent := newParent(t, s, projA, &tr.ID)

		clientConn, serverConn := pipeForHandler(t)
		go s.handleCreateTasksAndWait(serverConn, CreateTasksAndWaitRequest{
			ParentTaskID: parent.ID,
			Tasks:        []CreateTaskRequest{{Title: "far child", Input: "elsewhere", ProjectPath: projBPath, Track: "none"}},
		})
		msg := readOneMessage(t, clientConn)
		mustNotError(t, msg)
		resp := decodePayload[CreateTasksAndWaitResponse](t, msg)

		child, err := s.database.GetTask(resp.Children[0].ID)
		if err != nil {
			t.Fatalf("get child: %v", err)
		}
		if child.TrackID != nil {
			t.Errorf("child TrackID=%d, want trackless", *child.TrackID)
		}
		waitTaskLeavesInit(t, s, child.ID)
	})
}

func TestHandleWaitForTasks_CrossProject(t *testing.T) {
	t.Run("accepts a pre-existing cross-project child", func(t *testing.T) {
		s, projA := setupServerWithProject(t)
		projB := mustOtherProject(t, s)
		parent := newParent(t, s, projA, nil)

		child, err := s.database.CreateTask(projB, "far child", "desc", "far-child", "wf", "branch-far", task.StatusPending, nil)
		if err != nil {
			t.Fatalf("create child: %v", err)
		}

		clientConn, serverConn := pipeForHandler(t)
		go s.handleWaitForTasks(serverConn, WaitForTasksRequest{ParentTaskID: parent.ID, ChildTaskIDs: []int64{child.ID}})
		msg := readOneMessage(t, clientConn)
		mustNotError(t, msg)
		resp := decodePayload[WaitForTasksResponse](t, msg)

		if len(resp.Children) != 1 || resp.Children[0].ID != child.ID {
			t.Fatalf("resp.Children=%+v, want just #%d", resp.Children, child.ID)
		}
		ids := waitsOnIDs(t, s, parent.ID)
		if len(ids) != 1 || ids[0] != child.ID {
			t.Errorf("waits-on edges = %v, want [%d]", ids, child.ID)
		}
	})

	t.Run("still skips already-terminal cross-project children", func(t *testing.T) {
		s, projA := setupServerWithProject(t)
		projB := mustOtherProject(t, s)
		parent := newParent(t, s, projA, nil)

		child, err := s.database.CreateTask(projB, "done child", "desc", "done-child", "wf", "branch-done", task.StatusCompleted, nil)
		if err != nil {
			t.Fatalf("create child: %v", err)
		}

		clientConn, serverConn := pipeForHandler(t)
		go s.handleWaitForTasks(serverConn, WaitForTasksRequest{ParentTaskID: parent.ID, ChildTaskIDs: []int64{child.ID}})
		msg := readOneMessage(t, clientConn)
		mustNotError(t, msg)
		resp := decodePayload[WaitForTasksResponse](t, msg)

		if len(resp.Children) != 0 {
			t.Errorf("resp.Children=%+v, want empty (terminal child skipped)", resp.Children)
		}
		if ids := waitsOnIDs(t, s, parent.ID); len(ids) != 0 {
			t.Errorf("waits-on edges = %v, want none", ids)
		}
	})

	t.Run("self-wait and cycle guards still fire across projects", func(t *testing.T) {
		s, projA := setupServerWithProject(t)
		projB := mustOtherProject(t, s)
		parent := newParent(t, s, projA, nil)

		child, err := s.database.CreateTask(projB, "far child", "desc", "far-child", "wf", "branch-far", task.StatusPending, nil)
		if err != nil {
			t.Fatalf("create child: %v", err)
		}

		// Self-wait, cross-project handler path.
		clientConn, serverConn := pipeForHandler(t)
		go s.handleWaitForTasks(serverConn, WaitForTasksRequest{ParentTaskID: parent.ID, ChildTaskIDs: []int64{parent.ID}})
		expectError(t, readOneMessage(t, clientConn), "cannot wait on itself")

		// A (proj A) waits on B (proj B).
		clientConn2, serverConn2 := pipeForHandler(t)
		go s.handleWaitForTasks(serverConn2, WaitForTasksRequest{ParentTaskID: parent.ID, ChildTaskIDs: []int64{child.ID}})
		mustNotError(t, readOneMessage(t, clientConn2))

		// B waiting on A closes a cross-project cycle.
		clientConn3, serverConn3 := pipeForHandler(t)
		go s.handleWaitForTasks(serverConn3, WaitForTasksRequest{ParentTaskID: child.ID, ChildTaskIDs: []int64{parent.ID}})
		expectError(t, readOneMessage(t, clientConn3), "would create a cycle")
	})
}

func TestHandleCreateTasksAndWait_ErrorPaths(t *testing.T) {
	t.Run("unknown parent task is rejected", func(t *testing.T) {
		s, _ := setupServerWithProject(t)

		clientConn, serverConn := pipeForHandler(t)
		go s.handleCreateTasksAndWait(serverConn, CreateTasksAndWaitRequest{
			ParentTaskID: 9999,
			Tasks:        []CreateTaskRequest{{Title: "orphan child", Input: "desc"}},
		})
		expectError(t, readOneMessage(t, clientConn), "parent task #9999 not found")
	})

	t.Run("empty tasks array is rejected", func(t *testing.T) {
		s, projA := setupServerWithProject(t)
		parent := newParent(t, s, projA, nil)

		clientConn, serverConn := pipeForHandler(t)
		go s.handleCreateTasksAndWait(serverConn, CreateTasksAndWaitRequest{ParentTaskID: parent.ID})
		expectError(t, readOneMessage(t, clientConn), "at least one child task")
	})

	t.Run("fail-fast leaves earlier cross-project children and their edges in place", func(t *testing.T) {
		// The handler's documented contract: validation is fail-fast and there
		// is NO rollback — children created before the failing spec stay in
		// place with their wait-on edges, so the agent can inspect/delete them.
		// With cross-project fan-out the partial state now spans projects.
		s, projA := setupServerWithProject(t)
		projB := mustOtherProject(t, s)
		parent := newParent(t, s, projA, nil)

		clientConn, serverConn := pipeForHandler(t)
		go s.handleCreateTasksAndWait(serverConn, CreateTasksAndWaitRequest{
			ParentTaskID: parent.ID,
			Tasks: []CreateTaskRequest{
				{Title: "good child", Input: "elsewhere", ProjectPath: projBPath},
				{Title: "bad child", Input: "elsewhere", ProjectPath: projBPath, Track: "no-such-track"},
			},
		})
		expectError(t, readOneMessage(t, clientConn), "failed to create child task 2/2")

		ids := waitsOnIDs(t, s, parent.ID)
		if len(ids) != 1 {
			t.Fatalf("waits-on edges after fail-fast = %v, want exactly the first child's", ids)
		}
		child, err := s.database.GetTask(ids[0])
		if err != nil {
			t.Fatalf("get surviving child: %v", err)
		}
		if child.ProjectID != projB {
			t.Errorf("surviving child ProjectID=%d, want project B (%d)", child.ProjectID, projB)
		}
		// kickOffPostCreate only runs after ALL children succeed, so the
		// surviving child is parked in init — no background goroutine to drain.
		if child.Status != task.StatusInit {
			t.Errorf("surviving child status = %q, want init (post-create never kicked off)", child.Status)
		}
	})
}

func TestHandleWaitForTasks_ErrorPaths(t *testing.T) {
	t.Run("empty child_task_ids is rejected", func(t *testing.T) {
		s, projA := setupServerWithProject(t)
		parent := newParent(t, s, projA, nil)

		clientConn, serverConn := pipeForHandler(t)
		go s.handleWaitForTasks(serverConn, WaitForTasksRequest{ParentTaskID: parent.ID})
		expectError(t, readOneMessage(t, clientConn), "at least one task ID")
	})

	t.Run("unknown child task is rejected", func(t *testing.T) {
		s, projA := setupServerWithProject(t)
		parent := newParent(t, s, projA, nil)

		clientConn, serverConn := pipeForHandler(t)
		go s.handleWaitForTasks(serverConn, WaitForTasksRequest{ParentTaskID: parent.ID, ChildTaskIDs: []int64{9999}})
		expectError(t, readOneMessage(t, clientConn), "child task #9999 not found")
	})

	t.Run("duplicate cross-project child IDs dedupe to a single edge", func(t *testing.T) {
		s, projA := setupServerWithProject(t)
		projB := mustOtherProject(t, s)
		parent := newParent(t, s, projA, nil)

		child, err := s.database.CreateTask(projB, "far child", "desc", "far-child", "wf", "branch-far", task.StatusPending, nil)
		if err != nil {
			t.Fatalf("create child: %v", err)
		}

		clientConn, serverConn := pipeForHandler(t)
		go s.handleWaitForTasks(serverConn, WaitForTasksRequest{ParentTaskID: parent.ID, ChildTaskIDs: []int64{child.ID, child.ID}})
		msg := readOneMessage(t, clientConn)
		mustNotError(t, msg)
		resp := decodePayload[WaitForTasksResponse](t, msg)

		if len(resp.Children) != 1 {
			t.Errorf("resp.Children=%+v, want exactly one (duplicate deduped)", resp.Children)
		}
		if ids := waitsOnIDs(t, s, parent.ID); len(ids) != 1 || ids[0] != child.ID {
			t.Errorf("waits-on edges = %v, want [%d]", ids, child.ID)
		}
	})
}

// TestDeleteCrossProjectChildUnblocksParent is the documented recovery path for
// a stuck cross-project wait: deleting the child drops the wait-on edge, so
// AllWaitsOnTerminal sees an empty (trivially unblocked) set on the next tick.
func TestDeleteCrossProjectChildUnblocksParent(t *testing.T) {
	s, projA := setupServerWithProject(t)
	projB := mustOtherProject(t, s)

	parent := newParent(t, s, projA, nil)
	if err := s.database.UpdateTaskStatus(parent.ID, task.StatusAwaitingChildren); err != nil {
		t.Fatalf("suspend parent: %v", err)
	}

	// worktree=false so the background cleanupDeletedTaskResources does not
	// shell out to git against a path that isn't a repo.
	child, err := s.database.CreateTaskWithPriority(
		projB, "far child", "desc", "far-child", "wf", "", "branch-far", "", "",
		task.StatusRunning, task.PriorityMedium, false, nil, nil,
	)
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := s.database.AddTaskWaitsOn(parent.ID, child.ID); err != nil {
		t.Fatalf("AddTaskWaitsOn: %v", err)
	}

	clientConn, serverConn := pipeForHandler(t)
	go s.handleDeleteTask(serverConn, DeleteTaskRequest{TaskID: child.ID})
	msg := readOneMessage(t, clientConn)
	mustNotError(t, msg)
	if msg.Type != MsgOK {
		t.Fatalf("delete response type = %s, want %s", msg.Type, MsgOK)
	}

	if ids := waitsOnIDs(t, s, parent.ID); len(ids) != 0 {
		t.Fatalf("waits-on edges = %v after deleting the child, want none", ids)
	}

	s.checkSuspendedParents()

	refreshed, err := s.database.GetTask(parent.ID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if refreshed.Status != task.StatusPending {
		t.Errorf("parent status = %q, want pending (unblocked by the delete)", refreshed.Status)
	}
}
