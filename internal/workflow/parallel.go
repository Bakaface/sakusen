package workflow

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/task"
)

// PARALLEL GROUPS (fan-out / fan-in)
//
// A `parallel:` step is a GROUP: one entry in wf.Steps, one cursor slot
// (t.StepIndex), and N BRANCHES that run concurrently as headless agents in
// the task's single worktree. The group itself never spawns an agent — it
// launches its branches, joins them, and publishes one AGGREGATE step context
// under its own name (see FormatParallelAggregate).
//
// Invariants this file owns:
//
//   - Every branch is an ordinary step from the DB's point of view: its own
//     task_steps row keyed by branch name, its own {{steps.<branch>.context}}.
//     The group's row is the only one the engine writes the aggregate to, and
//     it is ENGINE-OWNED: captureHeadlessStepContext short-circuits for groups
//     rather than applying the usual manual/last_message/summarize precedence.
//   - A COMPLETED branch row is never re-run. That single rule covers both
//     retry-from-step (the daemon deletes the group row plus non-completed
//     branch rows, so only the losers re-run) and loop-back (applyStepResult
//     deletes every branch row of a group inside the loop range, so the whole
//     group re-runs). Whoever wants a re-run deletes rows first.
//   - Branch agent output never enters the unified task log: each branch gets
//     its own branch-<name>.log with the identical region layout, so
//     stepAgentOutput works on it unchanged. task.log receives only the two
//     group marker lines.
//   - In a shared group, branches share one worktree and run at once, so they
//     are documented as read-only. Nothing enforces it.
//   - In an isolated group (isolation: worktree), each branch runs in its own
//     worktree on its own git branch, forked from the task worktree's HEAD and
//     committed on success. Both survive a retry: an existing branch worktree
//     is reused as-is, a missing one is re-added on the surviving branch.

// BranchLogPath returns the per-branch agent log for a parallel branch. It
// sits next to the unified task.log in the same per-task log dir and uses the
// same region layout runHeadlessAgent writes for ordinary steps.
func BranchLogPath(dataDir string, taskID int64, branchName string) string {
	return filepath.Join(ProjectLogsDir(dataDir, taskID), fmt.Sprintf("branch-%s.log", branchName))
}

// branchOutputFile returns the per-branch raw stdout+stderr capture path. It
// lives under the worktree's .sakusen/ directory (which `sakusen init`
// gitignores, like the prompt and result files) rather than at
// runner.OutputLogFileName, which every concurrent branch would truncate.
func branchOutputFile(worktreePath, branchName string) string {
	return filepath.Join(worktreePath, ".sakusen", fmt.Sprintf("output-%s.log", branchName))
}

// IsolatedBranchWorktreePath returns where an isolated branch's worktree lives:
// a sibling of the task worktree.
func IsolatedBranchWorktreePath(taskWorktree, name string) string {
	return taskWorktree + "-" + name
}

// IsolatedBranchGitBranch returns the git branch an isolated branch commits to.
func IsolatedBranchGitBranch(taskBranch, name string) string {
	return taskBranch + "-" + name
}

// IsolatedBranchNames returns the name of every branch of every isolated group
// in wf, in config order.
func IsolatedBranchNames(wf *config.WorkflowConfig) []string {
	if wf == nil {
		return nil
	}
	var names []string
	for _, step := range wf.Steps {
		if !step.Parallel.IsIsolated() {
			continue
		}
		for _, b := range step.Parallel.Branches {
			names = append(names, b.Name)
		}
	}
	return names
}

// isolatedBranchVars builds the {{branches.<name>.*}} map for t.
func isolatedBranchVars(wf *config.WorkflowConfig, t *task.Task) map[string]IsolatedBranchVars {
	names := IsolatedBranchNames(wf)
	if len(names) == 0 {
		return nil
	}
	vars := make(map[string]IsolatedBranchVars, len(names))
	for _, name := range names {
		vars[name] = IsolatedBranchVars{
			Branch:   IsolatedBranchGitBranch(t.Branch, name),
			Worktree: IsolatedBranchWorktreePath(t.WorktreePath, name),
		}
	}
	return vars
}

// branchOutcome is one branch's result after the join.
type branchOutcome struct {
	step       config.StepConfig // the EFFECTIVE branch (group fallbacks applied)
	agentSlug  string
	resultText string
	// failReason is empty when the branch completed; otherwise it is the short
	// human-readable cause used in the aggregate header and the group error.
	failReason string
	// skipped marks a branch whose row was already 'completed' from an earlier
	// run: it was not launched, and its stored context is reused.
	skipped bool
	// context is the branch's captured step context, read back after capture
	// (or from the pre-existing row for a skipped branch).
	context string
	// hasManualContext records that the branch published its own context via
	// the update_step_context MCP tool while it ran, which wins over both the
	// result text and any summarize_chat pass.
	hasManualContext bool
}

// runParallelGroup fans out a group's branches, joins them under the group's
// `require` policy, and returns the aggregate as the group's step result.
//
// tmplCtx is the template context runStep already built for this cursor slot
// (step contexts, track chain, children, loop vars); each branch resolves its
// prompt from a copy with {{branch.*}} filled in. A non-nil error means the
// group failed and RunTask must stop — the group's own row is left 'running',
// matching how an ordinary failed step behaves.
func (e *Engine) runParallelGroup(ctx context.Context, t *task.Task, wf *config.WorkflowConfig, group config.StepConfig, tmplCtx *TemplateContext, ws workspaceContext, outputFn func([]string)) (stepResult, error) {
	branches := group.EffectiveBranches()
	isolated := group.Parallel.IsIsolated()

	// baseCommit is the task worktree's HEAD, the fork point of every isolated
	// branch; it stays empty for a shared group.
	var baseCommit string
	if isolated {
		if !t.Worktree {
			errMsg := fmt.Sprintf("parallel group %q: isolation: worktree requires a worktree", group.Name)
			e.recordExitCode(t.ID, 1, errMsg)
			return stepResult{}, errors.New(errMsg)
		}
		head, err := e.repo.GetLastCommitHash(t.WorktreePath)
		if err != nil {
			errMsg := fmt.Sprintf("parallel group %q: %v", group.Name, err)
			e.recordExitCode(t.ID, 1, errMsg)
			return stepResult{}, errors.New(errMsg)
		}
		baseCommit = head
	}

	rows, err := e.database.GetTaskStepRows(t.ID)
	if err != nil {
		log.Printf("Warning: failed to read step rows for parallel group %q of task #%d: %v", group.Name, t.ID, err)
		rows = nil
	}

	outcomes := make([]branchOutcome, len(branches))
	var launched []int
	for j, b := range branches {
		outcomes[j] = branchOutcome{step: b}
		if row, ok := rows[b.Name]; ok && row.Status == "completed" {
			// Reuse rather than re-run: see the completed-branch invariant above.
			// The slug is still resolved so the aggregate header names an agent.
			outcomes[j].skipped = true
			outcomes[j].context = row.Context
			outcomes[j].agentSlug = e.cfg.StepAgentSlug(wf, &branches[j])
			continue
		}
		launched = append(launched, j)
	}

	startMsg := fmt.Sprintf("=== parallel %s: started %d branches ===", group.Name, len(launched))
	if skipped := len(branches) - len(launched); skipped > 0 {
		startMsg = fmt.Sprintf("=== parallel %s: started %d branches (%d previously completed, skipped) ===",
			group.Name, len(launched), skipped)
	}
	e.appendTaskLog(t.ID, "%s", startMsg)
	if outputFn != nil {
		outputFn([]string{startMsg})
	}

	// gctx lets `require: all` stop the siblings the moment one branch fails,
	// and carries task-level cancellation (stop, daemon shutdown) through to
	// every branch.
	gctx, cancel := context.WithCancel(ctx)
	defer cancel()

	failFast := group.Parallel.EffectiveRequire() == config.RequireAll

	// An isolated branch's work is kept even when the group has enough
	// successes, so siblings are only cancelled once the threshold is out of
	// reach.
	var (
		failMu   sync.Mutex
		failures int
	)
	maxFailures := len(branches) - group.Parallel.RequiredCount()

	var wg sync.WaitGroup
	for _, j := range launched {
		wg.Add(1)
		go func(j int) {
			defer wg.Done()
			out := e.runBranch(gctx, t, wf, branches[j], tmplCtx, ws, baseCommit, outputFn)
			outcomes[j] = out
			if out.failReason == "" {
				return
			}
			if failFast {
				cancel()
				return
			}
			if isolated {
				failMu.Lock()
				failures++
				unreachable := failures > maxFailures
				failMu.Unlock()
				if unreachable {
					cancel()
				}
			}
		}(j)
	}
	wg.Wait()

	// summarize_chat passes run AFTER the join and one at a time: the
	// summarizer is a synchronous shell-out, and running several at once would
	// multiply the cost spike the group already represents.
	e.summarizeParallelBranches(ctx, t, wf, outcomes)

	completed := 0
	for _, out := range outcomes {
		if out.failReason == "" {
			completed++
		}
	}
	doneMsg := fmt.Sprintf("=== parallel %s: %d/%d succeeded ===", group.Name, completed, len(branches))
	e.appendTaskLog(t.ID, "%s", doneMsg)
	if outputFn != nil {
		outputFn([]string{doneMsg})
	}

	if required := group.Parallel.RequiredCount(); completed < required {
		errMsg := formatParallelFailure(group, outcomes, completed, len(branches))
		if tail := e.firstFailedBranchTail(t.ID, outcomes); tail != "" {
			errMsg += "\n" + tail
		}
		e.recordExitCode(t.ID, 1, errMsg)
		return stepResult{}, errors.New(errMsg)
	}

	entries := make([]ParallelBranchResult, 0, len(outcomes))
	for _, out := range outcomes {
		entries = append(entries, ParallelBranchResult{
			Name:       out.step.Name,
			Agent:      out.agentSlug,
			Context:    out.context,
			FailReason: out.failReason,
		})
	}
	return stepResult{
		step:       group,
		exitCode:   0,
		resultText: FormatParallelAggregate(entries),
	}, nil
}

// runBranch executes one branch to completion and records its task_steps row.
// It never returns an error: a branch failure is data (branchOutcome.failReason)
// that the group's join policy decides what to do with.
// branch is the EFFECTIVE branch (group fallbacks already applied by
// EffectiveBranch), so the group itself is not needed here.
// baseCommit is non-empty only for an isolated group: the branch then runs in
// its own worktree forked from it, and its changes are committed on success.
func (e *Engine) runBranch(ctx context.Context, t *task.Task, wf *config.WorkflowConfig, branch config.StepConfig, tmplCtx *TemplateContext, ws workspaceContext, baseCommit string, outputFn func([]string)) branchOutcome {
	out := branchOutcome{step: branch}

	if err := e.database.CreateTaskStep(t.ID, branch.Name); err != nil {
		log.Printf("Warning: failed to create task step record for branch %q: %v", branch.Name, err)
	}

	fail := func(reason string, exitCode int) branchOutcome {
		out.failReason = reason
		if err := e.database.FailTaskStep(t.ID, branch.Name, exitCode); err != nil {
			log.Printf("Warning: failed to mark branch %q failed for task #%d: %v", branch.Name, t.ID, err)
		}
		return out
	}

	agentSlug, agentCfg, agentErr := e.cfg.StepAgent(wf, &branch)
	out.agentSlug = agentSlug
	if agentErr != nil {
		return fail(agentErr.Error(), 1)
	}

	// run is the task as the agent sees it: for an isolated branch, a copy
	// pointed at the branch's own worktree and git branch. Rows, logs and the
	// manual-context lookup stay keyed on the shared task ID.
	run := t
	imageRelPaths := ws.imageRelPaths
	if baseCommit != "" {
		if e.branchSlots != nil {
			release, err := e.branchSlots.AcquireBranchSlot(ctx, t.ID)
			if err != nil {
				return fail(branchFailReason(err, e.cfg.GetStepTimeout(branch)), 1)
			}
			defer release()
		}
		bt := *t
		bt.WorktreePath = IsolatedBranchWorktreePath(t.WorktreePath, branch.Name)
		bt.Branch = IsolatedBranchGitBranch(t.Branch, branch.Name)
		if _, err := e.repo.EnsureWorktreeAt(bt.WorktreePath, bt.Branch, baseCommit); err != nil {
			return fail(fmt.Sprintf("worktree: %v", err), 1)
		}
		paths, err := e.populateWorktree(ctx, &bt, wf)
		if err != nil {
			return fail(err.Error(), 1)
		}
		run = &bt
		imageRelPaths = paths
	}

	// Each branch resolves its own prompt from a COPY of the shared context so
	// {{branch.*}} is branch-local and the snapshot (step contexts, track
	// chain, children) stays identical across the fan-out.
	bctx := *tmplCtx
	bctx.Branch = BranchVars{Name: branch.Name, Agent: agentSlug}
	prompt, err := ResolveTemplate(branch.Prompt, &bctx)
	if err != nil {
		return fail(fmt.Sprintf("prompt: %v", err), 1)
	}
	prompt = appendImagesSection(prompt, imageRelPaths)

	env := e.stepEnv(run, branch.Name, agentSlug)
	spawn := headlessSpawn{
		LogPath:    BranchLogPath(e.dataDir, t.ID, branch.Name),
		OutputFile: branchOutputFile(run.WorktreePath, branch.Name),
		Tag:        branch.Name,
	}

	exitCode, resultText, _, spawnErr := e.runner.runHeadlessStep(ctx, e, run, branch, agentCfg, prompt, env, outputFn, spawn)
	if spawnErr != nil {
		return fail(branchFailReason(spawnErr, e.cfg.GetStepTimeout(branch)), 1)
	}
	if exitCode != 0 {
		return fail(fmt.Sprintf("exit %d", exitCode), exitCode)
	}
	if baseCommit != "" {
		msg := fmt.Sprintf("sakusen: task #%d branch %s", t.ID, branch.Name)
		if err := e.repo.Commit(run.WorktreePath, msg); err != nil {
			return fail(fmt.Sprintf("commit: %v", err), 1)
		}
	}

	out.resultText = resultText
	out.context, out.hasManualContext = e.captureBranchContext(t, branch, resultText, exitCode)
	return out
}

// branchFailReason turns a spawn error into the short cause shown in the
// aggregate header. The timeout and cancellation cases are distinguished
// because they read very differently to whoever consumes the aggregate: a
// timeout means the branch had more to say, a cancellation means a sibling
// failed first under `require: all`.
func branchFailReason(err error, timeout time.Duration) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("timed out after %s", timeout)
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return err.Error()
	}
}

// captureBranchContext applies the manual > last_message precedence for a
// branch that just finished and flips its row to 'completed'. summarize_chat
// is deliberately NOT handled here — it runs sequentially after the join (see
// summarizeParallelBranches), because a branch goroutine must not shell out to
// the summarizer while its siblings are still streaming. Returns the context
// stored on the row and whether it came from a manual override.
func (e *Engine) captureBranchContext(t *task.Task, branch config.StepConfig, resultText string, exitCode int) (string, bool) {
	manual, hasManual, mErr := e.readManualOverride(t.ID, branch.Name, false)
	if mErr != nil {
		log.Printf("Warning: failed to read running step context for branch %q of task #%d: %v", branch.Name, t.ID, mErr)
	}

	strategy := branch.EffectiveSummarizationStrategy()
	_, value, hasValue := decideInitialStepContext(hasManual, manual, strategy, resultText)
	var ctxPtr *string
	if hasValue {
		ctxPtr = &value
	}
	if err := e.database.CompleteTaskStep(t.ID, branch.Name, ctxPtr, exitCode); err != nil {
		log.Printf("Warning: failed to complete task step record for branch %q: %v", branch.Name, err)
	}
	return value, hasManual
}

// summarizeParallelBranches runs the summarize_chat pass for every completed
// branch that asks for one, sequentially, under a single summarizing-status
// flip. Branch outcomes are updated in place with the resulting context so the
// aggregate reflects the summary rather than the raw result text.
func (e *Engine) summarizeParallelBranches(ctx context.Context, t *task.Task, wf *config.WorkflowConfig, outcomes []branchOutcome) {
	var pending []int
	for i := range outcomes {
		out := &outcomes[i]
		if out.failReason != "" || out.skipped {
			continue
		}
		if out.step.EffectiveSummarizationStrategy() != config.SummarizationStrategySummarizeChat {
			continue
		}
		pending = append(pending, i)
	}
	if len(pending) == 0 {
		return
	}

	restore := e.markSummarizingStep(t, wf)
	defer restore()

	for _, i := range pending {
		out := &outcomes[i]
		// A manual override already won in captureBranchContext; re-deriving
		// the context from the chat would clobber it.
		if out.hasManualContext {
			continue
		}
		chat := e.loadBranchChatContent(t, out.step.Name)
		if chat == "" || !shouldSummarizeChat(chat, out.resultText, false) {
			continue
		}
		summary, err := e.summarizeChatLog(ctx, t, out.step.Name, out.step.SummarizationPrompt, chat)
		if err != nil {
			log.Printf("Warning: summarize_chat failed for branch %q of task #%d: %v", out.step.Name, t.ID, err)
			continue
		}
		if summary == "" {
			continue
		}
		if err := e.database.UpdateTaskStepContext(t.ID, out.step.Name, summary); err != nil {
			log.Printf("Warning: failed to update branch %q context after summarize_chat: %v", out.step.Name, err)
			continue
		}
		out.context = summary
	}
}

// loadBranchChatContent returns a branch's streamed agent output for the
// summarize_chat pass. It is loadStepChatContent's headless arm pointed at the
// per-branch log instead of the unified task log — the region layout is
// identical, so stepAgentOutput applies unchanged.
func (e *Engine) loadBranchChatContent(t *task.Task, branchName string) string {
	data, err := os.ReadFile(BranchLogPath(e.dataDir, t.ID, branchName))
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Warning: failed to read branch log for %q of task #%d: %v", branchName, t.ID, err)
		}
		return ""
	}
	return stepAgentOutput(string(data), branchName)
}

// formatParallelFailure builds the error a group returns when too few branches
// succeeded. It names every failed branch with its reason so the task's error
// message alone explains the join, without opening any log.
func formatParallelFailure(group config.StepConfig, outcomes []branchOutcome, completed, total int) string {
	var reasons []string
	for _, out := range outcomes {
		if out.failReason == "" {
			continue
		}
		reasons = append(reasons, fmt.Sprintf("%s: %s", out.step.Name, out.failReason))
	}
	return fmt.Sprintf("parallel group %q: %d/%d branches succeeded (require %s): %s",
		group.Name, completed, total, group.Parallel.EffectiveRequire(), strings.Join(reasons, ", "))
}

// firstFailedBranchTail returns the last lines of the first failed branch's
// log, mirroring the outputTail an ordinary failed step appends to its error.
// A branch that was merely cancelled (a sibling failed first under
// `require: all`) is skipped when a genuine failure exists: its log ends
// mid-sentence and explains nothing about why the group failed.
func (e *Engine) firstFailedBranchTail(taskID int64, outcomes []branchOutcome) string {
	pick := -1
	for i, out := range outcomes {
		if out.failReason == "" {
			continue
		}
		if out.failReason != "cancelled" {
			pick = i
			break
		}
		if pick < 0 {
			pick = i
		}
	}
	if pick < 0 {
		return ""
	}
	return readLogTail(BranchLogPath(e.dataDir, taskID, outcomes[pick].step.Name), 20)
}
