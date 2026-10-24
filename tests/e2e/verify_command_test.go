//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// verifyWorkflowYAML is simpleWorkflowYAML with git.verify_command set.
func verifyWorkflowYAML(stubPath, verifyCommand string) string {
	return fmt.Sprintf(`default_agent: stub
agents:
  stub:
    mode: headless
    command: "%s"
poll_interval: 100ms
git:
  base_branch: main
  verify_command: %q
on_complete: merge
workflows:
  - name: simple
    steps:
      - name: implementing
        prompt: "Implement the task"
`, stubPath, verifyCommand)
}

// TestVerifyCommandPassMerges: a passing verify command, run in the task
// worktree (where the stub created hello.txt), lets the merge proceed.
func TestVerifyCommandPassMerges(t *testing.T) {
	e := setupE2E(t, "happy_path")
	e.WriteSakusenYAML(verifyWorkflowYAML(e.StubPath, "test -f hello.txt"))

	e.MustSakusen("create", "--title", "do thing", "do thing")

	e.WaitStatus(1, "completed", 10*time.Second)
	e.AssertMergedFor(1)
}

// TestVerifyCommandFailureBlocksMerge: a failing verify command leaves the
// task merge-failed with the output in its error, main untouched, and the
// task worktree and branch kept.
func TestVerifyCommandFailureBlocksMerge(t *testing.T) {
	e := setupE2E(t, "happy_path")
	e.WriteSakusenYAML(verifyWorkflowYAML(e.StubPath, "echo verify-broke; exit 1"))
	mainBefore := strings.TrimSpace(gitInDir(e.ProjectDir, "rev-parse", "main"))

	e.MustSakusen("create", "--title", "do thing", "do thing")

	e.WaitStatus(1, "merge-failed", 10*time.Second)

	errMsg := e.TaskField(1, "error_message")
	for _, want := range []string{"verify command failed", "exited with code 1", "verify-broke"} {
		if !strings.Contains(errMsg, want) {
			t.Errorf("error_message %q missing %q", errMsg, want)
		}
	}
	if got := strings.TrimSpace(gitInDir(e.ProjectDir, "rev-parse", "main")); got != mainBefore {
		t.Errorf("main moved to %s after failed verify, want %s", got, mainBefore)
	}
	branch := e.TaskField(1, "branch")
	if branch == "" {
		t.Fatal("task has no branch")
	}
	e.AssertBranchExists(branch)
	if wt := e.TaskField(1, "worktree_path"); wt == "" {
		t.Error("worktree_path cleared after failed verify")
	} else if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree removed after failed verify: %v", err)
	}
}
