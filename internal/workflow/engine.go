package workflow

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Bakaface/sortie/internal/config"
	gitpkg "github.com/Bakaface/sortie/internal/git"
	"github.com/Bakaface/sortie/internal/merge"
	"github.com/Bakaface/sortie/internal/notify"
	"github.com/Bakaface/sortie/internal/runner"
	"github.com/Bakaface/sortie/internal/task"
)

const (
	// processExitPollInterval is how often to check whether a headless agent subprocess has exited.
	processExitPollInterval = 500 * time.Millisecond
)

type Engine struct {
	cfg      *engineConfig
	database taskStore
	notifier *notify.Notifier
	repoRoot string
	repo     *gitpkg.Repo // git operations scoped to repoRoot / task worktrees
	dataDir  string
	coord    *merge.Coordinator // owns merge serialization, retry, and cleanup-on-failure

	// runner is the AGENT-RUNNER seam: runStep calls it (instead of calling
	// runHeadlessAgent directly) to execute a headless step's agent command,
	// so tests can substitute a fake that returns scripted results without
	// spawning a real subprocess. Defaults to realAgentRunner{} (see
	// step.go); tests in this package override it by setting the field
	// directly after construction — the same pattern already used for e.repo
	// in fasttrack_test.go. The tmux path (runStepTmux) is NOT behind
	// this seam; see agentRunner's doc comment in step.go for why.
	runner agentRunner

	// onPause, when set, is invoked from applyStepResult the moment the engine
	// decides to pause a task at an approval gate (human step or tmux step),
	// before the pause status lands in the DB. It gives the daemon an explicit
	// "this run genuinely paused" signal so its agent-completion handler does
	// not have to infer a pause from the task's DB status alone — a status that
	// a concurrent request can overwrite (see SetPauseCallback).
	onPause func(taskID int64)
}

// NewEngine creates a new workflow engine. It still accepts the full
// *config.Config (callers — the daemon and tests — construct/reload the
// whole blob, so there's no reason to make them narrow it themselves) but
// only retains the narrow engineConfig view internally; see engine_config.go
// for the dependency surface this actually uses.
//
// The optional Lock parameter is an externally-owned per-repo merge
// serializer; when non-nil, the lock survives config reloads (the daemon
// owns it via merge.Locks). When nil, a fresh lock is created for
// standalone/test usage.
func NewEngine(cfg *config.Config, database taskStore, notifier *notify.Notifier, repoRoot string, lock ...*merge.Lock) *Engine {
	var lk *merge.Lock
	if len(lock) > 0 && lock[0] != nil {
		lk = lock[0]
	} else {
		lk = &merge.Lock{}
	}
	e := &Engine{
		cfg:      newEngineConfig(cfg),
		database: database,
		notifier: notifier,
		repoRoot: repoRoot,
		repo:     gitpkg.NewRepo(repoRoot),
		dataDir:  filepath.Join(repoRoot, ".sortie"),
		runner:   realAgentRunner{},
	}
	e.coord = merge.NewCoordinator(
		repoRoot,
		lk,
		merge.Config{},
		e.bindConflictResolver(),
		database.UpdateTaskStatus,
		database.AppendTaskCommit,
	)
	return e
}

// Coord returns the engine's merge coordinator. The daemon uses it to take the
// per-repo lock for ad-hoc operations (revert, worktree teardown) that must
// serialize against merges.
func (e *Engine) Coord() *merge.Coordinator { return e.coord }

// SetPauseCallback registers cb to be called with the task ID whenever the
// engine pauses a task at an approval gate (see Engine.onPause). The callback
// runs synchronously on the runner goroutine, before RunTask returns, so it
// must not block. The registry the daemon keeps behind this callback must live
// on the daemon side, not on the Engine: engines are reconstructed whenever a
// project's .sortie.yml changes, which would silently drop any pause state
// held here.
func (e *Engine) SetPauseCallback(cb func(taskID int64)) {
	e.onPause = cb
}

// buildTemplateContext constructs a TemplateContext wired to the engine's
// database for {{tasks.X.field}} lookups. Centralised so engine + summarizer
// call sites don't drift.
func (e *Engine) buildTemplateContext(t *task.Task, taskVars TaskVars, stepContexts map[string]string, loopVars LoopVars) *TemplateContext {
	return &TemplateContext{
		Task:  taskVars,
		Steps: stepContexts,
		Git: GitVars{
			BaseBranch:   e.cfg.BaseBranch,
			TargetBranch: e.effectiveBaseBranch(t),
			RepoRoot:     e.repoRoot,
		},
		Loop:       loopVars,
		TaskLookup: e.database.GetTask,
	}
}

// findStepIndex returns the index of a step by name, or -1 if not found.
func findStepIndex(steps []config.StepConfig, name string) int {
	for i, s := range steps {
		if s.Name == name {
			return i
		}
	}
	return -1
}

// effectiveBaseBranch returns the base branch for a task, checking the task's
// per-task TargetBranch override first, then falling back to the config's Git.BaseBranch,
// and finally to "main".
func (e *Engine) effectiveBaseBranch(t *task.Task) string {
	if t.TargetBranch != "" {
		return t.TargetBranch
	}
	if e.cfg.BaseBranch != "" {
		return e.cfg.BaseBranch
	}
	return "main"
}

// effectiveOnComplete returns the finalization action for a task. Resolution
// (locality precedence between workflow-level and project-level on_complete)
// lives in config.Config.EffectiveOnComplete.
func (e *Engine) effectiveOnComplete(t *task.Task) string {
	return e.cfg.EffectiveOnComplete(t.Workflow)
}

// dirExists reports whether path exists and is a directory. Used by
// EnsureWorktree's checkDisk mode to detect a worktree whose directory has
// vanished out from under a persisted t.WorktreePath (e.g. after
// cleanupWorktreeAndBranch ran on a task that's now being resumed).
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// EnsureWorktree idempotently provisions the git worktree for a
// worktree-mode task: resolving/persisting the branch name, then creating a
// new worktree (or checking out an existing branch when t.CheckoutBranch is
// set) unless one is already in place. It is the single implementation
// shared by Engine.RunTask (first-run provisioning) and the daemon's
// worktree-recreation path (handlers_continue.go's ensureWorktree, used when
// resuming a task whose worktree may have been cleaned up) — those two call
// sites previously duplicated this sequence and had drifted; see the
// checkDisk doc below for the one behavioral difference preserved between
// them.
//
// No-op for non-worktree tasks (t.Worktree == false): returns
// (t.WorktreePath, false, nil). Callers own the no-worktree path themselves
// (RunTask sets WorktreePath = repoRoot; the daemon does the same in
// handleContinueTask) since it isn't "provisioning a worktree" at all.
//
// checkDisk controls the idempotency check that decides "is a worktree
// already provisioned":
//   - false (RunTask's historical behavior): a non-empty t.WorktreePath
//     alone is trusted as already-provisioned, even if the directory no
//     longer exists on disk.
//   - true (the daemon's recreation semantics): the directory is also
//     verified to exist, and a missing directory triggers recreation even
//     though WorktreePath was persisted from an earlier run.
//
// Returns the resolved worktree path, whether this call actually performed a
// create/checkout (false when an existing worktree was reused — callers use
// this to decide whether to (re-)run sync-paths/setup-commands), and any
// error from the underlying git operation.
func (e *Engine) EnsureWorktree(t *task.Task, checkDisk bool) (path string, provisioned bool, err error) {
	if !t.Worktree {
		return t.WorktreePath, false, nil
	}
	if t.WorktreePath != "" && (!checkDisk || dirExists(t.WorktreePath)) {
		return t.WorktreePath, false, nil
	}

	if t.CheckoutBranch != "" {
		// Checkout existing branch mode
		if t.Branch == "" {
			t.Branch = t.CheckoutBranch
		}
		if dbErr := e.database.UpdateTaskBranch(t.ID, t.Branch); dbErr != nil {
			log.Printf("Warning: failed to persist branch name for task #%d: %v", t.ID, dbErr)
		}
		worktree, err := e.repo.CheckoutWorktree(t.ID, t.CheckoutBranch)
		if err != nil {
			return "", false, fmt.Errorf("failed to checkout worktree: %w", err)
		}
		t.WorktreePath = worktree.Path
	} else {
		// Normal new branch mode
		if t.Branch == "" {
			t.Branch = e.cfg.ResolveBranchForTask(t.ID, t.Title, t.Slug, t.BranchName)
			if dbErr := e.database.UpdateTaskBranch(t.ID, t.Branch); dbErr != nil {
				log.Printf("Warning: failed to persist branch name for task #%d: %v", t.ID, dbErr)
			}
		}
		worktree, err := e.repo.CreateWorktree(t.ID, e.effectiveBaseBranch(t), t.Branch)
		if err != nil {
			return "", false, fmt.Errorf("failed to create worktree: %w", err)
		}
		t.WorktreePath = worktree.Path
	}

	if err := e.database.UpdateTaskWorktreePath(t.ID, t.WorktreePath); err != nil {
		log.Printf("Warning: failed to update worktree path for task #%d: %v", t.ID, err)
	}
	return t.WorktreePath, true, nil
}

// RunTask executes the full workflow pipeline for a task.
// It creates/reuses the worktree, then loops through steps starting from t.StepIndex.
// outputFn is called with parsed log lines for live streaming (may be nil).
//
// The pipeline is a driver loop over three named stages:
//   - prepareWorkspace: everything that happens once, before the step loop
//     (worktree provisioning, path sync, setup commands, image copy).
//   - runStep: builds the per-step template context/prompt and executes the
//     step (headless via the agentRunner seam, or tmux), returning a
//     stepResult. An error here means the step itself failed to run (spawn
//     error or non-zero exit) — RunTask returns immediately.
//   - applyStepResult: the post-step decisions (session recording, waits-on
//     suspension, no-output validation, step-context capture, loop
//     evaluation, approval/tmux pausing) — returns a stepOutcome the driver
//     switches on.
func (e *Engine) RunTask(ctx context.Context, t *task.Task, outputFn func([]string)) error {
	wf := e.cfg.GetWorkflow(t.Workflow)
	steps := wf.Steps

	ws, err := e.prepareWorkspace(ctx, t, wf)
	if err != nil {
		return err
	}

	// Loop through steps from current index
	for i := t.StepIndex; i < len(steps); i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		step := steps[i]

		result, err := e.runStep(ctx, t, wf, steps, step, i, ws, outputFn)
		if err != nil {
			return err
		}

		outcome, err := e.applyStepResult(ctx, t, wf, steps, i, result)
		if err != nil {
			return err
		}

		switch outcome.kind {
		case stepOutcomeSuspend, stepOutcomePause:
			return nil
		case stepOutcomeGoto:
			i = outcome.gotoIdx - 1 // for-loop will increment
			continue
		case stepOutcomeAdvance:
			// fall through to the for-loop's natural i++
		}
	}

	// All steps completed — merge, summarization, and cleanup
	// are handled by the daemon on agent completion.
	return nil
}

// workspaceContext carries the state prepareWorkspace produces that every
// step in the loop needs. Currently just the attached-image paths (resolved
// once, relative to the worktree) — the worktree path itself lives on
// t.WorktreePath, which prepareWorkspace also sets.
type workspaceContext struct {
	imageRelPaths []string
}

// prepareWorkspace runs everything RunTask needs to do exactly once, before
// the per-step loop: provisioning/reusing the worktree, syncing configured
// paths into it, running worktree setup command(s), ensuring the .sortie
// work dirs exist, and copying attached images. Extracted verbatim from the
// front of RunTask; see EnsureWorktree's doc comment for the worktree
// provisioning semantics this delegates to.
func (e *Engine) prepareWorkspace(ctx context.Context, t *task.Task, wf *config.WorkflowConfig) (workspaceContext, error) {
	if t.Worktree {
		if _, _, err := e.EnsureWorktree(t, false); err != nil {
			return workspaceContext{}, err
		}
	} else {
		// No-worktree mode: run in project root directory.
		// Persist the path so daemon restart can restore tmux sessions for
		// non-worktree tasks (DB row would otherwise have NULL worktree_path).
		t.WorktreePath = e.repoRoot
		if err := e.database.UpdateTaskWorktreePath(t.ID, e.repoRoot); err != nil {
			log.Printf("Warning: failed to persist worktree path for non-worktree task #%d: %v", t.ID, err)
		}
	}

	// Sync configured paths from project root into the worktree. This runs on
	// every RunTask call (including resumes after approval), not just when
	// EnsureWorktree actually provisioned a fresh worktree — see
	// EnsureWorktree's doc comment for why the daemon's recreation path
	// (ensureWorktree in handlers_continue.go) does not mirror this.
	if t.Worktree {
		syncPaths := e.cfg.GetWorktreeSyncPaths(wf)
		if !syncPaths.IsEmpty() {
			if err := SyncPathsToWorktree(e.repoRoot, t.WorktreePath, syncPaths); err != nil {
				log.Printf("Warning: failed to sync paths to worktree: %v", err)
			}
		}
	}

	// Run worktree setup command(s) if configured
	if t.Worktree {
		if setupCmd := e.cfg.GetWorktreeSetupCommand(wf); setupCmd != "" {
			if err := RunWorktreeSetupCommand(ctx, e.repoRoot, t.WorktreePath, setupCmd); err != nil {
				return workspaceContext{}, fmt.Errorf("worktree setup failed: %w", err)
			}
		}
		if setupCmds := e.cfg.GetWorktreeSetupCommands(wf); len(setupCmds) > 0 {
			if err := RunWorktreeSetupCommands(ctx, e.repoRoot, t.WorktreePath, setupCmds); err != nil {
				return workspaceContext{}, fmt.Errorf("worktree setup failed: %w", err)
			}
		}
	}

	// Ensure .sortie directories exist in worktree
	if err := EnsureWorkDirs(t.WorktreePath); err != nil {
		return workspaceContext{}, fmt.Errorf("failed to create sortie dirs: %w", err)
	}

	// Copy attached images to worktree
	var imageRelPaths []string
	if len(t.Images) > 0 {
		var err error
		imageRelPaths, err = CopyImagesToWorktree(t.WorktreePath, t.Images)
		if err != nil {
			log.Printf("Warning: failed to copy images to worktree: %v", err)
		}
	}

	return workspaceContext{imageRelPaths: imageRelPaths}, nil
}

// stepResult carries a single step's execution outcome from runStep to
// applyStepResult.
type stepResult struct {
	step       config.StepConfig
	exitCode   int
	resultText string
	outputTail string
	useTmux    bool
}

// runStep builds the per-step template context/prompt and executes step i,
// either headless (through the agentRunner seam — see step.go) or in tmux.
// A non-nil error means the step itself did not complete successfully (spawn
// error or non-zero exit): RunTask's driver returns immediately in that
// case, so this function performs the same exit-code bookkeeping the
// original inline loop body did (UpdateTaskExitCode + formatted error)
// before any post-step decision (session recording, loop evaluation,
// approval pausing, ...) gets a chance to run.
func (e *Engine) runStep(ctx context.Context, t *task.Task, wf *config.WorkflowConfig, steps []config.StepConfig, step config.StepConfig, i int, ws workspaceContext, outputFn func([]string)) (stepResult, error) {
	// Update step tracking
	if err := e.database.UpdateTaskStep(t.ID, i, step.Name); err != nil {
		log.Printf("Warning: failed to update task step: %v", err)
	}

	// If we are resuming after children finished (StatusAwaitingChildren →
	// pending via the poller), the wait-on edges still exist. Snapshot the
	// children for {{children.*}} template vars, then clear the edges so
	// the post-step check sees only NEW edges added during this run.
	// First-time step runs simply find an empty list and skip the work.
	childrenVars, err := e.loadAndClearChildren(t)
	if err != nil {
		log.Printf("Warning: failed to load waits-on children for task #%d: %v", t.ID, err)
	}

	// Record step start in task_steps table
	if err := e.database.CreateTaskStep(t.ID, step.Name); err != nil {
		log.Printf("Warning: failed to create task step record: %v", err)
	}

	// Collect step contexts from ALL steps that have a recorded context.
	// We include later-indexed steps too because loops can populate them
	// in earlier iterations (e.g. reviewing's issues feeding back to
	// implementing on the next pass). Steps that haven't run yet simply
	// have no DB row and resolve to "".
	var allStepNames []string
	for _, s := range steps {
		allStepNames = append(allStepNames, s.Name)
	}
	stepContexts, err := e.database.GetTaskStepContexts(t.ID, allStepNames)
	if err != nil {
		log.Printf("Warning: failed to get step contexts: %v", err)
		stepContexts = make(map[string]string)
	}

	// Build template context and resolve prompt
	loopVars := LoopVars{}
	if step.Loop != nil {
		loopVars.Iteration = t.LoopIteration
		loopVars.MaxIterations = step.Loop.MaxIterations
	} else {
		// Check if any step in the current loop range references this step
		// to populate loop vars for non-loop steps inside a loop
		for _, s := range steps {
			if s.Loop != nil && s.Loop.Goto != "" {
				targetIdx := findStepIndex(steps, s.Loop.Goto)
				if targetIdx >= 0 && targetIdx <= i && i <= findStepIndex(steps, s.Name) {
					loopVars.Iteration = t.LoopIteration
					loopVars.MaxIterations = s.Loop.MaxIterations
					break
				}
			}
		}
	}
	// Pre-resolve any {{tasks.X.field}} refs inside this task's own
	// input / context so step prompts that inline {{task.input}}
	// or {{task.context}} see the expanded text. Single-pass — nested refs
	// in the looked-up task's fields remain verbatim.
	resolvedInput := ResolveTaskRefs(t.Input, e.database.GetTask)
	resolvedContext := ResolveTaskRefs(t.Context, e.database.GetTask)

	tmplCtx := e.buildTemplateContext(t, TaskVars{
		ID:      t.ID,
		Title:   t.Title,
		Input:   resolvedInput,
		Context: resolvedContext,
		Slug:    t.Slug,
		Branch:  t.Branch,
		Images:  ws.imageRelPaths,
	}, stepContexts, loopVars)
	tmplCtx.Children = childrenVars

	// Load the task's track chain fresh at every step launch (not once per
	// task) so context published mid-run — by this task or by sibling tasks on
	// the same track — is visible to later steps. Trackless tasks leave
	// tmplCtx.Track at its zero value, resolving every {{track.*}} var to "".
	if t.TrackID != nil {
		chain, err := e.database.GetTrackChain(*t.TrackID)
		if err != nil {
			return stepResult{}, fmt.Errorf("failed to load track chain for track %d: %w", *t.TrackID, err)
		}
		leaf := chain[len(chain)-1]
		tmplCtx.Track = TrackVars{
			ID:         leaf.ID,
			Name:       leaf.Name,
			Context:    FormatTrackChain(chain),
			OwnContext: leaf.Context,
		}
	}

	resolvedPrompt := ResolveTemplate(step.Prompt, tmplCtx)

	// Surface attached images by appending a pointer section to the prompt —
	// there is no agent-agnostic system-prompt channel to inject them through.
	if len(ws.imageRelPaths) > 0 {
		var sb strings.Builder
		sb.WriteString(resolvedPrompt)
		sb.WriteString("\n\n## Attached Images\n\n")
		sb.WriteString("The following images were attached to this task. Use your file reading tool to view them:\n\n")
		for _, imgPath := range ws.imageRelPaths {
			sb.WriteString("- `" + imgPath + "`\n")
		}
		resolvedPrompt = sb.String()
	}

	// Resolve the agent that runs this step (step.agent → workflow.agent →
	// default_agent → "claude"). An unresolvable slug fails the step with an
	// instructive error instead of spawning anything.
	agentSlug, agentCfg, agentErr := e.cfg.StepAgent(wf, &step)
	if agentErr != nil {
		e.database.UpdateTaskExitCode(t.ID, 1, agentErr.Error())
		return stepResult{}, fmt.Errorf("step %q failed: %w", step.Name, agentErr)
	}

	// Set environment variables
	env := map[string]string{
		"SORTIE_TASK_ID":  fmt.Sprintf("%d", t.ID),
		"SORTIE_STEP":     step.Name,
		"SORTIE_WORKTREE": t.WorktreePath,
		"SORTIE_PURPOSE":  "step",
		// SORTIE_PROJECT_PATH is the absolute path to the project repo
		// root (NOT the worktree). MCP servers / scripts spawned inside
		// the step that need to create child tasks under the same
		// project use this so they don't accidentally register their
		// `git rev-parse --show-toplevel` (which in a worktree returns
		// the worktree path) as a new project row.
		"SORTIE_PROJECT_PATH": e.repoRoot,
	}
	if t.TrackID != nil {
		// Lets agents target "their" track via the update_track_context MCP tool.
		env["SORTIE_TRACK_ID"] = fmt.Sprintf("%d", *t.TrackID)
	}
	// The resolved agent slug, so stubs/scripts can tell which agent record a
	// spawn came from without re-deriving the cascade.
	env["SORTIE_AGENT"] = agentSlug

	// Spawn the step's agent (tmux or headless, per the agent record's mode)
	useTmux := agentCfg.IsTmux()
	var exitCode int
	var resultText string
	var outputTail string
	var spawnErr error
	if useTmux {
		exitCode, outputTail, spawnErr = e.runStepTmux(ctx, t, step, agentCfg, resolvedPrompt, env, outputFn)
	} else {
		exitCode, resultText, outputTail, spawnErr = e.runner.runHeadlessStep(ctx, e, t, step, agentCfg, resolvedPrompt, env, outputFn)
	}
	if spawnErr != nil {
		e.database.UpdateTaskExitCode(t.ID, 1, spawnErr.Error())
		return stepResult{}, fmt.Errorf("step %q failed: %w", step.Name, spawnErr)
	}

	if exitCode != 0 {
		errMsg := fmt.Sprintf("step %q exited with code %d", step.Name, exitCode)
		if outputTail != "" {
			errMsg += "\n" + outputTail
		}
		e.database.UpdateTaskExitCode(t.ID, exitCode, errMsg)
		return stepResult{}, errors.New(errMsg)
	}

	return stepResult{
		step:       step,
		exitCode:   exitCode,
		resultText: resultText,
		outputTail: outputTail,
		useTmux:    useTmux,
	}, nil
}

// stepOutcomeKind is the driver-level signal applyStepResult returns to tell
// RunTask's loop what to do next — the control-flow equivalent of the
// continue/return-nil/loop-back branches the original inline loop body had.
type stepOutcomeKind int

const (
	// stepOutcomeAdvance: proceed to the next step (the for-loop's natural i++).
	stepOutcomeAdvance stepOutcomeKind = iota
	// stepOutcomeGoto: jump back to gotoIdx (a loop step's Goto target).
	stepOutcomeGoto
	// stepOutcomeSuspend: the step spawned child tasks it must wait on;
	// RunTask returns nil immediately, StepIndex left pointing at this step.
	stepOutcomeSuspend
	// stepOutcomePause: the step requires human approval or ran in tmux;
	// RunTask returns nil immediately, StepIndex already advanced past it.
	stepOutcomePause
)

type stepOutcome struct {
	kind    stepOutcomeKind
	gotoIdx int
}

// applyStepResult makes the post-step decisions that follow a successfully-
// executed step (runStep already handled spawn/exit-code failures): check
// for waits-on suspension, validate the step
// produced meaningful output, capture the step's context, evaluate any loop
// condition, and pause for approval/tmux if required. Returns the outcome
// RunTask's driver switches on, or a non-nil error when the step must fail
// the task (no-output validation).
func (e *Engine) applyStepResult(ctx context.Context, t *task.Task, wf *config.WorkflowConfig, steps []config.StepConfig, i int, result stepResult) (stepOutcome, error) {
	step := result.step

	// (Chat sessions for tmux steps are recorded from sentinel payloads by the
	// daemon's monitor — see RecordTmuxStepSentinelSession. Headless steps
	// have no session-discovery mechanism and record nothing.)

	// If this step spawned child tasks via the MCP create_tasks_and_wait /
	// wait_for_tasks tools, the daemon recorded task_waits_on edges. Suspend
	// the task at the SAME step (do not advance step_index) so the engine
	// re-runs this step once the children reach terminal status. The poller
	// detects "all children terminal" and flips status back to pending. On
	// the re-run, loadAndClearChildren (runStep) will surface their results
	// via {{children.*}} template variables.
	hasWaits, waitsErr := e.database.HasAnyWaitsOn(t.ID)
	if waitsErr != nil {
		log.Printf("Warning: failed to check waits-on for task #%d: %v", t.ID, waitsErr)
	}
	if hasWaits {
		// Skip output validation, step completion record, summarize_chat,
		// loop handling, and the tmux/human approval pause — none of them
		// apply to a step that is going to re-run. The step_index stays at
		// i so the next invocation resumes here.
		if err := e.database.UpdateTaskStatus(t.ID, task.StatusAwaitingChildren); err != nil {
			log.Printf("Warning: failed to set %s status for task #%d: %v", task.StatusAwaitingChildren, t.ID, err)
		}
		log.Printf("Task #%d suspended at step %q: waiting for spawned child tasks", t.ID, step.Name)
		return stepOutcome{kind: stepOutcomeSuspend}, nil
	}

	// Validate that the step produced meaningful changes
	// Skip for human and tmux steps (handled separately or not yet complete)
	if !step.Human && !result.useTmux {
		if strings.TrimSpace(result.resultText) == "" {
			// No output — require a diff as the alternative signal
			noiseFiles := []string{runner.OutputLogFileName}
			hasChanges, err := e.repo.HasMeaningfulChanges(t.WorktreePath, noiseFiles)
			if err != nil {
				log.Printf("Warning: failed to check for changes in step %q: %v", step.Name, err)
			} else if !hasChanges {
				errMsg := fmt.Sprintf("step %q exited successfully but produced no output or code changes", step.Name)
				e.database.UpdateTaskExitCode(t.ID, 1, errMsg)
				return stepOutcome{}, errors.New(errMsg)
			}
		}
	}

	// Capture this step's context following the manual > last_message >
	// summarize_chat precedence and the running/paused row-status routing
	// documented once in stepcontext.go (STEP-CONTEXT LIFECYCLE).
	e.captureHeadlessStepContext(ctx, t, wf, step, result.resultText, result.exitCode, result.useTmux)

	// Evaluate loop condition
	if step.Loop != nil {
		shouldLoop := true

		// Check max iterations
		if t.LoopIteration >= step.Loop.MaxIterations {
			shouldLoop = false
		}

		// Check exit condition
		if shouldLoop && step.Loop.ExitCondition != nil {
			if step.Loop.ExitCondition.StepContextEmpty != "" {
				content, _ := e.database.GetTaskStepContext(t.ID, step.Loop.ExitCondition.StepContextEmpty)
				if strings.TrimSpace(content) == "" {
					shouldLoop = false
					log.Printf("Loop exit: step context %q is empty for task #%d", step.Loop.ExitCondition.StepContextEmpty, t.ID)
				}
			}
		}

		if shouldLoop {
			targetIdx := findStepIndex(steps, step.Loop.Goto)
			t.LoopIteration++
			if err := e.database.UpdateTaskLoopIteration(t.ID, t.LoopIteration); err != nil {
				log.Printf("Warning: failed to update loop iteration: %v", err)
			}
			if err := e.database.UpdateTaskStep(t.ID, targetIdx, steps[targetIdx].Name); err != nil {
				log.Printf("Warning: failed to update task step for loop: %v", err)
			}
			log.Printf("Task #%d looping back to step %q (iteration %d/%d)", t.ID, step.Loop.Goto, t.LoopIteration, step.Loop.MaxIterations)
			return stepOutcome{kind: stepOutcomeGoto, gotoIdx: targetIdx}, nil
		}

		// Loop done, reset counter
		log.Printf("Task #%d loop completed after %d iterations", t.ID, t.LoopIteration)
		t.LoopIteration = 0
		if err := e.database.UpdateTaskLoopIteration(t.ID, 0); err != nil {
			log.Printf("Warning: failed to reset loop iteration: %v", err)
		}
	}

	// Check if approval required before continuing
	needsApproval := step.Human || result.useTmux
	if needsApproval {
		// Signal the pause explicitly BEFORE the status write: the daemon's
		// agent-completion handler must not have to trust the DB status alone
		// to distinguish "engine paused this run" from "a concurrent request
		// overwrote the status while the agent ran".
		if e.onPause != nil {
			e.onPause(t.ID)
		}
		// Set status to pause the task. The daemon will wait for user action.
		if err := e.database.UpdateTaskStep(t.ID, i+1, ""); err != nil {
			log.Printf("Warning: failed to update task step: %v", err)
		}
		pauseStatus := task.StatusAwaitingApproval
		if result.useTmux {
			pauseStatus = task.StatusTmux
		}
		if err := e.database.UpdateTaskStatus(t.ID, pauseStatus); err != nil {
			log.Printf("Warning: failed to set %s: %v", pauseStatus, err)
		}
		return stepOutcome{kind: stepOutcomePause}, nil
	}

	return stepOutcome{kind: stepOutcomeAdvance}, nil
}

// ErrStepContextRequired is returned by summarizePreviousTmuxStep when a step
// marked `require_context: true` fails to capture its summarize_chat context.
// Callers wrap it; the daemon checks it with errors.Is to decide whether to
// block the task (fail) instead of advancing/finalizing with an empty context.
var ErrStepContextRequired = errors.New("required step context could not be captured")

// ResumeAfterApproval resumes a task from its current step index.
// If the previously-completed step was a tmux step with summarize_chat strategy,
// summarisation runs synchronously here (it could not run during the step itself
// because tmux steps return early and pause the engine).
//
// When the previous step is marked `require_context: true` and its context
// cannot be captured, the task is NOT advanced: the error is recorded and
// returned so the agent-completion path fails the task instead of running the
// next step with an empty {{steps.<name>.context}}.
func (e *Engine) ResumeAfterApproval(ctx context.Context, t *task.Task, outputFn func([]string)) error {
	if err := e.summarizePreviousTmuxStep(ctx, t, nil); err != nil {
		e.database.UpdateTaskExitCode(t.ID, 1, err.Error())
		return err
	}
	return e.RunTask(ctx, t, outputFn)
}

// summarizePreviousTmuxStep runs summarize_chat for the paused step (see
// PausedStep and the cursor invariant in cursor.go) when that step is tmux
// with the summarize_chat strategy. No-op otherwise. This is the real capture
// point for tmux steps in the manual > last_message > summarize_chat
// precedence chain documented in stepcontext.go (STEP-CONTEXT LIFECYCLE) —
// tmux steps have no last_message fallback, and captureHeadlessStepContext's
// attempt during RunTask almost always no-ops because the chat doesn't exist
// yet when a tmux step's spawn returns.
// This is the shared hook for capturing tmux step context after the engine
// has paused (since tmux steps return before their chat is meaningful):
//   - ResumeAfterApproval calls it when the user advances to the next step
//   - FinalizeTask calls it when the last step is a tmux step
//
// Best-effort by default: a failure to capture context is logged and nil is
// returned so the surrounding flow proceeds. When the step sets
// `require_context: true`, the same failure instead returns an error wrapping
// ErrStepContextRequired so the caller can block the task.
func (e *Engine) summarizePreviousTmuxStep(ctx context.Context, t *task.Task, logFn func(string, ...any)) error {
	logMsg := func(format string, args ...any) {
		log.Printf(format, args...)
		if logFn != nil {
			logFn(format, args...)
		}
	}

	wf := e.cfg.GetWorkflow(t.Workflow)
	prevStep, ok := PausedStep(t, wf)
	if !ok {
		return nil
	}
	if !e.cfg.StepIsTmux(wf, &prevStep) || prevStep.EffectiveSummarizationStrategy() != config.SummarizationStrategySummarizeChat {
		return nil
	}

	// If the step's context was already published manually — the agent folded
	// its chat into the step context via the update_step_context MCP tool — that
	// is the canonical artifact and must not be clobbered by the auto
	// summarizer. Folding manually is the whole point of update_step_context for
	// tmux steps, so respecting an existing context is the expected behaviour
	// (and it satisfies require_context: the context IS captured). See
	// readManualOverride (stepcontext.go) for the row-status this checks
	// (pausedTmux=true: the COMPLETED row).
	if existing, hasManual, ctxErr := e.readManualOverride(t.ID, prevStep.Name, true); ctxErr == nil && hasManual {
		logMsg("Step %q of task #%d already has a manually-folded context (%d chars); skipping summarize_chat", prevStep.Name, t.ID, len(existing))
		return nil
	}

	// fail records a context-capture failure. When the step demands context it
	// returns a blocking error (wrapping ErrStepContextRequired); otherwise it
	// logs a warning and returns nil so the flow proceeds best-effort.
	fail := func(format string, args ...any) error {
		if prevStep.RequireContext {
			return fmt.Errorf("step %q: %w: "+format, append([]any{prevStep.Name, ErrStepContextRequired}, args...)...)
		}
		logMsg("Warning: "+format, args...)
		return nil
	}

	chat, err := e.loadStepChatContent(ctx, t, wf, prevStep, true)
	if err != nil {
		return fail("failed to load chat for tmux step %q of task #%d: %v", prevStep.Name, t.ID, err)
	}
	if chat == "" {
		// The step completed but yielded no chat content to summarize, so its
		// context stays empty — and downstream steps that template
		// {{steps.<name>.context}} will silently receive nothing. loadStepChatContent
		// logs the specific cause (missing JSONL, no recorded session) below.
		return fail("tmux step %q of task #%d produced no chat content; its step context will be empty", prevStep.Name, t.ID)
	}
	// Surface the step summarization phase via the task status so
	// the TUI can distinguish it from regular step execution.
	restore := e.markSummarizingStep(t, wf)
	summary, err := e.summarizeChatLog(ctx, t, prevStep.Name, prevStep.SummarizationPrompt, chat)
	restore()
	if err != nil {
		return fail("summarize_chat failed for tmux step %q of task #%d: %v", prevStep.Name, t.ID, err)
	}
	if summary == "" {
		return fail("summarize_chat produced an empty summary for tmux step %q of task #%d", prevStep.Name, t.ID)
	}
	if err := e.database.UpdateTaskStepContext(t.ID, prevStep.Name, summary); err != nil {
		return fail("failed to write step context for tmux step %q of task #%d: %v", prevStep.Name, t.ID, err)
	}
	logMsg("summarize_chat updated step context for tmux step %q of task #%d (%d chars)", prevStep.Name, t.ID, len(summary))
	return nil
}

// loadAndClearChildren loads all task_waits_on children for t, converts them
// into a ChildrenVars map keyed by child ID, then deletes the wait-on edges
// from the DB. Returns an empty (but non-nil) map on first-time step runs (no
// edges). The clear-on-read pattern ensures the post-step check sees only
// NEW edges added during this run.
func (e *Engine) loadAndClearChildren(t *task.Task) (ChildrenVars, error) {
	children, err := e.database.GetWaitsOnChildren(t.ID)
	if err != nil {
		return ChildrenVars{ByID: map[int64]ChildVars{}}, err
	}
	out := ChildrenVars{ByID: make(map[int64]ChildVars, len(children))}
	if len(children) == 0 {
		return out, nil
	}
	for _, c := range children {
		out.ByID[c.ID] = ChildVars{
			ID:      c.ID,
			Title:   c.Title,
			Status:  string(c.Status),
			Context: c.Context,
		}
	}
	if err := e.database.RemoveAllTaskWaitsOn(t.ID); err != nil {
		log.Printf("Warning: failed to clear waits-on for task #%d after resume: %v", t.ID, err)
	}
	return out, nil
}

// markSummarizingStep transitions the task to the appropriate "summarizing"
// status and returns a function that restores the previous status. Used to
// surface the step summarization phase in the TUI without persisting it as
// the resting status of the task.
//
// For single-step workflows the step summary IS the task summary (FinalizeTask
// promotes it directly into task.context, skipping the cross-step summarizer),
// so the canonical StatusSummarizing is used. Multi-step workflows use
// StatusSummarizingStep to distinguish per-step summarization from the final
// cross-step summarizer.
//
// On any DB failure the function is a no-op (logged) — step summarization is
// a best-effort phase and must not block forward progress.
func (e *Engine) markSummarizingStep(t *task.Task, wf *config.WorkflowConfig) func() {
	prev := t.Status
	status := task.StatusSummarizingStep
	if wf != nil && len(wf.Steps) == 1 {
		status = task.StatusSummarizing
	}
	if err := e.database.UpdateTaskStatus(t.ID, status); err != nil {
		log.Printf("Warning: failed to set %s status for task #%d: %v", status, t.ID, err)
		return func() {}
	}
	t.Status = status
	return func() {
		if err := e.database.UpdateTaskStatus(t.ID, prev); err != nil {
			log.Printf("Warning: failed to restore status %q for task #%d after step summarization: %v", prev, t.ID, err)
			return
		}
		t.Status = prev
	}
}
