package daemon

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
)

func projectToInfo(p *db.Project, counts db.ProjectTaskCounts) ProjectInfo {
	info, err := os.Stat(p.Path)
	return ProjectInfo{
		ID:          p.ID,
		Name:        p.Name,
		Path:        p.Path,
		PathExists:  err == nil && info.IsDir(),
		Tasks:       counts.Total,
		ActiveTasks: counts.Active,
	}
}

func (s *Server) handleListProjects(conn net.Conn) {
	projects, err := s.database.ListProjects()
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to list projects: %v", err))
		return
	}
	counts, err := s.database.CountTasksByProject()
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to count tasks: %v", err))
		return
	}
	infos := make([]ProjectInfo, len(projects))
	for i, p := range projects {
		infos[i] = projectToInfo(p, counts[p.ID])
	}
	_ = s.sendMessage(conn, MsgListProjects, ListProjectsResponse{Projects: infos})
}

// handleRemoveProject unregisters a project whose tasks are all terminal,
// deleting its tasks and every row keyed by it. When the project directory
// still exists, the tasks' worktrees, branches, tmux sessions and logs are
// then torn down in the background, as handleDeleteTask does for one task.
func (s *Server) handleRemoveProject(conn net.Conn, req RemoveProjectRequest) {
	proj, err := s.database.GetProject(req.ProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		s.sendError(conn, fmt.Sprintf("project #%d not found", req.ProjectID))
		return
	}
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get project: %v", err))
		return
	}
	tasks, err := s.database.GetTasksByProject(proj.ID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to list tasks: %v", err))
		return
	}
	var active []*task.Task
	for _, t := range tasks {
		if !t.Status.IsTerminal() {
			active = append(active, t)
		}
	}
	if len(active) > 0 {
		s.sendError(conn, activeTasksMessage(proj, active))
		return
	}

	info := projectToInfo(proj, db.ProjectTaskCounts{Total: len(tasks)})
	// Resolved before the delete: getProjectContext needs the project row.
	var pc *projectContext
	if info.PathExists && len(tasks) > 0 {
		if pc, err = s.getProjectContext(proj.ID); err != nil {
			log.Printf("Warning: project %s: no project context, leaving task worktrees in place: %v", proj.Path, err)
		}
	}

	s.periodicReconcileMu.Lock()
	err = s.database.DeleteProject(proj.ID)
	s.periodicReconcileMu.Unlock()
	if err != nil {
		var activeErr *db.ActiveTasksError
		if errors.As(err, &activeErr) {
			s.sendError(conn, fmt.Sprintf("project %s: %v", proj.Path, activeErr))
			return
		}
		s.sendError(conn, fmt.Sprintf("failed to remove project: %v", err))
		return
	}
	s.forgetProject(proj.ID)

	_ = s.sendMessage(conn, MsgRemoveProject, RemoveProjectResponse{Project: info, TasksRemoved: len(tasks)})

	if pc != nil {
		go func() {
			for _, t := range tasks {
				s.releaseTaskResources(pc, t)
			}
		}()
	}
}

func activeTasksMessage(proj *db.Project, active []*task.Task) string {
	names := make([]string, len(active))
	for i, t := range active {
		names[i] = fmt.Sprintf("#%d %q (%s)", t.ID, t.Title, t.Status)
	}
	return fmt.Sprintf("project %s has %d active task(s): %s — finish, stop or delete them first",
		proj.Path, len(active), strings.Join(names, ", "))
}

// forgetProject drops the daemon's in-memory state for a removed project.
func (s *Server) forgetProject(id int64) {
	s.projectsMu.Lock()
	delete(s.projects, id)
	s.projectsMu.Unlock()

	s.periodicMu.Lock()
	delete(s.periodicReconciled, id)
	delete(s.periodicAliasLogged, id)
	s.periodicMu.Unlock()
}
