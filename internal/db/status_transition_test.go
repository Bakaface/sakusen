package db

import (
	"errors"
	"sync"
	"testing"

	"github.com/Bakaface/sakusen/internal/task"
)

var everyStatus = []task.Status{
	task.StatusPending, task.StatusInit, task.StatusRunning, task.StatusAwaitingApproval, task.StatusAwaitingChildren,
	task.StatusTmux, task.StatusFinalizing, task.StatusSummarizing, task.StatusSummarizingStep, task.StatusMergeBlocked,
	task.StatusResolvingConflicts, task.StatusCompleted, task.StatusFailed, task.StatusMergeFailed,
}

func TestUpdateTaskStatusAllowsEveryTableTransition(t *testing.T) {
	database := openTestDB(t)
	projID := mustProject(t, database, t.TempDir())
	for _, from := range everyStatus {
		for _, to := range everyStatus {
			if !task.CanTransition(from, to) {
				continue
			}
			tk := mustCreateTask(t, database, projID, string(from)+"->"+string(to), from)
			if err := database.UpdateTaskStatus(tk.ID, to); err != nil {
				t.Errorf("%s -> %s: %v", from, to, err)
				continue
			}
			got, err := database.GetTask(tk.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != to {
				t.Errorf("%s -> %s: status = %s", from, to, got.Status)
			}
		}
	}
}

func TestStatusWritesRejectIllegalTransitions(t *testing.T) {
	cases := []struct {
		name  string
		from  task.Status
		to    task.Status
		write func(d *DB, id int64) error
	}{
		{"completed to running", task.StatusCompleted, task.StatusRunning,
			func(d *DB, id int64) error { return d.UpdateTaskStatus(id, task.StatusRunning) }},
		{"failed to pending", task.StatusFailed, task.StatusPending,
			func(d *DB, id int64) error { return d.UpdateTaskStatus(id, task.StatusPending) }},
		{"merge-failed to completed", task.StatusMergeFailed, task.StatusCompleted,
			func(d *DB, id int64) error { return d.UpdateTaskStatus(id, task.StatusCompleted) }},
		{"completed to failed via UpdateTaskError", task.StatusCompleted, task.StatusFailed,
			func(d *DB, id int64) error { return d.UpdateTaskError(id, "late failure") }},
		{"failed to merge-blocked via UpdateTaskStatusError", task.StatusFailed, task.StatusMergeBlocked,
			func(d *DB, id int64) error { return d.UpdateTaskStatusError(id, task.StatusMergeBlocked, "blocked") }},
		{"pending to awaiting-children", task.StatusPending, task.StatusAwaitingChildren,
			func(d *DB, id int64) error { return d.UpdateTaskStatus(id, task.StatusAwaitingChildren) }},
		{"running to init", task.StatusRunning, task.StatusInit,
			func(d *DB, id int64) error { return d.UpdateTaskStatus(id, task.StatusInit) }},
		{"tmux to pending", task.StatusTmux, task.StatusPending,
			func(d *DB, id int64) error { return d.UpdateTaskStatus(id, task.StatusPending) }},
		{"failed to tmux via TransitionTaskStatus", task.StatusFailed, task.StatusTmux,
			func(d *DB, id int64) error { return d.TransitionTaskStatus(id, task.StatusFailed, task.StatusTmux) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			database := openTestDB(t)
			tk := mustCreateTask(t, database, mustProject(t, database, t.TempDir()), "t", c.from)

			err := c.write(database, tk.ID)
			var terr *task.TransitionError
			if !errors.As(err, &terr) {
				t.Fatalf("want *task.TransitionError, got %v", err)
			}
			if !errors.Is(err, task.ErrIllegalTransition) {
				t.Error("error must match task.ErrIllegalTransition")
			}
			if terr.TaskID != tk.ID || terr.From != c.from || terr.To != c.to {
				t.Errorf("error = %+v, want task %d %s -> %s", terr, tk.ID, c.from, c.to)
			}

			got, err := database.GetTask(tk.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != c.from || got.ErrorMessage != "" {
				t.Errorf("rejected write changed the row: status=%s error=%q", got.Status, got.ErrorMessage)
			}
		})
	}
}

func TestUpdateTaskStatusMissingTaskIsNoop(t *testing.T) {
	database := openTestDB(t)
	if err := database.UpdateTaskStatus(424242, task.StatusRunning); err != nil {
		t.Errorf("missing task: %v", err)
	}
}

func TestTransitionTaskStatusRequiresExpectedFrom(t *testing.T) {
	database := openTestDB(t)
	tk := mustCreateTask(t, database, mustProject(t, database, t.TempDir()), "t", task.StatusRunning)

	err := database.TransitionTaskStatus(tk.ID, task.StatusTmux, task.StatusRunning)
	var terr *task.TransitionError
	if !errors.As(err, &terr) {
		t.Fatalf("want *task.TransitionError, got %v", err)
	}
	if terr.From != task.StatusRunning {
		t.Errorf("From = %s, want the status actually found (running)", terr.From)
	}

	if err := database.TransitionTaskStatus(tk.ID, task.StatusRunning, task.StatusTmux); err != nil {
		t.Fatalf("running -> tmux: %v", err)
	}
	got, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != task.StatusTmux {
		t.Errorf("status = %s, want tmux", got.Status)
	}
}

// Two advance requests both observe a tmux task and race to resume it; only
// one may win, and the loser must learn the task already moved on rather than
// overwrite it.
func TestTransitionTaskStatusRaceHasOneWinner(t *testing.T) {
	database := openTestDB(t)
	projID := mustProject(t, database, t.TempDir())

	for i := 0; i < 50; i++ {
		tk := mustCreateTask(t, database, projID, "race", task.StatusTmux)

		start := make(chan struct{})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for w := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[w] = database.TransitionTaskStatus(tk.ID, task.StatusTmux, task.StatusRunning)
			}()
		}
		close(start)
		wg.Wait()

		wins := 0
		for _, err := range errs {
			if err == nil {
				wins++
				continue
			}
			var terr *task.TransitionError
			if !errors.As(err, &terr) || terr.From != task.StatusRunning {
				t.Fatalf("loser error = %v, want TransitionError from running", err)
			}
		}
		if wins != 1 {
			t.Fatalf("iteration %d: %d writers won, want exactly 1", i, wins)
		}
		got, err := database.GetTask(tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != task.StatusRunning {
			t.Fatalf("status = %s, want running", got.Status)
		}
	}
}

func TestStartMergeRetry(t *testing.T) {
	database := openTestDB(t)
	proj := mustProject(t, database, t.TempDir())

	tk := mustCreateTask(t, database, proj, "t", task.StatusFinalizing)
	if err := database.UpdateTaskStatusError(tk.ID, task.StatusMergeFailed, "verify failed"); err != nil {
		t.Fatal(err)
	}
	if err := database.StartMergeRetry(tk.ID); err != nil {
		t.Fatalf("StartMergeRetry: %v", err)
	}
	got, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != task.StatusFinalizing || got.ErrorMessage != "" {
		t.Errorf("status=%s error=%q, want finalizing with the old error cleared", got.Status, got.ErrorMessage)
	}

	for _, from := range []task.Status{task.StatusCompleted, task.StatusFailed, task.StatusRunning} {
		other := mustCreateTask(t, database, proj, "t", from)
		err := database.StartMergeRetry(other.ID)
		var terr *task.TransitionError
		if !errors.As(err, &terr) || terr.From != from {
			t.Errorf("StartMergeRetry from %s: want *task.TransitionError from %s, got %v", from, from, err)
		}
		if got, err := database.GetTask(other.ID); err != nil {
			t.Fatal(err)
		} else if got.Status != from {
			t.Errorf("rejected retry changed status %s to %s", from, got.Status)
		}
	}
}
