package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// AgentSyncCall describes one synchronous agent invocation through the file
// contract: the prompt goes in via SAKUSEN_PROMPT_FILE and the answer comes
// back out of SAKUSEN_RESULT_FILE.
type AgentSyncCall struct {
	// Command is the agent record's command, run via `sh -c`.
	Command string
	// WorkDir is the command's working directory.
	WorkDir string
	// ProjectPath is exported as SAKUSEN_PROJECT_PATH — agent commands
	// routinely locate their own scripts through it.
	ProjectPath string
	// Purpose is exported as SAKUSEN_PURPOSE so a single agent command can
	// route by call site.
	Purpose string
	// Env is the agent record's `env:` map, merged underneath the contract
	// variables (the contract wins).
	Env map[string]string
}

// RunAgentSync runs an agent command synchronously with the prompt passed
// through a file rather than stdin, and returns its answer. Both files live in
// a per-invocation scratch directory removed before returning — never the task
// worktree, because callers like AI title generation run before any worktree
// exists.
//
// The answer is read from the result file; when the command wrote nothing
// there, the trimmed tail of its stdout is used instead (mirroring
// Process.ResultText, for pipelines that only print).
func RunAgentSync(ctx context.Context, call AgentSyncCall, prompt string) (string, error) {
	scratch, err := os.MkdirTemp("", "sakusen-agent-")
	if err != nil {
		return "", fmt.Errorf("create scratch dir: %w", err)
	}
	defer os.RemoveAll(scratch)

	promptFile := filepath.Join(scratch, "prompt.txt")
	resultFile := filepath.Join(scratch, "result.txt")
	if err := os.WriteFile(promptFile, []byte(prompt), 0644); err != nil {
		return "", fmt.Errorf("write prompt file: %w", err)
	}

	contract := map[string]string{
		"SAKUSEN_PROMPT_FILE": promptFile,
		"SAKUSEN_RESULT_FILE": resultFile,
	}
	if call.Purpose != "" {
		contract["SAKUSEN_PURPOSE"] = call.Purpose
	}
	if call.ProjectPath != "" {
		contract["SAKUSEN_PROJECT_PATH"] = call.ProjectPath
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", call.Command)
	if call.WorkDir != "" {
		cmd.Dir = call.WorkDir
	}
	env := stripClaudeCode(os.Environ())
	for k, v := range MergeEnv(contract, call.Env) {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("agent command failed: %w (stdout: %s, stderr: %s)", err, truncateForError(stdout.String()), truncateForError(stderr.String()))
	}

	if data, err := os.ReadFile(resultFile); err == nil {
		if text := strings.TrimSpace(string(data)); text != "" {
			return text, nil
		}
	}
	return stdoutTail(stdout.String()), nil
}

// stdoutTail returns the last stdoutTailLines lines of s, trimmed — the crude
// fallback for agent commands that print their answer instead of writing
// $SAKUSEN_RESULT_FILE.
func stdoutTail(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > stdoutTailLines {
		lines = lines[len(lines)-stdoutTailLines:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
