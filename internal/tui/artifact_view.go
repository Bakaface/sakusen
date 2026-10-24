package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

type artifactViewState struct {
	name     string
	viewport viewport.Model
	width    int
	height   int
	ready    bool
	pendingG bool
	editable bool  // true when the current step's context may be edited
	taskID   int64 // owning task, used when launching the editor
	showHelp bool  // toggled by ctrl+h
}

func (v *artifactViewState) SetSize(width, height int) {
	v.width = width
	v.height = height
	v.recalcViewport()
}

func (v *artifactViewState) recalcViewport() {
	if v.width == 0 || v.height == 0 {
		return
	}

	// Header: title bar + blank line + artifact name + gap = 4 lines
	// Footer: help bar + bottom margin = 3 lines
	headerHeight := 4
	footerHeight := 3
	vpHeight := v.height - headerHeight - footerHeight
	if vpHeight < 1 {
		vpHeight = 1
	}

	if !v.ready {
		v.viewport = viewport.New(v.width-4, vpHeight)
		v.ready = true
	} else {
		v.viewport.Width = v.width - 4
		v.viewport.Height = vpHeight
	}
}

func (v *artifactViewState) SetContent(name, content string) {
	v.name = name
	if !v.ready {
		return
	}
	wrapped := lipgloss.NewStyle().Width(v.viewport.Width).Render(content)
	v.viewport.SetContent(wrapped)
	v.viewport.GotoTop()
}

func (v *artifactViewState) ScrollUp()   { v.viewport.ScrollUp(1) }
func (v *artifactViewState) ScrollDown() { v.viewport.ScrollDown(1) }
func (v *artifactViewState) PageUp()     { v.viewport.HalfPageUp() }
func (v *artifactViewState) PageDown()   { v.viewport.HalfPageDown() }
func (v *artifactViewState) GotoTop()    { v.viewport.GotoTop() }
func (v *artifactViewState) GotoBottom() { v.viewport.GotoBottom() }

func (v *artifactViewState) View() string {
	var b strings.Builder

	// App title
	b.WriteString(titleStyle.Render(" " + AppTitle + " "))
	b.WriteString("\n\n")

	// Artifact name
	artifactTitle := fmt.Sprintf("  Step Context: %s", v.name)
	b.WriteString(subHeaderStyle.Render(artifactTitle))
	b.WriteString("\n")

	// Scrollable content viewport
	if v.ready {
		vpContent := viewportStyle.Render(v.viewport.View())
		b.WriteString(vpContent)
	} else {
		b.WriteString("  Loading...")
	}

	b.WriteString("\n")
	b.WriteString(v.renderHelp())

	return b.String()
}

func (v *artifactViewState) renderHelp() string {
	var help strings.Builder
	help.WriteString(helpStyle.Render("  "))
	bindings := cachedArtifactViewKeyMap.ShortHelp(v.editable)
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
