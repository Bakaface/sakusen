package merge

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/task"
)

// commitMessageFixture is a task worktree with one uncommitted file.
func commitMessageFixture(t *testing.T, branch string) (repo, wt string) {
	t.Helper()
	repo = initRepoWithBranch(t, branch, "feature.txt", "feature\n")
	wt = makeWorktree(t, repo, branch)
	if err := os.WriteFile(filepath.Join(wt, "widget.txt"), []byte("widget content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return repo, wt
}

func lastSubject(t *testing.T, dir, rev string) string {
	t.Helper()
	return strings.TrimSpace(gitOutput(t, dir, "log", "-1", "--format=%s", rev))
}

// finalizeCommit runs on_complete=commit and returns the worktree's new
// commit subject plus the task log.
func finalizeCommit(t *testing.T, cfg Config, tk *task.Task) (string, string) {
	t.Helper()
	repo, wt := commitMessageFixture(t, "cm")
	tk.Branch, tk.Worktree, tk.WorktreePath = "cm", true, wt
	var logs logCapture
	coord := NewCoordinator(repo, &Lock{}, cfg, nil, nil, nil, nil, nil)
	if err := coord.Finalize(context.Background(), tk, "main", ActionCommit, logs.logf); err != nil {
		t.Fatalf("Finalize: %v\nlog:\n%s", err, logs.String())
	}
	if status := strings.TrimSpace(gitOutput(t, wt, "status", "--porcelain")); status != "" {
		t.Fatalf("worktree not committed:\n%s", status)
	}
	return lastSubject(t, wt, "HEAD"), logs.String()
}

func TestCommitMessageUnsetUsesTitle(t *testing.T) {
	got, _ := finalizeCommit(t, Config{}, &task.Task{ID: 1, Title: "Add widget"})
	if got != "feat: add widget" {
		t.Errorf("subject = %q, want title-derived %q", got, "feat: add widget")
	}
}

func TestCommitMessageCommandOutputUsed(t *testing.T) {
	cfg := Config{CommitMessageCommand: `echo; echo "  feat(widget): add the widget  "; echo second line`}
	got, logs := finalizeCommit(t, cfg, &task.Task{ID: 2, Title: "Add widget"})
	if got != "feat(widget): add the widget" {
		t.Errorf("subject = %q, want the command's first non-empty line", got)
	}
	if !strings.Contains(logs, "Commit message: feat(widget): add the widget") {
		t.Errorf("log missing chosen message:\n%s", logs)
	}
}

func TestCommitMessageCommandFallbacks(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantLog string
	}{
		{"failure", Config{CommitMessageCommand: "echo 'feat: ignored'; echo boom >&2; exit 4"}, "exited with code 4"},
		{"timeout", Config{CommitMessageCommand: "sleep 30", CommitMessageTimeout: 200 * time.Millisecond}, "timed out after 200ms"},
		{"empty output", Config{CommitMessageCommand: `printf '\n   \n'`}, "printed nothing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			got, logs := finalizeCommit(t, tc.cfg, &task.Task{ID: 3, Title: "Add widget"})
			if got != "feat: add widget" {
				t.Errorf("subject = %q, want fallback %q", got, "feat: add widget")
			}
			if !strings.Contains(logs, tc.wantLog) || !strings.Contains(logs, "using title-derived message") {
				t.Errorf("log missing fallback reason %q:\n%s", tc.wantLog, logs)
			}
			if elapsed := time.Since(start); elapsed > 10*time.Second {
				t.Errorf("Finalize took %s, want the command bounded by its timeout", elapsed)
			}
		})
	}
	t.Run("stderr logged", func(t *testing.T) {
		_, logs := finalizeCommit(t, cases[0].cfg, &task.Task{ID: 3, Title: "Add widget"})
		if !strings.Contains(logs, "boom") {
			t.Errorf("log missing stderr tail:\n%s", logs)
		}
	})
}

func TestCommitMessageCommandGetsStagedDiff(t *testing.T) {
	out := filepath.Join(t.TempDir(), "stdin.txt")
	cfg := Config{CommitMessageCommand: `cat > "$OUT"; echo "feat: x"`}
	t.Setenv("OUT", out)
	if got, _ := finalizeCommit(t, cfg, &task.Task{ID: 4, Title: "Add widget"}); got != "feat: x" {
		t.Fatalf("subject = %q, want %q", got, "feat: x")
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"diff --git a/widget.txt b/widget.txt", "+widget content"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("stdin missing %q:\n%s", want, data)
		}
	}
}

func TestCommitMessageCommandEnv(t *testing.T) {
	out := filepath.Join(t.TempDir(), "env.txt")
	t.Setenv("OUT", out)
	cfg := Config{CommitMessageCommand: `printf '%s\n%s\n' "$SAKUSEN_WORKTREE" "$SAKUSEN_PURPOSE" > "$OUT"; echo "chore: $SAKUSEN_TASK_ID $SAKUSEN_TASK_TITLE"`}
	tk := &task.Task{ID: 42, Title: "Add widget"}
	if got, _ := finalizeCommit(t, cfg, tk); got != "chore: 42 Add widget" {
		t.Errorf("subject = %q, want task id and title from env", got)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := tk.WorktreePath + "\ncommit_message\n"; string(data) != want {
		t.Errorf("env = %q, want %q", data, want)
	}
}

// Nothing to commit means no commit, so the command is not run.
func TestCommitMessageCommandSkippedWhenClean(t *testing.T) {
	repo := initRepoWithBranch(t, "clean", "feature.txt", "feature\n")
	wt := makeWorktree(t, repo, "clean")
	marker := filepath.Join(t.TempDir(), "ran")
	t.Setenv("MARKER", marker)
	coord := NewCoordinator(repo, &Lock{}, Config{CommitMessageCommand: `touch "$MARKER"; echo "feat: x"`}, nil, nil, nil, nil, nil)
	tk := &task.Task{ID: 5, Title: "Add widget", Branch: "clean", Worktree: true, WorktreePath: wt}
	if err := coord.Finalize(context.Background(), tk, "main", ActionCommit, nil); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("command ran on a clean worktree (stat err = %v)", err)
	}
}

func TestCommitMessageCommandUsedByMerge(t *testing.T) {
	repo, wt := commitMessageFixture(t, "cmm")
	coord := verifyCoordinator(repo, Config{CommitMessageCommand: `echo "feat(widget): add widget"`})
	tk := &task.Task{ID: 6, Title: "Add widget", Branch: "cmm", Worktree: true, WorktreePath: wt}
	var logs logCapture
	if err := coord.Finalize(context.Background(), tk, "main", ActionMerge, logs.logf); err != nil {
		t.Fatalf("Finalize: %v\nlog:\n%s", err, logs.String())
	}
	if got := lastSubject(t, repo, "main"); got != "feat(widget): add widget" {
		t.Errorf("main subject = %q, want the command's message", got)
	}
}

func TestCapDiff(t *testing.T) {
	small := "diff --git a/x b/x\n+x\n"
	if got := capDiff(small); got != small {
		t.Errorf("capDiff(small) = %q, want unchanged", got)
	}

	line := strings.Repeat("a", 99) + "\n"
	big := strings.Repeat(line, maxCommitDiffBytes/len(line)+10)
	got := capDiff(big)
	head, note, ok := strings.Cut(got, "\n[diff truncated: ")
	if !ok {
		t.Fatalf("capDiff(big) has no truncation note: ...%q", got[len(got)-200:])
	}
	if len(head) > maxCommitDiffBytes || !strings.HasSuffix(head, "\n") {
		t.Errorf("truncated head is %d bytes (max %d), ends at line boundary = %v", len(head), maxCommitDiffBytes, strings.HasSuffix(head, "\n"))
	}
	if !strings.Contains(note, "of "+strconv.Itoa(len(big))+" bytes shown]") {
		t.Errorf("note %q missing the full size %d", note, len(big))
	}
}
