package mcp

import (
	"context"
	"time"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ListRoutinesArgs is the typed input schema for list_routines.
type ListRoutinesArgs struct {
	ProjectPath string `json:"project_path,omitempty" jsonschema:"Absolute path to the project repo root. Defaults to the git toplevel of the MCP process's cwd."`
}

// routineSummary is the agent-facing projection of a routine. It deliberately
// omits the DB row id and the project ids: routines are addressed by name.
type routineSummary struct {
	Name          string     `json:"name"`
	Description   string     `json:"description,omitempty"`
	Workflow      string     `json:"workflow"`
	Cadence       string     `json:"cadence,omitempty"`
	Input         string     `json:"input,omitempty"`
	RequiresInput bool       `json:"requires_input"`
	Paused        bool       `json:"paused"`
	NextFireAt    *time.Time `json:"next_fire_at,omitempty"`
	LastFiredAt   *time.Time `json:"last_fired_at,omitempty"`
	LastTaskID    *int64     `json:"last_task_id,omitempty"`
}

func registerListRoutines(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"list_routines",
		mcp.WithDescription("List the routines configured for a sakusen project. A routine binds a workflow to a way of invoking it: it fixes the workflow, its pins (input/worktree/branch/checkout/target) and its priority, so running one takes nothing but its name. Pass any name to run_routine. A routine with a 'cadence' also fires on that schedule; one without is on-demand only. 'requires_input' true means run_routine's input argument is mandatory for that routine."),
		mcp.WithInputSchema[ListRoutinesArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args ListRoutinesArgs) (*mcp.CallToolResult, error) {
		return handleListRoutines(c, args)
	}))
}

func handleListRoutines(c *client.Client, args ListRoutinesArgs) (*mcp.CallToolResult, error) {
	projectPath, err := resolveProjectPath(args.ProjectPath)
	if err != nil {
		return resultErr("%v", err)
	}

	routines, err := c.ListRoutines(projectPath)
	if err != nil {
		return resultErr("list routines failed: %v", err)
	}

	out := make([]routineSummary, 0, len(routines))
	for _, r := range routines {
		out = append(out, routineSummary{
			Name:          r.Name,
			Description:   r.Description,
			Workflow:      r.WorkflowRef,
			Cadence:       r.Cadence,
			Input:         r.Input,
			RequiresInput: r.RequiresInput,
			Paused:        r.Paused,
			NextFireAt:    nextFireOrNil(r),
			LastFiredAt:   r.LastFiredAt,
			LastTaskID:    r.LastTaskID,
		})
	}
	return jsonResult(map[string]any{"routines": out})
}

// nextFireOrNil drops the next-fire time for on-demand routines, whose row
// carries one only because the column is NOT NULL.
func nextFireOrNil(r daemon.PeriodicInfo) *time.Time {
	if r.Cadence == "" {
		return nil
	}
	t := r.NextFireAt
	return &t
}
