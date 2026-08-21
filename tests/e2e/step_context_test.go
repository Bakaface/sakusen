//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"
)

func threeStepWorkflowYAML(stubPath string) string {
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
  - name: plan-impl-review
    steps:
      - name: planning
        prompt: "Plan the task"
      - name: implementing
        prompt: "Implement based on: {{step.planning.context}}"
      - name: reviewing
        prompt: "Review implementation"
`, stubPath)
}

// TestStepContextPropagation verifies that each step's result is stored as context
// and that later steps can reference earlier step contexts.
// Assertions:
// - task_steps.context for each step matches the stub-emitted result text
// - stub was called 3 times for purpose=step
func TestStepContextPropagation(t *testing.T) {
	e := setupE2E(t, "step_context")
	e.WriteSakusenYAML(threeStepWorkflowYAML(e.StubPath))

	e.MustSakusen("create", "--title", "plan impl review", "plan impl review task")

	e.WaitStatus(1, "completed", 15*time.Second)

	// Verify step contexts stored in DB
	db := e.DB()
	steps := map[string]string{
		"planning":     "planning-output",
		"implementing": "implementing-output",
		"reviewing":    "reviewing-output",
	}
	for name, want := range steps {
		var got string
		row := db.QueryRow(`SELECT context FROM task_steps WHERE task_id = 1 AND step_name = ?`, name)
		if err := row.Scan(&got); err != nil {
			t.Errorf("task_steps context for %q: %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("task_steps.context[%q] = %q, want %q", name, got, want)
		}
	}

	// Verify stub was called 3 times for step
	calls := e.StubCalls("step")
	if len(calls) != 3 {
		t.Errorf("stub step calls: got %d, want 3", len(calls))
	}
}
