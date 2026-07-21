package mcp

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/Bakaface/sortie/internal/client"
	"github.com/Bakaface/sortie/internal/daemon"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ChildTaskSpec mirrors the create_task arg surface but is reused in arrays.
// Kept structurally identical to CreateTaskArgs (sans WaitForReady) so agents
// can copy-paste a spec between tools without remapping fields.
type ChildTaskSpec struct {
	Description    string   `json:"description,omitempty" jsonschema:"What the child agent should do. Required unless checkout_branch is set or the workflow's first step is tmux."`
	ProjectPath    string   `json:"project_path,omitempty" jsonschema:"Absolute path to the project repo root. Defaults to the parent task's project."`
	Title          string   `json:"title,omitempty" jsonschema:"Skip AI title generation and use this title verbatim."`
	Workflow       string   `json:"workflow,omitempty" jsonschema:"Workflow name to run. Empty selects the project's first task workflow."`
	Priority       string   `json:"priority,omitempty" jsonschema:"Task priority: low, medium, high, or urgent."`
	BranchName     string   `json:"branch_name,omitempty" jsonschema:"Branch template, e.g. 'feat/{{task.slug}}'."`
	TargetBranch   string   `json:"target_branch,omitempty" jsonschema:"Base/merge branch override."`
	CheckoutBranch string   `json:"checkout_branch,omitempty" jsonschema:"Check out an existing branch instead of creating a new one."`
	Worktree       *bool    `json:"worktree,omitempty" jsonschema:"Run in an isolated git worktree."`
	TmuxDirect     bool     `json:"tmux_direct,omitempty" jsonschema:"Skip the workflow and drop straight into an interactive Claude session in tmux."`
	Images         []string `json:"images,omitempty" jsonschema:"Absolute paths to image attachments for the initial prompt."`
	BlockedBy      []int64  `json:"blocked_by,omitempty" jsonschema:"Task IDs that must complete before this child runs."`
	Track          string   `json:"track,omitempty" jsonschema:"Track to attach (slug or numeric ID). Empty inherits the parent task's track; pass 'none' to explicitly detach the child from any track."`
}

// CreateTasksAndWaitArgs is the input schema for create_tasks_and_wait.
type CreateTasksAndWaitArgs struct {
	// ParentTaskID is the calling task whose currently-executing step will
	// suspend pending child completion. Defaults to the value of the
	// SORTIE_TASK_ID env var injected by the workflow engine, so an agent
	// running inside a step does not need to pass it explicitly.
	ParentTaskID int64           `json:"parent_task_id,omitempty" jsonschema:"Task ID that should suspend on the spawned children. Defaults to $SORTIE_TASK_ID (set by the workflow engine for the active step)."`
	Tasks        []ChildTaskSpec `json:"tasks" jsonschema:"One spec per child task to spawn. Must be non-empty."`
}

// WaitForTasksArgs is the input schema for wait_for_tasks.
type WaitForTasksArgs struct {
	ParentTaskID int64   `json:"parent_task_id,omitempty" jsonschema:"Task ID that should suspend. Defaults to $SORTIE_TASK_ID."`
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
			"Spawn one or more child sortie tasks and suspend the calling task's current step until ALL children reach a terminal status (completed or failed). "+
				"The calling step is paused on the daemon side; this tool returns immediately with the child task IDs. "+
				"When the children all finish, the calling step is re-run from the same step index — the agent must check {{children.<id>.status}} to detect failures and decide whether to proceed, retry, or abort.",
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
				"For spawning + waiting in one atomic operation, prefer create_tasks_and_wait. Children already in completed/failed state are silently skipped.",
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
			Description:    t.Description,
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
			"Spawned %d child task(s) %v. Parent task #%d will suspend at the current step until all children reach terminal status, then re-run this step with {{children.<id>.context}}, {{children.<id>.status}}, {{children.<id>.title}}, and {{children.summary}} populated.",
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
// SORTIE_TASK_ID env var that the workflow engine sets for every step's
// Claude subprocess (and which MCP servers spawned inside that process
// inherit). Returns an explanatory error if neither source is available so
// the agent gets a clear remediation hint.
func resolveParentTaskID(explicit int64) (int64, error) {
	if explicit > 0 {
		return explicit, nil
	}
	env := os.Getenv("SORTIE_TASK_ID")
	if env == "" {
		return 0, fmt.Errorf("parent_task_id is required (SORTIE_TASK_ID env var not set; this tool must be called from a running sortie step)")
	}
	id, err := strconv.ParseInt(env, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid SORTIE_TASK_ID=%q", env)
	}
	return id, nil
}
