package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Bakaface/sortie/internal/agent"
	"github.com/Bakaface/sortie/internal/claude"
	gitpkg "github.com/Bakaface/sortie/internal/git"
	"github.com/Bakaface/sortie/internal/task"
	"github.com/Bakaface/sortie/internal/tmux"
	"github.com/Bakaface/sortie/internal/workflow"
)

func (s *Server) handleContinueTask(conn net.Conn, req ContinueTaskRequest) {
	t, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get task: %v", err))
		return
	}

	if t.Status == task.StatusAwaitingApproval || t.Status == task.StatusTmux {
		s.continuePausedTask(conn, t)
		return
	}

	if !t.Status.IsTerminal() {
		s.sendError(conn, fmt.Sprintf("task is not in a continuable state (status: %s)", t.Status))
		return
	}

	// Terminal tasks (completed/failed that made it here) - workflow selected by user
	if req.Workflow != "" {
		// User selected a workflow — reset task to run through it
		if err := s.database.ResetTaskForContinue(t.ID, req.Workflow, req.Prompt); err != nil {
			s.sendError(conn, fmt.Sprintf("failed to reset task for continue: %v", err))
			return
		}
		log.Printf("%sTask #%d continuing with workflow %q", s.projectLogPrefix(t.ProjectID), t.ID, req.Workflow)
		s.respondWithContinueTask(conn, req.TaskID)
		return
	}

	// Fallback: no workflow specified — use tmux (legacy behavior)
	if t.Status == task.StatusFailed {
		if err := s.database.ResetTaskForRetryFromStep(req.TaskID); err != nil {
			s.sendError(conn, fmt.Sprintf("failed to reset task: %v", err))
			return
		}
		log.Printf("%sTask #%d retrying from step %d", s.projectLogPrefix(t.ProjectID), t.ID, t.StepIndex)
		s.respondWithContinueTask(conn, req.TaskID)
		return
	}

	if !tmux.IsAvailable() {
		s.sendError(conn, "tmux is not installed or not in PATH")
		return
	}

	pc, err := s.getProjectContext(t.ProjectID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get project context: %v", err))
		return
	}

	if !t.Worktree {
		// No-worktree mode: run in project root
		t.WorktreePath = pc.repoRoot
	} else if err := s.ensureWorktree(t, pc); err != nil {
		s.sendError(conn, err.Error())
		return
	}

	var claudeMD strings.Builder
	fmt.Fprintf(&claudeMD, "# Continue Task #%d: %s\n\n", t.ID, t.Title)
	claudeMD.WriteString("You are continuing work on a previously completed task.\n\n")
	claudeMD.WriteString("## Task Description\n\n")
	claudeMD.WriteString(t.Input)
	claudeMD.WriteString("\n\n")
	if t.Context != "" {
		claudeMD.WriteString("## Previous Context\n\n")
		claudeMD.WriteString(t.Context)
		claudeMD.WriteString("\n\n")
	}
	claudeMD.WriteString("The user wants to continue working on this task. Help them with whatever they need.\n")

	claudeMDPath := filepath.Join(t.WorktreePath, "CLAUDE.md")
	if err := os.WriteFile(claudeMDPath, []byte(claudeMD.String()), 0644); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to write CLAUDE.md: %v", err))
		return
	}

	taskID := fmt.Sprintf("%d", t.ID)
	session := tmux.NewSession(pc.cfg.Project.Name, taskID, t.WorktreePath)

	if session.Exists() {
		session.Kill()
	}

	sortieDir := filepath.Join(t.WorktreePath, ".sortie")
	if err := os.MkdirAll(sortieDir, 0755); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to create sortie dir: %v", err))
		return
	}
	scriptFile := filepath.Join(sortieDir, "run-continue.sh")

	if err := writeClaudeScript(scriptFile, pc.cfg.Claude.Command, pc.cfg.Claude.Yolo, "", "", pc.cfg.Claude.DefaultArgs); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to write wrapper script: %v", err))
		return
	}

	// Snapshot pre-existing Claude sessions in this workdir BEFORE spawning so
	// the async session-ID poller below can distinguish the one we are about to
	// launch from any unrelated chat the user already has open in the same
	// directory.
	preExistingSessions := claude.SnapshotSessionsByWorkdir(t.WorktreePath)

	setupCmd := pc.cfg.TmuxSetupCommand
	if tmux.SetupCommandControlsAgent(setupCmd) {
		if err := session.Create(""); err != nil {
			s.sendError(conn, fmt.Sprintf("failed to create tmux session: %v", err))
			return
		}
	} else {
		if err := session.Create("bash", scriptFile); err != nil {
			s.sendError(conn, fmt.Sprintf("failed to create tmux session: %v", err))
			return
		}
	}

	// Run tmux setup command if configured
	if setupCmd != "" {
		vars := &tmux.SetupVars{
			ClaudeCommand: buildClaudeCommand(pc.cfg.Claude.Command, pc.cfg.Claude.Yolo, "", "", pc.cfg.Claude.DefaultArgs),
			RunAgent:      scriptFile,
		}
		if err := session.RunSetupCommand(setupCmd, vars); err != nil {
			log.Printf("%sWarning: tmux setup command failed for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
		}
	}

	// Async: discover the freshly-spawned Claude session ID and record it.
	// Filtering against preExistingSessions prevents locking onto an unrelated
	// pre-existing chat in the same worktree.
	go func() {
		sid, _ := claude.FindNewSessionByWorkdir(t.WorktreePath, preExistingSessions, 15*time.Second)
		if sid != "" {
			if err := s.database.UpsertChat(t.ID, "continue", sid, session.Name); err != nil {
				log.Printf("%sWarning: failed to upsert chat for continue task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
			}
		}
	}()

	if err := s.database.UpdateTaskStatus(t.ID, task.StatusTmux); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to update task status: %v", err))
		return
	}

	log.Printf("%sContinue session started for task #%d (tmux: %s)", s.projectLogPrefix(t.ProjectID), t.ID, session.Name)
	s.respondWithContinueTask(conn, req.TaskID)
}

// continuePausedTask resumes a task paused at an approval gate (awaiting
// approval or tmux): kills the tmux session and restarts the workflow agent.
// Serialized per task via taskFlowLock and re-validated under the lock so a
// concurrent advance/continue can't double-start the agent or clobber each
// other's status writes.
func (s *Server) continuePausedTask(conn net.Conn, t *task.Task) {
	mu := s.taskFlowLock(t.ID)
	mu.Lock()
	defer mu.Unlock()

	// Re-read under the lock: the caller's snapshot may predate a concurrent
	// transition (another continue/advance may already have resumed the task).
	t, err := s.database.GetTask(t.ID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to reload task: %v", err))
		return
	}
	if t.Status != task.StatusAwaitingApproval && t.Status != task.StatusTmux {
		s.sendError(conn, fmt.Sprintf("task is no longer paused (status: %s)", t.Status))
		return
	}

	agentID := fmt.Sprintf("%d", t.ID)
	pc, pcErr := s.getProjectContext(t.ProjectID)
	if pcErr == nil {
		if err := tmux.KillSessionsForTask(pc.cfg.Project.Name, agentID); err != nil {
			log.Printf("%sWarning: failed to kill tmux sessions for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
		}
	}

	// Ensure worktree exists for worktree tasks before continuing.
	// The worktree may have been cleaned up after a previous completion/merge.
	if pcErr == nil {
		if err := s.ensureWorktree(t, pc); err != nil {
			s.sendError(conn, err.Error())
			return
		}
	}

	origStatus := t.Status

	if err := s.database.UpdateTaskStatus(t.ID, task.StatusRunning); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to update task status: %v", err))
		return
	}
	// Keep the in-memory struct in sync: the engine restores t.Status into
	// the DB after each step summarization, so a stale pause status here
	// would be written back while the task is running.
	t.Status = task.StatusRunning

	if err := s.startTaskAgent(t); err != nil {
		if errors.Is(err, agent.ErrTaskAlreadyTracked) {
			// An agent is already running this task, so the StatusRunning we
			// just wrote is accurate. Rolling back to the pause status here
			// would mark a running task as paused, and its completion would
			// then be misread as an approval pause (task #220, 2026-07-13).
			log.Printf("%sContinue for task #%d found an agent already tracked; leaving status running", s.projectLogPrefix(t.ProjectID), t.ID)
		} else if rbErr := s.database.UpdateTaskStatus(t.ID, origStatus); rbErr != nil {
			log.Printf("%sWarning: failed to roll back task #%d status to %s: %v", s.projectLogPrefix(t.ProjectID), t.ID, origStatus, rbErr)
		}
		log.Printf("%sContinue failed for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
		s.sendError(conn, fmt.Sprintf("failed to start agent: %v", err))
		return
	}

	s.respondWithContinueTask(conn, t.ID)
}

// respondWithContinueTask refreshes the task from the DB, broadcasts the
// update, and sends a ContinueTaskResponse to the caller. Used by every exit
// path of handleContinueTask so callers receive a fresh TaskInfo without an
// extra round trip.
func (s *Server) respondWithContinueTask(conn net.Conn, taskID int64) {
	refreshed, err := s.database.GetTask(taskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to load task after continue: %v", err))
		return
	}
	s.broadcastToSubscribers(MsgTaskUpdate, TaskUpdateResponse{Task: s.taskToInfo(refreshed)})
	s.sendMessage(conn, MsgContinueTask, ContinueTaskResponse{Task: s.taskToInfo(refreshed)})
}

func (s *Server) handleAdvanceTask(conn net.Conn, req AdvanceTaskRequest) {
	t, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get task: %v", err))
		return
	}

	outcome, err := s.advanceTmuxTask(t)
	if err != nil {
		// Log daemon-side too: sendError only reaches the requesting client,
		// and a failed advance with no trace in the daemon log made the #220
		// incident (2026-07-13) nearly impossible to reconstruct.
		log.Printf("%sAdvance failed for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
		s.sendError(conn, err.Error())
		return
	}
	s.sendMessage(conn, MsgOK, OKResponse{Message: outcome.message})
}

// tmuxAdvanceOutcome describes the action taken by advanceTmuxTask. The
// `message` field is used as the OKResponse body for socket callers and as
// a log line for the auto-advance path. The `advanced` flag distinguishes
// "moved to next step" from "fully finalized".
type tmuxAdvanceOutcome struct {
	advanced bool
	message  string
}

// advanceTmuxTask kills the task's tmux session and either resumes the engine
// (when more workflow steps remain) or kicks off the full finalization
// pipeline (merge + summarizer + cleanup). It is the shared implementation for
// both the user-driven AdvanceTask handler and the daemon's auto-advance
// pathway triggered by the Claude Stop hook sentinel.
//
// Concurrent advance/continue attempts for the same task are serialized via
// taskFlowLock, and the task is re-read from the DB under the lock, so callers
// may pass a stale snapshot; only t.ID is trusted. The caller surfaces the
// returned message to whoever requested the advance.
func (s *Server) advanceTmuxTask(t *task.Task) (tmuxAdvanceOutcome, error) {
	mu := s.taskFlowLock(t.ID)
	mu.Lock()
	defer mu.Unlock()

	// Re-read under the lock: two requests racing here used to both observe
	// StatusTmux and double-start the agent — the loser's error path then
	// rolled the status back to tmux while the winner's agent was running,
	// stranding the task at a finished step (task #220, 2026-07-13).
	t, err := s.database.GetTask(t.ID)
	if err != nil {
		return tmuxAdvanceOutcome{}, fmt.Errorf("failed to reload task: %v", err)
	}
	if t.Status != task.StatusTmux {
		return tmuxAdvanceOutcome{}, fmt.Errorf("task is not in tmux state (status: %s)", t.Status)
	}

	pc, err := s.getProjectContext(t.ProjectID)
	if err != nil {
		return tmuxAdvanceOutcome{}, fmt.Errorf("failed to get project context: %v", err)
	}

	// Kill tmux sessions
	agentID := fmt.Sprintf("%d", t.ID)
	if err := tmux.KillSessionsForTask(pc.cfg.Project.Name, agentID); err != nil {
		log.Printf("%sWarning: failed to kill tmux sessions for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
	}

	// Determine whether the workflow has more steps to run after the one the
	// task is paused on. See workflow.HasMoreSteps for the cursor invariant.
	wf := pc.cfg.GetWorkflow(t.Workflow)
	hasMoreSteps := workflow.HasMoreSteps(t, wf)

	if hasMoreSteps {
		// Advance to the next step: resume the engine. ResumeAfterApproval will
		// run summarise_chat for the just-completed tmux step and then continue
		// with the remaining workflow steps.
		origStatus := t.Status
		if err := s.database.UpdateTaskStatus(t.ID, task.StatusRunning); err != nil {
			return tmuxAdvanceOutcome{}, fmt.Errorf("failed to update task status: %v", err)
		}
		// Keep the in-memory struct in sync: ResumeAfterApproval summarizes
		// the just-finished tmux step, and markSummarizingStep restores
		// t.Status into the DB afterwards — a stale "tmux" here would be
		// written back while the task is running.
		t.Status = task.StatusRunning

		engine := pc.engine
		manager := s.manager
		runner := func(ctx context.Context) error {
			var outputFn func([]string)
			if a, ok := manager.GetAgentByTaskID(t.ID); ok {
				outputFn = a.AppendOutput
			}
			return engine.ResumeAfterApproval(ctx, t, outputFn)
		}
		if _, err := s.manager.StartAgent(t, t.WorktreePath, runner); err != nil {
			if errors.Is(err, agent.ErrTaskAlreadyTracked) {
				// An agent is already running this task, so the StatusRunning
				// we just wrote is accurate. Rolling back to tmux here would
				// mark a running task as paused, and its completion would then
				// be misread as an approval pause (task #220, 2026-07-13).
				log.Printf("%sAdvance for task #%d found an agent already tracked; leaving status running", s.projectLogPrefix(t.ProjectID), t.ID)
			} else if rbErr := s.database.UpdateTaskStatus(t.ID, origStatus); rbErr != nil {
				log.Printf("%sWarning: failed to roll back task #%d status to %s: %v", s.projectLogPrefix(t.ProjectID), t.ID, origStatus, rbErr)
			}
			return tmuxAdvanceOutcome{}, fmt.Errorf("failed to start agent: %w", err)
		}

		s.broadcastTaskUpdate(t.ID)
		log.Printf("%sTask #%d: tmux step done, advancing to step %d of %d", s.projectLogPrefix(t.ProjectID), t.ID, t.StepIndex, len(wf.Steps))
		return tmuxAdvanceOutcome{advanced: true, message: fmt.Sprintf("task #%d advancing to next step", t.ID)}, nil
	}

	// Last step: run full finalization (on_complete merge + summarizer + cleanup).

	// Fast-track: if no meaningful changes were made, skip full finalization
	// and go straight to completed, cleaning up worktree and branch. See
	// maybeFastTrackCompletion (broadcast.go) for the shared decision + side
	// effects — this is the tmux-advance twin of finalizeCompletedTask's use
	// of the same helper.
	if s.maybeFastTrackCompletion(pc, t) {
		return tmuxAdvanceOutcome{message: fmt.Sprintf("task #%d fast-tracked to completed (no changes)", t.ID)}, nil
	}

	// Set finalizing status while we run summarizer + on_complete
	if err := s.database.UpdateTaskStatus(t.ID, task.StatusFinalizing); err != nil {
		log.Printf("%sWarning: failed to set finalizing status for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
	}
	s.broadcastTaskUpdate(t.ID)

	// Run summarizer + on_complete asynchronously so the caller doesn't block.
	go s.runFinalization(t, pc)
	return tmuxAdvanceOutcome{message: fmt.Sprintf("task #%d finalizing", t.ID)}, nil
}

func (s *Server) runFinalization(t *task.Task, pc *projectContext) {
	if err := pc.engine.FinalizeTask(s.ctx, t); err != nil {
		// A required-context capture failure blocks the task: fail it instead
		// of merging/completing with an empty step context. Preserve the
		// worktree and branch so the user can inspect and retry.
		if errors.Is(err, workflow.ErrStepContextRequired) {
			log.Printf("%sBlocking task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
			if dbErr := s.database.UpdateTaskError(t.ID, err.Error()); dbErr != nil {
				log.Printf("%sError: failed to mark task #%d failed: %v", s.projectLogPrefix(t.ProjectID), t.ID, dbErr)
			}
			s.broadcastTaskUpdate(t.ID)
			return
		}
		// The branch never landed, so this is not `completed`. Keep the worktree
		// and branch: they still hold the unmerged work a retry/continue needs.
		log.Printf("%sError: finalize failed for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
		if dbErr := s.database.UpdateTaskStatusError(t.ID, task.StatusMergeFailed, err.Error()); dbErr != nil {
			log.Printf("%sError: failed to mark task #%d merge-failed: %v", s.projectLogPrefix(t.ProjectID), t.ID, dbErr)
		}
		s.broadcastTaskUpdate(t.ID)
		return
	}

	// Mark task as completed
	if err := s.database.UpdateTaskStatus(t.ID, task.StatusCompleted); err != nil {
		log.Printf("%sError: failed to mark task #%d as completed: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
		return
	}

	s.broadcastTaskUpdate(t.ID)
	log.Printf("%sTask #%d finalized from tmux continue session", s.projectLogPrefix(t.ProjectID), t.ID)
}

// ensureWorktree ensures a task's worktree exists, creating it if needed —
// e.g. because it was cleaned up (cleanupWorktreeAndBranch) since it was last
// recorded and the task is now being resumed. The create/checkout + branch
// resolution + DB persistence sequence lives once in
// workflow.Engine.EnsureWorktree (shared with Engine.RunTask's first-run
// provisioning); this wrapper only adds the daemon-specific setup-command
// run, which — unlike RunTask — fires ONLY when EnsureWorktree actually
// (re)created the worktree (checkDisk=true below), not on every call, and
// deliberately does not run worktree-sync-paths or resolve a workflow-level
// setup-command override (nil workflow): both are pre-existing daemon
// behaviors preserved as-is by this refactor, not something this phase
// changed. Returns an error if worktree creation fails.
func (s *Server) ensureWorktree(t *task.Task, pc *projectContext) error {
	_, provisioned, err := pc.engine.EnsureWorktree(t, true)
	if err != nil {
		return fmt.Errorf("failed to ensure worktree for task #%d: %v", t.ID, err)
	}
	if !provisioned {
		return nil
	}
	log.Printf("%sRecreated worktree for task #%d at %s", s.projectLogPrefix(t.ProjectID), t.ID, t.WorktreePath)

	if setupCmd := pc.cfg.GetWorktreeSetupCommand(nil); setupCmd != "" {
		if err := workflow.RunWorktreeSetupCommand(context.Background(), pc.repoRoot, t.WorktreePath, setupCmd); err != nil {
			log.Printf("%sWarning: worktree setup command failed for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
		}
	}
	if setupCmds := pc.cfg.GetWorktreeSetupCommands(nil); len(setupCmds) > 0 {
		if err := workflow.RunWorktreeSetupCommands(context.Background(), pc.repoRoot, t.WorktreePath, setupCmds); err != nil {
			log.Printf("%sWarning: worktree setup commands failed for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
		}
	}

	return nil
}

// cleanupWorktreeAndBranch removes the worktree and branch for a task while
// holding the per-repo merge serializer (so it cannot race with an in-progress
// merge). It logs warnings on errors rather than returning them, since cleanup
// is best-effort.
func (s *Server) cleanupWorktreeAndBranch(pc *projectContext, t *task.Task) {
	pc.engine.Coord().Lock().WithLock(func() {
		if t.WorktreePath != "" {
			if err := pc.repo.RemoveWorktree(t.WorktreePath); err != nil {
				log.Printf("%sWarning: failed to remove worktree for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
			}
			pc.repo.CleanupWorktrees()
		}
		// Only delete branches that sortie created; preserve user-provided branches
		if t.Branch != "" && t.CheckoutBranch == "" {
			if err := pc.repo.ForceDeleteBranch(t.Branch); err != nil {
				log.Printf("%sWarning: failed to delete branch for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
			}
		}
		if t.WorktreePath != "" {
			if err := s.database.ClearWorktreePath(t.ID); err != nil {
				log.Printf("%sWarning: failed to clear worktree path for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
			}
		}
	})
}

// setupTmuxDirect creates a tmux session for a task that should go directly into tmux state,
// skipping the normal workflow. Used for branch tasks created via the "b" keybind and for
// MCP create_task with tmux_direct=true. When the task has a non-empty description, it is
// passed to claude as the initial prompt so the user lands in a session that's already
// thinking about the request.
func (s *Server) setupTmuxDirect(taskID, projectID int64, title string) {
	slug := task.Slugify(title)

	t, err := s.database.GetTask(taskID)
	if err != nil {
		log.Printf("%sFailed to get task #%d for tmux-direct: %v", s.projectLogPrefix(projectID), taskID, err)
		return
	}

	pc, err := s.getProjectContext(projectID)
	if err != nil {
		log.Printf("%sFailed to get project context for tmux-direct task #%d: %v", s.projectLogPrefix(projectID), taskID, err)
		return
	}

	// Resolve branch
	branch := t.CheckoutBranch

	if err := s.database.FinalizeTaskIdentity(taskID, title, slug, branch); err != nil {
		log.Printf("%sFailed to finalize identity for tmux-direct task #%d: %v", s.projectLogPrefix(projectID), taskID, err)
		return
	}

	// Ensure worktree exists
	if err := s.ensureWorktree(t, pc); err != nil {
		log.Printf("%sFailed to ensure worktree for tmux-direct task #%d: %v", s.projectLogPrefix(projectID), taskID, err)
		return
	}

	if !tmux.IsAvailable() {
		log.Printf("%sTmux not available for tmux-direct task #%d", s.projectLogPrefix(projectID), taskID)
		return
	}

	// Create tmux session
	taskIDStr := fmt.Sprintf("%d", taskID)
	session := tmux.NewSession(pc.cfg.Project.Name, taskIDStr, t.WorktreePath)

	if session.Exists() {
		session.Kill()
	}

	sortieDir := filepath.Join(t.WorktreePath, ".sortie")
	if err := os.MkdirAll(sortieDir, 0755); err != nil {
		log.Printf("%sFailed to create sortie dir for tmux-direct task #%d: %v", s.projectLogPrefix(projectID), taskID, err)
		return
	}
	scriptFile := filepath.Join(sortieDir, "run-continue.sh")

	initialPrompt := strings.TrimSpace(t.Input)
	if err := writeClaudeScript(scriptFile, pc.cfg.Claude.Command, pc.cfg.Claude.Yolo, "", initialPrompt, pc.cfg.Claude.DefaultArgs); err != nil {
		log.Printf("%sFailed to write claude script for tmux-direct task #%d: %v", s.projectLogPrefix(projectID), taskID, err)
		return
	}

	// Snapshot pre-existing Claude sessions in this workdir BEFORE spawning so
	// the async session-ID poller below can distinguish the one we are about to
	// launch from any unrelated chat the user already has open in the same
	// directory.
	preExistingSessions := claude.SnapshotSessionsByWorkdir(t.WorktreePath)

	setupCmd := pc.cfg.TmuxSetupCommand
	if tmux.SetupCommandControlsAgent(setupCmd) {
		if err := session.Create(""); err != nil {
			log.Printf("%sFailed to create tmux session for tmux-direct task #%d: %v", s.projectLogPrefix(projectID), taskID, err)
			return
		}
	} else {
		if err := session.Create("bash", scriptFile); err != nil {
			log.Printf("%sFailed to create tmux session for tmux-direct task #%d: %v", s.projectLogPrefix(projectID), taskID, err)
			return
		}
	}

	// Run tmux setup command if configured
	if setupCmd != "" {
		vars := &tmux.SetupVars{
			ClaudeCommand: buildClaudeCommand(pc.cfg.Claude.Command, pc.cfg.Claude.Yolo, "", initialPrompt, pc.cfg.Claude.DefaultArgs),
			RunAgent:      scriptFile,
		}
		if err := session.RunSetupCommand(setupCmd, vars); err != nil {
			log.Printf("%sWarning: tmux setup command failed for tmux-direct task #%d: %v", s.projectLogPrefix(projectID), taskID, err)
		}
	}

	// Async: discover the freshly-spawned Claude session ID and record it.
	// Filtering against preExistingSessions prevents locking onto an unrelated
	// pre-existing chat in the same worktree.
	worktreePath := t.WorktreePath
	sessionName := session.Name
	go func() {
		sid, _ := claude.FindNewSessionByWorkdir(worktreePath, preExistingSessions, 15*time.Second)
		if sid != "" {
			if err := s.database.UpsertChat(taskID, "tmux-direct", sid, sessionName); err != nil {
				log.Printf("%sWarning: failed to upsert chat for tmux-direct task #%d: %v", s.projectLogPrefix(projectID), taskID, err)
			}
		}
	}()

	if err := s.database.UpdateTaskStatus(taskID, task.StatusTmux); err != nil {
		log.Printf("%sFailed to update status for tmux-direct task #%d: %v", s.projectLogPrefix(projectID), taskID, err)
		return
	}

	s.broadcastTaskUpdate(taskID)
	log.Printf("%sTmux-direct session started for task #%d (tmux: %s)", s.projectLogPrefix(projectID), taskID, session.Name)
}

func (s *Server) handleDetachBranch(conn net.Conn, req DetachBranchRequest) {
	t, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get task: %v", err))
		return
	}

	if !t.Worktree || t.WorktreePath == "" {
		s.sendError(conn, "task does not have a worktree")
		return
	}

	if t.Status == task.StatusRunning || t.Status == task.StatusInit || t.Status == task.StatusPending || t.Status == task.StatusFinalizing || t.Status == task.StatusSummarizing || t.Status == task.StatusSummarizingStep || t.Status == task.StatusResolvingConflicts {
		s.sendError(conn, fmt.Sprintf("cannot detach while agent is active (status: %s)", t.Status))
		return
	}

	if t.WorktreeDetached {
		s.sendError(conn, "worktree branch is already detached")
		return
	}

	if err := gitpkg.DetachWorktreeHead(t.WorktreePath); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to detach worktree HEAD: %v", err))
		return
	}

	if err := s.database.SetWorktreeDetached(t.ID, true); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to update detached state: %v", err))
		return
	}

	refreshed, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to load task after detach: %v", err))
		return
	}
	s.broadcastToSubscribers(MsgTaskUpdate, TaskUpdateResponse{Task: s.taskToInfo(refreshed)})
	s.sendMessage(conn, MsgDetachBranch, DetachBranchResponse{Task: s.taskToInfo(refreshed)})
}

func (s *Server) handleAttachBranch(conn net.Conn, req AttachBranchRequest) {
	t, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get task: %v", err))
		return
	}

	if !t.Worktree || t.WorktreePath == "" {
		s.sendError(conn, "task does not have a worktree")
		return
	}

	if !t.WorktreeDetached {
		s.sendError(conn, "worktree branch is not detached")
		return
	}

	// Auto-checkout the default branch on root repo before reattaching,
	// in case root is currently on the task's branch (which would prevent reattach)
	if repoRoot := s.getProjectRepoRoot(t); repoRoot != "" {
		repo := gitpkg.NewRepo(repoRoot)
		defaultBranch := repo.GetDefaultBranch()
		if err := repo.CheckoutBranch(defaultBranch); err != nil {
			log.Printf("%sWarning: failed to checkout %s on root before reattach: %v", s.projectLogPrefix(t.ProjectID), defaultBranch, err)
		}
	}

	if err := gitpkg.ReattachWorktreeBranch(t.WorktreePath, t.Branch); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to reattach branch: %v", err))
		return
	}

	if err := s.database.SetWorktreeDetached(t.ID, false); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to update detached state: %v", err))
		return
	}

	refreshed, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to load task after attach: %v", err))
		return
	}
	s.broadcastToSubscribers(MsgTaskUpdate, TaskUpdateResponse{Task: s.taskToInfo(refreshed)})
	s.sendMessage(conn, MsgAttachBranch, AttachBranchResponse{Task: s.taskToInfo(refreshed)})
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// writeClaudeScript writes a bash wrapper script that runs claude and drops to a shell.
// claudeBin is the configured Claude binary (cfg.Claude.Command); falls back to "claude".
// If resumeSessionID is non-empty, the script invokes `claude --resume <id>` to
// restore a previous chat session.
func writeClaudeScript(scriptPath string, claudeBin string, yolo bool, resumeSessionID string, initialPrompt string, defaultArgs []string) error {
	script := fmt.Sprintf("#!/bin/bash\n%s\nexec bash\n", buildClaudeCommand(claudeBin, yolo, resumeSessionID, initialPrompt, defaultArgs))
	return os.WriteFile(scriptPath, []byte(script), 0755)
}

// buildClaudeCommand assembles the `claude` CLI invocation with the appropriate
// flags. claudeBin is the configured Claude binary (cfg.Claude.Command); falls
// back to "claude". If resumeSessionID is non-empty, `--resume <id>` is appended
// so the chat is automatically restored. If initialPrompt is non-empty, it is
// appended as the positional prompt so Claude opens with that message as the
// user's first turn (mutually exclusive with --resume in practice; resume wins).
func buildClaudeCommand(claudeBin string, yolo bool, resumeSessionID string, initialPrompt string, defaultArgs []string) string {
	if claudeBin == "" {
		claudeBin = "claude"
	}
	cmd := fmt.Sprintf("%q", claudeBin)
	if yolo {
		cmd += " --dangerously-skip-permissions"
	}
	// Configured default_args (e.g. --plugin-dir for the sortie plugin) must be
	// reapplied on resume/restore/continue — otherwise a session that had
	// sortie's MCP tools loses them the moment it is resumed.
	for _, a := range defaultArgs {
		cmd += fmt.Sprintf(" %q", a)
	}
	if resumeSessionID != "" {
		cmd += " --resume " + resumeSessionID
	} else if initialPrompt != "" {
		cmd += " " + shellSingleQuote(initialPrompt)
	}
	return cmd
}

// shellSingleQuote wraps s in POSIX-safe single quotes. Single quotes inside
// the string are escaped via the standard close-escape-reopen sequence (the
// replacement string below) so the result is safe to drop into a bash command
// line.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
