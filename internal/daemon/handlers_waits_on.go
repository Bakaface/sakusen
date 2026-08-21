package daemon

import (
	"fmt"
	"log"
	"net"
	"strconv"

	"github.com/Bakaface/sakusen/internal/task"
)

// handleCreateTasksAndWait creates each child task in req.Tasks and records
// task_waits_on edges from req.ParentTaskID to each new child. The parent's
// currently-running step suspends to StatusAwaitingChildren when the engine's
// next post-Claude check observes the edges.
//
// Children MAY live in a different project than the parent: pass a per-child
// project_path. An omitted project_path defaults to the parent's project.
//
// Cross-project waits cannot deadlock. On suspend the parent's agent goroutine
// returns, so the agent manager marks it completed and frees its max_workers
// slot (canStartMore counts only agents whose state IsActive). GetClaimableTasks
// is project-blind, so startTaskAgent picks up whichever project the child lives
// in and runs it through that project's own engine. No scheduler resource is
// held while blocked — no hold-and-wait, hence no deadlock, even at
// max_workers = 1.
//
// A child that never reaches terminal status leaves the parent suspended
// indefinitely. That is a stuck wait, not a deadlock — nothing is held.
// Recovery is `sakusen delete <child_id>`, which drops the wait-on edge (see
// DeleteTask) and lets the poller resume the parent on the next tick.
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
	var crossProject []int64
	for i, childReq := range req.Tasks {
		// Default project_path to the parent's project so the agent doesn't
		// have to discover it.
		if childReq.ProjectPath == "" {
			childReq.ProjectPath = parentProj.Path
		}
		// Resolve the child's project row BEFORE track inheritance.
		// GetOrCreateProject is idempotent and applies exactly the same
		// filepath.Abs normalization createTaskFromRequest will apply, so
		// comparing IDs is the honest same-project test.
		childProj, perr := s.database.GetOrCreateProject(childReq.ProjectPath)
		if perr != nil {
			s.sendError(conn, fmt.Sprintf("failed to resolve project for child task %d/%d: %v", i+1, len(req.Tasks), perr))
			return
		}
		// Children inherit the parent task's track by default, keeping sprint
		// fan-outs coherent. An explicit per-child Track overrides; the "none"
		// sentinel passes through and is normalized to trackless inside
		// createTaskFromRequest.
		//
		// Track inheritance is SAME-PROJECT ONLY. Tracks are project-scoped
		// (resolveTrackRef rejects a numeric track ID owned by another
		// project), so stamping the parent's track ID onto a child in another
		// project would hard-fail the create. Cross-project children start
		// trackless; an explicit per-child Track is still honored and resolves
		// in the CHILD's own project. The rule is uniform — it applies even
		// when the parent's track is global — so the behavior is predictable.
		if childReq.Track == "" && parent.TrackID != nil && childProj.ID == parent.ProjectID {
			childReq.Track = strconv.FormatInt(*parent.TrackID, 10)
		}
		child, _, err := s.createTaskFromRequest(childReq)
		if err != nil {
			s.sendError(conn, fmt.Sprintf("failed to create child task %d/%d: %v", i+1, len(req.Tasks), err))
			return
		}
		if child.ProjectID != parent.ProjectID {
			crossProject = append(crossProject, child.ID)
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

	// Call out cross-project fan-out explicitly — the daemon log is the only
	// place an operator can see that children landed outside the parent's
	// project.
	if len(crossProject) > 0 {
		log.Printf("%sParent task #%d will suspend on %d children: %v (cross-project: %v)", s.projectLogPrefix(parent.ProjectID), parent.ID, len(createdTasks), childIDsOf(createdTasks), crossProject)
	} else {
		log.Printf("%sParent task #%d will suspend on %d children: %v", s.projectLogPrefix(parent.ProjectID), parent.ID, len(createdTasks), childIDsOf(createdTasks))
	}

	s.sendMessage(conn, MsgCreateTasksAndWait, CreateTasksAndWaitResponse{
		ParentTaskID: parent.ID,
		Children:     created,
	})
}

// handleWaitForTasks records task_waits_on edges from req.ParentTaskID to a
// pre-existing set of child task IDs. Useful for "wait on tasks I didn't
// just spawn" patterns; the bundled create_tasks_and_wait is preferred for
// fresh children because it eliminates the risk of forgetting to wait.
//
// Children may live in any project — the wait relation is purely ID-based and
// project-blind, and the same slot-release argument documented on
// handleCreateTasksAndWait means a cross-project wait cannot deadlock.
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
