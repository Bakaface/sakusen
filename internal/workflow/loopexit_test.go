package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
)

func loopExitCommandWorkflow(ec *config.LoopExitCondition) config.WorkflowConfig {
	return config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{
			{Name: "plan", Prompt: "plan the next unit"},
			{
				Name:   "implement",
				Prompt: "implement: {{steps.plan.context}}",
				Loop: &config.LoopConfig{
					Goto:          "plan",
					MaxIterations: 5,
					ExitCondition: ec,
				},
			},
		},
	}
}

func TestLoopExitCommand(t *testing.T) {
	t.Run("exit 0 ends the loop", func(t *testing.T) {
		engine, tk, runner, _ := newFakeRunnerTestEngine(t, loopExitCommandWorkflow(&config.LoopExitCondition{Command: "exit 0"}))
		if err := engine.RunTask(context.Background(), tk, nil); err != nil {
			t.Fatalf("RunTask: %v", err)
		}
		if got := len(runner.callsFor("plan")); got != 1 {
			t.Errorf("plan ran %d times, want 1", got)
		}
		logged, err := os.ReadFile(ProjectLogPath(engine.dataDir, tk.ID))
		if err != nil {
			t.Fatalf("read task log: %v", err)
		}
		if !strings.Contains(string(logged), "Loop exit: command `exit 0` exited 0") {
			t.Errorf("task log is missing the evaluation record:\n%s", logged)
		}
	})

	t.Run("exit 1 continues the loop", func(t *testing.T) {
		engine, tk, runner, _ := newFakeRunnerTestEngine(t, loopExitCommandWorkflow(&config.LoopExitCondition{
			Command: `test "$SAKUSEN_LOOP_ITERATION" -ge 2`,
		}))
		if err := engine.RunTask(context.Background(), tk, nil); err != nil {
			t.Fatalf("RunTask: %v", err)
		}
		if got := len(runner.callsFor("plan")); got != 3 {
			t.Errorf("plan ran %d times, want 3 (exit 1 on iterations 0 and 1, exit 0 on 2)", got)
		}
		logged, err := os.ReadFile(ProjectLogPath(engine.dataDir, tk.ID))
		if err != nil {
			t.Fatalf("read task log: %v", err)
		}
		if got := strings.Count(string(logged), "exited 1, continuing loop"); got != 2 {
			t.Errorf("task log has %d continue records, want 2:\n%s", got, logged)
		}
	})

	failCases := []struct {
		name    string
		ec      *config.LoopExitCondition
		wantErr string
		wantOut string
	}{
		{
			name:    "other status fails the task",
			ec:      &config.LoopExitCondition{Command: "echo cannot decide; exit 2"},
			wantErr: "exited 2",
			wantOut: "cannot decide",
		},
		{
			name:    "timeout fails the task",
			ec:      &config.LoopExitCondition{Command: "echo started; sleep 10", Timeout: "200ms"},
			wantErr: "timed out after 200ms",
			wantOut: "started",
		},
		{
			name:    "missing binary fails the task",
			ec:      &config.LoopExitCondition{Command: "sakusen-no-such-binary-645"},
			wantErr: "exited 127",
			wantOut: "sakusen-no-such-binary-645",
		},
	}
	for _, tc := range failCases {
		t.Run(tc.name, func(t *testing.T) {
			engine, tk, runner, database := newFakeRunnerTestEngine(t, loopExitCommandWorkflow(tc.ec))
			err := engine.RunTask(context.Background(), tk, nil)
			if err == nil {
				t.Fatal("RunTask succeeded, want the loop exit command to fail the task")
			}
			if !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), tc.wantOut) {
				t.Errorf("error = %q, want it to contain %q and the output %q", err, tc.wantErr, tc.wantOut)
			}
			if got := len(runner.callsFor("plan")); got != 1 {
				t.Errorf("plan ran %d times, want 1 (no silent continue)", got)
			}
			stored, err := database.GetTask(tk.ID)
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}
			if !strings.Contains(stored.ErrorMessage, tc.wantErr) {
				t.Errorf("failure reason = %q, want it to contain %q", stored.ErrorMessage, tc.wantErr)
			}
			logged, err := os.ReadFile(ProjectLogPath(engine.dataDir, tk.ID))
			if err != nil {
				t.Fatalf("read task log: %v", err)
			}
			if !strings.Contains(string(logged), tc.wantErr) || !strings.Contains(string(logged), tc.wantOut) {
				t.Errorf("task log is missing the failure or output:\n%s", logged)
			}
		})
	}

	t.Run("stdin, env and workdir", func(t *testing.T) {
		engine, tk, runner, _ := newFakeRunnerTestEngine(t, loopExitCommandWorkflow(&config.LoopExitCondition{
			Command:   `cat > stdin.txt; printf %s "$SAKUSEN_TASK_ID" > id.txt; grep -q DONE stdin.txt`,
			StdinFrom: "plan",
		}))
		tk.Workdir = "sub"
		workdir := filepath.Join(tk.WorktreePath, "sub")
		if err := os.Mkdir(workdir, 0755); err != nil {
			t.Fatal(err)
		}
		runner.script("plan", fakeAgentResult{resultText: "verdict: DONE"})

		if err := engine.RunTask(context.Background(), tk, nil); err != nil {
			t.Fatalf("RunTask: %v", err)
		}
		if got := len(runner.callsFor("plan")); got != 1 {
			t.Errorf("plan ran %d times, want 1", got)
		}
		stdin, err := os.ReadFile(filepath.Join(workdir, "stdin.txt"))
		if err != nil {
			t.Fatalf("command did not run in the task workdir: %v", err)
		}
		if strings.TrimSpace(string(stdin)) != "verdict: DONE" {
			t.Errorf("stdin = %q, want the plan step's context", stdin)
		}
		id, err := os.ReadFile(filepath.Join(workdir, "id.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(id) != fmt.Sprintf("%d", tk.ID) {
			t.Errorf("SAKUSEN_TASK_ID = %q, want %d", id, tk.ID)
		}
	})
}
