package mcp

import (
	"context"
	"strings"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RetryTaskArgs is the typed input schema for retry_task.
type RetryTaskArgs struct {
	TaskID   int64  `json:"task_id" jsonschema:"Task ID to retry. Required."`
	StepName string `json:"step_name,omitempty" jsonschema:"Workflow step to restart from. Earlier completed steps (and their captured contexts) are preserved. Empty restarts the task from the beginning."`
}

func registerRetryTask(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"retry_task",
		mcp.WithDescription("Retry a sakusen task. Stops any running agent for the task and resets it so the daemon re-runs it — either from the beginning (default) or from a specific workflow step (step_name), preserving earlier completed steps and their contexts. Use get_task with include_steps to discover step names. Returns the post-reset TaskInfo as JSON."),
		mcp.WithInputSchema[RetryTaskArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args RetryTaskArgs) (*mcp.CallToolResult, error) {
		return handleRetryTask(c, args)
	}))
}

func handleRetryTask(c *client.Client, args RetryTaskArgs) (*mcp.CallToolResult, error) {
	if args.TaskID <= 0 {
		return resultErr("task_id must be a positive integer")
	}

	task, err := c.RetryTask(args.TaskID, strings.TrimSpace(args.StepName))
	if err != nil {
		return resultErr("retry task failed: %v", err)
	}
	return jsonResult(task)
}
