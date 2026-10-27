package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/daemon"
	tea "github.com/charmbracelet/bubbletea"
)

func fanoutRenderWorkflow() *config.WorkflowConfig {
	return &config.WorkflowConfig{
		Name: "fanned",
		Steps: []config.StepConfig{
			{Name: "plan", Prompt: "p"},
			{Name: "units", Fanout: &config.FanoutConfig{From: "plan", Workflow: "unit", Require: "any"}},
			{Name: "next", Prompt: "n"},
		},
	}
}

func fanoutTask(id int64) *daemon.TaskInfo {
	return &daemon.TaskInfo{
		ID: id, Title: "Fanned task", Status: "awaiting-children",
		Workflow: "fanned", StepIndex: 1, CurrentStep: "units",
	}
}

func renderFanoutTaskInfo(t *testing.T, children []daemon.FanoutChildInfo) (metadata, view string) {
	t.Helper()
	v := newTaskInfoView()
	v.SetSize(120, 80)
	v.SetWorkflow(fanoutRenderWorkflow())
	v.SetTask(fanoutTask(5))
	v.SetFanoutChildren(children)
	return stripANSI(v.renderMetadata()), v.View()
}

// childRowIndex returns the metadata line index of the sub-row for "<slug> #<id> <status>".
func childRowIndex(t *testing.T, lines []string, c daemon.FanoutChildInfo) int {
	t.Helper()
	want := fmt.Sprintf("%s #%d %s", c.Slug, c.TaskID, c.Status)
	for i, l := range lines {
		if strings.HasPrefix(l, "      ") && strings.HasSuffix(strings.TrimRight(l, " "), want) {
			return i
		}
	}
	return -1
}

func TestTaskInfoView_FanoutRowsOrdered(t *testing.T) {
	children := []daemon.FanoutChildInfo{
		{Step: "units", Slug: "alpha", TaskID: 11, Status: "completed"},
		{Step: "units", Slug: "beta", TaskID: 12, Status: "failed"},
		{Step: "units", Slug: "gamma", TaskID: 13, Status: "running"},
		{Step: "other", Slug: "stray", TaskID: 99, Status: "failed"},
	}
	metadata, view := renderFanoutTaskInfo(t, children)
	lines := strings.Split(metadata, "\n")

	var stepRow string
	for _, l := range lines {
		if strings.Contains(l, "2. units") {
			stepRow = l
		}
	}
	if !strings.Contains(stepRow, "[fanout 1/3, require:any →unit]") {
		t.Errorf("fanout step row = %q, want the K/N badge", stepRow)
	}

	completed := childRowIndex(t, lines, children[0])
	failed := childRowIndex(t, lines, children[1])
	running := childRowIndex(t, lines, children[2])
	if completed < 0 || failed < 0 || running < 0 {
		t.Fatalf("missing child sub-row (completed=%d failed=%d running=%d)\n%s", completed, failed, running, metadata)
	}
	if failed > running || running > completed {
		t.Errorf("sub-row order failed=%d running=%d completed=%d, want failed < running < completed", failed, running, completed)
	}
	if childRowIndex(t, lines, children[3]) >= 0 {
		t.Error("a child of another step must not render under units")
	}

	assertViewWidth(t, view, 120)
	assertUniformFrameWidth(t, view)
}

func TestTaskInfoView_FanoutRowsCapped(t *testing.T) {
	var children []daemon.FanoutChildInfo
	var mustShow []daemon.FanoutChildInfo
	for i := range 50 {
		status := "completed"
		switch i % 10 {
		case 3:
			status = "failed"
		case 7:
			status = "running"
		}
		c := daemon.FanoutChildInfo{Step: "units", Slug: fmt.Sprintf("unit-%02d", i), TaskID: int64(100 + i), Status: status}
		children = append(children, c)
		if status != "completed" {
			mustShow = append(mustShow, c)
		}
	}
	metadata, view := renderFanoutTaskInfo(t, children)
	lines := strings.Split(metadata, "\n")

	rows := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "      ") && strings.Contains(l, " unit-") {
			rows++
		}
	}
	if rows != maxFanoutSubRows {
		t.Errorf("child sub-rows = %d, want %d", rows, maxFanoutSubRows)
	}
	if !strings.Contains(metadata, "      +30 more") {
		t.Errorf("missing +30 more line\n%s", metadata)
	}
	if !strings.Contains(stripANSI(view), "+30 more") {
		t.Error("View() output lacks the +30 more line")
	}
	for _, c := range mustShow {
		if childRowIndex(t, lines, c) < 0 {
			t.Errorf("%s child %s not among the shown rows", c.Status, c.Slug)
		}
	}

	assertViewWidth(t, view, 120)
	assertUniformFrameWidth(t, view)
}

func TestTaskInfoView_FanoutChildrenSurviveSameTaskRefresh(t *testing.T) {
	v := newTaskInfoView()
	v.SetSize(120, 40)
	v.SetWorkflow(fanoutRenderWorkflow())
	v.SetTask(fanoutTask(5))
	v.SetFanoutChildren([]daemon.FanoutChildInfo{{Step: "units", Slug: "alpha", TaskID: 11, Status: "running"}})

	v.SetTask(fanoutTask(5))
	if len(v.fanoutChildren) != 1 {
		t.Fatalf("same-task SetTask dropped children: %v", v.fanoutChildren)
	}
	v.SetTask(fanoutTask(6))
	if len(v.fanoutChildren) != 0 {
		t.Fatalf("switching task kept children: %v", v.fanoutChildren)
	}
}

func TestTaskInfo_OpenFetchesFanoutChildren(t *testing.T) {
	want := []daemon.FanoutChildInfo{{Step: "units", Slug: "alpha", TaskID: 11, Status: "running"}}
	var called []int64
	svc := &fakeTaskService{getTask: func(id int64) (*daemon.TaskInfo, error) {
		called = append(called, id)
		info := *fanoutTask(id)
		info.FanoutChildren = want
		return &info, nil
	}}
	m := Model{
		keys:     newKeyMap(),
		client:   svc,
		list:     newListView(false, ""),
		detail:   newDetailView(),
		taskInfo: newTaskInfoView(),
		view:     viewList,
		cfg: &config.Config{Workflows: []config.WorkflowConfig{
			*fanoutRenderWorkflow(),
			{Name: "plain", Steps: []config.StepConfig{{Name: "run", Prompt: "r"}}},
		}},
	}
	m.list.SetTasks([]daemon.TaskInfo{*fanoutTask(5)})

	result, cmd := m.handleListKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("opening a fanout task's info returned no fetch cmd")
	}
	result, _ = result.(Model).Update(cmd())
	got := result.(Model)
	if len(called) != 1 || called[0] != 5 {
		t.Fatalf("GetTask calls = %v, want [5]", called)
	}
	if len(got.taskInfo.fanoutChildren) != 1 || got.taskInfo.fanoutChildren[0].Slug != "alpha" {
		t.Fatalf("children not stored: %v", got.taskInfo.fanoutChildren)
	}

	// A result for a task no longer shown is dropped.
	result, _ = got.Update(taskFanoutLoadedMsg{taskID: 6, children: nil})
	if len(result.(Model).taskInfo.fanoutChildren) != 1 {
		t.Error("stale result for another task overwrote children")
	}

	plain := daemon.TaskInfo{ID: 9, Status: "running", Workflow: "plain"}
	if cmd := m.loadFanoutChildren(&plain); cmd != nil {
		t.Error("a task without a fanout step must not trigger GetTask")
	}
}
