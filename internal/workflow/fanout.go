package workflow

import (
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/task"
)

// fanoutSummaryRunes caps each item's summary in the fanout aggregate, and
// fanoutLineRunes caps each whole item line, cutting the summary further when
// a long slug, ID or status leaves less room.
const (
	fanoutSummaryRunes = 120
	fanoutLineRunes    = 200
)

// FanoutSpawnRequest is one child task a fanout step asks its host to create.
type FanoutSpawnRequest struct {
	Workflow  string
	Input     string
	Title     string
	Item      string // published item slug, recorded as the child's fanout origin
	Step      string // the fanout step's name
	Merge     string // config.FanoutMergeParent or config.FanoutMergeBase
	BlockedBy []int64
	Priority  task.Priority
	TrackID   *int64
}

// FanoutHost creates fanout child tasks. The daemon implements it so children
// go through the same creation path as any other task.
type FanoutHost interface {
	SpawnChild(parent *task.Task, req FanoutSpawnRequest) (*task.Task, error)
	// RetryChild resets a terminal child to pending through the full-retry path.
	RetryChild(id int64) error
}

// SetFanoutHost registers the host fanout steps spawn their children through.
// Without one, a fanout step fails.
func (e *Engine) SetFanoutHost(h FanoutHost) {
	e.fanoutHost = h
}

// cascadeFailurePrefix starts the error_message of a task failed because a
// blocker failed (db.FailDependentTasks); such a task never ran.
const cascadeFailurePrefix = "blocker task #"

// runFanoutStep spawns one child task per item published by the step's
// `from:` step, then joins on them. While any child is still running it
// records wait-on edges and returns a suspend result; once every child is
// terminal it returns the aggregate as the step's result text, or fails the
// task when fewer children completed than `require` demands. Re-entry is
// idempotent: children already spawned for an item are reused.
//
// Failed children are retried before judging: on a fresh entry (parent
// retry) every failed child is reset with its attempt counter zeroed; on a
// resume a child's own failure is retried while its attempts are below
// `retries`. Either way, children cascade-failed by a retried blocker are
// reset alongside it.
func (e *Engine) runFanoutStep(t *task.Task, step config.StepConfig, tmplCtx *TemplateContext, fresh bool) (stepResult, error) {
	f := step.Fanout
	fail := func(format string, args ...any) (stepResult, error) {
		err := fmt.Errorf("fanout %q: "+format, append([]any{step.Name}, args...)...)
		e.recordExitCode(t.ID, 1, err.Error())
		return stepResult{}, err
	}

	if e.fanoutHost == nil {
		return fail("no fanout host is configured")
	}

	depth, err := e.database.GetFanoutDepth(t.ID)
	if err != nil {
		return fail("failed to read fanout depth: %v", err)
	}
	if depth >= config.MaxFanoutDepth {
		return fail("task is at fanout depth %d; nested fanouts are capped at depth %d (MaxFanoutDepth)", depth, config.MaxFanoutDepth)
	}

	if f.IsNodes() {
		return e.runFanoutNodes(t, step, tmplCtx, fresh, fail)
	}

	items, ok, err := e.database.GetPublishedItems(t.ID, f.From)
	if err != nil {
		return fail("failed to read items published by step %q: %v", f.From, err)
	}
	if !ok || len(items) == 0 {
		if f.AllowEmpty {
			e.appendTaskLog(t.ID, "=== fanout %s: step %s published no items (allow_empty), nothing to spawn ===", step.Name, f.From)
			return stepResult{step: step, resultText: FormatEmptyFanoutAggregate(step.Name)}, nil
		}
		return fail("step %q published no items", f.From)
	}

	policy := f.EffectiveRequire()
	required := f.RequiredCount(len(items))
	if required > len(items) {
		return fail("require %s exceeds the %d published item(s)", policy, len(items))
	}

	children, err := e.fanoutChildrenBySlug(t.ID, step.Name)
	if err != nil {
		return fail("failed to list children: %v", err)
	}

	// Topological order guarantees every item's needs already have a child,
	// so its blocked_by edges can be wired at creation.
	for _, i := range task.TopoOrder(items) {
		it := items[i]
		if children[it.Slug] != nil {
			continue
		}
		itemCtx := *tmplCtx
		itemCtx.Item = ItemVars{Input: it.Input, Slug: it.Slug, Title: it.Title, Index: i + 1}
		input, err := ResolveTemplate(f.EffectiveInput(), &itemCtx)
		if err != nil {
			return fail("input for item %q: %v", it.Slug, err)
		}
		title := it.Title
		if title == "" {
			title = it.Slug
		}
		var blockedBy []int64
		for _, n := range it.Needs {
			if c := children[n]; c != nil {
				blockedBy = append(blockedBy, c.ID)
			}
		}
		child, spawnErr := e.fanoutHost.SpawnChild(t, FanoutSpawnRequest{
			Workflow:  f.Workflow,
			Input:     input,
			Title:     title,
			Item:      it.Slug,
			Step:      step.Name,
			Merge:     f.EffectiveMerge(),
			BlockedBy: blockedBy,
			Priority:  t.Priority,
			TrackID:   t.TrackID,
		})
		if spawnErr != nil {
			// The unique (parent, step, item) index rejects a second child, so
			// a failed spawn may mean one already exists: reuse it.
			existing, qErr := e.fanoutChildrenBySlug(t.ID, step.Name)
			if qErr != nil || existing[it.Slug] == nil {
				return fail("failed to spawn child for item %q: %v", it.Slug, spawnErr)
			}
			child = existing[it.Slug]
		}
		children[it.Slug] = child
	}

	retried, err := e.retryFanoutChildren(f, items, children, fresh)
	if err != nil {
		return fail("%v", err)
	}
	if retried > 0 {
		if children, err = e.fanoutChildrenBySlug(t.ID, step.Name); err != nil {
			return fail("failed to list children: %v", err)
		}
	}

	var pending []int64
	for _, it := range items {
		if c := children[it.Slug]; !c.Status.IsTerminal() {
			pending = append(pending, c.ID)
		}
	}
	if len(pending) > 0 {
		for _, id := range pending {
			if err := e.database.AddTaskWaitsOn(t.ID, id); err != nil {
				return fail("failed to record wait on child #%d: %v", id, err)
			}
		}
		if retried > 0 {
			e.appendTaskLog(t.ID, "=== fanout %s: retrying %d child task(s) ===", step.Name, retried)
		}
		e.appendTaskLog(t.ID, "=== fanout %s: waiting on %d of %d child task(s) ===", step.Name, len(pending), len(items))
		log.Printf("Task #%d suspended at fanout %q: waiting on %d child task(s)", t.ID, step.Name, len(pending))
		return stepResult{step: step, suspend: true}, nil
	}

	// Contexts come from each child's task_context: its final summarized
	// output, which every finished workflow writes regardless of shape.
	contexts := make(map[int64]string, len(children))
	completed := 0
	var failed []string
	for _, it := range items {
		c := children[it.Slug]
		contexts[c.ID] = c.Context
		if c.Status == task.StatusCompleted {
			completed++
		} else {
			failed = append(failed, fmt.Sprintf("%s #%d", it.Slug, c.ID))
		}
	}
	aggregate := FormatFanoutAggregate(step.Name, policy, items, children, contexts, nil)
	e.appendTaskLog(t.ID, "=== fanout %s: %d/%d children completed (require %s) ===", step.Name, completed, len(items), policy)
	if completed < required {
		return fail("%d/%d children completed (require %s); failed: %s", completed, len(items), policy, strings.Join(failed, ", "))
	}
	return stepResult{step: step, resultText: aggregate}, nil
}

// FormatEmptyFanoutAggregate is the aggregate step context of an allow_empty
// fanout whose source published nothing.
func FormatEmptyFanoutAggregate(step string) string {
	return fmt.Sprintf("fanout %s: 0 items published (allow_empty)", step)
}

// fanoutAllowsEmpty reports whether the named fanout step sets allow_empty.
func fanoutAllowsEmpty(wf *config.WorkflowConfig, name string) bool {
	for i := range wf.Steps {
		if wf.Steps[i].IsFanout() && wf.Steps[i].Name == name {
			return wf.Steps[i].Fanout.AllowEmpty
		}
	}
	return false
}

// runFanoutNodes runs a `nodes:` fanout: each node's child is spawned only
// once every node it needs has completed, so its input can render those
// nodes' contexts via {{steps.<node>.context}}. Children carry no blocked_by
// edges; the engine itself holds a node back. A node whose need failed for
// good is skipped and counts as failed at the join.
//
// Every pass rebuilds one parent task_steps row per node from its child, so
// a completed node's context is addressable by later steps and a retried
// node's stale context is dropped.
func (e *Engine) runFanoutNodes(t *task.Task, step config.StepConfig, tmplCtx *TemplateContext, fresh bool, fail func(string, ...any) (stepResult, error)) (stepResult, error) {
	f := step.Fanout
	nodes := f.Nodes
	items := make([]task.PublishedItem, len(nodes))
	for i, n := range nodes {
		items[i] = task.PublishedItem{Slug: n.Name, Title: n.Name, Needs: n.Needs}
	}
	policy := f.EffectiveRequire()
	required := f.RequiredCount(len(nodes))
	if required > len(nodes) {
		return fail("require %s exceeds the %d declared node(s)", policy, len(nodes))
	}
	order := f.NodeTopoOrder()

	children, err := e.fanoutChildrenBySlug(t.ID, step.Name)
	if err != nil {
		return fail("failed to list children: %v", err)
	}

	retried, err := e.retryFanoutNodes(f, order, children, fresh)
	if err != nil {
		return fail("%v", err)
	}
	if retried > 0 {
		if children, err = e.fanoutChildrenBySlug(t.ID, step.Name); err != nil {
			return fail("failed to list children: %v", err)
		}
	}

	nodeCtx := make(map[string]string, len(nodes))
	for _, n := range nodes {
		c := children[n.Name]
		if c != nil && c.Status == task.StatusCompleted {
			if err := e.database.CreateTaskStep(t.ID, n.Name); err != nil {
				return fail("failed to record node %q: %v", n.Name, err)
			}
			if err := e.database.CompleteTaskStep(t.ID, n.Name, &c.Context, 0); err != nil {
				return fail("failed to record node %q: %v", n.Name, err)
			}
			nodeCtx[n.Name] = c.Context
			continue
		}
		if err := e.database.DeleteTaskStepsFrom(t.ID, []string{n.Name}); err != nil {
			return fail("failed to clear node %q: %v", n.Name, err)
		}
	}

	// Retries ran above, so a terminal non-completed child is failed for good.
	failedFor := func(c *task.Task) bool {
		return c != nil && c.Status.IsTerminal() && c.Status != task.StatusCompleted
	}
	skipped := make(map[string]string)
	var ready []int
	waiting := 0
	for _, i := range order {
		n := nodes[i]
		if children[n.Name] != nil {
			continue
		}
		allDone := true
		for _, need := range n.Needs {
			if _, isSkipped := skipped[need]; isSkipped || failedFor(children[need]) {
				skipped[n.Name] = need
				break
			}
			if c := children[need]; c == nil || c.Status != task.StatusCompleted {
				allDone = false
			}
		}
		switch {
		case skipped[n.Name] != "":
		case allDone:
			ready = append(ready, i)
		default:
			waiting++
		}
	}

	for _, i := range ready {
		n := nodes[i]
		nodeTmpl := *tmplCtx
		nodeTmpl.Steps = make(map[string]string, len(tmplCtx.Steps)+len(nodeCtx))
		for k, v := range tmplCtx.Steps {
			nodeTmpl.Steps[k] = v
		}
		for k, v := range nodeCtx {
			nodeTmpl.Steps[k] = v
		}
		nodeTmpl.Item = ItemVars{Slug: n.Name, Title: n.Name, Index: i + 1}
		input, err := ResolveTemplate(n.Input, &nodeTmpl)
		if err != nil {
			return fail("input for node %q: %v", n.Name, err)
		}
		child, spawnErr := e.fanoutHost.SpawnChild(t, FanoutSpawnRequest{
			Workflow: f.EffectiveNodeWorkflow(i),
			Input:    input,
			Title:    n.Name,
			Item:     n.Name,
			Step:     step.Name,
			Merge:    f.EffectiveMerge(),
			Priority: t.Priority,
			TrackID:  t.TrackID,
		})
		if spawnErr != nil {
			// The unique (parent, step, item) index rejects a second child, so
			// a failed spawn may mean one already exists: reuse it.
			existing, qErr := e.fanoutChildrenBySlug(t.ID, step.Name)
			if qErr != nil || existing[n.Name] == nil {
				return fail("failed to spawn child for node %q: %v", n.Name, spawnErr)
			}
			child = existing[n.Name]
		}
		children[n.Name] = child
	}

	var pending []int64
	for _, n := range nodes {
		if c := children[n.Name]; c != nil && !c.Status.IsTerminal() {
			pending = append(pending, c.ID)
		}
	}
	if len(pending) > 0 || waiting > 0 {
		for _, id := range pending {
			if err := e.database.AddTaskWaitsOn(t.ID, id); err != nil {
				return fail("failed to record wait on child #%d: %v", id, err)
			}
		}
		if retried > 0 {
			e.appendTaskLog(t.ID, "=== fanout %s: retrying %d child task(s) ===", step.Name, retried)
		}
		e.appendTaskLog(t.ID, "=== fanout %s: waiting on %d child task(s), %d of %d node(s) not yet spawned ===", step.Name, len(pending), waiting, len(nodes))
		log.Printf("Task #%d suspended at fanout %q: waiting on %d child task(s)", t.ID, step.Name, len(pending))
		return stepResult{step: step, suspend: true}, nil
	}

	contexts := make(map[int64]string, len(children))
	completed := 0
	var failed []string
	for _, n := range nodes {
		c := children[n.Name]
		switch {
		case c == nil:
			failed = append(failed, n.Name+" (skipped)")
		case c.Status == task.StatusCompleted:
			contexts[c.ID] = c.Context
			completed++
		default:
			contexts[c.ID] = c.Context
			failed = append(failed, fmt.Sprintf("%s #%d", n.Name, c.ID))
		}
	}
	aggregate := FormatFanoutAggregate(step.Name, policy, items, children, contexts, skipped)
	e.appendTaskLog(t.ID, "=== fanout %s: %d/%d nodes completed (require %s) ===", step.Name, completed, len(nodes), policy)
	if completed < required {
		return fail("%d/%d nodes completed (require %s); failed: %s", completed, len(nodes), policy, strings.Join(failed, ", "))
	}
	return stepResult{step: step, resultText: aggregate}, nil
}

// retryFanoutNodes resets the failed children of nodes due a retry, in
// dependency order, and returns how many it reset. On a fresh entry every
// failed child is retried with its attempts zeroed; on a resume while its
// attempts are below the node's retries.
func (e *Engine) retryFanoutNodes(f *config.FanoutConfig, order []int, children map[string]*task.Task, fresh bool) (int, error) {
	n := 0
	for _, i := range order {
		c := children[f.Nodes[i].Name]
		if c == nil || c.Status != task.StatusFailed {
			continue
		}
		switch {
		case fresh:
			if err := e.database.ResetFanoutAttempts(c.ID); err != nil {
				return n, fmt.Errorf("failed to reset attempts of child #%d: %v", c.ID, err)
			}
		case c.FanoutAttempts < f.EffectiveNodeRetries(i):
			if _, err := e.database.IncrementFanoutAttempts(c.ID); err != nil {
				return n, fmt.Errorf("failed to count attempt of child #%d: %v", c.ID, err)
			}
		default:
			continue
		}
		if err := e.fanoutHost.RetryChild(c.ID); err != nil {
			return n, fmt.Errorf("failed to retry child #%d: %v", c.ID, err)
		}
		n++
	}
	return n, nil
}

// retryFanoutChildren resets the failed children of items that are due a
// retry and returns how many it reset. children must hold a child for every
// item.
func (e *Engine) retryFanoutChildren(f *config.FanoutConfig, items []task.PublishedItem, children map[string]*task.Task, fresh bool) (int, error) {
	retry := make(map[int64]bool)
	var cascaded []*task.Task
	for _, it := range items {
		c := children[it.Slug]
		if c.Status != task.StatusFailed {
			continue
		}
		switch {
		case fresh:
			if err := e.database.ResetFanoutAttempts(c.ID); err != nil {
				return 0, fmt.Errorf("failed to reset attempts of child #%d: %v", c.ID, err)
			}
			retry[c.ID] = true
		case strings.HasPrefix(c.ErrorMessage, cascadeFailurePrefix):
			cascaded = append(cascaded, c)
		case c.FanoutAttempts < f.Retries:
			if _, err := e.database.IncrementFanoutAttempts(c.ID); err != nil {
				return 0, fmt.Errorf("failed to count attempt of child #%d: %v", c.ID, err)
			}
			retry[c.ID] = true
		}
	}

	// A cascade-failed child never ran: it goes back with its blocker, at no
	// cost to its own attempts. Repeat until stable so chains are covered.
	for changed := true; changed; {
		changed = false
		for _, c := range cascaded {
			if retry[c.ID] {
				continue
			}
			for _, b := range c.BlockedBy {
				if retry[b] {
					retry[c.ID] = true
					changed = true
					break
				}
			}
		}
	}

	// Blockers first, so a dependent is never pending behind a failed blocker.
	n := 0
	for _, i := range task.TopoOrder(items) {
		c := children[items[i].Slug]
		if !retry[c.ID] {
			continue
		}
		if err := e.fanoutHost.RetryChild(c.ID); err != nil {
			return n, fmt.Errorf("failed to retry child #%d: %v", c.ID, err)
		}
		n++
	}
	return n, nil
}

// fanoutChildrenBySlug returns the children parentID's fanout step spawned,
// keyed by published item slug.
func (e *Engine) fanoutChildrenBySlug(parentID int64, step string) (map[string]*task.Task, error) {
	list, err := e.database.GetFanoutChildren(parentID, step)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*task.Task, len(list))
	for _, c := range list {
		out[c.FanoutItem] = c
	}
	return out, nil
}

// FormatFanoutAggregate renders a fanout step's aggregate step context — the
// value {{steps.<fanout>.context}} resolves to. One header line, then one line
// per item in published order:
//
//	fanout units: 2/3 completed (require all)
//	- api #12 completed — Added the endpoint and its tests
//	- ui #13 completed — (no context)
//	- docs #14 failed — (failed: step "write" exited with code 1)
//
// A summary is the first non-empty line of the child's context with
// whitespace collapsed, truncated to 120 runes and further if needed so no
// item line exceeds 200 runes; the aggregate stays one short line per item
// however large the fanout. Full outputs are reachable through
// {{tasks.<id>.context}}.
//
// skipped maps an item that was never spawned to the need that failed, which
// its line names instead of "(not spawned)".
func FormatFanoutAggregate(step, policy string, items []task.PublishedItem, children map[string]*task.Task, contexts map[int64]string, skipped map[string]string) string {
	completed := 0
	for _, it := range items {
		if c := children[it.Slug]; c != nil && c.Status == task.StatusCompleted {
			completed++
		}
	}
	lines := make([]string, 0, len(items)+1)
	lines = append(lines, fmt.Sprintf("fanout %s: %d/%d completed (require %s)", step, completed, len(items), policy))
	for _, it := range items {
		c := children[it.Slug]
		if c == nil {
			if need, ok := skipped[it.Slug]; ok {
				lines = append(lines, fmt.Sprintf("- %s (skipped: need %s failed)", it.Slug, need))
				continue
			}
			lines = append(lines, fmt.Sprintf("- %s (not spawned)", it.Slug))
			continue
		}
		prefix := fmt.Sprintf("- %s #%d %s — ", it.Slug, c.ID, c.Status)
		budget := fanoutLineRunes - utf8.RuneCountInString(prefix)
		var summary string
		switch {
		case c.Status != task.StatusCompleted:
			const wrap = "(failed: )"
			reason := summaryLine(c.ErrorMessage, min(fanoutSummaryRunes, budget-len(wrap)))
			if reason == "" {
				reason = string(c.Status)
			}
			summary = "(failed: " + reason + ")"
		default:
			summary = summaryLine(contexts[c.ID], min(fanoutSummaryRunes, budget))
			if summary == "" {
				summary = "(no context)"
			}
		}
		lines = append(lines, prefix+summary)
	}
	return strings.Join(lines, "\n")
}

// summaryLine returns the first non-empty line of s with whitespace collapsed,
// truncated to limit runes (ending in "…" when cut). A limit below 1 is
// treated as 1 so non-empty input never yields an empty summary.
func summaryLine(s string, limit int) string {
	limit = max(limit, 1)
	for _, line := range strings.Split(s, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > limit {
			line = string(r[:limit-1]) + "…"
		}
		return line
	}
	return ""
}
