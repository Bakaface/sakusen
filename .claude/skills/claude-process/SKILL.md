---
name: claude-process
description: >
  Agent process spawning (internal/runner), output handling, agent state
  machine, and concurrency control. Use when editing files in internal/runner/ or
  internal/agent/, working on agent command execution, the env contract
  (SORTIE_PROMPT_FILE/SORTIE_RESULT_FILE), agent state transitions, or the
  concurrent agent manager.
---

# Runner & Agent Management

## Process Spawning (internal/runner/)

The runner is the single seam between sortie's execution engine and whatever tool actually
runs a step. Every agent is a shell command (see `config.AgentConfig`) executed via `sh -c`
in the task workdir with sortie's env contract exported. The runner knows nothing about any
specific agent CLI — no output parsing, no session discovery, no turn-end signalling.

`Process` runs one headless agent command.

```go
type Process struct {
    TaskID, WorkDir string
    OutputFile string              // raw stdout+stderr capture (OutputLogFileName in WorkDir)
    ResultFile string              // where the agent pipeline writes the final result text
    OutputFunc func(lines []string)
    // internal: command, cmd, env, stdoutTail, exitCode, exited, exitErr
}

NewProcess(taskID, workDir, command, resultFile string) *Process
SetEnv(env map[string]string)   // Set env vars before Start (KEY=VALUE over os.Environ())
Start() error                   // sh -c <command> in WorkDir; streams stdout line-by-line
Stop() error                    // SIGTERM -> 5s grace -> SIGKILL
IsRunning() / HasExited() / IsSuccess() bool
ExitCode() int                  // -1 while running
PID() int
ResultText() string             // $SORTIE_RESULT_FILE content; stdout-tail fallback (after exit)
```

- `OutputLogFileName` = `".sortie-output.log"` — per-workdir raw output capture (stdout
  verbatim + stderr). The daemon's `noiseFiles` list references it.
- Stdout lines are forwarded to `OutputFunc` with `[HH:MM:SS]` timestamps and retained in a
  bounded tail (`stdoutTailLines` = 50) as the crude `ResultText` fallback for pipelines that
  never write `$SORTIE_RESULT_FILE`.
- `buildEnv()` filters `CLAUDECODE=` out of the child environment so claude-based agent
  commands don't refuse to launch inside a Claude Code session; harmless for other tools.

### RunSync (sync.go)

```go
RunSync(ctx context.Context, command, workDir string, env map[string]string, stdin string) (string, error)
```

Synchronous `sh -c` with stdin piped, stdout returned. Used for the configured `summarizer:`
command (chat/step/task summaries, AI titles, backfill-context — tagged via `SORTIE_PURPOSE`)
and agent helper commands (`chat_log_command`). Also strips `CLAUDECODE`. On failure the
error carries truncated stdout AND stderr (some tools print errors to stdout).

### Wrapper Scripts (script.go)

```go
BuildWrapperScript(command string, env map[string]string) string
MergeEnv(contract, agentEnv map[string]string) map[string]string
```

`BuildWrapperScript` renders the bash script run inside a tmux session: exports env (sorted
keys — deterministic across retries/restores), runs the command, then `exec bash` so the pane
survives for inspection. `MergeEnv` folds an agent record's `env:` into the per-spawn
contract; the contract wins on collisions so agents cannot mask `SORTIE_*` variables.

### Tests

`process_test.go` is integration-tagged (`//go:build integration`) — run with `mise run ti`,
not plain `go test ./...`. `script_test.go` (BuildWrapperScript quoting/determinism, MergeEnv
contract-wins) runs as a plain unit test.

## Agent Management (internal/agent/)

### Agent States

```
pending -> starting -> running -+-> completed
                                +-> failed
                                +-> stopped
              \-> waiting_for_input (from running)
```

- `State.IsTerminal()` — completed, failed, stopped
- `State.IsActive()` — starting, running, waiting_for_input

### Agent Struct

```go
type Agent struct {
    ID          string
    Task        *task.Task
    WorkDir     string
    State       State
    PID         int          // Process ID of the spawned agent command
    StartedAt   time.Time
    EndedAt     time.Time
    Error       string
    CurrentStep string
    StepIndex   int
    // internal: outputBuffer *RingBuffer
}

New(t *task.Task, bufferSize int) *Agent
Duration() time.Duration          // EndedAt - StartedAt (or now - StartedAt)
GetState() / SetState(State)
SetError(string) / SetPID(int) / GetPID() int / SetWorkDir(string)
AppendOutput([]string) / GetOutput(fromLine int) / GetAllOutput()
```

### Manager

```go
var (
    ErrTaskAlreadyTracked = errors.New("task already tracked")
    ErrAgentNotFound      = errors.New("agent not found")
    ErrNoWorkDir          = errors.New("task has no workdir")
)

NewManager(maxConcurrent, bufferSize int) *Manager
SetStateChangeCallback(cb StateChangeCallback)
StartAgent(t *task.Task, workDir string, runner func(ctx context.Context) error) (*Agent, error)
StopAgent(agentID string) error
GetAgent(agentID string) (*Agent, bool)
GetAgentByTaskID(taskID int64) (*Agent, bool)
ListAgents() []*Agent
IsTaskKnown(taskID int64) bool
Shutdown(gracePeriod time.Duration)
GetOutput(agentID string, fromLine int) ([]string, int, error)
```

- Enforces `maxConcurrent` limit; excess agents queued
- `OnStateChange` callback fires outside mutex (deadlock prevention)
- Queue processing in `processQueueLocked()` after agent completes/stops

### RingBuffer

Fixed-size circular buffer for streaming output: `Append(lines)`, `GetFrom(fromLine)`, `GetAll()`. Supports incremental consumption for live TUI updates.

## Data Flow

Agent command stdout -> Process.OutputFunc -> Agent.outputBuffer -> TUI via `get_output`

## Patterns

- State transitions go through Manager methods, not direct field assignment
- `OnStateChange` is the primary integration point with daemon
- Check `HasExited()` before reading `ExitCode()`
- Env vars (`SORTIE_TASK_ID`, `SORTIE_PROMPT_FILE`, `SORTIE_RESULT_FILE`, etc.) set via
  `process.SetEnv()` before start — the engine builds the contract, the runner just exports it
