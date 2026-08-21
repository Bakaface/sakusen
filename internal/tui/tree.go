package tui

import (
	"strings"

	"github.com/Bakaface/sakusen/internal/daemon"
)

// treeEntry holds the tree-rendering metadata for a single task in branch view.
type treeEntry struct {
	Depth     int    // nesting level (0 = root)
	IsLast    bool   // true = └─, false = ├─
	Ancestors []bool // true at depth i means ancestor at that depth still has more siblings (draw │)
}

// buildBranchTree reorders tasks into a tree based on TargetBranch → Branch relationships
// and returns the corresponding treeEntry metadata for each task.
func buildBranchTree(tasks []daemon.TaskInfo) ([]daemon.TaskInfo, []treeEntry) {
	if len(tasks) == 0 {
		return tasks, nil
	}

	// Map Branch → index of the owning task (lowest ID wins if multiple tasks share a branch)
	branchOwner := make(map[string]int)
	for i, t := range tasks {
		if t.Branch == "" {
			continue
		}
		if existing, ok := branchOwner[t.Branch]; ok {
			if tasks[i].ID < tasks[existing].ID {
				branchOwner[t.Branch] = i
			}
		} else {
			branchOwner[t.Branch] = i
		}
	}

	// Build adjacency list: parent task index → child task indices
	children := make(map[int][]int)
	isChild := make(map[int]bool)
	for i, t := range tasks {
		if t.TargetBranch == "" {
			continue
		}
		parentIdx, ok := branchOwner[t.TargetBranch]
		if !ok || parentIdx == i {
			continue
		}
		children[parentIdx] = append(children[parentIdx], i)
		isChild[i] = true
	}

	// Identify roots: tasks that are not children of any other task
	var roots []int
	for i := range tasks {
		if !isChild[i] {
			roots = append(roots, i)
		}
	}

	// Sort roots by ID descending (preserve original sort order)
	sortIndicesDesc(roots, tasks)

	// Sort children of each parent by ID descending
	for k := range children {
		sortIndicesDesc(children[k], tasks)
	}

	// DFS traversal
	ordered := make([]daemon.TaskInfo, 0, len(tasks))
	entries := make([]treeEntry, 0, len(tasks))
	visited := make(map[int]bool)

	var dfs func(idx int, depth int, isLast bool, ancestors []bool)
	dfs = func(idx int, depth int, isLast bool, ancestors []bool) {
		if visited[idx] {
			return
		}
		visited[idx] = true

		entry := treeEntry{
			Depth:     depth,
			IsLast:    isLast,
			Ancestors: make([]bool, len(ancestors)),
		}
		copy(entry.Ancestors, ancestors)

		ordered = append(ordered, tasks[idx])
		entries = append(entries, entry)

		kids := children[idx]
		// Build ancestors for children: current node's isLast determines whether
		// descendants draw a continuation line ("│") at this depth level.
		// This is the same for ALL children — only their own isLast differs.
		newAncestors := make([]bool, len(ancestors)+1)
		copy(newAncestors, ancestors)
		newAncestors[len(ancestors)] = !isLast

		for ci, childIdx := range kids {
			childIsLast := ci == len(kids)-1
			dfs(childIdx, depth+1, childIsLast, newAncestors)
		}
	}

	for ri, rootIdx := range roots {
		dfs(rootIdx, 0, ri == len(roots)-1, nil)
	}

	// Any unvisited tasks (from cycles) become roots
	for i := range tasks {
		if !visited[i] {
			dfs(i, 0, true, nil)
		}
	}

	return ordered, entries
}

// sortIndicesDesc sorts a slice of task indices by task ID descending.
func sortIndicesDesc(indices []int, tasks []daemon.TaskInfo) {
	// Simple insertion sort — child lists are typically small
	for i := 1; i < len(indices); i++ {
		for j := i; j > 0 && tasks[indices[j]].ID > tasks[indices[j-1]].ID; j-- {
			indices[j], indices[j-1] = indices[j-1], indices[j]
		}
	}
}

// renderBranchColumn renders a branch name with tree connectors for the given width.
func (e treeEntry) renderBranchColumn(branchName string, width int) string {
	var b strings.Builder

	// Draw ancestor continuation lines
	for _, hasMore := range e.Ancestors {
		if hasMore {
			b.WriteString("│ ")
		} else {
			b.WriteString("  ")
		}
	}

	// Draw current connector
	if e.IsLast {
		b.WriteString("└─")
	} else {
		b.WriteString("├─")
	}

	prefix := b.String()
	prefixWidth := len([]rune(prefix))
	remaining := width - prefixWidth
	remaining = max(remaining, 0)

	return prefix + truncateOrPad(branchName, remaining)
}
