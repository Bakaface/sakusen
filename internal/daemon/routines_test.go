package daemon

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
)

// baseRoutineConfig is the project .sakusen.yml most scheduler tests run
// against: one workflow and one scheduled routine bound to it.
const baseRoutineConfig = `workflows:
  - name: default
    steps:
      - name: impl
        prompt: "{{task.input}}"
routines:
  - name: nightly
    cadence: "@every 5m"
    workflow: default
    input: "the input"
`

// newSchedulerTestServer builds a server whose project carries a real on-disk
// .sakusen.yml. That file — not the server's fallback cfg — is what the fire
// path resolves routines and pins against, and it mirrors production: a routine
// row in the DB always has a matching entry in the project config (reconcile
// soft-deletes the ones that don't). HOME/XDG are isolated so the user's real
// global config can't leak in.
func newSchedulerTestServer(t *testing.T) (*Server, *db.DB, *db.Project) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	cfg := &config.Config{
		Workflows: []config.WorkflowConfig{
			{Name: "default", Steps: []config.StepConfig{{Name: "impl", Prompt: "do it"}}},
		},
	}
	s := NewServer(cfg, database)
	t.Cleanup(func() {
		s.manager.Shutdown(2 * time.Second)
		s.cancel()
	})

	proj, err := database.GetOrCreateProject(t.TempDir())
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	writeProjectConfig(t, proj.Path, baseRoutineConfig)
	return s, database, proj
}

// dueRoutine inserts a routine row whose next_fire_at is already in the past,
// so it is immediately due. UpsertPeriodicDef uses the supplied next_fire_at
// verbatim on INSERT, so no separate clamp step is needed.
func dueRoutine(t *testing.T, database *db.DB, projID int64, name, cadence, workflowRef, input, priority string) *db.PeriodicDef {
	t.Helper()
	d, err := database.UpsertPeriodicDef(projID, name, cadence, workflowRef, input, priority, false, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("upsert routine %q: %v", name, err)
	}
	return d
}

// writeProjectConfig drops a .sakusen.yml into the project dir so
// getProjectContext loads it (the fire path resolves the routine and its pins
// against the project config, not the server's fallback cfg).
func writeProjectConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".sakusen.yml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write .sakusen.yml: %v", err)
	}
}

// runsFor returns the tasks a routine has created, failing when the count
// doesn't match.
func runsFor(t *testing.T, database *db.DB, d *db.PeriodicDef, want int) []*task.Task {
	t.Helper()
	runs, err := database.GetTasksForPeriodic(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != want {
		t.Fatalf("expected %d task(s) for routine %q, got %d", want, d.Name, len(runs))
	}
	return runs
}

func TestReconcileRoutines_UpsertAndSoftDelete(t *testing.T) {
	s, database, proj := newSchedulerTestServer(t)

	cfg := &config.Config{
		Routines: []config.RoutineConfig{
			{Name: "nightly", Cadence: "0 3 * * *", Workflow: "default"},
			{Name: "compose-wiki", Workflow: "default"},
		},
	}
	if err := s.reconcilePeriodicsForProject(proj, cfg); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	defs, err := database.ListPeriodicsForProject(proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 2 {
		t.Fatalf("expected 2 routines (on-demand ones get a row too), got %d", len(defs))
	}

	// Remove "compose-wiki" from the config; reconcile must soft-delete it.
	cfg2 := &config.Config{
		Routines: []config.RoutineConfig{
			{Name: "nightly", Cadence: "0 3 * * *", Workflow: "default"},
		},
	}
	if err := s.reconcilePeriodicsForProject(proj, cfg2); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	defs2, err := database.ListPeriodicsForProject(proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs2) != 1 || defs2[0].Name != "nightly" {
		t.Fatalf("expected only 'nightly' after soft-delete, got %+v", defs2)
	}
}

func TestScheduledFire_CreatesTask(t *testing.T) {
	s, database, proj := newSchedulerTestServer(t)

	d := dueRoutine(t, database, proj.ID, "nightly", "@every 5m", "default", "the input", "high")

	s.fireDuePeriodics()
	// Allow the async title-finalize goroutine to settle before assertions/teardown.
	time.Sleep(200 * time.Millisecond)

	got := runsFor(t, database, d, 1)[0]
	if got.Workflow != "default" {
		t.Errorf("expected the referenced workflow name, got %q", got.Workflow)
	}
	if got.Input != "the input" {
		t.Errorf("expected input from the routine, got %q", got.Input)
	}
	if got.PeriodicID == nil || *got.PeriodicID != d.ID {
		t.Errorf("expected routine id %d, got %v", d.ID, got.PeriodicID)
	}

	// next_fire_at must have advanced to the future.
	reloaded, err := database.GetPeriodicByID(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.NextFireAt.After(time.Now()) {
		t.Errorf("expected next_fire_at in the future, got %v", reloaded.NextFireAt)
	}
}

// A routine's pins are passed explicitly on the create request, so they beat
// the referenced workflow's own — that is what lets one workflow back several
// differently pinned routines.
func TestScheduledFire_RoutinePinsBeatWorkflowPins(t *testing.T) {
	s, database, proj := newSchedulerTestServer(t)

	writeProjectConfig(t, proj.Path, `workflows:
  - name: default
    checkout: staging
    target: main
    steps:
      - name: impl
        prompt: "{{task.input}}"
routines:
  - name: nightly
    cadence: "@every 5m"
    workflow: default
    input: "the input"
    worktree: true
    branch: "sakusen/nightly-{{task.id}}"
`)

	d := dueRoutine(t, database, proj.ID, "nightly", "@every 5m", "default", "the input", "")

	s.fireDuePeriodics()
	time.Sleep(200 * time.Millisecond)

	got := runsFor(t, database, d, 1)[0]
	if got.BranchName != "sakusen/nightly-{{task.id}}" {
		t.Errorf("branch_name = %q, want the routine's pin", got.BranchName)
	}
	if got.CheckoutBranch != "" {
		t.Errorf("checkout_branch = %q, want the workflow's dropped with its pair", got.CheckoutBranch)
	}
	if got.TargetBranch != "main" {
		t.Errorf("target_branch = %q, want the workflow's to survive", got.TargetBranch)
	}
	if !got.Worktree {
		t.Error("worktree pin from the routine did not reach the task")
	}
}

// With no routine pins the referenced workflow's own still apply — the
// fallback in createTaskFromRequest is intact.
func TestScheduledFire_WorkflowPinsApplyWithoutRoutinePins(t *testing.T) {
	s, database, proj := newSchedulerTestServer(t)

	if !proj.DefaultWorktree {
		t.Fatalf("expected project default worktree true, got false")
	}

	writeProjectConfig(t, proj.Path, `workflows:
  - name: default
    worktree: false
    steps:
      - name: impl
        prompt: "{{task.input}}"
routines:
  - name: docs-refresh
    cadence: "@every 5m"
    workflow: default
    input: "refresh the docs"
`)

	d := dueRoutine(t, database, proj.ID, "docs-refresh", "@every 5m", "default", "refresh the docs", "")

	s.fireDuePeriodics()
	time.Sleep(200 * time.Millisecond)

	if runsFor(t, database, d, 1)[0].Worktree {
		t.Error("expected the workflow's worktree: false pin to reach the task")
	}

	// No pin may leak into the persisted project default.
	reloaded, err := database.GetProject(proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.DefaultWorktree {
		t.Error("a routine fire must not clobber the project default worktree")
	}
}

// An on-demand routine's row exists but is never due, whatever its
// next_fire_at says.
func TestScheduledFire_OnDemandRoutineNeverFires(t *testing.T) {
	s, database, proj := newSchedulerTestServer(t)

	writeProjectConfig(t, proj.Path, `workflows:
  - name: default
    steps:
      - name: impl
        prompt: "{{task.input}}"
routines:
  - name: compose-wiki
    workflow: default
    input: "compose it"
`)

	d := dueRoutine(t, database, proj.ID, "compose-wiki", "", "default", "compose it", "")

	s.fireDuePeriodics()
	time.Sleep(150 * time.Millisecond)

	runsFor(t, database, d, 0)
}

// A routine whose workflow needs an input nobody supplies can only exist
// on-demand (the config validator rejects the scheduled form), so a stale row
// that has become due must be skipped rather than fire an empty task.
func TestScheduledFire_SkipsRoutineRequiringInput(t *testing.T) {
	s, database, proj := newSchedulerTestServer(t)

	writeProjectConfig(t, proj.Path, `workflows:
  - name: default
    steps:
      - name: impl
        prompt: "work on {{task.input}}"
routines:
  - name: digest
    workflow: default
`)

	d := dueRoutine(t, database, proj.ID, "digest", "@every 5m", "default", "", "")

	s.fireDuePeriodics()
	time.Sleep(150 * time.Millisecond)

	runsFor(t, database, d, 0)
}

// TestFireDuePeriodics_AtMostOnceAfterDowntime exercises the startup catch-up
// guarantee. checkPeriodicCatchup is just reconcile + fireDuePeriodics, and the
// "fire at most once after downtime" property is delivered entirely by
// ClaimPeriodicFire advancing next_fire_at to the next FUTURE slot. So a
// routine that fell due during downtime fires exactly one task; a subsequent
// pass sees a future next_fire_at and fires nothing.
func TestFireDuePeriodics_AtMostOnceAfterDowntime(t *testing.T) {
	s, database, proj := newSchedulerTestServer(t)

	d := dueRoutine(t, database, proj.ID, "nightly", "@every 5m", "default", "the input", "high")

	// First pass: the routine is due (next_fire_at in the past) → fires once.
	s.fireDuePeriodics()
	time.Sleep(200 * time.Millisecond)

	// Second pass simulates a follow-up tick after catch-up. next_fire_at has
	// already advanced to the future, so nothing new should fire.
	s.fireDuePeriodics()
	time.Sleep(200 * time.Millisecond)

	runsFor(t, database, d, 1)

	reloaded, err := database.GetPeriodicByID(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.NextFireAt.After(time.Now()) {
		t.Errorf("expected next_fire_at clamped to the future, got %v", reloaded.NextFireAt)
	}
}

func TestScheduledFire_SkipOverlap(t *testing.T) {
	s, database, proj := newSchedulerTestServer(t)

	d := dueRoutine(t, database, proj.ID, "nightly", "@every 5m", "default", "the input", "medium")

	// Simulate a still-running prior fire.
	prior, err := database.CreateTask(proj.ID, "prev", "d", "prev", "default", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetTaskPeriodicID(prior.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdatePeriodicLastTask(d.ID, prior.ID); err != nil {
		t.Fatal(err)
	}

	s.fireDuePeriodics()
	time.Sleep(150 * time.Millisecond)

	runsFor(t, database, d, 1)
	// Schedule still advanced past now despite skip.
	reloaded, _ := database.GetPeriodicByID(d.ID)
	if !reloaded.NextFireAt.After(time.Now()) {
		t.Errorf("expected schedule to advance even when skipped, got %v", reloaded.NextFireAt)
	}
}

func TestScheduledFire_PausedNeverFires(t *testing.T) {
	s, database, proj := newSchedulerTestServer(t)

	d := dueRoutine(t, database, proj.ID, "nightly", "@every 5m", "default", "the input", "medium")
	if err := database.SetPeriodicPaused(d.ID, true); err != nil {
		t.Fatal(err)
	}

	s.fireDuePeriodics()
	time.Sleep(100 * time.Millisecond)

	runsFor(t, database, d, 0)
}

// ── Run-now handlers (name-addressed) ──

// callHandler runs a handler against a pipe and returns its single reply.
func callHandler(t *testing.T, fn func(net.Conn)) *Message {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { clientConn.Close() })
	go func() {
		fn(serverConn)
		serverConn.Close()
	}()
	return readOneMessage(t, clientConn)
}

func TestRunRoutineNow_OnDemandByName(t *testing.T) {
	s, database, proj := newSchedulerTestServer(t)

	writeProjectConfig(t, proj.Path, `workflows:
  - name: default
    steps:
      - name: impl
        prompt: "{{task.input}}"
routines:
  - name: compose-wiki
    workflow: default
    input: "compose it"
`)

	msg := callHandler(t, func(c net.Conn) {
		s.handleFirePeriodicNow(c, FirePeriodicNowRequest{ProjectPath: proj.Path, Name: "compose-wiki"})
	})
	if msg.Type != MsgFirePeriodicNow {
		t.Fatalf("expected a task reply, got %s: %s", msg.Type, string(msg.Payload))
	}
	time.Sleep(200 * time.Millisecond)

	d, err := database.GetPeriodicByName(proj.ID, "compose-wiki")
	if err != nil {
		t.Fatalf("routine row missing after run: %v", err)
	}
	got := runsFor(t, database, d, 1)[0]
	if got.Input != "compose it" {
		t.Errorf("input = %q, want the routine's literal input", got.Input)
	}
	if d.LastTaskID == nil || *d.LastTaskID != got.ID {
		t.Errorf("last_task_id = %v, want %d", d.LastTaskID, got.ID)
	}
}

func TestRunRoutineNow_RequiresInput(t *testing.T) {
	s, database, proj := newSchedulerTestServer(t)

	writeProjectConfig(t, proj.Path, `workflows:
  - name: default
    steps:
      - name: impl
        prompt: "work on {{task.input}}"
routines:
  - name: digest
    workflow: default
`)

	msg := callHandler(t, func(c net.Conn) {
		s.handleFirePeriodicNow(c, FirePeriodicNowRequest{ProjectPath: proj.Path, Name: "digest"})
	})
	if msg.Type != MsgError || !strings.Contains(string(msg.Payload), "requires an input argument") {
		t.Fatalf("expected a requires-input error, got %s: %s", msg.Type, string(msg.Payload))
	}

	msg = callHandler(t, func(c net.Conn) {
		s.handleFirePeriodicNow(c, FirePeriodicNowRequest{ProjectPath: proj.Path, Name: "digest", Input: "hello"})
	})
	if msg.Type != MsgFirePeriodicNow {
		t.Fatalf("expected a task reply with input, got %s: %s", msg.Type, string(msg.Payload))
	}
	time.Sleep(200 * time.Millisecond)

	d, err := database.GetPeriodicByName(proj.ID, "digest")
	if err != nil {
		t.Fatal(err)
	}
	if got := runsFor(t, database, d, 1)[0].Input; got != "hello" {
		t.Errorf("input = %q, want the supplied argument", got)
	}
}

func TestRunRoutineNow_ExplicitInputOverridesLiteral(t *testing.T) {
	s, database, proj := newSchedulerTestServer(t)

	msg := callHandler(t, func(c net.Conn) {
		s.handleFirePeriodicNow(c, FirePeriodicNowRequest{ProjectPath: proj.Path, Name: "nightly", Input: "override"})
	})
	if msg.Type != MsgFirePeriodicNow {
		t.Fatalf("expected a task reply, got %s: %s", msg.Type, string(msg.Payload))
	}
	time.Sleep(200 * time.Millisecond)

	d, err := database.GetPeriodicByName(proj.ID, "nightly")
	if err != nil {
		t.Fatal(err)
	}
	if got := runsFor(t, database, d, 1)[0].Input; got != "override" {
		t.Errorf("input = %q, want the explicit argument to win", got)
	}
}

func TestRunRoutineNow_UnknownName(t *testing.T) {
	s, _, proj := newSchedulerTestServer(t)

	msg := callHandler(t, func(c net.Conn) {
		s.handleFirePeriodicNow(c, FirePeriodicNowRequest{ProjectPath: proj.Path, Name: "nope"})
	})
	if msg.Type != MsgError || !strings.Contains(string(msg.Payload), "no routine") || !strings.Contains(string(msg.Payload), "nope") {
		t.Fatalf("expected an unknown-routine error, got %s: %s", msg.Type, string(msg.Payload))
	}
}

func TestRunRoutineNow_SoftDeletedRejected(t *testing.T) {
	s, database, proj := newSchedulerTestServer(t)

	// A row for a routine the config no longer declares: reconcile (which
	// resolveRoutineRow runs) soft-deletes it, and firing it is rejected.
	dueRoutine(t, database, proj.ID, "gone", "@every 5m", "default", "x", "")

	msg := callHandler(t, func(c net.Conn) {
		s.handleFirePeriodicNow(c, FirePeriodicNowRequest{ProjectPath: proj.Path, Name: "gone"})
	})
	if msg.Type != MsgError || !strings.Contains(string(msg.Payload), "removed from .sakusen.yml") {
		t.Fatalf("expected a removed-routine error, got %s: %s", msg.Type, string(msg.Payload))
	}
}

func TestSetRoutinePaused_RejectsOnDemand(t *testing.T) {
	s, _, proj := newSchedulerTestServer(t)

	writeProjectConfig(t, proj.Path, `workflows:
  - name: default
    steps:
      - name: impl
        prompt: "{{task.input}}"
routines:
  - name: compose-wiki
    workflow: default
    input: "compose it"
`)

	msg := callHandler(t, func(c net.Conn) {
		s.handleSetPeriodicPaused(c, SetPeriodicPausedRequest{ProjectPath: proj.Path, Name: "compose-wiki", Paused: true})
	})
	if msg.Type != MsgError || !strings.Contains(string(msg.Payload), "nothing to pause") {
		t.Fatalf("expected a no-cadence error, got %s: %s", msg.Type, string(msg.Payload))
	}
}
