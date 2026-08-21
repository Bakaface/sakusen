package daemon

import (
	"fmt"
	"log"
	"net"
	"os"

	"github.com/Bakaface/sakusen/internal/task"
	"github.com/Bakaface/sakusen/internal/workflow"
)

// handleCleanup removes worktrees, branches, and log directories for one or
// more completed/failed tasks. A TaskID of 0 fans out to every eligible task.
// Cleanup that fails for an individual task is logged but does not abort the
// batch, mirroring the previous CLI behavior.
func (s *Server) handleCleanup(conn net.Conn, req CleanupRequest) {
	var tasks []*task.Task

	if req.TaskID != 0 {
		t, err := s.database.GetTask(req.TaskID)
		if err != nil {
			s.sendError(conn, fmt.Sprintf("task not found: %v", err))
			return
		}
		tasks = []*task.Task{t}
	} else {
		all, err := s.database.GetAllTasks()
		if err != nil {
			s.sendError(conn, fmt.Sprintf("failed to list tasks: %v", err))
			return
		}
		for _, t := range all {
			if t.Status == task.StatusCompleted || t.Status == task.StatusFailed {
				tasks = append(tasks, t)
			}
		}
	}

	count := 0
	infos := make([]TaskInfo, 0, len(tasks))
	for _, t := range tasks {
		cleaned, err := s.cleanupTask(t)
		if err != nil {
			log.Printf("%sWarning: cleanup failed for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
			continue
		}
		if cleaned {
			count++
			if refreshed, err := s.database.GetTask(t.ID); err == nil {
				infos = append(infos, s.taskToInfo(refreshed))
			}
		}
	}

	s.sendMessage(conn, MsgCleanup, CleanupResponse{Count: count, Tasks: infos})
}

// cleanupTask removes the worktree, deletes the task branch (unless the user
// provided it via --checkout), and removes the log directory. It returns
// whether anything was cleaned and any non-fatal error encountered.
func (s *Server) cleanupTask(t *task.Task) (bool, error) {
	pc, err := s.getProjectContext(t.ProjectID)
	if err != nil {
		return false, fmt.Errorf("failed to get project context: %w", err)
	}

	cleaned := false

	if t.Worktree && t.WorktreePath != "" {
		s.cleanupWorktreeAndBranch(pc, t)
		cleaned = true
	} else if t.Worktree && t.Branch != "" && t.CheckoutBranch == "" {
		// No worktree path but a sakusen-created branch remains — remove it.
		pc.engine.Coord().Lock().WithLock(func() {
			if err := pc.repo.ForceDeleteBranch(t.Branch); err != nil {
				log.Printf("%sWarning: failed to delete branch for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
			} else {
				cleaned = true
			}
		})
	}

	dataDir := s.getProjectDataDir(t)
	logDir := workflow.ProjectLogsDir(dataDir, t.ID)
	if err := os.RemoveAll(logDir); err != nil {
		log.Printf("%sWarning: failed to remove log dir for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
	} else {
		cleaned = true
	}

	return cleaned, nil
}
