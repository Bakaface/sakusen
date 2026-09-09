package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/daemon"
	mcppkg "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// shortSocketPath returns a sun_path-friendly Unix socket path.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mcp")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, fmt.Sprintf("%d.sock", os.Getpid()))
}

// fakeDaemon is a hand-rolled stand-in for the real daemon that lets us
// assert on the wire protocol the MCP server uses. It reads one request,
// looks it up in the handlers map, and writes the configured response.
type fakeDaemon struct {
	t        *testing.T
	listener net.Listener
	handlers map[daemon.MessageType]func(*daemon.Message) *daemon.Message

	mu       sync.Mutex
	received []daemon.MessageType
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	listener, err := net.Listen("unix", shortSocketPath(t))
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	f := &fakeDaemon{
		t:        t,
		listener: listener,
		handlers: map[daemon.MessageType]func(*daemon.Message) *daemon.Message{},
	}
	go f.serve()
	return f
}

func (f *fakeDaemon) socketPath() string {
	return f.listener.Addr().String()
}

func (f *fakeDaemon) handle(msgType daemon.MessageType, h func(*daemon.Message) *daemon.Message) {
	f.handlers[msgType] = h
}

func (f *fakeDaemon) requestTypes() []daemon.MessageType {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]daemon.MessageType, len(f.received))
	copy(out, f.received)
	return out
}

func (f *fakeDaemon) serve() {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handleConn(conn)
	}
}

func (f *fakeDaemon) handleConn(conn net.Conn) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		msg, err := daemon.DecodeMessage(scanner.Bytes())
		if err != nil {
			return
		}
		f.mu.Lock()
		f.received = append(f.received, msg.Type)
		f.mu.Unlock()

		h, ok := f.handlers[msg.Type]
		var resp *daemon.Message
		if !ok {
			resp, _ = daemon.NewMessage(daemon.MsgError, daemon.ErrorResponse{Message: fmt.Sprintf("unhandled: %s", msg.Type)})
		} else {
			resp = h(msg)
		}
		data, _ := daemon.EncodeMessage(resp)
		if _, err := conn.Write(data); err != nil {
			return
		}
	}
}

// startMCPServer wires the MCP server in-process against a fake daemon and
// returns a connected MCP client. The MCP server is otherwise identical to
// what `sakusen mcp` runs in production — same registerTools call.
func startMCPServer(t *testing.T, fake *fakeDaemon) *mcppkg.Client {
	t.Helper()

	// Several tools default their identity args from the workflow engine's env
	// vars. Clear them so a test run that itself happens inside a sakusen step
	// doesn't leak a task ID into tools under test; the env-defaulting tests
	// set them again after calling this helper.
	t.Setenv("SAKUSEN_TASK_ID", "")
	t.Setenv("SAKUSEN_STEP", "")

	cfg := &config.Config{}
	cfg.SocketPath = fake.socketPath()

	c := client.New(cfg)
	if err := c.Connect(); err != nil {
		t.Fatalf("daemon client connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	s := server.NewMCPServer("sakusen-test", "0.0.0", server.WithToolCapabilities(false))
	registerTools(s, c)

	mcpClient, err := mcppkg.NewInProcessClient(s)
	if err != nil {
		t.Fatalf("new in-process client: %v", err)
	}
	t.Cleanup(func() { mcpClient.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mcpClient.Start(ctx); err != nil {
		t.Fatalf("mcp client start: %v", err)
	}
	if _, err := mcpClient.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "test", Version: "0.0.0"},
		},
	}); err != nil {
		t.Fatalf("mcp initialize: %v", err)
	}
	return mcpClient
}

func TestMCP_ListsToolsAdvertisedToClients(t *testing.T) {
	fake := newFakeDaemon(t)
	c := startMCPServer(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	got := map[string]bool{}
	for _, tool := range resp.Tools {
		got[tool.Name] = true
	}
	for _, want := range []string{
		"create_task", "create_tasks_and_wait", "wait_for_tasks", "list_workflows",
		"get_task", "list_tasks", "retry_task", "advance_task", "stop_task",
		"continue_task", "update_task", "update_step_context",
		"create_track", "get_track", "update_track", "list_tracks",
		"list_routines", "run_routine",
	} {
		if !got[want] {
			t.Errorf("tool %q not advertised; got %v", want, got)
		}
	}
	// The consolidated envelopes replaced these outright — no deprecated aliases.
	for _, gone := range []string{
		"update_task_input", "update_task_dependencies",
		"update_track_context", "update_track_description",
	} {
		if got[gone] {
			t.Errorf("removed tool %q is still advertised", gone)
		}
	}
	if len(resp.Tools) != 18 {
		t.Errorf("tool count: got %d, want 18 — %v", len(resp.Tools), got)
	}
}

func TestMCP_ListWorkflows_FromExplicitProjectPath(t *testing.T) {
	fake := newFakeDaemon(t)
	fake.handle(daemon.MsgListWorkflows, func(msg *daemon.Message) *daemon.Message {
		var req daemon.ListWorkflowsRequest
		_ = msg.DecodePayload(&req)
		if !filepath.IsAbs(req.ProjectPath) {
			resp, _ := daemon.NewMessage(daemon.MsgError, daemon.ErrorResponse{
				Message: fmt.Sprintf("expected absolute path, got %q", req.ProjectPath),
			})
			return resp
		}
		resp, _ := daemon.NewMessage(daemon.MsgListWorkflows, daemon.ListWorkflowsResponse{
			ProjectPath: req.ProjectPath,
			ProjectName: "test-project",
			Workflows: []daemon.WorkflowSummary{
				{Name: "implement", Description: "Plan + implement", FirstStepIsTmux: false,
					Steps: []daemon.WorkflowStepSummary{{Name: "plan"}, {Name: "implement"}}},
				{Name: "tmux-session", FirstStepIsTmux: true,
					Steps: []daemon.WorkflowStepSummary{{Name: "session", Tmux: true}}},
			},
		})
		return resp
	})

	c := startMCPServer(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "list_workflows",
			Arguments: map[string]any{"project_path": "/tmp/some-repo"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}

	var payload daemon.ListWorkflowsResponse
	if err := json.Unmarshal([]byte(textOf(res)), &payload); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, textOf(res))
	}
	if payload.ProjectName != "test-project" {
		t.Errorf("ProjectName: got %q, want test-project", payload.ProjectName)
	}
	if len(payload.Workflows) != 2 {
		t.Fatalf("Workflows: got %d, want 2", len(payload.Workflows))
	}
	if !payload.Workflows[1].FirstStepIsTmux {
		t.Errorf("expected second workflow to be flagged FirstStepIsTmux")
	}
}

func TestMCP_ListWorkflows_PinFieldsAndFullySpec(t *testing.T) {
	worktreeTrue := true
	fake := newFakeDaemon(t)
	fake.handle(daemon.MsgListWorkflows, func(msg *daemon.Message) *daemon.Message {
		resp, _ := daemon.NewMessage(daemon.MsgListWorkflows, daemon.ListWorkflowsResponse{
			ProjectPath: "/tmp/proj",
			ProjectName: "pinned-project",
			Workflows: []daemon.WorkflowSummary{
				{
					Name:      "pinned-impl",
					Worktree:  &worktreeTrue,
					Branch:    "feat/{{task.slug}}",
					Checkout:  "",
					Target:    "main",
					FullySpec: true,
					Steps:     []daemon.WorkflowStepSummary{{Name: "implement"}},
				},
				{
					Name:      "unpinned-impl",
					FullySpec: false,
					Steps:     []daemon.WorkflowStepSummary{{Name: "implement"}},
				},
			},
		})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "list_workflows",
			Arguments: map[string]any{"project_path": "/tmp/proj"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}

	var payload daemon.ListWorkflowsResponse
	if err := json.Unmarshal([]byte(textOf(res)), &payload); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, textOf(res))
	}
	if len(payload.Workflows) != 2 {
		t.Fatalf("Workflows: got %d, want 2", len(payload.Workflows))
	}

	pinned := payload.Workflows[0]
	if pinned.Worktree == nil || !*pinned.Worktree {
		t.Errorf("pinned.Worktree: want *true, got %v", pinned.Worktree)
	}
	if pinned.Branch != "feat/{{task.slug}}" {
		t.Errorf("pinned.Branch: got %q, want feat/{{task.slug}}", pinned.Branch)
	}
	if pinned.Target != "main" {
		t.Errorf("pinned.Target: got %q, want main", pinned.Target)
	}
	if !pinned.FullySpec {
		t.Errorf("pinned.FullySpec: want true")
	}

	unpinned := payload.Workflows[1]
	if unpinned.FullySpec {
		t.Errorf("unpinned.FullySpec: want false")
	}
}

func TestMCP_CreateTask_PassesAllFields(t *testing.T) {
	fake := newFakeDaemon(t)

	var captured daemon.CreateTaskRequest
	fake.handle(daemon.MsgCreateTask, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&captured)
		resp, _ := daemon.NewMessage(daemon.MsgCreateTask, daemon.CreateTaskResponse{
			Task: daemon.TaskInfo{
				ID:     42,
				Title:  "Implement login page",
				Status: "init",
			},
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
				"input":         "Implement the login page",
				"slug":          "login-page",
				"project_path":  "/tmp/proj",
				"workflow":      "implement",
				"priority":      "high",
				"branch_name":   "feat/{{task.slug}}",
				"target_branch": "develop",
				"images":        []string{"/tmp/a.png"},
				"blocked_by":    []int{7, 8},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}

	if captured.Input != "Implement the login page" {
		t.Errorf("Input: %q", captured.Input)
	}
	if captured.ProjectPath != "/tmp/proj" {
		t.Errorf("ProjectPath: %q", captured.ProjectPath)
	}
	if captured.Slug != "login-page" {
		t.Errorf("Slug: %q", captured.Slug)
	}
	if captured.Workflow != "implement" {
		t.Errorf("Workflow: %q", captured.Workflow)
	}
	if captured.Priority != "high" {
		t.Errorf("Priority: %q", captured.Priority)
	}
	if captured.BranchName != "feat/{{task.slug}}" {
		t.Errorf("BranchName: %q", captured.BranchName)
	}
	if captured.TargetBranch != "develop" {
		t.Errorf("TargetBranch: %q", captured.TargetBranch)
	}
	if len(captured.Images) != 1 || captured.Images[0] != "/tmp/a.png" {
		t.Errorf("Images: %v", captured.Images)
	}
	if len(captured.BlockedBy) != 2 || captured.BlockedBy[0] != 7 || captured.BlockedBy[1] != 8 {
		t.Errorf("BlockedBy: %v", captured.BlockedBy)
	}

	var out daemon.TaskInfo
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatalf("unmarshal task: %v", err)
	}
	if out.ID != 42 {
		t.Errorf("returned task ID: %d, want 42", out.ID)
	}
}

func TestMCP_CreateTask_RequiresWorkflow(t *testing.T) {
	fake := newFakeDaemon(t)
	// No MsgCreateTask handler on purpose — the tool must reject the call
	// before ever talking to the daemon.
	c := startMCPServer(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cases := map[string]map[string]any{
		"plain task": {
			"input":        "do something",
			"project_path": "/tmp/proj",
		},
		// checkout_branch tasks still run their workflow steps, so they are
		// deliberately NOT exempt from the requirement.
		"checkout_branch task": {
			"checkout_branch": "feat/existing",
			"project_path":    "/tmp/proj",
		},
	}
	for name, arguments := range cases {
		res, err := c.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: "create_task", Arguments: arguments},
		})
		if err != nil {
			t.Fatalf("%s: CallTool: %v", name, err)
		}
		if !res.IsError {
			t.Fatalf("%s: expected tool error for missing workflow, got success: %s", name, textOf(res))
		}
		if !strings.Contains(textOf(res), "workflow is required") ||
			!strings.Contains(textOf(res), "list_workflows") {
			t.Errorf("%s: error should say workflow is required and point to list_workflows; got %q", name, textOf(res))
		}
	}

	for _, msgType := range fake.requestTypes() {
		if msgType == daemon.MsgCreateTask {
			t.Errorf("create_task should not be sent to the daemon when workflow is missing")
		}
	}
}

func TestMCP_CreateTask_TmuxDirectExemptFromWorkflowRequirement(t *testing.T) {
	fake := newFakeDaemon(t)

	var captured daemon.CreateTaskRequest
	fake.handle(daemon.MsgCreateTask, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&captured)
		resp, _ := daemon.NewMessage(daemon.MsgCreateTask, daemon.CreateTaskResponse{
			Task: daemon.TaskInfo{ID: 7, Status: "init"},
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
				"input":        "interactive session",
				"project_path": "/tmp/proj",
				"tmux_direct":  true,
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tmux_direct task without workflow should be accepted, got error: %s", textOf(res))
	}
	if !captured.TmuxDirect {
		t.Errorf("TmuxDirect should be forwarded as true")
	}
	if captured.Workflow != "" {
		t.Errorf("Workflow should be forwarded empty, got %q", captured.Workflow)
	}
}

func TestMCP_CreateTask_AdvertisesWorkflowAsRequired(t *testing.T) {
	fake := newFakeDaemon(t)
	c := startMCPServer(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	for _, tool := range resp.Tools {
		if tool.Name != "create_task" {
			continue
		}
		for _, req := range tool.InputSchema.Required {
			if req == "workflow" {
				return
			}
		}
		t.Fatalf("create_task schema should list workflow as required; got required=%v", tool.InputSchema.Required)
	}
	t.Fatalf("create_task tool not advertised")
}

func TestMCP_CreateTasksAndWait_RequiresWorkflowPerChild(t *testing.T) {
	fake := newFakeDaemon(t)
	// No MsgCreateTasksAndWait handler on purpose — validation must fail
	// before the daemon is contacted.
	c := startMCPServer(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "create_tasks_and_wait",
			Arguments: map[string]any{
				"parent_task_id": 1,
				"tasks": []map[string]any{
					// Child 1 is tmux_direct: exempt from the requirement.
					{"input": "interactive child", "tmux_direct": true},
					// Child 2 is a plain workflow task with no workflow: rejected.
					{"input": "plain child"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error for missing child workflow, got success: %s", textOf(res))
	}
	if !strings.Contains(textOf(res), "child 2") ||
		!strings.Contains(textOf(res), "workflow is required") ||
		!strings.Contains(textOf(res), "list_workflows") {
		t.Errorf("error should name child 2, say workflow is required, and point to list_workflows; got %q", textOf(res))
	}

	for _, msgType := range fake.requestTypes() {
		if msgType == daemon.MsgCreateTasksAndWait {
			t.Errorf("create_tasks_and_wait should not be sent to the daemon when a child workflow is missing")
		}
	}
}

func TestMCP_CreateTask_RejectsCwdOutsideRepo(t *testing.T) {
	// When project_path isn't supplied, the tool must fall back to
	// `git rev-parse --show-toplevel` on cwd. From an arbitrary tempdir
	// that's not a git repo, the call must fail cleanly rather than
	// pass an empty path to the daemon (which would silently create a
	// wrong-rooted project row).
	tmpDir, err := os.MkdirTemp("", "mcp-no-git")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	origCwd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	fake := newFakeDaemon(t)
	// Don't register a create handler — if the tool wrongly delegates to
	// the daemon, we'll see an "unhandled" error instead of our cwd error.
	c := startMCPServer(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "create_task",
			Arguments: map[string]any{"input": "hello", "workflow": "implement"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error for non-git cwd, got success: %s", textOf(res))
	}
	if !strings.Contains(textOf(res), "not inside a git repository") {
		t.Errorf("error should mention missing git repo; got %q", textOf(res))
	}

	for _, msgType := range fake.requestTypes() {
		if msgType == daemon.MsgCreateTask {
			t.Errorf("create_task should not be sent when cwd resolution fails")
		}
	}
}

func TestMCP_GetTask_AggregatesSections(t *testing.T) {
	fake := newFakeDaemon(t)
	fake.handle(daemon.MsgGetTask, func(msg *daemon.Message) *daemon.Message {
		resp, _ := daemon.NewMessage(daemon.MsgGetTask, daemon.GetTaskResponse{
			Task: daemon.TaskInfo{ID: 99, Title: "demo", Status: "running"},
		})
		return resp
	})
	fake.handle(daemon.MsgGetTaskSteps, func(msg *daemon.Message) *daemon.Message {
		resp, _ := daemon.NewMessage(daemon.MsgGetTaskSteps, daemon.GetTaskStepsResponse{
			Steps: []daemon.TaskStepDetail{
				{Name: "plan", Status: "completed", Context: "outline"},
				{Name: "implement", Status: "running"},
			},
		})
		return resp
	})
	fake.handle(daemon.MsgGetStepContexts, func(msg *daemon.Message) *daemon.Message {
		resp, _ := daemon.NewMessage(daemon.MsgGetStepContexts, daemon.GetStepContextsResponse{
			Steps: map[string]string{"plan": "outline"},
		})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "get_task",
			Arguments: map[string]any{
				"task_id":               99,
				"include_steps":         true,
				"include_step_contexts": true,
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}

	var out GetTaskResult
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Task == nil || out.Task.ID != 99 {
		t.Errorf("task: %+v", out.Task)
	}
	if len(out.Steps) != 2 {
		t.Errorf("steps: got %d, want 2", len(out.Steps))
	}
	if out.StepContexts["plan"] != "outline" {
		t.Errorf("StepContexts: %v", out.StepContexts)
	}

	// Verify the optional sections were actually requested.
	seen := map[daemon.MessageType]bool{}
	for _, mt := range fake.requestTypes() {
		seen[mt] = true
	}
	for _, want := range []daemon.MessageType{daemon.MsgGetTask, daemon.MsgGetTaskSteps, daemon.MsgGetStepContexts} {
		if !seen[want] {
			t.Errorf("expected request %s", want)
		}
	}
}

func TestMCP_GetTask_OmitsSectionsWhenNotRequested(t *testing.T) {
	fake := newFakeDaemon(t)
	fake.handle(daemon.MsgGetTask, func(msg *daemon.Message) *daemon.Message {
		resp, _ := daemon.NewMessage(daemon.MsgGetTask, daemon.GetTaskResponse{
			Task: daemon.TaskInfo{ID: 1, Title: "t", Status: "pending"},
		})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "get_task",
			Arguments: map[string]any{"task_id": 1},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(res))
	}

	for _, mt := range fake.requestTypes() {
		switch mt {
		case daemon.MsgGetTaskSteps, daemon.MsgGetStepContexts, daemon.MsgGetOutput, daemon.MsgGetLogs:
			t.Errorf("did not expect optional request %s when flags off", mt)
		}
	}
}

func TestMCP_GetTask_RejectsInvalidID(t *testing.T) {
	fake := newFakeDaemon(t)
	c := startMCPServer(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "get_task",
			Arguments: map[string]any{"task_id": 0},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected error for task_id=0")
	}

	if len(fake.requestTypes()) != 0 {
		t.Errorf("daemon should not be contacted for invalid id; got %v", fake.requestTypes())
	}
}

func TestMCP_UpdateStepContext_ForwardsPayload(t *testing.T) {
	fake := newFakeDaemon(t)

	var captured daemon.UpdateActiveStepContextRequest
	fake.handle(daemon.MsgUpdateActiveStepContext, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&captured)
		resp, _ := daemon.NewMessage(daemon.MsgOK, daemon.OKResponse{Message: "ok"})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "update_step_context",
			Arguments: map[string]any{
				"task_id":   42,
				"step_name": "implement",
				"context":   "canonical artifact body",
				"mode":      "append",
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}

	if captured.TaskID != 42 {
		t.Errorf("TaskID: %d, want 42", captured.TaskID)
	}
	if captured.StepName != "implement" {
		t.Errorf("StepName: %q", captured.StepName)
	}
	if captured.Context != "canonical artifact body" {
		t.Errorf("Context: %q", captured.Context)
	}
	if captured.Mode != "append" {
		t.Errorf("Mode: %q", captured.Mode)
	}
}

func TestMCP_UpdateStepContext_RejectsInvalidArgs(t *testing.T) {
	fake := newFakeDaemon(t)
	c := startMCPServer(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cases := []struct {
		name      string
		arguments map[string]any
		wantErr   string
	}{
		{
			name:      "task_id absent with no env fallback",
			arguments: map[string]any{"step_name": "implement", "context": "x"},
			wantErr:   "task_id is required (SAKUSEN_TASK_ID env var not set",
		},
		{
			name:      "empty step_name with no env fallback",
			arguments: map[string]any{"task_id": 1, "step_name": "  ", "context": "x"},
			wantErr:   "step_name is required (SAKUSEN_STEP env var not set",
		},
		{
			name:      "invalid mode",
			arguments: map[string]any{"task_id": 1, "step_name": "implement", "context": "x", "mode": "overwrite"},
			wantErr:   "invalid mode",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := c.CallTool(ctx, mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name:      "update_step_context",
					Arguments: tc.arguments,
				},
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

	// None of the rejected calls should have touched the daemon.
	for _, mt := range fake.requestTypes() {
		if mt == daemon.MsgUpdateActiveStepContext {
			t.Errorf("invalid arg call leaked to daemon")
		}
	}
}

func TestMCP_UpdateStepContext_DefaultsModeToReplace(t *testing.T) {
	fake := newFakeDaemon(t)

	var captured daemon.UpdateActiveStepContextRequest
	fake.handle(daemon.MsgUpdateActiveStepContext, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&captured)
		resp, _ := daemon.NewMessage(daemon.MsgOK, daemon.OKResponse{Message: "ok"})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "update_step_context",
			Arguments: map[string]any{
				"task_id":   7,
				"step_name": "implement",
				"context":   "value",
			},
		},
	}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	if captured.Mode != "replace" {
		t.Errorf("expected default mode=replace, got %q", captured.Mode)
	}
}

func TestMCP_RetryTask_ForwardsStepName(t *testing.T) {
	fake := newFakeDaemon(t)

	var captured daemon.RetryTaskRequest
	fake.handle(daemon.MsgRetryTask, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&captured)
		resp, _ := daemon.NewMessage(daemon.MsgRetryTask, daemon.RetryTaskResponse{
			Task: daemon.TaskInfo{ID: 42, Title: "demo", Status: "pending"},
		})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "retry_task",
			Arguments: map[string]any{"task_id": 42, "step_name": "implement"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}

	if captured.TaskID != 42 {
		t.Errorf("TaskID: %d, want 42", captured.TaskID)
	}
	if captured.StepName != "implement" {
		t.Errorf("StepName: %q, want implement", captured.StepName)
	}

	var out daemon.TaskInfo
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatalf("unmarshal task: %v", err)
	}
	if out.ID != 42 || out.Status != "pending" {
		t.Errorf("returned task: %+v", out)
	}
}

func TestMCP_RetryTask_RejectsInvalidID(t *testing.T) {
	fake := newFakeDaemon(t)
	c := startMCPServer(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "retry_task",
			Arguments: map[string]any{"task_id": 0},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected error for task_id=0")
	}
	if len(fake.requestTypes()) != 0 {
		t.Errorf("daemon should not be contacted for invalid id; got %v", fake.requestTypes())
	}
}

func TestMCP_UpdateTask_AppliesFieldsInOrder(t *testing.T) {
	fake := newFakeDaemon(t)

	var fields []daemon.UpdateFieldRequest
	var priorities []daemon.UpdatePriorityRequest
	var deps []daemon.UpdateDependencyRequest
	var order []daemon.MessageType

	fake.handle(daemon.MsgUpdateField, func(msg *daemon.Message) *daemon.Message {
		var req daemon.UpdateFieldRequest
		_ = msg.DecodePayload(&req)
		fields = append(fields, req)
		order = append(order, daemon.MsgUpdateField)
		resp, _ := daemon.NewMessage(daemon.MsgUpdateField, daemon.UpdateFieldResponse{
			Task: daemon.TaskInfo{ID: req.TaskID},
		})
		return resp
	})
	fake.handle(daemon.MsgUpdatePriority, func(msg *daemon.Message) *daemon.Message {
		var req daemon.UpdatePriorityRequest
		_ = msg.DecodePayload(&req)
		priorities = append(priorities, req)
		order = append(order, daemon.MsgUpdatePriority)
		resp, _ := daemon.NewMessage(daemon.MsgUpdatePriority, daemon.UpdatePriorityResponse{
			Task: daemon.TaskInfo{ID: req.TaskID},
		})
		return resp
	})
	fake.handle(daemon.MsgUpdateDependency, func(msg *daemon.Message) *daemon.Message {
		var req daemon.UpdateDependencyRequest
		_ = msg.DecodePayload(&req)
		deps = append(deps, req)
		order = append(order, daemon.MsgUpdateDependency)
		resp, _ := daemon.NewMessage(daemon.MsgUpdateDependency, daemon.UpdateDependencyResponse{
			Task: daemon.TaskInfo{ID: req.TaskID, BlockedBy: []int64{req.BlockedBy}},
		})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "update_task",
			Arguments: map[string]any{
				"task_id":           10,
				"input":             "new body",
				"title":             "New title",
				"priority":          "urgent",
				"add_blocked_by":    []int{5, 6},
				"remove_blocked_by": []int{7},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}

	wantFields := []daemon.UpdateFieldRequest{
		{TaskID: 10, Field: "input", Value: "new body"},
		{TaskID: 10, Field: "title", Value: "New title"},
	}
	if len(fields) != len(wantFields) {
		t.Fatalf("field requests: got %+v, want %+v", fields, wantFields)
	}
	for i, w := range wantFields {
		if fields[i] != w {
			t.Errorf("field request[%d]: got %+v, want %+v", i, fields[i], w)
		}
	}

	if len(priorities) != 1 || priorities[0].Priority != "urgent" || priorities[0].TaskID != 10 {
		t.Errorf("priority requests: %+v", priorities)
	}

	wantDeps := []daemon.UpdateDependencyRequest{
		{TaskID: 10, BlockedBy: 7, Action: "remove"},
		{TaskID: 10, BlockedBy: 5, Action: "add"},
		{TaskID: 10, BlockedBy: 6, Action: "add"},
	}
	if len(deps) != len(wantDeps) {
		t.Fatalf("dependency requests: got %+v, want %+v", deps, wantDeps)
	}
	for i, w := range wantDeps {
		if deps[i] != w {
			t.Errorf("dependency request[%d]: got %+v, want %+v", i, deps[i], w)
		}
	}

	wantOrder := []daemon.MessageType{
		daemon.MsgUpdateField, daemon.MsgUpdateField, daemon.MsgUpdatePriority,
		daemon.MsgUpdateDependency, daemon.MsgUpdateDependency, daemon.MsgUpdateDependency,
	}
	if len(order) != len(wantOrder) {
		t.Fatalf("call order: got %v, want %v", order, wantOrder)
	}
	for i, w := range wantOrder {
		if order[i] != w {
			t.Fatalf("call order: got %v, want %v", order, wantOrder)
		}
	}

	// The returned task must be the result of the LAST daemon call.
	var out daemon.TaskInfo
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatalf("unmarshal task: %v", err)
	}
	if len(out.BlockedBy) != 1 || out.BlockedBy[0] != 6 {
		t.Errorf("returned task should reflect last update, got %+v", out)
	}
}

// TestMCP_UpdateTask_PartialFieldsOnly pins that omitted fields cause no
// daemon traffic at all — an input-only edit must not touch priority or deps.
func TestMCP_UpdateTask_PartialFieldsOnly(t *testing.T) {
	fake := newFakeDaemon(t)
	fake.handle(daemon.MsgUpdateField, func(msg *daemon.Message) *daemon.Message {
		var req daemon.UpdateFieldRequest
		_ = msg.DecodePayload(&req)
		resp, _ := daemon.NewMessage(daemon.MsgUpdateField, daemon.UpdateFieldResponse{
			Task: daemon.TaskInfo{ID: req.TaskID, Input: req.Value},
		})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "update_task",
			Arguments: map[string]any{"task_id": 3, "input": "only the input"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}

	got := fake.requestTypes()
	if len(got) != 1 || got[0] != daemon.MsgUpdateField {
		t.Errorf("expected exactly one MsgUpdateField, got %v", got)
	}
}

// TestMCP_UpdateTask_PartialApplicationIsReported verifies that when a later
// change fails, the error says the earlier ones already landed.
func TestMCP_UpdateTask_PartialApplicationIsReported(t *testing.T) {
	fake := newFakeDaemon(t)
	fake.handle(daemon.MsgUpdateField, func(msg *daemon.Message) *daemon.Message {
		var req daemon.UpdateFieldRequest
		_ = msg.DecodePayload(&req)
		resp, _ := daemon.NewMessage(daemon.MsgUpdateField, daemon.UpdateFieldResponse{
			Task: daemon.TaskInfo{ID: req.TaskID},
		})
		return resp
	})
	fake.handle(daemon.MsgUpdateDependency, func(*daemon.Message) *daemon.Message {
		resp, _ := daemon.NewMessage(daemon.MsgError, daemon.ErrorResponse{Message: "would create a cycle"})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "update_task",
			Arguments: map[string]any{
				"task_id":        4,
				"input":          "new body",
				"add_blocked_by": []int{9},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error, got success: %s", textOf(res))
	}
	if !strings.Contains(textOf(res), "would create a cycle") ||
		!strings.Contains(textOf(res), "earlier changes in this call were already applied") {
		t.Errorf("error should name the failure and the partial application, got %q", textOf(res))
	}
}

// TestMCP_UpdateTask_FirstChangeFailureOmitsPartialWarning: nothing landed, so
// the misleading "earlier changes already applied" note must be absent.
func TestMCP_UpdateTask_FirstChangeFailureOmitsPartialWarning(t *testing.T) {
	fake := newFakeDaemon(t)
	fake.handle(daemon.MsgUpdateField, func(*daemon.Message) *daemon.Message {
		resp, _ := daemon.NewMessage(daemon.MsgError, daemon.ErrorResponse{Message: "unknown task reference"})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "update_task",
			Arguments: map[string]any{"task_id": 4, "input": "bad {{tasks.99.context}}"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error, got success: %s", textOf(res))
	}
	if strings.Contains(textOf(res), "earlier changes") {
		t.Errorf("no change landed, so the partial-application note must be absent: %q", textOf(res))
	}
}

func TestMCP_UpdateTask_RejectsInvalidArgs(t *testing.T) {
	fake := newFakeDaemon(t)
	c := startMCPServer(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cases := []struct {
		name      string
		arguments map[string]any
		wantErr   string
	}{
		{
			name:      "task_id zero",
			arguments: map[string]any{"task_id": 0, "input": "x"},
			wantErr:   "task_id must be a positive integer",
		},
		{
			name:      "no fields",
			arguments: map[string]any{"task_id": 1},
			wantErr:   "at least one of input, title, priority, add_blocked_by or remove_blocked_by",
		},
		{
			name:      "blank input",
			arguments: map[string]any{"task_id": 1, "input": "   "},
			wantErr:   "input must not be empty",
		},
		{
			name:      "blank title",
			arguments: map[string]any{"task_id": 1, "title": "  "},
			wantErr:   "title must not be empty",
		},
		{
			name:      "invalid priority",
			arguments: map[string]any{"task_id": 1, "priority": "yesterday"},
			wantErr:   "invalid priority",
		},
		{
			name:      "self dependency",
			arguments: map[string]any{"task_id": 5, "add_blocked_by": []int{5}},
			wantErr:   "cannot depend on itself",
		},
		{
			name:      "non-positive dependency id",
			arguments: map[string]any{"task_id": 5, "remove_blocked_by": []int{0}},
			wantErr:   "must be positive integers",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := c.CallTool(ctx, mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name:      "update_task",
					Arguments: tc.arguments,
				},
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

func TestMCP_ListTasks_Global(t *testing.T) {
	fake := newFakeDaemon(t)
	fake.handle(daemon.MsgListTasks, func(msg *daemon.Message) *daemon.Message {
		var req daemon.ListTasksRequest
		_ = msg.DecodePayload(&req)
		if req.ProjectName != "" || req.ProjectID != 0 {
			resp, _ := daemon.NewMessage(daemon.MsgError, daemon.ErrorResponse{
				Message: fmt.Sprintf("expected unfiltered request, got %+v", req),
			})
			return resp
		}
		resp, _ := daemon.NewMessage(daemon.MsgTaskList, daemon.TaskListResponse{
			Tasks: []daemon.TaskInfo{
				{ID: 1, Title: "a", Status: "completed", ProjectName: "p1", ProjectPath: "/tmp/p1"},
				{ID: 2, Title: "b", Status: "failed", ProjectName: "p2", ProjectPath: "/tmp/p2"},
			},
		})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "list_tasks",
			Arguments: map[string]any{"all_projects": true},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}

	var out ListTasksResult
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Count != 2 || len(out.Tasks) != 2 {
		t.Fatalf("count: %d, tasks: %d, want 2/2", out.Count, len(out.Tasks))
	}
	if out.ProjectName != "" || out.ProjectPath != "" {
		t.Errorf("global listing should not set project fields: %q %q", out.ProjectName, out.ProjectPath)
	}
	// Newest-first ordering: equal CreatedAt falls back to descending ID.
	if out.Tasks[0].ID != 2 || out.Tasks[0].Status != "failed" {
		t.Errorf("first task: %+v", out.Tasks[0])
	}
	if out.Tasks[1].ID != 1 {
		t.Errorf("second task: %+v", out.Tasks[1])
	}
}

func TestMCP_ListTasks_ProjectScopedWithStatusFilter(t *testing.T) {
	fake := newFakeDaemon(t)
	fake.handle(daemon.MsgListTasks, func(msg *daemon.Message) *daemon.Message {
		var req daemon.ListTasksRequest
		_ = msg.DecodePayload(&req)
		if req.ProjectName != "some-repo" {
			resp, _ := daemon.NewMessage(daemon.MsgError, daemon.ErrorResponse{
				Message: fmt.Sprintf("expected project_name=some-repo, got %q", req.ProjectName),
			})
			return resp
		}
		resp, _ := daemon.NewMessage(daemon.MsgTaskList, daemon.TaskListResponse{
			Tasks: []daemon.TaskInfo{
				// Same basename, different repo — must be narrowed out.
				{ID: 1, Title: "other", Status: "failed", ProjectName: "some-repo", ProjectPath: "/elsewhere/some-repo"},
				{ID: 2, Title: "match-failed", Status: "failed", ProjectName: "some-repo", ProjectPath: "/tmp/some-repo"},
				{ID: 3, Title: "match-done", Status: "completed", ProjectName: "some-repo", ProjectPath: "/tmp/some-repo"},
			},
		})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "list_tasks",
			Arguments: map[string]any{"project_path": "/tmp/some-repo", "status": "failed"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}

	var out ListTasksResult
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ProjectName != "some-repo" || out.ProjectPath != "/tmp/some-repo" {
		t.Errorf("project fields: %q %q", out.ProjectName, out.ProjectPath)
	}
	if out.Count != 1 || len(out.Tasks) != 1 {
		t.Fatalf("count: %d, tasks: %d, want 1/1 — got %+v", out.Count, len(out.Tasks), out.Tasks)
	}
	if out.Tasks[0].ID != 2 {
		t.Errorf("expected task 2 (path+status match), got %+v", out.Tasks[0])
	}
}

// textOf collapses the content slice into a single string, since our tools
// always return one TextContent block.
func textOf(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// TestMCP_CreateTasksAndWait_PassesSlugThrough verifies the optional per-child
// slug reaches the daemon request untouched (normalization happens daemon-side).
func TestMCP_CreateTasksAndWait_PassesSlugThrough(t *testing.T) {
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
					{"input": "child a", "workflow": "implement", "slug": "child-a"},
					{"input": "child b", "workflow": "implement"},
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
	if captured.Tasks[0].Slug != "child-a" {
		t.Errorf("child a slug = %q, want child-a", captured.Tasks[0].Slug)
	}
	if captured.Tasks[1].Slug != "" {
		t.Errorf("child b slug = %q, want empty (auto-generated)", captured.Tasks[1].Slug)
	}
}

// listTasksFake serves a fixed global task list so the ordering/filter/limit
// tests can assert purely on the MCP-side narrowing.
func listTasksFake(t *testing.T, tasks []daemon.TaskInfo) *fakeDaemon {
	t.Helper()
	fake := newFakeDaemon(t)
	fake.handle(daemon.MsgListTasks, func(*daemon.Message) *daemon.Message {
		resp, _ := daemon.NewMessage(daemon.MsgTaskList, daemon.TaskListResponse{Tasks: tasks})
		return resp
	})
	return fake
}

func callListTasks(t *testing.T, c *mcppkg.Client, args map[string]any) ListTasksResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: "list_tasks", Arguments: args},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}
	var out ListTasksResult
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, textOf(res))
	}
	return out
}

func TestMCP_ListTasks_NewestFirstWithDefaultLimit(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var tasks []daemon.TaskInfo
	for i := 1; i <= 60; i++ {
		tasks = append(tasks, daemon.TaskInfo{
			ID:        int64(i),
			Title:     fmt.Sprintf("task %d", i),
			Status:    "completed",
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}

	c := startMCPServer(t, listTasksFake(t, tasks))

	out := callListTasks(t, c, map[string]any{"all_projects": true})
	if out.Count != 50 || len(out.Tasks) != 50 {
		t.Fatalf("default limit: count %d, tasks %d, want 50/50", out.Count, len(out.Tasks))
	}
	if out.TotalMatched != 60 {
		t.Errorf("total_matched should report the untruncated size, got %d", out.TotalMatched)
	}
	if out.Tasks[0].ID != 60 || out.Tasks[49].ID != 11 {
		t.Errorf("expected newest-first window 60..11, got %d..%d", out.Tasks[0].ID, out.Tasks[49].ID)
	}

	out = callListTasks(t, c, map[string]any{"all_projects": true, "limit": 3})
	if len(out.Tasks) != 3 || out.Tasks[0].ID != 60 || out.Tasks[2].ID != 58 {
		t.Errorf("limit=3: %+v", out.Tasks)
	}
	if out.TotalMatched != 60 {
		t.Errorf("limit=3 total_matched: got %d, want 60", out.TotalMatched)
	}

	out = callListTasks(t, c, map[string]any{"all_projects": true, "limit": 0})
	if out.Count != 60 || out.TotalMatched != 60 {
		t.Errorf("limit=0 should be unlimited, got count %d total_matched %d", out.Count, out.TotalMatched)
	}
}

func TestMCP_ListTasks_TrackFilterAndSummaryFields(t *testing.T) {
	trackID := int64(4)
	otherTrackID := int64(9)
	tasks := []daemon.TaskInfo{
		{ID: 1, Title: "on payments", Slug: "on-payments", Status: "completed", Track: "payments-api", TrackID: &trackID},
		{ID: 2, Title: "on billing", Slug: "on-billing", Status: "completed", Track: "billing", TrackID: &otherTrackID},
		{ID: 3, Title: "trackless", Slug: "trackless", Status: "completed"},
	}
	c := startMCPServer(t, listTasksFake(t, tasks))

	out := callListTasks(t, c, map[string]any{"all_projects": true, "track": "Payments-API"})
	if out.Count != 1 || out.Tasks[0].ID != 1 {
		t.Fatalf("slug filter (case-insensitive): %+v", out.Tasks)
	}
	if out.Tasks[0].Track != "payments-api" || out.Tasks[0].Slug != "on-payments" {
		t.Errorf("summary should carry track and slug: %+v", out.Tasks[0])
	}

	out = callListTasks(t, c, map[string]any{"all_projects": true, "track": "9"})
	if out.Count != 1 || out.Tasks[0].ID != 2 {
		t.Errorf("numeric track ID filter: %+v", out.Tasks)
	}

	out = callListTasks(t, c, map[string]any{"all_projects": true, "track": "nope"})
	if out.Count != 0 {
		t.Errorf("unknown track should match nothing, got %+v", out.Tasks)
	}
}

func TestMCP_ListTasks_RejectsNegativeLimit(t *testing.T) {
	fake := newFakeDaemon(t)
	fake.handle(daemon.MsgListTasks, func(*daemon.Message) *daemon.Message {
		resp, _ := daemon.NewMessage(daemon.MsgTaskList, daemon.TaskListResponse{})
		return resp
	})
	c := startMCPServer(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "list_tasks",
			Arguments: map[string]any{"all_projects": true, "limit": -1},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError || !strings.Contains(textOf(res), "limit must not be negative") {
		t.Errorf("expected negative-limit rejection, got %q", textOf(res))
	}
}

func TestMCP_StopTask_ForwardsAndReturnsTask(t *testing.T) {
	fake := newFakeDaemon(t)
	var captured daemon.StopTaskRequest
	fake.handle(daemon.MsgStopTask, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&captured)
		resp, _ := daemon.NewMessage(daemon.MsgStopTask, daemon.StopTaskResponse{
			// The daemon has no "stopped" status: the task keeps its own.
			Task: daemon.TaskInfo{ID: captured.TaskID, Status: "running"},
		})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: "stop_task", Arguments: map[string]any{"task_id": 12}},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}
	if captured.TaskID != 12 {
		t.Errorf("TaskID: %d, want 12", captured.TaskID)
	}
	var out daemon.TaskInfo
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatalf("unmarshal task: %v", err)
	}
	if out.ID != 12 || out.Status != "running" {
		t.Errorf("returned task: %+v", out)
	}
}

func TestMCP_StopTask_RejectsInvalidID(t *testing.T) {
	fake := newFakeDaemon(t)
	c := startMCPServer(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: "stop_task", Arguments: map[string]any{"task_id": 0}},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError || !strings.Contains(textOf(res), "task_id must be a positive integer") {
		t.Errorf("expected task_id rejection, got %q", textOf(res))
	}
	if len(fake.requestTypes()) != 0 {
		t.Errorf("invalid arg call leaked to daemon: %v", fake.requestTypes())
	}
}

// TestMCP_ContinueTask_AlwaysSetsTerminalOnly pins the guard that keeps agents
// off the human-gate approval path: the flag must be on the wire every time.
func TestMCP_ContinueTask_AlwaysSetsTerminalOnly(t *testing.T) {
	fake := newFakeDaemon(t)
	var captured daemon.ContinueTaskRequest
	fake.handle(daemon.MsgContinueTask, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&captured)
		resp, _ := daemon.NewMessage(daemon.MsgContinueTask, daemon.ContinueTaskResponse{
			Task: daemon.TaskInfo{ID: captured.TaskID, Status: "pending", Workflow: captured.Workflow},
		})
		return resp
	})

	c := startMCPServer(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "continue_task",
			Arguments: map[string]any{
				"task_id":  8,
				"workflow": "review",
				"prompt":   "address the review comments",
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}
	if !captured.TerminalOnly {
		t.Errorf("continue_task must always set terminal_only, got %+v", captured)
	}
	if captured.TaskID != 8 || captured.Workflow != "review" || captured.Prompt != "address the review comments" {
		t.Errorf("captured = %+v", captured)
	}

	var out daemon.TaskInfo
	if err := json.Unmarshal([]byte(textOf(res)), &out); err != nil {
		t.Fatalf("unmarshal task: %v", err)
	}
	if out.ID != 8 || out.Status != "pending" {
		t.Errorf("returned task: %+v", out)
	}
}

func TestMCP_ContinueTask_RejectsInvalidArgs(t *testing.T) {
	fake := newFakeDaemon(t)
	c := startMCPServer(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cases := []struct {
		name      string
		arguments map[string]any
		wantErr   string
	}{
		{
			name:      "task_id zero",
			arguments: map[string]any{"task_id": 0, "workflow": "review"},
			wantErr:   "task_id must be a positive integer",
		},
		{
			name:      "missing workflow",
			arguments: map[string]any{"task_id": 1},
			wantErr:   "workflow is required",
		},
		{
			name:      "blank workflow",
			arguments: map[string]any{"task_id": 1, "workflow": "  "},
			wantErr:   "workflow is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := c.CallTool(ctx, mcp.CallToolRequest{
				Params: mcp.CallToolParams{Name: "continue_task", Arguments: tc.arguments},
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

func TestMCP_UpdateStepContext_DefaultsFromEnv(t *testing.T) {
	fake := newFakeDaemon(t)
	var captured daemon.UpdateActiveStepContextRequest
	fake.handle(daemon.MsgUpdateActiveStepContext, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&captured)
		resp, _ := daemon.NewMessage(daemon.MsgOK, daemon.OKResponse{Message: "ok"})
		return resp
	})

	c := startMCPServer(t, fake)
	t.Setenv("SAKUSEN_TASK_ID", "77")
	t.Setenv("SAKUSEN_STEP", "grilling")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "update_step_context",
			Arguments: map[string]any{"context": "from env"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}
	if captured.TaskID != 77 || captured.StepName != "grilling" {
		t.Errorf("env defaults not applied: %+v", captured)
	}

	// Explicit arguments must win over the env.
	res, err = c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "update_step_context",
			Arguments: map[string]any{"task_id": 5, "step_name": "implement", "context": "explicit"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}
	if captured.TaskID != 5 || captured.StepName != "implement" {
		t.Errorf("explicit args should win over env: %+v", captured)
	}
}

func TestMCP_UpdateStepContext_RejectsUnparseableEnvTaskID(t *testing.T) {
	fake := newFakeDaemon(t)
	c := startMCPServer(t, fake)
	t.Setenv("SAKUSEN_TASK_ID", "not-a-number")
	t.Setenv("SAKUSEN_STEP", "implement")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "update_step_context",
			Arguments: map[string]any{"context": "x"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError || !strings.Contains(textOf(res), `invalid SAKUSEN_TASK_ID="not-a-number"`) {
		t.Errorf("expected env parse rejection, got %q", textOf(res))
	}
}

// TestMCP_WaitForTasks_DefaultsParentTaskIDFromEnv covers the shared env
// helper on the waits-on side, where parent_task_id is the defaulted arg.
func TestMCP_WaitForTasks_DefaultsParentTaskIDFromEnv(t *testing.T) {
	fake := newFakeDaemon(t)
	var captured daemon.WaitForTasksRequest
	fake.handle(daemon.MsgWaitForTasks, func(msg *daemon.Message) *daemon.Message {
		_ = msg.DecodePayload(&captured)
		resp, _ := daemon.NewMessage(daemon.MsgWaitForTasks, daemon.WaitForTasksResponse{
			Children: []daemon.TaskInfo{{ID: 2}},
		})
		return resp
	})

	c := startMCPServer(t, fake)
	t.Setenv("SAKUSEN_TASK_ID", "88")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "wait_for_tasks",
			Arguments: map[string]any{"child_task_ids": []int{2}},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}
	if captured.ParentTaskID != 88 {
		t.Errorf("parent_task_id should default from env, got %d", captured.ParentTaskID)
	}

	res, err = c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "wait_for_tasks",
			Arguments: map[string]any{"parent_task_id": 3, "child_task_ids": []int{2}},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %v", textOf(res))
	}
	if captured.ParentTaskID != 3 {
		t.Errorf("explicit parent_task_id should win over env, got %d", captured.ParentTaskID)
	}
}

// TestMCP_EnvDefaulting_RejectsNegativeExplicitID pins that only an OMITTED id
// falls back to the env — a negative one is a caller mistake, and silently
// substituting the running task's ID would write to the wrong task.
func TestMCP_EnvDefaulting_RejectsNegativeExplicitID(t *testing.T) {
	fake := newFakeDaemon(t)
	c := startMCPServer(t, fake)
	t.Setenv("SAKUSEN_TASK_ID", "77")
	t.Setenv("SAKUSEN_STEP", "implement")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cases := []struct {
		tool      string
		arguments map[string]any
		wantErr   string
	}{
		{"update_step_context", map[string]any{"task_id": -1, "context": "x"}, "task_id must be a positive integer"},
		{"update_track", map[string]any{"task_id": -1, "context": "x"}, "task_id must be a positive integer"},
		{"wait_for_tasks", map[string]any{"parent_task_id": -1, "child_task_ids": []int{2}}, "parent_task_id must be a positive integer"},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			res, err := c.CallTool(ctx, mcp.CallToolRequest{
				Params: mcp.CallToolParams{Name: tc.tool, Arguments: tc.arguments},
			})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if !res.IsError || !strings.Contains(textOf(res), tc.wantErr) {
				t.Errorf("expected %q, got %q", tc.wantErr, textOf(res))
			}
		})
	}

	if len(fake.requestTypes()) != 0 {
		t.Errorf("negative-id call leaked to daemon: %v", fake.requestTypes())
	}
}
