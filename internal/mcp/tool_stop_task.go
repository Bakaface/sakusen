package mcp

import (
	"context"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// StopTaskArgs is the typed input schema for stop_task.
type StopTaskArgs struct {
	TaskID int64 `json:"task_id" jsonschema:"Task ID whose agent should be stopped. Required."`
}

func registerStopTask(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"stop_task",
		mcp.WithDescription("Stop the agent running a sakusen task. Only that task's own agent is stopped; its worktree, branch and captured step contexts survive. There is no 'stopped' status — the task keeps whatever status it had (typically 'running'), so it will not progress on its own: re-queue it with retry_task (a daemon restart also recovers a stranded running task to pending). Returns the task's TaskInfo as JSON."),
		mcp.WithInputSchema[StopTaskArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args StopTaskArgs) (*mcp.CallToolResult, error) {
		return handleStopTask(c, args)
	}))
}

func handleStopTask(c *client.Client, args StopTaskArgs) (*mcp.CallToolResult, error) {
	if args.TaskID <= 0 {
		return resultErr("task_id must be a positive integer")
	}

	task, err := c.StopTask(args.TaskID)
	if err != nil {
		return resultErr("stop task failed: %v", err)
	}
	return jsonResult(task)
}
