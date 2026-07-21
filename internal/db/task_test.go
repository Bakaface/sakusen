package db

import (
	"path/filepath"
	"testing"

	"github.com/Bakaface/sortie/internal/task"
)

func TestGetAllTasks_SortedDescending(t *testing.T) {
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

	// Create tasks in ascending order
	_, err = database.CreateTask(proj.ID, "First", "First task", "first", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.CreateTask(proj.ID, "Second", "Second task", "second", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.CreateTask(proj.ID, "Third", "Third task", "third", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}

	tasks, err := database.GetAllTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}

	// Tasks should be sorted by ID descending (newest first)
	for i := 1; i < len(tasks); i++ {
		if tasks[i-1].ID <= tasks[i].ID {
			t.Errorf("expected descending order: task[%d].ID=%d should be > task[%d].ID=%d",
				i-1, tasks[i-1].ID, i, tasks[i].ID)
		}
	}
}

func TestGetAllTasks_TmuxTasksSortedFirst(t *testing.T) {
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

	// Create a mix of pending and tmux tasks
	pending1, err := database.CreateTask(proj.ID, "Pending 1", "desc", "pending-1", "", "", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}
	tmux1, err := database.CreateTask(proj.ID, "Tmux 1", "desc", "tmux-1", "", "", task.StatusTmux, nil)
	if err != nil {
		t.Fatal(err)
	}
	pending2, err := database.CreateTask(proj.ID, "Pending 2", "desc", "pending-2", "", "", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}
	tmux2, err := database.CreateTask(proj.ID, "Tmux 2", "desc", "tmux-2", "", "", task.StatusTmux, nil)
	if err != nil {
		t.Fatal(err)
	}

	tasks, err := database.GetAllTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 4 {
		t.Fatalf("expected 4 tasks, got %d", len(tasks))
	}

	// Tmux tasks should come first (sorted by ID desc within group), then non-tmux (sorted by ID desc)
	if tasks[0].ID != tmux2.ID {
		t.Errorf("expected first task to be tmux2 (ID=%d), got ID=%d", tmux2.ID, tasks[0].ID)
	}
	if tasks[1].ID != tmux1.ID {
		t.Errorf("expected second task to be tmux1 (ID=%d), got ID=%d", tmux1.ID, tasks[1].ID)
	}
	if tasks[2].ID != pending2.ID {
		t.Errorf("expected third task to be pending2 (ID=%d), got ID=%d", pending2.ID, tasks[2].ID)
	}
	if tasks[3].ID != pending1.ID {
		t.Errorf("expected fourth task to be pending1 (ID=%d), got ID=%d", pending1.ID, tasks[3].ID)
	}
}

func TestGetTasksByProject_TmuxTasksSortedFirst(t *testing.T) {
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

	pending1, err := database.CreateTask(proj.ID, "Pending 1", "desc", "pending-1", "", "", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}
	tmux1, err := database.CreateTask(proj.ID, "Tmux 1", "desc", "tmux-1", "", "", task.StatusTmux, nil)
	if err != nil {
		t.Fatal(err)
	}

	tasks, err := database.GetTasksByProject(proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}

	// Tmux task should be first
	if tasks[0].ID != tmux1.ID {
		t.Errorf("expected first task to be tmux (ID=%d), got ID=%d", tmux1.ID, tasks[0].ID)
	}
	if tasks[1].ID != pending1.ID {
		t.Errorf("expected second task to be pending (ID=%d), got ID=%d", pending1.ID, tasks[1].ID)
	}
}

func TestGetTasksByProject_SortedDescending(t *testing.T) {
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

	// Create tasks in ascending order
	_, err = database.CreateTask(proj.ID, "First", "First task", "first", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.CreateTask(proj.ID, "Second", "Second task", "second", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.CreateTask(proj.ID, "Third", "Third task", "third", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}

	tasks, err := database.GetTasksByProject(proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}

	// Tasks should be sorted by ID descending (newest first)
	for i := 1; i < len(tasks); i++ {
		if tasks[i-1].ID <= tasks[i].ID {
			t.Errorf("expected descending order: task[%d].ID=%d should be > task[%d].ID=%d",
				i-1, tasks[i-1].ID, i, tasks[i].ID)
		}
	}
}

func TestCreateTaskWithPriority(t *testing.T) {
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

	// Create task with explicit priority
	created, err := database.CreateTaskWithPriority(proj.ID, "Urgent task", "Do it now", "urgent-task", "", "", "", "", "", "pending", task.PriorityUrgent, true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if created.Priority != task.PriorityUrgent {
		t.Errorf("expected priority 'urgent', got %q", created.Priority)
	}

	// Create task with default priority (via CreateTask)
	defaultTask, err := database.CreateTask(proj.ID, "Default task", "Do it", "default-task", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}

	if defaultTask.Priority != task.PriorityMedium {
		t.Errorf("expected default priority 'medium', got %q", defaultTask.Priority)
	}
}

func TestUpdateTaskPriority(t *testing.T) {
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

	created, err := database.CreateTask(proj.ID, "Task", "Description", "task-slug", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}

	if created.Priority != task.PriorityMedium {
		t.Errorf("expected initial priority 'medium', got %q", created.Priority)
	}

	// Update priority
	if err := database.UpdateTaskPriority(created.ID, task.PriorityHigh); err != nil {
		t.Fatal(err)
	}

	updated, err := database.GetTask(created.ID)
	if err != nil {
		t.Fatal(err)
	}

	if updated.Priority != task.PriorityHigh {
		t.Errorf("expected priority 'high', got %q", updated.Priority)
	}
}

func TestUpdateTaskTitle(t *testing.T) {
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

	created, err := database.CreateTask(proj.ID, "Original Title", "Description", "task-slug", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}

	if created.Title != "Original Title" {
		t.Errorf("expected initial title 'Original Title', got %q", created.Title)
	}

	// Update title
	if err := database.UpdateTaskTitle(created.ID, "Updated Title"); err != nil {
		t.Fatal(err)
	}

	updated, err := database.GetTask(created.ID)
	if err != nil {
		t.Fatal(err)
	}

	if updated.Title != "Updated Title" {
		t.Errorf("expected title 'Updated Title', got %q", updated.Title)
	}
}

func TestGetClaimableTasks_SortedByPriority(t *testing.T) {
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

	// Create tasks with different priorities
	_, err = database.CreateTaskWithPriority(proj.ID, "Low task", "Low", "low-task", "", "", "", "", "", "pending", task.PriorityLow, true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.CreateTaskWithPriority(proj.ID, "High task", "High", "high-task", "", "", "", "", "", "pending", task.PriorityHigh, true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.CreateTaskWithPriority(proj.ID, "Urgent task", "Urgent", "urgent-task", "", "", "", "", "", "pending", task.PriorityUrgent, true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.CreateTaskWithPriority(proj.ID, "Medium task", "Medium", "medium-task", "", "", "", "", "", "pending", task.PriorityMedium, true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	tasks, err := database.GetClaimableTasks()
	if err != nil {
		t.Fatal(err)
	}

	if len(tasks) != 4 {
		t.Fatalf("expected 4 claimable tasks, got %d", len(tasks))
	}

	// Should be sorted by priority descending: urgent, high, medium, low
	expectedOrder := []task.Priority{task.PriorityUrgent, task.PriorityHigh, task.PriorityMedium, task.PriorityLow}
	for i, expected := range expectedOrder {
		if tasks[i].Priority != expected {
			t.Errorf("task[%d]: expected priority %q, got %q (title: %s)", i, expected, tasks[i].Priority, tasks[i].Title)
		}
	}
}

func TestCreateTaskWithBranchName(t *testing.T) {
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

	// Create task with a branch name template
	created, err := database.CreateTaskWithPriority(
		proj.ID, "Task with branch", "Description", "task-slug", "", "feature/{{task.title}}", "", "", "",
		task.StatusPending, task.PriorityMedium, true, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	if created.BranchName != "feature/{{task.title}}" {
		t.Errorf("expected branch_name 'feature/{{task.title}}', got %q", created.BranchName)
	}

	// Verify round-trip via GetTask
	fetched, err := database.GetTask(created.ID)
	if err != nil {
		t.Fatal(err)
	}

	if fetched.BranchName != "feature/{{task.title}}" {
		t.Errorf("expected branch_name 'feature/{{task.title}}' after fetch, got %q", fetched.BranchName)
	}

	// Create task without branch name
	defaultTask, err := database.CreateTask(proj.ID, "Default", "Desc", "default", "", "", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}

	if defaultTask.BranchName != "" {
		t.Errorf("expected empty branch_name for default task, got %q", defaultTask.BranchName)
	}
}

func TestCreateTaskWithWorktreeDisabled(t *testing.T) {
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

	// Create task with worktree disabled
	created, err := database.CreateTaskWithPriority(
		proj.ID, "No worktree task", "Run in current dir", "no-wt-task", "", "", "", "", "",
		task.StatusPending, task.PriorityMedium, false, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	if created.Worktree {
		t.Error("expected worktree to be false")
	}

	// Verify round-trip via GetTask
	fetched, err := database.GetTask(created.ID)
	if err != nil {
		t.Fatal(err)
	}

	if fetched.Worktree {
		t.Error("expected worktree to be false after fetch")
	}

	// Create task with worktree enabled (default)
	withWt, err := database.CreateTaskWithPriority(
		proj.ID, "Worktree task", "Run in worktree", "wt-task", "", "", "", "", "",
		task.StatusPending, task.PriorityMedium, true, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	if !withWt.Worktree {
		t.Error("expected worktree to be true")
	}

	// Verify CreateTask defaults to worktree=true
	defaultTask, err := database.CreateTask(proj.ID, "Default", "Desc", "default", "", "", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}

	if !defaultTask.Worktree {
		t.Error("expected default worktree to be true")
	}
}

func TestResetTaskForRetryPreservesWorktree(t *testing.T) {
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

	// Create a tmux task with worktree and branch set
	created, err := database.CreateTask(proj.ID, "Tmux task", "Description", "tmux-task", "", "", task.StatusTmux, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Set worktree path and branch
	if err := database.UpdateTaskWorktreePath(created.ID, "/tmp/worktree/tmux-task"); err != nil {
		t.Fatal(err)
	}
	if err := database.FinalizeTaskIdentity(created.ID, "Tmux task", "tmux-task", "sortie/tmux-task"); err != nil {
		t.Fatal(err)
	}

	// Reset for retry
	if err := database.ResetTaskForRetry(created.ID); err != nil {
		t.Fatal(err)
	}

	updated, err := database.GetTask(created.ID)
	if err != nil {
		t.Fatal(err)
	}

	if updated.Status != task.StatusPending {
		t.Errorf("expected status 'pending', got %q", updated.Status)
	}
	if updated.StepIndex != 0 {
		t.Errorf("expected step_index 0, got %d", updated.StepIndex)
	}
	// worktree_path and branch should be preserved
	if updated.WorktreePath != "/tmp/worktree/tmux-task" {
		t.Errorf("expected worktree_path to be preserved, got %q", updated.WorktreePath)
	}
	if updated.Branch != "sortie/tmux-task" {
		t.Errorf("expected branch to be preserved, got %q", updated.Branch)
	}
}

func TestResetTaskForRetryFromStep(t *testing.T) {
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

	// Create a failed task with step_index = 2
	created, err := database.CreateTask(proj.ID, "Failed task", "Description", "failed-task", "", "", task.StatusFailed, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate task progression
	if err := database.UpdateTaskStep(created.ID, 2, "step2"); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateTaskExitCode(created.ID, 1, "error occurred"); err != nil {
		t.Fatal(err)
	}

	// Reset for retry from step (should preserve step_index)
	if err := database.ResetTaskForRetryFromStep(created.ID); err != nil {
		t.Fatal(err)
	}

	updated, err := database.GetTask(created.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Status should be pending
	if updated.Status != task.StatusPending {
		t.Errorf("expected status 'pending', got %q", updated.Status)
	}

	// step_index should be preserved (still 2)
	if updated.StepIndex != 2 {
		t.Errorf("expected step_index 2, got %d", updated.StepIndex)
	}

	// current_step should be cleared
	if updated.CurrentStep != "" {
		t.Errorf("expected current_step to be empty, got %q", updated.CurrentStep)
	}

	// exit_code should be cleared
	if updated.ExitCode != nil {
		t.Errorf("expected exit_code to be nil, got %v", *updated.ExitCode)
	}

	// error_message should be cleared
	if updated.ErrorMessage != "" {
		t.Errorf("expected error_message to be empty, got %q", updated.ErrorMessage)
	}

	// started_at and completed_at should be cleared
	if updated.StartedAt != nil {
		t.Errorf("expected started_at to be nil, got %v", *updated.StartedAt)
	}
	if updated.CompletedAt != nil {
		t.Errorf("expected completed_at to be nil, got %v", *updated.CompletedAt)
	}
}

// TestResetTaskForRetryAtStep verifies that the per-step retry reset moves
// the task back to a specified step while preserving step records and chats
// for earlier completed steps.
func TestResetTaskForRetryAtStep(t *testing.T) {
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

	created, err := database.CreateTask(proj.ID, "Failed task", "Description", "failed-task", "", "", task.StatusFailed, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Three completed steps + one chat per step
	for _, step := range []string{"plan", "implement", "review"} {
		if err := database.CreateTaskStep(created.ID, step); err != nil {
			t.Fatal(err)
		}
		ctx := "ctx-" + step
		if err := database.CompleteTaskStep(created.ID, step, &ctx, 0); err != nil {
			t.Fatal(err)
		}
		if err := database.UpsertChat(created.ID, step, "sid-"+step, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.UpdateTaskStep(created.ID, 2, "review"); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateTaskExitCode(created.ID, 1, "boom"); err != nil {
		t.Fatal(err)
	}

	// Retry from "implement" (index 1) — only implement and review records
	// should be removed; plan must be preserved.
	if err := database.ResetTaskForRetryAtStep(created.ID, 1, []string{"implement", "review"}); err != nil {
		t.Fatal(err)
	}

	updated, err := database.GetTask(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != task.StatusPending {
		t.Errorf("expected status pending, got %q", updated.Status)
	}
	if updated.StepIndex != 1 {
		t.Errorf("expected step_index 1, got %d", updated.StepIndex)
	}
	if updated.CurrentStep != "" {
		t.Errorf("expected current_step cleared, got %q", updated.CurrentStep)
	}
	if updated.ExitCode != nil {
		t.Errorf("expected exit_code cleared, got %v", *updated.ExitCode)
	}
	if updated.ErrorMessage != "" {
		t.Errorf("expected error_message cleared, got %q", updated.ErrorMessage)
	}

	// Plan step record + chat must be preserved
	ctxPlan, err := database.GetTaskStepContext(created.ID, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if ctxPlan != "ctx-plan" {
		t.Errorf("expected plan step context preserved, got %q", ctxPlan)
	}
	planChat, err := database.GetChatByStep(created.ID, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if planChat == nil || planChat.SessionID != "sid-plan" {
		t.Errorf("expected plan chat preserved, got %+v", planChat)
	}

	// Implement and review records must be deleted
	rows, err := database.GetTaskStepRows(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rows["implement"]; ok {
		t.Error("expected implement step row to be deleted")
	}
	if _, ok := rows["review"]; ok {
		t.Error("expected review step row to be deleted")
	}
	for _, deleted := range []string{"implement", "review"} {
		c, err := database.GetChatByStep(created.ID, deleted)
		if err != nil {
			t.Fatal(err)
		}
		if c != nil {
			t.Errorf("expected chat for %q to be deleted, got %+v", deleted, c)
		}
	}
}
