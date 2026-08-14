package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Agent execution modes. Mode lives on the agent record, not the step (per the
// agents PRD): a step selects an agent, and the agent's mode decides whether it
// runs headless (spawned subprocess, synchronous result) or inside a detached
// tmux session (interactive, pauses the workflow at an approval gate).
const (
	AgentModeHeadless = "headless"
	AgentModeTmux     = "tmux"
)

// DefaultAgentSlug is the implicit fallback agent slug used when neither the
// step, the workflow, nor the top-level `default_agent:` names one. It is NOT
// synthesized by sortie — `sortie init` scaffolds an agent record under this
// slug, but a config that never defines it simply fails at step-run time with
// an instructive error (everything that doesn't spawn an agent keeps working).
const DefaultAgentSlug = "claude"

// AgentConfig defines a user-configured agent: the shell command sortie runs
// to execute a workflow step. Agents are declared under the top-level
// `agents:` map (slug → record) in .sortie.yml / ~/.sortie.yml and referenced
// from workflows and steps via `agent: <slug>`.
//
// Commands are executed via `sh -c` in the task's working directory (worktree
// or repo root) with sortie's environment contract exported:
//
//	SORTIE_TASK_ID       task id
//	SORTIE_STEP          step name
//	SORTIE_WORKTREE      absolute path of the task workdir
//	SORTIE_PROJECT_PATH  absolute path of the project repo root
//	SORTIE_PURPOSE       "step" (or "merge_conflict" for the conflict resolver)
//	SORTIE_TRACK_ID      task's track id (only when the task is on a track)
//	SORTIE_PROMPT_FILE   file containing the fully-resolved step prompt
//
// Headless mode additionally exports:
//
//	SORTIE_RESULT_FILE   file the command (pipeline) should write the agent's
//	                     final result text to; when absent after exit, sortie
//	                     falls back to the tail of captured stdout (crude —
//	                     write the file)
//
// Tmux mode additionally exports (inside the session wrapper script):
//
//	SORTIE_DONE_DIR      directory sentinel files must be written to
//	SORTIE_DONE_PREFIX   filename prefix a sentinel for this step must use
//
// Turn-end signalling for tmux agents is the generic sentinel convention: when
// the agent finishes a unit of work, something (a hook, the agent itself, a
// userland idle-watcher) writes a file named
// "$SORTIE_DONE_DIR/$SORTIE_DONE_PREFIX-$(date +%s%N).json". The file's
// content MAY be a JSON object carrying "session_id" (an opaque, agent-native
// session identifier recorded for resume + chat-log lookup) and
// "transcript_path" (a chat transcript file). Agents that never write a
// sentinel are manual-advance: the task pauses until the user advances it.
type AgentConfig struct {
	// Mode is "headless" (default when empty) or "tmux".
	Mode string `yaml:"mode,omitempty"`

	// Command is the shell command that runs the agent (required).
	Command string `yaml:"command"`

	// ResumeCommand (tmux only), when non-empty, marks the agent resumable
	// after a daemon restart: instead of Command, sortie runs this with
	// SORTIE_SESSION_ID set to the recorded session id (captured from a
	// sentinel payload's session_id). Empty → restored sessions start fresh.
	ResumeCommand string `yaml:"resume_command,omitempty"`

	// ChatLogCommand (tmux only), when non-empty, is run to obtain the step's
	// chat log for the summarize_chat strategy: it must print the
	// conversation log on stdout. Env: SORTIE_SESSION_ID (recorded session id,
	// may be empty), SORTIE_TRANSCRIPT_PATH and SORTIE_SENTINEL_FILE (from the
	// step's most recent sentinel, when present). Empty → tmux steps of this
	// agent cannot capture summarize_chat context (they degrade to no context,
	// or fail when the step sets require_context: true).
	ChatLogCommand string `yaml:"chat_log_command,omitempty"`

	// Env holds extra environment variables exported to every spawn of this
	// agent (Command, ResumeCommand, and ChatLogCommand alike).
	Env map[string]string `yaml:"env,omitempty"`
}

// EffectiveMode returns the agent's mode, defaulting to headless when unset.
func (a *AgentConfig) EffectiveMode() string {
	if a.Mode == "" {
		return AgentModeHeadless
	}
	return a.Mode
}

// IsTmux reports whether the agent runs in tmux mode.
func (a *AgentConfig) IsTmux() bool {
	return a.EffectiveMode() == AgentModeTmux
}

// SummarizerConfig configures the utility LLM command sortie shells out to for
// text-in/text-out work: chat/step summarization, the final task summarizer,
// AI title generation, and `sortie backfill-context`. The command is executed
// via `sh -c` with the prompt piped on STDIN and must print the response on
// stdout. SORTIE_PURPOSE identifies the call site ("summarize",
// "summarize_chat", "summarize_chat_chunk", "title", "backfill_context").
//
// When no summarizer is configured, everything degrades gracefully: titles
// fall back to a truncated task input, summarize passes are skipped with a
// warning (or fail the task when a step sets require_context: true), and
// backfill-context errors out with an instructive message.
type SummarizerConfig struct {
	Command string `yaml:"command"`

	// MaxPromptBytes, when > 0, bounds the prompt size for a single
	// invocation: larger chat logs are summarized map-reduce style (split on
	// line boundaries into chunks below the ceiling, each chunk summarized,
	// then the chunk summaries reduced). 0 disables chunking.
	MaxPromptBytes int `yaml:"max_prompt_bytes,omitempty"`
}

// Configured reports whether a summarizer command is set.
func (s *SummarizerConfig) Configured() bool {
	return strings.TrimSpace(s.Command) != ""
}

// ResolveAgent looks up an agent record by slug in the merged registry.
func (c *Config) ResolveAgent(slug string) (AgentConfig, bool) {
	a, ok := c.Agents[slug]
	return a, ok
}

// EffectiveDefaultAgentSlug returns the top-level `default_agent:` slug, or
// DefaultAgentSlug when unset.
func (c *Config) EffectiveDefaultAgentSlug() string {
	if c.DefaultAgent != "" {
		return c.DefaultAgent
	}
	return DefaultAgentSlug
}

// StepAgentSlug resolves the agent slug for a step following the cascade:
// step.agent → workflow.agent → top-level default_agent → "claude".
func (c *Config) StepAgentSlug(wf *WorkflowConfig, step *StepConfig) string {
	if step != nil && step.Agent != "" {
		return step.Agent
	}
	return c.WorkflowAgentSlug(wf)
}

// WorkflowAgentSlug resolves the workflow-level agent slug (workflow.agent →
// top-level default_agent → "claude"). This is the slug a step inherits when
// it doesn't set its own.
func (c *Config) WorkflowAgentSlug(wf *WorkflowConfig) string {
	if wf != nil && wf.Agent != "" {
		return wf.Agent
	}
	return c.EffectiveDefaultAgentSlug()
}

// StepAgent resolves the agent record that runs a step. Returns the resolved
// slug and record, or an error when the slug has no record in the registry —
// explicit refs are rejected at config load, so in practice this error means
// "nothing configured at all" and carries the `sortie init` hint.
func (c *Config) StepAgent(wf *WorkflowConfig, step *StepConfig) (string, AgentConfig, error) {
	slug := c.StepAgentSlug(wf, step)
	agent, ok := c.ResolveAgent(slug)
	if !ok {
		return slug, AgentConfig{}, fmt.Errorf(
			"no agent %q configured: define it under `agents:` in .sortie.yml (or set `agent:`/`default_agent:` to an existing slug); run `sortie init` in a fresh project to scaffold defaults", slug)
	}
	return slug, agent, nil
}

// StepIsTmux reports whether a step resolves to a tmux-mode agent. An
// unresolvable slug reports false (the step will fail with a clear error at
// run time anyway).
func (c *Config) StepIsTmux(wf *WorkflowConfig, step *StepConfig) bool {
	_, agent, err := c.StepAgent(wf, step)
	if err != nil {
		return false
	}
	return agent.IsTmux()
}

// FirstStepIsTmux returns true when the workflow has at least one step and
// that step resolves to a tmux-mode agent. Tmux-first workflows may be started
// without an input since the user drives the session interactively.
func (c *Config) FirstStepIsTmux(wf *WorkflowConfig) bool {
	if wf == nil || len(wf.Steps) == 0 {
		return false
	}
	return c.StepIsTmux(wf, &wf.Steps[0])
}

// validateAgents validates the merged agent registry and every agent
// reference reachable from the resolved config: record shape (mode, command,
// slug format, tmux-only fields), explicit refs (default_agent, workflow
// agent:, step agent:), loop-step mode constraints, and the removed
// {{claude_command}} tmux-setup-command variable. Called by Load /
// LoadForProject after workflows and tiers are fully merged.
func validateAgents(cfg *Config) error {
	if err := validateAgentRecords(cfg.Agents); err != nil {
		return err
	}
	return validateAgentRefs(cfg)
}

// validateAgentRecords checks every agent record's shape: slug format, mode,
// required command, and tmux-only fields. Shared by the full load-time
// validation (validateAgents) and single-file diagnosis (validateProject).
func validateAgentRecords(agents map[string]AgentConfig) error {
	for slug, a := range agents {
		if !validKebabCaseName.MatchString(slug) {
			return fmt.Errorf("agents: invalid agent slug %q (must be kebab-case: [a-z0-9-]+)", slug)
		}
		switch a.EffectiveMode() {
		case AgentModeHeadless, AgentModeTmux:
		default:
			return fmt.Errorf("agent %q: invalid mode %q (must be %q or %q)", slug, a.Mode, AgentModeHeadless, AgentModeTmux)
		}
		if strings.TrimSpace(a.Command) == "" {
			return fmt.Errorf("agent %q: command is required", slug)
		}
		if !a.IsTmux() {
			if a.ResumeCommand != "" {
				return fmt.Errorf("agent %q: resume_command is only valid for tmux-mode agents", slug)
			}
			if a.ChatLogCommand != "" {
				return fmt.Errorf("agent %q: chat_log_command is only valid for tmux-mode agents", slug)
			}
		}
	}
	return nil
}

// validateAgentRefs checks agent references against the merged registry.
func validateAgentRefs(cfg *Config) error {
	// Explicit refs must resolve. The implicit "claude" fallback is exempt so
	// a config with no agents at all still loads (task CRUD, TUI, etc. keep
	// working); running a step then fails with the instructive StepAgent error.
	checkRef := func(slug, where string) error {
		if slug == "" {
			return nil
		}
		if _, ok := cfg.Agents[slug]; !ok {
			return fmt.Errorf("%s: unknown agent %q (no such slug under `agents:`)", where, slug)
		}
		return nil
	}
	if err := checkRef(cfg.DefaultAgent, "default_agent"); err != nil {
		return err
	}
	for i := range cfg.Workflows {
		wf := &cfg.Workflows[i]
		if err := checkRef(wf.Agent, fmt.Sprintf("workflow %q", wf.Name)); err != nil {
			return err
		}
		for j := range wf.Steps {
			step := &wf.Steps[j]
			if err := checkRef(step.Agent, fmt.Sprintf("workflow %q step %q", wf.Name, step.Name)); err != nil {
				return err
			}
			// Loop steps must run headless: a tmux step pauses the engine, so
			// a loop over it could never iterate. Only enforceable when the
			// slug resolves; an unresolvable implicit slug fails at run time.
			if step.Loop != nil {
				if agent, ok := cfg.ResolveAgent(cfg.StepAgentSlug(wf, step)); ok && agent.IsTmux() {
					return fmt.Errorf("workflow %q step %q: loop steps cannot use a tmux-mode agent", wf.Name, step.Name)
				}
			}
		}
	}

	if strings.Contains(cfg.TmuxSetupCommand, "{{claude_command}}") {
		return fmt.Errorf("tmux-setup-command: template variable {{claude_command}} was removed — use {{agent_command}} (the resolved agent command) or {{run_agent}} (the wrapper script)")
	}
	for i := range cfg.Workflows {
		if strings.Contains(cfg.Workflows[i].TmuxSetupCommand, "{{claude_command}}") {
			return fmt.Errorf("workflow %q tmux-setup-command: template variable {{claude_command}} was removed — use {{agent_command}} or {{run_agent}}", cfg.Workflows[i].Name)
		}
	}

	return nil
}

// mergeAgents overlays the src agent map onto dst per slug: a slug redefined
// in a more-local tier wins wholesale (records are not field-merged).
func mergeAgents(dst map[string]AgentConfig, src map[string]AgentConfig) map[string]AgentConfig {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[string]AgentConfig, len(src))
	}
	for slug, a := range src {
		dst[slug] = a
	}
	return dst
}

// removedProjectKeys maps removed .sortie.yml top-level keys to their
// migration errors. Hard load-time errors, no back-compat mapping.
var removedProjectKeys = map[string]string{
	"claude":                       "the `claude:` block was removed: define agents under the top-level `agents:` map instead (run `sortie init` in a fresh project for scaffolded claude agent records)",
	"yolo":                         "`yolo:` was removed with the `claude:` block: put permission flags (e.g. --dangerously-skip-permissions) directly in your agent's `command`",
	"system_prompt":                "`system_prompt:` was removed: bake system-prompt flags into your agent's `command` (e.g. claude --append-system-prompt \"...\") or fold the text into step prompts",
	"allowed_summarization_models": "`allowed_summarization_models` was removed: summarization now runs the top-level `summarizer:` command; pick the model inside that command",
}

// checkRemovedProjectKeys scans the top level of a config document for removed
// keys and returns their migration errors. Applied to both .sortie.yml /
// ~/.sortie.yml and the global ~/.config/sortie/config.yaml (whose old shape
// also carried `claude:`/`yolo:`). Works on the raw bytes so the friendly
// message fires even though yaml decoding would silently drop the unknown key.
func checkRemovedProjectKeys(data []byte) error {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil // surface the parse error from the main decode path
	}
	root := &node
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i]
		if key.Kind != yaml.ScalarNode {
			continue
		}
		if msg, removed := removedProjectKeys[key.Value]; removed {
			return fmt.Errorf("%s", msg)
		}
	}
	return nil
}
