package db

import (
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/task"
)

func newPendingTask(t *testing.T, database *DB, projID int64, title string) *task.Task {
	t.Helper()
	tk, err := database.CreateTask(projID, title, "input", title, "default", "", task.StatusPending, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return tk
}

func claimableIDs(t *testing.T, database *DB, now time.Time) map[int64]bool {
	t.Helper()
	tasks, err := database.GetClaimableTasks(now)
	if err != nil {
		t.Fatalf("GetClaimableTasks: %v", err)
	}
	ids := make(map[int64]bool, len(tasks))
	for _, tk := range tasks {
		ids[tk.ID] = true
	}
	return ids
}

func TestGetClaimableTasks_HonorsNotBefore(t *testing.T) {
	database := openTestDB(t)
	proj, _ := database.GetOrCreateProject(t.TempDir())
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	plain := newPendingTask(t, database, proj.ID, "plain")
	future := newPendingTask(t, database, proj.ID, "future")
	past := newPendingTask(t, database, proj.ID, "past")

	// Offsets differ from UTC to exercise normalization: +02:00 13:30 is 11:30Z.
	futureAt := time.Date(2026, 3, 1, 14, 30, 0, 0, time.FixedZone("", 2*3600)) // 12:30Z
	pastAt := time.Date(2026, 3, 1, 13, 30, 0, 0, time.FixedZone("", 2*3600))   // 11:30Z
	if err := database.SetTaskNotBefore(future.ID, &futureAt); err != nil {
		t.Fatal(err)
	}
	if err := database.SetTaskNotBefore(past.ID, &pastAt); err != nil {
		t.Fatal(err)
	}

	ids := claimableIDs(t, database, now)
	if !ids[plain.ID] || !ids[past.ID] || ids[future.ID] {
		t.Errorf("at %v claimable = %v; want plain and past only", now, ids)
	}

	// Comparing against a local-offset now must agree with the UTC instant.
	later := time.Date(2026, 3, 1, 15, 30, 0, 0, time.FixedZone("", 3*3600)) // 12:30Z
	if !claimableIDs(t, database, later)[future.ID] {
		t.Errorf("future task not claimable once now == not_before")
	}

	got, err := database.GetTask(future.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.NotBefore == nil || !got.NotBefore.Equal(futureAt) || got.NotBefore.Location() != time.UTC {
		t.Errorf("NotBefore round-trip = %v, want %v in UTC", got.NotBefore, futureAt)
	}

	if err := database.SetTaskNotBefore(future.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := database.GetTask(future.ID); got.NotBefore != nil {
		t.Errorf("NotBefore after clear = %v, want nil", got.NotBefore)
	}
}

func TestGetClaimableTasks_NotBeforeAndBlocker(t *testing.T) {
	database := openTestDB(t)
	proj, _ := database.GetOrCreateProject(t.TempDir())
	now := time.Now()

	blocker := newPendingTask(t, database, proj.ID, "blocker")
	tk := newPendingTask(t, database, proj.ID, "both")
	if err := database.SetTaskDependencies(tk.ID, []int64{blocker.ID}); err != nil {
		t.Fatal(err)
	}
	at := now.Add(time.Hour)
	if err := database.SetTaskNotBefore(tk.ID, &at); err != nil {
		t.Fatal(err)
	}

	if claimableIDs(t, database, now.Add(2*time.Hour))[tk.ID] {
		t.Error("claimable with unfinished blocker after not_before")
	}
	if err := database.UpdateTaskStatus(blocker.ID, task.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if claimableIDs(t, database, now)[tk.ID] {
		t.Error("claimable before not_before with completed blocker")
	}
	if !claimableIDs(t, database, now.Add(2*time.Hour))[tk.ID] {
		t.Error("not claimable once both conditions cleared")
	}
}

func TestSetPendingTaskNotBefore_OnlyPending(t *testing.T) {
	database := openTestDB(t)
	proj, _ := database.GetOrCreateProject(t.TempDir())
	tk := newPendingTask(t, database, proj.ID, "x")
	at := time.Now().Add(time.Hour)

	ok, err := database.SetPendingTaskNotBefore(tk.ID, &at)
	if err != nil || !ok {
		t.Fatalf("pending: ok=%v err=%v", ok, err)
	}
	if err := database.UpdateTaskStatus(tk.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}
	ok, err = database.SetPendingTaskNotBefore(tk.ID, nil)
	if err != nil || ok {
		t.Fatalf("running: ok=%v err=%v, want false", ok, err)
	}
	if got, _ := database.GetTask(tk.ID); got.NotBefore == nil {
		t.Error("not_before changed on a running task")
	}
}

func TestMigrateV25AddsNotBeforeAndAfterColumns(t *testing.T) {
	database := openTestDB(t)
	for _, stmt := range []string{
		`ALTER TABLE tasks DROP COLUMN not_before`,
		`ALTER TABLE periodic_definitions DROP COLUMN after_name`,
		`ALTER TABLE periodic_definitions DROP COLUMN after_last_task_id`,
		`UPDATE schema_version SET version = 24`,
	} {
		if _, err := database.sqlDB.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := database.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Replaying on the final schema must be a no-op.
	if err := migrateV25(database); err != nil {
		t.Fatalf("migrateV25 replay: %v", err)
	}
	for _, q := range []string{
		`SELECT not_before FROM tasks LIMIT 1`,
		`SELECT after_name, after_last_task_id FROM periodic_definitions LIMIT 1`,
	} {
		if _, err := database.sqlDB.Exec(q); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}
}
