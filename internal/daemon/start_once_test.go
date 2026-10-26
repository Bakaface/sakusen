package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/agent"
	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
)

// Concurrent poller ticks plus a direct start from a stale pending snapshot
// must launch the task's headless step exactly once: ClaimTask's
// compare-and-set lets only one caller past the pending check.
func TestStartTaskAgent_ConcurrentClaimsStartOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	repoDir := initRecoveryTestRepo(t)
	countFile := filepath.Join(t.TempDir(), "runs")
	yml := fmt.Sprintf(`on_complete: none
default_agent: stub
agents:
  stub:
    mode: headless
    command: |
      echo x >> "%s"; sleep 1; printf done > "$SAKUSEN_RESULT_FILE"
workflows:
  - name: w
    worktree: false
    steps:
      - name: run
        prompt: "run"
        summarization_strategy: last_message
`, countFile)
	if err := os.WriteFile(filepath.Join(repoDir, ".sakusen.yml"), []byte(yml), 0644); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	s := NewServer(&config.Config{}, database)
	t.Cleanup(func() {
		s.cancel()
		s.manager.Shutdown(2 * time.Second)
		database.Close()
	})
	proj, err := database.GetOrCreateProject(repoDir)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	tk, err := database.CreateTaskWithPriority(
		proj.ID, "t", "in", "slug", "w", "", "", "", "",
		task.StatusPending, task.PriorityMedium, false, nil, nil,
	)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	stale, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s.checkPendingTasks()
		}()
	}
	close(start)
	wg.Wait()

	if got, err := database.GetTask(tk.ID); err != nil {
		t.Fatal(err)
	} else if got.Status == task.StatusPending {
		t.Fatal("task still pending after concurrent poller ticks")
	}
	if err := s.startTaskAgent(stale); !errors.Is(err, agent.ErrTaskAlreadyTracked) {
		t.Fatalf("direct start from stale pending snapshot: err = %v, want ErrTaskAlreadyTracked", err)
	}
	if !s.manager.IsTaskKnown(tk.ID) {
		t.Fatal("agent no longer tracked; stub too fast to probe a live re-start")
	}
	live, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.startTaskAgent(live); !errors.Is(err, agent.ErrTaskAlreadyTracked) {
		t.Fatalf("direct start while tracked: err = %v, want ErrTaskAlreadyTracked", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for s.manager.IsTaskKnown(tk.ID) {
		if time.Now().After(deadline) {
			t.Fatal("agent still tracked after 10s")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	out, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatalf("read run count: %v", err)
	}
	if n := strings.Count(string(out), "x\n"); n != 1 {
		t.Fatalf("step ran %d times, want 1", n)
	}
}
