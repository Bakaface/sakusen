// Package runner spawns user-configured agent commands. It is the single seam
// between sortie's execution engine and whatever tool actually runs a step:
// every agent is a shell command (see config.AgentConfig) executed via
// `sh -c` in the task workdir with sortie's environment contract exported
// (SORTIE_PROMPT_FILE, SORTIE_RESULT_FILE, SORTIE_TASK_ID, ...). The runner
// knows nothing about any specific agent CLI — output parsing, session
// discovery, and turn-end signalling are the agent pipeline's job.
package runner

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// OutputLogFileName is the per-workdir raw output capture file for
	// headless agent spawns (stdout + stderr, verbatim). The daemon's
	// noiseFiles list and `sortie init`'s .gitignore entry reference this
	// exact name.
	OutputLogFileName = ".sortie-output.log"

	// stopPollInterval is how often to check whether a process has exited during graceful shutdown.
	stopPollInterval = 500 * time.Millisecond

	// stopGracePollAttempts is how many times to poll before force-killing (10 * 500ms = 5s grace period).
	stopGracePollAttempts = 10

	// stdoutTailLines is how many trailing stdout lines are retained in memory
	// as the crude ResultText fallback for pipelines that never write
	// $SORTIE_RESULT_FILE.
	stdoutTailLines = 50
)

// Process represents one headless agent command execution: `sh -c <command>`
// run in WorkDir. The process's stdout is streamed line-by-line to OutputFunc
// (timestamp-prefixed, for logs/TUI) and captured raw — together with stderr —
// into OutputFile.
//
// The agent's final result text is read from ResultFile after exit (the
// command's pipeline is expected to write $SORTIE_RESULT_FILE); when the file
// is absent or empty, the tail of captured stdout is used as a crude fallback.
type Process struct {
	TaskID     string
	WorkDir    string
	OutputFile string // raw stdout+stderr capture (OutputLogFileName in WorkDir)
	ResultFile string // where the agent pipeline writes the final result text
	OutputFunc func(lines []string)

	command string

	mu         sync.RWMutex
	cmd        *exec.Cmd
	env        []string
	stdoutTail []string
	exitCode   int
	exited     bool
	exitErr    error
}

// NewProcess creates a Process that will run the given shell command in
// workDir. resultFile is exported to the command as SORTIE_RESULT_FILE by the
// caller (via SetEnv) and read back by ResultText.
func NewProcess(taskID, workDir, command, resultFile string) *Process {
	return &Process{
		TaskID:     taskID,
		WorkDir:    workDir,
		OutputFile: filepath.Join(workDir, OutputLogFileName),
		ResultFile: resultFile,
		command:    command,
		exitCode:   -1,
	}
}

// SetEnv sets environment variables for the child process.
// Must be called before Start. Each entry is "KEY=VALUE".
func (p *Process) SetEnv(env map[string]string) {
	// Start with the current process environment
	p.env = os.Environ()
	for k, v := range env {
		p.env = append(p.env, fmt.Sprintf("%s=%s", k, v))
	}
}

// buildEnv returns the environment for the child process.
func (p *Process) buildEnv() []string {
	base := p.env
	if base == nil {
		base = os.Environ()
	}
	return stripClaudeCode(base)
}

// stripClaudeCode filters CLAUDECODE out of an environment so claude-based
// agent commands spawned from within a Claude Code session don't refuse to
// launch ("cannot launch inside another session"). Harmless for every other
// tool. Shared by Process spawns and RunSync.
func stripClaudeCode(base []string) []string {
	env := make([]string, 0, len(base))
	for _, e := range base {
		if strings.HasPrefix(e, "CLAUDECODE=") {
			continue
		}
		env = append(env, e)
	}
	return env
}

// Start launches the agent command. The process exits when the command's
// pipeline completes.
func (p *Process) Start() error {
	p.cmd = exec.Command("sh", "-c", p.command)
	p.cmd.Dir = p.WorkDir
	p.cmd.Env = p.buildEnv()

	// Raw output capture for debugging (stdout verbatim + stderr).
	outFile, err := os.Create(p.OutputFile)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}

	// Pipe stdout for line streaming.
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		outFile.Close()
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Stderr goes to the raw output file for diagnostics.
	p.cmd.Stderr = outFile

	if err := p.cmd.Start(); err != nil {
		outFile.Close()
		return fmt.Errorf("failed to start agent command: %w", err)
	}

	// Stream stdout, then wait for exit — must drain stdout before cmd.Wait().
	go func() {
		p.streamOutput(stdout, outFile)
		p.waitForExit(outFile)
	}()

	return nil
}

// streamOutput reads stdout lines, writes them raw to the output file, keeps a
// bounded tail for the result fallback, and forwards timestamp-prefixed lines
// to OutputFunc.
func (p *Process) streamOutput(stdout io.ReadCloser, rawFile *os.File) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB buffer for long lines

	linesRead := 0
	for scanner.Scan() {
		line := scanner.Text()
		linesRead++

		rawFile.WriteString(line)
		rawFile.WriteString("\n")

		p.mu.Lock()
		p.stdoutTail = append(p.stdoutTail, line)
		if len(p.stdoutTail) > stdoutTailLines {
			p.stdoutTail = p.stdoutTail[len(p.stdoutTail)-stdoutTailLines:]
		}
		p.mu.Unlock()

		if trimmed := strings.TrimRight(line, " \t"); trimmed != "" && p.OutputFunc != nil {
			p.OutputFunc([]string{fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), trimmed)})
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[task %s] stdout scanner error after %d lines: %v", p.TaskID, linesRead, err)
	}
	if linesRead == 0 {
		log.Printf("[task %s] warning: no stdout lines read from agent command", p.TaskID)
	}
}

func (p *Process) waitForExit(outFile *os.File) {
	err := p.cmd.Wait()
	outFile.Close()

	p.mu.Lock()
	defer p.mu.Unlock()

	p.exited = true
	p.exitErr = err

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			p.exitCode = exitErr.ExitCode()
		} else {
			p.exitCode = 1
		}
	} else {
		p.exitCode = 0
	}
}

// Stop sends SIGTERM for graceful shutdown, escalating to SIGKILL after 5s.
func (p *Process) Stop() error {
	p.mu.RLock()
	if p.exited || p.cmd == nil || p.cmd.Process == nil {
		p.mu.RUnlock()
		return nil
	}
	p.mu.RUnlock()

	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return p.cmd.Process.Kill()
	}

	for i := 0; i < stopGracePollAttempts; i++ {
		time.Sleep(stopPollInterval)
		p.mu.RLock()
		exited := p.exited
		p.mu.RUnlock()
		if exited {
			return nil
		}
	}

	// Still running — force kill
	if err := p.cmd.Process.Kill(); err != nil {
		return err
	}

	// Brief wait for kernel cleanup
	time.Sleep(stopPollInterval)
	return nil
}

func (p *Process) IsRunning() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return !p.exited && p.cmd != nil && p.cmd.Process != nil
}

// ExitCode returns the exit code, or -1 if still running
func (p *Process) ExitCode() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.exitCode
}

// HasExited returns true if the process has terminated
func (p *Process) HasExited() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.exited
}

// IsSuccess returns true if process exited with code 0
func (p *Process) IsSuccess() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.exited && p.exitCode == 0
}

// ResultText returns the agent's final result text. Only meaningful after the
// process has exited. Reads ResultFile when present and non-empty; otherwise
// falls back to the retained tail of stdout (crude — agent pipelines should
// write $SORTIE_RESULT_FILE).
func (p *Process) ResultText() string {
	if p.ResultFile != "" {
		if data, err := os.ReadFile(p.ResultFile); err == nil {
			if text := strings.TrimSpace(string(data)); text != "" {
				return text
			}
		}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return strings.TrimSpace(strings.Join(p.stdoutTail, "\n"))
}

// PID returns the process ID, or 0 if not running
func (p *Process) PID() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.cmd != nil && p.cmd.Process != nil {
		return p.cmd.Process.Pid
	}
	return 0
}
