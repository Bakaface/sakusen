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

// parallelWorkflowYAML builds the fan-out scenario: implement → a two-branch
// review group → synthesize. review-b always fails (its hook exits 1), so the
// `require` value alone decides whether the task advances.
func parallelWorkflowYAML(stubPath, require string) string {
	return fmt.Sprintf(`default_agent: stub
agents:
  stub:
    mode: headless
    command: "%s"
summarizer:
  command: "%s"
poll_interval: 100ms
git:
  base_branch: main
on_complete: none
workflows:
  - name: reviewed
    steps:
      - name: implement
        prompt: "Implement the task"
      - name: review
        parallel:
          require: %s
          branches:
            - name: review-a
              prompt: "Review as {{branch.name}} with {{branch.agent}}"
            - name: review-b
              prompt: "Review as {{branch.name}}"
      - name: synthesize
        prompt: |
          Reviews follow.
          {{steps.review.context}}
`, stubPath, stubPath, require)
}

// TestParallelGroupRequireAny drives a full daemon through a parallel group
// whose `require: any` tolerates a losing branch: the task completes, the
// branch rows carry their own statuses, and the synthesis step's prompt
// contains the aggregate with the failure marked.
func TestParallelGroupRequireAny(t *testing.T) {
	e := setupE2E(t, "parallel")
	e.WriteSakusenYAML(parallelWorkflowYAML(e.StubPath, "any"))

	e.MustSakusen("create", "--title", "parallel review", "parallel review task")
	e.WaitStatus(1, "completed", 30*time.Second)

	db := e.DB()
	wantRows := map[string]string{
		"implement":  "completed",
		"review":     "completed",
		"review-a":   "completed",
		"review-b":   "failed",
		"synthesize": "completed",
	}
	for name, want := range wantRows {
		var got string
		row := db.QueryRow(`SELECT status FROM task_steps WHERE task_id = 1 AND step_name = ?`, name)
		if err := row.Scan(&got); err != nil {
			t.Errorf("task_steps status for %q: %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("task_steps.status[%q] = %q, want %q", name, got, want)
		}
	}

	// The group row holds the aggregate, with the failed branch marked.
	var aggregate string
	if err := db.QueryRow(`SELECT context FROM task_steps WHERE task_id = 1 AND step_name = 'review'`).Scan(&aggregate); err != nil {
		t.Fatalf("read group context: %v", err)
	}
	if !strings.Contains(aggregate, "## review-a (stub)") {
		t.Errorf("aggregate missing the succeeding branch header:\n%s", aggregate)
	}
	if !strings.Contains(aggregate, "review-a says: rename the widget") {
		t.Errorf("aggregate missing review-a's context:\n%s", aggregate)
	}
	if !strings.Contains(aggregate, "## review-b (stub) (failed: exit 1)") {
		t.Errorf("aggregate must mark the failed branch loudly:\n%s", aggregate)
	}

	// The synthesis prompt is where the aggregate actually has to land.
	prompt := e.CapturedPrompt(1, "synthesize")
	if !strings.Contains(prompt, "## review-a (stub)") || !strings.Contains(prompt, "## review-b (stub) (failed: exit 1)") {
		t.Errorf("synthesize prompt did not receive the aggregate:\n%s", prompt)
	}

	// Branch prompts resolved {{branch.*}}.
	if got := e.CapturedPrompt(1, "review-a"); !strings.Contains(got, "Review as review-a with stub") {
		t.Errorf("review-a prompt = %q, want {{branch.name}}/{{branch.agent}} resolved", got)
	}

	// task.log carries the group markers and NONE of the branch agent output.
	logPath := filepath.Join(e.ProjectDir, ".sakusen", "logs", "1", "task.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read task log: %v", err)
	}
	taskLog := string(data)
	for _, marker := range []string{
		"=== parallel review: started 2 branches ===",
		"=== parallel review: 1/2 succeeded ===",
	} {
		if !strings.Contains(taskLog, marker) {
			t.Errorf("task.log missing %q:\n%s", marker, taskLog)
		}
	}
	// The stub prints a decoy line on stdout for every step spawn; the group's
	// branches must not have contributed theirs to the unified log.
	if n := strings.Count(taskLog, "stub-stdout-decoy"); n != 2 {
		t.Errorf("task.log has %d stub decoy lines, want 2 (implement + synthesize only — no branch output)", n)
	}

	// Both branches wrote their own log file.
	for _, name := range []string{"review-a", "review-b"} {
		branchLog := filepath.Join(e.ProjectDir, ".sakusen", "logs", "1", "branch-"+name+".log")
		if _, err := os.Stat(branchLog); err != nil {
			t.Errorf("branch log for %q missing: %v", name, err)
		}
	}

	// Retrying the GROUP re-runs only the branches that did not complete:
	// review-a's completed row survives the reset and is reused. Deterministic
	// here because require:any never cancels a sibling.
	before := len(e.StubCalls("step"))
	e.MustSakusen("retry", "1", "--from-step", "review")
	e.WaitStatus(1, "completed", 30*time.Second)

	var reran []string
	for _, c := range e.StubCalls("step")[before:] {
		reran = append(reran, c.Step)
	}
	for _, name := range reran {
		if name == "review-a" {
			t.Errorf("a completed branch must never be re-run by a group retry, got %v", reran)
		}
	}
	if !containsStep(reran, "review-b") {
		t.Errorf("re-run steps = %v, want the non-completed branch re-run", reran)
	}
}

func containsStep(steps []string, want string) bool {
	for _, s := range steps {
		if s == want {
			return true
		}
	}
	return false
}

// TestParallelGroupRequireAll drives the failure path: with `require: all` the
// losing branch fails the whole group, the task's error message explains the
// join, and a BRANCH name is rejected as a retry target.
//
// It deliberately does NOT assert which branches re-run: `require: all` fails
// fast and cancels the siblings, so whether review-a completed before the
// cancel landed is a genuine race. The retry-reuse behaviour is pinned in
// TestParallelGroupRequireAny, where no cancellation happens.
func TestParallelGroupRequireAll(t *testing.T) {
	e := setupE2E(t, "parallel")
	e.WriteSakusenYAML(parallelWorkflowYAML(e.StubPath, "all"))

	e.MustSakusen("create", "--title", "parallel review", "parallel review task")
	e.WaitStatus(1, "failed", 30*time.Second)

	msg := e.TaskField(1, "error_message")
	if !strings.Contains(msg, `parallel group "review"`) || !strings.Contains(msg, "review-b: exit 1") {
		t.Errorf("error_message = %q, want the group join failure naming the losing branch", msg)
	}

	// The synthesis step must never have run.
	for _, c := range e.StubCalls("step") {
		if c.Step == "synthesize" {
			t.Error("synthesize ran despite the require:all group failing")
		}
	}

	// A branch is not an independent retry target.
	out, err := e.Sakusen("retry", "1", "--from-step", "review-b")
	if err == nil {
		t.Errorf("retrying a branch must fail, got success: %s", out)
	} else if !strings.Contains(out, `is a branch of parallel group "review"`) {
		t.Errorf("branch retry error = %q, want the retry-the-group hint", out)
	}
}
