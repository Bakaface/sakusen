---
name: tmux
description: >
  Sortie's tmux session management: session creation, lifecycle, pane capture,
  key sending, nested tmux detection, and activity monitoring. Use when editing
  files in internal/tmux/, working on tmux session creation, attachment, log
  piping, session cleanup, or activity detection.
---

# Tmux Session Management

`internal/tmux/` manages tmux sessions for interactive task steps and monitors their activity state.

## File Map

| File | Purpose |
|---|---|
| `session.go` | Session struct, creation, teardown, pane interaction, attachment commands, setup command support |
| `monitor.go` | Activity detection via content hash stability + idle pattern matching |

## Session Naming

```go
// SessionPrefix is a function that returns a project-scoped prefix.
// The project name is sanitized (dots replaced with underscores) to match tmux behavior.
func SessionPrefix(projectName string) string  // returns sanitizeSessionName(projectName) + "-"
```

- Session names are project-scoped: `<sanitizedProject>-<taskID>` (via `NewSession`)
- `sanitizeSessionName(name string) string` replaces `.` with `_` to match tmux's own character replacements

## Session Struct & Lifecycle

```go
type Session struct {
    Name    string
    WorkDir string
}

NewSession(projectName, taskID, workDir string) *Session
```

### Creation & Teardown
```go
(s *Session) Create(command string, args ...string) error  // tmux new-session -d
(s *Session) Exists() bool
(s *Session) IsAlive() bool                                // Has active processes
(s *Session) Kill() error
KillSessionsForTask(projectName, taskID string) error      // Kill all sessions matching prefix+taskID
```

## Setup Commands

```go
// SetupVars holds template variables for tmux setup command interpolation.
type SetupVars struct {
    AgentCommand string  // raw agent shell command from the agent record
    RunAgent     string  // path to the wrapper script that runs the agent with the SORTIE_* env exported (prefer this)
}

// Returns true if setup command contains {{run_agent}} or {{agent_command}},
// meaning the user controls where the agent runs.
SetupCommandControlsAgent(command string) bool

// Runs a user-configured command after tmux session creation.
// Template variables: {{session_name}}, {{worktree_path}}, {{agent_command}}, {{run_agent}}.
(s *Session) RunSetupCommand(command string, vars *SetupVars) error
```

`{{claude_command}}` was renamed `{{agent_command}}`; the old variable in a
`tmux-setup-command` is rejected at config load with a migration error.

## Interaction

```go
(s *Session) CapturePane(scrollbackLines int) ([]string, error)  // Capture terminal output
(s *Session) SendKeys(keys string) error                          // Send keystrokes
(s *Session) PipePane(logFile string) error                       // Stream output to log file
```

## Attachment Commands

```go
AttachCommand(sessionName string) *exec.Cmd           // tmux attach-session
SwitchClientCommand(sessionName string) *exec.Cmd     // tmux switch-client (when inside tmux)
NestedAttachCommand(sessionName string) *exec.Cmd     // For nested tmux scenarios
```

## Detection & Listing

```go
IsAvailable() bool                              // tmux binary exists and works
IsInsideTmux() bool                             // Checks $TMUX env var
ListSessions(prefix string) ([]*Session, error) // List sessions matching prefix
ExtractTaskID(projectName, sessionName string) string  // Parse task ID from session name
```

## Activity Monitoring

`monitor.go` detects whether a tmux session is idle or actively working by combining content hash stability with idle pattern matching.

### Types

```go
type Activity string

const (
    ActivityIdle    Activity = "idle"
    ActivityWIP     Activity = "wip"
    ActivityUnknown Activity = "unknown"
)
```

### Configuration

```go
type MonitorConfig struct {
    PollInterval      time.Duration    // how often to check sessions (default: 2s)
    StableThreshold   int              // consecutive identical hashes needed when pattern matches (default: 3)
    FallbackThreshold int              // hash-only threshold when no pattern configured (default: 6)
    IdlePatterns      []*regexp.Regexp // patterns to match against tail lines
    PatternScanLines  int              // number of lines from bottom to scan (default: 5)
}

DefaultMonitorConfig() MonitorConfig  // sensible defaults for Claude Code sessions
```

Default idle patterns match: `╰─>` (Claude Code prompt), `$\s*$` (shell prompt), `>\s*$` (generic prompt).

### Monitor

```go
type Monitor struct { ... }

NewMonitor(cfg MonitorConfig) *Monitor

// Check captures pane content and determines activity state.
// Returns the activity and whether it changed from the previous check.
(m *Monitor) Check(session *Session) (Activity, bool)

// Remove cleans up tracking state for a session that has ended.
(m *Monitor) Remove(sessionName string)

// Sessions returns the set of tracked session names (for cleanup).
(m *Monitor) Sessions() map[string]*sessionState
```

Detection logic: hash all captured lines, track consecutive identical hashes per session. If idle patterns are configured and match the tail lines, use `StableThreshold`; otherwise fall back to `FallbackThreshold` (hash-only stability).

## Patterns

- Always check `IsAvailable()` before attempting tmux operations
- Use `SwitchClientCommand` when `IsInsideTmux()` is true, `AttachCommand` otherwise
- `NestedAttachCommand` used when `TmuxNestedAttachBehavior == "nest"`
- Session working directory set via `-c` flag on creation
- `PipePane` enables log capture for non-interactive monitoring
- Use `SetupCommandControlsAgent()` to check if the user's setup command manages agent startup
