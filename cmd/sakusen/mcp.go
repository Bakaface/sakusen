package main

import (
	"github.com/Bakaface/sakusen/internal/mcp"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run an MCP server over stdio that talks to the sakusen daemon",
	Long: `Run a Model Context Protocol server that exposes a safe-by-default
subset of the sakusen daemon to MCP clients (e.g. Claude Code sessions).

The server speaks MCP over stdin/stdout, so it should be launched by the
client — typically by adding it to the client's MCP server configuration.

Exposed tools:
  create_task    Create a task in the project rooted at cwd (or an
                 explicit project_path). Supports workflow choice,
                 branch templates, dependencies, and tmux mode.
  list_workflows List the workflows configured for a project as a
                 flat list.
  get_task       Fetch a task's status, current step, optional
                 per-step state, captured step contexts, and
                 recent agent output.

The server requires a running sakusen daemon — it does not start one. Run
'sakusen daemon start' first.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return mcp.Serve(cfg)
	},
}
