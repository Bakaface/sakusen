# internal/tmux — Tmux Session Management

Session creation, lifecycle, pane capture, activity monitoring. Load `/tmux` skill before making substantial changes.

## Critical Invariants

- **Session names use sanitized project name (dots → underscores)** — must match tmux's own character replacement rules
- **`SetupCommandControlsAgent()` check required** — a setup command containing `{{run_agent}}` or `{{agent_command}}` means the user manages agent startup; skipping the check causes double-start or no-start. The `{{claude_command}}` variable was renamed `{{agent_command}}` (`SetupVars.AgentCommand`); the old name is a config load error
- **Must call `IsAvailable()` before any tmux operations** — binary may not exist; skipping causes cryptic failures
