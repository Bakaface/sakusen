package merge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/Bakaface/sakusen/internal/task"
)

// ErrVerifyFailed is returned (wrapped) when Config.VerifyCommand exits
// non-zero or times out. The merge is not attempted.
var ErrVerifyFailed = errors.New("verify command failed")

const (
	// maxVerifyLogBytes caps the output tail written to the task log.
	maxVerifyLogBytes = 16 << 10
	// maxVerifyDetailBytes caps the output tail carried in the returned error,
	// which becomes the task's failure reason.
	maxVerifyDetailBytes = 2 << 10
	// verifyWaitDelay bounds how long Wait blocks on inherited pipes after the
	// process group was killed.
	verifyWaitDelay = 5 * time.Second
)

// verify runs Config.VerifyCommand in the task worktree. It is a no-op when no
// command is configured.
func (c *Coordinator) verify(ctx context.Context, t *task.Task, logf LogFunc) error {
	command := c.cfg.VerifyCommand
	if command == "" {
		return nil
	}
	logf("Running verify command: %s", command)

	runCtx := ctx
	if c.cfg.VerifyTimeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, c.cfg.VerifyTimeout)
		defer cancel()
	}
	output, err := runVerifyCommand(runCtx, t.WorktreePath, command)
	if err == nil {
		logf("Verify command passed")
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("verify command interrupted: %w", ctx.Err())
	}

	var reason string
	var exitErr *exec.ExitError
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		reason = fmt.Sprintf("timed out after %s", c.cfg.VerifyTimeout)
	case errors.As(err, &exitErr):
		reason = fmt.Sprintf("exited with code %d", exitErr.ExitCode())
	default:
		reason = err.Error()
	}

	if tail := outputTail(output, maxVerifyLogBytes); tail != "" {
		logf("Verify command output (tail):\n%s", tail)
	}
	logf("Verify command %s", reason)

	detail := fmt.Sprintf("`%s` %s", command, reason)
	if tail := outputTail(output, maxVerifyDetailBytes); tail != "" {
		detail += "\n" + tail
	}
	return fmt.Errorf("%w: %s", ErrVerifyFailed, detail)
}

// runVerifyCommand runs command via `sh -c` in dir and returns its combined
// output. The command runs in its own process group so a timeout kills
// everything it spawned, not just the shell.
func runVerifyCommand(ctx context.Context, dir, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = verifyWaitDelay

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// outputTail returns at most maxBytes from the end of s, starting at a line
// boundary when one is available.
func outputTail(s string, maxBytes int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxBytes {
		return s
	}
	s = s[len(s)-maxBytes:]
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < len(s)-1 {
		s = s[i+1:]
	}
	return "... (truncated)\n" + s
}
