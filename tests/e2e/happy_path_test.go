//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"
)

// simpleWorkflowYAML returns a one-step workflow YAML using the stub at stubPath.
func simpleWorkflowYAML(stubPath string) string {
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
  - name: simple
    steps:
      - name: implementing
        prompt: "Implement the task"
`, stubPath)
}

// TestHappyPath runs a single-step workflow end-to-end and asserts:
// - task reaches "completed"
// - a merge commit exists on main
// - the stub was called exactly once with purpose=step
func TestHappyPath(t *testing.T) {
	e := setupE2E(t, "happy_path")
	e.WriteSakusenYAML(simpleWorkflowYAML(e.StubPath))

	e.MustSakusen("create", "--title", "do thing", "do thing")

	e.WaitStatus(1, "completed", 10*time.Second)
	e.AssertMergedFor(1)

	calls := e.StubCalls("step")
	if len(calls) != 1 {
		t.Errorf("stub step calls: got %d, want 1", len(calls))
	}
}
