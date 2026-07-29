package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Bakaface/sortie/internal/daemon"
	// Aliased: this file uses "task" as the local parameter/variable name for
	// daemon.TaskInfo throughout (statusText, effectiveStatusFor, renderTask,
	// hasFailedBlocker, ...); the alias avoids that shadowing the package.
	taskpkg "github.com/Bakaface/sortie/internal/task"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type listView struct {
	table              table.Model
	tasks              []daemon.TaskInfo
	allTasks           []daemon.TaskInfo // unfiltered tasks (used when showFinished is false)
	width              int
	height             int
	showHelp           bool
	showLineNumbers    bool
	showBranch         bool
	showTarget         bool
	showFinished       bool
	globalMode         bool
	projectName        string
	tmuxSessions       map[int64]bool
	branchView         bool
	treeEntries        []treeEntry
	matchedIndices     []int         // indices of tasks matching current search
	currentMatchIdx    int           // index within matchedIndices
	scrollOffset       int           // index of first visible task row
	extraLines         int           // extra lines reserved below the list (search bar, command bar, etc.)
	helpOverride       string        // when non-empty, replaces the help bar (e.g. command/search input)
	hasScrollIndicator bool          // true when not all tasks fit in the visible window
	loading            bool          // true before the first successful task load
	refreshing         bool          // true while a background refresh is in-flight
	spinner            spinner.Model // loading spinner for initial load
	cw                 colWidths     // computed column widths
}

type colWidths struct {
	lineNum int
	id      int
	pri     int
	status  int
	project int
	branch  int
	target  int
	title   int
}

// safeKeyMap returns a table.KeyMap that only handles basic navigation.
// Keys like G, gg, dd, n, N are handled manually to avoid conflicts.
func safeKeyMap() table.KeyMap {
	return table.KeyMap{
		LineUp:       key.NewBinding(key.WithKeys("up", "k")),
		LineDown:     key.NewBinding(key.WithKeys("down", "j")),
		PageUp:       key.NewBinding(key.WithKeys("pgup")),
		PageDown:     key.NewBinding(key.WithKeys("pgdown")),
		HalfPageUp:   key.NewBinding(key.WithKeys("ctrl+u")),
		HalfPageDown: key.NewBinding(key.WithKeys("ctrl+d")),
		GotoTop:      key.NewBinding(key.WithKeys("home")),
		GotoBottom:   key.NewBinding(key.WithKeys("end")),
	}
}

func newListView(globalMode bool, projectName string) listView {
	// Default columns (recomputed when tasks arrive)
	initialCols := []table.Column{
		{Title: "ID", Width: 5},
		{Title: "P", Width: 2},
	}
	if globalMode {
		initialCols = append(initialCols, table.Column{Title: "Project", Width: 14})
	}
	initialCols = append(initialCols, table.Column{Title: "Status", Width: 18})
	initialCols = append(initialCols, table.Column{Title: "Title", Width: 40})

	t := table.New(
		table.WithColumns(initialCols),
		table.WithFocused(true),
		table.WithHeight(20),
		table.WithKeyMap(safeKeyMap()),
	)
	// Minimal styles — custom rendering handles visuals
	s := table.DefaultStyles()
	s.Selected = lipgloss.NewStyle()
	s.Cell = lipgloss.NewStyle()
	s.Header = lipgloss.NewStyle()
	t.SetStyles(s)
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = dimStyle
	return listView{
		table:           t,
		tasks:           make([]daemon.TaskInfo, 0),
		allTasks:        make([]daemon.TaskInfo, 0),
		showLineNumbers: true,
		showFinished:    true,
		globalMode:      globalMode,
		projectName:     projectName,
		loading:         true,
		spinner:         sp,
		cw:              colWidths{id: 5, pri: 2, status: 18, project: 14, branch: 20, target: 14, title: 40},
	}
}

// lineNumWidthForCount returns the gutter width for a given task count.
func lineNumWidthForCount(n int) int {
	width := 0
	for n > 0 {
		width++
		n /= 10
	}
	return max(2, width)
}

// computeWidths measures the actual content widths across all tasks and returns
// computed column widths, with TITLE getting whatever space remains.
func (l *listView) computeWidths() colWidths {
	const (
		minID      = 2
		maxID      = 8
		minStatus  = 10
		maxStatus  = 30
		minProject = 5
		maxProject = 20
		minBranch  = 8
		maxBranch  = 35
		minTarget  = 6
		maxTarget  = 20
		minTitle   = 15
	)

	cw := colWidths{pri: 2}

	if l.showLineNumbers {
		cw.lineNum = lineNumWidthForCount(len(l.tasks))
	}

	// Measure max content width for each column
	maxIDW := minID
	maxStatusW := minStatus
	maxProjectW := minProject
	maxBranchW := minBranch
	maxTargetW := minTarget

	for i, task := range l.tasks {
		if w := len(fmt.Sprintf("%d", task.ID)); w > maxIDW {
			maxIDW = w
		}
		if w := len([]rune(l.statusText(task))); w > maxStatusW {
			maxStatusW = w
		}
		if l.globalMode {
			if w := len([]rune(l.projectNameFor(task))); w > maxProjectW {
				maxProjectW = w
			}
		}
		if l.showBranch {
			w := len([]rune(task.Branch))
			if l.branchView && i < len(l.treeEntries) {
				w += (l.treeEntries[i].Depth + 1) * 2 // tree prefix: depth ancestors + connector
			}
			if w > maxBranchW {
				maxBranchW = w
			}
		}
		if l.showTarget {
			if w := len([]rune(task.TargetBranch)); w > maxTargetW {
				maxTargetW = w
			}
		}
	}

	// Apply caps and add 1 char padding for breathing room
	cw.id = min(maxIDW+1, maxID)
	cw.status = min(maxStatusW+1, maxStatus)
	cw.project = min(maxProjectW+1, maxProject)
	cw.branch = min(maxBranchW+1, maxBranch)
	cw.target = min(maxTargetW+1, maxTarget)

	// Compute title width from remaining space.
	// Format strings use these separators:
	//   non-global with lineNumbers: " %s %s %s %s%s%s %s" = 5 spaces (leading + 4 between)
	//   global with lineNumbers:     " %s %s %s %s %s%s%s %s" = 6 spaces
	// branch and target are prefixed with " " when included (already in midCols)
	spacing := 5
	if l.globalMode {
		spacing = 6
	}
	if l.showBranch {
		spacing++
	}
	if l.showTarget {
		spacing++
	}

	fixed := cw.id + cw.pri + cw.status + spacing
	if l.showLineNumbers {
		fixed += cw.lineNum
	}
	if l.globalMode {
		fixed += cw.project
	}
	if l.showBranch {
		fixed += cw.branch
	}
	if l.showTarget {
		fixed += cw.target
	}

	cw.title = l.width - fixed
	if cw.title < minTitle {
		cw.title = minTitle
	}

	return cw
}

func (l *listView) SetTasks(tasks []daemon.TaskInfo) {
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].ID > tasks[j].ID
	})

	wasLoading := l.loading

	// Skip full rebuild if nothing meaningful changed
	if !wasLoading && !l.tasksChanged(l.allTasks, tasks) {
		l.loading = false
		l.refreshing = false
		return
	}

	l.allTasks = tasks
	l.tasks = l.filterTasks(tasks)
	l.rebuildTree()
	l.table.SetRows(l.toRows())
	if l.table.Cursor() >= len(l.tasks) && len(l.tasks) > 0 {
		l.table.SetCursor(len(l.tasks) - 1)
	} else if len(l.tasks) == 0 {
		l.table.SetCursor(0)
	}
	l.recomputeColumns()
	l.ensureVisible()
	l.loading = false
	l.refreshing = false
}

// tasksChanged returns true if the two task slices differ in any visible field.
func (l *listView) tasksChanged(old, new []daemon.TaskInfo) bool {
	if len(old) != len(new) {
		return true
	}
	for i := range old {
		a, b := old[i], new[i]
		if a.ID != b.ID ||
			a.Status != b.Status ||
			a.Title != b.Title ||
			a.CurrentStep != b.CurrentStep ||
			a.Priority != b.Priority ||
			a.LoopIteration != b.LoopIteration ||
			a.TmuxActivity != b.TmuxActivity ||
			a.WorktreeDetached != b.WorktreeDetached ||
			a.Branch != b.Branch ||
			a.TargetBranch != b.TargetBranch {
			return true
		}
		// Compare blocked-by lists
		if len(a.BlockedBy) != len(b.BlockedBy) {
			return true
		}
		for j := range a.BlockedBy {
			if a.BlockedBy[j] != b.BlockedBy[j] {
				return true
			}
		}
	}
	return false
}

// filterTasks returns tasks filtered according to display settings,
// with tmux tasks prioritized at the top of the list.
func (l *listView) filterTasks(tasks []daemon.TaskInfo) []daemon.TaskInfo {
	var filtered []daemon.TaskInfo
	if l.showFinished {
		filtered = make([]daemon.TaskInfo, len(tasks))
		copy(filtered, tasks)
	} else {
		filtered = make([]daemon.TaskInfo, 0, len(tasks))
		for _, t := range tasks {
			if !taskpkg.Status(t.Status).IsTerminal() {
				filtered = append(filtered, t)
			}
		}
	}
	// Stable sort to preserve incoming order (ID desc) within each group,
	// while floating tmux tasks to the top.
	sort.SliceStable(filtered, func(i, j int) bool {
		iTmux := taskpkg.Status(filtered[i].Status) == taskpkg.StatusTmux
		jTmux := taskpkg.Status(filtered[j].Status) == taskpkg.StatusTmux
		if iTmux != jTmux {
			return iTmux
		}
		return false
	})
	return filtered
}

// applyFilter re-filters allTasks into tasks and adjusts cursor.
// rebuildTree reorders tasks into branch tree order when branchView is active.
func (l *listView) rebuildTree() {
	if !l.branchView {
		l.treeEntries = nil
		return
	}
	reordered, entries := buildBranchTree(l.tasks)
	l.tasks = reordered
	l.treeEntries = entries
}

func (l *listView) applyFilter() {
	l.tasks = l.filterTasks(l.allTasks)
	l.rebuildTree()
	l.table.SetRows(l.toRows())
	if l.table.Cursor() >= len(l.tasks) && len(l.tasks) > 0 {
		l.table.SetCursor(len(l.tasks) - 1)
	} else if len(l.tasks) == 0 {
		l.table.SetCursor(0)
	}
	l.recomputeColumns()
	l.ensureVisible()
}

func (l *listView) UpdateTask(task daemon.TaskInfo) {
	// Update in allTasks
	found := false
	for i, t := range l.allTasks {
		if t.ID == task.ID {
			l.allTasks[i] = task
			found = true
			break
		}
	}
	if !found {
		l.allTasks = append(l.allTasks, task)
		// Keep allTasks sorted by ID desc (same order as SetTasks) so the next
		// tick-driven SetTasks sees no change and skips the full rebuild.
		sort.Slice(l.allTasks, func(i, j int) bool {
			return l.allTasks[i].ID > l.allTasks[j].ID
		})
	}
	l.applyFilter()
}

func (l *listView) RemoveTask(id int64) {
	for i, t := range l.allTasks {
		if t.ID == id {
			l.allTasks = append(l.allTasks[:i], l.allTasks[i+1:]...)
			break
		}
	}
	l.applyFilter()
}

func (l *listView) Selected() *daemon.TaskInfo {
	cursor := l.table.Cursor()
	if cursor >= 0 && cursor < len(l.tasks) {
		return &l.tasks[cursor]
	}
	return nil
}

func (l *listView) MoveUp() {
	l.table.MoveUp(1)
	l.ensureVisible()
}

func (l *listView) MoveDown() {
	l.table.MoveDown(1)
	l.ensureVisible()
}

func (l *listView) GotoTop() {
	l.table.GotoTop()
	l.ensureVisible()
}

func (l *listView) GotoBottom() {
	l.table.GotoBottom()
	l.ensureVisible()
}

// GotoIndex moves the cursor to the task at the given row index.
func (l *listView) GotoIndex(index int) {
	if index >= 0 && index < len(l.tasks) {
		l.table.SetCursor(index)
	}
	l.ensureVisible()
}

// ensureVisible adjusts scrollOffset so the cursor is within the visible window.
func (l *listView) ensureVisible() {
	cursor := l.table.Cursor()
	visible := l.visibleRows()
	if cursor < l.scrollOffset {
		l.scrollOffset = cursor
	}
	if cursor >= l.scrollOffset+visible {
		l.scrollOffset = cursor - visible + 1
	}
	if l.scrollOffset < 0 {
		l.scrollOffset = 0
	}
}

// lineNumWidth returns the character width needed for the line number gutter.
func (l *listView) lineNumWidth() int {
	return lineNumWidthForCount(len(l.tasks))
}

// visibleRows returns the number of task rows visible in the list.
// Layout: title(1) + blank(1) + header(2, includes border) + tasks + blank(1) + help(1) = 6 lines overhead.
// extraLines accounts for overlays appended below (search bar, command bar, etc.).
// When scroll indicators are shown (tasks above or below), they replace task rows
// rather than adding extra lines, so the total height stays constant.
func (l *listView) visibleRows() int {
	return max(1, l.height-7-l.extraLines)
}

func (l *listView) PageDown() {
	half := max(1, l.visibleRows()/2)
	target := l.table.Cursor() + half
	if target >= len(l.tasks) {
		target = max(0, len(l.tasks)-1)
	}
	l.table.SetCursor(target)
	l.ensureVisible()
}

func (l *listView) PageUp() {
	half := max(1, l.visibleRows()/2)
	target := l.table.Cursor() - half
	if target < 0 {
		target = 0
	}
	l.table.SetCursor(target)
	l.ensureVisible()
}

func (l *listView) SetSize(width, height int) {
	l.width = width
	l.height = height
	l.table.SetWidth(width)
	l.table.SetHeight(l.visibleRows())
	l.recomputeColumns()
	l.ensureVisible()
}

func (l *listView) recomputeColumns() {
	l.cw = l.computeWidths()

	cols := []table.Column{
		{Title: "ID", Width: l.cw.id},
		{Title: "P", Width: l.cw.pri},
	}
	if l.globalMode {
		cols = append(cols, table.Column{Title: "Project", Width: l.cw.project})
	}
	cols = append(cols, table.Column{Title: "Status", Width: l.cw.status})
	if l.showBranch {
		cols = append(cols, table.Column{Title: "Branch", Width: l.cw.branch})
	}
	if l.showTarget {
		cols = append(cols, table.Column{Title: "Target", Width: l.cw.target})
	}
	cols = append(cols, table.Column{Title: "Title", Width: l.cw.title})
	l.table.SetColumns(cols)
}

// Update delegates key messages to the table for navigation.
func (l *listView) Update(msg tea.Msg) tea.Cmd {
	if l.loading {
		var cmd tea.Cmd
		l.spinner, cmd = l.spinner.Update(msg)
		return cmd
	}
	var cmd tea.Cmd
	l.table, cmd = l.table.Update(msg)
	return cmd
}

// titleWidth returns the width available for the title column.
func (l *listView) titleWidth() int {
	if l.cw.title > 0 {
		return l.cw.title
	}
	return 60
}

func (l *listView) View() string {
	var b strings.Builder

	titleText := " " + AppTitle + " "
	if l.globalMode {
		titleText = " " + AppTitle + " (Global) "
	}
	title := titleStyle.Render(titleText)
	if l.refreshing && !l.loading {
		title += " " + dimStyle.Render("⟳")
	}

	// Right-align the project name bracket widget on the same line
	if !l.globalMode && l.projectName != "" && l.width > 0 {
		projectWidget := projectIndicatorStyle.Render("[" + l.projectName + "]")
		gap := l.width - lipgloss.Width(title) - lipgloss.Width(projectWidget)
		if gap < 0 {
			gap = 0
		}
		b.WriteString(title + strings.Repeat(" ", gap) + projectWidget)
	} else {
		b.WriteString(title)
	}
	b.WriteString("\n\n")

	if len(l.tasks) == 0 && l.loading {
		b.WriteString("  " + l.spinner.View() + dimStyle.Render(" Loading tasks…"))
		b.WriteString("\n")
	} else if len(l.tasks) == 0 {
		b.WriteString(dimStyle.Render("  No tasks found. Use 'sortie plan <PRD.md>' to create tasks."))
		b.WriteString("\n")
	} else {
		b.WriteString(l.renderHeader())
		b.WriteString("\n")

		// Windowed rendering: only show tasks in the visible range
		cursor := l.table.Cursor()
		visible := l.visibleRows()
		l.ensureVisible()
		end := min(l.scrollOffset+visible, len(l.tasks))

		for i := l.scrollOffset; i < end; i++ {
			line := l.renderTask(l.tasks[i], i, i == cursor)
			b.WriteString(line)
			b.WriteString("\n")
		}

		// Show scroll position indicator in the help bar when not all tasks visible
		l.hasScrollIndicator = l.scrollOffset > 0 || end < len(l.tasks)
	}

	b.WriteString("\n")
	if l.helpOverride != "" {
		b.WriteString(l.helpOverride)
	} else {
		b.WriteString(l.renderHelp())
	}

	return b.String()
}

func (l *listView) renderHeader() string {
	gutter := " "
	if l.showLineNumbers {
		gutter = strings.Repeat(" ", l.cw.lineNum+1)
	}
	tw := l.cw.title

	var midCols string
	if l.showBranch {
		midCols += fmt.Sprintf(" %-*s", l.cw.branch, "Branch")
	}
	if l.showTarget {
		midCols += fmt.Sprintf(" %-*s", l.cw.target, "Target")
	}

	var header string
	if l.globalMode {
		header = fmt.Sprintf("%s %-*s %-*s %-*s %-*s%s %-*s",
			gutter, l.cw.id, "ID", l.cw.pri, "P", l.cw.project, "Project", l.cw.status, "Status", midCols, tw, "Title")
	} else {
		header = fmt.Sprintf("%s %-*s %-*s %-*s%s %-*s",
			gutter, l.cw.id, "ID", l.cw.pri, "P", l.cw.status, "Status", midCols, tw, "Title")
	}
	return headerStyle.Render(header)
}

// statusText returns the formatted status string for a task (icon + label with annotations).
//
// Tmux tasks render with their *real* underlying status rather than a generic
// "tmux" — the engine pauses at every tmux step, so a non-human tmux step is
// effectively still "running" (Claude is working in the pane) and a human
// tmux step is "awaiting-approval" (the user has been handed the wheel).
// The fact that there's a tmux session attached is communicated by the
// trailing postfix: [wip] when the Claude pane is actively working, [T] when
// it has gone idle. The live signal comes from the daemon's tmux activity
// monitor (`task.TmuxActivity`); when no monitor data has arrived yet we fall
// back to a heuristic: only a Claude-owned (non-human) step with a *live*
// tmux session reads as [wip] (working until the monitor says otherwise).
// Everything else — a human step (Claude finished, wheel handed over) or a
// dead session (e.g. after a daemon restart) — reads as stale [T].
func (l *listView) statusText(task daemon.TaskInfo) string {
	effectiveStatus := effectiveStatusFor(task)
	statusIcon := statusIconFor(effectiveStatus)
	statusLabel := string(effectiveStatus)
	switch effectiveStatus {
	case taskpkg.StatusRunning, taskpkg.StatusAwaitingApproval, taskpkg.StatusFailed:
		if task.CurrentStep != "" {
			statusLabel = task.CurrentStep
		}
	case taskpkg.StatusSummarizingStep:
		if task.CurrentStep != "" {
			statusLabel = "summarizing " + task.CurrentStep
		} else {
			statusLabel = "summarizing step"
		}
	case taskpkg.StatusMergeBlocked:
		statusLabel = string(taskpkg.StatusMergeBlocked)
	case taskpkg.StatusResolvingConflicts:
		statusLabel = string(taskpkg.StatusResolvingConflicts)
	case taskpkg.StatusPending:
		if len(task.BlockedBy) > 0 {
			if l.hasFailedBlocker(task) {
				statusLabel = "pending (deadlocked)"
			} else {
				statusLabel = "pending (blocked)"
			}
		}
	}
	if task.LoopIteration > 0 {
		statusLabel += fmt.Sprintf(" [L%d]", task.LoopIteration)
	}
	if task.WorktreeDetached {
		statusLabel += " [detached]"
	} else if taskpkg.Status(task.Status) == taskpkg.StatusResolvingConflicts {
		// Don't apply the tmux postfix: conflict resolution runs in a fresh
		// non-tmux Claude process even though a stale tmux session for the
		// previous step may still be visible to ListSessions for a beat.
	} else if taskpkg.Status(task.Status) == taskpkg.StatusTmux || l.tmuxSessions[task.ID] {
		switch task.TmuxActivity {
		case "wip":
			statusLabel += " [wip]"
		case "idle":
			statusLabel += " [T]"
		default:
			// No live monitor signal. Only claim work-in-progress when the
			// pane is both Claude-owned (non-human step) AND actually live;
			// a dead session (e.g. after a daemon restart) or a human step
			// where Claude has handed over the wheel reads as stale [T].
			if !task.StepHuman && l.tmuxSessions[task.ID] {
				statusLabel += " [wip]"
			} else {
				statusLabel += " [T]"
			}
		}
	}
	return fmt.Sprintf("%s %s", statusIcon, statusLabel)
}

// effectiveStatusFor returns the status a task should render as. The daemon
// precomputes this in TaskInfo.EffectiveStatus (see taskToInfo) — tmux tasks
// resolve to awaiting-approval (human steps) or running (non-human steps)
// based on StepHuman, since "tmux" is a transport-level detail rather than a
// real workflow-engine state. This falls back to deriving the mapping
// locally when EffectiveStatus hasn't been populated (a TaskInfo built
// directly, as in tests, or the CLI's offline TaskInfoFromTask fallback
// where live StepHuman data isn't available).
func effectiveStatusFor(task daemon.TaskInfo) taskpkg.Status {
	if task.EffectiveStatus != "" {
		return taskpkg.Status(task.EffectiveStatus)
	}
	status := taskpkg.Status(task.Status)
	if status != taskpkg.StatusTmux {
		return status
	}
	if task.StepHuman {
		return taskpkg.StatusAwaitingApproval
	}
	return taskpkg.StatusRunning
}

func (l *listView) renderTask(task daemon.TaskInfo, index int, selected bool) string {
	// Check if this task is a search match
	isMatch := l.isSearchMatch(index)

	taskID := fmt.Sprintf("%-2d", task.ID)
	status := l.statusText(task)

	// Use title or truncated description
	title := task.Title
	if title == "" {
		title = task.Input
	}

	tw := l.titleWidth()
	title = truncateOrPad(title, tw)

	priBadge := priorityBadge(task.Priority)

	// Selected and matched rows use plain inner styles so the outer style's
	// background covers the entire row without inner ANSI resets breaking it.
	if selected || isMatch {
		outerStyle := searchMatchStyle
		if selected {
			outerStyle = selectedStyle
		}
		idCol := truncateOrPad(taskID, l.cw.id)
		priCol := truncateOrPad(priBadge, 2)
		statusCol := truncateOrPad(status, l.cw.status)

		branchCol := ""
		if l.showBranch {
			if l.branchView && index < len(l.treeEntries) {
				branchCol = " " + l.treeEntries[index].renderBranchColumn(task.Branch, l.cw.branch)
			} else {
				branchCol = " " + truncateOrPad(task.Branch, l.cw.branch)
			}
		}
		targetCol := ""
		if l.showTarget {
			targetCol = " " + truncateOrPad(task.TargetBranch, l.cw.target)
		}

		var line string
		if l.showLineNumbers {
			gutterWidth := l.cw.lineNum
			idxStr := fmt.Sprintf("%*d", gutterWidth, index+1)
			idxCol := truncateOrPad(idxStr, gutterWidth)
			if l.globalMode {
				projCol := truncateOrPad(l.projectNameFor(task), l.cw.project)
				line = fmt.Sprintf(" %s %s %s %s %s%s%s %s", idxCol, idCol, priCol, projCol, statusCol, branchCol, targetCol, title)
			} else {
				line = fmt.Sprintf(" %s %s %s %s%s%s %s", idxCol, idCol, priCol, statusCol, branchCol, targetCol, title)
			}
		} else {
			if l.globalMode {
				projCol := truncateOrPad(l.projectNameFor(task), l.cw.project)
				line = fmt.Sprintf("  %s %s %s %s%s%s %s", idCol, priCol, projCol, statusCol, branchCol, targetCol, title)
			} else {
				line = fmt.Sprintf("  %s %s %s%s%s %s", idCol, priCol, statusCol, branchCol, targetCol, title)
			}
		}
		// Pad to full terminal width so the background fills the entire row
		if lineWidth := lipgloss.Width(line); lineWidth < l.width {
			line += strings.Repeat(" ", l.width-lineWidth)
		}
		return outerStyle.Render(line)
	}

	// Apply priority/status colors for non-selected, non-matched rows.
	// Pre-truncate cell text before styling: lipgloss.Width() wraps overflow
	// onto a new line, which corrupts the table layout.
	idCol := lipgloss.NewStyle().Render(truncateOrPad(taskID, l.cw.id))
	priCol := priorityStyle(task.Priority).Render(truncateOrPad(priBadge, 2))
	// Color from the effective status so tmux tasks adopt the running /
	// awaiting-approval palette that matches their rendered icon and label.
	statusSt := stateStyle(effectiveStatusFor(task))
	if strings.Contains(status, "(deadlocked)") {
		statusSt = stateStyle(taskpkg.StatusFailed)
	}
	statusCol := statusSt.Render(truncateOrPad(status, l.cw.status))

	branchCol := ""
	if l.showBranch {
		branchText := truncateOrPad(task.Branch, l.cw.branch)
		if l.branchView && index < len(l.treeEntries) {
			branchText = l.treeEntries[index].renderBranchColumn(task.Branch, l.cw.branch)
		}
		branchCol = " " + dimStyle.Render(truncateOrPad(branchText, l.cw.branch))
	}
	targetCol := ""
	if l.showTarget {
		targetCol = " " + dimStyle.Render(truncateOrPad(task.TargetBranch, l.cw.target))
	}

	var line string
	if l.showLineNumbers {
		gutterWidth := l.cw.lineNum
		idxStr := fmt.Sprintf("%*d", gutterWidth, index+1)
		idxCol := lineNumStyle.Render(truncateOrPad(idxStr, gutterWidth))
		if l.globalMode {
			projCol := lipgloss.NewStyle().Render(truncateOrPad(l.projectNameFor(task), l.cw.project))
			line = fmt.Sprintf(" %s %s %s %s %s%s%s %s", idxCol, idCol, priCol, projCol, statusCol, branchCol, targetCol, title)
		} else {
			line = fmt.Sprintf(" %s %s %s %s%s%s %s", idxCol, idCol, priCol, statusCol, branchCol, targetCol, title)
		}
	} else {
		if l.globalMode {
			projCol := lipgloss.NewStyle().Render(truncateOrPad(l.projectNameFor(task), l.cw.project))
			line = fmt.Sprintf("  %s %s %s %s%s%s %s", idCol, priCol, projCol, statusCol, branchCol, targetCol, title)
		} else {
			line = fmt.Sprintf("  %s %s %s%s%s %s", idCol, priCol, statusCol, branchCol, targetCol, title)
		}
	}
	return normalStyle.Render(line)
}

func (l *listView) renderHelp() string {
	keys := newKeyMap()
	var help strings.Builder

	help.WriteString(helpStyle.Render("  "))

	bindings := keys.ShortHelp()
	for i, binding := range bindings {
		if i > 0 {
			help.WriteString(helpStyle.Render(" | "))
		}
		help.WriteString(dimStyle.Render(binding.Help().Key))
		help.WriteString(helpStyle.Render(" "))
		help.WriteString(helpStyle.Render(binding.Help().Desc))
	}

	// Show scroll position indicator when task list is scrollable
	if l.hasScrollIndicator && len(l.tasks) > 0 {
		visible := l.visibleRows()
		end := min(l.scrollOffset+visible, len(l.tasks))
		help.WriteString(helpStyle.Render(" | "))
		help.WriteString(dimStyle.Render(fmt.Sprintf("%d-%d/%d", l.scrollOffset+1, end, len(l.tasks))))
	}

	return help.String()
}

// hasFailedBlocker reports whether any of the task's blockers reached a
// terminal status without landing (failed or merge-failed). Such a blocker
// never becomes `completed`, so the dependency can never clear — the caller
// renders this as "pending (deadlocked)" rather than "pending (blocked)".
func (l *listView) hasFailedBlocker(task daemon.TaskInfo) bool {
	for _, blockerID := range task.BlockedBy {
		for _, t := range l.tasks {
			if t.ID != blockerID {
				continue
			}
			switch taskpkg.Status(t.Status) {
			case taskpkg.StatusFailed, taskpkg.StatusMergeFailed:
				return true
			}
		}
	}
	return false
}

// toRows converts tasks to table.Row for the table's internal management.
func (l *listView) toRows() []table.Row {
	rows := make([]table.Row, len(l.tasks))
	for i, t := range l.tasks {
		rows[i] = table.Row{fmt.Sprintf("%d", t.ID), t.Title}
	}
	return rows
}

// projectNameFor returns a sanitized project name for display.
func (l *listView) projectNameFor(task daemon.TaskInfo) string {
	name := task.ProjectName
	if name == "" {
		name = "-"
	}
	if len(name) > 12 {
		name = name[:12]
	}
	return name
}

// statusIconFor returns the status icon for a given task status. Statuses
// with no case (e.g. taskpkg.StatusInit, taskpkg.StatusAwaitingChildren)
// fall through to the default icon — a pre-existing gap, preserved as-is.
func statusIconFor(status taskpkg.Status) string {
	switch status {
	case taskpkg.StatusCompleted:
		return "✓"
	case taskpkg.StatusRunning:
		return "●"
	case taskpkg.StatusAwaitingApproval:
		return "◷"
	case taskpkg.StatusTmux:
		return "▣"
	case taskpkg.StatusPending:
		return "○"
	case taskpkg.StatusFailed, taskpkg.StatusMergeFailed:
		return "✗"
	case taskpkg.StatusFinalizing:
		return "◉"
	case taskpkg.StatusSummarizing:
		return "◉"
	case taskpkg.StatusSummarizingStep:
		return "◉"
	case taskpkg.StatusMergeBlocked:
		return "⊘"
	case taskpkg.StatusResolvingConflicts:
		return "◉"
	default:
		return "○"
	}
}

// truncateOrPad truncates or pads a plain string to the given width.
func truncateOrPad(s string, width int) string {
	// Count rune width for proper handling
	runeLen := len([]rune(s))
	if runeLen > width {
		runes := []rune(s)
		if width > 3 {
			return string(runes[:width-3]) + "..."
		}
		return string(runes[:width])
	}
	if runeLen < width {
		return s + strings.Repeat(" ", width-runeLen)
	}
	return s
}
