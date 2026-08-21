//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"
)

func humanApprovalWorkflowYAML(stubPath string) string {
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
  - name: human-approval
    steps:
      - name: implementing
        prompt: "Implement the task"
      - name: approve
        human: true
        prompt: "Human review step"
`, stubPath)
}

// TestHumanApprovalPausesAndResumes verifies that a workflow with a human step:
// 1. Pauses at the human step (status = "awaiting-approval")
// 2. Resumes after sakusen continue <id>
// 3. Completes successfully
func TestHumanApprovalPausesAndResumes(t *testing.T) {
	e := setupE2E(t, "human_gate")
	e.WriteSakusenYAML(humanApprovalWorkflowYAML(e.StubPath))

	e.MustSakusen("create", "--title", "human gate task", "human gate task")

	// Should pause at the human step
	e.WaitStatus(1, "awaiting-approval", 10*time.Second)

	// Resume
	e.MustSakusen("continue", "1")

	// Should complete
	e.WaitStatus(1, "completed", 10*time.Second)
}
