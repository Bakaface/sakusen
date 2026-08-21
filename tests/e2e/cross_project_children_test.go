//go:build e2e

package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// crossProjectParentYAML is project A's config: a one-step "spawn" workflow
// whose stub hook fans out into project B (see
// testdata/cross_project_children/step-spawn.hook.sh) and registers wait-on
// edges via `sakusen wait-for-tasks --use-env`.
//
// The prompt embeds {{children.2.status}} / {{children.3.status}} markers —
// child IDs are deterministic because the parent is always task 1 in a fresh
// DB. On the FIRST run these resolve to empty (c2=[]); on the resume run the
// engine hydrates them from the still-attached wait-on edges, and the stub's
// prompt capture (overwritten per invocation) lets the test assert on the
// resume run's resolved values.
//
// max_workers is a top-level config key (internal/config/types.go); it only
// takes effect after RestartDaemon because the agent manager reads it once at
// server construction.
func crossProjectParentYAML(stubPath string, maxWorkers int) string {
	return fmt.Sprintf(`default_agent: stub
agents:
  stub:
    mode: headless
    command: "%s"
poll_interval: 100ms
max_workers: %d
git:
  base_branch: main
on_complete: commit
workflows:
  - name: parent
    steps:
      - name: spawn
        prompt: "Spawn cross-project children; on resume c2=[{{children.2.status}}] c3=[{{children.3.status}}] summary={{children.summary}}"
`, stubPath, maxWorkers)
}

// crossProjectChildYAML is project B's config: `child` succeeds, `child-fail`
// fails (its hook exits 1, aborting the stub). Children run under project B's
// own workflows and commit into project B's own repo.
func crossProjectChildYAML(stubPath string) string {
	return fmt.Sprintf(`default_agent: stub
agents:
  stub:
    mode: headless
    command: "%s"
poll_interval: 100ms
git:
  base_branch: main
on_complete: commit
workflows:
  - name: child
    steps:
      - name: do-work
        prompt: "Do the work for child task #{{task.id}}"
  - name: child-fail
    steps:
      - name: fail-work
        prompt: "Fail the work for child task #{{task.id}}"
`, stubPath)
}

// childProjectPathForTask returns the projects.path row the given task belongs
// to, resolved through symlinks (on macOS /tmp is a symlink to /private/tmp,
// and the daemon records the project path as seen from the child process's
// getcwd, so a raw string compare against MkdirTemp's return value can differ).
func childProjectPathForTask(e *Env, taskID int64) string {
	p := e.DBQueryString("SELECT p.path FROM projects p JOIN tasks t ON t.project_id = p.id WHERE t.id = ?", taskID)
	if p == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return resolved
}

// TestCrossProjectChildrenSuspendAndResume drives the core cross-project
// scenario end-to-end: a parent in project A spawns children in project B,
// suspends, and resumes when BOTH children are terminal — one completed, one
// FAILED — with {{children.<id>.status}} visible to the resumed step.
func TestCrossProjectChildrenSuspendAndResume(t *testing.T) {
	e := setupE2E(t, "cross_project_children")
	e.WriteSakusenYAML(crossProjectParentYAML(e.StubPath, 3))

	childDir := e.AddProject(crossProjectChildYAML(e.StubPath))

	// The spawn hook reads these from $HOME (the per-test XDG dir): where to
	// create children, and which workflow each child runs.
	e.WriteHookFile("e2e-child-project", childDir)
	e.WriteHookFile("e2e-child-specs", "child\nchild-fail\n")

	// Parent is task 1; its hook spawns children 2 (child) and 3 (child-fail).
	e.MustSakusen("create", "--title", "cross project parent", "-w", "parent", "parent work")

	// a) Parent suspends on the cross-project children.
	e.WaitStatus(1, "awaiting-children", 20*time.Second)

	// b) Both wait-on edges are recorded.
	if got := e.DBQueryInt("SELECT COUNT(*) FROM task_waits_on WHERE task_id = 1"); got != 2 {
		t.Errorf("task_waits_on count while suspended: got %d, want 2", got)
	}

	// c) The children live in project B; the parent does not.
	projA := e.DBQueryInt("SELECT project_id FROM tasks WHERE id = 1")
	projB2 := e.DBQueryInt("SELECT project_id FROM tasks WHERE id = 2")
	projB3 := e.DBQueryInt("SELECT project_id FROM tasks WHERE id = 3")
	if projB2 != projB3 {
		t.Errorf("children landed in different projects: %d vs %d", projB2, projB3)
	}
	if projA == projB2 {
		t.Errorf("children landed in the parent's project (%d) — expected a distinct project", projA)
	}
	wantChildDir, err := filepath.EvalSymlinks(childDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", childDir, err)
	}
	if got := childProjectPathForTask(e, 2); got != wantChildDir {
		t.Errorf("child #2 project path = %q, want %q", got, wantChildDir)
	}

	// d) Resume-on-failure across projects: one child completes, one FAILS —
	// both are terminal, so the parent must resume.
	e.WaitStatus(2, "completed", 30*time.Second)
	e.WaitStatus(3, "failed", 30*time.Second)

	// e) Parent resumed and finished.
	e.WaitStatus(1, "completed", 40*time.Second)

	// f) Edges cleared by the engine's loadAndClearChildren on resume.
	if got := e.DBQueryInt("SELECT COUNT(*) FROM task_waits_on WHERE task_id = 1"); got != 0 {
		t.Errorf("task_waits_on count after resume: got %d, want 0 (engine must clear edges)", got)
	}

	// g) The spawn step ran exactly twice: initial (spawn + suspend) + resume.
	spawnCalls := stubStepCallsFor(e, "spawn")
	if len(spawnCalls) != 2 {
		t.Errorf("spawn-step stub calls: got %d, want 2 (initial + resume)", len(spawnCalls))
	}

	// h) The key assertion: {{children.<id>.status}} is visible to the
	// resumed parent step for cross-project children, including the FAILED
	// one. The stub overwrites prompt-1-spawn.txt each invocation, so the
	// capture holds the resume run's resolved prompt.
	prompt := e.CapturedPrompt(1, "spawn")
	if !strings.Contains(prompt, "c2=[completed]") {
		t.Errorf("resume prompt missing c2=[completed]:\n%s", prompt)
	}
	if !strings.Contains(prompt, "c3=[failed]") {
		t.Errorf("resume prompt missing c3=[failed]:\n%s", prompt)
	}
}

// TestCrossProjectChildrenNoDeadlockUnderContention is the no-deadlock proof
// for cross-project waits: with max_workers = 1 the suspended parent holds the
// ONLY worker slot's history — if suspension did not release the slot (the
// agent goroutine returns, the manager counts only active agents), no child
// could ever be claimed and everything would hang forever. The test passing
// at max_workers = 1 demonstrates no hold-and-wait, hence no deadlock.
func TestCrossProjectChildrenNoDeadlockUnderContention(t *testing.T) {
	e := setupE2E(t, "cross_project_children")
	e.WriteSakusenYAML(crossProjectParentYAML(e.StubPath, 1))
	// max_workers is read once at server construction; the daemon started
	// before the YAML existed, so restart to make max_workers: 1 effective.
	e.RestartDaemon()

	childDir := e.AddProject(crossProjectChildYAML(e.StubPath))
	e.WriteHookFile("e2e-child-project", childDir)
	e.WriteHookFile("e2e-child-specs", "child\nchild\nchild\n")

	e.MustSakusen("create", "--title", "cross project parent", "-w", "parent", "parent work")

	// Parent (task 1) suspends on children 2, 3, 4.
	e.WaitStatus(1, "awaiting-children", 20*time.Second)

	// All three children complete one at a time through the single worker
	// slot the parent released on suspend.
	e.WaitStatus(2, "completed", 60*time.Second)
	e.WaitStatus(3, "completed", 60*time.Second)
	e.WaitStatus(4, "completed", 60*time.Second)

	// The parent re-claims the slot and finishes.
	e.WaitStatus(1, "completed", 60*time.Second)
}
