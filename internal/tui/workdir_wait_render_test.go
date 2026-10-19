package tui

import (
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/daemon"
	taskpkg "github.com/Bakaface/sakusen/internal/task"
	"github.com/charmbracelet/lipgloss"
)

func TestListView_RendersWorkdirBusyStatus(t *testing.T) {
	waiting := daemon.TaskInfo{
		ID:            2,
		Title:         "Nightly sync",
		Status:        string(taskpkg.StatusPending),
		WorkdirHeldBy: 1,
	}
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Session", Status: string(taskpkg.StatusAwaitingApproval)},
		waiting,
		{ID: 3, Title: "Plain pending", Status: string(taskpkg.StatusPending)},
	})
	l.SetSize(100, 24)

	if got := l.statusText(waiting); !strings.HasSuffix(got, "pending (workdir busy #1)") {
		t.Errorf("statusText(waiting) = %q", got)
	}
	blocked := waiting
	blocked.BlockedBy = []int64{1}
	if got := l.statusText(blocked); !strings.HasSuffix(got, "pending (blocked)") {
		t.Errorf("dependency blocking must take precedence, got %q", got)
	}

	output := l.View()
	if !strings.Contains(output, "pending (workdir busy #1)") {
		t.Error("task list has no workdir-busy label")
	}
	var widths []int
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "Session") && !strings.Contains(line, "Nightly sync") && !strings.Contains(line, "Plain pending") {
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

func TestTaskInfoView_RendersWorkdirHeldBy(t *testing.T) {
	v := newTaskInfoView()
	v.SetSize(100, 50)
	v.SetTask(&daemon.TaskInfo{
		ID:            2,
		Title:         "Nightly sync",
		Status:        string(taskpkg.StatusPending),
		WorkdirHeldBy: 7,
	})

	var heldLine string
	for _, line := range strings.Split(v.renderMetadata(), "\n") {
		if w := lipgloss.Width(line); w > 100 {
			t.Errorf("line exceeds view width (%d): %q", w, line)
		}
		if strings.Contains(stripANSI(line), "Workdir held by:") {
			heldLine = stripANSI(line)
		}
	}
	if !strings.Contains(heldLine, "#7") {
		t.Fatalf("Workdir held by line = %q, want it to name #7", heldLine)
	}

	v.SetTask(&daemon.TaskInfo{ID: 2, Title: "Nightly sync", Status: string(taskpkg.StatusPending)})
	if strings.Contains(stripANSI(v.renderMetadata()), "Workdir held by:") {
		t.Error("task info view renders a Workdir held by line for a task that is not waiting")
	}
}
