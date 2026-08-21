package mcp

import (
	"context"
	"strings"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// UpdateTrackContextArgs is the typed input schema for update_track_context.
//
// The tool is deliberately own-track-only: it takes a task_id (not a track
// ref), and the daemon writes only to that task's own track after verifying
// the task has a track AND an active step — the same enforcement shape as
// update_step_context. Arbitrary-track edits are a human operation (the
// `sakusen tracks set-context` CLI), not an agent one.
type UpdateTrackContextArgs struct {
	TaskID  int64  `json:"task_id" jsonschema:"The calling task's ID (from $SAKUSEN_TASK_ID). Required. This tool only writes to the calling task's own track — the daemon rejects tasks with no track or no active step."`
	Context string `json:"context" jsonschema:"Context to publish to the track. WARNING: flows verbatim into the prompts of every future task attached to this track or its children."`
	Mode    string `json:"mode,omitempty" jsonschema:"'append' (default) concatenates with a blank-line separator — the normal way to accumulate sprint context. 'replace' overwrites the track's entire own context."`
}

func registerUpdateTrackContext(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"update_track_context",
		mcp.WithDescription("Publish context to the calling task's own track so future tasks on the same track (or its children) see it via {{track.context}}. Only writes to the calling task's track — pass your own task_id (from $SAKUSEN_TASK_ID); the daemon rejects tasks with no track or no active step. Default mode is 'append' (gradual accumulation); 'replace' overwrites the track's entire own context and can destroy context shared with other tasks. WARNING: whatever you write flows verbatim into other agents' prompts, persistently."),
		mcp.WithInputSchema[UpdateTrackContextArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args UpdateTrackContextArgs) (*mcp.CallToolResult, error) {
		return handleUpdateTrackContext(c, args)
	}))
}

func handleUpdateTrackContext(c *client.Client, args UpdateTrackContextArgs) (*mcp.CallToolResult, error) {
	if args.TaskID <= 0 {
		return resultErr("task_id must be a positive integer")
	}

	mode := strings.ToLower(strings.TrimSpace(args.Mode))
	if mode == "" {
		mode = "append"
	}
	if mode != "replace" && mode != "append" {
		return resultErr("invalid mode %q: must be \"replace\" or \"append\"", args.Mode)
	}

	if err := c.UpdateTaskTrackContext(args.TaskID, args.Context, mode); err != nil {
		return resultErr("update track context failed: %v", err)
	}

	return mcp.NewToolResultText("ok"), nil
}
