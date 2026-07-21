package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Bakaface/sortie/internal/agent"
	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/db"
	"github.com/Bakaface/sortie/internal/task"
)

// These tests cover the fixes for the 2026-07-13 task #220 incident: two
// concurrent advance requests raced into StartAgent, the loser's error path
// rolled the task status back to tmux while the winner's agent was running,
// and the agent-completion handler then misread that status as an approval
// pause and skipped finalization, stranding the task.

// newAdvanceTestServer builds a Server against a real git repo carrying the
// given .sortie.yml (getProjectContext loads project config from disk, so an
// in-memory config would not be honored). The repo's HEAD is an empty commit
// so HasMeaningfulChanges is false and finalization fast-tracks to completed
// without spawning merge/summarizer subprocesses.
func newAdvanceTestServer(t *testing.T, sortieYML string) (*Server, *db.DB, int64) {
	t.Helper()
	repoDir := initRecoveryTestRepo(t)

	if err := os.WriteFile(filepath.Join(repoDir, ".sortie.yml"), []byte(sortieYML), 0644); err != nil {
		t.Fatalf("failed to write .sortie.yml: %v", err)
	}
	for _, args := range [][]string{
		// -f: the user's global excludes may ignore .sortie.yml; the repo must
		// end up clean with the config tracked so fast-track sees no changes.
		{"git", "add", "-f", "--", ".sortie.yml"},
		{"git", "commit", "-q", "-m", "add sortie config"},
		{"git", "commit", "-q", "--allow-empty", "-m", "empty"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v failed: %v\n%s", args, err, out)
		}
	}

	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	s := NewServer(&config.Config{}, database)
	t.Cleanup(func() { s.cancel() })
	t.Cleanup(func() { s.manager.Shutdown(2 * time.Second) })

	proj, err := database.GetOrCreateProject(repoDir)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	if _, err := s.getProjectContext(proj.ID); err != nil {
		t.Fatalf("failed to pre-load project context: %v", err)
	}
	return s, database, proj.ID
}

const twoStepConfigYML = `on_complete: none
workflows:
  - name: default
    steps:
      - name: research
        prompt: research
      - name: implement
        prompt: implement
`

const oneStepConfigYML = `on_complete: none
workflows:
  - name: default
    steps:
      - name: implement
        prompt: implement
`

func waitForTaskStatus(t *testing.T, database *db.DB, id int64, want task.Status, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tk, err := database.GetTask(id)
		if err == nil && tk.Status == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	tk, _ := database.GetTask(id)
	t.Fatalf("task #%d never reached status %s (last: %s)", id, want, tk.Status)
}

// A failed StartAgent inside an advance must NOT roll the status back to tmux
// when the failure is ErrTaskAlreadyTracked: the task genuinely is running
// under another agent, and restoring tmux would mark a running task as paused.
func TestAdvanceTmuxTask_NoRollbackWhenAgentAlreadyTracked(t *testing.T) {
	s, database, projID := newAdvanceTestServer(t, twoStepConfigYML)

	tk, err := database.CreateTaskWithPriority(
		projID, "Test task", "desc", "slug", "default", "", "branch", "", "",
		task.StatusTmux, task.PriorityMedium, false, nil, nil,
	)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	// Paused after step 0 (cursor invariant: StepIndex = next step), so the
	// advance takes the hasMoreSteps branch where the buggy rollback lived.
	if err := database.UpdateTaskStep(tk.ID, 1, ""); err != nil {
		t.Fatalf("failed to set step index: %v", err)
	}

	// Occupy the task with a blocked agent, simulating the winner of a
	// concurrent advance whose engine run is still in flight.
	unblock := make(chan struct{})
	_, err = s.manager.StartAgent(tk, t.TempDir(), func(ctx context.Context) error {
		<-unblock
		return errors.New("test teardown")
	})
	if err != nil {
		t.Fatalf("failed to pre-track agent: %v", err)
	}
	t.Cleanup(func() {
		close(unblock)
		deadline := time.Now().Add(2 * time.Second)
		for s.manager.IsTaskKnown(tk.ID) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	})

	_, advErr := s.advanceTmuxTask(tk)
	if advErr == nil {
		t.Fatal("expected advance to fail while another agent is tracked")
	}
	if !errors.Is(advErr, agent.ErrTaskAlreadyTracked) {
		t.Fatalf("expected ErrTaskAlreadyTracked, got: %v", advErr)
	}

	refreshed, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if refreshed.Status == task.StatusTmux {
		t.Fatalf("status rolled back to tmux while an agent is running (the bug)")
	}
	if refreshed.Status != task.StatusRunning {
		t.Errorf("expected status running (an agent is tracked), got %s", refreshed.Status)
	}
}

// Concurrent advances of the same task must be serialized: exactly one may
// perform the transition; the other must observe the post-transition status
// under the flow lock and fail cleanly without touching the task.
func TestAdvanceTmuxTask_ConcurrentAdvancesSerialized(t *testing.T) {
	s, database, projID := newAdvanceTestServer(t, oneStepConfigYML)

	tk, err := database.CreateTaskWithPriority(
		projID, "Test task", "desc", "slug", "default", "", "branch", "", "",
		task.StatusTmux, task.PriorityMedium, true, nil, nil,
	)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	// Paused after the LAST step: the advance takes the finalization branch,
	// and the clean repo makes it fast-track to completed with no agents.
	if err := database.UpdateTaskStep(tk.ID, 1, ""); err != nil {
		t.Fatalf("failed to set step index: %v", err)
	}
	proj, err := database.GetProject(projID)
	if err != nil {
		t.Fatalf("failed to get project: %v", err)
	}
	if err := database.UpdateTaskWorktreePath(tk.ID, proj.Path); err != nil {
		t.Fatalf("failed to set worktree path: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.advanceTmuxTask(tk)
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for _, e := range errs {
		if e == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("expected exactly 1 advance to succeed, got %d (errs: %v)", succeeded, errs)
	}

	waitForTaskStatus(t, database, tk.ID, task.StatusCompleted, 5*time.Second)
}

// An agent completion that finds a pause-looking status WITHOUT the engine's
// explicit pause signal must proceed to finalization: the status was
// overwritten out from under the agent, and trusting it strands the task.
func TestOnAgentStateChange_SpuriousPauseStatusFinalizes(t *testing.T) {
	s, database, projID := newAdvanceTestServer(t, oneStepConfigYML)

	tk, err := database.CreateTaskWithPriority(
		projID, "Test task", "desc", "slug", "default", "", "branch", "", "",
		task.StatusTmux, task.PriorityMedium, true, nil, nil,
	)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	proj, err := database.GetProject(projID)
	if err != nil {
		t.Fatalf("failed to get project: %v", err)
	}
	// Clean repo as worktree → finalization fast-tracks to completed without
	// spawning any subprocess.
	if err := database.UpdateTaskWorktreePath(tk.ID, proj.Path); err != nil {
		t.Fatalf("failed to set worktree path: %v", err)
	}
	tk.WorktreePath = proj.Path

	a := agent.New(tk, 10)
	s.onAgentStateChange(a, agent.StateRunning, agent.StateCompleted)

	waitForTaskStatus(t, database, tk.ID, task.StatusCompleted, 5*time.Second)
}

// The inverse: when the engine DID pause the task this run (signal present),
// the completion handler must treat it as an approval pause and leave the
// status alone.
func TestOnAgentStateChange_GenuinePauseSkipsFinalization(t *testing.T) {
	s, database, projID := newAdvanceTestServer(t, oneStepConfigYML)

	tk, err := database.CreateTaskWithPriority(
		projID, "Test task", "desc", "slug", "default", "", "branch", "", "",
		task.StatusTmux, task.PriorityMedium, true, nil, nil,
	)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	s.markEnginePaused(tk.ID)
	a := agent.New(tk, 10)
	s.onAgentStateChange(a, agent.StateRunning, agent.StateCompleted)

	// Finalization runs async when misclassified, so poll briefly to make
	// sure the status genuinely stays put.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		refreshed, err := database.GetTask(tk.ID)
		if err != nil {
			t.Fatalf("failed to get task: %v", err)
		}
		if refreshed.Status != task.StatusTmux {
			t.Fatalf("genuine pause was finalized: status became %s", refreshed.Status)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
