package mcp

import (
	"context"
	"strings"
	"time"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/Bakaface/sakusen/internal/task"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// UpdateTaskArgs is the typed input schema for update_task. Every editable
// field is optional; omitted (empty/absent) fields are left untouched.
type UpdateTaskArgs struct {
	TaskID          int64   `json:"task_id" jsonschema:"Task ID to edit. Required."`
	Input           string  `json:"input,omitempty" jsonschema:"New task input, replacing the existing one entirely. Seeds {{task.input}} in step prompts. May contain {{tasks.<id>.<field>}} references; the daemon validates them before applying and auto-adds newly referenced tasks as blockers."`
	Title           string  `json:"title,omitempty" jsonschema:"New task title, replacing the existing one entirely."`
	Priority        string  `json:"priority,omitempty" jsonschema:"New scheduling priority: low, medium, high, or urgent."`
	Track           string  `json:"track,omitempty" jsonschema:"Track to attach (slug or numeric ID), replacing any current track. The track's context becomes available to step prompts via {{track.context}}. Pass 'none' to detach the task from its track."`
	NotBefore       string  `json:"not_before,omitempty" jsonschema:"Keep the task pending until this time (RFC3339 with offset, e.g. '2026-01-02T15:04:05Z'). Only allowed while the task is pending. Mutually exclusive with clear_not_before."`
	ClearNotBefore  bool    `json:"clear_not_before,omitempty" jsonschema:"Remove the task's not-before time so it can start on the next poll. Only allowed while the task is pending."`
	AddBlockedBy    []int64 `json:"add_blocked_by,omitempty" jsonschema:"Task IDs to add as blockers — this task will not start until they complete. Adding an already-present blocker is a no-op; the daemon rejects edges that would create a cycle."`
	RemoveBlockedBy []int64 `json:"remove_blocked_by,omitempty" jsonschema:"Task IDs to remove from this task's blockers. Removing the last incomplete blocker can make the task immediately eligible to run."`
}

func registerUpdateTask(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"update_task",
		mcp.WithDescription("Edit an existing sakusen task: its input, title, priority, track, not-before time (pending tasks only), and/or blocked_by dependencies. At least one field must be supplied; omitted fields are left untouched. Changes are applied one at a time in the order input, title, priority, track, not-before, dependency removals, dependency additions — so an ID listed in both add_blocked_by and remove_blocked_by ends up a blocker, and a failure part-way through leaves the earlier changes applied (the error says so). Returns the updated TaskInfo as JSON."),
		mcp.WithInputSchema[UpdateTaskArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args UpdateTaskArgs) (*mcp.CallToolResult, error) {
		return handleUpdateTask(c, args)
	}))
}

func handleUpdateTask(c *client.Client, args UpdateTaskArgs) (*mcp.CallToolResult, error) {
	if args.TaskID <= 0 {
		return resultErr("task_id must be a positive integer")
	}
	if args.Input == "" && args.Title == "" && args.Priority == "" && args.Track == "" &&
		args.NotBefore == "" && !args.ClearNotBefore &&
		len(args.AddBlockedBy) == 0 && len(args.RemoveBlockedBy) == 0 {
		return resultErr("at least one of input, title, priority, track, not_before, clear_not_before, add_blocked_by or remove_blocked_by must be supplied")
	}
	if args.NotBefore != "" && args.ClearNotBefore {
		return resultErr("not_before and clear_not_before are mutually exclusive")
	}
	var notBefore *time.Time
	if args.NotBefore != "" {
		nb, err := task.ParseNotBefore(args.NotBefore)
		if err != nil {
			return resultErr("%v", err)
		}
		notBefore = &nb
	}
	if args.Input != "" && strings.TrimSpace(args.Input) == "" {
		return resultErr("input must not be empty")
	}
	if args.Title != "" && strings.TrimSpace(args.Title) == "" {
		return resultErr("title must not be empty")
	}
	if args.Priority != "" && !task.IsValidPriority(args.Priority) {
		return resultErr("invalid priority %q: must be low, medium, high, or urgent", args.Priority)
	}
	if args.Track != "" && strings.TrimSpace(args.Track) == "" {
		return resultErr("track must not be empty")
	}
	for _, ids := range [][]int64{args.AddBlockedBy, args.RemoveBlockedBy} {
		for _, id := range ids {
			if id <= 0 {
				return resultErr("dependency task IDs must be positive integers, got %d", id)
			}
			if id == args.TaskID {
				return resultErr("task #%d cannot depend on itself", args.TaskID)
			}
		}
	}

	// Every change is a separate daemon request, so a failure part-way through
	// leaves the earlier ones applied — the errors say so. Removals precede
	// additions so an ID in both lists ends up a blocker (the safer final
	// state: blocked rather than unexpectedly runnable).
	var updated *daemon.TaskInfo
	apply := func(t *daemon.TaskInfo) { updated = t }

	if args.Input != "" {
		t, err := c.UpdateTaskField(args.TaskID, "input", args.Input)
		if err != nil {
			return resultErr("failed to update input: %v", err)
		}
		apply(t)
	}
	if args.Title != "" {
		t, err := c.UpdateTaskField(args.TaskID, "title", args.Title)
		if err != nil {
			return resultErr("failed to update title: %v%s", err, appliedSuffix(updated))
		}
		apply(t)
	}
	if args.Priority != "" {
		t, err := c.UpdateTaskPriority(args.TaskID, args.Priority)
		if err != nil {
			return resultErr("failed to update priority: %v%s", err, appliedSuffix(updated))
		}
		apply(t)
	}
	if args.Track != "" {
		t, err := c.UpdateTaskField(args.TaskID, "track", args.Track)
		if err != nil {
			return resultErr("failed to update track: %v%s", err, appliedSuffix(updated))
		}
		apply(t)
	}
	if notBefore != nil || args.ClearNotBefore {
		t, err := c.UpdateTaskNotBefore(args.TaskID, notBefore)
		if err != nil {
			return resultErr("failed to update not_before: %v%s", err, appliedSuffix(updated))
		}
		apply(t)
	}
	for _, id := range args.RemoveBlockedBy {
		t, err := c.RemoveTaskDependency(args.TaskID, id)
		if err != nil {
			return resultErr("failed to remove dependency on task #%d: %v%s", id, err, appliedSuffix(updated))
		}
		apply(t)
	}
	for _, id := range args.AddBlockedBy {
		t, err := c.AddTaskDependency(args.TaskID, id)
		if err != nil {
			return resultErr("failed to add dependency on task #%d: %v%s", id, err, appliedSuffix(updated))
		}
		apply(t)
	}

	return jsonResult(updated)
}

// appliedSuffix warns that the call was partially performed, but only once at
// least one change actually landed.
func appliedSuffix(updated *daemon.TaskInfo) string {
	if updated == nil {
		return ""
	}
	return " (earlier changes in this call were already applied)"
}
