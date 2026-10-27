package daemon

import (
	"fmt"
	"log"
	"strconv"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/task"
	"github.com/Bakaface/sakusen/internal/workflow"
)

// fanoutHost creates fanout children through createTaskFromRequest, so they
// get the same validation, blocked_by wiring and async title refinement as
// any other task.
type fanoutHost struct {
	s *Server
}

var _ workflow.FanoutHost = fanoutHost{}

// SpawnChild creates one child of parent in parent's project.
func (h fanoutHost) SpawnChild(parent *task.Task, req workflow.FanoutSpawnRequest) (*task.Task, error) {
	s := h.s
	proj, err := s.database.GetProject(parent.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve parent project: %w", err)
	}

	track := "none"
	if req.TrackID != nil {
		track = strconv.FormatInt(*req.TrackID, 10)
	}
	createReq := CreateTaskRequest{
		Title:       req.Title,
		Input:       req.Input,
		Workflow:    req.Workflow,
		Priority:    string(req.Priority),
		ProjectPath: proj.Path,
		BlockedBy:   req.BlockedBy,
		Track:       track,
		FanoutOrigin: &task.FanoutOrigin{
			ParentID: parent.ID,
			Step:     req.Step,
			Item:     req.Item,
		},
	}
	if req.Merge == config.FanoutMergeParent {
		target, err := task.ParentMergeTarget(parent, parent.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("merge: parent: %w", err)
		}
		createReq.TargetBranch = target
	}

	child, _, err := s.createTaskFromRequest(createReq)
	if err != nil {
		return nil, err
	}
	s.broadcastToSubscribers(MsgTaskUpdate, TaskUpdateResponse{Task: s.taskToInfo(child)})
	s.kickOffPostCreate(child, createReq)
	log.Printf("%sTask #%d: fanout %q spawned child #%d for item %q", s.projectLogPrefix(parent.ProjectID), parent.ID, req.Step, child.ID, req.Item)
	return child, nil
}

// RetryChild resets a terminal child to pending through the full-retry path.
func (h fanoutHost) RetryChild(id int64) error {
	_, err := h.s.retryTaskFull(id)
	return err
}
