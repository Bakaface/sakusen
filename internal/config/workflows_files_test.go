package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupProject writes a .sakusen.yml and any .sakusen/workflows/* files into a
// temp dir and returns the dir + .sakusen.yml path.
func setupProject(t *testing.T, sakusenYml string, files map[string]string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	ymlPath := filepath.Join(dir, ".sakusen.yml")
	if err := os.WriteFile(ymlPath, []byte(sakusenYml), 0644); err != nil {
		t.Fatalf("write .sakusen.yml: %v", err)
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return dir, ymlPath
}

// TestFileWorkflows_StringRefResolves verifies that a string ref in the flat
// workflows list resolves against the flat .sakusen/workflows/<name>.yml pool.
func TestFileWorkflows_StringRefResolves(t *testing.T) {
	dir, _ := setupProject(t, `
workflows:
  - implement
`, map[string]string{
		".sakusen/workflows/implement.yml": `
description: Implement task
steps:
  - name: do
    prompt: "do it"
`,
	})

	cfg, err := LoadForProject(dir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if len(cfg.Workflows) != 1 {
		t.Fatalf("want 1 workflow, got %d", len(cfg.Workflows))
	}
	wf := cfg.Workflows[0]
	if wf.Name != "implement" {
		t.Errorf("want name implement, got %q", wf.Name)
	}
	if wf.Hidden {
		t.Errorf("referenced workflow should not be hidden")
	}
	if wf.Source == "" || !strings.HasSuffix(wf.Source, "implement.yml") {
		t.Errorf("want source path ending with implement.yml, got %q", wf.Source)
	}
	if wf.Description != "Implement task" {
		t.Errorf("want description 'Implement task', got %q", wf.Description)
	}
}

func TestFileWorkflows_MissingRefIsHardError(t *testing.T) {
	dir, _ := setupProject(t, `
workflows:
  - nonexistent
`, nil)

	_, err := LoadForProject(dir)
	if err == nil || !strings.Contains(err.Error(), "nonexistent") {
		t.Fatalf("want missing-ref error, got %v", err)
	}
}

func TestFileWorkflows_InlineFileCollisionIsError(t *testing.T) {
	dir, _ := setupProject(t, `
workflows:
  - name: foo
    steps:
      - name: a
        prompt: "a"
`, map[string]string{
		".sakusen/workflows/foo.yml": `
steps:
  - name: b
    prompt: "b"
`,
	})

	_, err := LoadForProject(dir)
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("want collision error, got %v", err)
	}
}

// TestFileWorkflows_UnreferencedFileLoadedAsHidden verifies that files not
// referenced from .sakusen.yml are loaded as Hidden=true.
func TestFileWorkflows_UnreferencedFileLoadedAsHidden(t *testing.T) {
	dir, _ := setupProject(t, `
workflows:
  - active
`, map[string]string{
		".sakusen/workflows/active.yml": `
steps:
  - name: a
    prompt: "a"
`,
		".sakusen/workflows/hidden.yml": `
steps:
  - name: b
    prompt: "b"
`,
	})

	cfg, err := LoadForProject(dir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if len(cfg.Workflows) != 2 {
		t.Fatalf("want 2 workflows (1 active + 1 hidden), got %d", len(cfg.Workflows))
	}
	// Active first, hidden after.
	if cfg.Workflows[0].Name != "active" || cfg.Workflows[0].Hidden {
		t.Errorf("want first=active (active), got %s hidden=%v", cfg.Workflows[0].Name, cfg.Workflows[0].Hidden)
	}
	if cfg.Workflows[1].Name != "hidden" || !cfg.Workflows[1].Hidden {
		t.Errorf("want second=hidden (hidden), got %s hidden=%v", cfg.Workflows[1].Name, cfg.Workflows[1].Hidden)
	}
}

func TestFileWorkflows_OrderPreservedFromListing(t *testing.T) {
	dir, _ := setupProject(t, `
workflows:
  - second
  - first
`, map[string]string{
		".sakusen/workflows/first.yml":  "steps:\n  - name: a\n    prompt: a\n",
		".sakusen/workflows/second.yml": "steps:\n  - name: b\n    prompt: b\n",
	})

	cfg, err := LoadForProject(dir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if len(cfg.Workflows) != 2 {
		t.Fatalf("want 2 workflows, got %d", len(cfg.Workflows))
	}
	if cfg.Workflows[0].Name != "second" {
		t.Errorf("want first listed=second, got %s", cfg.Workflows[0].Name)
	}
	if cfg.Workflows[1].Name != "first" {
		t.Errorf("want second listed=first, got %s", cfg.Workflows[1].Name)
	}
}

func TestFileWorkflows_InvalidFilenameRejected(t *testing.T) {
	dir, _ := setupProject(t, `workflows: []
`, map[string]string{
		".sakusen/workflows/Bad_Name.yml": "steps: []\n",
	})

	_, err := LoadForProject(dir)
	if err == nil || !strings.Contains(err.Error(), "invalid filename") {
		t.Fatalf("want invalid filename error, got %v", err)
	}
}

func TestFileWorkflows_NameFieldRejected(t *testing.T) {
	dir, _ := setupProject(t, `workflows:
  - foo
`, map[string]string{
		".sakusen/workflows/foo.yml": `
name: explicit-name
steps:
  - name: a
    prompt: a
`,
	})

	_, err := LoadForProject(dir)
	if err == nil || !strings.Contains(err.Error(), "must not set `name:`") {
		t.Fatalf("want name-rejection error, got %v", err)
	}
}

// TestFileWorkflows_HiddenWorkflowReachableViaListAll verifies that hidden
// (unreferenced) file workflows appear in ListAllWorkflowNames but not
// ListWorkflowNames.
func TestFileWorkflows_HiddenWorkflowReachableViaListAll(t *testing.T) {
	dir, _ := setupProject(t, `workflows: []
`, map[string]string{
		".sakusen/workflows/hidden-job.yml": `
steps:
  - name: a
    prompt: "a"
`,
	})

	cfg, err := LoadForProject(dir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	// Active list should be empty (returns default).
	active := cfg.ListWorkflowNames()
	if len(active) != 1 || active[0] != "default" {
		t.Errorf("want [default] for empty active list, got %v", active)
	}
	// ListAll should include hidden one.
	all := cfg.ListAllWorkflowNames()
	if len(all) != 1 || all[0] != "hidden-job" {
		t.Errorf("want [hidden-job] in ListAll, got %v", all)
	}
}

func TestFileWorkflows_YamlExtension(t *testing.T) {
	dir, _ := setupProject(t, `workflows:
  - alpha
`, map[string]string{
		".sakusen/workflows/alpha.yaml": `
steps:
  - name: a
    prompt: a
`,
	})

	cfg, err := LoadForProject(dir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if len(cfg.Workflows) != 1 || cfg.Workflows[0].Name != "alpha" {
		t.Errorf("want [alpha] from .yaml file, got %+v", cfg.Workflows)
	}
}

// TestFileWorkflows_SubdirectoryRejected verifies that subdirectories under
// .sakusen/workflows/ produce an error (flat-only layout required).
func TestFileWorkflows_SubdirectoryRejected(t *testing.T) {
	dir, _ := setupProject(t, `workflows: []
`, map[string]string{
		".sakusen/workflows/sub/nested.yml": "steps: []\n",
	})

	_, err := LoadForProject(dir)
	if err == nil || !strings.Contains(err.Error(), "subdirector") {
		t.Fatalf("want subdirectory error, got %v", err)
	}
}

func TestFileWorkflows_MixedInlineAndRef(t *testing.T) {
	dir, _ := setupProject(t, `workflows:
  - file-based
  - name: inline-based
    steps:
      - name: a
        prompt: "a"
`, map[string]string{
		".sakusen/workflows/file-based.yml": `
steps:
  - name: b
    prompt: "b"
`,
	})

	cfg, err := LoadForProject(dir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if len(cfg.Workflows) != 2 {
		t.Fatalf("want 2 workflows, got %d", len(cfg.Workflows))
	}
	if cfg.Workflows[0].Name != "file-based" {
		t.Errorf("want first=file-based, got %s", cfg.Workflows[0].Name)
	}
	if cfg.Workflows[1].Name != "inline-based" {
		t.Errorf("want second=inline-based, got %s", cfg.Workflows[1].Name)
	}
	if cfg.Workflows[0].Source == "inline" {
		t.Errorf("file-based should have file path source, got 'inline'")
	}
	if cfg.Workflows[1].Source != "inline" {
		t.Errorf("inline workflow should have source='inline', got %q", cfg.Workflows[1].Source)
	}
}

func TestDiagnose_WarnsForUnreferencedFile(t *testing.T) {
	_, ymlPath := setupProject(t, `workflows:
  - active
`, map[string]string{
		".sakusen/workflows/active.yml": "steps:\n  - name: a\n    prompt: a\n",
		".sakusen/workflows/hidden.yml": "steps:\n  - name: b\n    prompt: b\n",
	})

	diags, err := Diagnose(ymlPath)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, "hidden") && strings.Contains(d.Message, "hidden.yml") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a 'hidden' diagnostic about hidden.yml, got %+v", diags)
	}
}

// TestDiagnose_WarnsForFilesWithNoListing verifies that when files exist
// under .sakusen/workflows/ but there is no workflows: listing in .sakusen.yml,
// a warning is surfaced.
func TestDiagnose_WarnsForFilesWithNoListing(t *testing.T) {
	// Files exist but no workflows: key in .sakusen.yml — all workflows hidden.
	_, ymlPath := setupProject(t, `max_workers: 2
`, map[string]string{
		".sakusen/workflows/bootstrap.yml": "steps:\n  - name: a\n    prompt: a\n",
	})

	diags, err := Diagnose(ymlPath)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	var got []string
	for _, d := range diags {
		got = append(got, d.Message)
	}
	found := false
	for _, msg := range got {
		if strings.Contains(msg, "hidden") && strings.Contains(msg, "workflows") {
			found = true
		}
	}
	if !found {
		t.Errorf("want diagnostic about hidden workflows under .sakusen/workflows/, got %v", got)
	}
}

// TestFileWorkflows_GetTaskWorkflowByName verifies that file-based workflows
// are accessible via GetTaskWorkflow by name.
func TestFileWorkflows_GetTaskWorkflowByName(t *testing.T) {
	dir, _ := setupProject(t, `workflows:
  - myflow
`, map[string]string{
		".sakusen/workflows/myflow.yml": `
description: My workflow
steps:
  - name: do
    prompt: "do something"
`,
	})

	cfg, err := LoadForProject(dir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}

	wf := cfg.GetTaskWorkflow("myflow")
	if wf == nil {
		t.Fatal("expected GetTaskWorkflow to find 'myflow'")
	}
	if wf.Name != "myflow" {
		t.Errorf("want name 'myflow', got %q", wf.Name)
	}
	if wf.Description != "My workflow" {
		t.Errorf("want description 'My workflow', got %q", wf.Description)
	}
}

// TestFileWorkflows_UnreferencedFileAccessibleViaGetTaskWorkflow verifies that
// hidden (unreferenced) file-based workflows are still accessible via
// GetTaskWorkflow (hidden workflows remain engine-reachable).
func TestFileWorkflows_UnreferencedFileAccessibleViaGetTaskWorkflow(t *testing.T) {
	dir, _ := setupProject(t, `workflows: []
`, map[string]string{
		".sakusen/workflows/hidden-flow.yml": `
steps:
  - name: a
    prompt: "hidden"
`,
	})

	cfg, err := LoadForProject(dir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}

	// Hidden workflows should be accessible by name even though not in active listing.
	wf := cfg.GetTaskWorkflow("hidden-flow")
	if wf == nil {
		t.Fatal("expected hidden-flow to be accessible via GetTaskWorkflow")
	}
	if !wf.Hidden {
		t.Error("expected workflow to be marked Hidden")
	}
}
