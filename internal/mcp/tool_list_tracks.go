package mcp

import (
	"context"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ListTracksArgs is the typed input schema for list_tracks.
type ListTracksArgs struct {
	ProjectPath string `json:"project_path,omitempty" jsonschema:"Absolute path to the project repo root. Defaults to the git toplevel of the MCP process's cwd."`
}

func registerListTracks(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"list_tracks",
		mcp.WithDescription("List the tracks visible from a project: its own project-scoped tracks plus all global tracks. Each entry carries id, slug, scope, parent, associated workflow, a stable description, a context preview, and context_len (bytes). Select a track by its description — a stable one-liner stating the track's purpose, set independently of the context; the context preview is a truncated slice of accumulated working notes, not a purpose statement. Use the sizes to spot tracks whose accumulated context is growing large."),
		mcp.WithInputSchema[ListTracksArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args ListTracksArgs) (*mcp.CallToolResult, error) {
		return handleListTracks(c, args)
	}))
}

func handleListTracks(c *client.Client, args ListTracksArgs) (*mcp.CallToolResult, error) {
	projectPath, err := resolveProjectPath(args.ProjectPath)
	if err != nil {
		return resultErr("%v", err)
	}

	tracks, err := c.ListTracks(projectPath)
	if err != nil {
		return resultErr("list tracks failed: %v", err)
	}

	return jsonResult(tracks)
}
