package workflow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/task"
)

const (
	// maxLoopExitLogBytes caps the command output tail written to the task log.
	maxLoopExitLogBytes = 16 << 10
	// maxLoopExitDetailBytes caps the output tail carried in the failure
	// reason.
	maxLoopExitDetailBytes = 2 << 10
	// loopExitWaitDelay bounds how long Wait blocks on inherited pipes after
	// the process group was killed.
	loopExitWaitDelay = 5 * time.Second
)

// evalLoopExitCommand runs a loop step's exit_condition command and reports
// whether the loop should exit: status 0 exits, 1 continues. Any other
// status, a timeout or a failure to start returns an error that must fail
// the task.
func (e *Engine) evalLoopExitCommand(ctx context.Context, t *task.Task, step config.StepConfig) (bool, error) {
	ec := step.Loop.ExitCondition
	fail := func(reason, output string) (bool, error) {
		if tail := loopExitOutputTail(output, maxLoopExitLogBytes); tail != "" {
			e.appendTaskLog(t.ID, "Loop exit: command output (tail):\n%s", tail)
		}
		e.appendTaskLog(t.ID, "Loop exit: command `%s` %s", ec.Command, reason)
		errMsg := fmt.Sprintf("step %q: loop exit command `%s` %s", step.Name, ec.Command, reason)
		if tail := loopExitOutputTail(output, maxLoopExitDetailBytes); tail != "" {
			errMsg += "\n" + tail
		}
		e.recordExitCode(t.ID, 1, errMsg)
		return false, errors.New(errMsg)
	}

	dir, err := ResolveAgentWorkDir(t)
	if err != nil {
		return fail(fmt.Sprintf("could not start: %v", err), "")
	}

	var stdin string
	if ec.StdinFrom != "" {
		stdin, err = e.database.GetTaskStepContext(t.ID, ec.StdinFrom)
		if err != nil {
			return fail(fmt.Sprintf("could not read stdin_from step %q context: %v", ec.StdinFrom, err), "")
		}
	}

	env := e.stepEnv(t, step.Name, "")
	delete(env, "SAKUSEN_AGENT")
	env["SAKUSEN_PURPOSE"] = "loop_exit"
	env["SAKUSEN_LOOP_ITERATION"] = strconv.Itoa(t.LoopIteration)

	timeout := ec.EffectiveTimeout()
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := runLoopExitCommand(runCtx, dir, ec.Command, stdin, env)

	var exitErr *exec.ExitError
	switch {
	case err == nil:
		e.appendTaskLog(t.ID, "Loop exit: command `%s` exited 0, exiting loop", ec.Command)
		return true, nil
	case ctx.Err() != nil:
		return false, fmt.Errorf("step %q: loop exit command interrupted: %w", step.Name, ctx.Err())
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return fail(fmt.Sprintf("timed out after %s", timeout), output)
	case errors.As(err, &exitErr) && exitErr.ExitCode() == 1:
		e.appendTaskLog(t.ID, "Loop exit: command `%s` exited 1, continuing loop", ec.Command)
		return false, nil
	case errors.As(err, &exitErr):
		return fail(fmt.Sprintf("exited %d (want 0 to exit the loop or 1 to continue)", exitErr.ExitCode()), output)
	default:
		return fail(fmt.Sprintf("could not start: %v", err), output)
	}
}

// runLoopExitCommand runs command via `bash -c` in dir with stdin and the
// given env overlaid on the daemon's, returning its combined output. The
// command runs in its own process group so a timeout kills everything it
// spawned, not just the shell.
func runLoopExitCommand(ctx context.Context, dir, command, stdin string, env map[string]string) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = strings.NewReader(stdin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = loopExitWaitDelay

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// loopExitOutputTail returns at most maxBytes from the end of s, starting at a
// line boundary when one is available.
func loopExitOutputTail(s string, maxBytes int) string {
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
