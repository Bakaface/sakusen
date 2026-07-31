package db

import (
	"path/filepath"
	"testing"

	"github.com/Bakaface/sortie/internal/task"
)

func openTestDBForWaits(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "tasks.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustCreateTaskForWaits(t *testing.T, db *DB, projectID int64, title string) *task.Task {
	t.Helper()
	tk, err := db.CreateTask(projectID, title, "desc-"+title, "slug-"+title, "wf", "branch-"+title, task.StatusPending, nil)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return tk
}

func mustProject(t *testing.T, db *DB, path string) int64 {
	t.Helper()
	proj, err := db.GetOrCreateProject(path)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return proj.ID
}

func TestAddAndGetTaskWaitsOn(t *testing.T) {
	db := openTestDBForWaits(t)
	projID := mustProject(t, db, "/tmp/proj-waits-1")
	parent := mustCreateTaskForWaits(t, db, projID, "parent")
	c1 := mustCreateTaskForWaits(t, db, projID, "child1")
	c2 := mustCreateTaskForWaits(t, db, projID, "child2")

	if err := db.AddTaskWaitsOn(parent.ID, c1.ID); err != nil {
		t.Fatalf("AddTaskWaitsOn c1: %v", err)
	}
	if err := db.AddTaskWaitsOn(parent.ID, c2.ID); err != nil {
		t.Fatalf("AddTaskWaitsOn c2: %v", err)
	}
	// Idempotent
	if err := db.AddTaskWaitsOn(parent.ID, c1.ID); err != nil {
		t.Fatalf("AddTaskWaitsOn duplicate: %v", err)
	}

	ids, err := db.GetTaskWaitsOn(parent.ID)
	if err != nil {
		t.Fatalf("GetTaskWaitsOn: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("got %d edges, want 2: %v", len(ids), ids)
	}

	has, err := db.HasAnyWaitsOn(parent.ID)
	if err != nil || !has {
		t.Fatalf("HasAnyWaitsOn=%v err=%v", has, err)
	}
}

func TestAddTaskWaitsOn_RejectsSelfWait(t *testing.T) {
	db := openTestDBForWaits(t)
	projID := mustProject(t, db, "/tmp/proj-waits-self")
	parent := mustCreateTaskForWaits(t, db, projID, "parent")

	if err := db.AddTaskWaitsOn(parent.ID, parent.ID); err == nil {
		t.Fatalf("expected error for self-wait, got nil")
	}
}

func TestAllWaitsOnTerminal(t *testing.T) {
	db := openTestDBForWaits(t)
	projID := mustProject(t, db, "/tmp/proj-waits-2")
	parent := mustCreateTaskForWaits(t, db, projID, "parent")
	c1 := mustCreateTaskForWaits(t, db, projID, "c1")
	c2 := mustCreateTaskForWaits(t, db, projID, "c2")

	// No edges → trivially all terminal
	allDone, err := db.AllWaitsOnTerminal(parent.ID)
	if err != nil || !allDone {
		t.Fatalf("empty wait set: allDone=%v err=%v", allDone, err)
	}

	db.AddTaskWaitsOn(parent.ID, c1.ID)
	db.AddTaskWaitsOn(parent.ID, c2.ID)

	allDone, err = db.AllWaitsOnTerminal(parent.ID)
	if err != nil || allDone {
		t.Fatalf("none terminal: allDone=%v err=%v", allDone, err)
	}

	if err := db.UpdateTaskStatus(c1.ID, task.StatusCompleted); err != nil {
		t.Fatalf("update c1: %v", err)
	}
	allDone, err = db.AllWaitsOnTerminal(parent.ID)
	if err != nil || allDone {
		t.Fatalf("one terminal: allDone=%v err=%v", allDone, err)
	}

	if err := db.UpdateTaskStatus(c2.ID, task.StatusFailed); err != nil {
		t.Fatalf("update c2 failed: %v", err)
	}
	allDone, err = db.AllWaitsOnTerminal(parent.ID)
	if err != nil || !allDone {
		t.Fatalf("both terminal (failed counts): allDone=%v err=%v", allDone, err)
	}
}

func TestRemoveAllTaskWaitsOn(t *testing.T) {
	db := openTestDBForWaits(t)
	projID := mustProject(t, db, "/tmp/proj-waits-3")
	parent := mustCreateTaskForWaits(t, db, projID, "parent")
	c1 := mustCreateTaskForWaits(t, db, projID, "c1")
	c2 := mustCreateTaskForWaits(t, db, projID, "c2")
	db.AddTaskWaitsOn(parent.ID, c1.ID)
	db.AddTaskWaitsOn(parent.ID, c2.ID)

	if err := db.RemoveAllTaskWaitsOn(parent.ID); err != nil {
		t.Fatalf("RemoveAllTaskWaitsOn: %v", err)
	}

	has, _ := db.HasAnyWaitsOn(parent.ID)
	if has {
		t.Fatalf("expected no waits-on after clear")
	}
}

func TestGetWaitsOnChildren(t *testing.T) {
	db := openTestDBForWaits(t)
	projID := mustProject(t, db, "/tmp/proj-waits-4")
	parent := mustCreateTaskForWaits(t, db, projID, "parent")
	c1 := mustCreateTaskForWaits(t, db, projID, "c1")
	c2 := mustCreateTaskForWaits(t, db, projID, "c2")

	db.AddTaskWaitsOn(parent.ID, c1.ID)
	db.AddTaskWaitsOn(parent.ID, c2.ID)
	db.UpdateTaskContext(c1.ID, "child1 context")
	db.UpdateTaskContext(c2.ID, "child2 context")
	db.UpdateTaskStatus(c1.ID, task.StatusCompleted)
	db.UpdateTaskStatus(c2.ID, task.StatusFailed)

	children, err := db.GetWaitsOnChildren(parent.ID)
	if err != nil {
		t.Fatalf("GetWaitsOnChildren: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("got %d children, want 2", len(children))
	}
	byID := map[int64]*task.Task{children[0].ID: children[0], children[1].ID: children[1]}
	if got := byID[c1.ID].Context; got != "child1 context" {
		t.Errorf("c1.Context=%q want %q", got, "child1 context")
	}
	if got := byID[c1.ID].Status; got != task.StatusCompleted {
		t.Errorf("c1.Status=%q want completed", got)
	}
	if got := byID[c2.ID].Status; got != task.StatusFailed {
		t.Errorf("c2.Status=%q want failed", got)
	}
}

func TestGetTasksAwaitingChildren(t *testing.T) {
	db := openTestDBForWaits(t)
	projID := mustProject(t, db, "/tmp/proj-waits-5")
	p1 := mustCreateTaskForWaits(t, db, projID, "p1")
	p2 := mustCreateTaskForWaits(t, db, projID, "p2")
	_ = mustCreateTaskForWaits(t, db, projID, "p3") // stays pending

	db.UpdateTaskStatus(p1.ID, task.StatusAwaitingChildren)
	db.UpdateTaskStatus(p2.ID, task.StatusAwaitingChildren)

	tasks, err := db.GetTasksAwaitingChildren()
	if err != nil {
		t.Fatalf("GetTasksAwaitingChildren: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d, want 2 awaiting-children tasks", len(tasks))
	}
}

func TestHasCircularWaitsOn(t *testing.T) {
	db := openTestDBForWaits(t)
	projID := mustProject(t, db, "/tmp/proj-waits-cycle")
	a := mustCreateTaskForWaits(t, db, projID, "a")
	b := mustCreateTaskForWaits(t, db, projID, "b")
	c := mustCreateTaskForWaits(t, db, projID, "c")

	// a waits on b; b waits on c
	db.AddTaskWaitsOn(a.ID, b.ID)
	db.AddTaskWaitsOn(b.ID, c.ID)

	// Adding c → a would close the cycle a → b → c → a
	got, err := db.HasCircularWaitsOn(c.ID, a.ID)
	if err != nil {
		t.Fatalf("HasCircularWaitsOn: %v", err)
	}
	if !got {
		t.Errorf("expected cycle a → b → c → a, got false")
	}

	// Self-cycle is trivially circular
	if got, _ := db.HasCircularWaitsOn(a.ID, a.ID); !got {
		t.Errorf("expected self-cycle to be detected")
	}

	// Unrelated edge is not circular
	d := mustCreateTaskForWaits(t, db, projID, "d")
	if got, err := db.HasCircularWaitsOn(d.ID, a.ID); err != nil || got {
		t.Errorf("expected no cycle for unrelated d → a, got %v err=%v", got, err)
	}
}

func TestHasCircularWaitsOn_CrossesDependencies(t *testing.T) {
	db := openTestDBForWaits(t)
	projID := mustProject(t, db, "/tmp/proj-waits-cycle-mixed")
	a := mustCreateTaskForWaits(t, db, projID, "a")
	b := mustCreateTaskForWaits(t, db, projID, "b")

	// b blocked_by a (dependency edge)
	db.AddTaskDependency(b.ID, a.ID)

	// Now adding a wait edge a → b would create a cycle: a waits on b which is
	// blocked_by a. HasCircularWaitsOn walks BOTH graphs.
	got, err := db.HasCircularWaitsOn(a.ID, b.ID)
	if err != nil {
		t.Fatalf("HasCircularWaitsOn: %v", err)
	}
	if !got {
		t.Errorf("expected cross-graph cycle (waits ∪ deps) to be detected")
	}
}

func TestDeleteTask_CleansWaitsOnEdges(t *testing.T) {
	db := openTestDBForWaits(t)
	projID := mustProject(t, db, "/tmp/proj-waits-del")
	parent := mustCreateTaskForWaits(t, db, projID, "parent")
	child := mustCreateTaskForWaits(t, db, projID, "child")
	db.AddTaskWaitsOn(parent.ID, child.ID)

	if err := db.DeleteTask(child.ID); err != nil {
		t.Fatalf("DeleteTask child: %v", err)
	}
	has, _ := db.HasAnyWaitsOn(parent.ID)
	if has {
		t.Errorf("expected waits-on edge removed when child deleted")
	}
}

// --- Cross-project waits ---------------------------------------------------
//
// task_waits_on is a pure task-ID relation with no project column, so a parent
// may wait on children living in an entirely different project. These tests
// lock that in so a future change does not add a project filter.

func TestAllWaitsOnTerminal_CrossProject(t *testing.T) {
	db := openTestDBForWaits(t)
	projA := mustProject(t, db, "/tmp/proj-a")
	projB := mustProject(t, db, "/tmp/proj-b")
	if projA == projB {
		t.Fatalf("expected distinct project rows, both = %d", projA)
	}

	parent := mustCreateTaskForWaits(t, db, projA, "xparent")
	c1 := mustCreateTaskForWaits(t, db, projB, "xc1")
	c2 := mustCreateTaskForWaits(t, db, projB, "xc2")

	if err := db.AddTaskWaitsOn(parent.ID, c1.ID); err != nil {
		t.Fatalf("AddTaskWaitsOn c1: %v", err)
	}
	if err := db.AddTaskWaitsOn(parent.ID, c2.ID); err != nil {
		t.Fatalf("AddTaskWaitsOn c2: %v", err)
	}

	allDone, err := db.AllWaitsOnTerminal(parent.ID)
	if err != nil || allDone {
		t.Fatalf("non-terminal cross-project children: allDone=%v err=%v", allDone, err)
	}

	if err := db.UpdateTaskStatus(c1.ID, task.StatusCompleted); err != nil {
		t.Fatalf("update c1: %v", err)
	}
	if err := db.UpdateTaskStatus(c2.ID, task.StatusFailed); err != nil {
		t.Fatalf("update c2: %v", err)
	}

	allDone, err = db.AllWaitsOnTerminal(parent.ID)
	if err != nil || !allDone {
		t.Fatalf("completed+failed cross-project children should be terminal: allDone=%v err=%v", allDone, err)
	}
}

func TestGetWaitsOnChildren_CrossProject(t *testing.T) {
	db := openTestDBForWaits(t)
	projA := mustProject(t, db, "/tmp/proj-a-children")
	projB := mustProject(t, db, "/tmp/proj-b-children")

	parent := mustCreateTaskForWaits(t, db, projA, "xcparent")
	c1 := mustCreateTaskForWaits(t, db, projB, "xcc1")
	c2 := mustCreateTaskForWaits(t, db, projB, "xcc2")
	db.AddTaskWaitsOn(parent.ID, c1.ID)
	db.AddTaskWaitsOn(parent.ID, c2.ID)
	db.UpdateTaskStatus(c1.ID, task.StatusCompleted)
	db.UpdateTaskStatus(c2.ID, task.StatusFailed)

	children, err := db.GetWaitsOnChildren(parent.ID)
	if err != nil {
		t.Fatalf("GetWaitsOnChildren: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("got %d children, want 2", len(children))
	}
	// ORDER BY id ASC
	if children[0].ID != c1.ID || children[1].ID != c2.ID {
		t.Fatalf("children not ordered by id: got %d,%d want %d,%d", children[0].ID, children[1].ID, c1.ID, c2.ID)
	}
	for _, c := range children {
		if c.ProjectID != projB {
			t.Errorf("child #%d ProjectID=%d, want %d (the child's own project)", c.ID, c.ProjectID, projB)
		}
	}
	if children[0].Status != task.StatusCompleted {
		t.Errorf("c1.Status=%q want completed", children[0].Status)
	}
	if children[1].Status != task.StatusFailed {
		t.Errorf("c2.Status=%q want failed", children[1].Status)
	}
}

func TestHasCircularWaitsOn_CrossProject(t *testing.T) {
	db := openTestDBForWaits(t)
	projA := mustProject(t, db, "/tmp/proj-a-cycle")
	projB := mustProject(t, db, "/tmp/proj-b-cycle")

	a := mustCreateTaskForWaits(t, db, projA, "xa")
	b := mustCreateTaskForWaits(t, db, projB, "xb")

	if err := db.AddTaskWaitsOn(a.ID, b.ID); err != nil {
		t.Fatalf("AddTaskWaitsOn a->b: %v", err)
	}

	// b waiting on a would close a cycle spanning two projects.
	got, err := db.HasCircularWaitsOn(b.ID, a.ID)
	if err != nil {
		t.Fatalf("HasCircularWaitsOn: %v", err)
	}
	if !got {
		t.Errorf("expected cross-project cycle a → b → a to be detected")
	}
}
