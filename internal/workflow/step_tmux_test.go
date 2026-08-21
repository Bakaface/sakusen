package workflow

// Tests for the generic tmux-mode step runner (runStepTmux). These require a
// real tmux binary (skip otherwise) and verify the on-disk artifacts a tmux
// spawn produces (wrapper script, prompt file, sentinel contract) rather than
// interacting with the session, following internal/daemon/restore_resume_test.go.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
	"github.com/Bakaface/sakusen/internal/tmux"
)

// newTmuxStepEngine wires a real DB-backed Engine whose workflow's single step
// resolves to a tmux-mode agent. The project name is nanosecond-unique so
// parallel test runs can't collide on tmux session names. Returns the engine,
// the (non-worktree) task, and the worktree dir.
func newTmuxStepEngine(t *testing.T, tmuxSetupCommand string) (*Engine, *task.Task, string) {
	t.Helper()
	if !tmux.IsAvailable() {
		t.Skip("tmux not available")
	}
	dir := t.TempDir()

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	project, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	tk, err := database.CreateTask(project.ID, "tmux step", "desc", "slug", "default", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	tk.Worktree = false
	tk.WorktreePath = dir

	cfg := &config.Config{
		OnComplete:       "none",
		TmuxSetupCommand: tmuxSetupCommand,
		Agents: map[string]config.AgentConfig{
			"claude": {Command: "true"},
			"pair": {
				Mode:    "tmux",
				Command: "echo agent-here",
				Env: map[string]string{
					"MY_AGENT_VAR":  "xyz",
					"SAKUSEN_AGENT": "masked", // must lose to the contract
				},
			},
		},
		Workflows: []config.WorkflowConfig{{
			Name: "default",
			Steps: []config.StepConfig{{
				Name:   "implement",
				Prompt: "do the tmux thing",
				Agent:  "pair",
			}},
		}},
	}
	cfg.Project.Name = fmt.Sprintf("wf-tmux-%d", time.Now().UnixNano())

	engine := NewEngine(cfg, database, nil, dir)

	t.Cleanup(func() {
		session := tmux.NewSession(cfg.Project.Name, fmt.Sprintf("%d", tk.ID), dir)
		session.Kill()
	})

	return engine, tk, dir
}

// TestRunTaskTmuxAgentDispatch proves the engine-level mode dispatch: a step
// whose agent record is tmux-mode routes through runStepTmux (fire-and-forget
// pause) instead of the headless runner, and the spawn leaves the full generic
// contract on disk — wrapper script with agent command + env exports
// (SAKUSEN_PROMPT_FILE, SAKUSEN_DONE_DIR, SAKUSEN_DONE_PREFIX, agent `env:`,
// contract-wins masking), the prompt file, and a cleared stale sentinel.
func TestRunTaskTmuxAgentDispatch(t *testing.T) {
	engine, tk, dir := newTmuxStepEngine(t, "")

	// A stale sentinel from a previous pass of this step must be cleared on
	// launch, or the daemon monitor would auto-advance the fresh session
	// before the agent does any work.
	doneDir := StepDoneDir(dir)
	if err := os.MkdirAll(doneDir, 0755); err != nil {
		t.Fatal(err)
	}
	staleSentinel := filepath.Join(doneDir, SentinelPrefix("implement")+"-123.json")
	if err := os.WriteFile(staleSentinel, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	if _, err := os.Stat(staleSentinel); !os.IsNotExist(err) {
		t.Errorf("stale sentinel %s must be cleared before the session launches", staleSentinel)
	}

	prompt, err := os.ReadFile(StepPromptFile(dir, "implement"))
	if err != nil {
		t.Fatalf("prompt file not written: %v", err)
	}
	if string(prompt) != "do the tmux thing" {
		t.Errorf("prompt file = %q, want the resolved step prompt", string(prompt))
	}

	scriptFile := filepath.Join(dir, ".sakusen", "run-step-implement.sh")
	scriptBytes, err := os.ReadFile(scriptFile)
	if err != nil {
		t.Fatalf("wrapper script not written: %v", err)
	}
	script := string(scriptBytes)

	wantFragments := []string{
		"echo agent-here", // the agent record's command, not any claude default
		fmt.Sprintf("export SAKUSEN_PROMPT_FILE='%s'", StepPromptFile(dir, "implement")),
		fmt.Sprintf("export SAKUSEN_DONE_DIR='%s'", doneDir),
		fmt.Sprintf("export SAKUSEN_DONE_PREFIX='%s'", SentinelPrefix("implement")),
		"export MY_AGENT_VAR='xyz'",
		"export SAKUSEN_AGENT='pair'",
		"export SAKUSEN_PURPOSE='step'",
	}
	for _, want := range wantFragments {
		if !strings.Contains(script, want) {
			t.Errorf("wrapper script missing %q\nscript:\n%s", want, script)
		}
	}
	if strings.Contains(script, "masked") {
		t.Errorf("agent env must not mask the SAKUSEN_AGENT contract value\nscript:\n%s", script)
	}

	// Fire-and-forget: the step must NOT have produced a completed context —
	// the task pauses at the tmux gate for the sentinel/manual advance.
	ctxVal, err := engine.database.GetTaskStepContext(tk.ID, "implement")
	if err != nil {
		t.Fatalf("GetTaskStepContext: %v", err)
	}
	if ctxVal != "" {
		t.Errorf("tmux step must not capture a context at launch, got %q", ctxVal)
	}
}

// TestRunStepTmuxSetupCommandControlsAgent covers the userland-placement
// branch: when tmux-setup-command references {{run_agent}}/{{agent_command}},
// the session is created bare (no auto-started window-0 agent) and the setup
// command receives the agent record's command and the wrapper script path via
// SetupVars.
func TestRunStepTmuxSetupCommandControlsAgent(t *testing.T) {
	varsOut := "setup-vars.txt"
	setupCmd := `printf '%s\n%s\n' "{{agent_command}}" "{{run_agent}}" > ` + varsOut
	engine, tk, dir := newTmuxStepEngine(t, setupCmd)

	wf := engine.cfg.GetWorkflow("default")
	step := wf.Steps[0]
	_, agentCfg, err := engine.cfg.StepAgent(wf, &step)
	if err != nil {
		t.Fatalf("StepAgent: %v", err)
	}

	exitCode, outputTail, spawnErr := engine.runStepTmux(context.Background(), tk, step, agentCfg,
		"prompt body", map[string]string{"SAKUSEN_AGENT": "pair"}, nil)
	if spawnErr != nil {
		t.Fatalf("runStepTmux: %v", spawnErr)
	}
	if exitCode != 0 || outputTail != "" {
		t.Errorf("runStepTmux = (%d, %q), want fire-and-forget (0, \"\")", exitCode, outputTail)
	}

	data, err := os.ReadFile(filepath.Join(dir, varsOut))
	if err != nil {
		t.Fatalf("setup command did not run (vars file missing): %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("vars file = %q, want 2 lines (agent_command, run_agent)", string(data))
	}
	if lines[0] != "echo agent-here" {
		t.Errorf("{{agent_command}} = %q, want the agent record's command %q", lines[0], "echo agent-here")
	}
	if want := filepath.Join(dir, ".sakusen", "run-step-implement.sh"); lines[1] != want {
		t.Errorf("{{run_agent}} = %q, want the wrapper script path %q", lines[1], want)
	}
}
