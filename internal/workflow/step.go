package workflow

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/runner"
	"github.com/Bakaface/sakusen/internal/task"
	"github.com/Bakaface/sakusen/internal/tmux"
)

// StepPromptFile returns the path the fully-resolved step prompt is written to
// before an agent spawn. Exported to the agent via SAKUSEN_PROMPT_FILE, and used
// by the daemon to re-feed the prompt when restoring a step session.
func StepPromptFile(worktreePath, stepName string) string {
	return filepath.Join(worktreePath, ".sakusen", fmt.Sprintf("step-prompt-%s.txt", stepName))
}

// stepResultFile returns the path a headless agent's pipeline is expected to
// write its final result text to. Exported via SAKUSEN_RESULT_FILE and read
// back after exit (with a crude stdout-tail fallback when absent).
func stepResultFile(worktreePath, stepName string) string {
	return filepath.Join(worktreePath, ".sakusen", fmt.Sprintf("step-result-%s.txt", stepName))
}

// agentRunner is the AGENT-RUNNER seam between the engine's step-execution
// driver (runStep, engine.go) and how a headless step's agent command
// actually runs. runStep calls e.runner.runHeadlessStep instead of spawning a
// runner.Process directly, so tests can substitute a fake that returns
// scripted results without spawning a real subprocess. NewEngine sets
// e.runner to realAgentRunner{} by default; tests in this package override
// the field directly after construction (the same pattern already used for
// e.repo in fasttrack_test.go — see Engine.runner's doc comment).
//
// The tmux path (runStepTmux) is deliberately NOT behind this seam.
// Tmux steps don't return a synchronous outcome the way headless steps do —
// they spawn a detached session and return immediately (exitCode 0, no
// resultText) so the engine can pause at the approval gate; the actual
// result only becomes known later via ResumeAfterApproval /
// summarizePreviousTmuxStep. There's no meaningful "scripted result" for a
// fake to return that runStep doesn't already synthesize inline for that
// path (see the useTmux branch in runStep), so seaming it would add
// indirection without buying any new coverage. Headless is also where
// essentially all of the exercisable workflow logic lives (loop evaluation,
// step-context capture, no-output validation, waits-on suspension) —
// seaming it alone is enough to exercise that logic in-process without a
// real agent binary.
type agentRunner interface {
	runHeadlessStep(ctx context.Context, e *Engine, t *task.Task, step config.StepConfig, agent config.AgentConfig, prompt string, envVars map[string]string, outputFn func([]string), spawn headlessSpawn) (exitCode int, resultText, outputTail string, err error)
}

// headlessSpawn overrides the two per-spawn file paths a headless step would
// otherwise derive from the task alone. The ZERO VALUE means today's defaults
// (the unified task log, and runner.Process's per-workdir raw capture), which
// is what every ordinary step passes.
//
// Parallel branches must override both: several agents run concurrently in one
// worktree, and each would otherwise append into the same task.log region (the
// step markers stepAgentOutput slices on would interleave) and truncate the
// same raw-capture file on start.
type headlessSpawn struct {
	// LogPath replaces the unified task log as the file the agent's header,
	// echoed prompt, streamed stdout and footer are appended to. The layout is
	// identical either way, so stepAgentOutput / extractLatestStepRegion work
	// on it unchanged.
	LogPath string
	// OutputFile replaces runner.Process's raw stdout+stderr capture path.
	OutputFile string
	// Tag, when non-empty, is inserted after the timestamp of every line
	// forwarded to outputFn (the live ring buffer the TUI reads), so
	// concurrently-streaming branches stay attributable: "[14:02:11] [review-opus] ...".
	Tag string
}

// realAgentRunner is the production agentRunner: it delegates to
// Engine.runHeadlessAgent, which spawns a real runner.Process. This is the
// default runner NewEngine wires up.
type realAgentRunner struct{}

func (realAgentRunner) runHeadlessStep(ctx context.Context, e *Engine, t *task.Task, step config.StepConfig, agent config.AgentConfig, prompt string, envVars map[string]string, outputFn func([]string), spawn headlessSpawn) (int, string, string, error) {
	return e.runHeadlessAgent(ctx, t, step, agent, prompt, envVars, outputFn, spawn)
}

// runHeadlessAgent executes a step's headless agent command synchronously:
// writes the resolved prompt to the step prompt file, spawns the agent's
// shell command via runner.Process with the sakusen env contract exported,
// streams its stdout into the unified task log, and returns the exit code,
// result text (from SAKUSEN_RESULT_FILE, stdout-tail fallback), and — on
// failure — a tail of the step log for diagnostics.
func (e *Engine) runHeadlessAgent(ctx context.Context, t *task.Task, step config.StepConfig, agent config.AgentConfig, prompt string, envVars map[string]string, outputFn func([]string), spawn headlessSpawn) (int, string, string, error) {
	sakusenDir := filepath.Join(t.WorktreePath, ".sakusen")
	if err := os.MkdirAll(sakusenDir, 0755); err != nil {
		return 1, "", "", fmt.Errorf("failed to create sakusen dir: %w", err)
	}
	promptFile := StepPromptFile(t.WorktreePath, step.Name)
	resultFile := stepResultFile(t.WorktreePath, step.Name)
	if err := os.WriteFile(promptFile, []byte(prompt), 0644); err != nil {
		return 1, "", "", fmt.Errorf("failed to write prompt file: %w", err)
	}
	// Clear any stale result from a previous pass of this step so the
	// fallback logic can't pick up a previous iteration's output.
	_ = os.Remove(resultFile)

	env := runner.MergeEnv(envVars, agent.Env)
	env["SAKUSEN_PROMPT_FILE"] = promptFile
	env["SAKUSEN_RESULT_FILE"] = resultFile

	proc := runner.NewProcess(fmt.Sprintf("%d", t.ID), t.WorktreePath, agent.Command, resultFile)
	if spawn.OutputFile != "" {
		if err := os.MkdirAll(filepath.Dir(spawn.OutputFile), 0755); err != nil {
			return 1, "", "", fmt.Errorf("failed to create output capture dir: %w", err)
		}
		proc.OutputFile = spawn.OutputFile
	}

	// Apply step timeout
	timeout := e.cfg.GetStepTimeout(step)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Open the unified task log file. Every step and the finalization phase append
	// into this single file so the on-disk order matches the chronological order
	// of events.
	logPath := ProjectLogPath(e.dataDir, t.ID)
	if spawn.LogPath != "" {
		logPath = spawn.LogPath
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return 1, "", "", fmt.Errorf("failed to create log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return 1, "", "", fmt.Errorf("failed to open task log: %w", err)
	}
	defer logFile.Close()

	// Write step header and prompt to log file and outputFn
	iterSuffix := ""
	if t.LoopIteration > 0 {
		iterSuffix = fmt.Sprintf(" [iteration %d]", t.LoopIteration)
	}
	header := fmt.Sprintf("[%s] === Step: %s (task #%d)%s ===",
		time.Now().Format("15:04:05"), step.Name, t.ID, iterSuffix)
	promptHeader := fmt.Sprintf("[%s] Prompt:", time.Now().Format("15:04:05"))
	var promptLines []string
	promptLines = append(promptLines, header)
	promptLines = append(promptLines, promptHeader)
	for _, line := range strings.Split(prompt, "\n") {
		promptLines = append(promptLines, fmt.Sprintf("[%s]   %s", time.Now().Format("15:04:05"), line))
	}
	promptLines = append(promptLines, "")

	for _, line := range promptLines {
		logFile.WriteString(line + "\n")
	}
	// The log file gets untagged lines (its region layout is what
	// stepAgentOutput parses); only the shared live stream carries the tag.
	emit := func(lines []string) {
		if outputFn == nil {
			return
		}
		outputFn(tagLines(lines, spawn.Tag))
	}
	emit(promptLines)

	// Compose OutputFunc: write to log file AND call the agent's outputFn
	var logMu sync.Mutex
	proc.OutputFunc = func(lines []string) {
		logMu.Lock()
		for _, line := range lines {
			logFile.WriteString(line + "\n")
		}
		logMu.Unlock()

		emit(lines)
	}

	// Set environment on the child process (not the daemon's global env)
	proc.SetEnv(env)

	if err := proc.Start(); err != nil {
		return 1, "", "", fmt.Errorf("failed to start agent: %w", err)
	}

	// Wait for process to exit
	ticker := time.NewTicker(processExitPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			proc.Stop()
			return 1, "", "", ctx.Err()
		case <-ticker.C:
			if proc.HasExited() {
				exitCode := proc.ExitCode()
				resultText := proc.ResultText()

				// Write step footer
				footer := fmt.Sprintf("[%s] === Step %s finished (exit=%d) ===",
					time.Now().Format("15:04:05"), step.Name, exitCode)
				logMu.Lock()
				logFile.WriteString(footer + "\n")
				logMu.Unlock()
				emit([]string{footer})

				var outputTail string
				if exitCode != 0 {
					// Read last 20 lines from the per-step log
					if lines, err := readLastLines(logPath, 20); err == nil && len(lines) > 0 {
						outputTail = strings.Join(lines, "\n")
					}
				}
				return exitCode, resultText, outputTail, nil
			}
		}
	}
}

// tagLines inserts "[tag] " after the "[HH:MM:SS] " timestamp prefix of every
// line, so lines from concurrently-running parallel branches stay attributable
// in the shared live output stream. An empty tag returns lines unchanged (the
// slice itself, since ordinary steps never need a copy). Lines that carry no
// timestamp prefix get the tag prepended.
func tagLines(lines []string, tag string) []string {
	if tag == "" {
		return lines
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		if len(line) > 0 && line[0] == '[' {
			if end := strings.Index(line, "] "); end > 0 {
				out[i] = line[:end+2] + "[" + tag + "] " + line[end+2:]
				continue
			}
		}
		out[i] = "[" + tag + "] " + line
	}
	return out
}

// readLastLines reads the last n lines from a file.
func readLastLines(path string, n int) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB buffer for long lines
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// readLogTail reads the last n lines from a log file.
// Returns empty string if the file doesn't exist or can't be read.
func readLogTail(path string, maxLines int) string {
	lines, err := readLastLines(path, maxLines)
	if err != nil || len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// runStepTmux starts a step's tmux-mode agent in a detached tmux session and
// returns immediately. The tmux session persists for the user to attach and
// interact with. The workflow engine treats tmux steps as human steps, so the
// task will pause at tmux status until the step's sentinel lands (see
// sentinel.go) or the user manually advances.
func (e *Engine) runStepTmux(ctx context.Context, t *task.Task, step config.StepConfig, agent config.AgentConfig, prompt string, envVars map[string]string, outputFn func([]string)) (int, string, error) {
	if !tmux.IsAvailable() {
		return 1, "", fmt.Errorf("tmux is not installed or not in PATH (required for tmux-mode agents)")
	}

	taskID := fmt.Sprintf("%d", t.ID)
	session := tmux.NewSession(e.cfg.ProjectName, taskID, t.WorktreePath)

	// Kill stale session if exists (handles retries)
	if session.Exists() {
		session.Kill()
	}

	sakusenDir := filepath.Join(t.WorktreePath, ".sakusen")
	promptFile := StepPromptFile(t.WorktreePath, step.Name)
	scriptFile := filepath.Join(sakusenDir, fmt.Sprintf("run-step-%s.sh", step.Name))
	logPath := ProjectLogPath(e.dataDir, t.ID)
	if err := os.MkdirAll(ProjectLogsDir(e.dataDir, t.ID), 0755); err != nil {
		return 1, "", fmt.Errorf("failed to create log dir: %w", err)
	}

	// Ensure the sentinel directory exists so agents (hooks, idle-watchers)
	// can write turn-end sentinels without having to mkdir themselves.
	if err := os.MkdirAll(StepDoneDir(t.WorktreePath), 0755); err != nil {
		log.Printf("Warning: failed to create step-done dir for task #%d: %v", t.ID, err)
	}

	// Clear any sentinels left from a previous pass of THIS step.
	// Without this, a stale turn-end marker (e.g. from a per-step retry, or one
	// that survived a daemon restart) would let the monitor auto-advance the
	// freshly-launched session before its agent does any work — handing the next
	// step an empty context. Scoped to the step name so concurrent or earlier
	// steps in the same worktree are untouched.
	ClearStepSentinels(t.WorktreePath, step.Name)

	// Write prompt to file (avoids shell quoting issues)
	if err := os.WriteFile(promptFile, []byte(prompt), 0644); err != nil {
		return 1, "", fmt.Errorf("failed to write prompt file: %w", err)
	}

	// Build the env the agent command sees inside the wrapper script: the
	// engine's per-step contract, the agent record's extra env, and the
	// tmux-specific additions (prompt file + sentinel contract).
	env := runner.MergeEnv(envVars, agent.Env)
	env["SAKUSEN_PROMPT_FILE"] = promptFile
	env["SAKUSEN_DONE_DIR"] = StepDoneDir(t.WorktreePath)
	env["SAKUSEN_DONE_PREFIX"] = SentinelPrefix(step.Name)

	script := runner.BuildWrapperScript(agent.Command, env)
	if err := os.WriteFile(scriptFile, []byte(script), 0755); err != nil {
		return 1, "", fmt.Errorf("failed to write wrapper script: %w", err)
	}

	// If the setup command contains {{run_agent}} or {{agent_command}}, the user
	// controls which window/pane runs the agent — create a bare session instead
	// of auto-starting the agent in window 0.
	setupCmd := e.cfg.TmuxSetupCommand
	if tmux.SetupCommandControlsAgent(setupCmd) {
		// Create bare session (just a shell), setup command will place the agent
		if err := session.Create(""); err != nil {
			return 1, "", fmt.Errorf("failed to create tmux session: %w", err)
		}
	} else {
		// Default: create session running the wrapper script in window 0
		if err := session.Create("bash", scriptFile); err != nil {
			return 1, "", fmt.Errorf("failed to create tmux session: %w", err)
		}
	}

	// Run tmux setup command if configured (e.g. create additional windows/panes)
	if setupCmd != "" {
		vars := &tmux.SetupVars{
			AgentCommand: agent.Command,
			RunAgent:     scriptFile,
		}
		if err := session.RunSetupCommand(setupCmd, vars); err != nil {
			log.Printf("Warning: tmux setup command failed: %v", err)
		}
	}

	// Write a clean log message instead of piping raw TUI output via pipe-pane
	logLines := writeTmuxLogMessage(logPath, t.ID, step.Name, session.Name, taskID)
	if outputFn != nil {
		outputFn(logLines)
	}

	log.Printf("Tmux session %q started for task #%d step %q (attach with: sakusen attach %s)",
		session.Name, t.ID, step.Name, taskID)

	// Session-id discovery is sentinel-driven: when the agent's turn-end
	// sentinel lands with a session_id payload, the daemon's monitor records
	// it via RecordTmuxStepSentinelSession. No launch-time discovery runs.

	// Fire-and-forget: return immediately, workflow will pause at approval gate
	return 0, "", nil
}

// writeTmuxLogMessage appends a clean status message to the unified task log for tmux
// steps, replacing the raw TUI output that pipe-pane would capture. Append (rather than
// truncate) so restarts and retries of the tmux step preserve prior history alongside
// the new session marker.
func writeTmuxLogMessage(logPath string, taskID int64, stepName, sessionName, taskIDStr string) []string {
	ts := time.Now().Format("15:04:05")
	lines := []string{
		fmt.Sprintf("[%s] === Step: %s (task #%d) ===", ts, stepName, taskID),
		fmt.Sprintf("[%s] Tmux session %q initiated", ts, sessionName),
		fmt.Sprintf("[%s] Attach with: sakusen attach %s", ts, taskIDStr),
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("Warning: failed to write tmux log message: %v", err)
		return lines
	}
	defer logFile.Close()

	for _, line := range lines {
		logFile.WriteString(line + "\n")
	}

	return lines
}
