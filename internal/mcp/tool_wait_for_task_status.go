package mcp

import (
	"context"
	"time"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// WaitForTaskStatusArgs is the typed input schema for wait_for_task_status.
type WaitForTaskStatusArgs struct {
	TaskIDs        []int64  `json:"task_ids" jsonschema:"Task IDs to watch. Required, non-empty. Returns as soon as ANY of them is in a target status."`
	Statuses       []string `json:"statuses,omitempty" jsonschema:"Target statuses. Defaults to completed, failed, merge-failed, merge-blocked."`
	TimeoutSeconds *int     `json:"timeout_seconds,omitempty" jsonschema:"Maximum seconds to wait (1-3600). Defaults to 600. A timeout is a normal result with timed_out: true, not an error."`
}

const (
	defaultWaitTimeoutSeconds = 600
	maxWaitTimeoutSeconds     = 3600
)

func registerWaitForTaskStatus(s *server.MCPServer, cfg *config.Config) {
	tool := mcp.NewTool(
		"wait_for_task_status",
		mcp.WithDescription("Block until any of the given sakusen tasks reaches one of the target statuses, or the timeout expires. Works from any MCP client — it does not need $SAKUSEN_TASK_ID and does not suspend a calling task. This BLOCKS your turn for up to timeout_seconds; to stay responsive, run `sakusen wait <task_id>...` as a background shell command instead. Default statuses: completed (the task's branch merged/landed), failed, merge-failed (steps succeeded but finalization did not land), merge-blocked (NOT terminal: the merge is waiting for a clean target branch and resumes on its own, but may need attention). Returns JSON {matched, tasks, timed_out}: matched lists every watched task currently in a target status, tasks holds the latest snapshot of every watched task. When waiting on several tasks, re-call with the IDs not yet matched."),
		mcp.WithInputSchema[WaitForTaskStatusArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args WaitForTaskStatusArgs) (*mcp.CallToolResult, error) {
		return handleWaitForTaskStatus(ctx, cfg, args)
	}))
}

func handleWaitForTaskStatus(ctx context.Context, cfg *config.Config, args WaitForTaskStatusArgs) (*mcp.CallToolResult, error) {
	if len(args.TaskIDs) == 0 {
		return resultErr("task_ids is required and must be non-empty")
	}
	for _, id := range args.TaskIDs {
		if id <= 0 {
			return resultErr("task_ids must be positive integers (got %d)", id)
		}
	}
	statuses, err := client.ParseWaitStatuses(args.Statuses)
	if err != nil {
		return resultErr("%v", err)
	}
	timeout := defaultWaitTimeoutSeconds
	if args.TimeoutSeconds != nil {
		timeout = *args.TimeoutSeconds
	}
	if timeout < 1 || timeout > maxWaitTimeoutSeconds {
		return resultErr("timeout_seconds must be between 1 and %d", maxWaitTimeoutSeconds)
	}

	// A dedicated connection: the waiter consumes every broadcast on its
	// client, which would starve create_task's wait_for_ready on the shared one.
	c := client.New(cfg)
	if err := c.Connect(); err != nil {
		return resultErr("sakusen daemon not reachable: %v", err)
	}
	defer c.Close()

	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	result, err := c.WaitForTaskStatus(waitCtx, args.TaskIDs, statuses, 0)
	if err != nil {
		return resultErr("wait failed: %v", err)
	}
	return jsonResult(result)
}
