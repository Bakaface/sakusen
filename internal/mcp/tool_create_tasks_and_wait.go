package mcp

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ChildTaskSpec mirrors the create_task arg surface but is reused in arrays.
// Kept structurally identical to CreateTaskArgs (sans WaitForReady) so agents
// can copy-paste a spec between tools without remapping fields.
type ChildTaskSpec struct {
	Input          string   `json:"input,omitempty" jsonschema:"The child task input — what the child agent should do. Required unless checkout_branch is set or the workflow's first step is tmux."`
	ProjectPath    string   `json:"project_path,omitempty" jsonschema:"Absolute path to the project repo root. Defaults to the parent task's project. May point at a DIFFERENT project than the parent — the parent still suspends until the child reaches terminal status, and the child runs under its own project's .sakusen.yml and workflows."`
	Title          string   `json:"title,omitempty" jsonschema:"Skip AI title generation and use this title verbatim."`
	Workflow       string   `json:"workflow" jsonschema:"Workflow name to run — call list_workflows to see available workflows. Required for every child except tmux_direct ones (tmux_direct skips the workflow entirely, so the field is ignored there). checkout_branch children still run their workflow steps, so the requirement applies to them too."`
	Priority       string   `json:"priority,omitempty" jsonschema:"Task priority: low, medium, high, or urgent."`
	BranchName     string   `json:"branch_name,omitempty" jsonschema:"Branch template, e.g. 'feat/{{task.slug}}'."`
	TargetBranch   string   `json:"target_branch,omitempty" jsonschema:"Base/merge branch override."`
	CheckoutBranch string   `json:"checkout_branch,omitempty" jsonschema:"Check out an existing branch instead of creating a new one."`
	Worktree       *bool    `json:"worktree,omitempty" jsonschema:"Run in an isolated git worktree."`
	TmuxDirect     bool     `json:"tmux_direct,omitempty" jsonschema:"Skip the workflow and drop straight into an interactive agent session in tmux."`
	Images         []string `json:"images,omitempty" jsonschema:"Absolute paths to image attachments for the initial prompt."`
	BlockedBy      []int64  `json:"blocked_by,omitempty" jsonschema:"Task IDs that must complete before this child runs."`
	Track          string   `json:"track,omitempty" jsonschema:"Track to attach (slug or numeric ID). Empty inherits the parent task's track ONLY when the child is in the parent's project — a child in a different project starts trackless unless you pass an explicit track, which is resolved in the child's own project. Pass 'none' to explicitly detach."`
}

// CreateTasksAndWaitArgs is the input schema for create_tasks_and_wait.
type CreateTasksAndWaitArgs struct {
	// ParentTaskID is the calling task whose currently-executing step will
	// suspend pending child completion. Defaults to the value of the
	// SAKUSEN_TASK_ID env var injected by the workflow engine, so an agent
	// running inside a step does not need to pass it explicitly.
	ParentTaskID int64           `json:"parent_task_id,omitempty" jsonschema:"Task ID that should suspend on the spawned children. Defaults to $SAKUSEN_TASK_ID (set by the workflow engine for the active step)."`
	Tasks        []ChildTaskSpec `json:"tasks" jsonschema:"One spec per child task to spawn. Must be non-empty."`
}

// WaitForTasksArgs is the input schema for wait_for_tasks.
type WaitForTasksArgs struct {
	ParentTaskID int64   `json:"parent_task_id,omitempty" jsonschema:"Task ID that should suspend. Defaults to $SAKUSEN_TASK_ID."`
	ChildTaskIDs []int64 `json:"child_task_ids" jsonschema:"IDs of pre-existing tasks the parent will wait on. Already-terminal tasks are skipped."`
}

// CreateTasksAndWaitResult is the structured tool response. We return the IDs
// up front so an agent that ignores the body still has the most-important
// signal in its tool-result text.
type CreateTasksAndWaitResult struct {
	ParentTaskID int64             `json:"parent_task_id"`
	ChildIDs     []int64           `json:"child_ids"`
	Children     []daemon.TaskInfo `json:"children"`
	Message      string            `json:"message"`
}

type WaitForTasksResult struct {
	ParentTaskID int64             `json:"parent_task_id"`
	ChildIDs     []int64           `json:"child_ids"`
	Children     []daemon.TaskInfo `json:"children"`
	Message      string            `json:"message"`
}

func registerCreateTasksAndWait(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"create_tasks_and_wait",
		mcp.WithDescription(
			"Spawn one or more child sakusen tasks and suspend the calling task's current step until ALL children reach a terminal status (completed or failed). "+
				"The calling step is paused on the daemon side; this tool returns immediately with the child task IDs. "+
				"When the children all finish, the calling step is re-run from the same step index — the agent must check {{children.<id>.status}} to detect failures and decide whether to proceed, retry, or abort. "+
				"Children may be created in a different project via project_path; the parent suspends and resumes identically. On resume, check {{children.<id>.status}} — it is 'completed' or 'failed' regardless of which project the child ran in. "+
				"Each child spec must name a workflow (call list_workflows to see available workflows); only tmux_direct children may omit it.",
		),
		mcp.WithInputSchema[CreateTasksAndWaitArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args CreateTasksAndWaitArgs) (*mcp.CallToolResult, error) {
		return handleCreateTasksAndWait(c, args)
	}))
}

func registerWaitForTasks(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"wait_for_tasks",
		mcp.WithDescription(
			"Suspend the calling task's current step until each named pre-existing task reaches a terminal status. "+
				"For spawning + waiting in one atomic operation, prefer create_tasks_and_wait. Children already in completed/failed state are silently skipped. "+
				"The named tasks may live in any project.",
		),
		mcp.WithInputSchema[WaitForTasksArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args WaitForTasksArgs) (*mcp.CallToolResult, error) {
		return handleWaitForTasks(c, args)
	}))
}

func handleCreateTasksAndWait(c *client.Client, args CreateTasksAndWaitArgs) (*mcp.CallToolResult, error) {
	parentID, err := resolveParentTaskID(args.ParentTaskID)
	if err != nil {
		return resultErr("%v", err)
	}
	if len(args.Tasks) == 0 {
		return resultErr("tasks must contain at least one child spec")
	}

	reqs := make([]daemon.CreateTaskRequest, len(args.Tasks))
	for i, t := range args.Tasks {
		// Same rule as create_task: an explicit workflow is required so a
		// child never silently falls back to the project's first workflow.
		// tmux_direct children are exempt — the daemon skips the workflow
		// engine entirely for them.
		if t.Workflow == "" && !t.TmuxDirect {
			return resultErr("child %d: workflow is required — call list_workflows to see available workflows (only tmux_direct children may omit it)", i+1)
		}
		projectPath := t.ProjectPath
		if projectPath != "" {
			abs, perr := resolveProjectPath(projectPath)
			if perr != nil {
				return resultErr("child %d: %v", i+1, perr)
			}
			projectPath = abs
		}
		reqs[i] = daemon.CreateTaskRequest{
			Title:          t.Title,
			Input:          t.Input,
			Workflow:       t.Workflow,
			Priority:       t.Priority,
			BranchName:     t.BranchName,
			TargetBranch:   t.TargetBranch,
			CheckoutBranch: t.CheckoutBranch,
			ProjectPath:    projectPath,
			Worktree:       t.Worktree,
			TmuxDirect:     t.TmuxDirect,
			Images:         t.Images,
			BlockedBy:      t.BlockedBy,
			Track:          t.Track,
		}
	}

	children, err := c.CreateTasksAndWait(parentID, reqs)
	if err != nil {
		return resultErr("create_tasks_and_wait failed: %v", err)
	}

	ids := make([]int64, len(children))
	for i, c := range children {
		ids[i] = c.ID
	}
	return jsonResult(CreateTasksAndWaitResult{
		ParentTaskID: parentID,
		ChildIDs:     ids,
		Children:     children,
		Message: fmt.Sprintf(
			"Spawned %d child task(s) %v. Parent task #%d will suspend at the current step until all children reach terminal status, then re-run this step with {{children.<id>.context}}, {{children.<id>.status}}, {{children.<id>.title}}, and {{children.summary}} populated. "+
				"Each entry in children carries project_name/project_path so you can confirm where each child landed.",
			len(children), ids, parentID,
		),
	})
}

func handleWaitForTasks(c *client.Client, args WaitForTasksArgs) (*mcp.CallToolResult, error) {
	parentID, err := resolveParentTaskID(args.ParentTaskID)
	if err != nil {
		return resultErr("%v", err)
	}
	if len(args.ChildTaskIDs) == 0 {
		return resultErr("child_task_ids must contain at least one ID")
	}
	children, err := c.WaitForTasks(parentID, args.ChildTaskIDs)
	if err != nil {
		return resultErr("wait_for_tasks failed: %v", err)
	}
	ids := make([]int64, len(children))
	for i, c := range children {
		ids[i] = c.ID
	}
	msg := fmt.Sprintf("Parent task #%d will suspend on %d child task(s): %v.", parentID, len(children), ids)
	if len(children) == 0 {
		msg = fmt.Sprintf("Parent task #%d: every supplied child was already terminal — no suspension recorded.", parentID)
	}
	return jsonResult(WaitForTasksResult{
		ParentTaskID: parentID,
		ChildIDs:     ids,
		Children:     children,
		Message:      msg,
	})
}

// resolveParentTaskID returns explicit if non-zero, else parses the
// SAKUSEN_TASK_ID env var that the workflow engine sets for every step's
// Claude subprocess (and which MCP servers spawned inside that process
// inherit). Returns an explanatory error if neither source is available so
// the agent gets a clear remediation hint.
func resolveParentTaskID(explicit int64) (int64, error) {
	if explicit > 0 {
		return explicit, nil
	}
	env := os.Getenv("SAKUSEN_TASK_ID")
	if env == "" {
		return 0, fmt.Errorf("parent_task_id is required (SAKUSEN_TASK_ID env var not set; this tool must be called from a running sakusen step)")
	}
	id, err := strconv.ParseInt(env, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid SAKUSEN_TASK_ID=%q", env)
	}
	return id, nil
}
