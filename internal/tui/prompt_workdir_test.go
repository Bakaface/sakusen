package tui

import (
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/charmbracelet/lipgloss"
)

func plainLines(view string) []string {
	lines := strings.Split(view, "\n")
	for i, l := range lines {
		lines[i] = ansiRegex.ReplaceAllString(l, "")
	}
	return lines
}

// TestPromptView_WorkdirFieldRendered verifies the Workdir row sits under Slug,
// shows its placeholder, and renders at the same width as its sibling rows
// across terminal widths, focused (with an overlong value) or not.
func TestPromptView_WorkdirFieldRendered(t *testing.T) {
	for _, width := range []int{50, 80, 120, 200} {
		for _, focused := range []bool{false, true} {
			p := newPromptView(true, branchModeNew, "main", "")
			p.workflows = []string{"default", "other"}
			p.SetSize(width, 40)
			if focused {
				p.focusInput(promptFieldWorkdir)
				p.workdirInput.SetValue(strings.Repeat("deep/", 60))
			}
			view := p.View()
			lines := strings.Split(view, "\n")
			plain := plainLines(view)

			slugRow, workdirRow := -1, -1
			for i, l := range plain {
				if strings.Contains(l, "Slug:") && slugRow == -1 {
					slugRow = i
				}
				if strings.Contains(l, "Workdir:") && workdirRow == -1 {
					workdirRow = i
				}
			}
			if slugRow == -1 || workdirRow != slugRow+2 {
				t.Fatalf("width %d: workdir row = %d, want %d (below Slug)\n%s", width, workdirRow, slugRow+2, view)
			}
			if !focused && !strings.Contains(plain[workdirRow], "checkout root if left blank") {
				t.Errorf("width %d: workdir row %q missing placeholder", width, plain[workdirRow])
			}

			// The Workdir row must match the Title/Slug rows' width and fit.
			want := lipgloss.Width(lines[slugRow])
			if w := lipgloss.Width(lines[workdirRow]); w != want || w > width {
				t.Errorf("width %d focused=%v: workdir row width %d, want %d (<= %d)", width, focused, w, want, width)
			}
		}
	}
}

// TestApplyPins_Workdir verifies a workflow workdir pin hides the row,
// supplies the value, and restores the typed value when lifted.
func TestApplyPins_Workdir(t *testing.T) {
	p := newPromptView(true, branchModeNew, "", "")
	p.SetSize(80, 30)
	p.workdirInput.SetValue("typed/dir")

	p.applyPins(&config.WorkflowConfig{Name: "pinned", Workdir: "em/rails"})
	if got := p.Workdir(); got != "em/rails" {
		t.Errorf("pinned Workdir() = %q, want em/rails", got)
	}
	for _, f := range p.visibleFields() {
		if f == promptFieldWorkdir {
			t.Error("pinned workdir must not be focusable")
		}
	}
	if strings.Contains(ansiRegex.ReplaceAllString(p.View(), ""), "Workdir:") {
		t.Error("pinned workdir row must be hidden")
	}

	p.applyPins(&config.WorkflowConfig{Name: "plain"})
	if got := p.Workdir(); got != "typed/dir" {
		t.Errorf("after lifting pin Workdir() = %q, want typed/dir", got)
	}
}

// TestPromptSubmit_WorkdirReachesCreateRequest verifies the typed workdir is
// forwarded to the daemon.
func TestPromptSubmit_WorkdirReachesCreateRequest(t *testing.T) {
	var captured daemon.CreateTaskRequest
	fake := &fakeTaskService{
		createTask: func(req daemon.CreateTaskRequest) (*daemon.TaskInfo, error) {
			captured = req
			return &daemon.TaskInfo{ID: 1}, nil
		},
	}
	m := Model{
		keys:        newKeyMap(),
		client:      fake,
		list:        newListView(false, ""),
		detail:      newDetailView(),
		view:        viewPrompt,
		projectPath: "/tmp/proj",
		prompt:      newPromptView(true, branchModeNew, "main", ""),
	}
	m.prompt.SetSize(80, 30)
	m.prompt.textarea.SetValue("do the thing")
	m.prompt.workdirInput.SetValue(" em/rails-foundation ")

	_, cmd := m.handlePromptSubmit()
	if cmd == nil {
		t.Fatal("expected a create command")
	}
	cmd()
	if captured.Workdir != "em/rails-foundation" {
		t.Errorf("CreateTaskRequest.Workdir = %q, want em/rails-foundation", captured.Workdir)
	}
}

// The task info panel shows a set workdir and omits the line otherwise.
func TestTaskInfoView_WorkdirLine(t *testing.T) {
	v := newTaskInfoView()
	v.SetSize(120, 40)
	v.SetTask(&daemon.TaskInfo{ID: 7, Title: "Sub task", Status: "running", Workdir: "em/rails-foundation"})
	if !strings.Contains(stripANSI(v.renderMetadata()), "Workdir:   em/rails-foundation") {
		t.Errorf("missing Workdir line:\n%s", stripANSI(v.renderMetadata()))
	}
	view := v.View()
	assertViewWidth(t, view, 120)
	assertUniformFrameWidth(t, view)

	v.SetTask(&daemon.TaskInfo{ID: 8, Title: "Root task", Status: "running"})
	if strings.Contains(stripANSI(v.renderMetadata()), "Workdir:") {
		t.Error("a task without a workdir must not render a Workdir line")
	}
}
