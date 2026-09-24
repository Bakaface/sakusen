package db

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/task"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func newHibernationTask(t *testing.T, database *DB) *task.Task {
	t.Helper()
	proj, err := database.GetOrCreateProject(t.TempDir())
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	tk, err := database.CreateTask(proj.ID, "Tmux task", "input", "slug", "default", "", task.StatusTmux, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return tk
}

// A fresh database gets hibernated_at from schema.sql.
func TestFreshSchemaHasHibernatedAt(t *testing.T) {
	database := openTestDB(t)
	if _, err := database.sqlDB.Exec(`SELECT hibernated_at FROM tasks LIMIT 1`); err != nil {
		t.Errorf("expected schema.sql to define tasks.hibernated_at: %v", err)
	}
}

// A database last migrated at version 23 is upgraded in place by migrateV24.
func TestMigrateV24AddsHibernatedAt(t *testing.T) {
	database := openTestDB(t)

	if _, err := database.sqlDB.Exec(`ALTER TABLE tasks DROP COLUMN hibernated_at`); err != nil {
		t.Fatalf("failed to drop tasks.hibernated_at: %v", err)
	}
	if _, err := database.sqlDB.Exec(`UPDATE schema_version SET version = 23`); err != nil {
		t.Fatalf("failed to roll back schema_version: %v", err)
	}

	if err := database.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := database.sqlDB.Exec(`SELECT hibernated_at FROM tasks LIMIT 1`); err != nil {
		t.Errorf("expected migrateV24 to restore tasks.hibernated_at: %v", err)
	}
}

// SetTaskHibernatedAt round-trips through both the single-task read and the
// list queries (the shared taskColumns / scanTaskRow pair).
func TestSetTaskHibernatedAtRoundTrip(t *testing.T) {
	database := openTestDB(t)
	tk := newHibernationTask(t, database)

	if got, err := database.GetTask(tk.ID); err != nil {
		t.Fatalf("GetTask: %v", err)
	} else if got.HibernatedAt != nil {
		t.Fatalf("a new task must not be hibernated, got %v", got.HibernatedAt)
	}

	at := time.Now().Truncate(time.Second)
	if err := database.SetTaskHibernatedAt(tk.ID, &at); err != nil {
		t.Fatalf("SetTaskHibernatedAt: %v", err)
	}

	got, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.HibernatedAt == nil || !got.HibernatedAt.Equal(at) {
		t.Errorf("GetTask HibernatedAt = %v, want %v", got.HibernatedAt, at)
	}
	if got.Status != task.StatusTmux {
		t.Errorf("hibernation must not change status, got %s", got.Status)
	}

	all, err := database.GetAllTasks()
	if err != nil {
		t.Fatalf("GetAllTasks: %v", err)
	}
	found := false
	for _, listed := range all {
		if listed.ID != tk.ID {
			continue
		}
		found = true
		if listed.HibernatedAt == nil || !listed.HibernatedAt.Equal(at) {
			t.Errorf("GetAllTasks HibernatedAt = %v, want %v", listed.HibernatedAt, at)
		}
	}
	if !found {
		t.Error("task missing from GetAllTasks")
	}

	if err := database.SetTaskHibernatedAt(tk.ID, nil); err != nil {
		t.Fatalf("SetTaskHibernatedAt(nil): %v", err)
	}
	if got, err := database.GetTask(tk.ID); err != nil {
		t.Fatalf("GetTask: %v", err)
	} else if got.HibernatedAt != nil {
		t.Errorf("expected the marker cleared, got %v", got.HibernatedAt)
	}
}

// Leaving tmux status by ANY route drops the marker, so a woken/advanced/
// retried/failed task can never render as hibernated.
func TestStatusChangeClearsHibernatedAt(t *testing.T) {
	routes := map[string]func(*DB, int64) error{
		"UpdateTaskStatus running":   func(d *DB, id int64) error { return d.UpdateTaskStatus(id, task.StatusRunning) },
		"UpdateTaskStatus completed": func(d *DB, id int64) error { return d.UpdateTaskStatus(id, task.StatusCompleted) },
		"UpdateTaskStatus failed":    func(d *DB, id int64) error { return d.UpdateTaskStatus(id, task.StatusFailed) },
		"UpdateTaskStatus pending":   func(d *DB, id int64) error { return d.UpdateTaskStatus(id, task.StatusPending) },
		"UpdateTaskError":            func(d *DB, id int64) error { return d.UpdateTaskError(id, "boom") },
		"UpdateTaskStatusError":      func(d *DB, id int64) error { return d.UpdateTaskStatusError(id, task.StatusMergeFailed, "boom") },
		"ResetTaskForRetry":          func(d *DB, id int64) error { return d.ResetTaskForRetry(id) },
		"ResetTaskForRetryFromStep":  func(d *DB, id int64) error { return d.ResetTaskForRetryFromStep(id) },
		"ResetTaskForRetryAtStep":    func(d *DB, id int64) error { return d.ResetTaskForRetryAtStep(id, 0, nil) },
		"ResetTaskForContinue":       func(d *DB, id int64) error { return d.ResetTaskForContinue(id, "default", "") },
		"ResetTaskForContinue prompt": func(d *DB, id int64) error {
			return d.ResetTaskForContinue(id, "default", "again")
		},
	}
	for name, route := range routes {
		t.Run(name, func(t *testing.T) {
			database := openTestDB(t)
			tk := newHibernationTask(t, database)

			at := time.Now()
			if err := database.SetTaskHibernatedAt(tk.ID, &at); err != nil {
				t.Fatalf("SetTaskHibernatedAt: %v", err)
			}
			if err := route(database, tk.ID); err != nil {
				t.Fatalf("%s: %v", name, err)
			}

			got, err := database.GetTask(tk.ID)
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}
			if got.HibernatedAt != nil {
				t.Errorf("%s must clear hibernated_at, got %v", name, got.HibernatedAt)
			}
		})
	}
}

// The ad-hoc chat lookup must return the most recent continue/tmux-direct row
// and never a row belonging to a workflow step.
func TestGetLatestChatForSteps(t *testing.T) {
	database := openTestDB(t)
	tk := newHibernationTask(t, database)

	if err := database.UpsertChat(tk.ID, "implement", "step-session", ""); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}

	got, err := database.GetLatestChatForSteps(tk.ID, []string{"continue", "tmux-direct"})
	if err != nil {
		t.Fatalf("GetLatestChatForSteps: %v", err)
	}
	if got != nil {
		t.Fatalf("a step chat must not satisfy an ad-hoc lookup, got %+v", got)
	}

	if err := database.UpsertChat(tk.ID, "continue", "continue-session", ""); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	got, err = database.GetLatestChatForSteps(tk.ID, []string{"continue", "tmux-direct"})
	if err != nil {
		t.Fatalf("GetLatestChatForSteps: %v", err)
	}
	if got == nil || got.SessionID != "continue-session" || got.StepName != "continue" {
		t.Fatalf("GetLatestChatForSteps = %+v, want the continue row", got)
	}
}
