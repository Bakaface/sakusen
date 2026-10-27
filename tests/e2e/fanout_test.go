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

func fanoutWorkflowYAML(stubPath string) string {
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
      - name: inventory
        prompt: "Publish the units of task #{{task.id}}"
      - name: units
        fanout:
          from: inventory
          workflow: child
          merge: parent
      - name: after
        prompt: "Continue after the units:\n{{steps.units.context}}"
  - name: child
    steps:
      - name: child-work
        prompt: "Do this unit: {{task.input}}"
`, stubPath)
}

// TestFanoutMergeIntoParent drives a fanout step with `merge: parent`: the
// inventory step publishes two items, the fanout spawns one child per item
// targeting the parent's branch, each child merges into the suspended parent's
// worktree without reaching main, and the parent's next step sees both the
// children's files and the fanout aggregate before its own merge lands
// everything on main.
func TestFanoutMergeIntoParent(t *testing.T) {
	e := setupE2E(t, "fanout")
	// As `sakusen init` does: the parent's own headless output capture must
	// not make its worktree a dirty merge target.
	gitignore := filepath.Join(e.ProjectDir, ".gitignore")
	if err := os.WriteFile(gitignore, []byte(".sakusen/\n.sakusen-output.log\n"), 0644); err != nil {
		t.Fatal(err)
	}
	e.mustRun(e.ProjectDir, "git", "add", ".gitignore")
	e.mustRun(e.ProjectDir, "git", "commit", "-m", "ignore sakusen output log")
	e.WriteSakusenYAML(fanoutWorkflowYAML(e.StubPath))
	mainSetupCommits := strings.TrimSpace(gitInDir(e.ProjectDir, "rev-list", "--count", "main"))

	e.MustSakusen("create", "--title", "fanout parent", "-w", "parent", "fanout parent work")
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
	if n := e.DBQueryInt(`SELECT COUNT(*) FROM tasks WHERE fanout_parent_id = 1`); n != 2 {
		t.Errorf("fanout children of #1 = %d, want 2", n)
	}
	items := e.DBQueryString(`SELECT group_concat(fanout_item, ',') FROM (SELECT fanout_item FROM tasks WHERE fanout_parent_id = 1 ORDER BY id)`)
	if items != "alpha,beta" {
		t.Errorf("fanout items = %q, want %q", items, "alpha,beta")
	}

	e.WaitStatus(2, "completed", 45*time.Second)
	e.WaitStatus(3, "completed", 45*time.Second)
	e.WaitStatus(1, "completed", 45*time.Second)

	files, err := os.ReadFile(filepath.Join(e.PromptDir, "after-files.txt"))
	if err != nil {
		t.Fatalf("after step did not record the parent worktree: %v", err)
	}
	for _, f := range []string{"child-2.txt", "child-3.txt"} {
		if !strings.Contains(string(files), f) {
			t.Errorf("parent worktree after the fanout lacks %s; files:\n%s", f, files)
		}
	}
	count, err := os.ReadFile(filepath.Join(e.PromptDir, "after-main-count.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(count)); got != mainSetupCommits {
		t.Errorf("main had %s commits when the parent resumed, want %s (children must not reach main)", got, mainSetupCommits)
	}

	prompt := e.CapturedPrompt(1, "after")
	for _, want := range []string{"fanout units: 2/2 completed", "alpha", "beta"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("after prompt lacks %q:\n%s", want, prompt)
		}
	}

	// The parent's merge carries both children plus its own commit to main.
	for _, f := range []string{"child-2.txt", "child-3.txt", "parent-after.txt"} {
		if strings.TrimSpace(gitInDir(e.ProjectDir, "ls-tree", "--name-only", "main", f)) != f {
			t.Errorf("main lacks %s after the parent merged", f)
		}
	}
	e.AssertMergedFor(1)
}
