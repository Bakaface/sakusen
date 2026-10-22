package db

import (
	"testing"

	"github.com/Bakaface/sakusen/internal/task"
)

// A pre-workdir database is upgraded in place; existing rows read as "".
func TestMigrateV25AddsWorkdir(t *testing.T) {
	database := openTestDB(t)
	tk := newHibernationTask(t, database)

	if _, err := database.sqlDB.Exec(`ALTER TABLE tasks DROP COLUMN workdir`); err != nil {
		t.Fatalf("failed to drop tasks.workdir: %v", err)
	}
	if _, err := database.sqlDB.Exec(`UPDATE schema_version SET version = 24`); err != nil {
		t.Fatalf("failed to roll back schema_version: %v", err)
	}
	if err := database.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	got, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Workdir != "" {
		t.Errorf("existing row Workdir = %q, want empty", got.Workdir)
	}
}

func TestSetTaskWorkdirRoundTrip(t *testing.T) {
	database := openTestDB(t)
	tk := newHibernationTask(t, database)
	if err := database.SetTaskWorkdir(tk.ID, "em/rails-foundation"); err != nil {
		t.Fatalf("SetTaskWorkdir: %v", err)
	}
	got, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Workdir != "em/rails-foundation" {
		t.Errorf("GetTask Workdir = %q", got.Workdir)
	}
	all, err := database.getTasksByStatus(task.StatusTmux)
	if err != nil {
		t.Fatalf("getTasksByStatus: %v", err)
	}
	if len(all) != 1 || all[0].Workdir != "em/rails-foundation" {
		t.Errorf("list query Workdir mismatch: %+v", all)
	}
}
