//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func workdirLockYAML(stubPath string) string {
	return fmt.Sprintf(`default_agent: stub
agents:
  stub:
    mode: headless
    command: "%s"
poll_interval: 100ms
git:
  base_branch: main
on_complete: none
workflows:
  - name: session
    worktree: false
    steps:
      - name: implementing
        prompt: "Implement the task"
      - name: approve
        human: true
        prompt: "Human review step"
  - name: sync
    worktree: false
    exclusive_workdir: true
    steps:
      - name: implementing
        prompt: "Sync the docs"
`, stubPath)
}

// TestExclusiveWorkdirWaitsForHolder verifies that an exclusive_workdir task
// stays pending while another worktree:false task of the project is paused in
// the project root, and runs once that task finishes.
func TestExclusiveWorkdirWaitsForHolder(t *testing.T) {
	e := setupE2E(t, "human_gate") // reuses the implementing/approve stub fixtures
	e.WriteSakusenYAML(workdirLockYAML(e.StubPath))

	e.MustSakusen("create", "--title", "session", "--workflow", "session", "session task")
	e.WaitStatus(1, "awaiting-approval", 10*time.Second)

	e.MustSakusen("create", "--title", "sync", "--workflow", "sync", "sync task")
	e.Eventually(10*time.Second, "wait logged", func() bool {
		data, _ := os.ReadFile(e.DaemonLogPath())
		return strings.Contains(string(data), "Task #2: waiting for working tree, held by task #1")
	})
	// Several poll ticks later it must still be waiting.
	time.Sleep(time.Second)
	if got := e.TaskStatus(2); got != "pending" {
		t.Fatalf("exclusive_workdir task status while workdir held: got %q, want pending", got)
	}

	e.MustSakusen("continue", "1")
	e.WaitStatus(1, "completed", 10*time.Second)
	e.WaitStatus(2, "completed", 10*time.Second)
}
