//go:build e2e

package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestResultFileStdoutFallback verifies the stdout-tail fallback of the agent
// result contract: an agent that never writes $SAKUSEN_RESULT_FILE and only
// prints a line to stdout still yields step context — production falls back
// to the retained tail of stdout (runner.Process.ResultText). The main stub
// covers the opposite (result-file) path via its stdout decoy.
func TestResultFileStdoutFallback(t *testing.T) {
	e := setupE2E(t, "result_fallback")

	agentScript := filepath.Join(repoRoot, "tests", "e2e", "testdata", "result_fallback", "stdout-agent.sh")
	yaml := fmt.Sprintf(`default_agent: stdout-only
agents:
  stdout-only:
    mode: headless
    command: "%s"
poll_interval: 100ms
git:
  base_branch: main
on_complete: merge
workflows:
  - name: fallback
    steps:
      - name: implementing
        prompt: "Implement the task"
        summarization_strategy: last_message
`, agentScript)
	e.WriteSakusenYAML(yaml)

	e.MustSakusen("create", "--title", "fallback task", "fallback task")

	e.WaitStatus(1, "completed", 10*time.Second)
	e.AssertMergedFor(1)

	got := e.DBQueryString(`SELECT context FROM task_steps WHERE task_id = 1 AND step_name = 'implementing'`)
	if !strings.Contains(got, "stdout-only-fallback-line") {
		t.Errorf("task_steps.context = %q, want it to contain stdout line %q", got, "stdout-only-fallback-line")
	}
}
