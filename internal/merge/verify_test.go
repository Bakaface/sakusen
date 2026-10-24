package merge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/task"
)

// logCapture collects Finalize log lines.
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (l *logCapture) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logCapture) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func verifyCoordinator(dir string, cfg Config) *Coordinator {
	cfg.MaxAttempts = 2
	cfg.BlockedPollInterval = 10 * time.Millisecond
	return NewCoordinator(dir, &Lock{}, cfg, nil, nil, nil, nil, nil)
}

func mainHead(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(gitOutput(t, dir, "rev-parse", "main"))
}

func TestVerifyCommandPassMerges(t *testing.T) {
	dir := initRepoWithBranch(t, "fv", "feature.txt", "feature\n")
	wt := makeWorktree(t, dir, "fv")
	branchTip := strings.TrimSpace(gitOutput(t, dir, "rev-parse", "fv"))

	// Runs in the worktree root, where the task's file exists.
	coord := verifyCoordinator(dir, Config{VerifyCommand: "test -f feature.txt", VerifyTimeout: time.Minute})
	tk := &task.Task{ID: 1, Title: "verify pass", Branch: "fv", Worktree: true, WorktreePath: wt}
	var logs logCapture
	if err := coord.Finalize(context.Background(), tk, "main", ActionMerge, logs.logf); err != nil {
		t.Fatalf("Finalize: %v\nlog:\n%s", err, logs.String())
	}
	if got := mainHead(t, dir); got != branchTip {
		t.Errorf("main = %s, want fast-forwarded to %s", got, branchTip)
	}
	if !strings.Contains(logs.String(), "Verify command passed") {
		t.Errorf("log missing pass line:\n%s", logs.String())
	}
}

func TestVerifyCommandFailureBlocksMerge(t *testing.T) {
	dir := initRepoWithBranch(t, "fx", "feature.txt", "feature\n")
	wt := makeWorktree(t, dir, "fx")
	before := mainHead(t, dir)

	coord := verifyCoordinator(dir, Config{VerifyCommand: "echo first-line; echo tests broke >&2; exit 3"})
	tk := &task.Task{ID: 2, Title: "verify fail", Branch: "fx", Worktree: true, WorktreePath: wt}
	var logs logCapture
	err := coord.Finalize(context.Background(), tk, "main", ActionMerge, logs.logf)
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("Finalize err = %v, want ErrVerifyFailed", err)
	}
	for _, want := range []string{"exited with code 3", "tests broke"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	for _, want := range []string{"first-line", "tests broke", "exited with code 3"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log missing %q:\n%s", want, logs.String())
		}
	}
	if got := mainHead(t, dir); got != before {
		t.Errorf("main moved to %s after failed verify, want %s", got, before)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree removed after failed verify: %v", err)
	}
	if strings.TrimSpace(gitOutput(t, dir, "branch", "--list", "fx")) == "" {
		t.Error("task branch deleted after failed verify")
	}
}

func TestVerifyCommandTimeoutBlocksMerge(t *testing.T) {
	dir := initRepoWithBranch(t, "ft", "feature.txt", "feature\n")
	wt := makeWorktree(t, dir, "ft")
	before := mainHead(t, dir)

	// The backgrounded sleep holds the output pipe open, so the timeout only
	// returns promptly if the whole process group is killed.
	coord := verifyCoordinator(dir, Config{VerifyCommand: "sleep 30 & sleep 30", VerifyTimeout: 200 * time.Millisecond})
	tk := &task.Task{ID: 3, Title: "verify timeout", Branch: "ft", Worktree: true, WorktreePath: wt}
	start := time.Now()
	err := coord.Finalize(context.Background(), tk, "main", ActionMerge, nil)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Finalize took %s, want the timeout to kill the command promptly", elapsed)
	}
	if !errors.Is(err, ErrVerifyFailed) || !strings.Contains(err.Error(), "timed out after 200ms") {
		t.Fatalf("Finalize err = %v, want ErrVerifyFailed timeout", err)
	}
	if got := mainHead(t, dir); got != before {
		t.Errorf("main moved to %s after verify timeout, want %s", got, before)
	}
}

// setupDivergedBranches returns a repo whose main already contains branch
// fa's a.txt, plus a worktree for fb, which forked before fa landed.
func setupDivergedBranches(t *testing.T) (dir, wtB string) {
	t.Helper()
	dir = initRepoWithBranch(t, "fa", "a.txt", "alpha\n")
	runIn(t, dir, "git", "checkout", "-b", "fb")
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("beta\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runIn(t, dir, "git", "add", "-A")
	runIn(t, dir, "git", "commit", "-m", "beta commit")
	runIn(t, dir, "git", "checkout", "main")
	runIn(t, dir, "git", "merge", "--ff-only", "fa")
	return dir, makeWorktree(t, dir, "fb")
}

func TestVerifyCommandRerunsAfterRebase(t *testing.T) {
	dir, wtB := setupDivergedBranches(t)
	countFile := filepath.Join(t.TempDir(), "runs")

	coord := verifyCoordinator(dir, Config{VerifyCommand: fmt.Sprintf("echo run >> %q", countFile)})
	tk := &task.Task{ID: 4, Title: "beta", Branch: "fb", Worktree: true, WorktreePath: wtB}
	if err := coord.Finalize(context.Background(), tk, "main", ActionMerge, nil); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	data, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatal(err)
	}
	if runs := strings.Count(string(data), "run"); runs != 2 {
		t.Errorf("verify ran %d times, want 2 (before merge and after rebase)", runs)
	}
	if got, want := mainHead(t, dir), strings.TrimSpace(gitOutput(t, wtB, "rev-parse", "HEAD")); got != want {
		t.Errorf("main = %s, want rebased tip %s", got, want)
	}
}

func TestVerifyCommandFailureAfterRebaseBlocksMerge(t *testing.T) {
	dir, wtB := setupDivergedBranches(t)
	before := mainHead(t, dir)

	// Passes on fb's own tip; fails once the rebase brings in a.txt.
	coord := verifyCoordinator(dir, Config{VerifyCommand: "test ! -f a.txt"})
	tk := &task.Task{ID: 5, Title: "beta", Branch: "fb", Worktree: true, WorktreePath: wtB}
	err := coord.Finalize(context.Background(), tk, "main", ActionMerge, nil)
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("Finalize err = %v, want ErrVerifyFailed", err)
	}
	if got := mainHead(t, dir); got != before {
		t.Errorf("main moved to %s after failed re-verify, want %s", got, before)
	}
}

func TestOutputTail(t *testing.T) {
	if got := outputTail("  short\n", 100); got != "short" {
		t.Errorf("outputTail(short) = %q", got)
	}
	got := outputTail("aaaa\nbbbb\ncccc", 7)
	if got != "... (truncated)\ncccc" {
		t.Errorf("outputTail(long) = %q", got)
	}
}
