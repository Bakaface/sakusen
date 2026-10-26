package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initRepo creates a git repo at dir (creating dir if needed) and returns it.
// Skips the test when git is unavailable.
func initRepo(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

func mkdirs(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	return path
}

func writeMarker(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".sakusen.yml"), []byte("workflows: []\n"), 0644); err != nil {
		t.Fatalf("write marker in %s: %v", dir, err)
	}
}

// repoToplevel is what `git rev-parse --show-toplevel` reports for dir. Temp
// dirs live under a symlink on macOS, so this is not the same string as dir.
func repoToplevel(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse in %s: %v", dir, err)
	}
	return string(out[:len(out)-1])
}

// canon is the symlink-resolved form of p, which is what FindProjectRoot
// returns.
func canon(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("eval symlinks %s: %v", p, err)
	}
	return resolved
}

func mustFind(t *testing.T, start string) (string, ProjectRootKind) {
	t.Helper()
	root, kind, err := FindProjectRoot(start)
	if err != nil {
		t.Fatalf("FindProjectRoot(%s): %v", start, err)
	}
	return root, kind
}

func TestFindProjectRoot_MarkerAtCwd(t *testing.T) {
	repo := initRepo(t, t.TempDir())
	writeMarker(t, repo)

	root, kind := mustFind(t, repo)
	if want := canon(t, repo); kind != ProjectRootMarker || root != want {
		t.Errorf("got (%q, %v), want (%q, Marker)", root, kind, want)
	}
}

func TestFindProjectRoot_MarkerInAncestorBelowToplevel(t *testing.T) {
	repo := initRepo(t, t.TempDir())
	sub := mkdirs(t, filepath.Join(repo, "sub"))
	writeMarker(t, sub)
	start := mkdirs(t, filepath.Join(sub, "a", "b"))

	root, kind := mustFind(t, start)
	if want := canon(t, sub); kind != ProjectRootMarker || root != want {
		t.Errorf("got (%q, %v), want (%q, Marker)", root, kind, want)
	}
}

func TestFindProjectRoot_NearestMarkerWins(t *testing.T) {
	repo := initRepo(t, t.TempDir())
	writeMarker(t, repo)
	sub := mkdirs(t, filepath.Join(repo, "sub"))
	writeMarker(t, sub)

	start := mkdirs(t, filepath.Join(sub, "a"))
	if root, kind := mustFind(t, start); kind != ProjectRootMarker || root != canon(t, sub) {
		t.Errorf("from %s: got (%q, %v), want (%q, Marker)", start, root, kind, canon(t, sub))
	}

	other := mkdirs(t, filepath.Join(repo, "other"))
	if root, kind := mustFind(t, other); kind != ProjectRootMarker || root != canon(t, repo) {
		t.Errorf("from %s: got (%q, %v), want (%q, Marker)", other, root, kind, canon(t, repo))
	}
}

func TestFindProjectRoot_ToplevelMarkerIsChecked(t *testing.T) {
	repo := initRepo(t, t.TempDir())
	writeMarker(t, repo)
	start := mkdirs(t, filepath.Join(repo, "a", "b"))

	root, kind := mustFind(t, start)
	if want := canon(t, repo); kind != ProjectRootMarker || root != want {
		t.Errorf("got (%q, %v), want (%q, Marker)", root, kind, want)
	}
}

func TestFindProjectRoot_NoMarkerFallsBackToToplevel(t *testing.T) {
	repo := initRepo(t, t.TempDir())
	start := mkdirs(t, filepath.Join(repo, "a", "b"))
	top := repoToplevel(t, repo)

	root, kind := mustFind(t, start)
	if kind != ProjectRootGitToplevel || root != top {
		t.Errorf("got (%q, %v), want (%q, GitToplevel)", root, kind, top)
	}
}

func TestFindProjectRoot_WalkStopsAtToplevel(t *testing.T) {
	parent := t.TempDir()
	writeMarker(t, parent)
	repo := initRepo(t, filepath.Join(parent, "R"))
	start := mkdirs(t, filepath.Join(repo, "a"))
	top := repoToplevel(t, repo)

	root, kind := mustFind(t, start)
	if kind != ProjectRootGitToplevel || root != top {
		t.Errorf("got (%q, %v), want (%q, GitToplevel) — the marker above the repo must not be seen", root, kind, top)
	}
}

func TestFindProjectRoot_OutsideGitRepo(t *testing.T) {
	withMarker := t.TempDir()
	writeMarker(t, withMarker)
	if root, kind := mustFind(t, withMarker); kind != ProjectRootMarker || root != canon(t, withMarker) {
		t.Errorf("got (%q, %v), want (%q, Marker)", root, kind, canon(t, withMarker))
	}

	bare := t.TempDir()
	if root, kind := mustFind(t, bare); kind != ProjectRootNone || root != "" {
		t.Errorf("got (%q, %v), want (\"\", None)", root, kind)
	}
}

func TestFindProjectRoot_RelativeStart(t *testing.T) {
	repo := initRepo(t, t.TempDir())
	writeMarker(t, repo)
	start := mkdirs(t, filepath.Join(repo, "a"))
	t.Chdir(start)

	root, kind := mustFind(t, ".")
	want := canon(t, filepath.Dir(mustGetwd(t)))
	if kind != ProjectRootMarker || root != want {
		t.Errorf("got (%q, %v), want (%q, Marker)", root, kind, want)
	}
}

// TestFindProjectRoot_SymlinkedRepoPath proves the os.SameFile boundary check:
// reached through a symlink, the start path never string-matches the
// symlink-resolved toplevel git reports, so a string comparison would walk to
// the filesystem root instead of stopping at the repo.
func TestFindProjectRoot_SymlinkedRepoPath(t *testing.T) {
	real := initRepo(t, filepath.Join(t.TempDir(), "real"))
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	mkdirs(t, filepath.Join(real, "a"))
	top := repoToplevel(t, real)

	root, kind := mustFind(t, filepath.Join(link, "a"))
	if kind != ProjectRootGitToplevel || root != top {
		t.Errorf("got (%q, %v), want (%q, GitToplevel)", root, kind, top)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return cwd
}

func TestLoad_ResolvesMarkerAboveCwd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	repo := initRepo(t, t.TempDir())
	sub := mkdirs(t, filepath.Join(repo, "sub"))
	if err := os.WriteFile(filepath.Join(sub, ".sakusen.yml"), []byte(`
workflows:
  - name: subproject
    steps:
      - name: implementing
        prompt: "x"
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Chdir(mkdirs(t, filepath.Join(sub, "a")))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	wantDir := canon(t, filepath.Dir(mustGetwd(t)))
	if cfg.ProjectDir != wantDir {
		t.Errorf("ProjectDir = %q, want %q", cfg.ProjectDir, wantDir)
	}
	if !cfg.ProjectConfigFound {
		t.Error("ProjectConfigFound = false, want true")
	}
	if cfg.GetWorkflow("subproject") == nil {
		t.Error("workflow from the marker file not loaded")
	}
}

func TestLoad_NoMarkerUsesToplevel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	repo := initRepo(t, t.TempDir())
	top := repoToplevel(t, repo)
	t.Chdir(mkdirs(t, filepath.Join(repo, "a")))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ProjectDir != top {
		t.Errorf("ProjectDir = %q, want %q", cfg.ProjectDir, top)
	}
	if cfg.ProjectConfigFound {
		t.Error("ProjectConfigFound = true, want false")
	}
}

func TestLoad_OutsideGitRepoFallsBackToCwd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	t.Chdir(t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ProjectDir != mustGetwd(t) {
		t.Errorf("ProjectDir = %q, want %q", cfg.ProjectDir, mustGetwd(t))
	}
	if cfg.ProjectConfigFound {
		t.Error("ProjectConfigFound = true, want false")
	}
}

func TestCanonicalPath_ResolvesSymlink(t *testing.T) {
	target := mkdirs(t, filepath.Join(t.TempDir(), "real"))
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	got, err := CanonicalPath(link)
	if err != nil {
		t.Fatalf("CanonicalPath: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCanonicalPath_MissingPathFallsBackToAbs(t *testing.T) {
	t.Chdir(t.TempDir())
	rel := filepath.Join("does", "not", "exist")
	want, err := filepath.Abs(rel)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}

	got, err := CanonicalPath(rel)
	if err != nil {
		t.Fatalf("CanonicalPath: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFindProjectRoot_SymlinkedMarkerDirResolves(t *testing.T) {
	target := mkdirs(t, filepath.Join(t.TempDir(), "real"))
	writeMarker(t, target)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	root, kind, err := FindProjectRoot(link)
	if err != nil {
		t.Fatalf("FindProjectRoot: %v", err)
	}
	if kind != ProjectRootMarker {
		t.Errorf("kind = %v, want ProjectRootMarker", kind)
	}
	if root != want {
		t.Errorf("root = %q, want %q", root, want)
	}
}

func TestFindProjectRoot_SymlinkedRepoSubdirResolves(t *testing.T) {
	repo := initRepo(t, filepath.Join(t.TempDir(), "repo"))
	sub := mkdirs(t, filepath.Join(repo, "sub"))
	writeMarker(t, sub)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	want, err := filepath.EvalSymlinks(sub)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	root, kind, err := FindProjectRoot(filepath.Join(link, "sub"))
	if err != nil {
		t.Fatalf("FindProjectRoot: %v", err)
	}
	if kind != ProjectRootMarker {
		t.Errorf("kind = %v, want ProjectRootMarker", kind)
	}
	if root != want {
		t.Errorf("root = %q, want %q", root, want)
	}
}
