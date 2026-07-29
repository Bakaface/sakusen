package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/db"
	"github.com/Bakaface/sortie/internal/task"
)

// runGit runs a git command in dir and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
	}
}

// writeFailingClaudeStub returns the path to an executable that stands in for
// the claude binary and always exits non-zero, so the merge pipeline's
// conflict resolver fails and the merge genuinely cannot land.
func writeFailingClaudeStub(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude-stub")
	script := "#!/bin/sh\necho 'stub conflict resolver refuses to resolve' >&2\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write claude stub: %v", err)
	}
	return path
}

// A finalization whose merge cannot land must NOT report the task as
// completed: `completed` means "the branch landed", so a poller reading
// TaskInfo would otherwise report a silent merge failure as success. The task
// must end in the distinct terminal merge-failed status, carry the failure
// reason, and keep its worktree/branch so the work is recoverable.
//
// This drives the shared runFinalization, which is the single funnel for both
// the agent-completion and tmux-advance finalization paths.
func TestRunFinalization_MergeFailureIsNotCompleted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the claude stub is a /bin/sh script")
	}

	repoDir := initRecoveryTestRepo(t)
	sortieYML := fmt.Sprintf(`on_complete: merge
git:
  base_branch: main
claude:
  command: %q
workflows:
  - name: default
    steps:
      - name: implement
        prompt: implement
`, writeFailingClaudeStub(t))
	if err := os.WriteFile(filepath.Join(repoDir, ".sortie.yml"), []byte(sortieYML), 0644); err != nil {
		t.Fatalf("failed to write .sortie.yml: %v", err)
	}
	// -f: the user's global excludes may ignore .sortie.yml.
	runGit(t, repoDir, "add", "-f", "--", ".sortie.yml")
	runGit(t, repoDir, "commit", "-q", "-m", "add sortie config")

	// Task branch forks here, then base diverges with a change to the same
	// file — so the fast-forward merge fails and the rebase-onto-base retry
	// hits a real conflict.
	const branch = "sortie-merge-failure"
	worktreePath := filepath.Join(t.TempDir(), "wt")
	runGit(t, repoDir, "worktree", "add", "-q", "-b", branch, worktreePath, "main")
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
		cmd.Dir = repoDir
		cmd.Run() // best-effort
	})

	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base side\n"), 0644); err != nil {
		t.Fatalf("failed to write base change: %v", err)
	}
	runGit(t, repoDir, "commit", "-q", "-am", "base diverges")

	if err := os.WriteFile(filepath.Join(worktreePath, "README.md"), []byte("task side\n"), 0644); err != nil {
		t.Fatalf("failed to write task change: %v", err)
	}

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	s := NewServer(&config.Config{}, database)
	t.Cleanup(func() { s.cancel() })
	t.Cleanup(func() { s.manager.Shutdown(2 * time.Second) })

	proj, err := database.GetOrCreateProject(repoDir)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	pc, err := s.getProjectContext(proj.ID)
	if err != nil {
		t.Fatalf("failed to load project context: %v", err)
	}

	tk, err := database.CreateTaskWithPriority(
		proj.ID, "Test task", "desc", "slug", "default", "", branch, "", "",
		task.StatusFinalizing, task.PriorityMedium, true, nil, nil,
	)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	if err := database.UpdateTaskWorktreePath(tk.ID, worktreePath); err != nil {
		t.Fatalf("failed to set worktree path: %v", err)
	}
	tk.WorktreePath = worktreePath

	s.runFinalization(tk, pc)

	refreshed, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if refreshed.Status == task.StatusCompleted {
		t.Fatalf("merge failed but task was marked completed (the bug)")
	}
	if refreshed.Status != task.StatusMergeFailed {
		t.Fatalf("expected status %s, got %s (error: %q)", task.StatusMergeFailed, refreshed.Status, refreshed.ErrorMessage)
	}

	info := s.taskToInfo(refreshed)
	// Substring check, not just non-empty: it pins the surfaced reason to the
	// conflict this test engineered, so an unrelated early failure elsewhere in
	// finalization can't masquerade as coverage of the merge path.
	if !strings.Contains(info.ErrorMessage, "conflict") {
		t.Errorf("TaskInfo.ErrorMessage does not surface the merge failure reason: %q", info.ErrorMessage)
	}
	if info.WorktreePath == "" {
		t.Error("worktree path was cleared: the recoverable failed-merge worktree was discarded")
	}
	if _, statErr := os.Stat(worktreePath); statErr != nil {
		t.Errorf("worktree directory was removed: %v", statErr)
	}
	verify := exec.Command("git", "rev-parse", "--verify", branch)
	verify.Dir = repoDir
	if out, err := verify.CombinedOutput(); err != nil {
		t.Errorf("task branch %s was deleted: %v\n%s", branch, err, out)
	}
}
