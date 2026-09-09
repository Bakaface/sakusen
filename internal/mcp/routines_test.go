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

func TestMCP_ListRoutines_PassesThroughFields(t *testing.T) {
	fired := time.Date(2026, 6, 2, 3, 0, 0, 0, time.UTC)
	next := time.Date(2026, 6, 3, 3, 0, 0, 0, time.UTC)
	fake := newFakeDaemon(t)
	fake.handle(daemon.MsgListPeriodics, func(*daemon.Message) *daemon.Message {
		resp, _ := daemon.NewMessage(daemon.MsgListPeriodics, daemon.ListPeriodicsResponse{
			Periodics: []daemon.PeriodicInfo{
				{
					Name: "nightly", Description: "Nightly sweep", WorkflowRef: "sweep",
					Cadence: "0 3 * * *", NextFireAt: next, LastFiredAt: &fired, Paused: true,
				},
				{Name: "digest-now", WorkflowRef: "digest", RequiresInput: true, NextFireAt: next},
			},
		})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "list_routines",
			Arguments: map[string]any{"project_path": "/tmp/some-repo"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}

	var payload struct {
		Routines []routineSummary `json:"routines"`
	}
	if err := json.Unmarshal([]byte(textOf(res)), &payload); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, textOf(res))
	}
	if len(payload.Routines) != 2 {
		t.Fatalf("expected 2 routines, got %d", len(payload.Routines))
	}

	nightly := payload.Routines[0]
	if nightly.Workflow != "sweep" || nightly.Description != "Nightly sweep" || !nightly.Paused {
		t.Errorf("scheduled routine mangled: %+v", nightly)
	}
	if nightly.NextFireAt == nil || !nightly.NextFireAt.Equal(next) {
		t.Errorf("next_fire_at = %v, want %v", nightly.NextFireAt, next)
	}
	if nightly.RequiresInput {
		t.Error("nightly must not require input")
	}

	onDemand := payload.Routines[1]
	if onDemand.Cadence != "" || onDemand.NextFireAt != nil {
		t.Errorf("on-demand routine must carry no schedule: %+v", onDemand)
	}
	if !onDemand.RequiresInput {
		t.Error("requires_input must survive the projection")
	}
}

func TestMCP_RunRoutine_ForwardsNameAndInput(t *testing.T) {
	var got daemon.FirePeriodicNowRequest
	fake := newFakeDaemon(t)
	fake.handle(daemon.MsgFirePeriodicNow, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&got)
		resp, _ := daemon.NewMessage(daemon.MsgFirePeriodicNow, daemon.FirePeriodicNowResponse{
			Task: daemon.TaskInfo{ID: 42, Title: "digest-now @ 2026"},
		})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "run_routine",
			Arguments: map[string]any{
				"project_path": "/tmp/some-repo",
				"name":         "digest-now",
				"input":        "the section",
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}
	if got.Name != "digest-now" || got.Input != "the section" {
		t.Errorf("daemon request = %+v", got)
	}

	var task daemon.TaskInfo
	if err := json.Unmarshal([]byte(textOf(res)), &task); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, textOf(res))
	}
	if task.ID != 42 {
		t.Errorf("task id = %d, want 42", task.ID)
	}
}

func TestMCP_RunRoutine_RequiresName(t *testing.T) {
	fake := newFakeDaemon(t)
	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "run_routine",
			Arguments: map[string]any{"project_path": "/tmp/some-repo"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError || !strings.Contains(textOf(res), "name is required") {
		t.Fatalf("expected a name-required error, got: %v", textOf(res))
	}
}

// Daemon-side errors (e.g. a routine that needs an input argument) reach the
// caller as a tool error rather than being swallowed.
func TestMCP_RunRoutine_SurfacesDaemonError(t *testing.T) {
	fake := newFakeDaemon(t)
	fake.handle(daemon.MsgFirePeriodicNow, func(*daemon.Message) *daemon.Message {
		resp, _ := daemon.NewMessage(daemon.MsgError, daemon.ErrorResponse{
			Message: `routine "digest-now" requires an input argument`,
		})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "run_routine",
			Arguments: map[string]any{"project_path": "/tmp/some-repo", "name": "digest-now"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError || !strings.Contains(textOf(res), "requires an input argument") {
		t.Fatalf("expected the daemon error to surface, got: %v", textOf(res))
	}
}
