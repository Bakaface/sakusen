package merge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/task"
)

const parentTaskID = 100

// parentFixture is a repo whose "parent" branch is checked out in a task
// worktree (the parent's), with the main checkout left on main.
type parentFixture struct {
	root     string
	parentWT string

	mu     sync.Mutex
	status task.Status
}

func newParentFixture(t *testing.T) *parentFixture {
	t.Helper()
	root := initRepoWithBranch(t, "parent", "parent.txt", "parent\n")
	return &parentFixture{root: root, parentWT: makeWorktree(t, root, "parent"), status: task.StatusAwaitingChildren}
}

func (f *parentFixture) setStatus(s task.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = s
}

func (f *parentFixture) lister(_ *task.Task, branch string) ([]TargetWorktree, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if branch != "parent" {
		return nil, nil
	}
	return []TargetWorktree{{TaskID: parentTaskID, Path: f.parentWT, Status: f.status}}, nil
}

// child creates a branch off the parent branch's current tip with one commit
// adding file, checks it out in a worktree and returns the child task.
func (f *parentFixture) child(t *testing.T, id int64, branch, file string) *task.Task {
	t.Helper()
	runIn(t, f.root, "git", "branch", branch, "parent")
	wt := makeWorktree(t, f.root, branch)
	if err := os.WriteFile(filepath.Join(wt, file), []byte(branch+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runIn(t, wt, "git", "add", "-A")
	runIn(t, wt, "git", "commit", "-m", "child "+branch)
	return &task.Task{ID: id, Title: "child " + branch, Branch: branch, Worktree: true, WorktreePath: wt}
}

func (f *parentFixture) coordinator(cfg Config, setDetail StatusDetailSetter) *Coordinator {
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 3
	}
	cfg.BlockedPollInterval = 10 * time.Millisecond
	c := NewCoordinator(f.root, &Lock{}, cfg, nil, nil, nil, setDetail, nil)
	c.SetTargetWorktrees(f.lister)
	return c
}

// detailRecorder captures every status detail the coordinator writes.
type detailRecorder struct {
	mu      sync.Mutex
	details []string
}

func (d *detailRecorder) set(_ int64, _ task.Status, detail string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.details = append(d.details, detail)
	return nil
}

func (d *detailRecorder) joined() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.Join(d.details, "\n")
}

func revParse(t *testing.T, dir, rev string) string {
	t.Helper()
	return strings.TrimSpace(gitOutput(t, dir, "rev-parse", rev))
}

func TestMergeIntoTaskWorktreeFastForwards(t *testing.T) {
	f := newParentFixture(t)
	mainBefore := revParse(t, f.root, "main")
	child := f.child(t, 1, "child-a", "a.txt")
	childTip := revParse(t, f.root, "child-a")

	var commits []string
	c := NewCoordinator(f.root, &Lock{}, Config{MaxAttempts: 1, BlockedPollInterval: 10 * time.Millisecond}, nil, nil,
		func(_ int64, hash string) error { commits = append(commits, hash); return nil }, nil, nil)
	c.SetTargetWorktrees(f.lister)

	var logs logCapture
	if err := c.Finalize(context.Background(), child, "parent", ActionMerge, logs.logf); err != nil {
		t.Fatalf("Finalize: %v\nlog:\n%s", err, logs.String())
	}

	if got := revParse(t, f.parentWT, "HEAD"); got != childTip {
		t.Errorf("parent worktree HEAD = %s, want fast-forwarded to %s", got, childTip)
	}
	if got := strings.TrimSpace(gitOutput(t, f.parentWT, "rev-parse", "--abbrev-ref", "HEAD")); got != "parent" {
		t.Errorf("parent worktree on %q, want parent", got)
	}
	if _, err := os.Stat(filepath.Join(f.parentWT, "a.txt")); err != nil {
		t.Errorf("child's file missing from the parent worktree: %v", err)
	}
	if got := revParse(t, f.root, "main"); got != mainBefore {
		t.Errorf("main moved to %s; a merge into the parent branch must not touch main", got)
	}
	if len(commits) != 1 || commits[0] != childTip {
		t.Errorf("recorded commits = %v, want [%s] (the parent worktree's new HEAD)", commits, childTip)
	}
}

func TestMergeIntoDirtyTaskWorktreeBlocks(t *testing.T) {
	f := newParentFixture(t)
	parentBefore := revParse(t, f.root, "parent")
	child := f.child(t, 1, "child-a", "a.txt")
	if err := os.WriteFile(filepath.Join(f.parentWT, "wip.txt"), []byte("wip\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var details detailRecorder
	c := f.coordinator(Config{CleanTargetTimeout: 50 * time.Millisecond}, details.set)
	err := c.Finalize(context.Background(), child, "parent", ActionMerge, nil)
	if !errors.Is(err, ErrTargetDirtyTimeout) {
		t.Fatalf("Finalize error = %v, want ErrTargetDirtyTimeout", err)
	}
	if got := details.joined(); !strings.Contains(got, "target worktree of task #100 has uncommitted changes: ?? wip.txt") {
		t.Errorf("merge-blocked detail = %q, want the parent worktree's blocking path", got)
	}
	if got := revParse(t, f.root, "parent"); got != parentBefore {
		t.Errorf("parent branch moved to %s while its worktree was dirty", got)
	}
	if b, err := os.ReadFile(filepath.Join(f.parentWT, "wip.txt")); err != nil || string(b) != "wip\n" {
		t.Errorf("parent's uncommitted file was touched: %q, %v", b, err)
	}
}

func TestMergeIntoBusyTaskWorktreeWaits(t *testing.T) {
	f := newParentFixture(t)
	f.setStatus(task.StatusRunning)
	parentBefore := revParse(t, f.root, "parent")
	child := f.child(t, 1, "child-a", "a.txt")

	t.Run("blocked while the parent runs", func(t *testing.T) {
		var details detailRecorder
		c := f.coordinator(Config{CleanTargetTimeout: 50 * time.Millisecond}, details.set)
		err := c.Finalize(context.Background(), child, "parent", ActionMerge, nil)
		if !errors.Is(err, ErrTargetDirtyTimeout) {
			t.Fatalf("Finalize error = %v, want ErrTargetDirtyTimeout", err)
		}
		if got := details.joined(); !strings.Contains(got, "parent is checked out in the worktree of task #100, which is running") {
			t.Errorf("merge-blocked detail = %q, want the busy parent as blocker", got)
		}
		if got := revParse(t, f.root, "parent"); got != parentBefore {
			t.Errorf("parent branch moved to %s while the parent was running", got)
		}
	})

	t.Run("lands once the parent suspends", func(t *testing.T) {
		var details detailRecorder
		c := f.coordinator(Config{}, details.set)
		go func() {
			time.Sleep(50 * time.Millisecond)
			f.setStatus(task.StatusAwaitingChildren)
		}()
		if err := c.Finalize(context.Background(), child, "parent", ActionMerge, nil); err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		if got := details.joined(); !strings.Contains(got, "which is running") {
			t.Errorf("merge-blocked details = %q, want a wait on the running parent first", got)
		}
		if got, want := revParse(t, f.parentWT, "HEAD"), revParse(t, f.root, "child-a"); got != want {
			t.Errorf("parent worktree HEAD = %s, want %s", got, want)
		}
	})
}

func TestMergeChildrenIntoTaskWorktreeInSequence(t *testing.T) {
	f := newParentFixture(t)
	a := f.child(t, 1, "child-a", "a.txt")
	b := f.child(t, 2, "child-b", "b.txt")

	runs := filepath.Join(t.TempDir(), "verify-runs")
	c := f.coordinator(Config{VerifyCommand: "pwd >> " + runs}, nil)

	if err := c.Finalize(context.Background(), a, "parent", ActionMerge, nil); err != nil {
		t.Fatalf("Finalize child-a: %v", err)
	}
	var logs logCapture
	if err := c.Finalize(context.Background(), b, "parent", ActionMerge, logs.logf); err != nil {
		t.Fatalf("Finalize child-b: %v\nlog:\n%s", err, logs.String())
	}
	if !strings.Contains(logs.String(), "Rebasing branch onto parent") {
		t.Errorf("child-b did not rebase onto the moved parent tip; log:\n%s", logs.String())
	}

	for _, file := range []string{"parent.txt", "a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(f.parentWT, file)); err != nil {
			t.Errorf("%s missing from the parent worktree: %v", file, err)
		}
	}
	if got, want := revParse(t, f.parentWT, "HEAD"), revParse(t, f.root, "child-b"); got != want {
		t.Errorf("parent worktree HEAD = %s, want child-b's rebased tip %s", got, want)
	}
	if got := strings.TrimSpace(gitOutput(t, f.root, "rev-list", "--count", "main..parent")); got != "3" {
		t.Errorf("parent is %s commits ahead of main, want 3 (own + both children, linear)", got)
	}
	// child-a verifies once; child-b before the merge and again after its
	// rebase moved the tip.
	out, err := os.ReadFile(runs)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(out), "\n"); got != 3 {
		t.Errorf("verify ran %d times, want 3:\n%s", got, out)
	}
}

func TestMergeIntoTaskWorktreeNeedsLister(t *testing.T) {
	f := newParentFixture(t)
	child := f.child(t, 1, "child-a", "a.txt")

	var details detailRecorder
	c := NewCoordinator(f.root, &Lock{}, Config{MaxAttempts: 1, BlockedPollInterval: 10 * time.Millisecond, CleanTargetTimeout: 50 * time.Millisecond}, nil, nil, nil, details.set, nil)
	err := c.Finalize(context.Background(), child, "parent", ActionMerge, nil)
	if !errors.Is(err, ErrTargetDirtyTimeout) {
		t.Fatalf("Finalize error = %v, want ErrTargetDirtyTimeout", err)
	}
	if got := details.joined(); !strings.Contains(got, "main checkout is on branch main instead of parent") {
		t.Errorf("merge-blocked detail = %q, want the main-checkout blocker", got)
	}
}

func countVerifyRuns(t *testing.T, path string) int {
	t.Helper()
	out, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Fields(string(out)))
}

func TestVerifyParentMerges(t *testing.T) {
	cases := []struct {
		name     string
		skip     bool
		wantRuns int
		wantLog  bool
	}{
		{"verified by default", false, 1, false},
		{"skipped when disabled", true, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newParentFixture(t)
			child := f.child(t, 1, "child-a", "a.txt")
			runs := filepath.Join(t.TempDir(), "verify-runs")
			c := f.coordinator(Config{VerifyCommand: "echo run >> " + runs, SkipVerifyParentMerges: tc.skip}, nil)

			var logs logCapture
			if err := c.Finalize(context.Background(), child, "parent", ActionMerge, logs.logf); err != nil {
				t.Fatalf("Finalize: %v\nlog:\n%s", err, logs.String())
			}
			if got := countVerifyRuns(t, runs); got != tc.wantRuns {
				t.Errorf("verify ran %d times, want %d", got, tc.wantRuns)
			}
			if got := strings.Contains(logs.String(), "Skipping verify command"); got != tc.wantLog {
				t.Errorf("skip logged = %v, want %v; log:\n%s", got, tc.wantLog, logs.String())
			}
		})
	}
}

// The parent's own merge into the base branch is verified even when
// SkipVerifyParentMerges is set.
func TestVerifyParentMergesStillVerifiesBaseMerge(t *testing.T) {
	f := newParentFixture(t)
	runs := filepath.Join(t.TempDir(), "verify-runs")
	c := f.coordinator(Config{VerifyCommand: "echo run >> " + runs, SkipVerifyParentMerges: true}, nil)

	// The parent's branch merges into main, which no task worktree holds.
	parent := &task.Task{ID: parentTaskID, Title: "parent", Branch: "parent", Worktree: true, WorktreePath: f.parentWT}
	var logs logCapture
	if err := c.Finalize(context.Background(), parent, "main", ActionMerge, logs.logf); err != nil {
		t.Fatalf("Finalize: %v\nlog:\n%s", err, logs.String())
	}
	if got := countVerifyRuns(t, runs); got != 1 {
		t.Errorf("verify ran %d times, want 1", got)
	}
}
