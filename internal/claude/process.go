package claude

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

	"github.com/Bakaface/sortie/internal/config"
)

const (
	// stopPollInterval is how often to check whether a process has exited during graceful shutdown.
	stopPollInterval = 500 * time.Millisecond

	// stopGracePollAttempts is how many times to poll before force-killing (10 * 500ms = 5s grace period).
	stopGracePollAttempts = 10
)

// Process represents a Claude Code CLI process running in automatic mode.
// Uses -p flag for one-shot execution (Ralphy-style approach).
//
// Future: Manual mode will use tmux sessions for interactive workflows
// where humans can attach, test, and continue conversations.
type Process struct {
	TaskID     string
	WorkDir    string
	OutputFile string
	OutputFunc func(lines []string) // Callback for parsed log lines
	cfg        *config.ClaudeConfig

	mu          sync.RWMutex
	cmd         *exec.Cmd
	env         []string
	parser      *StreamParser
	outputLines []string
	exitCode    int
	exited      bool
	exitErr     error
}

func NewProcess(taskID, workDir string, cfg *config.ClaudeConfig) *Process {
	outputFile := filepath.Join(workDir, ".claude-output.log")
	return &Process{
		TaskID:     taskID,
		WorkDir:    workDir,
		OutputFile: outputFile,
		cfg:        cfg,
		parser:     NewStreamParser(),
		exitCode:   -1,
	}
}

// SetEnv sets environment variables for the child process.
// Must be called before StartWithPrompt. Each entry is "KEY=VALUE".
func (p *Process) SetEnv(env map[string]string) {
	// Start with the current process environment
	p.env = os.Environ()
	for k, v := range env {
		p.env = append(p.env, fmt.Sprintf("%s=%s", k, v))
	}
}

// buildEnv returns the environment for the child process, filtering out
// CLAUDECODE to prevent "cannot launch inside another session" errors.
func (p *Process) buildEnv() []string {
	base := p.env
	if base == nil {
		base = os.Environ()
	}
	env := make([]string, 0, len(base))
	for _, e := range base {
		if strings.HasPrefix(e, "CLAUDECODE=") {
			continue
		}
		env = append(env, e)
	}
	return env
}

// StartWithPrompt runs Claude Code with -p flag (one-shot mode).
// The process exits automatically when the task is complete.
// systemPrompt, if non-empty, is passed via --system-prompt to override
// the default Claude system prompt.
func (p *Process) StartWithPrompt(prompt string, systemPrompt ...string) error {
	args := append([]string{}, p.cfg.Args()...)
	if len(systemPrompt) > 0 && systemPrompt[0] != "" {
		args = append(args, "--system-prompt", systemPrompt[0])
	}
	args = append(args, "--verbose", "--output-format", "stream-json", "-p", prompt)

	p.cmd = exec.Command(p.cfg.Command, args...)
	p.cmd.Dir = p.WorkDir
	p.cmd.Env = p.buildEnv()

	// Create raw output file for debugging (raw NDJSON + stderr)
	outFile, err := os.Create(p.OutputFile)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}

	// Pipe stdout for real-time NDJSON parsing
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		outFile.Close()
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Stderr goes to the raw output file for diagnostics
	p.cmd.Stderr = outFile

	if err := p.cmd.Start(); err != nil {
		outFile.Close()
		return fmt.Errorf("failed to start claude process: %w", err)
	}

	// Stream stdout, then wait for exit — must drain stdout before cmd.Wait()
	go func() {
		p.streamOutput(stdout, outFile)
		p.waitForExit(outFile)
	}()

	return nil
}

// streamOutput reads NDJSON lines from stdout, writes raw JSON to the output file,
// and passes parsed lines through the StreamParser to OutputFunc.
func (p *Process) streamOutput(stdout io.ReadCloser, rawFile *os.File) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB buffer for large tool inputs

	linesRead := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		linesRead++

		// Write raw JSON to .claude-output.log for debugging
		rawFile.Write(line)
		rawFile.Write([]byte("\n"))

		// Parse into human-readable lines
		formatted := p.parser.ParseLine(line)
		if len(formatted) > 0 && p.OutputFunc != nil {
			p.OutputFunc(formatted)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[task %s] stdout scanner error after %d lines: %v", p.TaskID, linesRead, err)
	}
	if linesRead == 0 {
		log.Printf("[task %s] warning: no stdout lines read from claude process", p.TaskID)
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

func (p *Process) Stop() error {
	p.mu.RLock()
	if p.exited || p.cmd == nil || p.cmd.Process == nil {
		p.mu.RUnlock()
		return nil
	}
	p.mu.RUnlock()

	// Send SIGTERM for graceful shutdown
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return p.cmd.Process.Kill()
	}

	// Wait up to 5s for graceful exit, then SIGKILL
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

// CaptureOutput reads the output file and returns lines
func (p *Process) CaptureOutput(maxLines int) ([]string, error) {
	file, err := os.Open(p.OutputFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB buffer for large NDJSON lines
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if maxLines > 0 && len(lines) >= maxLines {
			break
		}
	}

	return lines, scanner.Err()
}

// ResultText returns the final text output from the Claude process.
// Only meaningful after the process has exited.
func (p *Process) ResultText() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.parser.ResultText()
}

// SessionID returns the Claude session ID extracted from the NDJSON stream.
// Available as soon as the first system init event is parsed.
func (p *Process) SessionID() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.parser.SessionID()
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
