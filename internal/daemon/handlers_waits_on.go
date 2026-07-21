package daemon

import (
	"fmt"
	"log"
	"net"
	"strconv"

	"github.com/Bakaface/sortie/internal/task"
)

// handleCreateTasksAndWait creates each child task in req.Tasks and records
// task_waits_on edges from req.ParentTaskID to each new child. The parent's
// currently-running step suspends to StatusAwaitingChildren when the engine's
// next post-Claude check observes the edges.
//
// All children must resolve to the same project as the parent (the engine
// re-runs only that project's workflow, so cross-project waits are nonsensical
// and would deadlock under the current scheduler).
//
// Validation is fail-fast: if any child fails to create, the partial children
// are left in place (caller can decide how to recover). We do NOT roll back —
// children created so far still need cleanup either way, and exposing the IDs
// gives the agent a chance to inspect/delete them.
func (s *Server) handleCreateTasksAndWait(conn net.Conn, req CreateTasksAndWaitRequest) {
	parent, err := s.database.GetTask(req.ParentTaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("parent task #%d not found: %v", req.ParentTaskID, err))
		return
	}
	if len(req.Tasks) == 0 {
		s.sendError(conn, "tasks array must contain at least one child task")
		return
	}

	parentProj, err := s.database.GetProject(parent.ProjectID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to resolve parent project: %v", err))
		return
	}

	created := make([]TaskInfo, 0, len(req.Tasks))
	createdTasks := make([]*task.Task, 0, len(req.Tasks))
	for i, childReq := range req.Tasks {
		// Default project_path to the parent's project so the agent doesn't
		// have to discover it.
		if childReq.ProjectPath == "" {
			childReq.ProjectPath = parentProj.Path
		}
		// Children inherit the parent task's track by default, keeping sprint
		// fan-outs coherent. An explicit per-child Track overrides; the "none"
		// sentinel passes through and is normalized to trackless inside
		// createTaskFromRequest.
		if childReq.Track == "" && parent.TrackID != nil {
			childReq.Track = strconv.FormatInt(*parent.TrackID, 10)
		}
		child, _, err := s.createTaskFromRequest(childReq)
		if err != nil {
			s.sendError(conn, fmt.Sprintf("failed to create child task %d/%d: %v", i+1, len(req.Tasks), err))
			return
		}
		if child.ProjectID != parent.ProjectID {
			s.sendError(conn, fmt.Sprintf("child task #%d belongs to a different project (%d) than parent (%d)", child.ID, child.ProjectID, parent.ProjectID))
			return
		}

		// Cycle check: a parent cannot wait on a task that already (transitively)
		// waits on or is blocked by the parent.
		circular, cerr := s.database.HasCircularWaitsOn(parent.ID, child.ID)
		if cerr != nil {
			s.sendError(conn, fmt.Sprintf("failed to check circular waits-on: %v", cerr))
			return
		}
		if circular {
			s.sendError(conn, fmt.Sprintf("adding wait-on edge parent #%d -> child #%d would create a cycle", parent.ID, child.ID))
			return
		}

		if err := s.database.AddTaskWaitsOn(parent.ID, child.ID); err != nil {
			s.sendError(conn, fmt.Sprintf("failed to record waits-on edge: %v", err))
			return
		}
		created = append(created, s.taskToInfo(child))
		createdTasks = append(createdTasks, child)
	}

	// Broadcast and kick off async refinement AFTER all edges are recorded so
	// the engine's wait-on probe sees a coherent picture on first observation.
	for i, child := range createdTasks {
		s.broadcastToSubscribers(MsgTaskUpdate, TaskUpdateResponse{Task: created[i]})
		s.kickOffPostCreate(child, req.Tasks[i])
	}
	s.broadcastTaskUpdate(parent.ID)

	log.Printf("%sParent task #%d will suspend on %d children: %v", s.projectLogPrefix(parent.ProjectID), parent.ID, len(createdTasks), childIDsOf(createdTasks))

	s.sendMessage(conn, MsgCreateTasksAndWait, CreateTasksAndWaitResponse{
		ParentTaskID: parent.ID,
		Children:     created,
	})
}

// handleWaitForTasks records task_waits_on edges from req.ParentTaskID to a
// pre-existing set of child task IDs. Useful for "wait on tasks I didn't
// just spawn" patterns; the bundled create_tasks_and_wait is preferred for
// fresh children because it eliminates the risk of forgetting to wait.
func (s *Server) handleWaitForTasks(conn net.Conn, req WaitForTasksRequest) {
	parent, err := s.database.GetTask(req.ParentTaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("parent task #%d not found: %v", req.ParentTaskID, err))
		return
	}
	if len(req.ChildTaskIDs) == 0 {
		s.sendError(conn, "child_task_ids must contain at least one task ID")
		return
	}

	// Dedupe — the agent might supply the same ID twice.
	seen := make(map[int64]bool, len(req.ChildTaskIDs))
	resolved := make([]*task.Task, 0, len(req.ChildTaskIDs))
	for _, childID := range req.ChildTaskIDs {
		if childID == parent.ID {
			s.sendError(conn, fmt.Sprintf("task cannot wait on itself (#%d)", parent.ID))
			return
		}
		if seen[childID] {
			continue
		}
		seen[childID] = true
		child, err := s.database.GetTask(childID)
		if err != nil || child == nil {
			s.sendError(conn, fmt.Sprintf("child task #%d not found", childID))
			return
		}
		if child.ProjectID != parent.ProjectID {
			s.sendError(conn, fmt.Sprintf("child task #%d belongs to a different project than parent #%d", childID, parent.ID))
			return
		}
		// Skip already-terminal children — adding a wait edge would
		// auto-resolve on next poller tick anyway, and trapping the parent on
		// a no-op suspension is gratuitous.
		if child.Status.IsTerminal() {
			continue
		}
		circular, cerr := s.database.HasCircularWaitsOn(parent.ID, child.ID)
		if cerr != nil {
			s.sendError(conn, fmt.Sprintf("failed to check circular waits-on: %v", cerr))
			return
		}
		if circular {
			s.sendError(conn, fmt.Sprintf("adding wait-on edge parent #%d -> child #%d would create a cycle", parent.ID, child.ID))
			return
		}
		if err := s.database.AddTaskWaitsOn(parent.ID, child.ID); err != nil {
			s.sendError(conn, fmt.Sprintf("failed to record waits-on edge: %v", err))
			return
		}
		resolved = append(resolved, child)
	}

	infos := make([]TaskInfo, 0, len(resolved))
	for _, c := range resolved {
		infos = append(infos, s.taskToInfo(c))
	}
	s.broadcastTaskUpdate(parent.ID)

	s.sendMessage(conn, MsgWaitForTasks, WaitForTasksResponse{
		ParentTaskID: parent.ID,
		Children:     infos,
	})
}

// childIDsOf is a logging helper.
func childIDsOf(tasks []*task.Task) []int64 {
	out := make([]int64, len(tasks))
	for i, t := range tasks {
		out[i] = t.ID
	}
	return out
}
