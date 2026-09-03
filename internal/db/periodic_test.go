package db

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/task"
)

func newPeriodicTestDB(t *testing.T) (*DB, int64) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	proj, err := database.GetOrCreateProject("/tmp/periodic-proj")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return database, proj.ID
}

func TestUpsertPeriodicDef_InsertAndUpdate(t *testing.T) {
	database, projID := newPeriodicTestDB(t)
	next := time.Now().Add(time.Hour).Truncate(time.Second)

	d, err := database.UpsertPeriodicDef(projID, "nightly", "0 3 * * *", "default", "do it", "high", false, next)
	if err != nil {
		t.Fatalf("upsert insert: %v", err)
	}
	if d.ID == 0 || d.Name != "nightly" || d.WorkflowRef != "default" || d.Priority != "high" {
		t.Fatalf("unexpected insert result: %+v", d)
	}
	firstFire := d.NextFireAt

	// Update without cadence change: next_fire_at must be preserved.
	later := time.Now().Add(10 * time.Hour)
	d2, err := database.UpsertPeriodicDef(projID, "nightly", "0 3 * * *", "default", "changed", "low", false, later)
	if err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	if !d2.NextFireAt.Equal(firstFire) {
		t.Errorf("next_fire_at changed without cadence change: was %v now %v", firstFire, d2.NextFireAt)
	}
	if d2.Input != "changed" || d2.Priority != "low" {
		t.Errorf("update did not refresh fields: %+v", d2)
	}

	// Update WITH cadence change: next_fire_at recomputed to the new value.
	newFire := time.Now().Add(20 * time.Hour).Truncate(time.Second)
	d3, err := database.UpsertPeriodicDef(projID, "nightly", "0 4 * * *", "default", "changed", "low", false, newFire)
	if err != nil {
		t.Fatalf("upsert cadence change: %v", err)
	}
	if !d3.NextFireAt.Equal(newFire) {
		t.Errorf("next_fire_at not recomputed on cadence change: %v", d3.NextFireAt)
	}
}

func TestUpsertPeriodicDef_PreservesInteractivePause(t *testing.T) {
	database, projID := newPeriodicTestDB(t)
	next := time.Now().Add(time.Hour)

	if _, err := database.UpsertPeriodicDef(projID, "p", "@every 5m", "default", "", "medium", false, next); err != nil {
		t.Fatal(err)
	}
	d, _ := database.GetPeriodicByName(projID, "p")
	if err := database.SetPeriodicPaused(d.ID, true); err != nil {
		t.Fatal(err)
	}
	// Reconcile again with paused=false in yml; interactive pause must win.
	if _, err := database.UpsertPeriodicDef(projID, "p", "@every 5m", "default", "", "medium", false, next); err != nil {
		t.Fatal(err)
	}
	d2, _ := database.GetPeriodicByName(projID, "p")
	if !d2.Paused {
		t.Error("interactive pause was clobbered by reconcile")
	}
}

func TestUpsertPeriodicDef_YAMLPausedToggleHonored(t *testing.T) {
	database, projID := newPeriodicTestDB(t)
	next := time.Now().Add(time.Hour)

	// Initial reconcile, not paused in yml.
	if _, err := database.UpsertPeriodicDef(projID, "p", "@every 5m", "default", "", "medium", false, next); err != nil {
		t.Fatal(err)
	}

	// yml now sets paused: true → runtime flag must follow.
	if _, err := database.UpsertPeriodicDef(projID, "p", "@every 5m", "default", "", "medium", true, next); err != nil {
		t.Fatal(err)
	}
	d, _ := database.GetPeriodicByName(projID, "p")
	if !d.Paused {
		t.Fatal("yml paused:true was not applied on reconcile")
	}

	// yml flips back to paused: false → runtime flag must resume.
	if _, err := database.UpsertPeriodicDef(projID, "p", "@every 5m", "default", "", "medium", false, next); err != nil {
		t.Fatal(err)
	}
	d2, _ := database.GetPeriodicByName(projID, "p")
	if d2.Paused {
		t.Error("yml paused:false (resume) was not applied on reconcile")
	}
}

func TestClaimPeriodicFire_Atomic(t *testing.T) {
	database, projID := newPeriodicTestDB(t)
	past := time.Now().Add(-time.Minute)
	d, err := database.UpsertPeriodicDef(projID, "p", "@every 5m", "default", "", "medium", false, past)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	newNext := now.Add(5 * time.Minute)
	claimed, err := database.ClaimPeriodicFire(d.ID, now, newNext)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("expected first claim to succeed")
	}
	// Second claim with the same `now` must fail: next_fire_at is now in the future.
	claimed2, err := database.ClaimPeriodicFire(d.ID, now, newNext)
	if err != nil {
		t.Fatal(err)
	}
	if claimed2 {
		t.Fatal("expected second claim to fail (already advanced)")
	}
}

func TestSoftDeletePreservesRuns(t *testing.T) {
	database, projID := newPeriodicTestDB(t)
	next := time.Now().Add(time.Hour)
	d, err := database.UpsertPeriodicDef(projID, "p", "@every 5m", "default", "", "medium", false, next)
	if err != nil {
		t.Fatal(err)
	}

	tk, err := database.CreateTask(projID, "run", "desc", "run", "default", "", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetTaskPeriodicID(tk.ID, d.ID); err != nil {
		t.Fatal(err)
	}

	if err := database.SoftDeletePeriodicDef(projID, "p"); err != nil {
		t.Fatal(err)
	}
	// Definition no longer appears in active listing.
	active, err := database.ListPeriodicsForProject(projID)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Errorf("expected 0 active definitions, got %d", len(active))
	}
	// But its run history survives.
	runs, err := database.GetTasksForPeriodic(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != tk.ID {
		t.Errorf("expected 1 run preserved, got %+v", runs)
	}
}

func TestTaskPeriodicIDRoundTrip(t *testing.T) {
	database, projID := newPeriodicTestDB(t)
	next := time.Now().Add(time.Hour)
	d, _ := database.UpsertPeriodicDef(projID, "p", "@every 5m", "default", "", "medium", false, next)

	tk, err := database.CreateTask(projID, "t", "d", "t", "default", "", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tk.PeriodicID != nil {
		t.Fatal("new task should have nil PeriodicID")
	}
	if err := database.SetTaskPeriodicID(tk.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	reloaded, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PeriodicID == nil || *reloaded.PeriodicID != d.ID {
		t.Errorf("expected PeriodicID %d, got %v", d.ID, reloaded.PeriodicID)
	}
}

func TestListDuePeriodics(t *testing.T) {
	database, projID := newPeriodicTestDB(t)
	now := time.Now()

	// Due (past), active.
	database.UpsertPeriodicDef(projID, "due", "@every 5m", "default", "", "medium", false, now.Add(-time.Minute))
	// Not due (future).
	database.UpsertPeriodicDef(projID, "future", "@every 5m", "default", "", "medium", false, now.Add(time.Hour))
	// Due but paused.
	dp, _ := database.UpsertPeriodicDef(projID, "paused", "@every 5m", "default", "", "medium", false, now.Add(-time.Minute))
	database.SetPeriodicPaused(dp.ID, true)

	due, err := database.ListDuePeriodics(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Name != "due" {
		t.Fatalf("expected only 'due', got %+v", due)
	}
}
