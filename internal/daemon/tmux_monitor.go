package daemon

import (
	"fmt"
	"log"
	"time"

	"github.com/Bakaface/sortie/internal/task"
	"github.com/Bakaface/sortie/internal/tmux"
	"github.com/Bakaface/sortie/internal/workflow"
)

func (s *Server) tmuxMonitorLoop() {
	defer s.wg.Done()

	cfg := tmux.DefaultMonitorConfig()
	monitor := tmux.NewMonitor(cfg)

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.checkTmuxActivity(monitor)
		}
	}
}

func (s *Server) checkTmuxActivity(monitor *tmux.Monitor) {
	tasks, err := s.database.GetAllTasks()
	if err != nil {
		return
	}

	// Collect active tmux task IDs for cleanup
	activeSessions := make(map[string]bool)

	for _, t := range tasks {
		if t.Status != task.StatusTmux {
			continue
		}

		pc, err := s.getProjectContext(t.ProjectID)
		if err != nil {
			continue
		}

		taskID := fmt.Sprintf("%d", t.ID)
		session := tmux.NewSession(pc.cfg.Project.Name, taskID, t.WorktreePath)
		activeSessions[session.Name] = true

		if !session.Exists() {
			continue
		}

		activity, changed := monitor.Check(session)

		// Update activity broadcast / cache when it changed.
		if changed {
			activityStr := string(activity)
			log.Printf("%sTask #%d tmux activity changed to: %s", s.projectLogPrefix(t.ProjectID), t.ID, activityStr)

			s.mu.Lock()
			s.tmuxActivity[t.ID] = activityStr
			s.mu.Unlock()

			// Broadcast activity change
			s.broadcastToSubscribers(MsgTmuxActivity, TmuxActivityResponse{
				TaskID:   t.ID,
				Activity: activityStr,
			})

			// Also broadcast a task update so TUI gets the TmuxActivity field
			s.broadcastTaskUpdate(t.ID)
		}

		// Poll for the step-done sentinel every tick (not only on `changed`):
		// the sentinel can land while the pane is sitting idle with no further
		// repaints, so an activity-change-gated check would miss it.
		s.maybeAutoAdvance(t)
	}

	// Cleanup: remove sessions / state for tasks that are no longer in tmux mode.
	s.mu.Lock()
	for taskID := range s.tmuxActivity {
		found := false
		for _, t := range tasks {
			if t.ID == taskID && t.Status == task.StatusTmux {
				found = true
				break
			}
		}
		if !found {
			delete(s.tmuxActivity, taskID)
			delete(s.tmuxAutoState, taskID)
		}
	}
	s.mu.Unlock()

	// Remove stale sessions from monitor
	for name := range monitor.Sessions() {
		if !activeSessions[name] {
			monitor.Remove(name)
		}
	}
}

// maybeAutoAdvance inspects a tmux-state task and triggers auto-advance only
// when a step-done sentinel file has appeared in <worktree>/.sortie/step-done/
// for the just-finished step.
//
// The sentinel-file convention is the SOLE auto-advance signal. Sortie does not
// infer completion from terminal state: an idle pane is indistinguishable from
// an agent that never started, is waiting for input, or is stalled mid-turn, so
// using idleness as a trigger advances the workflow with empty context (see
// .docs/context/auto-advance-sentinel-convention.md). Whatever runs inside the
// tmux session is responsible for creating the sentinel when the work is done
// (e.g. a Claude Code Stop hook, or the agent itself as its final action).
//
// Tasks whose just-finished step is marked `human: true` are never
// auto-advanced — they pause at the approval gate until the user acts. The
// sentinel is still consumed (and discarded) so it doesn't leak across the
// next attach/continue.
func (s *Server) maybeAutoAdvance(t *task.Task) {
	// Don't double-advance if a prior tick already kicked off the engine.
	s.mu.RLock()
	entry, hasEntry := s.tmuxAutoState[t.ID]
	if hasEntry && entry.advancing {
		s.mu.RUnlock()
		return
	}
	s.mu.RUnlock()

	pc, err := s.getProjectContext(t.ProjectID)
	if err != nil {
		return
	}

	// Figure out which step just finished: the one the task is paused on.
	// See workflow.PausedStep for the cursor invariant.
	wf := pc.cfg.GetWorkflow(t.Workflow)
	justFinished, ok := workflow.PausedStep(t, wf)
	if !ok {
		return
	}

	// Capture the session id from the sentinel payload (optional session_id
	// field) before anything consumes it — this is the only session-discovery
	// mechanism, feeding chat_log_command lookup and resume_command restore.
	// Done for human steps too — they summarize their chat on user finalize —
	// and is idempotent across the multiple turn-end sentinels a single
	// session emits.
	s.captureSentinelSession(pc, t, justFinished.Name)

	// Steps that the user explicitly wants to approve are out of scope for
	// auto-advance. Consume any stray sentinel so it doesn't trigger advance
	// the next time the user attaches an interactive session.
	if justFinished.Human {
		workflow.ClearStepSentinels(t.WorktreePath, justFinished.Name)
		return
	}

	// Sole signal: presence of a sentinel file for THIS step (scoped by step
	// name so a stale sentinel from a different step in the same worktree can't
	// trigger a premature advance).
	if !workflow.StepSentinelExists(t.WorktreePath, justFinished.Name) {
		return
	}

	// Mark advancing before any side-effects so the next tick is a no-op.
	s.mu.Lock()
	if entry == nil {
		entry = &tmuxAutoEntry{}
		s.tmuxAutoState[t.ID] = entry
	}
	entry.advancing = true
	s.mu.Unlock()

	// Sentinel files have served their purpose — clear them so they don't
	// resurrect advance attempts after a manual finalize/retry.
	workflow.ClearStepSentinels(t.WorktreePath, justFinished.Name)

	log.Printf("%sTask #%d: auto-advancing via step-done sentinel (step %q done)",
		s.projectLogPrefix(t.ProjectID), t.ID, justFinished.Name)

	outcome, err := s.advanceTmuxTask(t)
	if err != nil {
		log.Printf("%sTask #%d: auto-advance failed: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
		// Reset advancing so a future tick can retry. advanceTmuxTask rolls
		// the DB status back on error — except when an agent is already
		// running the task, in which case the task has left StatusTmux and
		// the monitor's cleanup pass will drop this entry anyway.
		s.mu.Lock()
		if e, ok := s.tmuxAutoState[t.ID]; ok {
			e.advancing = false
		}
		s.mu.Unlock()
		return
	}
	log.Printf("%sTask #%d: auto-advance result: %s", s.projectLogPrefix(t.ProjectID), t.ID, outcome.message)
}

// captureSentinelSession records the agent session id for the just-finished
// tmux step from its turn-end sentinel payload, if one is present. Delegates
// to workflow.Engine.RecordTmuxStepSentinelSession (see
// internal/workflow/stepcontext.go) — the session id gates which chat log
// summarize_chat capture reads later and which session a restore resumes, so
// this is part of the step-context lifecycle the Engine owns even though it
// never touches task_steps.context directly.
func (s *Server) captureSentinelSession(pc *projectContext, t *task.Task, stepName string) {
	pc.engine.RecordTmuxStepSentinelSession(t, stepName)
}
