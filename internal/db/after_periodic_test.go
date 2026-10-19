package db

import (
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/task"
)

func TestUpsertPeriodicDef_AfterSeedsConsumedRun(t *testing.T) {
	database, projID := newPeriodicTestDB(t)
	now := time.Now()

	up, err := database.UpsertPeriodicDef(projID, "sync", "@every 5m", "", "default", "", "", false, now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	run, _ := database.CreateTask(projID, "run", "", "run", "default", "", task.StatusCompleted, nil)
	if err := database.UpdatePeriodicLastTask(up.ID, run.ID); err != nil {
		t.Fatal(err)
	}

	d, err := database.UpsertPeriodicDef(projID, "backfill", "", "sync", "default", "", "", false, now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if d.AfterName != "sync" || d.Cadence != "" {
		t.Errorf("after=%q cadence=%q", d.AfterName, d.Cadence)
	}
	if d.AfterLastTaskID == nil || *d.AfterLastTaskID != run.ID {
		t.Errorf("AfterLastTaskID = %v, want %d", d.AfterLastTaskID, run.ID)
	}

	due, err := database.ListDuePeriodics(now)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range due {
		if p.Name == "backfill" {
			t.Error("after: definition returned by ListDuePeriodics")
		}
	}
	after, err := database.ListAfterPeriodics()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].Name != "backfill" {
		t.Errorf("ListAfterPeriodics = %v", after)
	}
}

func TestClaimAfterFire_Atomic(t *testing.T) {
	database, projID := newPeriodicTestDB(t)
	now := time.Now()
	database.UpsertPeriodicDef(projID, "sync", "@every 5m", "", "default", "", "", false, now)
	d, _ := database.UpsertPeriodicDef(projID, "backfill", "", "sync", "default", "", "", false, now)
	run, _ := database.CreateTask(projID, "run", "", "run", "default", "", task.StatusCompleted, nil)

	ok, err := database.ClaimAfterFire(d.ID, d.AfterLastTaskID, run.ID, true, now)
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	ok, err = database.ClaimAfterFire(d.ID, d.AfterLastTaskID, run.ID, true, now)
	if err != nil || ok {
		t.Fatalf("stale claim: ok=%v err=%v, want false", ok, err)
	}
	got, _ := database.GetPeriodicByID(d.ID)
	if got.AfterLastTaskID == nil || *got.AfterLastTaskID != run.ID || got.LastFiredAt == nil {
		t.Errorf("after claim: consumed=%v lastFired=%v", got.AfterLastTaskID, got.LastFiredAt)
	}
}

func TestSetPeriodicPaused_UnpauseSkipsFinishedUpstreamRun(t *testing.T) {
	database, projID := newPeriodicTestDB(t)
	now := time.Now()
	up, _ := database.UpsertPeriodicDef(projID, "sync", "@every 5m", "", "default", "", "", false, now)
	d, _ := database.UpsertPeriodicDef(projID, "backfill", "", "sync", "default", "", "", false, now)
	if err := database.SetPeriodicPaused(d.ID, true); err != nil {
		t.Fatal(err)
	}

	running, _ := database.CreateTask(projID, "r", "", "r", "default", "", task.StatusRunning, nil)
	database.UpdatePeriodicLastTask(up.ID, running.ID)
	if err := database.SetPeriodicPaused(d.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := database.GetPeriodicByID(d.ID); got.AfterLastTaskID != nil {
		t.Errorf("unpause consumed a still-running upstream run: %v", *got.AfterLastTaskID)
	}

	database.SetPeriodicPaused(d.ID, true)
	done, _ := database.CreateTask(projID, "c", "", "c", "default", "", task.StatusCompleted, nil)
	database.UpdatePeriodicLastTask(up.ID, done.ID)
	if err := database.SetPeriodicPaused(d.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := database.GetPeriodicByID(d.ID); got.AfterLastTaskID == nil || *got.AfterLastTaskID != done.ID {
		t.Errorf("unpause did not consume run completed while paused: %v", got.AfterLastTaskID)
	}
}

func TestUpsertPeriodicDef_YAMLUnpauseSkipsFinishedUpstreamRun(t *testing.T) {
	database, projID := newPeriodicTestDB(t)
	now := time.Now()
	up, _ := database.UpsertPeriodicDef(projID, "sync", "@every 5m", "", "default", "", "", false, now)
	if _, err := database.UpsertPeriodicDef(projID, "backfill", "", "sync", "default", "", "", true, now); err != nil {
		t.Fatal(err)
	}

	done, _ := database.CreateTask(projID, "c", "", "c", "default", "", task.StatusCompleted, nil)
	database.UpdatePeriodicLastTask(up.ID, done.ID)
	d, err := database.UpsertPeriodicDef(projID, "backfill", "", "sync", "default", "", "", false, now)
	if err != nil {
		t.Fatal(err)
	}
	if d.Paused {
		t.Fatal("yml paused: false did not resume the definition")
	}
	if d.AfterLastTaskID == nil || *d.AfterLastTaskID != done.ID {
		t.Errorf("yml unpause did not consume run completed while paused: %v", d.AfterLastTaskID)
	}
}
