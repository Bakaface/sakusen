package daemon

import (
	"reflect"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
)

func TestSummarizeWorkflows_PropagatesHiddenAndSource(t *testing.T) {
	in := []config.WorkflowConfig{
		{
			Name:        "active",
			Description: "Active workflow",
			Source:      "inline",
		},
		{
			Name:   "hidden-one",
			Hidden: true,
			Source: "/some/path/.sakusen/workflows/tasks/hidden-one.yml",
		},
	}

	out := summarizeWorkflows(&config.Config{}, in)
	if len(out) != 2 {
		t.Fatalf("want 2 summaries, got %d", len(out))
	}
	if out[0].Hidden {
		t.Errorf("active workflow should not be hidden in summary")
	}
	if out[0].Source != "inline" {
		t.Errorf("want source=inline, got %q", out[0].Source)
	}
	if !out[1].Hidden {
		t.Errorf("hidden workflow should have Hidden=true in summary")
	}
	if out[1].Source == "inline" || out[1].Source == "" {
		t.Errorf("want file path source, got %q", out[1].Source)
	}
}

// TestSummarizeWorkflows_PinFieldsAndFullySpec verifies the daemon-side mapping
// that feeds list_workflows: the worktree/branch/checkout/target pins must be
// copied verbatim and FullySpec must reflect IsFullySpec(). This is the data the
// TUI/MCP use to decide whether to skip the New Task screen, so a dropped field
// here would silently break the skip decision.
func TestSummarizeWorkflows_PinFieldsAndFullySpec(t *testing.T) {
	worktreeTrue := true
	in := []config.WorkflowConfig{
		{
			// Fully specified: input + worktree + new-branch + target.
			Name:     "pinned",
			Input:    "Pinned body",
			Worktree: &worktreeTrue,
			Branch:   "sakusen/{{task_id}}",
			Target:   "main",
			Steps:    []config.StepConfig{{Name: "implement"}},
		},
		{
			// Not fully specified: only an input pin, no worktree decision.
			Name:  "partial",
			Input: "Partial body",
			Steps: []config.StepConfig{{Name: "implement"}},
		},
	}

	out := summarizeWorkflows(&config.Config{}, in)
	if len(out) != 2 {
		t.Fatalf("want 2 summaries, got %d", len(out))
	}

	p := out[0]
	if p.Worktree == nil || !*p.Worktree {
		t.Errorf("pinned.Worktree: got %v, want non-nil true", p.Worktree)
	}
	if p.Branch != "sakusen/{{task_id}}" {
		t.Errorf("pinned.Branch: got %q, want the pinned template", p.Branch)
	}
	if p.Target != "main" {
		t.Errorf("pinned.Target: got %q, want %q", p.Target, "main")
	}
	if !p.FullySpec {
		t.Errorf("pinned.FullySpec: got false, want true (all New Task fields pinned)")
	}

	if out[1].FullySpec {
		t.Errorf("partial.FullySpec: got true, want false (worktree not pinned)")
	}
	if out[1].Worktree != nil {
		t.Errorf("partial.Worktree: got %v, want nil (no worktree pin)", *out[1].Worktree)
	}
}

// TestSummarizeWorkflows_ResolvesAgents verifies the agent projection added
// for the MCP surface: WorkflowSummary.Agent carries the workflow's EXPLICIT
// agent slug (empty when inherited), while each step's Agent is the fully
// RESOLVED slug (step → workflow → default_agent cascade) and Tmux reflects
// the resolved agent's mode.
func TestSummarizeWorkflows_ResolvesAgents(t *testing.T) {
	cfg := &config.Config{
		DefaultAgent: "worker",
		Agents: map[string]config.AgentConfig{
			"worker": {Command: "run-worker"},
			"pair":   {Mode: config.AgentModeTmux, Command: "run-pair"},
		},
	}
	in := []config.WorkflowConfig{
		{
			Name:  "mixed",
			Agent: "pair",
			Steps: []config.StepConfig{
				{Name: "shell"},                      // inherits workflow agent (tmux)
				{Name: "implement", Agent: "worker"}, // step-level override
			},
		},
		{
			Name:  "plain",
			Steps: []config.StepConfig{{Name: "implement"}},
		},
	}

	out := summarizeWorkflows(cfg, in)
	if len(out) != 2 {
		t.Fatalf("want 2 summaries, got %d", len(out))
	}

	mixed := out[0]
	if mixed.Agent != "pair" {
		t.Errorf("mixed.Agent: got %q, want the explicit workflow slug %q", mixed.Agent, "pair")
	}
	if !mixed.FirstStepIsTmux {
		t.Error("mixed.FirstStepIsTmux: got false, want true (first step inherits the tmux workflow agent)")
	}
	if got := mixed.Steps[0]; got.Agent != "pair" || !got.Tmux {
		t.Errorf("mixed.Steps[0]: got agent=%q tmux=%v, want inherited pair/tmux", got.Agent, got.Tmux)
	}
	if got := mixed.Steps[1]; got.Agent != "worker" || got.Tmux {
		t.Errorf("mixed.Steps[1]: got agent=%q tmux=%v, want step override worker/headless", got.Agent, got.Tmux)
	}

	plain := out[1]
	if plain.Agent != "" {
		t.Errorf("plain.Agent: got %q, want empty (no explicit workflow agent)", plain.Agent)
	}
	if plain.FirstStepIsTmux {
		t.Error("plain.FirstStepIsTmux: got true, want false (default agent is headless)")
	}
	if got := plain.Steps[0]; got.Agent != "worker" || got.Tmux {
		t.Errorf("plain.Steps[0]: got agent=%q tmux=%v, want default_agent worker/headless", got.Agent, got.Tmux)
	}
}

// TestSummarizeWorkflows_Advance verifies the advance projection: reported
// only for explicit-advance steps so existing workflows' output is unchanged.
func TestSummarizeWorkflows_Advance(t *testing.T) {
	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			"pair": {Mode: config.AgentModeTmux, Command: "run-pair"},
		},
	}
	in := []config.WorkflowConfig{{
		Name:  "capture",
		Agent: "pair",
		Steps: []config.StepConfig{
			{Name: "wait", Advance: config.AdvanceExplicit},
			{Name: "auto", Advance: config.AdvanceAuto},
			{Name: "unset"},
		},
	}}

	steps := summarizeWorkflows(cfg, in)[0].Steps
	if got := steps[0].Advance; got != config.AdvanceExplicit {
		t.Errorf("wait.Advance = %q, want %q", got, config.AdvanceExplicit)
	}
	for _, st := range steps[1:] {
		if st.Advance != "" {
			t.Errorf("%s.Advance = %q, want empty (omitted for auto/unset)", st.Name, st.Advance)
		}
	}
}

// TestSummarizeWorkflows_Fanout verifies the fanout projection: configured
// fields are reported, defaults are filled in, and no agent is attributed.
func TestSummarizeWorkflows_Fanout(t *testing.T) {
	cfg := &config.Config{DefaultAgent: "worker", Agents: map[string]config.AgentConfig{"worker": {Command: "run-worker"}}}
	in := []config.WorkflowConfig{{
		Name: "planner",
		Steps: []config.StepConfig{
			{Name: "plan"},
			{Name: "units", Fanout: &config.FanoutConfig{From: "plan", Workflow: "unit", Merge: config.FanoutMergeParent, Require: "2", Retries: 1}},
			{Name: "defaults", Fanout: &config.FanoutConfig{From: "plan", Workflow: "unit"}},
		},
	}}

	steps := summarizeWorkflows(cfg, in)[0].Steps
	if steps[0].Fanout != nil {
		t.Errorf("plan.Fanout = %+v, want nil", steps[0].Fanout)
	}
	want := FanoutStepSummary{From: "plan", Workflow: "unit", Merge: "parent", Require: "2", Retries: 1}
	if got := steps[1].Fanout; got == nil || !reflect.DeepEqual(*got, want) {
		t.Errorf("units.Fanout = %+v, want %+v", got, want)
	}
	wantDefaults := FanoutStepSummary{From: "plan", Workflow: "unit", Merge: "base", Require: "all", Retries: 0}
	if got := steps[2].Fanout; got == nil || !reflect.DeepEqual(*got, wantDefaults) {
		t.Errorf("defaults.Fanout = %+v, want %+v", got, wantDefaults)
	}
	for _, st := range steps[1:] {
		if st.Agent != "" || st.Tmux {
			t.Errorf("%s: agent=%q tmux=%v, want no agent", st.Name, st.Agent, st.Tmux)
		}
	}
}

// TestSummarizeWorkflows_FanoutNodes verifies the `nodes:` projection: nodes
// are listed in declaration order with the step's workflow and retries applied
// unless overridden, and From stays empty.
func TestSummarizeWorkflows_FanoutNodes(t *testing.T) {
	cfg := &config.Config{DefaultAgent: "worker", Agents: map[string]config.AgentConfig{"worker": {Command: "run-worker"}}}
	two := 2
	in := []config.WorkflowConfig{{
		Name: "graph",
		Steps: []config.StepConfig{
			{Name: "units", Fanout: &config.FanoutConfig{Workflow: "unit", Retries: 1, Nodes: []config.FanoutNodeConfig{
				{Name: "a", Input: "a", Workflow: "special"},
				{Name: "b", Input: "b", Retries: &two},
				{Name: "c", Input: "c", Needs: []string{"a", "b"}},
			}}},
		},
	}}

	st := summarizeWorkflows(cfg, in)[0].Steps[0]
	want := FanoutStepSummary{
		Nodes: []FanoutNodeSummary{
			{Name: "a", Workflow: "special", Retries: 1},
			{Name: "b", Workflow: "unit", Retries: 2},
			{Name: "c", Workflow: "unit", Needs: []string{"a", "b"}, Retries: 1},
		},
		Workflow: "unit",
		Merge:    "base",
		Require:  "all",
		Retries:  1,
	}
	if st.Fanout == nil || !reflect.DeepEqual(*st.Fanout, want) {
		t.Errorf("units.Fanout = %+v, want %+v", st.Fanout, want)
	}
	if st.Agent != "" || st.Tmux {
		t.Errorf("agent=%q tmux=%v, want no agent", st.Agent, st.Tmux)
	}
}
