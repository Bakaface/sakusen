package daemon

import (
	"fmt"
	"net"
	"time"

	"github.com/Bakaface/sakusen/internal/db"
)

// periodicToInfo projects a DB periodic definition into the client-facing
// PeriodicInfo, enriching it with the project name/path.
func (s *Server) periodicToInfo(d *db.PeriodicDef) PeriodicInfo {
	info := PeriodicInfo{
		ID:          d.ID,
		ProjectID:   d.ProjectID,
		Name:        d.Name,
		Cadence:     d.Cadence,
		WorkflowRef: d.WorkflowRef,
		Inline:      d.WorkflowRef == "",
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
	return info
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
	projectID := proj.ID

	// Reconcile from the project's current .sakusen.yml before listing so the
	// view reflects the on-disk config immediately, even for projects the
	// scheduler hasn't ticked yet (e.g. a freshly-registered periodic-only
	// project). Record the mod-time on success so the scheduler loop doesn't
	// redundantly re-reconcile this project on its next tick.
	if pc, err := s.getProjectContext(projectID); err == nil {
		if err := s.reconcilePeriodicsForProject(proj, pc.cfg); err == nil {
			s.markPeriodicReconciled(projectID, pc.configModTime)
		}
	}

	defs, err := s.database.ListPeriodicsForProject(projectID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to list periodics: %v", err))
		return
	}

	infos := make([]PeriodicInfo, 0, len(defs))
	for _, d := range defs {
		infos = append(infos, s.periodicToInfo(d))
	}
	s.sendMessage(conn, MsgListPeriodics, ListPeriodicsResponse{Periodics: infos})
}

func (s *Server) handleGetPeriodic(conn net.Conn, req GetPeriodicRequest) {
	d, err := s.database.GetPeriodicByID(req.ID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get periodic #%d: %v", req.ID, err))
		return
	}
	s.sendMessage(conn, MsgGetPeriodic, GetPeriodicResponse{Periodic: s.periodicToInfo(d)})
}

func (s *Server) handleSetPeriodicPaused(conn net.Conn, req SetPeriodicPausedRequest) {
	if err := s.database.SetPeriodicPaused(req.ID, req.Paused); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to update periodic #%d: %v", req.ID, err))
		return
	}
	d, err := s.database.GetPeriodicByID(req.ID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to reload periodic #%d: %v", req.ID, err))
		return
	}
	s.sendMessage(conn, MsgSetPeriodicPaused, SetPeriodicPausedResponse{Periodic: s.periodicToInfo(d)})
}

func (s *Server) handleListPeriodicRuns(conn net.Conn, req ListPeriodicRunsRequest) {
	tasks, err := s.database.GetTasksForPeriodic(req.PeriodicID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to list periodic runs: %v", err))
		return
	}
	infos := make([]TaskInfo, len(tasks))
	for i, t := range tasks {
		infos[i] = s.taskToInfo(t)
	}
	s.sendMessage(conn, MsgListPeriodicRuns, ListPeriodicRunsResponse{Tasks: infos})
}

// handleFirePeriodicNow materializes a one-shot fire using the same create path
// as the scheduler, WITHOUT advancing the cron schedule (next_fire_at is left
// untouched) or applying skip-overlap to this fire. A soft-deleted definition
// is rejected — its workflow may no longer exist and its schedule is dead. A
// PAUSED definition may be fired manually on purpose (pause stops the clock,
// not the operator). Note that materializePeriodicTask still records the new
// task as last_task_id, so if this manual run is still active when the next
// scheduled tick lands, that tick will skip-overlap on it.
func (s *Server) handleFirePeriodicNow(conn net.Conn, req FirePeriodicNowRequest) {
	d, err := s.database.GetPeriodicByID(req.ID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get periodic #%d: %v", req.ID, err))
		return
	}
	if d.DeletedAt != nil {
		s.sendError(conn, fmt.Sprintf("periodic #%d (%s) was removed from .sakusen.yml; re-add it before firing", req.ID, d.Name))
		return
	}
	t, err := s.materializePeriodicTask(d, time.Now())
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to fire periodic #%d: %v", req.ID, err))
		return
	}
	s.sendMessage(conn, MsgFirePeriodicNow, FirePeriodicNowResponse{Task: s.taskToInfo(t)})
}
