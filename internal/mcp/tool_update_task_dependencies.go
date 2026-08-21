package mcp

import (
	"context"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// UpdateTaskDependenciesArgs is the typed input schema for update_task_dependencies.
type UpdateTaskDependenciesArgs struct {
	TaskID int64   `json:"task_id" jsonschema:"Task ID whose blocked_by dependencies to modify. Required."`
	Add    []int64 `json:"add,omitempty" jsonschema:"Task IDs to add as blockers — this task will not start until they complete. The daemon rejects edges that would create a cycle."`
	Remove []int64 `json:"remove,omitempty" jsonschema:"Task IDs to remove from this task's blockers. Removing the last incomplete blocker makes the task eligible to run."`
}

func registerUpdateTaskDependencies(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"update_task_dependencies",
		mcp.WithDescription("Add and/or remove blocked_by dependencies on an existing sakusen task. Removals are applied before additions, so an ID present in both lists ends up as a blocker. Adding an already-present blocker is a no-op; cycles are rejected by the daemon. Note: removing blockers can make the task immediately eligible to run. Returns the updated TaskInfo as JSON."),
		mcp.WithInputSchema[UpdateTaskDependenciesArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args UpdateTaskDependenciesArgs) (*mcp.CallToolResult, error) {
		return handleUpdateTaskDependencies(c, args)
	}))
}

func handleUpdateTaskDependencies(c *client.Client, args UpdateTaskDependenciesArgs) (*mcp.CallToolResult, error) {
	if args.TaskID <= 0 {
		return resultErr("task_id must be a positive integer")
	}
	if len(args.Add) == 0 && len(args.Remove) == 0 {
		return resultErr("at least one of add or remove must be non-empty")
	}
	for _, id := range append(append([]int64{}, args.Add...), args.Remove...) {
		if id <= 0 {
			return resultErr("dependency task IDs must be positive integers, got %d", id)
		}
		if id == args.TaskID {
			return resultErr("task #%d cannot depend on itself", args.TaskID)
		}
	}

	// Each edge is a separate daemon request; on failure, edges already
	// processed in this call stay applied. Removals go first so that an ID
	// listed in both add and remove ends up as a blocker (the safer final
	// state: blocked rather than unexpectedly runnable).
	var task *daemon.TaskInfo
	for _, id := range args.Remove {
		t, err := c.RemoveTaskDependency(args.TaskID, id)
		if err != nil {
			return resultErr("failed to remove dependency on task #%d: %v (earlier changes in this call were already applied)", id, err)
		}
		task = t
	}
	for _, id := range args.Add {
		t, err := c.AddTaskDependency(args.TaskID, id)
		if err != nil {
			return resultErr("failed to add dependency on task #%d: %v (earlier changes in this call were already applied)", id, err)
		}
		task = t
	}

	return jsonResult(task)
}
