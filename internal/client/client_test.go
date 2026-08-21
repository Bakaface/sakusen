package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/daemon"
)

// shortSocketPath returns a short Unix socket path to avoid exceeding
// macOS's 104-byte sun_path limit (t.TempDir() paths are too long).
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "st")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, fmt.Sprintf("%d.sock", os.Getpid()))
}

// mockServer simulates a daemon that reads requests and sends back typed responses.
// It processes requests sequentially (like the real server).
func mockServer(t *testing.T, listener net.Listener, handler func(msg *daemon.Message) *daemon.Message) {
	t.Helper()

	conn, err := listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		msg, err := daemon.DecodeMessage(scanner.Bytes())
		if err != nil {
			t.Logf("mock server decode error: %v", err)
			continue
		}

		resp := handler(msg)
		data, err := daemon.EncodeMessage(resp)
		if err != nil {
			t.Logf("mock server encode error: %v", err)
			continue
		}
		conn.Write(data)
	}
}

func TestSendAndWait_ConcurrentCallsGetCorrectResponses(t *testing.T) {
	// Create a Unix socket pair for testing
	listener, err := net.Listen("unix", shortSocketPath(t))
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer listener.Close()

	// Start mock server that responds with a message type matching the request:
	// - list_tasks -> task_list with empty tasks
	// - continue_task -> ok
	// - ping -> pong
	go mockServer(t, listener, func(msg *daemon.Message) *daemon.Message {
		switch msg.Type {
		case daemon.MsgListTasks:
			// Simulate some processing time to widen the race window
			time.Sleep(10 * time.Millisecond)
			resp, _ := daemon.NewMessage(daemon.MsgTaskList, daemon.TaskListResponse{Tasks: []daemon.TaskInfo{}})
			return resp
		case daemon.MsgContinueTask:
			resp, _ := daemon.NewMessage(daemon.MsgContinueTask, daemon.ContinueTaskResponse{Task: daemon.TaskInfo{ID: 1}})
			return resp
		case daemon.MsgPing:
			resp, _ := daemon.NewMessage(daemon.MsgPong, nil)
			return resp
		default:
			resp, _ := daemon.NewMessage(daemon.MsgError, daemon.ErrorResponse{Message: "unknown"})
			return resp
		}
	})

	// Connect client
	conn, err := net.Dial("unix", listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}

	c := &Client{
		conn:     conn,
		respChan: make(chan *daemon.Message, 100),
		subChan:  make(chan *daemon.Message, 100),
		errChan:  make(chan error, 1),
		done:     make(chan struct{}),
	}
	go c.readLoop(conn)
	defer c.Close()

	// Run multiple concurrent sendAndWait calls and verify each gets the correct response type.
	// Before the fix, concurrent calls could receive each other's responses.
	const iterations = 20
	var wg sync.WaitGroup

	errCh := make(chan string, iterations*2)

	for range iterations {
		wg.Add(2)

		// Goroutine 1: ListTasks expects MsgTaskList response
		go func() {
			defer wg.Done()
			msg, err := c.sendAndWait(daemon.MsgListTasks, nil)
			if err != nil {
				errCh <- "ListTasks error: " + err.Error()
				return
			}
			if msg.Type != daemon.MsgTaskList {
				errCh <- "ListTasks got wrong response type: " + string(msg.Type)
			}
		}()

		// Goroutine 2: ContinueTask expects MsgOK response
		go func() {
			defer wg.Done()
			msg, err := c.sendAndWait(daemon.MsgContinueTask, daemon.ContinueTaskRequest{TaskID: 1})
			if err != nil {
				errCh <- "ContinueTask error: " + err.Error()
				return
			}
			if msg.Type != daemon.MsgContinueTask {
				errCh <- "ContinueTask got wrong response type: " + string(msg.Type)
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for errMsg := range errCh {
		t.Error(errMsg)
	}
}

func TestSendAndWait_BroadcastsNotRoutedToResponses(t *testing.T) {
	// Verify that broadcast messages (task_update, agent_update) go to subChan,
	// not respChan, so they don't interfere with sendAndWait.
	listener, err := net.Listen("unix", shortSocketPath(t))
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer listener.Close()

	go mockServer(t, listener, func(msg *daemon.Message) *daemon.Message {
		if msg.Type == daemon.MsgPing {
			resp, _ := daemon.NewMessage(daemon.MsgPong, nil)
			return resp
		}
		resp, _ := daemon.NewMessage(daemon.MsgError, daemon.ErrorResponse{Message: "unknown"})
		return resp
	})

	conn, err := net.Dial("unix", listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}

	c := &Client{
		conn:     conn,
		respChan: make(chan *daemon.Message, 100),
		subChan:  make(chan *daemon.Message, 100),
		errChan:  make(chan error, 1),
		done:     make(chan struct{}),
	}
	go c.readLoop(conn)
	defer c.Close()

	// Inject a broadcast message directly into the connection to simulate
	// a server-pushed update arriving while a request is in flight.
	// We can't easily do this with a simple mock, so instead verify the
	// daemon.IsBroadcast routing logic.
	broadcastTypes := []daemon.MessageType{daemon.MsgAgentUpdate, daemon.MsgTaskUpdate}
	for _, bt := range broadcastTypes {
		if !daemon.IsBroadcast(bt) {
			t.Errorf("expected %s to be classified as broadcast", bt)
		}
	}

	nonBroadcastTypes := []daemon.MessageType{daemon.MsgOK, daemon.MsgError, daemon.MsgTaskList, daemon.MsgPong}
	for _, nbt := range nonBroadcastTypes {
		if daemon.IsBroadcast(nbt) {
			t.Errorf("expected %s to NOT be classified as broadcast", nbt)
		}
	}

	// Verify a normal ping-pong still works
	msg, err := c.sendAndWait(daemon.MsgPing, nil)
	if err != nil {
		t.Fatalf("ping failed: %v", err)
	}
	if msg.Type != daemon.MsgPong {
		t.Errorf("expected pong, got %s", msg.Type)
	}
}

func TestSend_FireAndForget(t *testing.T) {
	// Verify that send() (used by Unsubscribe) works independently of sendAndWait.
	listener, err := net.Listen("unix", shortSocketPath(t))
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer listener.Close()

	var receivedMsg daemon.MessageType
	var serverDone sync.WaitGroup
	serverDone.Add(1)
	go func() {
		defer serverDone.Done()
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		scanner := bufio.NewScanner(conn)
		if scanner.Scan() {
			msg, _ := daemon.DecodeMessage(scanner.Bytes())
			if msg != nil {
				receivedMsg = msg.Type
			}
		}
	}()

	conn, err := net.Dial("unix", listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}

	c := &Client{
		conn:     conn,
		respChan: make(chan *daemon.Message, 100),
		subChan:  make(chan *daemon.Message, 100),
		errChan:  make(chan error, 1),
		done:     make(chan struct{}),
	}
	defer c.Close()

	err = c.send(daemon.MsgUnsubscribe, nil)
	if err != nil {
		t.Fatalf("send failed: %v", err)
	}

	conn.Close()
	serverDone.Wait()

	if receivedMsg != daemon.MsgUnsubscribe {
		t.Errorf("expected server to receive unsubscribe, got %s", receivedMsg)
	}
}

func TestUnsubscribe_DoesNotPoisonNextRequest(t *testing.T) {
	// Regression: Unsubscribe used to be fire-and-forget, but the daemon always
	// replies with MsgOK. The orphaned OK sat in respChan and ambushed the next
	// caller's response, decoding into the wrong type (e.g. zero TaskInfo).
	listener, err := net.Listen("unix", shortSocketPath(t))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	go mockServer(t, listener, func(msg *daemon.Message) *daemon.Message {
		switch msg.Type {
		case daemon.MsgUnsubscribe:
			resp, _ := daemon.NewMessage(daemon.MsgOK, daemon.OKResponse{Message: "unsubscribed"})
			return resp
		case daemon.MsgGetTask:
			resp, _ := daemon.NewMessage(daemon.MsgGetTask, daemon.GetTaskResponse{Task: daemon.TaskInfo{ID: 99, Title: "real"}})
			return resp
		}
		resp, _ := daemon.NewMessage(daemon.MsgError, daemon.ErrorResponse{Message: "unknown"})
		return resp
	})

	conn, err := net.Dial("unix", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := &Client{
		conn:     conn,
		respChan: make(chan *daemon.Message, 100),
		subChan:  make(chan *daemon.Message, 100),
		errChan:  make(chan error, 1),
		done:     make(chan struct{}),
	}
	go c.readLoop(conn)
	defer c.Close()

	if err := c.Unsubscribe(); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}

	task, err := c.GetTask(99)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.ID != 99 || task.Title != "real" {
		t.Errorf("GetTask returned wrong/zero data after Unsubscribe: %+v", task)
	}
}

// reconnectableMockServer is a mock daemon that accepts a sequence of
// connections. Each connection is handled with the supplied handler until
// the test closes the listener. Used by reconnect tests to break and then
// re-accept a fresh client connection.
type reconnectableMockServer struct {
	t        *testing.T
	listener net.Listener
	handler  func(*daemon.Message) *daemon.Message

	mu          sync.Mutex
	activeConns []net.Conn

	// broadcast is closed by tests once they want to push a broadcast to
	// every currently-subscribed conn. Use sendBroadcast for explicit pushes.
	subscribed map[net.Conn]bool

	wg sync.WaitGroup
}

func newReconnectableMockServer(t *testing.T, handler func(*daemon.Message) *daemon.Message) *reconnectableMockServer {
	t.Helper()
	listener, err := net.Listen("unix", shortSocketPath(t))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &reconnectableMockServer{
		t:          t,
		listener:   listener,
		handler:    handler,
		subscribed: make(map[net.Conn]bool),
	}
	s.wg.Add(1)
	go s.acceptLoop()
	return s
}

func (s *reconnectableMockServer) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.activeConns = append(s.activeConns, conn)
		s.mu.Unlock()
		s.wg.Add(1)
		go s.handleConn(conn)
	}
}

func (s *reconnectableMockServer) handleConn(conn net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.subscribed, conn)
		s.mu.Unlock()
		conn.Close()
	}()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		msg, err := daemon.DecodeMessage(scanner.Bytes())
		if err != nil {
			continue
		}
		if msg.Type == daemon.MsgSubscribe {
			s.mu.Lock()
			s.subscribed[conn] = true
			s.mu.Unlock()
		}
		if msg.Type == daemon.MsgUnsubscribe {
			s.mu.Lock()
			delete(s.subscribed, conn)
			s.mu.Unlock()
		}
		resp := s.handler(msg)
		if resp == nil {
			continue
		}
		data, _ := daemon.EncodeMessage(resp)
		conn.Write(data)
	}
}

// breakLatestConn closes the most recently-accepted connection from the
// server side, simulating a daemon-side conn drop / restart.
func (s *reconnectableMockServer) breakLatestConn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.activeConns) == 0 {
		return
	}
	last := s.activeConns[len(s.activeConns)-1]
	last.Close()
}

func (s *reconnectableMockServer) sendBroadcast(msgType daemon.MessageType, payload any) {
	msg, _ := daemon.NewMessage(msgType, payload)
	data, _ := daemon.EncodeMessage(msg)
	s.mu.Lock()
	conns := make([]net.Conn, 0, len(s.subscribed))
	for c := range s.subscribed {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.Write(data)
	}
}

func (s *reconnectableMockServer) connCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.activeConns)
}

func (s *reconnectableMockServer) close() {
	s.listener.Close()
	s.mu.Lock()
	for _, c := range s.activeConns {
		c.Close()
	}
	s.mu.Unlock()
}

// TestClient_ReconnectAfterServerSideBreak verifies that when the daemon
// closes the connection, the client transparently reconnects on the next
// RPC and the call succeeds.
func TestClient_ReconnectAfterServerSideBreak(t *testing.T) {
	srv := newReconnectableMockServer(t, func(msg *daemon.Message) *daemon.Message {
		switch msg.Type {
		case daemon.MsgPing:
			resp, _ := daemon.NewMessage(daemon.MsgPong, nil)
			return resp
		}
		resp, _ := daemon.NewMessage(daemon.MsgError, daemon.ErrorResponse{Message: "unknown"})
		return resp
	})
	defer srv.close()

	cfg := &config.Config{}
	cfg.SocketPath = srv.listener.Addr().String()
	c := New(cfg)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	// First ping establishes a working connection.
	if err := c.Ping(); err != nil {
		t.Fatalf("first ping: %v", err)
	}

	// Break the conn from the server side.
	srv.breakLatestConn()

	// Give the readLoop a beat to observe EOF and signal errChan. Not strictly
	// required (sendAndWait would Write-fail too) but exercises the EOF path.
	time.Sleep(20 * time.Millisecond)

	// Second ping must succeed — the client should reconnect transparently.
	if err := c.Ping(); err != nil {
		t.Fatalf("ping after break: %v", err)
	}

	// Sanity: at least two connections were accepted by the mock daemon.
	if got := srv.connCount(); got < 2 {
		t.Errorf("expected ≥2 accepted conns after reconnect, got %d", got)
	}
}

// TestClient_ReconnectSecondFailureEscalates verifies that if reconnect
// itself fails (daemon socket gone), the error escalates to the caller
// rather than spin-looping.
func TestClient_ReconnectSecondFailureEscalates(t *testing.T) {
	srv := newReconnectableMockServer(t, func(msg *daemon.Message) *daemon.Message {
		resp, _ := daemon.NewMessage(daemon.MsgPong, nil)
		return resp
	})

	cfg := &config.Config{}
	cfg.SocketPath = srv.listener.Addr().String()
	c := New(cfg)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	if err := c.Ping(); err != nil {
		t.Fatalf("first ping: %v", err)
	}

	// Kill the listener entirely AND break the current conn so reconnect's
	// own dial fails. Order matters — close listener first.
	srv.listener.Close()
	srv.breakLatestConn()

	// Wait for the readLoop to observe EOF so the next send sees errChan first.
	time.Sleep(20 * time.Millisecond)

	if err := c.Ping(); err == nil {
		t.Fatalf("expected ping to fail after listener closed; got nil error")
	}
}

// TestClient_ReconnectNotAttemptedAfterClose verifies that Close() is
// terminal — no reconnect fires for calls made after Close().
func TestClient_ReconnectNotAttemptedAfterClose(t *testing.T) {
	srv := newReconnectableMockServer(t, func(msg *daemon.Message) *daemon.Message {
		resp, _ := daemon.NewMessage(daemon.MsgPong, nil)
		return resp
	})
	defer srv.close()

	cfg := &config.Config{}
	cfg.SocketPath = srv.listener.Addr().String()
	c := New(cfg)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}

	if err := c.Ping(); err != nil {
		t.Fatalf("first ping: %v", err)
	}

	preCloseConns := srv.connCount()

	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Subsequent calls must return error without dialing a new conn.
	err := c.Ping()
	if err == nil {
		t.Fatalf("expected error pinging closed client")
	}

	if got := srv.connCount(); got != preCloseConns {
		t.Errorf("expected no new connections after Close(); got %d (was %d)", got, preCloseConns)
	}
}

// TestClient_SubscriptionPreservedAcrossReconnect verifies that the
// client's subscription state survives a transparent reconnect: after
// the connection is broken, a subsequent RPC triggers reconnect+resubscribe,
// and a daemon-side broadcast reaches the client over the new conn.
func TestClient_SubscriptionPreservedAcrossReconnect(t *testing.T) {
	srv := newReconnectableMockServer(t, func(msg *daemon.Message) *daemon.Message {
		switch msg.Type {
		case daemon.MsgSubscribe, daemon.MsgUnsubscribe:
			resp, _ := daemon.NewMessage(daemon.MsgOK, daemon.OKResponse{})
			return resp
		case daemon.MsgPing:
			resp, _ := daemon.NewMessage(daemon.MsgPong, nil)
			return resp
		}
		resp, _ := daemon.NewMessage(daemon.MsgError, daemon.ErrorResponse{Message: "unknown"})
		return resp
	})
	defer srv.close()

	cfg := &config.Config{}
	cfg.SocketPath = srv.listener.Addr().String()
	c := New(cfg)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	if err := c.Subscribe(); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Break the conn underneath, then trigger reconnect via another RPC.
	srv.breakLatestConn()
	time.Sleep(20 * time.Millisecond)

	if err := c.Ping(); err != nil {
		t.Fatalf("ping after break (should reconnect+resubscribe): %v", err)
	}

	// At this point the daemon-side mock should have re-registered the
	// new conn in its subscribers map. Fire a broadcast and assert the
	// client receives it.
	srv.sendBroadcast(daemon.MsgTaskUpdate, daemon.TaskUpdateResponse{Task: daemon.TaskInfo{ID: 77, Title: "post-reconnect"}})

	select {
	case msg := <-c.Messages():
		if msg.Type != daemon.MsgTaskUpdate {
			t.Errorf("expected MsgTaskUpdate, got %s", msg.Type)
		}
		var resp daemon.TaskUpdateResponse
		if err := msg.DecodePayload(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Task.ID != 77 {
			t.Errorf("expected task ID 77, got %d", resp.Task.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive broadcast after reconnect")
	}
}

// TestClient_FireAndForgetReconnectsOnBrokenPipe verifies that the
// fire-and-forget send() path also reconnects exactly once on Write
// failure (covers the `send()` retry contract).
//
// Note: a peer-side close alone does NOT reliably surface as Write error
// on Unix sockets — small writes can succeed into the kernel buffer even
// when the remote end is gone. To make this deterministic, we close the
// conn from the LOCAL side before send(), which guarantees a
// "use of closed network connection" error on the next Write and exercises
// the reconnect path.
func TestClient_FireAndForgetReconnectsOnBrokenPipe(t *testing.T) {
	srv := newReconnectableMockServer(t, func(msg *daemon.Message) *daemon.Message {
		return nil
	})
	defer srv.close()

	cfg := &config.Config{}
	cfg.SocketPath = srv.listener.Addr().String()
	c := New(cfg)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	// Close the conn locally — guarantees the next Write fails.
	c.mu.Lock()
	localConn := c.conn
	c.mu.Unlock()
	localConn.Close()

	// Give the readLoop a beat to observe EOF.
	time.Sleep(20 * time.Millisecond)

	if err := c.send(daemon.MsgPing, nil); err != nil {
		t.Fatalf("send (after local close, should reconnect): %v", err)
	}

	// net.Dial returns once the handshake completes, but the server's
	// acceptLoop goroutine may not have appended the new conn to
	// activeConns yet — poll briefly.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if srv.connCount() >= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("expected ≥2 accepted conns after fire-and-forget reconnect, got %d", srv.connCount())
}

func TestContinueTaskRequest_JSONEncoding(t *testing.T) {
	req := daemon.ContinueTaskRequest{TaskID: 42}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded daemon.ContinueTaskRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.TaskID != 42 {
		t.Errorf("expected task_id=42, got %d", decoded.TaskID)
	}
}
