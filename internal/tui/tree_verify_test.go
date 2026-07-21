package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Bakaface/sortie/internal/daemon"
	"github.com/charmbracelet/lipgloss"
)

// TestBranchViewRendering_FlatRoots verifies the tree output when all tasks
// target the same base branch (the user's reported scenario).
func TestBranchViewRendering_FlatRoots(t *testing.T) {
	width := 120
	list := newListView(false, "test-project")
	list.branchView = true
	list.showBranch = true
	list.SetSize(width, 24)

	tasks := []daemon.TaskInfo{
		{ID: 231, Branch: "bakaface/TECH-22577-observability", TargetBranch: "main", Status: "tmux", Title: "Task A"},
		{ID: 228, Branch: "bakaface/TECH-22566-import-linter", TargetBranch: "main", Status: "tmux", Title: "Task B"},
		{ID: 227, Branch: "aleksandrlsl/common-structure", TargetBranch: "main", Status: "tmux", Title: "Task C"},
		{ID: 226, Branch: "bakaface/TECH-22115-e2e-test", TargetBranch: "main", Status: "tmux", Title: "Task D"},
	}
	list.SetTasks(tasks)

	// Verify tree entries exist and all are depth 0
	if len(list.treeEntries) != 4 {
		t.Fatalf("expected 4 tree entries, got %d", len(list.treeEntries))
	}

	// First 3 should be ├─ (not last), last should be └─
	for i := 0; i < 3; i++ {
		if list.treeEntries[i].IsLast {
			t.Errorf("treeEntries[%d] should NOT be IsLast", i)
		}
	}
	if !list.treeEntries[3].IsLast {
		t.Error("treeEntries[3] should be IsLast")
	}

	// Verify rendered branch column has tree connectors
	branchWidth := list.cw.branch
	for i, entry := range list.treeEntries {
		rendered := entry.renderBranchColumn(list.tasks[i].Branch, branchWidth)
		if i < 3 {
			if !strings.HasPrefix(rendered, "├─") {
				t.Errorf("task %d branch should start with '├─', got %q", i, rendered)
			}
		} else {
			if !strings.HasPrefix(rendered, "└─") {
				t.Errorf("task %d branch should start with '└─', got %q", i, rendered)
			}
		}
		t.Logf("row %d: %s", i, rendered)
	}

	// Verify full View() output has uniform line widths
	output := list.View()
	lines := strings.Split(output, "\n")
	var taskLineWidths []int
	for _, line := range lines {
		if strings.Contains(line, "TECH-") || strings.Contains(line, "common-") {
			taskLineWidths = append(taskLineWidths, lipgloss.Width(line))
		}
	}
	if len(taskLineWidths) != 4 {
		t.Fatalf("expected 4 task lines, got %d", len(taskLineWidths))
	}
	for i := 1; i < len(taskLineWidths); i++ {
		if taskLineWidths[i] != taskLineWidths[0] {
			t.Errorf("task line widths not uniform: line 0 = %d, line %d = %d",
				taskLineWidths[0], i, taskLineWidths[i])
		}
	}
}

// TestBranchViewRendering_NestedTree verifies a mixed tree with parent-child chains.
func TestBranchViewRendering_NestedTree(t *testing.T) {
	width := 100
	list := newListView(false, "test-project")
	list.branchView = true
	list.showBranch = true
	list.SetSize(width, 24)

	tasks := []daemon.TaskInfo{
		{ID: 5, Branch: "feat-e", TargetBranch: "feat-a", Status: "pending", Title: "Task E"},
		{ID: 4, Branch: "feat-d", TargetBranch: "main", Status: "pending", Title: "Task D"},
		{ID: 3, Branch: "feat-c", TargetBranch: "feat-a", Status: "running", Title: "Task C"},
		{ID: 2, Branch: "feat-b", TargetBranch: "feat-c", Status: "pending", Title: "Task B"},
		{ID: 1, Branch: "feat-a", TargetBranch: "main", Status: "completed", Title: "Task A"},
	}
	list.SetTasks(tasks)

	// Expected tree:
	//   ├─feat-d        (root, not last)
	//   └─feat-a        (root, last)
	//     ├─feat-e      (child of feat-a, not last)
	//     └─feat-c      (child of feat-a, last)
	//       └─feat-b    (child of feat-c, last)

	if len(list.treeEntries) != 5 {
		t.Fatalf("expected 5 tree entries, got %d", len(list.treeEntries))
	}

	wantIDs := []int64{4, 1, 5, 3, 2}
	wantDepths := []int{0, 0, 1, 1, 2}
	wantIsLast := []bool{false, true, false, true, true}

	for i, want := range wantIDs {
		if list.tasks[i].ID != want {
			t.Errorf("tasks[%d].ID = %d, want %d", i, list.tasks[i].ID, want)
		}
	}
	for i, want := range wantDepths {
		if list.treeEntries[i].Depth != want {
			t.Errorf("treeEntries[%d].Depth = %d, want %d", i, list.treeEntries[i].Depth, want)
		}
	}
	for i, want := range wantIsLast {
		if list.treeEntries[i].IsLast != want {
			t.Errorf("treeEntries[%d].IsLast = %v, want %v", i, list.treeEntries[i].IsLast, want)
		}
	}

	// Render and log each branch column for visual verification
	branchWidth := list.cw.branch
	for i, entry := range list.treeEntries {
		rendered := entry.renderBranchColumn(list.tasks[i].Branch, branchWidth)
		t.Logf("row %d (ID %d): %s", i, list.tasks[i].ID, rendered)

		// Verify rune width matches column width
		if rw := len([]rune(rendered)); rw != branchWidth {
			t.Errorf("row %d: rune width = %d, want %d", i, rw, branchWidth)
		}
	}

	// Verify specific tree connector prefixes
	checks := []struct {
		idx    int
		prefix string
	}{
		{0, "├─feat-d"},     // root, not last
		{1, "└─feat-a"},     // root, last
		{2, "  ├─feat-e"},   // child of last root, not last child
		{3, "  └─feat-c"},   // child of last root, last child
		{4, "    └─feat-b"}, // grandchild of last root's last child
	}
	for _, c := range checks {
		rendered := list.treeEntries[c.idx].renderBranchColumn(list.tasks[c.idx].Branch, branchWidth)
		if !strings.HasPrefix(rendered, c.prefix) {
			t.Errorf("row %d: expected prefix %q, got %q", c.idx, c.prefix, rendered)
		}
	}

	// Verify full View() output line width uniformity
	output := list.View()
	lines := strings.Split(output, "\n")
	var taskLineWidths []int
	for _, line := range lines {
		if strings.Contains(line, "feat-") {
			taskLineWidths = append(taskLineWidths, lipgloss.Width(line))
		}
	}
	if len(taskLineWidths) > 1 {
		for i := 1; i < len(taskLineWidths); i++ {
			if taskLineWidths[i] != taskLineWidths[0] {
				t.Errorf("task lines have inconsistent widths: line 0 = %d, line %d = %d",
					taskLineWidths[0], i, taskLineWidths[i])
			}
		}
	}
}

// TestBranchViewRendering_ContinuationLines verifies "│" continuation lines
// appear correctly when a non-last root has children.
func TestBranchViewRendering_ContinuationLines(t *testing.T) {
	list := newListView(false, "test-project")
	list.branchView = true
	list.showBranch = true
	list.SetSize(80, 24)

	tasks := []daemon.TaskInfo{
		{ID: 4, Branch: "feat-d", TargetBranch: "main", Status: "pending", Title: "Task D"},
		{ID: 3, Branch: "feat-c", TargetBranch: "feat-a", Status: "pending", Title: "Task C"},
		{ID: 2, Branch: "feat-b", TargetBranch: "feat-a", Status: "pending", Title: "Task B"},
		{ID: 1, Branch: "feat-a", TargetBranch: "main", Status: "pending", Title: "Task A"},
	}
	list.SetTasks(tasks)

	// Expected tree:
	//   ├─feat-d        (root, not last → "│" continues below)
	//   └─feat-a        (root, last → "  " continues below)
	//     ├─feat-c      (child, not last)
	//     └─feat-b      (child, last)

	branchWidth := list.cw.branch
	results := make([]string, len(list.tasks))
	for i, entry := range list.treeEntries {
		results[i] = entry.renderBranchColumn(list.tasks[i].Branch, branchWidth)
	}

	fmt.Println("=== Branch view rendering ===")
	for i, r := range results {
		fmt.Printf("  row %d (ID %d): %s\n", i, list.tasks[i].ID, r)
	}

	// feat-d is first root (not last) → "├─"
	if !strings.HasPrefix(results[0], "├─feat-d") {
		t.Errorf("row 0: expected '├─feat-d' prefix, got %q", results[0])
	}

	// feat-a is last root → "└─"
	if !strings.HasPrefix(results[1], "└─feat-a") {
		t.Errorf("row 1: expected '└─feat-a' prefix, got %q", results[1])
	}

	// feat-c: child of feat-a (last root) → "  ├─" (no continuation at root level)
	if !strings.HasPrefix(results[2], "  ├─feat-c") {
		t.Errorf("row 2: expected '  ├─feat-c' prefix, got %q", results[2])
	}

	// feat-b: child of feat-a (last root) → "  └─" (no continuation at root level)
	if !strings.HasPrefix(results[3], "  └─feat-b") {
		t.Errorf("row 3: expected '  └─feat-b' prefix, got %q", results[3])
	}
}
