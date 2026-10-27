package daemon

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	gitpkg "github.com/Bakaface/sakusen/internal/git"
	"github.com/Bakaface/sakusen/internal/task"
	"github.com/Bakaface/sakusen/internal/workflow"
)

var isolatedBranchNames = []string{"impl-a", "impl-b"}

const isolatedCleanupYML = `git:
  base_branch: main
workflows:
  - name: default
    steps:
      - name: impl
        parallel:
          isolation: worktree
          branches:
            - name: impl-a
              prompt: a
            - name: impl-b
              prompt: b
`

// setupIsolatedCleanupTask builds a git-backed project whose workflow has an
// isolated group, plus a task with its worktree and both branch worktrees
// provisioned, each branch carrying a commit.
func setupIsolatedCleanupTask(t *testing.T, userCheckout bool) (*Server, *projectContext, *task.Task) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	repoDir := initRecoveryTestRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, ".sakusen.yml"), []byte(isolatedCleanupYML), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".gitignore"), []byte(".sakusen/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "--", ".sakusen.yml", ".gitignore")
	runGit(t, repoDir, "commit", "-q", "-m", "add sakusen config")

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	s := NewServer(&config.Config{}, database)
	t.Cleanup(func() { s.cancel() })
	t.Cleanup(func() { s.manager.Shutdown(2 * time.Second) })

	proj, err := database.GetOrCreateProject(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	pc, err := s.getProjectContext(proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := workflow.IsolatedBranchNames(pc.cfg.GetTaskWorkflow("default")); len(got) != len(isolatedBranchNames) {
		t.Fatalf("isolated branch names = %v (load err %v), want %v", got, pc.loadErr, isolatedBranchNames)
	}

	const branch = "sakusen-isolated-cleanup"
	checkout := ""
	if userCheckout {
		checkout = branch
	}
	tk, err := database.CreateTaskWithPriority(
		proj.ID, "Isolated task", "desc", "slug", "default", "", branch, "", checkout,
		task.StatusCompleted, task.PriorityMedium, true, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	worktreePath := filepath.Join(t.TempDir(), "wt")
	if err := database.UpdateTaskWorktreePath(tk.ID, worktreePath); err != nil {
		t.Fatal(err)
	}
	tk.WorktreePath = worktreePath

	repo := gitpkg.NewRepo(repoDir)
	if _, err := repo.EnsureWorktreeAt(worktreePath, branch, "main"); err != nil {
		t.Fatalf("task worktree: %v", err)
	}
	for _, name := range isolatedBranchNames {
		path := workflow.IsolatedBranchWorktreePath(worktreePath, name)
		if _, err := repo.EnsureWorktreeAt(path, workflow.IsolatedBranchGitBranch(branch, name), branch); err != nil {
			t.Fatalf("%s worktree: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(path, name+".txt"), []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
		runGit(t, path, "add", "-A")
		runGit(t, path, "commit", "-q", "-m", "branch "+name)
	}
	return s, pc, tk
}

// branchWorktreesGone reports what of tk's isolated-branch state survives, or
// "" when every branch worktree is gone and, if wantBranchesKept is false,
// every branch git branch too.
func branchWorktreesGone(t *testing.T, pc *projectContext, tk *task.Task, wantBranchesKept bool) string {
	t.Helper()
	out, err := exec.Command("git", "-C", pc.repoRoot, "worktree", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list: %v\n%s", err, out)
	}
	var left []string
	for _, name := range isolatedBranchNames {
		path := workflow.IsolatedBranchWorktreePath(tk.WorktreePath, name)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			left = append(left, path+" on disk")
		}
		if strings.Contains(string(out), path) {
			left = append(left, path+" registered")
		}
		branch := workflow.IsolatedBranchGitBranch(tk.Branch, name)
		if exists := pc.repo.BranchExists(branch); exists != wantBranchesKept {
			left = append(left, fmt.Sprintf("branch %s exists=%v", branch, exists))
		}
	}
	return strings.Join(left, "; ")
}

func TestCleanupWorktreeAndBranchRemovesBranchWorktrees(t *testing.T) {
	for _, tc := range []struct {
		name         string
		userCheckout bool
	}{
		{"sakusen branch", false},
		{"user checkout branch", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, pc, tk := setupIsolatedCleanupTask(t, tc.userCheckout)

			s.cleanupWorktreeAndBranch(pc, tk)

			if left := branchWorktreesGone(t, pc, tk, tc.userCheckout); left != "" {
				t.Errorf("after cleanup: %s", left)
			}
		})
	}
}

func TestDeleteTaskRemovesBranchWorktrees(t *testing.T) {
	s, pc, tk := setupIsolatedCleanupTask(t, false)

	clientConn, serverConn := pipeForHandler(t)
	go func() {
		scanner := bufio.NewScanner(clientConn)
		for scanner.Scan() {
		}
	}()
	s.handleDeleteTask(serverConn, DeleteTaskRequest{TaskID: tk.ID})

	deadline := time.Now().Add(10 * time.Second)
	for {
		left := branchWorktreesGone(t, pc, tk, false)
		if left == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background cleanup did not remove branch state: %s", left)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
