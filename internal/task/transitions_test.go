package task

import (
	"errors"
	"testing"
)

var allStatuses = []Status{
	StatusPending, StatusInit, StatusRunning, StatusAwaitingApproval, StatusAwaitingChildren,
	StatusTmux, StatusFinalizing, StatusSummarizing, StatusSummarizingStep, StatusMergeBlocked,
	StatusResolvingConflicts, StatusCompleted, StatusFailed, StatusMergeFailed,
}

// allowedTransitions spells out every non-self transition independently of
// buildTransitions, so a change to the table must be mirrored here.
func allowedTransitions() map[[2]Status]bool {
	phase := []Status{StatusFinalizing, StatusSummarizing, StatusSummarizingStep, StatusMergeBlocked, StatusResolvingConflicts}
	terminal := []Status{StatusCompleted, StatusFailed, StatusMergeFailed}
	active := []Status{StatusRunning, StatusAwaitingApproval, StatusAwaitingChildren, StatusTmux, StatusFinalizing, StatusSummarizing, StatusSummarizingStep, StatusMergeBlocked, StatusResolvingConflicts}

	want := map[[2]Status]bool{}
	add := func(from Status, to ...Status) {
		for _, s := range to {
			if s != from {
				want[[2]Status{from, s}] = true
			}
		}
	}
	add(StatusInit, StatusPending, StatusTmux, StatusFailed)
	add(StatusPending, StatusRunning, StatusFailed)
	add(StatusRunning, StatusAwaitingApproval, StatusTmux, StatusAwaitingChildren)
	add(StatusRunning, phase...)
	add(StatusRunning, terminal...)
	add(StatusAwaitingApproval, StatusRunning)
	add(StatusAwaitingApproval, phase...)
	add(StatusAwaitingApproval, terminal...)
	add(StatusTmux, StatusRunning)
	add(StatusTmux, phase...)
	add(StatusTmux, terminal...)
	add(StatusAwaitingChildren, StatusPending)
	add(StatusAwaitingChildren, phase...)
	add(StatusAwaitingChildren, terminal...)
	for _, p := range phase {
		add(p, active...)
		add(p, terminal...)
	}
	add(StatusCompleted, StatusTmux)
	add(StatusMergeFailed, StatusTmux)
	return want
}

func TestCanTransitionMatchesTable(t *testing.T) {
	want := allowedTransitions()
	for _, from := range allStatuses {
		for _, to := range allStatuses {
			expected := from == to || want[[2]Status{from, to}]
			if got := CanTransition(from, to); got != expected {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, expected)
			}
		}
	}
}

func TestCanTransitionRejections(t *testing.T) {
	cases := [][2]Status{
		{StatusCompleted, StatusRunning},
		{StatusCompleted, StatusPending},
		{StatusCompleted, StatusFailed},
		{StatusFailed, StatusPending},
		{StatusFailed, StatusRunning},
		{StatusFailed, StatusTmux},
		{StatusFailed, StatusCompleted},
		{StatusMergeFailed, StatusRunning},
		{StatusMergeFailed, StatusCompleted},
		{StatusRunning, StatusPending},
		{StatusRunning, StatusInit},
		{StatusPending, StatusInit},
		{StatusPending, StatusTmux},
		{StatusPending, StatusAwaitingChildren},
		{StatusPending, StatusCompleted},
		{StatusInit, StatusRunning},
		{StatusTmux, StatusPending},
		{StatusAwaitingApproval, StatusPending},
		{StatusAwaitingChildren, StatusRunning},
	}
	for _, c := range cases {
		if CanTransition(c[0], c[1]) {
			t.Errorf("CanTransition(%s, %s) = true, want false", c[0], c[1])
		}
	}
}

func TestTerminalStatusesOnlyLeaveForTmux(t *testing.T) {
	for _, from := range allStatuses {
		if !from.IsTerminal() {
			continue
		}
		for _, to := range allStatuses {
			if to == from || to == StatusTmux {
				continue
			}
			if CanTransition(from, to) {
				t.Errorf("terminal %s must not move to %s", from, to)
			}
		}
	}
}

func TestActiveStatusesMatchIsActive(t *testing.T) {
	in := map[Status]bool{}
	for _, s := range activeStatuses {
		in[s] = true
	}
	for _, s := range allStatuses {
		if in[s] != s.IsActive() {
			t.Errorf("%s: in activeStatuses=%v, IsActive=%v", s, in[s], s.IsActive())
		}
	}
}

func TestAllowedFrom(t *testing.T) {
	for _, to := range allStatuses {
		from := AllowedFrom(to)
		if len(from) == 0 || from[0] != to {
			t.Fatalf("AllowedFrom(%s) must start with %s itself, got %v", to, to, from)
		}
		set := map[Status]bool{}
		for _, f := range from {
			set[f] = true
		}
		for _, f := range allStatuses {
			if set[f] != CanTransition(f, to) {
				t.Errorf("AllowedFrom(%s) contains %s = %v, CanTransition = %v", to, f, set[f], CanTransition(f, to))
			}
		}
	}
}

func TestTransitionErrorIs(t *testing.T) {
	var err error = &TransitionError{TaskID: 7, From: StatusFailed, To: StatusRunning}
	if !errors.Is(err, ErrIllegalTransition) {
		t.Error("TransitionError must match ErrIllegalTransition")
	}
	if got, want := err.Error(), "task #7: illegal status transition failed -> running"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
