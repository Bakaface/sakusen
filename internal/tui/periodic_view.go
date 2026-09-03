package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// periodicViewState holds the state for the periodic-list and periodic-runs
// views. These are a read + pause/run-now surface — definitions are authored in
// .sakusen.yml, not here.
type periodicViewState struct {
	defs   []daemon.PeriodicInfo
	cursor int

	loading bool

	// Runs sub-view (tasks materialized by the selected definition).
	selectedID   int64
	selectedName string
	runs         []daemon.TaskInfo
	runsCursor   int
	runsLoading  bool
}

type periodicsLoadedMsg []daemon.PeriodicInfo
type periodicRunsLoadedMsg []daemon.TaskInfo

func (m Model) loadPeriodics() tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		defs, err := m.client.ListPeriodics(m.projectPath)
		if err != nil {
			return errorMsg(err)
		}
		return periodicsLoadedMsg(defs)
	}
}

func (m Model) loadPeriodicRuns(periodicID int64) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		tasks, err := m.client.ListPeriodicRuns(periodicID)
		if err != nil {
			return errorMsg(err)
		}
		return periodicRunsLoadedMsg(tasks)
	}
}

func (m Model) togglePeriodicPause(id int64, paused bool) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		if _, err := m.client.SetPeriodicPaused(id, paused); err != nil {
			return errorMsg(err)
		}
		// Reload the list to reflect the new state.
		defs, err := m.client.ListPeriodics(m.projectPath)
		if err != nil {
			return errorMsg(err)
		}
		return periodicsLoadedMsg(defs)
	}
}

func (m Model) firePeriodicNow(id int64) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		if _, err := m.client.FirePeriodicNow(id); err != nil {
			return errorMsg(err)
		}
		defs, err := m.client.ListPeriodics(m.projectPath)
		if err != nil {
			return errorMsg(err)
		}
		return periodicsLoadedMsg(defs)
	}
}

func (m *periodicViewState) selected() *daemon.PeriodicInfo {
	if m.cursor < 0 || m.cursor >= len(m.defs) {
		return nil
	}
	return &m.defs[m.cursor]
}

func (m Model) handlePeriodicListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		m.quitting = true
		if m.client != nil {
			m.client.Close()
		}
		return m, tea.Quit

	case key.Matches(msg, m.keys.Back):
		m.view = viewList
		return m, nil

	case key.Matches(msg, m.keys.Up):
		if m.periodic.cursor > 0 {
			m.periodic.cursor--
		}
		return m, nil

	case key.Matches(msg, m.keys.Down):
		if m.periodic.cursor < len(m.periodic.defs)-1 {
			m.periodic.cursor++
		}
		return m, nil

	case key.Matches(msg, m.keys.Enter):
		if p := m.periodic.selected(); p != nil {
			m.periodic.selectedID = p.ID
			m.periodic.selectedName = p.Name
			m.periodic.runsCursor = 0
			m.periodic.runsLoading = true
			m.view = viewPeriodicRuns
			return m, m.loadPeriodicRuns(p.ID)
		}
		return m, nil
	}

	switch msg.String() {
	case " ":
		if p := m.periodic.selected(); p != nil {
			return m, m.togglePeriodicPause(p.ID, !p.Paused)
		}
	case "r":
		if p := m.periodic.selected(); p != nil {
			m.statusMessage = fmt.Sprintf("fired periodic %q", p.Name)
			return m, m.firePeriodicNow(p.ID)
		}
	}
	return m, nil
}

func (m Model) handlePeriodicRunsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		m.quitting = true
		if m.client != nil {
			m.client.Close()
		}
		return m, tea.Quit

	case key.Matches(msg, m.keys.Back):
		m.view = viewPeriodicList
		return m, m.loadPeriodics()

	case key.Matches(msg, m.keys.Up):
		if m.periodic.runsCursor > 0 {
			m.periodic.runsCursor--
		}
		return m, nil

	case key.Matches(msg, m.keys.Down):
		if m.periodic.runsCursor < len(m.periodic.runs)-1 {
			m.periodic.runsCursor++
		}
		return m, nil
	}
	return m, nil
}

func (m Model) renderPeriodicList() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" Periodic Definitions ") + "\n\n")

	if m.periodic.loading {
		b.WriteString("  " + dimStyle.Render("loading…") + "\n")
		return b.String()
	}
	if len(m.periodic.defs) == 0 {
		b.WriteString("  " + dimStyle.Render("No periodic definitions. Add them under the top-level periodic: section of .sakusen.yml.") + "\n")
		b.WriteString("\n" + dimStyle.Render("  esc: back"))
		return b.String()
	}

	header := fmt.Sprintf("  %-20s %-16s %-16s %-16s %-8s", "NAME", "CADENCE", "WORKFLOW", "NEXT FIRE", "STATUS")
	b.WriteString(dimStyle.Render(header) + "\n")

	for i, p := range m.periodic.defs {
		wf := "(inline)"
		if !p.Inline && p.WorkflowRef != "" {
			wf = p.WorkflowRef
		}
		status := "active"
		if p.Paused {
			status = "paused"
		}
		row := fmt.Sprintf("%-20s %-16s %-16s %-16s %-8s",
			truncate(p.Name, 20),
			truncate(p.Cadence, 16),
			truncate(wf, 16),
			fmtPeriodicTime(&p.NextFireAt),
			status,
		)
		if i == m.periodic.cursor {
			b.WriteString(selectedStyle.Render("> "+row) + "\n")
		} else {
			b.WriteString("  " + row + "\n")
		}
	}

	b.WriteString("\n" + dimStyle.Render("  ↑/↓: navigate | enter: runs | space: pause/resume | r: run now | esc: back"))
	return b.String()
}

func (m Model) renderPeriodicRuns() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf(" Runs: %s ", m.periodic.selectedName)) + "\n\n")

	if m.periodic.runsLoading {
		b.WriteString("  " + dimStyle.Render("loading…") + "\n")
		return b.String()
	}
	if len(m.periodic.runs) == 0 {
		b.WriteString("  " + dimStyle.Render("No runs yet for this definition.") + "\n")
		b.WriteString("\n" + dimStyle.Render("  esc: back"))
		return b.String()
	}

	header := fmt.Sprintf("  %-6s %-18s %-16s %s", "ID", "STATUS", "STEP", "TITLE")
	b.WriteString(dimStyle.Render(header) + "\n")

	for i, t := range m.periodic.runs {
		step := t.CurrentStep
		if step == "" {
			step = "-"
		}
		title := t.Title
		if title == "" {
			title = truncate(t.Input, 40)
		}
		row := fmt.Sprintf("%-6s %-18s %-16s %s",
			fmt.Sprintf("#%d", t.ID),
			truncate(t.Status, 18),
			truncate(step, 16),
			truncate(title, 40),
		)
		if i == m.periodic.runsCursor {
			b.WriteString(selectedStyle.Render("> "+row) + "\n")
		} else {
			b.WriteString("  " + row + "\n")
		}
	}

	b.WriteString("\n" + dimStyle.Render("  ↑/↓: navigate | esc: back"))
	return b.String()
}

func fmtPeriodicTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.Local().Format("01-02 15:04")
}

// truncate shortens s to at most n runes, appending an ellipsis when cut.
// It always slices on rune boundaries, never bytes.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return string(r[:1])
	}
	return string(r[:n-1]) + "…"
}
