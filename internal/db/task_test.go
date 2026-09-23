package db

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/task"
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
	if err := database.FinalizeTaskIdentity(created.ID, "Tmux task", "tmux-task", "sakusen/tmux-task"); err != nil {
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
	if updated.Branch != "sakusen/tmux-task" {
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

// TestStartedAtIsWriteOnce pins that re-entering the running status does not
// move started_at. markSummarizingStep flips a task to a summarizing status
// and back to running after EVERY per-step summarization pass, so an
// unconditional write made a long task report the duration of its last
// summarizer round trip instead of its real runtime.
func TestStartedAtIsWriteOnce(t *testing.T) {
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
	created, err := database.CreateTask(proj.ID, "Loop", "Loop task", "loop", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}

	claimed, err := database.ClaimTask(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("expected to claim a pending task")
	}
	afterClaim, err := database.GetTask(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterClaim.StartedAt == nil {
		t.Fatal("ClaimTask must set started_at")
	}
	start := *afterClaim.StartedAt

	// One step-summarization round trip: running -> summarizing_step -> running.
	for i := 0; i < 3; i++ {
		if err := database.UpdateTaskStatus(created.ID, task.StatusSummarizingStep); err != nil {
			t.Fatal(err)
		}
		if err := database.UpdateTaskStatus(created.ID, task.StatusRunning); err != nil {
			t.Fatal(err)
		}
	}

	after, err := database.GetTask(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.StartedAt == nil {
		t.Fatal("started_at was cleared")
	}
	if !after.StartedAt.Equal(start) {
		t.Errorf("started_at moved from %v to %v; it must be write-once", start, *after.StartedAt)
	}
}

// TestRetryRearmsStartedAt guards the other half of the write-once rule: a
// retry NULLs started_at so the next claim records a fresh start.
func TestRetryRearmsStartedAt(t *testing.T) {
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
	created, err := database.CreateTask(proj.ID, "Retry", "Retry task", "retry", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ClaimTask(created.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.ResetTaskForRetry(created.ID); err != nil {
		t.Fatal(err)
	}

	afterRetry, err := database.GetTask(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRetry.StartedAt != nil {
		t.Fatalf("ResetTaskForRetry must clear started_at, got %v", *afterRetry.StartedAt)
	}

	claimed, err := database.ClaimTask(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("expected to re-claim the retried task")
	}
	afterReclaim, err := database.GetTask(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterReclaim.StartedAt == nil {
		t.Error("re-claiming a retried task must set a fresh started_at")
	}
}

// newDepTestDB opens a throwaway DB with one project for the
// FailDependentTasks cases below.
func newDepTestDB(t *testing.T) (*DB, int64) {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	proj, err := database.GetOrCreateProject("/home/user/deps")
	if err != nil {
		t.Fatal(err)
	}
	return database, proj.ID
}

func mustCreateTask(t *testing.T, database *DB, projID int64, title string, status task.Status) *task.Task {
	t.Helper()
	tk, err := database.CreateTask(projID, title, title, task.Slugify(title), "", "", status, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func mustStatus(t *testing.T, database *DB, id int64) task.Status {
	t.Helper()
	tk, err := database.GetTask(id)
	if err != nil {
		t.Fatal(err)
	}
	return tk.Status
}

// A dependent of a task that just failed can never be claimed, so it is
// failed too with a reason naming the blocker.
func TestFailDependentTasks_SingleLevel(t *testing.T) {
	database, projID := newDepTestDB(t)

	blocker := mustCreateTask(t, database, projID, "blocker", task.StatusFailed)
	dep := mustCreateTask(t, database, projID, "dependent", task.StatusPending)
	unrelated := mustCreateTask(t, database, projID, "unrelated", task.StatusPending)
	if err := database.AddTaskDependency(dep.ID, blocker.ID); err != nil {
		t.Fatal(err)
	}

	failed, err := database.FailDependentTasks(blocker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 || failed[0] != dep.ID {
		t.Fatalf("expected only task #%d to be failed, got %v", dep.ID, failed)
	}
	if got := mustStatus(t, database, dep.ID); got != task.StatusFailed {
		t.Errorf("dependent status: got %s, want %s", got, task.StatusFailed)
	}
	if got := mustStatus(t, database, unrelated.ID); got != task.StatusPending {
		t.Errorf("unrelated task should be untouched, got %s", got)
	}

	updated, err := database.GetTask(dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("blocker task #%d failed", blocker.ID)
	if updated.ErrorMessage != want {
		t.Errorf("status reason: got %q, want %q", updated.ErrorMessage, want)
	}
	if updated.CompletedAt == nil {
		t.Error("cascaded failure should stamp completed_at like any other terminal transition")
	}
}

// The cascade is transitive: a dependent of a dependent is stranded just as
// badly, so it fails too, naming its own immediate blocker.
func TestFailDependentTasks_Transitive(t *testing.T) {
	database, projID := newDepTestDB(t)

	blocker := mustCreateTask(t, database, projID, "blocker", task.StatusFailed)
	mid := mustCreateTask(t, database, projID, "mid", task.StatusPending)
	leaf := mustCreateTask(t, database, projID, "leaf", task.StatusInit)
	if err := database.AddTaskDependency(mid.ID, blocker.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.AddTaskDependency(leaf.ID, mid.ID); err != nil {
		t.Fatal(err)
	}

	failed, err := database.FailDependentTasks(blocker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 2 || failed[0] != mid.ID || failed[1] != leaf.ID {
		t.Fatalf("expected [%d %d] in visit order, got %v", mid.ID, leaf.ID, failed)
	}
	for _, id := range []int64{mid.ID, leaf.ID} {
		if got := mustStatus(t, database, id); got != task.StatusFailed {
			t.Errorf("task #%d status: got %s, want %s", id, got, task.StatusFailed)
		}
	}

	leafRow, err := database.GetTask(leaf.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Each level names its own immediate blocker, so the reasons chain.
	if want := fmt.Sprintf("blocker task #%d failed", mid.ID); leafRow.ErrorMessage != want {
		t.Errorf("leaf status reason: got %q, want %q", leafRow.ErrorMessage, want)
	}
}

// A dependency cycle (which HasCircularDependency blocks on insert, but which
// an older row could still carry) must terminate rather than loop forever.
func TestFailDependentTasks_CycleTerminates(t *testing.T) {
	database, projID := newDepTestDB(t)

	blocker := mustCreateTask(t, database, projID, "blocker", task.StatusFailed)
	a := mustCreateTask(t, database, projID, "a", task.StatusPending)
	b := mustCreateTask(t, database, projID, "b", task.StatusPending)
	// a ← blocker, and a ↔ b block each other.
	for _, edge := range [][2]int64{{a.ID, blocker.ID}, {b.ID, a.ID}, {a.ID, b.ID}} {
		if err := database.AddTaskDependency(edge[0], edge[1]); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan []int64, 1)
	go func() {
		failed, err := database.FailDependentTasks(blocker.ID)
		if err != nil {
			t.Error(err)
		}
		done <- failed
	}()

	select {
	case failed := <-done:
		if len(failed) != 2 {
			t.Fatalf("expected both cycle members failed exactly once, got %v", failed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("FailDependentTasks did not terminate on a dependency cycle")
	}

	for _, id := range []int64{a.ID, b.ID} {
		if got := mustStatus(t, database, id); got != task.StatusFailed {
			t.Errorf("task #%d status: got %s, want %s", id, got, task.StatusFailed)
		}
	}
}

// merge-failed is user-recoverable — a retry can land the merge and flip it to
// `completed`, unblocking dependents normally — so it must not cascade.
// Already-terminal and already-started dependents are likewise left alone.
func TestFailDependentTasks_SkipsNonStrandedDependents(t *testing.T) {
	database, projID := newDepTestDB(t)

	mergeFailed := mustCreateTask(t, database, projID, "merge failed", task.StatusMergeFailed)
	blockedByMergeFailed := mustCreateTask(t, database, projID, "waiting on merge", task.StatusPending)
	if err := database.AddTaskDependency(blockedByMergeFailed.ID, mergeFailed.ID); err != nil {
		t.Fatal(err)
	}
	if failed, err := database.FailDependentTasks(mergeFailed.ID); err != nil {
		t.Fatal(err)
	} else if len(failed) != 0 {
		t.Fatalf("merge-failed must not cascade, got %v", failed)
	}
	if got := mustStatus(t, database, blockedByMergeFailed.ID); got != task.StatusPending {
		t.Errorf("dependent of merge-failed: got %s, want %s", got, task.StatusPending)
	}

	blocker := mustCreateTask(t, database, projID, "blocker", task.StatusFailed)
	running := mustCreateTask(t, database, projID, "already running", task.StatusRunning)
	completed := mustCreateTask(t, database, projID, "already completed", task.StatusCompleted)
	for _, id := range []int64{running.ID, completed.ID} {
		if err := database.AddTaskDependency(id, blocker.ID); err != nil {
			t.Fatal(err)
		}
	}
	if failed, err := database.FailDependentTasks(blocker.ID); err != nil {
		t.Fatal(err)
	} else if len(failed) != 0 {
		t.Fatalf("started/terminal dependents must not cascade, got %v", failed)
	}
	if got := mustStatus(t, database, running.ID); got != task.StatusRunning {
		t.Errorf("running dependent: got %s, want %s", got, task.StatusRunning)
	}
	if got := mustStatus(t, database, completed.ID); got != task.StatusCompleted {
		t.Errorf("completed dependent: got %s, want %s", got, task.StatusCompleted)
	}
}

// GetClaimableTasks is what strands a dependent in the first place: the
// cascade must take it out of the claimable set entirely.
func TestFailDependentTasks_RemovesFromClaimable(t *testing.T) {
	database, projID := newDepTestDB(t)

	blocker := mustCreateTask(t, database, projID, "blocker", task.StatusFailed)
	dep := mustCreateTask(t, database, projID, "dependent", task.StatusPending)
	if err := database.AddTaskDependency(dep.ID, blocker.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := database.FailDependentTasks(blocker.ID); err != nil {
		t.Fatal(err)
	}
	claimable, err := database.GetClaimableTasks()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range claimable {
		if c.ID == dep.ID {
			t.Fatalf("task #%d should no longer be claimable after its blocker failed", dep.ID)
		}
	}
}
