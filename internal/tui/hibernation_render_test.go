package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/Bakaface/sakusen/internal/tmux"
)

// A hibernated tmux task renders [zz] instead of the live-session postfixes:
// there is no agent process left, so neither [wip] nor [T] would be true.
func TestStatusText_HibernatedRendersZZ(t *testing.T) {
	at := time.Date(2026, 9, 24, 10, 30, 0, 0, time.UTC)
	tests := []struct {
		name      string
		task      daemon.TaskInfo
		sessions  map[int64]bool
		wantLabel string
	}{
		{
			name:      "hibernated human step",
			task:      daemon.TaskInfo{ID: 1, Status: "tmux", CurrentStep: "review", StepHuman: true, HibernatedAt: &at},
			wantLabel: "review [zz]",
		},
		{
			name:      "a stale activity reading does not win over the marker",
			task:      daemon.TaskInfo{ID: 2, Status: "tmux", CurrentStep: "implement", TmuxActivity: "wip", HibernatedAt: &at},
			sessions:  map[int64]bool{2: true},
			wantLabel: "implement [zz]",
		},
		{
			name:      "not hibernated keeps the live postfix",
			task:      daemon.TaskInfo{ID: 3, Status: "tmux", CurrentStep: "implement", TmuxActivity: "idle"},
			sessions:  map[int64]bool{3: true},
			wantLabel: "implement [T]",
		},
		{
			name:      "detached still wins over the hibernation marker",
			task:      daemon.TaskInfo{ID: 4, Status: "tmux", CurrentStep: "dev", WorktreeDetached: true, HibernatedAt: &at},
			wantLabel: "dev [detached]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := newListView(false, "")
			l.tmuxSessions = tt.sessions
			got := l.statusText(tt.task)
			if !strings.HasSuffix(got, tt.wantLabel) {
				t.Errorf("statusText() = %q, want it to end with %q", got, tt.wantLabel)
			}
		})
	}
}

// The task info panel surfaces the marker next to Status, and the rendered
// view stays structurally sound (per the repo's programmatic-verification
// rule for View() changes).
func TestTaskInfoView_HibernatedLine(t *testing.T) {
	at := time.Date(2026, 9, 24, 10, 30, 0, 0, time.UTC)
	v := newTaskInfoView()
	v.SetSize(120, 40)
	v.SetTask(&daemon.TaskInfo{
		ID:           5,
		Title:        "Sleeping task",
		Status:       "tmux",
		CurrentStep:  "review",
		HibernatedAt: &at,
	})

	metadata := v.renderMetadata()
	if !strings.Contains(stripANSI(metadata), "Hibernated: 2026-09-24 10:30:00") {
		t.Errorf("expected a Hibernated: line in the metadata block:\n%s", metadata)
	}

	lines := strings.Split(stripANSI(metadata), "\n")
	statusIdx, hibernatedIdx := -1, -1
	for i, line := range lines {
		if strings.HasPrefix(line, "Status:") {
			statusIdx = i
		}
		if strings.HasPrefix(line, "Hibernated:") {
			hibernatedIdx = i
		}
	}
	if statusIdx < 0 || hibernatedIdx != statusIdx+1 {
		t.Errorf("expected the Hibernated line directly under Status (status=%d hibernated=%d)", statusIdx, hibernatedIdx)
	}

	view := v.View()
	assertViewWidth(t, view, 120)
	assertUniformFrameWidth(t, view)

	// A task that is not hibernated must not grow the line.
	v.SetTask(&daemon.TaskInfo{ID: 6, Title: "Awake task", Status: "tmux", CurrentStep: "review"})
	if strings.Contains(stripANSI(v.renderMetadata()), "Hibernated:") {
		t.Error("a live tmux task must not render a Hibernated line")
	}
	assertUniformFrameWidth(t, v.View())
}

// Attaching to a tmux task whose session is gone asks the daemon to wake it
// first, instead of the old "no tmux session found" dead end.
func TestAttachTmuxSession_WakesMissingSession(t *testing.T) {
	woke := make(chan int64, 1)
	fake := &fakeTaskService{
		wakeTask: func(id int64) error {
			woke <- id
			return nil
		},
	}
	m := Model{client: fake, list: newListView(false, "")}

	// A project name no real session could be using, so Exists() is false
	// whether or not tmux is installed on the machine running the tests.
	info := daemon.TaskInfo{
		ID:          42,
		Status:      "tmux",
		ProjectName: fmt.Sprintf("sakusen-wake-test-%d", time.Now().UnixNano()),
	}
	if tmux.NewSession(info.ProjectName, "42", "", "").Exists() {
		t.Skip("a tmux session already occupies the generated test name")
	}

	cmd := m.attachTmuxSession(info)
	if cmd == nil {
		t.Fatal("expected a non-nil cmd")
	}
	msg := cmd()

	select {
	case id := <-woke:
		if id != 42 {
			t.Errorf("WakeTask called with %d, want 42", id)
		}
	default:
		t.Fatalf("expected attach to wake the task, got msg %T: %v", msg, msg)
	}
	// What the cmd returns after the wake depends on the environment it runs
	// in (inside tmux it switches the client, outside it execs an attach), so
	// the assertion stops at the wake — the part this change owns.
}

// A wake failure surfaces as an error rather than a silent no-op.
func TestAttachTmuxSession_SurfacesWakeError(t *testing.T) {
	fake := &fakeTaskService{
		wakeTask: func(int64) error { return errors.New("daemon says no") },
	}
	m := Model{client: fake, list: newListView(false, "")}
	info := daemon.TaskInfo{
		ID:          43,
		Status:      "tmux",
		ProjectName: fmt.Sprintf("sakusen-wake-test-%d", time.Now().UnixNano()),
	}
	if tmux.NewSession(info.ProjectName, "43", "", "").Exists() {
		t.Skip("a tmux session already occupies the generated test name")
	}

	msg := m.attachTmuxSession(info)()
	err, ok := msg.(errorMsg)
	if !ok {
		t.Fatalf("expected an errorMsg, got %T: %v", msg, msg)
	}
	if !strings.Contains(err.Error(), "daemon says no") {
		t.Errorf("error = %v, want it to carry the wake failure", err)
	}
}

// A non-tmux task with no session is still a plain error: there is nothing to
// wake, and waking would be the wrong question to ask the daemon.
func TestAttachTmuxSession_NonTmuxTaskDoesNotWake(t *testing.T) {
	fake := &fakeTaskService{
		wakeTask: func(int64) error {
			t.Error("a non-tmux task must not be woken")
			return nil
		},
	}
	m := Model{client: fake, list: newListView(false, "")}
	info := daemon.TaskInfo{
		ID:          44,
		Status:      "running",
		ProjectName: fmt.Sprintf("sakusen-wake-test-%d", time.Now().UnixNano()),
	}
	if tmux.NewSession(info.ProjectName, "44", "", "").Exists() {
		t.Skip("a tmux session already occupies the generated test name")
	}

	msg := m.attachTmuxSession(info)()
	if _, ok := msg.(errorMsg); !ok {
		t.Fatalf("expected an errorMsg, got %T: %v", msg, msg)
	}
}
