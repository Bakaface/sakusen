package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
)

// fakeFanoutHost inserts real child rows, the way the daemon's host does,
// minus title refinement and broadcasting.
type fakeFanoutHost struct {
	database *db.DB
	calls    []FanoutSpawnRequest
	// failAfter makes every spawn fail once this many have succeeded (0 = never).
	failAfter int
	spawned   int
}

func (h *fakeFanoutHost) SpawnChild(parent *task.Task, req FanoutSpawnRequest) (*task.Task, error) {
	h.calls = append(h.calls, req)
	if h.failAfter > 0 && h.spawned >= h.failAfter {
		return nil, errors.New("host unavailable")
	}
	child, err := h.database.CreateFanoutChildTask(parent.ProjectID, req.Title, req.Input, req.Item, req.Workflow, "", "", "", "",
		task.StatusInit, req.Priority, true, nil, req.TrackID, task.FanoutOrigin{ParentID: parent.ID, Step: req.Step, Item: req.Item})
	if err != nil {
		return nil, err
	}
	for _, b := range req.BlockedBy {
		if err := h.database.AddTaskDependency(child.ID, b); err != nil {
			return nil, err
		}
	}
	h.spawned++
	return h.database.GetTask(child.ID)
}

// fanoutWorkflow: plan publishes items, units fans them out, after consumes
// the aggregate.
func fanoutWorkflow(require string) config.WorkflowConfig {
	return config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{
			{Name: "plan", Prompt: "plan it"},
			{Name: "units", Fanout: &config.FanoutConfig{
				From:     "plan",
				Workflow: "unit",
				Input:    "{{item.input}} (item {{item.index}} of plan: {{steps.plan.context}})",
				Require:  require,
			}},
			{Name: "after", Prompt: "results:\n{{steps.units.context}}"},
		},
	}
}

func newFanoutTestEngine(t *testing.T, wf config.WorkflowConfig) (*Engine, *task.Task, *fakeAgentRunner, *db.DB, *fakeFanoutHost) {
	t.Helper()
	engine, tk, runner, database := newFakeRunnerTestEngine(t, wf)
	host := &fakeFanoutHost{database: database}
	engine.SetFanoutHost(host)
	return engine, tk, runner, database, host
}

func items(slugs ...string) []task.PublishedItem {
	out := make([]task.PublishedItem, len(slugs))
	for i, s := range slugs {
		out[i] = task.PublishedItem{Slug: s, Input: "build " + s}
	}
	return out
}

// publishOnRun scripts the plan step to publish list while it runs.
func publishOnRun(t *testing.T, runner *fakeAgentRunner, database *db.DB, tk *task.Task, list []task.PublishedItem) {
	t.Helper()
	runner.script("plan", fakeAgentResult{resultText: "PLAN", onRun: func(*task.Task) {
		if err := database.PublishItems(tk.ID, "plan", list); err != nil {
			t.Errorf("PublishItems: %v", err)
		}
	}})
}

// finishChild drives a child through the legal transitions to a terminal status.
func finishChild(t *testing.T, database *db.DB, id int64, status task.Status, context string) {
	t.Helper()
	for _, s := range []task.Status{task.StatusPending, task.StatusRunning} {
		if err := database.UpdateTaskStatus(id, s); err != nil {
			t.Fatalf("child #%d -> %s: %v", id, s, err)
		}
	}
	if context != "" {
		if err := database.UpdateTaskContext(id, context); err != nil {
			t.Fatalf("UpdateTaskContext: %v", err)
		}
	}
	if status == task.StatusFailed {
		if err := database.UpdateTaskStatusError(id, status, "step \"build\" exited with code 1\nmore detail"); err != nil {
			t.Fatalf("child #%d -> failed: %v", id, err)
		}
		return
	}
	if err := database.UpdateTaskStatus(id, status); err != nil {
		t.Fatalf("child #%d -> %s: %v", id, status, err)
	}
}

// resumeParent mimics the poller waking a suspended parent and returns the
// reloaded task ready for RunTask.
func resumeParent(t *testing.T, database *db.DB, tk *task.Task) *task.Task {
	t.Helper()
	for _, s := range []task.Status{task.StatusPending, task.StatusRunning} {
		if err := database.UpdateTaskStatus(tk.ID, s); err != nil {
			t.Fatalf("parent -> %s: %v", s, err)
		}
	}
	return reloadTask(t, database, tk)
}

func reloadTask(t *testing.T, database *db.DB, tk *task.Task) *task.Task {
	t.Helper()
	fresh, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	fresh.Worktree = false
	fresh.WorktreePath = tk.WorktreePath
	return fresh
}

func childrenBySlug(t *testing.T, database *db.DB, parentID int64) map[string]*task.Task {
	t.Helper()
	list, err := database.GetFanoutChildren(parentID, "units")
	if err != nil {
		t.Fatalf("GetFanoutChildren: %v", err)
	}
	out := make(map[string]*task.Task, len(list))
	for _, c := range list {
		out[c.FanoutItem] = c
	}
	return out
}

// runToSuspend runs the parent until the fanout suspends on its children.
func runToSuspend(t *testing.T, engine *Engine, database *db.DB, tk *task.Task) map[string]*task.Task {
	t.Helper()
	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	got := reloadTask(t, database, tk)
	if got.Status != task.StatusAwaitingChildren {
		t.Fatalf("parent status = %q, want %q", got.Status, task.StatusAwaitingChildren)
	}
	return childrenBySlug(t, database, tk.ID)
}

func TestFanoutSpawnsSuspendsAndAdvances(t *testing.T) {
	engine, tk, runner, database, host := newFanoutTestEngine(t, fanoutWorkflow(""))
	publishOnRun(t, runner, database, tk, items("a", "b", "c"))

	children := runToSuspend(t, engine, database, tk)
	if len(children) != 3 {
		t.Fatalf("children = %d, want 3", len(children))
	}
	parent := reloadTask(t, database, tk)
	if parent.StepIndex != 1 {
		t.Errorf("step_index = %d, want 1 (still at the fanout)", parent.StepIndex)
	}
	if got := host.calls[1].Input; got != "build b (item 2 of plan: PLAN)" {
		t.Errorf("child input = %q, want item and step vars resolved", got)
	}
	if host.calls[0].Title != "a" || host.calls[0].Workflow != "unit" || host.calls[0].Merge != config.FanoutMergeBase {
		t.Errorf("spawn request = %+v", host.calls[0])
	}
	waits, err := database.GetTaskWaitsOn(tk.ID)
	if err != nil || len(waits) != 3 {
		t.Fatalf("waits-on = %v (err %v), want 3 edges", waits, err)
	}
	if len(runner.callsFor("after")) != 0 {
		t.Fatal("after ran before the children finished")
	}

	for slug, c := range children {
		finishChild(t, database, c.ID, task.StatusCompleted, strings.ToUpper(slug)+" done\nsecond line")
	}
	if err := engine.RunTask(context.Background(), resumeParent(t, database, tk), nil); err != nil {
		t.Fatalf("resumed RunTask: %v", err)
	}

	if n := len(runner.callsFor("units")); n != 0 {
		t.Errorf("runner called %d times for the fanout step, want 0", n)
	}
	if n := len(runner.callsFor("plan")); n != 1 {
		t.Errorf("plan ran %d times, want 1", n)
	}
	if len(host.calls) != 3 {
		t.Errorf("spawns = %d, want 3 (resume must reuse children)", len(host.calls))
	}
	rows, err := database.GetTaskStepRows(tk.ID)
	if err != nil {
		t.Fatalf("GetTaskStepRows: %v", err)
	}
	want := fmt.Sprintf("fanout units: 3/3 completed (require all)\n- a #%d completed — A done\n- b #%d completed — B done\n- c #%d completed — C done",
		children["a"].ID, children["b"].ID, children["c"].ID)
	if rows["units"].Status != "completed" || rows["units"].Context != want {
		t.Errorf("units row = %q %q, want completed with\n%q", rows["units"].Status, rows["units"].Context, want)
	}
	after := runner.callsFor("after")
	if len(after) != 1 || after[0].prompt != "results:\n"+want {
		t.Errorf("after prompt = %+v, want the aggregate verbatim", after)
	}
}

func TestFanoutNeedsWireBlockedBy(t *testing.T) {
	engine, tk, runner, database, _ := newFanoutTestEngine(t, fanoutWorkflow(""))
	list := items("b", "a")
	list[0].Needs = []string{"a"}
	publishOnRun(t, runner, database, tk, list)

	children := runToSuspend(t, engine, database, tk)
	a, b := children["a"], children["b"]
	if a == nil || b == nil {
		t.Fatalf("children = %v", children)
	}
	if a.ID > b.ID {
		t.Errorf("a (#%d) spawned after its dependent b (#%d)", a.ID, b.ID)
	}
	if len(b.BlockedBy) != 1 || b.BlockedBy[0] != a.ID {
		t.Errorf("b blocked_by = %v, want [%d]", b.BlockedBy, a.ID)
	}
	if len(a.BlockedBy) != 0 {
		t.Errorf("a blocked_by = %v, want none", a.BlockedBy)
	}
}

// runFanoutToJoin publishes three items, finishes the children with the given
// statuses and resumes the parent.
func runFanoutToJoin(t *testing.T, require string, statuses map[string]task.Status) (*fakeAgentRunner, *db.DB, *task.Task, map[string]*task.Task, error) {
	t.Helper()
	engine, tk, runner, database, _ := newFanoutTestEngine(t, fanoutWorkflow(require))
	publishOnRun(t, runner, database, tk, items("a", "b", "c"))
	children := runToSuspend(t, engine, database, tk)
	for slug, st := range statuses {
		finishChild(t, database, children[slug].ID, st, slug+" output")
	}
	err := engine.RunTask(context.Background(), resumeParent(t, database, tk), nil)
	return runner, database, tk, children, err
}

func TestFanoutRequireAll(t *testing.T) {
	runner, database, tk, children, err := runFanoutToJoin(t, "all", map[string]task.Status{
		"a": task.StatusCompleted, "b": task.StatusFailed, "c": task.StatusCompleted,
	})
	if err == nil {
		t.Fatal("expected require all to fail with a failed child")
	}
	wantMsg := fmt.Sprintf(`fanout "units": 2/3 children completed (require all); failed: b #%d`, children["b"].ID)
	if err.Error() != wantMsg {
		t.Errorf("error = %q, want %q", err, wantMsg)
	}
	if got := reloadTask(t, database, tk).ErrorMessage; got != wantMsg {
		t.Errorf("error_message = %q, want %q", got, wantMsg)
	}
	if len(runner.callsFor("after")) != 0 {
		t.Error("after ran despite the failed join")
	}
}

func TestFanoutRequireAny(t *testing.T) {
	runner, _, _, children, err := runFanoutToJoin(t, "any", map[string]task.Status{
		"a": task.StatusFailed, "b": task.StatusCompleted, "c": task.StatusFailed,
	})
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	after := runner.callsFor("after")
	if len(after) != 1 {
		t.Fatalf("after calls = %d, want 1", len(after))
	}
	wantLine := fmt.Sprintf(`- a #%d failed — (failed: step "build" exited with code 1)`, children["a"].ID)
	if !strings.Contains(after[0].prompt, "1/3 completed (require any)") || !strings.Contains(after[0].prompt, wantLine) {
		t.Errorf("aggregate =\n%s\nwant the any header and %q", after[0].prompt, wantLine)
	}
}

func TestFanoutRequireN(t *testing.T) {
	t.Run("met", func(t *testing.T) {
		runner, _, _, _, err := runFanoutToJoin(t, "2", map[string]task.Status{
			"a": task.StatusCompleted, "b": task.StatusFailed, "c": task.StatusCompleted,
		})
		if err != nil {
			t.Fatalf("RunTask: %v", err)
		}
		if len(runner.callsFor("after")) != 1 {
			t.Error("after did not run")
		}
	})
	t.Run("not met", func(t *testing.T) {
		_, _, _, _, err := runFanoutToJoin(t, "2", map[string]task.Status{
			"a": task.StatusCompleted, "b": task.StatusFailed, "c": task.StatusFailed,
		})
		if err == nil || !strings.Contains(err.Error(), "1/3 children completed (require 2)") {
			t.Errorf("error = %v, want the require 2 join failure", err)
		}
	})
	t.Run("exceeds item count", func(t *testing.T) {
		engine, tk, runner, database, host := newFanoutTestEngine(t, fanoutWorkflow("5"))
		publishOnRun(t, runner, database, tk, items("a", "b", "c"))
		err := engine.RunTask(context.Background(), tk, nil)
		if err == nil || !strings.Contains(err.Error(), "require 5 exceeds the 3 published item(s)") {
			t.Errorf("error = %v, want the require-exceeds-items failure", err)
		}
		if len(host.calls) != 0 {
			t.Errorf("spawned %d children, want 0", len(host.calls))
		}
	})
}

func TestFanoutMissingPublishFailsTask(t *testing.T) {
	engine, tk, _, database, host := newFanoutTestEngine(t, fanoutWorkflow(""))
	err := engine.RunTask(context.Background(), tk, nil)
	want := `step "plan" completed without publishing items for fanout "units"`
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	if got := reloadTask(t, database, tk).ErrorMessage; got != want {
		t.Errorf("error_message = %q, want %q", got, want)
	}
	if len(host.calls) != 0 {
		t.Errorf("spawned %d children, want 0", len(host.calls))
	}
}

func TestFanoutRepublishOnRerunOfFromStep(t *testing.T) {
	engine, tk, runner, database, host := newFanoutTestEngine(t, fanoutWorkflow(""))
	publishOnRun(t, runner, database, tk, items("a", "b"))
	first := runToSuspend(t, engine, database, tk)
	for _, c := range first {
		finishChild(t, database, c.ID, task.StatusCompleted, c.FanoutItem+" v1")
	}

	// Re-run from plan, which now publishes a different list.
	publishOnRun(t, runner, database, tk, items("a", "c"))
	tk = resumeParent(t, database, tk)
	tk.StepIndex = 0
	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	second := childrenBySlug(t, database, tk.ID)
	if len(host.calls) != 3 || second["c"] == nil {
		t.Fatalf("spawns = %d, children = %v; want only c spawned on the re-run", len(host.calls), second)
	}
	if second["a"].ID != first["a"].ID {
		t.Errorf("a was respawned (#%d -> #%d)", first["a"].ID, second["a"].ID)
	}

	finishChild(t, database, second["c"].ID, task.StatusCompleted, "c v1")
	if err := engine.RunTask(context.Background(), resumeParent(t, database, tk), nil); err != nil {
		t.Fatalf("resumed RunTask: %v", err)
	}
	ctx, err := database.GetTaskStepContext(tk.ID, "units")
	if err != nil {
		t.Fatalf("GetTaskStepContext: %v", err)
	}
	if !strings.HasPrefix(ctx, "fanout units: 2/2 completed") || strings.Contains(ctx, "- b ") {
		t.Errorf("aggregate =\n%s\nwant only the republished items a and c", ctx)
	}
}

func TestFanoutSpawnIdempotentAfterRestart(t *testing.T) {
	engine, tk, runner, database, host := newFanoutTestEngine(t, fanoutWorkflow(""))
	host.failAfter = 1
	publishOnRun(t, runner, database, tk, items("a", "b", "c"))
	if err := engine.RunTask(context.Background(), tk, nil); err == nil || !strings.Contains(err.Error(), "host unavailable") {
		t.Fatalf("error = %v, want the spawn failure", err)
	}
	if n := len(childrenBySlug(t, database, tk.ID)); n != 1 {
		t.Fatalf("children after the failed spawn = %d, want 1", n)
	}

	// A fresh engine on the same DB resumes at the fanout step.
	cfg := &config.Config{
		OnComplete: "none",
		Workflows:  []config.WorkflowConfig{fanoutWorkflow("")},
		Agents:     map[string]config.AgentConfig{"claude": {Command: "true"}},
	}
	fresh := NewEngine(cfg, database, nil, engine.repoRoot)
	fresh.runner = runner
	fresh.SetFanoutHost(&fakeFanoutHost{database: database})
	if err := fresh.RunTask(context.Background(), reloadTask(t, database, tk), nil); err != nil {
		t.Fatalf("RunTask after restart: %v", err)
	}
	if n := len(childrenBySlug(t, database, tk.ID)); n != 3 {
		t.Errorf("children = %d, want exactly 3", n)
	}
}

func TestFanoutDepthCapEnforcedAtSpawn(t *testing.T) {
	engine, tk, _, database, host := newFanoutTestEngine(t, fanoutWorkflow(""))
	// Build a chain so the running task sits MaxFanoutDepth hops below a root.
	parentID := tk.ID
	var leaf *task.Task
	for i := 0; i < config.MaxFanoutDepth; i++ {
		c, err := database.CreateFanoutChildTask(tk.ProjectID, "nested", "x", "nested", "default", "", "", "", "",
			task.StatusRunning, task.PriorityMedium, false, nil, nil, task.FanoutOrigin{ParentID: parentID, Step: "units", Item: "n"})
		if err != nil {
			t.Fatalf("CreateFanoutChildTask: %v", err)
		}
		parentID, leaf = c.ID, c
	}
	if err := database.PublishItems(leaf.ID, "plan", items("a")); err != nil {
		t.Fatalf("PublishItems: %v", err)
	}
	leaf.WorktreePath = tk.WorktreePath
	leaf.StepIndex = 1

	err := engine.RunTask(context.Background(), leaf, nil)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("capped at depth %d", config.MaxFanoutDepth)) {
		t.Fatalf("error = %v, want the depth cap", err)
	}
	if len(host.calls) != 0 {
		t.Errorf("spawned %d children, want 0", len(host.calls))
	}
}

func TestFormatFanoutAggregate(t *testing.T) {
	list := []task.PublishedItem{{Slug: "api"}, {Slug: "ui"}, {Slug: "docs"}, {Slug: "long"}}
	children := map[string]*task.Task{
		"api":  {ID: 12, Status: task.StatusCompleted},
		"ui":   {ID: 13, Status: task.StatusCompleted},
		"docs": {ID: 14, Status: task.StatusFailed, ErrorMessage: "step \"write\" exited with code 1\ntail"},
		"long": {ID: 15, Status: task.StatusCompleted},
	}
	contexts := map[int64]string{
		12: "\n\n  Added   the endpoint\tand tests  \nmore",
		13: "  \n",
		15: strings.Repeat("x", 130),
	}
	got := FormatFanoutAggregate("units", "all", list, children, contexts)
	want := "fanout units: 3/4 completed (require all)\n" +
		"- api #12 completed — Added the endpoint and tests\n" +
		"- ui #13 completed — (no context)\n" +
		"- docs #14 failed — (failed: step \"write\" exited with code 1)\n" +
		"- long #15 completed — " + strings.Repeat("x", 99) + "…"
	if got != want {
		t.Errorf("aggregate =\n%s\nwant\n%s", got, want)
	}

	big := make([]task.PublishedItem, 200)
	bigChildren := make(map[string]*task.Task, 200)
	bigContexts := make(map[int64]string, 200)
	for i := range big {
		slug := fmt.Sprintf("%03d-%s", i, strings.Repeat("s", 60))
		big[i] = task.PublishedItem{Slug: slug}
		bigChildren[slug] = &task.Task{ID: int64(100000 + i), Status: task.StatusCompleted}
		bigContexts[int64(100000+i)] = strings.Repeat("word ", 100)
	}
	lines := strings.Split(FormatFanoutAggregate("units", "all", big, bigChildren, bigContexts), "\n")
	if len(lines) != 201 {
		t.Fatalf("lines = %d, want 201", len(lines))
	}
	for i, l := range lines {
		if n := utf8.RuneCountInString(l); n > 200 {
			t.Errorf("line %d is %d runes, want <= 200", i, n)
		}
	}
}
