package daemon

import (
	"fmt"
	"log"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
)

// periodicTickInterval is the scheduler's poll cadence. Periodic cadences are
// minute-granular (the standard cron parser enforces a 1-minute floor), so a
// 30s tick guarantees each due definition is observed within the same minute.
const periodicTickInterval = 30 * time.Second

// periodicSchedulerLoop reconciles periodic definitions from each project's
// .sakusen.yml into the DB and fires due definitions as ordinary tasks. Startup
// catch-up runs separately via checkPeriodicCatchup (called from Start), so the
// loop only handles the steady-state ticks here.
func (s *Server) periodicSchedulerLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(periodicTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.reconcileAllPeriodics()
			s.fireDuePeriodics()
		}
	}
}

// checkPeriodicCatchup runs the startup reconcile + a single due-fire pass.
// Because ClaimPeriodicFire advances next_fire_at to the next FUTURE slot, a
// definition that fell due during downtime fires at most once here, then its
// schedule is clamped forward — no backlog of make-up fires.
func (s *Server) checkPeriodicCatchup() {
	s.reconcileAllPeriodics()
	s.fireDuePeriodics()
}

// reconcileAllPeriodics walks every registered project, loading its config
// through the cached project context (honoring the lazy-load/live-reload
// invariant) and reconciling the periodic: section into the DB.
//
// Reconciliation is skipped for projects whose .sakusen.yml has not changed
// since the last successful reconcile (tracked by config mod-time, the same
// signal getProjectContext uses for cache invalidation). This keeps the steady
// state cheap: most 30s ticks do no DB writes at all and only fireDuePeriodics
// runs.
//
// Note: only projects already registered in the DB are walked — a project
// whose .sakusen.yml has a periodic: section but which has never been touched
// by any sakusen command does not fire. `sakusen periodics list` (or any task
// creation) registers the project and reconciles immediately.
func (s *Server) reconcileAllPeriodics() {
	projects, err := s.database.ListProjects()
	if err != nil {
		log.Printf("periodic: failed to list projects: %v", err)
		return
	}
	for _, proj := range projects {
		pc, err := s.getProjectContext(proj.ID)
		if err != nil {
			continue
		}
		if !s.periodicNeedsReconcile(proj.ID, pc.configModTime) {
			continue
		}
		if err := s.reconcilePeriodicsForProject(proj, pc.cfg); err != nil {
			// Leave the mod-time unmarked so the next tick retries.
			continue
		}
		s.markPeriodicReconciled(proj.ID, pc.configModTime)
	}
}

// periodicNeedsReconcile reports whether a project's periodic: section should be
// reconciled, given the current config mod-time. It returns true when the
// project has never been reconciled or its config has changed since the last
// successful reconcile.
func (s *Server) periodicNeedsReconcile(projectID int64, modTime time.Time) bool {
	s.periodicMu.Lock()
	defer s.periodicMu.Unlock()
	last, ok := s.periodicReconciled[projectID]
	return !ok || !last.Equal(modTime)
}

// markPeriodicReconciled records the config mod-time of the last successful
// reconcile for a project.
func (s *Server) markPeriodicReconciled(projectID int64, modTime time.Time) {
	s.periodicMu.Lock()
	defer s.periodicMu.Unlock()
	s.periodicReconciled[projectID] = modTime
}

// reconcilePeriodicsForProject upserts each periodic entry from the config and
// soft-deletes DB rows whose names no longer appear in the yml (preserving run
// history). Per-entry upsert failures are logged and skipped; a hard error is
// returned only when the existing-definitions listing fails (so the caller can
// retry on the next tick rather than marking the config reconciled).
//
// The entry's priority is stored raw (possibly empty) — the project-default
// fallback is resolved at fire time by createTaskFromRequest, so a changed
// project default takes effect without a .sakusen.yml touch.
func (s *Server) reconcilePeriodicsForProject(proj *db.Project, cfg *config.Config) error {
	// Serialize reconcile across the scheduler loop and handleListPeriodics so a
	// brand-new entry isn't double-INSERTed by two racing callers (which would
	// surface a benign-but-confusing UNIQUE(project_id,name) error). See the
	// periodicReconcileMu doc comment in server.go.
	s.periodicReconcileMu.Lock()
	defer s.periodicReconcileMu.Unlock()

	now := time.Now()
	current := make(map[string]bool, len(cfg.Periodic))
	for i := range cfg.Periodic {
		p := &cfg.Periodic[i]
		current[p.Name] = true

		next, err := config.NextPeriodicFire(p.Cadence, now)
		if err != nil {
			log.Printf("%speriodic %q: invalid cadence %q: %v", s.projectLogPrefix(proj.ID), p.Name, p.Cadence, err)
			continue
		}

		if _, err := s.database.UpsertPeriodicDef(proj.ID, p.Name, p.Cadence, p.Workflow, p.Input, p.Priority, p.Paused, next); err != nil {
			log.Printf("%speriodic %q: failed to upsert: %v", s.projectLogPrefix(proj.ID), p.Name, err)
		}
	}

	existing, err := s.database.ListPeriodicsForProject(proj.ID)
	if err != nil {
		log.Printf("%speriodic: failed to list definitions for reconcile: %v", s.projectLogPrefix(proj.ID), err)
		return err
	}
	for _, d := range existing {
		if !current[d.Name] {
			if err := s.database.SoftDeletePeriodicDef(proj.ID, d.Name); err != nil {
				log.Printf("%speriodic %q: failed to soft-delete: %v", s.projectLogPrefix(proj.ID), d.Name, err)
			} else {
				log.Printf("%speriodic %q: removed from config, soft-deleted", s.projectLogPrefix(proj.ID), d.Name)
			}
		}
	}
	return nil
}

// fireDuePeriodics claims and fires every due definition. Each fire advances
// the schedule atomically (ClaimPeriodicFire) so a double-tick or concurrent
// daemon cannot double-fire.
func (s *Server) fireDuePeriodics() {
	now := time.Now()
	due, err := s.database.ListDuePeriodics(now)
	if err != nil {
		log.Printf("periodic: failed to list due definitions: %v", err)
		return
	}
	for _, d := range due {
		s.scheduledFire(d, now)
	}
}

// scheduledFire claims a single fire for a due definition, applies skip-overlap,
// and materializes a task. The schedule is advanced regardless of whether a task
// is created (skip-overlap still moves the clock forward).
func (s *Server) scheduledFire(d *db.PeriodicDef, now time.Time) {
	newNext, err := config.NextPeriodicFire(d.Cadence, now)
	if err != nil {
		log.Printf("%speriodic %q: invalid cadence %q: %v", s.projectLogPrefix(d.ProjectID), d.Name, d.Cadence, err)
		return
	}

	claimed, err := s.database.ClaimPeriodicFire(d.ID, now, newNext)
	if err != nil {
		log.Printf("%speriodic %q: claim failed: %v", s.projectLogPrefix(d.ProjectID), d.Name, err)
		return
	}
	if !claimed {
		return
	}

	// Skip-overlap (hard-coded): if the previous fire's task is still active,
	// skip creating a new one. The schedule was already advanced by the claim.
	if d.LastTaskID != nil {
		if last, err := s.database.GetTask(*d.LastTaskID); err == nil && !last.Status.IsTerminal() {
			log.Printf("%speriodic %q: skip-overlap, last task #%d still active (%s)", s.projectLogPrefix(d.ProjectID), d.Name, last.ID, last.Status)
			return
		}
	}

	if _, err := s.materializePeriodicTask(d, now); err != nil {
		log.Printf("%speriodic %q: failed to materialize task: %v", s.projectLogPrefix(d.ProjectID), d.Name, err)
	}
}

// materializePeriodicTask builds and creates a task for one periodic fire using
// the same create path as interactive task creation, then links it back to the
// definition. Shared by the scheduler (fireTime = the claimed slot time) and
// the run-now handler (fireTime = the request time).
func (s *Server) materializePeriodicTask(d *db.PeriodicDef, fireTime time.Time) (*task.Task, error) {
	proj, err := s.database.GetProject(d.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve project: %w", err)
	}

	workflowName := d.WorkflowRef
	if workflowName == "" {
		workflowName = "periodic:" + d.Name
	}

	pid := d.ID
	req := CreateTaskRequest{
		Title:       fmt.Sprintf("%s @ %s", d.Name, fireTime.Format(time.RFC3339)),
		Input:       d.Input,
		Workflow:    workflowName,
		ProjectPath: proj.Path,
		Priority:    d.Priority,
		PeriodicID:  &pid,
	}

	t, _, err := s.createTaskFromRequest(req)
	if err != nil {
		return nil, err
	}

	s.broadcastToSubscribers(MsgTaskUpdate, TaskUpdateResponse{Task: s.taskToInfo(t)})
	s.kickOffPostCreate(t, req)

	if err := s.database.UpdatePeriodicLastTask(d.ID, t.ID); err != nil {
		log.Printf("%speriodic %q: failed to record last task #%d: %v", s.projectLogPrefix(d.ProjectID), d.Name, t.ID, err)
	}

	log.Printf("%speriodic %q: fired task #%d (workflow %q)", s.projectLogPrefix(d.ProjectID), d.Name, t.ID, workflowName)
	return t, nil
}
