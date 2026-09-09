package mcp

import (
	"context"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RunRoutineArgs is the typed input schema for run_routine.
type RunRoutineArgs struct {
	Name        string `json:"name" jsonschema:"Routine name as declared under routines: in .sakusen.yml — call list_routines to see them."`
	ProjectPath string `json:"project_path,omitempty" jsonschema:"Absolute path to the project repo root. Defaults to the git toplevel of the MCP process's cwd."`
	Input       string `json:"input,omitempty" jsonschema:"Task input for this run, overriding the routine's own input:. Required when the routine's requires_input is true."`
}

func registerRunRoutine(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"run_routine",
		mcp.WithDescription("Run a sakusen routine now, creating one task from its binding. Returns the created TaskInfo as JSON. Everything but the input is fixed by the routine: workflow, pins and priority. Scheduled routines are unaffected — a run now does not advance or consume their cadence."),
		mcp.WithInputSchema[RunRoutineArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args RunRoutineArgs) (*mcp.CallToolResult, error) {
		return handleRunRoutine(c, args)
	}))
}

func handleRunRoutine(c *client.Client, args RunRoutineArgs) (*mcp.CallToolResult, error) {
	if args.Name == "" {
		return resultErr("name is required")
	}
	projectPath, err := resolveProjectPath(args.ProjectPath)
	if err != nil {
		return resultErr("%v", err)
	}

	t, err := c.RunRoutine(projectPath, args.Name, args.Input)
	if err != nil {
		return resultErr("run routine failed: %v", err)
	}
	return jsonResult(t)
}
