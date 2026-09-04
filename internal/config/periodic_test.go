package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const periodicBaseWorkflows = `
workflows:
  - name: default
    steps:
      - name: implementing
        prompt: "do the thing"
`

func TestValidatePeriodic_RefMode(t *testing.T) {
	path := writeTempConfig(t, periodicBaseWorkflows+`
periodic:
  - name: nightly
    cadence: "0 3 * * *"
    workflow: default
`)
	if err := ValidateFile(path); err != nil {
		t.Fatalf("expected valid ref-mode periodic, got: %v", err)
	}
}

func TestValidatePeriodic_InlineMode(t *testing.T) {
	path := writeTempConfig(t, periodicBaseWorkflows+`
periodic:
  - name: heartbeat
    cadence: "@every 5m"
    steps:
      - name: ping
        prompt: "say hi"
`)
	if err := ValidateFile(path); err != nil {
		t.Fatalf("expected valid inline-mode periodic, got: %v", err)
	}
}

func TestValidatePeriodic_RejectBoth(t *testing.T) {
	path := writeTempConfig(t, periodicBaseWorkflows+`
periodic:
  - name: bad
    cadence: "@every 5m"
    workflow: default
    steps:
      - name: ping
        prompt: "say hi"
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected 'exactly one' error, got: %v", err)
	}
}

func TestValidatePeriodic_RejectNeither(t *testing.T) {
	path := writeTempConfig(t, periodicBaseWorkflows+`
periodic:
  - name: bad
    cadence: "@every 5m"
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected 'exactly one' error, got: %v", err)
	}
}

func TestValidatePeriodic_BadCadence(t *testing.T) {
	path := writeTempConfig(t, periodicBaseWorkflows+`
periodic:
  - name: bad
    cadence: "not a cadence"
    workflow: default
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "invalid cadence") {
		t.Fatalf("expected invalid cadence error, got: %v", err)
	}
}

func TestValidatePeriodic_RefMissingWorkflow(t *testing.T) {
	path := writeTempConfig(t, periodicBaseWorkflows+`
periodic:
  - name: bad
    cadence: "@every 5m"
    workflow: nonexistent
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected missing workflow error, got: %v", err)
	}
}

func TestValidatePeriodic_InputRefWithoutInput(t *testing.T) {
	path := writeTempConfig(t, periodicBaseWorkflows+`
periodic:
  - name: bad
    cadence: "@every 5m"
    steps:
      - name: ping
        prompt: "work on {{task.input}}"
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "task.input") {
		t.Fatalf("expected task.input error, got: %v", err)
	}
}

// The guard must inspect every step, not only the first one (a later step
// referencing {{task.input}} would silently resolve to "").
func TestValidatePeriodic_InputRefInLaterStepWithoutInput(t *testing.T) {
	path := writeTempConfig(t, periodicBaseWorkflows+`
periodic:
  - name: bad
    cadence: "@every 5m"
    steps:
      - name: ping
        prompt: "say hi"
      - name: pong
        prompt: "work on {{task.input}}"
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "task.input") {
		t.Fatalf("expected task.input error for later step, got: %v", err)
	}
}

func TestValidatePeriodic_InputRefWithInputOK(t *testing.T) {
	path := writeTempConfig(t, periodicBaseWorkflows+`
periodic:
  - name: ok
    cadence: "@every 5m"
    input: "the daily digest"
    steps:
      - name: ping
        prompt: "work on {{task.input}}"
`)
	if err := ValidateFile(path); err != nil {
		t.Fatalf("expected valid config with input, got: %v", err)
	}
}

// A ref-mode periodic without input is fine when the referenced workflow pins
// its own input (createTaskFromRequest falls back to the pin).
func TestValidatePeriodic_RefModeWorkflowInputPinOK(t *testing.T) {
	path := writeTempConfig(t, `
workflows:
  - name: pinned
    input: "canned input"
    steps:
      - name: implementing
        prompt: "work on {{task.input}}"
periodic:
  - name: ok
    cadence: "@every 5m"
    workflow: pinned
`)
	if err := ValidateFile(path); err != nil {
		t.Fatalf("expected valid config with workflow input pin, got: %v", err)
	}
}

func TestValidatePeriodic_InvalidPriority(t *testing.T) {
	path := writeTempConfig(t, periodicBaseWorkflows+`
periodic:
  - name: bad
    cadence: "@every 5m"
    workflow: default
    priority: "sometimes"
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "invalid priority") {
		t.Fatalf("expected invalid priority error, got: %v", err)
	}
}

func TestValidatePeriodic_ValidPriority(t *testing.T) {
	for _, prio := range []string{"low", "medium", "high", "urgent"} {
		path := writeTempConfig(t, periodicBaseWorkflows+`
periodic:
  - name: ok
    cadence: "@every 5m"
    workflow: default
    priority: "`+prio+`"
`)
		if err := ValidateFile(path); err != nil {
			t.Fatalf("expected priority %q to be valid, got: %v", prio, err)
		}
	}
}

func TestValidatePeriodic_DuplicateName(t *testing.T) {
	path := writeTempConfig(t, periodicBaseWorkflows+`
periodic:
  - name: dup
    cadence: "@every 5m"
    workflow: default
  - name: dup
    cadence: "@every 10m"
    workflow: default
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate name error, got: %v", err)
	}
}

func TestValidatePeriodic_SubMinuteEveryWarns(t *testing.T) {
	path := writeTempConfig(t, periodicBaseWorkflows+`
periodic:
  - name: fast
    cadence: "@every 10s"
    workflow: default
`)
	diags, err := Diagnose(path)
	if err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, "sub-minute") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected sub-minute cadence warning, got: %+v", diags)
	}
}

func TestResolvePeriodic_InlineRegistersHiddenWorkflow(t *testing.T) {
	// Isolate HOME/XDG so the user's real global config can't leak in.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := writeTempConfig(t, periodicBaseWorkflows+`
periodic:
  - name: heartbeat
    cadence: "@every 5m"
    steps:
      - name: ping
        prompt: "say hi"
`)
	cfg, err := LoadForProject(filepath.Dir(path))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if len(cfg.Periodic) != 1 {
		t.Fatalf("expected 1 periodic entry, got %d", len(cfg.Periodic))
	}
	wf := cfg.GetWorkflow("periodic:heartbeat")
	if wf == nil || wf.Name != "periodic:heartbeat" {
		t.Fatalf("expected hidden workflow periodic:heartbeat, got %+v", wf)
	}
	if !wf.Hidden {
		t.Fatalf("expected periodic:heartbeat to be hidden")
	}
	if len(wf.Steps) != 1 || wf.Steps[0].Name != "ping" {
		t.Fatalf("inline steps not registered: %+v", wf.Steps)
	}
}

func TestParsePeriodicCadence(t *testing.T) {
	cases := []string{"0 3 * * *", "@every 5m", "@daily", "*/15 * * * *"}
	for _, c := range cases {
		if _, err := ParsePeriodicCadence(c); err != nil {
			t.Errorf("expected %q to parse, got: %v", c, err)
		}
	}
	if _, err := ParsePeriodicCadence("@every 30s"); err != nil {
		// @every 30s is technically parseable; minute-floor is enforced by the
		// cron parser only for field-based specs. Just ensure no panic.
		_ = err
	}
	if _, err := ParsePeriodicCadence("garbage"); err == nil {
		t.Error("expected garbage cadence to error")
	}
}

func TestNextPeriodicFire(t *testing.T) {
	from := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
	next, err := NextPeriodicFire("@every 5m", from)
	if err != nil {
		t.Fatalf("NextPeriodicFire: %v", err)
	}
	if !next.After(from) {
		t.Fatalf("expected next %v to be after %v", next, from)
	}
}

// An inline-mode periodic is registered as the hidden workflow
// "periodic:<name>" by design and fires regardless of Hidden, so validate must
// not flag it as an unreferenced workflow.
func TestDiagnose_InlinePeriodicProducesNoWarning(t *testing.T) {
	// Isolate HOME/XDG so the user's real global config can't leak in.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := writeTempConfig(t, periodicBaseWorkflows+`
periodic:
  - name: heartbeat
    cadence: "@every 5m"
    steps:
      - name: ping
        prompt: "say hi"
`)
	diags, err := Diagnose(path)
	if err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("expected no diagnostics, got: %+v", diags)
	}
}

// Inline-mode entries carry the New Task pins onto the hidden
// "periodic:<name>" workflow, which is the only lever a materialized fire has
// (CreateTaskRequest never sets them) for escaping the project default.
func TestResolvePeriodic_InlinePinsCopiedToHiddenWorkflow(t *testing.T) {
	// Isolate HOME/XDG so the user's real global config can't leak in.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := writeTempConfig(t, periodicBaseWorkflows+`
periodic:
  - name: docs-refresh
    cadence: "@every 5m"
    worktree: false
    steps:
      - name: refresh
        prompt: "refresh the docs"
  - name: nightly
    cadence: "0 3 * * *"
    worktree: true
    branch: "periodic/nightly-{{task.id}}"
    target: main
    steps:
      - name: run
        prompt: "run it"
`)
	cfg, err := LoadForProject(filepath.Dir(path))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}

	docs := cfg.GetWorkflow("periodic:docs-refresh")
	if docs == nil {
		t.Fatal("expected hidden workflow periodic:docs-refresh")
	}
	if docs.Worktree == nil || *docs.Worktree {
		t.Fatalf("expected worktree pinned false, got %v", docs.Worktree)
	}

	nightly := cfg.GetWorkflow("periodic:nightly")
	if nightly == nil {
		t.Fatal("expected hidden workflow periodic:nightly")
	}
	if nightly.Worktree == nil || !*nightly.Worktree {
		t.Fatalf("expected worktree pinned true, got %v", nightly.Worktree)
	}
	if nightly.Branch != "periodic/nightly-{{task.id}}" {
		t.Errorf("branch pin not copied, got %q", nightly.Branch)
	}
	if nightly.Target != "main" {
		t.Errorf("target pin not copied, got %q", nightly.Target)
	}
}

func TestResolvePeriodic_InlineCheckoutPinCopied(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := writeTempConfig(t, periodicBaseWorkflows+`
periodic:
  - name: staging-sweep
    cadence: "@every 1h"
    checkout: staging
    steps:
      - name: sweep
        prompt: "sweep"
`)
	cfg, err := LoadForProject(filepath.Dir(path))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	wf := cfg.GetWorkflow("periodic:staging-sweep")
	if wf == nil || wf.Checkout != "staging" {
		t.Fatalf("checkout pin not copied: %+v", wf)
	}
}

// In ref mode the referenced workflow's own pins apply and the entry's pins are
// ignored — the same way Agent and SummarizerPrompt are ignored there.
func TestResolvePeriodic_RefModeIgnoresEntryPins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := writeTempConfig(t, `
workflows:
  - name: default
    worktree: true
    steps:
      - name: implementing
        prompt: "do the thing"
periodic:
  - name: nightly
    cadence: "0 3 * * *"
    workflow: default
    worktree: false
`)
	cfg, err := LoadForProject(filepath.Dir(path))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	// GetWorkflow falls back to the default workflow for unknown names, so
	// scan the resolved list directly.
	for i := range cfg.Workflows {
		if cfg.Workflows[i].Name == "periodic:nightly" {
			t.Fatalf("ref mode must not register a hidden workflow, got %+v", cfg.Workflows[i])
		}
	}
	wf := cfg.GetWorkflow("default")
	if wf == nil || wf.Worktree == nil || !*wf.Worktree {
		t.Fatalf("referenced workflow's own pin must survive, got %+v", wf)
	}
}

// The inline workflow goes through the ordinary ValidatePins path, so pin
// combinations that are illegal on a workflow are illegal on a periodic entry.
func TestValidatePeriodic_RejectBranchWithWorktreeFalse(t *testing.T) {
	path := writeTempConfig(t, periodicBaseWorkflows+`
periodic:
  - name: bad
    cadence: "@every 5m"
    worktree: false
    branch: "periodic/bad"
    steps:
      - name: ping
        prompt: "say hi"
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "worktree: false") {
		t.Fatalf("expected worktree/branch pin conflict error, got: %v", err)
	}
}

func TestValidatePeriodic_RejectBranchAndCheckout(t *testing.T) {
	path := writeTempConfig(t, periodicBaseWorkflows+`
periodic:
  - name: bad
    cadence: "@every 5m"
    branch: "periodic/bad"
    checkout: staging
    steps:
      - name: ping
        prompt: "say hi"
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "both branch and checkout") {
		t.Fatalf("expected branch/checkout conflict error, got: %v", err)
	}
}
