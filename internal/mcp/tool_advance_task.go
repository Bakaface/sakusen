package mcp

import (
	"context"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// AdvanceTaskArgs is the typed input schema for advance_task.
type AdvanceTaskArgs struct {
	TaskID int64 `json:"task_id" jsonschema:"Task ID to advance. The task must be in tmux state. Required."`
}

func registerAdvanceTask(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"advance_task",
		mcp.WithDescription("Advance a sakusen task paused on a tmux step. Marks the tmux step as done: the daemon kills the task's tmux session and either resumes the workflow at the next step or, when the tmux step was the last one, runs full finalization (merge + summarize + cleanup). Fails if the task is not in tmux state. Returns the daemon's outcome message."),
		mcp.WithInputSchema[AdvanceTaskArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args AdvanceTaskArgs) (*mcp.CallToolResult, error) {
		return handleAdvanceTask(c, args)
	}))
}

func handleAdvanceTask(c *client.Client, args AdvanceTaskArgs) (*mcp.CallToolResult, error) {
	if args.TaskID <= 0 {
		return resultErr("task_id must be a positive integer")
	}

	outcome, err := c.AdvanceTask(args.TaskID)
	if err != nil {
		return resultErr("advance task failed: %v", err)
	}
	return mcp.NewToolResultText(outcome), nil
}
