package config

import (
	"fmt"
	"path/filepath"
	"reflect"
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
	if !reflect.DeepEqual(*units.Fanout, want) {
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

func TestFanoutIsFanoutSource(t *testing.T) {
	wf := &WorkflowConfig{Steps: []StepConfig{
		{Name: "plan"},
		{Name: "other"},
		{Name: "units", Fanout: &FanoutConfig{From: "plan", Workflow: "unit"}},
	}}
	for step, want := range map[string]bool{"plan": true, "other": false, "units": false, "missing": false} {
		if got := wf.IsFanoutSource(step); got != want {
			t.Errorf("IsFanoutSource(%q) = %v, want %v", step, got, want)
		}
	}
}

// loadFanoutNodesProject is loadFanoutProject plus "unit-merge", a headless
// child workflow with on_complete: merge that needed nodes can run.
func loadFanoutNodesProject(t *testing.T, steps string) (*Config, error) {
	t.Helper()
	yml := strings.Replace(fanoutProject(steps), "  - name: planner\n", `  - name: unit-merge
    on_complete: merge
    steps:
      - name: build
        prompt: "build {{task.input}}"
  - name: planner
`, 1)
	path := writeParallelProject(t, yml)
	return LoadForProject(filepath.Dir(path))
}

func TestFanoutNodes(t *testing.T) {
	cfg, err := loadFanoutNodesProject(t, `
      - name: plan
        prompt: "plan"
      - name: build
        fanout:
          workflow: unit-merge
          retries: 2
          nodes:
            - name: api
              input: "api"
            - name: ui
              input: "ui"
              retries: 0
            - name: schema
              input: "schema {{steps.plan.context}}"
            - name: integrate
              input: "{{steps.api.context}} {{steps.ui.context}} {{steps.schema.context}}"
              needs: [api, ui, schema]
            - name: docs
              input: "docs"
            - name: infra
              input: "infra"
            - name: release-prep
              input: "{{steps.integrate.context}} {{steps.api.context}}"
              needs: [integrate, docs, infra]
              workflow: unit
      - name: next
        prompt: "{{steps.release-prep.context}}"
`)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	wf := findWorkflowByName(cfg, "planner")
	if wf == nil {
		t.Fatal("planner workflow missing")
	}
	want := []string{"plan", "build", "api", "ui", "schema", "integrate", "docs", "infra", "release-prep", "next"}
	if got := wf.AllStepNames(); !reflect.DeepEqual(got, want) {
		t.Errorf("AllStepNames = %v, want %v", got, want)
	}

	f := wf.Steps[1].Fanout
	if !f.IsNodes() {
		t.Fatal("IsNodes = false for a nodes fanout")
	}
	if got := f.EffectiveNodeWorkflow(0); got != "unit-merge" {
		t.Errorf("api workflow = %q, want the step's unit-merge", got)
	}
	if got := f.EffectiveNodeWorkflow(6); got != "unit" {
		t.Errorf("release-prep workflow = %q, want its own unit", got)
	}
	if got := f.EffectiveNodeRetries(0); got != 2 {
		t.Errorf("api retries = %d, want the step's 2", got)
	}
	if got := f.EffectiveNodeRetries(1); got != 0 {
		t.Errorf("ui retries = %d, want its own 0", got)
	}

	var order []string
	for _, i := range f.NodeTopoOrder() {
		order = append(order, f.Nodes[i].Name)
	}
	wantOrder := []string{"api", "ui", "schema", "integrate", "docs", "infra", "release-prep"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Errorf("NodeTopoOrder = %v, want %v", order, wantOrder)
	}
	if wf.FanoutConsumer("") != "" {
		t.Error("FanoutConsumer(\"\") must not match a nodes fanout")
	}
}

func TestFanoutNodeTopoOrderDeclarationTies(t *testing.T) {
	f := &FanoutConfig{Nodes: []FanoutNodeConfig{
		{Name: "c", Needs: []string{"b"}},
		{Name: "a"},
		{Name: "b", Needs: []string{"a"}},
		{Name: "d"},
	}}
	var order []string
	for _, i := range f.NodeTopoOrder() {
		order = append(order, f.Nodes[i].Name)
	}
	if want := []string{"a", "b", "c", "d"}; !reflect.DeepEqual(order, want) {
		t.Errorf("NodeTopoOrder = %v, want %v", order, want)
	}
}

func TestFanoutNodesValidation(t *testing.T) {
	const plan = `
      - name: plan
        prompt: "plan"
`
	// nodes renders a nodes fanout step "units" with the given step-level
	// lines and node list.
	nodes := func(stepLines, list string) string {
		return "      - name: units\n        fanout:\n" + stepLines + "          nodes:\n" + list
	}
	const wfLine = "          workflow: unit-merge\n"
	node := func(name, extra string) string {
		return fmt.Sprintf("            - name: %s\n              input: \"x\"\n%s", name, extra)
	}
	group := `
      - name: review
        parallel:
          branches:
            - name: review-a
              prompt: "a"
`
	ref := func(s string) string { return "              input: \"" + s + "\"\n" }
	refNode := func(name, input, extra string) string {
		return "            - name: " + name + "\n" + ref(input) + extra
	}

	cases := []struct {
		name  string
		steps string
		want  []string
	}{
		{"a/duplicate node", plan + nodes(wfLine, node("api", "")+node("api", "")), []string{`"units"`, `duplicate fanout node "api"`}},
		{"b/collides with step", plan + nodes(wfLine, node("plan", "")), []string{`"units"`, `node "plan"`, "share one namespace"}},
		{"b/collides with branch", plan + group + nodes(wfLine, node("review-a", "")), []string{`"units"`, `node "review-a"`, "share one namespace"}},
		{"b/collides with other fanout node", plan + strings.Replace(nodes(wfLine, node("api", "")), "units", "first", 1) + nodes(wfLine, node("api", "")),
			[]string{`"units"`, `node "api"`, "share one namespace"}},
		{"c/bad name", plan + nodes(wfLine, node("-api", "")), []string{`"units"`, `"-api"`, "is invalid"}},
		{"c/empty name", plan + nodes(wfLine, "            - input: \"x\"\n"), []string{`"units"`, "node 0 is missing a name"}},
		{"d/unknown need", plan + nodes(wfLine, node("api", "              needs: [ghost]\n")), []string{`"units"`, `node "api"`, `unknown node "ghost"`}},
		{"d/need names step", plan + nodes(wfLine, node("api", "              needs: [plan]\n")), []string{`"units"`, `node "api"`, `unknown node "plan"`}},
		{"e/cycle", plan + nodes(wfLine, node("a", "              needs: [b]\n")+node("b", "              needs: [a]\n")),
			[]string{`"units"`, `node "a"`, "a -> b -> a"}},
		{"e/self need", plan + nodes(wfLine, node("a", "              needs: [a]\n")), []string{`"units"`, `node "a"`, "a -> a"}},
		{"f/both", plan + nodes(wfLine+"          from: plan\n", node("api", "")), []string{`"units"`, "both `from:`", `"plan"`}},
		{"f/neither", plan + "      - name: units\n        fanout:\n" + wfLine, []string{`"units"`, "needs `from:`", "`nodes:`"}},
		{"g/step input", plan + nodes(wfLine+"          input: \"{{item.input}}\"\n", node("api", "")), []string{`"units"`, `input "{{item.input}}"`, "`nodes:`"}},
		{"h/no workflow", plan + nodes("", node("api", "")), []string{`"units"`, `node "api" has no workflow`}},
		{"h/undefined workflow", plan + nodes(wfLine, node("api", "              workflow: ghost\n")), []string{`"units"`, `node "api"`, `workflow "ghost" is not defined`}},
		{"h/human workflow", plan + nodes(wfLine, node("api", "              workflow: unit-human\n")), []string{`"units"`, `node "api"`, `"unit-human"`, "human: true"}},
		{"h/tmux workflow", plan + nodes(wfLine, node("api", "              workflow: unit-tmux\n")), []string{`"units"`, `node "api"`, `"unit-tmux"`, "tmux-mode agent"}},
		{"i/unknown ref", plan + nodes(wfLine, refNode("api", "{{steps.ghost.context}}", "")), []string{`"units"`, `node "api"`, "steps.ghost.context"}},
		{"i/non-need sibling", plan + nodes(wfLine, node("api", "")+refNode("ui", "{{steps.api.context}}", "")), []string{`"units"`, `node "ui"`, "steps.api.context"}},
		{"i/fanout itself", plan + nodes(wfLine, refNode("api", "{{steps.units.context}}", "")), []string{`"units"`, `node "api"`, "steps.units.context"}},
		{"i/later step", plan + nodes(wfLine, refNode("api", "{{steps.after.context}}", "")) + "      - name: after\n        prompt: \"x\"\n",
			[]string{`"units"`, `node "api"`, "steps.after.context"}},
		{"j/require over count", plan + nodes(wfLine+"          require: \"3\"\n", node("api", "")+node("ui", "")), []string{`"units"`, "require 3", "node count (2)"}},
		{"k/negative node retries", plan + nodes(wfLine, node("api", "              retries: -1\n")), []string{`"units"`, `node "api"`, "got -1"}},
		{"k/negative step retries", plan + nodes(wfLine+"          retries: -2\n", node("api", "")), []string{`"units"`, "got -2"}},
		{"l/empty input", plan + nodes(wfLine, "            - name: api\n              input: \"\"\n"), []string{`"units"`, `node "api" needs ` + "`input:`"}},
		{"m/needed node not merging", plan + nodes(wfLine, node("api", "              workflow: unit\n")+node("ui", "              needs: [api]\n")),
			[]string{`"units"`, `node "api"`, `workflow "unit"`, "on_complete: merge"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadFanoutNodesProject(t, tc.steps)
			if err == nil {
				t.Fatalf("LoadForProject succeeded, want error containing %q", tc.want)
			}
			msg := err.Error()
			for _, want := range append([]string{`"planner"`}, tc.want...) {
				if !strings.Contains(msg, want) {
					t.Errorf("error = %q, want it to contain %q", msg, want)
				}
			}
		})
	}

	legal := []struct{ name, steps string }{
		{"transitive need", plan + nodes(wfLine, node("a", "")+node("b", "              needs: [a]\n")+refNode("c", "{{steps.a.context}}", "              needs: [b]\n"))},
		{"earlier step", plan + nodes(wfLine, refNode("api", "{{steps.plan.context}} {{steps.plan}}", ""))},
		{"earlier branch", plan + group + nodes(wfLine, refNode("api", "{{steps.review-a.context}} {{steps.review.context}}", ""))},
		{"earlier fanout node", plan + strings.Replace(nodes(wfLine, node("first-a", "")), "units", "first", 1) + nodes(wfLine, refNode("api", "{{steps.first-a.context}}", ""))},
	}
	for _, tc := range legal {
		t.Run("legal/"+tc.name, func(t *testing.T) {
			if _, err := loadFanoutNodesProject(t, tc.steps); err != nil {
				t.Fatalf("LoadForProject: %v", err)
			}
		})
	}
}

func TestFanoutAllowEmptyValidation(t *testing.T) {
	_, err := loadFanoutProject(t, `
      - name: plan
        prompt: "plan"
      - name: units
        fanout:
          from: plan
          workflow: unit
          allow_empty: true
`)
	if err != nil {
		t.Fatalf("allow_empty with from rejected: %v", err)
	}
	_, err = loadFanoutNodesProject(t, `
      - name: units
        fanout:
          workflow: unit-merge
          allow_empty: true
          nodes:
            - name: api
              input: "x"
`)
	if err == nil || !strings.Contains(err.Error(), "allow_empty cannot be set with `nodes:`") {
		t.Fatalf("error = %v, want allow_empty/nodes rejection", err)
	}
}
