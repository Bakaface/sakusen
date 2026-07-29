package tui

import (
	"testing"

	"github.com/Bakaface/sortie/internal/daemon"
)

func TestPerformSearch(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Fix authentication bug", Status: "pending"},
		{ID: 2, Title: "Add search feature", Status: "running"},
		{ID: 3, Title: "Update documentation", Status: "completed"},
		{ID: 4, Title: "Fix search crash", Status: "failed"},
	})
	// SetTasks sorts by ID descending, so order is: 4, 3, 2, 1
	// Indices: 0=ID4 "Fix search crash", 1=ID3 "Update documentation", 2=ID2 "Add search feature", 3=ID1 "Fix authentication bug"

	// Test case-insensitive search
	l.performSearch("search")
	if len(l.matchedIndices) != 2 {
		t.Errorf("expected 2 matches for 'search', got %d", len(l.matchedIndices))
	}
	// "search" appears in ID4 (index 0) and ID2 (index 2)
	if l.matchedIndices[0] != 0 || l.matchedIndices[1] != 2 {
		t.Errorf("expected matches at indices [0, 2], got %v", l.matchedIndices)
	}

	// Test empty search clears matches
	l.performSearch("")
	if len(l.matchedIndices) != 0 {
		t.Errorf("expected 0 matches for empty search, got %d", len(l.matchedIndices))
	}

	// Test search with no matches
	l.performSearch("nonexistent")
	if len(l.matchedIndices) != 0 {
		t.Errorf("expected 0 matches for 'nonexistent', got %d", len(l.matchedIndices))
	}
}

func TestPerformSearchWithDescription(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "", Input: "Fix authentication bug", Status: "pending"},
		{ID: 2, Title: "Add search feature", Input: "Implement vim-style search", Status: "running"},
	})
	// SetTasks sorts by ID descending, so order is: 2, 1
	// Indices: 0=ID2 "Add search feature", 1=ID1 "" (uses description)

	// Search should fall back to description if title is empty
	l.performSearch("authentication")
	if len(l.matchedIndices) != 1 {
		t.Errorf("expected 1 match for 'authentication', got %d", len(l.matchedIndices))
	}
	// ID1 is at index 1
	if l.matchedIndices[0] != 1 {
		t.Errorf("expected match at index 1, got %v", l.matchedIndices)
	}

	// Search in title should work normally
	l.performSearch("search")
	if len(l.matchedIndices) != 1 {
		t.Errorf("expected 1 match for 'search', got %d", len(l.matchedIndices))
	}
	// ID2 is at index 0
	if l.matchedIndices[0] != 0 {
		t.Errorf("expected match at index 0, got %v", l.matchedIndices)
	}
}

func TestPerformSearchAndJump_ForwardSearch(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Fix bug", Status: "pending"},
		{ID: 2, Title: "Add feature", Status: "running"},
		{ID: 3, Title: "Fix crash", Status: "completed"},
		{ID: 4, Title: "Fix typo", Status: "failed"},
	})
	// SetTasks sorts by ID descending, so order is: 4, 3, 2, 1
	// Indices: 0=ID4 "Fix typo", 1=ID3 "Fix crash", 2=ID2 "Add feature", 3=ID1 "Fix bug"
	// "fix" matches indices: 0, 1, 3

	// Forward search from beginning
	l.table.SetCursor(0)
	l.performSearchAndJump("fix", 0, 1)
	if len(l.matchedIndices) != 3 {
		t.Errorf("expected 3 matches, got %d", len(l.matchedIndices))
	}
	if l.table.Cursor() != 0 {
		t.Errorf("expected cursor at 0 (first match), got %d", l.table.Cursor())
	}

	// Forward search from middle (cursor at 1, next match is at 3)
	l.table.SetCursor(1)
	l.performSearchAndJump("fix", 1, 1)
	if l.table.Cursor() != 1 {
		t.Errorf("expected cursor at 1 (match at cursor), got %d", l.table.Cursor())
	}

	// Forward search from index 2 (not a match), should jump to 3
	l.table.SetCursor(2)
	l.performSearchAndJump("fix", 2, 1)
	if l.table.Cursor() != 3 {
		t.Errorf("expected cursor at 3 (next match after cursor), got %d", l.table.Cursor())
	}

	// Forward search from last match wraps to beginning
	l.table.SetCursor(3)
	l.performSearchAndJump("fix", 3, 1)
	if l.table.Cursor() != 3 {
		t.Errorf("expected cursor at 3 (match at cursor), got %d", l.table.Cursor())
	}
}

func TestPerformSearchAndJump_BackwardSearch(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Fix bug", Status: "pending"},
		{ID: 2, Title: "Add feature", Status: "running"},
		{ID: 3, Title: "Fix crash", Status: "completed"},
		{ID: 4, Title: "Fix typo", Status: "failed"},
	})
	// SetTasks sorts by ID descending, so order is: 4, 3, 2, 1
	// Indices: 0=ID4 "Fix typo", 1=ID3 "Fix crash", 2=ID2 "Add feature", 3=ID1 "Fix bug"
	// "fix" matches indices: 0, 1, 3

	// Backward search from end
	l.table.SetCursor(3)
	l.performSearchAndJump("fix", 3, -1)
	if len(l.matchedIndices) != 3 {
		t.Errorf("expected 3 matches, got %d", len(l.matchedIndices))
	}
	if l.table.Cursor() != 3 {
		t.Errorf("expected cursor at 3 (match at cursor), got %d", l.table.Cursor())
	}

	// Backward search from middle (cursor at 2, previous match is at 1)
	l.table.SetCursor(2)
	l.performSearchAndJump("fix", 2, -1)
	if l.table.Cursor() != 1 {
		t.Errorf("expected cursor at 1 (match at or before cursor), got %d", l.table.Cursor())
	}

	// Backward search from first match wraps to last
	l.table.SetCursor(0)
	l.performSearchAndJump("fix", 0, -1)
	if l.table.Cursor() != 0 {
		t.Errorf("expected cursor at 0 (match at cursor), got %d", l.table.Cursor())
	}
}

func TestNextMatch_ForwardSearch(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Fix bug", Status: "pending"},
		{ID: 2, Title: "Add feature", Status: "running"},
		{ID: 3, Title: "Fix crash", Status: "completed"},
	})

	l.performSearch("fix")
	l.table.SetCursor(0)
	l.currentMatchIdx = 0

	// Next match in forward direction
	l.nextMatch(1)
	if l.table.Cursor() != 2 {
		t.Errorf("expected cursor at 2, got %d", l.table.Cursor())
	}
	if l.currentMatchIdx != 1 {
		t.Errorf("expected currentMatchIdx at 1, got %d", l.currentMatchIdx)
	}

	// Wrap to first match
	l.nextMatch(1)
	if l.table.Cursor() != 0 {
		t.Errorf("expected cursor to wrap to 0, got %d", l.table.Cursor())
	}
	if l.currentMatchIdx != 0 {
		t.Errorf("expected currentMatchIdx to wrap to 0, got %d", l.currentMatchIdx)
	}
}

func TestNextMatch_BackwardSearch(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Fix bug", Status: "pending"},
		{ID: 2, Title: "Add feature", Status: "running"},
		{ID: 3, Title: "Fix crash", Status: "completed"},
	})

	l.performSearch("fix")
	l.table.SetCursor(2)
	l.currentMatchIdx = 1

	// Next match in backward direction (goes to lower index)
	l.nextMatch(-1)
	if l.table.Cursor() != 0 {
		t.Errorf("expected cursor at 0, got %d", l.table.Cursor())
	}
	if l.currentMatchIdx != 0 {
		t.Errorf("expected currentMatchIdx at 0, got %d", l.currentMatchIdx)
	}

	// Wrap to last match
	l.nextMatch(-1)
	if l.table.Cursor() != 2 {
		t.Errorf("expected cursor to wrap to 2, got %d", l.table.Cursor())
	}
	if l.currentMatchIdx != 1 {
		t.Errorf("expected currentMatchIdx to wrap to 1, got %d", l.currentMatchIdx)
	}
}

func TestPreviousMatch_ForwardSearch(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Fix bug", Status: "pending"},
		{ID: 2, Title: "Add feature", Status: "running"},
		{ID: 3, Title: "Fix crash", Status: "completed"},
	})

	l.performSearch("fix")
	l.table.SetCursor(2)
	l.currentMatchIdx = 1

	// Previous match in forward direction (goes to lower index)
	l.previousMatch(1)
	if l.table.Cursor() != 0 {
		t.Errorf("expected cursor at 0, got %d", l.table.Cursor())
	}
	if l.currentMatchIdx != 0 {
		t.Errorf("expected currentMatchIdx at 0, got %d", l.currentMatchIdx)
	}

	// Wrap to last match
	l.previousMatch(1)
	if l.table.Cursor() != 2 {
		t.Errorf("expected cursor to wrap to 2, got %d", l.table.Cursor())
	}
	if l.currentMatchIdx != 1 {
		t.Errorf("expected currentMatchIdx to wrap to 1, got %d", l.currentMatchIdx)
	}
}

func TestPreviousMatch_BackwardSearch(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Fix bug", Status: "pending"},
		{ID: 2, Title: "Add feature", Status: "running"},
		{ID: 3, Title: "Fix crash", Status: "completed"},
	})

	l.performSearch("fix")
	l.table.SetCursor(0)
	l.currentMatchIdx = 0

	// Previous match in backward direction (goes to higher index)
	l.previousMatch(-1)
	if l.table.Cursor() != 2 {
		t.Errorf("expected cursor at 2, got %d", l.table.Cursor())
	}
	if l.currentMatchIdx != 1 {
		t.Errorf("expected currentMatchIdx at 1, got %d", l.currentMatchIdx)
	}

	// Wrap to first match
	l.previousMatch(-1)
	if l.table.Cursor() != 0 {
		t.Errorf("expected cursor to wrap to 0, got %d", l.table.Cursor())
	}
	if l.currentMatchIdx != 0 {
		t.Errorf("expected currentMatchIdx to wrap to 0, got %d", l.currentMatchIdx)
	}
}

func TestIsSearchMatch(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Fix bug", Status: "pending"},
		{ID: 2, Title: "Add feature", Status: "running"},
		{ID: 3, Title: "Fix crash", Status: "completed"},
	})

	l.performSearch("fix")

	if !l.isSearchMatch(0) {
		t.Error("expected index 0 to be a match")
	}
	if l.isSearchMatch(1) {
		t.Error("expected index 1 NOT to be a match")
	}
	if !l.isSearchMatch(2) {
		t.Error("expected index 2 to be a match")
	}
}

func TestSearchWithNoMatches(t *testing.T) {
	l := newListView(false, "")
	l.SetTasks([]daemon.TaskInfo{
		{ID: 1, Title: "Fix bug", Status: "pending"},
		{ID: 2, Title: "Add feature", Status: "running"},
	})

	l.performSearchAndJump("nonexistent", 0, 1)
	if len(l.matchedIndices) != 0 {
		t.Errorf("expected 0 matches, got %d", len(l.matchedIndices))
	}
	// Cursor should not change when there are no matches
	if l.table.Cursor() != 0 {
		t.Errorf("expected cursor to remain at 0, got %d", l.table.Cursor())
	}

	// Next/previous match should do nothing with no matches
	originalCursor := l.table.Cursor()
	l.nextMatch(1)
	if l.table.Cursor() != originalCursor {
		t.Error("nextMatch should not change cursor when there are no matches")
	}
	l.previousMatch(1)
	if l.table.Cursor() != originalCursor {
		t.Error("previousMatch should not change cursor when there are no matches")
	}
}
