package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// RunSync executes a shell command synchronously (`sh -c command`) with stdin
// piped and returns its stdout. Used for utility LLM calls (the configured
// `summarizer:` command: chat summarization, title generation,
// backfill-context) and agent helper commands (chat_log_command).
//
// env entries are added on top of the current process environment; CLAUDECODE
// is stripped so claude-based commands work when sortie itself runs inside a
// Claude Code session.
func RunSync(ctx context.Context, command, workDir string, env map[string]string, stdin string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	if workDir != "" {
		cmd.Dir = workDir
	}

	merged := stripClaudeCode(os.Environ())
	for k, v := range env {
		merged = append(merged, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = merged

	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Some tools print user-facing errors to stdout rather than stderr, so
		// surface both streams for diagnosis.
		return "", fmt.Errorf("command failed: %w (stdout: %s, stderr: %s)", err, truncateForError(stdout.String()), truncateForError(stderr.String()))
	}

	return stdout.String(), nil
}

// truncateForError clips a string to a sensible length for inclusion in error
// messages so a multi-megabyte dump cannot drown a log line.
func truncateForError(s string) string {
	const maxLen = 500
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + fmt.Sprintf("... (truncated, %d total bytes)", len(s))
}
