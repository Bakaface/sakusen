package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/charmbracelet/lipgloss"
)

func TestRenderRoutineList_Structure(t *testing.T) {
	now := time.Now()
	last := now.Add(-time.Hour)
	m := Model{
		width:  120,
		height: 40,
		keys:   newKeyMap(),
		routines: routineViewState{
			defs: []daemon.PeriodicInfo{
				{ID: 1, Name: "nightly-digest", Cadence: "0 3 * * *", WorkflowRef: "default", NextFireAt: now, LastFiredAt: &last},
				{ID: 2, Name: "heartbeat", Cadence: "@every 5m", WorkflowRef: "ping", NextFireAt: now, Paused: true},
				{ID: 3, Name: "compose-wiki", WorkflowRef: "wiki-compose", NextFireAt: now},
			},
			cursor: 1,
		},
	}

	out := m.renderRoutineList()
	if !strings.Contains(out, "Routines") {
		t.Error("missing title")
	}
	for _, name := range []string{"nightly-digest", "heartbeat", "compose-wiki"} {
		if !strings.Contains(out, name) {
			t.Errorf("missing row for %q", name)
		}
	}
	if !strings.Contains(out, "paused") {
		t.Error("missing paused status")
	}
	if !strings.Contains(out, "on-demand") {
		t.Error("a cadence-less routine should show the on-demand status")
	}
	// The cursor row (heartbeat) should be highlighted with the selection marker.
	if !strings.Contains(out, "> ") {
		t.Error("expected a selected-row marker")
	}
	// No rendered line should overflow the terminal width.
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > m.width {
			t.Errorf("line exceeds width %d: %d (%q)", m.width, w, line)
		}
	}
}

// An on-demand routine has no schedule, so both time columns read "-" while the
// row keeps the same shape as a scheduled one.
func TestRenderRoutineList_OnDemandColumns(t *testing.T) {
	m := Model{
		width:  120,
		height: 40,
		keys:   newKeyMap(),
		routines: routineViewState{
			defs: []daemon.PeriodicInfo{
				{ID: 1, Name: "compose-wiki", WorkflowRef: "wiki-compose", NextFireAt: time.Now()},
			},
		},
	}
	out := m.renderRoutineList()
	row := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "compose-wiki") {
			row = line
		}
	}
	if row == "" {
		t.Fatalf("routine row not rendered:\n%s", out)
	}
	if strings.Count(row, "-") < 2 {
		t.Errorf("expected cadence and next-fire to render as \"-\", got %q", row)
	}
	if strings.Contains(row, time.Now().Format("15:04")) {
		t.Errorf("on-demand routine must not show a next fire time, got %q", row)
	}
}

func TestRenderRoutineList_Empty(t *testing.T) {
	m := Model{width: 80, height: 24, keys: newKeyMap()}
	out := m.renderRoutineList()
	if !strings.Contains(out, "No routines") || !strings.Contains(out, "routines:") {
		t.Errorf("expected empty-state message naming routines:, got: %q", out)
	}
}

func TestRenderRoutineRuns_Structure(t *testing.T) {
	m := Model{
		width:  100,
		height: 30,
		keys:   newKeyMap(),
		routines: routineViewState{
			selectedName: "nightly-digest",
			runs: []daemon.TaskInfo{
				{ID: 10, Status: "completed", CurrentStep: "impl", Title: "nightly-digest @ 2026"},
				{ID: 11, Status: "running", Title: "nightly-digest @ 2026"},
			},
			runsCursor: 0,
		},
	}
	out := m.renderRoutineRuns()
	if !strings.Contains(out, "Runs: nightly-digest") {
		t.Error("missing runs title")
	}
	if !strings.Contains(out, "#10") || !strings.Contains(out, "#11") {
		t.Error("missing run rows")
	}
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > m.width {
			t.Errorf("line exceeds width %d: %d (%q)", m.width, w, line)
		}
	}
}
