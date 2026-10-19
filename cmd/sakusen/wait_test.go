package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/daemon"
)

// startWaitDaemon serves subscribe/unsubscribe/get_task on socketPath from a
// fixed status table.
func startWaitDaemon(t *testing.T, socketPath string, statuses map[int64]string) {
	t.Helper()
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { l.Close() })

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				scanner := bufio.NewScanner(conn)
				for scanner.Scan() {
					msg, err := daemon.DecodeMessage(scanner.Bytes())
					if err != nil {
						return
					}
					var resp *daemon.Message
					switch msg.Type {
					case daemon.MsgSubscribe, daemon.MsgUnsubscribe:
						resp, _ = daemon.NewMessage(daemon.MsgOK, nil)
					case daemon.MsgGetTask:
						var req daemon.GetTaskRequest
						_ = msg.DecodePayload(&req)
						if s, ok := statuses[req.TaskID]; ok {
							resp, _ = daemon.NewMessage(daemon.MsgGetTask, daemon.GetTaskResponse{Task: daemon.TaskInfo{ID: req.TaskID, Status: s}})
						} else {
							resp, _ = daemon.NewMessage(daemon.MsgError, daemon.ErrorResponse{Message: fmt.Sprintf("task %d not found", req.TaskID)})
						}
					default:
						resp, _ = daemon.NewMessage(daemon.MsgError, daemon.ErrorResponse{Message: "unhandled"})
					}
					data, _ := daemon.EncodeMessage(resp)
					if _, err := conn.Write(data); err != nil {
						return
					}
				}
			}()
		}
	}()
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sw")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func waitTestConfig(t *testing.T, statuses map[int64]string) *config.Config {
	t.Helper()
	sock := filepath.Join(shortTempDir(t), "d.sock")
	startWaitDaemon(t, sock, statuses)
	cfg := &config.Config{}
	cfg.SocketPath = sock
	return cfg
}

func TestRunWait_MatchedExitsZero(t *testing.T) {
	cfg := waitTestConfig(t, map[int64]string{1: "running", 2: "completed"})
	var out bytes.Buffer
	code, err := runWait(context.Background(), &out, cfg, []string{"1", "2"}, nil, time.Minute)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	var res client.WaitResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal %q: %v", out.String(), err)
	}
	if res.TimedOut || len(res.Matched) != 1 || res.Matched[0].ID != 2 || len(res.Tasks) != 2 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestRunWait_TimeoutExitsTwo(t *testing.T) {
	cfg := waitTestConfig(t, map[int64]string{1: "running"})
	var out bytes.Buffer
	code, err := runWait(context.Background(), &out, cfg, []string{"1"}, nil, 100*time.Millisecond)
	if err != nil || code != waitExitTimedOut {
		t.Fatalf("code=%d err=%v", code, err)
	}
	var res client.WaitResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || !res.TimedOut {
		t.Fatalf("expected timed_out result, got %q (%v)", out.String(), err)
	}
}

func TestRunWait_RepeatableStatus(t *testing.T) {
	cfg := waitTestConfig(t, map[int64]string{1: "tmux", 2: "awaiting-approval"})
	var out bytes.Buffer
	code, err := runWait(context.Background(), &out, cfg, []string{"1", "2"}, []string{"pending", "awaiting-approval"}, time.Minute)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	var res client.WaitResult
	_ = json.Unmarshal(out.Bytes(), &res)
	if len(res.Matched) != 1 || res.Matched[0].ID != 2 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestRunWait_ErrorsExitOne(t *testing.T) {
	cfg := waitTestConfig(t, map[int64]string{1: "running"})
	for name, tc := range map[string]struct {
		args, statuses []string
		timeout        time.Duration
	}{
		"bad id":           {args: []string{"abc"}},
		"zero id":          {args: []string{"0"}},
		"unknown status":   {args: []string{"1"}, statuses: []string{"merged"}},
		"negative timeout": {args: []string{"1"}, timeout: -time.Second},
		"unknown task":     {args: []string{"1", "42"}},
	} {
		var out bytes.Buffer
		code, err := runWait(context.Background(), &out, cfg, tc.args, tc.statuses, tc.timeout)
		if err == nil || code != 1 {
			t.Errorf("%s: code=%d err=%v", name, code, err)
		}
		if out.Len() != 0 {
			t.Errorf("%s: nothing should be printed on error, got %q", name, out.String())
		}
	}

	unreachable := &config.Config{}
	unreachable.SocketPath = filepath.Join(shortTempDir(t), "none.sock")
	if code, err := runWait(context.Background(), &bytes.Buffer{}, unreachable, []string{"1"}, nil, 0); err == nil || code != 1 {
		t.Errorf("unreachable daemon: code=%d err=%v", code, err)
	}
}

// TestWaitCmd_RunsWithoutProjectConfig drives the real root command from a
// directory with no .sakusen.yml; the match on the second --status proves the
// flag accumulates rather than overwrites.
func TestWaitCmd_RunsWithoutProjectConfig(t *testing.T) {
	root := shortTempDir(t)
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	if err := os.MkdirAll(filepath.Join(root, "sakusen"), 0o755); err != nil {
		t.Fatal(err)
	}
	startWaitDaemon(t, filepath.Join(root, "sakusen", "daemon.sock"), map[int64]string{5: "failed"})
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetArgs([]string{"wait", "5", "--status", "completed", "--status", "failed", "--timeout", "10s"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetArgs(nil)
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("wait without .sakusen.yml: %v", err)
	}
	var res client.WaitResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || len(res.Matched) != 1 {
		t.Fatalf("unexpected output %q (%v)", out.String(), err)
	}
}
