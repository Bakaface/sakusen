package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
)

func newSchedulerTestServer(t *testing.T) (*Server, *db.DB, *db.Project) {
	t.Helper()
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
	return s, database, proj
}

// duePeriodicDef inserts a periodic definition whose next_fire_at is already in
// the past, so it is immediately due. UpsertPeriodicDef uses the supplied
// next_fire_at verbatim on INSERT, so no separate clamp step is needed.
func duePeriodicDef(t *testing.T, database *db.DB, projID int64, name, cadence, workflowRef, input, priority string) *db.PeriodicDef {
	t.Helper()
	d, err := database.UpsertPeriodicDef(projID, name, cadence, workflowRef, input, priority, false, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("upsert periodic %q: %v", name, err)
	}
	return d
}

// writeProjectConfig drops a .sakusen.yml into the project dir so
// getProjectContext loads it (createTaskFromRequest resolves workflow pins
// against the project config, not the server's fallback cfg).
func writeProjectConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".sakusen.yml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write .sakusen.yml: %v", err)
	}
}

func TestReconcilePeriodics_UpsertAndSoftDelete(t *testing.T) {
	s, database, proj := newSchedulerTestServer(t)

	cfg := &config.Config{
		Periodic: []config.PeriodicEntry{
			{Name: "nightly", Cadence: "0 3 * * *", Workflow: "default"},
			{Name: "hourly", Cadence: "@every 1h", Steps: []config.StepConfig{{Name: "s", Prompt: "p"}}},
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
		t.Fatalf("expected 2 definitions, got %d", len(defs))
	}

	// Remove "hourly" from the config; reconcile must soft-delete it.
	cfg2 := &config.Config{
		Periodic: []config.PeriodicEntry{
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

	d := duePeriodicDef(t, database, proj.ID, "nightly", "@every 5m", "", "the input", "high")

	s.fireDuePeriodics()
	// Allow the async title-finalize goroutine to settle before assertions/teardown.
	time.Sleep(200 * time.Millisecond)

	runs, err := database.GetTasksForPeriodic(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 materialized task, got %d", len(runs))
	}
	got := runs[0]
	if got.Workflow != "periodic:nightly" {
		t.Errorf("expected workflow periodic:nightly, got %q", got.Workflow)
	}
	if got.Input != "the input" {
		t.Errorf("expected input from definition, got %q", got.Input)
	}
	if got.PeriodicID == nil || *got.PeriodicID != d.ID {
		t.Errorf("expected periodic_id %d, got %v", d.ID, got.PeriodicID)
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

// A materialized fire never sets CreateTaskRequest.Worktree, so the inline
// entry's worktree pin (copied onto the hidden "periodic:<name>" workflow) is
// the only thing standing between the task and the project default — which the
// user's last interactive task creation mutates. This drives the full path:
// on-disk .sakusen.yml → hidden workflow pin → materialized task row.
func TestScheduledFire_InlineWorktreePinReachesTask(t *testing.T) {
	// Isolate HOME/XDG so the user's real global config can't leak in.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	s, database, proj := newSchedulerTestServer(t)

	// Project default is worktree-on (db default); the pin must win.
	if !proj.DefaultWorktree {
		t.Fatalf("expected project default worktree true, got false")
	}

	writeProjectConfig(t, proj.Path, `workflows:
  - name: default
    steps:
      - name: impl
        prompt: "do it"
periodic:
  - name: docs-refresh
    cadence: "@every 5m"
    worktree: false
    input: "refresh the docs"
    steps:
      - name: refresh
        prompt: "{{task.input}}"
`)

	d := duePeriodicDef(t, database, proj.ID, "docs-refresh", "@every 5m", "", "refresh the docs", "high")

	s.fireDuePeriodics()
	time.Sleep(200 * time.Millisecond)

	runs, err := database.GetTasksForPeriodic(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 materialized task, got %d", len(runs))
	}
	if runs[0].Worktree {
		t.Errorf("expected materialized task to honour the worktree: false pin")
	}

	// The pin must not leak into the persisted project default.
	reloaded, err := database.GetProject(proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.DefaultWorktree {
		t.Errorf("periodic pin must not clobber the project default worktree")
	}
}

// Without a pin the fire still inherits the project default, so the pin is
// genuinely what changes the outcome above.
func TestScheduledFire_InlineWithoutPinInheritsProjectDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	s, database, proj := newSchedulerTestServer(t)

	writeProjectConfig(t, proj.Path, `workflows:
  - name: default
    steps:
      - name: impl
        prompt: "do it"
periodic:
  - name: docs-refresh
    cadence: "@every 5m"
    input: "refresh the docs"
    steps:
      - name: refresh
        prompt: "{{task.input}}"
`)

	d := duePeriodicDef(t, database, proj.ID, "docs-refresh", "@every 5m", "", "refresh the docs", "high")

	s.fireDuePeriodics()
	time.Sleep(200 * time.Millisecond)

	runs, err := database.GetTasksForPeriodic(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 materialized task, got %d", len(runs))
	}
	if runs[0].Worktree != proj.DefaultWorktree {
		t.Errorf("expected unpinned fire to inherit project default %v, got %v", proj.DefaultWorktree, runs[0].Worktree)
	}
}

// TestScheduledFire_RefMode verifies that a ref-mode periodic (workflow_ref set,
// no inline steps) materializes a task whose workflow is the referenced name
// verbatim — NOT the "periodic:<name>" hidden-workflow alias used for inline mode.
func TestScheduledFire_RefMode(t *testing.T) {
	s, database, proj := newSchedulerTestServer(t)

	d := duePeriodicDef(t, database, proj.ID, "nightly", "@every 5m", "default", "the input", "high")

	s.fireDuePeriodics()
	time.Sleep(200 * time.Millisecond)

	runs, err := database.GetTasksForPeriodic(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 materialized task, got %d", len(runs))
	}
	if got := runs[0].Workflow; got != "default" {
		t.Errorf("ref-mode: expected workflow %q, got %q", "default", got)
	}
}

// TestFireDuePeriodics_AtMostOnceAfterDowntime exercises the startup catch-up
// guarantee. checkPeriodicCatchup is just reconcile + fireDuePeriodics, and the
// "fire at most once after downtime" property is delivered entirely by
// ClaimPeriodicFire advancing next_fire_at to the next FUTURE slot. So a
// definition that fell due during downtime fires exactly one task; a subsequent
// pass sees a future next_fire_at and fires nothing.
func TestFireDuePeriodics_AtMostOnceAfterDowntime(t *testing.T) {
	s, database, proj := newSchedulerTestServer(t)

	d := duePeriodicDef(t, database, proj.ID, "nightly", "@every 5m", "", "the input", "high")

	// First pass: the definition is due (next_fire_at in the past) → fires once.
	s.fireDuePeriodics()
	time.Sleep(200 * time.Millisecond)

	// Second pass simulates a follow-up tick after catch-up. next_fire_at has
	// already advanced to the future, so nothing new should fire.
	s.fireDuePeriodics()
	time.Sleep(200 * time.Millisecond)

	runs, err := database.GetTasksForPeriodic(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("catch-up must fire at most once, got %d tasks", len(runs))
	}

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

	d := duePeriodicDef(t, database, proj.ID, "p", "@every 5m", "", "", "medium")

	// Simulate a still-running prior fire.
	prior, err := database.CreateTask(proj.ID, "prev", "d", "prev", "periodic:p", "", task.StatusRunning, nil)
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

	runs, err := database.GetTasksForPeriodic(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("skip-overlap: expected no new task (still 1), got %d", len(runs))
	}
	// Schedule still advanced past now despite skip.
	reloaded, _ := database.GetPeriodicByID(d.ID)
	if !reloaded.NextFireAt.After(time.Now()) {
		t.Errorf("expected schedule to advance even when skipped, got %v", reloaded.NextFireAt)
	}
}

func TestScheduledFire_PausedNeverFires(t *testing.T) {
	s, database, proj := newSchedulerTestServer(t)

	d := duePeriodicDef(t, database, proj.ID, "p", "@every 5m", "", "", "medium")
	if err := database.SetPeriodicPaused(d.ID, true); err != nil {
		t.Fatal(err)
	}

	s.fireDuePeriodics()
	time.Sleep(100 * time.Millisecond)

	runs, err := database.GetTasksForPeriodic(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("paused definition fired %d task(s)", len(runs))
	}
}
