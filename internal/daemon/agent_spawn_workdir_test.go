package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/task"
	"github.com/Bakaface/sakusen/internal/tmux"
)

const restoreYMLWorkflowSetup = `default_agent: interactive
tmux-setup-command: 'touch project-setup-ran'
agents:
  interactive:
    mode: tmux
    command: 'echo fresh-session-cmd'
workflows:
  - name: default
    tmux-setup-command: 'pwd -P > setup-cwd.txt; echo "{{worktree_path}}" >> setup-cwd.txt'
    steps:
      - name: implement
        prompt: do it
`

// A restored session starts in the task's workdir, runs the workflow-level
// tmux-setup-command there (over the project-level one), and keeps
// {{worktree_path}} and the .sakusen files at the checkout root.
func TestRestoreTmuxSession_StartsInWorkdir(t *testing.T) {
	s, projectDir, tk := setupRestoreProject(t, restoreYMLWorkflowSetup)
	if err := s.database.SetTaskWorkdir(tk.ID, "em/rails-foundation"); err != nil {
		t.Fatal(err)
	}
	tk.Workdir = "em/rails-foundation"
	start := filepath.Join(projectDir, "em", "rails-foundation")
	if err := os.MkdirAll(start, 0755); err != nil {
		t.Fatal(err)
	}

	if _, err := s.restoreTmuxSession(tk); err != nil {
		t.Fatalf("restoreTmuxSession: %v", err)
	}

	pc, err := s.getProjectContext(tk.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	session := fmt.Sprintf("%s-%d-%s", pc.cfg.Project.Name, tk.ID, tk.Slug)
	out, err := exec.Command("tmux", "display-message", "-p", "-t", "="+session+":", "#{session_path}").Output()
	if err != nil {
		t.Fatalf("tmux display-message: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != start {
		t.Errorf("session path = %q, want %q", got, start)
	}

	data, err := os.ReadFile(filepath.Join(start, "setup-cwd.txt"))
	if err != nil {
		t.Fatalf("workflow-level setup command did not run in the start dir: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if real, _ := filepath.EvalSymlinks(start); len(lines) != 2 || lines[0] != real || lines[1] != projectDir {
		t.Errorf("setup output = %q, want cwd %q and {{worktree_path}} %q", lines, real, projectDir)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "project-setup-ran")); err == nil {
		t.Error("project-level setup command ran despite a workflow-level one")
	}
	readRestoreScript(t, projectDir, "continue") // .sakusen stays at the root
}

func TestRestoreTmuxSession_MissingWorkdirFails(t *testing.T) {
	s, projectDir, tk := setupRestoreProject(t, restoreYMLResumable)
	tk.Workdir = "missing"
	_, err := s.restoreTmuxSession(tk)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(projectDir, "missing")) {
		t.Fatalf("expected error naming the missing workdir, got %v", err)
	}
}

func TestSpawnInteractiveSessionReplacesLegacySession(t *testing.T) {
	s, projectDir, tk := setupRestoreProject(t, restoreYMLNoResume)
	tk.WorktreePath = projectDir
	pc, err := s.getProjectContext(tk.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	taskID := fmt.Sprintf("%d", tk.ID)
	legacy := tmux.SessionName(pc.cfg.Project.Name, taskID, "")
	slugged := tmux.SessionName(pc.cfg.Project.Name, taskID, tk.Slug)
	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", "="+legacy).Run()
		exec.Command("tmux", "kill-session", "-t", "="+slugged).Run()
	})
	if err := exec.Command("tmux", "new-session", "-d", "-s", legacy).Run(); err != nil {
		t.Fatalf("create legacy session: %v", err)
	}
	if !sessionExistsExactly(t, legacy) {
		t.Fatalf("legacy session %s not live", legacy)
	}

	slug, agentCfg, err := interactiveAgent(pc.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.spawnInteractiveSession(pc, tk, interactiveSpawnOpts{label: "continue", slug: slug, agent: agentCfg}); err != nil {
		t.Fatalf("spawnInteractiveSession: %v", err)
	}

	if !sessionExistsExactly(t, slugged) {
		t.Errorf("slugged session %s not created", slugged)
	}
	if sessionExistsExactly(t, legacy) {
		t.Errorf("legacy session %s still alive", legacy)
	}
}

func TestSetupTmuxDirectUsesFinalizedSlug(t *testing.T) {
	s, projectDir, fixture := setupRestoreProject(t, restoreYMLNoResume)
	tk, err := s.database.CreateTaskWithPriority(
		fixture.ProjectID, "", "", "", "", "", "", "", "",
		task.StatusInit, task.PriorityMedium, false, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.database.UpdateTaskWorktreePath(tk.ID, projectDir); err != nil {
		t.Fatal(err)
	}
	pc, err := s.getProjectContext(tk.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := interactiveAgent(pc.cfg); err != nil {
		t.Fatal(err)
	}
	taskID := fmt.Sprintf("%d", tk.ID)
	slugless := tmux.SessionName(pc.cfg.Project.Name, taskID, "")
	want := tmux.SessionName(pc.cfg.Project.Name, taskID, "rotate-tokens")
	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", "="+slugless).Run()
		exec.Command("tmux", "kill-session", "-t", "="+want).Run()
	})

	s.setupTmuxDirect(tk.ID, tk.ProjectID, "Rotate tokens")

	if !sessionExistsExactly(t, want) {
		t.Errorf("session %s not created", want)
	}
	if sessionExistsExactly(t, slugless) {
		t.Errorf("slugless session %s created", slugless)
	}
	got, err := s.database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Slug != "rotate-tokens" {
		t.Errorf("slug = %q, want rotate-tokens", got.Slug)
	}
	if got.Status != task.StatusTmux {
		t.Errorf("status = %s, want %s", got.Status, task.StatusTmux)
	}
}
