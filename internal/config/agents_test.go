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

	want := filepath.Join(xdgDir, "sortie", "config.yaml")
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
// defined in the XDG global config file ($XDG_CONFIG_HOME/sortie/config.yaml)
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
	projectPath := filepath.Join(projectDir, ".sortie.yml")
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

// TestAgentsGlobalSortieYmlOverridesConfigYaml verifies the load order of the
// two global tiers: a slug defined in both config.yaml and ~/.sortie.yml is
// taken wholesale from ~/.sortie.yml (which loads after config.yaml) — no
// field merging (the config.yaml record's tmux-only resume_command must not
// survive on the replacing headless record, or validation would reject it).
func TestAgentsGlobalSortieYmlOverridesConfigYaml(t *testing.T) {
	_, homeDir, _ := writeGlobalConfigYaml(t, `
agents:
  shared:
    mode: tmux
    command: "config-yaml shared"
    resume_command: "config-yaml resume"
  config-only:
    command: "config-yaml only"
`)

	globalSortieYml := `
agents:
  shared:
    command: "sortie-yml shared"
`
	if err := os.WriteFile(filepath.Join(homeDir, ".sortie.yml"), []byte(globalSortieYml), 0644); err != nil {
		t.Fatal(err)
	}

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, ".sortie.yml"), []byte("max_workers: 2\n"), 0644); err != nil {
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
	if shared.Command != "sortie-yml shared" {
		t.Errorf("shared.Command = %q, want ~/.sortie.yml tier to win over config.yaml", shared.Command)
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
// reference an agent defined solely in the global ~/.sortie.yml, and
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

// TestRemovedKeysInGlobalSortieYml verifies a removed top-level key in the
// global ~/.sortie.yml fails LoadForProject with the migration message
// prefixed by the offending file's path.
func TestRemovedKeysInGlobalSortieYml(t *testing.T) {
	globalYml := "claude:\n  command: /tmp/foo\n"
	projectDir := setupGlobalAndProject(t, globalYml, nil, "max_workers: 2\n", nil)

	_, err := LoadForProject(projectDir)
	if err == nil {
		t.Fatal("expected removed-key error from global ~/.sortie.yml")
	}
	wantPath := filepath.Join(os.Getenv("HOME"), ".sortie.yml")
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
	if err := os.WriteFile(filepath.Join(projectDir, ".sortie.yml"), []byte(projectYml), 0644); err != nil {
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
	if err := os.WriteFile(filepath.Join(projectDir, ".sortie.yml"), []byte(projectYml), 0644); err != nil {
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
