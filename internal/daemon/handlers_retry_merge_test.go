package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
)

// TestHandleRetryMerge_RejectsNonMergeFailed: every status but merge-failed is
// refused before any state changes.
func TestHandleRetryMerge_RejectsNonMergeFailed(t *testing.T) {
	for _, status := range []task.Status{
		task.StatusPending, task.StatusRunning, task.StatusTmux, task.StatusAwaitingApproval,
		task.StatusFinalizing, task.StatusCompleted, task.StatusFailed,
	} {
		t.Run(string(status), func(t *testing.T) {
			s, projID := setupServerWithProject(t)
			tk, err := s.database.CreateTask(projID, "t", "desc", "slug", "wf", "main", status, nil)
			if err != nil {
				t.Fatal(err)
			}

			clientConn, serverConn := pipeForHandler(t)
			go s.handleRetryMerge(serverConn, RetryMergeRequest{TaskID: tk.ID})

			msg := readOneMessage(t, clientConn)
			if msg.Type != MsgError {
				t.Fatalf("expected MsgError for status %s, got %s: %s", status, msg.Type, string(msg.Payload))
			}
			var resp ErrorResponse
			if err := msg.DecodePayload(&resp); err != nil {
				t.Fatalf("decode error payload: %v", err)
			}
			if !strings.Contains(resp.Message, "not merge-failed") || !strings.Contains(resp.Message, string(status)) {
				t.Errorf("error should name the status, got %q", resp.Message)
			}

			refreshed, err := s.database.GetTask(tk.ID)
			if err != nil {
				t.Fatal(err)
			}
			if refreshed.Status != status {
				t.Errorf("task status changed to %s despite rejection", refreshed.Status)
			}
		})
	}
}

// TestHandleRetryMerge_RerunsOnlyFinalization drives a merge-failed task
// through two retries: the first fails verify again and must land back in
// merge-failed with the new error and the worktree kept; once verify passes,
// the second merges and completes. No agent runs and no step state changes.
func TestHandleRetryMerge_RerunsOnlyFinalization(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent stub is a /bin/sh script")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	scratch := t.TempDir()
	agentMarker := filepath.Join(scratch, "agent-ran")
	verifyMarker := filepath.Join(scratch, "verify-ok")
	agentStub := filepath.Join(scratch, "agent-stub")
	if err := os.WriteFile(agentStub, []byte(fmt.Sprintf("#!/bin/sh\ntouch %q\nexit 1\n", agentMarker)), 0755); err != nil {
		t.Fatal(err)
	}

	repoDir := initRecoveryTestRepo(t)
	sakusenYML := fmt.Sprintf(`on_complete: merge
git:
  base_branch: main
  verify_command: test -f %q
agents:
  claude:
    command: %q
workflows:
  - name: default
    steps:
      - name: implement
        prompt: implement
`, verifyMarker, agentStub)
	if err := os.WriteFile(filepath.Join(repoDir, ".sakusen.yml"), []byte(sakusenYML), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".gitignore"), []byte(".sakusen/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "--", ".sakusen.yml", ".gitignore")
	runGit(t, repoDir, "commit", "-q", "-m", "add sakusen config")

	const branch = "sakusen-retry-merge"
	worktreePath := filepath.Join(t.TempDir(), "wt")
	runGit(t, repoDir, "worktree", "add", "-q", "-b", branch, worktreePath, "main")
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
		cmd.Dir = repoDir
		cmd.Run() // best-effort
	})
	if err := os.WriteFile(filepath.Join(worktreePath, "feature.txt"), []byte("work\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, worktreePath, "add", "feature.txt")
	runGit(t, worktreePath, "commit", "-q", "-m", "task work")

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	s := NewServer(&config.Config{}, database)
	t.Cleanup(func() { s.cancel() })
	t.Cleanup(func() { s.manager.Shutdown(2 * time.Second) })

	proj, err := database.GetOrCreateProject(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := database.CreateTaskWithPriority(
		proj.ID, "Test task", "desc", "slug", "default", "", branch, "", "",
		task.StatusFinalizing, task.PriorityMedium, true, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateTaskWorktreePath(tk.ID, worktreePath); err != nil {
		t.Fatal(err)
	}
	stepCtx := "implemented the feature"
	if err := database.CreateTaskStep(tk.ID, "implement"); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteTaskStep(tk.ID, "implement", &stepCtx, 0); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateTaskStep(tk.ID, 1, ""); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateTaskLoopIteration(tk.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateTaskStatusError(tk.ID, task.StatusMergeFailed, "stale cache"); err != nil {
		t.Fatal(err)
	}
	stepsBefore, err := database.GetTaskStepRows(tk.ID)
	if err != nil {
		t.Fatal(err)
	}

	retry := func() *task.Task {
		t.Helper()
		clientConn, serverConn := pipeForHandler(t)
		go s.handleRetryMerge(serverConn, RetryMergeRequest{TaskID: tk.ID})
		msg := readOneMessage(t, clientConn)
		if msg.Type != MsgRetryMerge {
			t.Fatalf("expected MsgRetryMerge, got %s: %s", msg.Type, string(msg.Payload))
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			got, err := database.GetTask(tk.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status.IsTerminal() {
				return got
			}
			if time.Now().After(deadline) {
				t.Fatalf("finalization did not finish; status %s", got.Status)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	assertStepsUntouched := func() {
		t.Helper()
		got, err := database.GetTask(tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.StepIndex != 1 || got.LoopIteration != 2 {
			t.Errorf("step_index=%d loop_iteration=%d, want 1 and 2", got.StepIndex, got.LoopIteration)
		}
		stepsAfter, err := database.GetTaskStepRows(tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(stepsBefore, stepsAfter) {
			t.Errorf("step rows changed:\nbefore %+v\nafter  %+v", stepsBefore, stepsAfter)
		}
		if _, err := os.Stat(agentMarker); err == nil {
			t.Error("an agent ran during retry merge")
		}
	}

	failed := retry()
	if failed.Status != task.StatusMergeFailed {
		t.Fatalf("status %s, want merge-failed (error: %q)", failed.Status, failed.ErrorMessage)
	}
	if !strings.Contains(failed.ErrorMessage, "verify command failed") {
		t.Errorf("error should be the new verify failure, got %q", failed.ErrorMessage)
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Errorf("worktree removed after a failed retry: %v", err)
	}
	assertStepsUntouched()

	if err := os.WriteFile(verifyMarker, nil, 0644); err != nil {
		t.Fatal(err)
	}
	done := retry()
	if done.Status != task.StatusCompleted {
		t.Fatalf("status %s, want completed (error: %q)", done.Status, done.ErrorMessage)
	}
	if done.ErrorMessage != "" {
		t.Errorf("completed task still carries the old error %q", done.ErrorMessage)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "feature.txt")); err != nil {
		t.Errorf("task work was not merged into main: %v", err)
	}
	assertStepsUntouched()
}
