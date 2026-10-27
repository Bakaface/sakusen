package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckoutBranch(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	// Create a feature branch
	runGit(t, repo, "checkout", "-b", "feature-branch")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add feature")

	// Checkout main using our function
	if err := r.CheckoutBranch("main"); err != nil {
		t.Fatalf("CheckoutBranch failed: %v", err)
	}

	branch, err := r.GetCurrentBranch(repo)
	if err != nil {
		t.Fatalf("GetCurrentBranch failed: %v", err)
	}
	if branch != "main" {
		t.Errorf("expected branch main, got %s", branch)
	}
}

func TestCheckoutBranch_InvalidBranch(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	err := r.CheckoutBranch("nonexistent-branch")
	if err == nil {
		t.Fatal("expected error for nonexistent branch")
	}
}

func TestGetDefaultBranch(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	branch := r.GetDefaultBranch()
	if branch != "main" {
		t.Errorf("expected default branch main, got %s", branch)
	}
}

func TestGetDefaultBranch_Master(t *testing.T) {
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "checkout", "-b", "master"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v failed: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "initial commit")

	branch := NewRepo(dir).GetDefaultBranch()
	if branch != "master" {
		t.Errorf("expected default branch master, got %s", branch)
	}
}

func TestCheckoutBranch_AutoCheckoutMainAfterReattach(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	// Create a feature branch with a commit
	runGit(t, repo, "checkout", "-b", "feature-branch")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add feature")
	runGit(t, repo, "checkout", "main")

	// Create a worktree on the feature branch
	worktreePath := filepath.Join(t.TempDir(), "worktree")
	runGit(t, repo, "worktree", "add", worktreePath, "feature-branch")

	// Detach the worktree HEAD (simulates "D" keybind)
	if err := DetachWorktreeHead(worktreePath); err != nil {
		t.Fatalf("DetachWorktreeHead failed: %v", err)
	}
	if !IsWorktreeDetached(worktreePath) {
		t.Fatal("expected worktree to be detached")
	}

	// Simulate user checking out the feature branch on root while worktree is detached
	if err := r.CheckoutBranch("feature-branch"); err != nil {
		t.Fatalf("CheckoutBranch to feature-branch failed: %v", err)
	}
	branch, _ := r.GetCurrentBranch(repo)
	if branch != "feature-branch" {
		t.Fatalf("expected root to be on feature-branch, got %s", branch)
	}

	// Now simulate "A" keybind: first checkout main on root, then reattach
	defaultBranch := r.GetDefaultBranch()
	if err := r.CheckoutBranch(defaultBranch); err != nil {
		t.Fatalf("CheckoutBranch to default branch failed: %v", err)
	}

	if err := ReattachWorktreeBranch(worktreePath, "feature-branch"); err != nil {
		t.Fatalf("ReattachWorktreeBranch failed: %v", err)
	}

	// Verify root is on main
	branch, err := r.GetCurrentBranch(repo)
	if err != nil {
		t.Fatalf("GetCurrentBranch failed: %v", err)
	}
	if branch != "main" {
		t.Errorf("expected root to be on main after reattach, got %s", branch)
	}

	// Verify worktree is on the feature branch
	wtBranch, err := r.GetCurrentBranch(worktreePath)
	if err != nil {
		t.Fatalf("GetCurrentBranch for worktree failed: %v", err)
	}
	if wtBranch != "feature-branch" {
		t.Errorf("expected worktree to be on feature-branch, got %s", wtBranch)
	}

	// Cleanup
	runGit(t, repo, "worktree", "remove", worktreePath)
}

func TestEnsureWorktreeAtCreatesFromBaseCommit(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)
	base := strings.TrimSpace(runGitOutput(t, repo, "rev-parse", "HEAD"))
	path := filepath.Join(t.TempDir(), "wt-branch")

	created, err := r.EnsureWorktreeAt(path, "task-x-a", base)
	if err != nil {
		t.Fatalf("EnsureWorktreeAt: %v", err)
	}
	if !created {
		t.Error("created = false, want true for a fresh worktree")
	}
	if branch, _ := r.GetCurrentBranch(path); branch != "task-x-a" {
		t.Errorf("branch = %q, want task-x-a", branch)
	}
	if head, _ := r.GetLastCommitHash(path); head != base {
		t.Errorf("HEAD = %q, want base %q", head, base)
	}
}

func TestEnsureWorktreeAtReusesExisting(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)
	base := strings.TrimSpace(runGitOutput(t, repo, "rev-parse", "HEAD"))
	path := filepath.Join(t.TempDir(), "wt-branch")

	if _, err := r.EnsureWorktreeAt(path, "task-x-a", base); err != nil {
		t.Fatalf("first EnsureWorktreeAt: %v", err)
	}
	marker := filepath.Join(path, "untracked.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}

	created, err := r.EnsureWorktreeAt(path, "task-x-a", base)
	if err != nil {
		t.Fatalf("second EnsureWorktreeAt: %v", err)
	}
	if created {
		t.Error("created = true, want false when the worktree already exists")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("untracked file did not survive reuse: %v", err)
	}
}

func TestEnsureWorktreeAtReaddsMissingWorktreeOnExistingBranch(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)
	base := strings.TrimSpace(runGitOutput(t, repo, "rev-parse", "HEAD"))
	path := filepath.Join(t.TempDir(), "wt-branch")

	if _, err := r.EnsureWorktreeAt(path, "task-x-a", base); err != nil {
		t.Fatalf("EnsureWorktreeAt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "work.txt"), []byte("done"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, path, "add", "-A")
	runGit(t, path, "commit", "-m", "branch work")
	branchHead := strings.TrimSpace(runGitOutput(t, path, "rev-parse", "HEAD"))

	// Delete the directory without pruning: the stale registration must not
	// block re-adding the branch.
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}

	created, err := r.EnsureWorktreeAt(path, "task-x-a", base)
	if err != nil {
		t.Fatalf("EnsureWorktreeAt after removal: %v", err)
	}
	if !created {
		t.Error("created = false, want true after the worktree was removed")
	}
	if head, _ := r.GetLastCommitHash(path); head != branchHead {
		t.Errorf("HEAD = %q, want the branch's own commit %q", head, branchHead)
	}
}
