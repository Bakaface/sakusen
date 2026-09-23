package tui

import (
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/daemon"
	taskpkg "github.com/Bakaface/sakusen/internal/task"
	"github.com/charmbracelet/lipgloss"
)

// Per the root CLAUDE.md rule, render output is verified programmatically.

func TestAwaitingChildren_HasDistinctIconAndStyle(t *testing.T) {
	icon := statusIconFor(taskpkg.StatusAwaitingChildren)
	if icon == statusIconFor(taskpkg.StatusPending) {
		t.Errorf("statusIconFor(awaiting-children) = %q, want an icon distinct from the default", icon)
	}
	for _, other := range []taskpkg.Status{
		taskpkg.StatusCompleted, taskpkg.StatusRunning, taskpkg.StatusAwaitingApproval,
		taskpkg.StatusTmux, taskpkg.StatusFailed, taskpkg.StatusMergeFailed,
		taskpkg.StatusFinalizing, taskpkg.StatusSummarizing, taskpkg.StatusSummarizingStep,
		taskpkg.StatusMergeBlocked, taskpkg.StatusResolvingConflicts,
	} {
		if statusIconFor(other) == icon {
			t.Errorf("statusIconFor(awaiting-children) = %q collides with %s", icon, other)
		}
	}
	if lipgloss.Width(icon) != 1 {
		t.Errorf("awaiting-children icon %q has width %d, want 1 (column alignment)", icon, lipgloss.Width(icon))
	}
	if _, ok := stateStyles[taskpkg.StatusAwaitingChildren]; !ok {
		t.Error("stateStyles has no entry for awaiting-children — it would fall back to normalStyle")
	}
}

// The list must name the state in human terms and say how many children are
// outstanding, rather than echoing the raw "awaiting-children" status.
func TestListView_RendersAwaitingChildrenStatus(t *testing.T) {
	parent := daemon.TaskInfo{
		ID:      1,
		Title:   "Parent task",
		Status:  string(taskpkg.StatusAwaitingChildren),
		WaitsOn: []int64{2, 3},
	}
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		parent,
		{ID: 2, Title: "Child one", Status: string(taskpkg.StatusRunning)},
		{ID: 3, Title: "Child two", Status: string(taskpkg.StatusPending)},
	})
	l.SetSize(100, 24)

	if got := l.statusText(parent); got != statusIconFor(taskpkg.StatusAwaitingChildren)+" waiting on 2 children" {
		t.Errorf("statusText(awaiting-children) = %q", got)
	}
	single := parent
	single.WaitsOn = []int64{2}
	if got := l.statusText(single); !strings.Contains(got, "waiting on 1 child") {
		t.Errorf("statusText with one child = %q, want it to read \"waiting on 1 child\"", got)
	}
	none := parent
	none.WaitsOn = nil
	if got := l.statusText(none); !strings.Contains(got, "waiting on children") {
		t.Errorf("statusText with no WaitsOn data = %q, want it to read \"waiting on children\"", got)
	}

	output := l.View()
	if !strings.Contains(output, "waiting on 2 children") {
		t.Error("expected task list to contain the awaiting-children label")
	}
	if !strings.Contains(output, statusIconFor(taskpkg.StatusAwaitingChildren)) {
		t.Errorf("expected task list to contain the %q icon for awaiting-children", statusIconFor(taskpkg.StatusAwaitingChildren))
	}
	if strings.Contains(output, string(taskpkg.StatusAwaitingChildren)) {
		t.Error("task list still shows the raw awaiting-children status string")
	}

	// Every task row must stay the same width: the status column is sized
	// from the widest label, so a longer label must not push one row out.
	var widths []int
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "Parent task") && !strings.Contains(line, "Child one") && !strings.Contains(line, "Child two") {
			continue
		}
		w := lipgloss.Width(line)
		if w > 100 {
			t.Errorf("task row exceeds view width (%d): %q", w, line)
		}
		widths = append(widths, w)
	}
	if len(widths) != 3 {
		t.Fatalf("expected 3 task rows, found %d", len(widths))
	}
	for _, w := range widths[1:] {
		if w != widths[0] {
			t.Errorf("task rows have non-uniform widths: %v", widths)
			break
		}
	}
}

// The detail panel must list the child task IDs the parent is suspended on,
// the same way it lists dependency blockers.
func TestTaskInfoView_RendersWaitingOnSection(t *testing.T) {
	v := newTaskInfoView()
	v.SetSize(100, 50)
	v.SetTask(&daemon.TaskInfo{
		ID:          1,
		Title:       "Parent task",
		Status:      string(taskpkg.StatusAwaitingChildren),
		CurrentStep: "fan-out",
		WaitsOn:     []int64{12, 13, 14},
	})

	output := v.renderMetadata()

	var waitingLine string
	for _, line := range strings.Split(output, "\n") {
		if w := lipgloss.Width(line); w > 100 {
			t.Errorf("line exceeds view width (%d): %q", w, line)
		}
		if strings.Contains(stripANSI(line), "Waiting on:") {
			waitingLine = stripANSI(line)
		}
	}
	if waitingLine == "" {
		t.Fatal("task info view has no \"Waiting on:\" section for an awaiting-children task")
	}
	if !strings.Contains(waitingLine, "#12, #13, #14") {
		t.Errorf("Waiting on line = %q, want the child IDs formatted like the Blocked by section", waitingLine)
	}

	// No WaitsOn data means no section at all (same as Blocked by).
	v.SetTask(&daemon.TaskInfo{ID: 1, Title: "Parent task", Status: string(taskpkg.StatusRunning)})
	if strings.Contains(stripANSI(v.renderMetadata()), "Waiting on:") {
		t.Error("task info view renders an empty \"Waiting on:\" section")
	}
}
