// Run with: go test -tags integration ./internal/runner/
//go:build integration

package runner

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// waitExit polls until the process exits or the deadline passes.
func waitExit(t *testing.T, proc *Process, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if proc.HasExited() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process did not exit within %s", d)
}

func TestProcessResultFile(t *testing.T) {
	workDir := t.TempDir()
	resultFile := filepath.Join(workDir, "result.txt")

	proc := NewProcess("test", workDir, `echo working; printf 'final result' > "$SORTIE_RESULT_FILE"`, resultFile)
	proc.SetEnv(map[string]string{"SORTIE_RESULT_FILE": resultFile})

	var mu sync.Mutex
	var captured []string
	proc.OutputFunc = func(lines []string) {
		mu.Lock()
		captured = append(captured, lines...)
		mu.Unlock()
	}

	if err := proc.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitExit(t, proc, 10*time.Second)

	if !proc.IsSuccess() {
		t.Errorf("expected success, exit=%d", proc.ExitCode())
	}
	if got := proc.ResultText(); got != "final result" {
		t.Errorf("ResultText = %q, want %q", got, "final result")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) == 0 || !strings.Contains(captured[0], "working") {
		t.Errorf("OutputFunc lines = %v, want a timestamped 'working' line", captured)
	}

	// Raw capture lands in .sortie-output.log.
	data, err := os.ReadFile(filepath.Join(workDir, OutputLogFileName))
	if err != nil {
		t.Fatalf("read output log: %v", err)
	}
	if !strings.Contains(string(data), "working") {
		t.Errorf("output log missing stdout capture: %q", string(data))
	}
}

func TestProcessStdoutTailFallback(t *testing.T) {
	workDir := t.TempDir()
	resultFile := filepath.Join(workDir, "result.txt") // never written

	proc := NewProcess("test", workDir, `echo line one; echo line two`, resultFile)
	proc.SetEnv(nil)
	if err := proc.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitExit(t, proc, 10*time.Second)

	if got := proc.ResultText(); got != "line one\nline two" {
		t.Errorf("ResultText fallback = %q, want stdout tail", got)
	}
}

func TestProcessNonZeroExit(t *testing.T) {
	workDir := t.TempDir()
	proc := NewProcess("test", workDir, `echo boom >&2; exit 3`, "")
	proc.SetEnv(nil)
	if err := proc.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitExit(t, proc, 10*time.Second)

	if proc.ExitCode() != 3 {
		t.Errorf("exit code = %d, want 3", proc.ExitCode())
	}
	data, _ := os.ReadFile(filepath.Join(workDir, OutputLogFileName))
	if !strings.Contains(string(data), "boom") {
		t.Errorf("stderr not captured in output log: %q", string(data))
	}
}

func TestProcessStop(t *testing.T) {
	workDir := t.TempDir()
	proc := NewProcess("test", workDir, `sleep 30`, "")
	proc.SetEnv(nil)
	if err := proc.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := proc.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	waitExit(t, proc, 10*time.Second)
	if proc.IsSuccess() {
		t.Errorf("expected non-success after Stop")
	}
}

// TestProcessStop_EscalatesToSigkill verifies the force-kill half of Stop: a
// command that traps (ignores) SIGTERM must still be terminated after the 5s
// grace period via SIGKILL. This is the primary reason Stop exists — without
// the escalation, a wedged agent would outlive its task forever.
func TestProcessStop_EscalatesToSigkill(t *testing.T) {
	workDir := t.TempDir()
	// The sleep child's stdout is redirected so the orphan left behind by
	// SIGKILL-ing `sh` doesn't hold the stdout pipe open (which would delay
	// the exit notification until the orphan dies — Stop only signals the
	// direct child, not the process group).
	proc := NewProcess("test", workDir, `trap '' TERM; sleep 60 > /dev/null 2>&1`, "")
	proc.SetEnv(nil)
	if err := proc.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Give sh a moment to install the trap before signalling.
	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	if err := proc.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	elapsed := time.Since(start)

	// Stop must have waited out the grace period (10 * 500ms) before killing.
	if elapsed < 4*time.Second {
		t.Errorf("Stop returned after %s; expected it to exhaust the ~5s SIGTERM grace before SIGKILL", elapsed)
	}
	waitExit(t, proc, 10*time.Second)
	if proc.IsSuccess() {
		t.Errorf("expected non-success after forced kill")
	}
}

// TestProcessStdoutTailBounded verifies the stdout tail ring keeps only the
// last stdoutTailLines lines, oldest dropped first — an off-by-one in the
// ring trim would silently corrupt the crude ResultText fallback.
func TestProcessStdoutTailBounded(t *testing.T) {
	workDir := t.TempDir()
	proc := NewProcess("test", workDir, `i=1; while [ $i -le 60 ]; do echo "line-$i"; i=$((i+1)); done`, "")
	proc.SetEnv(nil)
	if err := proc.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitExit(t, proc, 10*time.Second)

	lines := strings.Split(proc.ResultText(), "\n")
	if len(lines) != stdoutTailLines {
		t.Fatalf("tail has %d lines, want exactly %d", len(lines), stdoutTailLines)
	}
	if lines[0] != "line-11" {
		t.Errorf("oldest retained line = %q, want %q", lines[0], "line-11")
	}
	if lines[len(lines)-1] != "line-60" {
		t.Errorf("newest retained line = %q, want %q", lines[len(lines)-1], "line-60")
	}
}
