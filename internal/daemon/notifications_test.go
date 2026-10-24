package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/agent"
	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/notify"
	"github.com/Bakaface/sakusen/internal/task"
	"github.com/Bakaface/sakusen/internal/workflow"
)

type recordingBackend struct {
	mu     sync.Mutex
	titles []string
}

func (r *recordingBackend) Notify(title, _ string, _ notify.Urgency) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.titles = append(r.titles, title)
	return nil
}

func (r *recordingBackend) sent() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.titles)
}

func bptr(b bool) *bool { return &b }

var allOn = config.NotificationsConfig{Enabled: true, OnComplete: true, OnFailed: true, OnWaitingInput: true}

// newNotifyTestServer builds a Server with a recording notifier and a primed
// in-memory project context for cfg.
func newNotifyTestServer(t *testing.T, base config.NotificationsConfig, cfg *config.Config) (*Server, *recordingBackend, int64) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	s := NewServer(&config.Config{}, database)
	rec := &recordingBackend{}
	s.notifier = notify.NewWithBackend(&base, rec)

	repoRoot := t.TempDir()
	proj, err := database.GetOrCreateProject(repoRoot)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	s.projectsMu.Lock()
	s.projects[proj.ID] = &projectContext{
		cfg:                     cfg,
		engine:                  workflow.NewEngine(cfg, s.database, s.notifier, repoRoot),
		repoRoot:                repoRoot,
		tracksFingerprint:       tracksFingerprint(repoRoot),
		globalConfigFingerprint: globalConfigFingerprint(),
	}
	s.projectsMu.Unlock()
	return s, rec, proj.ID
}

// createNotifyTask creates a running task on workflowName with step "check"
// completed with stepCtx, linked to periodicName when non-empty.
func createNotifyTask(t *testing.T, s *Server, projID int64, workflowName, periodicName, stepCtx string) *task.Task {
	t.Helper()
	tk, err := s.database.CreateTask(projID, "Task", "desc", "slug", workflowName, "", task.StatusRunning, nil)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	if err := s.database.CreateTaskStep(tk.ID, "check"); err != nil {
		t.Fatal(err)
	}
	if err := s.database.CompleteTaskStep(tk.ID, "check", &stepCtx, 0); err != nil {
		t.Fatal(err)
	}
	if periodicName != "" {
		d, err := s.database.UpsertPeriodicDef(projID, periodicName, "@every 1h", "", workflowName, "", "", false, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.database.SetTaskPeriodicID(tk.ID, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	tk, err = s.database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	tk.Worktree = false
	tk.WorktreePath = ""
	return tk
}

func notifyCfg(wfNotif, periodicNotif *config.NotificationOverrides) *config.Config {
	return &config.Config{
		OnComplete: "none",
		Workflows: []config.WorkflowConfig{{
			Name:          "wf",
			Notifications: wfNotif,
			Steps:         []config.StepConfig{{Name: "check", Prompt: "check"}},
		}},
		Periodic: []config.PeriodicEntry{{
			Name:          "nightly",
			Cadence:       "@every 1h",
			Workflow:      "wf",
			Notifications: periodicNotif,
		}},
	}
}

func assertSent(t *testing.T, rec *recordingBackend, want ...string) {
	t.Helper()
	got := rec.sent()
	if !slices.Equal(got, want) {
		t.Fatalf("sent %q, want %q", got, want)
	}
}

func TestNotify_CompletionSuppressedSkipsAllTasksCompleted(t *testing.T) {
	s, rec, projID := newNotifyTestServer(t, allOn, notifyCfg(nil, &config.NotificationOverrides{OnComplete: bptr(false)}))
	tk := createNotifyTask(t, s, projID, "wf", "nightly", "")

	s.finalizeCompletedTask(tk, "agent-1", "Task")

	if got, _ := s.database.GetTask(tk.ID); got.Status != task.StatusCompleted {
		t.Fatalf("expected completed, got %s", got.Status)
	}
	assertSent(t, rec)
}

func TestNotify_NoOverridesUnchanged(t *testing.T) {
	s, rec, projID := newNotifyTestServer(t, allOn, notifyCfg(nil, nil))
	tk := createNotifyTask(t, s, projID, "wf", "nightly", "")

	s.finalizeCompletedTask(tk, "agent-1", "Task")

	assertSent(t, rec, "Agent Completed", "All Tasks Completed")
}

func TestNotify_FailureOverride(t *testing.T) {
	cases := []struct {
		name     string
		base     config.NotificationsConfig
		periodic *config.NotificationOverrides
		want     []string
	}{
		{"periodic on_failed beats workflow and global", config.NotificationsConfig{Enabled: true}, &config.NotificationOverrides{OnComplete: bptr(false), OnFailed: bptr(true)}, []string{"Agent Failed"}},
		{"global enabled false is a master switch", config.NotificationsConfig{Enabled: false}, &config.NotificationOverrides{OnComplete: bptr(false), OnFailed: bptr(true)}, nil},
		{"suppressed failure skips all tasks completed", allOn, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := notifyCfg(&config.NotificationOverrides{OnFailed: bptr(false)}, tc.periodic)
			s, rec, projID := newNotifyTestServer(t, tc.base, cfg)
			tk := createNotifyTask(t, s, projID, "wf", "nightly", "")
			if err := s.database.UpdateTaskStatus(tk.ID, task.StatusFailed); err != nil {
				t.Fatal(err)
			}

			a := agent.New(tk, 16)
			a.Error = "boom"
			s.onAgentStateChange(a, agent.StateRunning, agent.StateFailed)

			assertSent(t, rec, tc.want...)
		})
	}
}

func TestNotify_WaitingForInputOverride(t *testing.T) {
	for _, on := range []bool{true, false} {
		t.Run(fmt.Sprint(on), func(t *testing.T) {
			s, rec, projID := newNotifyTestServer(t, allOn, notifyCfg(&config.NotificationOverrides{OnWaitingInput: bptr(on)}, nil))
			tk := createNotifyTask(t, s, projID, "wf", "", "")
			if err := s.database.UpdateTaskStatus(tk.ID, task.StatusAwaitingApproval); err != nil {
				t.Fatal(err)
			}
			s.markEnginePaused(tk.ID)

			s.onAgentStateChange(agent.New(tk, 16), agent.StateRunning, agent.StateCompleted)

			if on {
				assertSent(t, rec, "Agent Needs Input")
			} else {
				assertSent(t, rec)
			}
		})
	}
}

func TestNotify_NotifyIfFullFinalization(t *testing.T) {
	contains := &config.NotifyCondition{StepContextContains: "check", Marker: "NEW"}
	nonempty := &config.NotifyCondition{StepContextNonempty: "check"}
	cases := []struct {
		name    string
		cond    *config.NotifyCondition
		stepCtx string
		want    []string
	}{
		{"marker present", contains, "found NEW stuff", []string{"Agent Completed", "All Tasks Completed"}},
		{"marker absent", contains, "nothing new", nil},
		{"empty context", contains, "", nil},
		{"nonempty", nonempty, "result", []string{"Agent Completed", "All Tasks Completed"}},
		{"whitespace counts as empty", nonempty, " \n\t ", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, rec, projID := newNotifyTestServer(t, allOn, notifyCfg(&config.NotificationOverrides{NotifyIf: tc.cond}, nil))
			tk := createNotifyTask(t, s, projID, "wf", "", tc.stepCtx)

			s.finalizeCompletedTask(tk, "agent-1", "Task")

			assertSent(t, rec, tc.want...)
		})
	}
}

func TestNotify_NotifyIfFastTrack(t *testing.T) {
	for _, tc := range []struct {
		stepCtx string
		want    []string
	}{
		{"found NEW stuff", []string{"Agent Completed", "All Tasks Completed"}},
		{"quiet run", nil},
	} {
		t.Run(tc.stepCtx, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())

			repoDir := initRecoveryTestRepo(t)
			yml := `on_complete: none
workflows:
  - name: wf
    notifications:
      notify_if:
        step_context_contains: check
        marker: NEW
    steps:
      - name: check
        prompt: check
`
			if err := os.WriteFile(filepath.Join(repoDir, ".sakusen.yml"), []byte(yml), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repoDir, ".gitignore"), []byte(".sakusen/\n"), 0644); err != nil {
				t.Fatal(err)
			}
			runGit(t, repoDir, "add", "--", ".sakusen.yml", ".gitignore")
			runGit(t, repoDir, "commit", "-q", "-m", "add sakusen config")
			// The no-changes check diffs HEAD~1, so the fork point must not
			// itself introduce files.
			runGit(t, repoDir, "commit", "-q", "--allow-empty", "-m", "fork point")

			const branch = "sakusen-fast-track"
			worktreePath := filepath.Join(t.TempDir(), "wt")
			runGit(t, repoDir, "worktree", "add", "-q", "-b", branch, worktreePath, "main")
			t.Cleanup(func() {
				cmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
				cmd.Dir = repoDir
				cmd.Run()
			})

			database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			s := NewServer(&config.Config{}, database)
			rec := &recordingBackend{}
			s.notifier = notify.NewWithBackend(&allOn, rec)

			proj, err := database.GetOrCreateProject(repoDir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.getProjectContext(proj.ID); err != nil {
				t.Fatalf("failed to load project context: %v", err)
			}
			tk, err := database.CreateTaskWithPriority(
				proj.ID, "Task", "desc", "slug", "wf", "", branch, "", "",
				task.StatusRunning, task.PriorityMedium, true, nil, nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.UpdateTaskWorktreePath(tk.ID, worktreePath); err != nil {
				t.Fatal(err)
			}
			tk.WorktreePath = worktreePath
			if err := database.CreateTaskStep(tk.ID, "check"); err != nil {
				t.Fatal(err)
			}
			if err := database.CompleteTaskStep(tk.ID, "check", &tc.stepCtx, 0); err != nil {
				t.Fatal(err)
			}

			s.finalizeCompletedTask(tk, "agent-1", "Task")

			if got, _ := database.GetTask(tk.ID); got.Status != task.StatusCompleted {
				t.Fatalf("expected fast-tracked completion, got %s", got.Status)
			}
			if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
				t.Fatalf("expected fast-track to remove the worktree (stat err: %v)", err)
			}
			assertSent(t, rec, tc.want...)
		})
	}
}
