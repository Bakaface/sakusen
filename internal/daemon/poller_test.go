package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	gitpkg "github.com/Bakaface/sakusen/internal/git"
	"github.com/Bakaface/sakusen/internal/task"
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

// --- Cross-project resume --------------------------------------------------
//
// checkSuspendedParents never touches getProjectContext, so these need no git
// repo: they exercise the project-blind AllWaitsOnTerminal join directly.

// suspendedParentWithChildren builds a parent in projA at StatusAwaitingChildren
// with stepIndex, plus one child in projB per status, wired with wait-on edges.
func suspendedParentWithChildren(t *testing.T, s *Server, projA int64, stepIndex int, childStatuses ...task.Status) (*task.Task, []*task.Task) {
	t.Helper()
	projB, err := s.database.GetOrCreateProject("/tmp/sakusen-test-poller-other")
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}

	parent, err := s.database.CreateTaskWithPriority(
		projA, "parent", "parent work", "parent", "", "", "branch-parent", "", "",
		task.StatusRunning, task.PriorityMedium, false, nil, nil,
	)
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := s.database.UpdateTaskStep(parent.ID, stepIndex, ""); err != nil {
		t.Fatalf("set parent step index: %v", err)
	}
	if err := s.database.UpdateTaskStatus(parent.ID, task.StatusAwaitingChildren); err != nil {
		t.Fatalf("suspend parent: %v", err)
	}

	children := make([]*task.Task, 0, len(childStatuses))
	for i, st := range childStatuses {
		name := fmt.Sprintf("child-%d", i)
		c, err := s.database.CreateTaskWithPriority(
			projB.ID, name, "child work", name, "", "", "branch-"+name, "", "",
			task.StatusPending, task.PriorityMedium, false, nil, nil,
		)
		if err != nil {
			t.Fatalf("create child %d: %v", i, err)
		}
		if err := s.database.UpdateTaskStatus(c.ID, st); err != nil {
			t.Fatalf("set child %d status: %v", i, err)
		}
		if err := s.database.AddTaskWaitsOn(parent.ID, c.ID); err != nil {
			t.Fatalf("AddTaskWaitsOn child %d: %v", i, err)
		}
		children = append(children, c)
	}
	return parent, children
}

func TestCheckSuspendedParents_ResumesOnCrossProjectChildrenCompleted(t *testing.T) {
	s, projA := setupServerWithProject(t)
	parent, _ := suspendedParentWithChildren(t, s, projA, 1, task.StatusCompleted, task.StatusCompleted)

	s.checkSuspendedParents()

	got, err := s.database.GetTask(parent.ID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if got.Status != task.StatusPending {
		t.Errorf("parent status = %q, want pending", got.Status)
	}
	// Resume must re-run the SAME step, not advance past it.
	if got.StepIndex != 1 {
		t.Errorf("parent StepIndex = %d, want 1 (preserved for resume)", got.StepIndex)
	}
}

func TestCheckSuspendedParents_ResumesOnCrossProjectChildFailure(t *testing.T) {
	s, projA := setupServerWithProject(t)
	parent, children := suspendedParentWithChildren(t, s, projA, 1, task.StatusCompleted, task.StatusFailed)

	s.checkSuspendedParents()

	got, err := s.database.GetTask(parent.ID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if got.Status != task.StatusPending {
		t.Fatalf("parent status = %q, want pending (failed is terminal)", got.Status)
	}

	// The edges must survive the resume flip so loadAndClearChildren can
	// hydrate {{children.<id>.status}} on the re-run.
	hydrated, err := s.database.GetWaitsOnChildren(parent.ID)
	if err != nil {
		t.Fatalf("GetWaitsOnChildren: %v", err)
	}
	if len(hydrated) != 2 {
		t.Fatalf("got %d children for template hydration, want 2", len(hydrated))
	}
	byID := map[int64]task.Status{}
	for _, c := range hydrated {
		byID[c.ID] = c.Status
	}
	if byID[children[0].ID] != task.StatusCompleted {
		t.Errorf("child #%d status = %q, want completed", children[0].ID, byID[children[0].ID])
	}
	if byID[children[1].ID] != task.StatusFailed {
		t.Errorf("child #%d status = %q, want failed", children[1].ID, byID[children[1].ID])
	}
}

func TestCheckSuspendedParents_DoesNotResumeWhileCrossProjectChildRunning(t *testing.T) {
	s, projA := setupServerWithProject(t)
	parent, _ := suspendedParentWithChildren(t, s, projA, 1, task.StatusCompleted, task.StatusRunning)

	s.checkSuspendedParents()

	got, err := s.database.GetTask(parent.ID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if got.Status != task.StatusAwaitingChildren {
		t.Errorf("parent status = %q, want awaiting-children (one child still running)", got.Status)
	}
}
