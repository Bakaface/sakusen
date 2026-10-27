package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Bakaface/sakusen/internal/task"
)

var (
	ErrTaskAlreadyTracked = errors.New("task already tracked")
	ErrAgentNotFound      = errors.New("agent not found")
	ErrNoWorkDir          = errors.New("task has no workdir")
)

type StateChangeCallback func(agent *Agent, oldState, newState State)

// stateTransition captures a state change to fire after releasing the mutex.
type stateTransition struct {
	agent    *Agent
	oldState State
	newState State
}

type Manager struct {
	mu             sync.RWMutex
	agents         map[string]*Agent
	knownTasks     map[int64]bool
	pendingQueue   []string
	pendingRunners map[string]func(ctx context.Context) error
	cancelFuncs    map[string]context.CancelFunc
	maxConcurrent  int
	bufferSize     int

	// branchHeld counts the parallel-branch slots each task holds; the first
	// borrows the task's own slot, every further one costs a unit of
	// maxConcurrent.
	branchHeld    map[int64]int
	branchWaiters []*branchWaiter

	onStateChange StateChangeCallback

	// shutdownPollInterval is how often Shutdown polls hasActiveAgents while
	// waiting for running agents to exit. Defaults to 500ms (set by
	// NewManager); tests in this package override the field directly after
	// construction to keep Shutdown tests fast and deterministic instead of
	// waiting out a real 500ms tick.
	shutdownPollInterval time.Duration
}

func NewManager(maxConcurrent, bufferSize int) *Manager {
	return &Manager{
		agents:               make(map[string]*Agent),
		knownTasks:           make(map[int64]bool),
		pendingQueue:         make([]string, 0),
		pendingRunners:       make(map[string]func(ctx context.Context) error),
		cancelFuncs:          make(map[string]context.CancelFunc),
		branchHeld:           make(map[int64]int),
		maxConcurrent:        maxConcurrent,
		bufferSize:           bufferSize,
		shutdownPollInterval: 500 * time.Millisecond,
	}
}

func (m *Manager) SetStateChangeCallback(cb StateChangeCallback) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onStateChange = cb
}

func (m *Manager) StartAgent(t *task.Task, workDir string, runner func(ctx context.Context) error) (*Agent, error) {
	m.mu.Lock()

	if m.knownTasks[t.ID] {
		m.mu.Unlock()
		return nil, ErrTaskAlreadyTracked
	}

	if workDir == "" {
		m.mu.Unlock()
		return nil, ErrNoWorkDir
	}

	agent := New(t, m.bufferSize)
	agent.WorkDir = workDir
	m.agents[agent.ID] = agent
	m.knownTasks[t.ID] = true

	var transitions []stateTransition
	if m.canStartMore() {
		transitions = m.startAgentLocked(agent, runner)
	} else {
		m.pendingRunners[agent.ID] = runner
		m.pendingQueue = append(m.pendingQueue, agent.ID)
	}

	m.mu.Unlock()
	m.fireTransitions(transitions)
	return agent, nil
}

func (m *Manager) canStartMore() bool {
	if m.maxConcurrent <= 0 {
		return true
	}

	activeCount := 0
	for _, agent := range m.agents {
		if agent.GetState().IsActive() {
			activeCount++
		}
	}

	return activeCount+m.branchExtraLocked() < m.maxConcurrent
}

type branchWaiter struct {
	taskID  int64
	granted chan struct{}
}

// branchExtraLocked is the number of slots held by branches beyond the one
// each task borrows from itself.
func (m *Manager) branchExtraLocked() int {
	extra := 0
	for _, held := range m.branchHeld {
		if held > 1 {
			extra += held - 1
		}
	}
	return extra
}

func (m *Manager) canGrantBranchLocked(taskID int64) bool {
	return m.maxConcurrent <= 0 || m.branchHeld[taskID] == 0 || m.canStartMore()
}

// AcquireBranchSlot blocks until taskID may run one more parallel branch
// under maxConcurrent, or ctx is done. Waiting branches are served before
// pending tasks. The returned release is idempotent.
func (m *Manager) AcquireBranchSlot(ctx context.Context, taskID int64) (func(), error) {
	m.mu.Lock()
	if m.branchHeld[taskID] == 0 || (len(m.branchWaiters) == 0 && m.canGrantBranchLocked(taskID)) {
		m.branchHeld[taskID]++
		m.mu.Unlock()
		return m.branchRelease(taskID), nil //nolint:contextcheck // a queued agent must not inherit the branch context
	}
	w := &branchWaiter{taskID: taskID, granted: make(chan struct{})}
	m.branchWaiters = append(m.branchWaiters, w)
	m.mu.Unlock()

	select {
	case <-w.granted:
		return m.branchRelease(taskID), nil //nolint:contextcheck // a queued agent must not inherit the branch context
	case <-ctx.Done():
	}

	m.mu.Lock()
	select {
	case <-w.granted:
		m.mu.Unlock()
		m.branchRelease(taskID)() //nolint:contextcheck // a queued agent must not inherit the branch context
		return nil, ctx.Err()
	default:
	}
	for i, q := range m.branchWaiters {
		if q == w {
			m.branchWaiters = append(m.branchWaiters[:i], m.branchWaiters[i+1:]...)
			break
		}
	}
	// A cancelled head waiter may have been blocking grantable ones behind it.
	transitions := m.releaseSlotLocked() //nolint:contextcheck // a queued agent must not inherit the waiter's context
	m.mu.Unlock()
	m.fireTransitions(transitions)
	return nil, ctx.Err()
}

func (m *Manager) branchRelease(taskID int64) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if m.branchHeld[taskID] <= 1 {
				delete(m.branchHeld, taskID)
			} else {
				m.branchHeld[taskID]--
			}
			transitions := m.releaseSlotLocked()
			m.mu.Unlock()
			m.fireTransitions(transitions)
		})
	}
}

// releaseSlotLocked hands freed capacity to waiting branches first, then to
// pending tasks.
func (m *Manager) releaseSlotLocked() []stateTransition {
	m.serveBranchWaitersLocked()
	return m.processQueueLocked()
}

func (m *Manager) serveBranchWaitersLocked() {
	for len(m.branchWaiters) > 0 {
		w := m.branchWaiters[0]
		if !m.canGrantBranchLocked(w.taskID) {
			return
		}
		m.branchHeld[w.taskID]++
		close(w.granted)
		m.branchWaiters = m.branchWaiters[1:]
	}
}

func (m *Manager) startAgentLocked(agent *Agent, runner func(ctx context.Context) error) []stateTransition {
	var transitions []stateTransition

	oldState := agent.GetState()
	agent.SetState(StateStarting)
	agent.StartedAt = time.Now()
	transitions = append(transitions, stateTransition{agent, oldState, StateStarting})

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelFuncs[agent.ID] = cancel

	agent.SetState(StateRunning)
	transitions = append(transitions, stateTransition{agent, StateStarting, StateRunning})

	go func() {
		err := runner(ctx)

		m.mu.Lock()

		oldState := agent.GetState()
		if err != nil {
			agent.SetError(err.Error())
		} else {
			agent.SetState(StateCompleted)
		}
		delete(m.cancelFuncs, agent.ID)
		delete(m.knownTasks, agent.Task.ID)

		newState := agent.GetState()
		queueTransitions := m.releaseSlotLocked() //nolint:contextcheck // a queued agent must not inherit the finished agent's context

		m.mu.Unlock()

		// Fire callbacks outside the lock
		m.fireTransitions([]stateTransition{{agent, oldState, newState}})
		m.fireTransitions(queueTransitions)
	}()

	return transitions
}

func (m *Manager) StopAgent(agentID string) error {
	m.mu.Lock()

	agent, exists := m.agents[agentID]
	if !exists {
		m.mu.Unlock()
		return ErrAgentNotFound
	}

	oldState := agent.GetState()
	if oldState.IsTerminal() {
		m.mu.Unlock()
		return nil
	}

	if cancel, exists := m.cancelFuncs[agentID]; exists {
		cancel()
		delete(m.cancelFuncs, agentID)
	}

	agent.SetState(StateStopped)
	delete(m.knownTasks, agent.Task.ID)

	var transitions []stateTransition
	transitions = append(transitions, stateTransition{agent, oldState, StateStopped})
	transitions = append(transitions, m.releaseSlotLocked()...)

	m.mu.Unlock()
	m.fireTransitions(transitions)

	return nil
}

func (m *Manager) processQueueLocked() []stateTransition {
	if len(m.pendingQueue) == 0 || len(m.branchWaiters) > 0 || !m.canStartMore() {
		return nil
	}

	agentID := m.pendingQueue[0]
	m.pendingQueue = m.pendingQueue[1:]

	if agent, exists := m.agents[agentID]; exists {
		if agent.GetState() == StatePending {
			if runner, ok := m.pendingRunners[agentID]; ok {
				delete(m.pendingRunners, agentID)
				return m.startAgentLocked(agent, runner)
			}
		}
	}
	return nil
}

// fireTransitions fires state change callbacks outside the mutex.
func (m *Manager) fireTransitions(transitions []stateTransition) {
	if m.onStateChange == nil {
		return
	}
	for _, t := range transitions {
		m.onStateChange(t.agent, t.oldState, t.newState)
	}
}

func (m *Manager) GetAgent(agentID string) (*Agent, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	agent, exists := m.agents[agentID]
	return agent, exists
}

func (m *Manager) GetAgentByTaskID(taskID int64) (*Agent, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, agent := range m.agents {
		if agent.Task.ID == taskID {
			return agent, true
		}
	}
	return nil, false
}

func (m *Manager) ListAgents() []*Agent {
	m.mu.RLock()
	defer m.mu.RUnlock()

	agents := make([]*Agent, 0, len(m.agents))
	for _, agent := range m.agents {
		agents = append(agents, agent)
	}
	return agents
}

func (m *Manager) IsTaskKnown(taskID int64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.knownTasks[taskID]
}

// Shutdown cancels all agents and waits up to gracePeriod for them to finish.
func (m *Manager) Shutdown(gracePeriod time.Duration) {
	m.mu.Lock()
	for id, cancel := range m.cancelFuncs {
		cancel()
		delete(m.cancelFuncs, id)
	}
	m.mu.Unlock()

	// Poll until all agents are done or grace period expires
	deadline := time.Now().Add(gracePeriod)
	for time.Now().Before(deadline) {
		if !m.hasActiveAgents() {
			return
		}
		time.Sleep(m.shutdownPollInterval)
	}
}

func (m *Manager) hasActiveAgents() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, a := range m.agents {
		if a.GetState().IsActive() {
			return true
		}
	}
	return false
}

func (m *Manager) GetOutput(agentID string, fromLine int) ([]string, int, error) {
	m.mu.RLock()
	agent, exists := m.agents[agentID]
	m.mu.RUnlock()

	if !exists {
		return nil, 0, ErrAgentNotFound
	}

	lines, total := agent.GetOutput(fromLine)
	return lines, total, nil
}
