package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/db"
	gitpkg "github.com/Bakaface/sortie/internal/git"
	"github.com/Bakaface/sortie/internal/task"
)

// TestRecoverOrphanedTasks_FinalizingRestartsAgent verifies the two coupled
// fixes for the merge-recovery bug:
//
//  1. Tasks killed during finalization (status=finalizing — the state used
//     while the merge coordinator is running, including mid-conflict
//     resolution) must NOT be silently demoted to StatusTmux. Previously the
//     demotion lost the in-flight merge entirely.
//  2. The repo-cleanup pass must cover Finalizing tasks. The deferred
//     CleanRepoState in merge.Coordinator does not fire on process kill, so
//     a half-merged base branch needs to be reset before any recovery agent
//     touches the repo.
func TestRecoverOrphanedTasks_FinalizingRestartsAgent(t *testing.T) {
	repoDir := initRecoveryTestRepo(t)

	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	cfg := &config.Config{
		OnComplete: "none",
		Workflows: []config.WorkflowConfig{
			{Name: "default", Steps: []config.StepConfig{{Name: "implement", Prompt: "do something"}}},
		},
	}
	s := NewServer(cfg, database)
	// Drain the recovery agent goroutine before closing the DB so the
	// engine's worktree-path persistence call doesn't race teardown.
	defer s.manager.Shutdown(2 * time.Second)
	defer s.cancel()

	proj, err := database.GetOrCreateProject(repoDir)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Pre-load project context so startTaskAgent can resolve the engine.
	if _, err := s.getProjectContext(proj.ID); err != nil {
		t.Fatalf("failed to pre-load project context: %v", err)
	}

	// Leave a staged change in the repo to simulate a half-merged base branch
	// from an interrupted merge commit.
	if err := os.WriteFile(filepath.Join(repoDir, "leftover.txt"), []byte("partial merge"), 0644); err != nil {
		t.Fatalf("failed to write leftover file: %v", err)
	}
	stage := exec.Command("git", "add", "leftover.txt")
	stage.Dir = repoDir
	if out, err := stage.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	if dirty, err := gitpkg.NewRepo(repoDir).HasChanges(); err != nil || !dirty {
		t.Fatalf("expected dirty repo before recovery (dirty=%v err=%v)", dirty, err)
	}

	// Simulate the post-merge-conflict-killed state: status=Finalizing with
	// step_index past the only step so the recovery RunTask invocation
	// no-ops cleanly without invoking claude.
	tk, err := database.CreateTaskWithPriority(
		proj.ID, "Test task", "desc", "slug", "default", "", "branch", "", "",
		task.StatusFinalizing, task.PriorityMedium, false, nil, nil,
	)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	if err := database.UpdateTaskStep(tk.ID, 1, ""); err != nil {
		t.Fatalf("failed to set step index past last step: %v", err)
	}

	if err := s.recoverOrphanedTasks(); err != nil {
		t.Fatalf("recoverOrphanedTasks failed: %v", err)
	}

	// Fix #1: the Finalizing→Tmux demotion bug would have synchronously set
	// status=tmux. The fix routes the task through startTaskAgent instead.
	refreshed, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if refreshed.Status == task.StatusTmux {
		t.Fatalf("Finalizing task demoted to tmux on recovery (the bug); status=%s", refreshed.Status)
	}

	// Fix #2: the repo cleanup pass must have run for the Finalizing task's
	// project. Previously it only iterated GetRunningTasks() and missed
	// repos whose only mid-flight task was Finalizing or MergeBlocked.
	if dirty, err := gitpkg.NewRepo(repoDir).HasChanges(); err != nil {
		t.Fatalf("failed to check repo state: %v", err)
	} else if dirty {
		t.Errorf("expected repo cleanup to reset staged changes for Finalizing task; repo still dirty")
	}
}

// TestRecoverOrphanedTasks_SummarizingRestartsAgent verifies that a task killed
// during summarization (which happens AFTER the merge completed) is also
// recovered via startTaskAgent rather than ResetTaskForRetry. The old
// ResetTaskForRetry behavior wiped step_index and re-ran the entire workflow
// from scratch — including any tmux step — which is wrong when the merge has
// already happened.
func TestRecoverOrphanedTasks_SummarizingRestartsAgent(t *testing.T) {
	repoDir := initRecoveryTestRepo(t)

	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	cfg := &config.Config{
		OnComplete: "none",
		Workflows: []config.WorkflowConfig{
			{Name: "default", Steps: []config.StepConfig{{Name: "implement", Prompt: "do something"}}},
		},
	}
	s := NewServer(cfg, database)
	// Drain the recovery agent goroutine before closing the DB so the
	// engine's worktree-path persistence call doesn't race teardown.
	defer s.manager.Shutdown(2 * time.Second)
	defer s.cancel()

	proj, err := database.GetOrCreateProject(repoDir)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	if _, err := s.getProjectContext(proj.ID); err != nil {
		t.Fatalf("failed to pre-load project context: %v", err)
	}

	tk, err := database.CreateTaskWithPriority(
		proj.ID, "Test task", "desc", "slug", "default", "", "branch", "", "",
		task.StatusSummarizing, task.PriorityMedium, false, nil, nil,
	)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	// Mid-workflow step_index — the old ResetTaskForRetry path would have
	// wiped this to 0, re-running every step including the tmux implement.
	if err := database.UpdateTaskStep(tk.ID, 1, ""); err != nil {
		t.Fatalf("failed to set step index: %v", err)
	}

	if err := s.recoverOrphanedTasks(); err != nil {
		t.Fatalf("recoverOrphanedTasks failed: %v", err)
	}

	refreshed, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if refreshed.Status == task.StatusPending {
		t.Fatalf("Summarizing task was reset to pending (the bug — re-runs whole workflow); status=%s", refreshed.Status)
	}
	if refreshed.StepIndex != 1 {
		t.Errorf("step_index was wiped on recovery (the bug); got %d, want 1", refreshed.StepIndex)
	}
}

// initRecoveryTestRepo creates a git repo on the `main` branch with one commit.
func initRecoveryTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init", "-q"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "checkout", "-q", "-b", "main"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v failed: %v\n%s", args, err, out)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "-A"},
		{"git", "commit", "-q", "-m", "initial commit"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v failed: %v\n%s", args, err, out)
		}
	}

	return dir
}
