package daemon

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/runner"
	"github.com/Bakaface/sakusen/internal/task"
	"github.com/Bakaface/sakusen/internal/tmux"
	"github.com/Bakaface/sakusen/internal/workflow"
)

// interactiveAgentSlug is the conventional slug for the interactive fallback
// agent (`sakusen init` scaffolds a tmux-mode record under this name).
const interactiveAgentSlug = "claude-tmux"

// interactiveAgent resolves the agent record used for ad-hoc interactive tmux
// sessions that have no owning workflow step: continuing a terminal task,
// tmux_direct tasks, and session restore fallbacks. Resolution: the top-level
// default agent when it is tmux-mode, else the conventional "claude-tmux"
// slug, else an error (there is nothing sensible to run interactively).
func interactiveAgent(cfg *config.Config) (string, config.AgentConfig, error) {
	slug := cfg.EffectiveDefaultAgentSlug()
	if a, ok := cfg.ResolveAgent(slug); ok && a.IsTmux() {
		return slug, a, nil
	}
	if a, ok := cfg.ResolveAgent(interactiveAgentSlug); ok && a.IsTmux() {
		return interactiveAgentSlug, a, nil
	}
	return "", config.AgentConfig{}, fmt.Errorf(
		"no interactive (tmux-mode) agent configured: define a tmux-mode agent under `agents:` (conventionally %q) or point `default_agent:` at one; run `sakusen init` in a fresh project to scaffold defaults", interactiveAgentSlug)
}

// interactiveSpawnOpts parameterizes spawnInteractiveSession.
type interactiveSpawnOpts struct {
	// label names the pseudo-step this session belongs to ("continue",
	// "tmux-direct", or a real step name on restore). It scopes the prompt
	// file, wrapper script, sentinel prefix, and chat records.
	label string
	// prompt is the initial prompt text written to SAKUSEN_PROMPT_FILE. May be
	// empty — the agent command decides what an empty prompt means.
	prompt string
	// resumeSessionID, when non-empty AND the agent has a resume_command,
	// resumes the recorded agent session instead of starting fresh.
	resumeSessionID string
	// keepSentinels preserves existing turn-end sentinels for the label
	// instead of clearing them. Set on daemon-restart restore: a sentinel that
	// landed just before the daemon died must survive so auto-advance and
	// session capture still fire.
	keepSentinels bool
	// agent + slug select the agent record to run.
	slug  string
	agent config.AgentConfig
}

// spawnInteractiveSession creates a detached tmux session running the given
// agent record for a task, with sakusen's env contract exported. It is the
// shared implementation behind the terminal-task continue fallback,
// tmux_direct task setup, and daemon-restart session restore, keeping the
// wrapper-script/session/setup-command sequence in one place for all three.
//
// Returns whether the session actually resumed a previous agent session.
func (s *Server) spawnInteractiveSession(pc *projectContext, t *task.Task, opts interactiveSpawnOpts) (resumed bool, err error) {
	if !tmux.IsAvailable() {
		return false, fmt.Errorf("tmux is not installed or not in PATH")
	}

	taskID := fmt.Sprintf("%d", t.ID)
	projectName := ""
	setupCmd := ""
	if pc != nil {
		projectName = pc.cfg.Project.Name
		setupCmd = pc.cfg.TmuxSetupCommand
	}
	session := tmux.NewSession(projectName, taskID, t.WorktreePath)
	if session.Exists() {
		session.Kill()
	}

	sakusenDir := filepath.Join(t.WorktreePath, ".sakusen")
	if err := os.MkdirAll(sakusenDir, 0755); err != nil {
		return false, fmt.Errorf("failed to create sakusen dir: %w", err)
	}

	// Ad-hoc session artifacts use a "session-"/"run-" prefix distinct from the
	// engine's step-prompt-*/run-step-* files: on a step restore the engine's
	// prompt file is the SOURCE of opts.prompt (see restoreTmuxSession), so
	// this path must never clobber it.
	label := workflow.SentinelPrefix(opts.label)
	promptFile := filepath.Join(sakusenDir, fmt.Sprintf("session-prompt-%s.txt", label))
	scriptFile := filepath.Join(sakusenDir, fmt.Sprintf("run-%s.sh", label))
	if err := os.WriteFile(promptFile, []byte(opts.prompt), 0644); err != nil {
		return false, fmt.Errorf("failed to write prompt file: %w", err)
	}

	// Sentinel contract: interactive sessions get a done-dir too so agent
	// hooks that write sentinels unconditionally don't scatter files, and so
	// the monitor can record session ids from their payloads. Stale sentinels
	// from a previous session under the same label are cleared first (unless
	// restoring, where a pre-crash sentinel must survive).
	if err := os.MkdirAll(workflow.StepDoneDir(t.WorktreePath), 0755); err != nil {
		log.Printf("%sWarning: failed to create step-done dir for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
	} else if !opts.keepSentinels {
		workflow.ClearStepSentinels(t.WorktreePath, opts.label)
	}

	repoRoot := t.WorktreePath
	if pc != nil && pc.repoRoot != "" {
		repoRoot = pc.repoRoot
	}
	env := map[string]string{
		"SAKUSEN_TASK_ID":      taskID,
		"SAKUSEN_STEP":         opts.label,
		"SAKUSEN_WORKTREE":     t.WorktreePath,
		"SAKUSEN_PROJECT_PATH": repoRoot,
		"SAKUSEN_PURPOSE":      "step",
		"SAKUSEN_AGENT":        opts.slug,
		"SAKUSEN_PROMPT_FILE":  promptFile,
		"SAKUSEN_DONE_DIR":     workflow.StepDoneDir(t.WorktreePath),
		"SAKUSEN_DONE_PREFIX":  workflow.SentinelPrefix(opts.label),
	}
	if t.TrackID != nil {
		env["SAKUSEN_TRACK_ID"] = fmt.Sprintf("%d", *t.TrackID)
	}

	command := opts.agent.Command
	if opts.resumeSessionID != "" && opts.agent.ResumeCommand != "" {
		command = opts.agent.ResumeCommand
		env["SAKUSEN_SESSION_ID"] = opts.resumeSessionID
		resumed = true
	}

	script := runner.BuildWrapperScript(command, runner.MergeEnv(env, opts.agent.Env))
	if err := os.WriteFile(scriptFile, []byte(script), 0755); err != nil {
		return false, fmt.Errorf("failed to write wrapper script: %w", err)
	}

	if tmux.SetupCommandControlsAgent(setupCmd) {
		if err := session.Create(""); err != nil {
			return false, fmt.Errorf("failed to create tmux session: %w", err)
		}
	} else {
		if err := session.Create("bash", scriptFile); err != nil {
			return false, fmt.Errorf("failed to create tmux session: %w", err)
		}
	}

	if setupCmd != "" {
		vars := &tmux.SetupVars{
			AgentCommand: command,
			RunAgent:     scriptFile,
		}
		if err := session.RunSetupCommand(setupCmd, vars); err != nil {
			log.Printf("%sWarning: tmux setup command failed for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
		}
	}

	return resumed, nil
}
