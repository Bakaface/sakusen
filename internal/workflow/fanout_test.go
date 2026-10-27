package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
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
	retried   []int64
}

func (h *fakeFanoutHost) RetryChild(id int64) error {
	h.retried = append(h.retried, id)
	return h.database.ResetTaskForRetry(id)
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

func TestFanoutNeedsOrdering(t *testing.T) {
	wf := fanoutWorkflow("")
	wf.Steps[1].Fanout.Retries = 1
	engine, tk, runner, database, host := newFanoutTestEngine(t, wf)
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

	claimable := func(id int64) bool {
		t.Helper()
		list, err := database.GetClaimableTasks(time.Now())
		if err != nil {
			t.Fatalf("GetClaimableTasks: %v", err)
		}
		for _, c := range list {
			if c.ID == id {
				return true
			}
		}
		return false
	}
	for _, id := range []int64{a.ID, b.ID} {
		if err := database.UpdateTaskStatus(id, task.StatusPending); err != nil {
			t.Fatalf("child #%d -> pending: %v", id, err)
		}
	}
	if !claimable(a.ID) || claimable(b.ID) {
		t.Errorf("claimable a=%v b=%v while a is pending, want a only", claimable(a.ID), claimable(b.ID))
	}
	if err := database.UpdateTaskStatus(a.ID, task.StatusRunning); err != nil {
		t.Fatalf("a -> running: %v", err)
	}
	if claimable(b.ID) {
		t.Error("b claimable while a is running")
	}

	// a fails and cascades to b; on resume both go back, and only a pays an attempt.
	if err := database.UpdateTaskStatusError(a.ID, task.StatusFailed, "boom"); err != nil {
		t.Fatalf("a -> failed: %v", err)
	}
	if _, err := database.FailDependentTasks(a.ID); err != nil {
		t.Fatalf("FailDependentTasks: %v", err)
	}
	if got := childrenBySlug(t, database, tk.ID)["b"]; got.Status != task.StatusFailed || !strings.HasPrefix(got.ErrorMessage, "blocker task #") {
		t.Fatalf("b = %s %q, want cascade-failed", got.Status, got.ErrorMessage)
	}
	if err := engine.RunTask(context.Background(), resumeParent(t, database, tk), nil); err != nil {
		t.Fatalf("resumed RunTask: %v", err)
	}
	after := childrenBySlug(t, database, tk.ID)
	if after["a"].Status != task.StatusPending || after["b"].Status != task.StatusPending {
		t.Errorf("a=%s b=%s, want both pending", after["a"].Status, after["b"].Status)
	}
	if after["a"].FanoutAttempts != 1 || after["b"].FanoutAttempts != 0 {
		t.Errorf("attempts a=%d b=%d, want 1 and 0", after["a"].FanoutAttempts, after["b"].FanoutAttempts)
	}
	if len(host.retried) != 2 || host.retried[0] != a.ID {
		t.Errorf("retried = %v, want a then b", host.retried)
	}
	if got := reloadTask(t, database, tk).Status; got != task.StatusAwaitingChildren {
		t.Errorf("parent status = %q, want %q", got, task.StatusAwaitingChildren)
	}

	// Once a completes, b becomes claimable.
	finishChild(t, database, a.ID, task.StatusCompleted, "a done")
	if !claimable(b.ID) {
		t.Error("b not claimable after a completed")
	}
}

func TestFanoutRetriesFailedChild(t *testing.T) {
	t.Run("retries once", func(t *testing.T) {
		wf := fanoutWorkflow("all")
		wf.Steps[1].Fanout.Retries = 1
		engine, tk, runner, database, host := newFanoutTestEngine(t, wf)
		publishOnRun(t, runner, database, tk, items("a", "b"))
		children := runToSuspend(t, engine, database, tk)
		finishChild(t, database, children["a"].ID, task.StatusCompleted, "a done")
		finishChild(t, database, children["b"].ID, task.StatusFailed, "")

		if err := engine.RunTask(context.Background(), resumeParent(t, database, tk), nil); err != nil {
			t.Fatalf("resumed RunTask: %v", err)
		}
		b := childrenBySlug(t, database, tk.ID)["b"]
		if b.Status != task.StatusPending || b.FanoutAttempts != 1 {
			t.Errorf("b = %s attempts %d, want pending with 1 attempt", b.Status, b.FanoutAttempts)
		}
		parent := reloadTask(t, database, tk)
		if parent.Status != task.StatusAwaitingChildren || parent.StepIndex != 1 {
			t.Errorf("parent = %s at step %d, want %s at 1", parent.Status, parent.StepIndex, task.StatusAwaitingChildren)
		}
		if len(runner.callsFor("after")) != 0 {
			t.Error("after ran while b was being retried")
		}

		finishChild(t, database, b.ID, task.StatusFailed, "")
		err := engine.RunTask(context.Background(), resumeParent(t, database, tk), nil)
		wantFailed := fmt.Sprintf("b #%d", b.ID)
		if err == nil || !strings.Contains(err.Error(), "failed: "+wantFailed) {
			t.Fatalf("error = %v, want the require all failure naming %s", err, wantFailed)
		}
		if got := reloadTask(t, database, tk).ErrorMessage; !strings.Contains(got, wantFailed) {
			t.Errorf("error_message = %q, want it to name %s", got, wantFailed)
		}
		if len(host.retried) != 1 {
			t.Errorf("retried = %v, want exactly one retry", host.retried)
		}
	})
	t.Run("no retries", func(t *testing.T) {
		engine, tk, runner, database, host := newFanoutTestEngine(t, fanoutWorkflow("all"))
		publishOnRun(t, runner, database, tk, items("a", "b"))
		children := runToSuspend(t, engine, database, tk)
		finishChild(t, database, children["a"].ID, task.StatusCompleted, "a done")
		finishChild(t, database, children["b"].ID, task.StatusFailed, "")

		err := engine.RunTask(context.Background(), resumeParent(t, database, tk), nil)
		if err == nil || !strings.Contains(err.Error(), "1/2 children completed (require all)") {
			t.Fatalf("error = %v, want the require all failure", err)
		}
		if len(host.retried) != 0 {
			t.Errorf("retried = %v, want none", host.retried)
		}
	})
}

// failFanoutJoin runs a retries:1 fanout over a, b, c until the parent fails
// the join: a and c complete, b fails twice. It returns the children.
func failFanoutJoin(t *testing.T, engine *Engine, runner *fakeAgentRunner, database *db.DB, tk *task.Task) map[string]*task.Task {
	t.Helper()
	publishOnRun(t, runner, database, tk, items("a", "b", "c"))
	children := runToSuspend(t, engine, database, tk)
	finishChild(t, database, children["a"].ID, task.StatusCompleted, "a done")
	finishChild(t, database, children["b"].ID, task.StatusFailed, "")
	finishChild(t, database, children["c"].ID, task.StatusCompleted, "c done")
	if err := engine.RunTask(context.Background(), resumeParent(t, database, tk), nil); err != nil {
		t.Fatalf("resumed RunTask: %v", err)
	}
	finishChild(t, database, children["b"].ID, task.StatusFailed, "")
	if err := engine.RunTask(context.Background(), resumeParent(t, database, tk), nil); err == nil {
		t.Fatal("expected the join to fail")
	}
	// The daemon marks a task failed when RunTask errors.
	if err := database.UpdateTaskStatus(tk.ID, task.StatusFailed); err != nil {
		t.Fatalf("parent -> failed: %v", err)
	}
	return children
}

func TestFanoutSpawnIdempotentAfterParentRetry(t *testing.T) {
	retryWorkflow := func() config.WorkflowConfig {
		wf := fanoutWorkflow("all")
		wf.Steps[1].Fanout.Retries = 1
		return wf
	}
	// runRetry retries the parent with reset and runs it to suspension.
	runRetry := func(t *testing.T, engine *Engine, database *db.DB, tk *task.Task, reset func() error) map[string]*task.Task {
		t.Helper()
		if err := reset(); err != nil {
			t.Fatalf("reset: %v", err)
		}
		if err := database.UpdateTaskStatus(tk.ID, task.StatusRunning); err != nil {
			t.Fatalf("parent -> running: %v", err)
		}
		return runToSuspend(t, engine, database, reloadTask(t, database, tk))
	}
	// checkReuse asserts a was reused untouched and b was retried with its
	// attempts zeroed.
	checkReuse := func(t *testing.T, host *fakeFanoutHost, before, after map[string]*task.Task) {
		t.Helper()
		a := after["a"]
		if a.ID != before["a"].ID || a.Status != task.StatusCompleted {
			t.Errorf("a = #%d %s, want #%d completed", a.ID, a.Status, before["a"].ID)
		}
		b := after["b"]
		if b.ID != before["b"].ID || b.Status != task.StatusPending || b.FanoutAttempts != 0 {
			t.Errorf("b = #%d %s attempts %d, want #%d pending with 0 attempts", b.ID, b.Status, b.FanoutAttempts, before["b"].ID)
		}
		for _, id := range host.retried {
			if id == a.ID {
				t.Errorf("completed child a #%d was retried", a.ID)
			}
		}
		if host.retried[len(host.retried)-1] != b.ID {
			t.Errorf("retried = %v, want b #%d last", host.retried, b.ID)
		}
		if len(after) > 3 {
			t.Errorf("children = %d, want <= 3", len(after))
		}
	}
	waitsOn := func(t *testing.T, database *db.DB, parentID, childID int64) bool {
		t.Helper()
		waits, err := database.GetTaskWaitsOn(parentID)
		if err != nil {
			t.Fatalf("GetTaskWaitsOn: %v", err)
		}
		for _, w := range waits {
			if w == childID {
				return true
			}
		}
		return false
	}
	retryAtUnits := func(database *db.DB, tk *task.Task) func() error {
		return func() error { return database.ResetTaskForRetryAtStep(tk.ID, 1, []string{"units", "after"}) }
	}

	t.Run("missing child is spawned", func(t *testing.T) {
		engine, tk, runner, database, host := newFanoutTestEngine(t, retryWorkflow())
		before := failFanoutJoin(t, engine, runner, database, tk)
		if err := database.DeleteTask(before["c"].ID); err != nil {
			t.Fatalf("DeleteTask: %v", err)
		}
		after := runRetry(t, engine, database, tk, retryAtUnits(database, tk))
		checkReuse(t, host, before, after)
		c := after["c"]
		if c == nil || c.ID == before["c"].ID {
			t.Fatalf("c = %v, want a newly spawned child", c)
		}
		if !waitsOn(t, database, tk.ID, c.ID) || !waitsOn(t, database, tk.ID, after["b"].ID) {
			t.Error("parent does not wait on the new c and the retried b")
		}
	})
	t.Run("non-terminal child is waited on", func(t *testing.T) {
		engine, tk, runner, database, host := newFanoutTestEngine(t, retryWorkflow())
		before := failFanoutJoin(t, engine, runner, database, tk)
		// c was re-queued by hand while the parent sat failed.
		if err := database.ResetTaskForRetry(before["c"].ID); err != nil {
			t.Fatalf("ResetTaskForRetry: %v", err)
		}
		spawns := len(host.calls)
		after := runRetry(t, engine, database, tk, retryAtUnits(database, tk))
		checkReuse(t, host, before, after)
		if after["c"].ID != before["c"].ID {
			t.Errorf("c was respawned (#%d -> #%d)", before["c"].ID, after["c"].ID)
		}
		if len(host.calls) != spawns {
			t.Errorf("spawns = %d, want %d (no new children)", len(host.calls), spawns)
		}
		if !waitsOn(t, database, tk.ID, after["c"].ID) {
			t.Error("parent does not wait on the pending c")
		}
	})
	t.Run("full retry republishes", func(t *testing.T) {
		engine, tk, runner, database, host := newFanoutTestEngine(t, retryWorkflow())
		before := failFanoutJoin(t, engine, runner, database, tk)
		publishOnRun(t, runner, database, tk, items("a", "b", "c"))
		spawns := len(host.calls)
		after := runRetry(t, engine, database, tk, func() error { return database.ResetTaskForRetry(tk.ID) })
		if n := len(runner.callsFor("plan")); n != 2 {
			t.Errorf("plan ran %d times, want 2", n)
		}
		checkReuse(t, host, before, after)
		if len(host.calls) != spawns {
			t.Errorf("spawns = %d, want %d (no new children)", len(host.calls), spawns)
		}
	})
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
