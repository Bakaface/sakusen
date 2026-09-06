package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/task"
)

// TestResolveConflictsAgentSelection verifies the agent resolveConflicts
// spawns: an explicit merge_conflicts.agent outranks the workflow's agent, and
// because the resolver must run headless, a tmux-mode workflow agent falls
// back to the implicit headless "claude" record — with an instructive error
// when no such fallback exists. The error branches fire before any spawn, so a
// minimal Engine (just cfg) is enough; the success cases actually spawn the
// headless command and prove which record ran.
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
		// The workflow references a slug with no record at all, so agent
		// resolution fails before any prompt is built.
		cfg := newWorkflowConfig(nil)
		e := &Engine{cfg: newEngineConfig(cfg, "")}
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
		e := &Engine{cfg: newEngineConfig(cfg, "")}
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
		e := &Engine{cfg: newEngineConfig(cfg, "")}
		tk := &task.Task{ID: 7, Workflow: "wf"}

		err := e.resolveConflicts(context.Background(), tk, []string{"main.go"}, nil)
		if err == nil {
			t.Fatal("expected an error when the claude fallback is itself tmux-mode")
		}
		if !strings.Contains(err.Error(), "tmux-mode and no headless") {
			t.Errorf("error = %q, expected the tmux-mode/no-headless message", err.Error())
		}
	})

	t.Run("merge_conflicts.agent outranks the workflow agent", func(t *testing.T) {
		worktree := t.TempDir()
		// Both records are headless, so only the cascade decides: without
		// merge_conflicts.agent the workflow's "my-tmux" slot would win.
		spawned := `printf '%s' "$SAKUSEN_AGENT" > agent-slug.txt; printf resolved > "$SAKUSEN_RESULT_FILE"`
		cfg := newWorkflowConfig(map[string]config.AgentConfig{
			"my-tmux": {Command: spawned},
			"fixer":   {Command: spawned},
		})
		cfg.MergeConflicts = config.MergeConflictsConfig{Agent: "fixer"}
		e := &Engine{
			cfg:      newEngineConfig(cfg, ""),
			dataDir:  filepath.Join(t.TempDir(), "data"),
			repoRoot: worktree,
		}
		tk := &task.Task{ID: 7, Workflow: "wf", Branch: "sakusen/7", WorktreePath: worktree}

		if err := e.resolveConflicts(context.Background(), tk, []string{"main.go"}, nil); err != nil {
			t.Fatalf("resolveConflicts: %v", err)
		}

		slug, err := os.ReadFile(filepath.Join(worktree, "agent-slug.txt"))
		if err != nil {
			t.Fatalf("expected the merge_conflicts.agent record to have run: %v", err)
		}
		if got := strings.TrimSpace(string(slug)); got != "fixer" {
			t.Errorf("SAKUSEN_AGENT seen by the conflict resolver = %q, want %q", got, "fixer")
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
			cfg:      newEngineConfig(cfg, ""),
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

// conflictPromptFor runs the resolver against a trivial headless agent and
// returns the prompt it was handed (runHeadlessAgent writes it to the step
// prompt file), which is the only place the resolved body is observable.
func conflictPromptFor(t *testing.T, mc config.MergeConflictsConfig, tk *task.Task, conflictFiles []string) string {
	t.Helper()
	worktree := t.TempDir()
	cfg := &config.Config{
		Git:    config.GitConfig{BaseBranch: "main"},
		Agents: map[string]config.AgentConfig{"claude": {Command: "true"}},
		Workflows: []config.WorkflowConfig{{
			Name:  "wf",
			Steps: []config.StepConfig{{Name: "s", Prompt: "p"}},
		}},
		MergeConflicts: mc,
	}
	e := &Engine{
		cfg:      newEngineConfig(cfg, ""),
		dataDir:  filepath.Join(t.TempDir(), "data"),
		repoRoot: worktree,
	}
	tk.WorktreePath = worktree

	if err := e.resolveConflicts(context.Background(), tk, conflictFiles, nil); err != nil {
		t.Fatalf("resolveConflicts: %v", err)
	}
	prompt, err := os.ReadFile(StepPromptFile(worktree, "resolve-conflicts"))
	if err != nil {
		t.Fatalf("read resolved prompt: %v", err)
	}
	return string(prompt)
}

// TestResolveConflictsBuiltInPrompt pins the built-in resolver prompt: routing
// it through the template must reproduce the previously hardcoded text
// byte-for-byte.
func TestResolveConflictsBuiltInPrompt(t *testing.T) {
	tk := &task.Task{ID: 7, Workflow: "wf", Branch: "sakusen/7-thing"}
	files := []string{"main.go", "internal/config/types.go"}

	var want strings.Builder
	want.WriteString("You are resolving merge conflicts in an automated merge pipeline.\n\n")
	want.WriteString("The branch `main` is being merged into `sakusen/7-thing`, and the following files have conflicts:\n\n")
	for _, f := range files {
		want.WriteString("- `" + f + "`\n")
	}
	want.WriteString("\nYour job:\n")
	want.WriteString("1. Open each conflicted file and resolve all `<<<<<<<`, `=======`, `>>>>>>>` conflict markers\n")
	want.WriteString("2. Choose the correct resolution by understanding both sides of the conflict\n")
	want.WriteString("3. Run `git add <file>` on each resolved file\n")
	want.WriteString("4. Do NOT run `git commit` — the merge commit will be created automatically\n")
	want.WriteString("5. Do NOT modify any files that are not conflicted\n")
	want.WriteString("6. Verify the code compiles after resolving conflicts (run `go build ./...` or equivalent)\n")

	got := conflictPromptFor(t, config.MergeConflictsConfig{}, tk, files)
	if got != want.String() {
		t.Errorf("built-in conflict prompt changed:\ngot:\n%s\nwant:\n%s", got, want.String())
	}
}

// TestResolveConflictsPromptOverride verifies merge_conflicts.prompt replaces
// the built-in body entirely, with task/git/conflict vars resolved and
// step/loop/children/track vars resolving empty (no step ran).
func TestResolveConflictsPromptOverride(t *testing.T) {
	tk := &task.Task{ID: 7, Workflow: "wf", Title: "Add login", Branch: "sakusen/7-login"}
	mc := config.MergeConflictsConfig{
		Prompt: "Task {{task.id}} ({{task.title}}): merging {{git.base_branch}} into {{task.branch}}\n" +
			"{{conflict.files}}\n" +
			"steps=[{{steps.implement.context}}] loop=[{{loop.iteration}}] track=[{{track.name}}]\n",
	}

	got := conflictPromptFor(t, mc, tk, []string{"a.go", "b.go"})

	want := "Task 7 (Add login): merging main into sakusen/7-login\n" +
		"- `a.go`\n- `b.go`\n" +
		"steps=[] loop=[0] track=[]\n"
	if got != want {
		t.Errorf("override prompt = %q, want %q", got, want)
	}
	if strings.Contains(got, "automated merge pipeline") {
		t.Error("override prompt must replace the built-in body, not wrap it")
	}
}

// TestResolveConflictsTimeoutApplied verifies the configured
// merge_conflicts.timeout reaches the synthetic resolve-conflicts step: a
// resolver that outlives it is cancelled instead of running to completion.
func TestResolveConflictsTimeoutApplied(t *testing.T) {
	worktree := t.TempDir()
	cfg := &config.Config{
		Git:    config.GitConfig{BaseBranch: "main"},
		Agents: map[string]config.AgentConfig{"claude": {Command: "sleep 30"}},
		Workflows: []config.WorkflowConfig{{
			Name:  "wf",
			Steps: []config.StepConfig{{Name: "s", Prompt: "p"}},
		}},
		MergeConflicts: config.MergeConflictsConfig{Timeout: "100ms"},
	}
	e := &Engine{
		cfg:      newEngineConfig(cfg, ""),
		dataDir:  filepath.Join(t.TempDir(), "data"),
		repoRoot: worktree,
	}
	tk := &task.Task{ID: 7, Workflow: "wf", Branch: "sakusen/7", WorktreePath: worktree}

	start := time.Now()
	err := e.resolveConflicts(context.Background(), tk, []string{"main.go"}, nil)
	if err == nil {
		t.Fatal("expected the resolver to be cancelled by the configured timeout")
	}
	if !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Errorf("error = %v, want a deadline-exceeded failure", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("resolver ran for %s, want it cut off near the configured 100ms", elapsed)
	}
}
