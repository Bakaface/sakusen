package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
)

// TestRunTask_NoWorktreePersistsWorktreePath ensures that for non-worktree
// tasks (Worktree=false), the engine persists WorktreePath = repoRoot to the
// database. Without this, daemon restart cannot restore tmux sessions for
// non-worktree tasks (it sees worktree_path = NULL and bails).
func TestRunTask_NoWorktreePersistsWorktreePath(t *testing.T) {
	dir := t.TempDir()

	dbPath := filepath.Join(dir, ".sakusen", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	project, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}

	tk, err := database.CreateTask(project.ID, "no-worktree task", "desc", "slug", "default", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	tk.Worktree = false
	// Intentionally leave tk.WorktreePath empty — simulates a fresh task.

	cfg := &config.Config{
		Agents:     map[string]config.AgentConfig{"claude": {Command: `printf done > "$SAKUSEN_RESULT_FILE"`}},
		OnComplete: "none",
		Workflows: []config.WorkflowConfig{
			{Name: "default", Steps: []config.StepConfig{{Name: "step1", Prompt: "do it"}}},
		},
	}
	engine := NewEngine(cfg, database, nil, dir)

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	refreshed, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if refreshed.WorktreePath != dir {
		t.Errorf("expected persisted WorktreePath=%q (repo root), got %q — daemon restart would fail to restore tmux session", dir, refreshed.WorktreePath)
	}
}
