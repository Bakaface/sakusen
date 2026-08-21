package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const trackWfYaml = `
steps:
  - name: implementing
    prompt: "track impl"
`

// isolateHome points HOME and XDG_CONFIG_HOME at fresh temp dirs so the
// developer's real ~/.sakusen tree can't leak into project-tier tests.
func isolateHome(t *testing.T) string {
	t.Helper()
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return homeDir
}

func findWorkflow(cfg *Config, name string) *WorkflowConfig {
	for i := range cfg.Workflows {
		if cfg.Workflows[i].Name == name {
			return &cfg.Workflows[i]
		}
	}
	return nil
}

// TestTrackWorkflows_ProjectTierDiscovery verifies that
// .sakusen/tracks/<slug>/workflows/*.yml register as hidden, namespaced
// "<slug>:<name>" workflows resolvable via GetTaskWorkflow but excluded from
// the new-task menu names.
func TestTrackWorkflows_ProjectTierDiscovery(t *testing.T) {
	isolateHome(t)
	dir, _ := setupProject(t, `
git:
  base_branch: main
`, map[string]string{
		".sakusen/tracks/pay/workflows/impl.yml": trackWfYaml,
	})

	cfg, err := LoadForProject(dir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}

	wf := findWorkflow(cfg, "pay:impl")
	if wf == nil {
		t.Fatalf("expected workflow %q in resolved pool; have %v", "pay:impl", cfg.ListAllWorkflowNames())
	}
	if !wf.Hidden {
		t.Error("track workflow must be Hidden")
	}
	if !strings.HasSuffix(wf.Source, filepath.Join("pay", "workflows", "impl.yml")) {
		t.Errorf("Source = %q, want path ending in pay/workflows/impl.yml", wf.Source)
	}

	for _, name := range cfg.ListWorkflowNames() {
		if name == "pay:impl" {
			t.Error("hidden track workflow must not appear in ListWorkflowNames")
		}
	}
	found := false
	for _, name := range cfg.ListAllWorkflowNames() {
		if name == "pay:impl" {
			found = true
		}
	}
	if !found {
		t.Error("track workflow must appear in ListAllWorkflowNames")
	}
	if cfg.GetTaskWorkflow("pay:impl") == nil {
		t.Error("GetTaskWorkflow must resolve the namespaced track workflow")
	}
}

// TestTrackWorkflows_GlobalTier verifies ~/.sakusen/tracks/<slug>/workflows/
// resolves from any project — including when ~/.sakusen.yml does not exist.
func TestTrackWorkflows_GlobalTier(t *testing.T) {
	homeDir := isolateHome(t)
	globalWf := filepath.Join(homeDir, ".sakusen", "tracks", "sprint", "workflows", "impl.yml")
	if err := os.MkdirAll(filepath.Dir(globalWf), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalWf, []byte(trackWfYaml), 0644); err != nil {
		t.Fatal(err)
	}

	dir, _ := setupProject(t, "git:\n  base_branch: main\n", nil)
	cfg, err := LoadForProject(dir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	wf := findWorkflow(cfg, "sprint:impl")
	if wf == nil {
		t.Fatalf("expected global track workflow %q; have %v", "sprint:impl", cfg.ListAllWorkflowNames())
	}
	if !wf.Hidden {
		t.Error("global track workflow must be Hidden")
	}
}

// TestTrackWorkflows_ProjectShadowsGlobal verifies an identical <slug>:<name>
// in both tiers resolves to the project tier without error.
func TestTrackWorkflows_ProjectShadowsGlobal(t *testing.T) {
	homeDir := isolateHome(t)
	globalWf := filepath.Join(homeDir, ".sakusen", "tracks", "pay", "workflows", "impl.yml")
	if err := os.MkdirAll(filepath.Dir(globalWf), 0755); err != nil {
		t.Fatal(err)
	}
	globalYaml := "steps:\n  - name: global-step\n    prompt: \"global\"\n"
	if err := os.WriteFile(globalWf, []byte(globalYaml), 0644); err != nil {
		t.Fatal(err)
	}

	projectYaml := "steps:\n  - name: project-step\n    prompt: \"project\"\n"
	dir, _ := setupProject(t, "git:\n  base_branch: main\n", map[string]string{
		".sakusen/tracks/pay/workflows/impl.yml": projectYaml,
	})

	cfg, err := LoadForProject(dir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	wf := findWorkflow(cfg, "pay:impl")
	if wf == nil {
		t.Fatal("expected pay:impl")
	}
	if len(wf.Steps) != 1 || wf.Steps[0].Name != "project-step" {
		t.Errorf("expected project tier to shadow global, got steps %+v", wf.Steps)
	}
	// Exactly one registration.
	count := 0
	for _, w := range cfg.Workflows {
		if w.Name == "pay:impl" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("pay:impl registered %d times, want 1", count)
	}
}

// TestTrackWorkflows_HiddenNeverImplicitDefault: in a project with NO
// configured workflows, track workflows (the only entries in cfg.Workflows,
// all hidden) must not become the implicit default — GetWorkflow("") has to
// fall through to DefaultWorkflow(), not silently run the alphabetically
// first track workflow.
func TestTrackWorkflows_HiddenNeverImplicitDefault(t *testing.T) {
	isolateHome(t)
	dir, _ := setupProject(t, "git:\n  base_branch: main\n", map[string]string{
		".sakusen/tracks/pay/workflows/impl.yml": trackWfYaml,
	})

	cfg, err := LoadForProject(dir)
	if err != nil {
		t.Fatalf("LoadForProject: %v", err)
	}
	if findWorkflow(cfg, "pay:impl") == nil {
		t.Fatal("expected pay:impl registered")
	}

	wf := cfg.GetWorkflow("")
	if wf == nil {
		t.Fatal("GetWorkflow(\"\") returned nil")
	}
	if wf.Name == "pay:impl" || wf.Hidden {
		t.Errorf("GetWorkflow(\"\") = %q (hidden=%v) — hidden track workflow must not become the implicit default", wf.Name, wf.Hidden)
	}
}

// TestTrackWorkflows_Errors covers the hard-error cases and the silently
// skipped non-directory entry.
func TestTrackWorkflows_Errors(t *testing.T) {
	t.Run("invalid slug dir name", func(t *testing.T) {
		isolateHome(t)
		dir, _ := setupProject(t, "", map[string]string{
			".sakusen/tracks/Bad_Slug/workflows/impl.yml": trackWfYaml,
		})
		if _, err := LoadForProject(dir); err == nil || !strings.Contains(err.Error(), "kebab-case") {
			t.Errorf("expected kebab-case error, got %v", err)
		}
	})

	t.Run("subdir inside track workflows dir", func(t *testing.T) {
		isolateHome(t)
		dir, _ := setupProject(t, "", map[string]string{
			".sakusen/tracks/pay/workflows/nested/impl.yml": trackWfYaml,
		})
		if _, err := LoadForProject(dir); err == nil || !strings.Contains(err.Error(), "subdirectories not supported") {
			t.Errorf("expected subdir error, got %v", err)
		}
	})

	t.Run("bad extension", func(t *testing.T) {
		isolateHome(t)
		dir, _ := setupProject(t, "", map[string]string{
			".sakusen/tracks/pay/workflows/impl.json": trackWfYaml,
		})
		if _, err := LoadForProject(dir); err == nil || !strings.Contains(err.Error(), "invalid file extension") {
			t.Errorf("expected extension error, got %v", err)
		}
	})

	t.Run("non-dir entry in tracks/ skipped", func(t *testing.T) {
		isolateHome(t)
		dir, _ := setupProject(t, "", map[string]string{
			".sakusen/tracks/README.md": "not a track",
		})
		if _, err := LoadForProject(dir); err != nil {
			t.Errorf("non-dir entries must be skipped silently, got %v", err)
		}
	})
}
