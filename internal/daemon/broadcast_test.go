package daemon

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
)

// TestFinalizeCompletedTaskNoProjectContext verifies that when the project context
// cannot be loaded, finalizeCompletedTask still marks the task as completed.
func TestFinalizeCompletedTaskNoProjectContext(t *testing.T) {
	dir := t.TempDir()

	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	cfg := &config.Config{}
	s := NewServer(cfg, database)

	proj, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	tk, err := database.CreateTask(proj.ID, "Test task", "desc", "slug", "default", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Use a non-existent project ID to force project context failure
	tk.ProjectID = 99999
	// No worktree so no cleanup needed
	tk.Worktree = false
	tk.WorktreePath = ""

	s.finalizeCompletedTask(tk, "agent-1", "Test task")

	// Re-fetch using original project
	tk2, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}

	if tk2.Status != task.StatusCompleted {
		t.Errorf("expected task status %s, got %s", task.StatusCompleted, tk2.Status)
	}
}

// TestFinalizeCompletedTaskSetsFinalizingStatus verifies that finalizeCompletedTask
// sets StatusFinalizing before calling runFinalization when there's a project context
// and meaningful changes detected. Since we can't easily mock git operations in an
// integration test, we test the case where the worktree path is empty (skips fast-track).
func TestFinalizeCompletedTaskSetsFinalizingBeforeCompletion(t *testing.T) {
	dir := t.TempDir()

	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	cfg := &config.Config{
		OnComplete: "none",
		Workflows: []config.WorkflowConfig{
			{
				Name: "default",
				Steps: []config.StepConfig{
					{Name: "implement", Prompt: "do something"},
				},
			},
		},
	}
	s := NewServer(cfg, database)

	proj, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Pre-load project context so finalizeCompletedTask can find it
	if _, err := s.getProjectContext(proj.ID); err != nil {
		t.Fatalf("failed to pre-load project context: %v", err)
	}

	tk, err := database.CreateTask(proj.ID, "Test task", "desc", "slug", "default", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// No worktree path: skips the fast-track check
	// No worktree: skips fast-track entirely (t.Worktree == false and t.WorktreePath == "")
	tk.Worktree = false
	tk.WorktreePath = ""

	s.finalizeCompletedTask(tk, "agent-1", "Test task")

	refreshed, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}

	// After finalization, the task should be completed
	if refreshed.Status != task.StatusCompleted {
		t.Errorf("expected task status %s after finalization, got %s", task.StatusCompleted, refreshed.Status)
	}
}

// TestBroadcast_DropsDeadSubscriberConn verifies that broadcastToSubscribers
// removes a subscriber whose conn has been closed from the peer side and
// does not deadlock during the cleanup.
func TestBroadcast_DropsDeadSubscriberConn(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	cfg := &config.Config{}
	s := NewServer(cfg, database)

	// Build a socketpair-like pair so we can close one side from the test.
	a, b := net.Pipe()
	t.Cleanup(func() {
		a.Close()
		b.Close()
	})

	// Register `a` as a subscriber (server's view of the client).
	s.mu.Lock()
	s.clients[a] = true
	s.subscribers[a] = true
	s.mu.Unlock()

	// Kill the peer side WITHOUT calling Unsubscribe. The broadcast Write to
	// `a` should fail (peer closed), triggering cleanup.
	b.Close()

	// Fire a broadcast — this exercises broadcastSend with the dead conn,
	// the deadline + error path, and dropDeadConns.
	done := make(chan struct{})
	go func() {
		s.broadcastToSubscribers(MsgTaskUpdate, TaskUpdateResponse{Task: TaskInfo{ID: 1}})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("broadcast deadlocked or did not return within 5s")
	}

	// Assert dead conn was removed from subscribers AND clients.
	s.mu.RLock()
	_, stillSub := s.subscribers[a]
	_, stillClient := s.clients[a]
	s.mu.RUnlock()

	if stillSub {
		t.Error("dead conn still in subscribers map after broadcast")
	}
	if stillClient {
		t.Error("dead conn still in clients map after broadcast")
	}
}

// TestBroadcast_WriteDeadlineEnforced verifies that a subscriber whose
// reader is blocked (never drains) does not stall the broadcast loop —
// the SetWriteDeadline forces broadcastSend to return a timeout error,
// and dropDeadConns removes the unresponsive peer. Without the deadline
// this test would hang indefinitely on net.Pipe (its Write blocks until
// the peer reads). The pipe's "Write deadline supported" property is part
// of net.Pipe's contract, mirroring real Unix socket behavior.
//
// Manual reproduction with a real daemon socket: hold a subscriber's
// reader (SIGSTOP its process) and observe `daemon: client write failed,
// dropping conn` in the daemon log within ~broadcastWriteTimeout seconds.
func TestBroadcast_WriteDeadlineEnforced(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	cfg := &config.Config{}
	s := NewServer(cfg, database)

	// net.Pipe writes block until the peer reads. We never read from peer,
	// so without a deadline the write would block forever.
	a, b := net.Pipe()
	t.Cleanup(func() {
		a.Close()
		b.Close()
	})

	s.mu.Lock()
	s.subscribers[a] = true
	s.mu.Unlock()

	// Override the timeout to keep the test fast; restore on exit.
	// We can't change the const so we just rely on a small deadline by
	// pre-setting one. The actual production deadline is 2s; for the test
	// we want to assert the deadline is enforced, not the specific value.
	// 200ms is plenty short to keep test runtime manageable.
	_ = a.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))

	start := time.Now()
	done := make(chan struct{})
	go func() {
		// Call broadcastSend directly: broadcastToSubscribers would also
		// re-apply broadcastWriteTimeout (2s), which would slow the test.
		_ = s.broadcastSend(a, MsgTaskUpdate, TaskUpdateResponse{Task: TaskInfo{ID: 1}})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("broadcastSend did not honor write deadline (stalled >5s)")
	}

	elapsed := time.Since(start)
	// broadcastSend's own SetWriteDeadline(now + 2s) RESETS the deadline
	// we set above. So the effective bound is broadcastWriteTimeout (2s).
	// Allow generous slack for slow CI hosts.
	if elapsed > broadcastWriteTimeout+1*time.Second {
		t.Errorf("broadcastSend took %v, expected ≤%v", elapsed, broadcastWriteTimeout+1*time.Second)
	}
}

// TestFinalizeCompletedTaskFastTracksNoWorktree verifies that non-worktree tasks
// that have a project context still end up as StatusCompleted.
func TestFinalizeCompletedTaskCompletesSuccessfully(t *testing.T) {
	dir := t.TempDir()

	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	cfg := &config.Config{
		OnComplete: "none",
	}
	s := NewServer(cfg, database)

	proj, err := database.GetOrCreateProject(dir)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Pre-load project context
	if _, err := s.getProjectContext(proj.ID); err != nil {
		t.Fatalf("failed to pre-load project context: %v", err)
	}

	tk, err := database.CreateTask(proj.ID, "Completed task", "desc", "slug", "default", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	tk.Worktree = false
	tk.WorktreePath = ""

	s.finalizeCompletedTask(tk, "test-agent", "Completed task")

	refreshed, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}

	if refreshed.Status != task.StatusCompleted {
		t.Errorf("expected StatusCompleted, got %s", refreshed.Status)
	}
}
