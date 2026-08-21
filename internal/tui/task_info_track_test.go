package tui

import (
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/charmbracelet/lipgloss"
)

// TestTaskInfoView_ShowsTrack verifies the read-only Track: line renders for
// tracked tasks and that label alignment stays consistent (per the root
// CLAUDE.md rule, this is checked programmatically, not by reasoning alone).
func TestTaskInfoView_ShowsTrack(t *testing.T) {
	v := newTaskInfoView()
	v.SetSize(100, 50)
	v.SetTask(&daemon.TaskInfo{
		ID:       1,
		Title:    "Tracked task",
		Status:   "pending",
		Workflow: "implement",
		Track:    "payments-api",
	})

	output := v.renderMetadata()
	if !strings.Contains(output, "Track:") {
		t.Fatal("expected task info to show Track label")
	}
	if !strings.Contains(output, "payments-api") {
		t.Error("expected task info to show track slug")
	}

	// The rendered label cells (up to the value) must be uniform in width so
	// values line up in a column, and no line may exceed the view width.
	var trackLabelW, workflowLabelW int
	for _, line := range strings.Split(output, "\n") {
		if lipgloss.Width(line) > 100 {
			t.Errorf("line exceeds view width: %q", line)
		}
		plain := line
		if idx := strings.Index(plain, "payments-api"); strings.Contains(plain, "Track:") && idx >= 0 {
			trackLabelW = lipgloss.Width(line[:strings.Index(line, "payments-api")])
		}
		if idx := strings.Index(plain, "implement"); strings.Contains(plain, "Workflow:") && idx >= 0 {
			workflowLabelW = lipgloss.Width(line[:strings.Index(line, "implement")])
		}
	}
	if trackLabelW == 0 || workflowLabelW == 0 {
		t.Fatalf("failed to locate Track/Workflow lines (track=%d workflow=%d)", trackLabelW, workflowLabelW)
	}
	if trackLabelW != workflowLabelW {
		t.Errorf("Track label width %d != Workflow label width %d — values misaligned", trackLabelW, workflowLabelW)
	}
}

// TestTaskInfoView_TracklessOmitsTrackLine: no Track: line for trackless tasks.
func TestTaskInfoView_TracklessOmitsTrackLine(t *testing.T) {
	v := newTaskInfoView()
	v.SetSize(100, 50)
	v.SetTask(&daemon.TaskInfo{ID: 1, Title: "Plain task", Status: "pending"})

	if strings.Contains(v.renderMetadata(), "Track:") {
		t.Error("trackless task must not render a Track line")
	}
}
