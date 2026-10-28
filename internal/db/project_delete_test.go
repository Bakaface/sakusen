package db

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/task"
)

// countRows returns how many rows of table match where (a fixed SQL
// fragment) with args.
func countRows(t *testing.T, database *DB, table, where string, args ...any) int {
	t.Helper()
	var n int
	if err := database.sqlDB.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE "+where, args...).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// seedProject gives a project terminal tasks with every kind of dependent
// row: steps, chats, published items, dependency and waits-on edges, a fanout
// child, a routine whose last run is one of the tasks, and a track hierarchy.
func seedProject(t *testing.T, database *DB, proj *Project) []int64 {
	t.Helper()
	track, err := database.CreateTrack(&proj.ID, "Track", "track", "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateTrack(&proj.ID, "Child", "child", "", "", "", &track.ID); err != nil {
		t.Fatal(err)
	}
	parent, err := database.CreateTaskWithPriority(proj.ID, "parent", "in", "parent", "default", "", "", "", "", task.StatusCompleted, task.PriorityMedium, true, nil, &track.ID)
	if err != nil {
		t.Fatal(err)
	}
	child, err := database.CreateFanoutChildTask(proj.ID, "child", "in", "child", "default", "", "", "", "", task.StatusFailed, task.PriorityMedium, true, nil, nil, task.FanoutOrigin{ParentID: parent.ID, Step: "fan", Item: "a"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := database.CreateTask(proj.ID, "other", "in", "other", "default", "", task.StatusMergeFailed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateTaskStep(parent.ID, "impl"); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertChat(parent.ID, "impl", "session", ""); err != nil {
		t.Fatal(err)
	}
	if err := database.PublishItems(parent.ID, "plan", []task.PublishedItem{{Slug: "a", Input: "x"}}); err != nil {
		t.Fatal(err)
	}
	if err := database.AddTaskDependency(other.ID, parent.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.AddTaskWaitsOn(parent.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	def, err := database.UpsertPeriodicDef(proj.ID, "nightly", "@daily", "", "default", "", "", false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetTaskPeriodicID(other.ID, def.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdatePeriodicLastTask(def.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	return []int64{parent.ID, child.ID, other.ID}
}

func TestDeleteProject_RemovesTerminalTasksAndEverythingKeyedByProject(t *testing.T) {
	database := openTestDB(t)
	proj, err := database.GetOrCreateProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keep, err := database.GetOrCreateProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ids := seedProject(t, database, proj)
	keptIDs := seedProject(t, database, keep)

	if err := database.DeleteProject(proj.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	if _, err := database.GetProject(proj.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("project still present: %v", err)
	}
	if n := countRows(t, database, "tasks", "project_id = ?", proj.ID); n != 0 {
		t.Errorf("%d tasks left", n)
	}
	if n := countRows(t, database, "periodic_definitions", "project_id = ?", proj.ID); n != 0 {
		t.Errorf("%d routine rows left", n)
	}
	if n := countRows(t, database, "tracks", "project_id = ?", proj.ID); n != 0 {
		t.Errorf("%d tracks left", n)
	}
	for _, table := range []string{"task_steps", "chats", "task_published_items"} {
		if n := countRows(t, database, table, "task_id IN (?, ?, ?)", ids[0], ids[1], ids[2]); n != 0 {
			t.Errorf("%d %s rows left", n, table)
		}
	}
	if n := countRows(t, database, "task_dependencies", "task_id IN (?, ?, ?) OR blocked_by IN (?, ?, ?)", ids[0], ids[1], ids[2], ids[0], ids[1], ids[2]); n != 0 {
		t.Errorf("%d dependency edges left", n)
	}
	if n := countRows(t, database, "task_waits_on", "task_id IN (?, ?, ?) OR waits_on_id IN (?, ?, ?)", ids[0], ids[1], ids[2], ids[0], ids[1], ids[2]); n != 0 {
		t.Errorf("%d waits-on edges left", n)
	}

	// The other project is untouched.
	if n := countRows(t, database, "tasks", "project_id = ?", keep.ID); n != len(keptIDs) {
		t.Errorf("other project has %d tasks, want %d", n, len(keptIDs))
	}
	if n := countRows(t, database, "periodic_definitions", "project_id = ?", keep.ID); n != 1 {
		t.Errorf("other project has %d routine rows, want 1", n)
	}
	if n := countRows(t, database, "tracks", "project_id = ?", keep.ID); n != 2 {
		t.Errorf("other project has %d tracks, want 2", n)
	}
	if n := countRows(t, database, "task_steps", "task_id = ?", keptIDs[0]); n != 1 {
		t.Errorf("other project has %d step rows, want 1", n)
	}
}

func TestDeleteProject_DetachesForeignTasksFromItsTracks(t *testing.T) {
	database := openTestDB(t)
	proj, err := database.GetOrCreateProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other, err := database.GetOrCreateProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	track, err := database.CreateTrack(&proj.ID, "Track", "track", "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := database.CreateTaskWithPriority(other.ID, "t", "in", "t", "default", "", "", "", "", task.StatusRunning, task.PriorityMedium, true, nil, &track.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := database.DeleteProject(proj.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	got, err := database.GetTask(foreign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrackID != nil {
		t.Errorf("foreign task still attached to track %d", *got.TrackID)
	}
}

func TestDeleteProject_RefusesWithActiveTask(t *testing.T) {
	database := openTestDB(t)
	proj, err := database.GetOrCreateProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ids := seedProject(t, database, proj)
	active, err := database.CreateTask(proj.ID, "busy", "in", "busy", "default", "", task.StatusAwaitingApproval, nil)
	if err != nil {
		t.Fatal(err)
	}

	err = database.DeleteProject(proj.ID)
	var activeErr *ActiveTasksError
	if !errors.As(err, &activeErr) {
		t.Fatalf("expected *ActiveTasksError, got %v", err)
	}
	if len(activeErr.TaskIDs) != 1 || activeErr.TaskIDs[0] != active.ID {
		t.Errorf("active tasks = %v, want [%d]", activeErr.TaskIDs, active.ID)
	}
	if _, err := database.GetProject(proj.ID); err != nil {
		t.Errorf("project removed despite refusal: %v", err)
	}
	if n := countRows(t, database, "tasks", "project_id = ?", proj.ID); n != len(ids)+1 {
		t.Errorf("%d tasks left, want %d", n, len(ids)+1)
	}
}

func TestDeleteProject_MissingProject(t *testing.T) {
	database := openTestDB(t)
	if err := database.DeleteProject(42); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestCountTasksByProject(t *testing.T) {
	database := openTestDB(t)
	proj, err := database.GetOrCreateProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	empty, err := database.GetOrCreateProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seedProject(t, database, proj)
	if _, err := database.CreateTask(proj.ID, "p", "in", "p", "default", "", task.StatusPending, nil); err != nil {
		t.Fatal(err)
	}

	counts, err := database.CountTasksByProject()
	if err != nil {
		t.Fatal(err)
	}
	if got := counts[proj.ID]; got != (ProjectTaskCounts{Total: 4, Active: 1}) {
		t.Errorf("counts = %+v, want {Total:4 Active:1}", got)
	}
	if _, ok := counts[empty.ID]; ok {
		t.Errorf("project without tasks should be absent")
	}
}
