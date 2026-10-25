package task

import (
	"errors"
	"fmt"
	"sort"
)

// Groups used to build the transition table below.
var (
	// phaseStatuses are the transient statuses the engine, merge coordinator
	// and finalization pipeline enter and later restore from. Each restore
	// writes back an in-memory "previous" status, so a phase can be entered
	// from, and can return to, any active status.
	phaseStatuses = []Status{StatusFinalizing, StatusSummarizing, StatusSummarizingStep, StatusMergeBlocked, StatusResolvingConflicts}
	// terminalStatuses are the outcomes any active run can end in.
	terminalStatuses = []Status{StatusCompleted, StatusFailed, StatusMergeFailed}
	// activeStatuses mirrors Status.IsActive.
	activeStatuses = []Status{StatusRunning, StatusAwaitingApproval, StatusAwaitingChildren, StatusTmux, StatusFinalizing, StatusSummarizing, StatusSummarizingStep, StatusMergeBlocked, StatusResolvingConflicts}
)

// transitions is the table of status changes the system performs through
// db.UpdateTaskStatus and its siblings (from → allowed to). A rewrite of the
// current status (from == to) is always allowed and not listed.
//
//	init               → pending (title refined, startup recovery)
//	                     tmux (tmux_direct setup)
//	                     failed (blocker failed)
//	pending            → running (claim)
//	                     failed (blocker failed, stopped agent's late failure)
//	running            → awaiting-approval, tmux (engine pause)
//	                     awaiting-children (step spawned children)
//	                     any phase, any terminal
//	awaiting-approval  → running (continue), any phase, any terminal
//	tmux               → running (advance/continue), any phase, any terminal
//	awaiting-children  → pending (all children terminal), any phase, any terminal
//	phase              → any active status (restore / next phase), any terminal
//	completed          → tmux (interactive continue session)
//	merge-failed       → tmux (interactive continue session)
//	                     finalizing (retry merge)
//	failed             → (none)
//
// where phase = finalizing | summarizing | summarizing_step | merge-blocked |
// resolving-conflicts. Out of a terminal status (and back to pending from an
// active one) a task otherwise moves only through the explicit reset paths
// (db.ResetTaskForRetry*, db.ResetTaskForContinue), which deliberately skip
// this table.
var transitions = buildTransitions()

func buildTransitions() map[Status]map[Status]bool {
	t := map[Status]map[Status]bool{}
	allow := func(from Status, to ...Status) {
		if t[from] == nil {
			t[from] = map[Status]bool{}
		}
		for _, s := range to {
			t[from][s] = true
		}
	}

	allow(StatusInit, StatusPending, StatusTmux, StatusFailed)
	allow(StatusPending, StatusRunning, StatusFailed)

	allow(StatusRunning, StatusAwaitingApproval, StatusTmux, StatusAwaitingChildren)
	allow(StatusAwaitingApproval, StatusRunning)
	allow(StatusTmux, StatusRunning)
	allow(StatusAwaitingChildren, StatusPending)
	for _, from := range []Status{StatusRunning, StatusAwaitingApproval, StatusTmux, StatusAwaitingChildren} {
		allow(from, phaseStatuses...)
		allow(from, terminalStatuses...)
	}
	for _, from := range phaseStatuses {
		allow(from, activeStatuses...)
		allow(from, terminalStatuses...)
	}

	allow(StatusCompleted, StatusTmux)
	allow(StatusMergeFailed, StatusTmux, StatusFinalizing)
	return t
}

// CanTransition reports whether a task may move from one status to another
// through an ordinary status write.
func CanTransition(from, to Status) bool {
	return from == to || transitions[from][to]
}

// AllowedFrom returns every status a task may move to `to` from, including
// `to` itself, in a stable order.
func AllowedFrom(to Status) []Status {
	from := []Status{to}
	for f, tos := range transitions {
		if f != to && tos[to] {
			from = append(from, f)
		}
	}
	sort.Slice(from[1:], func(i, j int) bool { return from[1+i] < from[1+j] })
	return from
}

// ErrIllegalTransition is matched (errors.Is) by every *TransitionError.
var ErrIllegalTransition = errors.New("illegal task status transition")

// TransitionError reports a status write rejected because the task's current
// status does not allow it.
type TransitionError struct {
	TaskID int64
	From   Status
	To     Status
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("task #%d: illegal status transition %s -> %s", e.TaskID, e.From, e.To)
}

func (e *TransitionError) Is(target error) bool {
	return target == ErrIllegalTransition
}
