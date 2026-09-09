package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mcpInitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

func mcpMkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	return path
}

func TestResolveProjectPath_ExplicitRelative(t *testing.T) {
	// A non-git temp dir: an explicit path must be honored without consulting
	// git or looking for a marker.
	t.Chdir(t.TempDir())

	got, err := resolveProjectPath(filepath.Join("some", "project"))
	if err != nil {
		t.Fatalf("resolveProjectPath: %v", err)
	}
	want, err := filepath.Abs(filepath.Join("some", "project"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveProjectPath_ImplicitSubdirectoryMarker(t *testing.T) {
	repo := mcpInitRepo(t)
	sub := mcpMkdir(t, filepath.Join(repo, "sub"))
	if err := os.WriteFile(filepath.Join(sub, ".sakusen.yml"), []byte("workflows: []\n"), 0644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	t.Chdir(mcpMkdir(t, filepath.Join(sub, "a")))
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	want := filepath.Dir(cwd)

	got, err := resolveProjectPath("")
	if err != nil {
		t.Fatalf("resolveProjectPath: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveProjectPath_ImplicitNoMarkerUsesToplevel(t *testing.T) {
	repo := mcpInitRepo(t)
	t.Chdir(mcpMkdir(t, filepath.Join(repo, "a")))

	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	want := strings.TrimSpace(string(out))

	got, err := resolveProjectPath("")
	if err != nil {
		t.Fatalf("resolveProjectPath: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveProjectPath_ImplicitOutsideRepoErrors(t *testing.T) {
	t.Chdir(t.TempDir())

	_, err := resolveProjectPath("")
	if err == nil {
		t.Fatal("expected an error outside a git repo with no marker")
	}
	if !strings.Contains(err.Error(), "project_path") {
		t.Errorf("error %q does not mention project_path", err)
	}
}
