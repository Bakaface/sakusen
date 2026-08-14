//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLoopExitsOnEmptyContext verifies that a loop step exits when the step context
// is empty. counter.sh returns non-empty for the first 3 calls (implementing +
// checking iter 0, implementing iter 1) and empty for call 4+ (checking iter 1),
// which triggers the loop's exit_condition.
//
// Assertions:
//   - task reaches "completed"
//   - the loop actually iterated at least once (stub called ≥ 4 times)
//   - merge commit on main
//
// Note: tasks.loop_iteration is reset to 0 in workflow/engine.go once the loop
// terminates, so we can't check it via the DB after completion — we infer
// iteration count from the call log instead.
func TestLoopExitsOnEmptyContext(t *testing.T) {
	e := setupE2E(t, "loop_exits")

	// counter.sh persists state in $E2E_RESPONSES_DIR/counter.state, which is
	// the shared testdata dir. Reset it so we always start from call 0.
	stateFile := filepath.Join(e.ResponsesDir, "counter.state")
	_ = os.Remove(stateFile)
	t.Cleanup(func() { _ = os.Remove(stateFile) })

	counterScript := filepath.Join(repoRoot, "tests", "e2e", "testdata", "loop_exits", "counter.sh")

	yaml := fmt.Sprintf(`default_agent: stub
agents:
  stub:
    mode: headless
    command: "%s"
poll_interval: 100ms
git:
  base_branch: main
on_complete: merge
workflows:
  - name: looping
    steps:
      - name: implementing
        prompt: "Do the work"
      - name: checking
        prompt: "Check if done"
        loop:
          goto: implementing
          max_iterations: 5
          exit_condition:
            step_context_empty: checking
`, counterScript)

	e.WriteSortieYAML(yaml)
	e.MustSortie("create", "--title", "loop task", "loop task")

	e.WaitStatus(1, "completed", 15*time.Second)

	stepCalls := len(e.StubCalls("step"))
	if stepCalls < 4 {
		t.Errorf("stub step calls = %d, want >= 4 (implies loop iterated at least once)", stepCalls)
	}

	e.AssertMergedFor(1)
}
