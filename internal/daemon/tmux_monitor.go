package daemon

import (
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/Bakaface/sakusen/internal/task"
	"github.com/Bakaface/sakusen/internal/tmux"
	"github.com/Bakaface/sakusen/internal/workflow"
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

	// liveTasks collects the tmux tasks whose session actually exists this
	// tick — the population the hibernation passes below consider, and the
	// count the live-session cap is measured against (across all projects).
	var liveTasks []*task.Task

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
		liveTasks = append(liveTasks, t)

		activity, changed := monitor.Check(session)

		// Update activity broadcast / cache when it changed.
		if changed {
			activityStr := string(activity)
			log.Printf("%sTask #%d tmux activity changed to: %s", s.projectLogPrefix(t.ProjectID), t.ID, activityStr)

			s.mu.Lock()
			previous := s.tmuxActivity[t.ID]
			s.tmuxActivity[t.ID] = activityStr
			entry := s.tmuxAutoEntryFor(t.ID)
			if activity == tmux.ActivityIdle {
				entry.idleSince = time.Now()
				if entry.atPromptOnIdle {
					entry.atPrompt, entry.atPromptOnIdle = true, false
				}
			} else {
				entry.idleSince = time.Time{}
				// An idle pane repainting again means whatever sentinel parked
				// this task at its prompt is stale: a turn is in flight (or
				// the pane was merely redrawn, which conservatively costs the
				// task its hibernation eligibility until the next turn ends).
				// The very first reading of a session is always wip — the
				// monitor starts from unknown — and is not such a transition.
				if previous == string(tmux.ActivityIdle) {
					entry.atPrompt = false
				}
			}
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

	s.hibernateIdleTmuxSessions(liveTasks)
}

// capMinIdle is how long a session must have sat idle before the soft cap
// may reclaim it. Without a floor the cap fired on the monitor's idle
// threshold alone (~12s of unchanged pane), so whenever the daemon sat over
// the cap every session was killed seconds after its user looked away — and
// a pane that went briefly static mid-turn was killed mid-turn.
const capMinIdle = 30 * time.Minute

// hibernateIdleTmuxSessions runs the two hibernation passes over the tmux
// tasks whose session was live this tick: first the idle threshold
// (tmux.hibernate_after), then the soft cap on live sessions
// (tmux.max_live_sessions). Both settings are read from the daemon's own
// merged config, exactly like max_workers — a per-project .sakusen.yml has no
// say over what the running daemon does — and either is disabled by setting
// it to 0.
//
// Both passes pre-filter on in-memory idle state before calling
// eligibleForHibernation, which shells out to tmux and hits the DB: on a
// 2-second tick, most tasks must cost nothing.
func (s *Server) hibernateIdleTmuxSessions(live []*task.Task) {
	hibernateAfter := s.cfg.TmuxHibernateAfter
	maxLive := s.cfg.TmuxMaxLiveSessions
	if len(live) == 0 || (hibernateAfter <= 0 && maxLive <= 0) {
		return
	}

	hibernated := make(map[int64]bool)
	now := time.Now()

	if hibernateAfter > 0 {
		for _, t := range live {
			idleSince, ok := s.tmuxIdleSince(t.ID)
			if !ok || now.Sub(idleSince) < hibernateAfter {
				continue
			}
			if !s.eligibleForHibernation(t) {
				continue
			}
			if s.hibernateTmuxTask(t, "idle") {
				hibernated[t.ID] = true
			}
		}
	}

	if maxLive <= 0 {
		return
	}
	liveCount := len(live) - len(hibernated)
	if liveCount <= maxLive {
		return
	}

	// Over the cap: reclaim oldest-idle-first among the eligible. Busy,
	// recently-idle (under capMinIdle), attached, mid-turn and non-resumable
	// sessions are simply skipped — the
	// cap is soft and must never interrupt work or block new tasks, so
	// staying over it is an acceptable outcome.
	type candidate struct {
		task      *task.Task
		idleSince time.Time
	}
	var candidates []candidate
	for _, t := range live {
		if hibernated[t.ID] {
			continue
		}
		if idleSince, ok := s.tmuxIdleSince(t.ID); ok && now.Sub(idleSince) >= capMinIdle {
			candidates = append(candidates, candidate{task: t, idleSince: idleSince})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].idleSince.Before(candidates[j].idleSince)
	})

	for _, c := range candidates {
		if liveCount <= maxLive {
			return
		}
		if !s.eligibleForHibernation(c.task) {
			continue
		}
		if s.hibernateTmuxTask(c.task, "cap") {
			liveCount--
		}
	}
}

// tmuxIdleSince returns when the monitor last saw a task's pane go idle.
// ok is false when the task has no recorded idle transition — it is busy, or
// the daemon restarted since it last went idle (the state is in-memory only,
// so a restart costs every session one fresh idle period).
func (s *Server) tmuxIdleSince(taskID int64) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.tmuxAutoState[taskID]
	if !ok || entry.idleSince.IsZero() {
		return time.Time{}, false
	}
	return entry.idleSince, true
}

// eligibleForHibernation reports whether a live tmux task's session may be
// killed right now. Every condition is a veto:
//   - not already hibernated;
//   - the monitor reports the pane idle, and a turn-end sentinel (or a resume)
//     parked the agent at its prompt — an idle pane alone cannot tell "waiting
//     for input" from "stalled mid-turn" (see maybeAutoAdvance);
//   - no advance is in flight for the task;
//   - no tmux client is attached — a user may be sitting in the session even
//     though nothing has repainted, and a failed query counts as attached;
//   - the session is genuinely resumable: a label-scoped chat row with a
//     session id plus an agent with a resume_command. Without that, killing
//     the session would throw the conversation away rather than park it.
func (s *Server) eligibleForHibernation(t *task.Task) bool {
	if t.HibernatedAt != nil {
		return false
	}

	s.mu.RLock()
	activity := s.tmuxActivity[t.ID]
	entry := s.tmuxAutoState[t.ID]
	var atPrompt, advancing bool
	if entry != nil {
		atPrompt, advancing = entry.atPrompt, entry.advancing
	}
	s.mu.RUnlock()

	if activity != string(tmux.ActivityIdle) || !atPrompt || advancing {
		return false
	}

	pc, err := s.getProjectContext(t.ProjectID)
	if err != nil {
		return false
	}
	target, err := s.resolveTmuxResume(pc, t)
	if err != nil || !target.scoped || !target.resumable() {
		return false
	}

	session := tmux.NewSession(pc.cfg.Project.Name, fmt.Sprintf("%d", t.ID), t.WorktreePath)
	attached, err := session.Attached()
	if err != nil {
		log.Printf("%sWarning: failed to check tmux clients for task #%d, not hibernating: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
		return false
	}
	return !attached
}

// hibernateTmuxTask kills a task's interactive session while leaving the task
// itself untouched: same status, same step cursor, same worktree and branch,
// and a chats row the wake path resumes from. Returns whether it hibernated.
//
// Serialized per task via taskFlowLock with a re-read under the lock, like
// every other lifecycle transition — a concurrent advance/continue must not
// find its session pulled out from under it.
func (s *Server) hibernateTmuxTask(t *task.Task, reason string) bool {
	taskID := t.ID
	mu := s.taskFlowLock(taskID)
	mu.Lock()
	defer mu.Unlock()

	// Re-read under the lock: the snapshot the monitor pass collected may
	// predate a concurrent advance/continue that took the task out of tmux.
	t, err := s.database.GetTask(taskID)
	if err != nil {
		log.Printf("Warning: failed to reload task #%d for hibernation: %v", taskID, err)
		return false
	}
	if t.Status != task.StatusTmux || t.HibernatedAt != nil {
		return false
	}

	pc, err := s.getProjectContext(t.ProjectID)
	if err != nil {
		return false
	}

	// Kill the task's EXACT session. tmux.KillSessionsForTask matches by
	// prefix, so hibernating task #1 would also kill #10-#19.
	session := tmux.NewSession(pc.cfg.Project.Name, fmt.Sprintf("%d", t.ID), t.WorktreePath)
	if err := session.Kill(); err != nil {
		log.Printf("%sWarning: failed to kill tmux session for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
		// Marking a task hibernated while its agent is still running would
		// hide a live session behind [zz]; a session that is simply gone
		// already is fine to mark, since wake restores it either way.
		if session.Exists() {
			return false
		}
	}

	// Drop the label's sentinels: a turn-end sentinel written as the agent
	// shut down would otherwise sit in the worktree and auto-advance the task
	// the moment it wakes.
	if target, err := s.resolveTmuxResume(pc, t); err == nil {
		workflow.ClearStepSentinels(t.WorktreePath, target.label)
	}

	now := time.Now()
	if err := s.database.SetTaskHibernatedAt(t.ID, &now); err != nil {
		log.Printf("%sWarning: failed to mark task #%d hibernated: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
		return false
	}

	s.mu.Lock()
	delete(s.tmuxActivity, t.ID)
	delete(s.tmuxAutoState, t.ID)
	s.mu.Unlock()

	s.broadcastTaskUpdate(t.ID)
	log.Printf("%sHibernated tmux session for task #%d (reason: %s)", s.projectLogPrefix(t.ProjectID), t.ID, reason)
	return true
}

// maybeAutoAdvance inspects a tmux-state task and triggers auto-advance only
// when a step-done sentinel file has appeared in <worktree>/.sakusen/step-done/
// for the just-finished step.
//
// The sentinel-file convention is the SOLE auto-advance signal. Sakusen does not
// infer completion from terminal state: an idle pane is indistinguishable from
// an agent that never started, is waiting for input, or is stalled mid-turn, so
// using idleness as a trigger advances the workflow with empty context (see
// .docs/context/auto-advance-sentinel-convention.md). Whatever runs inside the
// tmux session is responsible for creating the sentinel when the work is done
// (e.g. a Claude Code Stop hook, or the agent itself as its final action).
//
// Tasks whose just-finished step is marked `human: true` are never
// auto-advanced — they pause at the approval gate until the user acts.
// `advance: explicit` steps are never auto-advanced either; they wait for
// advance_task / the user, but unlike human steps they are not an approval
// gate. In both cases the sentinel is still consumed (and discarded) so it
// doesn't leak across the next attach/continue.
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
	wf := pc.cfg.GetTaskWorkflow(t.Workflow)
	justFinished, ok := workflow.PausedStep(t, wf)
	if !ok || !pc.cfg.StepIsTmux(wf, &justFinished) {
		// No workflow tmux step owns this session, so it is an ad-hoc one
		// (continue / tmux_direct) and nothing else ever reads its sentinels.
		s.captureAdHocSentinelSessions(pc, t)
		return
	}

	// Capture the session id from the sentinel payload (optional session_id
	// field) before anything consumes it — this is the only session-discovery
	// mechanism, feeding chat_log_command lookup and resume_command restore.
	// Done for human steps too — they summarize their chat on user finalize —
	// and is idempotent across the multiple turn-end sentinels a single
	// session emits.
	s.captureSentinelSession(pc, t, justFinished.Name)

	// A turn-end sentinel means the agent is parked at its prompt rather than
	// stalled mid-turn — the other half of hibernation eligibility. Recorded
	// for human steps too, which is where sessions idle longest.
	if workflow.StepSentinelExists(t.WorktreePath, justFinished.Name) {
		s.markTmuxAtPrompt(t.ID)
	}

	// Human (approval-gate) and explicit-advance steps are out of scope for
	// auto-advance. Consume any stray sentinel so it doesn't trigger advance
	// the next time the user attaches an interactive session.
	if justFinished.Human || justFinished.ExplicitAdvance() {
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

// captureAdHocSentinelSessions does for interactive sessions with no owning
// workflow step what captureSentinelSession + the human-step clear do for
// step sessions: record the session id from the latest turn-end sentinel
// under the session's own label, note that the agent is at its prompt, then
// consume the sentinel.
//
// Ad-hoc sessions never reach maybeAutoAdvance's step path, so without this
// their session id is never recorded — which would make them unresumable and
// therefore permanently ineligible for hibernation, even though continue and
// tmux_direct sessions are exactly the ones that sit idle for days.
func (s *Server) captureAdHocSentinelSessions(pc *projectContext, t *task.Task) {
	for _, label := range adHocSessionLabels {
		if !workflow.StepSentinelExists(t.WorktreePath, label) {
			continue
		}
		s.captureSentinelSession(pc, t, label)
		s.markTmuxAtPrompt(t.ID)
		workflow.ClearStepSentinels(t.WorktreePath, label)
	}
}
