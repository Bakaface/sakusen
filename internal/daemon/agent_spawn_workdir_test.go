package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	session := fmt.Sprintf("%s-%d", pc.cfg.Project.Name, tk.ID)
	out, err := exec.Command("tmux", "display-message", "-p", "-t", session, "#{session_path}").Output()
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
