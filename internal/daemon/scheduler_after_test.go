package daemon

import (
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
)

// afterFixture registers upstream "sync" (cadence, never due) and downstream
// "backfill" (after: sync), both running the project's "default" workflow.
func afterFixture(t *testing.T) (*Server, *db.DB, *db.Project, *db.PeriodicDef, *db.PeriodicDef) {
	t.Helper()
	s, database, proj := newSchedulerTestServer(t)
	future := time.Now().Add(time.Hour)
	up, err := database.UpsertPeriodicDef(proj.ID, "sync", "@every 1h", "", "default", "", "", false, future)
	if err != nil {
		t.Fatal(err)
	}
	down, err := database.UpsertPeriodicDef(proj.ID, "backfill", "", "sync", "default", "", "", false, future)
	if err != nil {
		t.Fatal(err)
	}
	return s, database, proj, up, down
}

// upstreamRun records a new upstream run in the given status.
func upstreamRun(t *testing.T, database *db.DB, proj *db.Project, up *db.PeriodicDef, status task.Status) *task.Task {
	t.Helper()
	run, err := database.CreateTask(proj.ID, "sync run", "", "sync-run", "default", "", status, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetTaskPeriodicID(run.ID, up.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdatePeriodicLastTask(up.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	return run
}

func tickAfter(s *Server) {
	s.fireAfterPeriodics()
	// Let the async title-finalize goroutine settle before assertions/teardown.
	time.Sleep(150 * time.Millisecond)
}

func runCount(t *testing.T, database *db.DB, d *db.PeriodicDef) int {
	t.Helper()
	runs, err := database.GetTasksForPeriodic(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	return len(runs)
}

func TestAfterFire_UpstreamCompletedFiresOnce(t *testing.T) {
	s, database, proj, up, down := afterFixture(t)
	upstreamRun(t, database, proj, up, task.StatusCompleted)

	tickAfter(s)
	tickAfter(s)
	if n := runCount(t, database, down); n != 1 {
		t.Fatalf("expected exactly 1 downstream run, got %d", n)
	}
}

func TestAfterFire_WaitsForRunningUpstream(t *testing.T) {
	s, database, proj, up, down := afterFixture(t)
	run := upstreamRun(t, database, proj, up, task.StatusRunning)

	tickAfter(s)
	if n := runCount(t, database, down); n != 0 {
		t.Fatalf("fired while upstream running: %d runs", n)
	}
	if err := database.UpdateTaskStatus(run.ID, task.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	tickAfter(s)
	if n := runCount(t, database, down); n != 1 {
		t.Fatalf("expected 1 run after upstream completed, got %d", n)
	}
}

func TestAfterFire_FailedUpstreamConsumedWithoutFiring(t *testing.T) {
	s, database, proj, up, down := afterFixture(t)
	failed := upstreamRun(t, database, proj, up, task.StatusFailed)

	tickAfter(s)
	if n := runCount(t, database, down); n != 0 {
		t.Fatalf("fired on failed upstream: %d runs", n)
	}
	got, _ := database.GetPeriodicByID(down.ID)
	if got.AfterLastTaskID == nil || *got.AfterLastTaskID != failed.ID {
		t.Fatalf("failed run not consumed: %v", got.AfterLastTaskID)
	}

	upstreamRun(t, database, proj, up, task.StatusCompleted)
	tickAfter(s)
	if n := runCount(t, database, down); n != 1 {
		t.Fatalf("next completed upstream run should fire, got %d runs", n)
	}
}

func TestAfterFire_SkipOverlap(t *testing.T) {
	s, database, proj, up, down := afterFixture(t)
	prior, err := database.CreateTask(proj.ID, "prev", "", "prev", "default", "", task.StatusRunning, nil)
	if err != nil {
		t.Fatal(err)
	}
	database.SetTaskPeriodicID(prior.ID, down.ID)
	database.UpdatePeriodicLastTask(down.ID, prior.ID)
	run := upstreamRun(t, database, proj, up, task.StatusCompleted)

	tickAfter(s)
	if n := runCount(t, database, down); n != 1 {
		t.Fatalf("skip-overlap: expected only the prior run, got %d", n)
	}
	got, _ := database.GetPeriodicByID(down.ID)
	if got.AfterLastTaskID == nil || *got.AfterLastTaskID != run.ID {
		t.Fatalf("skipped upstream run not consumed: %v", got.AfterLastTaskID)
	}
}

func TestAfterFire_PausedNeverFires(t *testing.T) {
	s, database, proj, up, down := afterFixture(t)
	if err := database.SetPeriodicPaused(down.ID, true); err != nil {
		t.Fatal(err)
	}
	upstreamRun(t, database, proj, up, task.StatusCompleted)

	tickAfter(s)
	if n := runCount(t, database, down); n != 0 {
		t.Fatalf("paused downstream fired: %d runs", n)
	}

	// Unpausing does not replay the completion that happened while paused.
	if err := database.SetPeriodicPaused(down.ID, false); err != nil {
		t.Fatal(err)
	}
	tickAfter(s)
	if n := runCount(t, database, down); n != 0 {
		t.Fatalf("unpause replayed an upstream completion: %d runs", n)
	}
}

func TestAfterFire_UpstreamRunNowTriggers(t *testing.T) {
	s, database, _, up, down := afterFixture(t)

	run, err := s.materializePeriodicTask(up, time.Now())
	if err != nil {
		t.Fatalf("run-now: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if ok, err := database.ClaimTask(run.ID); err != nil || !ok {
		t.Fatalf("claim run: ok=%v err=%v", ok, err)
	}
	if err := database.UpdateTaskStatus(run.ID, task.StatusCompleted); err != nil {
		t.Fatal(err)
	}

	tickAfter(s)
	if n := runCount(t, database, down); n != 1 {
		t.Fatalf("expected upstream run-now to trigger downstream, got %d runs", n)
	}
}

func TestReconcilePeriodics_AfterEntry(t *testing.T) {
	s, database, proj := newSchedulerTestServer(t)

	cfg := &config.Config{
		Periodic: []config.PeriodicEntry{
			{Name: "sync", Cadence: "@every 1h", Workflow: "default"},
			{Name: "backfill", After: "sync", Workflow: "default"},
		},
	}
	if err := s.reconcilePeriodicsForProject(proj, cfg); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	d, err := database.GetPeriodicByName(proj.ID, "backfill")
	if err != nil {
		t.Fatal(err)
	}
	if d.AfterName != "sync" || d.Cadence != "" {
		t.Errorf("after=%q cadence=%q", d.AfterName, d.Cadence)
	}
	due, err := database.ListDuePeriodics(time.Now().Add(24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range due {
		if p.Name == "backfill" {
			t.Error("after: entry returned by ListDuePeriodics")
		}
	}
}
