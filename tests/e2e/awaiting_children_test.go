//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// awaitChildrenWorkflowYAML defines two workflows used by TestAwaitingChildrenSuspendsAndResumes:
//
//   - parent: a one-step "spawn-and-wait" workflow whose stub hook spawns two
//     child tasks via `sortie create -w child` and registers waits-on edges via
//     `sortie wait-for-tasks --use-env`. The hook is gated by a marker file so
//     only the FIRST step run spawns children; the resume run finds the marker
//     and does nothing.
//
//   - child: a one-step trivial workflow used by the spawned children.
//
// We use on_complete: commit (not merge) so finalization is single-step and we
// don't need to coordinate per-task branch names across the bundle.
func awaitChildrenWorkflowYAML(stubPath string) string {
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
  - name: parent
    steps:
      - name: spawn
        prompt: "Spawn 2 children and wait for them; on resume read {{children.summary}}"
  - name: child
    steps:
      - name: do-work
        prompt: "Do the work for child task #{{task.id}}"
`, stubPath)
}

// TestAwaitingChildrenSuspendsAndResumes drives the full
// create_tasks_and_wait → awaiting-children → resume → completed lifecycle
// end-to-end. Assertions:
//
//	a) parent transitions to "awaiting-children"
//	b) children run to "completed"
//	c) once both children are terminal, parent resumes (status flips back to
//	   running / completed) and finishes at the SAME step
//	d) {{children.<id>.context}} and friends are populated correctly on resume
//	   (we observe this indirectly: the resume run's stub log records that the
//	   spawn step ran a second time, and the parent's final status is
//	   completed — both impossible if the engine had advanced past the step
//	   on first suspend, or never resumed it.)
func TestAwaitingChildrenSuspendsAndResumes(t *testing.T) {
	e := setupE2E(t, "awaiting_children")
	e.WriteSortieYAML(awaitChildrenWorkflowYAML(e.StubPath))

	// Create the parent task. Workflow "parent" has a single step "spawn".
	e.MustSortie("create", "--title", "parent task", "-w", "parent", "parent task work")

	// Parent suspends mid-step on the two children it spawned.
	e.WaitStatus(1, "awaiting-children", 15*time.Second)

	// Confirm both wait-on edges are recorded in the DB while the parent is
	// suspended. This is the persistent state the poller's
	// checkSuspendedParents loop probes every tick.
	if got := e.DBQueryInt("SELECT COUNT(*) FROM task_waits_on WHERE task_id = 1"); got != 2 {
		t.Errorf("task_waits_on count while suspended: got %d, want 2", got)
	}

	// Children should run to completion (they were created as task IDs 2 and 3).
	e.WaitStatus(2, "completed", 15*time.Second)
	e.WaitStatus(3, "completed", 15*time.Second)

	// Parent resumes and runs the same step a second time, then completes.
	e.WaitStatus(1, "completed", 20*time.Second)

	// Wait-on edges must be cleared by the engine's loadAndClearChildren on
	// resume — they are a transient suspension lock, not a historical record.
	if got := e.DBQueryInt("SELECT COUNT(*) FROM task_waits_on WHERE task_id = 1"); got != 0 {
		t.Errorf("task_waits_on count after resume: got %d, want 0 (engine must clear edges on resume)", got)
	}

	// Stub spawn-step invocations: 1 for the initial run (which spawned
	// children and suspended), 1 for the resume run. Anything else means the
	// engine either advanced past the step prematurely or re-ran it
	// extraneously.
	spawnCalls := stubStepCallsFor(e, "spawn")
	if len(spawnCalls) != 2 {
		t.Errorf("spawn-step stub calls: got %d, want 2 (initial + resume)", len(spawnCalls))
	}

	// On the resume run, the engine populates the step prompt with
	// {{children.summary}}. We can't observe the prompt directly (the stub
	// receives it via --system-prompt), but we can confirm the spawn step's
	// final captured context is the second-run output. If the engine had
	// failed to resume the parent at the SAME step, this would be empty.
	if got := e.DBQueryString("SELECT context FROM task_steps WHERE task_id = 1 AND step_name = ?", "spawn"); got == "" {
		t.Errorf("spawn step context after parent completion is empty — resume did not write a step context")
	}

	// Children's step contexts confirm they actually ran.
	if got := e.DBQueryString("SELECT context FROM task_steps WHERE task_id = 2 AND step_name = ?", "do-work"); got == "" {
		t.Errorf("child #2 do-work context is empty — child did not actually run")
	}
}

// stubStepCallsFor returns the subset of e.StubCalls("step") whose Step env
// matches stepName. Mirrors how the existing tests grep stub invocations.
func stubStepCallsFor(e *Env, stepName string) []StubCall {
	calls := e.StubCalls("step")
	out := make([]StubCall, 0, len(calls))
	for _, c := range calls {
		if c.Step == stepName {
			out = append(out, c)
		}
	}
	_ = strings.Compare // keep import to avoid future churn if we add string matching
	return out
}
