package mcp

import (
	"context"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RetryMergeArgs is the typed input schema for retry_merge.
type RetryMergeArgs struct {
	TaskID int64 `json:"task_id" jsonschema:"ID of the merge-failed task whose merge to retry. Required."`
}

func registerRetryMerge(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"retry_merge",
		mcp.WithDescription("Retry only the merge of a merge-failed sakusen task: re-runs finalization (git.verify_command, merge with rebase/conflict handling, summarize, cleanup) without re-running any agent step — no step rows or contexts change. Use it once the cause of the merge failure is fixed (a stale tool cache, a conflict resolved by hand, a dirty target). Only merge-failed tasks are accepted. The task moves to finalizing and ends completed, or merge-failed again with the new error and its worktree kept; poll get_task or wait_for_task_status for the outcome. Returns the TaskInfo as JSON."),
		mcp.WithInputSchema[RetryMergeArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args RetryMergeArgs) (*mcp.CallToolResult, error) {
		return handleRetryMerge(c, args)
	}))
}

func handleRetryMerge(c *client.Client, args RetryMergeArgs) (*mcp.CallToolResult, error) {
	if args.TaskID <= 0 {
		return resultErr("task_id must be a positive integer")
	}

	task, err := c.RetryMerge(args.TaskID)
	if err != nil {
		return resultErr("retry merge failed: %v", err)
	}
	return jsonResult(task)
}
