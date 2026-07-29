package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sortie/internal/daemon"
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

func TestMCP_UpdateTrackContext_ForwardsAndDefaultsToAppend(t *testing.T) {
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

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "update_track_context",
			Arguments: map[string]any{
				"task_id": 7,
				"context": "endpoint A done",
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(res))
	}

	if captured.TaskID != 7 || captured.Context != "endpoint A done" {
		t.Errorf("captured = %+v", captured)
	}
	if captured.Mode != "append" {
		t.Errorf("expected default mode=append, got %q", captured.Mode)
	}
}

func TestMCP_UpdateTrackContext_RejectsInvalidArgs(t *testing.T) {
	fake := newFakeDaemon(t)
	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cases := []struct {
		name      string
		arguments map[string]any
		wantErr   string
	}{
		{"zero task_id", map[string]any{"task_id": 0, "context": "x"}, "task_id must be a positive integer"},
		{"invalid mode", map[string]any{"task_id": 7, "context": "x", "mode": "merge"}, "invalid mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := c.CallTool(ctx, mcp.CallToolRequest{
				Params: mcp.CallToolParams{Name: "update_track_context", Arguments: tc.arguments},
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
		if mt == daemon.MsgUpdateTaskTrackContext {
			t.Errorf("invalid arg call leaked to daemon")
		}
	}
}

func TestMCP_UpdateTrackDescription_Forwards(t *testing.T) {
	fake := newFakeDaemon(t)

	var captured daemon.UpdateTaskTrackDescriptionRequest
	fake.handle(daemon.MsgUpdateTaskTrackDescription, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&captured)
		resp, _ := daemon.NewMessage(daemon.MsgOK, daemon.OKResponse{Message: "ok"})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "update_track_description",
			Arguments: map[string]any{
				"task_id":     7,
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

	if captured.TaskID != 7 || captured.Description != "Owns the payments API surface" {
		t.Errorf("captured = %+v", captured)
	}
}

func TestMCP_UpdateTrackDescription_RejectsInvalidArgs(t *testing.T) {
	fake := newFakeDaemon(t)
	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cases := []struct {
		name      string
		arguments map[string]any
		wantErr   string
	}{
		{"zero task_id", map[string]any{"task_id": 0, "description": "x"}, "task_id must be a positive integer"},
		{"blank description", map[string]any{"task_id": 7, "description": "   "}, "description must not be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := c.CallTool(ctx, mcp.CallToolRequest{
				Params: mcp.CallToolParams{Name: "update_track_description", Arguments: tc.arguments},
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
		if mt == daemon.MsgUpdateTaskTrackDescription {
			t.Errorf("invalid arg call leaked to daemon")
		}
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
					{"input": "child a", "track": "payments-api"},
					{"input": "child b", "track": "none"},
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
