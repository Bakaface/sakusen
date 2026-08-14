//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// mixedAgentsYAML defines two headless agent records that both point at the
// generic stub but are distinguishable via the SORTIE_AGENT env var (logged by
// the stub) and a per-record env marker. The workflow default is agent-one;
// the second step overrides with agent: agent-two.
func mixedAgentsYAML(stubPath string) string {
	return fmt.Sprintf(`default_agent: agent-one
agents:
  agent-one:
    mode: headless
    command: "%s"
    env:
      E2E_AGENT_MARKER: one
  agent-two:
    mode: headless
    command: "%s"
    env:
      E2E_AGENT_MARKER: two
poll_interval: 100ms
git:
  base_branch: main
on_complete: merge
workflows:
  - name: mixed
    steps:
      - name: first
        prompt: "First step"
      - name: second
        agent: agent-two
        prompt: "Second step using: {{steps.first.context}}"
`, stubPath, stubPath)
}

// TestMixedAgentsPerStep verifies the agent abstraction end-to-end: two agent
// records, selected per step via the cascade (workflow default vs step-level
// override), both executing through the generic env contract
// (SORTIE_PROMPT_FILE / SORTIE_RESULT_FILE), with step context flowing from
// one agent's step to the other's prompt.
func TestMixedAgentsPerStep(t *testing.T) {
	e := setupE2E(t, "mixed_agents")
	e.WriteSortieYAML(mixedAgentsYAML(e.StubPath))

	e.MustSortie("create", "--title", "mixed agents", "mixed agents task")

	e.WaitStatus(1, "completed", 15*time.Second)
	e.AssertMergedFor(1)

	calls := e.StubCalls("step")
	if len(calls) != 2 {
		t.Fatalf("stub step calls: got %d, want 2", len(calls))
	}
	byStep := map[string]StubCall{}
	for _, c := range calls {
		byStep[c.Step] = c
	}
	if got := byStep["first"].Env["SORTIE_AGENT"]; got != "agent-one" {
		t.Errorf("step first ran under agent %q, want agent-one", got)
	}
	if got := byStep["second"].Env["SORTIE_AGENT"]; got != "agent-two" {
		t.Errorf("step second ran under agent %q, want agent-two", got)
	}

	// The per-record env: map must reach the spawned command — the stub logs
	// SORTIE_* and E2E_* vars into the TSV, so assert the marker directly.
	if got := byStep["first"].Env["E2E_AGENT_MARKER"]; got != "one" {
		t.Errorf("step first E2E_AGENT_MARKER = %q, want %q", got, "one")
	}
	if got := byStep["second"].Env["E2E_AGENT_MARKER"]; got != "two" {
		t.Errorf("step second E2E_AGENT_MARKER = %q, want %q", got, "two")
	}

	// Step context flows across agents: the second step's prompt templates
	// the first step's context, which the stub captured to the prompt dir.
	prompt := e.CapturedPrompt(1, "second")
	if want := "first-agent-output"; !strings.Contains(prompt, want) {
		t.Errorf("second step prompt does not contain first step context %q:\n%s", want, prompt)
	}
}

// workflowAgentYAML exercises the middle rung of the agent cascade: the
// workflow sets agent: agent-two, so a step without its own agent: must run
// under agent-two (NOT the top-level default_agent agent-one), while a
// step-level agent: agent-three still beats the workflow rung.
func workflowAgentYAML(stubPath string) string {
	return fmt.Sprintf(`default_agent: agent-one
agents:
  agent-one:
    mode: headless
    command: "%s"
    env:
      E2E_AGENT_MARKER: one
  agent-two:
    mode: headless
    command: "%s"
    env:
      E2E_AGENT_MARKER: two
  agent-three:
    mode: headless
    command: "%s"
    env:
      E2E_AGENT_MARKER: three
poll_interval: 100ms
git:
  base_branch: main
on_complete: merge
workflows:
  - name: wf-agent
    agent: agent-two
    steps:
      - name: first
        prompt: "First step"
      - name: second
        agent: agent-three
        prompt: "Second step"
`, stubPath, stubPath, stubPath)
}

// TestWorkflowLevelAgent verifies the workflow-level agent: rung of the
// step → workflow → default_agent cascade: the non-overridden step runs under
// the workflow's agent (not default_agent), and a step-level agent: still
// overrides the workflow's choice. Reuses the mixed_agents testdata (same
// step names first/second).
func TestWorkflowLevelAgent(t *testing.T) {
	e := setupE2E(t, "mixed_agents")
	e.WriteSortieYAML(workflowAgentYAML(e.StubPath))

	e.MustSortie("create", "--title", "workflow agent", "workflow agent task")

	e.WaitStatus(1, "completed", 15*time.Second)
	e.AssertMergedFor(1)

	calls := e.StubCalls("step")
	if len(calls) != 2 {
		t.Fatalf("stub step calls: got %d, want 2", len(calls))
	}
	byStep := map[string]StubCall{}
	for _, c := range calls {
		byStep[c.Step] = c
	}
	if got := byStep["first"].Env["SORTIE_AGENT"]; got != "agent-two" {
		t.Errorf("step first ran under agent %q, want workflow-level agent-two", got)
	}
	if got := byStep["first"].Env["E2E_AGENT_MARKER"]; got != "two" {
		t.Errorf("step first E2E_AGENT_MARKER = %q, want %q", got, "two")
	}
	if got := byStep["second"].Env["SORTIE_AGENT"]; got != "agent-three" {
		t.Errorf("step second ran under agent %q, want step-level agent-three", got)
	}
	if got := byStep["second"].Env["E2E_AGENT_MARKER"]; got != "three" {
		t.Errorf("step second E2E_AGENT_MARKER = %q, want %q", got, "three")
	}
}
