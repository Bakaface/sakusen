package mcp

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/Bakaface/sakusen/internal/task"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestPublishItemsRequestEnvDefaults(t *testing.T) {
	t.Setenv("SAKUSEN_TASK_ID", "77")
	t.Setenv("SAKUSEN_STEP", "plan")

	req, err := publishItemsRequest(PublishItemsArgs{Items: []PublishItemArg{{Input: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if req.TaskID != 77 || req.StepName != "plan" {
		t.Errorf("env defaults not applied: %+v", req)
	}

	req, err = publishItemsRequest(PublishItemsArgs{TaskID: 5, StepName: "design", Items: []PublishItemArg{{Input: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if req.TaskID != 5 || req.StepName != "design" {
		t.Errorf("explicit args should win over env: %+v", req)
	}
}

func TestPublishItemsRequestRequiresTaskAndStep(t *testing.T) {
	t.Setenv("SAKUSEN_TASK_ID", "")
	t.Setenv("SAKUSEN_STEP", "")

	if _, err := publishItemsRequest(PublishItemsArgs{StepName: "plan"}); err == nil || !strings.Contains(err.Error(), "task_id is required") {
		t.Errorf("missing task: err = %v", err)
	}
	if _, err := publishItemsRequest(PublishItemsArgs{TaskID: 1}); err == nil || !strings.Contains(err.Error(), "step_name is required") {
		t.Errorf("missing step: err = %v", err)
	}
}

func TestPublishItemsRequestMapsItems(t *testing.T) {
	req, err := publishItemsRequest(PublishItemsArgs{TaskID: 1, StepName: "plan", Items: []PublishItemArg{
		{Input: "do a", Slug: "a", Title: "A"},
		{Input: "do b", Needs: []string{"a"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []task.PublishedItem{
		{Slug: "a", Title: "A", Input: "do a"},
		{Input: "do b", Needs: []string{"a"}},
	}
	if !reflect.DeepEqual(req.Items, want) {
		t.Errorf("items = %+v, want %+v", req.Items, want)
	}
}

func TestMCP_PublishItems_RoundTrip(t *testing.T) {
	fake := newFakeDaemon(t)
	var captured daemon.PublishItemsRequest
	fake.handle(daemon.MsgPublishItems, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&captured)
		resp, _ := daemon.NewMessage(daemon.MsgPublishItems, daemon.PublishItemsResponse{
			Count: 1, Items: []task.PublishedItem{{Slug: "item-1", Input: "x"}},
		})
		return resp
	})
	c := startMCPServer(t, fake)
	t.Setenv("SAKUSEN_TASK_ID", "9")
	t.Setenv("SAKUSEN_STEP", "plan")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := c.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      "publish_items",
		Arguments: map[string]any{"items": []any{map[string]any{"input": "x"}}},
	}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}
	if captured.TaskID != 9 || captured.StepName != "plan" || len(captured.Items) != 1 {
		t.Errorf("request = %+v", captured)
	}
	if !strings.Contains(textOf(res), "item-1") {
		t.Errorf("result %q should carry the normalized slug", textOf(res))
	}
}
