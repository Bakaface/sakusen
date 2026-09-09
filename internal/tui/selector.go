package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// selectorKind identifies which selection dialog is active.
type selectorKind int

const (
	selectorNone selectorKind = iota
	selectorPriority
	selectorArtifact
	selectorWorkflow  // workflow picker: opens new-task prompt with workflow preselected (or skips if fully-pinned)
	selectorRoutine   // routine picker: runs the chosen routine (or asks for its input first)
	selectorRetryStep // retry: pick which workflow step to restart from
)

// selectionResult is returned by selector.HandleKey to signal what happened.
type selectionResult int

const (
	selNone      selectionResult = iota // key consumed, no action
	selChosen                           // user picked an item
	selCancelled                        // user cancelled
)

// selector is a generic list picker that handles vim-style navigation,
// number-key quick select, and rendering.
type selector struct {
	kind         selectorKind
	title        string
	items        []string
	cursor       int
	pendingG     bool
	descriptions []string                         // optional; shown under selected item
	itemStyle    func(name string) lipgloss.Style // optional; per-item coloring for non-selected
	disabled     []bool                           // optional; rows with disabled[i]==true are skipped on navigation and not selectable
	hint         string                           // footer override; empty uses default
	// Auxiliary data for the on-select callback
	taskID int64  // for priority, artifact
	action string // for artifact ("view"/"edit") — may be mutated mid-flow (e.g. pressing 'e' overrides to "edit")

	// Filtering — when filterable is true, printable characters typed by the
	// user append to filter (substring matching, case-insensitive). The
	// original items list is preserved in allItems / allDescriptions so the
	// filter can be reset without re-rendering.
	filterable      bool
	filter          string
	allItems        []string
	allDescriptions []string
}

func (s *selector) IsActive() bool {
	return s.kind != selectorNone
}

func (s *selector) Reset() {
	*s = selector{}
}

func (s *selector) Selected() string {
	if s.cursor >= 0 && s.cursor < len(s.items) {
		return s.items[s.cursor]
	}
	return ""
}

// HandleKey processes a key press and returns the result.
func (s *selector) HandleKey(keyStr string) selectionResult {
	// In filterable mode, navigation uses arrow keys and ctrl+j/k; printable
	// characters append to the filter rather than triggering letter shortcuts.
	if s.filterable {
		return s.handleFilterableKey(keyStr)
	}

	// Handle "gg" sequence for go-to-top
	if keyStr == "g" {
		if s.pendingG {
			s.pendingG = false
			s.cursor = s.firstActionable()
			return selNone
		}
		s.pendingG = true
		return selNone
	}
	s.pendingG = false

	switch keyStr {
	case "up", "k":
		s.cursor = s.stepCursor(s.cursor, -1)
	case "down", "j":
		s.cursor = s.stepCursor(s.cursor, 1)
	case "G":
		s.cursor = s.lastActionable()
	case "ctrl+d", "pgdown":
		half := max(1, len(s.items)/2)
		s.cursor = s.snapActionable(min(s.cursor+half, len(s.items)-1), 1)
	case "ctrl+u", "pgup":
		half := max(1, len(s.items)/2)
		s.cursor = s.snapActionable(max(s.cursor-half, 0), -1)
	case "enter":
		if s.isDisabled(s.cursor) {
			return selNone
		}
		return selChosen
	case "e":
		// Optional: pressing 'e' on artifact-style selectors picks edit mode
		// before firing the selection. Caller decides what to do with action.
		if s.kind == selectorArtifact && !s.isDisabled(s.cursor) {
			s.action = "edit"
			return selChosen
		}
	case "v":
		if s.kind == selectorArtifact && !s.isDisabled(s.cursor) {
			s.action = "view"
			return selChosen
		}
	case "esc", "q":
		return selCancelled
	default:
		// Number keys for quick selection (1-9)
		if len(keyStr) == 1 && keyStr[0] >= '1' && keyStr[0] <= '9' {
			idx := int(keyStr[0] - '1')
			if idx < len(s.items) && !s.isDisabled(idx) {
				s.cursor = idx
				return selChosen
			}
		}
	}
	return selNone
}

// handleFilterableKey is the input handler for filterable selectors.
// j/k become filter chars; navigation uses arrow keys and ctrl+j/k.
func (s *selector) handleFilterableKey(keyStr string) selectionResult {
	switch keyStr {
	case "up", "ctrl+p", "ctrl+k":
		s.cursor = s.stepCursor(s.cursor, -1)
		return selNone
	case "down", "ctrl+n", "ctrl+j":
		s.cursor = s.stepCursor(s.cursor, 1)
		return selNone
	case "ctrl+d", "pgdown":
		half := max(1, len(s.items)/2)
		s.cursor = s.snapActionable(min(s.cursor+half, len(s.items)-1), 1)
		return selNone
	case "ctrl+u", "pgup":
		half := max(1, len(s.items)/2)
		s.cursor = s.snapActionable(max(s.cursor-half, 0), -1)
		return selNone
	case "enter":
		if len(s.items) == 0 || s.isDisabled(s.cursor) {
			return selNone
		}
		return selChosen
	case "esc":
		// Esc clears a non-empty filter; if filter already empty, cancel.
		if s.filter != "" {
			s.filter = ""
			s.applyFilter()
			return selNone
		}
		return selCancelled
	case "backspace":
		if len(s.filter) > 0 {
			s.filter = s.filter[:len(s.filter)-1]
			s.applyFilter()
		}
		return selNone
	case "ctrl+w":
		s.filter = ""
		s.applyFilter()
		return selNone
	default:
		// Single printable characters append to filter. Anything else is ignored.
		if len(keyStr) == 1 && keyStr[0] >= ' ' && keyStr[0] <= '~' {
			s.filter += keyStr
			s.applyFilter()
		}
	}
	return selNone
}

// applyFilter substring-matches s.allItems against s.filter (case-insensitive)
// and replaces s.items/s.descriptions with the filtered subset. The cursor is
// reset to the first actionable row.
func (s *selector) applyFilter() {
	if s.filter == "" {
		s.items = append([]string(nil), s.allItems...)
		if s.allDescriptions != nil {
			s.descriptions = append([]string(nil), s.allDescriptions...)
		}
	} else {
		needle := strings.ToLower(s.filter)
		s.items = s.items[:0]
		if s.allDescriptions != nil {
			s.descriptions = s.descriptions[:0]
		}
		for i, item := range s.allItems {
			if strings.Contains(strings.ToLower(item), needle) {
				s.items = append(s.items, item)
				if s.allDescriptions != nil && i < len(s.allDescriptions) {
					s.descriptions = append(s.descriptions, s.allDescriptions[i])
				}
			}
		}
	}
	if s.cursor >= len(s.items) {
		s.cursor = 0
	}
}

// isDisabled reports whether the row at idx is non-actionable.
func (s *selector) isDisabled(idx int) bool {
	if idx < 0 || idx >= len(s.disabled) {
		return false
	}
	return s.disabled[idx]
}

// stepCursor moves the cursor one step in the given direction, skipping
// disabled rows. Stops at the first/last actionable row instead of wrapping.
func (s *selector) stepCursor(from, dir int) int {
	for i := from + dir; i >= 0 && i < len(s.items); i += dir {
		if !s.isDisabled(i) {
			return i
		}
	}
	return from
}

// snapActionable returns the nearest actionable row to idx in the given
// direction, falling back to the other direction if needed.
func (s *selector) snapActionable(idx, dir int) int {
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s.items) {
		idx = len(s.items) - 1
	}
	if !s.isDisabled(idx) {
		return idx
	}
	for i := idx; i >= 0 && i < len(s.items); i += dir {
		if !s.isDisabled(i) {
			return i
		}
	}
	// Fall back: scan the other direction.
	for i := idx; i >= 0 && i < len(s.items); i -= dir {
		if !s.isDisabled(i) {
			return i
		}
	}
	return idx
}

func (s *selector) firstActionable() int {
	for i := range s.items {
		if !s.isDisabled(i) {
			return i
		}
	}
	return 0
}

func (s *selector) lastActionable() int {
	for i := len(s.items) - 1; i >= 0; i-- {
		if !s.isDisabled(i) {
			return i
		}
	}
	return max(0, len(s.items)-1)
}

// View renders the selection dialog.
func (s *selector) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" "+s.title+" ") + "\n\n")

	if s.filterable {
		b.WriteString("  " + dimStyle.Render("filter: ") + s.filter + "█\n\n")
	}

	if s.filterable && len(s.items) == 0 {
		b.WriteString("  " + dimStyle.Render("(no matches)") + "\n")
	}

	for i, name := range s.items {
		var label string
		if s.filterable {
			// No numeric quick-select in filterable mode — drop the leading number.
			label = "  " + name
		} else {
			label = fmt.Sprintf("  %d. %s", i+1, name)
		}
		if i == s.cursor {
			b.WriteString(selectedStyle.Render("> "+label) + "\n")
			if s.descriptions != nil && i < len(s.descriptions) && s.descriptions[i] != "" {
				b.WriteString(dimStyle.Render("     "+s.descriptions[i]) + "\n")
			}
		} else {
			if s.itemStyle != nil {
				b.WriteString("    " + s.itemStyle(name).Render(label) + "\n")
			} else {
				b.WriteString("    " + label + "\n")
			}
		}
	}

	var hint string
	switch {
	case s.hint != "":
		hint = s.hint
	case s.filterable:
		hint = "type to filter | ↑/↓: navigate | enter: select | esc: clear/cancel"
	default:
		hint = "j/k: navigate | enter: select | 1-9: quick select | esc: cancel"
	}
	b.WriteString("\n" + dimStyle.Render("  "+hint))
	return b.String()
}
