package client

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/daemon"
)

// errConnectionClosed is the sentinel pushed onto errChan when readLoop exits
// (clean EOF or scan error). sendAndWait observes this to wake up promptly
// and attempt a single reconnect-and-retry rather than timing out on the
// response channel.
var errConnectionClosed = errors.New("daemon connection closed")

// Reconnect contract (Fix #102, sortie-102):
//
//   - sendAndWait and send do EXACTLY ONE reconnect-and-retry on a wire-level
//     failure (Write error or readLoop signaling errConnectionClosed). A second
//     consecutive failure escalates to the caller — never spin-loop.
//   - Reconnect does NOT fire after explicit Close() — the done channel is the
//     terminal signal, checked before any retry attempt.
//   - If Subscribe() has been called and not yet Unsubscribe()-d, the reconnect
//     path transparently re-issues MsgSubscribe on the fresh connection BEFORE
//     retrying the original request, so create_task --wait_for_ready and other
//     broadcast-dependent flows survive a daemon hiccup.
//   - The mutex contract is unchanged: c.mu is held for the entire send+wait
//     cycle (including reconnect+resubscribe+retry). Reconnect must NEVER
//     release the lock — concurrent sendAndWait callers would otherwise see
//     a partially-constructed connection.

type Client struct {
	cfg  *config.Config
	conn net.Conn
	mu   sync.Mutex

	respChan  chan *daemon.Message // request-response messages
	subChan   chan *daemon.Message // subscription broadcast messages
	errChan   chan error
	done      chan struct{}
	closeOnce sync.Once

	// subscribed tracks whether Subscribe() has been called and not yet
	// Unsubscribe()-d. The reconnect path uses this to transparently
	// re-subscribe on a fresh connection so broadcasts keep flowing.
	// Guarded by mu.
	subscribed bool
}

func New(cfg *config.Config) *Client {
	return &Client{
		cfg:      cfg,
		respChan: make(chan *daemon.Message, 100),
		subChan:  make(chan *daemon.Message, 100),
		errChan:  make(chan error, 1),
		done:     make(chan struct{}),
	}
}

func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	conn, err := net.Dial("unix", c.cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("failed to connect to daemon: %w", err)
	}
	c.conn = conn

	go c.readLoop(conn)

	return nil
}

func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

// readLoop reads messages from conn (NOT c.conn) until conn fails or closes.
// Passing conn as a parameter lets reconnect() start a fresh readLoop while
// any prior readLoop drains and exits on the now-closed previous connection.
//
// On exit (clean EOF or scan error), a sentinel is pushed onto c.errChan so
// callers blocked in sendAndWait wake up immediately. Push is non-blocking:
// if errChan is already populated or the client is closing, the signal is
// dropped — the buffered entry or the done channel will wake any waiter.
func (c *Client) readLoop(conn net.Conn) {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 10MB buffer for large log responses
	for scanner.Scan() {
		msg, err := daemon.DecodeMessage(scanner.Bytes())
		if err != nil {
			log.Printf("IPC decode error: %v", err)
			continue
		}

		ch := c.respChan
		if daemon.IsBroadcast(msg.Type) {
			ch = c.subChan
		}

		select {
		case ch <- msg:
		case <-c.done:
			return
		}
	}

	// Even on clean EOF (scanner.Err() == nil) we MUST signal — otherwise
	// callers blocked in sendAndWait would wait indefinitely on respChan.
	err := scanner.Err()
	if err == nil {
		err = errConnectionClosed
	}
	select {
	case c.errChan <- err:
	case <-c.done:
	default:
	}
}

func (c *Client) Messages() <-chan *daemon.Message {
	return c.subChan
}

func (c *Client) Errors() <-chan error {
	return c.errChan
}

// drainStaleSignalsLocked clears any pending errChan / respChan entries
// produced by a now-dead connection so they don't ambush the new request
// after a reconnect. Caller MUST hold c.mu.
func (c *Client) drainStaleSignalsLocked() {
	for {
		select {
		case <-c.errChan:
			continue
		default:
		}
		break
	}
	for {
		select {
		case <-c.respChan:
			continue
		default:
		}
		break
	}
}

// reconnectLocked closes the current connection and dials a fresh one,
// re-subscribing transparently if the client was subscribed before. Caller
// MUST hold c.mu. On success c.conn is the new connection and a fresh
// readLoop is running against it. On dial failure c.conn is set to nil
// and the next call will naturally retry the dial.
//
// skipResubscribe suppresses the implicit MsgSubscribe round-trip on the
// new conn. It is set when the in-flight request was itself MsgSubscribe —
// the outer retry will re-send subscribe, so doing it twice would orphan
// a MsgOK in respChan and ambush the next caller.
func (c *Client) reconnectLocked(skipResubscribe bool) error {
	if c.conn != nil {
		c.conn.Close()
	}

	c.drainStaleSignalsLocked()

	newConn, err := net.Dial("unix", c.cfg.SocketPath)
	if err != nil {
		c.conn = nil
		return fmt.Errorf("dial: %w", err)
	}
	c.conn = newConn
	go c.readLoop(newConn)

	if skipResubscribe || !c.subscribed {
		return nil
	}

	// Re-issue Subscribe on the new conn. We are already holding c.mu, so
	// we inline the write+wait rather than calling Subscribe() to avoid
	// re-entrant lock acquisition.
	subMsg, err := daemon.NewMessage(daemon.MsgSubscribe, nil)
	if err != nil {
		return fmt.Errorf("resubscribe encode: %w", err)
	}
	subData, err := daemon.EncodeMessage(subMsg)
	if err != nil {
		return fmt.Errorf("resubscribe encode: %w", err)
	}
	resp, err := c.writeAndWaitLocked(subData)
	if err != nil {
		return fmt.Errorf("resubscribe: %w", err)
	}
	if resp.Type == daemon.MsgError {
		return fmt.Errorf("resubscribe: daemon rejected")
	}
	return nil
}

// writeAndWaitLocked writes data on c.conn and blocks until a non-broadcast
// response arrives, the connection signals an error, or Close() is called.
// Caller MUST hold c.mu. Returns the wire error verbatim — does not attempt
// any reconnect itself; the caller decides retry policy.
func (c *Client) writeAndWaitLocked(data []byte) (*daemon.Message, error) {
	if c.conn == nil {
		return nil, errConnectionClosed
	}
	if _, err := c.conn.Write(data); err != nil {
		return nil, err
	}

	// Read responses, skipping any broadcast messages that may have leaked
	// into respChan (e.g. due to a missing daemon.IsBroadcast entry).
	for {
		select {
		case resp := <-c.respChan:
			if daemon.IsBroadcast(resp.Type) {
				continue
			}
			return resp, nil
		case err := <-c.errChan:
			return nil, err
		case <-c.done:
			return nil, fmt.Errorf("client closed")
		}
	}
}

// sendOnceWithReconnectLocked attempts writeAndWaitLocked; on transient
// wire failure it does EXACTLY ONE reconnect-and-retry. msgType is used
// purely to decide whether the reconnect-internal resubscribe must be
// skipped (when the request itself is MsgSubscribe). Caller MUST hold c.mu.
func (c *Client) sendOnceWithReconnectLocked(data []byte, msgType daemon.MessageType) (*daemon.Message, error) {
	resp, err := c.writeAndWaitLocked(data)
	if err == nil {
		return resp, nil
	}

	// Never reconnect after explicit Close() — done is the terminal signal.
	select {
	case <-c.done:
		return nil, err
	default:
	}

	log.Printf("client: send %s failed (%v), attempting one reconnect", msgType, err)
	skipResub := msgType == daemon.MsgSubscribe
	if reconErr := c.reconnectLocked(skipResub); reconErr != nil {
		return nil, fmt.Errorf("send %s failed: %v (reconnect also failed: %w)", msgType, err, reconErr)
	}
	return c.writeAndWaitLocked(data)
}

func (c *Client) send(msgType daemon.MessageType, payload any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	msg, err := daemon.NewMessage(msgType, payload)
	if err != nil {
		return err
	}

	data, err := daemon.EncodeMessage(msg)
	if err != nil {
		return err
	}

	writeErr := c.writeRawLocked(data)
	if writeErr == nil {
		return nil
	}

	// Fire-and-forget gets the same single-retry bound as sendAndWait.
	select {
	case <-c.done:
		return writeErr
	default:
	}
	log.Printf("client: send (fire-and-forget) %s failed (%v), attempting one reconnect", msgType, writeErr)
	skipResub := msgType == daemon.MsgSubscribe
	if reconErr := c.reconnectLocked(skipResub); reconErr != nil {
		return fmt.Errorf("send failed: %v (reconnect also failed: %w)", writeErr, reconErr)
	}
	return c.writeRawLocked(data)
}

// writeRawLocked writes data on c.conn without waiting for a response.
// Caller MUST hold c.mu.
func (c *Client) writeRawLocked(data []byte) error {
	if c.conn == nil {
		return errConnectionClosed
	}
	_, err := c.conn.Write(data)
	return err
}

// sendAndWait sends msgType with payload and blocks for the matching response.
// Holds c.mu for the entire send+wait cycle so concurrent callers can't steal
// each other's responses, and so reconnect (if it fires) can mutate c.conn
// without racing other callers.
func (c *Client) sendAndWait(msgType daemon.MessageType, payload any) (*daemon.Message, error) {
	return c.sendAndWaitWithHook(msgType, payload, nil)
}

// sendAndWaitWithHook is sendAndWait plus an optional callback fired while c.mu
// is still held, on a non-error response. Used by Subscribe/Unsubscribe to
// atomically flip c.subscribed alongside the daemon's ack — closing the race
// window where a concurrent reconnect could miss the subscription state change.
func (c *Client) sendAndWaitWithHook(msgType daemon.MessageType, payload any, onSuccess func()) (*daemon.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	msg, err := daemon.NewMessage(msgType, payload)
	if err != nil {
		return nil, err
	}

	data, err := daemon.EncodeMessage(msg)
	if err != nil {
		return nil, err
	}

	resp, err := c.sendOnceWithReconnectLocked(data, msgType)
	if err != nil {
		return nil, err
	}
	if onSuccess != nil && resp.Type != daemon.MsgError {
		onSuccess()
	}
	return resp, nil
}

// request sends a message and waits for a non-error response.
func (c *Client) request(msgType daemon.MessageType, payload any) (*daemon.Message, error) {
	msg, err := c.sendAndWait(msgType, payload)
	if err != nil {
		return nil, err
	}
	if msg.Type == daemon.MsgError {
		var errResp daemon.ErrorResponse
		msg.DecodePayload(&errResp)
		return nil, errors.New(errResp.Message)
	}
	return msg, nil
}

// requestOK sends a message and returns an error if the response is not successful.
func (c *Client) requestOK(msgType daemon.MessageType, payload any) error {
	_, err := c.request(msgType, payload)
	return err
}

func (c *Client) Ping() error {
	msg, err := c.sendAndWait(daemon.MsgPing, nil)
	if err != nil {
		return err
	}
	if msg.Type != daemon.MsgPong {
		return fmt.Errorf("unexpected response: %s", msg.Type)
	}
	return nil
}

func (c *Client) ListAgents() ([]daemon.AgentInfo, error) {
	msg, err := c.request(daemon.MsgListAgents, nil)
	if err != nil {
		return nil, err
	}

	var resp daemon.AgentListResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}

	return resp.Agents, nil
}

func (c *Client) ListTasks() ([]daemon.TaskInfo, error) {
	return c.ListTasksFiltered(0)
}

// ListTasksFiltered returns tasks optionally filtered by project ID.
// A projectID of 0 returns all tasks.
func (c *Client) ListTasksFiltered(projectID int64) ([]daemon.TaskInfo, error) {
	var payload any
	if projectID > 0 {
		payload = daemon.ListTasksRequest{ProjectID: projectID}
	}

	msg, err := c.request(daemon.MsgListTasks, payload)
	if err != nil {
		return nil, err
	}

	var resp daemon.TaskListResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}

	return resp.Tasks, nil
}

// ListTasksByProjectName returns tasks filtered by project name (repo basename).
func (c *Client) ListTasksByProjectName(name string) ([]daemon.TaskInfo, error) {
	msg, err := c.request(daemon.MsgListTasks, daemon.ListTasksRequest{ProjectName: name})
	if err != nil {
		return nil, err
	}

	var resp daemon.TaskListResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}

	return resp.Tasks, nil
}

func (c *Client) StartAgent(taskID int64) error {
	return c.requestOK(daemon.MsgStartAgent, daemon.StartAgentRequest{TaskID: taskID})
}

func (c *Client) StopAgent(agentID string) error {
	return c.requestOK(daemon.MsgStopAgent, daemon.StopAgentRequest{AgentID: agentID})
}

func (c *Client) Subscribe() error {
	// The onSuccess hook runs while c.mu is still held so c.subscribed flips
	// atomically with the daemon's ack — protects against a concurrent
	// sendAndWait triggering reconnect in the gap and missing the new state.
	msg, err := c.sendAndWaitWithHook(daemon.MsgSubscribe, nil, func() {
		c.subscribed = true
	})
	if err != nil {
		return err
	}
	if msg.Type == daemon.MsgError {
		var errResp daemon.ErrorResponse
		msg.DecodePayload(&errResp)
		return errors.New(errResp.Message)
	}
	return nil
}

func (c *Client) Unsubscribe() error {
	msg, err := c.sendAndWaitWithHook(daemon.MsgUnsubscribe, nil, func() {
		c.subscribed = false
	})
	if err != nil {
		return err
	}
	if msg.Type == daemon.MsgError {
		var errResp daemon.ErrorResponse
		msg.DecodePayload(&errResp)
		return errors.New(errResp.Message)
	}
	return nil
}

func (c *Client) GetOutput(agentID string, fromLine int) ([]string, int, error) {
	msg, err := c.request(daemon.MsgGetOutput, daemon.GetOutputRequest{
		AgentID:  agentID,
		FromLine: fromLine,
	})
	if err != nil {
		return nil, 0, err
	}

	var resp daemon.OutputChunkResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, 0, err
	}

	return resp.Lines, resp.TotalLines, nil
}

func (c *Client) SendInput(agentID, input string) error {
	return c.requestOK(daemon.MsgSendInput, daemon.SendInputRequest{
		AgentID: agentID,
		Input:   input,
	})
}

func (c *Client) GetTask(id int64) (*daemon.TaskInfo, error) {
	msg, err := c.request(daemon.MsgGetTask, daemon.GetTaskRequest{TaskID: id})
	if err != nil {
		return nil, err
	}

	var resp daemon.GetTaskResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}

	return &resp.Task, nil
}

// RetryTask resets a task so the daemon will pick it up and re-run it.
// When stepName is empty, the task is restarted from the beginning (full
// reset). When non-empty, completed work for earlier steps is preserved and
// the engine resumes from the named step. The daemon validates that the step
// exists in the task's workflow.
func (c *Client) RetryTask(id int64, stepName string) (*daemon.TaskInfo, error) {
	msg, err := c.request(daemon.MsgRetryTask, daemon.RetryTaskRequest{TaskID: id, StepName: stepName})
	if err != nil {
		return nil, err
	}
	var resp daemon.RetryTaskResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}
	return &resp.Task, nil
}

func (c *Client) CreateTask(input, workflow, branchName, projectPath string, worktree bool, images []string) (*daemon.TaskInfo, error) {
	msg, err := c.request(daemon.MsgCreateTask, daemon.CreateTaskRequest{
		Input:       input,
		Workflow:    workflow,
		BranchName:  branchName,
		ProjectPath: projectPath,
		Worktree:    &worktree,
		Images:      images,
	})
	if err != nil {
		return nil, err
	}

	var resp daemon.CreateTaskResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}

	return &resp.Task, nil
}

// CreateTaskWithOptions creates a task with all available options.
func (c *Client) CreateTaskWithOptions(req daemon.CreateTaskRequest) (*daemon.TaskInfo, error) {
	msg, err := c.request(daemon.MsgCreateTask, req)
	if err != nil {
		return nil, err
	}

	var resp daemon.CreateTaskResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}

	return &resp.Task, nil
}

func (c *Client) ContinueTask(id int64, workflow, prompt string) (*daemon.TaskInfo, error) {
	msg, err := c.request(daemon.MsgContinueTask, daemon.ContinueTaskRequest{TaskID: id, Workflow: workflow, Prompt: prompt})
	if err != nil {
		return nil, err
	}
	var resp daemon.ContinueTaskResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}
	return &resp.Task, nil
}

// AdvanceTask marks a tmux-gated task's current step as done: the daemon kills
// the tmux session and either resumes the workflow at the next step or, when
// the tmux step was the last one, runs full finalization. Returns the daemon's
// outcome message (e.g. "task #5 advancing to next step").
func (c *Client) AdvanceTask(id int64) (string, error) {
	msg, err := c.request(daemon.MsgAdvanceTask, daemon.AdvanceTaskRequest{TaskID: id})
	if err != nil {
		return "", err
	}
	var resp daemon.OKResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return "", err
	}
	return resp.Message, nil
}

func (c *Client) UpdateTaskPriority(id int64, priority string) (*daemon.TaskInfo, error) {
	msg, err := c.request(daemon.MsgUpdatePriority, daemon.UpdatePriorityRequest{
		TaskID:   id,
		Priority: priority,
	})
	if err != nil {
		return nil, err
	}
	var resp daemon.UpdatePriorityResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}
	return &resp.Task, nil
}

func (c *Client) UpdateTaskField(id int64, field, value string) (*daemon.TaskInfo, error) {
	msg, err := c.request(daemon.MsgUpdateField, daemon.UpdateFieldRequest{
		TaskID: id,
		Field:  field,
		Value:  value,
	})
	if err != nil {
		return nil, err
	}
	var resp daemon.UpdateFieldResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}
	return &resp.Task, nil
}

func (c *Client) DeleteTask(id int64) error {
	return c.requestOK(daemon.MsgDeleteTask, daemon.DeleteTaskRequest{TaskID: id})
}

func (c *Client) RevertTask(id int64) (*daemon.TaskInfo, error) {
	msg, err := c.request(daemon.MsgRevertTask, daemon.RevertTaskRequest{TaskID: id})
	if err != nil {
		return nil, err
	}
	var resp daemon.RevertTaskResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}
	return &resp.Task, nil
}

func (c *Client) AddTaskDependency(taskID, blockedByID int64) (*daemon.TaskInfo, error) {
	msg, err := c.request(daemon.MsgUpdateDependency, daemon.UpdateDependencyRequest{
		TaskID:    taskID,
		BlockedBy: blockedByID,
		Action:    "add",
	})
	if err != nil {
		return nil, err
	}
	var resp daemon.UpdateDependencyResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}
	return &resp.Task, nil
}

func (c *Client) RemoveTaskDependency(taskID, blockedByID int64) (*daemon.TaskInfo, error) {
	msg, err := c.request(daemon.MsgUpdateDependency, daemon.UpdateDependencyRequest{
		TaskID:    taskID,
		BlockedBy: blockedByID,
		Action:    "remove",
	})
	if err != nil {
		return nil, err
	}
	var resp daemon.UpdateDependencyResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}
	return &resp.Task, nil
}

func (c *Client) DetachBranch(id int64) (*daemon.TaskInfo, error) {
	msg, err := c.request(daemon.MsgDetachBranch, daemon.DetachBranchRequest{TaskID: id})
	if err != nil {
		return nil, err
	}
	var resp daemon.DetachBranchResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}
	return &resp.Task, nil
}

func (c *Client) AttachBranch(id int64) (*daemon.TaskInfo, error) {
	msg, err := c.request(daemon.MsgAttachBranch, daemon.AttachBranchRequest{TaskID: id})
	if err != nil {
		return nil, err
	}
	var resp daemon.AttachBranchResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}
	return &resp.Task, nil
}

func (c *Client) GetLogs(id int64, tail int, offset int) ([]string, int, error) {
	msg, err := c.request(daemon.MsgGetLogs, daemon.GetLogsRequest{
		TaskID: id,
		Tail:   tail,
		Offset: offset,
	})
	if err != nil {
		return nil, 0, err
	}

	var resp daemon.GetLogsResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, 0, err
	}

	return resp.Lines, resp.TotalLines, nil
}

func (c *Client) StopTask(id int64) (*daemon.TaskInfo, error) {
	msg, err := c.request(daemon.MsgStopTask, daemon.StopTaskRequest{TaskID: id})
	if err != nil {
		return nil, err
	}
	var resp daemon.StopTaskResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}
	return &resp.Task, nil
}

// CreateTasksAndWait creates each child task and atomically records
// task_waits_on edges from the parent so its current step suspends pending
// child completion. Returns the created children. Bundled API — preferred
// over CreateTask+WaitForTasks because it cannot be partially performed.
func (c *Client) CreateTasksAndWait(parentTaskID int64, tasks []daemon.CreateTaskRequest) ([]daemon.TaskInfo, error) {
	msg, err := c.request(daemon.MsgCreateTasksAndWait, daemon.CreateTasksAndWaitRequest{
		ParentTaskID: parentTaskID,
		Tasks:        tasks,
	})
	if err != nil {
		return nil, err
	}
	var resp daemon.CreateTasksAndWaitResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}
	return resp.Children, nil
}

// WaitForTasks records task_waits_on edges from parentTaskID to each
// child in childTaskIDs without creating new tasks. Used to suspend the
// parent's current step until pre-existing tasks reach terminal status.
func (c *Client) WaitForTasks(parentTaskID int64, childTaskIDs []int64) ([]daemon.TaskInfo, error) {
	msg, err := c.request(daemon.MsgWaitForTasks, daemon.WaitForTasksRequest{
		ParentTaskID: parentTaskID,
		ChildTaskIDs: childTaskIDs,
	})
	if err != nil {
		return nil, err
	}
	var resp daemon.WaitForTasksResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}
	return resp.Children, nil
}

// Cleanup removes worktrees, branches, and log directories for completed
// and failed tasks. Passing taskID == 0 cleans up every eligible task.
// The daemon returns the number of tasks cleaned and their post-cleanup
// TaskInfo entries so the TUI can drop them from its list.
func (c *Client) Cleanup(taskID int64) (int, []daemon.TaskInfo, error) {
	msg, err := c.request(daemon.MsgCleanup, daemon.CleanupRequest{TaskID: taskID})
	if err != nil {
		return 0, nil, err
	}
	var resp daemon.CleanupResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return 0, nil, err
	}
	return resp.Count, resp.Tasks, nil
}

// GetStepContexts fetches all completed step contexts for a task.
func (c *Client) GetStepContexts(taskID int64) (map[string]string, error) {
	msg, err := c.request(daemon.MsgGetStepContexts, daemon.GetStepContextsRequest{TaskID: taskID})
	if err != nil {
		return nil, err
	}
	var result daemon.GetStepContextsResponse
	if err := msg.DecodePayload(&result); err != nil {
		return nil, err
	}
	return result.Steps, nil
}

// GetTaskSteps fetches the full per-step state for a task, in workflow config
// order. Includes steps that have not started yet (Status == "pending").
func (c *Client) GetTaskSteps(taskID int64) ([]daemon.TaskStepDetail, error) {
	msg, err := c.request(daemon.MsgGetTaskSteps, daemon.GetTaskStepsRequest{TaskID: taskID})
	if err != nil {
		return nil, err
	}
	var result daemon.GetTaskStepsResponse
	if err := msg.DecodePayload(&result); err != nil {
		return nil, err
	}
	return result.Steps, nil
}

// UpdateStepContext overwrites the captured context for a completed step.
func (c *Client) UpdateStepContext(taskID int64, stepName, context string) error {
	return c.requestOK(daemon.MsgUpdateStepContext, daemon.UpdateStepContextRequest{
		TaskID:   taskID,
		StepName: stepName,
		Context:  context,
	})
}

// UpdateActiveStepContext writes context for the task's currently-active step.
// The daemon rejects the call if step_name doesn't match the task's running
// step. mode is "replace" (default) or "append". Used by the MCP
// update_step_context tool so an agent can push its canonical artifact mid-
// session instead of waiting for the post-session summarizer.
func (c *Client) UpdateActiveStepContext(taskID int64, stepName, context, mode string) error {
	return c.requestOK(daemon.MsgUpdateActiveStepContext, daemon.UpdateActiveStepContextRequest{
		TaskID:   taskID,
		StepName: stepName,
		Context:  context,
		Mode:     mode,
	})
}

// CreateTrack creates a new track (project-scoped unless req.Global) and
// returns its wire projection.
func (c *Client) CreateTrack(req daemon.CreateTrackRequest) (*daemon.TrackInfo, error) {
	msg, err := c.request(daemon.MsgCreateTrack, req)
	if err != nil {
		return nil, err
	}
	var resp daemon.CreateTrackResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}
	return &resp.Track, nil
}

// GetTrack resolves ref (slug or numeric ID) against the project at
// projectPath (project shadows global) and returns the track, its root-first
// ancestor chain, and the rendered {{track.context}} value.
func (c *Client) GetTrack(projectPath, ref string) (*daemon.GetTrackResponse, error) {
	msg, err := c.request(daemon.MsgGetTrack, daemon.GetTrackRequest{ProjectPath: projectPath, Track: ref})
	if err != nil {
		return nil, err
	}
	var resp daemon.GetTrackResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListTracks returns the tracks visible from the project at projectPath (its
// own plus all global tracks) with context previews and sizes.
func (c *Client) ListTracks(projectPath string) ([]daemon.TrackInfo, error) {
	msg, err := c.request(daemon.MsgListTracks, daemon.ListTracksRequest{ProjectPath: projectPath})
	if err != nil {
		return nil, err
	}
	var resp daemon.ListTracksResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}
	return resp.Tracks, nil
}

// SetTrackContext sets or appends (mode "replace"|"append") the context of an
// arbitrary track resolved by ref. This is the human/CLI surface; agents use
// UpdateTaskTrackContext instead.
func (c *Client) SetTrackContext(projectPath, ref, context, mode string) error {
	return c.requestOK(daemon.MsgSetTrackContext, daemon.SetTrackContextRequest{
		ProjectPath: projectPath,
		Track:       ref,
		Context:     context,
		Mode:        mode,
	})
}

// UpdateTaskTrackContext writes context to the calling task's own track. The
// daemon rejects tasks with no track or no active step — the own-track-only
// enforcement backing the update_track_context MCP tool.
func (c *Client) UpdateTaskTrackContext(taskID int64, context, mode string) error {
	return c.requestOK(daemon.MsgUpdateTaskTrackContext, daemon.UpdateTaskTrackContextRequest{
		TaskID:  taskID,
		Context: context,
		Mode:    mode,
	})
}

// SetTrackDescription replaces the description of an arbitrary track resolved
// by ref. This is the human/CLI surface; agents use UpdateTaskTrackDescription
// instead.
func (c *Client) SetTrackDescription(projectPath, ref, description string) error {
	return c.requestOK(daemon.MsgSetTrackDescription, daemon.SetTrackDescriptionRequest{
		ProjectPath: projectPath,
		Track:       ref,
		Description: description,
	})
}

// UpdateTaskTrackDescription replaces the description of the calling task's own
// track. The daemon rejects tasks with no track or no active step — the
// own-track-only enforcement backing the update_track_description MCP tool.
func (c *Client) UpdateTaskTrackDescription(taskID int64, description string) error {
	return c.requestOK(daemon.MsgUpdateTaskTrackDescription, daemon.UpdateTaskTrackDescriptionRequest{
		TaskID:      taskID,
		Description: description,
	})
}

// ListWorkflows returns the flat list of workflows configured for the project
// rooted at projectPath.
func (c *Client) ListWorkflows(projectPath string) (*daemon.ListWorkflowsResponse, error) {
	msg, err := c.request(daemon.MsgListWorkflows, daemon.ListWorkflowsRequest{ProjectPath: projectPath})
	if err != nil {
		return nil, err
	}
	var resp daemon.ListWorkflowsResponse
	if err := msg.DecodePayload(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func ParseAgentUpdate(msg *daemon.Message) (*daemon.AgentInfo, error) {
	if msg.Type != daemon.MsgAgentUpdate {
		return nil, fmt.Errorf("not an agent update message")
	}

	var resp daemon.AgentUpdateResponse
	if err := json.Unmarshal(msg.Payload, &resp); err != nil {
		return nil, err
	}

	return &resp.Agent, nil
}
