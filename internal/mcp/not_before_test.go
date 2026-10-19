package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/mark3labs/mcp-go/mcp"
)

func callTool(t *testing.T, fake *fakeDaemon, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: name, Arguments: args},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	return res
}

func TestMCP_CreateTask_NotBeforeNormalizedToUTC(t *testing.T) {
	fake := newFakeDaemon(t)
	var captured daemon.CreateTaskRequest
	fake.handle(daemon.MsgCreateTask, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&captured)
		resp, _ := daemon.NewMessage(daemon.MsgCreateTask, daemon.CreateTaskResponse{
			Task: daemon.TaskInfo{ID: 1, Status: "init", NotBefore: captured.NotBefore},
		})
		return resp
	})

	res := callTool(t, fake, "create_task", map[string]any{
		"input":        "prep the call",
		"project_path": "/tmp/proj",
		"workflow":     "implement",
		"not_before":   "2026-03-01T10:00:00+02:00",
	})
	if res.IsError {
		t.Fatalf("tool returned error: %s", textOf(res))
	}
	want := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	if captured.NotBefore == nil || !captured.NotBefore.Equal(want) || captured.NotBefore.Location() != time.UTC {
		t.Fatalf("NotBefore = %v, want %v (UTC)", captured.NotBefore, want)
	}
	var out daemon.TaskInfo
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.NotBefore == nil || !out.NotBefore.Equal(want) {
		t.Errorf("returned NotBefore = %v, want %v", out.NotBefore, want)
	}
}

func TestMCP_CreateTask_RejectsInvalidNotBefore(t *testing.T) {
	fake := newFakeDaemon(t)
	for _, v := range []string{"tomorrow", "+1h", "2026-03-01 10:00", "2026-03-01T10:00:00"} {
		res := callTool(t, fake, "create_task", map[string]any{
			"input": "x", "project_path": "/tmp/proj", "workflow": "implement", "not_before": v,
		})
		if !res.IsError || !strings.Contains(textOf(res), "RFC3339") {
			t.Errorf("not_before %q: want RFC3339 error, got %s", v, textOf(res))
		}
	}
	if len(fake.requestTypes()) != 0 {
		t.Errorf("invalid not_before leaked to daemon: %v", fake.requestTypes())
	}
}

func TestMCP_UpdateTask_SetsAndClearsNotBefore(t *testing.T) {
	fake := newFakeDaemon(t)
	var reqs []daemon.UpdateNotBeforeRequest
	fake.handle(daemon.MsgUpdateNotBefore, func(msg *daemon.Message) *daemon.Message {
		var req daemon.UpdateNotBeforeRequest
		_ = msg.DecodePayload(&req)
		reqs = append(reqs, req)
		resp, _ := daemon.NewMessage(daemon.MsgUpdateNotBefore, daemon.UpdateNotBeforeResponse{
			Task: daemon.TaskInfo{ID: req.TaskID, NotBefore: req.NotBefore},
		})
		return resp
	})

	res := callTool(t, fake, "update_task", map[string]any{"task_id": 3, "not_before": "2026-03-01T08:00:00Z"})
	if res.IsError {
		t.Fatalf("set: %s", textOf(res))
	}
	res = callTool(t, fake, "update_task", map[string]any{"task_id": 3, "clear_not_before": true})
	if res.IsError {
		t.Fatalf("clear: %s", textOf(res))
	}
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2", len(reqs))
	}
	if reqs[0].NotBefore == nil || !reqs[0].NotBefore.Equal(time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("set request NotBefore = %v", reqs[0].NotBefore)
	}
	if reqs[1].NotBefore != nil {
		t.Errorf("clear request NotBefore = %v, want nil", reqs[1].NotBefore)
	}
}

func TestMCP_UpdateTask_NotBeforeErrors(t *testing.T) {
	fake := newFakeDaemon(t)
	fake.handle(daemon.MsgUpdateNotBefore, func(*daemon.Message) *daemon.Message {
		resp, _ := daemon.NewMessage(daemon.MsgError, daemon.ErrorResponse{Message: "not_before can only be changed on a pending task (task #3 is running)"})
		return resp
	})

	res := callTool(t, fake, "update_task", map[string]any{"task_id": 3, "not_before": "2026-03-01T08:00:00Z", "clear_not_before": true})
	if !res.IsError || !strings.Contains(textOf(res), "mutually exclusive") {
		t.Errorf("both set: got %s", textOf(res))
	}
	res = callTool(t, fake, "update_task", map[string]any{"task_id": 3, "not_before": "soon"})
	if !res.IsError || !strings.Contains(textOf(res), "RFC3339") {
		t.Errorf("invalid: got %s", textOf(res))
	}
	if len(fake.requestTypes()) != 0 {
		t.Fatalf("invalid args leaked to daemon: %v", fake.requestTypes())
	}
	res = callTool(t, fake, "update_task", map[string]any{"task_id": 3, "not_before": "2026-03-01T08:00:00Z"})
	if !res.IsError || !strings.Contains(textOf(res), "pending task") {
		t.Errorf("non-pending: got %s", textOf(res))
	}
}
