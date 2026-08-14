package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStepConfigParsing verifies that a flat workflows: list with inline steps
// is parsed correctly.
func TestStepConfigParsing(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".sortie.yml")

	yaml := `
workflows:
  - name: default
    steps:
      - name: implement
        prompt: "Implement the task"
      - name: review
        prompt: "Review the implementation"
      - name: test
        prompt: "Write tests"
`
	if err := os.WriteFile(configPath, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	if err := loadProjectConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	steps := cfg.Workflows[0].Steps
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(steps))
	}
}

func TestOnCompleteParsing(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".sortie.yml")

	yamlContent := `
on_complete: merge
workflows:
  - name: default
    steps:
      - name: implement
        prompt: "do it"
  - name: quick
    on_complete: commit
    steps:
      - name: implement
        prompt: "do it"
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	if err := loadProjectConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	if cfg.OnComplete != "merge" {
		t.Errorf("expected project on_complete=merge, got %q", cfg.OnComplete)
	}
	if wf := cfg.GetWorkflow("default"); wf.OnComplete != "" {
		t.Errorf("expected default workflow to inherit (empty on_complete), got %q", wf.OnComplete)
	}
	if wf := cfg.GetWorkflow("quick"); wf.OnComplete != "commit" {
		t.Errorf("expected quick workflow on_complete=commit, got %q", wf.OnComplete)
	}
}

func TestGitOnCompleteRejected(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".sortie.yml")
	if err := os.WriteFile(configPath, []byte("git:\n  on_complete: merge\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	err := loadProjectConfig(configPath, cfg)
	if err == nil || !strings.Contains(err.Error(), "git.on_complete was moved") {
		t.Fatalf("expected git.on_complete migration error, got: %v", err)
	}
}

// TestRemovedProjectKeysRejected verifies the removed .sortie.yml top-level
// keys (claude:, yolo:, system_prompt:, allowed_summarization_models:) are hard
// load-time errors with migration messages pointing at the agents:/summarizer:
// replacements, instead of being silently dropped.
func TestRemovedProjectKeysRejected(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"claude block", "claude:\n  command: /tmp/foo\n  default_args: [--flag]\n", "`claude:` block was removed"},
		{"yolo", "yolo: true\n", "`yolo:` was removed"},
		{"system_prompt", "system_prompt: \"be terse\"\n", "`system_prompt:` was removed"},
		{"allowed_summarization_models", "allowed_summarization_models: [haiku]\n", "`allowed_summarization_models` was removed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, ".sortie.yml")
			if err := os.WriteFile(configPath, []byte(tt.yaml), 0644); err != nil {
				t.Fatal(err)
			}
			cfg := defaultConfig()
			err := loadProjectConfig(configPath, cfg)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected migration error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

// TestRemovedKeysRejectedInGlobalConfig verifies the same removed keys are
// rejected in the global ~/.config/sortie/config.yaml too.
func TestRemovedKeysRejectedInGlobalConfig(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"claude block", "claude:\n  command: /tmp/bar\n", "`claude:` block was removed"},
		{"yolo", "yolo: true\n", "`yolo:` was removed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(configPath, []byte(tt.yaml), 0644); err != nil {
				t.Fatal(err)
			}
			cfg := defaultConfig()
			err := loadGlobalConfig(configPath, cfg)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected migration error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestGetGlobalSortieYmlPath_HonorsXDG(t *testing.T) {
	xdgDir := t.TempDir()
	if err := os.Setenv("XDG_CONFIG_HOME", xdgDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Unsetenv("XDG_CONFIG_HOME") })

	xdgYml := filepath.Join(xdgDir, "sortie", "config.yml")
	if err := os.MkdirAll(filepath.Dir(xdgYml), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xdgYml, []byte("max_workers: 7\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got := getGlobalSortieYmlPath()
	if got != xdgYml {
		t.Errorf("expected %q, got %q", xdgYml, got)
	}
}

func TestGetGlobalSortieYmlPath_FallsBackToHome(t *testing.T) {
	xdgDir := t.TempDir() // empty XDG dir, no config.yml inside
	if err := os.Setenv("XDG_CONFIG_HOME", xdgDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Unsetenv("XDG_CONFIG_HOME") })

	homeDir := t.TempDir()
	if err := os.Setenv("HOME", homeDir); err != nil {
		t.Fatal(err)
	}

	homeYml := filepath.Join(homeDir, ".sortie.yml")
	if err := os.WriteFile(homeYml, []byte("max_workers: 9\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got := getGlobalSortieYmlPath()
	if got != homeYml {
		t.Errorf("expected %q, got %q", homeYml, got)
	}
}

func TestPollIntervalProjectConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".sortie.yml")

	yamlContent := `
poll_interval: 250ms
workflows:
  - name: default
    steps:
      - name: implement
        prompt: "Implement the task"
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	if err := loadProjectConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	if cfg.PollInterval != 250*time.Millisecond {
		t.Errorf("expected poll_interval 250ms, got %v", cfg.PollInterval)
	}
}

func TestPollIntervalGlobalConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(configPath, []byte("poll_interval: 1s\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	if err := loadGlobalConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	if cfg.PollInterval != time.Second {
		t.Errorf("expected poll_interval 1s, got %v", cfg.PollInterval)
	}
}

func TestPollIntervalDefault(t *testing.T) {
	cfg := defaultConfig()
	if cfg.PollInterval != 5*time.Second {
		t.Errorf("expected default poll_interval 5s, got %v", cfg.PollInterval)
	}
}

func TestPollIntervalInvalid(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".sortie.yml")

	yamlContent := `
poll_interval: "not a duration"
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	if err := loadProjectConfig(configPath, cfg); err == nil {
		t.Error("expected error for invalid poll_interval, got nil")
	}
}

func TestLoopConfigParsing(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".sortie.yml")

	yamlContent := `
workflows:
  - name: default
    steps:
      - name: plan
        prompt: "Plan"
      - name: implement
        prompt: "Implement"
      - name: verify
        prompt: "Verify"
        loop:
          goto: plan
          max_iterations: 5
          exit_condition:
            step_context_empty: plan
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	if err := loadProjectConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	steps := cfg.Workflows[0].Steps
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(steps))
	}

	// Check first two steps have no loop
	if steps[0].Loop != nil {
		t.Error("expected plan step to have no loop")
	}
	if steps[1].Loop != nil {
		t.Error("expected implement step to have no loop")
	}

	// Check third step has loop config
	if steps[2].Loop == nil {
		t.Fatal("expected verify step to have loop config")
	}

	loop := steps[2].Loop
	if loop.Goto != "plan" {
		t.Errorf("expected goto 'plan', got %q", loop.Goto)
	}
	if loop.MaxIterations != 5 {
		t.Errorf("expected max_iterations 5, got %d", loop.MaxIterations)
	}

	// Check exit condition
	if loop.ExitCondition == nil {
		t.Fatal("expected exit_condition to be set")
	}
	if loop.ExitCondition.StepContextEmpty != "plan" {
		t.Errorf("expected step_context_empty 'plan', got %q", loop.ExitCondition.StepContextEmpty)
	}
}

func TestValidateLoopsValidConfig(t *testing.T) {
	// The tmux-mode constraint on loop steps is enforced against the resolved
	// agent in validateAgents; ValidateLoops itself is mode-agnostic.
	wf := &WorkflowConfig{
		Name: "test",
		Steps: []StepConfig{
			{Name: "plan", Prompt: "Plan"},
			{Name: "implement", Prompt: "Implement"},
			{
				Name:   "verify",
				Prompt: "Verify",
				Loop: &LoopConfig{
					Goto:          "plan",
					MaxIterations: 3,
					ExitCondition: &LoopExitCondition{
						StepContextEmpty: "plan",
					},
				},
			},
		},
	}

	if err := wf.ValidateLoops(); err != nil {
		t.Errorf("expected valid config to pass validation, got error: %v", err)
	}
}

func TestValidateLoopsInvalidGoto(t *testing.T) {
	wf := &WorkflowConfig{
		Name: "test",
		Steps: []StepConfig{
			{Name: "plan", Prompt: "Plan"},
			{
				Name:   "implement",
				Prompt: "Implement",
				Loop: &LoopConfig{
					Goto:          "nonexistent",
					MaxIterations: 3,
				},
			},
		},
	}

	err := wf.ValidateLoops()
	if err == nil {
		t.Error("expected error for goto referencing unknown step")
	}
	if err != nil && !containsString(err.Error(), "unknown step") {
		t.Errorf("expected error about unknown step, got: %v", err)
	}
}

func TestValidateLoopsForwardGoto(t *testing.T) {
	wf := &WorkflowConfig{
		Name: "test",
		Steps: []StepConfig{
			{
				Name:   "plan",
				Prompt: "Plan",
				Loop: &LoopConfig{
					Goto:          "implement",
					MaxIterations: 3,
				},
			},
			{Name: "implement", Prompt: "Implement"},
		},
	}

	err := wf.ValidateLoops()
	if err == nil {
		t.Error("expected error for forward goto")
	}
	if err != nil && !containsString(err.Error(), "earlier step") {
		t.Errorf("expected error about earlier step, got: %v", err)
	}
}

func TestValidateLoopsSelfReference(t *testing.T) {
	wf := &WorkflowConfig{
		Name: "test",
		Steps: []StepConfig{
			{
				Name:   "plan",
				Prompt: "Plan",
				Loop: &LoopConfig{
					Goto:          "plan",
					MaxIterations: 3,
				},
			},
		},
	}

	err := wf.ValidateLoops()
	if err == nil {
		t.Error("expected error for self-reference")
	}
	if err != nil && !containsString(err.Error(), "earlier step") {
		t.Errorf("expected error about earlier step, got: %v", err)
	}
}

func TestValidateLoopsMaxIterationsZero(t *testing.T) {
	wf := &WorkflowConfig{
		Name: "test",
		Steps: []StepConfig{
			{Name: "plan", Prompt: "Plan"},
			{
				Name:   "implement",
				Prompt: "Implement",
				Loop: &LoopConfig{
					Goto:          "plan",
					MaxIterations: 0,
				},
			},
		},
	}

	err := wf.ValidateLoops()
	if err == nil {
		t.Error("expected error for max_iterations 0")
	}
	if err != nil && !containsString(err.Error(), "must be >= 1") {
		t.Errorf("expected error about max_iterations >= 1, got: %v", err)
	}
}

func TestValidateLoopsHumanStep(t *testing.T) {
	wf := &WorkflowConfig{
		Name: "test",
		Steps: []StepConfig{
			{Name: "plan", Prompt: "Plan"},
			{
				Name:   "implement",
				Prompt: "Implement",
				Human:  true,
				Loop: &LoopConfig{
					Goto:          "plan",
					MaxIterations: 3,
				},
			},
		},
	}

	err := wf.ValidateLoops()
	if err == nil {
		t.Error("expected error for human step with loop")
	}
	if err != nil && !containsString(err.Error(), "human: true") {
		t.Errorf("expected error about human: true, got: %v", err)
	}
}

func TestValidateLoopsInvalidExitCondition(t *testing.T) {
	wf := &WorkflowConfig{
		Name: "test",
		Steps: []StepConfig{
			{Name: "plan", Prompt: "Plan"},
			{
				Name:   "implement",
				Prompt: "Implement",
				Loop: &LoopConfig{
					Goto:          "plan",
					MaxIterations: 3,
					ExitCondition: &LoopExitCondition{
						StepContextEmpty: "nonexistent",
					},
				},
			},
		},
	}

	err := wf.ValidateLoops()
	if err == nil {
		t.Error("expected error for exit condition referencing unknown step")
	}
	if err != nil && !containsString(err.Error(), "unknown step") {
		t.Errorf("expected error about unknown step, got: %v", err)
	}
}

func TestValidateLoopsOverlapping(t *testing.T) {
	wf := &WorkflowConfig{
		Name: "test",
		Steps: []StepConfig{
			{Name: "step1", Prompt: "Step 1"},
			{Name: "step2", Prompt: "Step 2"},
			{Name: "step3", Prompt: "Step 3"},
			{
				Name:   "step4",
				Prompt: "Step 4",
				Loop: &LoopConfig{
					Goto:          "step1",
					MaxIterations: 2,
				},
			},
			{
				Name:   "step5",
				Prompt: "Step 5",
				Loop: &LoopConfig{
					Goto:          "step2",
					MaxIterations: 2,
				},
			},
		},
	}

	err := wf.ValidateLoops()
	if err == nil {
		t.Error("expected error for overlapping loops")
	}
	if err != nil && !containsString(err.Error(), "overlaps") {
		t.Errorf("expected error about overlapping loops, got: %v", err)
	}
}

func TestValidateLoopsNoLoop(t *testing.T) {
	wf := &WorkflowConfig{
		Name: "test",
		Steps: []StepConfig{
			{Name: "plan", Prompt: "Plan"},
			{Name: "implement", Prompt: "Implement"},
			{Name: "verify", Prompt: "Verify"},
		},
	}

	if err := wf.ValidateLoops(); err != nil {
		t.Errorf("expected workflow without loops to pass validation, got error: %v", err)
	}
}

// TestStepAgentCascade verifies the agent resolution cascade:
// step.agent → workflow.agent → top-level default_agent → "claude".
func TestStepAgentCascade(t *testing.T) {
	cfg := &Config{
		Agents: map[string]AgentConfig{
			"claude":     {Command: "claude-cmd"},
			"wf-agent":   {Command: "wf-cmd"},
			"step-agent": {Command: "step-cmd"},
			"def-agent":  {Command: "def-cmd"},
		},
	}
	wf := &WorkflowConfig{Name: "w", Agent: "wf-agent"}

	// Step-level agent wins over everything.
	slug, agent, err := cfg.StepAgent(wf, &StepConfig{Name: "s", Agent: "step-agent"})
	if err != nil {
		t.Fatalf("StepAgent: %v", err)
	}
	if slug != "step-agent" || agent.Command != "step-cmd" {
		t.Errorf("step-level: got (%q, %q), want (step-agent, step-cmd)", slug, agent.Command)
	}

	// Workflow-level agent wins when the step sets none.
	if slug := cfg.StepAgentSlug(wf, &StepConfig{Name: "s"}); slug != "wf-agent" {
		t.Errorf("workflow-level: got %q, want wf-agent", slug)
	}

	// default_agent wins when neither step nor workflow set one.
	cfg.DefaultAgent = "def-agent"
	if slug := cfg.StepAgentSlug(&WorkflowConfig{Name: "plain"}, &StepConfig{Name: "s"}); slug != "def-agent" {
		t.Errorf("default_agent: got %q, want def-agent", slug)
	}

	// Implicit "claude" fallback when nothing is configured.
	cfg.DefaultAgent = ""
	slug, agent, err = cfg.StepAgent(&WorkflowConfig{Name: "plain"}, &StepConfig{Name: "s"})
	if err != nil {
		t.Fatalf("StepAgent fallback: %v", err)
	}
	if slug != DefaultAgentSlug || agent.Command != "claude-cmd" {
		t.Errorf("fallback: got (%q, %q), want (claude, claude-cmd)", slug, agent.Command)
	}
}

// TestStepAgentUnresolvableSlugErrors verifies StepAgent surfaces an
// instructive error when the resolved slug has no record (e.g. the implicit
// "claude" fallback with no agents configured at all).
func TestStepAgentUnresolvableSlugErrors(t *testing.T) {
	cfg := &Config{}
	slug, _, err := cfg.StepAgent(&WorkflowConfig{Name: "w"}, &StepConfig{Name: "s"})
	if slug != DefaultAgentSlug {
		t.Errorf("slug = %q, want %q", slug, DefaultAgentSlug)
	}
	if err == nil || !strings.Contains(err.Error(), `no agent "claude" configured`) {
		t.Fatalf("expected instructive no-agent error, got: %v", err)
	}

	// StepIsTmux degrades to false on unresolvable slugs instead of erroring.
	if cfg.StepIsTmux(&WorkflowConfig{Name: "w"}, &StepConfig{Name: "s"}) {
		t.Error("StepIsTmux must report false for unresolvable agents")
	}
}

func TestEffectiveSummarizationStrategy(t *testing.T) {
	tests := []struct {
		name     string
		strategy string
		want     string
	}{
		{"empty falls back to default", "", DefaultSummarizationStrategy},
		{"explicit last_message preserved", SummarizationStrategyLastMessage, SummarizationStrategyLastMessage},
		{"explicit summarize_chat preserved", SummarizationStrategySummarizeChat, SummarizationStrategySummarizeChat},
		{"explicit none preserved", SummarizationStrategyNone, SummarizationStrategyNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step := &StepConfig{SummarizationStrategy: tt.strategy}
			if got := step.EffectiveSummarizationStrategy(); got != tt.want {
				t.Errorf("EffectiveSummarizationStrategy() = %q, want %q", got, tt.want)
			}
		})
	}

	if DefaultSummarizationStrategy != SummarizationStrategySummarizeChat {
		t.Errorf("DefaultSummarizationStrategy = %q, want %q (default flipped without updating tests/docs?)",
			DefaultSummarizationStrategy, SummarizationStrategySummarizeChat)
	}
}

func TestValidateStepsSummarizationStrategy(t *testing.T) {
	tests := []struct {
		name     string
		strategy string
		wantErr  bool
	}{
		{"empty (default)", "", false},
		{"last_message", "last_message", false},
		{"summarize_chat", "summarize_chat", false},
		{"none", "none", false},
		{"invalid", "bogus", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf := &WorkflowConfig{
				Name: "test",
				Steps: []StepConfig{
					{Name: "step1", Prompt: "Do something", SummarizationStrategy: tt.strategy},
				},
			}
			err := wf.ValidateSteps()
			if tt.wantErr && err == nil {
				t.Error("expected error for invalid summarization_strategy")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestResolveBranchTemplate(t *testing.T) {
	tests := []struct {
		name     string
		tmpl     string
		taskID   int64
		title    string
		slug     string
		expected string
	}{
		{
			name:     "config-style placeholders",
			tmpl:     "sortie/{{task_id}}-{{task_slug}}",
			taskID:   42,
			title:    "Add Login Page",
			slug:     "add-login-page",
			expected: "sortie/42-add-login-page",
		},
		{
			name:     "workflow-style placeholders",
			tmpl:     "feature/{{task.id}}-{{task.title}}",
			taskID:   7,
			title:    "Fix Bug",
			slug:     "fix-bug",
			expected: "feature/7-Fix Bug",
		},
		{
			name:     "mixed placeholders",
			tmpl:     "{{task_id}}/{{task.title}}/{{task.slug}}",
			taskID:   99,
			title:    "Refactor Auth",
			slug:     "refactor-auth",
			expected: "99/Refactor Auth/refactor-auth",
		},
		{
			name:     "no placeholders",
			tmpl:     "feature/my-branch",
			taskID:   1,
			title:    "Title",
			slug:     "title",
			expected: "feature/my-branch",
		},
		{
			name:     "task.slug placeholder",
			tmpl:     "feature/TASK-001-{{task.slug}}",
			taskID:   1,
			title:    "Add feature",
			slug:     "add-feature",
			expected: "feature/TASK-001-add-feature",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ResolveBranchTemplate(tt.tmpl, tt.taskID, tt.title, tt.slug)
			if result != tt.expected {
				t.Errorf("ResolveBranchTemplate(%q) = %q, want %q", tt.tmpl, result, tt.expected)
			}
		})
	}
}

// TestHierarchicalConfigMerging tests the 3-layer config loading:
// defaults -> global .sortie.yml -> project .sortie.yml
func TestHierarchicalConfigMerging(t *testing.T) {
	globalDir := t.TempDir()
	globalSortieYml := filepath.Join(globalDir, ".sortie.yml")
	os.WriteFile(globalSortieYml, []byte(`
max_workers: 5
notifications:
  enabled: true
  on_complete: true
  on_failed: false
git:
  base_branch: develop
on_complete: merge
workflows:
  - name: global-default
    steps:
      - name: global-step
        prompt: "Global default step"
`), 0644)

	projectDir := t.TempDir()
	projectSortieYml := filepath.Join(projectDir, ".sortie.yml")
	os.WriteFile(projectSortieYml, []byte(`
max_workers: 2
workflows:
  - name: default
    steps:
      - name: implement
        prompt: "Implement {{task.input}}"
`), 0644)

	// Simulate the 3-layer loading: defaults -> global .sortie.yml -> project .sortie.yml
	cfg := defaultConfig()

	// Layer 2: global .sortie.yml
	if err := loadProjectConfig(globalSortieYml, cfg); err != nil {
		t.Fatalf("failed to load global .sortie.yml: %v", err)
	}

	// After global .sortie.yml: max_workers=5, notifications set, git set, workflow set
	if cfg.MaxWorkers != 5 {
		t.Errorf("after global .sortie.yml: expected max_workers=5, got %d", cfg.MaxWorkers)
	}
	if !cfg.Notifications.Enabled {
		t.Error("after global .sortie.yml: expected notifications.enabled=true")
	}
	if !cfg.Notifications.OnComplete {
		t.Error("after global .sortie.yml: expected notifications.on_complete=true")
	}
	if cfg.Notifications.OnFailed {
		t.Error("after global .sortie.yml: expected notifications.on_failed=false")
	}
	if cfg.Git.BaseBranch != "develop" {
		t.Errorf("after global .sortie.yml: expected git.base_branch=develop, got %q", cfg.Git.BaseBranch)
	}
	if cfg.OnComplete != "merge" {
		t.Errorf("after global .sortie.yml: expected on_complete=merge, got %q", cfg.OnComplete)
	}
	if len(cfg.Workflows) != 1 || cfg.Workflows[0].Steps[0].Name != "global-step" {
		t.Error("after global .sortie.yml: expected global-step workflow")
	}

	// Layer 3: project .sortie.yml overrides
	if err := loadProjectConfig(projectSortieYml, cfg); err != nil {
		t.Fatalf("failed to load project .sortie.yml: %v", err)
	}

	// max_workers overridden by project
	if cfg.MaxWorkers != 2 {
		t.Errorf("after project override: expected max_workers=2, got %d", cfg.MaxWorkers)
	}
	// notifications should remain from global (project didn't set them)
	if !cfg.Notifications.Enabled {
		t.Error("after project override: expected notifications.enabled=true (from global)")
	}
	if !cfg.Notifications.OnComplete {
		t.Error("after project override: expected notifications.on_complete=true (from global)")
	}
	// git.base_branch should remain from global (project didn't set it)
	if cfg.Git.BaseBranch != "develop" {
		t.Errorf("after project override: expected git.base_branch=develop (from global), got %q", cfg.Git.BaseBranch)
	}
	// workflow should be overridden by project
	if len(cfg.Workflows) != 1 || cfg.Workflows[0].Steps[0].Name != "implement" {
		t.Error("after project override: expected implement workflow from project")
	}
}

func TestHierarchicalConfigProjectOverridesNotifications(t *testing.T) {
	globalDir := t.TempDir()
	globalSortieYml := filepath.Join(globalDir, ".sortie.yml")
	os.WriteFile(globalSortieYml, []byte(`
notifications:
  enabled: true
  on_complete: true
`), 0644)

	projectDir := t.TempDir()
	projectSortieYml := filepath.Join(projectDir, ".sortie.yml")
	os.WriteFile(projectSortieYml, []byte(`
notifications:
  enabled: false
`), 0644)

	cfg := defaultConfig()

	if err := loadProjectConfig(globalSortieYml, cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Notifications.Enabled {
		t.Error("expected notifications.enabled=true from global")
	}

	if err := loadProjectConfig(projectSortieYml, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Notifications.Enabled {
		t.Error("expected notifications.enabled=false after project override")
	}
}

func TestHierarchicalConfigGlobalOnlyNoProject(t *testing.T) {
	globalDir := t.TempDir()
	globalSortieYml := filepath.Join(globalDir, ".sortie.yml")
	os.WriteFile(globalSortieYml, []byte(`
max_workers: 7
git:
  base_branch: develop
  branch_template: "feature/{{task_id}}"
workflows:
  - name: default
    steps:
      - name: plan
        prompt: "Plan the task"
`), 0644)

	cfg := defaultConfig()

	if err := loadProjectConfig(globalSortieYml, cfg); err != nil {
		t.Fatal(err)
	}

	// All values from global .sortie.yml
	if cfg.MaxWorkers != 7 {
		t.Errorf("expected max_workers=7, got %d", cfg.MaxWorkers)
	}
	if cfg.Git.BaseBranch != "develop" {
		t.Errorf("expected git.base_branch=develop, got %q", cfg.Git.BaseBranch)
	}
	if cfg.Git.BranchTemplate != "feature/{{task_id}}" {
		t.Errorf("expected branch_template=feature/{{task_id}}, got %q", cfg.Git.BranchTemplate)
	}
	if len(cfg.Workflows) != 1 || cfg.Workflows[0].Steps[0].Name != "plan" {
		t.Error("expected plan workflow")
	}
}

func TestHierarchicalConfigTmuxNestedAttachBehavior(t *testing.T) {
	globalDir := t.TempDir()
	globalSortieYml := filepath.Join(globalDir, ".sortie.yml")
	os.WriteFile(globalSortieYml, []byte(`
tmux_nested_attach_behavior: nest
`), 0644)

	cfg := defaultConfig()

	if err := loadProjectConfig(globalSortieYml, cfg); err != nil {
		t.Fatal(err)
	}

	if cfg.TmuxNestedAttachBehavior != "nest" {
		t.Errorf("expected tmux_nested_attach_behavior=nest, got %q", cfg.TmuxNestedAttachBehavior)
	}
}

func TestHierarchicalConfigAllThreeLayers(t *testing.T) {
	// Layer 1: global config.yaml (sets notifications)
	globalConfigDir := t.TempDir()
	globalConfigPath := filepath.Join(globalConfigDir, "config.yaml")
	os.WriteFile(globalConfigPath, []byte(`
max_workers: 10
notifications:
  enabled: true
  on_complete: true
  on_failed: true
  on_waiting_input: true
tmux_nested_attach_behavior: switch
`), 0644)

	// Layer 2: global .sortie.yml (sets workflow defaults, overrides max_workers)
	globalSortieDir := t.TempDir()
	globalSortieYml := filepath.Join(globalSortieDir, ".sortie.yml")
	os.WriteFile(globalSortieYml, []byte(`
max_workers: 5
git:
  base_branch: develop
notifications:
  enabled: true
  on_complete: false
workflows:
  - name: default-step
    steps:
      - name: do
        prompt: "Default implementation"
`), 0644)

	// Layer 3: project .sortie.yml (overrides max_workers, adds project workflow)
	projectDir := t.TempDir()
	projectSortieYml := filepath.Join(projectDir, ".sortie.yml")
	os.WriteFile(projectSortieYml, []byte(`
max_workers: 2
workflows:
  - name: default
    steps:
      - name: implement
        prompt: "Implement the task"
`), 0644)

	cfg := defaultConfig()

	// Apply all 3 layers in order
	if err := loadGlobalConfig(globalConfigPath, cfg); err != nil {
		t.Fatalf("layer 1: %v", err)
	}
	if err := loadProjectConfig(globalSortieYml, cfg); err != nil {
		t.Fatalf("layer 2: %v", err)
	}
	if err := loadProjectConfig(projectSortieYml, cfg); err != nil {
		t.Fatalf("layer 3: %v", err)
	}

	// max_workers: project (2) overrides global .sortie.yml (5) overrides config.yaml (10)
	if cfg.MaxWorkers != 2 {
		t.Errorf("expected max_workers=2, got %d", cfg.MaxWorkers)
	}
	// notifications: global .sortie.yml overrides config.yaml
	if !cfg.Notifications.Enabled {
		t.Error("expected notifications.enabled=true")
	}
	if cfg.Notifications.OnComplete {
		t.Error("expected notifications.on_complete=false (from global .sortie.yml)")
	}
	// tmux_nested_attach_behavior: from config.yaml (not overridden)
	if cfg.TmuxNestedAttachBehavior != "switch" {
		t.Errorf("expected tmux_nested_attach_behavior=switch, got %q", cfg.TmuxNestedAttachBehavior)
	}
	// git.base_branch: from global .sortie.yml (not overridden by project)
	if cfg.Git.BaseBranch != "develop" {
		t.Errorf("expected git.base_branch=develop, got %q", cfg.Git.BaseBranch)
	}
	// workflow: from project (overrides global .sortie.yml)
	if len(cfg.Workflows) != 1 || cfg.Workflows[0].Steps[0].Name != "implement" {
		t.Error("expected implement workflow from project")
	}
}

func TestVerificationConfigParsing(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".sortie.yml")

	yamlContent := `
verification:
  max_retries: 3
  verify_summarizer: true
workflows:
  - name: default
    steps:
      - name: implement
        prompt: "Implement"
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	if err := loadProjectConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	if cfg.Verification.MaxRetries != 3 {
		t.Errorf("expected max_retries 3, got %d", cfg.Verification.MaxRetries)
	}
	if !cfg.Verification.VerifySummarizer {
		t.Error("expected verify_summarizer to be true")
	}
}

func TestVerificationConfigProjectOverridesGlobal(t *testing.T) {
	globalDir := t.TempDir()
	globalPath := filepath.Join(globalDir, "config.yaml")
	os.WriteFile(globalPath, []byte(`
verification:
  max_retries: 2
  verify_summarizer: true
`), 0644)

	projectDir := t.TempDir()
	projectPath := filepath.Join(projectDir, ".sortie.yml")
	os.WriteFile(projectPath, []byte(`
verification:
  max_retries: 5
`), 0644)

	cfg := defaultConfig()
	if err := loadGlobalConfig(globalPath, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Verification.MaxRetries != 2 {
		t.Errorf("expected max_retries 2 from global config, got %d", cfg.Verification.MaxRetries)
	}

	if err := loadProjectConfig(projectPath, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Verification.MaxRetries != 5 {
		t.Errorf("expected max_retries 5 after project override, got %d", cfg.Verification.MaxRetries)
	}
}

func TestVerificationConfigDefaultFalse(t *testing.T) {
	cfg := defaultConfig()
	if cfg.Verification.MaxRetries != 0 {
		t.Errorf("expected max_retries to default to 0, got %d", cfg.Verification.MaxRetries)
	}
	if cfg.Verification.VerifySummarizer {
		t.Error("expected verify_summarizer to default to false")
	}
}

// TestFlatWorkflowsParsing verifies the canonical flat workflows: list format.
func TestFlatWorkflowsParsing(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".sortie.yml")

	yamlContent := `
workflows:
  - name: fast
    steps:
      - name: implementing
        prompt: "Implement"
  - name: review
    description: "Full review pass"
    steps:
      - name: reviewing
        prompt: "Review"
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	if err := loadProjectConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	if len(cfg.Workflows) != 2 {
		t.Fatalf("expected 2 workflows, got %d", len(cfg.Workflows))
	}
	if cfg.Workflows[0].Name != "fast" {
		t.Errorf("expected first workflow 'fast', got %q", cfg.Workflows[0].Name)
	}
	if cfg.Workflows[1].Name != "review" {
		t.Errorf("expected second workflow 'review', got %q", cfg.Workflows[1].Name)
	}
	if cfg.Workflows[1].Description != "Full review pass" {
		t.Errorf("expected description 'Full review pass', got %q", cfg.Workflows[1].Description)
	}

	// ListWorkflowNames returns non-hidden workflows
	names := cfg.ListWorkflowNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 workflow names, got %d: %v", len(names), names)
	}
	if names[0] != "fast" || names[1] != "review" {
		t.Errorf("expected ['fast', 'review'], got %v", names)
	}

	// GetWorkflow by name
	wf := cfg.GetWorkflow("fast")
	if wf == nil || wf.Name != "fast" {
		t.Errorf("expected to resolve 'fast', got %+v", wf)
	}

	// GetTaskWorkflow by name
	twf := cfg.GetTaskWorkflow("review")
	if twf == nil || twf.Name != "review" {
		t.Errorf("expected to resolve 'review' via GetTaskWorkflow, got %+v", twf)
	}

	// GetTaskWorkflow with empty name returns first non-hidden
	first := cfg.GetTaskWorkflow("")
	if first == nil || first.Name != "fast" {
		t.Errorf("expected first non-hidden workflow 'fast', got %+v", first)
	}
}

// TestListWorkflowNamesWithHidden verifies that hidden workflows are excluded
// from ListWorkflowNames but included in ListAllWorkflowNames.
func TestListWorkflowNamesWithHidden(t *testing.T) {
	cfg := &Config{
		Workflows: []WorkflowConfig{
			{Name: "visible1"},
			{Name: "visible2"},
			{Name: "hidden1", Hidden: true},
		},
	}

	names := cfg.ListWorkflowNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 non-hidden names, got %d: %v", len(names), names)
	}
	if names[0] != "visible1" || names[1] != "visible2" {
		t.Errorf("expected ['visible1', 'visible2'], got %v", names)
	}

	all := cfg.ListAllWorkflowNames()
	if len(all) != 3 {
		t.Fatalf("expected 3 names including hidden, got %d: %v", len(all), all)
	}
}

// TestListWorkflowNamesEmptyReturnsDefault verifies that an empty workflow list
// returns ["default"] as fallback.
func TestListWorkflowNamesEmptyReturnsDefault(t *testing.T) {
	cfg := &Config{}
	names := cfg.ListWorkflowNames()
	if len(names) != 1 || names[0] != "default" {
		t.Errorf("expected [\"default\"] for empty config, got %v", names)
	}
}

// TestListWorkflowNamesAllHiddenReturnsDefault verifies that when all workflows
// are hidden, ListWorkflowNames returns ["default"].
func TestListWorkflowNamesAllHiddenReturnsDefault(t *testing.T) {
	cfg := &Config{
		Workflows: []WorkflowConfig{
			{Name: "hidden1", Hidden: true},
			{Name: "hidden2", Hidden: true},
		},
	}

	names := cfg.ListWorkflowNames()
	if len(names) != 1 || names[0] != "default" {
		t.Errorf("expected [\"default\"] when all hidden, got %v", names)
	}
}

// TestGetTaskWorkflowEmptyReturnsFirstNonHidden verifies GetTaskWorkflow("")
// returns the first non-hidden workflow, not hidden ones.
func TestGetTaskWorkflowEmptyReturnsFirstNonHidden(t *testing.T) {
	cfg := &Config{
		Workflows: []WorkflowConfig{
			{Name: "hidden-first", Hidden: true},
			{Name: "visible"},
		},
	}

	wf := cfg.GetTaskWorkflow("")
	if wf == nil || wf.Name != "visible" {
		t.Errorf("expected first non-hidden 'visible', got %+v", wf)
	}
}

// TestGetTaskWorkflowAllHiddenReturnsNil verifies GetTaskWorkflow("") returns
// nil when all workflows are hidden.
func TestGetTaskWorkflowAllHiddenReturnsNil(t *testing.T) {
	cfg := &Config{
		Workflows: []WorkflowConfig{
			{Name: "hidden1", Hidden: true},
		},
	}

	wf := cfg.GetTaskWorkflow("")
	if wf != nil {
		t.Errorf("expected nil when all workflows hidden, got %+v", wf)
	}
}

func TestWorktreeSyncPathsParsing(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".sortie.yml")

	yamlContent := `
worktree-sync-paths:
  copy:
    - node_modules/
  link:
    - .claude
    - .env.example
    - scripts/setup.sh
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	if err := loadProjectConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	if len(cfg.WorktreeSyncPaths.Copy) != 1 {
		t.Fatalf("expected 1 copy path, got %d", len(cfg.WorktreeSyncPaths.Copy))
	}
	if cfg.WorktreeSyncPaths.Copy[0] != "node_modules/" {
		t.Errorf("expected copy path 'node_modules/', got %q", cfg.WorktreeSyncPaths.Copy[0])
	}
	if len(cfg.WorktreeSyncPaths.Link) != 3 {
		t.Fatalf("expected 3 link paths, got %d", len(cfg.WorktreeSyncPaths.Link))
	}
	if cfg.WorktreeSyncPaths.Link[0] != ".claude" {
		t.Errorf("expected first link path '.claude', got %q", cfg.WorktreeSyncPaths.Link[0])
	}
}

func TestWorktreeSyncPathsLegacyListFormat(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".sortie.yml")

	// Legacy format: plain list → treated as copy paths
	yamlContent := `
worktree-sync-paths:
  - .claude
  - .env.example
  - scripts/setup.sh
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	if err := loadProjectConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	if len(cfg.WorktreeSyncPaths.Copy) != 3 {
		t.Fatalf("expected 3 copy paths from legacy format, got %d", len(cfg.WorktreeSyncPaths.Copy))
	}
	if cfg.WorktreeSyncPaths.Copy[0] != ".claude" {
		t.Errorf("expected first path '.claude', got %q", cfg.WorktreeSyncPaths.Copy[0])
	}
	if len(cfg.WorktreeSyncPaths.Link) != 0 {
		t.Errorf("expected no link paths from legacy format, got %d", len(cfg.WorktreeSyncPaths.Link))
	}
}

func TestWorktreeSyncPathsProjectOverridesGlobal(t *testing.T) {
	globalDir := t.TempDir()
	globalPath := filepath.Join(globalDir, ".sortie.yml")
	os.WriteFile(globalPath, []byte(`
worktree-sync-paths:
  copy:
    - .claude
    - .env
`), 0644)

	projectDir := t.TempDir()
	projectPath := filepath.Join(projectDir, ".sortie.yml")
	os.WriteFile(projectPath, []byte(`
worktree-sync-paths:
  link:
    - .vscode
`), 0644)

	cfg := defaultConfig()
	if err := loadProjectConfig(globalPath, cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.WorktreeSyncPaths.Copy) != 2 {
		t.Fatalf("expected 2 copy paths from global, got %d", len(cfg.WorktreeSyncPaths.Copy))
	}

	if err := loadProjectConfig(projectPath, cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.WorktreeSyncPaths.Link) != 1 {
		t.Fatalf("expected 1 link path after project override, got %d", len(cfg.WorktreeSyncPaths.Link))
	}
	if cfg.WorktreeSyncPaths.Link[0] != ".vscode" {
		t.Errorf("expected '.vscode', got %q", cfg.WorktreeSyncPaths.Link[0])
	}
	// Project override should replace global entirely
	if len(cfg.WorktreeSyncPaths.Copy) != 0 {
		t.Errorf("expected 0 copy paths after override, got %d", len(cfg.WorktreeSyncPaths.Copy))
	}
}

func TestWorktreeSyncPathsPerWorkflow(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".sortie.yml")

	yamlContent := `
worktree-sync-paths:
  link:
    - .claude
workflows:
  - name: custom
    worktree-sync-paths:
      copy:
        - node_modules
      link:
        - .claude
        - .vscode
    steps:
      - name: implementing
        prompt: "Implement"
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	if err := loadProjectConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	// Global level
	if len(cfg.WorktreeSyncPaths.Link) != 1 {
		t.Fatalf("expected 1 global link path, got %d", len(cfg.WorktreeSyncPaths.Link))
	}

	// Per-workflow override
	wf := cfg.GetWorkflow("custom")
	paths := cfg.GetWorktreeSyncPaths(wf)
	if len(paths.Copy) != 1 {
		t.Fatalf("expected 1 per-workflow copy path, got %d", len(paths.Copy))
	}
	if len(paths.Link) != 2 {
		t.Fatalf("expected 2 per-workflow link paths, got %d", len(paths.Link))
	}
	if paths.Link[0] != ".claude" || paths.Link[1] != ".vscode" {
		t.Errorf("expected [.claude, .vscode], got %v", paths.Link)
	}
}

func TestGetWorktreeSyncPathsFallsBackToGlobal(t *testing.T) {
	cfg := &Config{
		WorktreeSyncPaths: WorktreeSyncPathsConfig{
			Copy: []string{".claude"},
			Link: []string{".env"},
		},
	}

	// Workflow without override should fall back to global
	wf := &WorkflowConfig{Name: "default"}
	paths := cfg.GetWorktreeSyncPaths(wf)
	if len(paths.Copy) != 1 || len(paths.Link) != 1 {
		t.Fatalf("expected 1 copy + 1 link from global fallback, got %d copy + %d link", len(paths.Copy), len(paths.Link))
	}

	// Nil workflow should fall back to global
	paths = cfg.GetWorktreeSyncPaths(nil)
	if len(paths.Copy) != 1 || len(paths.Link) != 1 {
		t.Fatalf("expected 1 copy + 1 link from nil workflow fallback, got %d copy + %d link", len(paths.Copy), len(paths.Link))
	}
}

func TestWorktreeSyncPathsDefaultEmpty(t *testing.T) {
	cfg := defaultConfig()
	if !cfg.WorktreeSyncPaths.IsEmpty() {
		t.Errorf("expected empty sync paths by default, got copy=%v link=%v", cfg.WorktreeSyncPaths.Copy, cfg.WorktreeSyncPaths.Link)
	}
}

func TestWorktreeSetupCommandParsing(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".sortie.yml")

	yamlContent := `
worktree-setup-command: "./scripts/bootstrap.sh {{worktree_path}}"
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	if err := loadProjectConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	expected := "./scripts/bootstrap.sh {{worktree_path}}"
	if cfg.WorktreeSetupCommand != expected {
		t.Errorf("expected %q, got %q", expected, cfg.WorktreeSetupCommand)
	}
}

func TestWorktreeSetupCommandProjectOverridesGlobal(t *testing.T) {
	globalDir := t.TempDir()
	globalPath := filepath.Join(globalDir, ".sortie.yml")
	os.WriteFile(globalPath, []byte(`
worktree-setup-command: "./global-setup.sh"
`), 0644)

	projectDir := t.TempDir()
	projectPath := filepath.Join(projectDir, ".sortie.yml")
	os.WriteFile(projectPath, []byte(`
worktree-setup-command: "./project-setup.sh"
`), 0644)

	cfg := defaultConfig()
	if err := loadProjectConfig(globalPath, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.WorktreeSetupCommand != "./global-setup.sh" {
		t.Errorf("expected global command, got %q", cfg.WorktreeSetupCommand)
	}

	if err := loadProjectConfig(projectPath, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.WorktreeSetupCommand != "./project-setup.sh" {
		t.Errorf("expected project command to override, got %q", cfg.WorktreeSetupCommand)
	}
}

func TestWorktreeSetupCommandPerWorkflow(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".sortie.yml")

	yamlContent := `
worktree-setup-command: "./global-setup.sh"
workflows:
  - name: custom
    worktree-setup-command: "./custom-setup.sh"
    steps:
      - name: implement
        prompt: "do it"
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	if err := loadProjectConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	if cfg.WorktreeSetupCommand != "./global-setup.sh" {
		t.Fatalf("expected global command, got %q", cfg.WorktreeSetupCommand)
	}

	wf := cfg.GetWorkflow("custom")
	if wf == nil {
		t.Fatal("expected to find 'custom' workflow")
	}

	cmd := cfg.GetWorktreeSetupCommand(wf)
	if cmd != "./custom-setup.sh" {
		t.Errorf("expected workflow-level command, got %q", cmd)
	}
}

func TestGetWorktreeSetupCommandFallsBackToGlobal(t *testing.T) {
	cfg := &Config{
		WorktreeSetupCommand: "./global-setup.sh",
	}

	// Workflow without override — should fall back
	wf := &WorkflowConfig{Name: "test"}
	cmd := cfg.GetWorktreeSetupCommand(wf)
	if cmd != "./global-setup.sh" {
		t.Errorf("expected global fallback, got %q", cmd)
	}

	// Nil workflow — should fall back
	cmd = cfg.GetWorktreeSetupCommand(nil)
	if cmd != "./global-setup.sh" {
		t.Errorf("expected global fallback for nil workflow, got %q", cmd)
	}
}

func TestWorktreeSetupCommandDefaultEmpty(t *testing.T) {
	cfg := defaultConfig()
	if cfg.WorktreeSetupCommand != "" {
		t.Errorf("expected empty setup command by default, got %q", cfg.WorktreeSetupCommand)
	}
}

func TestWorktreeSetupCommandsParsing(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".sortie.yml")

	yamlContent := `
worktree-setup-commands:
  - "npm install"
  - "./scripts/bootstrap.sh {{worktree_path}}"
  - "make setup"
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	if err := loadProjectConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	expected := []string{"npm install", "./scripts/bootstrap.sh {{worktree_path}}", "make setup"}
	if len(cfg.WorktreeSetupCommands) != len(expected) {
		t.Fatalf("expected %d commands, got %d", len(expected), len(cfg.WorktreeSetupCommands))
	}
	for i, cmd := range cfg.WorktreeSetupCommands {
		if cmd != expected[i] {
			t.Errorf("command[%d]: expected %q, got %q", i, expected[i], cmd)
		}
	}
}

func TestWorktreeSetupCommandsProjectOverridesGlobal(t *testing.T) {
	globalDir := t.TempDir()
	globalPath := filepath.Join(globalDir, ".sortie.yml")
	os.WriteFile(globalPath, []byte(`
worktree-setup-commands:
  - "global-cmd-1"
  - "global-cmd-2"
`), 0644)

	projectDir := t.TempDir()
	projectPath := filepath.Join(projectDir, ".sortie.yml")
	os.WriteFile(projectPath, []byte(`
worktree-setup-commands:
  - "project-cmd-1"
`), 0644)

	cfg := defaultConfig()
	if err := loadProjectConfig(globalPath, cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.WorktreeSetupCommands) != 2 {
		t.Errorf("expected 2 global commands, got %d", len(cfg.WorktreeSetupCommands))
	}

	if err := loadProjectConfig(projectPath, cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.WorktreeSetupCommands) != 1 || cfg.WorktreeSetupCommands[0] != "project-cmd-1" {
		t.Errorf("expected project commands to override, got %v", cfg.WorktreeSetupCommands)
	}
}

func TestWorktreeSetupCommandsPerWorkflow(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".sortie.yml")

	yamlContent := `
worktree-setup-commands:
  - "global-cmd"
workflows:
  - name: custom
    worktree-setup-commands:
      - "wf-cmd-1"
      - "wf-cmd-2"
    steps:
      - name: implement
        prompt: "do it"
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	if err := loadProjectConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	if len(cfg.WorktreeSetupCommands) != 1 || cfg.WorktreeSetupCommands[0] != "global-cmd" {
		t.Fatalf("expected global commands, got %v", cfg.WorktreeSetupCommands)
	}

	wf := cfg.GetWorkflow("custom")
	if wf == nil {
		t.Fatal("expected to find 'custom' workflow")
	}

	cmds := cfg.GetWorktreeSetupCommands(wf)
	if len(cmds) != 2 || cmds[0] != "wf-cmd-1" || cmds[1] != "wf-cmd-2" {
		t.Errorf("expected workflow-level commands, got %v", cmds)
	}
}

func TestGetWorktreeSetupCommandsFallsBackToGlobal(t *testing.T) {
	cfg := &Config{
		WorktreeSetupCommands: []string{"global-cmd-1", "global-cmd-2"},
	}

	// Workflow without override — should fall back
	wf := &WorkflowConfig{Name: "test"}
	cmds := cfg.GetWorktreeSetupCommands(wf)
	if len(cmds) != 2 || cmds[0] != "global-cmd-1" {
		t.Errorf("expected global fallback, got %v", cmds)
	}

	// Nil workflow — should fall back
	cmds = cfg.GetWorktreeSetupCommands(nil)
	if len(cmds) != 2 {
		t.Errorf("expected global fallback for nil workflow, got %v", cmds)
	}
}

func TestWorktreeSetupCommandsDefaultEmpty(t *testing.T) {
	cfg := defaultConfig()
	if len(cfg.WorktreeSetupCommands) != 0 {
		t.Errorf("expected empty setup commands by default, got %v", cfg.WorktreeSetupCommands)
	}
}

func TestTmuxSetupCommandParsing(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".sortie.yml")

	yamlContent := `
tmux-setup-command: "tmux new-window -t {{session_name}}:1"
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	if err := loadProjectConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	expected := `tmux new-window -t {{session_name}}:1`
	if cfg.TmuxSetupCommand != expected {
		t.Errorf("expected %q, got %q", expected, cfg.TmuxSetupCommand)
	}
}

func TestTmuxSetupCommandPerWorkflow(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".sortie.yml")

	yamlContent := `
tmux-setup-command: "tmux new-window -t {{session_name}}:1"
workflows:
  - name: custom
    tmux-setup-command: "tmux split-window -t {{session_name}}"
    steps:
      - name: code
        prompt: Do work
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	if err := loadProjectConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	if cfg.TmuxSetupCommand != `tmux new-window -t {{session_name}}:1` {
		t.Fatalf("expected global command, got %q", cfg.TmuxSetupCommand)
	}

	wf := cfg.GetWorkflow("custom")
	if wf == nil {
		t.Fatal("workflow 'custom' not found")
	}

	cmd := cfg.GetTmuxSetupCommand(wf)
	if cmd != `tmux split-window -t {{session_name}}` {
		t.Errorf("expected workflow command, got %q", cmd)
	}
}

func TestGetTmuxSetupCommandFallsBackToGlobal(t *testing.T) {
	cfg := &Config{
		TmuxSetupCommand: "tmux new-window -t {{session_name}}:1",
	}

	// Workflow without override should fall back
	wf := &WorkflowConfig{Name: "test"}
	cmd := cfg.GetTmuxSetupCommand(wf)
	if cmd != "tmux new-window -t {{session_name}}:1" {
		t.Errorf("expected global fallback, got %q", cmd)
	}

	// Nil workflow should also fall back
	cmd = cfg.GetTmuxSetupCommand(nil)
	if cmd != "tmux new-window -t {{session_name}}:1" {
		t.Errorf("expected global fallback for nil, got %q", cmd)
	}
}

func TestTmuxSetupCommandDefaultEmpty(t *testing.T) {
	cfg := defaultConfig()
	if cfg.TmuxSetupCommand != "" {
		t.Errorf("expected empty tmux setup command by default, got %q", cfg.TmuxSetupCommand)
	}
}

// TestConfigFirstStepIsTmux verifies Config.FirstStepIsTmux resolves the first
// step's agent through the cascade and reports its mode.
func TestConfigFirstStepIsTmux(t *testing.T) {
	cfg := &Config{
		Agents: map[string]AgentConfig{
			"claude":      {Mode: AgentModeHeadless, Command: "true"},
			"claude-tmux": {Mode: AgentModeTmux, Command: "true"},
		},
	}

	tests := []struct {
		name string
		wf   *WorkflowConfig
		want bool
	}{
		{
			name: "nil workflow returns false",
			wf:   nil,
			want: false,
		},
		{
			name: "no steps returns false",
			wf:   &WorkflowConfig{Name: "empty"},
			want: false,
		},
		{
			// The implicit "claude" fallback resolves to a headless agent.
			name: "default agent is headless",
			wf: &WorkflowConfig{
				Name: "headless",
				Steps: []StepConfig{
					{Name: "implement"},
				},
			},
			want: false,
		},
		{
			// Workflow-level tmux agent makes the first step tmux.
			name: "workflow-level tmux agent",
			wf: &WorkflowConfig{
				Name:  "tmux-first",
				Agent: "claude-tmux",
				Steps: []StepConfig{
					{Name: "interact"},
					{Name: "review"},
				},
			},
			want: true,
		},
		{
			// Step-level agent overrides the workflow default.
			name: "step-level tmux agent on first step",
			wf: &WorkflowConfig{
				Name: "override-on",
				Steps: []StepConfig{
					{Name: "interact", Agent: "claude-tmux"},
				},
			},
			want: true,
		},
		{
			// Step-level headless agent overrides a workflow-level tmux agent.
			name: "step-level headless agent overrides workflow tmux",
			wf: &WorkflowConfig{
				Name:  "headless-first",
				Agent: "claude-tmux",
				Steps: []StepConfig{
					{Name: "implement", Agent: "claude"},
					{Name: "interact"},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cfg.FirstStepIsTmux(tt.wf); got != tt.want {
				t.Errorf("FirstStepIsTmux() = %v, want %v", got, tt.want)
			}
		})
	}

	// A tmux-mode default_agent flips the fallback path too.
	tmuxDefault := &Config{
		Agents:       cfg.Agents,
		DefaultAgent: "claude-tmux",
	}
	wf := &WorkflowConfig{Name: "plain", Steps: []StepConfig{{Name: "interact"}}}
	if !tmuxDefault.FirstStepIsTmux(wf) {
		t.Error("expected FirstStepIsTmux=true with a tmux-mode default_agent")
	}
}

// TestLoadProjectConfig_LegacyTmuxFieldRejected verifies the production load
// path (not just ValidateFile) refuses configs that still use the removed
// `tmux:` field, with an error that points at the agent mechanism so the user
// can migrate without consulting docs.
func TestLoadProjectConfig_LegacyTmuxFieldRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".sortie.yml")
	yaml := "workflows:\n  - name: w\n    tmux: true\n    steps:\n      - name: s\n        prompt: do\n"
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	err := loadProjectConfig(path, cfg)
	if err == nil {
		t.Fatal("expected error when loading config with legacy `tmux:` field")
	}
	if !contains(err.Error(), "tmux") || !contains(err.Error(), "agent") {
		t.Errorf("error must mention both `tmux` and the `agent` replacement for migration clarity, got: %v", err)
	}
}

// TestLoadProjectConfig_LegacyPrintFieldRejected verifies the removed `print:`
// field (workflow and step level) is a hard load-time error pointing at the
// agent mechanism.
func TestLoadProjectConfig_LegacyPrintFieldRejected(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "workflow-level print",
			yaml: "workflows:\n  - name: w\n    print: true\n    steps:\n      - name: s\n        prompt: do\n",
		},
		{
			name: "step-level print",
			yaml: "workflows:\n  - name: w\n    steps:\n      - name: s\n        prompt: do\n        print: true\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".sortie.yml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0644); err != nil {
				t.Fatal(err)
			}
			cfg := defaultConfig()
			err := loadProjectConfig(path, cfg)
			if err == nil {
				t.Fatal("expected error when loading config with legacy `print:` field")
			}
			if !contains(err.Error(), "print") || !contains(err.Error(), "agent") {
				t.Errorf("error must mention both `print` and the `agent` replacement, got: %v", err)
			}
		})
	}
}

// TestLoadProjectConfig_StepAllowedSummarizationModelsRejected verifies the
// removed step-level `allowed_summarization_models:` field is a hard load-time
// error pointing at the summarizer command.
func TestLoadProjectConfig_StepAllowedSummarizationModelsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".sortie.yml")
	yaml := "workflows:\n  - name: w\n    steps:\n      - name: s\n        prompt: do\n        allowed_summarization_models: [haiku]\n"
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	err := loadProjectConfig(path, cfg)
	if err == nil || !contains(err.Error(), "allowed_summarization_models") || !contains(err.Error(), "summarizer") {
		t.Fatalf("expected migration error pointing at `summarizer:`, got: %v", err)
	}
}

// TestWorkflowPinFields verifies parsing of worktree/branch/checkout/target
// pin fields on a workflow.
func TestWorkflowPinFields(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".sortie.yml")

	trueVal := true
	yamlContent := `
workflows:
  - name: pinned-worktree
    worktree: true
    branch: "feature/{{task.slug}}"
    target: develop
    steps:
      - name: implement
        prompt: "Implement"
  - name: pinned-checkout
    worktree: true
    checkout: main
    target: main
    steps:
      - name: implement
        prompt: "Implement"
  - name: no-worktree
    worktree: false
    steps:
      - name: implement
        prompt: "Implement"
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	if err := loadProjectConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	if len(cfg.Workflows) != 3 {
		t.Fatalf("expected 3 workflows, got %d", len(cfg.Workflows))
	}

	// First workflow: worktree=true, branch set, target set
	wf0 := cfg.Workflows[0]
	if wf0.Worktree == nil || *wf0.Worktree != trueVal {
		t.Errorf("expected worktree=true, got %v", wf0.Worktree)
	}
	if wf0.Branch != "feature/{{task.slug}}" {
		t.Errorf("expected branch 'feature/{{task.slug}}', got %q", wf0.Branch)
	}
	if wf0.Target != "develop" {
		t.Errorf("expected target 'develop', got %q", wf0.Target)
	}
	if wf0.Checkout != "" {
		t.Errorf("expected empty checkout, got %q", wf0.Checkout)
	}

	// Second workflow: worktree=true, checkout set, target set
	wf1 := cfg.Workflows[1]
	if wf1.Worktree == nil || !*wf1.Worktree {
		t.Errorf("expected worktree=true, got %v", wf1.Worktree)
	}
	if wf1.Checkout != "main" {
		t.Errorf("expected checkout 'main', got %q", wf1.Checkout)
	}
	if wf1.Target != "main" {
		t.Errorf("expected target 'main', got %q", wf1.Target)
	}
	if wf1.Branch != "" {
		t.Errorf("expected empty branch, got %q", wf1.Branch)
	}

	// Third workflow: worktree=false
	wf2 := cfg.Workflows[2]
	if wf2.Worktree == nil || *wf2.Worktree {
		t.Errorf("expected worktree=false, got %v", wf2.Worktree)
	}
}

// TestIsFullySpec covers the IsFullySpec() truth table.
func TestIsFullySpec(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name string
		wf   WorkflowConfig
		want bool
	}{
		{
			name: "empty workflow is not fully spec",
			wf:   WorkflowConfig{Name: "empty"},
			want: false,
		},
		{
			name: "input missing",
			wf:   WorkflowConfig{Name: "w", Worktree: &trueVal, Branch: "b", Target: "t"},
			want: false,
		},
		{
			name: "worktree nil",
			wf:   WorkflowConfig{Name: "w", Input: "d", Branch: "b", Target: "t"},
			want: false,
		},
		{
			name: "worktree=false is fully spec (no git section needed)",
			wf:   WorkflowConfig{Name: "w", Input: "d", Worktree: &falseVal},
			want: true,
		},
		{
			name: "worktree=true, branch set, target set",
			wf:   WorkflowConfig{Name: "w", Input: "d", Worktree: &trueVal, Branch: "feature/x", Target: "develop"},
			want: true,
		},
		{
			name: "worktree=true, checkout set, target set",
			wf:   WorkflowConfig{Name: "w", Input: "d", Worktree: &trueVal, Checkout: "main", Target: "main"},
			want: true,
		},
		{
			name: "worktree=true, branch missing",
			wf:   WorkflowConfig{Name: "w", Input: "d", Worktree: &trueVal, Target: "develop"},
			want: false,
		},
		{
			name: "worktree=true, target missing",
			wf:   WorkflowConfig{Name: "w", Input: "d", Worktree: &trueVal, Branch: "feature/x"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.wf.IsFullySpec()
			if got != tt.want {
				t.Errorf("IsFullySpec() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestValidatePins verifies ValidatePins() rejects invalid pin combinations.
func TestValidatePins(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name    string
		wf      WorkflowConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "no pins — valid",
			wf:      WorkflowConfig{Name: "w"},
			wantErr: false,
		},
		{
			name:    "worktree=true with branch — valid",
			wf:      WorkflowConfig{Name: "w", Worktree: &trueVal, Branch: "feature/x"},
			wantErr: false,
		},
		{
			name:    "worktree=true with checkout — valid",
			wf:      WorkflowConfig{Name: "w", Worktree: &trueVal, Checkout: "main"},
			wantErr: false,
		},
		{
			name:    "branch AND checkout together — error",
			wf:      WorkflowConfig{Name: "w", Branch: "feature/x", Checkout: "main"},
			wantErr: true,
			errMsg:  "cannot set both branch and checkout",
		},
		{
			name:    "worktree=false with branch — error",
			wf:      WorkflowConfig{Name: "w", Worktree: &falseVal, Branch: "feature/x"},
			wantErr: true,
			errMsg:  "branch/checkout/target cannot be set when worktree: false",
		},
		{
			name:    "worktree=false with checkout — error",
			wf:      WorkflowConfig{Name: "w", Worktree: &falseVal, Checkout: "main"},
			wantErr: true,
			errMsg:  "branch/checkout/target cannot be set when worktree: false",
		},
		{
			name:    "worktree=false with target — error",
			wf:      WorkflowConfig{Name: "w", Worktree: &falseVal, Target: "develop"},
			wantErr: true,
			errMsg:  "branch/checkout/target cannot be set when worktree: false",
		},
		{
			name:    "worktree=false with no branch/checkout/target — valid",
			wf:      WorkflowConfig{Name: "w", Worktree: &falseVal},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.wf.ValidatePins()
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errMsg)
				} else if !contains(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got: %v", tt.errMsg, err)
				}
			} else {
				if err != nil {
					t.Errorf("expected no error, got: %v", err)
				}
			}
		})
	}
}

// TestValidatePinsRejectedAtLoad verifies that ValidatePins() errors are
// surfaced during config loading.
func TestValidatePinsRejectedAtLoad(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".sortie.yml")

	// branch and checkout together is invalid
	yamlContent := `
workflows:
  - name: bad
    worktree: true
    branch: "feature/x"
    checkout: "main"
    steps:
      - name: implement
        prompt: "Implement"
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	err := loadProjectConfig(configPath, cfg)
	if err == nil {
		t.Fatal("expected error for branch+checkout together")
	}
	if !contains(err.Error(), "cannot set both branch and checkout") {
		t.Errorf("expected error about branch+checkout, got: %v", err)
	}
}

// Helper function to check if string contains substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && contains(s, substr))
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestEffectiveOnCompleteLocality verifies scope-aware on_complete precedence
// through the real loader: a workflow adopted from the global pool carries its
// on_complete only as a default — an explicit project-level on_complete beats
// it — while a project-defined workflow's on_complete beats the project-level
// setting.
func TestEffectiveOnCompleteLocality(t *testing.T) {
	writeGlobal := func(t *testing.T) string {
		globalDir := t.TempDir()
		globalPath := filepath.Join(globalDir, ".sortie.yml")
		if err := os.WriteFile(globalPath, []byte("workflows:\n  - gwf\n"), 0644); err != nil {
			t.Fatal(err)
		}
		wfDir := filepath.Join(globalDir, ".sortie", "workflows")
		if err := os.MkdirAll(wfDir, 0755); err != nil {
			t.Fatal(err)
		}
		gwf := "on_complete: none\nsteps:\n  - name: s\n    prompt: p\n"
		if err := os.WriteFile(filepath.Join(wfDir, "gwf.yml"), []byte(gwf), 0644); err != nil {
			t.Fatal(err)
		}
		return globalPath
	}

	load := func(t *testing.T, projectYml string) *Config {
		globalPath := writeGlobal(t)
		cfg := defaultConfig()
		if err := loadProjectConfigTier(globalPath, cfg, false); err != nil {
			t.Fatal(err)
		}
		cfg.globalPool = snapshotGlobalPool(cfg)

		projectDir := t.TempDir()
		projectPath := filepath.Join(projectDir, ".sortie.yml")
		if err := os.WriteFile(projectPath, []byte(projectYml), 0644); err != nil {
			t.Fatal(err)
		}
		localDir := filepath.Join(projectDir, ".sortie", "workflows")
		if err := os.MkdirAll(localDir, 0755); err != nil {
			t.Fatal(err)
		}
		lwf := "on_complete: commit\nsteps:\n  - name: s\n    prompt: p\n"
		if err := os.WriteFile(filepath.Join(localDir, "lwf.yml"), []byte(lwf), 0644); err != nil {
			t.Fatal(err)
		}
		if err := loadProjectConfig(projectPath, cfg); err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	t.Run("explicit project on_complete beats global workflow", func(t *testing.T) {
		cfg := load(t, "on_complete: merge\nworkflows:\n  - gwf\n  - lwf\n")

		if !cfg.OnCompleteFromProject {
			t.Fatal("OnCompleteFromProject = false, want true")
		}
		gwf := cfg.GetWorkflow("gwf")
		if !gwf.FromGlobal {
			t.Error("gwf.FromGlobal = false, want true (adopted from global pool)")
		}
		lwf := cfg.GetWorkflow("lwf")
		if lwf.FromGlobal {
			t.Error("lwf.FromGlobal = true, want false (project-local file)")
		}
		if got := cfg.EffectiveOnComplete("gwf"); got != "merge" {
			t.Errorf("EffectiveOnComplete(gwf) = %q, want %q (project explicit beats global workflow)", got, "merge")
		}
		if got := cfg.EffectiveOnComplete("lwf"); got != "commit" {
			t.Errorf("EffectiveOnComplete(lwf) = %q, want %q (project workflow beats project-level)", got, "commit")
		}
		if got := cfg.EffectiveOnComplete("unknown"); got != "merge" {
			t.Errorf("EffectiveOnComplete(unknown) = %q, want %q", got, "merge")
		}
	})

	t.Run("global workflow on_complete applies when project inherits", func(t *testing.T) {
		cfg := load(t, "workflows:\n  - gwf\n  - lwf\n")

		if cfg.OnCompleteFromProject {
			t.Fatal("OnCompleteFromProject = true, want false (project never set it)")
		}
		if got := cfg.EffectiveOnComplete("gwf"); got != "none" {
			t.Errorf("EffectiveOnComplete(gwf) = %q, want %q (global workflow default applies)", got, "none")
		}
		if got := cfg.EffectiveOnComplete("unknown"); got != "commit" {
			t.Errorf("EffectiveOnComplete(unknown) = %q, want %q (built-in default)", got, "commit")
		}
	})
}

// TestAgentsParsedFromYaml verifies the agents:/default_agent:/summarizer:
// surface parses fully (mode, tmux-only fields, env) and drives the
// mode-resolution accessors on a loaded Config.
func TestAgentsParsedFromYaml(t *testing.T) {
	isolateHome(t)
	dir, _ := setupProject(t, `
agents:
  worker:
    command: "run-headless"
  interactive:
    mode: tmux
    command: "run-tmux"
    resume_command: "run-tmux --resume"
    chat_log_command: "cat chat.log"
    env:
      FOO: bar
summarizer:
  command: "summarize-stdin"
  max_prompt_bytes: 1024
workflows:
  - name: w
    agent: interactive
    steps:
      - name: interact
        prompt: p
      - name: implement
        prompt: p
        agent: worker
`, nil)

	cfg, err := LoadForProject(dir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}

	interactive, ok := cfg.ResolveAgent("interactive")
	if !ok {
		t.Fatal("expected interactive agent in registry")
	}
	if !interactive.IsTmux() {
		t.Error("interactive agent must resolve as tmux mode")
	}
	if interactive.Command != "run-tmux" ||
		interactive.ResumeCommand != "run-tmux --resume" ||
		interactive.ChatLogCommand != "cat chat.log" {
		t.Errorf("interactive agent fields not parsed: %+v", interactive)
	}
	if interactive.Env["FOO"] != "bar" {
		t.Errorf("interactive agent env not parsed: %v", interactive.Env)
	}

	worker, ok := cfg.ResolveAgent("worker")
	if !ok || worker.IsTmux() {
		t.Errorf("worker must be a headless agent, got ok=%v %+v", ok, worker)
	}
	if worker.EffectiveMode() != AgentModeHeadless {
		t.Errorf("worker EffectiveMode = %q, want headless default", worker.EffectiveMode())
	}

	if cfg.Summarizer.Command != "summarize-stdin" || cfg.Summarizer.MaxPromptBytes != 1024 {
		t.Errorf("summarizer not parsed: %+v", cfg.Summarizer)
	}

	wf := cfg.GetTaskWorkflow("w")
	if wf == nil {
		t.Fatal("workflow w not found")
	}
	if !cfg.FirstStepIsTmux(wf) {
		t.Error("first step resolves to the workflow's tmux agent → FirstStepIsTmux must be true")
	}
	if cfg.StepIsTmux(wf, &wf.Steps[1]) {
		t.Error("second step overrides with the headless worker agent → StepIsTmux must be false")
	}
}

// TestAgentsMergeAcrossTiers verifies the agents map merges per slug across
// the global (~/.sortie.yml) and project tiers: a slug redefined by the project
// wins wholesale (no field merging), other global slugs survive, and the
// project default_agent overrides the global one.
func TestAgentsMergeAcrossTiers(t *testing.T) {
	globalYml := `
default_agent: global-default
agents:
  shared:
    mode: tmux
    command: "global shared"
    resume_command: "global resume"
  global-only:
    command: "global only"
  global-default:
    command: "gd"
`
	projectYml := `
default_agent: project-default
agents:
  shared:
    command: "project shared"
  project-default:
    command: "pd"
`
	projectDir := setupGlobalAndProject(t, globalYml, nil, projectYml, nil)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}

	// The project's "shared" replaces the global record wholesale: headless,
	// no leftover resume_command (a field-merged record would fail validation —
	// resume_command is tmux-only).
	shared, ok := cfg.ResolveAgent("shared")
	if !ok {
		t.Fatal("expected shared agent")
	}
	if shared.Command != "project shared" {
		t.Errorf("shared.Command = %q, want project tier to win", shared.Command)
	}
	if shared.IsTmux() || shared.ResumeCommand != "" {
		t.Errorf("shared must be replaced wholesale, got %+v", shared)
	}

	// Global-only slugs survive the merge.
	if _, ok := cfg.ResolveAgent("global-only"); !ok {
		t.Error("expected global-only agent to survive the merge")
	}

	// Project default_agent overrides the global one.
	if cfg.DefaultAgent != "project-default" {
		t.Errorf("DefaultAgent = %q, want project-default", cfg.DefaultAgent)
	}
	if cfg.EffectiveDefaultAgentSlug() != "project-default" {
		t.Errorf("EffectiveDefaultAgentSlug = %q, want project-default", cfg.EffectiveDefaultAgentSlug())
	}
}

// TestAgentsGlobalDefaultAgentInherited verifies a global default_agent applies
// when the project doesn't set its own.
func TestAgentsGlobalDefaultAgentInherited(t *testing.T) {
	projectDir := setupGlobalAndProject(t, `
default_agent: global-default
agents:
  global-default:
    command: "gd"
`, nil, "max_workers: 2\n", nil)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if cfg.DefaultAgent != "global-default" {
		t.Errorf("DefaultAgent = %q, want global-default inherited from ~/.sortie.yml", cfg.DefaultAgent)
	}
}

// TestValidateAgentsAtLoad covers the load-time agent validation surface:
// record shape errors, unresolvable explicit refs, loop-step tmux constraints,
// and the removed {{claude_command}} template variable.
func TestValidateAgentsAtLoad(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "invalid mode",
			yaml:    "agents:\n  a:\n    mode: quantum\n    command: x\n",
			wantErr: "invalid mode",
		},
		{
			name:    "missing command",
			yaml:    "agents:\n  a:\n    mode: headless\n",
			wantErr: "command is required",
		},
		{
			name:    "resume_command on headless agent",
			yaml:    "agents:\n  a:\n    command: x\n    resume_command: y\n",
			wantErr: "resume_command is only valid for tmux-mode agents",
		},
		{
			name:    "chat_log_command on headless agent",
			yaml:    "agents:\n  a:\n    command: x\n    chat_log_command: y\n",
			wantErr: "chat_log_command is only valid for tmux-mode agents",
		},
		{
			name:    "invalid slug",
			yaml:    "agents:\n  Bad_Slug:\n    command: x\n",
			wantErr: "kebab-case",
		},
		{
			name:    "unknown default_agent ref",
			yaml:    "default_agent: ghost\n",
			wantErr: `default_agent: unknown agent "ghost"`,
		},
		{
			name:    "unknown workflow agent ref",
			yaml:    "workflows:\n  - name: w\n    agent: ghost\n    steps:\n      - name: s\n        prompt: p\n",
			wantErr: `workflow "w": unknown agent "ghost"`,
		},
		{
			name:    "unknown step agent ref",
			yaml:    "workflows:\n  - name: w\n    steps:\n      - name: s\n        prompt: p\n        agent: ghost\n",
			wantErr: `workflow "w" step "s": unknown agent "ghost"`,
		},
		{
			name: "loop step resolving to tmux agent",
			yaml: "agents:\n  tm:\n    mode: tmux\n    command: x\n" +
				"workflows:\n  - name: w\n    steps:\n      - name: a\n        prompt: p\n" +
				"      - name: b\n        prompt: p\n        agent: tm\n        loop:\n          goto: a\n          max_iterations: 2\n",
			wantErr: "loop steps cannot use a tmux-mode agent",
		},
		{
			name:    "claude_command in top-level tmux-setup-command",
			yaml:    "tmux-setup-command: \"run {{claude_command}}\"\n",
			wantErr: "{{claude_command}} was removed",
		},
		{
			name:    "claude_command in workflow tmux-setup-command",
			yaml:    "workflows:\n  - name: w\n    tmux-setup-command: \"run {{claude_command}}\"\n    steps:\n      - name: s\n        prompt: p\n",
			wantErr: "{{claude_command}} was removed",
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

// TestValidateAgentsMissingImplicitClaudeAllowed verifies a config with no
// agents at all still loads: the implicit "claude" fallback is exempt from
// ref validation and only fails at step-run time.
func TestValidateAgentsMissingImplicitClaudeAllowed(t *testing.T) {
	isolateHome(t)
	dir, _ := setupProject(t, `
workflows:
  - name: w
    steps:
      - name: s
        prompt: p
`, nil)

	cfg, err := LoadForProject(dir)
	if err != nil {
		t.Fatalf("LoadForProject must tolerate a missing implicit claude agent, got: %v", err)
	}
	wf := cfg.GetTaskWorkflow("w")
	if _, _, err := cfg.StepAgent(wf, &wf.Steps[0]); err == nil {
		t.Error("expected StepAgent to fail at run time when no agent is configured")
	}
}
