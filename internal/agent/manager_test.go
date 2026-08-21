package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/task"
)

// transitionEvent is the test-visible shape of a fired StateChangeCallback
// invocation. It mirrors the unexported stateTransition type without
// depending on it, since assertions only need the agent ID and the two
// states.
type transitionEvent struct {
	agentID  string
	oldState State
	newState State
}

// recordTransitions returns a StateChangeCallback that pushes every fired
// transition onto a channel, plus the channel to read them from. Buffered
// generously so producers (goroutines running inside the Manager) never
// block on a slow test consumer.
func recordTransitions() (StateChangeCallback, chan transitionEvent) {
	ch := make(chan transitionEvent, 64)
	cb := func(a *Agent, oldState, newState State) {
		ch <- transitionEvent{agentID: a.ID, oldState: oldState, newState: newState}
	}
	return cb, ch
}

// waitForTransition blocks until the next transition arrives or the timeout
// elapses. Channel-synchronized rather than sleep-and-poll: the test blocks
// on the channel itself instead of racing a fixed sleep against the
// Manager's internal goroutines. The timeout is only a safety net against a
// genuinely hung test.
func waitForTransition(t *testing.T, ch chan transitionEvent, timeout time.Duration) transitionEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(timeout):
		t.Fatal("timed out waiting for a state transition")
		return transitionEvent{}
	}
}

const testTransitionTimeout = 2 * time.Second

// blockingRunner returns a StartAgent runner that blocks until release is
// closed (or receives a value), then returns result. Used to hold an agent
// in StateRunning under deterministic test control.
func blockingRunner(release <-chan struct{}, result error) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		<-release
		return result
	}
}

func TestManager_ConcurrencyCapAndQueuePromotion(t *testing.T) {
	m := NewManager(2, 10)
	cb, transitions := recordTransitions()
	m.SetStateChangeCallback(cb)

	release1 := make(chan struct{})
	release2 := make(chan struct{})
	release3 := make(chan struct{})
	t.Cleanup(func() {
		// Drain any runners left blocked so their goroutines don't leak past
		// the test, whether or not the test reached the point of releasing
		// them itself.
		for _, ch := range []chan struct{}{release1, release2, release3} {
			select {
			case <-ch:
			default:
				close(ch)
			}
		}
	})

	tk1 := &task.Task{ID: 1}
	tk2 := &task.Task{ID: 2}
	tk3 := &task.Task{ID: 3}

	a1, err := m.StartAgent(tk1, "/workdir-1", blockingRunner(release1, nil))
	if err != nil {
		t.Fatalf("StartAgent(1): %v", err)
	}
	a2, err := m.StartAgent(tk2, "/workdir-2", blockingRunner(release2, nil))
	if err != nil {
		t.Fatalf("StartAgent(2): %v", err)
	}
	a3, err := m.StartAgent(tk3, "/workdir-3", blockingRunner(release3, nil))
	if err != nil {
		t.Fatalf("StartAgent(3): %v", err)
	}

	t.Run("cap respected: first two agents start, third queues", func(t *testing.T) {
		// StartAgent fires its Pending->Starting and Starting->Running
		// transitions synchronously (under the caller's goroutine, before
		// StartAgent returns) — see startAgentLocked. So by the time all
		// three StartAgent calls above returned, agent 1 and agent 2 have
		// each already fired both transitions; agent 3 fired none (it went
		// straight to the pending queue).
		seen := map[string]int{}
		for i := 0; i < 4; i++ {
			ev := waitForTransition(t, transitions, testTransitionTimeout)
			seen[ev.agentID]++
			if ev.agentID != a1.ID && ev.agentID != a2.ID {
				t.Fatalf("unexpected transition for agent %s while starting; expected only agents 1/2 to transition", ev.agentID)
			}
		}
		if seen[a1.ID] != 2 || seen[a2.ID] != 2 {
			t.Fatalf("expected exactly 2 transitions each for agents 1 and 2, got %v", seen)
		}

		if got := a1.GetState(); got != StateRunning {
			t.Errorf("agent 1 state = %v, want %v", got, StateRunning)
		}
		if got := a2.GetState(); got != StateRunning {
			t.Errorf("agent 2 state = %v, want %v", got, StateRunning)
		}
		if got := a3.GetState(); got != StatePending {
			t.Errorf("agent 3 state = %v, want %v (queued behind the cap)", got, StatePending)
		}
	})

	t.Run("queue promotion: freeing a slot starts the queued agent", func(t *testing.T) {
		close(release1) // agent 1's runner returns nil -> StateCompleted

		// agent 1: Running -> Completed
		ev := waitForTransition(t, transitions, testTransitionTimeout)
		if ev.agentID != a1.ID || ev.oldState != StateRunning || ev.newState != StateCompleted {
			t.Fatalf("expected agent 1 Running->Completed, got %+v", ev)
		}

		// agent 3 promoted from the queue: Pending -> Starting -> Running
		ev = waitForTransition(t, transitions, testTransitionTimeout)
		if ev.agentID != a3.ID || ev.oldState != StatePending || ev.newState != StateStarting {
			t.Fatalf("expected agent 3 Pending->Starting, got %+v", ev)
		}
		ev = waitForTransition(t, transitions, testTransitionTimeout)
		if ev.agentID != a3.ID || ev.oldState != StateStarting || ev.newState != StateRunning {
			t.Fatalf("expected agent 3 Starting->Running, got %+v", ev)
		}

		if got := a3.GetState(); got != StateRunning {
			t.Errorf("agent 3 state = %v, want %v after promotion", got, StateRunning)
		}
	})

	// Let the remaining two blocked runners finish so their goroutines exit
	// cleanly before the test returns.
	close(release2)
	close(release3)
	waitForTransition(t, transitions, testTransitionTimeout) // agent 2 completion
	waitForTransition(t, transitions, testTransitionTimeout) // agent 3 completion
}

func TestManager_StateTransitionsFireInOrder(t *testing.T) {
	m := NewManager(0, 10) // unlimited concurrency: no queueing to worry about
	cb, transitions := recordTransitions()
	m.SetStateChangeCallback(cb)

	done := make(chan struct{})
	runner := func(ctx context.Context) error {
		<-done
		return nil
	}

	tk := &task.Task{ID: 42}
	a, err := m.StartAgent(tk, "/workdir", runner)
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	ev := waitForTransition(t, transitions, testTransitionTimeout)
	if ev.agentID != a.ID || ev.oldState != StatePending || ev.newState != StateStarting {
		t.Fatalf("transition 1: expected Pending->Starting, got %+v", ev)
	}

	ev = waitForTransition(t, transitions, testTransitionTimeout)
	if ev.agentID != a.ID || ev.oldState != StateStarting || ev.newState != StateRunning {
		t.Fatalf("transition 2: expected Starting->Running, got %+v", ev)
	}

	close(done)

	ev = waitForTransition(t, transitions, testTransitionTimeout)
	if ev.agentID != a.ID || ev.oldState != StateRunning || ev.newState != StateCompleted {
		t.Fatalf("transition 3: expected Running->Completed, got %+v", ev)
	}

	if got := a.GetState(); got != StateCompleted {
		t.Errorf("final state = %v, want %v", got, StateCompleted)
	}
}

func TestManager_FailedRunnerTransitionsToFailed(t *testing.T) {
	m := NewManager(0, 10)
	cb, transitions := recordTransitions()
	m.SetStateChangeCallback(cb)

	wantErr := errors.New("boom")
	done := make(chan struct{})
	runner := func(ctx context.Context) error {
		<-done
		return wantErr
	}

	tk := &task.Task{ID: 7}
	a, err := m.StartAgent(tk, "/workdir", runner)
	if err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	waitForTransition(t, transitions, testTransitionTimeout) // Pending->Starting
	waitForTransition(t, transitions, testTransitionTimeout) // Starting->Running

	close(done)

	ev := waitForTransition(t, transitions, testTransitionTimeout)
	if ev.agentID != a.ID || ev.oldState != StateRunning || ev.newState != StateFailed {
		t.Fatalf("expected Running->Failed, got %+v", ev)
	}

	if got := a.GetState(); got != StateFailed {
		t.Errorf("final state = %v, want %v", got, StateFailed)
	}
	if a.Error != wantErr.Error() {
		t.Errorf("agent.Error = %q, want %q", a.Error, wantErr.Error())
	}
}

func TestManager_ShutdownWaitsForRunningAgentsToRespondToCancellation(t *testing.T) {
	m := NewManager(0, 10)
	// Poll fast so the test doesn't spend real wall-clock time waiting on
	// the default 500ms tick — this is the testability affordance added to
	// Manager specifically so Shutdown tests don't need a multi-second grace
	// period to observe a prompt return.
	m.shutdownPollInterval = 2 * time.Millisecond

	started := make(chan struct{})
	runner := func(ctx context.Context) error {
		close(started)
		<-ctx.Done() // responds to cancellation, as StopAgent/Shutdown expect
		return ctx.Err()
	}

	tk := &task.Task{ID: 99}
	if _, err := m.StartAgent(tk, "/workdir", runner); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}

	select {
	case <-started:
	case <-time.After(testTransitionTimeout):
		t.Fatal("runner never started")
	}

	if !m.hasActiveAgents() {
		t.Fatal("expected an active agent before Shutdown")
	}

	const gracePeriod = 300 * time.Millisecond
	shutdownStart := time.Now()
	m.Shutdown(gracePeriod)
	elapsed := time.Since(shutdownStart)

	if m.hasActiveAgents() {
		t.Error("expected no active agents after Shutdown — cancellation should have propagated")
	}
	// The runner responds to ctx.Done() immediately, so Shutdown should
	// return well before the full grace period elapses (bounded by roughly
	// one poll interval, with generous margin for scheduler jitter). A
	// runner that ignored cancellation would instead make Shutdown consume
	// the entire grace period.
	if elapsed >= gracePeriod {
		t.Errorf("Shutdown took %v, expected it to return well before the %v grace period", elapsed, gracePeriod)
	}
}
