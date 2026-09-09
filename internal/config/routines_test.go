package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const routineBaseWorkflows = `
workflows:
  - name: default
    steps:
      - name: implementing
        prompt: "do the thing"
`

// loadRoutineConfig loads a project config from an isolated HOME so the user's
// real global config can't leak into pin/workflow assertions.
func loadRoutineConfig(t *testing.T, body string) *Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := writeTempConfig(t, body)
	cfg, err := LoadForProject(filepath.Dir(path))
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	return cfg
}

func TestValidateRoutines_Ref(t *testing.T) {
	path := writeTempConfig(t, routineBaseWorkflows+`
routines:
  - name: nightly
    cadence: "0 3 * * *"
    workflow: default
`)
	if err := ValidateFile(path); err != nil {
		t.Fatalf("expected valid routine, got: %v", err)
	}
}

// A routine without a cadence is on-demand only, not an error.
func TestValidateRoutines_NoCadenceIsValid(t *testing.T) {
	path := writeTempConfig(t, routineBaseWorkflows+`
routines:
  - name: compose-wiki
    workflow: default
`)
	if err := ValidateFile(path); err != nil {
		t.Fatalf("expected on-demand routine to be valid, got: %v", err)
	}
}

func TestValidateRoutines_MissingWorkflow(t *testing.T) {
	path := writeTempConfig(t, routineBaseWorkflows+`
routines:
  - name: bad
    cadence: "@every 5m"
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "`workflow` is required") {
		t.Fatalf("expected required-workflow error, got: %v", err)
	}
}

func TestValidateRoutines_UnknownWorkflow(t *testing.T) {
	path := writeTempConfig(t, routineBaseWorkflows+`
routines:
  - name: bad
    workflow: nonexistent
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected missing workflow error, got: %v", err)
	}
}

// Routines are bindings: every workflow-definition field is rejected outright.
func TestValidateRoutines_RejectsDefinitionFields(t *testing.T) {
	cases := map[string]string{
		"steps": `
    steps:
      - name: ping
        prompt: "say hi"`,
		"agent":             "\n    agent: claude",
		"summarizer_prompt": "\n    summarizer_prompt: \"summarize\"",
	}
	for field, extra := range cases {
		t.Run(field, func(t *testing.T) {
			path := writeTempConfig(t, routineBaseWorkflows+`
routines:
  - name: bad
    workflow: default`+extra+"\n")
			err := ValidateFile(path)
			if err == nil || !strings.Contains(err.Error(), "routines are bindings") {
				t.Fatalf("expected bindings error for %q, got: %v", field, err)
			}
		})
	}
}

func TestValidateRoutines_RejectsRemovedModeFields(t *testing.T) {
	for _, field := range []string{"tmux", "print"} {
		t.Run(field, func(t *testing.T) {
			path := writeTempConfig(t, routineBaseWorkflows+`
routines:
  - name: bad
    workflow: default
    `+field+": true\n")
			err := ValidateFile(path)
			if err == nil || !strings.Contains(err.Error(), "was removed") {
				t.Fatalf("expected removed-field error for %q, got: %v", field, err)
			}
		})
	}
}

func TestValidateRoutines_BadCadence(t *testing.T) {
	path := writeTempConfig(t, routineBaseWorkflows+`
routines:
  - name: bad
    cadence: "not a cadence"
    workflow: default
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "invalid cadence") {
		t.Fatalf("expected invalid cadence error, got: %v", err)
	}
}

func TestValidateRoutines_DuplicateName(t *testing.T) {
	path := writeTempConfig(t, routineBaseWorkflows+`
routines:
  - name: dup
    workflow: default
  - name: dup
    workflow: default
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate name error, got: %v", err)
	}
}

func TestValidateRoutines_EmptyName(t *testing.T) {
	path := writeTempConfig(t, routineBaseWorkflows+`
routines:
  - workflow: default
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "missing a name") {
		t.Fatalf("expected missing-name error, got: %v", err)
	}
}

func TestValidateRoutines_NonKebabName(t *testing.T) {
	path := writeTempConfig(t, routineBaseWorkflows+`
routines:
  - name: Nightly_Run
    workflow: default
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "kebab-case") {
		t.Fatalf("expected kebab-case error, got: %v", err)
	}
}

func TestValidateRoutines_InvalidPriority(t *testing.T) {
	path := writeTempConfig(t, routineBaseWorkflows+`
routines:
  - name: bad
    workflow: default
    priority: "sometimes"
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "invalid priority") {
		t.Fatalf("expected invalid priority error, got: %v", err)
	}
}

func TestValidateRoutines_ValidPriority(t *testing.T) {
	for _, prio := range []string{"low", "medium", "high", "urgent"} {
		path := writeTempConfig(t, routineBaseWorkflows+`
routines:
  - name: ok
    workflow: default
    priority: "`+prio+`"
`)
		if err := ValidateFile(path); err != nil {
			t.Fatalf("expected priority %q to be valid, got: %v", prio, err)
		}
	}
}

func TestValidateRoutines_SubMinuteEveryWarns(t *testing.T) {
	path := writeTempConfig(t, routineBaseWorkflows+`
routines:
  - name: fast
    cadence: "@every 10s"
    workflow: default
`)
	diags, err := Diagnose(path)
	if err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
	for _, d := range diags {
		if strings.Contains(d.Message, "sub-minute") && strings.Contains(d.Message, `routine "fast"`) {
			return
		}
	}
	t.Fatalf("expected sub-minute cadence warning naming the routine, got: %+v", diags)
}

// The removed `periodic:` key is a hard error naming its replacement, on both
// the load path and single-file diagnosis.
func TestRemovedPeriodicKey(t *testing.T) {
	body := routineBaseWorkflows + `
periodic:
  - name: nightly
    cadence: "0 3 * * *"
    workflow: default
`
	path := writeTempConfig(t, body)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "routines:") {
		t.Fatalf("expected removed-key error from Diagnose, got: %v", err)
	}

	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := LoadForProject(filepath.Dir(path)); err == nil || !strings.Contains(err.Error(), "routines:") {
		t.Fatalf("expected removed-key error from Load, got: %v", err)
	}
}

// ── Effective pins ──

func TestRoutinePins_BranchOverridesWorkflowCheckoutAsPair(t *testing.T) {
	cfg := loadRoutineConfig(t, `
workflows:
  - name: sweep
    checkout: staging
    target: main
    steps:
      - name: run
        prompt: "run it"
routines:
  - name: nightly
    workflow: sweep
    branch: "sakusen/nightly-{{task.id}}"
`)
	r := cfg.GetRoutine("nightly")
	if r == nil {
		t.Fatal("routine nightly not found")
	}
	pins := r.EffectivePins(cfg.GetTaskWorkflow("sweep"))
	if pins.Branch != "sakusen/nightly-{{task.id}}" {
		t.Errorf("branch = %q, want the routine's", pins.Branch)
	}
	if pins.Checkout != "" {
		t.Errorf("checkout = %q, want the workflow's to be dropped with its pair", pins.Checkout)
	}
	if pins.Target != "main" {
		t.Errorf("target = %q, want the workflow's to survive", pins.Target)
	}
}

func TestRoutinePins_TargetOverrideKeepsWorkflowBranch(t *testing.T) {
	cfg := loadRoutineConfig(t, `
workflows:
  - name: sweep
    branch: "wf/{{task.id}}"
    target: main
    input: "canned"
    steps:
      - name: run
        prompt: "run it"
routines:
  - name: nightly
    workflow: sweep
    target: release
`)
	pins := cfg.GetRoutine("nightly").EffectivePins(cfg.GetTaskWorkflow("sweep"))
	if pins.Target != "release" {
		t.Errorf("target = %q, want the routine's", pins.Target)
	}
	if pins.Branch != "wf/{{task.id}}" {
		t.Errorf("branch = %q, want the workflow's", pins.Branch)
	}
	if pins.Input != "canned" {
		t.Errorf("input = %q, want the workflow's", pins.Input)
	}
}

func TestValidateRoutines_WorktreeFalseAgainstWorkflowBranchPin(t *testing.T) {
	path := writeTempConfig(t, `
workflows:
  - name: sweep
    branch: "wf/{{task.id}}"
    steps:
      - name: run
        prompt: "run it"
routines:
  - name: nightly
    workflow: sweep
    worktree: false
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), `routine "nightly"`) || !strings.Contains(err.Error(), "worktree: false") {
		t.Fatalf("expected pin conflict naming the routine, got: %v", err)
	}
}

func TestValidateRoutines_WorktreeFalseAloneIsValid(t *testing.T) {
	path := writeTempConfig(t, routineBaseWorkflows+`
routines:
  - name: nightly
    workflow: default
    worktree: false
`)
	if err := ValidateFile(path); err != nil {
		t.Fatalf("expected worktree: false alone to be valid, got: %v", err)
	}
}

func TestValidateRoutines_RejectBranchAndCheckout(t *testing.T) {
	path := writeTempConfig(t, routineBaseWorkflows+`
routines:
  - name: bad
    workflow: default
    branch: "sakusen/bad"
    checkout: staging
`)
	err := ValidateFile(path)
	if err == nil || !strings.Contains(err.Error(), "both branch and checkout") {
		t.Fatalf("expected branch/checkout conflict error, got: %v", err)
	}
}

// ── Argument rule ──

const inputRefWorkflow = `
workflows:
  - name: digest
    steps:
      - name: run
        prompt: "work on {{task.input}}"
`

const inputRefBranchWorkflow = `
workflows:
  - name: digest
    steps:
      - name: review
        parallel:
          branches:
            - name: bugs
              prompt: "review {{task.input}} for bugs"
`

func TestValidateRoutines_ScheduledWithoutInputErrors(t *testing.T) {
	for name, wf := range map[string]string{"step": inputRefWorkflow, "branch": inputRefBranchWorkflow} {
		t.Run(name, func(t *testing.T) {
			path := writeTempConfig(t, wf+`
routines:
  - name: nightly
    cadence: "@every 5m"
    workflow: digest
`)
			err := ValidateFile(path)
			if err == nil || !strings.Contains(err.Error(), "{{task.input}}") {
				t.Fatalf("expected task.input error, got: %v", err)
			}
		})
	}
}

func TestRoutineRequiresInput_OnDemand(t *testing.T) {
	cfg := loadRoutineConfig(t, inputRefWorkflow+`
routines:
  - name: digest-now
    workflow: digest
`)
	if !cfg.RoutineRequiresInput(cfg.GetRoutine("digest-now")) {
		t.Fatal("expected the routine to require an input argument")
	}
}

func TestRoutineRequiresInput_SatisfiedByPins(t *testing.T) {
	cfg := loadRoutineConfig(t, inputRefWorkflow+`
routines:
  - name: routine-input
    workflow: digest
    input: "the daily digest"
`)
	if cfg.RoutineRequiresInput(cfg.GetRoutine("routine-input")) {
		t.Error("a routine input: must satisfy the requirement")
	}

	cfg = loadRoutineConfig(t, `
workflows:
  - name: digest
    input: "canned input"
    steps:
      - name: run
        prompt: "work on {{task.input}}"
routines:
  - name: nightly
    cadence: "@every 5m"
    workflow: digest
`)
	if cfg.RoutineRequiresInput(cfg.GetRoutine("nightly")) {
		t.Error("a workflow input pin must satisfy the requirement")
	}
}

// ── Hidden by placement ──

func TestDiagnose_RoutineReferencedPoolFileIsNotWarned(t *testing.T) {
	projectDir := setupGlobalAndProject(t, "", nil, routineBaseWorkflows+`
routines:
  - name: compose-wiki
    workflow: wiki-compose
`, map[string]string{
		".sakusen/workflows/wiki-compose.yml": `
steps:
  - name: compose
    prompt: "compose the wiki"
`,
		".sakusen/workflows/orphan.yml": `
steps:
  - name: run
    prompt: "run it"
`,
	})

	diags, err := Diagnose(filepath.Join(projectDir, ".sakusen.yml"))
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	sawOrphan := false
	for _, d := range diags {
		if strings.Contains(d.Message, "wiki-compose") {
			t.Errorf("routine-referenced pool file must not warn, got: %s", d.Message)
		}
		if strings.Contains(d.Message, "orphan") {
			sawOrphan = true
		}
	}
	if !sawOrphan {
		t.Errorf("unreferenced pool file must still warn, got: %+v", diags)
	}
}

func TestValidateRoutines_MayReferenceHiddenPoolFile(t *testing.T) {
	projectDir := setupGlobalAndProject(t, "", nil, routineBaseWorkflows+`
routines:
  - name: compose-wiki
    workflow: wiki-compose
`, map[string]string{
		".sakusen/workflows/wiki-compose.yml": `
steps:
  - name: compose
    prompt: "compose the wiki"
`,
	})
	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	wf := cfg.GetTaskWorkflow("wiki-compose")
	if wf == nil || !wf.Hidden {
		t.Fatalf("expected the pool file to resolve and stay hidden, got %+v", wf)
	}
}

// Track workflows are appended after project resolution, so a routine may
// bind one only if routine validation runs once every tier is assembled —
// on the full load path and in single-file diagnosis alike.
func TestValidateRoutines_MayReferenceTrackWorkflow(t *testing.T) {
	trackFile := map[string]string{
		".sakusen/tracks/wiki/workflows/compose.yml": `
steps:
  - name: compose
    prompt: "compose {{task.input}}"
`,
	}

	projectDir := setupGlobalAndProject(t, "", nil, routineBaseWorkflows+`
routines:
  - name: compose-wiki
    workflow: wiki:compose
`, trackFile)
	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	r := cfg.GetRoutine("compose-wiki")
	if r == nil || !cfg.RoutineRequiresInput(r) {
		t.Fatalf("expected the routine to resolve its track workflow and require input, got %+v", r)
	}
	if err := ValidateFile(filepath.Join(projectDir, ".sakusen.yml")); err != nil {
		t.Fatalf("ValidateFile: %v", err)
	}

	// The guard still applies to a track-backed routine: scheduling it with
	// no input is rejected by both paths.
	projectDir = setupGlobalAndProject(t, "", nil, routineBaseWorkflows+`
routines:
  - name: nightly
    cadence: "@daily"
    workflow: wiki:compose
`, trackFile)
	if _, err := LoadForProject(projectDir); err == nil || !strings.Contains(err.Error(), "{{task.input}}") {
		t.Fatalf("expected task.input error from Load, got: %v", err)
	}
	if err := ValidateFile(filepath.Join(projectDir, ".sakusen.yml")); err == nil || !strings.Contains(err.Error(), "{{task.input}}") {
		t.Fatalf("expected task.input error from ValidateFile, got: %v", err)
	}
}

// ── Accessors ──

func TestRoutineAccessors(t *testing.T) {
	cfg := loadRoutineConfig(t, routineBaseWorkflows+`
routines:
  - name: nightly
    workflow: default
    cadence: "0 3 * * *"
  - name: compose-wiki
    description: Rebuild the wiki
    workflow: default
`)
	names := cfg.ListRoutineNames()
	if len(names) != 2 || names[0] != "nightly" || names[1] != "compose-wiki" {
		t.Fatalf("ListRoutineNames = %v, want config order", names)
	}
	if cfg.GetRoutine("missing") != nil {
		t.Error("GetRoutine must return nil for an unknown name")
	}
	r := cfg.GetRoutine("compose-wiki")
	if r == nil || r.Description != "Rebuild the wiki" {
		t.Fatalf("GetRoutine(compose-wiki) = %+v", r)
	}
	if r.IsScheduled() {
		t.Error("a routine without a cadence is not scheduled")
	}
	if !cfg.GetRoutine("nightly").IsScheduled() {
		t.Error("a routine with a cadence is scheduled")
	}
}

// ── Cadence parsing ──

func TestParseRoutineCadence(t *testing.T) {
	for _, c := range []string{"0 3 * * *", "@every 5m", "@daily", "*/15 * * * *"} {
		if _, err := ParseRoutineCadence(c); err != nil {
			t.Errorf("expected %q to parse, got: %v", c, err)
		}
	}
	if _, err := ParseRoutineCadence("garbage"); err == nil {
		t.Error("expected garbage cadence to error")
	}
}

func TestNextRoutineFire(t *testing.T) {
	from := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
	next, err := NextRoutineFire("@every 5m", from)
	if err != nil {
		t.Fatalf("NextRoutineFire: %v", err)
	}
	if !next.After(from) {
		t.Fatalf("expected next %v to be after %v", next, from)
	}
}
