package tui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/Bakaface/sakusen/internal/task"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

// ansiRegex matches ANSI escape sequences (CSI, OSC, and carriage returns).
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b[()][0-9A-B]|\r`)

type detailView struct {
	task       *daemon.TaskInfo
	output     []string
	viewport   viewport.Model
	width      int
	height     int
	ready      bool
	followMode bool
	pendingG   bool
	loading    bool // true while waiting for initial output load after task switch
	showHelp   bool // toggled by ctrl+h

	// Performance: track content state to avoid redundant processing
	contentLineCount int    // number of lines last set on viewport
	contentDirty     bool   // true when content needs re-wrap (e.g. resize)
	cachedContent    string // cached joined content string to avoid re-joining
}

func newDetailView() detailView {
	return detailView{
		output:     make([]string, 0),
		followMode: true,
	}
}

func (d *detailView) SetTask(task *daemon.TaskInfo) {
	// When switching to a different task, clear stale output and show loading state
	if d.task == nil || d.task.ID != task.ID {
		d.output = d.output[:0]
		d.loading = true
		d.contentDirty = true
		d.contentLineCount = 0
		d.cachedContent = ""
	}
	d.task = task
	d.recalcViewport()
}

// AppendNewLines handles incremental log updates. It ANSI-strips only the new
// lines, appends them to the existing output, and efficiently updates the
// viewport content by extending the cached string rather than re-joining
// everything.
func (d *detailView) AppendNewLines(lines []string) {
	d.loading = false
	if len(lines) == 0 && !d.contentDirty {
		return
	}

	// Strip ANSI only on the new lines
	cleaned := make([]string, len(lines))
	for i, line := range lines {
		cleaned[i] = ansiRegex.ReplaceAllString(line, "")
	}
	d.output = append(d.output, cleaned...)

	if d.contentDirty {
		// Full rebuild needed (e.g. after resize)
		d.rebuildViewportContent()
	} else if len(cleaned) > 0 {
		// Incremental append to cached content
		newContent := strings.Join(cleaned, "\n")
		if d.cachedContent == "" {
			d.cachedContent = newContent
		} else {
			d.cachedContent += "\n" + newContent
		}
		d.setViewportContent(d.cachedContent)
	}

	d.contentLineCount = len(d.output)
	d.contentDirty = false

	if d.followMode {
		d.viewport.GotoBottom()
	}
}

// SetOutput replaces all output (used for full reload, e.g. task switch).
func (d *detailView) SetOutput(lines []string) {
	d.loading = false
	// Skip expensive re-wrap if content hasn't changed
	if len(lines) == d.contentLineCount && !d.contentDirty {
		return
	}
	// Strip ANSI escape codes (from tmux pipe-pane captures, etc.)
	cleaned := make([]string, len(lines))
	for i, line := range lines {
		cleaned[i] = ansiRegex.ReplaceAllString(line, "")
	}
	d.output = cleaned
	d.rebuildViewportContent()
	d.contentLineCount = len(d.output)
	d.contentDirty = false
	if d.followMode {
		d.viewport.GotoBottom()
	}
}

func (d *detailView) AppendOutput(lines []string) {
	d.output = append(d.output, lines...)
	d.contentDirty = true
	d.rebuildViewportContent()
	d.contentLineCount = len(d.output)
	d.contentDirty = false
	d.viewport.GotoBottom()
}

func (d *detailView) SetSize(width, height int) {
	d.width = width
	d.height = height
	d.recalcViewport()
}

func (d *detailView) headerLines() int {
	// "Sakusen" title bar + blank line + gap before viewport = 3 lines
	base := 3
	if d.task != nil && d.width > 0 {
		taskTitle := d.task.Title
		if taskTitle == "" {
			taskTitle = d.task.Input
		}
		titleText := fmt.Sprintf("#%d %s", d.task.ID, taskTitle)
		rendered := lipgloss.NewStyle().Width(d.width).Render(titleText)
		return base + lipgloss.Height(rendered)
	}
	return base + 1
}

func (d *detailView) recalcViewport() {
	if d.width == 0 || d.height == 0 {
		return
	}

	headerHeight := d.headerLines()
	footerHeight := 3
	vpHeight := d.height - headerHeight - footerHeight
	if vpHeight < 1 {
		vpHeight = 1
	}

	if !d.ready {
		d.viewport = viewport.New(d.width-4, vpHeight)
		d.viewport.HighPerformanceRendering = false
		d.ready = true
	} else {
		d.viewport.Width = d.width - 4
		d.viewport.Height = vpHeight
	}

	d.contentDirty = true
	d.rebuildViewportContent()
	d.contentLineCount = len(d.output)
	d.contentDirty = false
}

// rebuildViewportContent rebuilds the cached content string from scratch
// and sets it on the viewport. Used after resize or full content replacement.
func (d *detailView) rebuildViewportContent() {
	if !d.ready {
		return
	}
	d.cachedContent = strings.Join(d.output, "\n")
	d.setViewportContent(d.cachedContent)
}

// setViewportContent sets content on the viewport.
func (d *detailView) setViewportContent(content string) {
	if !d.ready {
		return
	}
	d.viewport.SetContent(content)
}

func (d *detailView) ScrollUp() {
	d.viewport.LineUp(1)
}

func (d *detailView) ScrollDown() {
	d.viewport.LineDown(1)
}

func (d *detailView) PageUp() {
	d.viewport.HalfViewUp()
}

func (d *detailView) PageDown() {
	d.viewport.HalfViewDown()
}

func (d *detailView) GotoTop() {
	d.viewport.GotoTop()
}

func (d *detailView) GotoBottom() {
	d.viewport.GotoBottom()
}

func (d *detailView) IsFollowMode() bool {
	return d.followMode
}

func (d *detailView) SetFollowMode(follow bool) {
	d.followMode = follow
	if follow {
		d.viewport.GotoBottom()
	}
}

func (d *detailView) IsPendingG() bool {
	return d.pendingG
}

func (d *detailView) SetPendingG(pending bool) {
	d.pendingG = pending
}

func (d *detailView) View() string {
	if d.task == nil {
		return "No task selected"
	}

	var b strings.Builder

	// App title
	b.WriteString(titleStyle.Render(" " + AppTitle + " "))
	b.WriteString("\n\n")

	// Task title with word wrapping
	taskTitle := d.task.Title
	if taskTitle == "" {
		taskTitle = d.task.Input
	}
	titleText := fmt.Sprintf("#%d %s", d.task.ID, taskTitle)
	wrappedTitle := subHeaderStyle.Width(d.width).Render("  " + titleText)
	b.WriteString(wrappedTitle)
	b.WriteString("\n")

	// Live logs viewport
	if d.loading && len(d.output) == 0 {
		b.WriteString("  Loading logs...")
	} else if d.ready {
		vpContent := viewportStyle.Render(d.viewport.View())
		b.WriteString(vpContent)
	} else {
		b.WriteString("  Loading...")
	}

	b.WriteString("\n")
	b.WriteString(d.renderHelp())

	return b.String()
}

func (d *detailView) renderHelp() string {
	var help strings.Builder
	help.WriteString(helpStyle.Render("  "))

	// Mode indicator
	if d.followMode {
		help.WriteString(stateStyle(task.StatusRunning).Render("FOLLOW"))
	} else {
		help.WriteString(stateStyle(task.StatusPending).Render("NORMAL"))
	}
	help.WriteString(helpStyle.Render(" "))

	var bindings []key.Binding
	if d.followMode {
		bindings = cachedDetailFollowKeyMap.ShortHelp()
	} else {
		bindings = cachedDetailNormalKeyMap.ShortHelp()
	}
	for i, binding := range bindings {
		if i > 0 {
			help.WriteString(helpStyle.Render(" | "))
		}
		help.WriteString(dimStyle.Render(binding.Help().Key))
		help.WriteString(helpStyle.Render(" "))
		help.WriteString(helpStyle.Render(binding.Help().Desc))
	}

	return help.String()
}
