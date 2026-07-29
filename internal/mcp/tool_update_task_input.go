package mcp

import (
	"context"
	"strings"

	"github.com/Bakaface/sortie/internal/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// UpdateTaskInputArgs is the typed input schema for update_task_input.
type UpdateTaskInputArgs struct {
	TaskID int64  `json:"task_id" jsonschema:"Task ID to edit. Required."`
	Input  string `json:"input" jsonschema:"New task input. Replaces the existing input entirely. Seeds {{task.input}} in step prompts. May contain {{tasks.<id>.<field>}} references; the daemon validates them and auto-adds newly referenced tasks as blockers."`
}

func registerUpdateTaskInput(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"update_task_input",
		mcp.WithDescription("Replace a sortie task's input (the text that seeds {{task.input}} in step prompts). The daemon validates any {{tasks.<id>.<field>}} references against the new value before applying it (a bad reference leaves the input untouched) and auto-adds newly referenced tasks as blockers. Returns the updated TaskInfo as JSON."),
		mcp.WithInputSchema[UpdateTaskInputArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args UpdateTaskInputArgs) (*mcp.CallToolResult, error) {
		return handleUpdateTaskInput(c, args)
	}))
}

func handleUpdateTaskInput(c *client.Client, args UpdateTaskInputArgs) (*mcp.CallToolResult, error) {
	if args.TaskID <= 0 {
		return resultErr("task_id must be a positive integer")
	}
	if strings.TrimSpace(args.Input) == "" {
		return resultErr("input must not be empty")
	}

	task, err := c.UpdateTaskField(args.TaskID, "input", args.Input)
	if err != nil {
		return resultErr("update task input failed: %v", err)
	}
	return jsonResult(task)
}
