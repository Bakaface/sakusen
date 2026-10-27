package mcp

import (
	"context"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/Bakaface/sakusen/internal/task"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// PublishItemArg is one item of a publish_items call.
type PublishItemArg struct {
	Input string   `json:"input" jsonschema:"What the child task spawned for this item should do. Required."`
	Slug  string   `json:"slug,omitempty" jsonschema:"Stable item identifier (letters, digits, '.', '_', '-'; max 64 chars) used by needs and by templates as {{item.slug}}. Defaults to item-<n> (1-based position)."`
	Title string   `json:"title,omitempty" jsonschema:"Short human-readable title for the item; available to templates as {{item.title}}."`
	Needs []string `json:"needs,omitempty" jsonschema:"Slugs of items whose children must complete before this item's child starts. Must name other items in this list; cycles are rejected."`
}

// PublishItemsArgs is the typed input schema for publish_items.
type PublishItemsArgs struct {
	TaskID   int64            `json:"task_id,omitempty" jsonschema:"Task publishing the items. Defaults to $SAKUSEN_TASK_ID (set by the workflow engine for the running step)."`
	StepName string           `json:"step_name,omitempty" jsonschema:"Step publishing the items. Defaults to $SAKUSEN_STEP. Must be the running step and be named in a later fanout step's from:."`
	Items    []PublishItemArg `json:"items" jsonschema:"The items to publish, in order. Required and non-empty."`
}

func registerPublishItems(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"publish_items",
		mcp.WithDescription("Publish the list of work items a later `fanout:` step in this workflow spawns child tasks from (one child per item; needs orders them). Call it from the step named in that fanout step's `from:`, while the step is still running. Each call replaces any list the step published earlier. The daemon validates the list (non-empty inputs, unique slugs, known needs, no cycles) and returns the stored items with missing slugs filled in."),
		mcp.WithInputSchema[PublishItemsArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args PublishItemsArgs) (*mcp.CallToolResult, error) {
		return handlePublishItems(c, args)
	}))
}

// publishItemsRequest resolves args against the engine env vars into a
// daemon request.
func publishItemsRequest(args PublishItemsArgs) (daemon.PublishItemsRequest, error) {
	taskID, err := resolveTaskID(args.TaskID)
	if err != nil {
		return daemon.PublishItemsRequest{}, err
	}
	stepName, err := resolveStepName(args.StepName)
	if err != nil {
		return daemon.PublishItemsRequest{}, err
	}
	items := make([]task.PublishedItem, len(args.Items))
	for i, it := range args.Items {
		items[i] = task.PublishedItem{Slug: it.Slug, Title: it.Title, Input: it.Input, Needs: it.Needs}
	}
	return daemon.PublishItemsRequest{TaskID: taskID, StepName: stepName, Items: items}, nil
}

func handlePublishItems(c *client.Client, args PublishItemsArgs) (*mcp.CallToolResult, error) {
	req, err := publishItemsRequest(args)
	if err != nil {
		return resultErr("%v", err)
	}
	items, err := c.PublishItems(req.TaskID, req.StepName, req.Items)
	if err != nil {
		return resultErr("publish items failed: %v", err)
	}
	return jsonResult(daemon.PublishItemsResponse{Count: len(items), Items: items})
}
