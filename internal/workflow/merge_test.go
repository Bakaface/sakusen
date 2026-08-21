package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/task"
)

// TestResolveConflictsAgentSelection verifies the agent-selection logic at the
// top of resolveConflicts (merge.go): the conflict resolver must run headless,
// so a tmux-mode workflow agent falls back to the implicit headless "claude"
// record — and errors with an instructive message when no such fallback
// exists. The two error branches fire before any spawn, so a minimal Engine
// (just cfg) is enough; the fallback-success case actually spawns the headless
// command and proves the "claude" record is what ran.
func TestResolveConflictsAgentSelection(t *testing.T) {
	newWorkflowConfig := func(agents map[string]config.AgentConfig) *config.Config {
		return &config.Config{
			Workflows: []config.WorkflowConfig{{
				Name:  "wf",
				Agent: "my-tmux",
				Steps: []config.StepConfig{{Name: "s", Prompt: "p"}},
			}},
			Agents: agents,
		}
	}

	t.Run("workflow agent unresolvable errors before selection", func(t *testing.T) {
		// The workflow references a slug with no record at all: WorkflowAgent
		// itself fails, exercising the earliest error return (merge.go:41-43).
		cfg := newWorkflowConfig(nil)
		e := &Engine{cfg: newEngineConfig(cfg)}
		tk := &task.Task{ID: 7, Workflow: "wf"}

		err := e.resolveConflicts(context.Background(), tk, []string{"main.go"}, nil)
		if err == nil {
			t.Fatal("expected an error when the workflow agent slug has no record")
		}
		if !strings.Contains(err.Error(), "failed to resolve merge conflicts") {
			t.Errorf("error = %q, expected the resolve-merge-conflicts wrapper", err.Error())
		}
	})

	t.Run("tmux workflow agent with no headless claude errors", func(t *testing.T) {
		cfg := newWorkflowConfig(map[string]config.AgentConfig{
			"my-tmux": {Mode: config.AgentModeTmux, Command: "true"},
		})
		e := &Engine{cfg: newEngineConfig(cfg)}
		tk := &task.Task{ID: 7, Workflow: "wf"}

		err := e.resolveConflicts(context.Background(), tk, []string{"main.go"}, nil)
		if err == nil {
			t.Fatal("expected an error when the workflow agent is tmux and no headless claude exists")
		}
		if !strings.Contains(err.Error(), "tmux-mode and no headless") {
			t.Errorf("error = %q, expected the tmux-mode/no-headless message", err.Error())
		}
		if !strings.Contains(err.Error(), `"my-tmux"`) {
			t.Errorf("error = %q, expected it to name the tmux workflow agent slug", err.Error())
		}
	})

	t.Run("tmux workflow agent with tmux claude errors", func(t *testing.T) {
		// A "claude" record exists but is itself tmux-mode — not a usable
		// fallback for a synchronous conflict-resolution pass.
		cfg := newWorkflowConfig(map[string]config.AgentConfig{
			"my-tmux": {Mode: config.AgentModeTmux, Command: "true"},
			"claude":  {Mode: config.AgentModeTmux, Command: "true"},
		})
		e := &Engine{cfg: newEngineConfig(cfg)}
		tk := &task.Task{ID: 7, Workflow: "wf"}

		err := e.resolveConflicts(context.Background(), tk, []string{"main.go"}, nil)
		if err == nil {
			t.Fatal("expected an error when the claude fallback is itself tmux-mode")
		}
		if !strings.Contains(err.Error(), "tmux-mode and no headless") {
			t.Errorf("error = %q, expected the tmux-mode/no-headless message", err.Error())
		}
	})

	t.Run("tmux workflow agent falls back to headless claude", func(t *testing.T) {
		worktree := t.TempDir()
		cfg := newWorkflowConfig(map[string]config.AgentConfig{
			"my-tmux": {Mode: config.AgentModeTmux, Command: "true"},
			// The headless fallback records which slug it was spawned as (so
			// the test can prove selection picked "claude", not "my-tmux") and
			// satisfies the result-file contract.
			"claude": {Command: `printf '%s' "$SAKUSEN_AGENT" > agent-slug.txt; printf resolved > "$SAKUSEN_RESULT_FILE"`},
		})
		e := &Engine{
			cfg:      newEngineConfig(cfg),
			dataDir:  filepath.Join(t.TempDir(), "data"),
			repoRoot: worktree,
		}
		tk := &task.Task{ID: 7, Workflow: "wf", Branch: "sakusen/7", WorktreePath: worktree}

		if err := e.resolveConflicts(context.Background(), tk, []string{"main.go"}, nil); err != nil {
			t.Fatalf("expected fallback selection to succeed, got %v", err)
		}

		slug, err := os.ReadFile(filepath.Join(worktree, "agent-slug.txt"))
		if err != nil {
			t.Fatalf("expected the headless fallback agent to have run: %v", err)
		}
		if got := strings.TrimSpace(string(slug)); got != config.DefaultAgentSlug {
			t.Errorf("SAKUSEN_AGENT seen by the conflict resolver = %q, want %q", got, config.DefaultAgentSlug)
		}
	})
}
