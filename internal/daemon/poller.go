package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/Bakaface/sortie/internal/agent"
	"github.com/Bakaface/sortie/internal/config"
	gitpkg "github.com/Bakaface/sortie/internal/git"
	"github.com/Bakaface/sortie/internal/task"
	"github.com/Bakaface/sortie/internal/tmux"
	"github.com/Bakaface/sortie/internal/workflow"
)

func (s *Server) taskPollerLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()

	s.checkPendingTasks()
	s.checkSuspendedParents()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			// Promote suspended parents BEFORE the claimable scan so they
			// hit the same tick — without this, parents whose children all
			// completed during the previous poll interval would wait an
			// extra tick to resume.
			s.checkSuspendedParents()
			s.checkPendingTasks()
		}
	}
}

func (s *Server) checkPendingTasks() {
	tasks, err := s.database.GetClaimableTasks()
	if err != nil {
		log.Printf("Failed to get claimable tasks: %v", err)
		return
	}

	for _, t := range tasks {
		if s.manager.IsTaskKnown(t.ID) {
			continue
		}

		title := t.Title
		if title == "" {
			title = t.Input
		}
		log.Printf("%sStarting agent for task #%d: %s", s.projectLogPrefix(t.ProjectID), t.ID, title)
		if err := s.startTaskAgent(t); err != nil {
			log.Printf("%sFailed to start agent for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
		}
	}
}

// checkSuspendedParents promotes tasks in StatusAwaitingChildren back to
// StatusPending once every wait-on child has reached terminal status. The
// next checkPendingTasks tick re-enqueues the parent, the engine re-runs the
// same step (step_index is preserved), and loadAndClearChildren in the engine
// hydrates {{children.*}} from the still-attached wait-on edges before
// clearing them.
//
// The resume path is project-blind: the parent and its wait-on children may
// live in different projects. AllWaitsOnTerminal joins on task IDs only, and
// the subsequent checkPendingTasks tick re-claims the parent through the same
// global GetClaimableTasks scan, which has no project filter.
func (s *Server) checkSuspendedParents() {
	suspended, err := s.database.GetTasksAwaitingChildren()
	if err != nil {
		log.Printf("Failed to get awaiting-children tasks: %v", err)
		return
	}
	for _, t := range suspended {
		// Defensive: if the manager still tracks this task (e.g., agent
		// somehow didn't clean up), skip to avoid double-start.
		if s.manager.IsTaskKnown(t.ID) {
			continue
		}
		allDone, err := s.database.AllWaitsOnTerminal(t.ID)
		if err != nil {
			log.Printf("%sFailed to check waits-on terminal for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
			continue
		}
		if !allDone {
			continue
		}
		// Failure semantics: even if some children failed, the parent
		// resumes. `failed` is terminal, so this holds for a cross-project
		// child failure exactly as it does for a same-project one. The agent
		// inspects {{children.<id>.status}} on resume and decides whether to
		// fail, retry, or proceed — see loadAndClearChildren in
		// internal/workflow/engine.go for how those vars are hydrated.
		log.Printf("%sTask #%d: all waited-on children terminal, resuming at step %d", s.projectLogPrefix(t.ProjectID), t.ID, t.StepIndex)
		if err := s.database.UpdateTaskStatus(t.ID, task.StatusPending); err != nil {
			log.Printf("%sFailed to flip task #%d to pending for resume: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
			continue
		}
		// The next checkPendingTasks tick picks up the now-pending task and
		// calls startTaskAgent → engine.RunTask. The engine's wait-on probe
		// at step start (loadAndClearChildren) reads the still-attached
		// edges into the template and removes them, so the post-Claude
		// wait-on check sees only NEW edges added during the resumed step.
		s.broadcastTaskUpdate(t.ID)
	}
}

func (s *Server) startTaskAgent(t *task.Task) error {
	if t.Status == task.StatusPending {
		claimed, err := s.database.ClaimTask(t.ID)
		if err != nil {
			return fmt.Errorf("failed to claim task: %w", err)
		}
		if !claimed {
			return agent.ErrTaskAlreadyTracked
		}
		// Mirror the claim into the in-memory struct: the engine holds this
		// task for the whole run, and markSummarizingStep restores t.Status
		// into the DB after each step summarization — a stale "pending" here
		// would be written back mid-run.
		t.Status = task.StatusRunning
	}

	pc, err := s.getProjectContext(t.ProjectID)
	if err != nil {
		return fmt.Errorf("failed to get project context: %w", err)
	}

	workDir := t.WorktreePath
	if workDir == "" {
		workDir = pc.repoRoot
	}

	engine := pc.engine
	manager := s.manager
	runner := func(ctx context.Context) error {
		var outputFn func([]string)
		if a, ok := manager.GetAgentByTaskID(t.ID); ok {
			outputFn = a.AppendOutput
		}
		return engine.RunTask(ctx, t, outputFn)
	}

	if _, err := s.manager.StartAgent(t, workDir, runner); err != nil {
		return err
	}

	return nil
}

func (s *Server) recoverOrphanedTasks() error {
	allTasks, err := s.database.GetAllTasks()
	if err != nil {
		return err
	}

	// Clean repo state for any projects whose tasks may have been mid-mutation
	// when the daemon was killed. Running tasks could have been in any step;
	// finalizing / merge-blocked tasks were specifically inside the on_complete
	// pipeline and may have left staged conflict markers on the base branch.
	// The deferred CleanRepoState in merge.Coordinator only fires on Go-return,
	// so killed processes need this top-level sweep before any recovery agent
	// restarts touch the repo.
	cleanedRepos := make(map[string]bool)
	for _, t := range allTasks {
		if !t.Status.MayHaveDirtyRepoState() {
			continue
		}
		repoRoot := s.getProjectRepoRoot(t)
		if repoRoot == "" || cleanedRepos[repoRoot] {
			continue
		}
		cleanedRepos[repoRoot] = true
		repo := gitpkg.NewRepo(repoRoot)
		if dirty, err := repo.HasChanges(); err == nil && dirty {
			log.Printf("Cleaning up dirty repo state at %s from previous run", repoRoot)
			if err := repo.CleanRepoState(); err != nil {
				log.Printf("Warning: failed to clean repo state at %s: %v", repoRoot, err)
			}
		}
	}

	var tmuxTasksToRestore []*task.Task

	for _, t := range allTasks {
		prefix := s.projectLogPrefix(t.ProjectID)
		switch t.Status {
		case task.StatusRunning, task.StatusSummarizingStep:
			log.Printf("%sRecovering orphaned task #%d (status: %s), resetting to pending", prefix, t.ID, t.Status)
			if err := s.database.ResetTaskForRetry(t.ID); err != nil {
				log.Printf("%sFailed to reset task #%d: %v", prefix, t.ID, err)
			}
		case task.StatusInit:
			log.Printf("%sRecovering task #%d stuck in init, resetting to pending", prefix, t.ID)
			if err := s.database.UpdateTaskStatus(t.ID, task.StatusPending); err != nil {
				log.Printf("%sFailed to reset task #%d: %v", prefix, t.ID, err)
			}
		case task.StatusFinalizing:
			// Re-enter the finalization pipeline rather than demoting to tmux:
			// RunTask is a no-op when all steps are complete, and the agent
			// completion callback re-runs FinalizeTask so an interrupted merge
			// or conflict resolution retries automatically. The previous
			// behavior (demote to tmux) silently dropped the in-flight merge.
			log.Printf("%sRecovering task #%d stuck in finalizing, restarting agent to re-run merge/summarize", prefix, t.ID)
			if err := s.startTaskAgent(t); err != nil {
				log.Printf("%sFailed to restart finalizing task #%d: %v", prefix, t.ID, err)
			}
		case task.StatusSummarizing:
			// Same recovery path as Finalizing: re-enter the agent so the
			// completion callback re-runs FinalizeTask. ResetTaskForRetry
			// would wipe step_index and re-run the whole workflow from
			// scratch (including any tmux step) — wrong if the merge has
			// already happened.
			log.Printf("%sRecovering task #%d stuck in summarizing, restarting agent to re-run summarize", prefix, t.ID)
			if err := s.startTaskAgent(t); err != nil {
				log.Printf("%sFailed to restart summarizing task #%d: %v", prefix, t.ID, err)
			}
		case task.StatusMergeBlocked:
			log.Printf("%sRecovering merge-blocked task #%d, restarting merge agent", prefix, t.ID)
			if err := s.startTaskAgent(t); err != nil {
				log.Printf("%sFailed to restart merge-blocked task #%d: %v", prefix, t.ID, err)
			}
		case task.StatusResolvingConflicts:
			// Conflict resolution was interrupted mid-flight (the conflict
			// resolver was running when the daemon died). Re-enter the agent
			// so the completion callback re-runs FinalizeTask and the merge
			// pipeline retries — the worktree may have a half-applied merge,
			// which the next attempt's refreshFromBase will clean up.
			log.Printf("%sRecovering task #%d stuck in resolving-conflicts, restarting agent to re-run merge", prefix, t.ID)
			if err := s.startTaskAgent(t); err != nil {
				log.Printf("%sFailed to restart resolving-conflicts task #%d: %v", prefix, t.ID, err)
			}
		case task.StatusAwaitingApproval:
			log.Printf("%sTask #%d is awaiting approval (use 'continue' command)", prefix, t.ID)
		case task.StatusAwaitingChildren:
			// No-op recovery: the next checkSuspendedParents tick re-evaluates
			// children terminal status and resumes if appropriate. The
			// task_waits_on edges survived the daemon restart, so the parent
			// will pick up exactly where it left off.
			waits, _ := s.database.GetTaskWaitsOn(t.ID)
			log.Printf("%sTask #%d is awaiting %d children (will resume when all reach terminal status)", prefix, t.ID, len(waits))
		case task.StatusTmux:
			tmuxTasksToRestore = append(tmuxTasksToRestore, t)
		}
	}

	// Restore tmux sessions for tasks that had active tmux sessions before daemon restart
	if len(tmuxTasksToRestore) > 0 && tmux.IsAvailable() {
		for _, t := range tmuxTasksToRestore {
			prefix := s.projectLogPrefix(t.ProjectID)
			resumed, err := s.restoreTmuxSession(t)
			if err != nil {
				log.Printf("%sFailed to restore tmux session for task #%d: %v", prefix, t.ID, err)
			} else if resumed {
				log.Printf("%sRestored tmux session for task #%d (auto-resumed previous agent session)", prefix, t.ID)
			} else {
				log.Printf("%sRestored tmux session for task #%d (no resumable agent session; started fresh)", prefix, t.ID)
			}
		}
	}

	return nil
}

// restoreTmuxSession recreates a tmux session for a task whose session was
// lost during a daemon restart. The agent is re-resolved from the CURRENT
// config: the paused step's agent when the task sits on a workflow step, else
// the interactive fallback agent. When a previous agent session id is
// recorded in the chats table AND the agent defines a resume_command, the
// session is resumed; otherwise a fresh session starts (with the still-on-disk
// step prompt for step restores).
// Returns (resumed, error) where resumed indicates whether a prior session
// was found and resumed.
func (s *Server) restoreTmuxSession(t *task.Task) (bool, error) {
	pc, _ := s.getProjectContext(t.ProjectID)
	projectName := ""
	if pc != nil {
		projectName = pc.cfg.Project.Name
	}

	// Non-worktree tasks may have empty WorktreePath in older DB rows
	// (the engine used to set it in-memory only). Fall back to repo root,
	// matching startTaskAgent's fallback behavior.
	if t.WorktreePath == "" && pc != nil && pc.repoRoot != "" {
		t.WorktreePath = pc.repoRoot
	}
	if t.WorktreePath == "" || !dirExists(t.WorktreePath) {
		return false, fmt.Errorf("worktree path does not exist: %s", t.WorktreePath)
	}

	session := tmux.NewSession(projectName, fmt.Sprintf("%d", t.ID), t.WorktreePath)
	if session.Exists() {
		log.Printf("%sTmux session already exists for task #%d, skipping restore", s.projectLogPrefix(t.ProjectID), t.ID)
		return false, nil
	}
	if pc == nil {
		return false, fmt.Errorf("no project context for task #%d", t.ID)
	}

	// Resolve the agent from the current config. A task paused on a workflow
	// step restores that step's agent; ad-hoc sessions (continue/tmux-direct)
	// restore the interactive fallback.
	label := "continue"
	var slug string
	var agentCfg config.AgentConfig
	wf := pc.cfg.GetWorkflow(t.Workflow)
	if step, ok := workflow.PausedStep(t, wf); ok && pc.cfg.StepIsTmux(wf, &step) {
		var agentErr error
		slug, agentCfg, agentErr = pc.cfg.StepAgent(wf, &step)
		if agentErr != nil {
			return false, agentErr
		}
		label = step.Name
	} else {
		var agentErr error
		slug, agentCfg, agentErr = interactiveAgent(pc.cfg)
		if agentErr != nil {
			return false, agentErr
		}
	}

	// Look up the most recent recorded agent session for this task so we can
	// auto-resume it (only effective when the agent defines resume_command).
	// Failures here are non-fatal: we fall back to a fresh session.
	var resumeSessionID string
	if chat, err := s.database.GetLatestChat(t.ID); err != nil {
		log.Printf("%sWarning: failed to look up chat for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
	} else if chat != nil {
		resumeSessionID = chat.SessionID
	}

	// For step restores, re-feed the step prompt still on disk so a
	// non-resumed session at least starts with its instructions. Ad-hoc
	// restores start blank.
	prompt := ""
	if label != "continue" {
		if data, err := os.ReadFile(workflow.StepPromptFile(t.WorktreePath, label)); err == nil {
			prompt = string(data)
		}
	}

	return s.spawnInteractiveSession(pc, t, interactiveSpawnOpts{
		label:           label,
		prompt:          prompt,
		resumeSessionID: resumeSessionID,
		slug:            slug,
		agent:           agentCfg,
		keepSentinels:   true,
	})
}
