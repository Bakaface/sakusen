package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/Bakaface/sakusen/internal/task"
)

// DefaultWaitPollInterval is how often WaitForTaskStatus re-reads every
// watched task. Some status transitions (merge-blocked, cascade-fail,
// shutdown fail) are never broadcast, so the poll is what catches them.
const DefaultWaitPollInterval = 5 * time.Second

// DefaultWaitStatuses are the statuses a waiter reacts to when none are
// given: the three terminal statuses plus merge-blocked, which is not
// terminal but needs attention.
var DefaultWaitStatuses = []task.Status{
	task.StatusCompleted,
	task.StatusFailed,
	task.StatusMergeFailed,
	task.StatusMergeBlocked,
}

var knownStatuses = []task.Status{
	task.StatusPending,
	task.StatusInit,
	task.StatusRunning,
	task.StatusAwaitingApproval,
	task.StatusAwaitingChildren,
	task.StatusTmux,
	task.StatusFinalizing,
	task.StatusSummarizing,
	task.StatusSummarizingStep,
	task.StatusMergeBlocked,
	task.StatusResolvingConflicts,
	task.StatusCompleted,
	task.StatusFailed,
	task.StatusMergeFailed,
}

// ParseWaitStatuses validates status names, de-duplicating them. An empty
// input yields DefaultWaitStatuses.
func ParseWaitStatuses(names []string) ([]task.Status, error) {
	if len(names) == 0 {
		return append([]task.Status(nil), DefaultWaitStatuses...), nil
	}
	seen := make(map[task.Status]bool, len(names))
	out := make([]task.Status, 0, len(names))
	for _, name := range names {
		s := task.Status(strings.TrimSpace(name))
		if !isKnownStatus(s) {
			valid := make([]string, len(knownStatuses))
			for i, k := range knownStatuses {
				valid[i] = string(k)
			}
			return nil, fmt.Errorf("unknown status %q (valid: %s)", name, strings.Join(valid, ", "))
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

func isKnownStatus(s task.Status) bool {
	for _, k := range knownStatuses {
		if s == k {
			return true
		}
	}
	return false
}

// WaitResult is the outcome of WaitForTaskStatus. Matched holds every watched
// task in a target status at the deciding check; Tasks holds the latest-known
// snapshot of every watched task, in input order.
type WaitResult struct {
	Matched  []daemon.TaskInfo `json:"matched"`
	Tasks    []daemon.TaskInfo `json:"tasks"`
	TimedOut bool              `json:"timed_out"`
}

// WaitForTaskStatus blocks until any of ids is in one of statuses (empty
// means DefaultWaitStatuses), or ctx ends. A ctx deadline is a normal result
// (TimedOut: true, nil error); cancellation returns ctx.Err(). An unknown
// task ID fails immediately. A pollInterval <= 0 uses
// DefaultWaitPollInterval.
//
// The receiver must be a dedicated client: this subscribes it and consumes
// every message on Messages() until it returns, so it must not be shared
// with other subscribers.
func (c *Client) WaitForTaskStatus(ctx context.Context, ids []int64, statuses []task.Status, pollInterval time.Duration) (*WaitResult, error) {
	ids = dedupeIDs(ids)
	if len(ids) == 0 {
		return nil, errors.New("at least one task ID is required")
	}
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("invalid task ID %d: must be positive", id)
		}
	}
	if len(statuses) == 0 {
		statuses = DefaultWaitStatuses
	}
	if pollInterval <= 0 {
		pollInterval = DefaultWaitPollInterval
	}

	watched := make(map[int64]bool, len(ids))
	for _, id := range ids {
		watched[id] = true
	}
	targets := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		targets[string(s)] = true
	}

	// Subscribe before the first read so no transition between the read and
	// the subscription is missed.
	if err := c.Subscribe(); err != nil {
		return nil, fmt.Errorf("subscribe: %w", err)
	}

	// Drain broadcasts on a separate goroutine so readLoop never blocks on a
	// full subChan while the main loop is mid-GetTask (which would deadlock
	// the RPC waiting on respChan).
	var (
		mu      sync.Mutex
		updates = map[int64]daemon.TaskInfo{}
	)
	notify := make(chan struct{}, 1)
	stopDrain := make(chan struct{})
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for {
			select {
			case msg := <-c.Messages():
				if msg.Type != daemon.MsgTaskUpdate {
					continue
				}
				var payload daemon.TaskUpdateResponse
				if err := msg.DecodePayload(&payload); err != nil || !watched[payload.Task.ID] {
					continue
				}
				mu.Lock()
				updates[payload.Task.ID] = payload.Task
				mu.Unlock()
				select {
				case notify <- struct{}{}:
				default:
				}
			case <-stopDrain:
				return
			case <-c.done:
				return
			}
		}
	}()
	defer func() {
		_ = c.Unsubscribe()
		close(stopDrain)
		<-drainDone
	}()

	snapshots := make(map[int64]daemon.TaskInfo, len(ids))
	poll := func() error {
		for _, id := range ids {
			t, err := c.GetTask(id)
			if err != nil {
				return fmt.Errorf("get task #%d: %w", id, err)
			}
			snapshots[id] = *t
		}
		return nil
	}
	result := func(timedOut bool) *WaitResult {
		r := &WaitResult{Matched: []daemon.TaskInfo{}, Tasks: make([]daemon.TaskInfo, 0, len(ids)), TimedOut: timedOut}
		for _, id := range ids {
			t := snapshots[id]
			r.Tasks = append(r.Tasks, t)
			if !timedOut && targets[t.Status] {
				r.Matched = append(r.Matched, t)
			}
		}
		return r
	}
	matched := func() bool {
		for _, id := range ids {
			if targets[snapshots[id].Status] {
				return true
			}
		}
		return false
	}

	if err := poll(); err != nil {
		return nil, err
	}
	if matched() {
		return result(false), nil
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-notify:
			mu.Lock()
			for id, t := range updates {
				snapshots[id] = t
			}
			clear(updates)
			mu.Unlock()
		case <-ticker.C:
			if err := poll(); err != nil {
				return nil, err
			}
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return result(true), nil
			}
			return nil, ctx.Err()
		}
		if matched() {
			return result(false), nil
		}
	}
}

func dedupeIDs(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
