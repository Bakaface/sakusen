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
	"github.com/Bakaface/sakusen/internal/task"
)

// TestRecoverOrphanedTasks_FinalizingRestartsAgent verifies that tasks killed
// during finalization or while merge-blocked are re-entered via startTaskAgent
// (not silently demoted to StatusTmux, which lost the in-flight merge), and
// that recovery never touches uncommitted user edits in the main checkout —
// a merge-blocked task is blocked precisely because those edits exist.
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

	writeUserEdits(t, repoDir)

	// Step index past the only step so the recovery RunTask invocation
	// no-ops cleanly without invoking claude.
	var ids []int64
	for _, status := range []task.Status{task.StatusFinalizing, task.StatusMergeBlocked} {
		tk, err := database.CreateTaskWithPriority(
			proj.ID, "Test task "+string(status), "desc", "slug-"+string(status), "default", "", "branch-"+string(status), "", "",
			status, task.PriorityMedium, false, nil, nil,
		)
		if err != nil {
			t.Fatalf("failed to create task: %v", err)
		}
		if err := database.UpdateTaskStep(tk.ID, 1, ""); err != nil {
			t.Fatalf("failed to set step index past last step: %v", err)
		}
		ids = append(ids, tk.ID)
	}

	if err := s.recoverOrphanedTasks(); err != nil {
		t.Fatalf("recoverOrphanedTasks failed: %v", err)
	}

	for _, id := range ids {
		refreshed, err := database.GetTask(id)
		if err != nil {
			t.Fatalf("failed to get task: %v", err)
		}
		if refreshed.Status == task.StatusTmux {
			t.Fatalf("task #%d demoted to tmux on recovery; status=%s", id, refreshed.Status)
		}
	}

	assertUserEditsIntact(t, repoDir)
}

// writeUserEdits leaves a staged new file and an unstaged modification of a
// tracked file in the repo root, simulating a human editing the main checkout.
func writeUserEdits(t *testing.T, repoDir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repoDir, "staged.txt"), []byte("user staged"), 0644); err != nil {
		t.Fatal(err)
	}
	stage := exec.Command("git", "add", "staged.txt")
	stage.Dir = repoDir
	if out, err := stage.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("user unstaged"), 0644); err != nil {
		t.Fatal(err)
	}
}

// assertUserEditsIntact checks that the edits from writeUserEdits survived.
func assertUserEditsIntact(t *testing.T, repoDir string) {
	t.Helper()
	for file, want := range map[string]string{"staged.txt": "user staged", "README.md": "user unstaged"} {
		got, err := os.ReadFile(filepath.Join(repoDir, file))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q (err %v), want %q", file, got, err, want)
		}
	}
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if got, want := string(out), " M README.md\nA  staged.txt\n"; got != want {
		t.Errorf("git status = %q, want %q", got, want)
	}
}

// TestRecoverOrphanedTasks_MergeBlockedKeepsDirtyRepo verifies that the
// startup sweep does not hard-reset a repo whose only flagged task was
// merge-blocked: that task never touched the base repo, and the dirty state is
// someone else's uncommitted work on the main checkout.
func TestRecoverOrphanedTasks_MergeBlockedKeepsDirtyRepo(t *testing.T) {
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
	defer s.manager.Shutdown(2 * time.Second)
	defer s.cancel()

	proj, err := database.GetOrCreateProject(repoDir)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	if _, err := s.getProjectContext(proj.ID); err != nil {
		t.Fatalf("failed to pre-load project context: %v", err)
	}

	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("user edit"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "scratch.txt"), []byte("scratch"), 0644); err != nil {
		t.Fatal(err)
	}

	tk, err := database.CreateTaskWithPriority(
		proj.ID, "Test task", "desc", "slug", "default", "", "branch", "", "",
		task.StatusMergeBlocked, task.PriorityMedium, false, nil, nil,
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

	if got, _ := os.ReadFile(filepath.Join(repoDir, "README.md")); string(got) != "user edit" {
		t.Errorf("modified README.md was reset by the startup sweep: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(repoDir, "scratch.txt")); string(got) != "scratch" {
		t.Errorf("untracked scratch.txt was removed by the startup sweep: %q", got)
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
			task.StatusRunning, task.PriorityMedium, false, nil, nil,
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
