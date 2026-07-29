package tui

import (
	"strings"
	"testing"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/daemon"
	taskpkg "github.com/Bakaface/sortie/internal/task"
	"github.com/charmbracelet/lipgloss"
)

// Per the root CLAUDE.md rule, render output is verified programmatically.

func TestMergeFailed_HasIconAndStyle(t *testing.T) {
	if got := statusIconFor(taskpkg.StatusMergeFailed); got != "✗" {
		t.Errorf("statusIconFor(merge-failed) = %q, want %q", got, "✗")
	}
	if _, ok := stateStyles[taskpkg.StatusMergeFailed]; !ok {
		t.Error("stateStyles has no entry for merge-failed — it would render unstyled")
	}
}

// The list must show merge-failed as its own label (not "completed") so a
// user scanning the list can tell a landed change from an unmerged one.
func TestListView_RendersMergeFailedStatus(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Merge failed task", Status: string(taskpkg.StatusMergeFailed), CurrentStep: "implement"},
	})
	l.SetSize(100, 24)

	output := l.View()

	if !strings.Contains(output, "merge-failed") {
		t.Error("expected task list to contain the merge-failed label")
	}
	if !strings.Contains(output, "✗") {
		t.Error("expected task list to contain the ✗ icon for merge-failed")
	}
	// The task row must still fit the view: "merge-failed" is the longest
	// status label, so it is the one that could push the row over.
	found := false
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "Merge failed task") {
			continue
		}
		found = true
		if w := lipgloss.Width(line); w > 100 {
			t.Errorf("task row exceeds view width (%d): %q", w, line)
		}
	}
	if !found {
		t.Fatal("task row not found in rendered list")
	}
}

// A merge-failed blocker never becomes `completed`, so the dependency can
// never clear — the dependent must read as deadlocked, not merely blocked.
func TestListView_MergeFailedBlockerReadsAsDeadlocked(t *testing.T) {
	dependent := daemon.TaskInfo{ID: 2, Title: "Dependent", Status: string(taskpkg.StatusPending), BlockedBy: []int64{1}}
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Blocker", Status: string(taskpkg.StatusMergeFailed)},
		dependent,
	})
	l.SetSize(100, 24)

	if got := l.statusText(dependent); !strings.Contains(got, "deadlocked") {
		t.Errorf("statusText for task blocked by merge-failed = %q, want it to mention deadlocked", got)
	}
}

// merge-failed means all workflow steps ran to completion and only
// finalization failed, so every step must render ✓ — never ✗ on a step that
// actually succeeded. The reason belongs on the Error line instead.
func TestTaskInfoView_MergeFailedMarksAllStepsCompleted(t *testing.T) {
	v := newTaskInfoView()
	v.SetSize(100, 50)
	v.SetWorkflow(&config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{
			{Name: "research", Prompt: "research"},
			{Name: "implement", Prompt: "implement"},
		},
	})
	v.SetTask(&daemon.TaskInfo{
		ID:           1,
		Title:        "Merge failed task",
		Status:       string(taskpkg.StatusMergeFailed),
		Workflow:     "default",
		StepIndex:    2,
		ErrorMessage: "merge failed after 3 attempts: conflict resolution failed",
	})

	output := v.renderMetadata()

	if !strings.Contains(output, "merge failed after 3 attempts") {
		t.Error("expected the merge failure reason to be surfaced in the task info view")
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "research") || strings.Contains(line, "implement") {
			if strings.Contains(line, "✗") {
				t.Errorf("workflow step rendered as failed for merge-failed: %q", line)
			}
			if !strings.Contains(line, "✓") {
				t.Errorf("workflow step not rendered as completed for merge-failed: %q", line)
			}
		}
		if w := lipgloss.Width(line); w > 100 {
			t.Errorf("line exceeds view width (%d): %q", w, line)
		}
	}
}
