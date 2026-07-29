package tui

import "strings"

// Search helper methods

// performSearch searches for the query string in task titles (or descriptions if title is empty)
// and populates matchedIndices with the indices of matching tasks.
// It resets currentMatchIdx to 0.
func (l *listView) performSearch(query string) {
	if query == "" {
		l.matchedIndices = []int{}
		l.currentMatchIdx = 0
		return
	}

	queryLower := strings.ToLower(query)
	matches := []int{}

	for i, task := range l.tasks {
		// Search in title, or description if title is empty
		searchText := task.Title
		if searchText == "" {
			searchText = task.Input
		}
		searchTextLower := strings.ToLower(searchText)

		if strings.Contains(searchTextLower, queryLower) {
			matches = append(matches, i)
		}
	}

	l.matchedIndices = matches
	l.currentMatchIdx = 0
}

// performSearchAndJump performs a search and then jumps to the nearest match
// in the given direction relative to the cursor position.
// direction: 1 for forward search, -1 for backward search
func (l *listView) performSearchAndJump(query string, cursor int, direction int) {
	l.performSearch(query)

	if len(l.matchedIndices) == 0 {
		return
	}

	if direction >= 0 {
		// Forward search: find first match at or after cursor
		for i, idx := range l.matchedIndices {
			if idx >= cursor {
				l.currentMatchIdx = i
				l.table.SetCursor(idx)
				return
			}
		}
		// No match at/after cursor, wrap to first match
		l.currentMatchIdx = 0
		l.table.SetCursor(l.matchedIndices[0])
	} else {
		// Backward search: find first match at or before cursor
		for i := len(l.matchedIndices) - 1; i >= 0; i-- {
			if l.matchedIndices[i] <= cursor {
				l.currentMatchIdx = i
				l.table.SetCursor(l.matchedIndices[i])
				return
			}
		}
		// No match at/before cursor, wrap to last match
		l.currentMatchIdx = len(l.matchedIndices) - 1
		l.table.SetCursor(l.matchedIndices[l.currentMatchIdx])
	}
}

// nextMatch moves to the next match in the search direction with wrapping.
// direction: 1 for forward search, -1 for backward search
func (l *listView) nextMatch(direction int) {
	if len(l.matchedIndices) == 0 {
		return
	}

	// n moves in the direction of search
	if direction >= 0 {
		// Forward: go to next higher index
		l.currentMatchIdx = (l.currentMatchIdx + 1) % len(l.matchedIndices)
	} else {
		// Backward: go to next lower index
		l.currentMatchIdx--
		if l.currentMatchIdx < 0 {
			l.currentMatchIdx = len(l.matchedIndices) - 1
		}
	}
	l.table.SetCursor(l.matchedIndices[l.currentMatchIdx])
}

// previousMatch moves to the previous match (opposite direction) with wrapping.
// direction: 1 for forward search, -1 for backward search
func (l *listView) previousMatch(direction int) {
	if len(l.matchedIndices) == 0 {
		return
	}

	// N moves opposite to the direction of search
	if direction >= 0 {
		// Forward search: N goes to previous (lower index)
		l.currentMatchIdx--
		if l.currentMatchIdx < 0 {
			l.currentMatchIdx = len(l.matchedIndices) - 1
		}
	} else {
		// Backward search: N goes to next (higher index)
		l.currentMatchIdx = (l.currentMatchIdx + 1) % len(l.matchedIndices)
	}
	l.table.SetCursor(l.matchedIndices[l.currentMatchIdx])
}

// isSearchMatch checks if the given task index is in the current match set.
func (l listView) isSearchMatch(taskIndex int) bool {
	for _, idx := range l.matchedIndices {
		if idx == taskIndex {
			return true
		}
	}
	return false
}
