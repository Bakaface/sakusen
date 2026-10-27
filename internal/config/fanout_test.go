package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// fanoutProject returns a .sakusen.yml whose "planner" workflow has the given
// steps (indented as list items under `steps:`), alongside child workflows a
// fanout can target.
func fanoutProject(steps string) string {
	return fmt.Sprintf(`
agents:
  claude:
    command: "claude -p"
  interactive:
    mode: tmux
    command: "claude"
workflows:
  - name: unit
    steps:
      - name: build
        prompt: "build {{task.input}}"
  - name: unit-tmux
    steps:
      - name: build
        agent: interactive
        prompt: "build"
  - name: unit-human
    steps:
      - name: build
        prompt: "build"
      - name: approve
        prompt: "approve"
        human: true
  - name: planner
    steps:
%s`, steps)
}

func loadFanoutProject(t *testing.T, steps string) (*Config, error) {
	t.Helper()
	path := writeParallelProject(t, fanoutProject(steps))
	return LoadForProject(filepath.Dir(path))
}

func TestFanoutValidConfigLoads(t *testing.T) {
	cfg, err := loadFanoutProject(t, `
      - name: plan
        prompt: "plan"
      - name: units
        fanout:
          from: plan
          workflow: unit
          input: "{{item.title}}"
          merge: parent
          require: any
          retries: 2
      - name: defaults
        fanout:
          from: plan
          workflow: unit
      - name: next
        prompt: "{{steps.units.context}}"
`)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	wf := findWorkflowByName(cfg, "planner")
	if wf == nil {
		t.Fatal("planner workflow missing")
	}

	units := wf.Steps[1]
	if !units.IsFanout() || units.IsParallel() {
		t.Fatalf("units: IsFanout=%v IsParallel=%v, want a fanout step", units.IsFanout(), units.IsParallel())
	}
	want := FanoutConfig{From: "plan", Workflow: "unit", Input: "{{item.title}}", Merge: FanoutMergeParent, Require: RequireAny, Retries: 2}
	if *units.Fanout != want {
		t.Errorf("units.Fanout = %+v, want %+v", *units.Fanout, want)
	}
	if got := units.Fanout.RequiredCount(5); got != 1 {
		t.Errorf("RequiredCount(5) for require:any = %d, want 1", got)
	}
	if cfg.StepIsTmux(wf, &units) {
		t.Error("a fanout step must never be a tmux step")
	}

	d := wf.Steps[2].Fanout
	if got := d.EffectiveInput(); got != DefaultFanoutInput {
		t.Errorf("EffectiveInput = %q, want %q", got, DefaultFanoutInput)
	}
	if got := d.EffectiveMerge(); got != FanoutMergeBase {
		t.Errorf("EffectiveMerge = %q, want %q", got, FanoutMergeBase)
	}
	if got := d.EffectiveRequire(); got != RequireAll {
		t.Errorf("EffectiveRequire = %q, want %q", got, RequireAll)
	}
	if d.Retries != 0 {
		t.Errorf("Retries = %d, want 0", d.Retries)
	}
	if d.Input != "" || d.Merge != "" || d.Require != "" {
		t.Errorf("accessors must not rewrite the parsed config: %+v", *d)
	}
	if got := d.RequiredCount(4); got != 4 {
		t.Errorf("RequiredCount(4) for require:all = %d, want 4", got)
	}
	two := FanoutConfig{Require: "2"}
	if got := two.RequiredCount(4); got != 2 {
		t.Errorf(`RequiredCount(4) for require:"2" = %d, want 2`, got)
	}
}

func TestFanoutValidation(t *testing.T) {
	const plan = `
      - name: plan
        prompt: "plan"
`
	// fan renders a fanout step named "units" with the given body lines.
	fan := func(body string) string {
		return "      - name: units\n" + body
	}
	ok := "        fanout:\n          from: plan\n          workflow: unit\n"

	cases := []struct {
		name    string
		steps   string
		wantErr string
	}{
		{"a/missing from", plan + fan("        fanout:\n          workflow: unit\n"), "needs `from:`"},
		{"b/unknown from", plan + fan("        fanout:\n          from: nope\n          workflow: unit\n"), `unknown step "nope"`},
		{"c/later from", plan + fan("        fanout:\n          from: after\n          workflow: unit\n") +
			"      - name: after\n        prompt: \"x\"\n", "must name an earlier step"},
		{"c/self from", plan + fan("        fanout:\n          from: units\n          workflow: unit\n"), "must name an earlier step"},
		{"d/from fanout", plan + "      - name: first\n" + ok + fan("        fanout:\n          from: first\n          workflow: unit\n"),
			"names another fanout step"},
		{"e/from group", `
      - name: review
        parallel:
          branches:
            - name: review-a
              prompt: "a"
` + fan("        fanout:\n          from: review\n          workflow: unit\n"), "names a parallel group"},
		{"e/from branch", `
      - name: review
        parallel:
          branches:
            - name: review-a
              prompt: "a"
` + fan("        fanout:\n          from: review-a\n          workflow: unit\n"), "names a parallel branch"},
		{"f/missing workflow", plan + fan("        fanout:\n          from: plan\n"), "needs `workflow:`"},
		{"f/unknown workflow", plan + fan("        fanout:\n          from: plan\n          workflow: ghost\n"), `fanout workflow "ghost" is not defined`},
		{"g/human child", plan + fan("        fanout:\n          from: plan\n          workflow: unit-human\n"), `step "approve" has human: true`},
		{"h/tmux child", plan + fan("        fanout:\n          from: plan\n          workflow: unit-tmux\n"), "resolves to a tmux-mode agent"},
		{"i/prompt", plan + fan("        prompt: \"x\"\n"+ok), "cannot set prompt"},
		{"i/agent", plan + fan("        agent: claude\n"+ok), "cannot set agent"},
		{"i/loop", plan + fan("        loop:\n          goto: plan\n          max_iterations: 2\n"+ok), "cannot set loop"},
		{"i/human", plan + fan("        human: true\n"+ok), "cannot set human"},
		{"i/advance", plan + fan("        advance: auto\n"+ok), "cannot set advance"},
		{"i/parallel", plan + fan("        parallel:\n          branches:\n            - name: b\n              prompt: \"b\"\n"+ok), "cannot set parallel"},
		{"i/summarization_strategy", plan + fan("        summarization_strategy: none\n"+ok), "cannot set summarization_strategy"},
		{"i/summarization_prompt", plan + fan("        summarization_prompt: \"x\"\n"+ok), "cannot set summarization_prompt"},
		{"i/require_context", plan + fan("        require_context: true\n"+ok), "cannot set require_context"},
		{"j/loop over fanout", plan + fan(ok) + `
      - name: check
        prompt: "check"
        loop:
          goto: plan
          max_iterations: 2
`, `loop range contains fanout step "units"`},
		{"k/zero", plan + fan(ok+"          require: \"0\"\n"), `fanout require "0" is invalid`},
		{"k/negative", plan + fan(ok+"          require: \"-1\"\n"), `fanout require "-1" is invalid`},
		{"k/word", plan + fan(ok+"          require: foo\n"), `fanout require "foo" is invalid`},
		{"l/negative retries", plan + fan(ok+"          retries: -1\n"), "fanout retries must be >= 0"},
		{"m/bad merge", plan + fan(ok+"          merge: target\n"), `fanout merge "target" is invalid`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadFanoutProject(t, tc.steps)
			if err == nil {
				t.Fatalf("LoadForProject succeeded, want error containing %q", tc.wantErr)
			}
			msg := err.Error()
			for _, want := range []string{`"planner"`, tc.wantErr} {
				if !strings.Contains(msg, want) {
					t.Errorf("error = %q, want it to contain %q", msg, want)
				}
			}
			// The loop rule names the loop step; every other rule names the fanout.
			step := `"units"`
			if strings.HasPrefix(tc.name, "j/") {
				step = `"check"`
			}
			if !strings.Contains(msg, step) {
				t.Errorf("error = %q, want it to name step %s", msg, step)
			}
		})
	}
}

func TestRequiredCountMatchesParallel(t *testing.T) {
	cases := []struct {
		require string
		n, want int
	}{
		{"", 3, 3},
		{RequireAll, 3, 3},
		{RequireAny, 3, 1},
		{"2", 3, 2},
		{"bogus", 3, 3},
	}
	for _, tc := range cases {
		branches := make([]StepConfig, tc.n)
		p := &ParallelConfig{Require: tc.require, Branches: branches}
		if got := p.RequiredCount(); got != tc.want {
			t.Errorf("ParallelConfig{Require: %q}.RequiredCount() = %d, want %d", tc.require, got, tc.want)
		}
		if got := RequiredCount(tc.require, tc.n); got != tc.want {
			t.Errorf("RequiredCount(%q, %d) = %d, want %d", tc.require, tc.n, got, tc.want)
		}
	}
}
