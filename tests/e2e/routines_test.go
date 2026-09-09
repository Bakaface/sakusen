//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// routinesYAML returns a config with one workflow and three routines binding
// it: a scheduled one (cadence far in the future so it never auto-fires — the
// tests trigger it explicitly), an on-demand one whose pins override the
// workflow's, and an on-demand one that requires an input argument.
func routinesYAML(stubPath string) string {
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
  - name: compose
    target: main
    steps:
      - name: implementing
        prompt: "Implement the task"
  - name: digest
    steps:
      - name: implementing
        prompt: "Implement {{task.input}}"
routines:
  - name: nightly
    cadence: "0 3 1 1 *"
    workflow: compose
  - name: compose-wiki
    description: Rebuild the wiki
    workflow: compose
    branch: "sakusen/routine-{{task_id}}"
  - name: digest-now
    workflow: digest
`, stubPath)
}

// TestRoutineRunRunsToCompletion drives a scheduled routine end-to-end: the
// daemon reconciles it from .sakusen.yml, an explicit run-now materializes a
// task, and that task runs through the engine to completion + merge.
func TestRoutineRunRunsToCompletion(t *testing.T) {
	e := setupE2E(t, "routines")
	e.WriteSakusenYAML(routinesYAML(e.StubPath))

	// `routines list` reconciles the routines into the DB and registers the
	// project. It must surface every one of them.
	out := e.MustSakusen("routines", "list")
	for _, name := range []string{"nightly", "compose-wiki", "digest-now"} {
		if !strings.Contains(out, name) {
			t.Fatalf("expected %q in routines list, got:\n%s", name, out)
		}
	}
	if !strings.Contains(out, "on-demand") {
		t.Errorf("expected the on-demand status in routines list, got:\n%s", out)
	}

	id := e.DBQueryInt("SELECT id FROM periodic_definitions WHERE name = ?", "nightly")
	if id == 0 {
		t.Fatal("routine was not reconciled into the DB")
	}

	e.MustSakusen("routines", "run", "nightly")

	taskID := e.DBQueryInt("SELECT id FROM tasks WHERE periodic_id = ? ORDER BY id DESC LIMIT 1", id)
	if taskID == 0 {
		t.Fatal("no task was created for the routine")
	}

	e.WaitStatus(taskID, "completed", 15*time.Second)
	e.AssertMergedFor(taskID)

	// The task ran the workflow the routine names, not a synthesized one.
	if workflow := e.DBQueryString("SELECT workflow FROM tasks WHERE id = ?", taskID); workflow != "compose" {
		t.Errorf("expected workflow compose, got %q", workflow)
	}
}

// A routine's own pins override the referenced workflow's, per field: the
// routine's branch template reaches the task while the workflow's target
// survives.
func TestRoutinePinsOverrideWorkflowPins(t *testing.T) {
	e := setupE2E(t, "routines")
	e.WriteSakusenYAML(routinesYAML(e.StubPath))

	e.MustSakusen("routines", "run", "compose-wiki")

	id := e.DBQueryInt("SELECT id FROM periodic_definitions WHERE name = ?", "compose-wiki")
	taskID := e.DBQueryInt("SELECT id FROM tasks WHERE periodic_id = ? ORDER BY id DESC LIMIT 1", id)
	if taskID == 0 {
		t.Fatal("no task was created for the routine")
	}

	if got := e.TaskField(taskID, "branch_name"); got != "sakusen/routine-{{task_id}}" {
		t.Errorf("branch template: got %q, want the routine's pin", got)
	}
	if got := e.TaskField(taskID, "target_branch"); got != "main" {
		t.Errorf("target branch: got %q, want the workflow's pin to survive", got)
	}

	runs := e.MustSakusen("routines", "runs", "compose-wiki")
	if !strings.Contains(runs, fmt.Sprintf("%d", taskID)) {
		t.Errorf("expected task #%d in routines runs, got:\n%s", taskID, runs)
	}
}

// A routine whose workflow references {{task.input}} without supplying one
// demands the argument; the supplied text reaches the step prompt.
func TestRoutineRequiresInputArgument(t *testing.T) {
	e := setupE2E(t, "routines")
	e.WriteSakusenYAML(routinesYAML(e.StubPath))

	out, err := e.Sakusen("routines", "run", "digest-now")
	if err == nil {
		t.Fatalf("expected a requires-input failure, got:\n%s", out)
	}
	if !strings.Contains(out, "requires an input argument") {
		t.Fatalf("expected the requires-input error, got:\n%s", out)
	}

	e.MustSakusen("routines", "run", "digest-now", "hello")

	id := e.DBQueryInt("SELECT id FROM periodic_definitions WHERE name = ?", "digest-now")
	taskID := e.DBQueryInt("SELECT id FROM tasks WHERE periodic_id = ? ORDER BY id DESC LIMIT 1", id)
	if taskID == 0 {
		t.Fatal("no task was created for the routine")
	}
	if prompt := e.CapturedPrompt(taskID, "implementing"); !strings.Contains(prompt, "hello") {
		t.Errorf("expected the input argument in the step prompt, got:\n%s", prompt)
	}
}

// `periodic:` was removed outright: a config still using it fails validation
// with the migration error rather than loading.
func TestRemovedPeriodicKeyFailsValidate(t *testing.T) {
	e := setupE2E(t, "routines")
	e.WriteSakusenYAML(fmt.Sprintf(`default_agent: stub
agents:
  stub:
    mode: headless
    command: "%s"
workflows:
  - name: compose
    steps:
      - name: implementing
        prompt: "Implement the task"
periodic:
  - name: nightly
    cadence: "0 3 * * *"
    workflow: compose
`, e.StubPath))

	out, err := e.Sakusen("validate")
	if err == nil {
		t.Fatalf("expected validate to fail on the removed periodic: key, got:\n%s", out)
	}
	if !strings.Contains(out, "routines:") {
		t.Fatalf("expected the migration error naming routines:, got:\n%s", out)
	}
}
