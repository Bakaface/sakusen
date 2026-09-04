package mcp

import (
	"context"
	"strings"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ContinueTaskArgs is the typed input schema for continue_task.
type ContinueTaskArgs struct {
	TaskID   int64  `json:"task_id" jsonschema:"Task ID to continue. Required. Must be terminal: completed, failed, or merge-failed."`
	Workflow string `json:"workflow" jsonschema:"Workflow to re-run the task under — call list_workflows to see available workflows. Required."`
	Prompt   string `json:"prompt,omitempty" jsonschema:"New task input for the follow-up run, replacing the existing input. Omit to re-run against the current input."`
}

func registerContinueTask(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"continue_task",
		mcp.WithDescription("Re-run a finished sakusen task under a (possibly different) workflow, optionally with new input — the way to follow up on a completed task without creating a new one. The task is reset to pending under the named workflow and its step rows are cleared; the branch and worktree survive, so the new run builds on the existing work. Only terminal tasks (completed, failed, merge-failed) are accepted: the daemon rejects anything else, including tasks paused at a human approval gate. Use retry_task to re-run a step of a task that is not finished. Returns the post-reset TaskInfo as JSON."),
		mcp.WithInputSchema[ContinueTaskArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args ContinueTaskArgs) (*mcp.CallToolResult, error) {
		return handleContinueTask(c, args)
	}))
}

func handleContinueTask(c *client.Client, args ContinueTaskArgs) (*mcp.CallToolResult, error) {
	if args.TaskID <= 0 {
		return resultErr("task_id must be a positive integer")
	}
	// Same rule as create_task: never let a continue silently fall back to the
	// daemon's no-workflow legacy tmux path.
	workflow := strings.TrimSpace(args.Workflow)
	if workflow == "" {
		return resultErr("workflow is required — call list_workflows to see available workflows")
	}

	task, err := c.ContinueTaskTerminalOnly(args.TaskID, workflow, args.Prompt)
	if err != nil {
		return resultErr("continue task failed: %v", err)
	}
	return jsonResult(task)
}
