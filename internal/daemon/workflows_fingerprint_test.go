package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
)

// writeWorkflowFile writes a single-step workflow whose prompt is prompt and
// pushes its mtime (and its dir's) forward, so an edit is visible regardless
// of filesystem timestamp granularity.
func writeWorkflowFile(t *testing.T, path, prompt string, bump time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	body := "steps:\n  - name: implementing\n    prompt: \"" + prompt + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(bump)
	for _, p := range []string{path, filepath.Dir(path)} {
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
}

func workflowPrompt(pc *projectContext, name string) string {
	wf := pc.cfg.GetTaskWorkflow(name)
	if wf == nil || len(wf.Steps) == 0 {
		return ""
	}
	return wf.Steps[0].Prompt
}

// TestGetProjectContextReloadsOnWorkflowFileChange proves the fourth freshness
// signal: editing, adding or deleting a file-based workflow, in either the
// global or the project tier, evicts the cached project context while every
// other signal (.sakusen.yml files, tracks) stays untouched.
func TestGetProjectContextReloadsOnWorkflowFileChange(t *testing.T) {
	tiers := []struct {
		name  string
		wfDir func(homeDir, projectDir string) string
	}{
		{"global", func(homeDir, _ string) string { return filepath.Join(homeDir, ".sakusen", "workflows") }},
		{"project", func(_, projectDir string) string { return filepath.Join(projectDir, ".sakusen", "workflows") }},
	}

	for _, tier := range tiers {
		t.Run(tier.name, func(t *testing.T) {
			homeDir := isolateTracksHome(t)
			if err := os.WriteFile(filepath.Join(homeDir, ".sakusen.yml"), []byte("git:\n  base_branch: main\n"), 0644); err != nil {
				t.Fatal(err)
			}
			projectDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(projectDir, ".sakusen.yml"), []byte("git:\n  base_branch: main\n"), 0644); err != nil {
				t.Fatal(err)
			}
			wfDir := tier.wfDir(homeDir, projectDir)
			implPath := filepath.Join(wfDir, "impl.yml")
			writeWorkflowFile(t, implPath, "v1", 0)

			database, err := db.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			proj, err := database.GetOrCreateProject(projectDir)
			if err != nil {
				t.Fatal(err)
			}
			s := NewServer(&config.Config{}, database)

			get := func(stage string) *projectContext {
				t.Helper()
				pc, err := s.getProjectContext(proj.ID)
				if err != nil {
					t.Fatalf("getProjectContext (%s): %v", stage, err)
				}
				if pc.loadErr != nil {
					t.Fatalf("config load failed (%s): %v", stage, pc.loadErr)
				}
				return pc
			}

			pc1 := get("initial")
			if got := workflowPrompt(pc1, "impl"); got != "v1" {
				t.Fatalf("precondition: impl prompt = %q, want v1", got)
			}
			if pc1Again := get("unchanged"); pc1Again != pc1 {
				t.Fatal("expected cached project context while nothing changed")
			}

			writeWorkflowFile(t, implPath, "v2", time.Minute)
			pc2 := get("after edit")
			if pc2 == pc1 {
				t.Fatal("expected cache eviction after a workflow file was edited")
			}
			if got := workflowPrompt(pc2, "impl"); got != "v2" {
				t.Errorf("reloaded impl prompt = %q, want v2", got)
			}

			extraPath := filepath.Join(wfDir, "extra.yml")
			writeWorkflowFile(t, extraPath, "extra", 2*time.Minute)
			pc3 := get("after add")
			if pc3 == pc2 {
				t.Fatal("expected cache eviction after a workflow file was added")
			}
			if pc3.cfg.GetTaskWorkflow("extra") == nil {
				t.Error("reloaded config must resolve the added workflow")
			}

			// Restore the dir mtime after deleting so only the entry count can
			// catch the removal.
			dirInfo, err := os.Stat(wfDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(extraPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(wfDir, dirInfo.ModTime(), dirInfo.ModTime()); err != nil {
				t.Fatal(err)
			}
			pc4 := get("after delete")
			if pc4 == pc3 {
				t.Fatal("expected cache eviction after a workflow file was deleted")
			}
			if pc4.cfg.GetTaskWorkflow("extra") != nil {
				t.Error("deleted workflow must no longer resolve")
			}
			if pc4Again := get("unchanged after delete"); pc4Again != pc4 {
				t.Fatal("expected cached project context while nothing changed")
			}
		})
	}
}

// TestWorkflowsFingerprintFollowsXDGGlobalConfig checks the fingerprint watches
// the workflows dir beside $XDG_CONFIG_HOME/sakusen/config.yml when the loader
// resolves the global .sakusen.yml there.
func TestWorkflowsFingerprintFollowsXDGGlobalConfig(t *testing.T) {
	isolateTracksHome(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := os.MkdirAll(filepath.Join(xdg, "sakusen"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "sakusen", "config.yml"), []byte("git:\n  base_branch: main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	repoRoot := t.TempDir()
	if fp := workflowsFingerprint(repoRoot); fp != "" {
		t.Fatalf("precondition: fingerprint = %q, want empty", fp)
	}
	writeWorkflowFile(t, filepath.Join(xdg, "sakusen", ".sakusen", "workflows", "impl.yml"), "v1", 0)
	if fp := workflowsFingerprint(repoRoot); fp == "" {
		t.Error("expected the XDG global workflows dir to contribute to the fingerprint")
	}
}
