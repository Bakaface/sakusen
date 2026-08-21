package mcp

import (
	"context"
	"strings"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// CreateTrackArgs is the typed input schema for create_track.
type CreateTrackArgs struct {
	Name        string `json:"name" jsonschema:"Human-readable track name. Slugified (lowercase, kebab-case) to form the canonical track handle."`
	ParentTrack string `json:"parent_track,omitempty" jsonschema:"Parent track (slug or numeric ID). The parent's context is automatically concatenated before this track's in {{track.context}}. Parent must be same-or-broader scope: a project track may have a global parent, but not vice versa."`
	Scope       string `json:"scope,omitempty" jsonschema:"'project' (default) scopes the track to project_path's project; 'global' makes it attachable from any project."`
	Workflow    string `json:"workflow,omitempty" jsonschema:"Workflow to run for tasks attached to this track when they don't specify one explicitly. May reference a namespaced track workflow like '<slug>:<name>' from .sakusen/tracks/<slug>/workflows/."`
	Description string `json:"description,omitempty" jsonschema:"A stable one-liner stating what this track is for. Routing agents read it from list_tracks to pick a track for a new task, so state the scope, not the current state of work. Distinct from context: it is never injected into agent prompts and does not accumulate."`
	Context     string `json:"context,omitempty" jsonschema:"Initial context seed. WARNING: track context flows verbatim into the prompts of every future task attached to this track (or its children) — treat it as a persistent cross-task prompt surface."`
	ProjectPath string `json:"project_path,omitempty" jsonschema:"Project repo root. Required for scope 'project'; defaults to the git toplevel of the MCP process's cwd."`
}

func registerCreateTrack(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"create_track",
		mcp.WithDescription("Create a new track — a named, mutable context container that tasks attach to at create time. A track's (parent-concatenated) context is injectable into workflow step prompts via {{track.context}} / {{track.own_context}}, so agents can accumulate shared context across a stream of related tasks. Set description to a stable one-liner stating the track's purpose: it is what routing agents see in list_tracks when picking a track. Returns the created track as JSON."),
		mcp.WithInputSchema[CreateTrackArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args CreateTrackArgs) (*mcp.CallToolResult, error) {
		return handleCreateTrack(c, args)
	}))
}

func handleCreateTrack(c *client.Client, args CreateTrackArgs) (*mcp.CallToolResult, error) {
	if strings.TrimSpace(args.Name) == "" {
		return resultErr("name is required")
	}

	scope := strings.ToLower(strings.TrimSpace(args.Scope))
	if scope != "" && scope != "project" && scope != "global" {
		return resultErr("invalid scope %q: must be \"project\" or \"global\"", args.Scope)
	}
	global := scope == "global"

	// Resolve the project path (required for project scope). For global
	// tracks it's best-effort and the daemon ignores it — a global track's
	// parent must itself be global, so no project context is needed.
	projectPath, err := resolveProjectPath(args.ProjectPath)
	if err != nil && !global {
		return resultErr("%v", err)
	}

	track, err := c.CreateTrack(daemon.CreateTrackRequest{
		Name:        args.Name,
		ParentTrack: args.ParentTrack,
		Global:      global,
		Workflow:    args.Workflow,
		Context:     args.Context,
		Description: args.Description,
		ProjectPath: projectPath,
	})
	if err != nil {
		return resultErr("create track failed: %v", err)
	}

	return jsonResult(track)
}
