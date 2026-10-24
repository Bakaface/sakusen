package db

import (
	"path/filepath"
	"testing"
)

func TestAppendTaskCommit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	proj, err := database.GetOrCreateProject("/home/user/myproject")
	if err != nil {
		t.Fatal(err)
	}

	task, err := database.CreateTask(proj.ID, "Task", "Description", "task-slug", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Initially no commits
	commits, err := database.GetTaskCommits(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 0 {
		t.Fatalf("expected 0 commits initially, got %d", len(commits))
	}

	// Append first commit
	if err := database.AppendTaskCommit(task.ID, "abc123"); err != nil {
		t.Fatal(err)
	}

	commits, err = database.GetTaskCommits(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(commits))
	}
	if commits[0] != "abc123" {
		t.Errorf("expected commit 'abc123', got %q", commits[0])
	}

	// Append second commit
	if err := database.AppendTaskCommit(task.ID, "def456"); err != nil {
		t.Fatal(err)
	}

	commits, err = database.GetTaskCommits(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 {
		t.Fatalf("expected 2 commits, got %d", len(commits))
	}
	if commits[0] != "abc123" {
		t.Errorf("expected first commit 'abc123', got %q", commits[0])
	}
	if commits[1] != "def456" {
		t.Errorf("expected second commit 'def456', got %q", commits[1])
	}
}

func TestCommitsPersistedInTaskScan(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	proj, err := database.GetOrCreateProject("/home/user/myproject")
	if err != nil {
		t.Fatal(err)
	}

	task, err := database.CreateTask(proj.ID, "Task", "Description", "task-slug", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := database.AppendTaskCommit(task.ID, "abc123"); err != nil {
		t.Fatal(err)
	}
	if err := database.AppendTaskCommit(task.ID, "def456"); err != nil {
		t.Fatal(err)
	}

	// Verify commits are returned via GetTask (scanTaskRow)
	retrieved, err := database.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(retrieved.Commits) != 2 {
		t.Fatalf("expected 2 commits on retrieved task, got %d", len(retrieved.Commits))
	}
	if retrieved.Commits[0] != "abc123" {
		t.Errorf("expected first commit 'abc123', got %q", retrieved.Commits[0])
	}
	if retrieved.Commits[1] != "def456" {
		t.Errorf("expected second commit 'def456', got %q", retrieved.Commits[1])
	}
}

func TestCommitsNilForNewTask(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	proj, err := database.GetOrCreateProject("/home/user/myproject")
	if err != nil {
		t.Fatal(err)
	}

	task, err := database.CreateTask(proj.ID, "Task", "Description", "task-slug", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(task.Commits) != 0 {
		t.Fatalf("expected no commits on new task, got %v", task.Commits)
	}
}

func TestCommitsPersistedInGetAllTasks(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	proj, err := database.GetOrCreateProject("/home/user/myproject")
	if err != nil {
		t.Fatal(err)
	}

	task, err := database.CreateTask(proj.ID, "Task", "Description", "task-slug", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := database.AppendTaskCommit(task.ID, "abc123"); err != nil {
		t.Fatal(err)
	}

	// Verify commits show up in GetAllTasks
	tasks, err := database.GetAllTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if len(tasks[0].Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(tasks[0].Commits))
	}
	if tasks[0].Commits[0] != "abc123" {
		t.Errorf("expected commit 'abc123', got %q", tasks[0].Commits[0])
	}
}
