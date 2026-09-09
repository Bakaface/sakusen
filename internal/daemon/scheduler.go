package daemon

import (
	"fmt"
	"log"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
)

// periodicTickInterval is the scheduler's poll cadence. Routine cadences are
// minute-granular (the standard cron parser enforces a 1-minute floor), so a
// 30s tick guarantees each due routine is observed within the same minute.
const periodicTickInterval = 30 * time.Second

// periodicSchedulerLoop reconciles routines from each project's .sakusen.yml
// into the DB and fires due ones as ordinary tasks. Startup
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
// routine that fell due during downtime fires at most once here, then its
// schedule is clamped forward — no backlog of make-up fires.
func (s *Server) checkPeriodicCatchup() {
	s.reconcileAllPeriodics()
	s.fireDuePeriodics()
}

// reconcileAllPeriodics walks every registered project, loading its config
// through the cached project context (honoring the lazy-load/live-reload
// invariant) and reconciling the routines: section into the DB.
//
// Reconciliation is skipped for projects whose .sakusen.yml has not changed
// since the last successful reconcile (tracked by config mod-time, the same
// signal getProjectContext uses for cache invalidation). This keeps the steady
// state cheap: most 30s ticks do no DB writes at all and only fireDuePeriodics
// runs.
//
// Note: only projects already registered in the DB are walked — a project
// whose .sakusen.yml has a routines: section but which has never been touched
// by any sakusen command does not fire. `sakusen routines list` (or any task
// creation) registers the project and reconciles immediately.
func (s *Server) reconcileAllPeriodics() {
	projects, err := s.database.ListProjects()
	if err != nil {
		log.Printf("routines: failed to list projects: %v", err)
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

// periodicNeedsReconcile reports whether a project's routines: section should be
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

// reconcilePeriodicsForProject upserts each routine from the config and
// soft-deletes DB rows whose names no longer appear in the yml (preserving run
// history). Per-routine upsert failures are logged and skipped; a hard error is
// returned only when the existing-definitions listing fails (so the caller can
// retry on the next tick rather than marking the config reconciled).
//
// On-demand routines (no cadence) get a row too, so `show`, `runs` and
// last-task tracking work the same for them; their next_fire_at is a filler
// value the due-query ignores.
//
// The routine's priority is stored raw (possibly empty) — the project-default
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
	current := make(map[string]bool, len(cfg.Routines))
	for i := range cfg.Routines {
		r := &cfg.Routines[i]
		current[r.Name] = true

		next := now
		if r.IsScheduled() {
			var err error
			if next, err = config.NextRoutineFire(r.Cadence, now); err != nil {
				log.Printf("%sroutine %q: invalid cadence %q: %v", s.projectLogPrefix(proj.ID), r.Name, r.Cadence, err)
				continue
			}
		}

		if _, err := s.database.UpsertPeriodicDef(proj.ID, r.Name, r.Cadence, r.Workflow, r.Input, r.Priority, r.Paused, next); err != nil {
			log.Printf("%sroutine %q: failed to upsert: %v", s.projectLogPrefix(proj.ID), r.Name, err)
		}
	}

	existing, err := s.database.ListPeriodicsForProject(proj.ID)
	if err != nil {
		log.Printf("%sroutines: failed to list definitions for reconcile: %v", s.projectLogPrefix(proj.ID), err)
		return err
	}
	for _, d := range existing {
		if !current[d.Name] {
			if err := s.database.SoftDeletePeriodicDef(proj.ID, d.Name); err != nil {
				log.Printf("%sroutine %q: failed to soft-delete: %v", s.projectLogPrefix(proj.ID), d.Name, err)
			} else {
				log.Printf("%sroutine %q: removed from config, soft-deleted", s.projectLogPrefix(proj.ID), d.Name)
			}
		}
	}
	return nil
}

// fireDuePeriodics claims and fires every due routine. Each fire advances
// the schedule atomically (ClaimPeriodicFire) so a double-tick or concurrent
// daemon cannot double-fire.
func (s *Server) fireDuePeriodics() {
	now := time.Now()
	due, err := s.database.ListDuePeriodics(now)
	if err != nil {
		log.Printf("routines: failed to list due routines: %v", err)
		return
	}
	for _, d := range due {
		s.scheduledFire(d, now)
	}
}

// scheduledFire claims a single fire for a due routine, applies skip-overlap,
// and materializes a task. The schedule is advanced regardless of whether a
// task is created (skip-overlap still moves the clock forward).
func (s *Server) scheduledFire(d *db.PeriodicDef, now time.Time) {
	newNext, err := config.NextRoutineFire(d.Cadence, now)
	if err != nil {
		log.Printf("%sroutine %q: invalid cadence %q: %v", s.projectLogPrefix(d.ProjectID), d.Name, d.Cadence, err)
		return
	}

	claimed, err := s.database.ClaimPeriodicFire(d.ID, now, newNext)
	if err != nil {
		log.Printf("%sroutine %q: claim failed: %v", s.projectLogPrefix(d.ProjectID), d.Name, err)
		return
	}
	if !claimed {
		return
	}

	// Skip-overlap (hard-coded): if the previous fire's task is still active,
	// skip creating a new one. The schedule was already advanced by the claim.
	if d.LastTaskID != nil {
		if last, err := s.database.GetTask(*d.LastTaskID); err == nil && !last.Status.IsTerminal() {
			log.Printf("%sroutine %q: skip-overlap, last task #%d still active (%s)", s.projectLogPrefix(d.ProjectID), d.Name, last.ID, last.Status)
			return
		}
	}

	// Re-resolve against the project's CURRENT config: the row may be stale
	// (config edited between reconcile and tick). A routine that no longer
	// resolves — or that now needs an input argument nobody can supply on a
	// schedule — is skipped; the claim already advanced the clock.
	b, err := s.resolveRoutine(d)
	if err != nil {
		log.Printf("%sroutine %q: skipping scheduled fire: %v", s.projectLogPrefix(d.ProjectID), d.Name, err)
		return
	}

	if _, err := s.fireRoutine(b, d, "", now); err != nil {
		log.Printf("%sroutine %q: failed to fire: %v", s.projectLogPrefix(d.ProjectID), d.Name, err)
	}
}

// routineBinding is a DB routine row resolved against its project's current
// config: everything a fire needs, looked up once.
type routineBinding struct {
	proj     *db.Project
	cfg      *config.Config
	routine  *config.RoutineConfig
	workflow *config.WorkflowConfig
}

// resolveRoutine pairs a periodic_definitions row with the routine and workflow
// it names in the project's current .sakusen.yml. The config — not the row — is
// authoritative for pins and the input requirement, so an edited routine takes
// effect on the next fire without waiting for a reconcile.
func (s *Server) resolveRoutine(d *db.PeriodicDef) (*routineBinding, error) {
	proj, err := s.database.GetProject(d.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve project: %w", err)
	}
	pc, err := s.getProjectContext(d.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to load config for %s: %w", proj.Path, err)
	}
	r := pc.cfg.GetRoutine(d.Name)
	if r == nil {
		return nil, fmt.Errorf("no longer declared under routines: in .sakusen.yml")
	}
	wf := pc.cfg.GetTaskWorkflow(r.Workflow)
	if wf == nil {
		return nil, fmt.Errorf("references unknown workflow %q", r.Workflow)
	}
	return &routineBinding{proj: proj, cfg: pc.cfg, routine: r, workflow: wf}, nil
}

// fireRoutine builds and creates a task for one routine fire using the same
// create path as interactive task creation, then links it back to the routine's
// row. Shared by the scheduler (fireTime = the claimed slot time) and the
// run-now handlers (fireTime = the request time).
//
// The routine's effective pins are passed EXPLICITLY on the request so they
// beat the referenced workflow's own pins in createTaskFromRequest — that
// per-request precedence is what lets one workflow back several differently
// pinned routines. An explicit input likewise overrides the routine's literal
// `input:`.
func (s *Server) fireRoutine(b *routineBinding, d *db.PeriodicDef, input string, fireTime time.Time) (*task.Task, error) {
	if input == "" && b.cfg.RoutineRequiresInput(b.routine) {
		return nil, fmt.Errorf("routine %q requires an input argument", b.routine.Name)
	}

	pins := b.routine.EffectivePins(b.workflow)
	if input == "" {
		input = pins.Input
	}

	pid := d.ID
	req := CreateTaskRequest{
		Title:          fmt.Sprintf("%s @ %s", b.routine.Name, fireTime.Format(time.RFC3339)),
		Input:          input,
		Workflow:       b.routine.Workflow,
		ProjectPath:    b.proj.Path,
		Priority:       b.routine.Priority,
		PeriodicID:     &pid,
		Worktree:       pins.Worktree,
		BranchName:     pins.Branch,
		CheckoutBranch: pins.Checkout,
		TargetBranch:   pins.Target,
	}

	t, _, err := s.createTaskFromRequest(req)
	if err != nil {
		return nil, err
	}

	s.broadcastToSubscribers(MsgTaskUpdate, TaskUpdateResponse{Task: s.taskToInfo(t)})
	s.kickOffPostCreate(t, req)

	if err := s.database.UpdatePeriodicLastTask(d.ID, t.ID); err != nil {
		log.Printf("%sroutine %q: failed to record last task #%d: %v", s.projectLogPrefix(d.ProjectID), b.routine.Name, t.ID, err)
	}

	log.Printf("%sroutine %q: fired task #%d (workflow %q)", s.projectLogPrefix(d.ProjectID), b.routine.Name, t.ID, b.routine.Workflow)
	return t, nil
}
