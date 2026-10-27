package db

import (
	"testing"

	"github.com/Bakaface/sakusen/internal/task"
)

func mustCreateFanoutChild(t *testing.T, db *DB, projectID int64, origin task.FanoutOrigin) *task.Task {
	t.Helper()
	tk, err := db.CreateFanoutChildTask(projectID, "child-"+origin.Item, "input-"+origin.Item, origin.Item, "wf", "", "", "", "", task.StatusPending, task.PriorityMedium, true, nil, nil, origin)
	if err != nil {
		t.Fatalf("create fanout child %+v: %v", origin, err)
	}
	return tk
}

func TestCreateFanoutChildTaskSetsOrigin(t *testing.T) {
	db := openTestDBForWaits(t)
	pid := mustProject(t, db, "/p")
	parent := mustCreateTaskForWaits(t, db, pid, "parent")

	child := mustCreateFanoutChild(t, db, pid, task.FanoutOrigin{ParentID: parent.ID, Step: "spawn", Item: "a"})
	got, err := db.GetTask(child.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.FanoutParentID == nil || *got.FanoutParentID != parent.ID {
		t.Errorf("FanoutParentID = %v, want %d", got.FanoutParentID, parent.ID)
	}
	if got.FanoutStep != "spawn" || got.FanoutItem != "a" || got.FanoutAttempts != 0 {
		t.Errorf("fanout fields = (%q, %q, %d), want (spawn, a, 0)", got.FanoutStep, got.FanoutItem, got.FanoutAttempts)
	}

	if parent.FanoutParentID != nil || parent.FanoutStep != "" || parent.FanoutItem != "" {
		t.Errorf("ordinary task has fanout fields: %+v", parent)
	}
}

func TestCreateFanoutChildTaskRejectsDuplicateItem(t *testing.T) {
	db := openTestDBForWaits(t)
	pid := mustProject(t, db, "/p")
	p1 := mustCreateTaskForWaits(t, db, pid, "p1")
	p2 := mustCreateTaskForWaits(t, db, pid, "p2")

	mustCreateFanoutChild(t, db, pid, task.FanoutOrigin{ParentID: p1.ID, Step: "spawn", Item: "a"})
	if _, err := db.CreateFanoutChildTask(pid, "dup", "dup", "a", "wf", "", "", "", "", task.StatusPending, task.PriorityMedium, true, nil, nil, task.FanoutOrigin{ParentID: p1.ID, Step: "spawn", Item: "a"}); err == nil {
		t.Fatal("duplicate (parent, step, item) insert succeeded, want constraint error")
	}
	mustCreateFanoutChild(t, db, pid, task.FanoutOrigin{ParentID: p1.ID, Step: "other", Item: "a"})
	mustCreateFanoutChild(t, db, pid, task.FanoutOrigin{ParentID: p2.ID, Step: "spawn", Item: "a"})
}

func TestGetFanoutChildrenFiltersByStep(t *testing.T) {
	db := openTestDBForWaits(t)
	pid := mustProject(t, db, "/p")
	parent := mustCreateTaskForWaits(t, db, pid, "parent")
	a := mustCreateFanoutChild(t, db, pid, task.FanoutOrigin{ParentID: parent.ID, Step: "s1", Item: "a"})
	b := mustCreateFanoutChild(t, db, pid, task.FanoutOrigin{ParentID: parent.ID, Step: "s2", Item: "b"})
	c := mustCreateFanoutChild(t, db, pid, task.FanoutOrigin{ParentID: parent.ID, Step: "s1", Item: "c"})

	s1, err := db.GetFanoutChildren(parent.ID, "s1")
	if err != nil {
		t.Fatalf("GetFanoutChildren: %v", err)
	}
	if len(s1) != 2 || s1[0].ID != a.ID || s1[1].ID != c.ID {
		t.Errorf("s1 children = %v, want [%d %d]", taskIDs(s1), a.ID, c.ID)
	}

	all, err := db.GetAllFanoutChildren(parent.ID)
	if err != nil {
		t.Fatalf("GetAllFanoutChildren: %v", err)
	}
	if len(all) != 3 || all[0].ID != a.ID || all[1].ID != b.ID || all[2].ID != c.ID {
		t.Errorf("all children = %v, want [%d %d %d]", taskIDs(all), a.ID, b.ID, c.ID)
	}
}

func taskIDs(ts []*task.Task) []int64 {
	ids := make([]int64, len(ts))
	for i, t := range ts {
		ids[i] = t.ID
	}
	return ids
}

func TestGetFanoutDepth(t *testing.T) {
	db := openTestDBForWaits(t)
	pid := mustProject(t, db, "/p")
	root := mustCreateTaskForWaits(t, db, pid, "root")
	mid := mustCreateFanoutChild(t, db, pid, task.FanoutOrigin{ParentID: root.ID, Step: "s", Item: "mid"})
	leaf := mustCreateFanoutChild(t, db, pid, task.FanoutOrigin{ParentID: mid.ID, Step: "s", Item: "leaf"})

	for _, tc := range []struct {
		id   int64
		want int
	}{{root.ID, 0}, {mid.ID, 1}, {leaf.ID, 2}} {
		got, err := db.GetFanoutDepth(tc.id)
		if err != nil {
			t.Fatalf("GetFanoutDepth(%d): %v", tc.id, err)
		}
		if got != tc.want {
			t.Errorf("GetFanoutDepth(%d) = %d, want %d", tc.id, got, tc.want)
		}
	}
}

func TestGetFanoutDepthTerminatesOnLoop(t *testing.T) {
	db := openTestDBForWaits(t)
	pid := mustProject(t, db, "/p")
	a := mustCreateTaskForWaits(t, db, pid, "a")
	b := mustCreateFanoutChild(t, db, pid, task.FanoutOrigin{ParentID: a.ID, Step: "s", Item: "b"})
	if _, err := db.sqlDB.Exec(`UPDATE tasks SET fanout_parent_id = ? WHERE id = ?`, b.ID, a.ID); err != nil {
		t.Fatalf("corrupt parent link: %v", err)
	}
	if _, err := db.GetFanoutDepth(b.ID); err != nil {
		t.Fatalf("GetFanoutDepth on a loop: %v", err)
	}
}

func TestFanoutAttempts(t *testing.T) {
	db := openTestDBForWaits(t)
	pid := mustProject(t, db, "/p")
	tk := mustCreateTaskForWaits(t, db, pid, "t")

	for want := 1; want <= 2; want++ {
		got, err := db.IncrementFanoutAttempts(tk.ID)
		if err != nil {
			t.Fatalf("IncrementFanoutAttempts: %v", err)
		}
		if got != want {
			t.Errorf("IncrementFanoutAttempts = %d, want %d", got, want)
		}
	}
	if err := db.ResetFanoutAttempts(tk.ID); err != nil {
		t.Fatalf("ResetFanoutAttempts: %v", err)
	}
	got, err := db.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.FanoutAttempts != 0 {
		t.Errorf("FanoutAttempts after reset = %d, want 0", got.FanoutAttempts)
	}
}

func TestDeletingFanoutParentKeepsChild(t *testing.T) {
	db := openTestDBForWaits(t)
	pid := mustProject(t, db, "/p")
	parent := mustCreateTaskForWaits(t, db, pid, "parent")
	child := mustCreateFanoutChild(t, db, pid, task.FanoutOrigin{ParentID: parent.ID, Step: "s", Item: "a"})

	if err := db.DeleteTask(parent.ID); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	got, err := db.GetTask(child.ID)
	if err != nil {
		t.Fatalf("child gone after parent delete: %v", err)
	}
	if got.FanoutParentID != nil {
		t.Errorf("FanoutParentID = %d, want nil", *got.FanoutParentID)
	}
}
