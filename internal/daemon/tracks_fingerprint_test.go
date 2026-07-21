package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/db"
)

// isolateTracksHome points HOME (and XDG_CONFIG_HOME) at fresh temp dirs so
// the developer's real ~/.sortie/tracks tree can't leak into fingerprint
// tests — tracksFingerprint always scans the global tier too.
func isolateTracksHome(t *testing.T) string {
	t.Helper()
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return homeDir
}

func writeTrackWorkflowFile(t *testing.T, tracksDir, slug, name string) string {
	t.Helper()
	wfDir := filepath.Join(tracksDir, slug, "workflows")
	if err := os.MkdirAll(wfDir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wfDir, name)
	if err := os.WriteFile(path, []byte("steps:\n  - name: implementing\n    prompt: \"track impl\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// countComponent returns the ":fileCount" suffix of a fingerprint.
func countComponent(t *testing.T, fp string) string {
	t.Helper()
	idx := strings.LastIndex(fp, ":")
	if idx < 0 {
		t.Fatalf("fingerprint %q has no count component", fp)
	}
	return fp[idx:]
}

func TestTracksFingerprint(t *testing.T) {
	t.Run("empty when neither tree exists", func(t *testing.T) {
		isolateTracksHome(t)
		if fp := tracksFingerprint(t.TempDir()); fp != "" {
			t.Errorf("fingerprint = %q, want empty", fp)
		}
	})

	t.Run("counts only workflow yml/yaml files", func(t *testing.T) {
		isolateTracksHome(t)
		repoRoot := t.TempDir()
		tracksDir := filepath.Join(repoRoot, ".sortie", "tracks")

		writeTrackWorkflowFile(t, tracksDir, "pay", "impl.yml")
		// Non-workflow files are ignored by the count component.
		if err := os.WriteFile(filepath.Join(tracksDir, "pay", "workflows", "README.txt"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		fp := tracksFingerprint(repoRoot)
		if fp == "" {
			t.Fatal("expected non-empty fingerprint")
		}
		if got := countComponent(t, fp); got != ":1" {
			t.Errorf("count component = %q, want :1 (README.txt must not count)", got)
		}

		writeTrackWorkflowFile(t, tracksDir, "pay", "more.yaml")
		fp2 := tracksFingerprint(repoRoot)
		if fp2 == fp {
			t.Error("fingerprint must change when a workflow file is added")
		}
		if got := countComponent(t, fp2); got != ":2" {
			t.Errorf("count component = %q, want :2", got)
		}
	})

	t.Run("deletion caught by count even with restored mtimes", func(t *testing.T) {
		isolateTracksHome(t)
		repoRoot := t.TempDir()
		tracksDir := filepath.Join(repoRoot, ".sortie", "tracks")

		kept := writeTrackWorkflowFile(t, tracksDir, "pay", "impl.yml")
		removed := writeTrackWorkflowFile(t, tracksDir, "pay", "extra.yml")
		before := tracksFingerprint(repoRoot)

		// Snapshot mtimes of everything the walk stats, remove a file, then
		// restore the mtimes — only the count component may catch the change.
		wfDir := filepath.Dir(kept)
		slugDir := filepath.Dir(wfDir)
		var stamps = map[string]os.FileInfo{}
		for _, p := range []string{tracksDir, slugDir, wfDir, kept} {
			info, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			stamps[p] = info
		}
		if err := os.Remove(removed); err != nil {
			t.Fatal(err)
		}
		for p, info := range stamps {
			if err := os.Chtimes(p, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
		}

		after := tracksFingerprint(repoRoot)
		if after == before {
			t.Errorf("fingerprint must change on deletion even when mtimes are unchanged: %q", after)
		}
		if got := countComponent(t, after); got != ":1" {
			t.Errorf("count component = %q, want :1", got)
		}
	})

	t.Run("global tier contributes", func(t *testing.T) {
		homeDir := isolateTracksHome(t)
		repoRoot := t.TempDir()
		if fp := tracksFingerprint(repoRoot); fp != "" {
			t.Fatalf("precondition: fingerprint = %q, want empty", fp)
		}
		writeTrackWorkflowFile(t, filepath.Join(homeDir, ".sortie", "tracks"), "sprint", "impl.yml")
		if fp := tracksFingerprint(repoRoot); fp == "" {
			t.Error("expected global-tier track workflows to contribute to the fingerprint")
		}
	})
}

// TestGetProjectContextReloadsOnTrackWorkflowChange proves the second
// freshness signal end to end at the unit level: a track workflow file
// created (or removed) AFTER the project config was cached evicts the cache
// entry and the reloaded config resolves (or drops) the namespaced workflow —
// with .sortie.yml untouched throughout, so only the tracks fingerprint can
// have triggered the reload.
func TestGetProjectContextReloadsOnTrackWorkflowChange(t *testing.T) {
	isolateTracksHome(t)
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, ".sortie.yml"), []byte("git:\n  base_branch: main\n"), 0644); err != nil {
		t.Fatal(err)
	}

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

	pc1, err := s.getProjectContext(proj.ID)
	if err != nil {
		t.Fatalf("getProjectContext: %v", err)
	}
	if pc1.cfg.GetTaskWorkflow("pay:impl") != nil {
		t.Fatal("precondition: pay:impl must not exist yet")
	}
	pc1Again, err := s.getProjectContext(proj.ID)
	if err != nil {
		t.Fatalf("getProjectContext (cached): %v", err)
	}
	if pc1Again != pc1 {
		t.Fatal("expected cached project context while nothing changed")
	}

	// Drop a track workflow on disk — the fingerprint must evict the cache.
	writeTrackWorkflowFile(t, filepath.Join(projectDir, ".sortie", "tracks"), "pay", "impl.yml")
	pc2, err := s.getProjectContext(proj.ID)
	if err != nil {
		t.Fatalf("getProjectContext after add: %v", err)
	}
	if pc2 == pc1 {
		t.Fatal("expected cache eviction after track workflow file appeared")
	}
	if pc2.cfg.GetTaskWorkflow("pay:impl") == nil {
		t.Error("reloaded config must resolve the namespaced track workflow")
	}

	// Removing the file must also be picked up (count component).
	if err := os.Remove(filepath.Join(projectDir, ".sortie", "tracks", "pay", "workflows", "impl.yml")); err != nil {
		t.Fatal(err)
	}
	pc3, err := s.getProjectContext(proj.ID)
	if err != nil {
		t.Fatalf("getProjectContext after remove: %v", err)
	}
	if pc3 == pc2 {
		t.Fatal("expected cache eviction after track workflow file was removed")
	}
	if pc3.cfg.GetTaskWorkflow("pay:impl") != nil {
		t.Error("removed track workflow must no longer resolve")
	}
}
