package daemon

import (
	"encoding/json"
	"fmt"
	"os"
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

// hibernateYML pins a tmux-mode agent WITH a resume_command (the precondition
// for hibernating anything) behind a human-gated step — the population that
// sits idle at a prompt for days. The agent command exits immediately so the
// wrapper script's trailing `exec bash` owns the pane, which keeps the pane
// content static (hence "idle") and responsive to send-keys.
const hibernateYML = `on_complete: none
default_agent: interactive
agents:
  interactive:
    mode: tmux
    command: 'true'
    resume_command: 'true'
workflows:
  - name: default
    steps:
      - name: implement
        prompt: do it
        human: true
`

// hibernateYMLExplicitAdvance is hibernateYML with the step advancing only on
// an explicit advance instead of behind an approval gate.
const hibernateYMLExplicitAdvance = `on_complete: none
default_agent: interactive
agents:
  interactive:
    mode: tmux
    command: 'true'
    resume_command: 'true'
workflows:
  - name: default
    steps:
      - name: implement
        prompt: do it
        advance: explicit
`

// hibernateYMLNoResume is the same agent with no resume_command: its sessions
// can never be hibernated, because restoring one would start a fresh
// conversation rather than bring the old one back.
const hibernateYMLNoResume = `on_complete: none
default_agent: interactive
agents:
  interactive:
    mode: tmux
    command: 'true'
workflows:
  - name: default
    steps:
      - name: implement
        prompt: do it
        human: true
`

// hibernationFixture is a server with one project whose config is the given
// YAML. Tasks are created in the project root (no worktree), matching
// setupRestoreProject.
type hibernationFixture struct {
	server      *Server
	database    *db.DB
	projectID   int64
	projectDir  string
	projectName string
}

func setupHibernationFixture(t *testing.T, sakusenYML string) *hibernationFixture {
	t.Helper()
	if !tmux.IsAvailable() {
		t.Skip("tmux is not installed")
	}

	// Isolate config loading from the developer's real global config: agent
	// resolution here must come from the project .sakusen.yml alone.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Unique basename: it becomes the project name and thus the tmux session
	// prefix, so parallel test runs can't collide on session names.
	projectDir := filepath.Join(t.TempDir(), fmt.Sprintf("sakusen-hib-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".sakusen.yml"), []byte(sakusenYML), 0644); err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
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
	if _, err := s.getProjectContext(proj.ID); err != nil {
		t.Fatalf("failed to pre-load project context: %v", err)
	}

	return &hibernationFixture{
		server:      s,
		database:    database,
		projectID:   proj.ID,
		projectDir:  projectDir,
		projectName: config.ProjectNameFromPath(projectDir),
	}
}

const hibernateSlug = "hibernate-me"

// newTask creates a StatusTmux task rooted at the project dir. stepIndex 1
// leaves it paused on the workflow's "implement" step (the cursor invariant:
// StepIndex points one past the paused step); 0 leaves it with no paused
// step, i.e. an ad-hoc continue / tmux_direct session.
func (f *hibernationFixture) newTask(t *testing.T, stepIndex int) *task.Task {
	t.Helper()
	tk, err := f.database.CreateTaskWithPriority(
		f.projectID, "Hibernate me", "input", hibernateSlug, "default", "", "", "", "",
		task.StatusTmux, task.PriorityMedium, false, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.database.UpdateTaskWorktreePath(tk.ID, f.projectDir); err != nil {
		t.Fatal(err)
	}
	if stepIndex != 0 {
		if err := f.database.UpdateTaskStep(tk.ID, stepIndex, ""); err != nil {
			t.Fatal(err)
		}
	}
	tk.StepIndex = stepIndex
	tk.WorktreePath = f.projectDir
	t.Cleanup(func() { f.session(tk.ID).Kill() })
	return tk
}

func (f *hibernationFixture) session(taskID int64) *tmux.Session {
	return tmux.NewSession(f.projectName, fmt.Sprintf("%d", taskID), hibernateSlug, f.projectDir)
}

// sessionExistsExactly reports whether a session with exactly this name is
// live, by scanning session names rather than going through tmux target
// resolution — the code under test.
func sessionExistsExactly(t *testing.T, name string) bool {
	t.Helper()
	sessions, err := tmux.ListSessions("")
	if err != nil {
		t.Fatalf("failed to list tmux sessions: %v", err)
	}
	for _, s := range sessions {
		if s.Name == name {
			return true
		}
	}
	return false
}

// spawnSession brings up a real tmux session for the task the way production
// does, so Exists/Attached/Kill all operate on the genuine article.
func (f *hibernationFixture) spawnSession(t *testing.T, tk *task.Task, label string) {
	t.Helper()
	pc, err := f.server.getProjectContext(f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	slug, agentCfg, err := interactiveAgent(pc.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.spawnInteractiveSession(pc, tk, interactiveSpawnOpts{
		label: label,
		slug:  slug,
		agent: agentCfg,
	}); err != nil {
		t.Fatalf("failed to spawn tmux session: %v", err)
	}
}

// markIdleAtPrompt puts a task in the in-memory state hibernation requires:
// pane idle since the given time, agent parked at its prompt.
func (f *hibernationFixture) markIdleAtPrompt(taskID int64, idleSince time.Time) {
	s := f.server
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tmuxActivity[taskID] = string(tmux.ActivityIdle)
	entry := s.tmuxAutoEntryFor(taskID)
	entry.atPrompt = true
	entry.idleSince = idleSince
}

// writeSentinel writes a turn-end sentinel for the given label, optionally
// carrying a session id, using the filename shape production matches.
func writeSentinelWithSession(t *testing.T, worktreePath, label, sessionID string) string {
	t.Helper()
	dir := workflow.StepDoneDir(worktreePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(workflow.StepSentinel{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.json", workflow.SentinelPrefix(label), time.Now().UnixNano()))
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func (f *hibernationFixture) resolve(t *testing.T, tk *task.Task) tmuxResumeTarget {
	t.Helper()
	pc, err := f.server.getProjectContext(f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	target, err := f.server.resolveTmuxResume(pc, tk)
	if err != nil {
		t.Fatalf("resolveTmuxResume: %v", err)
	}
	return target
}

// ---------------------------------------------------------------- resolver

// A task paused on a tmux step, with a chat row for THAT step and an agent
// that can resume, is resumable off a scoped lookup.
func TestResolveTmuxResume_StepScoped(t *testing.T) {
	f := setupHibernationFixture(t, hibernateYML)
	tk := f.newTask(t, 1)
	if err := f.database.UpsertChat(tk.ID, "implement", "sess-step", ""); err != nil {
		t.Fatal(err)
	}

	target := f.resolve(t, tk)
	if target.label != "implement" || !target.isStep {
		t.Errorf("target label/isStep = %q/%v, want implement/true", target.label, target.isStep)
	}
	if !target.scoped || !target.resumable() {
		t.Errorf("expected a scoped, resumable target, got scoped=%v resumable=%v", target.scoped, target.resumable())
	}
	if target.chat == nil || target.chat.SessionID != "sess-step" {
		t.Errorf("target chat = %+v, want the implement row", target.chat)
	}
}

// A chat row for a DIFFERENT step must not count as this step's conversation:
// the resolver falls back to it (so a restore is no worse off than before
// hibernation existed) but marks it unscoped, which is what keeps hibernation
// from ever killing a session that would come back on the wrong conversation.
func TestResolveTmuxResume_ForeignStepChatIsUnscoped(t *testing.T) {
	f := setupHibernationFixture(t, hibernateYML)
	tk := f.newTask(t, 1)
	if err := f.database.UpsertChat(tk.ID, "some-other-step", "sess-other", ""); err != nil {
		t.Fatal(err)
	}

	target := f.resolve(t, tk)
	if target.scoped {
		t.Error("a foreign step's chat must not count as scoped")
	}
	if target.chat == nil || target.chat.SessionID != "sess-other" {
		t.Errorf("expected the GetLatestChat fallback to still supply a chat, got %+v", target.chat)
	}

	// Startup restore keeps using that fallback: the wrapper script resumes it.
	if _, err := f.server.restoreTmuxSession(tk); err != nil {
		t.Fatalf("restoreTmuxSession: %v", err)
	}
	script := readRestoreScript(t, f.projectDir, "implement")
	if !strings.Contains(script, "export SAKUSEN_SESSION_ID='sess-other'") {
		t.Errorf("expected restore to fall back to the latest chat\nscript:\n%s", script)
	}
}

func TestResolveTmuxResume_NotResumableWithoutResumeCommand(t *testing.T) {
	f := setupHibernationFixture(t, hibernateYMLNoResume)
	tk := f.newTask(t, 1)
	if err := f.database.UpsertChat(tk.ID, "implement", "sess-step", ""); err != nil {
		t.Fatal(err)
	}

	if target := f.resolve(t, tk); target.resumable() {
		t.Error("an agent without resume_command can never be resumed")
	}
}

func TestResolveTmuxResume_NotResumableWithoutSessionID(t *testing.T) {
	f := setupHibernationFixture(t, hibernateYML)
	tk := f.newTask(t, 1)
	if err := f.database.UpsertChat(tk.ID, "implement", "", ""); err != nil {
		t.Fatal(err)
	}

	if target := f.resolve(t, tk); target.resumable() {
		t.Error("a chat row with no session id is not resumable")
	}
}

// Ad-hoc sessions resolve off their own pseudo-step label, and restore under
// the label the conversation was actually recorded with.
func TestResolveTmuxResume_AdHocLabels(t *testing.T) {
	for _, label := range adHocSessionLabels {
		t.Run(label, func(t *testing.T) {
			f := setupHibernationFixture(t, hibernateYML)
			tk := f.newTask(t, 0)
			if err := f.database.UpsertChat(tk.ID, label, "sess-adhoc", ""); err != nil {
				t.Fatal(err)
			}

			target := f.resolve(t, tk)
			if target.isStep {
				t.Error("a task with no paused step must resolve as ad-hoc")
			}
			if target.label != label {
				t.Errorf("target label = %q, want %q", target.label, label)
			}
			if !target.scoped || !target.resumable() {
				t.Errorf("expected a scoped, resumable ad-hoc target, got scoped=%v resumable=%v", target.scoped, target.resumable())
			}
		})
	}
}

// ------------------------------------------------------- sentinel capture

// Ad-hoc sessions get their session id recorded under their own label and
// their sentinel consumed — without this they would never be resumable, and
// so never hibernate.
func TestCaptureAdHocSentinelSession(t *testing.T) {
	for _, label := range adHocSessionLabels {
		t.Run(label, func(t *testing.T) {
			f := setupHibernationFixture(t, hibernateYML)
			tk := f.newTask(t, 0)
			sentinel := writeSentinelWithSession(t, f.projectDir, label, "sess-adhoc")

			f.server.maybeAutoAdvance(tk)

			chat, err := f.database.GetChatByStep(tk.ID, label)
			if err != nil {
				t.Fatal(err)
			}
			if chat == nil || chat.SessionID != "sess-adhoc" {
				t.Fatalf("expected a %q chat row carrying the sentinel session, got %+v", label, chat)
			}
			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Errorf("expected the sentinel to be consumed (stat: %v)", err)
			}

			f.server.mu.RLock()
			entry := f.server.tmuxAutoState[tk.ID]
			f.server.mu.RUnlock()
			if entry == nil || !entry.atPrompt {
				t.Error("a turn-end sentinel must park the task at its prompt")
			}
		})
	}
}

// A human step's sentinel records the session AND marks the agent at its
// prompt, without advancing the task.
func TestMaybeAutoAdvance_HumanStepMarksAtPrompt(t *testing.T) {
	f := setupHibernationFixture(t, hibernateYML)
	tk := f.newTask(t, 1)
	writeSentinelWithSession(t, f.projectDir, "implement", "sess-step")

	f.server.maybeAutoAdvance(tk)

	f.server.mu.RLock()
	entry := f.server.tmuxAutoState[tk.ID]
	f.server.mu.RUnlock()
	if entry == nil || !entry.atPrompt {
		t.Fatal("expected the step sentinel to park the task at its prompt")
	}
	refreshed, err := f.database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Status != task.StatusTmux {
		t.Errorf("a human-gated step must not auto-advance, got %s", refreshed.Status)
	}
}

// An explicit-advance step's sentinel records the session AND marks the agent
// at its prompt, without advancing the task.
func TestMaybeAutoAdvance_ExplicitAdvanceStepCapturesSessionAndMarksAtPrompt(t *testing.T) {
	f := setupHibernationFixture(t, hibernateYMLExplicitAdvance)
	tk := f.newTask(t, 1)
	sentinel := writeSentinelWithSession(t, f.projectDir, "implement", "sess-explicit")

	f.server.maybeAutoAdvance(tk)

	chat, err := f.database.GetChatByStep(tk.ID, "implement")
	if err != nil {
		t.Fatal(err)
	}
	if chat == nil || chat.SessionID != "sess-explicit" {
		t.Fatalf("expected an implement chat row carrying the sentinel session, got %+v", chat)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Errorf("expected the sentinel to be consumed (stat: %v)", err)
	}
	f.server.mu.RLock()
	entry := f.server.tmuxAutoState[tk.ID]
	f.server.mu.RUnlock()
	if entry == nil || !entry.atPrompt {
		t.Fatal("expected the step sentinel to park the task at its prompt")
	}
	if entry.advancing {
		t.Error("an explicit-advance step must not be marked advancing")
	}
	refreshed, err := f.database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Status != task.StatusTmux {
		t.Errorf("an explicit-advance step must not auto-advance, got %s", refreshed.Status)
	}
}

// The monitor tick itself stamps idleSince when a pane settles and clears it
// (along with atPrompt) as soon as the pane starts repainting again.
func TestCheckTmuxActivity_TracksIdleAndPromptState(t *testing.T) {
	f := setupHibernationFixture(t, hibernateYML)
	tk := f.newTask(t, 1)
	f.spawnSession(t, tk, "implement")

	monitor := tmux.NewMonitor(tmux.DefaultMonitorConfig())

	// A static pane settles into "idle" after StableThreshold polls.
	var idleSince time.Time
	for i := 0; i < 30; i++ {
		f.server.checkTmuxActivity(monitor)
		if since, ok := f.server.tmuxIdleSince(tk.ID); ok {
			idleSince = since
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if idleSince.IsZero() {
		t.Fatal("expected the monitor to record an idle transition for a static pane")
	}

	f.server.markTmuxAtPrompt(tk.ID)

	// Repainting the pane means a turn is in flight again.
	if err := f.session(tk.ID).SendKeys("echo hibernation-probe"); err != nil {
		t.Fatalf("failed to send keys: %v", err)
	}
	wentWIP := false
	for i := 0; i < 30; i++ {
		f.server.checkTmuxActivity(monitor)
		if _, ok := f.server.tmuxIdleSince(tk.ID); !ok {
			wentWIP = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !wentWIP {
		t.Fatal("expected a repainting pane to clear idleSince")
	}
	f.server.mu.RLock()
	entry := f.server.tmuxAutoState[tk.ID]
	f.server.mu.RUnlock()
	if entry == nil || entry.atPrompt {
		t.Error("expected the idle->wip transition to clear atPrompt")
	}
}

// A woken session is NOT eligible to hibernate again just because its pane
// settled: the user woke it to use it, and a turn they start before the pane
// first goes idle produces no idle->wip edge, so an at-prompt mark set on wake
// would survive into that turn and get the session killed mid-turn. Only the
// next turn-end sentinel makes it eligible.
func TestCheckTmuxActivity_WokenSessionWaitsForTurnEnd(t *testing.T) {
	f, tk := setupEligible(t, hibernateYML)
	if !f.server.hibernateTmuxTask(tk, "idle") {
		t.Fatal("setup: expected the task to hibernate")
	}
	if _, err := f.server.wakeTmuxTask(tk.ID); err != nil {
		t.Fatalf("wakeTmuxTask: %v", err)
	}

	monitor := tmux.NewMonitor(tmux.DefaultMonitorConfig())
	settled := false
	for i := 0; i < 30; i++ {
		f.server.checkTmuxActivity(monitor)
		if _, ok := f.server.tmuxIdleSince(tk.ID); ok {
			settled = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !settled {
		t.Fatal("expected the woken pane to settle into idle")
	}

	refreshed, err := f.database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f.server.eligibleForHibernation(refreshed) {
		t.Error("a woken session must not be eligible before its next turn ends")
	}

	f.server.markTmuxAtPrompt(tk.ID)
	if !f.server.eligibleForHibernation(refreshed) {
		t.Error("a woken session must be eligible again once a turn-end sentinel parks it at its prompt")
	}
}

// ------------------------------------------------------------ eligibility

// setupEligible builds the happy path: a live, detached, idle-at-prompt task
// with a resumable conversation.
func setupEligible(t *testing.T, yml string) (*hibernationFixture, *task.Task) {
	t.Helper()
	f := setupHibernationFixture(t, yml)
	tk := f.newTask(t, 1)
	if err := f.database.UpsertChat(tk.ID, "implement", "sess-step", ""); err != nil {
		t.Fatal(err)
	}
	f.spawnSession(t, tk, "implement")
	f.markIdleAtPrompt(tk.ID, time.Now().Add(-time.Hour))
	return f, tk
}

func TestEligibleForHibernation_HappyPath(t *testing.T) {
	f, tk := setupEligible(t, hibernateYML)
	// A session created detached has no client attached, so this also covers
	// the #{session_attached} query's negative case. The attached case can't
	// be simulated without a real terminal client, so it is left to the
	// Attached() helper's own contract: a query failure reads as attached.
	if !f.server.eligibleForHibernation(tk) {
		t.Error("an idle, at-prompt, detached, resumable tmux task must be eligible")
	}
}

func TestEligibleForHibernation_Vetoes(t *testing.T) {
	t.Run("not at prompt", func(t *testing.T) {
		f, tk := setupEligible(t, hibernateYML)
		f.server.mu.Lock()
		f.server.tmuxAutoState[tk.ID].atPrompt = false
		f.server.mu.Unlock()
		if f.server.eligibleForHibernation(tk) {
			t.Error("a task that never reported a turn end may be stalled mid-turn")
		}
	})

	t.Run("wip", func(t *testing.T) {
		f, tk := setupEligible(t, hibernateYML)
		f.server.mu.Lock()
		f.server.tmuxActivity[tk.ID] = string(tmux.ActivityWIP)
		f.server.mu.Unlock()
		if f.server.eligibleForHibernation(tk) {
			t.Error("a busy pane must never be hibernated")
		}
	})

	t.Run("advancing", func(t *testing.T) {
		f, tk := setupEligible(t, hibernateYML)
		f.server.mu.Lock()
		f.server.tmuxAutoState[tk.ID].advancing = true
		f.server.mu.Unlock()
		if f.server.eligibleForHibernation(tk) {
			t.Error("a task mid-advance must not have its session pulled")
		}
	})

	t.Run("already hibernated", func(t *testing.T) {
		f, tk := setupEligible(t, hibernateYML)
		now := time.Now()
		tk.HibernatedAt = &now
		if f.server.eligibleForHibernation(tk) {
			t.Error("an already-hibernated task must not hibernate twice")
		}
	})

	t.Run("no resume_command", func(t *testing.T) {
		f, tk := setupEligible(t, hibernateYMLNoResume)
		if f.server.eligibleForHibernation(tk) {
			t.Error("killing a session that cannot be resumed would lose the conversation")
		}
	})

	t.Run("no scoped chat", func(t *testing.T) {
		f := setupHibernationFixture(t, hibernateYML)
		tk := f.newTask(t, 1)
		if err := f.database.UpsertChat(tk.ID, "some-other-step", "sess-other", ""); err != nil {
			t.Fatal(err)
		}
		f.spawnSession(t, tk, "implement")
		f.markIdleAtPrompt(tk.ID, time.Now().Add(-time.Hour))
		if f.server.eligibleForHibernation(tk) {
			t.Error("only a step/label-scoped chat may make a task eligible")
		}
	})
}

// --------------------------------------------------------- hibernate action

func TestHibernateTmuxTask(t *testing.T) {
	f, tk := setupEligible(t, hibernateYML)
	sentinel := writeSentinelWithSession(t, f.projectDir, "implement", "sess-step")

	// A decoy whose name starts with the target's: tmux.KillSessionsForTask
	// matches by prefix, so hibernating #N must not take #N0 with it.
	decoy := tmux.NewSession(f.projectName, fmt.Sprintf("%d0", tk.ID), "", f.projectDir)
	if err := decoy.Create("sleep", "600"); err != nil {
		t.Fatalf("failed to create decoy session: %v", err)
	}
	t.Cleanup(func() { decoy.Kill() })

	if !f.server.hibernateTmuxTask(tk, "idle") {
		t.Fatal("expected hibernateTmuxTask to hibernate the task")
	}

	if sessionExistsExactly(t, f.session(tk.ID).Name) {
		t.Error("expected the task's tmux session to be gone")
	}
	if !sessionExistsExactly(t, decoy.Name) {
		t.Error("hibernation must kill only the exact session, not prefix matches")
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Errorf("expected the label's sentinels cleared so a wake can't auto-advance (stat: %v)", err)
	}

	refreshed, err := f.database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.HibernatedAt == nil {
		t.Error("expected hibernated_at to be stamped")
	}
	if refreshed.Status != task.StatusTmux {
		t.Errorf("a hibernated task stays in tmux status, got %s", refreshed.Status)
	}

	f.server.mu.RLock()
	_, hasActivity := f.server.tmuxActivity[tk.ID]
	_, hasState := f.server.tmuxAutoState[tk.ID]
	f.server.mu.RUnlock()
	if hasActivity || hasState {
		t.Error("expected the in-memory monitor entries to be dropped")
	}
}

// The status re-read under the lock is what stops a hibernation pass from
// killing the session of a task a concurrent advance just resumed.
func TestHibernateTmuxTask_AbortsWhenStatusChanged(t *testing.T) {
	f, tk := setupEligible(t, hibernateYML)
	if err := f.database.UpdateTaskStatus(tk.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}

	if f.server.hibernateTmuxTask(tk, "idle") {
		t.Error("expected hibernation to abort for a task that left tmux status")
	}
	if !f.session(tk.ID).Exists() {
		t.Error("the session of a running task must be left alone")
	}
}

// ---------------------------------------------------------------- passes

func TestHibernateIdleTmuxSessions_Threshold(t *testing.T) {
	f, tk := setupEligible(t, hibernateYML)
	f.server.cfg.TmuxHibernateAfter = 30 * time.Minute

	f.server.hibernateIdleTmuxSessions([]*task.Task{tk})

	refreshed, err := f.database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.HibernatedAt == nil {
		t.Error("a task idle past the threshold must be hibernated")
	}
}

func TestHibernateIdleTmuxSessions_ThresholdNotReached(t *testing.T) {
	f, tk := setupEligible(t, hibernateYML)
	f.server.cfg.TmuxHibernateAfter = 4 * time.Hour

	f.server.hibernateIdleTmuxSessions([]*task.Task{tk})

	refreshed, err := f.database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.HibernatedAt != nil {
		t.Error("a task idle for an hour must survive a 4h threshold")
	}
	if !f.session(tk.ID).Exists() {
		t.Error("expected the session to still be live")
	}
}

func TestHibernateIdleTmuxSessions_Disabled(t *testing.T) {
	f, tk := setupEligible(t, hibernateYML)
	// Both settings at 0: hibernation is switched off entirely.
	f.server.cfg.TmuxHibernateAfter = 0
	f.server.cfg.TmuxMaxLiveSessions = 0

	f.server.hibernateIdleTmuxSessions([]*task.Task{tk})

	refreshed, err := f.database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.HibernatedAt != nil {
		t.Error("hibernation must be a no-op when both settings are 0")
	}
}

// The cap reclaims oldest-idle-first, stops the moment it is back at the cap,
// and never touches an ineligible session even while still over it.
func TestHibernateIdleTmuxSessions_Cap(t *testing.T) {
	f := setupHibernationFixture(t, hibernateYML)
	f.server.cfg.TmuxHibernateAfter = 0 // threshold off: isolate the cap pass
	f.server.cfg.TmuxMaxLiveSessions = 2

	now := time.Now()
	var live []*task.Task
	// Three eligible tasks with distinct idle ages, plus one busy task that
	// must survive no matter how far over the cap we are.
	for i, age := range []time.Duration{3 * time.Hour, 2 * time.Hour, time.Hour} {
		tk := f.newTask(t, 1)
		if err := f.database.UpsertChat(tk.ID, "implement", fmt.Sprintf("sess-%d", i), ""); err != nil {
			t.Fatal(err)
		}
		f.spawnSession(t, tk, "implement")
		f.markIdleAtPrompt(tk.ID, now.Add(-age))
		live = append(live, tk)
	}
	busy := f.newTask(t, 1)
	if err := f.database.UpsertChat(busy.ID, "implement", "sess-busy", ""); err != nil {
		t.Fatal(err)
	}
	f.spawnSession(t, busy, "implement")
	f.markIdleAtPrompt(busy.ID, now.Add(-4*time.Hour))
	f.server.mu.Lock()
	f.server.tmuxActivity[busy.ID] = string(tmux.ActivityWIP)
	f.server.mu.Unlock()
	live = append(live, busy)

	f.server.hibernateIdleTmuxSessions(live)

	hibernated := func(tk *task.Task) bool {
		refreshed, err := f.database.GetTask(tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		return refreshed.HibernatedAt != nil
	}

	// 4 live, cap 2 → the two oldest-idle eligible sessions go.
	if !hibernated(live[0]) {
		t.Error("expected the oldest-idle session to be hibernated first")
	}
	if !hibernated(live[1]) {
		t.Error("expected the second-oldest idle session to be hibernated")
	}
	if hibernated(live[2]) {
		t.Error("the cap pass must stop once the count is back at the cap")
	}
	if hibernated(busy) {
		t.Error("a busy session must never be hibernated, cap or no cap")
	}
	if !f.session(busy.ID).Exists() {
		t.Error("expected the busy session to still be live")
	}
}

// A session restored at daemon startup is marked at-prompt once it first
// settles, so untouched restored sessions stay reclaimable by the cap.
func TestCheckTmuxActivity_RestoredSessionMarkedOnFirstIdle(t *testing.T) {
	f := setupHibernationFixture(t, hibernateYML)
	tk := f.newTask(t, 1)
	f.spawnSession(t, tk, "implement")
	f.server.markTmuxAtPromptOnIdle(tk.ID)

	monitor := tmux.NewMonitor(tmux.DefaultMonitorConfig())
	settled := false
	for i := 0; i < 30; i++ {
		f.server.checkTmuxActivity(monitor)
		if _, ok := f.server.tmuxIdleSince(tk.ID); ok {
			settled = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !settled {
		t.Fatal("expected the restored pane to settle into idle")
	}

	f.server.mu.RLock()
	entry := f.server.tmuxAutoState[tk.ID]
	f.server.mu.RUnlock()
	if entry == nil || !entry.atPrompt || entry.atPromptOnIdle {
		t.Error("expected the first idle transition to turn the deferred mark into atPrompt")
	}
}

// The cap never reclaims a session idle for less than capMinIdle, however far
// over the cap the daemon is: a user who just looked away (or a pane briefly
// static mid-turn) must not lose the session.
func TestHibernateIdleTmuxSessions_CapSparesRecentlyIdle(t *testing.T) {
	f := setupHibernationFixture(t, hibernateYML)
	f.server.cfg.TmuxHibernateAfter = 0 // threshold off: isolate the cap pass
	f.server.cfg.TmuxMaxLiveSessions = 1

	now := time.Now()
	var live []*task.Task
	for i, age := range []time.Duration{capMinIdle - time.Minute, 15 * time.Second} {
		tk := f.newTask(t, 1)
		if err := f.database.UpsertChat(tk.ID, "implement", fmt.Sprintf("sess-%d", i), ""); err != nil {
			t.Fatal(err)
		}
		f.spawnSession(t, tk, "implement")
		f.markIdleAtPrompt(tk.ID, now.Add(-age))
		live = append(live, tk)
	}

	f.server.hibernateIdleTmuxSessions(live)

	for _, tk := range live {
		refreshed, err := f.database.GetTask(tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		if refreshed.HibernatedAt != nil {
			t.Errorf("task #%d was idle under capMinIdle and must not be reclaimed by the cap", tk.ID)
		}
	}
}

// ------------------------------------------------------------------- wake

func TestWakeTmuxTask_ResumesHibernatedSession(t *testing.T) {
	f, tk := setupEligible(t, hibernateYML)
	if !f.server.hibernateTmuxTask(tk, "idle") {
		t.Fatal("setup: expected the task to hibernate")
	}

	if _, err := f.server.wakeTmuxTask(tk.ID); err != nil {
		t.Fatalf("wakeTmuxTask: %v", err)
	}

	if !f.session(tk.ID).Exists() {
		t.Error("expected wake to bring the tmux session back")
	}
	script := readRestoreScript(t, f.projectDir, "implement")
	if !strings.Contains(script, "export SAKUSEN_SESSION_ID='sess-step'") {
		t.Errorf("expected wake to resume the recorded session\nscript:\n%s", script)
	}

	refreshed, err := f.database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.HibernatedAt != nil {
		t.Error("expected wake to clear the hibernation marker")
	}

	f.server.mu.RLock()
	entry := f.server.tmuxAutoState[tk.ID]
	f.server.mu.RUnlock()
	if entry != nil && entry.atPrompt {
		t.Error("wake must not mark the session at-prompt; only a turn-end sentinel does")
	}
}

func TestWakeTmuxTask_NoopWhenSessionLive(t *testing.T) {
	f, tk := setupEligible(t, hibernateYML)
	// A stale marker with a live session (e.g. the user started one by hand).
	now := time.Now()
	if err := f.database.SetTaskHibernatedAt(tk.ID, &now); err != nil {
		t.Fatal(err)
	}

	msg, err := f.server.wakeTmuxTask(tk.ID)
	if err != nil {
		t.Fatalf("wakeTmuxTask: %v", err)
	}
	if !strings.Contains(msg, "already") {
		t.Errorf("wake message = %q, want it to report the live session", msg)
	}
	refreshed, err := f.database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.HibernatedAt != nil {
		t.Error("expected the stale marker cleared even with nothing to spawn")
	}
}

// Wake also self-heals a session lost outside sakusen's control — no
// hibernation marker required.
func TestWakeTmuxTask_RestoresMissingSessionWithoutMarker(t *testing.T) {
	f := setupHibernationFixture(t, hibernateYML)
	tk := f.newTask(t, 1)
	if err := f.database.UpsertChat(tk.ID, "implement", "sess-step", ""); err != nil {
		t.Fatal(err)
	}

	if _, err := f.server.wakeTmuxTask(tk.ID); err != nil {
		t.Fatalf("wakeTmuxTask: %v", err)
	}
	if !f.session(tk.ID).Exists() {
		t.Error("expected wake to restore a missing session")
	}
}

func TestWakeTmuxTask_RejectsNonTmuxTask(t *testing.T) {
	f := setupHibernationFixture(t, hibernateYML)
	tk := f.newTask(t, 1)
	if err := f.database.UpdateTaskStatus(tk.ID, task.StatusRunning); err != nil {
		t.Fatal(err)
	}

	if _, err := f.server.wakeTmuxTask(tk.ID); err == nil {
		t.Error("expected wake to reject a task that is not in tmux state")
	}
}

// ---------------------------------------------------------------- startup

// Daemon restart must leave hibernated tasks down (otherwise every restart
// wakes everything) while restoring the rest as before.
func TestRecoverOrphanedTasks_SkipsHibernated(t *testing.T) {
	f := setupHibernationFixture(t, hibernateYML)

	sleeping := f.newTask(t, 1)
	if err := f.database.UpsertChat(sleeping.ID, "implement", "sess-sleeping", ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := f.database.SetTaskHibernatedAt(sleeping.ID, &now); err != nil {
		t.Fatal(err)
	}

	awake := f.newTask(t, 1)
	if err := f.database.UpsertChat(awake.ID, "implement", "sess-awake", ""); err != nil {
		t.Fatal(err)
	}

	if err := f.server.recoverOrphanedTasks(); err != nil {
		t.Fatalf("recoverOrphanedTasks: %v", err)
	}

	if f.session(sleeping.ID).Exists() {
		t.Error("a hibernated task must stay down across a daemon restart")
	}
	if !f.session(awake.ID).Exists() {
		t.Error("a non-hibernated tmux task must still be restored")
	}
}
