package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
)

// setupTUIRepo creates a git repo in a temp dir, isolates the global config,
// and points the package-level cfg at a throwaway database so that
// resolveProjectMode's GetOrCreateProject cannot touch the developer's real DB.
func setupTUIRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateGlobalConfig(t)

	dbDir := t.TempDir()
	prev := cfg
	cfg = &config.Config{DatabasePath: filepath.Join(dbDir, "tasks.db")}
	t.Cleanup(func() { cfg = prev })

	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

func tuiMkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	return path
}

func TestResolveProjectMode_SubdirectoryMarkerOwnsProject(t *testing.T) {
	repo := setupTUIRepo(t)
	sub := tuiMkdir(t, filepath.Join(repo, "sub"))
	if err := os.WriteFile(filepath.Join(sub, ".sakusen.yml"), []byte("workflows: []\n"), 0644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	t.Chdir(tuiMkdir(t, filepath.Join(sub, "a")))
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	wantPath := filepath.Dir(cwd)

	projectID, projectPath, projectName, globalMode, _, _, _ := resolveProjectMode(false)
	if globalMode {
		t.Fatal("globalMode = true, want false")
	}
	if projectID == 0 {
		t.Error("projectID = 0, want a registered project row")
	}
	if projectPath != wantPath {
		t.Errorf("projectPath = %q, want %q", projectPath, wantPath)
	}
	if projectName != "sub" {
		t.Errorf("projectName = %q, want %q", projectName, "sub")
	}

	database, err := db.Open(cfg.GetDatabasePath(""))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	proj, err := database.GetProject(projectID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if proj.Path != wantPath {
		t.Errorf("registered project path = %q, want %q", proj.Path, wantPath)
	}
}

func TestResolveProjectMode_NoMarkerFiltersByToplevelName(t *testing.T) {
	repo := setupTUIRepo(t)
	t.Chdir(tuiMkdir(t, filepath.Join(repo, "a")))

	top, err := gitToplevel(repo)
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}

	projectID, projectPath, projectName, globalMode, _, _, _ := resolveProjectMode(false)
	if globalMode {
		t.Fatal("globalMode = true, want false")
	}
	if projectID != 0 {
		t.Errorf("projectID = %d, want 0 (name-filter mode registers no row)", projectID)
	}
	if projectPath != top {
		t.Errorf("projectPath = %q, want %q", projectPath, top)
	}
	if want := config.ProjectNameFromPath(top); projectName != want {
		t.Errorf("projectName = %q, want %q", projectName, want)
	}
}

func TestResolveProjectMode_OutsideGitRepoIsGlobal(t *testing.T) {
	setupTUIRepo(t)
	t.Chdir(t.TempDir())

	_, _, _, globalMode, _, _, _ := resolveProjectMode(false)
	if !globalMode {
		t.Error("globalMode = false, want true outside a repo with no marker")
	}
}

func TestResolveProjectMode_GlobalFlagWins(t *testing.T) {
	repo := setupTUIRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".sakusen.yml"), []byte("workflows: []\n"), 0644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	t.Chdir(repo)

	_, _, _, globalMode, _, _, _ := resolveProjectMode(true)
	if !globalMode {
		t.Error("globalMode = false, want true with --global")
	}
}

func gitToplevel(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out[:len(out)-1]), nil
}
