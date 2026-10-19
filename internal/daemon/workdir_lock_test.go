package daemon

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
)

const workdirLockConfig = `on_complete: none
workflows:
  - name: session
    worktree: false
    steps:
      - name: talk
        prompt: "talk"
  - name: sync
    worktree: false
    exclusive_workdir: true
    steps:
      - name: run
        prompt: "run"
`

type workdirLockFixture struct {
	s        *Server
	database *db.DB
	projID   int64
}

func newWorkdirLockFixture(t *testing.T) *workdirLockFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	repoDir := initRecoveryTestRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, ".sakusen.yml"), []byte(workdirLockConfig), 0644); err != nil {
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
	return &workdirLockFixture{s: s, database: database, projID: proj.ID}
}

func (f *workdirLockFixture) task(t *testing.T, projID int64, workflow string, status task.Status, worktree bool) *task.Task {
	t.Helper()
	tk, err := f.database.CreateTaskWithPriority(
		projID, "t", "in", "slug", workflow, "", "", "", "",
		status, task.PriorityMedium, worktree, nil, nil,
	)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	// Past the only step, so a started task finishes without spawning an agent.
	if err := f.database.UpdateTaskStep(tk.ID, 1, ""); err != nil {
		t.Fatal(err)
	}
	return tk
}

func (f *workdirLockFixture) status(t *testing.T, id int64) task.Status {
	t.Helper()
	tk, err := f.database.GetTask(id)
	if err != nil {
		t.Fatal(err)
	}
	return tk.Status
}

func TestWorkdirLock_WaitsForHolderThenStarts(t *testing.T) {
	f := newWorkdirLockFixture(t)
	holder := f.task(t, f.projID, "session", task.StatusTmux, false)
	waiter := f.task(t, f.projID, "sync", task.StatusPending, false)

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	f.s.checkPendingTasks()
	f.s.checkPendingTasks()
	f.s.checkPendingTasks()

	if got := f.status(t, waiter.ID); got != task.StatusPending {
		t.Fatalf("waiter status = %s, want pending", got)
	}
	if n := strings.Count(buf.String(), "waiting for working tree, held by task #"); n != 1 {
		t.Errorf("wait logged %d times across ticks, want 1:\n%s", n, buf.String())
	}
	if got := f.s.workdirHeldBy(waiter.ID); got != holder.ID {
		t.Errorf("workdirHeldBy = %d, want %d", got, holder.ID)
	}
	tk, _ := f.database.GetTask(waiter.ID)
	if info := f.s.taskToInfo(tk); info.WorkdirHeldBy != holder.ID {
		t.Errorf("TaskInfo.WorkdirHeldBy = %d, want %d", info.WorkdirHeldBy, holder.ID)
	}

	if err := f.database.UpdateTaskStatus(holder.ID, task.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	f.s.checkPendingTasks()

	if got := f.status(t, waiter.ID); got == task.StatusPending {
		t.Fatal("waiter still pending after holder completed")
	}
	if !strings.Contains(buf.String(), "working tree free, starting") {
		t.Errorf("missing proceed log:\n%s", buf.String())
	}
	if got := f.s.workdirHeldBy(waiter.ID); got != 0 {
		t.Errorf("wait entry not cleared on start, got holder %d", got)
	}
}

func TestWorkdirLock_HolderStatuses(t *testing.T) {
	blocking := []task.Status{task.StatusRunning, task.StatusAwaitingApproval, task.StatusTmux, task.StatusMergeBlocked}
	for _, st := range blocking {
		t.Run(string(st), func(t *testing.T) {
			f := newWorkdirLockFixture(t)
			holder := f.task(t, f.projID, "session", st, false)
			if st == task.StatusTmux {
				now := time.Now()
				if err := f.database.SetTaskHibernatedAt(holder.ID, &now); err != nil {
					t.Fatal(err)
				}
			}
			waiter := f.task(t, f.projID, "sync", task.StatusPending, false)
			got, err := f.s.workdirHolder(waiter)
			if err != nil {
				t.Fatal(err)
			}
			if got == nil || got.ID != holder.ID {
				t.Errorf("holder = %v, want #%d", got, holder.ID)
			}
		})
	}
}

func TestWorkdirLock_NonHolders(t *testing.T) {
	cases := []struct {
		name     string
		status   task.Status
		worktree bool
		otherPrj bool
	}{
		{"pending", task.StatusPending, false, false},
		{"init", task.StatusInit, false, false},
		{"completed", task.StatusCompleted, false, false},
		{"failed", task.StatusFailed, false, false},
		{"worktree task", task.StatusRunning, true, false},
		{"other project", task.StatusRunning, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newWorkdirLockFixture(t)
			projID := f.projID
			if tc.otherPrj {
				other, err := f.database.GetOrCreateProject(initRecoveryTestRepo(t))
				if err != nil {
					t.Fatal(err)
				}
				projID = other.ID
			}
			f.task(t, projID, "session", tc.status, tc.worktree)
			waiter := f.task(t, f.projID, "sync", task.StatusPending, false)
			got, err := f.s.workdirHolder(waiter)
			if err != nil {
				t.Fatal(err)
			}
			if got != nil {
				t.Errorf("unexpected holder #%d (%s)", got.ID, got.Status)
			}
		})
	}
}

func TestWorkdirLock_OnlyOptedInWorktreeFalseTasksWait(t *testing.T) {
	f := newWorkdirLockFixture(t)
	f.task(t, f.projID, "session", task.StatusTmux, false)

	notOptedIn := f.task(t, f.projID, "session", task.StatusPending, false)
	if got, err := f.s.workdirHolder(notOptedIn); err != nil || got != nil {
		t.Errorf("task without exclusive_workdir gated: holder=%v err=%v", got, err)
	}
	inWorktree := f.task(t, f.projID, "sync", task.StatusPending, true)
	if got, err := f.s.workdirHolder(inWorktree); err != nil || got != nil {
		t.Errorf("worktree:true task gated: holder=%v err=%v", got, err)
	}
}

func TestWorkdirLock_SameTickOnlyOneStarts(t *testing.T) {
	f := newWorkdirLockFixture(t)
	a := f.task(t, f.projID, "sync", task.StatusPending, false)
	b := f.task(t, f.projID, "sync", task.StatusPending, false)

	f.s.checkPendingTasks()

	pendingCount := 0
	for _, id := range []int64{a.ID, b.ID} {
		if f.status(t, id) == task.StatusPending {
			pendingCount++
		}
	}
	if pendingCount != 1 {
		t.Errorf("%d of 2 opted-in tasks still pending after one tick, want exactly 1", pendingCount)
	}
}

func TestWorkdirLock_RelogsOnlyOnHolderChange(t *testing.T) {
	f := newWorkdirLockFixture(t)
	first := f.task(t, f.projID, "session", task.StatusTmux, false)
	waiter := f.task(t, f.projID, "sync", task.StatusPending, false)

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	f.s.checkPendingTasks()
	second := f.task(t, f.projID, "session", task.StatusRunning, false)
	if err := f.database.UpdateTaskStatus(first.ID, task.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	f.s.checkPendingTasks()
	f.s.checkPendingTasks()

	out := buf.String()
	if n := strings.Count(out, "waiting for working tree, held by task #"); n != 2 {
		t.Errorf("wait logged %d times, want 2 (start + one holder change):\n%s", n, out)
	}
	if want := fmt.Sprintf("held by task #%d", second.ID); !strings.Contains(out, want) {
		t.Errorf("missing %q after holder change:\n%s", want, out)
	}
	if got := f.s.workdirHeldBy(waiter.ID); got != second.ID {
		t.Errorf("workdirHeldBy = %d, want %d", got, second.ID)
	}
}

func TestWorkdirLock_WaitClearedWhenTaskLeavesPending(t *testing.T) {
	f := newWorkdirLockFixture(t)
	f.task(t, f.projID, "session", task.StatusTmux, false)
	waiter := f.task(t, f.projID, "sync", task.StatusPending, false)

	f.s.checkPendingTasks()
	if f.s.workdirHeldBy(waiter.ID) == 0 {
		t.Fatal("waiter not recorded as waiting")
	}

	if err := f.database.UpdateTaskStatus(waiter.ID, task.StatusFailed); err != nil {
		t.Fatal(err)
	}
	f.s.checkPendingTasks()
	if got := f.s.workdirHeldBy(waiter.ID); got != 0 {
		t.Errorf("wait entry survived task leaving pending, holder %d", got)
	}
	tk, _ := f.database.GetTask(waiter.ID)
	if info := f.s.taskToInfo(tk); info.WorkdirHeldBy != 0 {
		t.Errorf("TaskInfo.WorkdirHeldBy = %d for a task that is not waiting", info.WorkdirHeldBy)
	}
}
