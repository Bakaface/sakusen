//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDaemonStopCleanShutdown asserts that `sakusen daemon stop` lets the
// daemon finish its full shutdown sequence before the process exits
// (sakusen#337): previously the MsgShutdown handler's listener.Close()
// unblocked the main goroutine's accept loop, so the process could exit
// mid-shutdown — leaving a stale daemon.pid, an unclosed SQLite database,
// and a daemon log without the final "Daemon stopped" line.
func TestDaemonStopCleanShutdown(t *testing.T) {
	e := setupE2E(t, "happy_path")

	e.MustSakusen("daemon", "stop")

	// The daemon process must exit.
	done := make(chan struct{})
	go func() {
		_ = e.daemonCmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		e.logDaemonOutput(t)
		t.Fatal("daemon process did not exit within 5s of `daemon stop`")
	}

	// The pid and socket files must be gone.
	pidFile := filepath.Join(e.XDGDir, "sakusen", "daemon.pid")
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Errorf("daemon.pid still present after daemon stop (stat err: %v)", err)
	}
	sockFile := filepath.Join(e.XDGDir, "sakusen", "daemon.sock")
	if _, err := os.Stat(sockFile); !os.IsNotExist(err) {
		t.Errorf("daemon.sock still present after daemon stop (stat err: %v)", err)
	}

	// The log must end with the final shutdown line — its absence means the
	// process died partway through shutdown().
	data, err := os.ReadFile(e.DaemonLogPath())
	if err != nil {
		t.Fatalf("read daemon log: %v", err)
	}
	if !strings.Contains(string(data), "Daemon stopped") {
		t.Errorf("daemon log does not contain %q — shutdown did not complete:\n%s", "Daemon stopped", string(data))
	}
}
