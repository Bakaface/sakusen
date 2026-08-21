package daemon

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/tmux"
)

// TestShutdownCompletesBeforeStartReturns covers the sakusen#337 race: the
// MsgShutdown handler runs shutdown() on a connection goroutine, and its first
// act — closing the listener — unblocks Start()'s accept loop. Before the fix,
// Start() could return (and the process exit) while shutdown() was still
// mid-flight, leaving a stale daemon.pid and an unclosed database. Start()
// must not return until shutdown's tail (DB close, pid/socket removal) has
// completed.
func TestShutdownCompletesBeforeStartReturns(t *testing.T) {
	// macOS unix socket paths are limited to 104 bytes and t.TempDir() embeds
	// the full test name, so use a short /tmp dir instead (same reasoning as
	// tests/e2e/env.go).
	dir, err := os.MkdirTemp("/tmp", "s")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	dbPath := filepath.Join(dir, "tasks.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// No defer database.Close() — shutdown() owns closing it.

	cfg := &config.Config{
		SocketPath:   filepath.Join(dir, "daemon.sock"),
		PidFile:      filepath.Join(dir, "daemon.pid"),
		DatabasePath: dbPath,
		PollInterval: 50 * time.Millisecond,
	}

	s := NewServer(cfg, database)

	startDone := make(chan error, 1)
	go func() { startDone <- s.Start() }()

	// Wait for the daemon socket to appear.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(cfg.SocketPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Send MsgShutdown over the socket, exactly like `sakusen daemon stop`.
	conn, err := net.Dial("unix", cfg.SocketPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	msg, err := NewMessage(MsgShutdown, nil)
	if err != nil {
		t.Fatalf("new message: %v", err)
	}
	data, err := EncodeMessage(msg)
	if err != nil {
		t.Fatalf("encode message: %v", err)
	}
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write shutdown message: %v", err)
	}

	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return after MsgShutdown")
	}

	// By the time Start() has returned, shutdown's tail must have run: the
	// pid file and socket must both be gone.
	if _, err := os.Stat(cfg.PidFile); !os.IsNotExist(err) {
		t.Errorf("pid file still present after Start returned (stat err: %v)", err)
	}
	if _, err := os.Stat(cfg.SocketPath); !os.IsNotExist(err) {
		t.Errorf("socket file still present after Start returned (stat err: %v)", err)
	}
}

// TestKillTaskSessionsSparesForeignSessions covers sakusen#357: the shutdown
// sweep used to kill every tmux session matching the bare "<project>-" prefix,
// so a project named "nexus" took unrelated user sessions like "nexus-em" down
// with it on every daemon restart. Only sessions whose suffix is a numeric
// task ID may be killed.
func TestKillTaskSessionsSparesForeignSessions(t *testing.T) {
	if !tmux.IsAvailable() {
		t.Skip("tmux is not installed")
	}

	s, _, projID := newAdvanceTestServer(t, oneStepConfigYML)

	// Give the project a name unlikely to collide with the developer's own
	// tmux sessions, since these tests talk to the real tmux server.
	projectName := fmt.Sprintf("sakusen357test%d", os.Getpid())
	s.projectsMu.Lock()
	s.projects[projID].cfg.Project.Name = projectName
	s.projectsMu.Unlock()

	workDir := t.TempDir()
	taskSession := tmux.NewSession(projectName, "42", workDir)
	foreignSession := &tmux.Session{Name: projectName + "-em", WorkDir: workDir}
	for _, sess := range []*tmux.Session{taskSession, foreignSession} {
		if err := sess.Create("sleep", "300"); err != nil {
			t.Fatalf("failed to create tmux session %s: %v", sess.Name, err)
		}
		t.Cleanup(func() { _ = sess.Kill() })
	}

	s.killTaskSessions()

	if taskSession.Exists() {
		t.Errorf("task session %s must be killed on shutdown", taskSession.Name)
	}
	if !foreignSession.Exists() {
		t.Errorf("session %s is not a sakusen task session and must survive shutdown", foreignSession.Name)
	}
}
