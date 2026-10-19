package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/daemon"
	taskpkg "github.com/Bakaface/sakusen/internal/task"
	"github.com/charmbracelet/lipgloss"
)

func TestListView_RendersNotBeforeStatus(t *testing.T) {
	future := time.Now().Add(3 * time.Hour)
	past := time.Now().Add(-time.Hour)
	waiting := daemon.TaskInfo{ID: 1, Title: "Waiting task", Status: string(taskpkg.StatusPending), NotBefore: &future}
	elapsed := daemon.TaskInfo{ID: 2, Title: "Elapsed task", Status: string(taskpkg.StatusPending), NotBefore: &past}
	blocked := daemon.TaskInfo{ID: 3, Title: "Blocked task", Status: string(taskpkg.StatusPending), NotBefore: &future, BlockedBy: []int64{2}}

	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{waiting, elapsed, blocked})
	l.SetSize(100, 24)

	want := "pending (until " + fmtPeriodicTime(&future) + ")"
	if got := l.statusText(waiting); !strings.HasSuffix(got, want) {
		t.Errorf("future not_before: statusText = %q, want suffix %q", got, want)
	}
	if got := l.statusText(elapsed); strings.Contains(got, "until") || !strings.HasSuffix(got, "pending") {
		t.Errorf("elapsed not_before: statusText = %q, want plain pending", got)
	}
	if got := l.statusText(blocked); !strings.Contains(got, "pending (blocked)") {
		t.Errorf("blocked + not_before: statusText = %q, want blocked label to win", got)
	}

	output := l.View()
	if !strings.Contains(output, want) {
		t.Errorf("list view missing %q", want)
	}
	var widths []int
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "Waiting task") && !strings.Contains(line, "Elapsed task") && !strings.Contains(line, "Blocked task") {
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

func TestTaskInfoView_RendersNotBefore(t *testing.T) {
	at := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	v := newTaskInfoView()
	v.SetSize(100, 50)
	v.SetTask(&daemon.TaskInfo{ID: 1, Title: "Task", Status: string(taskpkg.StatusPending), NotBefore: &at})

	var line string
	for _, l := range strings.Split(v.renderMetadata(), "\n") {
		if w := lipgloss.Width(l); w > 100 {
			t.Errorf("line exceeds view width (%d): %q", w, l)
		}
		if strings.Contains(stripANSI(l), "Not before:") {
			line = stripANSI(l)
		}
	}
	if !strings.Contains(line, fmtPeriodicTime(&at)) {
		t.Errorf("Not before line = %q, want %q", line, fmtPeriodicTime(&at))
	}

	v.SetTask(&daemon.TaskInfo{ID: 1, Title: "Task", Status: string(taskpkg.StatusPending)})
	if strings.Contains(stripANSI(v.renderMetadata()), "Not before:") {
		t.Error("Not before row shown without a value")
	}
}
