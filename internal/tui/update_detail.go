package tui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) handleDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Handle confirmation prompt if active
	if m.confirmAction != "" {
		return m.handleConfirmKey(msg)
	}

	fk := cachedDetailFollowKeyMap
	dk := cachedDetailNormalKeyMap

	// Help overlay — ctrl+h toggles, ctrl+h or esc dismisses.
	if m.detail.showHelp {
		if key.Matches(msg, fk.Help) || key.Matches(msg, fk.ExitFollow) || key.Matches(msg, fk.Back) {
			m.detail.showHelp = false
		}
		return m, nil
	}
	if key.Matches(msg, fk.Help) {
		m.detail.showHelp = true
		return m, nil
	}

	// Common keys (both modes)
	switch {
	case key.Matches(msg, fk.Back): // "q"
		m.view = viewList
		return m, nil
	case key.Matches(msg, fk.Stop): // "ctrl+c"
		if m.detail.task != nil && m.client != nil {
			m.confirmAction = "stop"
			m.confirmTaskID = m.detail.task.ID
			return m, nil
		}
		return m, nil
	case key.Matches(msg, fk.Attach): // "t"
		if m.detail.task != nil {
			return m, m.attachTmuxSession(*m.detail.task)
		}
		return m, nil
	case key.Matches(msg, fk.EditLog): // "e"
		if m.detail.task != nil {
			return m, m.openLogInEditor(m.detail.task)
		}
		return m, nil
	}

	// Navigation keys work in both modes; in follow mode they switch to normal first
	// Handle "gg" sequence
	if key.Matches(msg, dk.GotoTop) {
		if m.detail.IsPendingG() {
			m.detail.SetPendingG(false)
			m.detail.SetFollowMode(false)
			m.detail.GotoTop()
			return m, nil
		}
		m.detail.SetPendingG(true)
		return m, nil
	}
	// Any non-g key resets pendingG
	m.detail.SetPendingG(false)

	switch {
	case key.Matches(msg, dk.GotoBottom): // "G"
		m.detail.SetFollowMode(false)
		m.detail.GotoBottom()
		return m, nil
	case key.Matches(msg, dk.Down): // "j", "down"
		m.detail.SetFollowMode(false)
		m.detail.ScrollDown()
		return m, nil
	case key.Matches(msg, dk.Up): // "k", "up"
		m.detail.SetFollowMode(false)
		m.detail.ScrollUp()
		return m, nil
	case key.Matches(msg, dk.HalfDown): // "ctrl+d", "pgdown"
		m.detail.SetFollowMode(false)
		m.detail.PageDown()
		return m, nil
	case key.Matches(msg, dk.HalfUp): // "ctrl+u", "pgup"
		m.detail.SetFollowMode(false)
		m.detail.PageUp()
		return m, nil
	case key.Matches(msg, dk.Follow): // "enter"
		m.detail.SetFollowMode(true)
		return m, nil
	case key.Matches(msg, fk.ExitFollow): // "esc"
		if m.detail.IsFollowMode() {
			m.detail.SetFollowMode(false)
		} else {
			m.view = viewList
		}
		return m, nil
	}

	return m, nil
}
