package tmux

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// SanitizeName replaces characters that tmux does not allow in session
// names. Tmux silently converts dots to underscores, so we do the same
// up-front to keep our prefix matching consistent with real session names.
//
// This is the single source of truth for the tmux session-name sanitization
// rule. internal/config derives project names through this same function
// (via config.SanitizeProjectName) so that a project's stored name always
// matches the tmux session prefix it must round-trip through.
func SanitizeName(name string) string {
	return strings.ReplaceAll(name, ".", "_")
}

// SessionPrefix returns the tmux session name prefix for a given project.
// The project name is sanitized to match tmux's own character replacements.
func SessionPrefix(projectName string) string {
	return SanitizeName(projectName) + "-"
}

type Session struct {
	Name    string
	WorkDir string
}

func NewSession(projectName, taskID, workDir string) *Session {
	return &Session{
		Name:    SessionPrefix(projectName) + taskID,
		WorkDir: workDir,
	}
}

// IsAvailable checks if the tmux binary exists in PATH.
func IsAvailable() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

func (s *Session) Create(command string, args ...string) error {
	cmdArgs := []string{
		"new-session",
		"-d",
		"-s", s.Name,
		"-c", s.WorkDir,
	}

	if command != "" {
		// Append command and args as separate arguments
		// tmux will execute: command arg1 arg2 ...
		cmdArgs = append(cmdArgs, command)
		cmdArgs = append(cmdArgs, args...)
	}

	cmd := exec.Command("tmux", cmdArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create tmux session: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

// SetupVars holds template variables for tmux setup command interpolation.
type SetupVars struct {
	// AgentCommand is the raw agent shell command from the agent record.
	// NOTE: running it outside the wrapper script loses the exported
	// SAKUSEN_* env contract — prefer {{run_agent}} unless you re-export env
	// yourself.
	AgentCommand string
	// RunAgent is the path to the wrapper script that runs the agent (env
	// exports + agent command + fallback shell).
	RunAgent string
}

// SetupCommandControlsAgent returns true if the setup command contains
// {{run_agent}} or {{agent_command}}, meaning the user controls where
// the agent runs rather than having it auto-start in window 0.
func SetupCommandControlsAgent(command string) bool {
	return strings.Contains(command, "{{run_agent}}") ||
		strings.Contains(command, "{{agent_command}}")
}

// RunSetupCommand runs a user-configured command after tmux session creation.
// Template variables: {{session_name}}, {{worktree_path}}, {{agent_command}}, {{run_agent}}.
func (s *Session) RunSetupCommand(command string, vars *SetupVars) error {
	if command == "" {
		return nil
	}

	resolved := strings.ReplaceAll(command, "{{session_name}}", s.Name)
	resolved = strings.ReplaceAll(resolved, "{{worktree_path}}", s.WorkDir)
	if vars != nil {
		resolved = strings.ReplaceAll(resolved, "{{agent_command}}", vars.AgentCommand)
		resolved = strings.ReplaceAll(resolved, "{{run_agent}}", vars.RunAgent)
	}

	cmd := exec.Command("sh", "-c", resolved)
	cmd.Dir = s.WorkDir

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tmux setup command failed: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

func (s *Session) Exists() bool {
	cmd := exec.Command("tmux", "has-session", "-t", s.Name)
	return cmd.Run() == nil
}

func (s *Session) Kill() error {
	cmd := exec.Command("tmux", "kill-session", "-t", s.Name)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to kill tmux session: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

func (s *Session) CapturePane(scrollbackLines int) ([]string, error) {
	args := []string{
		"capture-pane",
		"-t", s.Name,
		"-p",
		"-S", fmt.Sprintf("-%d", scrollbackLines),
	}

	cmd := exec.Command("tmux", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to capture pane: %w (stderr: %s)", err, stderr.String())
	}

	output := stdout.String()
	if output == "" {
		return nil, nil
	}

	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	return lines, nil
}

func (s *Session) SendKeys(keys string) error {
	// Use -l for literal text (handles special characters correctly)
	cmd := exec.Command("tmux", "send-keys", "-t", s.Name, "-l", keys)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to send keys: %w (stderr: %s)", err, stderr.String())
	}

	// Send Enter separately to submit
	enterCmd := exec.Command("tmux", "send-keys", "-t", s.Name, "Enter")
	enterCmd.Stderr = &stderr

	if err := enterCmd.Run(); err != nil {
		return fmt.Errorf("failed to send Enter: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

func (s *Session) IsAlive() bool {
	cmd := exec.Command("tmux", "list-panes", "-t", s.Name, "-F", "#{pane_dead}")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return false
	}

	return strings.TrimSpace(stdout.String()) == "0"
}

func ListSessions(prefix string) ([]*Session, error) {
	cmd := exec.Command("tmux", "list-sessions", "-F", "#{session_name}")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "no server running") ||
			strings.Contains(stderr.String(), "no sessions") {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list sessions: %w (stderr: %s)", err, stderr.String())
	}

	var sessions []*Session
	for _, name := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if name == "" {
			continue
		}
		if prefix == "" || strings.HasPrefix(name, prefix) {
			sessions = append(sessions, &Session{Name: name})
		}
	}

	return sessions, nil
}

// ExtractTaskID extracts the task ID from a session name.
// Strips the projectName prefix, then returns the next numeric segment.
func ExtractTaskID(projectName, sessionName string) string {
	prefix := SessionPrefix(projectName)
	if !strings.HasPrefix(sessionName, prefix) {
		return sessionName
	}
	rest := sessionName[len(prefix):]
	// rest is "<taskID>" — task IDs are numeric
	// If there's a dash (legacy format), return only the numeric part
	if idx := strings.Index(rest, "-"); idx > 0 {
		return rest[:idx]
	}
	return rest
}

// PipePane tees the pane output to a log file using tmux pipe-pane.
func (s *Session) PipePane(logFile string) error {
	cmd := exec.Command("tmux", "pipe-pane", "-t", s.Name, fmt.Sprintf("cat >> %s", logFile))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to pipe pane: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

// KillSessionsForTask kills the tmux session matching <projectName>-<taskID>.
func KillSessionsForTask(projectName, taskID string) error {
	prefix := SessionPrefix(projectName) + taskID
	sessions, err := ListSessions(prefix)
	if err != nil {
		return err
	}

	var lastErr error
	for _, s := range sessions {
		if err := s.Kill(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func AttachCommand(sessionName string) *exec.Cmd {
	return exec.Command("tmux", "attach-session", "-t", sessionName)
}

// IsInsideTmux returns true if the current process is running inside a tmux session.
func IsInsideTmux() bool {
	return os.Getenv("TMUX") != ""
}

// SwitchClientCommand returns a command that switches the current tmux client
// to the given session. Use this instead of AttachCommand when already inside tmux.
func SwitchClientCommand(sessionName string) *exec.Cmd {
	return exec.Command("tmux", "switch-client", "-t", sessionName)
}

// NestedAttachCommand returns an attach-session command with $TMUX unset,
// allowing it to run inside an existing tmux session (nested tmux).
func NestedAttachCommand(sessionName string) *exec.Cmd {
	cmd := exec.Command("tmux", "attach-session", "-t", sessionName)
	// Copy current environment but strip TMUX to allow nesting
	var filtered []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TMUX=") {
			filtered = append(filtered, e)
		}
	}
	cmd.Env = filtered
	return cmd
}
