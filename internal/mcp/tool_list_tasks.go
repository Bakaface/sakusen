package mcp

import (
	"context"
	"time"

	"github.com/Bakaface/sortie/internal/client"
	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/daemon"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ListTasksArgs is the typed input schema for list_tasks.
type ListTasksArgs struct {
	AllProjects bool   `json:"all_projects,omitempty" jsonschema:"List tasks across every project known to the daemon. Default false lists only the resolved project's tasks."`
	ProjectPath string `json:"project_path,omitempty" jsonschema:"Absolute path to the project repo root. Defaults to the git toplevel of the MCP process's cwd. Ignored when all_projects is true."`
	Status      string `json:"status,omitempty" jsonschema:"Filter to tasks whose status (raw or effective) equals this value, e.g. pending, running, awaiting-approval, merge-blocked, completed, failed, merge-failed."`
}

// TaskSummary is a compact per-task view for list responses. Heavy fields
// (description, context, commits, images) are omitted — use get_task for full
// details on a specific task.
type TaskSummary struct {
	ID              int64      `json:"id"`
	ProjectName     string     `json:"project_name,omitempty"`
	ProjectPath     string     `json:"project_path,omitempty"`
	Title           string     `json:"title"`
	Status          string     `json:"status"`
	EffectiveStatus string     `json:"effective_status"`
	Priority        string     `json:"priority"`
	Workflow        string     `json:"workflow,omitempty"`
	CurrentStep     string     `json:"current_step,omitempty"`
	Branch          string     `json:"branch,omitempty"`
	ErrorMessage    string     `json:"error_message,omitempty"`
	BlockedBy       []int64    `json:"blocked_by,omitempty"`
	WaitsOn         []int64    `json:"waits_on,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

// ListTasksResult is the payload returned by list_tasks. Project fields are
// only set for project-scoped listings.
type ListTasksResult struct {
	ProjectName string        `json:"project_name,omitempty"`
	ProjectPath string        `json:"project_path,omitempty"`
	Count       int           `json:"count"`
	Tasks       []TaskSummary `json:"tasks"`
}

func registerListTasks(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"list_tasks",
		mcp.WithDescription("List sortie tasks as compact summaries — for the current project by default, or across all projects with all_projects=true. Optionally filter by status. Use get_task for full details (description, steps, output) on a specific task."),
		mcp.WithInputSchema[ListTasksArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args ListTasksArgs) (*mcp.CallToolResult, error) {
		return handleListTasks(c, args)
	}))
}

func handleListTasks(c *client.Client, args ListTasksArgs) (*mcp.CallToolResult, error) {
	var (
		tasks  []daemon.TaskInfo
		result ListTasksResult
	)

	if args.AllProjects {
		all, err := c.ListTasks()
		if err != nil {
			return resultErr("list tasks failed: %v", err)
		}
		tasks = all
	} else {
		root, err := resolveProjectPath(args.ProjectPath)
		if err != nil {
			return resultErr("%v", err)
		}
		// The daemon filters by project name (repo basename) — the same
		// convention the TUI uses — then we narrow to the exact path in case
		// two projects share a basename.
		name := config.ProjectNameFromPath(root)
		byName, err := c.ListTasksByProjectName(name)
		if err != nil {
			return resultErr("list tasks failed: %v", err)
		}
		tasks = narrowToProjectPath(byName, root)
		result.ProjectName = name
		result.ProjectPath = root
	}

	summaries := make([]TaskSummary, 0, len(tasks))
	for _, t := range tasks {
		if args.Status != "" && t.Status != args.Status && t.EffectiveStatus != args.Status {
			continue
		}
		summaries = append(summaries, taskSummaryFrom(t))
	}

	result.Count = len(summaries)
	result.Tasks = summaries
	return jsonResult(result)
}

// narrowToProjectPath keeps only tasks whose project path matches root when at
// least one task matches. When none match (e.g. the daemon recorded the
// project under a different-but-equivalent path), the name-matched set is
// returned as-is rather than silently dropping everything.
func narrowToProjectPath(tasks []daemon.TaskInfo, root string) []daemon.TaskInfo {
	matched := make([]daemon.TaskInfo, 0, len(tasks))
	for _, t := range tasks {
		if t.ProjectPath == root {
			matched = append(matched, t)
		}
	}
	if len(matched) == 0 {
		return tasks
	}
	return matched
}

func taskSummaryFrom(t daemon.TaskInfo) TaskSummary {
	return TaskSummary{
		ID:              t.ID,
		ProjectName:     t.ProjectName,
		ProjectPath:     t.ProjectPath,
		Title:           t.Title,
		Status:          t.Status,
		EffectiveStatus: t.EffectiveStatus,
		Priority:        t.Priority,
		Workflow:        t.Workflow,
		CurrentStep:     t.CurrentStep,
		Branch:          t.Branch,
		ErrorMessage:    t.ErrorMessage,
		BlockedBy:       t.BlockedBy,
		WaitsOn:         t.WaitsOn,
		CreatedAt:       t.CreatedAt,
		CompletedAt:     t.CompletedAt,
	}
}
