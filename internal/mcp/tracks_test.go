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

func TestMCP_CreateTrack_ForwardsFields(t *testing.T) {
	fake := newFakeDaemon(t)

	var captured daemon.CreateTrackRequest
	fake.handle(daemon.MsgCreateTrack, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&captured)
		resp, _ := daemon.NewMessage(daemon.MsgCreateTrack, daemon.CreateTrackResponse{
			Track: daemon.TrackInfo{ID: 42, Name: captured.Name, Slug: "payments-api", Global: captured.Global},
		})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "create_track",
			Arguments: map[string]any{
				"name":         "Payments API",
				"parent_track": "sprint-12",
				"scope":        "global",
				"workflow":     "payments-api:impl",
				"context":      "seed",
				"description":  "Owns the payments API surface",
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(res))
	}

	if captured.Name != "Payments API" || captured.ParentTrack != "sprint-12" ||
		!captured.Global || captured.Workflow != "payments-api:impl" || captured.Context != "seed" ||
		captured.Description != "Owns the payments API surface" {
		t.Errorf("captured request = %+v", captured)
	}
	if !strings.Contains(textOf(res), "payments-api") {
		t.Errorf("result should contain the track JSON, got %s", textOf(res))
	}
}

func TestMCP_CreateTrack_Validation(t *testing.T) {
	fake := newFakeDaemon(t)
	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cases := []struct {
		name      string
		arguments map[string]any
		wantErr   string
	}{
		{"missing name", map[string]any{"scope": "global"}, "name is required"},
		{"invalid scope", map[string]any{"name": "X", "scope": "universe"}, "invalid scope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := c.CallTool(ctx, mcp.CallToolRequest{
				Params: mcp.CallToolParams{Name: "create_track", Arguments: tc.arguments},
			})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if !res.IsError {
				t.Fatalf("expected tool error, got success: %s", textOf(res))
			}
			if !strings.Contains(textOf(res), tc.wantErr) {
				t.Errorf("error should contain %q, got %q", tc.wantErr, textOf(res))
			}
		})
	}

	for _, mt := range fake.requestTypes() {
		if mt == daemon.MsgCreateTrack {
			t.Errorf("invalid arg call leaked to daemon")
		}
	}
}

func TestMCP_UpdateTrack_ForwardsBothFieldsAndDefaultsToAppend(t *testing.T) {
	fake := newFakeDaemon(t)

	var capturedCtx daemon.UpdateTaskTrackContextRequest
	var capturedDesc daemon.UpdateTaskTrackDescriptionRequest
	fake.handle(daemon.MsgUpdateTaskTrackContext, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&capturedCtx)
		resp, _ := daemon.NewMessage(daemon.MsgOK, daemon.OKResponse{Message: "ok"})
		return resp
	})
	fake.handle(daemon.MsgUpdateTaskTrackDescription, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&capturedDesc)
		resp, _ := daemon.NewMessage(daemon.MsgOK, daemon.OKResponse{Message: "ok"})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "update_track",
			Arguments: map[string]any{
				"task_id":     7,
				"context":     "endpoint A done",
				"description": "Owns the payments API surface",
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(res))
	}

	if capturedCtx.TaskID != 7 || capturedCtx.Context != "endpoint A done" {
		t.Errorf("context request = %+v", capturedCtx)
	}
	if capturedCtx.Mode != "append" {
		t.Errorf("expected default context_mode=append, got %q", capturedCtx.Mode)
	}
	if capturedDesc.TaskID != 7 || capturedDesc.Description != "Owns the payments API surface" {
		t.Errorf("description request = %+v", capturedDesc)
	}
}

// TestMCP_UpdateTrack_DescriptionOnlySkipsContextWrite pins that an omitted
// field produces no daemon call at all — a description edit must not touch the
// accumulated context.
func TestMCP_UpdateTrack_DescriptionOnlySkipsContextWrite(t *testing.T) {
	fake := newFakeDaemon(t)
	fake.handle(daemon.MsgUpdateTaskTrackDescription, func(*daemon.Message) *daemon.Message {
		resp, _ := daemon.NewMessage(daemon.MsgOK, daemon.OKResponse{Message: "ok"})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "update_track",
			Arguments: map[string]any{"task_id": 7, "description": "Owns payments"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(res))
	}

	got := fake.requestTypes()
	if len(got) != 1 || got[0] != daemon.MsgUpdateTaskTrackDescription {
		t.Errorf("expected only a description write, got %v", got)
	}
}

func TestMCP_UpdateTrack_ReplaceMode(t *testing.T) {
	fake := newFakeDaemon(t)
	var captured daemon.UpdateTaskTrackContextRequest
	fake.handle(daemon.MsgUpdateTaskTrackContext, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&captured)
		resp, _ := daemon.NewMessage(daemon.MsgOK, daemon.OKResponse{Message: "ok"})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "update_track",
			Arguments: map[string]any{"task_id": 7, "context": "fresh", "context_mode": "REPLACE"},
		},
	}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if captured.Mode != "replace" {
		t.Errorf("context_mode should be normalized to replace, got %q", captured.Mode)
	}
}

func TestMCP_UpdateTrack_DefaultsTaskIDFromEnv(t *testing.T) {
	fake := newFakeDaemon(t)
	var captured daemon.UpdateTaskTrackContextRequest
	fake.handle(daemon.MsgUpdateTaskTrackContext, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&captured)
		resp, _ := daemon.NewMessage(daemon.MsgOK, daemon.OKResponse{Message: "ok"})
		return resp
	})

	c := startMCPServer(t, fake)
	t.Setenv("SAKUSEN_TASK_ID", "31")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "update_track",
			Arguments: map[string]any{"context": "from env"},
		},
	}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if captured.TaskID != 31 {
		t.Errorf("expected task_id defaulted from env, got %d", captured.TaskID)
	}

	if _, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "update_track",
			Arguments: map[string]any{"task_id": 4, "context": "explicit"},
		},
	}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if captured.TaskID != 4 {
		t.Errorf("explicit task_id should win over env, got %d", captured.TaskID)
	}
}

func TestMCP_UpdateTrack_RejectsInvalidArgs(t *testing.T) {
	fake := newFakeDaemon(t)
	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cases := []struct {
		name      string
		arguments map[string]any
		wantErr   string
	}{
		{"no task_id and no env", map[string]any{"context": "x"}, "task_id is required (SAKUSEN_TASK_ID env var not set"},
		{"no fields", map[string]any{"task_id": 7}, "at least one of context or description"},
		{"blank description", map[string]any{"task_id": 7, "description": "   "}, "description must not be empty"},
		{"invalid context_mode", map[string]any{"task_id": 7, "context": "x", "context_mode": "merge"}, "invalid context_mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := c.CallTool(ctx, mcp.CallToolRequest{
				Params: mcp.CallToolParams{Name: "update_track", Arguments: tc.arguments},
			})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if !res.IsError {
				t.Fatalf("expected tool error, got success: %s", textOf(res))
			}
			if !strings.Contains(textOf(res), tc.wantErr) {
				t.Errorf("error should contain %q, got %q", tc.wantErr, textOf(res))
			}
		})
	}

	if len(fake.requestTypes()) != 0 {
		t.Errorf("invalid arg calls leaked to daemon: %v", fake.requestTypes())
	}
}

// TestMCP_UpdateTrack_ReportsPartialApplication: the context write landed, the
// description write did not — the error must say so.
func TestMCP_UpdateTrack_ReportsPartialApplication(t *testing.T) {
	fake := newFakeDaemon(t)
	fake.handle(daemon.MsgUpdateTaskTrackContext, func(*daemon.Message) *daemon.Message {
		resp, _ := daemon.NewMessage(daemon.MsgOK, daemon.OKResponse{Message: "ok"})
		return resp
	})
	fake.handle(daemon.MsgUpdateTaskTrackDescription, func(*daemon.Message) *daemon.Message {
		resp, _ := daemon.NewMessage(daemon.MsgError, daemon.ErrorResponse{Message: "task #7 has no track"})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "update_track",
			Arguments: map[string]any{"task_id": 7, "context": "x", "description": "y"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error, got success: %s", textOf(res))
	}
	if !strings.Contains(textOf(res), "the context update in this call was already applied") {
		t.Errorf("error should report the partial application, got %q", textOf(res))
	}
}

func TestMCP_GetTrack_ForwardsRefAndReturnsFullContext(t *testing.T) {
	fake := newFakeDaemon(t)

	var captured daemon.GetTrackRequest
	fake.handle(daemon.MsgGetTrack, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&captured)
		resp, _ := daemon.NewMessage(daemon.MsgGetTrack, daemon.GetTrackResponse{
			Track: daemon.TrackInfo{ID: 4, Slug: "payments-api", Context: "own notes", ContextLen: 9},
			Chain: []daemon.TrackInfo{
				{ID: 1, Slug: "sprint-12", Context: "sprint goals"},
				{ID: 4, Slug: "payments-api", Context: "own notes"},
			},
			RenderedContext: "sprint goals\n\nown notes",
		})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "get_track",
			Arguments: map[string]any{"track": "payments-api", "project_path": "/tmp/proj"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(res))
	}

	if captured.Track != "payments-api" || captured.ProjectPath != "/tmp/proj" {
		t.Errorf("captured = %+v", captured)
	}

	var out daemon.GetTrackResponse
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, textOf(res))
	}
	if out.Track.Context != "own notes" {
		t.Errorf("full own context should be returned, got %q", out.Track.Context)
	}
	if len(out.Chain) != 2 || out.Chain[0].Slug != "sprint-12" {
		t.Errorf("root-first chain: %+v", out.Chain)
	}
	if out.RenderedContext != "sprint goals\n\nown notes" {
		t.Errorf("rendered_context: %q", out.RenderedContext)
	}
}

func TestMCP_GetTrack_RequiresTrackRef(t *testing.T) {
	fake := newFakeDaemon(t)
	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: "get_track", Arguments: map[string]any{"track": "  "}},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError || !strings.Contains(textOf(res), "track is required") {
		t.Errorf("expected track rejection, got %q", textOf(res))
	}
	if len(fake.requestTypes()) != 0 {
		t.Errorf("invalid arg call leaked to daemon: %v", fake.requestTypes())
	}
}

func TestMCP_ListTracks_ForwardsProjectPath(t *testing.T) {
	fake := newFakeDaemon(t)

	var captured daemon.ListTracksRequest
	fake.handle(daemon.MsgListTracks, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&captured)
		resp, _ := daemon.NewMessage(daemon.MsgListTracks, daemon.ListTracksResponse{
			Tracks: []daemon.TrackInfo{{ID: 1, Slug: "payments-api", ContextPreview: "seed", ContextLen: 4}},
		})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "list_tracks",
			Arguments: map[string]any{"project_path": "/tmp/some-project"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(res))
	}
	if captured.ProjectPath != "/tmp/some-project" {
		t.Errorf("ProjectPath = %q", captured.ProjectPath)
	}
	if !strings.Contains(textOf(res), "payments-api") {
		t.Errorf("result should list tracks, got %s", textOf(res))
	}
}

func TestMCP_CreateTask_PassesTrackThrough(t *testing.T) {
	fake := newFakeDaemon(t)

	var captured daemon.CreateTaskRequest
	fake.handle(daemon.MsgCreateTask, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&captured)
		resp, _ := daemon.NewMessage(daemon.MsgCreateTask, daemon.CreateTaskResponse{
			Task: daemon.TaskInfo{ID: 1, Status: "pending"},
		})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "create_task",
			Arguments: map[string]any{
				"input":        "do the thing",
				"project_path": "/tmp/some-project",
				"workflow":     "implement",
				"track":        "payments-api",
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(res))
	}
	if captured.Track != "payments-api" {
		t.Errorf("Track = %q, want payments-api", captured.Track)
	}
}

func TestMCP_CreateTasksAndWait_PassesTrackThrough(t *testing.T) {
	fake := newFakeDaemon(t)

	var captured daemon.CreateTasksAndWaitRequest
	fake.handle(daemon.MsgCreateTasksAndWait, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&captured)
		resp, _ := daemon.NewMessage(daemon.MsgCreateTasksAndWait, daemon.CreateTasksAndWaitResponse{
			ParentTaskID: captured.ParentTaskID,
			Children:     []daemon.TaskInfo{{ID: 2}},
		})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "create_tasks_and_wait",
			Arguments: map[string]any{
				"parent_task_id": 1,
				"tasks": []map[string]any{
					{"input": "child a", "workflow": "implement", "track": "payments-api"},
					{"input": "child b", "workflow": "implement", "track": "none"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(res))
	}
	if len(captured.Tasks) != 2 {
		t.Fatalf("captured %d tasks", len(captured.Tasks))
	}
	if captured.Tasks[0].Track != "payments-api" || captured.Tasks[1].Track != "none" {
		t.Errorf("tracks = %q, %q", captured.Tasks[0].Track, captured.Tasks[1].Track)
	}
}
