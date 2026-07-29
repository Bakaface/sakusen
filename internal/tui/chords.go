package tui

import (
	"fmt"

	"github.com/Bakaface/sortie/internal/daemon"
	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
)

// chordEntry defines the handler for a completed two-key chord sequence.
type chordEntry struct {
	exec func(m Model) (tea.Model, tea.Cmd)
}

// chordRegistry maps view -> sequence -> handler.
// A chord is a two-key sequence like "dd", "gg", "oa".
var chordRegistry map[view]map[string]chordEntry

// chordPrefixes tracks which single keys start a chord in each view.
// Built at init from chordRegistry.
var chordPrefixes map[view]map[string]bool

func init() {
	chordRegistry = map[view]map[string]chordEntry{
		viewList: {
			"dd": {exec: chordDeleteTask},
			"gg": {exec: chordListGotoTop},
			"oa": {exec: chordOpenArtifact},
			"ea": {exec: chordEditArtifact},
			"ed": {exec: chordEditDesc},
			"et": {exec: chordEditTitle},
			"ec": {exec: chordEditContext},
		},
		viewTaskInfo: {
			"gg": {exec: chordTaskInfoGotoTop},
			"oa": {exec: chordOpenArtifact},
			"ea": {exec: chordEditArtifact},
			"ed": {exec: chordEditDesc},
			"et": {exec: chordEditTitle},
			"ec": {exec: chordEditContext},
			"yd": {exec: chordYankDesc},
			"yc": {exec: chordYankContext},
		},
	}

	// Build prefix sets from registry keys.
	chordPrefixes = make(map[view]map[string]bool, len(chordRegistry))
	for v, seqs := range chordRegistry {
		chordPrefixes[v] = make(map[string]bool)
		for seq := range seqs {
			chordPrefixes[v][seq[:1]] = true
		}
	}
}

// tryChord attempts to handle a keypress as part of a chord sequence.
// Returns (model, cmd, true) if the key was consumed (either as a chord prefix
// or a completed/unmatched second key). Returns (zero, nil, false) if the key
// should be handled normally by the caller.
func (m Model) tryChord(keyStr string) (tea.Model, tea.Cmd, bool) {
	// If we have a pending first key, try to complete the chord.
	if m.pendingChord != "" {
		seq := m.pendingChord + keyStr
		m.pendingChord = ""
		if viewChords, ok := chordRegistry[m.view]; ok {
			if entry, ok := viewChords[seq]; ok {
				ret, cmd := entry.exec(m)
				return ret, cmd, true
			}
		}
		// No matching chord — consume the key silently.
		return m, nil, true
	}

	// Check if this key starts any chord in the current view.
	if prefixes, ok := chordPrefixes[m.view]; ok && prefixes[keyStr] {
		m.pendingChord = keyStr
		return m, nil, true
	}

	return m, nil, false
}

// --- Chord handlers ---

// selectedTask returns the task relevant to the current view (list or taskInfo).
func (m *Model) selectedTask() *daemon.TaskInfo {
	switch m.view {
	case viewList:
		return m.list.Selected()
	case viewTaskInfo:
		return m.taskInfo.task
	default:
		return nil
	}
}

func chordDeleteTask(m Model) (tea.Model, tea.Cmd) {
	if task := m.list.Selected(); task != nil && m.client != nil {
		m.confirmAction = "delete"
		m.confirmTaskID = task.ID
	}
	return m, nil
}

func chordListGotoTop(m Model) (tea.Model, tea.Cmd) {
	m.list.GotoTop()
	return m, nil
}

func chordTaskInfoGotoTop(m Model) (tea.Model, tea.Cmd) {
	m.taskInfo.GotoTop()
	return m, nil
}

func chordOpenArtifact(m Model) (tea.Model, tea.Cmd) {
	if task := m.selectedTask(); task != nil {
		return m.openArtifactSelection(task, "view")
	}
	return m, nil
}

func chordEditArtifact(m Model) (tea.Model, tea.Cmd) {
	if task := m.selectedTask(); task != nil {
		return m.openArtifactSelection(task, "edit")
	}
	return m, nil
}

func chordEditDesc(m Model) (tea.Model, tea.Cmd) {
	if task := m.selectedTask(); task != nil {
		return m, m.openEditorForField(task.ID, "input", task.Input)
	}
	return m, nil
}

func chordEditTitle(m Model) (tea.Model, tea.Cmd) {
	if task := m.selectedTask(); task != nil {
		return m, m.openEditorForField(task.ID, "title", task.Title)
	}
	return m, nil
}

func chordEditContext(m Model) (tea.Model, tea.Cmd) {
	if task := m.selectedTask(); task != nil {
		return m, m.openEditorForField(task.ID, "context", task.Context)
	}
	return m, nil
}

func chordYankDesc(m Model) (tea.Model, tea.Cmd) {
	if task := m.selectedTask(); task != nil && task.Input != "" {
		if err := clipboard.WriteAll(task.Input); err != nil {
			m.err = fmt.Errorf("clipboard: %w", err)
		} else {
			m.statusMessage = "Copied input to clipboard"
			m.statusMessageTTL = 2
		}
	}
	return m, nil
}

func chordYankContext(m Model) (tea.Model, tea.Cmd) {
	if task := m.selectedTask(); task != nil && task.Context != "" {
		if err := clipboard.WriteAll(task.Context); err != nil {
			m.err = fmt.Errorf("clipboard: %w", err)
		} else {
			m.statusMessage = "Copied context to clipboard"
			m.statusMessageTTL = 2
		}
	}
	return m, nil
}
