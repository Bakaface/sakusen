//go:build e2e

package e2e

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// periodicWorkflowYAML returns a periodic-only config with one inline-step
// definition. The cadence is far in the future so it never auto-fires — the
// test triggers it explicitly via `sakusen periodics run`.
func periodicWorkflowYAML(stubPath string) string {
	return fmt.Sprintf(`default_agent: stub
agents:
  stub:
    mode: headless
    command: "%s"
poll_interval: 100ms
git:
  base_branch: main
on_complete: merge
periodic:
  - name: heartbeat
    cadence: "0 3 1 1 *"
    steps:
      - name: implementing
        prompt: "Implement the task"
`, stubPath)
}

// TestPeriodicFireRunsToCompletion drives a periodic definition end-to-end:
// the daemon reconciles it from .sakusen.yml, an explicit run-now materializes a
// task, and that task runs through the engine to completion + merge.
func TestPeriodicFireRunsToCompletion(t *testing.T) {
	e := setupE2E(t, "periodic")
	e.WriteSakusenYAML(periodicWorkflowYAML(e.StubPath))

	// `periodics list` reconciles the definition into the DB and registers the
	// project. It must surface our heartbeat definition.
	out := e.MustSakusen("periodics", "list")
	if !strings.Contains(out, "heartbeat") {
		t.Fatalf("expected heartbeat in periodics list, got:\n%s", out)
	}

	id := e.DBQueryInt("SELECT id FROM periodic_definitions WHERE name = ?", "heartbeat")
	if id == 0 {
		t.Fatal("periodic definition was not reconciled into the DB")
	}

	// Trigger an immediate one-shot run.
	e.MustSakusen("periodics", "run", strconv.FormatInt(id, 10))

	// The materialized task should be linked to the definition and run to
	// completion through the engine.
	taskID := e.DBQueryInt("SELECT id FROM tasks WHERE periodic_id = ? ORDER BY id DESC LIMIT 1", id)
	if taskID == 0 {
		t.Fatal("no task was materialized for the periodic definition")
	}

	e.WaitStatus(taskID, "completed", 15*time.Second)
	e.AssertMergedFor(taskID)

	// The materialized task ran the inline periodic workflow.
	workflow := e.DBQueryString("SELECT workflow FROM tasks WHERE id = ?", taskID)
	if workflow != "periodic:heartbeat" {
		t.Errorf("expected workflow periodic:heartbeat, got %q", workflow)
	}
}
