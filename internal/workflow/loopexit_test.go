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
		if strings.Contains(string(logged), "command output") {
			t.Errorf("task log has an output record for a silent command:\n%s", logged)
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
		if strings.Contains(string(logged), "command output") {
			t.Errorf("task log has an output record for a silent command:\n%s", logged)
		}
	})

	t.Run("output is logged on exit 0 and 1", func(t *testing.T) {
		engine, tk, runner, _ := newFakeRunnerTestEngine(t, loopExitCommandWorkflow(&config.LoopExitCondition{
			Command: `if [ "$SAKUSEN_LOOP_ITERATION" -ge 1 ]; then echo "met p=0.93"; exit 0; fi; echo "unmet p=0.04"; echo "retry 1: uncertain" >&2; exit 1`,
		}))
		if err := engine.RunTask(context.Background(), tk, nil); err != nil {
			t.Fatalf("RunTask: %v", err)
		}
		if got := len(runner.callsFor("plan")); got != 2 {
			t.Errorf("plan ran %d times, want 2", got)
		}
		logged, err := os.ReadFile(ProjectLogPath(engine.dataDir, tk.ID))
		if err != nil {
			t.Fatalf("read task log: %v", err)
		}
		if got := strings.Count(string(logged), "Loop exit: command output (tail):"); got != 2 {
			t.Errorf("task log has %d output records, want 2:\n%s", got, logged)
		}
		for _, want := range []string{"unmet p=0.04", "retry 1: uncertain", "met p=0.93"} {
			if !strings.Contains(string(logged), want) {
				t.Errorf("task log is missing the command output %q:\n%s", want, logged)
			}
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

func loopCapWorkflow(onMax string, ec *config.LoopExitCondition) config.WorkflowConfig {
	return config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{
			{Name: "plan", Prompt: "plan the next unit"},
			{
				Name:   "implement",
				Prompt: "implement: {{steps.plan.context}}",
				Loop: &config.LoopConfig{
					Goto:            "plan",
					MaxIterations:   2,
					OnMaxIterations: onMax,
					ExitCondition:   ec,
				},
			},
			{Name: "review", Prompt: "review"},
		},
	}
}

func TestLoopOnMaxIterations(t *testing.T) {
	marker := &config.LoopExitCondition{StepContextContains: "plan", Marker: "LOOP-EXIT"}
	const capReason = `loop on step "implement" reached max_iterations (2) without meeting its exit condition`

	passCases := []struct {
		name  string
		onMax string
		ec    *config.LoopExitCondition
		plans []string
	}{
		{name: "default continues past the cap", onMax: "", ec: marker},
		{name: "explicit continue continues past the cap", onMax: config.LoopOnMaxIterationsContinue, ec: marker},
		{
			name:  "fail with marker met on the last pass exits normally",
			onMax: config.LoopOnMaxIterationsFail,
			ec:    marker,
			plans: []string{"not yet", "not yet", "LOOP-EXIT"},
		},
		{
			name:  "fail with command exiting 0 on the last pass exits normally",
			onMax: config.LoopOnMaxIterationsFail,
			ec:    &config.LoopExitCondition{Command: `test "$SAKUSEN_LOOP_ITERATION" -ge 2`},
		},
	}
	for _, tc := range passCases {
		t.Run(tc.name, func(t *testing.T) {
			engine, tk, runner, _ := newFakeRunnerTestEngine(t, loopCapWorkflow(tc.onMax, tc.ec))
			for _, text := range tc.plans {
				runner.script("plan", fakeAgentResult{resultText: text})
			}
			if err := engine.RunTask(context.Background(), tk, nil); err != nil {
				t.Fatalf("RunTask: %v", err)
			}
			if got := len(runner.callsFor("plan")); got != 3 {
				t.Errorf("plan ran %d times, want 3", got)
			}
			if got := len(runner.callsFor("review")); got != 1 {
				t.Errorf("review ran %d times, want 1", got)
			}
		})
	}

	failCases := []struct {
		name    string
		ec      *config.LoopExitCondition
		wantErr string
	}{
		{name: "marker never met fails the task", ec: marker, wantErr: capReason},
		{name: "command exiting 1 on the last pass fails the task", ec: &config.LoopExitCondition{Command: "exit 1"}, wantErr: capReason},
		{
			name:    "command exiting 2 on the last pass fails the task",
			ec:      &config.LoopExitCondition{Command: `test "$SAKUSEN_LOOP_ITERATION" -ge 2 && exit 2; exit 1`},
			wantErr: "exited 2",
		},
	}
	for _, tc := range failCases {
		t.Run(tc.name, func(t *testing.T) {
			engine, tk, runner, database := newFakeRunnerTestEngine(t, loopCapWorkflow(config.LoopOnMaxIterationsFail, tc.ec))
			err := engine.RunTask(context.Background(), tk, nil)
			if err == nil {
				t.Fatal("RunTask succeeded, want the task to fail at the iteration cap")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
			if got := len(runner.callsFor("plan")); got != 3 {
				t.Errorf("plan ran %d times, want 3", got)
			}
			if got := len(runner.callsFor("review")); got != 0 {
				t.Errorf("review ran %d times, want 0 (no fall-through)", got)
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
			if tc.wantErr == capReason && !strings.Contains(string(logged), `=== Loop "implement" reached max_iterations (2) without meeting its exit condition ===`) {
				t.Errorf("task log is missing the cap failure:\n%s", logged)
			}
		})
	}
}

func selfLoopWorkflow(onMax string, ec *config.LoopExitCondition) config.WorkflowConfig {
	return config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{
			{
				Name:   "batch",
				Prompt: "batch pass {{loop.iteration}}/{{loop.max_iterations}}",
				Loop: &config.LoopConfig{
					Goto:            "batch",
					MaxIterations:   2,
					OnMaxIterations: onMax,
					ExitCondition:   ec,
				},
			},
			{Name: "review", Prompt: "review"},
		},
	}
}

func TestSelfLoop(t *testing.T) {
	t.Run("repeats until the exit command exits 0", func(t *testing.T) {
		engine, tk, runner, _ := newFakeRunnerTestEngine(t, selfLoopWorkflow(config.LoopOnMaxIterationsFail, &config.LoopExitCondition{
			Command: `test "$SAKUSEN_LOOP_ITERATION" -ge 1`,
		}))
		if err := engine.RunTask(context.Background(), tk, nil); err != nil {
			t.Fatalf("RunTask: %v", err)
		}
		calls := runner.callsFor("batch")
		if len(calls) != 2 {
			t.Fatalf("batch ran %d times, want 2", len(calls))
		}
		for i, c := range calls {
			if want := fmt.Sprintf("batch pass %d/2", i); !strings.Contains(c.prompt, want) {
				t.Errorf("batch call %d prompt = %q, want it to contain %q", i, c.prompt, want)
			}
		}
		if got := len(runner.callsFor("review")); got != 1 {
			t.Errorf("review ran %d times, want 1", got)
		}
		logged, err := os.ReadFile(ProjectLogPath(engine.dataDir, tk.ID))
		if err != nil {
			t.Fatalf("read task log: %v", err)
		}
		if !strings.Contains(string(logged), `=== Loop "batch" completed after 1 iteration(s) (max 2) ===`) {
			t.Errorf("task log is missing the loop completion:\n%s", logged)
		}
	})

	t.Run("fail at the cap fails the task", func(t *testing.T) {
		engine, tk, runner, database := newFakeRunnerTestEngine(t, selfLoopWorkflow(config.LoopOnMaxIterationsFail, &config.LoopExitCondition{Command: "exit 1"}))
		err := engine.RunTask(context.Background(), tk, nil)
		const capReason = `loop on step "batch" reached max_iterations (2) without meeting its exit condition`
		if err == nil || !strings.Contains(err.Error(), capReason) {
			t.Fatalf("RunTask error = %v, want it to contain %q", err, capReason)
		}
		if got := len(runner.callsFor("batch")); got != 3 {
			t.Errorf("batch ran %d times, want 3", got)
		}
		if got := len(runner.callsFor("review")); got != 0 {
			t.Errorf("review ran %d times, want 0 (no fall-through)", got)
		}
		stored, err := database.GetTask(tk.ID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if !strings.Contains(stored.ErrorMessage, capReason) {
			t.Errorf("failure reason = %q, want it to contain %q", stored.ErrorMessage, capReason)
		}
	})

	t.Run("continue at the cap falls through", func(t *testing.T) {
		engine, tk, runner, _ := newFakeRunnerTestEngine(t, selfLoopWorkflow(config.LoopOnMaxIterationsContinue, &config.LoopExitCondition{Command: "exit 1"}))
		if err := engine.RunTask(context.Background(), tk, nil); err != nil {
			t.Fatalf("RunTask: %v", err)
		}
		if got := len(runner.callsFor("batch")); got != 3 {
			t.Errorf("batch ran %d times, want 3", got)
		}
		if got := len(runner.callsFor("review")); got != 1 {
			t.Errorf("review ran %d times, want 1", got)
		}
	})
}
