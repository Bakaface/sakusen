package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// initTestRepo creates a temporary git repo with an initial commit and returns its path.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "checkout", "-b", "main"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v failed: %v\n%s", args, err, out)
		}
	}

	// Create initial commit
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "-A"},
		{"git", "commit", "-m", "initial commit"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v failed: %v\n%s", args, err, out)
		}
	}

	return dir
}

func TestForceDeleteBranch_AfterSquashMerge(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	// Create a feature branch with a commit
	for _, args := range [][]string{
		{"git", "checkout", "-b", "feature-branch"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "-A"},
		{"git", "commit", "-m", "add feature"},
		{"git", "checkout", "main"},
		{"git", "merge", "--squash", "feature-branch"},
		{"git", "commit", "-m", "squash merge feature-branch"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// DeleteBranch (-d) should fail because squash merge doesn't track merge history
	err := r.DeleteBranch("feature-branch")
	if err == nil {
		t.Fatal("expected DeleteBranch to fail after squash merge, but it succeeded")
	}

	// ForceDeleteBranch (-D) should succeed
	err = r.ForceDeleteBranch("feature-branch")
	if err != nil {
		t.Fatalf("ForceDeleteBranch should succeed after squash merge: %v", err)
	}
}

func TestDeleteBranch_FullyMerged(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	// Create a feature branch with a commit
	for _, args := range [][]string{
		{"git", "checkout", "-b", "feature-branch"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "-A"},
		{"git", "commit", "-m", "add feature"},
		{"git", "checkout", "main"},
		{"git", "merge", "feature-branch"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// DeleteBranch (-d) should succeed for a fully merged branch
	err := r.DeleteBranch("feature-branch")
	if err != nil {
		t.Fatalf("DeleteBranch should succeed for fully merged branch: %v", err)
	}
}

func TestForceDeleteBranch_NonExistent(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	err := r.ForceDeleteBranch("nonexistent-branch")
	if err == nil {
		t.Fatal("expected ForceDeleteBranch to fail for nonexistent branch")
	}
}

func TestDiffStat_WithChanges(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	// Create a feature branch with changes
	for _, args := range [][]string{
		{"git", "checkout", "-b", "feature"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "new_file.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "-A"},
		{"git", "commit", "-m", "add new file"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	stat, err := r.DiffStat(repo, "main")
	if err != nil {
		t.Fatalf("DiffStat failed: %v", err)
	}
	if stat == "" {
		t.Error("expected non-empty diff stat for branch with changes")
	}
	if !strings.Contains(stat, "new_file.go") {
		t.Errorf("expected diff stat to mention new_file.go, got: %s", stat)
	}
}

func TestDiffStat_NoChanges(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	// Stay on main, no changes
	stat, err := r.DiffStat(repo, "main")
	if err != nil {
		t.Fatalf("DiffStat failed: %v", err)
	}
	if stat != "" {
		t.Errorf("expected empty diff stat when no changes, got: %s", stat)
	}
}

func TestMergeBranch_SequentialMerges(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	// Create two feature branches with non-overlapping changes
	branches := []struct {
		name string
		file string
		body string
	}{
		{"feature-a", "a.txt", "feature A content"},
		{"feature-b", "b.txt", "feature B content"},
	}

	for _, b := range branches {
		for _, args := range [][]string{
			{"git", "checkout", "main"},
			{"git", "checkout", "-b", b.name},
		} {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = repo
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v failed: %v\n%s", args, err, out)
			}
		}
		if err := os.WriteFile(filepath.Join(repo, b.file), []byte(b.body), 0644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"git", "add", "-A"},
			{"git", "commit", "-m", "add " + b.file},
		} {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = repo
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v failed: %v\n%s", args, err, out)
			}
		}
	}

	// Return to main before merging
	cmd := exec.Command("git", "checkout", "main")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checkout main failed: %v\n%s", err, out)
	}

	// Merge both branches sequentially (as the mutex would enforce). feature-a
	// fast-forwards from init; feature-b is rebased onto main (now at A) so it
	// can also fast-forward.
	if err := r.MergeBranch("feature-a"); err != nil {
		t.Fatalf("MergeBranch feature-a failed: %v", err)
	}
	runGit(t, repo, "checkout", "feature-b")
	if err := r.RebaseBranch(repo, "main"); err != nil {
		t.Fatalf("RebaseBranch feature-b failed: %v", err)
	}
	runGit(t, repo, "checkout", "main")
	if err := r.MergeBranch("feature-b"); err != nil {
		t.Fatalf("MergeBranch feature-b failed: %v", err)
	}

	// Verify both files exist on main
	for _, b := range branches {
		content, err := os.ReadFile(filepath.Join(repo, b.file))
		if err != nil {
			t.Fatalf("expected %s to exist on main after merge: %v", b.file, err)
		}
		if string(content) != b.body {
			t.Errorf("expected %s content %q, got %q", b.file, b.body, string(content))
		}
	}

	// Verify we're on main
	branch, err := r.GetCurrentBranch(repo)
	if err != nil {
		t.Fatalf("GetCurrentBranch failed: %v", err)
	}
	if branch != "main" {
		t.Errorf("expected to be on main, got %s", branch)
	}
}

func TestMergeBranch_ConcurrentWithMutex(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	// Create multiple feature branches with non-overlapping changes
	const numBranches = 5
	for i := range numBranches {
		branchName := fmt.Sprintf("feature-%d", i)
		fileName := fmt.Sprintf("file-%d.txt", i)

		for _, args := range [][]string{
			{"git", "checkout", "main"},
			{"git", "checkout", "-b", branchName},
		} {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = repo
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v failed: %v\n%s", args, err, out)
			}
		}
		if err := os.WriteFile(filepath.Join(repo, fileName), []byte(fmt.Sprintf("content %d", i)), 0644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"git", "add", "-A"},
			{"git", "commit", "-m", fmt.Sprintf("add %s", fileName)},
		} {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = repo
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v failed: %v\n%s", args, err, out)
			}
		}
	}

	// Return to main
	cmd := exec.Command("git", "checkout", "main")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checkout main failed: %v\n%s", err, out)
	}

	// Merge all branches concurrently, protected by a mutex (simulating the Engine behavior)
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make([]error, numBranches)

	for i := range numBranches {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			branchName := fmt.Sprintf("feature-%d", idx)
			mu.Lock()
			defer mu.Unlock()
			// First task fast-forwards; later tasks must rebase onto the
			// updated main first because --ff-only refuses when their fork
			// point has fallen behind.
			err := r.MergeBranch(branchName)
			if err != nil {
				runGit(t, repo, "checkout", branchName)
				if rebaseErr := r.RebaseBranch(repo, "main"); rebaseErr != nil {
					errs[idx] = rebaseErr
					runGit(t, repo, "checkout", "main")
					return
				}
				runGit(t, repo, "checkout", "main")
				err = r.MergeBranch(branchName)
			}
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	// All merges should succeed when serialized by the mutex
	for i, err := range errs {
		if err != nil {
			t.Errorf("MergeBranch feature-%d failed: %v", i, err)
		}
	}

	// Verify all files exist on main
	for i := range numBranches {
		fileName := fmt.Sprintf("file-%d.txt", i)
		if _, err := os.ReadFile(filepath.Join(repo, fileName)); err != nil {
			t.Errorf("expected %s to exist on main after merge: %v", fileName, err)
		}
	}
}

func TestMergeBranch_PreservesHistory(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	// Create a feature branch with multiple commits
	runGit(t, repo, "checkout", "-b", "feature")

	commits := []struct {
		file string
		body string
		msg  string
	}{
		{"step1.txt", "first", "feat: step 1"},
		{"step2.txt", "second", "feat: step 2"},
		{"step3.txt", "third", "feat: step 3"},
	}
	for _, c := range commits {
		if err := os.WriteFile(filepath.Join(repo, c.file), []byte(c.body), 0644); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "add", "-A")
		runGit(t, repo, "commit", "-m", c.msg)
	}

	// Capture the tip of the feature branch before merging
	cmd := exec.Command("git", "rev-parse", "feature")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse feature failed: %v\n%s", err, out)
	}
	featureTip := strings.TrimSpace(string(out))

	runGit(t, repo, "checkout", "main")
	if err := r.MergeBranch("feature"); err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	// main hasn't advanced since feature branched off, so the merge fast-forwards:
	// HEAD has a single parent and points at the original feature tip.
	parentsOut := runGitOutput(t, repo, "rev-list", "--parents", "-n", "1", "HEAD")
	parents := strings.Fields(strings.TrimSpace(parentsOut))
	if len(parents) != 2 {
		t.Fatalf("expected fast-forward (1 parent, 2 fields incl. self), got %d: %v", len(parents)-1, parents)
	}
	if parents[0] != featureTip {
		t.Errorf("expected main HEAD to be feature tip %s after fast-forward, got %s", featureTip, parents[0])
	}

	// All individual feature commits must be reachable from main.
	logOut := runGitOutput(t, repo, "log", "--format=%s", "main")
	for _, c := range commits {
		if !strings.Contains(logOut, c.msg) {
			t.Errorf("expected commit %q to be reachable from main, log was:\n%s", c.msg, logOut)
		}
	}
}

func TestMergeBranch_NonFastForwardPreservesUserEdits(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	// Add shared.txt so the branches actually diverge.
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add shared.txt")

	// branch-a forks from main and changes shared.txt
	runGit(t, repo, "checkout", "-b", "branch-a")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("branch A changes"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "modify shared.txt in branch-a")

	// branch-b forks from the same point and changes shared.txt differently
	runGit(t, repo, "checkout", "main")
	runGit(t, repo, "checkout", "-b", "branch-b")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("branch B changes"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "modify shared.txt in branch-b")

	runGit(t, repo, "checkout", "main")

	// First merge fast-forwards.
	if err := r.MergeBranch("branch-a"); err != nil {
		t.Fatalf("first merge should fast-forward: %v", err)
	}

	// An unrelated uncommitted user edit in the main checkout.
	if err := os.WriteFile(filepath.Join(repo, "user.txt"), []byte("user edit"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "user.txt")
	headBefore := strings.TrimSpace(runGitOutput(t, repo, "rev-parse", "HEAD"))

	// Second merge must fail — branch-b is not descended from main's new tip,
	// so --ff-only refuses. The caller's job is to rebase branch-b first.
	if err := r.MergeBranch("branch-b"); err == nil {
		t.Fatal("expected --ff-only merge to refuse non-descendant branch")
	}

	if got := strings.TrimSpace(runGitOutput(t, repo, "rev-parse", "HEAD")); got != headBefore {
		t.Errorf("HEAD moved after refused merge: %s -> %s", headBefore, got)
	}
	if statusOut := runGitOutput(t, repo, "status", "--porcelain"); strings.TrimSpace(statusOut) != "A  user.txt" {
		t.Errorf("expected only the staged user edit after refused merge, got:\n%s", statusOut)
	}
	if data, err := os.ReadFile(filepath.Join(repo, "user.txt")); err != nil || string(data) != "user edit" {
		t.Errorf("user edit lost after refused merge: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Errorf("expected no MERGE_HEAD after refused merge, stat err: %v", err)
	}
	runGit(t, repo, "rm", "--cached", "-q", "user.txt")
	if err := os.Remove(filepath.Join(repo, "user.txt")); err != nil {
		t.Fatal(err)
	}

	// A descendant branch should still merge cleanly afterwards.
	runGit(t, repo, "checkout", "-b", "branch-c")
	if err := os.WriteFile(filepath.Join(repo, "other.txt"), []byte("no conflict"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add other.txt")
	runGit(t, repo, "checkout", "main")

	if err := r.MergeBranch("branch-c"); err != nil {
		t.Fatalf("descendant merge after refusal should succeed: %v", err)
	}
}

func TestMergeBranch_DirtyTouchedFilePreservesUserEdit(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add shared.txt")

	runGit(t, repo, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("feature change"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "modify shared.txt")
	runGit(t, repo, "checkout", "main")

	// The user edits the same file the fast-forward would touch.
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("user edit"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := r.MergeBranch("feature"); err == nil {
		t.Fatal("expected merge to refuse overwriting a dirty file")
	}

	if data, err := os.ReadFile(filepath.Join(repo, "shared.txt")); err != nil || string(data) != "user edit" {
		t.Errorf("user edit lost after refused merge: %q, %v", data, err)
	}
	if branch, _ := r.GetCurrentBranch(repo); branch != "main" {
		t.Errorf("expected to stay on main, got %s", branch)
	}
}

func TestRebaseBranch(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	// Create a file on main
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add base.txt")

	// Create a feature branch
	runGit(t, repo, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add feature.txt")

	// Advance main with a non-conflicting change
	runGit(t, repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "main-only.txt"), []byte("main advance"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "advance main")

	// Rebase feature onto main
	runGit(t, repo, "checkout", "feature")
	if err := r.RebaseBranch(repo, "main"); err != nil {
		t.Fatalf("RebaseBranch should succeed for non-conflicting changes: %v", err)
	}

	// Verify feature branch has both its own commit and main's advance
	if _, err := os.ReadFile(filepath.Join(repo, "feature.txt")); err != nil {
		t.Error("feature.txt should exist after rebase")
	}
	if _, err := os.ReadFile(filepath.Join(repo, "main-only.txt")); err != nil {
		t.Error("main-only.txt should exist after rebase (rebased onto main)")
	}
}

func TestRebaseBranch_ConflictAborts(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	// Create shared.txt on main
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add shared.txt")

	// Create feature branch modifying shared.txt
	runGit(t, repo, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("feature version"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "modify shared.txt in feature")

	// Advance main with a conflicting change to shared.txt
	runGit(t, repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("main version"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "modify shared.txt on main")

	// Rebase should fail and abort cleanly
	runGit(t, repo, "checkout", "feature")
	err := r.RebaseBranch(repo, "main")
	if err == nil {
		t.Fatal("expected rebase to fail due to conflict")
	}

	// Verify the branch is clean (rebase --abort worked)
	statusOut := runGitOutput(t, repo, "status", "--porcelain")
	if strings.TrimSpace(statusOut) != "" {
		t.Errorf("expected clean state after failed rebase, got:\n%s", statusOut)
	}

	// Verify we're still on the feature branch (not in detached HEAD state)
	branch, err := r.GetCurrentBranch(repo)
	if err != nil {
		t.Fatalf("GetCurrentBranch failed: %v", err)
	}
	if branch != "feature" {
		t.Errorf("expected to be on feature branch after aborted rebase, got %s", branch)
	}
}

func TestMergeBranch_RetryAfterRebase(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	// Create initial shared file
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("line1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add shared.txt")

	// Create branch-a: adds to a different file
	runGit(t, repo, "checkout", "-b", "branch-a")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("from branch a"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add a.txt")

	// Create branch-b from main: adds to a different file
	runGit(t, repo, "checkout", "main")
	runGit(t, repo, "checkout", "-b", "branch-b")
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("from branch b"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add b.txt")

	runGit(t, repo, "checkout", "main")

	// Merge branch-a first — advances main
	var mu sync.Mutex
	mu.Lock()
	if err := r.MergeBranch("branch-a"); err != nil {
		mu.Unlock()
		t.Fatalf("first merge failed: %v", err)
	}

	// branch-b now isn't descended from main, so --ff-only must refuse it.
	mergeErr := r.MergeBranch("branch-b")
	if mergeErr == nil {
		mu.Unlock()
		t.Fatal("expected --ff-only to refuse branch-b before rebase")
	}
	mu.Unlock()

	// Rebase branch-b onto the new main, then retry.
	runGit(t, repo, "checkout", "branch-b")
	if err := r.RebaseBranch(repo, "main"); err != nil {
		t.Fatalf("rebase should succeed for non-conflicting branches: %v", err)
	}
	runGit(t, repo, "checkout", "main")

	mu.Lock()
	if err := r.MergeBranch("branch-b"); err != nil {
		mu.Unlock()
		t.Fatalf("merge after rebase should succeed: %v", err)
	}
	mu.Unlock()

	// Verify both files exist
	if _, err := os.ReadFile(filepath.Join(repo, "a.txt")); err != nil {
		t.Error("a.txt should exist on main")
	}
	if _, err := os.ReadFile(filepath.Join(repo, "b.txt")); err != nil {
		t.Error("b.txt should exist on main")
	}

	// Linear history: no merge commits should have been created.
	parentsOut := runGitOutput(t, repo, "log", "--merges", "--oneline", "main")
	if strings.TrimSpace(parentsOut) != "" {
		t.Errorf("expected linear history, but found merge commits:\n%s", parentsOut)
	}
}

func TestRebaseInto_ConflictLeavesRebaseInProgress(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add shared.txt")

	// feature edits shared.txt
	runGit(t, repo, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("feature edit"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "feature edit")

	// main also edits shared.txt
	runGit(t, repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("main edit"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "main edit")

	// Conflict path: RebaseInto must NOT auto-abort, so the caller can resolve.
	runGit(t, repo, "checkout", "feature")
	if err := r.RebaseInto(repo, "main"); err == nil {
		t.Fatal("expected RebaseInto to surface conflict as error")
	}

	conflicts, err := r.GetConflictedFiles(repo)
	if err != nil {
		t.Fatalf("GetConflictedFiles after RebaseInto: %v", err)
	}
	if len(conflicts) != 1 || conflicts[0] != "shared.txt" {
		t.Errorf("expected shared.txt to be the only conflict, got %v", conflicts)
	}

	if _, statErr := os.Stat(filepath.Join(repo, ".git", "rebase-merge")); os.IsNotExist(statErr) {
		// Some git versions use rebase-apply for non-interactive rebase.
		if _, fallback := os.Stat(filepath.Join(repo, ".git", "rebase-apply")); os.IsNotExist(fallback) {
			t.Fatal("expected an in-progress rebase directory after conflict")
		}
	}

	if err := r.AbortRebase(repo); err != nil {
		t.Fatalf("AbortRebase failed: %v", err)
	}

	statusOut := runGitOutput(t, repo, "status", "--porcelain")
	if strings.TrimSpace(statusOut) != "" {
		t.Errorf("expected clean state after AbortRebase, got:\n%s", statusOut)
	}
}

func TestContinueRebase_ResolvesConflict(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add shared.txt")

	runGit(t, repo, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("feature edit"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "feature edit")

	runGit(t, repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("main edit"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "main edit")

	runGit(t, repo, "checkout", "feature")
	if err := r.RebaseInto(repo, "main"); err == nil {
		t.Fatal("expected initial rebase to conflict")
	}

	// Resolve by taking a merged content, then continue.
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("resolved"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := r.ContinueRebase(repo); err != nil {
		t.Fatalf("ContinueRebase after resolution failed: %v", err)
	}

	// Resulting branch must contain main's commit + the rebased feature commit, no merge commit.
	if branch, _ := r.GetCurrentBranch(repo); branch != "feature" {
		t.Errorf("expected to be on feature after successful rebase, got %s", branch)
	}
	mergesOut := runGitOutput(t, repo, "log", "--merges", "--oneline", "feature")
	if strings.TrimSpace(mergesOut) != "" {
		t.Errorf("expected linear history after rebase, found merges:\n%s", mergesOut)
	}
	content, err := os.ReadFile(filepath.Join(repo, "shared.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "resolved" {
		t.Errorf("expected shared.txt to contain resolution, got %q", string(content))
	}
}

func TestConventionalCommitFromTitle(t *testing.T) {
	tests := []struct {
		title string
		want  string
	}{
		// Already conventional — pass through
		{"feat: add login", "feat: add login"},
		{"fix(auth): handle nil token", "fix(auth): handle nil token"},
		{"refactor!: rewrite parser", "refactor!: rewrite parser"},

		// Inferred from leading verb
		{"Fix the login bug", "fix: the login bug"},
		{"Refactor database layer", "refactor: database layer"},
		{"Test edge cases in parser", "test: edge cases in parser"},
		{"Optimize query performance", "perf: query performance"},

		// Default to feat
		{"Add user authentication", "feat: add user authentication"},
		{"Implement dark mode", "feat: implement dark mode"},
		{"Update the sidebar layout", "feat: update the sidebar layout"},
	}

	for _, tt := range tests {
		got := ConventionalCommitFromTitle(tt.title)
		if got != tt.want {
			t.Errorf("ConventionalCommitFromTitle(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
}

func TestGetLastCommitHash(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	hash, err := r.GetLastCommitHash(repo)
	if err != nil {
		t.Fatalf("GetLastCommitHash failed: %v", err)
	}

	// SHA-1 hash should be 40 hex characters
	if len(hash) != 40 {
		t.Errorf("expected 40 character hash, got %d chars: %q", len(hash), hash)
	}

	// Make another commit and verify hash changes
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "new commit")

	hash2, err := r.GetLastCommitHash(repo)
	if err != nil {
		t.Fatalf("GetLastCommitHash failed: %v", err)
	}

	if hash == hash2 {
		t.Error("expected different hash after new commit")
	}
}

func TestRevertCommits(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	// Create two commits with separate files
	if err := os.WriteFile(filepath.Join(repo, "file1.txt"), []byte("file1 content"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add file1")
	hash1, err := r.GetLastCommitHash(repo)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(repo, "file2.txt"), []byte("file2 content"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add file2")
	hash2, err := r.GetLastCommitHash(repo)
	if err != nil {
		t.Fatal(err)
	}

	// Both files should exist
	if _, err := os.ReadFile(filepath.Join(repo, "file1.txt")); err != nil {
		t.Fatal("file1.txt should exist before revert")
	}
	if _, err := os.ReadFile(filepath.Join(repo, "file2.txt")); err != nil {
		t.Fatal("file2.txt should exist before revert")
	}

	// Revert both commits (newest first automatically)
	if err := r.RevertCommits([]string{hash1, hash2}); err != nil {
		t.Fatalf("RevertCommits failed: %v", err)
	}

	// Both files should be removed after revert
	if _, err := os.ReadFile(filepath.Join(repo, "file1.txt")); err == nil {
		t.Error("file1.txt should not exist after revert")
	}
	if _, err := os.ReadFile(filepath.Join(repo, "file2.txt")); err == nil {
		t.Error("file2.txt should not exist after revert")
	}

	// Verify repo is clean
	statusOut := runGitOutput(t, repo, "status", "--porcelain")
	if strings.TrimSpace(statusOut) != "" {
		t.Errorf("expected clean working directory after revert, got:\n%s", statusOut)
	}
}

func TestRevertCommits_EmptyList(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	// Reverting empty list should be a no-op
	if err := r.RevertCommits([]string{}); err != nil {
		t.Fatalf("RevertCommits with empty list should succeed: %v", err)
	}
}

func TestRevertCommits_SingleCommit(t *testing.T) {
	repo := initTestRepo(t)
	r := NewRepo(repo)

	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add feature")
	hash, err := r.GetLastCommitHash(repo)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.RevertCommits([]string{hash}); err != nil {
		t.Fatalf("RevertCommits failed: %v", err)
	}

	if _, err := os.ReadFile(filepath.Join(repo, "feature.txt")); err == nil {
		t.Error("feature.txt should not exist after revert")
	}
}

// runGit is a test helper that runs a git command and fails on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// runGitOutput is a test helper that runs a git command and returns stdout.
func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestChangedPaths(t *testing.T) {
	dir := initTestRepo(t)
	r := NewRepo(dir)

	if paths, err := r.ChangedPaths(nil); err != nil || len(paths) != 0 {
		t.Fatalf("clean repo: ChangedPaths = %v, %v; want empty", paths, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("edited"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.tmp"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "scratch"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scratch", "a.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	paths, err := r.ChangedPaths(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"M README.md", "?? notes.tmp", "?? scratch/"}
	if strings.Join(paths, "|") != strings.Join(want, "|") {
		t.Errorf("ChangedPaths(nil) = %q, want %q", paths, want)
	}

	paths, err = r.ChangedPaths([]string{"scratch/**", "*.tmp"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, "|") != "M README.md" {
		t.Errorf("ChangedPaths(ignore) = %q, want only README.md", paths)
	}

	paths, err = r.ChangedPaths([]string{"scratch/**", "*.tmp", "README.md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Errorf("fully ignored dirty tree: ChangedPaths = %q, want empty", paths)
	}
}
