package merge

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gitpkg "github.com/Bakaface/sortie/internal/git"
	"github.com/Bakaface/sortie/internal/task"
)

// initRepoWithBranch creates a real git repo with a base branch and a feature
// branch that has a single commit. Returns the repo root.
func initRepoWithBranch(t *testing.T, branch string, file, contents string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v failed: %v\n%s", args, err, out)
		}
	}

	run("git", "init")
	run("git", "config", "user.email", "test@test.com")
	run("git", "config", "user.name", "Test")
	run("git", "checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "-A")
	run("git", "commit", "-m", "init")

	run("git", "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(dir, file), []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "-A")
	run("git", "commit", "-m", "feature commit")
	run("git", "checkout", "main")

	return dir
}

// makeWorktree adds a worktree for the named branch under the same repo. The
// merge coordinator commits in this directory then merges back into the base.
func makeWorktree(t *testing.T, repoRoot, branch string) string {
	t.Helper()
	wtPath := filepath.Join(t.TempDir(), "worktree-"+branch)
	cmd := exec.Command("git", "worktree", "add", wtPath, branch)
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree add %s: %v\n%s", branch, err, out)
	}
	return wtPath
}

// TestFinalizeNoneIsNoOp confirms that with no on_complete configured the
// coordinator does not touch the repo at all.
func TestFinalizeNoneIsNoOp(t *testing.T) {
	dir := initRepoWithBranch(t, "feature-1", "feature.txt", "feature\n")
	wt := makeWorktree(t, dir, "feature-1")

	coord := NewCoordinator(dir, &Lock{}, Config{}, nil, nil, nil)

	tk := &task.Task{ID: 1, Title: "test", Branch: "feature-1", Worktree: true, WorktreePath: wt}
	if err := coord.Finalize(context.Background(), tk, "main", ActionNone, nil); err != nil {
		t.Fatalf("Finalize(none) returned error: %v", err)
	}

	// Repo HEAD should still be the initial commit (no merge happened).
	out, err := exec.Command("git", "-C", dir, "log", "--oneline").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(out), "\n") != 1 {
		t.Errorf("expected exactly 1 commit on main, got:\n%s", out)
	}
}

// TestFinalizeMergeHappyPath confirms a clean merge fast-forwards the base
// branch to the feature tip and the commit recorder fires.
func TestFinalizeMergeHappyPath(t *testing.T) {
	dir := initRepoWithBranch(t, "feature-2", "feature.txt", "feature\n")
	wt := makeWorktree(t, dir, "feature-2")

	var recordedHash atomic.Value
	recordCommit := func(taskID int64, hash string) error {
		recordedHash.Store(hash)
		return nil
	}

	coord := NewCoordinator(
		dir, &Lock{},
		Config{MaxAttempts: 1, BlockedPollInterval: 10 * time.Millisecond},
		nil, nil, recordCommit,
	)

	tk := &task.Task{ID: 2, Title: "add feature", Branch: "feature-2", Worktree: true, WorktreePath: wt}
	if err := coord.Finalize(context.Background(), tk, "main", ActionMerge, nil); err != nil {
		t.Fatalf("Finalize returned error: %v", err)
	}

	out, err := exec.Command("git", "-C", dir, "log", "--oneline", "main").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	// Expect 2 commits: init + feature commit (fast-forwarded into main).
	if got := strings.Count(string(out), "\n"); got != 2 {
		t.Errorf("expected 2 commits on main after fast-forward merge, got %d:\n%s", got, out)
	}

	if hash, _ := recordedHash.Load().(string); hash == "" {
		t.Error("expected RecordCommit callback to fire with the merged commit hash")
	}
}

// TestFinalizeNonWorktreeIsNoOp confirms non-worktree mode falls through.
// Non-worktree mode is the path where the agent edits the user's working tree
// directly, so the daemon must never auto-commit or auto-merge.
func TestFinalizeNonWorktreeIsNoOp(t *testing.T) {
	dir := initRepoWithBranch(t, "feature-nw", "f.txt", "x\n")
	coord := NewCoordinator(dir, &Lock{}, Config{}, nil, nil, nil)

	tk := &task.Task{ID: 7, Title: "nw", Branch: "feature-nw", Worktree: false, WorktreePath: dir}
	if err := coord.Finalize(context.Background(), tk, "main", ActionMerge, nil); err != nil {
		t.Fatalf("Finalize on non-worktree task returned error: %v", err)
	}

	// HEAD on main should still be the single initial commit.
	out, _ := exec.Command("git", "-C", dir, "log", "--oneline", "main").CombinedOutput()
	if strings.Count(string(out), "\n") != 1 {
		t.Errorf("non-worktree mode must not merge; saw:\n%s", out)
	}
}

// TestConcurrentFinalizesSerialize is the core deepening test: two
// finalize calls into the same repo must serialize via the per-repo lock and
// both must succeed without producing a corrupt base-repo state.
//
// Without serialization, two concurrent `git merge` invocations against the
// same checkout race on the index — one of them can leave staged conflict
// markers or fail with "another git process is running".
func TestConcurrentFinalizesSerialize(t *testing.T) {
	dir := initRepoWithBranch(t, "fa", "a.txt", "alpha\n")
	// Add a second feature branch on top of main (not on top of fa) so the two
	// merges don't conflict on file contents.
	runIn(t, dir, "git", "checkout", "main")
	runIn(t, dir, "git", "checkout", "-b", "fb")
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("beta\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runIn(t, dir, "git", "add", "-A")
	runIn(t, dir, "git", "commit", "-m", "beta commit")
	runIn(t, dir, "git", "checkout", "main")

	wtA := makeWorktree(t, dir, "fa")
	wtB := makeWorktree(t, dir, "fb")

	lock := &Lock{}

	// Two goroutines share one *Lock. The Lock's invariant — only one
	// goroutine can hold it — is what makes serialization correct; this test
	// asserts that *given* that invariant the end-to-end outcome is right:
	// both merges land cleanly and no staged conflict markers remain on main.
	// (TestConcurrentFinalizesActuallyTakeLock below asserts the lock is
	// actually taken by Finalize.)

	makeCoord := func() *Coordinator {
		return NewCoordinator(
			dir, lock, // shared lock
			// MaxAttempts=2 so the second branch can rebase onto the
			// fast-forwarded base before retrying its --ff-only merge.
			Config{MaxAttempts: 2, BlockedPollInterval: 10 * time.Millisecond},
			nil, nil, nil,
		)
	}

	tkA := &task.Task{ID: 10, Title: "alpha", Branch: "fa", Worktree: true, WorktreePath: wtA}
	tkB := &task.Task{ID: 11, Title: "beta", Branch: "fb", Worktree: true, WorktreePath: wtB}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs <- makeCoord().Finalize(context.Background(), tkA, "main", ActionMerge, nil)
	}()
	go func() {
		defer wg.Done()
		errs <- makeCoord().Finalize(context.Background(), tkB, "main", ActionMerge, nil)
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Finalize returned error: %v", err)
		}
	}

	// Confirm the base repo ended up clean: no staged or unstaged files
	// (the "race leaves merge markers on main" failure mode).
	if dirty, err := gitpkg.NewRepo(dir).HasChanges(); err != nil {
		t.Fatalf("HasChanges: %v", err)
	} else if dirty {
		out, _ := exec.Command("git", "-C", dir, "status", "--porcelain").CombinedOutput()
		t.Fatalf("base repo dirty after concurrent merges:\n%s", out)
	}

	// Expect a linear history of exactly 3 commits on main: init, alpha,
	// beta. The fast-forward + rebase pipeline must never create merge
	// commits when integrating non-conflicting branches.
	out, err := exec.Command("git", "-C", dir, "log", "--oneline", "main").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Count(string(out), "\n")
	if got != 3 {
		t.Errorf("expected 3 commits on main after two merges, got %d:\n%s", got, out)
	}
	mergesOut, err := exec.Command("git", "-C", dir, "log", "--merges", "--oneline", "main").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(mergesOut)) != "" {
		t.Errorf("expected linear history with no merge commits on main, got:\n%s", mergesOut)
	}
}

// TestConcurrentFinalizesActuallyTakeLock verifies serialization more directly:
// it wraps Finalize calls with timing instrumentation and asserts that the
// inner lockedMerge sections never overlap. Done by holding the lock from the
// test side while a real Finalize tries to enter — the Finalize call must
// block until the test releases.
func TestConcurrentFinalizesActuallyTakeLock(t *testing.T) {
	dir := initRepoWithBranch(t, "fc", "c.txt", "gamma\n")
	wt := makeWorktree(t, dir, "fc")

	lock := &Lock{}
	coord := NewCoordinator(
		dir, lock,
		Config{MaxAttempts: 1, BlockedPollInterval: 10 * time.Millisecond},
		nil, nil, nil,
	)

	// Hold the lock from the test side.
	lock.Acquire()

	done := make(chan error, 1)
	go func() {
		tk := &task.Task{ID: 20, Title: "gamma", Branch: "fc", Worktree: true, WorktreePath: wt}
		done <- coord.Finalize(context.Background(), tk, "main", ActionMerge, nil)
	}()

	// The goroutine must be blocked inside lockedMerge (the merge primitive
	// is the only place that takes the lock). Give it time to reach that
	// point — if it returned already, serialization is broken.
	select {
	case err := <-done:
		t.Fatalf("Finalize returned before lock was released (err=%v) — serialization broken", err)
	case <-time.After(150 * time.Millisecond):
		// Expected: still blocked.
	}

	lock.Release()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Finalize returned error after lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Finalize did not complete within 5s after lock release")
	}
}

// TestCleanupOnConflictResolverFailure verifies the "cleanup on failure"
// invariant: when the merge pipeline fails *after* an attempted merge — here,
// because the injected ConflictResolver fails — the base repo must end up in a
// clean state (no staged conflict markers, no half-merged index).
//
// This is the regression that motivated this module. The previous code path
// relied on internal/git's deferred CleanRepoState inside MergeBranch; a
// failure outside MergeBranch (e.g. in the conflict resolver) could leave the
// base repo dirty until the next daemon shutdown.
func TestCleanupOnConflictResolverFailure(t *testing.T) {
	dir := initRepoWithBranch(t, "feat", "shared.txt", "from-feature\n")

	// Mutate the same file on main so the merge conflicts.
	runIn(t, dir, "git", "checkout", "main")
	if err := os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("from-main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runIn(t, dir, "git", "add", "-A")
	runIn(t, dir, "git", "commit", "-m", "main edit")

	wt := makeWorktree(t, dir, "feat")

	// Inject a resolver that simulates the agent failing to resolve the conflict.
	resolverFailure := errors.New("simulated agent failure")
	resolveCalled := 0
	resolver := func(ctx context.Context, t *task.Task, conflictFiles []string) error {
		resolveCalled++
		return resolverFailure
	}

	coord := NewCoordinator(
		dir, &Lock{},
		Config{MaxAttempts: 2, BlockedPollInterval: 10 * time.Millisecond},
		resolver, nil, nil,
	)

	tk := &task.Task{ID: 30, Title: "conflicting feature", Branch: "feat", Worktree: true, WorktreePath: wt}
	err := coord.Finalize(context.Background(), tk, "main", ActionMerge, nil)
	if err == nil {
		t.Fatal("expected Finalize to return error when resolver fails")
	}
	if !strings.Contains(err.Error(), "simulated agent failure") &&
		!strings.Contains(err.Error(), "conflict resolution failed") {
		t.Fatalf("error should propagate resolver failure, got: %v", err)
	}

	if resolveCalled == 0 {
		t.Error("resolver was never invoked — conflict path not exercised")
	}

	// THE INVARIANT under test: base repo is clean after failure.
	if dirty, err := gitpkg.NewRepo(dir).HasChanges(); err != nil {
		t.Fatalf("HasChanges: %v", err)
	} else if dirty {
		out, _ := exec.Command("git", "-C", dir, "status", "--porcelain").CombinedOutput()
		t.Fatalf("base repo dirty after merge failure — cleanup invariant broken:\n%s", out)
	}

	// And no in-progress merge marker on disk.
	if _, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatal("MERGE_HEAD still present after cleanup — merge was not aborted")
	}
}

// TestResolvingConflictsStatusReportedDuringResolver verifies that the
// coordinator surfaces the "resolving-conflicts" phase via the StatusSetter
// while the conflict resolver is running, and restores the previous status
// once the resolver returns. Regression for sortie#95, where tasks stuck in
// conflict resolution displayed as "implementing [wip]" instead of telling
// the user that conflict resolution was actually in flight.
func TestResolvingConflictsStatusReportedDuringResolver(t *testing.T) {
	dir := initRepoWithBranch(t, "feat-status", "shared.txt", "from-feature\n")

	// Mutate the same file on main so the merge conflicts.
	runIn(t, dir, "git", "checkout", "main")
	if err := os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("from-main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runIn(t, dir, "git", "add", "-A")
	runIn(t, dir, "git", "commit", "-m", "main edit")

	wt := makeWorktree(t, dir, "feat-status")

	// Capture the order of status transitions so we can assert that
	// resolving-conflicts is set *before* the resolver runs and the previous
	// status is restored *after* it returns.
	var (
		mu       sync.Mutex
		statuses []task.Status
	)
	setStatus := func(taskID int64, status task.Status) error {
		mu.Lock()
		statuses = append(statuses, status)
		mu.Unlock()
		return nil
	}

	// Resolver records the in-flight status — it must see resolving-conflicts.
	var (
		inFlight    task.Status
		resolverErr error
	)
	resolver := func(ctx context.Context, tk *task.Task, conflictFiles []string) error {
		inFlight = tk.Status
		// Stage the resolution so the merge can complete.
		for _, f := range conflictFiles {
			path := filepath.Join(tk.WorktreePath, f)
			if err := os.WriteFile(path, []byte("resolved\n"), 0644); err != nil {
				resolverErr = err
				return err
			}
		}
		cmd := exec.Command("git", "add", "-A")
		cmd.Dir = tk.WorktreePath
		if out, err := cmd.CombinedOutput(); err != nil {
			resolverErr = errors.New(string(out))
			return resolverErr
		}
		return nil
	}

	coord := NewCoordinator(
		dir, &Lock{},
		Config{MaxAttempts: 2, BlockedPollInterval: 10 * time.Millisecond},
		resolver, setStatus, nil,
	)

	tk := &task.Task{
		ID: 95, Title: "conflict task", Branch: "feat-status",
		Worktree: true, WorktreePath: wt, Status: task.StatusFinalizing,
	}
	if err := coord.Finalize(context.Background(), tk, "main", ActionMerge, nil); err != nil {
		t.Fatalf("Finalize: %v (resolver err: %v)", err, resolverErr)
	}

	if inFlight != task.StatusResolvingConflicts {
		t.Errorf("resolver observed status %q, want %q", inFlight, task.StatusResolvingConflicts)
	}

	mu.Lock()
	got := append([]task.Status(nil), statuses...)
	mu.Unlock()

	if len(got) < 2 {
		t.Fatalf("expected at least 2 status updates (set + restore), got %v", got)
	}
	if got[0] != task.StatusResolvingConflicts {
		t.Errorf("first status update was %q, want %q", got[0], task.StatusResolvingConflicts)
	}
	if got[len(got)-1] != task.StatusFinalizing {
		t.Errorf("last status update was %q, want %q (restored)", got[len(got)-1], task.StatusFinalizing)
	}
	if tk.Status != task.StatusFinalizing {
		t.Errorf("task.Status after resolver = %q, want restored %q", tk.Status, task.StatusFinalizing)
	}
}

// TestCleanupOnTargetBranchWaitCancellation verifies that cancelling the
// context while waiting for a clean target branch leaves the base repo clean
// (no half-applied state).
func TestCleanupOnTargetBranchWaitCancellation(t *testing.T) {
	dir := initRepoWithBranch(t, "feat-cancel", "x.txt", "x\n")

	// Dirty the base repo so waitForCleanTarget enters its polling loop.
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirt\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runIn(t, dir, "git", "add", "dirty.txt")

	wt := makeWorktree(t, dir, "feat-cancel")

	statusUpdates := make(chan task.Status, 4)
	setStatus := func(taskID int64, status task.Status) error {
		statusUpdates <- status
		return nil
	}

	coord := NewCoordinator(
		dir, &Lock{},
		Config{MaxAttempts: 1, BlockedPollInterval: 20 * time.Millisecond},
		nil, setStatus, nil,
	)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()

	tk := &task.Task{ID: 40, Title: "cancelled", Branch: "feat-cancel", Worktree: true, WorktreePath: wt}
	err := coord.Finalize(ctx, tk, "main", ActionMerge, nil)
	if !errors.Is(err, context.Canceled) {
		// errors.Is may not unwrap fmt.Errorf with %w of a wrapped wrapping;
		// fall back to substring match.
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("expected cancellation error, got: %v", err)
		}
	}

	// The status callback should have fired with merge-blocked.
	select {
	case s := <-statusUpdates:
		if s != task.StatusMergeBlocked {
			t.Errorf("expected merge-blocked status, got %s", s)
		}
	default:
		t.Error("merge-blocked status never reported")
	}

	// `git add` left `dirty.txt` staged, so HasChanges *should* still report
	// dirty — that's user state, not our state. What we care about is that
	// the *feature* file (x.txt) and the staged dirty.txt are untouched, and
	// no MERGE_HEAD was left behind.
	if _, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatal("MERGE_HEAD left behind after cancelled target-clean wait")
	}
}

// TestLocksRegistryReturnsSamePtr verifies the daemon-level invariant: two
// calls to Locks.For with the same repoRoot return the same *Lock, so config
// reloads cannot break serialization.
func TestLocksRegistryReturnsSamePtr(t *testing.T) {
	r := NewLocks()
	a := r.For("/a")
	b := r.For("/b")
	a2 := r.For("/a")
	if a != a2 {
		t.Error("Locks.For returned different *Lock for the same repoRoot")
	}
	if a == b {
		t.Error("Locks.For returned the same *Lock for different repoRoots")
	}
}

// TestLocksRegistryConcurrent confirms the registry itself is goroutine-safe.
func TestLocksRegistryConcurrent(t *testing.T) {
	r := NewLocks()
	var wg sync.WaitGroup
	got := make([]*Lock, 50)
	for i := 0; i < 50; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i] = r.For("/shared")
		}()
	}
	wg.Wait()
	for i := 1; i < len(got); i++ {
		if got[i] != got[0] {
			t.Fatalf("registry handed out distinct *Lock for shared repoRoot at i=%d", i)
		}
	}
}

// TestCommitActionSkipsMergeMutex confirms that Action="commit" does not touch
// the per-repo merge lock — only merges into the base repo serialize.
func TestCommitActionSkipsMergeMutex(t *testing.T) {
	dir := initRepoWithBranch(t, "feat-c", "y.txt", "y\n")
	wt := makeWorktree(t, dir, "feat-c")
	// Add an uncommitted change in the worktree so Commit has something to do.
	if err := os.WriteFile(filepath.Join(wt, "added.txt"), []byte("added\n"), 0644); err != nil {
		t.Fatal(err)
	}

	lock := &Lock{}
	lock.Acquire() // hold the lock — commit must not need it.
	defer lock.Release()

	coord := NewCoordinator(
		dir, lock,
		Config{},
		nil, nil, nil,
	)

	done := make(chan error, 1)
	go func() {
		tk := &task.Task{ID: 50, Title: "commit-only", Branch: "feat-c", Worktree: true, WorktreePath: wt}
		done <- coord.Finalize(context.Background(), tk, "main", ActionCommit, nil)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("commit-action Finalize failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Finalize with on_complete=commit blocked on merge lock — should not need it")
	}
}

// runIn is a tiny test helper that runs a command in dir or fails the test.
func runIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// fakeGitRepo is a test-only stand-in for *git.Repo satisfying the
// Coordinator's gitRepo seam. It lets TestFinalizeMergeWithFakeConflictThenSuccess
// exercise the conflict-retry path (MergeBranch fails → RebaseInto conflicts →
// resolver runs → retry succeeds) without touching a real git binary.
type fakeGitRepo struct {
	root string

	// mergeFailCount is how many leading MergeBranch calls should fail
	// (simulating a non-fast-forward) before the call succeeds.
	mergeFailCount int
	mergeCalls     int

	conflictCalls       int
	continueRebaseCalls int

	lastCommitHash string
}

func (f *fakeGitRepo) Root() string { return f.root }

func (f *fakeGitRepo) Commit(worktreePath, message string) error { return nil }

func (f *fakeGitRepo) CleanRepoState() error { return nil }

func (f *fakeGitRepo) MergeBranch(branch, baseBranch string) error {
	f.mergeCalls++
	if f.mergeCalls <= f.mergeFailCount {
		return errors.New("simulated non-fast-forward merge failure")
	}
	return nil
}

func (f *fakeGitRepo) AbortRebase(worktreePath string) error { return nil }

func (f *fakeGitRepo) RebaseInto(worktreePath, baseBranch string) error {
	return errors.New("simulated rebase conflict")
}

func (f *fakeGitRepo) GetConflictedFiles(worktreePath string) ([]string, error) {
	f.conflictCalls++
	// First call (surfacing the conflict to the resolver) reports one
	// conflicted file; the second call (verifying resolution) reports none.
	if f.conflictCalls == 1 {
		return []string{"conflicted.txt"}, nil
	}
	return nil, nil
}

func (f *fakeGitRepo) ContinueRebase(worktreePath string) error {
	f.continueRebaseCalls++
	return nil
}

func (f *fakeGitRepo) HasChanges() (bool, error) { return false, nil }

func (f *fakeGitRepo) GetLastCommitHash(dir string) (string, error) {
	return f.lastCommitHash, nil
}

// TestFinalizeMergeWithFakeConflictThenSuccess demonstrates the gitRepo fake
// seam: it drives the full conflict-retry path — MergeBranch fails once,
// RebaseInto reports a conflict, the resolver runs and resolves it, then the
// retried MergeBranch succeeds — using only a fake, no real git process.
// This is the behavior TestCleanupOnConflictResolverFailure and
// TestConcurrentFinalizesSerialize exercise via a real repo; this test
// verifies the same conflict-path wiring in isolation.
func TestFinalizeMergeWithFakeConflictThenSuccess(t *testing.T) {
	fake := &fakeGitRepo{
		root:           "/fake/repo",
		mergeFailCount: 1,
		lastCommitHash: "abc123fake",
	}

	resolveCalls := 0
	resolver := func(ctx context.Context, tk *task.Task, conflictFiles []string) error {
		resolveCalls++
		if len(conflictFiles) != 1 || conflictFiles[0] != "conflicted.txt" {
			t.Errorf("resolver got unexpected conflict files: %v", conflictFiles)
		}
		return nil
	}

	var recordedHash string
	recordCommit := func(taskID int64, hash string) error {
		recordedHash = hash
		return nil
	}

	coord := newCoordinator(
		fake, &Lock{},
		Config{MaxAttempts: 2, BlockedPollInterval: 10 * time.Millisecond},
		resolver, nil, recordCommit,
	)

	tk := &task.Task{ID: 99, Title: "fake test", Branch: "feature-x", Worktree: true, WorktreePath: "/fake/worktree"}
	if err := coord.Finalize(context.Background(), tk, "main", ActionMerge, nil); err != nil {
		t.Fatalf("Finalize returned error: %v", err)
	}

	if resolveCalls != 1 {
		t.Errorf("expected resolver to be called once, got %d", resolveCalls)
	}
	if fake.mergeCalls != 2 {
		t.Errorf("expected 2 MergeBranch attempts (fail then succeed), got %d", fake.mergeCalls)
	}
	if fake.continueRebaseCalls != 1 {
		t.Errorf("expected ContinueRebase called once, got %d", fake.continueRebaseCalls)
	}
	if recordedHash != "abc123fake" {
		t.Errorf("expected recordCommit to fire with the fake hash, got %q", recordedHash)
	}
}
