//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func failingWorkflowYAML(stubPath string) string {
	return fmt.Sprintf(`default_agent: stub
agents:
  stub:
    mode: headless
    command: "%s"
poll_interval: 100ms
git:
  base_branch: main
workflows:
  - name: failing
    steps:
      - name: implementing
        prompt: "Implement the task"
`, stubPath)
}

// TestStepFailureAndRetry verifies that:
// 1. A stub that exits non-zero causes the task to fail
// 2. error_message is non-empty after failure
// 3. After SwapResponses to a success stub, sakusen retry succeeds
func TestStepFailureAndRetry(t *testing.T) {
	e := setupE2E(t, "failure_retry")

	// Use the fail.sh script which exits 1
	failScript := filepath.Join(repoRoot, "tests", "e2e", "testdata", "failure_retry", "fail.sh")
	e.WriteSakusenYAML(failingWorkflowYAML(failScript))

	e.MustSakusen("create", "--title", "failing task", "failing task")

	// Task should fail
	e.WaitStatus(1, "failed", 10*time.Second)

	errMsg := e.TaskField(1, "error_message")
	if errMsg == "" {
		t.Errorf("expected non-empty error_message after failure")
	}

	// Swap to success responses: write success subdir pointer
	// The success response is at testdata/failure_retry/success/step-implementing.txt
	// We swap the stub to use the regular stub-agent.sh pointing at the success subdir.
	// SwapResponses writes .current-subdir into the shared testdata dir; remove
	// it on cleanup so it doesn't leak into other runs (or the repo).
	e.SwapResponses("success")
	t.Cleanup(func() { _ = os.Remove(filepath.Join(e.ResponsesDir, ".current-subdir")) })

	// Now retry with the regular stub (which will read from success/ subdir)
	// Update .sakusen.yml to use the regular stub
	e.WriteSakusenYAML(failingWorkflowYAML(e.StubPath))

	// Also update E2E_RESPONSES_DIR to point at failure_retry (SwapResponses handles subdir)
	// E2E_RESPONSES_DIR is already failure_retry — SwapResponses wrote "success" as .current-subdir
	// So stub will look at failure_retry/success/step-implementing.txt

	e.MustSakusen("retry", "1")

	e.WaitStatus(1, "completed", 10*time.Second)
}
