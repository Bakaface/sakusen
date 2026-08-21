package mcp

import (
	"context"
	"fmt"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// GetTaskArgs is the typed input schema for get_task.
type GetTaskArgs struct {
	TaskID              int64 `json:"task_id" jsonschema:"Task ID to look up. Required."`
	IncludeSteps        bool  `json:"include_steps,omitempty" jsonschema:"Include per-step status (pending/running/completed) in workflow order."`
	IncludeStepContexts bool  `json:"include_step_contexts,omitempty" jsonschema:"Include captured step contexts (the structured output between workflow steps). Only populated for completed steps."`
	TailOutput          int   `json:"tail_output,omitempty" jsonschema:"Return the last N lines of agent output. 0 omits output entirely. Live agent buffer is preferred; falls back to on-disk step logs."`
}

// GetTaskResult is the aggregated payload returned by get_task. Optional
// sections stay nil when the caller doesn't ask for them so the response stays
// compact.
type GetTaskResult struct {
	Task         *daemon.TaskInfo        `json:"task"`
	Steps        []daemon.TaskStepDetail `json:"steps,omitempty"`
	StepContexts map[string]string       `json:"step_contexts,omitempty"`
	Output       *taskOutput             `json:"output,omitempty"`
}

type taskOutput struct {
	Source     string   `json:"source"` // "agent_buffer" or "step_logs"
	Lines      []string `json:"lines"`
	TotalLines int      `json:"total_lines"`
}

func registerGetTask(s *server.MCPServer, c *client.Client) {
	tool := mcp.NewTool(
		"get_task",
		mcp.WithDescription("Get detailed information about a sakusen task: its TaskInfo (status, branch, current step, etc.), and optionally per-step state, captured step contexts, and recent agent output. Use include_* flags to opt into extra sections."),
		mcp.WithInputSchema[GetTaskArgs](),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args GetTaskArgs) (*mcp.CallToolResult, error) {
		return handleGetTask(c, args)
	}))
}

func handleGetTask(c *client.Client, args GetTaskArgs) (*mcp.CallToolResult, error) {
	if args.TaskID <= 0 {
		return resultErr("task_id must be a positive integer")
	}

	task, err := c.GetTask(args.TaskID)
	if err != nil {
		return resultErr("get task failed: %v", err)
	}

	result := GetTaskResult{Task: task}

	if args.IncludeSteps {
		steps, err := c.GetTaskSteps(args.TaskID)
		if err != nil {
			return resultErr("get task steps failed: %v", err)
		}
		result.Steps = steps
	}

	if args.IncludeStepContexts {
		contexts, err := c.GetStepContexts(args.TaskID)
		if err != nil {
			return resultErr("get step contexts failed: %v", err)
		}
		result.StepContexts = contexts
	}

	if args.TailOutput > 0 {
		result.Output = collectOutput(c, args.TaskID, args.TailOutput)
	}

	return jsonResult(result)
}

// collectOutput tries the live agent ring buffer first (keyed by stringified
// task ID — see internal/client.StopTask for the same convention). If that
// fails (e.g. agent already exited), falls back to the on-disk step logs.
// Errors are swallowed because output is best-effort context, not the primary
// payload — but the source field tells the caller which path served the
// content.
func collectOutput(c *client.Client, taskID int64, tail int) *taskOutput {
	agentID := fmt.Sprintf("%d", taskID)
	if lines, total, err := c.GetOutput(agentID, 0); err == nil {
		return &taskOutput{
			Source:     "agent_buffer",
			Lines:      tailLines(lines, tail),
			TotalLines: total,
		}
	}

	lines, total, err := c.GetLogs(taskID, tail, 0)
	if err != nil {
		return nil
	}
	return &taskOutput{
		Source:     "task_log",
		Lines:      lines,
		TotalLines: total,
	}
}

func tailLines(lines []string, tail int) []string {
	if tail <= 0 || len(lines) <= tail {
		return lines
	}
	return lines[len(lines)-tail:]
}
