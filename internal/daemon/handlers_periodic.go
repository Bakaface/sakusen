package daemon

import (
	"database/sql"
	"fmt"
	"net"
	"time"

	"github.com/Bakaface/sakusen/internal/db"
)

// periodicToInfo projects a routine's DB row into the client-facing
// PeriodicInfo, enriching it with the project name/path and the config-derived
// fields (description, input requirement) that live in .sakusen.yml rather than
// in the row.
func (s *Server) periodicToInfo(d *db.PeriodicDef) PeriodicInfo {
	info := PeriodicInfo{
		ID:          d.ID,
		ProjectID:   d.ProjectID,
		Name:        d.Name,
		Cadence:     d.Cadence,
		WorkflowRef: d.WorkflowRef,
		Input:       d.Input,
		Priority:    d.Priority,
		Paused:      d.Paused,
		NextFireAt:  d.NextFireAt,
		LastFiredAt: d.LastFiredAt,
		LastTaskID:  d.LastTaskID,
	}
	if proj, err := s.database.GetProject(d.ProjectID); err == nil {
		info.ProjectName = proj.Name
		info.ProjectPath = proj.Path
	}
	if pc, err := s.getProjectContext(d.ProjectID); err == nil {
		if r := pc.cfg.GetRoutine(d.Name); r != nil {
			info.Description = r.Description
			info.RequiresInput = pc.cfg.RoutineRequiresInput(r)
		}
	}
	return info
}

// reconcileProjectRoutines brings the project's routine rows in line with its
// current .sakusen.yml before a read, so a routine added to the yml is visible
// (and runnable) immediately rather than after the next scheduler tick. The
// mod-time is recorded on success so the loop doesn't redundantly redo it.
func (s *Server) reconcileProjectRoutines(proj *db.Project) {
	pc, err := s.getProjectContext(proj.ID)
	if err != nil {
		return
	}
	if err := s.reconcilePeriodicsForProject(proj, pc.cfg); err == nil {
		s.markPeriodicReconciled(proj.ID, pc.configModTime)
	}
}

// resolveRoutineRow resolves a routine by project path + name, reconciling the
// project first. Every name-addressed handler goes through it.
func (s *Server) resolveRoutineRow(projectPath, name string) (*db.PeriodicDef, error) {
	if projectPath == "" {
		return nil, fmt.Errorf("project_path is required")
	}
	if name == "" {
		return nil, fmt.Errorf("routine name is required")
	}
	proj, err := s.database.GetOrCreateProject(projectPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve project: %w", err)
	}
	s.reconcileProjectRoutines(proj)

	d, err := s.database.GetPeriodicByName(proj.ID, name)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no routine %q in %s (check the `routines:` list in .sakusen.yml)", name, proj.Path)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get routine %q: %w", name, err)
	}
	return d, nil
}

func (s *Server) handleListPeriodics(conn net.Conn, req ListPeriodicsRequest) {
	var proj *db.Project
	var err error
	if req.ProjectID > 0 {
		proj, err = s.database.GetProject(req.ProjectID)
	} else if req.ProjectPath != "" {
		proj, err = s.database.GetOrCreateProject(req.ProjectPath)
	} else {
		s.sendError(conn, "project_path or project_id is required")
		return
	}
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to resolve project: %v", err))
		return
	}

	s.reconcileProjectRoutines(proj)

	defs, err := s.database.ListPeriodicsForProject(proj.ID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to list routines: %v", err))
		return
	}

	infos := make([]PeriodicInfo, 0, len(defs))
	for _, d := range defs {
		infos = append(infos, s.periodicToInfo(d))
	}
	s.sendMessage(conn, MsgListPeriodics, ListPeriodicsResponse{Periodics: infos})
}

func (s *Server) handleGetPeriodic(conn net.Conn, req GetPeriodicRequest) {
	d, err := s.resolveRoutineRow(req.ProjectPath, req.Name)
	if err != nil {
		s.sendError(conn, err.Error())
		return
	}
	s.sendMessage(conn, MsgGetPeriodic, GetPeriodicResponse{Periodic: s.periodicToInfo(d)})
}

// handleSetPeriodicPaused toggles a scheduled routine's pause flag. Pausing an
// on-demand routine is rejected rather than silently accepted: there is no
// clock to stop, and the flag would only make its run surfaces confusing.
func (s *Server) handleSetPeriodicPaused(conn net.Conn, req SetPeriodicPausedRequest) {
	d, err := s.resolveRoutineRow(req.ProjectPath, req.Name)
	if err != nil {
		s.sendError(conn, err.Error())
		return
	}
	if d.Cadence == "" {
		s.sendError(conn, fmt.Sprintf("routine %q has no cadence; nothing to pause", d.Name))
		return
	}
	if err := s.database.SetPeriodicPaused(d.ID, req.Paused); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to update routine %q: %v", d.Name, err))
		return
	}
	updated, err := s.database.GetPeriodicByID(d.ID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to reload routine %q: %v", d.Name, err))
		return
	}
	s.sendMessage(conn, MsgSetPeriodicPaused, SetPeriodicPausedResponse{Periodic: s.periodicToInfo(updated)})
}

func (s *Server) handleListPeriodicRuns(conn net.Conn, req ListPeriodicRunsRequest) {
	d, err := s.resolveRoutineRow(req.ProjectPath, req.Name)
	if err != nil {
		s.sendError(conn, err.Error())
		return
	}
	tasks, err := s.database.GetTasksForPeriodic(d.ID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to list runs for routine %q: %v", d.Name, err))
		return
	}
	infos := make([]TaskInfo, len(tasks))
	for i, t := range tasks {
		infos[i] = s.taskToInfo(t)
	}
	s.sendMessage(conn, MsgListPeriodicRuns, ListPeriodicRunsResponse{Tasks: infos})
}

// handleFirePeriodicNow runs a routine on demand through the same create path
// as the scheduler, WITHOUT advancing any cron schedule (next_fire_at is left
// untouched) or applying skip-overlap to this fire. A soft-deleted routine is
// rejected — its workflow may no longer exist and its schedule is dead. A
// PAUSED routine may be run manually on purpose (pause stops the clock, not the
// operator). Note that fireRoutine still records the new task as last_task_id,
// so if this manual run is still active when the next scheduled tick lands,
// that tick will skip-overlap on it.
func (s *Server) handleFirePeriodicNow(conn net.Conn, req FirePeriodicNowRequest) {
	d, err := s.resolveRoutineRow(req.ProjectPath, req.Name)
	if err != nil {
		s.sendError(conn, err.Error())
		return
	}
	if d.DeletedAt != nil {
		s.sendError(conn, fmt.Sprintf("routine %q was removed from .sakusen.yml; re-add it before running", d.Name))
		return
	}
	b, err := s.resolveRoutine(d)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("routine %q: %v", d.Name, err))
		return
	}
	t, err := s.fireRoutine(b, d, req.Input, time.Now())
	if err != nil {
		s.sendError(conn, err.Error())
		return
	}
	s.sendMessage(conn, MsgFirePeriodicNow, FirePeriodicNowResponse{Task: s.taskToInfo(t)})
}
