package workflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	gitpkg "github.com/Bakaface/sakusen/internal/git"
	"github.com/Bakaface/sakusen/internal/task"
)

// initFastTrackTestRepo creates a throwaway git repo with a single initial
// commit, following the same setup pattern as internal/git's initTestRepo
// (not reusable directly — unexported across packages).
func initFastTrackTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	runFastTrackGit(t, dir, "init")
	runFastTrackGit(t, dir, "config", "user.email", "test@test.com")
	runFastTrackGit(t, dir, "config", "user.name", "Test")
	runFastTrackGit(t, dir, "checkout", "-b", "main")

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init"), 0644); err != nil {
		t.Fatal(err)
	}
	runFastTrackGit(t, dir, "add", "-A")
	runFastTrackGit(t, dir, "commit", "-m", "initial commit")

	return dir
}

func runFastTrackGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

// TestCheckFastTrackCompletion exercises the single-owner no-change
// fast-track decision (see CheckFastTrackCompletion's doc comment): the
// daemon's finalizeCompletedTask and advanceTmuxTask previously each
// re-derived this via HasMeaningfulChanges + a local noiseFiles list. This
// test drives the decision against real temp git repos the same way existing
// engine tests do (Engine holds a concrete *git.Repo, not an interface, and
// HasMeaningfulChanges operates on the workDir argument rather than the
// Repo's own root, so any Repo instance works here — see the doc on
// e.repo's construction below).
func TestCheckFastTrackCompletion(t *testing.T) {
	// HasMeaningfulChanges takes workDir as an explicit argument and never
	// touches the Repo's own root, so a single Repo scoped to an unrelated
	// temp dir is fine to reuse across all subtests below.
	e := &Engine{repo: gitpkg.NewRepo(t.TempDir())}

	t.Run("non-worktree task never fast-tracks", func(t *testing.T) {
		repo := initFastTrackTestRepo(t)
		tk := &task.Task{ID: 1, Worktree: false, WorktreePath: repo}
		got, err := e.CheckFastTrackCompletion(tk, []string{".claude-output.log"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got {
			t.Error("expected fastTrack=false for a non-worktree task")
		}
	})

	t.Run("empty worktree path never fast-tracks", func(t *testing.T) {
		tk := &task.Task{ID: 2, Worktree: true, WorktreePath: ""}
		got, err := e.CheckFastTrackCompletion(tk, []string{".claude-output.log"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got {
			t.Error("expected fastTrack=false when WorktreePath is empty")
		}
	})

	t.Run("only noise-file changes fast-tracks", func(t *testing.T) {
		repo := initFastTrackTestRepo(t)
		if err := os.WriteFile(filepath.Join(repo, ".claude-output.log"), []byte("log noise"), 0644); err != nil {
			t.Fatal(err)
		}
		runFastTrackGit(t, repo, "add", "-A")
		runFastTrackGit(t, repo, "commit", "-m", "noise only")

		tk := &task.Task{ID: 3, Worktree: true, WorktreePath: repo}
		got, err := e.CheckFastTrackCompletion(tk, []string{".claude-output.log"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got {
			t.Error("expected fastTrack=true when only excluded noise files changed")
		}
	})

	t.Run("real file changes do not fast-track", func(t *testing.T) {
		repo := initFastTrackTestRepo(t)
		if err := os.WriteFile(filepath.Join(repo, "feature.go"), []byte("package main"), 0644); err != nil {
			t.Fatal(err)
		}
		runFastTrackGit(t, repo, "add", "-A")
		runFastTrackGit(t, repo, "commit", "-m", "real work")

		tk := &task.Task{ID: 4, Worktree: true, WorktreePath: repo}
		got, err := e.CheckFastTrackCompletion(tk, []string{".claude-output.log"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got {
			t.Error("expected fastTrack=false when a real file changed")
		}
	})

	t.Run("meaningful-changes check error is surfaced, not fast-tracked", func(t *testing.T) {
		notARepo := t.TempDir() // no .git here — git status will fail
		tk := &task.Task{ID: 5, Worktree: true, WorktreePath: notARepo}
		got, err := e.CheckFastTrackCompletion(tk, []string{".claude-output.log"})
		if err == nil {
			t.Fatal("expected an error for a non-git WorktreePath")
		}
		if got {
			t.Error("expected fastTrack=false alongside the error")
		}
	})
}
