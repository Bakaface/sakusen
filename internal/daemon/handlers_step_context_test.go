package daemon

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/task"
	"github.com/Bakaface/sakusen/internal/workflow"
)

// readOneMessage reads a single line-framed Message off the given conn and
// decodes it. Returns the message type and the raw payload error string when
// the message is a MsgError, so callers can assert on the daemon's reply.
func readOneMessage(t *testing.T, conn net.Conn) *Message {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !scanner.Scan() {
		t.Fatalf("no response from handler: %v", scanner.Err())
	}
	msg, err := DecodeMessage(scanner.Bytes())
	if err != nil {
		t.Fatalf("decode message: %v", err)
	}
	return msg
}

// pipeForHandler returns a (clientConn, serverConn) pair. The handler under
// test writes its response to serverConn; the test reads it from clientConn.
func pipeForHandler(t *testing.T) (clientConn, serverConn net.Conn) {
	t.Helper()
	clientConn, serverConn = net.Pipe()
	t.Cleanup(func() {
		clientConn.Close()
		serverConn.Close()
	})
	return clientConn, serverConn
}

// createRunningStep is a small fixture that creates a task in 'running' state
// with the given current step name and a corresponding running task_steps row.
func createRunningStep(t *testing.T, s *Server, projID int64, stepName string) *task.Task {
	t.Helper()
	tk, err := s.database.CreateTask(projID, "test task", "desc", "test", "default", "main", task.StatusRunning, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.database.UpdateTaskStep(tk.ID, 0, stepName); err != nil {
		t.Fatal(err)
	}
	if err := s.database.CreateTaskStep(tk.ID, stepName); err != nil {
		t.Fatal(err)
	}
	refreshed, err := s.database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	return refreshed
}

// createPausedTmuxStep builds the exact on-disk state of a tmux/human step
// paused at its approval gate: status 'tmux', current_step cleared, StepIndex
// bumped one past the step, and the step's task_steps row 'completed' — plus a
// cached project workflow (and a real Engine bound to it, mirroring
// getProjectContext's construction — the handler now resolves and writes the
// paused step entirely through workflow.Engine) so the handler can resolve the
// paused step (see workflow.PausedStep). stepName is placed at workflow index
// 1 (StepIndex 2 resolves to the paused step at index 1).
func createPausedTmuxStep(t *testing.T, s *Server, projID int64, stepName string) *task.Task {
	t.Helper()
	cfg := &config.Config{Workflows: []config.WorkflowConfig{{
		Name: "wf",
		Steps: []config.StepConfig{
			{Name: "initial_planning"},
			{Name: stepName, Human: true},
		},
	}}}
	repoRoot := "/tmp/sakusen-test"
	s.projectsMu.Lock()
	s.projects[projID] = &projectContext{
		cfg:      cfg,
		engine:   workflow.NewEngine(cfg, s.database, s.notifier, repoRoot),
		repoRoot: repoRoot,
		// Match the live fingerprint so getProjectContext's tracks-workflows
		// freshness check doesn't evict this hand-primed entry (a developer
		// machine may have a real ~/.sakusen/tracks tree).
		tracksFingerprint: tracksFingerprint(repoRoot),
	}
	s.projectsMu.Unlock()

	tk, err := s.database.CreateTask(projID, "tmux task", "desc", "slug", "wf", "main", task.StatusTmux, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The engine clears current_step and bumps StepIndex past the tmux step
	// before pausing at the approval gate.
	if err := s.database.UpdateTaskStep(tk.ID, 2, ""); err != nil {
		t.Fatal(err)
	}
	// The step row is created (running) then completed the instant the session
	// is spawned — its context starts empty.
	if err := s.database.CreateTaskStep(tk.ID, stepName); err != nil {
		t.Fatal(err)
	}
	if err := s.database.CompleteTaskStep(tk.ID, stepName, nil, 0); err != nil {
		t.Fatal(err)
	}
	refreshed, err := s.database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	return refreshed
}

// TestHandleUpdateActiveStepContext_TmuxPausedStep is the regression test for
// the original failure ("task #N has no active step"): an agent inside a live
// tmux step must be able to fold its chat into the step context even though the
// engine has already cleared current_step and marked the step row completed.
func TestHandleUpdateActiveStepContext_TmuxPausedStep(t *testing.T) {
	s, projID := setupServerWithProject(t)
	tk := createPausedTmuxStep(t, s, projID, "grilling")

	clientConn, serverConn := pipeForHandler(t)

	go s.handleUpdateActiveStepContext(serverConn, UpdateActiveStepContextRequest{
		TaskID:   tk.ID,
		StepName: "grilling",
		Context:  "final plan body",
	})

	msg := readOneMessage(t, clientConn)
	if msg.Type != MsgOK {
		t.Fatalf("expected MsgOK, got %s: %s", msg.Type, string(msg.Payload))
	}

	rows, err := s.database.GetTaskStepRows(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := rows["grilling"].Context; got != "final plan body" {
		t.Errorf("context after fold: got %q", got)
	}
	if rows["grilling"].Status != "completed" {
		t.Errorf("paused tmux step should stay completed, got status %q", rows["grilling"].Status)
	}
}

// TestHandleUpdateActiveStepContext_TmuxRejectsNonActiveStep verifies the
// safety boundary still holds for paused tmux tasks: only the step that owns
// the live session (the paused step) is writable, not some other completed
// step.
func TestHandleUpdateActiveStepContext_TmuxRejectsNonActiveStep(t *testing.T) {
	s, projID := setupServerWithProject(t)
	tk := createPausedTmuxStep(t, s, projID, "grilling")

	clientConn, serverConn := pipeForHandler(t)

	go s.handleUpdateActiveStepContext(serverConn, UpdateActiveStepContextRequest{
		TaskID:   tk.ID,
		StepName: "initial_planning",
		Context:  "should not stick",
	})

	msg := readOneMessage(t, clientConn)
	if msg.Type != MsgError {
		t.Fatalf("expected MsgError, got %s: %s", msg.Type, string(msg.Payload))
	}
	var resp ErrorResponse
	if err := msg.DecodePayload(&resp); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if !strings.Contains(resp.Message, `step "initial_planning" is not the active step`) ||
		!strings.Contains(resp.Message, `current: "grilling"`) {
		t.Errorf("error should name the mismatched and active steps, got %q", resp.Message)
	}
}

func TestHandleUpdateActiveStepContext_ReplaceDefaultMode(t *testing.T) {
	s, projID := setupServerWithProject(t)
	tk := createRunningStep(t, s, projID, "implement")

	clientConn, serverConn := pipeForHandler(t)

	go s.handleUpdateActiveStepContext(serverConn, UpdateActiveStepContextRequest{
		TaskID:   tk.ID,
		StepName: "implement",
		Context:  "canonical artifact body",
		// Mode left empty → defaults to replace.
	})

	msg := readOneMessage(t, clientConn)
	if msg.Type != MsgOK {
		t.Fatalf("expected MsgOK, got %s: %s", msg.Type, string(msg.Payload))
	}

	rows, err := s.database.GetTaskStepRows(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := rows["implement"].Context
	if got != "canonical artifact body" {
		t.Errorf("context after replace: got %q", got)
	}
	if rows["implement"].Status != "running" {
		t.Errorf("step should still be running, got status %q", rows["implement"].Status)
	}
}

func TestHandleUpdateActiveStepContext_AppendConcatenates(t *testing.T) {
	s, projID := setupServerWithProject(t)
	tk := createRunningStep(t, s, projID, "implement")

	for _, line := range []string{"first", "second", "third"} {
		clientConn, serverConn := net.Pipe()
		go func(value string) {
			s.handleUpdateActiveStepContext(serverConn, UpdateActiveStepContextRequest{
				TaskID:   tk.ID,
				StepName: "implement",
				Context:  value,
				Mode:     "append",
			})
			serverConn.Close()
		}(line)
		msg := readOneMessage(t, clientConn)
		if msg.Type != MsgOK {
			t.Fatalf("append %q: expected MsgOK, got %s: %s", line, msg.Type, string(msg.Payload))
		}
		clientConn.Close()
	}

	rows, err := s.database.GetTaskStepRows(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := "first\nsecond\nthird"
	if got := rows["implement"].Context; got != want {
		t.Errorf("append result: got %q want %q", got, want)
	}
}

func TestHandleUpdateActiveStepContext_RejectsNonActiveStep(t *testing.T) {
	s, projID := setupServerWithProject(t)
	tk := createRunningStep(t, s, projID, "implement")

	clientConn, serverConn := pipeForHandler(t)

	go s.handleUpdateActiveStepContext(serverConn, UpdateActiveStepContextRequest{
		TaskID:   tk.ID,
		StepName: "review",
		Context:  "should not stick",
	})

	msg := readOneMessage(t, clientConn)
	if msg.Type != MsgError {
		t.Fatalf("expected MsgError, got %s: %s", msg.Type, string(msg.Payload))
	}
	var resp ErrorResponse
	if err := msg.DecodePayload(&resp); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if !strings.Contains(resp.Message, `step "review" is not the active step`) ||
		!strings.Contains(resp.Message, `current: "implement"`) {
		t.Errorf("error message should name the mismatched and active steps, got %q", resp.Message)
	}

	rows, err := s.database.GetTaskStepRows(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := rows["implement"].Context; got != "" {
		t.Errorf("active step context should be untouched, got %q", got)
	}
}

func TestHandleUpdateActiveStepContext_MissingTask(t *testing.T) {
	s, _ := setupServerWithProject(t)

	clientConn, serverConn := pipeForHandler(t)

	go s.handleUpdateActiveStepContext(serverConn, UpdateActiveStepContextRequest{
		TaskID:   99999,
		StepName: "implement",
		Context:  "ignored",
	})

	msg := readOneMessage(t, clientConn)
	if msg.Type != MsgError {
		t.Fatalf("expected MsgError, got %s: %s", msg.Type, string(msg.Payload))
	}
	var resp ErrorResponse
	if err := msg.DecodePayload(&resp); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if !strings.Contains(resp.Message, "failed to get task #99999") {
		t.Errorf("error message should mention the missing task id, got %q", resp.Message)
	}
}

func TestHandleUpdateActiveStepContext_RejectsInvalidMode(t *testing.T) {
	s, projID := setupServerWithProject(t)
	tk := createRunningStep(t, s, projID, "implement")

	clientConn, serverConn := pipeForHandler(t)

	go s.handleUpdateActiveStepContext(serverConn, UpdateActiveStepContextRequest{
		TaskID:   tk.ID,
		StepName: "implement",
		Context:  "x",
		Mode:     "overwrite",
	})

	msg := readOneMessage(t, clientConn)
	if msg.Type != MsgError {
		t.Fatalf("expected MsgError, got %s: %s", msg.Type, string(msg.Payload))
	}
	var resp ErrorResponse
	if err := msg.DecodePayload(&resp); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if !strings.Contains(resp.Message, "invalid mode") {
		t.Errorf("error message should call out the invalid mode, got %q", resp.Message)
	}
}

func TestHandleUpdateActiveStepContext_NoActiveStep(t *testing.T) {
	s, projID := setupServerWithProject(t)
	// Create a task without setting current_step.
	tk, err := s.database.CreateTask(projID, "pending task", "", "p", "default", "main", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}

	clientConn, serverConn := pipeForHandler(t)

	go s.handleUpdateActiveStepContext(serverConn, UpdateActiveStepContextRequest{
		TaskID:   tk.ID,
		StepName: "implement",
		Context:  "ignored",
	})

	msg := readOneMessage(t, clientConn)
	if msg.Type != MsgError {
		t.Fatalf("expected MsgError, got %s: %s", msg.Type, string(msg.Payload))
	}
	var resp ErrorResponse
	if err := msg.DecodePayload(&resp); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if !strings.Contains(resp.Message, "no active step") {
		t.Errorf("error message should mention no active step, got %q", resp.Message)
	}
}
