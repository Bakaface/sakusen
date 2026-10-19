package main

import (
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/daemon"
)

func TestPeriodicLabels_AfterEntry(t *testing.T) {
	next := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	after := daemon.PeriodicInfo{Name: "intrum-log-backfill", After: "intrum-sync", NextFireAt: next}
	if got := periodicCadenceLabel(after); got != "-" {
		t.Errorf("cadence label = %q, want -", got)
	}
	if got := periodicNextFireLabel(after); got != "after intrum-sync" {
		t.Errorf("next-fire label = %q, want %q", got, "after intrum-sync")
	}

	cron := daemon.PeriodicInfo{Name: "intrum-sync", Cadence: "0 0 * * *", NextFireAt: next}
	if got := periodicCadenceLabel(cron); got != "0 0 * * *" {
		t.Errorf("cadence label = %q", got)
	}
	if got, want := periodicNextFireLabel(cron), fmtTime(&next); got != want {
		t.Errorf("next-fire label = %q, want %q", got, want)
	}
}
