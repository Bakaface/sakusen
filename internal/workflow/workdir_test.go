package workflow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/runner"
	"github.com/Bakaface/sakusen/internal/task"
)

const pwdAgent = `pwd -P > "$SAKUSEN_RESULT_FILE"`

// newWorkdirEngine wires a real-runner Engine around a non-worktree task
// (WorktreePath = the project root) with the given workdir.
func newWorkdirEngine(t *testing.T, workdir string, wf config.WorkflowConfig) (*Engine, *task.Task, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	project, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	tk, err := database.CreateTask(project.ID, "workdir task", "desc", "slug", wf.Name, "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	tk.Worktree = false
	tk.WorktreePath = dir
	tk.Workdir = workdir

	cfg := &config.Config{
		Agents:     map[string]config.AgentConfig{"claude": {Command: pwdAgent}},
		OnComplete: "none",
		Workflows:  []config.WorkflowConfig{wf},
	}
	return NewEngine(cfg, database, nil, dir), tk, dir
}

func singleStepWorkflow() config.WorkflowConfig {
	return config.WorkflowConfig{Name: "default", Steps: []config.StepConfig{{
		Name: "step1", Prompt: "do it", SummarizationStrategy: config.SummarizationStrategyLastMessage,
	}}}
}

func TestHeadlessStepStartsInWorkdir(t *testing.T) {
	for _, workdir := range []string{"", "em/rails-foundation"} {
		t.Run("workdir="+workdir, func(t *testing.T) {
			engine, tk, dir := newWorkdirEngine(t, workdir, singleStepWorkflow())
			want := filepath.Join(dir, workdir)
			if err := os.MkdirAll(want, 0755); err != nil {
				t.Fatal(err)
			}
			if err := engine.RunTask(context.Background(), tk, nil); err != nil {
				t.Fatalf("RunTask: %v", err)
			}
			got, err := engine.database.GetTaskStepContext(tk.ID, "step1")
			if err != nil {
				t.Fatalf("GetTaskStepContext: %v", err)
			}
			if strings.TrimSpace(got) != want {
				t.Errorf("agent cwd = %q, want %q", strings.TrimSpace(got), want)
			}
			// sakusen's own files stay at the checkout root.
			if _, err := os.Stat(filepath.Join(dir, runner.OutputLogFileName)); err != nil {
				t.Errorf("raw output log not at checkout root: %v", err)
			}
			if _, err := os.Stat(StepPromptFile(dir, "step1")); err != nil {
				t.Errorf("prompt file not under checkout root: %v", err)
			}
			if workdir != "" {
				if _, err := os.Stat(filepath.Join(want, runner.OutputLogFileName)); err == nil {
					t.Errorf("raw output log must not be written into the workdir")
				}
				if _, err := os.Stat(filepath.Join(want, ".sakusen")); err == nil {
					t.Errorf(".sakusen must not be created inside the workdir")
				}
			}
		})
	}
}

func TestParallelBranchesStartInWorkdir(t *testing.T) {
	wf := config.WorkflowConfig{Name: "default", Steps: []config.StepConfig{{
		Name: "review",
		Parallel: &config.ParallelConfig{Branches: []config.StepConfig{
			{Name: "review-a", Prompt: "a"},
			{Name: "review-b", Prompt: "b"},
		}},
	}}}
	engine, tk, dir := newWorkdirEngine(t, "sub", wf)
	want := filepath.Join(dir, "sub")
	if err := os.MkdirAll(want, 0755); err != nil {
		t.Fatal(err)
	}
	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	for _, b := range []string{"review-a", "review-b"} {
		got, err := engine.database.GetTaskStepContext(tk.ID, b)
		if err != nil {
			t.Fatalf("GetTaskStepContext(%s): %v", b, err)
		}
		if strings.TrimSpace(got) != want {
			t.Errorf("branch %s cwd = %q, want %q", b, strings.TrimSpace(got), want)
		}
	}
}

func TestHeadlessStepMissingWorkdirFails(t *testing.T) {
	engine, tk, dir := newWorkdirEngine(t, "does/not/exist", singleStepWorkflow())
	err := engine.RunTask(context.Background(), tk, nil)
	missing := filepath.Join(dir, "does/not/exist")
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if refreshed, gerr := engine.database.GetTask(tk.ID); gerr == nil {
		msg += " " + refreshed.ErrorMessage
	}
	if !strings.Contains(msg, missing) {
		t.Fatalf("expected a failure naming %s, got err=%v msg=%q", missing, err, msg)
	}
	if ctx, _ := engine.database.GetTaskStepContext(tk.ID, "step1"); ctx != "" {
		t.Errorf("agent must not have run, got context %q", ctx)
	}
}

func TestResolveAgentWorkDirRejectsFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveAgentWorkDir(&task.Task{WorktreePath: dir, Workdir: "f"}); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("expected not-a-directory error, got %v", err)
	}
	if got, err := ResolveAgentWorkDir(&task.Task{WorktreePath: dir}); err != nil || got != dir {
		t.Errorf("empty workdir = (%q, %v), want (%q, nil)", got, err, dir)
	}
}

func TestCopyImagesRelativeToWorkdir(t *testing.T) {
	root := t.TempDir()
	start := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(start, 0755); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(t.TempDir(), "x.png")
	if err := os.WriteFile(img, []byte("png"), 0644); err != nil {
		t.Fatal(err)
	}
	rel, err := CopyImagesToWorktree(root, start, []string{img})
	if err != nil {
		t.Fatalf("CopyImagesToWorktree: %v", err)
	}
	if want := filepath.Join("..", "..", ".sakusen", "images", "x.png"); len(rel) != 1 || rel[0] != want {
		t.Fatalf("rel = %v, want [%s]", rel, want)
	}
	if _, err := os.Stat(filepath.Join(start, rel[0])); err != nil {
		t.Errorf("image path does not resolve from the start dir: %v", err)
	}
}

func TestTmuxStepStartsInWorkdir(t *testing.T) {
	// Project-level command must lose to the workflow-level one.
	engine, tk, dir := newTmuxStepEngine(t, `touch project-setup-ran`)
	engine.cfg.full.Workflows[0].TmuxSetupCommand = `pwd -P > setup-cwd.txt; echo "{{worktree_path}}" >> setup-cwd.txt`
	tk.Workdir = "sub/dir"
	start := filepath.Join(dir, "sub", "dir")
	if err := os.MkdirAll(start, 0755); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	session := fmt.Sprintf("%s-%d", engine.cfg.ProjectName, tk.ID)
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
	if len(lines) != 2 {
		t.Fatalf("setup output = %q", string(data))
	}
	if real, _ := filepath.EvalSymlinks(start); lines[0] != real {
		t.Errorf("setup cwd = %q, want %q", lines[0], real)
	}
	if lines[1] != dir {
		t.Errorf("{{worktree_path}} = %q, want checkout root %q", lines[1], dir)
	}
	for _, d := range []string{dir, start} {
		if _, err := os.Stat(filepath.Join(d, "project-setup-ran")); err == nil {
			t.Errorf("project-level setup command ran despite a workflow-level one")
		}
	}
	if _, err := os.Stat(StepPromptFile(dir, "implement")); err != nil {
		t.Errorf("prompt file not under checkout root: %v", err)
	}
}

func TestTmuxStepFallsBackToProjectSetupCommand(t *testing.T) {
	engine, tk, dir := newTmuxStepEngine(t, `touch project-setup-ran`)
	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "project-setup-ran")); err != nil {
		t.Errorf("project-level setup command did not run: %v", err)
	}
}

func TestTmuxStepMissingWorkdirFails(t *testing.T) {
	engine, tk, dir := newTmuxStepEngine(t, "")
	tk.Workdir = "nope"
	err := engine.RunTask(context.Background(), tk, nil)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if refreshed, gerr := engine.database.GetTask(tk.ID); gerr == nil {
		msg += " " + refreshed.ErrorMessage
	}
	if !strings.Contains(msg, filepath.Join(dir, "nope")) {
		t.Fatalf("expected failure naming the missing workdir, got err=%v msg=%q", err, msg)
	}
}
