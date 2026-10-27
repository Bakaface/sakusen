package daemon

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/Bakaface/sakusen/internal/task"
)

// handlePublishItems stores the item list a running step publishes for a
// fanout step to spawn, replacing any earlier list for that step.
func (s *Server) handlePublishItems(conn net.Conn, req PublishItemsRequest) {
	items, err := s.publishItems(req)
	if err != nil {
		s.sendError(conn, err.Error())
		return
	}
	s.broadcastTaskUpdate(req.TaskID)
	_ = s.sendMessage(conn, MsgPublishItems, PublishItemsResponse{Count: len(items), Items: items})
}

func (s *Server) publishItems(req PublishItemsRequest) ([]task.PublishedItem, error) {
	if req.TaskID <= 0 {
		return nil, errors.New("task_id is required")
	}
	if strings.TrimSpace(req.StepName) == "" {
		return nil, errors.New("step_name is required")
	}
	t, err := s.database.GetTask(req.TaskID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task #%d: %v", req.TaskID, err)
	}
	if len(req.Items) == 0 {
		return nil, errors.New("publish_items: items list is empty")
	}
	items, err := task.NormalizeItems(req.Items)
	if err != nil {
		return nil, err
	}

	pc, err := s.getProjectContext(t.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to get project context for task #%d: %v", req.TaskID, err)
	}
	wf := pc.cfg.GetWorkflow(t.Workflow)
	if wf == nil {
		return nil, fmt.Errorf("task #%d: workflow %q not found", req.TaskID, t.Workflow)
	}
	if !wf.IsFanoutSource(req.StepName) {
		return nil, fmt.Errorf("step %q in workflow %q is not a fanout source: no fanout step names it in its from field", req.StepName, wf.Name)
	}

	rows, err := s.database.GetTaskStepRows(req.TaskID)
	if err != nil {
		return nil, fmt.Errorf("failed to read steps of task #%d: %v", req.TaskID, err)
	}
	row, ok := rows[req.StepName]
	if !ok {
		return nil, fmt.Errorf("step %q is not running (not started)", req.StepName)
	}
	if row.Status != "running" {
		return nil, fmt.Errorf("step %q is not running (status: %s)", req.StepName, row.Status)
	}

	if err := s.database.PublishItems(req.TaskID, req.StepName, items); err != nil {
		return nil, fmt.Errorf("failed to store published items: %v", err)
	}
	return items, nil
}
