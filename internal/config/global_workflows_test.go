package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupGlobalAndProject writes a global ~/.sortie.yml and an optional
// ~/.sortie/workflows/<name>.yml tree under an isolated HOME, plus a
// project .sortie.yml (with optional .sortie/workflows files) under a separate
// project directory. Returns the project directory.
//
// The test's HOME and XDG_CONFIG_HOME are pointed at a temp dir so that
// LoadForProject picks up only the synthesized global config.
func setupGlobalAndProject(
	t *testing.T,
	globalYml string,
	globalFiles map[string]string,
	projectYml string,
	projectFiles map[string]string,
) string {
	t.Helper()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	// Force XDG_CONFIG_HOME to a fresh empty dir to silence config.yaml lookup
	// and avoid the user's real ~/.config/sortie/ leaking in.
	xdgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgDir)

	if globalYml != "" {
		globalPath := filepath.Join(homeDir, ".sortie.yml")
		if err := os.WriteFile(globalPath, []byte(globalYml), 0644); err != nil {
			t.Fatalf("write global .sortie.yml: %v", err)
		}
	}
	for rel, content := range globalFiles {
		full := filepath.Join(homeDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	projectDir := t.TempDir()
	projectPath := filepath.Join(projectDir, ".sortie.yml")
	if err := os.WriteFile(projectPath, []byte(projectYml), 0644); err != nil {
		t.Fatalf("write project .sortie.yml: %v", err)
	}
	for rel, content := range projectFiles {
		full := filepath.Join(projectDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	return projectDir
}

// Example 1: global defines an *inline* workflow, project references it via
// string ref just as it would reference a workflow file under .sortie/workflows/.
func TestGlobalWorkflows_ProjectReferencesGlobalInline(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		`workflows:
  - name: shared-impl
    description: Globally-defined inline workflow
    steps:
      - name: do
        prompt: "global inline implementation"
`,
		nil,
		`workflows:
  - shared-impl
`,
		nil,
	)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}

	if len(cfg.Workflows) != 1 {
		t.Fatalf("want 1 workflow, got %d: %+v", len(cfg.Workflows), cfg.Workflows)
	}
	wf := cfg.Workflows[0]
	if wf.Name != "shared-impl" {
		t.Errorf("want name shared-impl, got %q", wf.Name)
	}
	if wf.Description != "Globally-defined inline workflow" {
		t.Errorf("want global description, got %q", wf.Description)
	}
	if len(wf.Steps) != 1 || wf.Steps[0].Prompt != "global inline implementation" {
		t.Errorf("want global step prompt, got %+v", wf.Steps)
	}
	if wf.Hidden {
		t.Errorf("referenced workflow should not be hidden")
	}
}

// Example 2: global defines a file workflow under ~/.sortie/workflows/, project
// references it via string ref just as it would reference a project-local file.
func TestGlobalWorkflows_ProjectReferencesGlobalFile(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		// Global .sortie.yml is empty so its file-pool workflow becomes
		// "hidden" globally — but project can still reference it by name.
		`# global file pool workflow exists but isn't listed; project can still reference it.
`,
		map[string]string{
			".sortie/workflows/global-file-impl.yml": `
description: Globally-defined file workflow
steps:
  - name: do
    prompt: "global file implementation"
`,
		},
		`workflows:
  - global-file-impl
`,
		nil,
	)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}

	if len(cfg.Workflows) != 1 {
		t.Fatalf("want 1 workflow, got %d: %+v", len(cfg.Workflows), cfg.Workflows)
	}
	wf := cfg.Workflows[0]
	if wf.Name != "global-file-impl" {
		t.Errorf("want name global-file-impl, got %q", wf.Name)
	}
	if wf.Description != "Globally-defined file workflow" {
		t.Errorf("want global description, got %q", wf.Description)
	}
	if len(wf.Steps) != 1 || wf.Steps[0].Prompt != "global file implementation" {
		t.Errorf("want global file step prompt, got %+v", wf.Steps)
	}
	if wf.Hidden {
		t.Errorf("project-referenced workflow should be active, not hidden")
	}
	// Source should point at the global file path so users can trace where the
	// definition came from.
	if !strings.HasSuffix(wf.Source, "global-file-impl.yml") {
		t.Errorf("want source ending in global-file-impl.yml, got %q", wf.Source)
	}
}

// Example 3a: global defines a workflow (inline), project overrides it inline.
func TestGlobalWorkflows_ProjectOverridesGlobalInlineWithInline(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		`workflows:
  - name: shared-impl
    steps:
      - name: do
        prompt: "global version"
`,
		nil,
		`workflows:
  - name: shared-impl
    steps:
      - name: do
        prompt: "project override"
`,
		nil,
	)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if len(cfg.Workflows) != 1 {
		t.Fatalf("want 1 workflow, got %d", len(cfg.Workflows))
	}
	got := cfg.Workflows[0]
	if got.Steps[0].Prompt != "project override" {
		t.Errorf("want project override prompt, got %q", got.Steps[0].Prompt)
	}
	if got.Source != "inline" {
		t.Errorf("want source=inline (project's), got %q", got.Source)
	}
}

// Example 3b: global defines a workflow (inline), project overrides it via a
// project-local file under .sortie/workflows/ + a string ref to that name.
func TestGlobalWorkflows_ProjectOverridesGlobalInlineWithLocalFile(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		`workflows:
  - name: shared-impl
    steps:
      - name: do
        prompt: "global version"
`,
		nil,
		`workflows:
  - shared-impl
`,
		map[string]string{
			".sortie/workflows/shared-impl.yml": `
steps:
  - name: do
    prompt: "project file override"
`,
		},
	)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if len(cfg.Workflows) != 1 {
		t.Fatalf("want 1 workflow, got %d", len(cfg.Workflows))
	}
	got := cfg.Workflows[0]
	if got.Steps[0].Prompt != "project file override" {
		t.Errorf("want project file override prompt, got %q", got.Steps[0].Prompt)
	}
	if !strings.HasSuffix(got.Source, "shared-impl.yml") || strings.Contains(got.Source, "/.sortie.yml") {
		t.Errorf("want project file source, got %q", got.Source)
	}
	if strings.HasPrefix(got.Source, os.Getenv("HOME")+"/.sortie/") {
		t.Errorf("source should be the project-local file, got global path %q", got.Source)
	}
}

// Example 3c: global defines a workflow in its file pool (under
// ~/.sortie/workflows/), project overrides it inline.
func TestGlobalWorkflows_ProjectOverridesGlobalFileWithInline(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		`workflows:
  - shared-impl
`,
		map[string]string{
			".sortie/workflows/shared-impl.yml": `
steps:
  - name: do
    prompt: "global file version"
`,
		},
		`workflows:
  - name: shared-impl
    steps:
      - name: do
        prompt: "project inline override"
`,
		nil,
	)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if len(cfg.Workflows) != 1 {
		t.Fatalf("want 1 workflow, got %d", len(cfg.Workflows))
	}
	got := cfg.Workflows[0]
	if got.Steps[0].Prompt != "project inline override" {
		t.Errorf("want project inline override prompt, got %q", got.Steps[0].Prompt)
	}
	if got.Source != "inline" {
		t.Errorf("want source=inline, got %q", got.Source)
	}
}

// Mixing: project references a global workflow alongside a project-local file
// in the same listing. Both should resolve.
func TestGlobalWorkflows_MixedGlobalAndLocalRefs(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		`workflows:
  - name: from-global
    steps:
      - name: do
        prompt: "from global"
`,
		nil,
		`workflows:
  - from-global
  - from-local
`,
		map[string]string{
			".sortie/workflows/from-local.yml": `
steps:
  - name: do
    prompt: "from local"
`,
		},
	)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if len(cfg.Workflows) != 2 {
		t.Fatalf("want 2 workflows, got %d", len(cfg.Workflows))
	}
	if cfg.Workflows[0].Name != "from-global" {
		t.Errorf("want first=from-global, got %q", cfg.Workflows[0].Name)
	}
	if cfg.Workflows[1].Name != "from-local" {
		t.Errorf("want second=from-local, got %q", cfg.Workflows[1].Name)
	}
}

// Unreferenced global workflows should not appear in the project's listing
// when the project defines its own workflows. They only flow in when explicitly
// referenced.
func TestGlobalWorkflows_UnreferencedGlobalNotIncluded(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		`workflows:
  - name: g1
    steps: [{ name: a, prompt: a }]
  - name: g2
    steps: [{ name: a, prompt: a }]
`,
		nil,
		`workflows:
  - g1
`,
		nil,
	)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	// Only g1 explicitly referenced; g2 not included.
	nonHidden := 0
	for _, wf := range cfg.Workflows {
		if !wf.Hidden {
			nonHidden++
		}
	}
	if nonHidden != 1 {
		t.Fatalf("want 1 active workflow (g1 only), got %d active", nonHidden)
	}
	if cfg.Workflows[0].Name != "g1" {
		t.Errorf("want g1, got %q", cfg.Workflows[0].Name)
	}
}

// Missing string ref that exists neither in project nor global is a hard error.
func TestGlobalWorkflows_MissingRefAcrossBoth(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		`workflows:
  - name: g1
    steps: [{ name: a, prompt: a }]
`,
		nil,
		`workflows:
  - g1
  - missing
`,
		nil,
	)

	_, err := LoadForProject(projectDir)
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("want error about missing workflow, got %v", err)
	}
}

// Flat global workflows should support the same global pool references.
func TestGlobalWorkflows_FlatGlobalAndProjectRefs(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		`workflows:
  - name: cleanup
    steps: [{ name: a, prompt: "global cleanup" }]
  - name: bootstrap
    steps: [{ name: a, prompt: "global bootstrap" }]
`,
		nil,
		`workflows:
  - cleanup
  - bootstrap
`,
		nil,
	)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if len(cfg.Workflows) != 2 {
		t.Fatalf("want 2 workflows, got %d", len(cfg.Workflows))
	}

	// Both should be resolvable via GetWorkflow
	cleanup := cfg.GetWorkflow("cleanup")
	if cleanup == nil || cleanup.Steps[0].Prompt != "global cleanup" {
		t.Errorf("want cleanup resolved from global, got %+v", cleanup)
	}
	bootstrap := cfg.GetWorkflow("bootstrap")
	if bootstrap == nil || bootstrap.Steps[0].Prompt != "global bootstrap" {
		t.Errorf("want bootstrap resolved from global, got %+v", bootstrap)
	}
}

// Step refs: project overrides a global workflow, references two of its steps
// by name, and appends a new inline step. Referenced steps carry their global
// definitions; the new step is added locally; ordering follows the listing.
func TestStepRefs_ReferenceGlobalStepsAndAddNew(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		`workflows:
  - name: the-work
    steps:
      - name: planning
        prompt: "global planning"
      - name: implementing
        prompt: "global implementing"
`,
		nil,
		`workflows:
  - name: the-work
    steps:
      - planning
      - implementing
      - name: reviewing
        prompt: "local reviewing"
`,
		nil,
	)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	wf := cfg.GetTaskWorkflow("the-work")
	if wf == nil {
		t.Fatalf("want the-work workflow, got nil")
	}
	if len(wf.Steps) != 3 {
		t.Fatalf("want 3 steps, got %d: %+v", len(wf.Steps), wf.Steps)
	}
	want := []struct{ name, prompt string }{
		{"planning", "global planning"},
		{"implementing", "global implementing"},
		{"reviewing", "local reviewing"},
	}
	for i, w := range want {
		if wf.Steps[i].Name != w.name || wf.Steps[i].Prompt != w.prompt {
			t.Errorf("step %d: want {%q, %q}, got {%q, %q}", i, w.name, w.prompt, wf.Steps[i].Name, wf.Steps[i].Prompt)
		}
	}
}

// Step refs: an inline step whose name matches a base step fully overrides it
// (replace-not-merge), while another step is pulled unchanged by reference.
func TestStepRefs_InlineOverridesReferencedStep(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		`workflows:
  - name: the-work
    steps:
      - name: planning
        prompt: "global planning"
      - name: implementing
        prompt: "global implementing"
`,
		nil,
		`workflows:
  - name: the-work
    steps:
      - planning
      - name: implementing
        prompt: "project implementing"
`,
		nil,
	)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	wf := cfg.GetTaskWorkflow("the-work")
	if wf == nil || len(wf.Steps) != 2 {
		t.Fatalf("want 2 steps, got %+v", wf)
	}
	if wf.Steps[0].Prompt != "global planning" {
		t.Errorf("referenced planning: want global prompt, got %q", wf.Steps[0].Prompt)
	}
	if wf.Steps[1].Prompt != "project implementing" {
		t.Errorf("overridden implementing: want project prompt, got %q", wf.Steps[1].Prompt)
	}
}

// Step refs: dropping a step is just omitting it from the list — the project
// references only a subset (and may reorder) of the global workflow's steps.
func TestStepRefs_DropAndReorderSteps(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		`workflows:
  - name: the-work
    steps:
      - name: planning
        prompt: "global planning"
      - name: implementing
        prompt: "global implementing"
      - name: reviewing
        prompt: "global reviewing"
`,
		nil,
		`workflows:
  - name: the-work
    steps:
      - reviewing
      - planning
`,
		nil,
	)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	wf := cfg.GetTaskWorkflow("the-work")
	if wf == nil || len(wf.Steps) != 2 {
		t.Fatalf("want 2 steps, got %+v", wf)
	}
	if wf.Steps[0].Name != "reviewing" || wf.Steps[1].Name != "planning" {
		t.Errorf("want [reviewing, planning], got [%q, %q]", wf.Steps[0].Name, wf.Steps[1].Name)
	}
	if wf.Steps[0].Prompt != "global reviewing" {
		t.Errorf("want global reviewing prompt, got %q", wf.Steps[0].Prompt)
	}
}

// Step refs: referencing a step that exists in neither the base nor inline is a
// hard error naming the offending step.
func TestStepRefs_UnknownStepRefErrors(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		`workflows:
  - name: the-work
    steps:
      - name: planning
        prompt: "global planning"
`,
		nil,
		`workflows:
  - name: the-work
    steps:
      - planning
      - nonexistent
`,
		nil,
	)

	_, err := LoadForProject(projectDir)
	if err == nil || !strings.Contains(err.Error(), "nonexistent") {
		t.Fatalf("want error naming unknown step %q, got %v", "nonexistent", err)
	}
}

// Step refs: a step reference in a workflow with no same-named base workflow
// (a brand-new project workflow) errors — there is nothing to resolve against.
func TestStepRefs_NoBaseWorkflowErrors(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		`# no global workflows
`,
		nil,
		`workflows:
  - name: brand-new
    steps:
      - planning
`,
		nil,
	)

	_, err := LoadForProject(projectDir)
	if err == nil || !strings.Contains(err.Error(), "planning") {
		t.Fatalf("want error about unresolvable step %q, got %v", "planning", err)
	}
}

// Step refs also resolve against a global workflow defined in a file under
// ~/.sortie/workflows/ (not just inline global workflows).
func TestStepRefs_ReferenceGlobalFileWorkflowSteps(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		`# global file-pool workflow exists but isn't listed; project references it.
`,
		map[string]string{
			".sortie/workflows/the-work.yml": `
steps:
  - name: planning
    prompt: "global file planning"
  - name: implementing
    prompt: "global file implementing"
`,
		},
		`workflows:
  - name: the-work
    steps:
      - planning
      - name: implementing
        prompt: "project implementing"
`,
		nil,
	)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	wf := cfg.GetTaskWorkflow("the-work")
	if wf == nil || len(wf.Steps) != 2 {
		t.Fatalf("want 2 steps, got %+v", wf)
	}
	if wf.Steps[0].Prompt != "global file planning" {
		t.Errorf("want global file planning prompt, got %q", wf.Steps[0].Prompt)
	}
	if wf.Steps[1].Prompt != "project implementing" {
		t.Errorf("want project implementing override, got %q", wf.Steps[1].Prompt)
	}
}

// The flat list form supports string refs to global workflows.
func TestListForm_StringRefResolvesGlobal(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		`workflows:
  - name: the-work
    steps:
      - name: planning
        prompt: "global planning"
`,
		nil,
		`workflows:
  - the-work
`,
		nil,
	)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	wf := cfg.GetTaskWorkflow("the-work")
	if wf == nil {
		t.Fatalf("want the-work resolved from global via list form, got nil")
	}
	if len(wf.Steps) != 1 || wf.Steps[0].Prompt != "global planning" {
		t.Errorf("want global planning step, got %+v", wf.Steps)
	}
}

// The named-body sugar (`- the-work:` with a nested body) overrides the global
// workflow and its step refs resolve against the global definition — the exact
// syntax from the feature request.
func TestListForm_NamedBodySugarOverridesSteps(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		`workflows:
  - name: the-work
    steps:
      - name: planning
        prompt: "global planning"
      - name: implementing
        prompt: "global implementing"
`,
		nil,
		`workflows:
  - the-work:
      steps:
        - planning
        - implementing
        - name: reviewing
          prompt: "local reviewing"
`,
		nil,
	)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	wf := cfg.GetTaskWorkflow("the-work")
	if wf == nil || len(wf.Steps) != 3 {
		t.Fatalf("want 3 steps, got %+v", wf)
	}
	want := []struct{ name, prompt string }{
		{"planning", "global planning"},
		{"implementing", "global implementing"},
		{"reviewing", "local reviewing"},
	}
	for i, w := range want {
		if wf.Steps[i].Name != w.name || wf.Steps[i].Prompt != w.prompt {
			t.Errorf("step %d: want {%q,%q}, got {%q,%q}", i, w.name, w.prompt, wf.Steps[i].Name, wf.Steps[i].Prompt)
		}
	}
}

// A named-body sugar entry with an empty body (`- the-work:`) is treated as a
// bare ref, identical to `- the-work`.
func TestListForm_NamedBodySugarEmptyBodyIsRef(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		`workflows:
  - name: the-work
    steps:
      - name: planning
        prompt: "global planning"
`,
		nil,
		`workflows:
  - the-work:
`,
		nil,
	)

	cfg, err := LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	wf := cfg.GetTaskWorkflow("the-work")
	if wf == nil || len(wf.Steps) != 1 || wf.Steps[0].Prompt != "global planning" {
		t.Fatalf("want the-work pulled whole from global, got %+v", wf)
	}
}

// Diagnose should pick up the global pool so that project configs referencing
// global workflows validate cleanly (no false-positive "missing file" error).
func TestDiagnose_ResolvesGlobalRefs(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		`workflows:
  - name: shared-impl
    steps:
      - name: do
        prompt: "global"
`,
		nil,
		`workflows:
  - shared-impl
`,
		nil,
	)

	diags, err := Diagnose(filepath.Join(projectDir, ".sortie.yml"))
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	for _, d := range diags {
		if strings.Contains(d.Message, "shared-impl") {
			t.Errorf("did not expect diagnostic about shared-impl, got %q", d.Message)
		}
	}
}

// Diagnose resolves step refs against the global pool so a project that
// references global workflow steps by name validates cleanly, and flags an
// unresolvable step ref as an error.
func TestDiagnose_StepRefs(t *testing.T) {
	projectDir := setupGlobalAndProject(t,
		`workflows:
  - name: the-work
    steps:
      - name: planning
        prompt: "global planning"
`,
		nil,
		`workflows:
  - name: the-work
    steps:
      - planning
      - name: reviewing
        prompt: "local reviewing"
`,
		nil,
	)

	if err := ValidateFile(filepath.Join(projectDir, ".sortie.yml")); err != nil {
		t.Fatalf("ValidateFile on valid step refs: %v", err)
	}

	// Now break the ref and expect a fatal error.
	bad := `workflows:
  - name: the-work
    steps:
      - planning
      - bogus
`
	if err := os.WriteFile(filepath.Join(projectDir, ".sortie.yml"), []byte(bad), 0644); err != nil {
		t.Fatalf("rewrite project config: %v", err)
	}
	if err := ValidateFile(filepath.Join(projectDir, ".sortie.yml")); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("want error naming unresolvable step %q, got %v", "bogus", err)
	}
}
