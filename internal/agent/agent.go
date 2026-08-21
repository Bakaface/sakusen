package agent

import (
	"fmt"
	"sync"
	"time"

	"github.com/Bakaface/sakusen/internal/task"
)

// Agent is a concurrency-slot handle the Manager uses to track a running
// claude process for a task. It intentionally does NOT duplicate the task's
// own workflow-progress fields (step index, current step name, etc.) — those
// live authoritatively on Task, which every reader (the daemon, in
// particular) re-fetches from the DB rather than trusting a cached copy
// here. WorkDir is a genuine exception: it's the resolved directory the
// agent actually runs in (worktree path, or the repo root fallback when the
// task has no worktree), computed once by the caller of StartAgent before
// Task.WorktreePath may even be set — see poller.go's startTaskAgent.
type Agent struct {
	mu sync.RWMutex

	ID        string
	Task      *task.Task
	WorkDir   string
	State     State
	PID       int // Process ID of the spawned agent command
	StartedAt time.Time
	EndedAt   time.Time
	Error     string

	outputBuffer *RingBuffer
}

func New(t *task.Task, bufferSize int) *Agent {
	return &Agent{
		ID:           fmt.Sprintf("%d", t.ID),
		Task:         t,
		WorkDir:      t.WorktreePath,
		State:        StatePending,
		outputBuffer: NewRingBuffer(bufferSize),
	}
}

func (a *Agent) GetState() State {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.State
}

func (a *Agent) SetState(state State) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.State = state
	if state.IsTerminal() && a.EndedAt.IsZero() {
		a.EndedAt = time.Now()
	}
}

func (a *Agent) SetError(err string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Error = err
	a.State = StateFailed
	if a.EndedAt.IsZero() {
		a.EndedAt = time.Now()
	}
}

func (a *Agent) SetPID(pid int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.PID = pid
}

func (a *Agent) GetPID() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.PID
}

func (a *Agent) SetWorkDir(workDir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.WorkDir = workDir
}

func (a *Agent) AppendOutput(lines []string) {
	a.outputBuffer.Append(lines)
}

func (a *Agent) GetOutput(fromLine int) ([]string, int) {
	return a.outputBuffer.GetFrom(fromLine)
}

func (a *Agent) GetAllOutput() []string {
	return a.outputBuffer.GetAll()
}

func (a *Agent) Duration() time.Duration {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.StartedAt.IsZero() {
		return 0
	}

	if !a.EndedAt.IsZero() {
		return a.EndedAt.Sub(a.StartedAt)
	}

	return time.Since(a.StartedAt)
}
