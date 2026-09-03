package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/charmbracelet/lipgloss"
)

func TestRenderPeriodicList_Structure(t *testing.T) {
	now := time.Now()
	last := now.Add(-time.Hour)
	m := Model{
		width:  120,
		height: 40,
		keys:   newKeyMap(),
		periodic: periodicViewState{
			defs: []daemon.PeriodicInfo{
				{ID: 1, Name: "nightly-digest", Cadence: "0 3 * * *", WorkflowRef: "default", NextFireAt: now, LastFiredAt: &last},
				{ID: 2, Name: "heartbeat", Cadence: "@every 5m", Inline: true, NextFireAt: now, Paused: true},
			},
			cursor: 1,
		},
	}

	out := m.renderPeriodicList()
	if !strings.Contains(out, "Periodic Definitions") {
		t.Error("missing title")
	}
	if !strings.Contains(out, "nightly-digest") || !strings.Contains(out, "heartbeat") {
		t.Error("missing definition rows")
	}
	if !strings.Contains(out, "paused") {
		t.Error("missing paused status")
	}
	if !strings.Contains(out, "(inline)") {
		t.Error("inline definition should show (inline) workflow label")
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

func TestRenderPeriodicList_Empty(t *testing.T) {
	m := Model{width: 80, height: 24, keys: newKeyMap()}
	out := m.renderPeriodicList()
	if !strings.Contains(out, "No periodic definitions") {
		t.Errorf("expected empty-state message, got: %q", out)
	}
}

func TestRenderPeriodicRuns_Structure(t *testing.T) {
	m := Model{
		width:  100,
		height: 30,
		keys:   newKeyMap(),
		periodic: periodicViewState{
			selectedName: "nightly-digest",
			runs: []daemon.TaskInfo{
				{ID: 10, Status: "completed", CurrentStep: "impl", Title: "nightly-digest @ 2026"},
				{ID: 11, Status: "running", Title: "nightly-digest @ 2026"},
			},
			runsCursor: 0,
		},
	}
	out := m.renderPeriodicRuns()
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
