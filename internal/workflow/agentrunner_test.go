package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
)

// fakeAgentResult is one scripted return value for a fakeAgentRunner call.
type fakeAgentResult struct {
	exitCode   int
	resultText string
	outputTail string
	err        error
}

// fakeAgentCall records the inputs a single runHeadlessStep invocation was
// made with, so tests can assert on what the engine actually handed the
// runner (e.g. that a later step's resolved prompt contains an earlier
// step's captured context).
type fakeAgentCall struct {
	stepName string
	prompt   string
	agent    config.AgentConfig
	env      map[string]string
}

// fakeAgentRunner is the AGENT-RUNNER seam's test double (see agentRunner in
// step.go): it satisfies the agentRunner interface without spawning a real
// claude subprocess. Results are scripted per step name as a FIFO queue —
// each call to runHeadlessStep for a given step pops the next queued result
// for that step name (falling back to a zero-value success result once the
// queue is exhausted, so tests that don't care about a step's exact call
// count don't need to over-script it).
type fakeAgentRunner struct {
	mu      sync.Mutex
	results map[string][]fakeAgentResult
	calls   []fakeAgentCall
}

func newFakeAgentRunner() *fakeAgentRunner {
	return &fakeAgentRunner{results: make(map[string][]fakeAgentResult)}
}

// script queues a result to be returned by the next runHeadlessStep call for
// stepName.
func (f *fakeAgentRunner) script(stepName string, r fakeAgentResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[stepName] = append(f.results[stepName], r)
}

func (f *fakeAgentRunner) runHeadlessStep(ctx context.Context, e *Engine, t *task.Task, step config.StepConfig, agent config.AgentConfig, prompt string, envVars map[string]string, outputFn func([]string)) (int, string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	envCopy := make(map[string]string, len(envVars))
	for k, v := range envVars {
		envCopy[k] = v
	}
	f.calls = append(f.calls, fakeAgentCall{stepName: step.Name, prompt: prompt, agent: agent, env: envCopy})

	queue := f.results[step.Name]
	if len(queue) == 0 {
		return 0, "ok", "", nil
	}
	res := queue[0]
	f.results[step.Name] = queue[1:]
	return res.exitCode, res.resultText, res.outputTail, res.err
}

// callsFor returns the recorded calls for stepName, in invocation order.
func (f *fakeAgentRunner) callsFor(stepName string) []fakeAgentCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeAgentCall
	for _, c := range f.calls {
		if c.stepName == stepName {
			out = append(out, c)
		}
	}
	return out
}

// newFakeRunnerTestEngine wires a real (in-memory) DB-backed Engine, a
// non-worktree task (so no git repo is needed unless a scenario explicitly
// wants one), and a fakeAgentRunner installed in place of the production
// agentRunner. Direct field assignment after NewEngine mirrors the pattern
// already used for e.repo in fasttrack_test.go — Engine.runner's doc comment
// documents this as the sanctioned test seam.
func newFakeRunnerTestEngine(t *testing.T, wf config.WorkflowConfig) (*Engine, *task.Task, *fakeAgentRunner, *db.DB) {
	t.Helper()
	dir := t.TempDir()

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	proj, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	tk, err := database.CreateTask(proj.ID, "fake-runner task", "desc", "slug", wf.Name, "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	tk.Worktree = false
	tk.WorktreePath = dir

	cfg := &config.Config{
		OnComplete: "none",
		Workflows:  []config.WorkflowConfig{wf},
		// The implicit "claude" fallback agent must resolve (headless) so
		// runStep routes through the agentRunner seam instead of failing with
		// "no agent configured". The command is never spawned — the fake
		// runner intercepts the call.
		Agents: map[string]config.AgentConfig{"claude": {Command: "true"}},
	}
	engine := NewEngine(cfg, database, nil, dir)

	runner := newFakeAgentRunner()
	engine.runner = runner

	return engine, tk, runner, database
}

// TestRunTaskFakeRunner_MultiStepHappyPath proves the AGENT-RUNNER seam end
// to end: a two-step workflow runs entirely through the fake runner (no real
// claude subprocess), the first step's captured context is templated into
// the second step's resolved prompt, and both steps' contexts land in the
// store. Previously only tests/e2e exercised this multi-step flow.
func TestRunTaskFakeRunner_MultiStepHappyPath(t *testing.T) {
	wf := config.WorkflowConfig{
		Name: "default", // resolves to the headless "claude" agent: routes through the agentRunner seam, not tmux
		Steps: []config.StepConfig{
			{Name: "implement", Prompt: "implement the thing"},
			{Name: "review", Prompt: "review this: {{steps.implement.context}}"},
		},
	}
	engine, tk, runner, database := newFakeRunnerTestEngine(t, wf)

	runner.script("implement", fakeAgentResult{exitCode: 0, resultText: "IMPLEMENTATION SUMMARY"})
	runner.script("review", fakeAgentResult{exitCode: 0, resultText: "REVIEW SUMMARY"})

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	// The review step's resolved prompt must have seen implement's captured
	// context — this is what proves step contexts flow into later prompts
	// through the real template-resolution path, not just through the fake's
	// bookkeeping.
	reviewCalls := runner.callsFor("review")
	if len(reviewCalls) != 1 {
		t.Fatalf("expected exactly 1 call to the review step, got %d", len(reviewCalls))
	}
	if !strings.Contains(reviewCalls[0].prompt, "IMPLEMENTATION SUMMARY") {
		t.Errorf("review step prompt = %q, expected it to contain implement's captured context", reviewCalls[0].prompt)
	}

	// Assert through the store: both steps' contexts were persisted.
	implCtx, err := database.GetTaskStepContext(tk.ID, "implement")
	if err != nil {
		t.Fatalf("GetTaskStepContext(implement): %v", err)
	}
	if implCtx != "IMPLEMENTATION SUMMARY" {
		t.Errorf("implement step context = %q, want %q", implCtx, "IMPLEMENTATION SUMMARY")
	}
	reviewCtx, err := database.GetTaskStepContext(tk.ID, "review")
	if err != nil {
		t.Fatalf("GetTaskStepContext(review): %v", err)
	}
	if reviewCtx != "REVIEW SUMMARY" {
		t.Errorf("review step context = %q, want %q", reviewCtx, "REVIEW SUMMARY")
	}

	refreshed, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	// RunTask never writes StepIndex back to the DB after a step that falls
	// straight through (no loop-back, no approval/tmux pause) — only the
	// pause branch advances it to i+1. So after the last step completes
	// normally it's left pointing at that step's own index; finalization
	// (the daemon's job) is what actually closes the task out.
	if refreshed.StepIndex != len(wf.Steps)-1 {
		t.Errorf("StepIndex = %d, want %d (index of the last executed step)", refreshed.StepIndex, len(wf.Steps)-1)
	}
	// RunTask itself must not touch status — finalization is the daemon's job
	// (see TestRunTaskDoesNotSetSummarizingStatus for the equivalent
	// assertion on the real-subprocess path).
	if refreshed.Status != task.StatusRunning {
		t.Errorf("Status = %q, want unchanged %q", refreshed.Status, task.StatusRunning)
	}
}

// TestRunTaskFakeRunner_FailedStepFailsTask verifies that a step exiting
// with a non-zero code (as reported by the agentRunner) fails the task: the
// exit code and error message are persisted, and RunTask returns an error
// without running any further steps.
func TestRunTaskFakeRunner_FailedStepFailsTask(t *testing.T) {
	wf := config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{
			{Name: "implement", Prompt: "implement the thing"},
			{Name: "review", Prompt: "review the thing"},
		},
	}
	engine, tk, runner, database := newFakeRunnerTestEngine(t, wf)

	runner.script("implement", fakeAgentResult{exitCode: 3, resultText: "", outputTail: "boom: something broke"})

	err := engine.RunTask(context.Background(), tk, nil)
	if err == nil {
		t.Fatal("expected RunTask to return an error for a non-zero step exit code")
	}
	if !strings.Contains(err.Error(), `step "implement" exited with code 3`) {
		t.Errorf("error = %q, expected it to mention the failing step and exit code", err.Error())
	}

	if calls := runner.callsFor("review"); len(calls) != 0 {
		t.Errorf("expected the review step never to run after implement failed, got %d calls", len(calls))
	}

	refreshed, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if refreshed.ExitCode == nil || *refreshed.ExitCode != 3 {
		t.Errorf("persisted ExitCode = %v, want 3", refreshed.ExitCode)
	}
	if !strings.Contains(refreshed.ErrorMessage, "boom: something broke") {
		t.Errorf("persisted ErrorMessage = %q, expected it to include the output tail", refreshed.ErrorMessage)
	}
}

// TestRunTaskFakeRunner_LoopExitsAtMaxIterations verifies the loop-evaluation
// stage: a step configured with `loop.goto` back to an earlier step runs
// repeatedly until max_iterations is reached, then falls through (loop
// counter reset, no further looping) instead of pausing or failing.
func TestRunTaskFakeRunner_LoopExitsAtMaxIterations(t *testing.T) {
	wf := config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{
			{Name: "plan", Prompt: "plan iteration {{loop.iteration}}"},
			{
				Name:   "work",
				Prompt: "do the work",
				Loop:   &config.LoopConfig{Goto: "plan", MaxIterations: 1},
			},
		},
	}
	engine, tk, runner, database := newFakeRunnerTestEngine(t, wf)

	// Both steps run twice: the loop body executes once (iteration 0 -> 1,
	// still under max_iterations), then a second time where the iteration
	// counter (1) has reached max_iterations and the loop falls through.
	runner.script("plan", fakeAgentResult{exitCode: 0, resultText: "plan v1"})
	runner.script("work", fakeAgentResult{exitCode: 0, resultText: "work v1"})
	runner.script("plan", fakeAgentResult{exitCode: 0, resultText: "plan v2"})
	runner.script("work", fakeAgentResult{exitCode: 0, resultText: "work v2"})

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	planCalls := runner.callsFor("plan")
	workCalls := runner.callsFor("work")
	if len(planCalls) != 2 {
		t.Fatalf("expected plan to run twice (initial + one loop-back), got %d", len(planCalls))
	}
	if len(workCalls) != 2 {
		t.Fatalf("expected work to run twice (initial + one loop-back), got %d", len(workCalls))
	}
	// The loop-back re-resolved plan's prompt with the bumped iteration
	// number — proves the loop actually went through applyStepResult's
	// stepOutcomeGoto path and back through runStep, not just called twice
	// by coincidence.
	if !strings.Contains(planCalls[1].prompt, "iteration 1") {
		t.Errorf("second plan call prompt = %q, expected it to reflect loop.iteration=1", planCalls[1].prompt)
	}

	refreshed, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if refreshed.LoopIteration != 0 {
		t.Errorf("LoopIteration = %d, want 0 (reset after the loop exits)", refreshed.LoopIteration)
	}
	// See the equivalent comment in TestRunTaskFakeRunner_MultiStepHappyPath:
	// a step that falls through normally (here, the loop exiting rather than
	// looping back again) leaves StepIndex at its own index.
	if refreshed.StepIndex != len(wf.Steps)-1 {
		t.Errorf("StepIndex = %d, want %d (index of the last executed step, no pause)", refreshed.StepIndex, len(wf.Steps)-1)
	}
	if refreshed.Status != task.StatusRunning {
		t.Errorf("Status = %q, want unchanged %q (no approval/tmux pause on this path)", refreshed.Status, task.StatusRunning)
	}

	workCtx, err := database.GetTaskStepContext(tk.ID, "work")
	if err != nil {
		t.Fatalf("GetTaskStepContext(work): %v", err)
	}
	if workCtx != "work v2" {
		t.Errorf("work step context = %q, want %q (the final iteration's value)", workCtx, "work v2")
	}
}

// TestRunTaskFakeRunner_UnresolvableAgentFailsStep verifies the engine-level
// agent-resolution failure path (runStep, engine.go): a step whose `agent:`
// slug has no record fails the step with an instructive error BEFORE anything
// is spawned. An in-memory Config literal skips load-time validation, so the
// unresolvable slug is only caught here.
func TestRunTaskFakeRunner_UnresolvableAgentFailsStep(t *testing.T) {
	wf := config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{
			{Name: "implement", Prompt: "implement the thing", Agent: "no-such-agent"},
		},
	}
	engine, tk, runner, database := newFakeRunnerTestEngine(t, wf)

	err := engine.RunTask(context.Background(), tk, nil)
	if err == nil {
		t.Fatal("expected RunTask to fail for an unresolvable step agent")
	}
	if !strings.Contains(err.Error(), `step "implement" failed`) {
		t.Errorf("error = %q, expected it to name the failing step", err.Error())
	}
	if !strings.Contains(err.Error(), `no agent "no-such-agent" configured`) {
		t.Errorf("error = %q, expected the instructive no-agent message", err.Error())
	}

	// The failure happens before the spawn: the fake runner must never be called.
	if calls := runner.callsFor("implement"); len(calls) != 0 {
		t.Errorf("expected the runner never to be called, got %d calls", len(calls))
	}

	refreshed, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if refreshed.ExitCode == nil || *refreshed.ExitCode != 1 {
		t.Errorf("persisted ExitCode = %v, want 1", refreshed.ExitCode)
	}
	if !strings.Contains(refreshed.ErrorMessage, `no agent "no-such-agent" configured`) {
		t.Errorf("persisted ErrorMessage = %q, expected the no-agent message", refreshed.ErrorMessage)
	}
}

// TestRunTaskFakeRunner_AgentEnvAndRecord verifies that runStep hands the
// runner the RESOLVED agent record and exports the resolved slug as
// SAKUSEN_AGENT: a step-level `agent:` override wins over the workflow default,
// and a step without one falls back to the implicit "claude" record.
func TestRunTaskFakeRunner_AgentEnvAndRecord(t *testing.T) {
	wf := config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{
			{Name: "implement", Prompt: "implement the thing"},
			{Name: "review", Prompt: "review the thing", Agent: "alt"},
		},
	}
	engine, tk, runner, _ := newFakeRunnerTestEngine(t, wf)
	// newFakeRunnerTestEngine only injects the "claude" record; add the
	// override target to the shared registry.
	engine.cfg.full.Agents["alt"] = config.AgentConfig{Command: "alt-agent-cmd"}

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	implCalls := runner.callsFor("implement")
	if len(implCalls) != 1 {
		t.Fatalf("expected 1 implement call, got %d", len(implCalls))
	}
	if got := implCalls[0].env["SAKUSEN_AGENT"]; got != "claude" {
		t.Errorf("implement SAKUSEN_AGENT = %q, want %q (the implicit default)", got, "claude")
	}
	if got := implCalls[0].agent.Command; got != "true" {
		t.Errorf("implement resolved agent command = %q, want %q", got, "true")
	}

	reviewCalls := runner.callsFor("review")
	if len(reviewCalls) != 1 {
		t.Fatalf("expected 1 review call, got %d", len(reviewCalls))
	}
	if got := reviewCalls[0].env["SAKUSEN_AGENT"]; got != "alt" {
		t.Errorf("review SAKUSEN_AGENT = %q, want %q (the step-level override)", got, "alt")
	}
	if got := reviewCalls[0].agent.Command; got != "alt-agent-cmd" {
		t.Errorf("review resolved agent command = %q, want %q", got, "alt-agent-cmd")
	}
}

// TestRunTaskFakeRunner_AttachedImagesAppendedToPrompt verifies that attached
// images surface as an "## Attached Images" section appended to the step
// prompt itself — the prompt is the only agent-agnostic channel to point an
// agent at them.
func TestRunTaskFakeRunner_AttachedImagesAppendedToPrompt(t *testing.T) {
	wf := config.WorkflowConfig{
		Name:  "default",
		Steps: []config.StepConfig{{Name: "implement", Prompt: "implement the thing"}},
	}
	engine, tk, runner, _ := newFakeRunnerTestEngine(t, wf)

	img := filepath.Join(t.TempDir(), "screenshot.png")
	if err := os.WriteFile(img, []byte("fake png data"), 0644); err != nil {
		t.Fatal(err)
	}
	tk.Images = []string{img}

	runner.script("implement", fakeAgentResult{exitCode: 0, resultText: "done"})
	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	calls := runner.callsFor("implement")
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	prompt := calls[0].prompt
	if !strings.Contains(prompt, "## Attached Images") {
		t.Errorf("expected prompt to contain the Attached Images section, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "screenshot.png") {
		t.Errorf("expected prompt to reference the copied image, got:\n%s", prompt)
	}
}
