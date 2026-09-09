package tui

import (
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/charmbracelet/lipgloss"
)

// parallelRenderWorkflow is a three-slot workflow whose middle slot is a
// four-branch parallel group — enough branches to cover every sub-row state.
func parallelRenderWorkflow() *config.WorkflowConfig {
	return &config.WorkflowConfig{
		Name: "reviewed",
		Steps: []config.StepConfig{
			{Name: "implement", Prompt: "i"},
			{
				Name: "review",
				Parallel: &config.ParallelConfig{
					Require: "any",
					Branches: []config.StepConfig{
						{Name: "review-opus", Agent: "claude:opus", Prompt: "a"},
						{Name: "review-codex", Agent: "codex", Prompt: "b"},
						{Name: "review-slow", Prompt: "c"},
						{Name: "review-late", Prompt: "d"},
					},
				},
			},
			{Name: "synthesize", Prompt: "s"},
		},
	}
}

// renderParallelTaskInfo builds the task info view and returns both the raw
// metadata block (for content assertions) and the full View() output (for
// width/structure assertions).
func renderParallelTaskInfo(t *testing.T, info *daemon.TaskInfo) (metadata, view string) {
	t.Helper()
	v := newTaskInfoView()
	v.SetSize(120, 40)
	v.SetWorkflow(parallelRenderWorkflow())
	v.SetTask(info)
	return v.renderMetadata(), v.View()
}

// TestTaskInfoView_ParallelGroupRunning renders a task sitting on the group
// with mixed branch states and verifies the badge, the sub-row icons, and that
// the current-step marker lands on the GROUP row, never on a branch.
func TestTaskInfoView_ParallelGroupRunning(t *testing.T) {
	metadata, view := renderParallelTaskInfo(t, &daemon.TaskInfo{
		ID:          7,
		Title:       "Reviewed task",
		Status:      "running",
		Workflow:    "reviewed",
		StepIndex:   1,
		CurrentStep: "review",
		BranchStatus: map[string]string{
			"review-opus":  "completed",
			"review-codex": "running",
			"review-slow":  "failed",
			// review-late deliberately absent → pending
		},
	})

	lines := strings.Split(metadata, "\n")
	find := func(substr string) string {
		t.Helper()
		for _, l := range lines {
			if strings.Contains(l, substr) {
				return l
			}
		}
		t.Fatalf("no rendered line contains %q\n%s", substr, metadata)
		return ""
	}

	groupRow := find("2. review ")
	if !strings.Contains(groupRow, "[parallel 1/4, require:any]") {
		t.Errorf("group row = %q, want the K/N + require badge", groupRow)
	}
	if !strings.Contains(groupRow, "●") {
		t.Errorf("group row = %q, want the active-step marker on the GROUP", groupRow)
	}

	wantIcons := map[string]string{
		"review-opus":  "✓",
		"review-codex": "●",
		"review-slow":  "✗",
		"review-late":  "○",
	}
	for name, icon := range wantIcons {
		row := find(" " + name)
		if !strings.HasPrefix(strings.TrimLeft(stripANSI(row), " "), icon) {
			t.Errorf("branch row for %q = %q, want it to start with %q", name, row, icon)
		}
		if !strings.HasPrefix(stripANSI(row), "      ") {
			t.Errorf("branch row for %q = %q, want it indented under the group", name, row)
		}
		// The current-step marker must never land on a branch: only
		// review-codex (running) legitimately carries ●.
		if name != "review-codex" && strings.Contains(stripANSI(row), "●") {
			t.Errorf("branch row for %q = %q must not carry the active marker", name, row)
		}
	}
	if !strings.Contains(find(" review-opus"), "[agent:claude:opus]") {
		t.Error("branch sub-row must show its explicit agent")
	}
	// This panel has no agent registry, so a branch with no explicit agent
	// (and no group agent to inherit) shows no tag — the same rule the
	// top-level step rows already follow.
	if strings.Contains(find(" review-slow"), "[agent:") {
		t.Error("a branch with no explicit agent must not show a re-derived default")
	}

	assertViewWidth(t, view, 120)
	assertUniformFrameWidth(t, view)
}

// TestTaskInfoView_ParallelGroupCompleted verifies the K/N badge stays on the
// row after the task finishes — the count of branches that actually produced
// output is otherwise invisible once the group's icon turns ✓.
func TestTaskInfoView_ParallelGroupCompleted(t *testing.T) {
	metadata, view := renderParallelTaskInfo(t, &daemon.TaskInfo{
		ID:        8,
		Title:     "Reviewed task",
		Status:    "completed",
		Workflow:  "reviewed",
		StepIndex: 2,
		BranchStatus: map[string]string{
			"review-opus":  "completed",
			"review-codex": "completed",
			"review-slow":  "failed",
			"review-late":  "completed",
		},
	})

	var groupRow string
	for _, l := range strings.Split(metadata, "\n") {
		if strings.Contains(l, "2. review ") {
			groupRow = l
		}
	}
	if groupRow == "" {
		t.Fatalf("group row not rendered:\n%s", metadata)
	}
	if !strings.Contains(groupRow, "[parallel 3/4, require:any]") {
		t.Errorf("completed group row = %q, want the final K/N badge", groupRow)
	}
	if !strings.Contains(groupRow, "✓") {
		t.Errorf("completed group row = %q, want the completed icon", groupRow)
	}

	assertViewWidth(t, view, 120)
}

// TestTaskInfoView_ParallelGroupWithoutBranchStatus proves the panel is safe
// against a client that has not yet received BranchStatus (nil map): every
// branch reads pending and nothing panics.
func TestTaskInfoView_ParallelGroupWithoutBranchStatus(t *testing.T) {
	metadata, view := renderParallelTaskInfo(t, &daemon.TaskInfo{
		ID: 9, Title: "Reviewed task", Status: "pending", Workflow: "reviewed",
	})
	if !strings.Contains(metadata, "[parallel 0/4, require:any]") {
		t.Errorf("group row badge missing with a nil BranchStatus:\n%s", metadata)
	}
	if n := strings.Count(stripANSI(metadata), "○ review-"); n != 4 {
		t.Errorf("pending branch sub-rows = %d, want 4", n)
	}
	assertViewWidth(t, view, 120)
}

// assertViewWidth checks that no line of the scrollable content region
// exceeds the view width — the repo's non-interactive rendering rule (see root
// CLAUDE.md), which cannot be established by reasoning about the layout math
// alone. The help footer is excluded: it is a single pre-existing long line
// that this feature does not touch.
func assertViewWidth(t *testing.T, view string, width int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		if strings.Contains(line, "ctrl+h help") {
			continue
		}
		if w := lipgloss.Width(line); w > width {
			t.Errorf("line %d width %d exceeds view width %d: %q", i, w, width, line)
		}
	}
}

// assertUniformFrameWidth checks that every bordered viewport line renders at
// the same width, which is what actually keeps the frame from tearing.
func assertUniformFrameWidth(t *testing.T, view string) {
	t.Helper()
	widths := map[int]int{}
	for _, line := range strings.Split(view, "\n") {
		if !strings.HasPrefix(stripANSI(line), "│") {
			continue
		}
		widths[lipgloss.Width(line)]++
	}
	if len(widths) > 1 {
		t.Errorf("bordered lines have inconsistent widths: %v", widths)
	}
}

// stripANSI removes SGR escape sequences so tests can assert on the plain text
// of a styled line.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// TestStepSelectorNestsBranchRows verifies the artifact/step-context selector
// labels branch rows as nested under their group, and that failed rows are
// selectable (they open to a placeholder) but not editable.
func TestStepSelectorNestsBranchRows(t *testing.T) {
	group := daemon.TaskStepDetail{Name: "review", Status: "completed", Context: "agg"}
	branch := daemon.TaskStepDetail{Name: "review-opus", Parent: "review", Status: "completed", Context: "a"}
	failed := daemon.TaskStepDetail{Name: "review-codex", Parent: "review", Status: "failed"}

	if got := stepSelectorLabel(group); got != "✓ review" {
		t.Errorf("group label = %q", got)
	}
	if got := stepSelectorLabel(branch); got != "✓   └ review-opus" {
		t.Errorf("branch label = %q, want it nested under the group", got)
	}
	if got := stepSelectorLabel(failed); got != "✗   └ review-codex (failed)" {
		t.Errorf("failed branch label = %q", got)
	}
	if !stepIsActionable(failed) {
		t.Error("a failed branch row must be viewable (it opens to a placeholder)")
	}
	if stepIsEditable(failed) {
		t.Error("a failed branch row has no context and must not be editable")
	}
	if body := renderStepBody(failed); !strings.Contains(body, "Branch failed") {
		t.Errorf("failed branch body = %q, want the failure placeholder", body)
	}
}

// TestTopLevelStepsFiltersBranches pins the retry picker's input: branches are
// not retry targets (the daemon rejects them), so they never reach the list.
func TestTopLevelStepsFiltersBranches(t *testing.T) {
	steps := []daemon.TaskStepDetail{
		{Name: "implement"},
		{Name: "review"},
		{Name: "review-opus", Parent: "review"},
		{Name: "review-codex", Parent: "review"},
		{Name: "synthesize"},
	}
	got := topLevelSteps(steps)
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	want := "implement,review,synthesize"
	if strings.Join(names, ",") != want {
		t.Errorf("topLevelSteps = %v, want %s", names, want)
	}
}

// TestTasksLoadedRefreshesOpenTaskInfo pins the tick-driven refresh path:
// parallel branch rows change status through engine-side DB writes that emit
// no broadcast, so the periodic task list refresh is what keeps an open task
// info panel's K/N badge and sub-rows current.
func TestTasksLoadedRefreshesOpenTaskInfo(t *testing.T) {
	m := Model{view: viewTaskInfo, taskInfo: newTaskInfoView()}
	m.taskInfo.SetSize(120, 40)
	m.taskInfo.SetWorkflow(parallelRenderWorkflow())
	m.taskInfo.SetTask(&daemon.TaskInfo{ID: 7, Title: "Reviewed task", Status: "running", Workflow: "reviewed"})

	updated, _ := m.Update(tasksLoadedMsg{{
		ID: 7, Title: "Reviewed task", Status: "running", Workflow: "reviewed",
		BranchStatus: map[string]string{"review-opus": "completed", "review-codex": "running"},
	}})
	got := updated.(Model)

	if !strings.Contains(got.taskInfo.renderMetadata(), "[parallel 1/4, require:any]") {
		t.Errorf("task info panel did not pick up the refreshed BranchStatus:\n%s", got.taskInfo.renderMetadata())
	}
}
