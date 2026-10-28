package merge

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

	gitpkg "github.com/Bakaface/sakusen/internal/git"
	"github.com/Bakaface/sakusen/internal/task"
)

const (
	// defaultCommitMessageTimeout applies when Config.CommitMessageTimeout is
	// zero.
	defaultCommitMessageTimeout = 2 * time.Minute
	// maxCommitDiffBytes caps the staged diff fed to the message command.
	maxCommitDiffBytes = 128 << 10
	// maxCommitMessageLogBytes caps the stderr tail logged on failure.
	maxCommitMessageLogBytes = 2 << 10
)

// commitMessage returns the message for the worktree commit about to be made.
// With Config.CommitMessageCommand set it stages the worktree and asks the
// command; any failure falls back to the title-derived message, so the
// command can never block a commit.
func (c *Coordinator) commitMessage(ctx context.Context, t *task.Task, logf LogFunc) string {
	fallback := gitpkg.ConventionalCommitFromTitle(t.Title)
	command := c.cfg.CommitMessageCommand
	if command == "" {
		return fallback
	}
	useFallback := func(reason string) string {
		logf("Commit message command %s; using title-derived message", reason)
		return fallback
	}

	if err := c.repo.StageAll(t.WorktreePath); err != nil {
		return useFallback(fmt.Sprintf("skipped (%v)", err))
	}
	diff, err := c.repo.StagedDiff(t.WorktreePath)
	if err != nil {
		return useFallback(fmt.Sprintf("skipped (%v)", err))
	}
	if diff == "" {
		// Nothing staged: the commit is a no-op, so don't run the command.
		return fallback
	}

	logf("Running commit message command: %s", command)
	runCtx, cancel := context.WithTimeout(ctx, c.cfg.CommitMessageTimeout)
	defer cancel()
	stdout, stderr, err := runCommitMessageCommand(runCtx, t.WorktreePath, command, capDiff(diff), c.commitMessageEnv(t))
	if err != nil {
		if tail := outputTail(stderr, maxCommitMessageLogBytes); tail != "" {
			logf("Commit message command stderr (tail):\n%s", tail)
		}
		var exitErr *exec.ExitError
		switch {
		case ctx.Err() != nil:
			return useFallback("interrupted")
		case errors.Is(runCtx.Err(), context.DeadlineExceeded):
			return useFallback(fmt.Sprintf("timed out after %s", c.cfg.CommitMessageTimeout))
		case errors.As(err, &exitErr):
			return useFallback(fmt.Sprintf("exited with code %d", exitErr.ExitCode()))
		default:
			return useFallback(fmt.Sprintf("failed: %v", err))
		}
	}

	msg := firstNonEmptyLine(stdout)
	if msg == "" {
		return useFallback("printed nothing")
	}
	logf("Commit message: %s", msg)
	return msg
}

// commitMessageEnv is the message command's environment: the daemon's own
// plus the step env contract keys that apply outside a step.
func (c *Coordinator) commitMessageEnv(t *task.Task) []string {
	env := append(os.Environ(),
		"SAKUSEN_TASK_ID="+strconv.FormatInt(t.ID, 10),
		"SAKUSEN_TASK_TITLE="+t.Title,
		"SAKUSEN_WORKTREE="+t.WorktreePath,
		"SAKUSEN_PROJECT_PATH="+c.repo.Root(),
		"SAKUSEN_PURPOSE=commit_message",
	)
	if t.TrackID != nil {
		env = append(env, "SAKUSEN_TRACK_ID="+strconv.FormatInt(*t.TrackID, 10))
	}
	return env
}

// runCommitMessageCommand runs command via `bash -c` in dir with stdin on its
// standard input. Like the verify command it gets its own process group so a
// timeout kills everything it spawned.
func runCommitMessageCommand(ctx context.Context, dir, command, stdin string, env []string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = verifyWaitDelay

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// capDiff truncates diff to maxCommitDiffBytes at a line boundary and appends
// a note saying so.
func capDiff(diff string) string {
	if len(diff) <= maxCommitDiffBytes {
		return diff
	}
	head := diff[:maxCommitDiffBytes]
	if i := strings.LastIndexByte(head, '\n'); i > 0 {
		head = head[:i+1]
	}
	return fmt.Sprintf("%s\n[diff truncated: first %d of %d bytes shown]\n", head, len(head), len(diff))
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
