package mcp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Bakaface/sortie/internal/client"
	"github.com/Bakaface/sortie/internal/daemon"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// CreateTaskArgs is the typed input schema for create_task. JSON tags
// drive the MCP tool's input schema generation; jsonschema tags become field
// descriptions in the schema visible to the LLM.
type CreateTaskArgs struct {
	Input          string   `json:"input,omitempty" jsonschema:"The task input — what the agent should do. Seeds {{task.input}} in step prompts. Required unless checkout_branch is set or the chosen workflow's first step is tmux."`
	ProjectPath    string   `json:"project_path,omitempty" jsonschema:"Absolute path to the project repo root. Defaults to the git toplevel of the MCP process's cwd."`
	Title          string   `json:"title,omitempty" jsonschema:"Skip AI title generation and use this title verbatim."`
	Workflow       string   `json:"workflow" jsonschema:"Workflow name to run — call list_workflows to see available workflows. Required for every task except tmux_direct ones (tmux_direct skips the workflow entirely, so the field is ignored there). checkout_branch tasks still run their workflow steps, so the requirement applies to them too. A workflow may pin fields like input/worktree/branch/target; explicit arguments here override those pins."`
	Priority       string   `json:"priority,omitempty" jsonschema:"Task priority: low, medium, high, or urgent. Defaults to the project's configured priority."`
	BranchName     string   `json:"branch_name,omitempty" jsonschema:"Branch template, e.g. 'feat/{{task.slug}}'. Supports {{task.id}}, {{task.title}}, {{task.slug}}."`
	TargetBranch   string   `json:"target_branch,omitempty" jsonschema:"Base/merge branch override (defaults to git.base_branch from .sortie.yml)."`
	CheckoutBranch string   `json:"checkout_branch,omitempty" jsonschema:"Check out an existing branch instead of creating a new one. Mutually exclusive with branch_name."`
	Worktree       *bool    `json:"worktree,omitempty" jsonschema:"Run in an isolated git worktree. Defaults to the project's preference (usually true)."`
	TmuxDirect     bool     `json:"tmux_direct,omitempty" jsonschema:"Skip the workflow and drop straight into an interactive agent session in tmux."`
	Images         []string `json:"images,omitempty" jsonschema:"Absolute paths to image attachments for the initial prompt."`
	BlockedBy      []int64  `json:"blocked_by,omitempty" jsonschema:"Task IDs that must complete before this task runs."`
	Track          string   `json:"track,omitempty" jsonschema:"Track to attach (slug or numeric ID). The track's context becomes available to step prompts via {{track.context}}. For create_tasks_and_wait children: empty inherits the parent task's track; pass 'none' to explicitly detach."`
	WaitForReady   bool     `json:"wait_for_ready,omitempty" jsonschema:"Block until the daemon resolves the task's title and branch (typically a few seconds). Default false returns immediately with a half-initialized task."`
}

// readyTimeout caps how long wait_for_ready will block. Title generation is
// gated by titleGenerationTimeout (30s) on the daemon side, so 45s gives some
// slack for branch resolution and broadcast delivery.
const readyTimeout = 45 * time.Second

func registerCreateTask(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"create_task",
		mcp.WithDescription("Create a new sortie task. The task is queued; the daemon will assign an agent when capacity allows. Returns the created TaskInfo as JSON. The workflow argument is required — call list_workflows first to choose one. Only tmux_direct tasks may omit it (they skip the workflow entirely); checkout_branch tasks still run their workflow, so it stays required for them."),
		mcp.WithInputSchema[CreateTaskArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args CreateTaskArgs) (*mcp.CallToolResult, error) {
		return handleCreateTask(ctx, c, args)
	}))
}

func handleCreateTask(ctx context.Context, c *client.Client, args CreateTaskArgs) (*mcp.CallToolResult, error) {
	// Require an explicit workflow so callers never silently fall back to the
	// project's first configured workflow. tmux_direct tasks are exempt: the
	// daemon skips the workflow engine entirely for them.
	if args.Workflow == "" && !args.TmuxDirect {
		return resultErr("workflow is required — call list_workflows to see available workflows (only tmux_direct tasks may omit it)")
	}

	projectPath, err := resolveProjectPath(args.ProjectPath)
	if err != nil {
		return resultErr("%v", err)
	}

	req := daemon.CreateTaskRequest{
		Title:          args.Title,
		Input:          args.Input,
		Workflow:       args.Workflow,
		Priority:       args.Priority,
		BranchName:     args.BranchName,
		TargetBranch:   args.TargetBranch,
		CheckoutBranch: args.CheckoutBranch,
		ProjectPath:    projectPath,
		Worktree:       args.Worktree,
		TmuxDirect:     args.TmuxDirect,
		Images:         args.Images,
		BlockedBy:      args.BlockedBy,
		Track:          args.Track,
	}

	// Subscribe before issuing the create so we don't miss the post-refinement
	// task_update broadcast. The MCP server holds a single shared client, so
	// we can only enable subscription mode for the duration of this call.
	if args.WaitForReady {
		if err := c.Subscribe(); err != nil {
			return resultErr("failed to subscribe for ready signal: %v", err)
		}
		defer func() { _ = c.Unsubscribe() }()
	}

	task, err := c.CreateTaskWithOptions(req)
	if err != nil {
		return resultErr("create task failed: %v", err)
	}

	if args.WaitForReady {
		updated := waitForReady(ctx, c, task.ID, args.TmuxDirect)
		if updated != nil {
			task = updated
		}
	}

	return jsonResult(task)
}

// waitForReady listens for task_update broadcasts on the client's subscription
// channel until the task reports a resolved branch (or, for tmux-direct tasks,
// reaches the tmux status). Returns nil on timeout — the caller falls back to
// the original create response.
func waitForReady(ctx context.Context, c *client.Client, taskID int64, tmuxDirect bool) *daemon.TaskInfo {
	deadline, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()

	for {
		select {
		case msg, ok := <-c.Messages():
			if !ok {
				return nil
			}
			if msg.Type != daemon.MsgTaskUpdate {
				continue
			}
			var payload daemon.TaskUpdateResponse
			if err := msg.DecodePayload(&payload); err != nil {
				continue
			}
			if payload.Task.ID != taskID {
				continue
			}
			if isReady(&payload.Task, tmuxDirect) {
				t := payload.Task
				return &t
			}
		case <-deadline.Done():
			return nil
		}
	}
}

// isReady reports whether a freshly-created task has progressed past the
// daemon's async post-create work. For ordinary workflow tasks that means the
// title+branch have been finalized and the status moved off "init". For
// tmux_direct tasks the goroutine writes a "tmux" status instead.
func isReady(t *daemon.TaskInfo, tmuxDirect bool) bool {
	if t.Status == "init" {
		return false
	}
	if tmuxDirect {
		return true
	}
	// Non-worktree tasks legitimately end up with an empty branch, so don't
	// gate on Branch != "" alone.
	return t.Worktree == false || t.Branch != "" || t.CheckoutBranch != ""
}

// jsonResult marshals v as pretty JSON and wraps it as a text tool result.
// We don't use WithOutputSchema here because the surface includes nullable
// pointer fields and time.Time values that don't round-trip cleanly through
// MCP's structured-output validation.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return resultErr("failed to serialize result: %v", err)
	}
	return mcp.NewToolResultText(string(data)), nil
}
