package tui

import (
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/Bakaface/sakusen/internal/tmux"
)

// The daemon names a task's tmux session after the TASK's project. The TUI used
// to rebuild that name from the cwd's config instead, so attaching from a
// directory that resolves to a different project row (global mode, or a nested
// .sakusen.yml — e.g. cwd /repo/personal while the task belongs to /repo) went
// looking for "<cwd project>-<id>" and always reported "no tmux session found".
func TestTmuxProjectNameFor_UsesTaskProjectNotCwd(t *testing.T) {
	m := Model{cfg: &config.Config{}}
	m.cfg.Project.Name = "personal"

	task := daemon.TaskInfo{ID: 455, ProjectName: "nexus"}

	if got := m.tmuxProjectNameFor(task); got != "nexus" {
		t.Fatalf("project name = %q, want %q (the task's project, not cwd's)", got, "nexus")
	}

	if got := tmux.NewSession(m.tmuxProjectNameFor(task), "455", "", "").Name; got != "nexus-455" {
		t.Errorf("session name = %q, want %q", got, "nexus-455")
	}
}

// With no live session, a slugged task resolves to its full session name.
func TestAttachSessionName_UsesSlug(t *testing.T) {
	m := Model{cfg: &config.Config{}}
	task := daemon.TaskInfo{ID: 455, ProjectName: "nexus", Slug: "fix-login"}

	if got := tmux.FindSession(m.tmuxProjectNameFor(task), "455", task.Slug, "").Name; got != "nexus-455-fix-login" {
		t.Errorf("session name = %q, want %q", got, "nexus-455-fix-login")
	}
}

// A TaskInfo predating ProjectName enrichment still resolves to something.
func TestTmuxProjectNameFor_FallsBackToCwdConfig(t *testing.T) {
	m := Model{cfg: &config.Config{}}
	m.cfg.Project.Name = "personal"

	if got := m.tmuxProjectNameFor(daemon.TaskInfo{ID: 7}); got != "personal" {
		t.Errorf("project name = %q, want %q", got, "personal")
	}

	var noCfg Model
	if got := noCfg.tmuxProjectNameFor(daemon.TaskInfo{ID: 7}); got != "" {
		t.Errorf("project name = %q, want empty with no config", got)
	}
}

// checkTmuxSessions polls one name per project in the list, not just cwd's,
// so tmux indicators appear for every project shown in global mode.
func TestCheckTmuxSessions_CollectsEveryProjectInList(t *testing.T) {
	m := Model{cfg: &config.Config{}, list: newListView(true, "")}
	m.cfg.Project.Name = "personal"
	m.list.allTasks = []daemon.TaskInfo{
		{ID: 455, ProjectName: "nexus"},
		{ID: 456, ProjectName: "nexus"},
		{ID: 450, ProjectName: "em"},
		{ID: 7}, // no ProjectName → falls back to cwd's
	}

	got := m.tmuxProjectNames()
	want := []string{"nexus", "em", "personal"}

	if len(got) != len(want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("names = %v, want %v (order follows first appearance)", got, want)
		}
	}
}

func TestTmuxProjectNames_FallsBackToCwdWhenListEmpty(t *testing.T) {
	m := Model{cfg: &config.Config{}, list: newListView(false, "")}
	m.cfg.Project.Name = "personal"

	got := m.tmuxProjectNames()
	if len(got) != 1 || got[0] != "personal" {
		t.Errorf("names = %v, want [personal]", got)
	}
}
