package mcp

import (
	"context"

	"github.com/Bakaface/sortie/internal/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ListWorkflowsArgs is the typed input schema for list_workflows.
type ListWorkflowsArgs struct {
	ProjectPath string `json:"project_path,omitempty" jsonschema:"Absolute path to the project repo root. Defaults to the git toplevel of the MCP process's cwd."`
}

func registerListWorkflows(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"list_workflows",
		mcp.WithDescription("List the workflows configured for a sortie project. Returns a flat 'workflows' list; pass any name to create_task's workflow argument. Each entry includes name, description (human-readable metadata), any pinned New Task fields (input/worktree/branch/checkout/target — a non-empty 'input' means the workflow supplies the task input itself), fully_spec, first_step_is_tmux, and a per-step summary (each step carries its own name and description)."),
		mcp.WithInputSchema[ListWorkflowsArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args ListWorkflowsArgs) (*mcp.CallToolResult, error) {
		return handleListWorkflows(c, args)
	}))
}

func handleListWorkflows(c *client.Client, args ListWorkflowsArgs) (*mcp.CallToolResult, error) {
	projectPath, err := resolveProjectPath(args.ProjectPath)
	if err != nil {
		return resultErr("%v", err)
	}

	resp, err := c.ListWorkflows(projectPath)
	if err != nil {
		return resultErr("list workflows failed: %v", err)
	}

	return jsonResult(resp)
}
