package daemon

import (
	"fmt"
	"net"
	"strings"

	"github.com/Bakaface/sakusen/internal/config"
)

// handleListWorkflows returns the flat list of workflows for the project rooted
// at req.ProjectPath. The project is resolved (or created) the same way
// CreateTask resolves it, so the response reflects the daemon's view of the
// config rather than a fresh read by the caller.
func (s *Server) handleListWorkflows(conn net.Conn, req ListWorkflowsRequest) {
	projectPath := strings.TrimSpace(req.ProjectPath)
	if projectPath == "" {
		s.sendError(conn, "project_path is required")
		return
	}

	proj, err := s.database.GetOrCreateProject(projectPath)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to resolve project: %v", err))
		return
	}

	pc, err := s.getProjectContext(proj.ID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to load project config: %v", err))
		return
	}

	resp := ListWorkflowsResponse{
		ProjectPath: proj.Path,
		ProjectName: pc.cfg.Project.Name,
		Workflows:   summarizeWorkflows(pc.cfg, pc.cfg.Workflows),
	}

	// If no workflows are configured, expose the built-in default so the MCP
	// caller sees the same surface as `sakusen create -w default`.
	if len(resp.Workflows) == 0 {
		def := config.DefaultWorkflow()
		resp.Workflows = summarizeWorkflows(pc.cfg, []config.WorkflowConfig{def})
	}

	s.sendMessage(conn, MsgListWorkflows, resp)
}

func summarizeWorkflows(cfg *config.Config, workflows []config.WorkflowConfig) []WorkflowSummary {
	out := make([]WorkflowSummary, 0, len(workflows))
	for i := range workflows {
		wf := &workflows[i]
		summary := WorkflowSummary{
			Name:            wf.Name,
			Description:     wf.Description,
			Input:           wf.Input,
			Worktree:        wf.Worktree,
			Branch:          wf.Branch,
			Checkout:        wf.Checkout,
			Target:          wf.Target,
			FullySpec:       wf.IsFullySpec(),
			Agent:           wf.Agent,
			FirstStepIsTmux: cfg.FirstStepIsTmux(wf),
			Hidden:          wf.Hidden,
			Source:          wf.Source,
			Steps:           make([]WorkflowStepSummary, 0, len(wf.Steps)),
		}
		for j := range wf.Steps {
			step := &wf.Steps[j]
			summary.Steps = append(summary.Steps, WorkflowStepSummary{
				Name:        step.Name,
				Description: step.Description,
				Mode:        step.Mode,
				Agent:       cfg.StepAgentSlug(wf, step),
				Tmux:        cfg.StepIsTmux(wf, step),
				Human:       step.Human,
				Loop:        step.Loop != nil,
			})
		}
		out = append(out, summary)
	}
	return out
}
