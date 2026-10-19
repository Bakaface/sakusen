package client

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/Bakaface/sakusen/internal/task"
)

// waitDaemon is a fake daemon serving subscribe/unsubscribe/get_task from a
// mutable status table and able to push broadcasts at any time.
type waitDaemon struct {
	t        *testing.T
	listener net.Listener

	mu       sync.Mutex
	statuses map[int64]task.Status
	conn     net.Conn
	writeMu  sync.Mutex
	gets     int
	ready    chan struct{} // closed once the client has subscribed
	readyOne sync.Once
}

func newWaitDaemon(t *testing.T, statuses map[int64]task.Status) *waitDaemon {
	t.Helper()
	l, err := net.Listen("unix", shortSocketPath(t))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	d := &waitDaemon{t: t, listener: l, statuses: statuses, ready: make(chan struct{})}
	go d.serve()
	return d
}

func (d *waitDaemon) serve() {
	conn, err := d.listener.Accept()
	if err != nil {
		return
	}
	d.mu.Lock()
	d.conn = conn
	d.mu.Unlock()
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		msg, err := daemon.DecodeMessage(scanner.Bytes())
		if err != nil {
			continue
		}
		var resp *daemon.Message
		switch msg.Type {
		case daemon.MsgSubscribe, daemon.MsgUnsubscribe:
			resp, _ = daemon.NewMessage(daemon.MsgOK, nil)
		case daemon.MsgGetTask:
			var req daemon.GetTaskRequest
			_ = msg.DecodePayload(&req)
			d.mu.Lock()
			d.gets++
			s, ok := d.statuses[req.TaskID]
			d.mu.Unlock()
			if !ok {
				resp, _ = daemon.NewMessage(daemon.MsgError, daemon.ErrorResponse{Message: fmt.Sprintf("task %d not found", req.TaskID)})
			} else {
				resp, _ = daemon.NewMessage(daemon.MsgGetTask, daemon.GetTaskResponse{Task: daemon.TaskInfo{ID: req.TaskID, Status: string(s)}})
			}
		default:
			resp, _ = daemon.NewMessage(daemon.MsgError, daemon.ErrorResponse{Message: "unknown"})
		}
		d.write(resp)
		if msg.Type == daemon.MsgGetTask {
			d.readyOne.Do(func() { close(d.ready) })
		}
	}
}

func (d *waitDaemon) write(msg *daemon.Message) {
	data, err := daemon.EncodeMessage(msg)
	if err != nil {
		d.t.Errorf("encode: %v", err)
		return
	}
	d.mu.Lock()
	conn := d.conn
	d.mu.Unlock()
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, _ = conn.Write(data)
}

// setStatus changes what get_task returns without broadcasting.
func (d *waitDaemon) setStatus(id int64, s task.Status) {
	d.mu.Lock()
	d.statuses[id] = s
	d.mu.Unlock()
}

func (d *waitDaemon) broadcastTask(id int64, s task.Status) {
	d.setStatus(id, s)
	msg, _ := daemon.NewMessage(daemon.MsgTaskUpdate, daemon.TaskUpdateResponse{Task: daemon.TaskInfo{ID: id, Status: string(s)}})
	d.write(msg)
}

func (d *waitDaemon) getCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.gets
}

func (d *waitDaemon) client(t *testing.T) *Client {
	t.Helper()
	c := New(&config.Config{SocketPath: d.listener.Addr().String()})
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

type waitOutcome struct {
	res *WaitResult
	err error
	dur time.Duration
}

func startWait(c *Client, ctx context.Context, ids []int64, statuses []task.Status, poll time.Duration) <-chan waitOutcome {
	out := make(chan waitOutcome, 1)
	go func() {
		start := time.Now()
		res, err := c.WaitForTaskStatus(ctx, ids, statuses, poll)
		out <- waitOutcome{res, err, time.Since(start)}
	}()
	return out
}

func awaitOutcome(t *testing.T, ch <-chan waitOutcome) waitOutcome {
	t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(10 * time.Second):
		t.Fatal("wait did not return")
		return waitOutcome{}
	}
}

func TestWaitForTaskStatus_AlreadyMatched(t *testing.T) {
	d := newWaitDaemon(t, map[int64]task.Status{1: task.StatusRunning, 2: task.StatusCompleted})
	c := d.client(t)

	o := awaitOutcome(t, startWait(c, context.Background(), []int64{1, 2, 2}, nil, time.Hour))
	if o.err != nil {
		t.Fatalf("unexpected error: %v", o.err)
	}
	if o.res.TimedOut || len(o.res.Matched) != 1 || o.res.Matched[0].ID != 2 {
		t.Fatalf("unexpected result: %+v", o.res)
	}
	if len(o.res.Tasks) != 2 {
		t.Fatalf("want 2 snapshots (dupes removed), got %+v", o.res.Tasks)
	}
}

func TestWaitForTaskStatus_BroadcastWakesPromptly(t *testing.T) {
	d := newWaitDaemon(t, map[int64]task.Status{1: task.StatusRunning, 2: task.StatusTmux})
	c := d.client(t)

	ch := startWait(c, context.Background(), []int64{1, 2}, nil, time.Hour)
	<-d.ready
	time.Sleep(20 * time.Millisecond) // let the initial poll finish

	// Noise that must be drained and ignored: another type and an unwatched ID.
	for i := range 150 {
		noise, _ := daemon.NewMessage(daemon.MsgAgentUpdate, daemon.AgentInfo{ID: fmt.Sprint(i)})
		d.write(noise)
		d.broadcastTask(99, task.StatusCompleted)
	}
	d.broadcastTask(2, task.StatusCompleted)

	o := awaitOutcome(t, ch)
	if o.err != nil {
		t.Fatalf("unexpected error: %v", o.err)
	}
	if o.dur > 5*time.Second {
		t.Fatalf("broadcast did not wake the waiter promptly: %v", o.dur)
	}
	if len(o.res.Matched) != 1 || o.res.Matched[0].ID != 2 {
		t.Fatalf("unexpected matched: %+v", o.res.Matched)
	}
	if len(o.res.Tasks) != 2 || o.res.Tasks[0].Status != string(task.StatusRunning) || o.res.Tasks[1].Status != string(task.StatusCompleted) {
		t.Fatalf("unexpected snapshots: %+v", o.res.Tasks)
	}
}

func TestWaitForTaskStatus_PollCatchesUnbroadcastTransition(t *testing.T) {
	d := newWaitDaemon(t, map[int64]task.Status{1: task.StatusFinalizing})
	c := d.client(t)

	ch := startWait(c, context.Background(), []int64{1}, nil, 50*time.Millisecond)
	<-d.ready
	d.setStatus(1, task.StatusMergeBlocked)

	o := awaitOutcome(t, ch)
	if o.err != nil {
		t.Fatalf("unexpected error: %v", o.err)
	}
	if len(o.res.Matched) != 1 || o.res.Matched[0].Status != string(task.StatusMergeBlocked) {
		t.Fatalf("unexpected result: %+v", o.res)
	}
	if d.getCount() < 2 {
		t.Fatalf("expected a poll re-read, got %d get_task calls", d.getCount())
	}
}

func TestWaitForTaskStatus_CustomStatuses(t *testing.T) {
	d := newWaitDaemon(t, map[int64]task.Status{1: task.StatusRunning})
	c := d.client(t)

	ch := startWait(c, context.Background(), []int64{1}, []task.Status{task.StatusCompleted}, time.Hour)
	<-d.ready
	d.broadcastTask(1, task.StatusFailed)

	select {
	case o := <-ch:
		t.Fatalf("failed must not match a completed-only wait: %+v", o.res)
	case <-time.After(200 * time.Millisecond):
	}

	d.broadcastTask(1, task.StatusCompleted)
	o := awaitOutcome(t, ch)
	if o.err != nil || len(o.res.Matched) != 1 || o.res.Matched[0].Status != string(task.StatusCompleted) {
		t.Fatalf("unexpected outcome: %+v err=%v", o.res, o.err)
	}
}

func TestWaitForTaskStatus_Timeout(t *testing.T) {
	d := newWaitDaemon(t, map[int64]task.Status{1: task.StatusRunning})
	c := d.client(t)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	o := awaitOutcome(t, startWait(c, ctx, []int64{1}, nil, time.Hour))
	if o.err != nil {
		t.Fatalf("timeout must not be an error: %v", o.err)
	}
	if !o.res.TimedOut || len(o.res.Matched) != 0 || len(o.res.Tasks) != 1 {
		t.Fatalf("unexpected result: %+v", o.res)
	}
}

func TestWaitForTaskStatus_Cancel(t *testing.T) {
	d := newWaitDaemon(t, map[int64]task.Status{1: task.StatusRunning})
	c := d.client(t)

	ctx, cancel := context.WithCancel(context.Background())
	ch := startWait(c, ctx, []int64{1}, nil, time.Hour)
	<-d.ready
	cancel()
	o := awaitOutcome(t, ch)
	if !errors.Is(o.err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", o.err)
	}
}

func TestWaitForTaskStatus_UnknownTask(t *testing.T) {
	d := newWaitDaemon(t, map[int64]task.Status{1: task.StatusRunning})
	c := d.client(t)

	o := awaitOutcome(t, startWait(c, context.Background(), []int64{1, 42}, nil, time.Hour))
	if o.err == nil {
		t.Fatalf("expected error for unknown task, got %+v", o.res)
	}
}

func TestWaitForTaskStatus_InvalidIDs(t *testing.T) {
	c := New(&config.Config{})
	if _, err := c.WaitForTaskStatus(context.Background(), nil, nil, 0); err == nil {
		t.Error("expected error for empty IDs")
	}
	if _, err := c.WaitForTaskStatus(context.Background(), []int64{0}, nil, 0); err == nil {
		t.Error("expected error for non-positive ID")
	}
}

func TestParseWaitStatuses(t *testing.T) {
	got, err := ParseWaitStatuses(nil)
	if err != nil || len(got) != len(DefaultWaitStatuses) {
		t.Fatalf("default: got %v, %v", got, err)
	}
	got, err = ParseWaitStatuses([]string{"completed", "completed", "failed"})
	if err != nil || len(got) != 2 {
		t.Fatalf("dedupe: got %v, %v", got, err)
	}
	if _, err := ParseWaitStatuses([]string{"merged"}); err == nil {
		t.Fatal("expected error for unknown status")
	}
}
