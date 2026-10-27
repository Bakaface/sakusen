package workflow

import (
	"fmt"
	"log"
	"strings"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/task"
)

// fanoutSummaryRunes caps each item's summary in the fanout aggregate so a
// line stays near 200 runes even with a maximal (64-rune) slug.
const fanoutSummaryRunes = 120

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

// fanoutConsumer returns the name of the first fanout step in wf whose
// `from:` is step, or "" when none is.
func fanoutConsumer(wf *config.WorkflowConfig, step string) string {
	for i := range wf.Steps {
		if wf.Steps[i].IsFanout() && wf.Steps[i].Fanout.From == step {
			return wf.Steps[i].Name
		}
	}
	return ""
}

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

	items, ok, err := e.database.GetPublishedItems(t.ID, f.From)
	if err != nil {
		return fail("failed to read items published by step %q: %v", f.From, err)
	}
	if !ok || len(items) == 0 {
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
	aggregate := FormatFanoutAggregate(step.Name, policy, items, children, contexts)
	e.appendTaskLog(t.ID, "=== fanout %s: %d/%d children completed (require %s) ===", step.Name, completed, len(items), policy)
	if completed < required {
		return fail("%d/%d children completed (require %s); failed: %s", completed, len(items), policy, strings.Join(failed, ", "))
	}
	return stepResult{step: step, resultText: aggregate}, nil
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
// whitespace collapsed, truncated to 120 runes, so the aggregate stays one
// line per item however large the fanout. Full outputs are reachable through
// {{tasks.<id>.context}}.
func FormatFanoutAggregate(step, policy string, items []task.PublishedItem, children map[string]*task.Task, contexts map[int64]string) string {
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
			lines = append(lines, fmt.Sprintf("- %s (not spawned)", it.Slug))
			continue
		}
		var summary string
		switch {
		case c.Status != task.StatusCompleted:
			reason := summaryLine(c.ErrorMessage)
			if reason == "" {
				reason = string(c.Status)
			}
			summary = "(failed: " + reason + ")"
		default:
			summary = summaryLine(contexts[c.ID])
			if summary == "" {
				summary = "(no context)"
			}
		}
		lines = append(lines, fmt.Sprintf("- %s #%d %s — %s", it.Slug, c.ID, c.Status, summary))
	}
	return strings.Join(lines, "\n")
}

// summaryLine returns the first non-empty line of s with whitespace collapsed,
// truncated to fanoutSummaryRunes runes (ending in "…" when cut).
func summaryLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > fanoutSummaryRunes {
			line = string(r[:fanoutSummaryRunes-1]) + "…"
		}
		return line
	}
	return ""
}
