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

// routineViewState holds the state for the routine-list and routine-runs
// views. These are a read + pause/run-now surface — routines are authored in
// .sakusen.yml, not here.
type routineViewState struct {
	defs   []daemon.PeriodicInfo
	cursor int

	loading bool

	// Runs sub-view (tasks created by the selected routine).
	selectedName string
	runs         []daemon.TaskInfo
	runsCursor   int
	runsLoading  bool
}

type routinesLoadedMsg []daemon.PeriodicInfo
type routineRunsLoadedMsg []daemon.TaskInfo

func (m Model) loadRoutines() tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		defs, err := m.client.ListRoutines(m.projectPath)
		if err != nil {
			return errorMsg(err)
		}
		return routinesLoadedMsg(defs)
	}
}

func (m Model) loadRoutineRuns(name string) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		tasks, err := m.client.ListRoutineRuns(m.projectPath, name)
		if err != nil {
			return errorMsg(err)
		}
		return routineRunsLoadedMsg(tasks)
	}
}

func (m Model) toggleRoutinePause(name string, paused bool) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		if _, err := m.client.SetRoutinePaused(m.projectPath, name, paused); err != nil {
			return errorMsg(err)
		}
		// Reload the list to reflect the new state.
		defs, err := m.client.ListRoutines(m.projectPath)
		if err != nil {
			return errorMsg(err)
		}
		return routinesLoadedMsg(defs)
	}
}

// runRoutineCmd fires a routine and reloads the routine list. The reload keeps
// the "P" view's last-fire column honest; from any other view the resulting
// message is inert.
func (m Model) runRoutineCmd(name, input string) tea.Cmd {
	return func() tea.Msg {
		if m.client == nil {
			return nil
		}
		if _, err := m.client.RunRoutine(m.projectPath, name, input); err != nil {
			return errorMsg(err)
		}
		defs, err := m.client.ListRoutines(m.projectPath)
		if err != nil {
			return errorMsg(err)
		}
		return routinesLoadedMsg(defs)
	}
}

func (m *routineViewState) selected() *daemon.PeriodicInfo {
	if m.cursor < 0 || m.cursor >= len(m.defs) {
		return nil
	}
	return &m.defs[m.cursor]
}

func (m Model) handleRoutineListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		if m.routines.cursor > 0 {
			m.routines.cursor--
		}
		return m, nil

	case key.Matches(msg, m.keys.Down):
		if m.routines.cursor < len(m.routines.defs)-1 {
			m.routines.cursor++
		}
		return m, nil

	case key.Matches(msg, m.keys.Enter):
		if r := m.routines.selected(); r != nil {
			m.routines.selectedName = r.Name
			m.routines.runsCursor = 0
			m.routines.runsLoading = true
			m.view = viewRoutineRuns
			return m, m.loadRoutineRuns(r.Name)
		}
		return m, nil
	}

	switch msg.String() {
	case " ":
		r := m.routines.selected()
		if r == nil {
			return m, nil
		}
		// On-demand routines have no clock to stop; the daemon rejects the
		// request, so say why here instead of surfacing that as an error.
		if r.Cadence == "" {
			m.statusMessage = fmt.Sprintf("routine %q is on-demand — nothing to pause", r.Name)
			return m, nil
		}
		return m, m.toggleRoutinePause(r.Name, !r.Paused)
	case "r":
		if r := m.routines.selected(); r != nil {
			return m.launchRoutine(r.Name)
		}
	}
	return m, nil
}

func (m Model) handleRoutineRunsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		m.quitting = true
		if m.client != nil {
			m.client.Close()
		}
		return m, tea.Quit

	case key.Matches(msg, m.keys.Back):
		m.view = viewRoutineList
		return m, m.loadRoutines()

	case key.Matches(msg, m.keys.Up):
		if m.routines.runsCursor > 0 {
			m.routines.runsCursor--
		}
		return m, nil

	case key.Matches(msg, m.keys.Down):
		if m.routines.runsCursor < len(m.routines.runs)-1 {
			m.routines.runsCursor++
		}
		return m, nil
	}
	return m, nil
}

func (m Model) renderRoutineList() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" Routines ") + "\n\n")

	if m.routines.loading {
		b.WriteString("  " + dimStyle.Render("loading…") + "\n")
		return b.String()
	}
	if len(m.routines.defs) == 0 {
		b.WriteString("  " + dimStyle.Render("No routines. Add them under the top-level routines: section of .sakusen.yml.") + "\n")
		b.WriteString("\n" + dimStyle.Render("  esc: back"))
		return b.String()
	}

	header := fmt.Sprintf("  %-20s %-16s %-16s %-16s %-10s", "NAME", "CADENCE", "WORKFLOW", "NEXT FIRE", "STATUS")
	b.WriteString(dimStyle.Render(header) + "\n")

	for i, r := range m.routines.defs {
		row := fmt.Sprintf("%-20s %-16s %-16s %-16s %-10s",
			truncate(r.Name, 20),
			truncate(routineCadenceLabel(r), 16),
			truncate(r.WorkflowRef, 16),
			routineNextFireLabel(r),
			routineStatusLabel(r),
		)
		if i == m.routines.cursor {
			b.WriteString(selectedStyle.Render("> "+row) + "\n")
		} else {
			b.WriteString("  " + row + "\n")
		}
	}

	b.WriteString("\n" + dimStyle.Render("  ↑/↓: navigate | enter: runs | space: pause/resume | r: run now | esc: back"))
	return b.String()
}

// routineCadenceLabel, routineNextFireLabel and routineStatusLabel render the
// on-demand case: no cadence means no schedule, so both time columns are "-".
func routineCadenceLabel(r daemon.PeriodicInfo) string {
	if r.Cadence == "" {
		return "-"
	}
	return r.Cadence
}

func routineNextFireLabel(r daemon.PeriodicInfo) string {
	if r.Cadence == "" {
		return fmtRoutineTime(nil)
	}
	return fmtRoutineTime(&r.NextFireAt)
}

func routineStatusLabel(r daemon.PeriodicInfo) string {
	switch {
	case r.Cadence == "":
		return "on-demand"
	case r.Paused:
		return "paused"
	default:
		return "active"
	}
}

func (m Model) renderRoutineRuns() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf(" Runs: %s ", m.routines.selectedName)) + "\n\n")

	if m.routines.runsLoading {
		b.WriteString("  " + dimStyle.Render("loading…") + "\n")
		return b.String()
	}
	if len(m.routines.runs) == 0 {
		b.WriteString("  " + dimStyle.Render("No runs yet for this routine.") + "\n")
		b.WriteString("\n" + dimStyle.Render("  esc: back"))
		return b.String()
	}

	header := fmt.Sprintf("  %-6s %-18s %-16s %s", "ID", "STATUS", "STEP", "TITLE")
	b.WriteString(dimStyle.Render(header) + "\n")

	for i, t := range m.routines.runs {
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
		if i == m.routines.runsCursor {
			b.WriteString(selectedStyle.Render("> "+row) + "\n")
		} else {
			b.WriteString("  " + row + "\n")
		}
	}

	b.WriteString("\n" + dimStyle.Render("  ↑/↓: navigate | esc: back"))
	return b.String()
}

func fmtRoutineTime(t *time.Time) string {
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
