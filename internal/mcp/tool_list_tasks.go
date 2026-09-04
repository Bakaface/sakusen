package mcp

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// defaultListTasksLimit caps a listing that doesn't ask for a size, so an
// agent calling list_tasks on a long-lived project doesn't pull every task
// ever created into its context.
const defaultListTasksLimit = 50

// ListTasksArgs is the typed input schema for list_tasks.
type ListTasksArgs struct {
	AllProjects bool   `json:"all_projects,omitempty" jsonschema:"List tasks across every project known to the daemon. Default false lists only the resolved project's tasks."`
	ProjectPath string `json:"project_path,omitempty" jsonschema:"Absolute path to the project repo root. Defaults to the git toplevel of the MCP process's cwd. Ignored when all_projects is true."`
	Status      string `json:"status,omitempty" jsonschema:"Filter to tasks whose status (raw or effective) equals this value, e.g. pending, running, awaiting-approval, merge-blocked, completed, failed, merge-failed."`
	Track       string `json:"track,omitempty" jsonschema:"Filter to tasks attached to this track (slug or numeric ID). Tasks with no track are excluded."`
	Limit       *int   `json:"limit,omitempty" jsonschema:"Maximum number of tasks to return, newest first. Defaults to 50; pass 0 for no limit."`
}

// TaskSummary is a compact per-task view for list responses. Heavy fields
// (description, context, commits, images) are omitted — use get_task for full
// details on a specific task.
type TaskSummary struct {
	ID              int64      `json:"id"`
	ProjectName     string     `json:"project_name,omitempty"`
	ProjectPath     string     `json:"project_path,omitempty"`
	Title           string     `json:"title"`
	Slug            string     `json:"slug,omitempty"`
	Track           string     `json:"track,omitempty"`
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
	ProjectName string `json:"project_name,omitempty"`
	ProjectPath string `json:"project_path,omitempty"`
	Count       int    `json:"count"`
	// TotalMatched is how many tasks matched the filters before limit
	// truncation, so a capped listing can't be mistaken for the whole set.
	TotalMatched int           `json:"total_matched"`
	Tasks        []TaskSummary `json:"tasks"`
}

func registerListTasks(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"list_tasks",
		mcp.WithDescription("List sakusen tasks as compact summaries — for the current project by default, or across all projects with all_projects=true. Optionally filter by status and/or track. Results are newest-first and capped at 50 by default; compare count against total_matched to see whether the cap truncated the listing, and raise or lift it with limit. Use get_task for full details (description, steps, output) on a specific task."),
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

	limit := defaultListTasksLimit
	if args.Limit != nil {
		if *args.Limit < 0 {
			return resultErr("limit must not be negative (pass 0 for no limit)")
		}
		limit = *args.Limit
	}

	track := strings.ToLower(strings.TrimSpace(args.Track))

	matched := make([]daemon.TaskInfo, 0, len(tasks))
	for _, t := range tasks {
		if args.Status != "" && t.Status != args.Status && t.EffectiveStatus != args.Status {
			continue
		}
		if track != "" && !taskOnTrack(t, track) {
			continue
		}
		matched = append(matched, t)
	}

	// Newest first, so a capped listing keeps the tasks an agent is most
	// likely to be reasoning about.
	sort.SliceStable(matched, func(i, j int) bool {
		if !matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].CreatedAt.After(matched[j].CreatedAt)
		}
		return matched[i].ID > matched[j].ID
	})
	totalMatched := len(matched)
	if limit > 0 && totalMatched > limit {
		matched = matched[:limit]
	}

	summaries := make([]TaskSummary, 0, len(matched))
	for _, t := range matched {
		summaries = append(summaries, taskSummaryFrom(t))
	}

	result.Count = len(summaries)
	result.TotalMatched = totalMatched
	result.Tasks = summaries
	return jsonResult(result)
}

// taskOnTrack matches a lowercased track ref against a task's attached track,
// by slug or by numeric track ID.
func taskOnTrack(t daemon.TaskInfo, ref string) bool {
	if t.Track != "" && strings.ToLower(t.Track) == ref {
		return true
	}
	if t.TrackID != nil {
		if id, err := strconv.ParseInt(ref, 10, 64); err == nil && *t.TrackID == id {
			return true
		}
	}
	return false
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
		Slug:            t.Slug,
		Track:           t.Track,
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
