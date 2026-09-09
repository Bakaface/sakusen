package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// parseWorkflow decodes a single workflow from YAML the way the loader does.
func parseWorkflow(t *testing.T, y string) WorkflowConfig {
	t.Helper()
	var wf WorkflowConfig
	if err := yaml.Unmarshal([]byte(y), &wf); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	return wf
}

const twoBranchGroupYAML = `
name: reviewed
steps:
  - name: implement
    prompt: "do it"
  - name: review
    timeout: 20m
    agent: fallback
    parallel:
      require: any
      branches:
        - name: review-opus
          agent: claude:opus
          prompt: "review as {{branch.name}}"
        - name: review-codex
          prompt: "review it"
          timeout: 5m
          summarization_strategy: summarize_chat
  - name: synthesize
    prompt: "{{steps.review.context}}"
`

func TestParallelGroupParses(t *testing.T) {
	wf := parseWorkflow(t, twoBranchGroupYAML)
	if err := wf.ValidateSteps(); err != nil {
		t.Fatalf("ValidateSteps: %v", err)
	}

	group := wf.Steps[1]
	if !group.IsParallel() {
		t.Fatal("expected step 1 to be a parallel group")
	}
	if got := len(group.Parallel.Branches); got != 2 {
		t.Fatalf("branches = %d, want 2", got)
	}
	if got := group.Parallel.RequiredCount(); got != 1 {
		t.Errorf("RequiredCount for require:any = %d, want 1", got)
	}
	if got := group.Parallel.EffectiveRequire(); got != RequireAny {
		t.Errorf("EffectiveRequire = %q, want %q", got, RequireAny)
	}
}

func TestParallelEffectiveBranchFallbacks(t *testing.T) {
	wf := parseWorkflow(t, twoBranchGroupYAML)
	group := wf.Steps[1]

	opus := group.Parallel.EffectiveBranch(0, &group)
	if opus.Agent != "claude:opus" {
		t.Errorf("branch agent = %q, want the branch's own %q", opus.Agent, "claude:opus")
	}
	if opus.Timeout != "20m" {
		t.Errorf("branch timeout = %q, want the group's %q", opus.Timeout, "20m")
	}
	if got := opus.EffectiveSummarizationStrategy(); got != SummarizationStrategyLastMessage {
		t.Errorf("unset branch strategy = %q, want %q (branches do NOT take the global step default)",
			got, SummarizationStrategyLastMessage)
	}

	codex := group.Parallel.EffectiveBranch(1, &group)
	if codex.Agent != "fallback" {
		t.Errorf("branch agent = %q, want the group's %q", codex.Agent, "fallback")
	}
	if codex.Timeout != "5m" {
		t.Errorf("branch timeout = %q, want the branch's own %q", codex.Timeout, "5m")
	}
	if got := codex.EffectiveSummarizationStrategy(); got != SummarizationStrategySummarizeChat {
		t.Errorf("explicit branch strategy = %q, want %q", got, SummarizationStrategySummarizeChat)
	}

	// The parsed config must never be rewritten by normalization.
	if wf.Steps[1].Parallel.Branches[1].Agent != "" {
		t.Error("EffectiveBranch must not mutate the authored branch record")
	}
}

func TestParallelAllStepNamesOrder(t *testing.T) {
	wf := parseWorkflow(t, twoBranchGroupYAML)
	got := wf.AllStepNames()
	want := []string{"implement", "review", "review-opus", "review-codex", "synthesize"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("AllStepNames = %v, want %v (group immediately followed by its branches)", got, want)
	}
}

func TestParallelBranchGroupLookup(t *testing.T) {
	wf := parseWorkflow(t, twoBranchGroupYAML)
	group, idx, ok := wf.BranchGroup("review-codex")
	if !ok {
		t.Fatal("BranchGroup(review-codex) not found")
	}
	if group.Name != "review" || idx != 1 {
		t.Errorf("BranchGroup = (%q, %d), want (review, 1)", group.Name, idx)
	}
	if _, _, ok := wf.BranchGroup("implement"); ok {
		t.Error("a top-level step must not resolve as a branch")
	}
}

func TestParallelRequireValues(t *testing.T) {
	base := `
name: wf
steps:
  - name: review
    parallel:
      require: %s
      branches:
        - name: a
          prompt: "a"
        - name: b
          prompt: "b"
`
	cases := []struct {
		require string
		want    int
		wantErr string
	}{
		{require: `""`, want: 2},
		{require: "all", want: 2},
		{require: "any", want: 1},
		{require: `"2"`, want: 2},
		{require: `"1"`, want: 1},
		{require: `"0"`, wantErr: "out of range"},
		{require: `"3"`, wantErr: "out of range"},
		{require: "some", wantErr: "invalid parallel.require"},
	}
	for _, tc := range cases {
		t.Run(tc.require, func(t *testing.T) {
			wf := parseWorkflow(t, strings.Replace(base, "%s", tc.require, 1))
			err := wf.ValidateSteps()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ValidateSteps err = %v, want it to mention %q", err, tc.wantErr)
				}
				if !strings.Contains(err.Error(), `"review"`) {
					t.Errorf("error = %q, expected it to name the group", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateSteps: %v", err)
			}
			if got := wf.Steps[0].Parallel.RequiredCount(); got != tc.want {
				t.Errorf("RequiredCount = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestParallelValidationRejections(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "group with prompt",
			yaml: `
name: wf
steps:
  - name: review
    prompt: "nope"
    parallel:
      branches:
        - name: a
          prompt: "a"
`,
			wantErr: "cannot set prompt",
		},
		{
			name: "group with loop",
			yaml: `
name: wf
steps:
  - name: seed
    prompt: "s"
  - name: review
    parallel:
      branches:
        - name: a
          prompt: "a"
    loop:
      goto: seed
      max_iterations: 2
`,
			wantErr: "cannot set loop",
		},
		{
			name: "group with human",
			yaml: `
name: wf
steps:
  - name: review
    human: true
    parallel:
      branches:
        - name: a
          prompt: "a"
`,
			wantErr: "cannot set human",
		},
		{
			name: "group with summarization_strategy",
			yaml: `
name: wf
steps:
  - name: review
    summarization_strategy: none
    parallel:
      branches:
        - name: a
          prompt: "a"
`,
			wantErr: "cannot set summarization_strategy",
		},
		{
			name: "zero branches",
			yaml: `
name: wf
steps:
  - name: review
    parallel:
      require: all
      branches: []
`,
			wantErr: "at least one branch",
		},
		{
			name: "nested group",
			yaml: `
name: wf
steps:
  - name: review
    parallel:
      branches:
        - name: inner
          parallel:
            branches:
              - name: deep
                prompt: "d"
`,
			wantErr: "no nesting",
		},
		{
			name: "branch with loop",
			yaml: `
name: wf
steps:
  - name: review
    parallel:
      branches:
        - name: a
          prompt: "a"
          loop:
            goto: a
            max_iterations: 2
`,
			wantErr: "cannot have a loop",
		},
		{
			name: "branch with human",
			yaml: `
name: wf
steps:
  - name: review
    parallel:
      branches:
        - name: a
          prompt: "a"
          human: true
`,
			wantErr: "cannot have human: true",
		},
		{
			name: "scalar branch entry",
			yaml: `
name: wf
steps:
  - name: review
    parallel:
      branches:
        - some-step
`,
			wantErr: "must be an inline mapping",
		},
		{
			name: "duplicate name across step and branch",
			yaml: `
name: wf
steps:
  - name: implement
    prompt: "i"
  - name: review
    parallel:
      branches:
        - name: implement
          prompt: "a"
`,
			wantErr: `duplicate step name "implement"`,
		},
		{
			name: "duplicate branch names",
			yaml: `
name: wf
steps:
  - name: review
    parallel:
      branches:
        - name: a
          prompt: "a"
        - name: a
          prompt: "a2"
`,
			wantErr: `duplicate step name "a"`,
		},
		{
			name: "branch bad summarization strategy",
			yaml: `
name: wf
steps:
  - name: review
    parallel:
      branches:
        - name: a
          prompt: "a"
          summarization_strategy: bogus
`,
			wantErr: "invalid summarization_strategy",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf := parseWorkflow(t, tc.yaml)
			err := wf.ValidateSteps()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateSteps err = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestParallelLoopGotoRejectsBranch(t *testing.T) {
	wf := parseWorkflow(t, `
name: wf
steps:
  - name: review
    parallel:
      branches:
        - name: review-a
          prompt: "a"
  - name: fix
    prompt: "fix"
    loop:
      goto: review-a
      max_iterations: 2
`)
	err := wf.ValidateLoops()
	if err == nil || !strings.Contains(err.Error(), `targets a branch of parallel group "review"`) {
		t.Fatalf("ValidateLoops err = %v, want the branch-goto rejection", err)
	}
}

func TestParallelExitConditionMayNameBranch(t *testing.T) {
	wf := parseWorkflow(t, `
name: wf
steps:
  - name: review
    parallel:
      branches:
        - name: review-a
          prompt: "a"
  - name: fix
    prompt: "fix"
    loop:
      goto: review
      max_iterations: 2
      exit_condition:
        step_context_contains: review-a
        marker: "DONE"
`)
	if err := wf.ValidateLoops(); err != nil {
		t.Fatalf("an exit condition naming a branch must be accepted, got: %v", err)
	}
}

// writeParallelProject writes a .sakusen.yml with the given workflow body and
// returns its path, for exercising the full Load/Diagnose paths.
func writeParallelProject(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".sakusen.yml")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestParallelDiagnoseRejectsDuplicateBranchName(t *testing.T) {
	// Isolate from the developer's real ~/.sakusen.yml.
	t.Setenv("HOME", t.TempDir())
	path := writeParallelProject(t, `
workflows:
  - name: reviewed
    steps:
      - name: review
        parallel:
          branches:
            - name: review
              prompt: "a"
`)
	_, err := Diagnose(path)
	if err == nil || !strings.Contains(err.Error(), `duplicate step name "review"`) {
		t.Fatalf("Diagnose err = %v, want the duplicate-name rejection", err)
	}
}

func TestParallelLoadRejectsTmuxBranchAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeParallelProject(t, `
agents:
  claude:
    command: "claude -p"
  interactive:
    mode: tmux
    command: "claude"
workflows:
  - name: reviewed
    steps:
      - name: review
        parallel:
          branches:
            - name: review-a
              agent: interactive
              prompt: "a"
`)
	_, err := LoadForProject(filepath.Dir(path))
	if err == nil || !strings.Contains(err.Error(), "parallel branches cannot use a tmux-mode agent") {
		t.Fatalf("LoadForProject err = %v, want the tmux-branch rejection", err)
	}
}

func TestParallelBranchInheritsGroupAgentForTmuxCheck(t *testing.T) {
	// The branch declares no agent, so the tmux check must run against the
	// EFFECTIVE agent (group → workflow → default_agent).
	t.Setenv("HOME", t.TempDir())
	path := writeParallelProject(t, `
agents:
  claude:
    command: "claude -p"
  interactive:
    mode: tmux
    command: "claude"
workflows:
  - name: reviewed
    steps:
      - name: review
        agent: interactive
        parallel:
          branches:
            - name: review-a
              prompt: "a"
`)
	_, err := LoadForProject(filepath.Dir(path))
	if err == nil || !strings.Contains(err.Error(), "parallel branches cannot use a tmux-mode agent") {
		t.Fatalf("LoadForProject err = %v, want the inherited-agent tmux rejection", err)
	}
}

func TestParallelComposedRefInBranchExpands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeParallelProject(t, `
agents:
  claude:
    command: "claude -p"
    variants:
      opus:
        env:
          MODEL: opus
      with-plugins:
        env:
          PLUGINS: "1"
workflows:
  - name: reviewed
    steps:
      - name: review
        parallel:
          branches:
            - name: review-a
              agent: claude:opus:with-plugins
              prompt: "a"
`)
	cfg, err := LoadForProject(filepath.Dir(path))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	agent, ok := cfg.ResolveAgent("claude:opus:with-plugins")
	if !ok {
		t.Fatal("a composed ref used only inside a branch must still be expanded into the registry")
	}
	if agent.Env["MODEL"] != "opus" || agent.Env["PLUGINS"] != "1" {
		t.Errorf("composed record env = %v, want both modifiers applied", agent.Env)
	}
}

func TestParallelBranchPromptIncludeIsValidated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeParallelProject(t, `
workflows:
  - name: reviewed
    steps:
      - name: review
        parallel:
          branches:
            - name: review-a
              prompt: "{{prompt.missing}}"
`)
	_, err := LoadForProject(filepath.Dir(path))
	if err == nil {
		t.Fatal("expected a load error for a missing prompt include inside a branch")
	}
	if !strings.Contains(err.Error(), `branch "review-a": prompt`) {
		t.Errorf("error = %q, expected it to name the branch and field", err)
	}
}

func TestParallelGroupIsNeverTmux(t *testing.T) {
	// The group runs no agent: even when the cascade it would fall back to is
	// a tmux-mode default agent, StepIsTmux / FirstStepIsTmux must say no —
	// otherwise a group-first workflow would be offered as an interactive one.
	t.Setenv("HOME", t.TempDir())
	path := writeParallelProject(t, `
default_agent: interactive
agents:
  claude:
    command: "claude -p"
  interactive:
    mode: tmux
    command: "claude"
workflows:
  - name: reviewed
    steps:
      - name: review
        parallel:
          branches:
            - name: review-a
              agent: claude
              prompt: "a"
`)
	cfg, err := LoadForProject(filepath.Dir(path))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	wf := cfg.GetWorkflow("reviewed")
	if wf == nil {
		t.Fatal("workflow not found")
	}
	if cfg.StepIsTmux(wf, &wf.Steps[0]) {
		t.Error("StepIsTmux(group) = true, want false")
	}
	if cfg.FirstStepIsTmux(wf) {
		t.Error("FirstStepIsTmux = true for a group-first workflow, want false")
	}
}
