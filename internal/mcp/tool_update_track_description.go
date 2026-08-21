package mcp

import (
	"context"
	"strings"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// UpdateTrackDescriptionArgs is the typed input schema for
// update_track_description.
//
// Like update_track_context, the tool is deliberately own-track-only: it takes
// a task_id (not a track ref), and the daemon writes only to that task's own
// track after verifying the task has a track AND an active step.
// Arbitrary-track edits are a human operation (the `sakusen tracks
// set-description` CLI), not an agent one.
type UpdateTrackDescriptionArgs struct {
	TaskID      int64  `json:"task_id" jsonschema:"The calling task's ID (from $SAKUSEN_TASK_ID). Required. This tool only writes to the calling task's own track — the daemon rejects tasks with no track or no active step."`
	Description string `json:"description" jsonschema:"The track's new description: a stable one-liner stating what the track is for, used by routing agents to pick a track. Replaces the existing description entirely. Distinct from the accumulating context — keep implementation notes and progress out of it."`
}

func registerUpdateTrackDescription(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"update_track_description",
		mcp.WithDescription("Set the description of the calling task's own track — the stable one-liner that states the track's purpose and primes routing agents choosing a track in list_tracks. Only writes to the calling task's track — pass your own task_id (from $SAKUSEN_TASK_ID); the daemon rejects tasks with no track or no active step. Replace-only: the new description overwrites the old one. Use update_track_context, not this tool, to accumulate working context."),
		mcp.WithInputSchema[UpdateTrackDescriptionArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args UpdateTrackDescriptionArgs) (*mcp.CallToolResult, error) {
		return handleUpdateTrackDescription(c, args)
	}))
}

func handleUpdateTrackDescription(c *client.Client, args UpdateTrackDescriptionArgs) (*mcp.CallToolResult, error) {
	if args.TaskID <= 0 {
		return resultErr("task_id must be a positive integer")
	}
	// Agents have no reason to blank a track's routing signal; clearing it is a
	// human operation (`sakusen tracks set-description`).
	if strings.TrimSpace(args.Description) == "" {
		return resultErr("description must not be empty")
	}

	if err := c.UpdateTaskTrackDescription(args.TaskID, args.Description); err != nil {
		return resultErr("update track description failed: %v", err)
	}

	return mcp.NewToolResultText("ok"), nil
}
