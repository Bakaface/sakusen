package mcp

import (
	"context"
	"strings"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// UpdateStepContextArgs is the typed input schema for update_step_context.
//
// The tool writes the canonical context value for a task's currently-active
// workflow step. It is intended for use by Claude agents running inside a step
// that want to publish a long-form artifact (e.g. a refined PRD) without
// waiting for the post-session summarizer to compress their chat log.
//
// The daemon enforces the safety invariants documented below: step_name must
// match the task's active step. That covers both a running agent step and a
// tmux/human step paused at its approval gate — for the latter the engine has
// already cleared current_step and marked the step row 'completed', so the
// daemon resolves the active step as the one owning the live session (the
// paused step — see workflow.PausedStep) and writes there. There is no
// MCP-server-side notion of "the calling agent's task ID", so the client must
// pass task_id explicitly; misrouted updates are caught by the active-step
// check rather than by trusting the caller.
type UpdateStepContextArgs struct {
	TaskID   int64  `json:"task_id" jsonschema:"Task ID whose active step should receive the context. Required. Must be a task that's currently in a running step."`
	StepName string `json:"step_name" jsonschema:"Name of the workflow step receiving the context. Must match the task's currently-active step — updates to non-active steps are rejected."`
	Context  string `json:"context" jsonschema:"The canonical context value to publish for the step. Required (may be empty when mode is 'replace' to clear an existing value)."`
	Mode     string `json:"mode,omitempty" jsonschema:"'replace' (default) overwrites the existing context. 'append' concatenates the new value to the existing one with a newline separator."`
}

func registerUpdateStepContext(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"update_step_context",
		mcp.WithDescription("Publish the canonical context value for a task's currently-active workflow step. Use this when the agent is producing a long-form artifact (e.g. a refined PRD) that should be persisted verbatim rather than going through the post-session chat-log summarizer. The daemon rejects updates to non-active steps with a clear error. mode='append' is useful for incremental progress updates within a step; mode='replace' (default) is the typical end-of-step write."),
		mcp.WithInputSchema[UpdateStepContextArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args UpdateStepContextArgs) (*mcp.CallToolResult, error) {
		return handleUpdateStepContext(c, args)
	}))
}

func handleUpdateStepContext(c *client.Client, args UpdateStepContextArgs) (*mcp.CallToolResult, error) {
	if args.TaskID <= 0 {
		return resultErr("task_id must be a positive integer")
	}
	if strings.TrimSpace(args.StepName) == "" {
		return resultErr("step_name is required")
	}

	mode := strings.ToLower(strings.TrimSpace(args.Mode))
	if mode == "" {
		mode = "replace"
	}
	if mode != "replace" && mode != "append" {
		return resultErr("invalid mode %q: must be \"replace\" or \"append\"", args.Mode)
	}

	if err := c.UpdateActiveStepContext(args.TaskID, args.StepName, args.Context, mode); err != nil {
		return resultErr("update step context failed: %v", err)
	}

	return mcp.NewToolResultText("ok"), nil
}
