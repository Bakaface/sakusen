# internal/tmux — Tmux Session Management

Session creation, lifecycle, pane capture, activity monitoring for interactive task steps.

## Critical Invariants

- **Session names use sanitized project name (dots → underscores)** — must match tmux's own character replacement rules
- **`SetupCommandControlsAgent()` check required** — a setup command containing `{{run_agent}}` or `{{agent_command}}` means the user manages agent startup; skipping the check causes double-start or no-start. The `{{claude_command}}` variable was renamed `{{agent_command}}` (`SetupVars.AgentCommand`); the old name is a config load error
- **Must call `IsAvailable()` before any tmux operations** — binary may not exist; skipping causes cryptic failures

## File Map

| File | Purpose |
|------|---------|
| `session.go` | `Session{Name, WorkDir, RootDir}`: create (`new-session -d`, working dir via `-c`; the setup command also runs in `WorkDir`, while `{{worktree_path}}` expands to `RootDir`, defaulting to `WorkDir`), `SessionName` / `NewSession`, `FindSession` (live-session lookup with legacy fallback), exists/alive/kill, `KillSessionsForTask`, pane capture, `SendKeys`, `PipePane` (log capture), attach/switch-client/nested-attach commands, setup-command templating (`{{session_name}}`, `{{worktree_path}}`, `{{agent_command}}`, `{{run_agent}}`), `IsAvailable`, `IsInsideTmux`, `ListSessions`, `ExtractTaskID` |
| `monitor.go` | `Monitor.Check(session)` → idle / wip / unknown by hashing captured lines and counting consecutive identical hashes per session; a configured idle pattern matching the tail lines uses the lower `StableThreshold`, otherwise the hash-only fallback threshold. `Remove` drops tracking state for ended sessions |

## Conventions

- Session names are `<sanitizedProject>-<taskID>-<slug>` (`SessionName`): the slug passes through `task.Slugify` (at most 24 chars), and a slug that cleans to empty gives `<sanitizedProject>-<taskID>`. `SessionPrefix(project)` gives the project-scoped prefix for listing. Every tmux `-t` target uses the exact form (`=name`, or `=name:` for pane/window targets) so `proj-1` never resolves to `proj-10`.
- Create sites use `NewSession`; anything looking up a possibly-live session uses `FindSession`, which falls back to the legacy bare `<sanitizedProject>-<taskID>` session when only that one exists (exact `Exists()` checks, never prefix matching). `KillSessionsForTask` kills both exact names, slugged and legacy, and nothing else.
- Attaching: `SwitchClientCommand` when `IsInsideTmux()`, otherwise `AttachCommand`; `NestedAttachCommand` when the config's nested-attach behavior is `nest`.
- Prefer `{{run_agent}}` over `{{agent_command}}` in setup commands: it is the wrapper script that exports the `SAKUSEN_*` env before running the agent.
