package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeGlobalConfigYaml writes a global config.yaml under an isolated
// XDG_CONFIG_HOME (with HOME pointed at a separate temp dir) at the exact
// path getGlobalConfigPath computes, and returns (xdgDir, homeDir, path).
func writeGlobalConfigYaml(t *testing.T, yml string) (string, string, string) {
	t.Helper()
	xdgDir := t.TempDir()
	homeDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgDir)
	t.Setenv("HOME", homeDir)

	want := filepath.Join(xdgDir, "sakusen", "config.yaml")
	if got := getGlobalConfigPath(); got != want {
		t.Fatalf("getGlobalConfigPath() = %q, want %q", got, want)
	}
	if err := os.MkdirAll(filepath.Dir(want), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte(yml), 0644); err != nil {
		t.Fatal(err)
	}
	return xdgDir, homeDir, want
}

// TestAgentsGlobalConfigYamlTier verifies agents:/default_agent:/summarizer:
// defined in the XDG global config file ($XDG_CONFIG_HOME/sakusen/config.yaml)
// reach the merged Config via LoadForProject when the project defines none of
// its own.
func TestAgentsGlobalConfigYamlTier(t *testing.T) {
	writeGlobalConfigYaml(t, `
agents:
  xdg-agent:
    mode: tmux
    command: "xdg cmd"
    resume_command: "xdg resume"
default_agent: xdg-agent
summarizer:
  command: "xdg summarize"
  max_prompt_bytes: 512
`)

	projectDir := t.TempDir()
	projectPath := filepath.Join(projectDir, ".sakusen.yml")
	if err := os.WriteFile(projectPath, []byte("max_workers: 2\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}

	agent, ok := cfg.ResolveAgent("xdg-agent")
	if !ok {
		t.Fatal("expected xdg-agent from the global config.yaml tier in the merged registry")
	}
	if agent.Command != "xdg cmd" || agent.ResumeCommand != "xdg resume" || !agent.IsTmux() {
		t.Errorf("xdg-agent not parsed from config.yaml: %+v", agent)
	}
	if cfg.DefaultAgent != "xdg-agent" {
		t.Errorf("DefaultAgent = %q, want xdg-agent from config.yaml", cfg.DefaultAgent)
	}
	if cfg.Summarizer.Command != "xdg summarize" || cfg.Summarizer.MaxPromptBytes != 512 {
		t.Errorf("summarizer not merged from config.yaml: %+v", cfg.Summarizer)
	}
	if !cfg.Summarizer.Configured() {
		t.Error("Summarizer.Configured() = false, want true")
	}
}

// TestAgentsGlobalSakusenYmlOverridesConfigYaml verifies the load order of the
// two global tiers: a slug defined in both config.yaml and ~/.sakusen.yml is
// taken wholesale from ~/.sakusen.yml (which loads after config.yaml) — no
// field merging (the config.yaml record's tmux-only resume_command must not
// survive on the replacing headless record, or validation would reject it).
func TestAgentsGlobalSakusenYmlOverridesConfigYaml(t *testing.T) {
	_, homeDir, _ := writeGlobalConfigYaml(t, `
agents:
  shared:
    mode: tmux
    command: "config-yaml shared"
    resume_command: "config-yaml resume"
  config-only:
    command: "config-yaml only"
`)

	globalSakusenYml := `
agents:
  shared:
    command: "sakusen-yml shared"
`
	if err := os.WriteFile(filepath.Join(homeDir, ".sakusen.yml"), []byte(globalSakusenYml), 0644); err != nil {
		t.Fatal(err)
	}

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, ".sakusen.yml"), []byte("max_workers: 2\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}

	shared, ok := cfg.ResolveAgent("shared")
	if !ok {
		t.Fatal("expected shared agent in merged registry")
	}
	if shared.Command != "sakusen-yml shared" {
		t.Errorf("shared.Command = %q, want ~/.sakusen.yml tier to win over config.yaml", shared.Command)
	}
	if shared.IsTmux() || shared.ResumeCommand != "" {
		t.Errorf("shared must be replaced wholesale, got %+v", shared)
	}

	// Slugs unique to config.yaml survive the merge.
	if _, ok := cfg.ResolveAgent("config-only"); !ok {
		t.Error("expected config-only agent from config.yaml to survive the merge")
	}
}

// TestAgentsCrossTierRefResolution verifies the rationale for running
// validateAgents only after every tier merges: a project workflow may
// reference an agent defined solely in the global ~/.sakusen.yml, and
// LoadForProject must both accept the ref and resolve it via StepAgent.
func TestAgentsCrossTierRefResolution(t *testing.T) {
	globalYml := `
agents:
  global-worker:
    command: "global worker cmd"
`
	projectYml := `
workflows:
  - name: w
    agent: global-worker
    steps:
      - name: s
        prompt: p
`
	projectDir := setupGlobalAndProject(t, globalYml, nil, projectYml, nil)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject must accept a ref to a global-tier agent, got: %v", err)
	}

	wf := cfg.GetTaskWorkflow("w")
	if wf == nil {
		t.Fatal("workflow w not found")
	}
	slug, agent, err := cfg.StepAgent(wf, &wf.Steps[0])
	if err != nil {
		t.Fatalf("StepAgent: %v", err)
	}
	if slug != "global-worker" || agent.Command != "global worker cmd" {
		t.Errorf("StepAgent = (%q, %q), want (global-worker, global worker cmd)", slug, agent.Command)
	}
}

// TestValidateAgentsLoopTmuxCascade verifies the loop-step tmux constraint
// through the inherited parts of the agent cascade (workflow-level agent: and
// top-level default_agent:), not just the step-level ref, plus the headless
// positive case.
func TestValidateAgentsLoopTmuxCascade(t *testing.T) {
	loopSteps := "workflows:\n  - name: w\n%s    steps:\n      - name: a\n        prompt: p\n" +
		"      - name: b\n        prompt: p\n        loop:\n          goto: a\n          max_iterations: 2\n"

	tests := []struct {
		name    string
		yaml    string
		wantErr string // empty → load must succeed
	}{
		{
			name: "loop step inheriting workflow-level tmux agent",
			yaml: "agents:\n  tm:\n    mode: tmux\n    command: x\n" +
				strings.Replace(loopSteps, "%s", "    agent: tm\n", 1),
			wantErr: "loop steps cannot use a tmux-mode agent",
		},
		{
			name: "loop step inheriting tmux default_agent",
			yaml: "agents:\n  tm:\n    mode: tmux\n    command: x\ndefault_agent: tm\n" +
				strings.Replace(loopSteps, "%s", "", 1),
			wantErr: "loop steps cannot use a tmux-mode agent",
		},
		{
			name: "loop step on a headless agent loads fine",
			yaml: "agents:\n  hl:\n    command: x\ndefault_agent: hl\n" +
				strings.Replace(loopSteps, "%s", "", 1),
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateHome(t)
			dir, _ := setupProject(t, tt.yaml, nil)
			_, err := LoadForProject(dir)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected config to load, got: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

// TestValidateAgentRecordEdges covers agent-record validation edge cases at
// load time: explicit headless mode, whitespace-only command, slug boundary
// shapes for the kebab-case regex, and an empty registry.
func TestValidateAgentRecordEdges(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string // empty → load must succeed
	}{
		{
			name:    "explicit mode headless accepted",
			yaml:    "agents:\n  a:\n    mode: headless\n    command: x\n",
			wantErr: "",
		},
		{
			name:    "whitespace-only command rejected",
			yaml:    "agents:\n  a:\n    command: \"   \"\n",
			wantErr: "command is required",
		},
		{
			name:    "slug with leading dash rejected",
			yaml:    "agents:\n  -foo:\n    command: x\n",
			wantErr: "kebab-case",
		},
		{
			name:    "slug with trailing dash accepted",
			yaml:    "agents:\n  foo-:\n    command: x\n",
			wantErr: "",
		},
		{
			name:    "empty agents registry loads fine",
			yaml:    "agents: {}\n",
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateHome(t)
			dir, _ := setupProject(t, tt.yaml, nil)
			_, err := LoadForProject(dir)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected config to load, got: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

// TestStepAgentResolutionNilStep verifies the nil-step guard in StepAgentSlug:
// resolution falls through to the workflow-level agent without panicking, and
// StepIsTmux reports the workflow agent's mode.
func TestStepAgentResolutionNilStep(t *testing.T) {
	cfg := &Config{
		Agents: map[string]AgentConfig{
			"tm": {Mode: AgentModeTmux, Command: "x"},
		},
	}
	wf := &WorkflowConfig{Name: "w", Agent: "tm"}

	if got := cfg.StepAgentSlug(wf, nil); got != "tm" {
		t.Errorf("StepAgentSlug(wf, nil) = %q, want tm (workflow-level agent)", got)
	}
	if !cfg.StepIsTmux(wf, nil) {
		t.Error("StepIsTmux(wf, nil) = false, want true for the workflow's tmux agent")
	}
}

// TestRemovedKeysInGlobalSakusenYml verifies a removed top-level key in the
// global ~/.sakusen.yml fails LoadForProject with the migration message
// prefixed by the offending file's path.
func TestRemovedKeysInGlobalSakusenYml(t *testing.T) {
	globalYml := "claude:\n  command: /tmp/foo\n"
	projectDir := setupGlobalAndProject(t, globalYml, nil, "max_workers: 2\n", nil)

	_, err := LoadForProject(projectDir)
	if err == nil {
		t.Fatal("expected removed-key error from global ~/.sakusen.yml")
	}
	wantPath := filepath.Join(os.Getenv("HOME"), ".sakusen.yml")
	if !strings.Contains(err.Error(), wantPath+":") {
		t.Errorf("error must be prefixed with the offending file path %q, got: %v", wantPath, err)
	}
	if !strings.Contains(err.Error(), "`claude:` block was removed") {
		t.Errorf("expected claude-block migration message, got: %v", err)
	}
}

// TestSummarizerConfigured verifies SummarizerConfig.Configured: unset and
// whitespace-only commands report false (TrimSpace), a real command true.
func TestSummarizerConfigured(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{"empty command", "", false},
		{"whitespace-only command", "   \t", false},
		{"command set", "summarize-stdin", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &SummarizerConfig{Command: tt.command}
			if got := s.Configured(); got != tt.want {
				t.Errorf("Configured() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestValidateAgentsLoopUnresolvableSlugStillLoads pins the `ok &&` guard in
// validateAgentRefs' loop-step tmux check: a loop step whose agent slug is
// unresolvable (here the implicit "claude" with zero agents configured) must
// be SKIPPED by the tmux constraint, not error — the missing-agent failure is
// deferred to step-run time (StepAgent), per the load-time leniency contract
// (see TestValidateAgentsMissingImplicitClaudeAllowed).
func TestValidateAgentsLoopUnresolvableSlugStillLoads(t *testing.T) {
	isolateHome(t)
	yaml := "workflows:\n  - name: w\n    steps:\n      - name: a\n        prompt: p\n" +
		"      - name: b\n        prompt: p\n        loop:\n          goto: a\n          max_iterations: 2\n"
	dir, _ := setupProject(t, yaml, nil)
	if _, err := LoadForProject(dir); err != nil {
		t.Fatalf("config with a loop step and no agents must still load (agent resolution is deferred to run time), got: %v", err)
	}
}

// TestSummarizerProjectOverridesGlobal verifies the project tier's
// `summarizer:` block wins wholesale over the XDG global config's — including
// max_prompt_bytes, which must not leak through from the global block when
// the project block omits it (overrideFromPtr replaces the whole struct).
func TestSummarizerProjectOverridesGlobal(t *testing.T) {
	writeGlobalConfigYaml(t, `
summarizer:
  command: "global summarize"
  max_prompt_bytes: 4096
`)

	projectDir := t.TempDir()
	projectYml := "summarizer:\n  command: \"project summarize\"\n"
	if err := os.WriteFile(filepath.Join(projectDir, ".sakusen.yml"), []byte(projectYml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if cfg.Summarizer.Command != "project summarize" {
		t.Errorf("Summarizer.Command = %q, want the project tier's %q", cfg.Summarizer.Command, "project summarize")
	}
	if cfg.Summarizer.MaxPromptBytes != 0 {
		t.Errorf("Summarizer.MaxPromptBytes = %d, want 0 (project block replaces the global block wholesale)", cfg.Summarizer.MaxPromptBytes)
	}
}

// TestAgentVariantsExpansion verifies the core variant contract: an env-only
// variant inherits mode/command from the parent, env merges per-key (variant
// wins, parent-only keys survive), the expanded `parent:variant` slug is
// accepted by every agent ref position (step, workflow, default_agent) and
// resolves via ResolveAgent/StepAgent, and the parent record stays usable
// with its own env and no Variants.
func TestAgentVariantsExpansion(t *testing.T) {
	isolateHome(t)
	yaml := `
agents:
  claude:
    command: run --model "$SAKUSEN_MODEL"
    env:
      SAKUSEN_MODEL: default
      KEEP: base
    variants:
      opus:
        env:
          SAKUSEN_MODEL: opus-model
default_agent: claude:opus
workflows:
  - name: w
    agent: claude:opus
    steps:
      - name: s1
        prompt: p
        agent: claude:opus
      - name: s2
        prompt: p
`
	dir, _ := setupProject(t, yaml, nil)
	cfg, err := LoadForProject(dir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}

	variant, ok := cfg.ResolveAgent("claude:opus")
	if !ok {
		t.Fatal("expected expanded claude:opus in the registry")
	}
	if variant.Command != `run --model "$SAKUSEN_MODEL"` {
		t.Errorf("variant.Command = %q, want the parent's command inherited", variant.Command)
	}
	if variant.EffectiveMode() != AgentModeHeadless {
		t.Errorf("variant mode = %q, want inherited headless", variant.EffectiveMode())
	}
	if variant.Env["SAKUSEN_MODEL"] != "opus-model" {
		t.Errorf("Env[SAKUSEN_MODEL] = %q, want the variant's override", variant.Env["SAKUSEN_MODEL"])
	}
	if variant.Env["KEEP"] != "base" {
		t.Errorf("Env[KEEP] = %q, want the parent-only key to survive the merge", variant.Env["KEEP"])
	}

	parent, ok := cfg.ResolveAgent("claude")
	if !ok {
		t.Fatal("parent claude must remain a usable agent")
	}
	if parent.Env["SAKUSEN_MODEL"] != "default" {
		t.Errorf("parent Env[SAKUSEN_MODEL] = %q, want untouched %q", parent.Env["SAKUSEN_MODEL"], "default")
	}
	if parent.Variants != nil {
		t.Errorf("parent.Variants = %v, want nil after expansion", parent.Variants)
	}

	wf := cfg.GetTaskWorkflow("w")
	slug, agent, err := cfg.StepAgent(wf, &wf.Steps[0])
	if err != nil {
		t.Fatalf("StepAgent: %v", err)
	}
	if slug != "claude:opus" || agent.Env["SAKUSEN_MODEL"] != "opus-model" {
		t.Errorf("StepAgent = (%q, env %v), want claude:opus with the variant env", slug, agent.Env)
	}
	// s2 has no step-level agent: the workflow-level claude:opus ref applies.
	if slug := cfg.StepAgentSlug(wf, &wf.Steps[1]); slug != "claude:opus" {
		t.Errorf("StepAgentSlug(s2) = %q, want the workflow-level claude:opus", slug)
	}
}

// TestAgentVariantFieldOverride verifies whole-field override semantics: a
// variant that redefines command keeps everything else inherited, a variant
// may change mode (headless parent → tmux variant with tmux-only fields), and
// a variant that switches a tmux parent to headless fails validation against
// its *resolved* record because the inherited resume_command survives.
func TestAgentVariantFieldOverride(t *testing.T) {
	t.Run("command override keeps inherited fields", func(t *testing.T) {
		isolateHome(t)
		yaml := "agents:\n  base:\n    mode: tmux\n    command: base-cmd\n    resume_command: base-resume\n" +
			"    variants:\n      alt:\n        command: alt-cmd\n"
		dir, _ := setupProject(t, yaml, nil)
		cfg, err := LoadForProject(dir)
		if err != nil {
			t.Fatalf("LoadForProject: %v", err)
		}
		alt, ok := cfg.ResolveAgent("base:alt")
		if !ok {
			t.Fatal("expected base:alt in the registry")
		}
		if alt.Command != "alt-cmd" {
			t.Errorf("Command = %q, want the variant's alt-cmd", alt.Command)
		}
		if !alt.IsTmux() || alt.ResumeCommand != "base-resume" {
			t.Errorf("mode/resume_command not inherited: %+v", alt)
		}
	})

	t.Run("mode change to tmux with tmux-only fields", func(t *testing.T) {
		isolateHome(t)
		yaml := "agents:\n  base:\n    command: base-cmd\n" +
			"    variants:\n      interactive:\n        mode: tmux\n        resume_command: r\n"
		dir, _ := setupProject(t, yaml, nil)
		cfg, err := LoadForProject(dir)
		if err != nil {
			t.Fatalf("LoadForProject: %v", err)
		}
		v, ok := cfg.ResolveAgent("base:interactive")
		if !ok || !v.IsTmux() || v.ResumeCommand != "r" {
			t.Errorf("base:interactive = (%+v, %v), want a tmux record with resume_command", v, ok)
		}
	})

	t.Run("mode change to headless rejects inherited tmux-only field", func(t *testing.T) {
		isolateHome(t)
		yaml := "agents:\n  base:\n    mode: tmux\n    command: base-cmd\n    resume_command: base-resume\n" +
			"    variants:\n      hl:\n        mode: headless\n"
		dir, _ := setupProject(t, yaml, nil)
		_, err := LoadForProject(dir)
		if err == nil || !strings.Contains(err.Error(), "resume_command is only valid for tmux-mode agents") {
			t.Fatalf("expected tmux-only field error on the resolved base:hl record, got: %v", err)
		}
		if err != nil && !strings.Contains(err.Error(), "base:hl") {
			t.Errorf("error should name the variant slug base:hl, got: %v", err)
		}
	})
}

// TestAgentVariantErrors covers variant misdeclarations rejected at load
// time: nested variants, non-kebab-case variant names, a user-authored
// literal `parent:variant` key under agents:, and a ref to an undeclared
// variant.
func TestAgentVariantErrors(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "nested variants rejected",
			yaml:    "agents:\n  a:\n    command: x\n    variants:\n      b:\n        variants:\n          c:\n            command: y\n",
			wantErr: "nested variants are not supported",
		},
		{
			name:    "non-kebab-case variant name rejected",
			yaml:    "agents:\n  a:\n    command: x\n    variants:\n      Opus:\n        command: y\n",
			wantErr: "invalid variant name",
		},
		{
			name:    "user-authored colon key rejected",
			yaml:    "agents:\n  claude:opus:\n    command: x\n",
			wantErr: "invalid agent slug",
		},
		{
			name:    "ref to undeclared variant rejected",
			yaml:    "agents:\n  claude:\n    command: x\nworkflows:\n  - name: w\n    steps:\n      - name: s\n        prompt: p\n        agent: claude:nope\n",
			wantErr: "unknown agent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateHome(t)
			dir, _ := setupProject(t, tt.yaml, nil)
			_, err := LoadForProject(dir)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

// TestAgentVariantsCrossTierWholesale verifies variants ride the wholesale
// per-slug tier merge: a project redefinition of the parent slug replaces the
// global record including its variants, so global-only variant slugs
// disappear from the expanded registry.
func TestAgentVariantsCrossTierWholesale(t *testing.T) {
	globalYml := "agents:\n  claude:\n    command: global-cmd\n" +
		"    variants:\n      opus:\n        env:\n          M: o\n"
	projectYml := "agents:\n  claude:\n    command: project-cmd\n"
	projectDir := setupGlobalAndProject(t, globalYml, nil, projectYml, nil)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	claude, ok := cfg.ResolveAgent("claude")
	if !ok || claude.Command != "project-cmd" {
		t.Errorf("claude = (%+v, %v), want the project-tier record", claude, ok)
	}
	if _, ok := cfg.ResolveAgent("claude:opus"); ok {
		t.Error("claude:opus must not survive a project-tier redefinition of claude (records replace wholesale, variants included)")
	}
}

// TestAgentAliases verifies agent_aliases resolve into ordinary registry
// entries: an alias may target a plain agent or an expanded variant, and the
// alias name is accepted by default_agent and step-level agent refs.
func TestAgentAliases(t *testing.T) {
	isolateHome(t)
	yaml := `
agents:
  claude:
    command: base-cmd
    variants:
      opus:
        env:
          M: opus
  other:
    command: other-cmd
agent_aliases:
  headless-implementer: claude:opus
  fallback-worker: other
default_agent: headless-implementer
workflows:
  - name: w
    steps:
      - name: s
        prompt: p
        agent: fallback-worker
`
	dir, _ := setupProject(t, yaml, nil)
	cfg, err := LoadForProject(dir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}

	impl, ok := cfg.ResolveAgent("headless-implementer")
	if !ok {
		t.Fatal("expected alias headless-implementer in the registry")
	}
	if impl.Command != "base-cmd" || impl.Env["M"] != "opus" {
		t.Errorf("headless-implementer = %+v, want a copy of the claude:opus record", impl)
	}
	worker, ok := cfg.ResolveAgent("fallback-worker")
	if !ok || worker.Command != "other-cmd" {
		t.Errorf("fallback-worker = (%+v, %v), want a copy of other", worker, ok)
	}

	wf := cfg.GetTaskWorkflow("w")
	slug, agent, err := cfg.StepAgent(wf, &wf.Steps[0])
	if err != nil {
		t.Fatalf("StepAgent: %v", err)
	}
	if slug != "fallback-worker" || agent.Command != "other-cmd" {
		t.Errorf("StepAgent = (%q, %q), want the alias slug with the target's record", slug, agent.Command)
	}
}

// TestAgentAliasErrors covers alias misdeclarations rejected at load time:
// collisions with agent and variant slugs, unknown targets, alias→alias
// chains, and malformed alias names (non-kebab-case, colon-containing).
func TestAgentAliasErrors(t *testing.T) {
	base := "agents:\n  claude:\n    command: x\n    variants:\n      opus:\n        env:\n          M: o\n"
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "alias colliding with an agent slug",
			yaml:    base + "agent_aliases:\n  claude: claude:opus\n",
			wantErr: "collides with an existing agent or variant slug",
		},
		// An alias can never collide with a variant slug directly: variant
		// slugs contain a colon, which the alias name check rejects first
		// (covered by the colon-containing case below).
		{
			name:    "unknown alias target",
			yaml:    base + "agent_aliases:\n  worker: missing\n",
			wantErr: "targets unknown agent",
		},
		{
			name:    "alias chain",
			yaml:    base + "agent_aliases:\n  one: claude\n  two: one\n",
			wantErr: "alias chains are not supported",
		},
		{
			name:    "non-kebab-case alias name",
			yaml:    base + "agent_aliases:\n  Worker: claude\n",
			wantErr: "invalid alias name",
		},
		{
			name:    "colon-containing alias name",
			yaml:    base + "agent_aliases:\n  role:impl: claude\n",
			wantErr: "invalid alias name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateHome(t)
			dir, _ := setupProject(t, tt.yaml, nil)
			_, err := LoadForProject(dir)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

// TestAgentAliasesCrossTierOverlay verifies aliases merge per-key across
// tiers: the project re-points a globally-defined alias while global-only
// aliases survive.
func TestAgentAliasesCrossTierOverlay(t *testing.T) {
	globalYml := "agents:\n  ga:\n    command: ga-cmd\n  gb:\n    command: gb-cmd\n" +
		"agent_aliases:\n  worker: ga\n  reviewer: gb\n"
	projectYml := "agent_aliases:\n  worker: gb\n"
	projectDir := setupGlobalAndProject(t, globalYml, nil, projectYml, nil)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	worker, ok := cfg.ResolveAgent("worker")
	if !ok || worker.Command != "gb-cmd" {
		t.Errorf("worker = (%+v, %v), want the project-tier re-point to gb", worker, ok)
	}
	reviewer, ok := cfg.ResolveAgent("reviewer")
	if !ok || reviewer.Command != "gb-cmd" {
		t.Errorf("reviewer = (%+v, %v), want the global-only alias to survive", reviewer, ok)
	}
}

// TestAgentsEnvReplacedWholesaleAcrossTiers verifies that redefining an agent
// slug in a more-local tier replaces the record's `env:` map entirely — keys
// from the global record must not be merged into the project record (a
// leftover global env var reaching a redefined agent would be invisible in
// the project config).
func TestAgentsEnvReplacedWholesaleAcrossTiers(t *testing.T) {
	writeGlobalConfigYaml(t, `
agents:
  worker:
    command: "global cmd"
    env:
      GLOBAL_ONLY: "1"
      SHARED: "global"
`)

	projectDir := t.TempDir()
	projectYml := "agents:\n  worker:\n    command: \"project cmd\"\n    env:\n      SHARED: project\n"
	if err := os.WriteFile(filepath.Join(projectDir, ".sakusen.yml"), []byte(projectYml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	worker, ok := cfg.ResolveAgent("worker")
	if !ok {
		t.Fatal("agent \"worker\" not resolvable after merge")
	}
	if worker.Command != "project cmd" {
		t.Errorf("Command = %q, want the project tier's %q", worker.Command, "project cmd")
	}
	if _, leaked := worker.Env["GLOBAL_ONLY"]; leaked {
		t.Errorf("Env = %v: global-only key must not survive a project-tier redefinition (env replaces wholesale)", worker.Env)
	}
	if worker.Env["SHARED"] != "project" {
		t.Errorf("Env[SHARED] = %q, want %q", worker.Env["SHARED"], "project")
	}
}
