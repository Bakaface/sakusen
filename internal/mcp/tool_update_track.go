package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// UpdateTrackArgs is the typed input schema for update_track.
//
// The tool is deliberately own-track-only: it takes a task_id (not a track
// ref), and the daemon writes only to that task's own track after verifying
// the task has a track AND an active step — the same enforcement shape as
// update_step_context. Arbitrary-track edits are a human operation (the
// `sakusen tracks set-context` / `set-description` CLI), not an agent one.
type UpdateTrackArgs struct {
	TaskID      int64  `json:"task_id,omitempty" jsonschema:"The calling task's ID. Defaults to $SAKUSEN_TASK_ID (set by the workflow engine for the active step). This tool only writes to the calling task's own track — the daemon rejects tasks with no track or no active step."`
	Context     string `json:"context,omitempty" jsonschema:"Context to publish to the track. WARNING: flows verbatim into the prompts of every future task attached to this track or its children."`
	ContextMode string `json:"context_mode,omitempty" jsonschema:"'append' (default) concatenates with a blank-line separator — the normal way to accumulate sprint context. 'replace' overwrites the track's entire own context and can destroy context shared with other tasks."`
	Description string `json:"description,omitempty" jsonschema:"The track's new description: a stable one-liner stating what the track is for, used by routing agents to pick a track in list_tracks. Replaces the existing description entirely. Distinct from the accumulating context — keep implementation notes and progress out of it."`
}

func registerUpdateTrack(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"update_track",
		mcp.WithDescription("Edit the calling task's own track: publish context (so future tasks on the same track or its children see it via {{track.context}}) and/or set the description that routing agents read in list_tracks. At least one of context or description must be supplied. Only writes to the calling task's track — task_id defaults to $SAKUSEN_TASK_ID; the daemon rejects tasks with no track or no active step. Context and description are applied as separate daemon calls, so a failure on the second leaves the first applied. WARNING: whatever you write into context flows verbatim into other agents' prompts, persistently."),
		mcp.WithInputSchema[UpdateTrackArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args UpdateTrackArgs) (*mcp.CallToolResult, error) {
		return handleUpdateTrack(c, args)
	}))
}

func handleUpdateTrack(c *client.Client, args UpdateTrackArgs) (*mcp.CallToolResult, error) {
	taskID, err := resolveTaskID(args.TaskID)
	if err != nil {
		return resultErr("%v", err)
	}
	if args.Context == "" && args.Description == "" {
		return resultErr("at least one of context or description must be supplied")
	}
	// Agents have no reason to blank a track's routing signal; clearing it is a
	// human operation (`sakusen tracks set-description`).
	if args.Description != "" && strings.TrimSpace(args.Description) == "" {
		return resultErr("description must not be empty")
	}

	mode := strings.ToLower(strings.TrimSpace(args.ContextMode))
	if mode == "" {
		mode = "append"
	}
	if mode != "replace" && mode != "append" {
		return resultErr("invalid context_mode %q: must be \"replace\" or \"append\"", args.ContextMode)
	}

	var applied []string
	if args.Context != "" {
		if err := c.UpdateTaskTrackContext(taskID, args.Context, mode); err != nil {
			return resultErr("update track context failed: %v", err)
		}
		applied = append(applied, fmt.Sprintf("context (%s)", mode))
	}
	if args.Description != "" {
		if err := c.UpdateTaskTrackDescription(taskID, args.Description); err != nil {
			suffix := ""
			if len(applied) > 0 {
				suffix = " (the context update in this call was already applied)"
			}
			return resultErr("update track description failed: %v%s", err, suffix)
		}
		applied = append(applied, "description")
	}

	return mcp.NewToolResultText(fmt.Sprintf("ok: updated %s on task #%d's track", strings.Join(applied, " and "), taskID)), nil
}
