package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
	"github.com/Bakaface/sakusen/internal/tmux"
	"github.com/Bakaface/sakusen/internal/workflow"
)

// restoreYMLResumable defines a tmux-mode default agent WITH a resume_command,
// so restores can auto-resume a recorded session.
const restoreYMLResumable = `default_agent: interactive
agents:
  interactive:
    mode: tmux
    command: 'echo fresh-session-cmd'
    resume_command: 'echo resume-session-cmd'
workflows:
  - name: default
    steps:
      - name: implement
        prompt: do it
`

// restoreYMLNoResume is the same agent without a resume_command: restores must
// always start fresh even when a session id is recorded.
const restoreYMLNoResume = `default_agent: interactive
agents:
  interactive:
    mode: tmux
    command: 'echo fresh-session-cmd'
workflows:
  - name: default
    steps:
      - name: implement
        prompt: do it
`

// setupRestoreProject creates a project dir holding the given .sakusen.yml, a
// server, and a StatusTmux task running in the project root (no worktree, so
// restoreTmuxSession falls back to the repo root as the workdir). The tmux
// session a restore creates is killed on cleanup.
func setupRestoreProject(t *testing.T, sakusenYML string) (*Server, string, *task.Task) {
	t.Helper()
	if !tmux.IsAvailable() {
		t.Skip("tmux is not installed")
	}

	// Isolate config loading from the developer's real global config
	// (~/.sakusen.yml and ~/.config/sakusen/config.yaml): agent resolution in
	// these tests must come from the project .sakusen.yml alone.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Unique basename: it becomes the project name and thus the tmux session
	// prefix, so parallel test runs can't collide on session names.
	projectDir := filepath.Join(t.TempDir(), fmt.Sprintf("sakusen-restore-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".sakusen.yml"), []byte(sakusenYML), 0644); err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	s := NewServer(&config.Config{}, database)
	t.Cleanup(func() { s.cancel() })
	t.Cleanup(func() { s.manager.Shutdown(2 * time.Second) })

	proj, err := database.GetOrCreateProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := database.CreateTaskWithPriority(
		proj.ID, "Restore me", "restore input", "restore-me", "default", "", "", "", "",
		task.StatusTmux, task.PriorityMedium, false, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		tmux.NewSession(config.ProjectNameFromPath(projectDir), fmt.Sprintf("%d", tk.ID), projectDir).Kill()
	})
	return s, projectDir, tk
}

// readRestoreScript reads the wrapper script a restore wrote for the given
// label and checks it is syntactically valid bash.
func readRestoreScript(t *testing.T, projectDir, label string) string {
	t.Helper()
	scriptPath := filepath.Join(projectDir, ".sakusen", fmt.Sprintf("run-%s.sh", label))
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("failed to read wrapper script %s: %v", scriptPath, err)
	}
	if err := exec.Command("bash", "-n", scriptPath).Run(); err != nil {
		t.Errorf("bash -n rejected generated script: %v\nscript:\n%s", err, string(data))
	}
	return string(data)
}

// A recorded chats row + an agent with resume_command must auto-resume: the
// wrapper script runs the resume command with SAKUSEN_SESSION_ID exported.
func TestRestoreTmuxSession_ResumesRecordedSession(t *testing.T) {
	s, projectDir, tk := setupRestoreProject(t, restoreYMLResumable)

	if err := s.database.UpsertChat(tk.ID, "implement", "sess-123", ""); err != nil {
		t.Fatalf("failed to record chat: %v", err)
	}

	resumed, err := s.restoreTmuxSession(tk)
	if err != nil {
		t.Fatalf("restoreTmuxSession failed: %v", err)
	}
	if !resumed {
		t.Error("expected restore to resume the recorded session")
	}

	// Ad-hoc restores (task not paused on a workflow step) use the "continue" label.
	got := readRestoreScript(t, projectDir, "continue")
	if !strings.Contains(got, "echo resume-session-cmd") {
		t.Errorf("expected script to run the agent's resume_command\nscript:\n%s", got)
	}
	if !strings.Contains(got, `export SAKUSEN_SESSION_ID='sess-123'`) {
		t.Errorf("expected script to export the recorded session id\nscript:\n%s", got)
	}
	if !strings.Contains(got, "exec bash") {
		t.Errorf("expected script to drop to bash after the agent exits\nscript:\n%s", got)
	}
}

// Without a chats row there is nothing to resume: the restore starts a fresh
// session with the agent's normal command and no session id.
func TestRestoreTmuxSession_StartsFreshWithoutChatRow(t *testing.T) {
	s, projectDir, tk := setupRestoreProject(t, restoreYMLResumable)

	resumed, err := s.restoreTmuxSession(tk)
	if err != nil {
		t.Fatalf("restoreTmuxSession failed: %v", err)
	}
	if resumed {
		t.Error("expected fresh session when no chat is recorded")
	}

	got := readRestoreScript(t, projectDir, "continue")
	if !strings.Contains(got, "echo fresh-session-cmd") {
		t.Errorf("expected script to run the agent's normal command\nscript:\n%s", got)
	}
	if strings.Contains(got, "SAKUSEN_SESSION_ID") {
		t.Errorf("expected no SAKUSEN_SESSION_ID export for a fresh session\nscript:\n%s", got)
	}
}

// A recorded session id is only usable when the agent defines resume_command;
// otherwise the restore must fall back to a fresh session.
func TestRestoreTmuxSession_StartsFreshWithoutResumeCommand(t *testing.T) {
	s, projectDir, tk := setupRestoreProject(t, restoreYMLNoResume)

	if err := s.database.UpsertChat(tk.ID, "implement", "sess-456", ""); err != nil {
		t.Fatalf("failed to record chat: %v", err)
	}

	resumed, err := s.restoreTmuxSession(tk)
	if err != nil {
		t.Fatalf("restoreTmuxSession failed: %v", err)
	}
	if resumed {
		t.Error("expected fresh session when the agent has no resume_command")
	}

	got := readRestoreScript(t, projectDir, "continue")
	if !strings.Contains(got, "echo fresh-session-cmd") {
		t.Errorf("expected script to run the agent's normal command\nscript:\n%s", got)
	}
	if strings.Contains(got, "SAKUSEN_SESSION_ID") {
		t.Errorf("expected no SAKUSEN_SESSION_ID export without resume_command\nscript:\n%s", got)
	}
}

// A task paused on a workflow step whose agent is tmux-mode must restore THAT
// step's agent (not the interactive fallback), label the session after the
// step, and re-feed the step prompt still on disk.
func TestRestoreTmuxSession_UsesPausedStepAgent(t *testing.T) {
	yml := `default_agent: headless-main
agents:
  headless-main:
    command: 'true'
  step-agent:
    mode: tmux
    command: 'echo step-agent-cmd'
  claude-tmux:
    mode: tmux
    command: 'echo interactive-cmd'
workflows:
  - name: default
    steps:
      - name: build
        prompt: build it
      - name: review
        agent: step-agent
        prompt: review it
`
	s, projectDir, tk := setupRestoreProject(t, yml)

	// Pause the task on the "review" step (StepIndex is one past the paused step).
	if err := s.database.UpdateTaskStep(tk.ID, 2, ""); err != nil {
		t.Fatal(err)
	}
	tk.StepIndex = 2

	// The step prompt the engine left on disk before the daemon died.
	sakusenDir := filepath.Join(projectDir, ".sakusen")
	if err := os.MkdirAll(sakusenDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sakusenDir, "step-prompt-review.txt"), []byte("review instructions"), 0644); err != nil {
		t.Fatal(err)
	}

	resumed, err := s.restoreTmuxSession(tk)
	if err != nil {
		t.Fatalf("restoreTmuxSession failed: %v", err)
	}
	if resumed {
		t.Error("expected fresh session (no chat recorded)")
	}

	got := readRestoreScript(t, projectDir, "review")
	if !strings.Contains(got, "echo step-agent-cmd") {
		t.Errorf("expected the paused step's agent command\nscript:\n%s", got)
	}
	if strings.Contains(got, "echo interactive-cmd") {
		t.Errorf("interactive fallback agent must not be used for a step restore\nscript:\n%s", got)
	}
	if !strings.Contains(got, `export SAKUSEN_STEP='review'`) {
		t.Errorf("expected SAKUSEN_STEP export for the paused step\nscript:\n%s", got)
	}

	prompt, err := os.ReadFile(filepath.Join(sakusenDir, "session-prompt-review.txt"))
	if err != nil {
		t.Fatalf("failed to read session prompt: %v", err)
	}
	if string(prompt) != "review instructions" {
		t.Errorf("expected the on-disk step prompt to be re-fed, got %q", string(prompt))
	}
}

// A task paused on a HEADLESS step has no step session to restore: the restore
// falls back to the interactive agent under the "continue" label.
func TestRestoreTmuxSession_HeadlessPausedStepFallsBackToInteractive(t *testing.T) {
	yml := `default_agent: claude
agents:
  claude:
    command: 'true'
  claude-tmux:
    mode: tmux
    command: 'echo interactive-cmd'
workflows:
  - name: default
    steps:
      - name: build
        prompt: build it
`
	s, projectDir, tk := setupRestoreProject(t, yml)

	if err := s.database.UpdateTaskStep(tk.ID, 1, ""); err != nil {
		t.Fatal(err)
	}
	tk.StepIndex = 1

	if _, err := s.restoreTmuxSession(tk); err != nil {
		t.Fatalf("restoreTmuxSession failed: %v", err)
	}

	got := readRestoreScript(t, projectDir, "continue")
	if !strings.Contains(got, "echo interactive-cmd") {
		t.Errorf("expected the interactive fallback agent for a headless paused step\nscript:\n%s", got)
	}
}

// TestSpawnInteractiveSession_ExportsEnvContract verifies the wrapper script
// an interactive spawn writes carries sakusen's full env contract, folds in the
// agent record's `env:` map, and lets the contract win on key collisions
// (runner.MergeEnv semantics: an agent env entry cannot mask a SAKUSEN_*
// variable sakusen relies on).
func TestSpawnInteractiveSession_ExportsEnvContract(t *testing.T) {
	yml := `default_agent: interactive
agents:
  interactive:
    mode: tmux
    command: 'echo agent-cmd'
    env:
      MY_AGENT_VAR: xyz
      SAKUSEN_AGENT: masked
workflows:
  - name: default
    steps:
      - name: implement
        prompt: do it
`
	s, projectDir, tk := setupRestoreProject(t, yml)

	if _, err := s.restoreTmuxSession(tk); err != nil {
		t.Fatalf("restoreTmuxSession failed: %v", err)
	}

	got := readRestoreScript(t, projectDir, "continue")
	wantExports := []string{
		fmt.Sprintf("export SAKUSEN_DONE_DIR='%s'", filepath.Join(projectDir, ".sakusen", "step-done")),
		`export SAKUSEN_DONE_PREFIX='continue'`,
		`export SAKUSEN_AGENT='interactive'`,
		fmt.Sprintf("export SAKUSEN_PROJECT_PATH='%s'", projectDir),
		`export SAKUSEN_PURPOSE='step'`,
		`export MY_AGENT_VAR='xyz'`,
	}
	for _, want := range wantExports {
		if !strings.Contains(got, want) {
			t.Errorf("wrapper script missing %s\nscript:\n%s", want, got)
		}
	}
	if strings.Contains(got, "masked") {
		t.Errorf("agent env must not mask the SAKUSEN_AGENT contract value\nscript:\n%s", got)
	}
}

// TestRestoreTmuxSession_SkipsWhenSessionExists verifies a restore is a no-op
// (false, nil) when the task's tmux session already exists: no wrapper script
// is written, so the running session is not respawned over.
func TestRestoreTmuxSession_SkipsWhenSessionExists(t *testing.T) {
	s, projectDir, tk := setupRestoreProject(t, restoreYMLResumable)

	// Pre-create the session under the exact name a restore would use.
	// setupRestoreProject's cleanup kills it.
	session := tmux.NewSession(config.ProjectNameFromPath(projectDir), fmt.Sprintf("%d", tk.ID), projectDir)
	if err := session.Create(""); err != nil {
		t.Fatalf("failed to pre-create tmux session: %v", err)
	}

	resumed, err := s.restoreTmuxSession(tk)
	if err != nil {
		t.Fatalf("restoreTmuxSession failed: %v", err)
	}
	if resumed {
		t.Error("expected resumed=false when the session already exists")
	}

	scriptPath := filepath.Join(projectDir, ".sakusen", "run-continue.sh")
	if _, statErr := os.Stat(scriptPath); !os.IsNotExist(statErr) {
		t.Errorf("expected no wrapper script when the session already exists (stat: %v)", statErr)
	}
}

// TestRestoreTmuxSession_ErrorsWhenNoAgentResolvable verifies the error path
// when no tmux-capable agent can be resolved for a restore. The paused step's
// implicit "claude" slug has no record, so StepIsTmux reports false and the
// restore falls through to the interactive fallback — which also resolves
// nothing (default agent is headless-only, no "claude-tmux" record) and must
// propagate its resolution error instead of spawning.
func TestRestoreTmuxSession_ErrorsWhenNoAgentResolvable(t *testing.T) {
	yml := `agents:
  worker:
    command: 'true'
workflows:
  - name: default
    steps:
      - name: implement
        prompt: do it
`
	s, projectDir, tk := setupRestoreProject(t, yml)

	// Pause the task on the "implement" step (cursor invariant: StepIndex is
	// one past the paused step).
	if err := s.database.UpdateTaskStep(tk.ID, 1, ""); err != nil {
		t.Fatal(err)
	}
	tk.StepIndex = 1

	_, err := s.restoreTmuxSession(tk)
	if err == nil {
		t.Fatal("expected an agent-resolution error when no tmux-mode agent is configured")
	}
	if !strings.Contains(err.Error(), interactiveAgentSlug) {
		t.Errorf("error should carry the interactive-agent guidance naming %q, got: %v", interactiveAgentSlug, err)
	}

	for _, label := range []string{"continue", "implement"} {
		scriptPath := filepath.Join(projectDir, ".sakusen", fmt.Sprintf("run-%s.sh", label))
		if _, statErr := os.Stat(scriptPath); !os.IsNotExist(statErr) {
			t.Errorf("expected no wrapper script %s on a failed restore (stat: %v)", scriptPath, statErr)
		}
	}
}

// TestRestoreTmuxSession_PreservesSentinels pins the keepSentinels contract:
// a turn-end sentinel that landed just before the daemon died must SURVIVE the
// restore respawn, so the monitor's auto-advance still fires for the completed
// turn. (Ad-hoc spawns clear stale sentinels; restores must not.)
func TestRestoreTmuxSession_PreservesSentinels(t *testing.T) {
	s, projectDir, tk := setupRestoreProject(t, restoreYMLResumable)

	doneDir := workflow.StepDoneDir(projectDir)
	if err := os.MkdirAll(doneDir, 0755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(doneDir, workflow.SentinelPrefix("continue")+"-123.json")
	if err := os.WriteFile(sentinel, []byte(`{"session_id":"sess-999"}`), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := s.restoreTmuxSession(tk); err != nil {
		t.Fatalf("restoreTmuxSession failed: %v", err)
	}

	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("pre-restart sentinel must survive a restore (keepSentinels), got: %v", err)
	}
}
