//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mergeIntoParentWorkflowYAML(stubPath string) string {
	return fmt.Sprintf(`default_agent: stub
agents:
  stub:
    mode: headless
    command: "%s"
poll_interval: 100ms
git:
  base_branch: main
on_complete: merge
workflows:
  - name: parent
    steps:
      - name: spawn
        prompt: "Spawn 2 children onto my branch and wait; on resume read {{children.summary}}"
  - name: child
    steps:
      - name: do-work
        prompt: "Do the work for child task #{{task.id}}"
`, stubPath)
}

// TestChildrenMergeIntoParentBranch drives children whose target is the
// parent's branch: each child's merge fast-forwards the suspended parent's
// worktree (the second rebasing onto the first), nothing reaches main while
// the parent waits, the resumed parent sees every child's commits in its
// worktree, and only the parent's own merge lands them on main.
func TestChildrenMergeIntoParentBranch(t *testing.T) {
	e := setupE2E(t, "merge_into_parent")
	// As `sakusen init` does: the parent's own headless output capture must
	// not make its worktree a dirty merge target.
	gitignore := filepath.Join(e.ProjectDir, ".gitignore")
	if err := os.WriteFile(gitignore, []byte(".sakusen/\n.sakusen-output.log\n"), 0644); err != nil {
		t.Fatal(err)
	}
	e.mustRun(e.ProjectDir, "git", "commit", "-am", "ignore sakusen output log")
	e.WriteSakusenYAML(mergeIntoParentWorkflowYAML(e.StubPath))
	mainSetupCommits := strings.TrimSpace(gitInDir(e.ProjectDir, "rev-list", "--count", "main"))

	e.MustSakusen("create", "--title", "parent task", "-w", "parent", "parent task work")
	e.WaitStatus(1, "awaiting-children", 15*time.Second)
	parentBranch := e.TaskField(1, "branch")
	if parentBranch == "" {
		t.Fatal("parent has no branch")
	}
	for _, id := range []int64{2, 3} {
		if got := e.TaskField(id, "target_branch"); got != parentBranch {
			t.Errorf("child #%d target_branch = %q, want the parent's branch %q", id, got, parentBranch)
		}
	}

	e.WaitStatus(2, "completed", 45*time.Second)
	e.WaitStatus(3, "completed", 45*time.Second)
	e.WaitStatus(1, "completed", 45*time.Second)

	files, err := os.ReadFile(filepath.Join(e.PromptDir, "resume-files.txt"))
	if err != nil {
		t.Fatalf("resume run did not record the parent worktree: %v", err)
	}
	for _, f := range []string{"child-2.txt", "child-3.txt"} {
		if !strings.Contains(string(files), f) {
			t.Errorf("parent worktree on resume lacks %s; files:\n%s", f, files)
		}
	}
	count, err := os.ReadFile(filepath.Join(e.PromptDir, "resume-main-count.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(count)); got != mainSetupCommits {
		t.Errorf("main had %s commits when the parent resumed, want %s (children must not reach main)", got, mainSetupCommits)
	}

	// The parent's merge carries both children plus its own commit to main.
	for _, f := range []string{"child-2.txt", "child-3.txt", "parent-resumed.txt"} {
		if strings.TrimSpace(gitInDir(e.ProjectDir, "ls-tree", "--name-only", "main", f)) != f {
			t.Errorf("main lacks %s after the parent merged", f)
		}
	}
	if merges := strings.TrimSpace(gitInDir(e.ProjectDir, "rev-list", "--merges", "--count", "main")); merges != "0" {
		t.Errorf("main has %s merge commits, want linear history", merges)
	}
}
