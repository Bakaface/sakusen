package mcp

import (
	"context"
	"strings"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// GetTrackArgs is the typed input schema for get_track.
type GetTrackArgs struct {
	Track       string `json:"track" jsonschema:"Track to read: slug or numeric ID. Required. A project-scoped track shadows a global one with the same slug."`
	ProjectPath string `json:"project_path,omitempty" jsonschema:"Absolute path to the project repo root the slug is resolved against. Defaults to the nearest ancestor of the MCP process's cwd containing .sakusen.yml (not crossing the git toplevel), else the git toplevel."`
}

func registerGetTrack(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"get_track",
		mcp.WithDescription("Read one track in full: the track itself with its complete own context (not the truncated preview list_tracks returns), its root-first ancestor chain with each ancestor's context, and rendered_context — exactly the string {{track.context}} yields in step prompts. Use this before appending to a track so you know what is already there and don't duplicate it."),
		mcp.WithInputSchema[GetTrackArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args GetTrackArgs) (*mcp.CallToolResult, error) {
		return handleGetTrack(c, args)
	}))
}

func handleGetTrack(c *client.Client, args GetTrackArgs) (*mcp.CallToolResult, error) {
	ref := strings.TrimSpace(args.Track)
	if ref == "" {
		return resultErr("track is required (slug or numeric ID)")
	}

	projectPath, err := resolveProjectPath(args.ProjectPath)
	if err != nil {
		return resultErr("%v", err)
	}

	resp, err := c.GetTrack(projectPath, ref)
	if err != nil {
		return resultErr("get track failed: %v", err)
	}
	return jsonResult(resp)
}
