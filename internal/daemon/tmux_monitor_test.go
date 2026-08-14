package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Bakaface/sortie/internal/db"
	"github.com/Bakaface/sortie/internal/task"
	"github.com/Bakaface/sortie/internal/tmux"
	"github.com/Bakaface/sortie/internal/workflow"
)

// These tests pin down maybeAutoAdvance's sentinel gating: the step-done
// sentinel file is the SOLE auto-advance signal for tasks paused in tmux
// state — no sentinel means no advance (generic tmux agents that never write
// one are manual-advance), a sentinel scoped to a different step must not
// trigger, and `human: true` steps consume the sentinel without advancing.

// tmuxStepConfigYML pins a one-step workflow to a tmux-mode agent, matching
// the paused-in-tmux state maybeAutoAdvance operates on.
const tmuxStepConfigYML = `on_complete: none
default_agent: pane
agents:
  pane:
    mode: tmux
    command: 'true'
workflows:
  - name: default
    steps:
      - name: implement
        prompt: implement
`

// tmuxHumanStepConfigYML is the same workflow with the step gated behind
// explicit user approval.
const tmuxHumanStepConfigYML = `on_complete: none
default_agent: pane
agents:
  pane:
    mode: tmux
    command: 'true'
workflows:
  - name: default
    steps:
      - name: implement
        prompt: implement
        human: true
`

// newTmuxPausedTask creates a StatusTmux task paused on the workflow's single
// "implement" step (cursor invariant: StepIndex is one past the paused step),
// with the clean repo root as its worktree so an advance fast-tracks to
// completed without spawning merge/summarizer subprocesses.
func newTmuxPausedTask(t *testing.T, database *db.DB, projID int64) *task.Task {
	t.Helper()
	tk, err := database.CreateTaskWithPriority(
		projID, "Tmux task", "input", "slug", "default", "", "branch", "", "",
		task.StatusTmux, task.PriorityMedium, true, nil, nil,
	)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
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
	tk.StepIndex = 1
	tk.WorktreePath = proj.Path
	return tk
}

// writeStepSentinel writes a turn-end sentinel for the given step into the
// worktree's step-done dir, using the "<prefix>-<timestamp>.json" filename
// shape production matches (see workflow/sentinel.go), and returns its path.
func writeStepSentinel(t *testing.T, worktreePath, stepName string) string {
	t.Helper()
	dir := workflow.StepDoneDir(worktreePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create step-done dir: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.json", workflow.SentinelPrefix(stepName), time.Now().UnixNano()))
	if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to write sentinel: %v", err)
	}
	return path
}

// TestMaybeAutoAdvance_NoSentinelStaysPut verifies that a paused tmux task
// with no step-done sentinel is left alone: tmux agents that never write a
// sentinel are manual-advance by design, and pane idleness must never be
// treated as completion.
func TestMaybeAutoAdvance_NoSentinelStaysPut(t *testing.T) {
	s, database, projID := newAdvanceTestServer(t, tmuxStepConfigYML)
	tk := newTmuxPausedTask(t, database, projID)

	s.maybeAutoAdvance(tk)

	refreshed, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if refreshed.Status != task.StatusTmux {
		t.Errorf("task must stay in tmux state without a sentinel, got %s", refreshed.Status)
	}
}

// TestMaybeAutoAdvance_SentinelAdvances verifies the positive path: a
// sentinel for the paused step triggers the advance (here the final step, so
// the clean repo fast-tracks to completed) and the sentinel is consumed.
func TestMaybeAutoAdvance_SentinelAdvances(t *testing.T) {
	if !tmux.IsAvailable() {
		t.Skip("tmux is not installed")
	}
	s, database, projID := newAdvanceTestServer(t, tmuxStepConfigYML)
	tk := newTmuxPausedTask(t, database, projID)

	sentinel := writeStepSentinel(t, tk.WorktreePath, "implement")

	s.maybeAutoAdvance(tk)

	waitForTaskStatus(t, database, tk.ID, task.StatusCompleted, 5*time.Second)
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Errorf("expected the sentinel to be consumed on advance (stat: %v)", err)
	}
}

// TestMaybeAutoAdvance_ForeignStepSentinelIgnored verifies sentinel scoping:
// a sentinel written for a DIFFERENT step name in the same worktree must
// neither advance the task nor be consumed.
func TestMaybeAutoAdvance_ForeignStepSentinelIgnored(t *testing.T) {
	s, database, projID := newAdvanceTestServer(t, tmuxStepConfigYML)
	tk := newTmuxPausedTask(t, database, projID)

	foreign := writeStepSentinel(t, tk.WorktreePath, "research")

	s.maybeAutoAdvance(tk)

	refreshed, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if refreshed.Status != task.StatusTmux {
		t.Errorf("a foreign step's sentinel must not advance the task, got status %s", refreshed.Status)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("a foreign step's sentinel must be left in place (stat: %v)", err)
	}
}

// TestMaybeAutoAdvance_HumanStepConsumesSentinelWithoutAdvancing verifies the
// approval gate: a `human: true` step never auto-advances, but its stray
// sentinel is consumed so it can't trigger an advance on a later attach.
func TestMaybeAutoAdvance_HumanStepConsumesSentinelWithoutAdvancing(t *testing.T) {
	s, database, projID := newAdvanceTestServer(t, tmuxHumanStepConfigYML)
	tk := newTmuxPausedTask(t, database, projID)

	sentinel := writeStepSentinel(t, tk.WorktreePath, "implement")

	s.maybeAutoAdvance(tk)

	refreshed, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if refreshed.Status != task.StatusTmux {
		t.Errorf("a human-gated step must not auto-advance, got status %s", refreshed.Status)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Errorf("expected the human step's sentinel to be consumed (stat: %v)", err)
	}
}
