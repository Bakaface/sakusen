package workflow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
)

// twoBranchWorkflow is the canonical fan-out shape: one group of two branches
// feeding a synthesis step that templates the aggregate.
func twoBranchWorkflow(require string) config.WorkflowConfig {
	return config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{
			{
				Name: "review",
				Parallel: &config.ParallelConfig{
					Require: require,
					Branches: []config.StepConfig{
						{Name: "review-a", Prompt: "review as {{branch.name}} using {{branch.agent}}"},
						{Name: "review-b", Prompt: "review it"},
					},
				},
			},
			{Name: "synthesize", Prompt: "aggregate:\n{{steps.review.context}}\nsolo:\n{{steps.review-a.context}}"},
		},
	}
}

func TestParallelGroupRunsBranchesConcurrently(t *testing.T) {
	engine, tk, runner, _ := newFakeRunnerTestEngine(t, twoBranchWorkflow(""))
	// Both branches sleep, so back-to-back execution could not produce
	// overlapping [startedAt, finishedAt] intervals.
	runner.script("review-a", fakeAgentResult{resultText: "A findings", delay: 150 * time.Millisecond})
	runner.script("review-b", fakeAgentResult{resultText: "B findings", delay: 150 * time.Millisecond})

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	a := runner.callsFor("review-a")
	b := runner.callsFor("review-b")
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("branch calls = %d/%d, want 1 each", len(a), len(b))
	}
	if !a[0].startedAt.Before(b[0].finishedAt) || !b[0].startedAt.Before(a[0].finishedAt) {
		t.Errorf("branches did not overlap: a=[%v..%v] b=[%v..%v]",
			a[0].startedAt, a[0].finishedAt, b[0].startedAt, b[0].finishedAt)
	}
}

func TestParallelGroupBranchEnvAndTemplateVars(t *testing.T) {
	engine, tk, runner, _ := newFakeRunnerTestEngine(t, twoBranchWorkflow(""))
	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	call := runner.callsFor("review-a")[0]
	if got := call.env["SAKUSEN_STEP"]; got != "review-a" {
		t.Errorf("SAKUSEN_STEP = %q, want the branch name", got)
	}
	if got := call.env["SAKUSEN_AGENT"]; got != "claude" {
		t.Errorf("SAKUSEN_AGENT = %q, want the resolved slug", got)
	}
	if want := "review as review-a using claude"; call.prompt != want {
		t.Errorf("branch prompt = %q, want %q ({{branch.*}} resolved)", call.prompt, want)
	}

	// Outside a branch, {{branch.*}} must resolve to "".
	synth := runner.callsFor("synthesize")[0]
	if strings.Contains(synth.prompt, "{{branch.") {
		t.Errorf("synthesize prompt leaked a branch placeholder: %q", synth.prompt)
	}
}

func TestParallelGroupAggregateAndRows(t *testing.T) {
	engine, tk, runner, database := newFakeRunnerTestEngine(t, twoBranchWorkflow(""))
	runner.script("review-a", fakeAgentResult{resultText: "A findings"})
	runner.script("review-b", fakeAgentResult{resultText: "B findings"})

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	rows, err := database.GetTaskStepRows(tk.ID)
	if err != nil {
		t.Fatalf("GetTaskStepRows: %v", err)
	}
	for name, wantCtx := range map[string]string{"review-a": "A findings", "review-b": "B findings"} {
		row, ok := rows[name]
		if !ok {
			t.Fatalf("no task_steps row for branch %q", name)
		}
		if row.Status != "completed" {
			t.Errorf("branch %q status = %q, want completed", name, row.Status)
		}
		if row.Context != wantCtx {
			t.Errorf("branch %q context = %q, want %q", name, row.Context, wantCtx)
		}
	}

	wantAggregate := "## review-a (claude)\n\nA findings\n\n## review-b (claude)\n\nB findings"
	if got := rows["review"].Context; got != wantAggregate {
		t.Errorf("group context =\n%q\nwant\n%q", got, wantAggregate)
	}
	if rows["review"].Status != "completed" {
		t.Errorf("group row status = %q, want completed", rows["review"].Status)
	}

	// The synthesis step must have seen both the aggregate and the per-branch ref.
	prompt := runner.callsFor("synthesize")[0].prompt
	if !strings.Contains(prompt, wantAggregate) {
		t.Errorf("synthesize prompt missing the aggregate:\n%s", prompt)
	}
	if !strings.Contains(prompt, "solo:\nA findings") {
		t.Errorf("synthesize prompt missing the per-branch ref:\n%s", prompt)
	}
}

func TestParallelRequireAllCancelsSiblingsAndFailsTask(t *testing.T) {
	engine, tk, runner, database := newFakeRunnerTestEngine(t, twoBranchWorkflow("all"))
	runner.script("review-a", fakeAgentResult{exitCode: 1})
	// review-b blocks until its context is cancelled — which only happens if
	// review-a's failure triggers the group's fail-fast cancel.
	runner.script("review-b", fakeAgentResult{block: true})

	err := engine.RunTask(context.Background(), tk, nil)
	if err == nil {
		t.Fatal("expected RunTask to fail when a require:all group loses a branch")
	}
	if !strings.Contains(err.Error(), `parallel group "review": 0/2 branches succeeded (require all)`) {
		t.Errorf("error = %q, want the group join message", err)
	}
	if !strings.Contains(err.Error(), "review-a: exit 1") {
		t.Errorf("error = %q, want it to name the failing branch and reason", err)
	}
	if !strings.Contains(err.Error(), "review-b: cancelled") {
		t.Errorf("error = %q, want the cancelled sibling reported", err)
	}

	rows, err := database.GetTaskStepRows(tk.ID)
	if err != nil {
		t.Fatalf("GetTaskStepRows: %v", err)
	}
	for _, name := range []string{"review-a", "review-b"} {
		if rows[name].Status != "failed" {
			t.Errorf("branch %q status = %q, want failed", name, rows[name].Status)
		}
	}
	// An ordinary failed step leaves its row 'running'; the group matches that.
	if rows["review"].Status != "running" {
		t.Errorf("group row status = %q, want running (unchanged, like any failed step)", rows["review"].Status)
	}
	if calls := runner.callsFor("synthesize"); len(calls) != 0 {
		t.Errorf("synthesize must not run after the group failed, got %d calls", len(calls))
	}
}

func TestParallelRequireAnySucceedsWithFailedBranchMarked(t *testing.T) {
	engine, tk, runner, database := newFakeRunnerTestEngine(t, twoBranchWorkflow("any"))
	runner.script("review-a", fakeAgentResult{resultText: "A findings"})
	runner.script("review-b", fakeAgentResult{exitCode: 1})

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	rows, err := database.GetTaskStepRows(tk.ID)
	if err != nil {
		t.Fatalf("GetTaskStepRows: %v", err)
	}
	want := "## review-a (claude)\n\nA findings\n\n## review-b (claude) (failed: exit 1)"
	if got := rows["review"].Context; got != want {
		t.Errorf("aggregate =\n%q\nwant\n%q", got, want)
	}
	if len(runner.callsFor("synthesize")) != 1 {
		t.Error("the task must advance past a require:any group that lost one branch")
	}
}

func TestParallelRequireCountThreshold(t *testing.T) {
	threeBranch := func(require string) config.WorkflowConfig {
		return config.WorkflowConfig{
			Name: "default",
			Steps: []config.StepConfig{{
				Name: "review",
				Parallel: &config.ParallelConfig{
					Require: require,
					Branches: []config.StepConfig{
						{Name: "r1", Prompt: "1"},
						{Name: "r2", Prompt: "2"},
						{Name: "r3", Prompt: "3"},
					},
				},
			}},
		}
	}

	t.Run("two of three succeed", func(t *testing.T) {
		engine, tk, runner, _ := newFakeRunnerTestEngine(t, threeBranch("2"))
		runner.script("r1", fakeAgentResult{resultText: "one"})
		runner.script("r2", fakeAgentResult{resultText: "two"})
		runner.script("r3", fakeAgentResult{exitCode: 1})
		if err := engine.RunTask(context.Background(), tk, nil); err != nil {
			t.Fatalf("RunTask: %v", err)
		}
	})

	t.Run("one of three is not enough", func(t *testing.T) {
		engine, tk, runner, _ := newFakeRunnerTestEngine(t, threeBranch("2"))
		runner.script("r1", fakeAgentResult{resultText: "one"})
		runner.script("r2", fakeAgentResult{exitCode: 1})
		runner.script("r3", fakeAgentResult{exitCode: 1})
		err := engine.RunTask(context.Background(), tk, nil)
		if err == nil || !strings.Contains(err.Error(), "1/3 branches succeeded (require 2)") {
			t.Fatalf("err = %v, want the 1/3 join failure", err)
		}
	})
}

func TestParallelBranchTimeoutOverridesGroup(t *testing.T) {
	wf := config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{{
			Name:    "review",
			Timeout: "30m",
			Parallel: &config.ParallelConfig{
				Require: "any",
				Branches: []config.StepConfig{
					{Name: "fast", Prompt: "f", Timeout: "10ms"},
					{Name: "slow", Prompt: "s"},
				},
			},
		}},
	}
	engine, tk, runner, database := newFakeRunnerTestEngine(t, wf)
	// The fake honours ctx cancellation, and the engine's own per-step timeout
	// wrapper is applied by runHeadlessAgent — which the fake replaces. Script
	// the deadline error directly to exercise the reason formatting.
	runner.script("fast", fakeAgentResult{err: context.DeadlineExceeded})
	runner.script("slow", fakeAgentResult{resultText: "slow findings"})

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	rows, err := database.GetTaskStepRows(tk.ID)
	if err != nil {
		t.Fatalf("GetTaskStepRows: %v", err)
	}
	if !strings.Contains(rows["review"].Context, "## fast (claude) (failed: timed out after 10ms)") {
		t.Errorf("aggregate =\n%s\nwant the branch's own 10ms timeout in the reason", rows["review"].Context)
	}
}

func TestParallelBranchStrategyNoneYieldsNoContext(t *testing.T) {
	wf := config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{{
			Name: "review",
			Parallel: &config.ParallelConfig{Branches: []config.StepConfig{
				{Name: "quiet", Prompt: "q", SummarizationStrategy: config.SummarizationStrategyNone},
				{Name: "loud", Prompt: "l"},
			}},
		}},
	}
	engine, tk, runner, database := newFakeRunnerTestEngine(t, wf)
	runner.script("quiet", fakeAgentResult{resultText: "ignored"})
	runner.script("loud", fakeAgentResult{resultText: "kept"})

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	rows, _ := database.GetTaskStepRows(tk.ID)
	if !strings.Contains(rows["review"].Context, "## quiet (claude) (no context)") {
		t.Errorf("aggregate =\n%s\nwant the strategy:none branch marked (no context)", rows["review"].Context)
	}
	if !strings.Contains(rows["review"].Context, "## loud (claude)\n\nkept") {
		t.Errorf("aggregate =\n%s\nwant the other branch's context intact", rows["review"].Context)
	}
}

func TestParallelDefaultBranchStrategySkipsSummarizer(t *testing.T) {
	// A branch that leaves summarization_strategy unset defaults to
	// last_message, so even a huge chat log must not trigger a summarizer pass
	// (which would rewrite the review the aggregate is built from).
	wf := twoBranchWorkflow("all")
	engine, tk, runner, database := newFakeRunnerTestEngine(t, wf)
	bigChat := strings.Repeat("chatter line\n", 1000)
	runner.script("review-a", fakeAgentResult{resultText: "A findings", chatOutput: bigChat})
	runner.script("review-b", fakeAgentResult{resultText: "B findings", chatOutput: bigChat})

	// No summarizer is configured on the test engine, so a pass would fail
	// loudly rather than silently succeed; assert on the stored value instead.
	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	rows, _ := database.GetTaskStepRows(tk.ID)
	if rows["review-a"].Context != "A findings" {
		t.Errorf("branch context = %q, want the untouched result text", rows["review-a"].Context)
	}
}

func TestParallelCompletedBranchIsSkippedAndReused(t *testing.T) {
	wf := twoBranchWorkflow("all")
	engine, tk, runner, database := newFakeRunnerTestEngine(t, wf)

	// Pre-seed review-a as already completed, exactly as a prior run would.
	if err := database.CreateTaskStep(tk.ID, "review-a"); err != nil {
		t.Fatalf("CreateTaskStep: %v", err)
	}
	prior := "PRIOR A FINDINGS"
	if err := database.CompleteTaskStep(tk.ID, "review-a", &prior, 0); err != nil {
		t.Fatalf("CompleteTaskStep: %v", err)
	}
	runner.script("review-b", fakeAgentResult{resultText: "B findings"})

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if calls := runner.callsFor("review-a"); len(calls) != 0 {
		t.Errorf("a completed branch row must never be re-run, got %d calls", len(calls))
	}
	rows, _ := database.GetTaskStepRows(tk.ID)
	if !strings.Contains(rows["review"].Context, "## review-a (claude)\n\nPRIOR A FINDINGS") {
		t.Errorf("aggregate =\n%s\nwant the reused prior context", rows["review"].Context)
	}
}

func TestParallelLoopBackClearsBranchRows(t *testing.T) {
	// A loop whose range spans the group must re-run every branch: the
	// completed-branch skip would otherwise freeze the group at pass 1.
	wf := config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{
			{
				Name: "review",
				Parallel: &config.ParallelConfig{Branches: []config.StepConfig{
					{Name: "review-a", Prompt: "a"},
				}},
			},
			{
				Name:   "fix",
				Prompt: "fix",
				Loop:   &config.LoopConfig{Goto: "review", MaxIterations: 1},
			},
		},
	}
	engine, tk, runner, _ := newFakeRunnerTestEngine(t, wf)
	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if calls := runner.callsFor("review-a"); len(calls) != 2 {
		t.Errorf("branch calls = %d, want 2 (initial + one loop-back)", len(calls))
	}
}

func TestParallelLoopExitConditionOnBranchContext(t *testing.T) {
	wf := config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{
			{
				Name: "review",
				Parallel: &config.ParallelConfig{Branches: []config.StepConfig{
					{Name: "review-a", Prompt: "a"},
				}},
			},
			{
				Name:   "fix",
				Prompt: "fix",
				Loop: &config.LoopConfig{
					Goto: "review", MaxIterations: 5,
					ExitCondition: &config.LoopExitCondition{StepContextContains: "review-a", Marker: "ALL CLEAR"},
				},
			},
		},
	}
	engine, tk, runner, _ := newFakeRunnerTestEngine(t, wf)
	runner.script("review-a", fakeAgentResult{resultText: "issues remain"})
	runner.script("review-a", fakeAgentResult{resultText: "ALL CLEAR"})

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if calls := runner.callsFor("review-a"); len(calls) != 2 {
		t.Errorf("branch calls = %d, want 2 (the marker ends the loop on pass 2)", len(calls))
	}
}

func TestParallelManualBranchContextWinsOverResultText(t *testing.T) {
	wf := config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{{
			Name: "review",
			Parallel: &config.ParallelConfig{Branches: []config.StepConfig{
				{Name: "review-a", Prompt: "a"},
			}},
		}},
	}
	engine, tk, runner, database := newFakeRunnerTestEngine(t, wf)
	// Simulate the branch calling update_step_context mid-run: the fake blocks
	// long enough for the write to land on its 'running' row.
	runner.script("review-a", fakeAgentResult{resultText: "result text", delay: 200 * time.Millisecond})
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			n, err := database.UpdateRunningTaskStepContext(tk.ID, "review-a", "MANUAL ARTIFACT", false)
			if err == nil && n > 0 {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	rows, _ := database.GetTaskStepRows(tk.ID)
	if rows["review-a"].Context != "MANUAL ARTIFACT" {
		t.Errorf("branch context = %q, want the manual override", rows["review-a"].Context)
	}
}

func TestParallelLoggingSplitsTaskLogFromBranchLogs(t *testing.T) {
	engine, tk, runner, _ := newFakeRunnerTestEngine(t, twoBranchWorkflow("all"))
	runner.script("review-a", fakeAgentResult{resultText: "A", chatOutput: "branch-a stdout"})
	runner.script("review-b", fakeAgentResult{resultText: "B", chatOutput: "branch-b stdout"})

	var live []string
	outputFn := func(lines []string) { live = append(live, lines...) }
	if err := engine.RunTask(context.Background(), tk, outputFn); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	taskLog, err := os.ReadFile(ProjectLogPath(engine.dataDir, tk.ID))
	if err != nil {
		t.Fatalf("read task log: %v", err)
	}
	for _, want := range []string{
		"=== parallel review: started 2 branches ===",
		"=== parallel review: 2/2 succeeded ===",
	} {
		if !strings.Contains(string(taskLog), want) {
			t.Errorf("task.log missing marker %q:\n%s", want, taskLog)
		}
	}
	if strings.Contains(string(taskLog), "branch-a stdout") {
		t.Errorf("branch agent output must never reach task.log:\n%s", taskLog)
	}

	for name, want := range map[string]string{"review-a": "branch-a stdout", "review-b": "branch-b stdout"} {
		data, err := os.ReadFile(BranchLogPath(engine.dataDir, tk.ID, name))
		if err != nil {
			t.Fatalf("read branch log %q: %v", name, err)
		}
		if !strings.Contains(string(data), want) {
			t.Errorf("branch log %q missing its output:\n%s", name, data)
		}
		if !strings.Contains(string(data), "=== Step: "+name+" (task #") {
			t.Errorf("branch log %q missing the step header region layout:\n%s", name, data)
		}
	}
}

func TestParallelSpawnPathsArePerBranch(t *testing.T) {
	engine, tk, runner, _ := newFakeRunnerTestEngine(t, twoBranchWorkflow("all"))
	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	a := runner.callsFor("review-a")[0].spawn
	b := runner.callsFor("review-b")[0].spawn
	if a.LogPath == b.LogPath || a.OutputFile == b.OutputFile {
		t.Errorf("branches must get distinct spawn paths, got %+v and %+v", a, b)
	}
	if a.Tag != "review-a" {
		t.Errorf("spawn tag = %q, want the branch name", a.Tag)
	}
	// The synthesis step keeps today's defaults.
	if s := runner.callsFor("synthesize")[0].spawn; s != (headlessSpawn{}) {
		t.Errorf("ordinary step spawn = %+v, want the zero value", s)
	}
}

func TestTagLinesInsertsBranchTagAfterTimestamp(t *testing.T) {
	got := tagLines([]string{"[14:02:11] Reading file", "no timestamp"}, "review-opus")
	want := []string{"[14:02:11] [review-opus] Reading file", "[review-opus] no timestamp"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tagLines[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// An untagged spawn must not copy or alter anything.
	in := []string{"[14:02:11] plain"}
	if out := tagLines(in, ""); &out[0] != &in[0] {
		t.Error("tagLines with an empty tag must return the input slice unchanged")
	}
}

func TestFormatParallelAggregateGolden(t *testing.T) {
	got := FormatParallelAggregate([]ParallelBranchResult{
		{Name: "review-opus", Agent: "claude:opus", Context: "  found three issues\n\n"},
		{Name: "review-codex", Agent: "codex", FailReason: "timed out after 30m"},
		{Name: "review-none", Agent: "claude"},
	})
	want := "## review-opus (claude:opus)\n\nfound three issues\n\n" +
		"## review-codex (codex) (failed: timed out after 30m)\n\n" +
		"## review-none (claude) (no context)"
	if got != want {
		t.Errorf("aggregate =\n%q\nwant\n%q", got, want)
	}
}

func TestBranchLogAndOutputPaths(t *testing.T) {
	if got := BranchLogPath("/data", 7, "review-a"); got != "/data/logs/7/branch-review-a.log" {
		t.Errorf("BranchLogPath = %q", got)
	}
	if got := branchOutputFile("/wt", "review-a"); got != "/wt/.sakusen/output-review-a.log" {
		t.Errorf("branchOutputFile = %q", got)
	}
}

// TestParallelBranchRowStatusValues pins the exact status strings the daemon
// and TUI key off, so a rename here cannot silently break them.
func TestParallelBranchRowStatusValues(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	proj, err := database.GetOrCreateProject(t.TempDir())
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	tk, err := database.CreateTask(proj.ID, "t", "", "s", "wf", "", "running", nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := database.CreateTaskStep(tk.ID, "b"); err != nil {
		t.Fatalf("CreateTaskStep: %v", err)
	}
	if err := database.FailTaskStep(tk.ID, "b", 3); err != nil {
		t.Fatalf("FailTaskStep: %v", err)
	}
	rows, err := database.GetTaskStepRows(tk.ID)
	if err != nil {
		t.Fatalf("GetTaskStepRows: %v", err)
	}
	if rows["b"].Status != "failed" {
		t.Errorf("status = %q, want failed", rows["b"].Status)
	}
	if !rows["b"].CompletedAt.Valid {
		t.Error("FailTaskStep must stamp completed_at")
	}
}

func TestParallelSummarizeChatBranchRunsAfterJoin(t *testing.T) {
	wf := config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{{
			Name: "review",
			Parallel: &config.ParallelConfig{Branches: []config.StepConfig{
				{Name: "review-a", Prompt: "a", SummarizationStrategy: config.SummarizationStrategySummarizeChat},
				{Name: "review-b", Prompt: "b"},
			}},
		}},
	}
	engine, tk, runner, database := newFakeRunnerTestEngine(t, wf)
	// A bare summarizer command: the prompt arrives on stdin, the summary on
	// stdout. Counting invocations via an appended marker file proves the pass
	// ran exactly once and only for the summarize_chat branch.
	marker := filepath.Join(t.TempDir(), "calls")
	engine.cfg.Summarizer = config.SummarizerConfig{
		Command: fmt.Sprintf("cat > /dev/null; echo x >> %s; echo 'BRANCH SUMMARY'", marker),
	}

	// The chat must clear smallChatBytes, or shouldSummarizeChat keeps the
	// result text (the same gate ordinary steps use).
	bigChat := strings.Repeat("agent chatter line\n", 500)
	runner.script("review-a", fakeAgentResult{resultText: "raw A", chatOutput: bigChat})
	runner.script("review-b", fakeAgentResult{resultText: "raw B", chatOutput: bigChat})

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	rows, _ := database.GetTaskStepRows(tk.ID)
	if rows["review-a"].Context != "BRANCH SUMMARY" {
		t.Errorf("summarize_chat branch context = %q, want the summary", rows["review-a"].Context)
	}
	if rows["review-b"].Context != "raw B" {
		t.Errorf("default-strategy branch context = %q, want the untouched result text", rows["review-b"].Context)
	}
	if !strings.Contains(rows["review"].Context, "## review-a (claude)\n\nBRANCH SUMMARY") {
		t.Errorf("aggregate must carry the summary, got:\n%s", rows["review"].Context)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("summarizer was never invoked: %v", err)
	}
	if n := strings.Count(string(data), "x"); n != 1 {
		t.Errorf("summarizer invocations = %d, want exactly 1", n)
	}
}

// isolatedWorkflow is one isolated group feeding a step that templates the
// first branch's checkout.
func isolatedWorkflow(require string, names ...string) config.WorkflowConfig {
	branches := make([]config.StepConfig, len(names))
	for i, n := range names {
		branches[i] = config.StepConfig{Name: n, Prompt: "work as {{branch.name}}"}
	}
	return config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{
			{
				Name: "impl",
				Parallel: &config.ParallelConfig{
					Require:   require,
					Isolation: config.IsolationWorktree,
					Branches:  branches,
				},
			},
			{Name: "pick", Prompt: "branch={{branches." + names[0] + ".branch}} worktree={{branches." + names[0] + ".worktree}}"},
		},
	}
}

// newIsolatedTestEngine is newFakeRunnerTestEngine on a real git repo with a
// worktree-mode task, so isolated groups can provision branch worktrees.
func newIsolatedTestEngine(t *testing.T, wf config.WorkflowConfig) (*Engine, *task.Task, *fakeAgentRunner, *db.DB) {
	t.Helper()
	engine, tk, runner, database := newFakeRunnerTestEngine(t, wf)
	dir := engine.repoRoot
	runFastTrackGit(t, dir, "init")
	runFastTrackGit(t, dir, "config", "user.email", "test@test.com")
	runFastTrackGit(t, dir, "config", "user.name", "Test")
	runFastTrackGit(t, dir, "checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".sakusen/\nsynced.env\nsetup-marker\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init"), 0644); err != nil {
		t.Fatal(err)
	}
	runFastTrackGit(t, dir, "add", "-A")
	runFastTrackGit(t, dir, "commit", "-m", "initial commit")

	tk.Worktree = true
	tk.WorktreePath = ""
	tk.Branch = fmt.Sprintf("task-%d", tk.ID)
	return engine, tk, runner, database
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestParallelIsolatedFailsWithoutWorktree(t *testing.T) {
	engine, tk, runner, database := newFakeRunnerTestEngine(t, isolatedWorkflow("all", "impl-a", "impl-b"))

	err := engine.RunTask(context.Background(), tk, nil)
	if err == nil || !strings.Contains(err.Error(), `parallel group "impl": isolation: worktree requires a worktree`) {
		t.Fatalf("err = %v, want the requires-a-worktree failure", err)
	}
	if n := len(runner.callsFor("impl-a")) + len(runner.callsFor("impl-b")); n != 0 {
		t.Errorf("no branch may launch, got %d calls", n)
	}
	rows, _ := database.GetTaskStepRows(tk.ID)
	for _, name := range []string{"impl-a", "impl-b"} {
		if _, ok := rows[name]; ok {
			t.Errorf("branch row %q created, want none", name)
		}
	}
}

func TestParallelIsolatedProvisionsBranchWorktrees(t *testing.T) {
	wf := isolatedWorkflow("all", "impl-a", "impl-b")
	engine, tk, runner, _ := newIsolatedTestEngine(t, wf)
	if err := os.WriteFile(filepath.Join(engine.repoRoot, "synced.env"), []byte("SECRET=1"), 0644); err != nil {
		t.Fatal(err)
	}
	engine.cfg.full.WorktreeSyncPaths = config.WorktreeSyncPathsConfig{Copy: []string{"synced.env"}}
	engine.cfg.full.WorktreeSetupCommand = "touch {{worktree_path}}/setup-marker"

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	taskHead := gitOutput(t, tk.WorktreePath, "rev-parse", "HEAD")
	for _, name := range []string{"impl-a", "impl-b"} {
		path := IsolatedBranchWorktreePath(tk.WorktreePath, name)
		branch := IsolatedBranchGitBranch(tk.Branch, name)

		calls := runner.callsFor(name)
		if len(calls) != 1 {
			t.Fatalf("%s calls = %d, want 1", name, len(calls))
		}
		if calls[0].worktreePath != path {
			t.Errorf("%s ran in %q, want its own worktree %q", name, calls[0].worktreePath, path)
		}
		if got := calls[0].env["SAKUSEN_WORKTREE"]; got != path {
			t.Errorf("%s SAKUSEN_WORKTREE = %q, want %q", name, got, path)
		}
		if got := gitOutput(t, path, "rev-parse", "--abbrev-ref", "HEAD"); got != branch {
			t.Errorf("%s git branch = %q, want %q", name, got, branch)
		}
		if got := gitOutput(t, path, "rev-parse", "HEAD"); got != taskHead {
			t.Errorf("%s base = %q, want the task HEAD %q", name, got, taskHead)
		}
		if _, err := os.Stat(filepath.Join(path, "synced.env")); err != nil {
			t.Errorf("%s missing synced file: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(path, "setup-marker")); err != nil {
			t.Errorf("%s missing setup-command marker: %v", name, err)
		}
	}
	if got := runner.callsFor("pick")[0].worktreePath; got != tk.WorktreePath {
		t.Errorf("the step after the group ran in %q, want the task worktree %q", got, tk.WorktreePath)
	}
}

func TestParallelIsolatedCommitsAndExposesBranchVars(t *testing.T) {
	engine, tk, runner, _ := newIsolatedTestEngine(t, isolatedWorkflow("all", "impl-a", "impl-b"))
	runner.script("impl-a", fakeAgentResult{resultText: "done", onRun: func(bt *task.Task) {
		_ = os.WriteFile(filepath.Join(bt.WorktreePath, "a.txt"), []byte("a"), 0644)
	}})

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	branch := IsolatedBranchGitBranch(tk.Branch, "impl-a")
	path := IsolatedBranchWorktreePath(tk.WorktreePath, "impl-a")
	if log := gitOutput(t, engine.repoRoot, "log", "--format=%s", branch, "--", "a.txt"); !strings.Contains(log, "branch impl-a") {
		t.Errorf("git log %s -- a.txt = %q, want the branch commit", branch, log)
	}
	if status := gitOutput(t, path, "status", "--porcelain"); status != "" {
		t.Errorf("branch worktree left dirty after commit:\n%s", status)
	}

	want := "branch=" + branch + " worktree=" + path
	if got := runner.callsFor("pick")[0].prompt; got != want {
		t.Errorf("next step prompt = %q, want %q", got, want)
	}
}

func TestParallelIsolatedRequireOneWaitsForAll(t *testing.T) {
	engine, tk, runner, database := newIsolatedTestEngine(t, isolatedWorkflow("1", "impl-a", "impl-b"))
	runner.script("impl-a", fakeAgentResult{resultText: "fast"})
	runner.script("impl-b", fakeAgentResult{resultText: "slow", delay: 200 * time.Millisecond})

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	rows, _ := database.GetTaskStepRows(tk.ID)
	if rows["impl-b"].Status != "completed" {
		t.Errorf("slow branch status = %q, want completed (not cancelled by the first success)", rows["impl-b"].Status)
	}
}

func TestParallelIsolatedFailsWhenThresholdUnreachable(t *testing.T) {
	wf := isolatedWorkflow("2", "r1", "r2", "r3")
	engine, tk, runner, database := newIsolatedTestEngine(t, wf)
	runner.script("r1", fakeAgentResult{exitCode: 1})
	runner.script("r2", fakeAgentResult{exitCode: 1})
	// r3 only returns once cancelled, which must happen as soon as two
	// failures make require 2 of 3 unreachable.
	runner.script("r3", fakeAgentResult{block: true})

	err := engine.RunTask(context.Background(), tk, nil)
	if err == nil || !strings.Contains(err.Error(), "0/3 branches succeeded (require 2)") {
		t.Fatalf("err = %v, want the 0/3 join failure", err)
	}
	if !strings.Contains(err.Error(), "r3: cancelled") {
		t.Errorf("err = %v, want the blocked branch reported cancelled", err)
	}
	rows, _ := database.GetTaskStepRows(tk.ID)
	if rows["r3"].Status != "failed" {
		t.Errorf("r3 status = %q, want failed", rows["r3"].Status)
	}
}

func TestParallelIsolatedRetryReusesWorktree(t *testing.T) {
	engine, tk, runner, _ := newIsolatedTestEngine(t, isolatedWorkflow("all", "impl-a", "impl-b"))
	runner.script("impl-b", fakeAgentResult{exitCode: 1})
	if err := engine.RunTask(context.Background(), tk, nil); err == nil {
		t.Fatal("expected the first run to fail on impl-b")
	}

	path := IsolatedBranchWorktreePath(tk.WorktreePath, "impl-b")
	if err := os.WriteFile(filepath.Join(path, "committed.txt"), []byte("c"), 0644); err != nil {
		t.Fatal(err)
	}
	runFastTrackGit(t, path, "add", "committed.txt")
	runFastTrackGit(t, path, "commit", "-m", "earlier work")
	priorHead := gitOutput(t, path, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(path, "untracked.txt"), []byte("u"), 0644); err != nil {
		t.Fatal(err)
	}

	var sawUntracked, sawHead bool
	runner.script("impl-b", fakeAgentResult{resultText: "ok", onRun: func(bt *task.Task) {
		_, err := os.Stat(filepath.Join(bt.WorktreePath, "untracked.txt"))
		sawUntracked = err == nil
		sawHead = gitOutput(t, bt.WorktreePath, "rev-parse", "HEAD") == priorHead
	}})
	tk.StepIndex = 0
	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("retry RunTask: %v", err)
	}
	if !sawUntracked || !sawHead {
		t.Errorf("retry saw untracked=%v priorHead=%v, want the existing worktree reused", sawUntracked, sawHead)
	}
	if n := len(runner.callsFor("impl-a")); n != 1 {
		t.Errorf("completed impl-a ran %d times, want 1 (skipped on retry)", n)
	}
}

func TestParallelIsolatedRetryReprovisionsMissingWorktree(t *testing.T) {
	engine, tk, runner, database := newIsolatedTestEngine(t, isolatedWorkflow("all", "impl-a", "impl-b"))
	runner.script("impl-b", fakeAgentResult{resultText: "ok", onRun: func(bt *task.Task) {
		_ = os.WriteFile(filepath.Join(bt.WorktreePath, "b.txt"), []byte("b"), 0644)
	}})
	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	path := IsolatedBranchWorktreePath(tk.WorktreePath, "impl-b")
	branchHead := gitOutput(t, path, "rev-parse", "HEAD")
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	runFastTrackGit(t, engine.repoRoot, "worktree", "prune")
	if err := database.DeleteTaskStepsFrom(tk.ID, []string{"impl", "impl-b"}); err != nil {
		t.Fatalf("DeleteTaskStepsFrom: %v", err)
	}

	tk.StepIndex = 0
	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("retry RunTask: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "b.txt")); err != nil {
		t.Errorf("re-provisioned worktree missing the branch's earlier commit: %v", err)
	}
	if got := gitOutput(t, path, "rev-parse", "HEAD"); got != branchHead {
		t.Errorf("re-provisioned HEAD = %q, want the surviving branch tip %q", got, branchHead)
	}
}
