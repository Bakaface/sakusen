// Package mcp implements a Model Context Protocol server that lets Claude Code
// (or any MCP client) interact with a running sortie daemon over its Unix
// socket. The server speaks MCP over stdio and exposes task lifecycle
// management: creating, listing, retrying, advancing, and editing tasks,
// managing dependencies, listing workflows, reading task state, and creating
// tracks and publishing step/track context. Irrecoverably destructive
// operations (delete, revert, cleanup) are not exposed.
package mcp

import (
	"fmt"

	"github.com/Bakaface/sortie/internal/client"
	"github.com/Bakaface/sortie/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	serverName    = "sortie"
	serverVersion = "0.1.0"
)

// Serve constructs the MCP server, registers tools, and runs it over stdio
// until stdin closes. The supplied config is used to find the daemon socket;
// it is not required for the project the caller's tools target — those are
// resolved per-call via git rev-parse on the client cwd.
func Serve(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("config is required")
	}

	// Build a daemon client up front so we can fail fast if the daemon isn't
	// running. The connection is held for the lifetime of the MCP process.
	c := client.New(cfg)
	if err := c.Connect(); err != nil {
		return fmt.Errorf("sortie daemon not reachable on %s: %w", cfg.SocketPath, err)
	}
	defer c.Close()

	s := server.NewMCPServer(
		serverName,
		serverVersion,
		server.WithToolCapabilities(false),
	)

	registerTools(s, c)

	return server.ServeStdio(s)
}

// registerTools wires the public tool surface. Keep this list aligned with
// the package promise: no irrecoverably destructive operations (delete_task,
// revert_task, cleanup).
//
// create_tasks_and_wait and wait_for_tasks are intentionally additive: they
// spawn children and gate the caller's own step, but never delete, stop, or
// retry foreign tasks. They mutate task_waits_on edges only, which the engine
// clears automatically on the parent's resume.
//
// update_step_context is included even though it mutates DB state because the
// daemon enforces strict safety constraints: it only writes to the task's
// currently-active step (verified against tasks.current_step AND task_steps
// row status), and it cannot affect any other task. It cannot delete data,
// stop agents, or retry/revert tasks. The worst-case is that an agent
// overwrites its own in-flight context — recoverable by re-running the step.
//
// retry_task, update_task_description, list_tasks, and
// update_task_dependencies were added as an explicit design decision
// (task #217) to let orchestrating agents manage the task lifecycle. All are
// recoverable mutations: retry_task stops the task's own agent and re-queues
// it (no work is deleted — worktree and branch survive; a from-step retry
// preserves earlier step contexts), description and dependency edits are
// plain re-editable DB updates, and list_tasks is read-only. Task deletion,
// revert, and worktree cleanup remain off the surface.
//
// advance_task is an explicit design decision to let a Claude Code session
// signal "this tmux step is done" from outside the tmux pane (the in-pane
// signal is the step-done sentinel file). The daemon only accepts it for
// tasks in tmux state, and no work is deleted: the workflow either resumes
// at the next step or runs the same finalization the TUI's advance/finalize
// keybind triggers. If it advanced too early, retry_task re-runs the step.
//
// create_track and list_tracks are additive/read-only. update_track_context is
// admitted with the same admission argument as update_step_context: the daemon
// enforces it can only write the CALLING task's own track (the task must have
// a track and an active step). Note the trust-model caveat, though: track
// context is a PERSISTENT, CROSS-TASK prompt-injection surface — anything an
// agent writes flows verbatim into the prompts of every future task attached
// to that track or its children. That is the feature (gradual context
// accumulation across a sprint), but it widens the blast radius of a
// misbehaving agent from "its own step context" to "future prompts on its
// track"; the tool descriptions call this out explicitly.
func registerTools(s *server.MCPServer, c *client.Client) {
	registerCreateTask(s, c)
	registerListWorkflows(s, c)
	registerGetTask(s, c)
	registerListTasks(s, c)
	registerRetryTask(s, c)
	registerAdvanceTask(s, c)
	registerUpdateTaskDescription(s, c)
	registerUpdateTaskDependencies(s, c)
	registerCreateTasksAndWait(s, c)
	registerWaitForTasks(s, c)
	registerUpdateStepContext(s, c)
	registerCreateTrack(s, c)
	registerUpdateTrackContext(s, c)
	registerListTracks(s, c)
}

// resultErr builds an MCP error result without losing the underlying error
// message. Returning (result, nil) is the MCP convention for tool-level
// errors — the protocol-level error channel is reserved for transport faults.
func resultErr(format string, args ...any) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultError(fmt.Sprintf(format, args...)), nil
}
